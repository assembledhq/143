package codingcredentials

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestParseRateLimits(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, 9, 10, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name, body string
		claude     bool
		expected   *models.CodingCredentialRateLimit
		wantErr    bool
	}{
		{name: "codex reset", body: `{"rate_limit":{"allowed":true,"limit_reached":false}}`},
		{name: "codex limited", body: `{"rate_limit":{"allowed":false,"limit_reached":true,"primary_window":{"used_percent":100,"reset_at":1789131600}}}`, expected: &models.CodingCredentialRateLimit{Until: time.Unix(1789131600, 0), Message: "Provider reports a rate limit"}},
		{name: "codex unknown reset", body: `{"rate_limit":{"allowed":false,"limit_reached":true}}`, wantErr: true},
		{name: "codex additional limit", body: `{"rate_limit":{"allowed":true,"limit_reached":false},"additional_rate_limits":[{"rate_limit":{"allowed":false,"limit_reached":true}}]}`, wantErr: true},
		{name: "missing codex fields", body: `{"rate_limit":{}}`, wantErr: true},
		{name: "missing codex limit", body: `{}`, wantErr: true},
		{name: "invalid JSON", body: `<html>`, wantErr: true},
		{name: "claude reset", claude: true, body: `{"five_hour":{"utilization":10},"seven_day":{"utilization":20},"seven_day_sonnet":null}`},
		{name: "claude weekly limit", claude: true, body: `{"five_hour":{"utilization":100,"resets_at":"2026-09-10T13:00:00Z"},"seven_day":{"utilization":100,"resets_at":"2026-09-11T12:00:00Z"}}`, expected: &models.CodingCredentialRateLimit{Until: now.Add(24 * time.Hour), Message: "Provider reports a rate limit"}},
		{name: "claude model limit", claude: true, body: `{"five_hour":{"utilization":10},"seven_day":{"utilization":20},"seven_day_opus":{"utilization":100,"resets_at":"2026-09-11T12:00:00Z"}}`, expected: &models.CodingCredentialRateLimit{Until: now.Add(24 * time.Hour), Message: "Provider reports a rate limit"}},
		{name: "claude unknown quota", claude: true, body: `{"five_hour":null,"seven_day":null}`, wantErr: true},
		{name: "claude missing utilization", claude: true, body: `{"five_hour":{},"seven_day":{"utilization":20}}`, wantErr: true},
		{name: "claude missing reset", claude: true, body: `{"five_hour":{"utilization":100},"seven_day":{"utilization":20}}`, wantErr: true},
		{name: "claude stale reset", claude: true, body: `{"five_hour":{"utilization":100,"resets_at":"2026-09-09T12:00:00Z"},"seven_day":{"utilization":20}}`, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			parse := codexLimit
			if tt.claude {
				parse = claudeLimit
			}
			actual, err := parse([]byte(tt.body), now)
			if tt.wantErr {
				require.ErrorIs(t, err, ErrUnavailable, "incomplete provider data must not clear cooldown")
				return
			}
			require.NoError(t, err, "complete provider usage should parse")
			require.Equal(t, tt.expected, actual, "provider usage should produce the expected cooldown")
		})
	}
}

type testStore struct {
	cred             *models.DecryptedCodingCredential
	scope            models.Scope
	getErr, applyErr error
	applied          bool
	limit            *models.CodingCredentialRateLimit
}

