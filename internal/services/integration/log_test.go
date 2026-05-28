package integration

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/stretchr/testify/require"
)

type registryLogProvider struct {
	name models.ProviderName
}

func (p registryLogProvider) Name() models.ProviderName { return p.name }
func (p registryLogProvider) QueryLogs(_ context.Context, _ LogQueryRequest) (*LogQueryResult, error) {
	return nil, nil
}
func (p registryLogProvider) GetLogContext(_ context.Context, _ LogContextRequest) (*LogContextResult, error) {
	return nil, nil
}
func (p registryLogProvider) ListLogFields(_ context.Context, _ LogFieldsRequest) (*LogFieldsResult, error) {
	return nil, nil
}
func (p registryLogProvider) QueryLogStats(_ context.Context, _ LogStatsRequest) (*LogStatsResult, error) {
	return nil, ErrLogStatsUnsupported
}

func TestRegistry_LogProviders(t *testing.T) {
	t.Parallel()

	r := NewRegistry()
	r.RegisterLogProvider(registryLogProvider{name: models.ProviderVictoriaLogs})
	r.RegisterLogProvider(registryLogProvider{name: models.ProviderMezmo})

	require.True(t, r.HasAny(), "registry with log providers should report integrations are configured")
	require.Len(t, r.LogProviders(), 2, "registry should return all registered log providers")

	provider, err := r.LogProvider(models.ProviderMezmo)
	require.NoError(t, err, "registry should retrieve a log provider by provider name")
	require.Equal(t, models.ProviderMezmo, provider.Name(), "registry should return the requested log provider")

	summary := r.Summary()
	require.ElementsMatch(t, []string{"victorialogs", "mezmo"}, summary["log_providers"], "registry summary should include log providers")
}

func TestResolveLogProvider(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		providers     []LogProvider
		selector      LogToolSelector
		defaultName   models.ProviderName
		expectedName  models.ProviderName
		expectedError error
	}{
		{
			name:         "single provider selected by default",
			providers:    []LogProvider{registryLogProvider{name: models.ProviderVictoriaLogs}},
			expectedName: models.ProviderVictoriaLogs,
		},
		{
			name:         "explicit provider selected",
			providers:    []LogProvider{registryLogProvider{name: models.ProviderVictoriaLogs}, registryLogProvider{name: models.ProviderMezmo}},
			selector:     LogToolSelector{Provider: stringPtr("mezmo")},
			expectedName: models.ProviderMezmo,
		},
		{
			name:         "configured default selected",
			providers:    []LogProvider{registryLogProvider{name: models.ProviderVictoriaLogs}, registryLogProvider{name: models.ProviderMezmo}},
			defaultName:  models.ProviderVictoriaLogs,
			expectedName: models.ProviderVictoriaLogs,
		},
		{
			name:          "missing providers",
			expectedError: ErrLogProviderUnconfigured,
		},
		{
			name:          "ambiguous providers",
			providers:     []LogProvider{registryLogProvider{name: models.ProviderVictoriaLogs}, registryLogProvider{name: models.ProviderMezmo}},
			expectedError: ErrLogProviderAmbiguous,
		},
		{
			name:          "unknown provider",
			providers:     []LogProvider{registryLogProvider{name: models.ProviderVictoriaLogs}},
			selector:      LogToolSelector{Provider: stringPtr("mezmo")},
			expectedError: ErrLogProviderUnknown,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			provider, err := ResolveLogProvider(tt.providers, tt.selector, tt.defaultName)
			if tt.expectedError != nil {
				require.ErrorIs(t, err, tt.expectedError, "ResolveLogProvider should return the expected provider resolution error")
				return
			}

			require.NoError(t, err, "ResolveLogProvider should resolve a provider")
			require.Equal(t, tt.expectedName, provider.Name(), "ResolveLogProvider should choose the expected provider")
		})
	}
}

func TestLogCursorSigner(t *testing.T) {
	t.Parallel()

	signer := NewLogCursorSigner([]byte("test-secret"))
	expiresAt := time.Now().Add(time.Hour).UTC()
	constraints := LogCursorConstraints{
		Provider:       models.ProviderVictoriaLogs,
		Query:          "service:api",
		StartTime:      time.Date(2026, 5, 28, 10, 0, 0, 0, time.UTC),
		EndTime:        time.Date(2026, 5, 28, 11, 0, 0, 0, time.UTC),
		Direction:      LogDirectionDesc,
		Fields:         []string{"message", "level"},
		ExpiresAt:      expiresAt,
		ProviderCursor: "provider-page-2",
	}

	cursor, err := signer.Sign(constraints)
	require.NoError(t, err, "Sign should create an HMAC protected log cursor")

	got, err := signer.Verify(cursor, constraints)
	require.NoError(t, err, "Verify should accept an untampered cursor with matching constraints")
	require.Equal(t, "provider-page-2", got.ProviderCursor, "Verify should return the provider cursor payload")

	_, err = signer.Verify(cursor+"x", constraints)
	require.ErrorIs(t, err, ErrLogCursorInvalid, "Verify should reject tampered cursors before provider dispatch")

	mismatched := constraints
	mismatched.Query = "service:worker"
	_, err = signer.Verify(cursor, mismatched)
	require.ErrorIs(t, err, ErrLogCursorInvalid, "Verify should reject cursors with mismatched constraints")
}

func TestRedactLogPayload(t *testing.T) {
	t.Parallel()

	payload := map[string]any{
		"message": "hello",
		"api_key": "secret",
		"nested": map[string]any{
			"Authorization": "Bearer secret",
			"safe":          "value",
		},
		"items": []any{
			map[string]any{"session_token": "secret", "count": float64(2)},
		},
	}

	redacted := RedactLogPayload(payload)
	require.Equal(t, "[REDACTED]", redacted["api_key"], "RedactLogPayload should redact sensitive top-level fields")
	nested := redacted["nested"].(map[string]any)
	require.Equal(t, "[REDACTED]", nested["Authorization"], "RedactLogPayload should redact sensitive nested fields")
	require.Equal(t, "value", nested["safe"], "RedactLogPayload should preserve non-sensitive nested fields")
	items := redacted["items"].([]any)
	item := items[0].(map[string]any)
	require.Equal(t, "[REDACTED]", item["session_token"], "RedactLogPayload should redact sensitive fields inside arrays")
	require.Equal(t, float64(2), item["count"], "RedactLogPayload should preserve safe fields inside arrays")
}

func TestValidateLogTimeBounds(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		since         *time.Duration
		start         *time.Time
		end           *time.Time
		expectedError error
	}{
		{name: "since is accepted", since: durationPtr(time.Hour)},
		{name: "start and end are accepted", start: timePtr(time.Now().Add(-time.Hour)), end: timePtr(time.Now())},
		{name: "missing bounds", expectedError: ErrLogTimeBoundRequired},
		{name: "lookback too large", since: durationPtr(8 * 24 * time.Hour), expectedError: ErrLogTimeBoundRequired},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			_, _, err := NormalizeLogTimeBounds(tt.since, tt.start, tt.end, 7*24*time.Hour, time.Now())
			if tt.expectedError != nil {
				require.True(t, errors.Is(err, tt.expectedError), "NormalizeLogTimeBounds should return the expected time-bound error")
				return
			}
			require.NoError(t, err, "NormalizeLogTimeBounds should accept bounded log requests")
		})
	}
}

func stringPtr(v string) *string                 { return &v }
func durationPtr(v time.Duration) *time.Duration { return &v }
func timePtr(v time.Time) *time.Time             { return &v }
