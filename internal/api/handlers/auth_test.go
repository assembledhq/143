package handlers

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/assembledhq/143/internal/config"
	"github.com/assembledhq/143/internal/db"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

func TestAuthHandler_Login_Redirects(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		GitHubOAuthClientID: "test-client-id",
		BaseURL:             "http://localhost:8080",
	}
	handler := NewAuthHandler(cfg, nil, nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/github/login", nil)
	w := httptest.NewRecorder()

	handler.Login(w, req)
	require.Equal(t, http.StatusTemporaryRedirect, w.Code, "should redirect to GitHub OAuth")

	location := w.Header().Get("Location")
	require.Contains(t, location, "https://github.com/login/oauth/authorize", "should redirect to GitHub OAuth authorize URL")
	require.Contains(t, location, "client_id=test-client-id", "redirect URL should include the configured client ID")
	require.Contains(t, location, "redirect_uri=", "redirect URL should include a redirect URI")

	// Verify oauth_state cookie is set
	cookies := w.Result().Cookies()
	var foundStateCookie bool
	for _, c := range cookies {
		if c.Name == "oauth_state" {
			foundStateCookie = true
			require.NotEmpty(t, c.Value, "oauth_state cookie value should not be empty")
			require.True(t, c.HttpOnly, "oauth_state cookie should be HttpOnly")
		}
	}
	require.True(t, foundStateCookie, "oauth_state cookie should be set")
}

func TestAuthHandler_Logout(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		cookie         *http.Cookie
		setupMock      func(mock pgxmock.PgxPoolIface)
		expectedCode   int
		expectedBody   string
		expectClearedCookie bool
	}{
		{
			name:   "with session cookie deletes session and clears cookie",
			cookie: &http.Cookie{Name: "session_token", Value: "test-session-token"},
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectExec("DELETE FROM sessions WHERE token").
					WithArgs(pgxmock.AnyArg()).
					WillReturnResult(pgxmock.NewResult("DELETE", 1))
			},
			expectedCode:        http.StatusOK,
			expectedBody:        "logged out",
			expectClearedCookie: true,
		},
		{
			name:                "without session cookie still returns success",
			cookie:              nil,
			setupMock:           nil,
			expectedCode:        http.StatusOK,
			expectedBody:        "logged out",
			expectClearedCookie: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg := &config.Config{}
			var handler *AuthHandler

			if tt.setupMock != nil {
				mock, err := pgxmock.NewPool()
				require.NoError(t, err, "should create pgxmock pool without error")
				defer mock.Close()

				sessionStore := db.NewSessionStore(mock)
				handler = NewAuthHandler(cfg, nil, nil, sessionStore)
				tt.setupMock(mock)

				req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/logout", nil)
				if tt.cookie != nil {
					req.AddCookie(tt.cookie)
				}
				w := httptest.NewRecorder()

				handler.Logout(w, req)
				require.Equal(t, tt.expectedCode, w.Code, "should return expected status code")
				require.Contains(t, w.Body.String(), tt.expectedBody, "response body should contain expected message")

				if tt.expectClearedCookie {
					cookies := w.Result().Cookies()
					var foundClearedCookie bool
					for _, c := range cookies {
						if c.Name == "session_token" {
							foundClearedCookie = true
							require.Equal(t, "", c.Value, "session_token cookie value should be empty")
							require.Equal(t, -1, c.MaxAge, "session_token cookie MaxAge should be -1 to clear it")
						}
					}
					require.True(t, foundClearedCookie, "session_token cookie should be cleared")
				}

				require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
			} else {
				handler = NewAuthHandler(cfg, nil, nil, nil)

				req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/logout", nil)
				if tt.cookie != nil {
					req.AddCookie(tt.cookie)
				}
				w := httptest.NewRecorder()

				handler.Logout(w, req)
				require.Equal(t, tt.expectedCode, w.Code, "should return expected status code")
				require.Contains(t, w.Body.String(), tt.expectedBody, "response body should contain expected message")
			}
		})
	}
}

func TestAuthHandler_Callback(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		url          string
		cookie       *http.Cookie
		expectedCode int
		expectedBody string
	}{
		{
			name:         "state mismatch returns bad request",
			url:          "/api/v1/auth/github/callback?state=abc&code=test-code",
			cookie:       &http.Cookie{Name: "oauth_state", Value: "different-state"},
			expectedCode: http.StatusBadRequest,
			expectedBody: "INVALID_STATE",
		},
		{
			name:         "missing code returns bad request",
			url:          "/api/v1/auth/github/callback?state=valid-state",
			cookie:       &http.Cookie{Name: "oauth_state", Value: "valid-state"},
			expectedCode: http.StatusBadRequest,
			expectedBody: "MISSING_CODE",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg := &config.Config{}
			handler := NewAuthHandler(cfg, nil, nil, nil)

			req := httptest.NewRequest(http.MethodGet, tt.url, nil)
			if tt.cookie != nil {
				req.AddCookie(tt.cookie)
			}
			w := httptest.NewRecorder()

			handler.Callback(w, req)
			require.Equal(t, tt.expectedCode, w.Code, "should return expected status code")
			require.Contains(t, w.Body.String(), tt.expectedBody, "response body should contain expected error code")
		})
	}
}
