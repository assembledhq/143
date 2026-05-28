package integration

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/assembledhq/143/internal/models"
)

const (
	LogDefaultLimit       = 100
	LogMaxLimit           = 1000
	LogDefaultFieldsSince = 24 * time.Hour
	LogMaxLookback        = 7 * 24 * time.Hour
)

func ResolveLogProvider(providers []LogProvider, selector LogToolSelector, defaultProvider models.ProviderName) (LogProvider, error) {
	if len(providers) == 0 {
		return nil, fmt.Errorf("%w", ErrLogProviderUnconfigured)
	}

	if selector.Provider != nil && strings.TrimSpace(*selector.Provider) != "" {
		requested := models.ProviderName(strings.TrimSpace(*selector.Provider))
		for _, provider := range providers {
			if provider.Name() == requested {
				return provider, nil
			}
		}
		return nil, fmt.Errorf("%w: %s; configured providers: %s", ErrLogProviderUnknown, requested, strings.Join(LogProviderNames(providers), ", "))
	}

	if len(providers) == 1 {
		return providers[0], nil
	}

	if defaultProvider != "" {
		for _, provider := range providers {
			if provider.Name() == defaultProvider {
				return provider, nil
			}
		}
	}

	return nil, fmt.Errorf("%w; configured providers: %s", ErrLogProviderAmbiguous, strings.Join(LogProviderNames(providers), ", "))
}

func LogProviderNames(providers []LogProvider) []string {
	names := make([]string, 0, len(providers))
	for _, provider := range providers {
		names = append(names, string(provider.Name()))
	}
	slices.Sort(names)
	return names
}

func NormalizeLogTimeBounds(since *time.Duration, startTime *time.Time, endTime *time.Time, maxLookback time.Duration, now time.Time) (time.Time, time.Time, error) {
	if since != nil {
		if *since <= 0 || *since > maxLookback {
			return time.Time{}, time.Time{}, fmt.Errorf("%w: since must be >0 and <= %s", ErrLogTimeBoundRequired, maxLookback)
		}
		end := now.UTC()
		return end.Add(-*since), end, nil
	}
	if startTime == nil || endTime == nil {
		return time.Time{}, time.Time{}, ErrLogTimeBoundRequired
	}
	start := startTime.UTC()
	end := endTime.UTC()
	if !start.Before(end) || end.Sub(start) > maxLookback {
		return time.Time{}, time.Time{}, fmt.Errorf("%w: start_time must be before end_time and range must be <= %s", ErrLogTimeBoundRequired, maxLookback)
	}
	return start, end, nil
}

func RedactLogPayload(value any) map[string]any {
	redacted, ok := redactLogValue(value, "").(map[string]any)
	if !ok {
		return map[string]any{}
	}
	return redacted
}

func redactLogValue(value any, key string) any {
	if isSensitiveLogField(key) {
		return "[REDACTED]"
	}
	switch v := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(v))
		for childKey, childValue := range v {
			out[childKey] = redactLogValue(childValue, childKey)
		}
		return out
	case map[string]string:
		out := make(map[string]any, len(v))
		for childKey, childValue := range v {
			out[childKey] = redactLogValue(childValue, childKey)
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, childValue := range v {
			out[i] = redactLogValue(childValue, "")
		}
		return out
	default:
		return value
	}
}

func isSensitiveLogField(key string) bool {
	key = strings.ToLower(key)
	if key == "" {
		return false
	}
	for _, pattern := range []string{"token", "secret", "password", "cookie", "authorization", "api_key", "session", "key", "credential", "auth", "private"} {
		if strings.Contains(key, pattern) {
			return true
		}
	}
	return false
}

type LogCursorConstraints struct {
	Provider       models.ProviderName `json:"provider"`
	Query          string              `json:"query"`
	StartTime      time.Time           `json:"start_time"`
	EndTime        time.Time           `json:"end_time"`
	Direction      LogDirection        `json:"direction,omitempty"`
	Fields         []string            `json:"fields,omitempty"`
	ExpiresAt      time.Time           `json:"expires_at"`
	ProviderCursor string              `json:"provider_cursor,omitempty"`
}

type LogCursorSigner struct {
	secret []byte
	now    func() time.Time
}

func NewLogCursorSigner(secret []byte) *LogCursorSigner {
	return &LogCursorSigner{secret: secret, now: time.Now}
}

func (s *LogCursorSigner) Sign(constraints LogCursorConstraints) (string, error) {
	if len(s.secret) == 0 {
		return "", errors.New("log cursor signer secret is required")
	}
	payload, err := canonicalLogCursorPayload(constraints)
	if err != nil {
		return "", err
	}
	sig := s.sign(payload)
	return base64.RawURLEncoding.EncodeToString(payload) + "." + base64.RawURLEncoding.EncodeToString(sig), nil
}

func (s *LogCursorSigner) Verify(cursor string, expected LogCursorConstraints) (LogCursorConstraints, error) {
	if len(s.secret) == 0 {
		return LogCursorConstraints{}, errors.New("log cursor signer secret is required")
	}
	payloadB64, sigB64, ok := strings.Cut(cursor, ".")
	if !ok {
		return LogCursorConstraints{}, ErrLogCursorInvalid
	}
	payload, err := base64.RawURLEncoding.DecodeString(payloadB64)
	if err != nil {
		return LogCursorConstraints{}, ErrLogCursorInvalid
	}
	sig, err := base64.RawURLEncoding.DecodeString(sigB64)
	if err != nil {
		return LogCursorConstraints{}, ErrLogCursorInvalid
	}
	if !hmac.Equal(sig, s.sign(payload)) {
		return LogCursorConstraints{}, ErrLogCursorInvalid
	}
	var got LogCursorConstraints
	if err := json.Unmarshal(payload, &got); err != nil {
		return LogCursorConstraints{}, ErrLogCursorInvalid
	}
	if !got.ExpiresAt.IsZero() && s.now().After(got.ExpiresAt) {
		return LogCursorConstraints{}, ErrLogCursorInvalid
	}
	if !logCursorConstraintsMatch(got, expected) {
		return LogCursorConstraints{}, ErrLogCursorInvalid
	}
	return got, nil
}

func (s *LogCursorSigner) sign(payload []byte) []byte {
	mac := hmac.New(sha256.New, s.secret)
	_, _ = mac.Write(payload)
	return mac.Sum(nil)
}

func canonicalLogCursorPayload(constraints LogCursorConstraints) ([]byte, error) {
	normalized := constraints
	normalized.StartTime = constraints.StartTime.UTC()
	normalized.EndTime = constraints.EndTime.UTC()
	normalized.ExpiresAt = constraints.ExpiresAt.UTC()
	normalized.Fields = append([]string(nil), constraints.Fields...)
	slices.Sort(normalized.Fields)
	return json.Marshal(normalized)
}

func logCursorConstraintsMatch(got LogCursorConstraints, expected LogCursorConstraints) bool {
	got.Fields = append([]string(nil), got.Fields...)
	expected.Fields = append([]string(nil), expected.Fields...)
	slices.Sort(got.Fields)
	slices.Sort(expected.Fields)
	return got.Provider == expected.Provider &&
		got.Query == expected.Query &&
		got.StartTime.Equal(expected.StartTime) &&
		got.EndTime.Equal(expected.EndTime) &&
		got.Direction == expected.Direction &&
		slices.Equal(got.Fields, expected.Fields)
}
