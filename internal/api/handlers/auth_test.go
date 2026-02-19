package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/config"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
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
		name                string
		cookie              *http.Cookie
		setupMock           func(mock pgxmock.PgxPoolIface)
		expectedCode        int
		expectedBody        string
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

func TestAuthHandler_Providers(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		cfg      *config.Config
		expected map[string]bool
	}{
		{
			name: "all providers configured",
			cfg: &config.Config{
				GitHubOAuthClientID: "gh-id",
				GoogleOAuthClientID: "g-id",
			},
			expected: map[string]bool{"github": true, "google": true, "email": true},
		},
		{
			name: "only github configured",
			cfg: &config.Config{
				GitHubOAuthClientID: "gh-id",
			},
			expected: map[string]bool{"github": true, "google": false, "email": true},
		},
		{
			name:     "no oauth configured",
			cfg:      &config.Config{},
			expected: map[string]bool{"github": false, "google": false, "email": true},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			handler := NewAuthHandler(tt.cfg, nil, nil, nil)
			req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/providers", nil)
			w := httptest.NewRecorder()

			handler.Providers(w, req)
			require.Equal(t, http.StatusOK, w.Code)

			var resp struct {
				Data map[string]bool `json:"data"`
			}
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
			require.Equal(t, tt.expected, resp.Data)
		})
	}
}

func TestAuthHandler_Me(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		setupCtx     func(r *http.Request) *http.Request
		expectedCode int
	}{
		{
			name: "returns user when authenticated",
			setupCtx: func(r *http.Request) *http.Request {
				user := &models.User{
					ID:    uuid.New(),
					OrgID: uuid.New(),
					Email: "me@example.com",
					Name:  "Me",
					Role:  "admin",
				}
				ctx := middleware.WithUser(r.Context(), user)
				return r.WithContext(ctx)
			},
			expectedCode: http.StatusOK,
		},
		{
			name: "returns 401 when not authenticated",
			setupCtx: func(r *http.Request) *http.Request {
				return r
			},
			expectedCode: http.StatusUnauthorized,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			handler := NewAuthHandler(&config.Config{}, nil, nil, nil)
			req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/me", nil)
			req = tt.setupCtx(req)
			w := httptest.NewRecorder()

			handler.Me(w, req)
			require.Equal(t, tt.expectedCode, w.Code)
		})
	}
}

func TestAuthHandler_Register(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		body         map[string]string
		setupMock    func(mock pgxmock.PgxPoolIface)
		expectedCode int
		expectedBody string
	}{
		{
			name:         "missing name returns 400",
			body:         map[string]string{"email": "a@b.com", "password": "12345678"},
			setupMock:    nil,
			expectedCode: http.StatusBadRequest,
			expectedBody: "MISSING_NAME",
		},
		{
			name:         "invalid email returns 400",
			body:         map[string]string{"email": "not-email", "password": "12345678", "name": "Test"},
			setupMock:    nil,
			expectedCode: http.StatusBadRequest,
			expectedBody: "INVALID_EMAIL",
		},
		{
			name:         "short password returns 400",
			body:         map[string]string{"email": "a@b.com", "password": "short", "name": "Test"},
			setupMock:    nil,
			expectedCode: http.StatusBadRequest,
			expectedBody: "WEAK_PASSWORD",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var handler *AuthHandler
			if tt.setupMock != nil {
				mock, err := pgxmock.NewPool()
				require.NoError(t, err)
				defer mock.Close()
				tt.setupMock(mock)
				handler = NewAuthHandler(&config.Config{}, db.NewOrganizationStore(mock), db.NewUserStore(mock), db.NewSessionStore(mock))
			} else {
				handler = NewAuthHandler(&config.Config{}, nil, nil, nil)
			}

			body, _ := json.Marshal(tt.body)
			req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/register", bytes.NewReader(body))
			w := httptest.NewRecorder()

			handler.Register(w, req)
			require.Equal(t, tt.expectedCode, w.Code)
			require.Contains(t, w.Body.String(), tt.expectedBody)
		})
	}
}

