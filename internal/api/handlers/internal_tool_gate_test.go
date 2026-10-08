package handlers

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/auth"
	"github.com/assembledhq/143/internal/models"
)

func TestInternalToolGateRequire(t *testing.T) {
	t.Parallel()
	const secret = "gate-secret"
	orgID, repoID, sessionID := uuid.New(), uuid.New(), uuid.New()
	mint := func(scopes []string) string {
		token, err := auth.GenerateSessionThreadTokenWithClaims(secret, orgID, repoID, sessionID, nil, scopes, string(models.SessionOriginAutomation), nil, time.Minute)
		require.NoError(t, err, "mint token")
		return token
	}
	tests := []struct {
		name       string
		token      string
		tool       string
		wantStatus int
		wantNext   bool
	}{
		{name: "allowlisted token calling an allowlisted tool passes to the handler", token: mint(models.PerTargetToolScopes()), tool: "session-history:search", wantStatus: http.StatusOK, wantNext: true},
		{name: "allowlisted token calling a denied tool is refused before the handler", token: mint(models.PerTargetToolScopes()), tool: "pr:create", wantStatus: http.StatusForbidden},
		{name: "allowlisted token calling a preview route is refused", token: mint(models.PerTargetToolScopes()), tool: "preview:ensure", wantStatus: http.StatusForbidden},
		{name: "allowlisted token calling a policy write is refused", token: mint(models.PerTargetToolScopes()), tool: "code-review-history:update_policy", wantStatus: http.StatusForbidden},
		{name: "an ordinary token is not gated", token: mint([]string{"preview:read"}), tool: "pr:create", wantStatus: http.StatusOK, wantNext: true},
		{name: "a missing token is left to the handler's own authorization", token: "", tool: "pr:create", wantStatus: http.StatusOK, wantNext: true},
		{name: "an invalid token is left to the handler's own authorization", token: "not-a-token", tool: "pr:create", wantStatus: http.StatusOK, wantNext: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			gate := NewInternalToolGate(secret)
			nextCalled := false
			handler := gate.Require(tt.tool, func(w http.ResponseWriter, r *http.Request) {
				nextCalled = true
				w.WriteHeader(http.StatusOK)
			})
			req := httptest.NewRequest(http.MethodPost, "/api/v1/internal/x", nil)
			if tt.token != "" {
				req.Header.Set("Authorization", "Bearer "+tt.token)
			}
			rec := httptest.NewRecorder()
			handler(rec, req)
			require.Equal(t, tt.wantStatus, rec.Code, "status")
			require.Equal(t, tt.wantNext, nextCalled, "handler reached")
			if tt.wantStatus == http.StatusForbidden {
				require.Contains(t, rec.Body.String(), "TOOL_NOT_ALLOWED", "the refusal names the allowlist")
			}
		})
	}
}
