package handlers

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/assembledhq/143/internal/config"
	"github.com/assembledhq/143/internal/db"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAuthHandler_Login_Redirects(t *testing.T) {
	cfg := &config.Config{
		GitHubOAuthClientID: "test-client-id",
		BaseURL:             "http://localhost:8080",
	}
	handler := NewAuthHandler(cfg, nil, nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/github/login", nil)
	w := httptest.NewRecorder()

	handler.Login(w, req)
	assert.Equal(t, http.StatusTemporaryRedirect, w.Code)

	location := w.Header().Get("Location")
	assert.Contains(t, location, "https://github.com/login/oauth/authorize")
	assert.Contains(t, location, "client_id=test-client-id")
	assert.Contains(t, location, "redirect_uri=")

	// Verify oauth_state cookie is set
	cookies := w.Result().Cookies()
	var foundStateCookie bool
	for _, c := range cookies {
		if c.Name == "oauth_state" {
			foundStateCookie = true
			assert.NotEmpty(t, c.Value)
			assert.True(t, c.HttpOnly)
		}
	}
	assert.True(t, foundStateCookie, "oauth_state cookie should be set")
}

func TestAuthHandler_Logout_WithCookie(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	sessionStore := db.NewSessionStore(mock)
	cfg := &config.Config{}
	handler := NewAuthHandler(cfg, nil, nil, sessionStore)

	// DeleteByToken
	mock.ExpectExec("DELETE FROM sessions WHERE token").
		WithArgs(pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("DELETE", 1))

	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/logout", nil)
	req.AddCookie(&http.Cookie{Name: "session_token", Value: "test-session-token"})
	w := httptest.NewRecorder()

	handler.Logout(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "logged out")

	// Verify session_token cookie is cleared
	cookies := w.Result().Cookies()
	var foundClearedCookie bool
	for _, c := range cookies {
		if c.Name == "session_token" {
			foundClearedCookie = true
			assert.Equal(t, "", c.Value)
			assert.Equal(t, -1, c.MaxAge)
		}
	}
	assert.True(t, foundClearedCookie, "session_token cookie should be cleared")
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestAuthHandler_Logout_NoCookie(t *testing.T) {
	cfg := &config.Config{}
	handler := NewAuthHandler(cfg, nil, nil, nil)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/logout", nil)
	w := httptest.NewRecorder()

	handler.Logout(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), "logged out")
}

func TestAuthHandler_Callback_StateMismatch(t *testing.T) {
	cfg := &config.Config{}
	handler := NewAuthHandler(cfg, nil, nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/github/callback?state=abc&code=test-code", nil)
	// Set a different state in the cookie
	req.AddCookie(&http.Cookie{Name: "oauth_state", Value: "different-state"})
	w := httptest.NewRecorder()

	handler.Callback(w, req)
	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "INVALID_STATE")
}

func TestAuthHandler_Callback_MissingCode(t *testing.T) {
	cfg := &config.Config{}
	handler := NewAuthHandler(cfg, nil, nil, nil)

	// state matches but no code param
	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/github/callback?state=valid-state", nil)
	req.AddCookie(&http.Cookie{Name: "oauth_state", Value: "valid-state"})
	w := httptest.NewRecorder()

	handler.Callback(w, req)
	assert.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "MISSING_CODE")
}
