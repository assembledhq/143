package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/config"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

func TestAuthHandler_Login_Redirects(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		GitHubOAuthClientID:    "test-client-id",
		BaseURL:                "http://localhost:8080",
		GitHubOAuthRedirectURI: "http://localhost:8080/api/v1/auth/github/callback",
	}
	handler := NewAuthHandler(cfg, nil, nil, nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/github/login", nil)
	w := httptest.NewRecorder()

	handler.Login(w, req)
	require.Equal(t, http.StatusTemporaryRedirect, w.Code, "should redirect to GitHub OAuth")

	location := w.Header().Get("Location")
	require.Contains(t, location, "https://github.com/login/oauth/authorize", "should redirect to GitHub OAuth authorize URL")
	require.Contains(t, location, "client_id=test-client-id", "redirect URL should include the configured client ID")
	require.Contains(t, location, "redirect_uri=", "redirect URL should include a redirect URI")

	// Verify github_oauth_state cookie is set
	cookies := w.Result().Cookies()
	var foundStateCookie bool
	for _, c := range cookies {
		if c.Name == "github_oauth_state" {
			foundStateCookie = true
			require.NotEmpty(t, c.Value, "github_oauth_state cookie value should not be empty")
			require.True(t, c.HttpOnly, "github_oauth_state cookie should be HttpOnly")
		}
	}
	require.True(t, foundStateCookie, "github_oauth_state cookie should be set")
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
				mock.ExpectExec("DELETE FROM auth_sessions WHERE token").
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

				sessionStore := db.NewAuthSessionStore(mock)
				handler = NewAuthHandler(cfg, nil, nil, sessionStore, nil)
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
				handler = NewAuthHandler(cfg, nil, nil, nil, nil)

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
			cookie:       &http.Cookie{Name: "github_oauth_state", Value: "different-state"},
			expectedCode: http.StatusBadRequest,
			expectedBody: "INVALID_STATE",
		},
		{
			name:         "missing code returns bad request",
			url:          "/api/v1/auth/github/callback?state=valid-state",
			cookie:       &http.Cookie{Name: "github_oauth_state", Value: "valid-state"},
			expectedCode: http.StatusBadRequest,
			expectedBody: "MISSING_CODE",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			cfg := &config.Config{}
			handler := NewAuthHandler(cfg, nil, nil, nil, nil)

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

			handler := NewAuthHandler(tt.cfg, nil, nil, nil, nil)
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

			handler := NewAuthHandler(&config.Config{}, nil, nil, nil, nil)
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
				handler = NewAuthHandler(&config.Config{}, db.NewOrganizationStore(mock), db.NewUserStore(mock), db.NewAuthSessionStore(mock), nil)
			} else {
				handler = NewAuthHandler(&config.Config{}, nil, nil, nil, nil)
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

			handler := NewAuthHandler(&config.Config{}, nil, db.NewUserStore(mock), nil, nil)

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
	handler := NewAuthHandler(cfg, nil, nil, nil, nil)

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
			cookie:       &http.Cookie{Name: "google_oauth_state", Value: "different-state"},
			expectedCode: http.StatusBadRequest,
			expectedBody: "INVALID_STATE",
		},
		{
			name:         "missing code returns bad request",
			url:          "/api/v1/auth/google/callback?state=valid-state",
			cookie:       &http.Cookie{Name: "google_oauth_state", Value: "valid-state"},
			expectedCode: http.StatusBadRequest,
			expectedBody: "MISSING_CODE",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			handler := NewAuthHandler(&config.Config{}, nil, nil, nil, nil)
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

	handler := NewAuthHandler(&config.Config{}, nil, db.NewUserStore(mock), nil, nil)

	body, _ := json.Marshal(map[string]string{"email": "dup@example.com", "password": "12345678", "name": "Dup"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/register", bytes.NewReader(body))
	req = req.WithContext(context.Background())
	w := httptest.NewRecorder()

	handler.Register(w, req)
	require.Equal(t, http.StatusConflict, w.Code)
	require.Contains(t, w.Body.String(), "EMAIL_EXISTS")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAuthHandler_Register_InvitationClaimFailureReturnsGone(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	userColumns := []string{"id", "org_id", "email", "name", "role", "github_id", "github_login", "avatar_url", "password_hash", "google_id", "created_at"}
	orgID := uuid.New()
	invitationID := uuid.New()

	mock.ExpectQuery("SELECT .+ FROM users WHERE email").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(userColumns))
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT id, org_id, email, github_username, role, invited_by, token, status, expires_at, created_at, accepted_at FROM invitations WHERE token = @token").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{
				"id", "org_id", "email", "github_username", "role", "invited_by", "token", "status", "expires_at", "created_at", "accepted_at",
			}).AddRow(
				invitationID, orgID, strPtr("invitee@example.com"), nil, "member", uuid.New(), "test-token", "pending", time.Now().Add(time.Hour), time.Now(), nil,
			),
		)
	mock.ExpectExec("UPDATE invitations SET status = 'accepted', accepted_at = now\\(\\) WHERE id = @id AND status = 'pending'").
		WithArgs(pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 0))
	mock.ExpectRollback()

	handler := NewAuthHandler(
		&config.Config{},
		nil,
		db.NewUserStore(mock),
		db.NewAuthSessionStore(mock),
		db.NewInvitationStore(mock),
	)

	body, marshalErr := json.Marshal(map[string]string{
		"email":      "invitee@example.com",
		"password":   "12345678",
		"name":       "Invitee",
		"invitation": "test-token",
	})
	require.NoError(t, marshalErr, "should marshal request body")

	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/register", bytes.NewReader(body))
	w := httptest.NewRecorder()

	handler.Register(w, req)
	require.Equal(t, http.StatusGone, w.Code, "should reject registration when invitation cannot be claimed")
	require.Contains(t, w.Body.String(), "INVITE_INVALID", "should return invitation invalid error code when claim fails")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestAuthHandler_AcceptInvitationAndUpsertUser_ClaimFailureReturnsInvalid(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool without error")
	defer mock.Close()

	invitationID := uuid.New()

	mock.ExpectBegin()
	mock.ExpectExec("UPDATE invitations SET status = 'accepted', accepted_at = now\\(\\) WHERE id = @id AND status = 'pending'").
		WithArgs(pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 0))
	mock.ExpectRollback()

	handler := NewAuthHandler(
		&config.Config{},
		nil,
		db.NewUserStore(mock),
		nil,
		db.NewInvitationStore(mock),
	)

	upsertCalled := false
	user := &models.User{}

	createdUser, invErr, createErr := handler.acceptInvitationAndUpsertUser(
		context.Background(),
		invitationID,
		user,
		func(_ context.Context, _ *db.UserStore, _ *models.User) error {
			upsertCalled = true
			return nil
		},
	)

	require.Nil(t, createdUser, "should not create a user when invitation claim fails")
	require.NotNil(t, invErr, "should return invitation error when invitation claim fails")
	require.Equal(t, "INVITE_INVALID", invErr.code, "should map invitation claim failure to INVITE_INVALID")
	require.Equal(t, http.StatusGone, invErr.status, "should return gone status for invalid invitation claim")
	require.NoError(t, createErr, "should not return a generic error when invitation claim fails due to race")
	require.False(t, upsertCalled, "should not attempt user upsert when invitation claim fails")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestAuthHandler_AcceptInvitationAndUpsertUser_Success(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool without error")
	defer mock.Close()

	invitationID := uuid.New()
	orgID := uuid.New()
	expectedUserID := uuid.New()
	now := time.Now()
	githubID := int64(42)
	githubLogin := "octocat"
	avatarURL := "https://example.com/avatar.png"

	mock.ExpectBegin()
	mock.ExpectExec("UPDATE invitations SET status = 'accepted', accepted_at = now\\(\\) WHERE id = @id AND status = 'pending'").
		WithArgs(pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectQuery("INSERT INTO users").
		WithArgs(
			pgxmock.AnyArg(),
			pgxmock.AnyArg(),
			pgxmock.AnyArg(),
			pgxmock.AnyArg(),
			pgxmock.AnyArg(),
			pgxmock.AnyArg(),
			pgxmock.AnyArg(),
		).
		WillReturnRows(pgxmock.NewRows([]string{"id", "created_at"}).AddRow(expectedUserID, now))
	mock.ExpectCommit()

	handler := NewAuthHandler(
		&config.Config{},
		nil,
		db.NewUserStore(mock),
		nil,
		db.NewInvitationStore(mock),
	)

	user := &models.User{
		OrgID:       orgID,
		Email:       "invitee@example.com",
		Name:        "Invitee",
		Role:        "member",
		GitHubID:    &githubID,
		GitHubLogin: &githubLogin,
		AvatarURL:   &avatarURL,
	}

	upsertCalled := false
	createdUser, invErr, createErr := handler.acceptInvitationAndUpsertUser(
		context.Background(),
		invitationID,
		user,
		func(ctx context.Context, store *db.UserStore, invitedUser *models.User) error {
			upsertCalled = true
			return store.UpsertFromGitHub(ctx, invitedUser)
		},
	)

	require.NoError(t, createErr, "should not return an error when invitation claim and upsert both succeed")
	require.Nil(t, invErr, "should not return invitation error when invitation claim succeeds")
	require.True(t, upsertCalled, "should upsert user after successfully claiming invitation")
	require.NotNil(t, createdUser, "should return the created user on success")
	require.Equal(t, expectedUserID, createdUser.ID, "should populate user ID from upsert result")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

// --- Invitation-aware Register tests (#16) ---

func TestAuthHandler_Register_WithInvitation_NotFound(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	userColumns := []string{"id", "org_id", "email", "name", "role", "github_id", "github_login", "avatar_url", "password_hash", "google_id", "created_at"}
	invitationColumns := []string{"id", "org_id", "email", "github_username", "role", "invited_by", "token", "status", "expires_at", "created_at", "accepted_at"}

	// GetByEmail returns no user
	mock.ExpectQuery("SELECT .+ FROM users WHERE email").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(userColumns))
	// Begin transaction
	mock.ExpectBegin()
	// GetByToken returns no rows
	mock.ExpectQuery("SELECT .+ FROM invitations WHERE token").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(invitationColumns))
	mock.ExpectRollback()

	handler := NewAuthHandler(&config.Config{}, nil, db.NewUserStore(mock), db.NewAuthSessionStore(mock), db.NewInvitationStore(mock))
	body, _ := json.Marshal(map[string]string{"email": "new@example.com", "password": "12345678", "name": "New User", "invitation": "nonexistent-token"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/register", bytes.NewReader(body))
	w := httptest.NewRecorder()

	handler.Register(w, req)
	require.Equal(t, http.StatusBadRequest, w.Code)
	require.Contains(t, w.Body.String(), "INVITE_NOT_FOUND")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAuthHandler_Register_WithInvitation_Expired(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	userColumns := []string{"id", "org_id", "email", "name", "role", "github_id", "github_login", "avatar_url", "password_hash", "google_id", "created_at"}

	mock.ExpectQuery("SELECT .+ FROM users WHERE email").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(userColumns))
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT .+ FROM invitations WHERE token").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{"id", "org_id", "email", "github_username", "role", "invited_by", "token", "status", "expires_at", "created_at", "accepted_at"}).
				AddRow(uuid.New(), uuid.New(), strPtr("new@example.com"), nil, "member", uuid.New(), "expired-token", "pending", time.Now().Add(-1*time.Hour), time.Now().Add(-48*time.Hour), nil),
		)
	mock.ExpectRollback()

	handler := NewAuthHandler(&config.Config{}, nil, db.NewUserStore(mock), db.NewAuthSessionStore(mock), db.NewInvitationStore(mock))
	body, _ := json.Marshal(map[string]string{"email": "new@example.com", "password": "12345678", "name": "New User", "invitation": "expired-token"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/register", bytes.NewReader(body))
	w := httptest.NewRecorder()

	handler.Register(w, req)
	require.Equal(t, http.StatusGone, w.Code)
	require.Contains(t, w.Body.String(), "INVITE_EXPIRED")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAuthHandler_Register_WithInvitation_EmailMismatch(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	userColumns := []string{"id", "org_id", "email", "name", "role", "github_id", "github_login", "avatar_url", "password_hash", "google_id", "created_at"}

	mock.ExpectQuery("SELECT .+ FROM users WHERE email").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(userColumns))
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT .+ FROM invitations WHERE token").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{"id", "org_id", "email", "github_username", "role", "invited_by", "token", "status", "expires_at", "created_at", "accepted_at"}).
				AddRow(uuid.New(), uuid.New(), strPtr("someone-else@example.com"), nil, "member", uuid.New(), "mismatch-token", "pending", time.Now().Add(time.Hour), time.Now(), nil),
		)
	mock.ExpectRollback()

	handler := NewAuthHandler(&config.Config{}, nil, db.NewUserStore(mock), db.NewAuthSessionStore(mock), db.NewInvitationStore(mock))
	body, _ := json.Marshal(map[string]string{"email": "different@example.com", "password": "12345678", "name": "Wrong User", "invitation": "mismatch-token"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/register", bytes.NewReader(body))
	w := httptest.NewRecorder()

	handler.Register(w, req)
	require.Equal(t, http.StatusBadRequest, w.Code)
	require.Contains(t, w.Body.String(), "INVITE_MISMATCH")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAuthHandler_Register_WithInvitation_AlreadyAccepted(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	userColumns := []string{"id", "org_id", "email", "name", "role", "github_id", "github_login", "avatar_url", "password_hash", "google_id", "created_at"}

	mock.ExpectQuery("SELECT .+ FROM users WHERE email").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(userColumns))
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT .+ FROM invitations WHERE token").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{"id", "org_id", "email", "github_username", "role", "invited_by", "token", "status", "expires_at", "created_at", "accepted_at"}).
				AddRow(uuid.New(), uuid.New(), strPtr("new@example.com"), nil, "member", uuid.New(), "used-token", "accepted", time.Now().Add(time.Hour), time.Now(), nil),
		)
	mock.ExpectRollback()

	handler := NewAuthHandler(&config.Config{}, nil, db.NewUserStore(mock), db.NewAuthSessionStore(mock), db.NewInvitationStore(mock))
	body, _ := json.Marshal(map[string]string{"email": "new@example.com", "password": "12345678", "name": "New User", "invitation": "used-token"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/register", bytes.NewReader(body))
	w := httptest.NewRecorder()

	handler.Register(w, req)
	require.Equal(t, http.StatusGone, w.Code)
	require.Contains(t, w.Body.String(), "INVITE_INVALID")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAuthHandler_Register_WithInvitation_Revoked(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	userColumns := []string{"id", "org_id", "email", "name", "role", "github_id", "github_login", "avatar_url", "password_hash", "google_id", "created_at"}

	mock.ExpectQuery("SELECT .+ FROM users WHERE email").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(userColumns))
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT .+ FROM invitations WHERE token").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{"id", "org_id", "email", "github_username", "role", "invited_by", "token", "status", "expires_at", "created_at", "accepted_at"}).
				AddRow(uuid.New(), uuid.New(), strPtr("new@example.com"), nil, "member", uuid.New(), "revoked-token", "revoked", time.Now().Add(time.Hour), time.Now(), nil),
		)
	mock.ExpectRollback()

	handler := NewAuthHandler(&config.Config{}, nil, db.NewUserStore(mock), db.NewAuthSessionStore(mock), db.NewInvitationStore(mock))
	body, _ := json.Marshal(map[string]string{"email": "new@example.com", "password": "12345678", "name": "New User", "invitation": "revoked-token"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/register", bytes.NewReader(body))
	w := httptest.NewRecorder()

	handler.Register(w, req)
	require.Equal(t, http.StatusGone, w.Code)
	require.Contains(t, w.Body.String(), "INVITE_INVALID")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAuthHandler_Register_WithInvitation_Success(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	userColumns := []string{"id", "org_id", "email", "name", "role", "github_id", "github_login", "avatar_url", "password_hash", "google_id", "created_at"}
	orgID := uuid.New()
	invitationID := uuid.New()
	newUserID := uuid.New()

	// GetByEmail returns no rows (new user)
	mock.ExpectQuery("SELECT .+ FROM users WHERE email").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(userColumns))
	// Begin transaction
	mock.ExpectBegin()
	// GetByToken returns valid pending invitation
	mock.ExpectQuery("SELECT .+ FROM invitations WHERE token").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{"id", "org_id", "email", "github_username", "role", "invited_by", "token", "status", "expires_at", "created_at", "accepted_at"}).
				AddRow(invitationID, orgID, strPtr("invitee@example.com"), nil, "member", uuid.New(), "valid-token", "pending", time.Now().Add(24*time.Hour), time.Now(), nil),
		)
	// Accept invitation
	mock.ExpectExec("UPDATE invitations SET status = 'accepted'").
		WithArgs(pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	// CreateWithPassword (5 named args: org_id, email, name, role, password_hash)
	mock.ExpectQuery("INSERT INTO users").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "created_at"}).AddRow(newUserID, time.Now()))
	// Commit
	mock.ExpectCommit()
	// Create session (4 named args: user_id, org_id, token, expires_at)
	sessionID := uuid.New()
	mock.ExpectQuery("INSERT INTO auth_sessions").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "created_at"}).AddRow(sessionID, time.Now()))

	handler := NewAuthHandler(&config.Config{}, nil, db.NewUserStore(mock), db.NewAuthSessionStore(mock), db.NewInvitationStore(mock))
	body, _ := json.Marshal(map[string]string{"email": "invitee@example.com", "password": "12345678", "name": "Invitee", "invitation": "valid-token"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/register", bytes.NewReader(body))
	w := httptest.NewRecorder()

	handler.Register(w, req)
	require.Equal(t, http.StatusOK, w.Code, "should create user successfully with valid invitation")
	require.NoError(t, mock.ExpectationsWereMet())

	// Verify session cookie was set
	cookies := w.Result().Cookies()
	var foundSession bool
	for _, c := range cookies {
		if c.Name == "session_token" {
			foundSession = true
			require.NotEmpty(t, c.Value)
		}
	}
	require.True(t, foundSession, "session cookie should be set")
}

// --- Login with invitation cookie tests (#17) ---

func TestAuthHandler_Login_SetsInvitationCookie(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		GitHubOAuthClientID:    "test-client-id",
		BaseURL:                "http://localhost:8080",
		GitHubOAuthRedirectURI: "http://localhost:8080/api/v1/auth/github/callback",
	}
	handler := NewAuthHandler(cfg, nil, nil, nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/github/login?invitation=test-invite-token", nil)
	w := httptest.NewRecorder()

	handler.Login(w, req)
	require.Equal(t, http.StatusTemporaryRedirect, w.Code)

	// Verify pending_invitation cookie is set
	var foundInvCookie bool
	for _, c := range w.Result().Cookies() {
		if c.Name == "pending_invitation" {
			foundInvCookie = true
			require.Equal(t, "test-invite-token", c.Value)
			require.True(t, c.HttpOnly)
		}
	}
	require.True(t, foundInvCookie, "pending_invitation cookie should be set when ?invitation is provided")
}

func TestAuthHandler_Login_NoInvitationCookieWithoutParam(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		GitHubOAuthClientID:    "test-client-id",
		BaseURL:                "http://localhost:8080",
		GitHubOAuthRedirectURI: "http://localhost:8080/api/v1/auth/github/callback",
	}
	handler := NewAuthHandler(cfg, nil, nil, nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/github/login", nil)
	w := httptest.NewRecorder()

	handler.Login(w, req)
	require.Equal(t, http.StatusTemporaryRedirect, w.Code)

	for _, c := range w.Result().Cookies() {
		require.NotEqual(t, "pending_invitation", c.Name, "pending_invitation cookie should not be set without ?invitation param")
	}
}

func TestAuthHandler_GoogleLogin_SetsInvitationCookie(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		GoogleOAuthClientID: "google-client-id",
		BaseURL:             "http://localhost:8080",
	}
	handler := NewAuthHandler(cfg, nil, nil, nil, nil)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/google/login?invitation=google-invite-token", nil)
	w := httptest.NewRecorder()

	handler.GoogleLogin(w, req)
	require.Equal(t, http.StatusTemporaryRedirect, w.Code)

	var foundInvCookie bool
	for _, c := range w.Result().Cookies() {
		if c.Name == "pending_invitation" {
			foundInvCookie = true
			require.Equal(t, "google-invite-token", c.Value)
			require.True(t, c.HttpOnly)
		}
	}
	require.True(t, foundInvCookie, "pending_invitation cookie should be set for Google login")
}

