package middleware

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func TestLogContext_AllowsAPIIdentityWithoutUser(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	clientID := uuid.New()
	tokenID := uuid.New()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions", nil)
	req.Header.Set(APIVersionHeader, "2026-06-01")
	req = req.WithContext(WithAPIIdentity(req.Context(),
		&models.APIClient{ID: clientID, OrgID: orgID, Name: "ci", Status: models.APIClientStatusEnabled},
		&models.APIToken{ID: tokenID, OrgID: orgID, APIClientID: clientID, Name: "deploy"},
	))
	rr := httptest.NewRecorder()

	LogContext(zerolog.Nop())(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Nil(t, UserFromContext(r.Context()), "API identity should not synthesize a user")
		require.Equal(t, "2026-06-01", APIVersionFromContext(r.Context()), "API version header should be carried in request context")
		w.WriteHeader(http.StatusNoContent)
	})).ServeHTTP(rr, req)

	require.Equal(t, http.StatusNoContent, rr.Code, "LogContext should not panic for service-token requests")
}

func TestRequireAPIScope_ReturnsRequiredScopeDetail(t *testing.T) {
	t.Parallel()

	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions", nil)
	req = req.WithContext(WithAPIIdentity(req.Context(), &models.APIClient{ID: uuid.New(), OrgID: uuid.New()}, &models.APIToken{
		ID:     uuid.New(),
		Scopes: []string{"sessions:read"},
	}))
	rr := httptest.NewRecorder()

	RequireAPIScope("sessions:create")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	})).ServeHTTP(rr, req)

	require.Equal(t, http.StatusForbidden, rr.Code, "missing API scope should be forbidden")
	var body struct {
		Error struct {
			Code    string         `json:"code"`
			Details map[string]any `json:"details"`
		} `json:"error"`
	}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &body), "error body should be valid JSON")
	require.Equal(t, "FORBIDDEN", body.Error.Code, "error should use stable forbidden code")
	require.Equal(t, "sessions:create", body.Error.Details["required_scope"], "error details should name the missing scope")
}

func TestRequireAPIScope_AllowsFamilyScopes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		tokenScopes   []string
		requiredScope string
		expectedCode  int
	}{
		{name: "sessions all allows session create", tokenScopes: []string{"sessions:all"}, requiredScope: "sessions:create", expectedCode: http.StatusNoContent},
		{name: "sessions all allows session publish", tokenScopes: []string{"sessions:all"}, requiredScope: "sessions:publish", expectedCode: http.StatusNoContent},
		{name: "automations all allows automation run", tokenScopes: []string{"automations:all"}, requiredScope: "automations:run", expectedCode: http.StatusNoContent},
		{name: "previews all allows preview stop", tokenScopes: []string{"previews:all"}, requiredScope: "previews:stop", expectedCode: http.StatusNoContent},
		{name: "family scope does not cross resource families", tokenScopes: []string{"sessions:all"}, requiredScope: "automations:read", expectedCode: http.StatusForbidden},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions", nil)
			req = req.WithContext(WithAPIIdentity(req.Context(), &models.APIClient{ID: uuid.New(), OrgID: uuid.New()}, &models.APIToken{
				ID:     uuid.New(),
				Scopes: tt.tokenScopes,
			}))
			rr := httptest.NewRecorder()

			RequireAPIScope(tt.requiredScope)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusNoContent)
			})).ServeHTTP(rr, req)

			require.Equal(t, tt.expectedCode, rr.Code, "family scopes should satisfy only their documented resource family")
		})
	}
}

func TestAPITokenAllowsSourceIP(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		token    models.APIToken
		sourceIP string
		expected bool
	}{
		{name: "empty allowlist allows any source", token: models.APIToken{}, sourceIP: "203.0.113.10", expected: true},
		{name: "exact IP allows matching source", token: models.APIToken{AllowedIPCidrs: []string{"203.0.113.10"}}, sourceIP: "203.0.113.10", expected: true},
		{name: "CIDR allows contained source", token: models.APIToken{AllowedIPCidrs: []string{"203.0.113.0/24"}}, sourceIP: "203.0.113.10", expected: true},
		{name: "allowlist rejects outside source", token: models.APIToken{AllowedIPCidrs: []string{"203.0.113.0/24"}}, sourceIP: "198.51.100.7", expected: false},
		{name: "invalid stored value fails closed", token: models.APIToken{AllowedIPCidrs: []string{"bad-value"}}, sourceIP: "203.0.113.10", expected: false},
		{name: "invalid source fails closed", token: models.APIToken{AllowedIPCidrs: []string{"203.0.113.0/24"}}, sourceIP: "bad-source", expected: false},
		{name: "empty stored entry is skipped and valid entry still allows", token: models.APIToken{AllowedIPCidrs: []string{"", "203.0.113.10"}}, sourceIP: "203.0.113.10", expected: true},
		{name: "empty stored entry is skipped and still blocks unlisted source", token: models.APIToken{AllowedIPCidrs: []string{"", "203.0.113.0/24"}}, sourceIP: "198.51.100.7", expected: false},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			actual := apiTokenAllowsSourceIP(tt.token, tt.sourceIP)

			require.Equal(t, tt.expected, actual, "source IP restrictions should be enforced exactly")
		})
	}
}

func TestExternalAPIRateLimit_PerTokenAndMutationBuckets(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	clientID := uuid.New()
	tokenID := uuid.New()
	// Fixed clock: 30 seconds into a minute, so reset is 30 s away.
	fixedNow := time.Date(2025, 1, 1, 12, 0, 30, 0, time.UTC)
	limiter := NewExternalAPIRateLimiter(ExternalAPIRateLimitConfig{
		ReadRequestsPerMinute:     2,
		MutatingRequestsPerMinute: 1,
		Now:                       func() time.Time { return fixedNow },
	})
	handler := limiter.Middleware()(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNoContent)
	}))

	request := func(method string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, "/api/v1/sessions", nil)
		req = req.WithContext(WithAPIIdentity(req.Context(),
			&models.APIClient{ID: clientID, OrgID: orgID},
			&models.APIToken{ID: tokenID, OrgID: orgID, APIClientID: clientID},
		))
		rr := httptest.NewRecorder()
		handler.ServeHTTP(rr, req)
		return rr
	}

	firstRead := request(http.MethodGet)
	require.Equal(t, http.StatusNoContent, firstRead.Code, "first read should pass")
	require.NotEmpty(t, firstRead.Header().Get("X-RateLimit-Limit"), "rate limit responses should expose limit headers")

	secondRead := request(http.MethodGet)
	require.Equal(t, http.StatusNoContent, secondRead.Code, "second read should pass within read budget")
	thirdRead := request(http.MethodGet)
	require.Equal(t, http.StatusTooManyRequests, thirdRead.Code, "third read should exceed per-token read budget")
	require.Equal(t, "30", thirdRead.Header().Get("Retry-After"), "rate limited responses should include retry guidance")

	firstWrite := request(http.MethodPost)
	require.Equal(t, http.StatusNoContent, firstWrite.Code, "first mutating request should use its separate budget")
	secondWrite := request(http.MethodPost)
	require.Equal(t, http.StatusTooManyRequests, secondWrite.Code, "second mutating request should exceed mutation budget")
}
