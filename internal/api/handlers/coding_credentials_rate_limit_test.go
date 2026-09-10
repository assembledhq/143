package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/codingcredentials"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type rateLimitCheckerFunc func(context.Context, models.Scope, uuid.UUID) error

func (f rateLimitCheckerFunc) Check(ctx context.Context, scope models.Scope, id uuid.UUID) error {
	return f(ctx, scope, id)
}

type rateLimitRetryChecker struct{ rateLimitCheckerFunc }

func (f rateLimitRetryChecker) Retry(ctx context.Context, scope models.Scope, id uuid.UUID) error {
	return f.rateLimitCheckerFunc(ctx, scope, id)
}
func (f rateLimitCheckerFunc) Retry(context.Context, models.Scope, uuid.UUID) error {
	panic("check endpoint must not call manual retry")
}
func (f rateLimitRetryChecker) Check(context.Context, models.Scope, uuid.UUID) error {
	panic("retry endpoint must not call provider check")
}

func TestCodingCredentialCheckRateLimit(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, scope, role string
		err               error
		invalidID         bool
		wantCode          int
		wantCall          bool
	}{
		{name: "org admin", scope: "org", role: "admin", wantCode: 204, wantCall: true},
		{name: "personal member", scope: "personal", role: "member", wantCode: 204, wantCall: true},
		{name: "personal builder", scope: "personal", role: "builder", wantCode: 204, wantCall: true},
		{name: "non-admin cannot check org", scope: "org", role: "member", wantCode: 403},
		{name: "resolved scope is read only", scope: "resolved", role: "admin", wantCode: 403},
		{name: "invalid scope", scope: "other", role: "admin", wantCode: 403},
		{name: "invalid ID", scope: "personal", role: "member", invalidID: true, wantCode: 400},
		{name: "cross-scope credential not found", scope: "personal", role: "member", err: db.ErrCodingCredentialNotFound, wantCode: 404, wantCall: true},
		{name: "provider failure", scope: "org", role: "admin", err: codingcredentials.ErrUnavailable, wantCode: 502, wantCall: true},
		{name: "unsupported credential", scope: "org", role: "admin", err: codingcredentials.ErrUnsupported, wantCode: 400, wantCall: true},
		{name: "setup token usage check", scope: "org", role: "admin", err: codingcredentials.ErrSetupTokenUsage, wantCode: 400, wantCall: true},
		{name: "unsupported manual retry", scope: "org", role: "admin", err: codingcredentials.ErrRetryUnsupported, wantCode: 400, wantCall: true},
		{name: "stale check", scope: "org", role: "admin", err: db.ErrRateLimitCheckStale, wantCode: 409, wantCall: true},
	}
	for _, action := range []string{"check", "retry"} {
		t.Run(action, func(t *testing.T) {
			t.Parallel()
			for _, tt := range tests {
				t.Run(tt.name, func(t *testing.T) {
					t.Parallel()
					orgID, userID, id := uuid.New(), uuid.New(), uuid.New()
					called := false
					handler := NewCodingCredentialHandler(&mockCodingCredentialStore{}, nil)
					checker := rateLimitCheckerFunc(func(_ context.Context, scope models.Scope, actualID uuid.UUID) error {
						called = true
						expected := models.Scope{OrgID: orgID}
						if tt.scope == "personal" {
							expected.UserID = &userID
						}
						require.Equal(t, expected, scope, "scope must come from authenticated tenant and user")
						require.Equal(t, id, actualID, "check must target requested credential")
						return tt.err
					})
					handler.SetRateLimitChecker(checker)
					if action == "retry" {
						handler.SetRateLimitChecker(rateLimitRetryChecker{checker})
					}
					r := withUserAndOrg(httptest.NewRequest(http.MethodPost, "/api/v1/coding-credentials/"+id.String()+"/"+action+"-rate-limit?scope="+tt.scope, nil), userID, orgID, tt.role)
					route := chi.NewRouteContext()
					param := id.String()
					if tt.invalidID {
						param = "invalid"
					}
					route.URLParams.Add("id", param)
					r = r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, route))
					w := httptest.NewRecorder()
					if action == "retry" {
						handler.RetryRateLimit(w, r)
					} else {
						handler.CheckRateLimit(w, r)
					}
					require.Equal(t, tt.wantCode, w.Code, "check should enforce scope and map provider outcomes")
					require.Equal(t, tt.wantCall, called, "unauthorized requests must never contact provider")
				})
			}
		})
	}
}