func TestAuthHandler_EmailLogin(t *testing.T) {
	t.Parallel()

	userColumns := []string{"id", "org_id", "email", "name", "role", "github_id", "github_login", "avatar_url", "password_hash", "google_id", "created_at"}

	tests := []struct {
		name         string
		body         map[string]string
		setupMock    func(mock pgxmock.PgxPoolIface)
		expectedCode int
		expectedBody string
	}{
		{
			name: "nonexistent email returns 401",
			body: map[string]string{"email": "nobody@example.com", "password": "12345678"},
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectQuery("SELECT .+ FROM users WHERE email").
					WithArgs(pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows(userColumns))
			},
			expectedCode: http.StatusUnauthorized,
			expectedBody: "INVALID_CREDENTIALS",
		},
		{
			name: "oauth-only account returns 401 with hint",
			body: map[string]string{"email": "oauth@example.com", "password": "12345678"},
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectQuery("SELECT .+ FROM users WHERE email").
					WithArgs(pgxmock.AnyArg()).
					WillReturnRows(
						pgxmock.NewRows(userColumns).
							AddRow(uuid.New(), uuid.New(), "oauth@example.com", "OAuth User", "admin", nil, nil, nil, nil, nil, nil),
					)
			},
			expectedCode: http.StatusUnauthorized,
			expectedBody: "OAUTH_ONLY",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err)
			defer mock.Close()
			tt.setupMock(mock)

			handler := NewAuthHandler(&config.Config{}, nil, db.NewUserStore(mock), nil)

			body, _ := json.Marshal(tt.body)
			req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", bytes.NewReader(body))
			w := httptest.NewRecorder()

			handler.EmailLogin(w, req)
			require.Equal(t, tt.expectedCode, w.Code)
			require.Contains(t, w.Body.String(), tt.expectedBody)
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestAuthHandler_GoogleLogin_Redirects(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		GoogleOAuthClientID: "google-client-id",
		BaseURL:             "http://localhost:8080",
	}
	handler := NewAuthHandler(cfg, nil, nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/google/login", nil)
	w := httptest.NewRecorder()

	handler.GoogleLogin(w, req)
	require.Equal(t, http.StatusTemporaryRedirect, w.Code, "should redirect to Google OAuth")

	location := w.Header().Get("Location")
	require.Contains(t, location, "accounts.google.com/o/oauth2/v2/auth", "should redirect to Google OAuth")
	require.Contains(t, location, "client_id=google-client-id", "should include client ID")
	require.Contains(t, location, "scope=openid+email+profile", "should include scopes")
}

func TestAuthHandler_GoogleCallback_Validation(t *testing.T) {
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
			url:          "/api/v1/auth/google/callback?state=abc&code=test-code",
			cookie:       &http.Cookie{Name: "oauth_state", Value: "different-state"},
			expectedCode: http.StatusBadRequest,
			expectedBody: "INVALID_STATE",
		},
		{
			name:         "missing code returns bad request",
			url:          "/api/v1/auth/google/callback?state=valid-state",
			cookie:       &http.Cookie{Name: "oauth_state", Value: "valid-state"},
			expectedCode: http.StatusBadRequest,
			expectedBody: "MISSING_CODE",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			handler := NewAuthHandler(&config.Config{}, nil, nil, nil)
			req := httptest.NewRequest(http.MethodGet, tt.url, nil)
			if tt.cookie != nil {
				req.AddCookie(tt.cookie)
			}
			w := httptest.NewRecorder()

			handler.GoogleCallback(w, req)
			require.Equal(t, tt.expectedCode, w.Code)
			require.Contains(t, w.Body.String(), tt.expectedBody)
		})
	}
}

func TestAuthHandler_Register_DuplicateEmail(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	userColumns := []string{"id", "org_id", "email", "name", "role", "github_id", "github_login", "avatar_url", "password_hash", "google_id", "created_at"}

	// GetByEmail returns a user (duplicate)
	mock.ExpectQuery("SELECT .+ FROM users WHERE email").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(userColumns).
				AddRow(uuid.New(), uuid.New(), "dup@example.com", "Existing", "admin", nil, nil, nil, nil, nil, nil),
		)

	handler := NewAuthHandler(&config.Config{}, nil, db.NewUserStore(mock), nil)

	body, _ := json.Marshal(map[string]string{"email": "dup@example.com", "password": "12345678", "name": "Dup"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/register", bytes.NewReader(body))
	req = req.WithContext(context.Background())
	w := httptest.NewRecorder()

	handler.Register(w, req)
	require.Equal(t, http.StatusConflict, w.Code)
	require.Contains(t, w.Body.String(), "EMAIL_EXISTS")
	require.NoError(t, mock.ExpectationsWereMet())
}
