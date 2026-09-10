package codingcredentials

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
)

var ErrUnsupported = errors.New("rate-limit checks are available for subscription auths only")
var ErrInactive = errors.New("reconnect this auth before checking its rate limit")
var ErrUsageUnauthorized = errors.New("this auth cannot read provider usage; reconnect with OAuth and check again. The saved rate limit has not changed")
var ErrUnavailable = errors.New("could not check provider usage; the saved rate limit has not changed")

type Store interface {
	Get(context.Context, models.Scope, uuid.UUID) (*models.DecryptedCodingCredential, error)
	ApplyRateLimitCheck(context.Context, models.Scope, *models.DecryptedCodingCredential, *models.CodingCredentialRateLimit) error
}

type Service struct {
	store  Store
	client *http.Client
}

func New(store Store) *Service {
	return &Service{store: store, client: &http.Client{
		Timeout:       15 * time.Second,
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

// Check reads provider usage without consuming a reset or starting an agent run.
// Failed or incomplete usage responses never clear the existing cooldown.
func (s *Service) Check(ctx context.Context, scope models.Scope, id uuid.UUID) error {
	cred, err := s.store.Get(ctx, scope, id)
	if err != nil {
		return err
	}
	if cred.Status != models.CodingCredentialStatusActive {
		return ErrInactive
	}
	var endpoint, token string
	switch cfg := cred.Config.(type) {
	case models.OpenAISubscriptionConfig:
		endpoint = "https://chatgpt.com/backend-api/wham/usage"
		token = cfg.AccessToken
	case models.AnthropicSubscriptionConfig:
		endpoint = "https://api.anthropic.com/api/oauth/usage"
		token = cfg.AccessToken
		if cfg.IsSetupToken() {
			token = cfg.OAuthToken
		}
	default:
		return ErrUnsupported
	}
	if token == "" {
		return ErrInactive
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Accept", "application/json")
	if cred.Provider == models.ProviderAnthropicSubscription {
		req.Header.Set("anthropic-beta", "oauth-2025-04-20")
	}
	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("%w: request failed", ErrUnavailable)
	}
	defer resp.Body.Close()
	// A 429 from the usage endpoint describes that endpoint's throttle, not
	// necessarily the account's inference quota. Preserve the saved state.
	if resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden {
		return ErrUsageUnauthorized
	}
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("%w (HTTP %d)", ErrUnavailable, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return ErrUnavailable
	}
	var limit *models.CodingCredentialRateLimit
	if cred.Provider == models.ProviderOpenAISubscription {
		limit, err = codexLimit(body, time.Now())
	} else {
		limit, err = claudeLimit(body, time.Now())
	}
	if err != nil {
		return err
	}
	return s.store.ApplyRateLimitCheck(ctx, scope, cred, limit)
}

type codexWindow struct {
	UsedPercent float64 `json:"used_percent"`
	ResetAt     int64   `json:"reset_at"`
}

type codexRateLimit struct {
	Allowed      *bool        `json:"allowed"`
	LimitReached *bool        `json:"limit_reached"`
	Primary      *codexWindow `json:"primary_window"`
	Secondary    *codexWindow `json:"secondary_window"`
}

func codexLimit(body []byte, now time.Time) (*models.CodingCredentialRateLimit, error) {
	var payload struct {
		RateLimit  *codexRateLimit `json:"rate_limit"`
		Additional []struct {
			RateLimit *codexRateLimit `json:"rate_limit"`
		} `json:"additional_rate_limits"`
	}
	if json.Unmarshal(body, &payload) != nil || payload.RateLimit == nil {
		return nil, ErrUnavailable
	}
	limits := []*codexRateLimit{payload.RateLimit}
	for _, extra := range payload.Additional {
		limits = append(limits, extra.RateLimit)
	}
	var result *models.CodingCredentialRateLimit
	for _, limit := range limits {
		if limit == nil || limit.Allowed == nil || limit.LimitReached == nil {
			return nil, ErrUnavailable
		}
		if *limit.Allowed && !*limit.LimitReached {
			continue
		}
		var until time.Time
		for _, window := range []*codexWindow{limit.Primary, limit.Secondary} {
			if window != nil && window.UsedPercent >= 100 && time.Unix(window.ResetAt, 0).After(now) && time.Unix(window.ResetAt, 0).After(until) {
				until = time.Unix(window.ResetAt, 0)
			}
		}
		if until.IsZero() {
			return nil, ErrUnavailable
		}
		if result == nil || until.After(result.Until) {
			result = &models.CodingCredentialRateLimit{Until: until, Message: "Provider reports a rate limit"}
		}
	}
	return result, nil
}

func claudeLimit(body []byte, now time.Time) (*models.CodingCredentialRateLimit, error) {
	var windows map[string]json.RawMessage
	if json.Unmarshal(body, &windows) != nil {
		return nil, ErrUnavailable
	}
	var result *models.CodingCredentialRateLimit
	// Require both account-wide windows before treating an auth as available.
	for _, key := range []string{"five_hour", "seven_day"} {
		if len(windows[key]) == 0 || string(windows[key]) == "null" {
			return nil, ErrUnavailable
		}
	}
	for key, raw := range windows {
		if key != "five_hour" && !strings.HasPrefix(key, "seven_day") {
			continue
		}
		if string(raw) == "null" {
			continue
		}
		var window struct {
			Utilization *float64   `json:"utilization"`
			ResetsAt    *time.Time `json:"resets_at"`
		}
		if json.Unmarshal(raw, &window) != nil || window.Utilization == nil || *window.Utilization < 0 {
			return nil, ErrUnavailable
		}
		if *window.Utilization < 100 {
			continue
		}
		if window.ResetsAt == nil || !window.ResetsAt.After(now) {
			return nil, ErrUnavailable
		}
		if result == nil || window.ResetsAt.After(result.Until) {
			result = &models.CodingCredentialRateLimit{Until: *window.ResetsAt, Message: "Provider reports a rate limit"}
		}
	}
	return result, nil
}