func TestAuthHandler_Register_WithInvitation_ClearsCookie(t *testing.T) {
	t.Parallel()

	// Even when the invitation is invalid, the pending_invitation cookie should be cleared
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	userColumns := []string{"id", "org_id", "email", "name", "role", "github_id", "github_login", "avatar_url", "password_hash", "google_id", "created_at"}
	invitationColumns := []string{"id", "org_id", "email", "github_username", "role", "invited_by", "token", "status", "expires_at", "created_at", "accepted_at"}

	mock.ExpectQuery("SELECT .+ FROM users WHERE email").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(userColumns))
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT .+ FROM invitations WHERE token").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(invitationColumns))
	mock.ExpectRollback()

	handler := NewAuthHandler(&config.Config{}, nil, db.NewUserStore(mock), db.NewAuthSessionStore(mock), db.NewInvitationStore(mock))
	body, _ := json.Marshal(map[string]string{"email": "new@example.com", "password": "12345678", "name": "New", "invitation": "some-token"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/register", bytes.NewReader(body))
	w := httptest.NewRecorder()

	handler.Register(w, req)

	// Verify pending_invitation cookie is cleared (MaxAge=-1)
	var foundClearedCookie bool
	for _, c := range w.Result().Cookies() {
		if c.Name == "pending_invitation" {
			foundClearedCookie = true
			require.Equal(t, -1, c.MaxAge, "pending_invitation cookie should be cleared")
		}
	}
	require.True(t, foundClearedCookie, "pending_invitation cookie should be explicitly cleared on email registration with invitation")
	require.NoError(t, mock.ExpectationsWereMet())
}

// --- validateInvitation tests (#18) ---

func TestValidateInvitation(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	email1 := "user@example.com"
	emailCorrect := "correct@example.com"
	ghLogin := "octocat"

	tests := []struct {
		name        string
		token       string
		email       string
		githubLogin string
		invitation  *models.Invitation
		lookupErr   error
		expectErr   bool
		expectCode  string
	}{
		{
			name:       "valid pending invitation",
			token:      "valid-token",
			email:      "user@example.com",
			invitation: &models.Invitation{ID: uuid.New(), OrgID: orgID, Email: &email1, Role: "member", Status: "pending", ExpiresAt: time.Now().Add(time.Hour)},
			expectErr:  false,
		},
		{
			name:       "case-insensitive email match",
			token:      "valid-token",
			email:      "User@Example.COM",
			invitation: &models.Invitation{ID: uuid.New(), OrgID: orgID, Email: &email1, Role: "admin", Status: "pending", ExpiresAt: time.Now().Add(time.Hour)},
			expectErr:  false,
		},
		{
			name:        "github login match on github-only invitation",
			token:       "gh-token",
			email:       "",
			githubLogin: "octocat",
			invitation:  &models.Invitation{ID: uuid.New(), OrgID: orgID, GitHubUsername: &ghLogin, Role: "member", Status: "pending", ExpiresAt: time.Now().Add(time.Hour)},
			expectErr:   false,
		},
		{
			name:       "token not found",
			token:      "missing-token",
			email:      "user@example.com",
			lookupErr:  pgx.ErrNoRows,
			expectErr:  true,
			expectCode: "INVITE_NOT_FOUND",
		},
		{
			name:       "database error on lookup",
			token:      "error-token",
			email:      "user@example.com",
			lookupErr:  context.DeadlineExceeded,
			expectErr:  true,
			expectCode: "INVITE_LOOKUP_FAILED",
		},
		{
			name:       "non-pending status (accepted)",
			token:      "used-token",
			email:      "user@example.com",
			invitation: &models.Invitation{ID: uuid.New(), OrgID: orgID, Email: &email1, Role: "member", Status: "accepted", ExpiresAt: time.Now().Add(time.Hour)},
			expectErr:  true,
			expectCode: "INVITE_INVALID",
		},
		{
			name:       "non-pending status (revoked)",
			token:      "revoked-token",
			email:      "user@example.com",
			invitation: &models.Invitation{ID: uuid.New(), OrgID: orgID, Email: &email1, Role: "member", Status: "revoked", ExpiresAt: time.Now().Add(time.Hour)},
			expectErr:  true,
			expectCode: "INVITE_INVALID",
		},
		{
			name:       "expired invitation",
			token:      "expired-token",
			email:      "user@example.com",
			invitation: &models.Invitation{ID: uuid.New(), OrgID: orgID, Email: &email1, Role: "member", Status: "pending", ExpiresAt: time.Now().Add(-1 * time.Hour)},
			expectErr:  true,
			expectCode: "INVITE_EXPIRED",
		},
		{
			name:       "email mismatch",
			token:      "mismatch-token",
			email:      "wrong@example.com",
			invitation: &models.Invitation{ID: uuid.New(), OrgID: orgID, Email: &emailCorrect, Role: "member", Status: "pending", ExpiresAt: time.Now().Add(time.Hour)},
			expectErr:  true,
			expectCode: "INVITE_MISMATCH",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			store := &mockInvitationLookupStore{
				getByTokenFn: func(_ context.Context, _ string) (models.Invitation, error) {
					if tt.lookupErr != nil {
						return models.Invitation{}, tt.lookupErr
					}
					return *tt.invitation, nil
				},
			}

			handler := &AuthHandler{}
			inv, retOrgID, role, invErr := handler.validateInvitationWithStore(context.Background(), store, tt.token, tt.email, tt.githubLogin)

			if tt.expectErr {
				require.NotNil(t, invErr, "should return an error")
				require.Equal(t, tt.expectCode, invErr.code)
			} else {
				require.Nil(t, invErr, "should not return an error")
				require.Equal(t, tt.invitation.OrgID, retOrgID)
				require.Equal(t, tt.invitation.Role, role)
				require.Equal(t, tt.invitation.ID, inv.ID)
			}
		})
	}
}

func TestValidateInvitation_NilStore(t *testing.T) {
	t.Parallel()

	handler := &AuthHandler{}
	_, _, _, invErr := handler.validateInvitationWithStore(context.Background(), nil, "token", "email@test.com", "")
	require.NotNil(t, invErr)
	require.Equal(t, "INVITE_LOOKUP_FAILED", invErr.code)
}

// mockInvitationLookupStore implements invitationLookupStore for tests.
type mockInvitationLookupStore struct {
	getByTokenFn func(ctx context.Context, token string) (models.Invitation, error)
}

func (m *mockInvitationLookupStore) GetByToken(ctx context.Context, token string) (models.Invitation, error) {
	return m.getByTokenFn(ctx, token)
}
