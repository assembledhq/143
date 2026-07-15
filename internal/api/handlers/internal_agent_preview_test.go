package handlers

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/auth"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func TestInternalAgentPreviewHandler_RejectsCrossSessionAccess(t *testing.T) {
	t.Parallel()
	secret := "internal-preview-test-secret"
	orgID, repoID, tokenSessionID, requestedSessionID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	token, err := auth.GenerateSessionToken(secret, orgID, repoID, tokenSessionID, time.Minute)
	require.NoError(t, err, "session token should be generated")
	handler := NewInternalAgentPreviewHandler(nil, nil, secret, zerolog.Nop())
	tests := []struct {
		name, suffix, body string
		method             http.HandlerFunc
	}{
		{name: "observe", suffix: "/observe", body: `{}`, method: handler.Observe},
		{name: "act", suffix: "/act", body: `{"steps":[{"action":"click","selector":"button"}]}`, method: handler.Act},
		{name: "control", suffix: "/control", body: ``, method: handler.BrowserControl},
		{name: "handoff", suffix: "/control/request-handoff", body: `{"reason":"MFA required"}`, method: handler.RequestHumanHandoff},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			req := httptest.NewRequest(http.MethodPost, "/api/v1/internal/sessions/"+requestedSessionID.String()+"/preview"+tt.suffix, strings.NewReader(tt.body))
			route := chi.NewRouteContext()
			route.URLParams.Add("id", requestedSessionID.String())
			req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, route))
			req.Header.Set("Authorization", "Bearer "+token)
			rr := httptest.NewRecorder()
			tt.method(rr, req)
			require.Equal(t, http.StatusForbidden, rr.Code, "sandbox token must not access another session preview")
			require.Contains(t, rr.Body.String(), "SESSION_MISMATCH", "cross-session rejection should use the stable error code")
		})
	}
}

func TestInternalAgentPreviewHandler_RequiresPreviewCapability(t *testing.T) {
	t.Parallel()
	secret := "internal-preview-test-secret"
	orgID, repoID, sessionID := uuid.New(), uuid.New(), uuid.New()
	token, err := auth.GenerateSessionToken(secret, orgID, repoID, sessionID, time.Minute)
	require.NoError(t, err, "session token should be generated")
	handler := NewInternalAgentPreviewHandler(nil, nil, secret, zerolog.Nop())
	req := httptest.NewRequest(http.MethodPost, "/api/v1/internal/sessions/"+sessionID.String()+"/preview/observe", strings.NewReader(`{}`))
	route := chi.NewRouteContext()
	route.URLParams.Add("id", sessionID.String())
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, route))
	req.Header.Set("Authorization", "Bearer "+token)
	rr := httptest.NewRecorder()
	handler.Observe(rr, req)
	require.Equal(t, http.StatusForbidden, rr.Code, "sandbox token without preview capability should be rejected")
	require.Contains(t, rr.Body.String(), "PREVIEW_TOOL_NOT_AVAILABLE", "missing preview capability should use the stable error code")
}