func (s *testStore) Get(_ context.Context, scope models.Scope, id uuid.UUID) (*models.DecryptedCodingCredential, error) {
	s.scope = scope
	return s.cred, s.getErr
}
func (s *testStore) ApplyRateLimitCheck(_ context.Context, scope models.Scope, cred *models.DecryptedCodingCredential, limit *models.CodingCredentialRateLimit) error {
	s.applied = true
	s.limit = limit
	return s.applyErr
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestCheck(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name                                  string
		cfg                                   models.ProviderConfig
		status                                models.CodingCredentialRowStatus
		httpStatus                            int
		body                                  string
		getErr, applyErr, networkErr, wantErr error
		applied                               bool
	}{
		{name: "reset clears", cfg: models.OpenAISubscriptionConfig{AccessToken: "test-token"}, httpStatus: 200, body: `{"rate_limit":{"allowed":true,"limit_reached":false}}`, applied: true},
		{name: "claude oauth", cfg: models.AnthropicSubscriptionConfig{AccessToken: "test-token"}, httpStatus: 200, body: `{"five_hour":{"utilization":0},"seven_day":{"utilization":1}}`, applied: true},
		{name: "claude setup token", cfg: models.AnthropicSubscriptionConfig{AuthMode: models.AnthropicSubscriptionAuthModeSetupToken, OAuthToken: "test-token"}, wantErr: ErrSetupTokenUsage},
		{name: "usage endpoint throttled", cfg: models.OpenAISubscriptionConfig{AccessToken: "test-token"}, httpStatus: 429, wantErr: ErrUnavailable},
		{name: "expired access token", cfg: models.OpenAISubscriptionConfig{AccessToken: "test-token"}, httpStatus: 401, wantErr: ErrUsageUnauthorized},
		{name: "provider outage", cfg: models.OpenAISubscriptionConfig{AccessToken: "test-token"}, httpStatus: 503, wantErr: ErrUnavailable},
		{name: "network failure", cfg: models.OpenAISubscriptionConfig{AccessToken: "test-token"}, networkErr: errors.New("network"), wantErr: ErrUnavailable},
		{name: "unknown payload", cfg: models.OpenAISubscriptionConfig{AccessToken: "test-token"}, httpStatus: 200, body: `{}`, wantErr: ErrUnavailable},
		{name: "wrong scope", getErr: db.ErrCodingCredentialNotFound, wantErr: db.ErrCodingCredentialNotFound},
		{name: "disabled credential", status: models.CodingCredentialStatusDisabled, wantErr: ErrInactive},
		{name: "API key", cfg: models.OpenAIConfig{APIKey: "key"}, wantErr: ErrUnsupported},
		{name: "no token", cfg: models.OpenAISubscriptionConfig{}, wantErr: ErrInactive},
		{name: "concurrent change", cfg: models.OpenAISubscriptionConfig{AccessToken: "test-token"}, httpStatus: 200, body: `{"rate_limit":{"allowed":true,"limit_reached":false}}`, applyErr: db.ErrRateLimitCheckStale, wantErr: db.ErrRateLimitCheckStale, applied: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			scope := models.Scope{OrgID: uuid.New()}
			status := tt.status
			if status == "" {
				status = models.CodingCredentialStatusActive
			}
			cred := &models.DecryptedCodingCredential{ID: uuid.New(), Status: status, Config: tt.cfg}
			if tt.cfg != nil {
				cred.Provider = tt.cfg.Provider()
			}
			store := &testStore{cred: cred, getErr: tt.getErr, applyErr: tt.applyErr}
			svc := New(store)
			svc.client.Transport = roundTripFunc(func(r *http.Request) (*http.Response, error) {
				require.Equal(t, "Bearer test-token", r.Header.Get("Authorization"), "check must use only the selected credential")
				require.Equal(t, http.MethodGet, r.Method, "check must not consume a reset")
				if cred.Provider == models.ProviderAnthropicSubscription {
					require.Equal(t, "https://api.anthropic.com/api/oauth/usage", r.URL.String(), "Claude check must target usage endpoint")
					require.Equal(t, "oauth-2025-04-20", r.Header.Get("anthropic-beta"), "Claude usage needs OAuth beta header")
				} else {
					require.Equal(t, "https://chatgpt.com/backend-api/wham/usage", r.URL.String(), "Codex check must target usage endpoint")
				}
				if tt.networkErr != nil {
					return nil, tt.networkErr
				}
				return &http.Response{StatusCode: tt.httpStatus, Body: io.NopCloser(strings.NewReader(tt.body))}, nil
			})
			err := svc.Check(context.Background(), scope, cred.ID)
			require.ErrorIs(t, err, tt.wantErr, "check should return expected outcome")
			require.Equal(t, scope, store.scope, "lookup must preserve tenant and personal scope")
			require.Equal(t, tt.applied, store.applied, "only complete successful checks may persist changes")
		})
	}
}

func TestRetry(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name                      string
		cfg                       models.ProviderConfig
		status                    models.CodingCredentialRowStatus
		noLimit                   bool
		getErr, applyErr, wantErr error
		applied                   bool
	}{
		{name: "setup token cooldown", applied: true},
		{name: "already cleared", noLimit: true},
		{name: "missing credential", getErr: db.ErrCodingCredentialNotFound, wantErr: db.ErrCodingCredentialNotFound},
		{name: "disabled credential", status: models.CodingCredentialStatusDisabled, wantErr: ErrInactive},
		{name: "rejected credential", status: models.CodingCredentialStatusInvalid, wantErr: ErrInactive},
		{name: "Codex cannot bypass provider check", cfg: models.OpenAISubscriptionConfig{AccessToken: "test-token"}, wantErr: ErrRetryUnsupported},
		{name: "rotating OAuth cannot bypass provider check", cfg: models.AnthropicSubscriptionConfig{AccessToken: "test-token"}, wantErr: ErrRetryUnsupported},
		{name: "empty setup token", cfg: models.AnthropicSubscriptionConfig{AuthMode: models.AnthropicSubscriptionAuthModeSetupToken}, wantErr: ErrInactive},
		{name: "concurrent change", applyErr: db.ErrRateLimitCheckStale, wantErr: db.ErrRateLimitCheckStale, applied: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			cfg := tt.cfg
			if cfg == nil {
				cfg = models.AnthropicSubscriptionConfig{AuthMode: models.AnthropicSubscriptionAuthModeSetupToken, OAuthToken: "test-token"}
			}
			status := tt.status
			if status == "" {
				status = models.CodingCredentialStatusActive
			}
			until := time.Now().Add(time.Hour)
			cred := &models.DecryptedCodingCredential{ID: uuid.New(), Provider: cfg.Provider(), Config: cfg, Status: status, RateLimitedUntil: &until}
			if tt.noLimit {
				cred.RateLimitedUntil = nil
			}
			store := &testStore{cred: cred, getErr: tt.getErr, applyErr: tt.applyErr}
			svc := New(store)
			svc.client.Transport = roundTripFunc(func(*http.Request) (*http.Response, error) {
				t.Error("manual retry must not contact a provider")
				return nil, errors.New("unexpected provider call")
			})
			userID := uuid.New()
			scope := models.Scope{OrgID: uuid.New(), UserID: &userID}
			err := svc.Retry(context.Background(), scope, cred.ID)
			require.ErrorIs(t, err, tt.wantErr, "retry should enforce eligibility and propagate failures")
			require.Equal(t, scope, store.scope, "retry must look up only the selected tenant and user scope")
			require.Equal(t, tt.applied, store.applied, "retry should clear only eligible cooldowns")
			require.Nil(t, store.limit, "manual retry should clear the cooldown without inventing a new limit")
		})
	}
}
