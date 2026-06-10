package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/config"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	chiMiddleware "github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func expectUserLastOrgLookup(mock pgxmock.PgxPoolIface, userID uuid.UUID, lastOrgID *uuid.UUID) {
	rows := pgxmock.NewRows([]string{"last_org_id"})
	if lastOrgID == nil {
		rows.AddRow(nil)
	} else {
		rows.AddRow(lastOrgID.String())
	}
	mock.ExpectQuery(`SELECT last_org_id FROM users WHERE id = @id`).
		WithArgs(userID).
		WillReturnRows(rows)
}

func TestAuthHandler_Login_Redirects(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		GitHubOAuthClientID:    "test-client-id",
		BaseURL:                "http://localhost:8080",
		GitHubOAuthRedirectURI: "http://localhost:8080/api/v1/auth/github/callback",
	}
	handler := NewAuthHandler(cfg, nil, nil, nil, nil, nil)

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

func TestAuthHandler_EmitAuthEvent_IncludesStructuredDetails(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should initialize")
	defer mock.Close()

	handler := NewAuthHandler(&config.Config{}, nil, nil, nil, nil, nil)
	handler.SetAuditEmitter(newAuditEmitterForTest(mock))
	expectAuditInsert(mock)

	user := &models.User{
		ID:    uuid.New(),
		OrgID: uuid.New(),
		Email: "u@example.com",
		Name:  "User Example",
		Role:  "member",
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/logout", nil)
	req.Header.Set("User-Agent", "codex-test")
	req.Header.Set("X-Forwarded-For", "203.0.113.10")
	req = req.WithContext(context.WithValue(req.Context(), chiMiddleware.RequestIDKey, "req-123"))

	handler.emitAuthEvent(req, user, models.AuditActionAuthLogout)

	require.NoError(t, mock.ExpectationsWereMet(), "auth event should emit one structured audit row")
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
				handler = NewAuthHandler(cfg, nil, nil, sessionStore, nil, nil)
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
				handler = NewAuthHandler(cfg, nil, nil, nil, nil, nil)

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

func TestAuthHandler_PersistSessionTx_SeedsLastOrgIDFromUserPreference(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should initialize")
	defer mock.Close()

	cfg := &config.Config{}
	userStore := db.NewUserStore(mock)
	handler := NewAuthHandler(cfg, mock, userStore, db.NewAuthSessionStore(mock), nil, nil)

	user := &models.User{
		ID:    uuid.New(),
		OrgID: uuid.New(),
	}
	lastOrgID := uuid.New()

	expectUserLastOrgLookup(mock, user.ID, &lastOrgID)
	mock.ExpectQuery("INSERT INTO auth_sessions").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), &lastOrgID, pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{"id", "created_at"}).
				AddRow(uuid.New(), time.Now()),
		)

	token, err := handler.persistSessionTx(context.Background(), mock, user)
	require.NoError(t, err, "persistSessionTx should create a session successfully")
	require.NotEmpty(t, token, "persistSessionTx should return the minted session token")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestAuthHandler_PersistSessionTx_UsesDBTXForLastOrgLookup(t *testing.T) {
	t.Parallel()

	sessionDB, err := pgxmock.NewPool()
	require.NoError(t, err, "session database mock should initialize")
	defer sessionDB.Close()

	handlerStoreDB, err := pgxmock.NewPool()
	require.NoError(t, err, "handler user store mock should initialize")
	defer handlerStoreDB.Close()

	lastOrgID := uuid.New()
	user := &models.User{
		ID:    uuid.New(),
		OrgID: uuid.New(),
	}

	expectUserLastOrgLookup(sessionDB, user.ID, &lastOrgID)
	sessionDB.ExpectQuery("INSERT INTO auth_sessions").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), &lastOrgID, pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{"id", "created_at"}).
				AddRow(uuid.New(), time.Now()),
		)

	handler := NewAuthHandler(
		&config.Config{},
		nil,
		db.NewUserStore(handlerStoreDB),
		db.NewAuthSessionStore(sessionDB),
		nil,
		nil,
	)

	token, err := handler.persistSessionTx(context.Background(), sessionDB, user)
	require.NoError(t, err, "persistSessionTx should read and write through the provided dbtx")
	require.NotEmpty(t, token, "persistSessionTx should return the minted session token")
	require.NoError(t, sessionDB.ExpectationsWereMet(), "all dbtx expectations should be met")
	require.NoError(t, handlerStoreDB.ExpectationsWereMet(), "handler-scoped user store should be unused")
}

func TestAuthHandler_PersistSessionTx_ReturnsLookupErrors(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should initialize")
	defer mock.Close()

	user := &models.User{
		ID:    uuid.New(),
		OrgID: uuid.New(),
	}

	mock.ExpectQuery(`SELECT last_org_id FROM users WHERE id = @id`).
		WithArgs(user.ID).
		WillReturnError(errors.New("lookup failed"))

	handler := NewAuthHandler(&config.Config{}, nil, nil, db.NewAuthSessionStore(mock), nil, nil)
	token, err := handler.persistSessionTx(context.Background(), mock, user)
	require.Error(t, err, "persistSessionTx should return last-org lookup failures")
	require.Empty(t, token, "persistSessionTx should not return a session token on lookup failure")
	require.Contains(t, err.Error(), "get user last_org_id", "persistSessionTx should wrap the lookup failure")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestAuthHandler_SetActiveOrg(t *testing.T) {
	t.Parallel()

	t.Run("requires an authenticated user", func(t *testing.T) {
		t.Parallel()

		handler := NewAuthHandler(&config.Config{}, nil, nil, nil, nil, nil)
		req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/active-org", bytes.NewBufferString(`{"org_id":"`+uuid.New().String()+`"}`))
		w := httptest.NewRecorder()

		handler.SetActiveOrg(w, req)
		require.Equal(t, http.StatusUnauthorized, w.Code, "SetActiveOrg should reject unauthenticated requests")
	})

	t.Run("rejects invalid request bodies", func(t *testing.T) {
		t.Parallel()

		handler := NewAuthHandler(
			&config.Config{},
			nil,
			db.NewUserStore(nil),
			nil,
			nil,
			db.NewOrganizationMembershipStore(nil),
		)
		req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/active-org", bytes.NewBufferString(`{"org_id":`))
		req = req.WithContext(middleware.WithUser(req.Context(), &models.User{ID: uuid.New()}))
		w := httptest.NewRecorder()

		handler.SetActiveOrg(w, req)
		require.Equal(t, http.StatusBadRequest, w.Code, "SetActiveOrg should reject malformed JSON")
	})

	t.Run("rejects invalid org ids", func(t *testing.T) {
		t.Parallel()

		handler := NewAuthHandler(
			&config.Config{},
			nil,
			db.NewUserStore(nil),
			nil,
			nil,
			db.NewOrganizationMembershipStore(nil),
		)
		req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/active-org", bytes.NewBufferString(`{"org_id":"not-a-uuid"}`))
		req = req.WithContext(middleware.WithUser(req.Context(), &models.User{ID: uuid.New()}))
		w := httptest.NewRecorder()

		handler.SetActiveOrg(w, req)
		require.Equal(t, http.StatusBadRequest, w.Code, "SetActiveOrg should reject invalid organization ids")
	})

	t.Run("persists a membership the user holds", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err, "pgxmock pool should initialize")
		defer mock.Close()

		userID := uuid.New()
		targetOrgID := uuid.New()
		now := time.Now()

		mock.ExpectQuery("SELECT .+ FROM organization_memberships").
			WithArgs(userID, targetOrgID).
			WillReturnRows(
				pgxmock.NewRows([]string{"user_id", "org_id", "role", "created_at"}).
					AddRow(userID, targetOrgID, "member", now),
			)
		mock.ExpectExec(`UPDATE users SET last_org_id = @last_org_id WHERE id = @id`).
			WithArgs(&targetOrgID, userID).
			WillReturnResult(pgxmock.NewResult("UPDATE", 1))

		handler := NewAuthHandler(
			&config.Config{},
			nil,
			db.NewUserStore(mock),
			nil,
			nil,
			db.NewOrganizationMembershipStore(mock),
		)

		body := bytes.NewBufferString(fmt.Sprintf(`{"org_id":"%s"}`, targetOrgID))
		req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/active-org", body)
		req = req.WithContext(middleware.WithUser(req.Context(), &models.User{ID: userID}))
		w := httptest.NewRecorder()

		handler.SetActiveOrg(w, req)
		require.Equal(t, http.StatusNoContent, w.Code, "SetActiveOrg should persist the preference and return 204")
		require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
	})

	t.Run("rejects orgs the user is not a member of", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err, "pgxmock pool should initialize")
		defer mock.Close()

		userID := uuid.New()
		targetOrgID := uuid.New()

		mock.ExpectQuery("SELECT .+ FROM organization_memberships").
			WithArgs(userID, targetOrgID).
			WillReturnRows(pgxmock.NewRows([]string{"user_id", "org_id", "role", "created_at"}))

		handler := NewAuthHandler(
			&config.Config{},
			nil,
			db.NewUserStore(mock),
			nil,
			nil,
			db.NewOrganizationMembershipStore(mock),
		)

		body := bytes.NewBufferString(fmt.Sprintf(`{"org_id":"%s"}`, targetOrgID))
		req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/active-org", body)
		req = req.WithContext(middleware.WithUser(req.Context(), &models.User{ID: userID}))
		w := httptest.NewRecorder()

		handler.SetActiveOrg(w, req)
		require.Equal(t, http.StatusForbidden, w.Code, "SetActiveOrg should reject unrelated org ids")
		require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
	})

	t.Run("returns 500 when membership lookup fails", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err, "pgxmock pool should initialize")
		defer mock.Close()

		userID := uuid.New()
		targetOrgID := uuid.New()

		mock.ExpectQuery("SELECT .+ FROM organization_memberships").
			WithArgs(userID, targetOrgID).
			WillReturnError(errors.New("db down"))

		handler := NewAuthHandler(
			&config.Config{},
			nil,
			db.NewUserStore(mock),
			nil,
			nil,
			db.NewOrganizationMembershipStore(mock),
		)

		body := bytes.NewBufferString(fmt.Sprintf(`{"org_id":"%s"}`, targetOrgID))
		req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/active-org", body)
		req = req.WithContext(middleware.WithUser(req.Context(), &models.User{ID: userID}))
		w := httptest.NewRecorder()

		handler.SetActiveOrg(w, req)
		require.Equal(t, http.StatusInternalServerError, w.Code, "SetActiveOrg should surface membership lookup failures as 500s")
		require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
	})

	t.Run("returns 500 when user preference persistence fails", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err, "pgxmock pool should initialize")
		defer mock.Close()

		userID := uuid.New()
		targetOrgID := uuid.New()
		now := time.Now()

		mock.ExpectQuery("SELECT .+ FROM organization_memberships").
			WithArgs(userID, targetOrgID).
			WillReturnRows(
				pgxmock.NewRows([]string{"user_id", "org_id", "role", "created_at"}).
					AddRow(userID, targetOrgID, "member", now),
			)
		mock.ExpectExec(`UPDATE users SET last_org_id = @last_org_id WHERE id = @id`).
			WithArgs(&targetOrgID, userID).
			WillReturnError(errors.New("write failed"))

		handler := NewAuthHandler(
			&config.Config{},
			nil,
			db.NewUserStore(mock),
			nil,
			nil,
			db.NewOrganizationMembershipStore(mock),
		)

		body := bytes.NewBufferString(fmt.Sprintf(`{"org_id":"%s"}`, targetOrgID))
		req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/active-org", body)
		req = req.WithContext(middleware.WithUser(req.Context(), &models.User{ID: userID}))
		w := httptest.NewRecorder()

		handler.SetActiveOrg(w, req)
		require.Equal(t, http.StatusInternalServerError, w.Code, "SetActiveOrg should surface user preference persistence failures as 500s")
		require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
	})

	t.Run("returns 500 when session hint persistence fails", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err, "pgxmock pool should initialize")
		defer mock.Close()

		userID := uuid.New()
		targetOrgID := uuid.New()
		now := time.Now()

		mock.ExpectQuery("SELECT .+ FROM organization_memberships").
			WithArgs(userID, targetOrgID).
			WillReturnRows(
				pgxmock.NewRows([]string{"user_id", "org_id", "role", "created_at"}).
					AddRow(userID, targetOrgID, "member", now),
			)
		mock.ExpectExec(`UPDATE users SET last_org_id = @last_org_id WHERE id = @id`).
			WithArgs(&targetOrgID, userID).
			WillReturnResult(pgxmock.NewResult("UPDATE", 1))
		mock.ExpectExec(`UPDATE auth_sessions SET last_org_id = @last_org_id WHERE token = @token`).
			WithArgs(&targetOrgID, "session-token").
			WillReturnError(errors.New("session write failed"))

		handler := NewAuthHandler(
			&config.Config{},
			nil,
			db.NewUserStore(mock),
			db.NewAuthSessionStore(mock),
			nil,
			db.NewOrganizationMembershipStore(mock),
		)

		body := bytes.NewBufferString(fmt.Sprintf(`{"org_id":"%s"}`, targetOrgID))
		req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/active-org", body)
		req.AddCookie(&http.Cookie{Name: middleware.SessionCookieName, Value: "session-token"})
		req = req.WithContext(middleware.WithUser(req.Context(), &models.User{ID: userID}))
		w := httptest.NewRecorder()

		handler.SetActiveOrg(w, req)
		require.Equal(t, http.StatusInternalServerError, w.Code, "SetActiveOrg should fail when it cannot update the current session hint")
		require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
	})
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
			handler := NewAuthHandler(cfg, nil, nil, nil, nil, nil)

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

func TestAuthHandler_Callback_ExistingGitHubUserAndEmailLink(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		setupMock func(mock pgxmock.PgxPoolIface, now time.Time)
	}{
		{
			name: "existing github user updates and signs in",
			setupMock: func(mock pgxmock.PgxPoolIface, now time.Time) {
				userColumns := []string{"id", "org_id", "email", "name", "role", "github_id", "github_login", "github_noreply_email", "avatar_url", "password_hash", "google_id", "created_at"}
				userID := uuid.New()
				orgID := uuid.New()
				githubID := int64(42)
				oldLogin := "alice-old"
				oldNoreply := "42+alice-old@users.noreply.github.com"

				mock.ExpectQuery("SELECT .* FROM users WHERE github_id").
					WithArgs(githubID).
					WillReturnRows(pgxmock.NewRows(userColumns).
						AddRow(userID, orgID, "old@example.com", "Old Alice", "member", &githubID, &oldLogin, &oldNoreply, (*string)(nil), (*string)(nil), (*string)(nil), now))

				mock.ExpectQuery("INSERT INTO users .* ON CONFLICT .* RETURNING id, created_at").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows([]string{"id", "created_at"}).AddRow(userID, now))

				expectUserLastOrgLookup(mock, userID, nil)
				mock.ExpectQuery("INSERT INTO auth_sessions").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows([]string{"id", "created_at"}).AddRow(uuid.New(), now))
			},
		},
		{
			name: "email link path links github account and signs in",
			setupMock: func(mock pgxmock.PgxPoolIface, now time.Time) {
				userColumns := []string{"id", "org_id", "email", "name", "role", "github_id", "github_login", "github_noreply_email", "avatar_url", "password_hash", "google_id", "created_at"}
				userID := uuid.New()
				orgID := uuid.New()

				mock.ExpectQuery("SELECT .* FROM users WHERE github_id").
					WithArgs(int64(42)).
					WillReturnRows(pgxmock.NewRows(userColumns))

				mock.ExpectQuery("(?s)SELECT .+ FROM users WHERE LOWER\\(email\\) = LOWER\\(@email\\)").
					WithArgs("42+alicehub@users.noreply.github.com").
					WillReturnRows(pgxmock.NewRows(userColumns).
						AddRow(userID, orgID, "alice@example.com", "Alice", "member", (*int64)(nil), (*string)(nil), (*string)(nil), (*string)(nil), (*string)(nil), (*string)(nil), now))

				mock.ExpectExec("UPDATE users\\s+SET github_id = @github_id").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnResult(pgxmock.NewResult("UPDATE", 1))

				expectUserLastOrgLookup(mock, userID, nil)
				mock.ExpectQuery("INSERT INTO auth_sessions").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows([]string{"id", "created_at"}).AddRow(uuid.New(), now))
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "should create pgxmock pool")
			defer mock.Close()

			now := time.Now()
			tt.setupMock(mock, now)

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/login/oauth/access_token":
					_, _ = w.Write([]byte(`{"access_token":"ghu_token","token_type":"bearer","scope":"repo,user:email"}`))
				case "/user":
					_, _ = w.Write([]byte(`{"id":42,"login":"alicehub","name":"Alice Hub","email":"","avatar_url":"https://example.com/avatar.png"}`))
				case "/user/emails":
					_, _ = w.Write([]byte(`[{"email":"42+alicehub@users.noreply.github.com","verified":true}]`))
				default:
					http.NotFound(w, r)
				}
			}))
			defer server.Close()

			handler := NewAuthHandler(
				&config.Config{FrontendURL: "http://frontend.test"},
				mock,
				db.NewUserStore(mock),
				db.NewAuthSessionStore(mock),
				nil,
				nil,
			)
			// Point the handler at the local httptest server. The same URL
			// serves /login/oauth/access_token, /user, and /user/emails —
			// the handler uses gitHubAPIBase() vs gitHubOAuthBase() to
			// choose the path prefix, but here both bases collapse to the
			// same httptest server.
			handler.SetGitHubURLsForTest(server.URL, server.URL, server.Client())

			req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/github/callback?state=valid-state&code=test-code", nil)
			req.AddCookie(&http.Cookie{Name: "github_oauth_state", Value: "valid-state"})
			w := httptest.NewRecorder()

			handler.Callback(w, req)
			require.Equal(t, http.StatusTemporaryRedirect, w.Code, "Callback should redirect after successful OAuth login")
			require.Equal(t, "http://frontend.test", w.Header().Get("Location"), "Callback should redirect to the configured frontend")
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

// TestAuthHandler_Callback_EmptyGitHubNameFallsBackToLogin pins the behavior
// that protects "Unknown user" from leaking through the UI: GitHub's /user
// API returns name:"" for users who haven't set a public display name, and
// we must persist the login as the display name instead of the empty string.
func TestAuthHandler_Callback_EmptyGitHubNameFallsBackToLogin(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create pgxmock pool")
	defer mock.Close()

	now := time.Now()
	userColumns := []string{"id", "org_id", "email", "name", "role", "github_id", "github_login", "github_noreply_email", "avatar_url", "password_hash", "google_id", "created_at"}
	userID := uuid.New()
	orgID := uuid.New()
	githubID := int64(42)
	oldLogin := "nisarg-old"
	oldNoreply := "42+nisarg-old@users.noreply.github.com"

	mock.ExpectQuery("SELECT .* FROM users WHERE github_id").
		WithArgs(githubID).
		WillReturnRows(pgxmock.NewRows(userColumns).
			AddRow(userID, orgID, "old@example.com", "" /* prior empty name */, "member", &githubID, &oldLogin, &oldNoreply, (*string)(nil), (*string)(nil), (*string)(nil), now))

	// Args order matches the INSERT statement in UpsertFromGitHub:
	// org_id, email, name, role, github_id, github_login, github_noreply_email, avatar_url.
	// Pin the name arg to the login so the fallback is enforced by the test.
	mock.ExpectQuery("INSERT INTO users .* ON CONFLICT .* RETURNING id, created_at").
		WithArgs(
			pgxmock.AnyArg(),
			pgxmock.AnyArg(),
			"nisarg-assembled",
			pgxmock.AnyArg(),
			pgxmock.AnyArg(),
			pgxmock.AnyArg(),
			pgxmock.AnyArg(),
			pgxmock.AnyArg(),
		).
		WillReturnRows(pgxmock.NewRows([]string{"id", "created_at"}).AddRow(userID, now))

	expectUserLastOrgLookup(mock, userID, nil)
	mock.ExpectQuery("INSERT INTO auth_sessions").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "created_at"}).AddRow(uuid.New(), now))

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/login/oauth/access_token":
			_, _ = w.Write([]byte(`{"access_token":"ghu_token","token_type":"bearer","scope":"repo,user:email"}`))
		case "/user":
			// name:"" mirrors GitHub's response for users with no public
			// display name. The handler must substitute the login.
			_, _ = w.Write([]byte(`{"id":42,"login":"nisarg-assembled","name":"","email":"","avatar_url":"https://example.com/avatar.png"}`))
		case "/user/emails":
			_, _ = w.Write([]byte(`[{"email":"42+nisarg-assembled@users.noreply.github.com","verified":true}]`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	handler := NewAuthHandler(
		&config.Config{FrontendURL: "http://frontend.test"},
		mock,
		db.NewUserStore(mock),
		db.NewAuthSessionStore(mock),
		nil,
		nil,
	)
	handler.SetGitHubURLsForTest(server.URL, server.URL, server.Client())

	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/github/callback?state=valid-state&code=test-code", nil)
	req.AddCookie(&http.Cookie{Name: "github_oauth_state", Value: "valid-state"})
	w := httptest.NewRecorder()

	handler.Callback(w, req)
	require.Equal(t, http.StatusTemporaryRedirect, w.Code)
	require.NoError(t, mock.ExpectationsWereMet(), "name arg to UpsertFromGitHub must be the login when GitHub returned empty name")
}

// TestAuthHandler_Callback_NewSignupEmptyGitHubNameFallsBackToLogin pins the
// same fallback in the new-signup branch (where GetByGitHubID and GetByEmail
// both miss and we land in createSignupOrg). The existing-user test pins one
// of three call sites; this one covers the fresh-account path so diff-cover
// stays satisfied without the fallback drifting between branches over time.
func TestAuthHandler_Callback_NewSignupEmptyGitHubNameFallsBackToLogin(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create pgxmock pool")
	defer mock.Close()

	now := time.Now()
	userColumns := []string{"id", "org_id", "email", "name", "role", "github_id", "github_login", "github_noreply_email", "avatar_url", "password_hash", "google_id", "created_at"}
	newOrgID := uuid.New()
	newUserID := uuid.New()
	githubID := int64(99)

	// No existing GitHub user, no email match — falls through to signup.
	mock.ExpectQuery("SELECT .* FROM users WHERE github_id").
		WithArgs(githubID).
		WillReturnRows(pgxmock.NewRows(userColumns))
	mock.ExpectQuery("(?s)SELECT .+ FROM users WHERE LOWER\\(email\\)").
		WithArgs("99+nisarg-fresh@users.noreply.github.com").
		WillReturnRows(pgxmock.NewRows(userColumns))

	// createSignupOrg: org → user → membership → session → commit.
	mock.ExpectBegin()
	mock.ExpectQuery("INSERT INTO organizations").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "created_at", "updated_at"}).AddRow(newOrgID, now, now))
	// Pin the name arg (3rd in UpsertFromGitHub's INSERT) to the login: with
	// GitHub returning name:"" the handler must substitute ghUser.Login.
	mock.ExpectQuery("INSERT INTO users .* ON CONFLICT .* RETURNING id, created_at").
		WithArgs(
			pgxmock.AnyArg(),
			pgxmock.AnyArg(),
			"nisarg-fresh",
			pgxmock.AnyArg(),
			pgxmock.AnyArg(),
			pgxmock.AnyArg(),
			pgxmock.AnyArg(),
			pgxmock.AnyArg(),
		).
		WillReturnRows(pgxmock.NewRows([]string{"id", "created_at"}).AddRow(newUserID, now))
	mock.ExpectExec("INSERT INTO organization_memberships").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	expectUserLastOrgLookup(mock, newUserID, nil)
	mock.ExpectQuery("INSERT INTO auth_sessions").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "created_at"}).AddRow(uuid.New(), now))
	mock.ExpectCommit()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/login/oauth/access_token":
			_, _ = w.Write([]byte(`{"access_token":"ghu_token","token_type":"bearer","scope":"repo,user:email"}`))
		case "/user":
			_, _ = w.Write([]byte(`{"id":99,"login":"nisarg-fresh","name":"","email":"","avatar_url":"https://example.com/avatar.png"}`))
		case "/user/emails":
			_, _ = w.Write([]byte(`[{"email":"99+nisarg-fresh@users.noreply.github.com","verified":true}]`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	handler := NewAuthHandler(
		&config.Config{FrontendURL: "http://frontend.test"},
		mock,
		db.NewUserStore(mock),
		db.NewAuthSessionStore(mock),
		nil,
		nil,
	)
	handler.SetGitHubURLsForTest(server.URL, server.URL, server.Client())

	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/github/callback?state=valid-state&code=test-code", nil)
	req.AddCookie(&http.Cookie{Name: "github_oauth_state", Value: "valid-state"})
	w := httptest.NewRecorder()

	handler.Callback(w, req)
	require.Equal(t, http.StatusTemporaryRedirect, w.Code)
	require.NoError(t, mock.ExpectationsWereMet(), "name arg to new-signup UpsertFromGitHub must be the login when GitHub returned empty name")
}

func TestAuthHandler_Providers(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		cfg      *config.Config
		expected map[string]any
	}{
		{
			name: "all providers configured",
			cfg: &config.Config{
				GitHubOAuthClientID: "gh-id",
				GoogleOAuthClientID: "g-id",
			},
			expected: map[string]any{"github": true, "google": true, "email": true, "demo": false},
		},
		{
			name: "only github configured",
			cfg: &config.Config{
				GitHubOAuthClientID: "gh-id",
			},
			expected: map[string]any{"github": true, "google": false, "email": true, "demo": false},
		},
		{
			name:     "no oauth configured",
			cfg:      &config.Config{},
			expected: map[string]any{"github": false, "google": false, "email": true, "demo": false},
		},
		{
			name: "demo mode hides github oauth and exposes banner credentials",
			cfg: &config.Config{
				GitHubOAuthClientID: "gh-id",
				DemoMode:            true,
				DemoEmail:           "preview-admin@143.dev",
				DemoPassword:        "preview",
			},
			expected: map[string]any{
				"github":        false,
				"google":        false,
				"email":         true,
				"demo":          true,
				"demo_email":    "preview-admin@143.dev",
				"demo_password": "preview",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			handler := NewAuthHandler(tt.cfg, nil, nil, nil, nil, nil)
			req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/providers", nil)
			w := httptest.NewRecorder()

			handler.Providers(w, req)
			require.Equal(t, http.StatusOK, w.Code)

			var resp struct {
				Data map[string]any `json:"data"`
			}
			require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
			require.Equal(t, tt.expected, resp.Data)
		})
	}
}

// TestAuthHandler_Providers_OmitsDemoCredentialsWhenOff confirms that the
// demo_email / demo_password fields do NOT leak into the response when
// DemoMode is off, even if the config happens to have defaults populated.
func TestAuthHandler_Providers_OmitsDemoCredentialsWhenOff(t *testing.T) {
	t.Parallel()

	cfg := &config.Config{
		DemoMode:     false,
		DemoEmail:    "dogfood@143.dev",
		DemoPassword: "preview-dogfood",
	}
	handler := NewAuthHandler(cfg, nil, nil, nil, nil, nil)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/providers", nil)
	w := httptest.NewRecorder()

	handler.Providers(w, req)
	require.Equal(t, http.StatusOK, w.Code)

	var resp struct {
		Data map[string]any `json:"data"`
	}
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
	require.NotContains(t, resp.Data, "demo_email", "must not expose demo_email when DemoMode is off")
	require.NotContains(t, resp.Data, "demo_password", "must not expose demo_password when DemoMode is off")
}

func TestAuthHandler_Me(t *testing.T) {
	t.Parallel()

	settingsUserID := uuid.MustParse("11111111-1111-1111-1111-111111111111")
	settingsOrgID := uuid.MustParse("22222222-2222-2222-2222-222222222222")

	tests := []struct {
		name         string
		handler      *AuthHandler
		setupCtx     func(r *http.Request) *http.Request
		expectedCode int
		assertBody   func(t *testing.T, body []byte)
	}{
		{
			name:    "returns user when authenticated",
			handler: NewAuthHandler(&config.Config{}, nil, nil, nil, nil, nil),
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
			name: "returns stored user settings when user store is configured",
			handler: func() *AuthHandler {
				mock, err := pgxmock.NewPool()
				require.NoError(t, err, "should create pgxmock pool without error")
				t.Cleanup(func() { mock.Close() })
				userStore := db.NewUserStore(mock)
				settings := []byte(`{"coding_agent_reasoning_defaults":{"codex":"xhigh"}}`)
				mock.ExpectQuery(`SELECT .+ FROM users\s+WHERE id = @id`).
					WithArgs(settingsUserID).
					WillReturnRows(pgxmock.NewRows([]string{
						"id", "org_id", "email", "name", "role", "github_id", "github_login", "avatar_url", "google_id", "email_verified_at", "created_at", "settings",
					}).AddRow(settingsUserID, uuid.New(), "me@example.com", "Me", "member", nil, nil, nil, nil, nil, time.Now(), settings))
				handler := NewAuthHandler(&config.Config{}, nil, userStore, nil, nil, nil)
				return handler
			}(),
			setupCtx: func(r *http.Request) *http.Request {
				user := &models.User{
					ID:    settingsUserID,
					OrgID: settingsOrgID,
					Email: "me@example.com",
					Name:  "Me",
					Role:  "admin",
				}
				ctx := middleware.WithUser(r.Context(), user)
				return r.WithContext(ctx)
			},
			expectedCode: http.StatusOK,
			assertBody: func(t *testing.T, body []byte) {
				t.Helper()
				var resp models.SingleResponse[models.UserWithSettings]
				require.NoError(t, json.Unmarshal(body, &resp), "response body should be valid JSON")
				require.Equal(t, settingsOrgID, resp.Data.OrgID, "/auth/me should preserve the active membership org id")
				require.Equal(t, models.RoleAdmin, resp.Data.Role, "/auth/me should preserve the active membership role")
				require.Equal(t, models.UserSettings{
					CodingAgentReasoningDefaults: map[models.AgentType]models.ReasoningEffort{
						models.AgentTypeCodex: models.ReasoningEffortXHigh,
					},
				}, resp.Data.Settings, "/auth/me should return typed persisted user settings")
			},
		},
		{
			name:    "returns 401 when not authenticated",
			handler: NewAuthHandler(&config.Config{}, nil, nil, nil, nil, nil),
			setupCtx: func(r *http.Request) *http.Request {
				return r
			},
			expectedCode: http.StatusUnauthorized,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/me", nil)
			req = tt.setupCtx(req)
			w := httptest.NewRecorder()

			tt.handler.Me(w, req)
			require.Equal(t, tt.expectedCode, w.Code)
			if tt.assertBody != nil {
				tt.assertBody(t, w.Body.Bytes())
			}
		})
	}
}

func TestAuthHandler_UpdateSettings(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create pgxmock pool without error")
	defer mock.Close()

	userStore := db.NewUserStore(mock)
	handler := NewAuthHandler(&config.Config{}, nil, userStore, nil, nil, nil)
	userID := uuid.New()
	orgID := uuid.New()
	now := time.Now()
	settings := []byte(`{"coding_agent_reasoning_defaults":{"claude_code":"max"}}`)

	mock.ExpectExec("UPDATE users SET settings = @settings").
		WithArgs(pgxmock.AnyArg(), userID).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectQuery(`SELECT .+ FROM users\s+WHERE id = @id`).
		WithArgs(userID).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "org_id", "email", "name", "role", "github_id", "github_login", "avatar_url", "google_id", "email_verified_at", "created_at", "settings",
		}).AddRow(userID, orgID, "me@example.com", "Me", "admin", nil, nil, nil, nil, nil, now, settings))

	req := httptest.NewRequest(http.MethodPatch, "/api/v1/auth/me/settings", bytes.NewBufferString(`{"coding_agent_reasoning_defaults":{"claude_code":"max"}}`))
	req = req.WithContext(middleware.WithUser(req.Context(), &models.User{
		ID:    userID,
		OrgID: orgID,
		Email: "me@example.com",
		Name:  "Me",
		Role:  "admin",
	}))
	w := httptest.NewRecorder()

	handler.UpdateSettings(w, req)

	require.Equal(t, http.StatusOK, w.Code, "UpdateSettings should return success")
	var resp models.SingleResponse[models.UserWithSettings]
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp), "response body should be valid JSON")
	require.Equal(t, models.UserSettings{
		CodingAgentReasoningDefaults: map[models.AgentType]models.ReasoningEffort{
			models.AgentTypeClaudeCode: models.ReasoningEffortMax,
		},
	}, resp.Data.Settings, "updated user response should include typed persisted settings")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestAuthHandler_Me_ErrorPaths(t *testing.T) {
	t.Parallel()

	t.Run("returns user lookup failure when settings load fails", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err, "should create pgxmock pool without error")
		defer mock.Close()

		userID := uuid.New()
		handler := NewAuthHandler(&config.Config{}, nil, db.NewUserStore(mock), nil, nil, nil)
		mock.ExpectQuery(`SELECT .+ FROM users\s+WHERE id = @id`).
			WithArgs(userID).
			WillReturnError(errors.New("db unavailable"))

		req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/me", nil)
		req = req.WithContext(middleware.WithUser(req.Context(), &models.User{
			ID:    userID,
			OrgID: uuid.New(),
			Role:  "admin",
		}))
		w := httptest.NewRecorder()

		handler.Me(w, req)
		require.Equal(t, http.StatusInternalServerError, w.Code, "Me should surface settings lookup failures")
		require.Contains(t, w.Body.String(), "USER_LOOKUP_FAILED", "Me should report settings lookup failures explicitly")
		require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
	})
}

func TestAuthHandler_UpdateSettings_ErrorPaths(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		handler      *AuthHandler
		body         string
		setupCtx     func(r *http.Request) *http.Request
		expectedCode int
		expectedBody string
	}{
		{
			name:         "returns unauthorized when not authenticated",
			handler:      NewAuthHandler(&config.Config{}, nil, nil, nil, nil, nil),
			body:         `{}`,
			setupCtx:     func(r *http.Request) *http.Request { return r },
			expectedCode: http.StatusUnauthorized,
			expectedBody: "UNAUTHORIZED",
		},
		{
			name:         "returns unconfigured error when user store is missing",
			handler:      NewAuthHandler(&config.Config{}, nil, nil, nil, nil, nil),
			body:         `{}`,
			setupCtx:     withAuthUser,
			expectedCode: http.StatusInternalServerError,
			expectedBody: "USER_STORE_UNCONFIGURED",
		},
		{
			name:         "returns invalid body for malformed json",
			handler:      newAuthHandlerWithUserStore(t, nil),
			body:         `{"coding_agent_reasoning_defaults":`,
			setupCtx:     withAuthUser,
			expectedCode: http.StatusBadRequest,
			expectedBody: "INVALID_BODY",
		},
		{
			name:         "returns invalid settings for unsupported effort",
			handler:      newAuthHandlerWithUserStore(t, nil),
			body:         `{"coding_agent_reasoning_defaults":{"codex":"max"}}`,
			setupCtx:     withAuthUser,
			expectedCode: http.StatusBadRequest,
			expectedBody: "INVALID_USER_SETTINGS",
		},
		{
			name: "returns update failure when persistence fails",
			handler: func() *AuthHandler {
				mock, err := pgxmock.NewPool()
				require.NoError(t, err, "should create pgxmock pool without error")
				t.Cleanup(func() { mock.Close() })
				userID := authCoverageUserID
				mock.ExpectExec("UPDATE users SET settings = @settings").
					WithArgs(pgxmock.AnyArg(), userID).
					WillReturnError(errors.New("write failed"))
				return NewAuthHandler(&config.Config{}, nil, db.NewUserStore(mock), nil, nil, nil)
			}(),
			body:         `{"coding_agent_reasoning_defaults":{"claude_code":"max"}}`,
			setupCtx:     withAuthCoverageUser,
			expectedCode: http.StatusInternalServerError,
			expectedBody: "USER_SETTINGS_UPDATE_FAILED",
		},
		{
			name: "returns lookup failure when refresh load fails",
			handler: func() *AuthHandler {
				mock, err := pgxmock.NewPool()
				require.NoError(t, err, "should create pgxmock pool without error")
				t.Cleanup(func() { mock.Close() })
				userID := authCoverageUserID
				mock.ExpectExec("UPDATE users SET settings = @settings").
					WithArgs(pgxmock.AnyArg(), userID).
					WillReturnResult(pgxmock.NewResult("UPDATE", 1))
				mock.ExpectQuery(`SELECT .+ FROM users\s+WHERE id = @id`).
					WithArgs(userID).
					WillReturnError(errors.New("reload failed"))
				return NewAuthHandler(&config.Config{}, nil, db.NewUserStore(mock), nil, nil, nil)
			}(),
			body:         `{"coding_agent_reasoning_defaults":{"claude_code":"max"}}`,
			setupCtx:     withAuthCoverageUser,
			expectedCode: http.StatusInternalServerError,
			expectedBody: "USER_LOOKUP_FAILED",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			req := httptest.NewRequest(http.MethodPatch, "/api/v1/auth/me/settings", bytes.NewBufferString(tt.body))
			req = tt.setupCtx(req)
			w := httptest.NewRecorder()

			tt.handler.UpdateSettings(w, req)
			require.Equal(t, tt.expectedCode, w.Code, "UpdateSettings should return the expected status code")
			require.Contains(t, w.Body.String(), tt.expectedBody, "UpdateSettings should return the expected error code")
		})
	}
}

var authCoverageUserID = uuid.MustParse("aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa")

func withAuthUser(r *http.Request) *http.Request {
	return withAuthCoverageUser(r)
}

func withAuthCoverageUser(r *http.Request) *http.Request {
	return r.WithContext(middleware.WithUser(r.Context(), &models.User{
		ID:    authCoverageUserID,
		OrgID: uuid.MustParse("bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb"),
		Email: "me@example.com",
		Name:  "Me",
		Role:  "admin",
	}))
}

func newAuthHandlerWithUserStore(t *testing.T, configure func(mock pgxmock.PgxPoolIface)) *AuthHandler {
	t.Helper()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create pgxmock pool without error")
	t.Cleanup(func() { mock.Close() })
	if configure != nil {
		configure(mock)
	}
	return NewAuthHandler(&config.Config{}, nil, db.NewUserStore(mock), nil, nil, nil)
}

func TestAuthHandler_Memberships(t *testing.T) {
	t.Parallel()

	t.Run("returns 401 when not authenticated", func(t *testing.T) {
		t.Parallel()

		handler := NewAuthHandler(&config.Config{}, nil, nil, nil, nil, nil)
		req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/memberships", nil)
		w := httptest.NewRecorder()

		handler.Memberships(w, req)
		require.Equal(t, http.StatusUnauthorized, w.Code)
	})

	t.Run("returns memberships and active resolution for authenticated user", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		userID := uuid.New()
		activeOrgID := uuid.New()
		otherOrgID := uuid.New()

		mock.ExpectQuery("(?s)FROM organization_memberships m.+JOIN organizations").
			WithArgs(userID).
			WillReturnRows(
				pgxmock.NewRows([]string{"org_id", "org_name", "role"}).
					AddRow(activeOrgID, "Acme", "admin").
					AddRow(otherOrgID, "Beta", "member"),
			)

		handler := NewAuthHandler(&config.Config{}, nil, nil, nil, nil, db.NewOrganizationMembershipStore(mock))

		req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/memberships", nil)
		ctx := middleware.WithUser(req.Context(), &models.User{ID: userID})
		ctx = middleware.WithOrgID(ctx, activeOrgID)
		ctx = middleware.WithActiveRole(ctx, "admin")
		req = req.WithContext(ctx)
		w := httptest.NewRecorder()

		handler.Memberships(w, req)
		require.Equal(t, http.StatusOK, w.Code)

		var resp struct {
			Data models.MembershipsResponse `json:"data"`
		}
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		require.Equal(t, activeOrgID, resp.Data.ActiveOrgID)
		require.Equal(t, models.RoleAdmin, resp.Data.ActiveRole)
		require.Len(t, resp.Data.Memberships, 2)
		require.Equal(t, activeOrgID, resp.Data.Memberships[0].OrgID)
		require.Equal(t, "Acme", resp.Data.Memberships[0].OrgName)
		require.Equal(t, models.RoleAdmin, resp.Data.Memberships[0].Role)
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("zero-membership user gets empty list with nil active org", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		userID := uuid.New()
		mock.ExpectQuery("(?s)FROM organization_memberships m.+JOIN organizations").
			WithArgs(userID).
			WillReturnRows(pgxmock.NewRows([]string{"org_id", "org_name", "role"}))

		handler := NewAuthHandler(&config.Config{}, nil, nil, nil, nil, db.NewOrganizationMembershipStore(mock))

		req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/memberships", nil)
		ctx := middleware.WithUser(req.Context(), &models.User{ID: userID})
		// No OrgID or ActiveRole on context: uuid.Nil / "" is the expected
		// zero-membership shape.
		req = req.WithContext(ctx)
		w := httptest.NewRecorder()

		handler.Memberships(w, req)
		require.Equal(t, http.StatusOK, w.Code)

		var resp struct {
			Data models.MembershipsResponse `json:"data"`
		}
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		require.Equal(t, uuid.Nil, resp.Data.ActiveOrgID)
		require.Empty(t, resp.Data.ActiveRole)
		require.NotNil(t, resp.Data.Memberships, "empty array must not serialize as null")
		require.Len(t, resp.Data.Memberships, 0)
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("lookup failure returns 500", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		userID := uuid.New()
		mock.ExpectQuery("(?s)FROM organization_memberships m.+JOIN organizations").
			WithArgs(userID).
			WillReturnError(errors.New("db down"))

		handler := NewAuthHandler(&config.Config{}, nil, nil, nil, nil, db.NewOrganizationMembershipStore(mock))

		req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/memberships", nil)
		ctx := middleware.WithUser(req.Context(), &models.User{ID: userID})
		req = req.WithContext(ctx)
		w := httptest.NewRecorder()

		handler.Memberships(w, req)
		require.Equal(t, http.StatusInternalServerError, w.Code)
		require.Contains(t, w.Body.String(), "MEMBERSHIPS_LOOKUP_FAILED")
		require.NoError(t, mock.ExpectationsWereMet())
	})
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
				handler = NewAuthHandler(&config.Config{}, mock, db.NewUserStore(mock), db.NewAuthSessionStore(mock), nil, db.NewOrganizationMembershipStore(mock))
			} else {
				handler = NewAuthHandler(&config.Config{}, nil, nil, nil, nil, nil)
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

	userColumns := []string{"id", "org_id", "email", "name", "role", "github_id", "github_login", "github_noreply_email", "avatar_url", "password_hash", "google_id", "created_at"}

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
				mock.ExpectQuery("(?s)SELECT .+ FROM users WHERE LOWER\\(email\\)").
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
				mock.ExpectQuery("(?s)SELECT .+ FROM users WHERE LOWER\\(email\\)").
					WithArgs(pgxmock.AnyArg()).
					WillReturnRows(
						pgxmock.NewRows(userColumns).
							AddRow(uuid.New(), uuid.New(), "oauth@example.com", "OAuth User", "admin", nil, nil, nil, nil, nil, nil, nil),
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

			handler := NewAuthHandler(&config.Config{}, nil, db.NewUserStore(mock), nil, nil, nil)

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
	handler := NewAuthHandler(cfg, nil, nil, nil, nil, nil)

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

			handler := NewAuthHandler(&config.Config{}, nil, nil, nil, nil, nil)
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

	userColumns := []string{"id", "org_id", "email", "name", "role", "github_id", "github_login", "github_noreply_email", "avatar_url", "password_hash", "google_id", "created_at"}

	// GetByEmail returns a user (duplicate)
	mock.ExpectQuery("(?s)SELECT .+ FROM users WHERE LOWER\\(email\\)").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(userColumns).
				AddRow(uuid.New(), uuid.New(), "dup@example.com", "Existing", "admin", nil, nil, nil, nil, nil, nil, nil),
		)

	handler := NewAuthHandler(&config.Config{}, nil, db.NewUserStore(mock), nil, nil, nil)

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

	userColumns := []string{"id", "org_id", "email", "name", "role", "github_id", "github_login", "github_noreply_email", "avatar_url", "password_hash", "google_id", "created_at"}
	orgID := uuid.New()
	invitationID := uuid.New()

	mock.ExpectQuery("(?s)SELECT .+ FROM users WHERE LOWER\\(email\\)").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(userColumns))
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT id, org_id, email, github_username, acceptance_method, role, invited_by, token, status, expires_at, created_at, accepted_at FROM invitations WHERE token = @token").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{
				"id", "org_id", "email", "github_username", "acceptance_method", "role", "invited_by", "token", "status", "expires_at", "created_at", "accepted_at",
			}).AddRow(
				invitationID, orgID, strPtr("invitee@example.com"), nil, models.InvitationAcceptanceMethodEither, "member", uuid.New(), "test-token", "pending", time.Now().Add(time.Hour), time.Now(), nil,
			),
		)
	mock.ExpectExec("UPDATE invitations SET status = 'accepted', accepted_at = now\\(\\) WHERE id = @id AND status = 'pending'").
		WithArgs(pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 0))
	mock.ExpectRollback()

	handler := NewAuthHandler(
		&config.Config{},
		mock,
		db.NewUserStore(mock),
		db.NewAuthSessionStore(mock),
		db.NewInvitationStore(mock),
		db.NewOrganizationMembershipStore(mock),
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
		mock,
		db.NewUserStore(mock),
		nil,
		db.NewInvitationStore(mock),
		db.NewOrganizationMembershipStore(mock),
	)

	upsertCalled := false
	user := &models.User{}

	createdUser, _, invErr, createErr := handler.acceptInvitationAndUpsertUser(
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
			pgxmock.AnyArg(),
		).
		WillReturnRows(pgxmock.NewRows([]string{"id", "created_at"}).AddRow(expectedUserID, now))
	// Grant membership inside the same tx as the invitation claim.
	mock.ExpectQuery("INSERT INTO organization_memberships").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"role"}).AddRow("member"))
	expectUserLastOrgLookup(mock, expectedUserID, nil)
	mock.ExpectQuery("INSERT INTO auth_sessions").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "created_at"}).AddRow(uuid.New(), time.Now()))
	mock.ExpectCommit()

	handler := NewAuthHandler(
		&config.Config{},
		mock,
		db.NewUserStore(mock),
		nil,
		db.NewInvitationStore(mock),
		db.NewOrganizationMembershipStore(mock),
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
	createdUser, _, invErr, createErr := handler.acceptInvitationAndUpsertUser(
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

	userColumns := []string{"id", "org_id", "email", "name", "role", "github_id", "github_login", "github_noreply_email", "avatar_url", "password_hash", "google_id", "created_at"}
	invitationColumns := []string{"id", "org_id", "email", "github_username", "acceptance_method", "role", "invited_by", "token", "status", "expires_at", "created_at", "accepted_at"}

	// GetByEmail returns no user
	mock.ExpectQuery("(?s)SELECT .+ FROM users WHERE LOWER\\(email\\)").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(userColumns))
	// Begin transaction
	mock.ExpectBegin()
	// GetByToken returns no rows
	mock.ExpectQuery("SELECT .+ FROM invitations WHERE token").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(invitationColumns))
	mock.ExpectRollback()

	handler := NewAuthHandler(&config.Config{}, mock, db.NewUserStore(mock), db.NewAuthSessionStore(mock), db.NewInvitationStore(mock), db.NewOrganizationMembershipStore(mock))
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

	userColumns := []string{"id", "org_id", "email", "name", "role", "github_id", "github_login", "github_noreply_email", "avatar_url", "password_hash", "google_id", "created_at"}

	mock.ExpectQuery("(?s)SELECT .+ FROM users WHERE LOWER\\(email\\)").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(userColumns))
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT .+ FROM invitations WHERE token").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{"id", "org_id", "email", "github_username", "acceptance_method", "role", "invited_by", "token", "status", "expires_at", "created_at", "accepted_at"}).
				AddRow(uuid.New(), uuid.New(), strPtr("new@example.com"), nil, models.InvitationAcceptanceMethodEither, "member", uuid.New(), "expired-token", "pending", time.Now().Add(-1*time.Hour), time.Now().Add(-48*time.Hour), nil),
		)
	mock.ExpectRollback()

	handler := NewAuthHandler(&config.Config{}, mock, db.NewUserStore(mock), db.NewAuthSessionStore(mock), db.NewInvitationStore(mock), db.NewOrganizationMembershipStore(mock))
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

	userColumns := []string{"id", "org_id", "email", "name", "role", "github_id", "github_login", "github_noreply_email", "avatar_url", "password_hash", "google_id", "created_at"}

	mock.ExpectQuery("(?s)SELECT .+ FROM users WHERE LOWER\\(email\\)").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(userColumns))
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT .+ FROM invitations WHERE token").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{"id", "org_id", "email", "github_username", "acceptance_method", "role", "invited_by", "token", "status", "expires_at", "created_at", "accepted_at"}).
				AddRow(uuid.New(), uuid.New(), strPtr("someone-else@example.com"), nil, models.InvitationAcceptanceMethodEither, "member", uuid.New(), "mismatch-token", "pending", time.Now().Add(time.Hour), time.Now(), nil),
		)
	mock.ExpectRollback()

	handler := NewAuthHandler(&config.Config{}, mock, db.NewUserStore(mock), db.NewAuthSessionStore(mock), db.NewInvitationStore(mock), db.NewOrganizationMembershipStore(mock))
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

	userColumns := []string{"id", "org_id", "email", "name", "role", "github_id", "github_login", "github_noreply_email", "avatar_url", "password_hash", "google_id", "created_at"}

	mock.ExpectQuery("(?s)SELECT .+ FROM users WHERE LOWER\\(email\\)").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(userColumns))
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT .+ FROM invitations WHERE token").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{"id", "org_id", "email", "github_username", "acceptance_method", "role", "invited_by", "token", "status", "expires_at", "created_at", "accepted_at"}).
				AddRow(uuid.New(), uuid.New(), strPtr("new@example.com"), nil, models.InvitationAcceptanceMethodEither, "member", uuid.New(), "used-token", "accepted", time.Now().Add(time.Hour), time.Now(), nil),
		)
	mock.ExpectRollback()

	handler := NewAuthHandler(&config.Config{}, mock, db.NewUserStore(mock), db.NewAuthSessionStore(mock), db.NewInvitationStore(mock), db.NewOrganizationMembershipStore(mock))
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

	userColumns := []string{"id", "org_id", "email", "name", "role", "github_id", "github_login", "github_noreply_email", "avatar_url", "password_hash", "google_id", "created_at"}

	mock.ExpectQuery("(?s)SELECT .+ FROM users WHERE LOWER\\(email\\)").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(userColumns))
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT .+ FROM invitations WHERE token").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{"id", "org_id", "email", "github_username", "acceptance_method", "role", "invited_by", "token", "status", "expires_at", "created_at", "accepted_at"}).
				AddRow(uuid.New(), uuid.New(), strPtr("new@example.com"), nil, models.InvitationAcceptanceMethodEither, "member", uuid.New(), "revoked-token", "revoked", time.Now().Add(time.Hour), time.Now(), nil),
		)
	mock.ExpectRollback()

	handler := NewAuthHandler(&config.Config{}, mock, db.NewUserStore(mock), db.NewAuthSessionStore(mock), db.NewInvitationStore(mock), db.NewOrganizationMembershipStore(mock))
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

	userColumns := []string{"id", "org_id", "email", "name", "role", "github_id", "github_login", "github_noreply_email", "avatar_url", "password_hash", "google_id", "created_at"}
	orgID := uuid.New()
	invitationID := uuid.New()
	newUserID := uuid.New()

	// GetByEmail returns no rows (new user)
	mock.ExpectQuery("(?s)SELECT .+ FROM users WHERE LOWER\\(email\\)").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(userColumns))
	// Begin transaction
	mock.ExpectBegin()
	// GetByToken returns valid pending invitation
	mock.ExpectQuery("SELECT .+ FROM invitations WHERE token").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{"id", "org_id", "email", "github_username", "acceptance_method", "role", "invited_by", "token", "status", "expires_at", "created_at", "accepted_at"}).
				AddRow(invitationID, orgID, strPtr("invitee@example.com"), nil, models.InvitationAcceptanceMethodEither, "member", uuid.New(), "valid-token", "pending", time.Now().Add(24*time.Hour), time.Now(), nil),
		)
	// Accept invitation
	mock.ExpectExec("UPDATE invitations SET status = 'accepted'").
		WithArgs(pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	// CreateWithPassword (5 named args: org_id, email, name, role, password_hash)
	mock.ExpectQuery("INSERT INTO users").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "created_at"}).AddRow(newUserID, time.Now()))
	// Claiming the emailed token proves address receipt → verification stamp.
	mock.ExpectExec("UPDATE users SET email_verified_at").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	// Membership upsert inside the signup tx.
	mock.ExpectQuery("INSERT INTO organization_memberships").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"role"}).AddRow("member"))
	// Create session inside the signup tx (5 named args: user_id, org_id, last_org_id, token, expires_at).
	sessionID := uuid.New()
	expectUserLastOrgLookup(mock, newUserID, nil)
	mock.ExpectQuery("INSERT INTO auth_sessions").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "created_at"}).AddRow(sessionID, time.Now()))
	mock.ExpectCommit()

	handler := NewAuthHandler(&config.Config{}, mock, db.NewUserStore(mock), db.NewAuthSessionStore(mock), db.NewInvitationStore(mock), db.NewOrganizationMembershipStore(mock))
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
	handler := NewAuthHandler(cfg, nil, nil, nil, nil, nil)

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
	handler := NewAuthHandler(cfg, nil, nil, nil, nil, nil)

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
	handler := NewAuthHandler(cfg, nil, nil, nil, nil, nil)

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

	userColumns := []string{"id", "org_id", "email", "name", "role", "github_id", "github_login", "github_noreply_email", "avatar_url", "password_hash", "google_id", "created_at"}
	invitationColumns := []string{"id", "org_id", "email", "github_username", "acceptance_method", "role", "invited_by", "token", "status", "expires_at", "created_at", "accepted_at"}

	mock.ExpectQuery("(?s)SELECT .+ FROM users WHERE LOWER\\(email\\)").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(userColumns))
	mock.ExpectBegin()
	mock.ExpectQuery("SELECT .+ FROM invitations WHERE token").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(invitationColumns))
	mock.ExpectRollback()

	handler := NewAuthHandler(&config.Config{}, mock, db.NewUserStore(mock), db.NewAuthSessionStore(mock), db.NewInvitationStore(mock), db.NewOrganizationMembershipStore(mock))
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
			name:        "github-required invitation rejects matching email without github login",
			token:       "gh-required-token",
			email:       "user@example.com",
			githubLogin: "",
			invitation:  &models.Invitation{ID: uuid.New(), OrgID: orgID, Email: &email1, GitHubUsername: &ghLogin, AcceptanceMethod: models.InvitationAcceptanceMethodGitHub, Role: "member", Status: "pending", ExpiresAt: time.Now().Add(time.Hour)},
			expectErr:   true,
			expectCode:  "INVITE_MISMATCH",
		},
		{
			name:        "github-required invitation accepts matching github login with durable email present",
			token:       "gh-required-token",
			email:       "user@example.com",
			githubLogin: "octocat",
			invitation:  &models.Invitation{ID: uuid.New(), OrgID: orgID, Email: &email1, GitHubUsername: &ghLogin, AcceptanceMethod: models.InvitationAcceptanceMethodGitHub, Role: "member", Status: "pending", ExpiresAt: time.Now().Add(time.Hour)},
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

// Register happy path without an invitation: creates org, user, and admin
// membership atomically and sets a session cookie. Exercises createSignupOrg.
func TestAuthHandler_Register_NoInvitation_Success(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	userColumns := []string{"id", "org_id", "email", "name", "role", "github_id", "github_login", "github_noreply_email", "avatar_url", "password_hash", "google_id", "created_at"}
	newOrgID := uuid.New()
	newUserID := uuid.New()
	now := time.Now()

	// GetByEmail returns no rows (fresh user).
	mock.ExpectQuery("(?s)SELECT .+ FROM users WHERE LOWER\\(email\\)").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(userColumns))
	// Signup transaction: create org, user, admin membership, commit.
	mock.ExpectBegin()
	mock.ExpectQuery("INSERT INTO organizations").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "created_at", "updated_at"}).AddRow(newOrgID, now, now))
	mock.ExpectQuery("INSERT INTO users").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "created_at"}).AddRow(newUserID, now))
	mock.ExpectExec("INSERT INTO organization_memberships").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	// Session insert happens inside the signup tx before commit.
	expectUserLastOrgLookup(mock, newUserID, nil)
	mock.ExpectQuery("INSERT INTO auth_sessions").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "created_at"}).AddRow(uuid.New(), now))
	mock.ExpectCommit()

	handler := NewAuthHandler(&config.Config{}, mock, db.NewUserStore(mock), db.NewAuthSessionStore(mock), db.NewInvitationStore(mock), db.NewOrganizationMembershipStore(mock))
	body, _ := json.Marshal(map[string]string{"email": "solo@example.com", "password": "12345678", "name": "Solo"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/register", bytes.NewReader(body))
	w := httptest.NewRecorder()

	handler.Register(w, req)
	require.Equal(t, http.StatusOK, w.Code)
	require.NoError(t, mock.ExpectationsWereMet())

	var foundSession bool
	for _, c := range w.Result().Cookies() {
		if c.Name == "session_token" {
			foundSession = true
		}
	}
	require.True(t, foundSession, "session cookie should be set")
}

// Register rolls back the signup transaction when user creation fails,
// ensuring the org row doesn't leak.
func TestAuthHandler_Register_NoInvitation_UserCreateFailsRollsBack(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	userColumns := []string{"id", "org_id", "email", "name", "role", "github_id", "github_login", "github_noreply_email", "avatar_url", "password_hash", "google_id", "created_at"}
	now := time.Now()

	mock.ExpectQuery("(?s)SELECT .+ FROM users WHERE LOWER\\(email\\)").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(userColumns))
	mock.ExpectBegin()
	mock.ExpectQuery("INSERT INTO organizations").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "created_at", "updated_at"}).AddRow(uuid.New(), now, now))
	mock.ExpectQuery("INSERT INTO users").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnError(context.DeadlineExceeded)
	mock.ExpectRollback()

	handler := NewAuthHandler(&config.Config{}, mock, db.NewUserStore(mock), db.NewAuthSessionStore(mock), db.NewInvitationStore(mock), db.NewOrganizationMembershipStore(mock))
	body, _ := json.Marshal(map[string]string{"email": "fail@example.com", "password": "12345678", "name": "Fail"})
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/register", bytes.NewReader(body))
	w := httptest.NewRecorder()

	handler.Register(w, req)
	require.Equal(t, http.StatusInternalServerError, w.Code)
	require.Contains(t, w.Body.String(), "USER_CREATE_FAILED")
	require.NoError(t, mock.ExpectationsWereMet())
}

// claimInvitationForExistingUser grants a second-org membership to an already-
// authenticated user after validating the invitation token.
func TestAuthHandler_ClaimInvitationForExistingUser_Success(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	userID := uuid.New()
	orgID := uuid.New()
	invID := uuid.New()

	// Validation read happens on the pool before any tx is opened.
	mock.ExpectQuery("SELECT .+ FROM invitations WHERE token").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{"id", "org_id", "email", "github_username", "acceptance_method", "role", "invited_by", "token", "status", "expires_at", "created_at", "accepted_at"}).
				AddRow(invID, orgID, strPtr("existing@example.com"), nil, models.InvitationAcceptanceMethodEither, "member", uuid.New(), "valid", "pending", time.Now().Add(time.Hour), time.Now(), nil),
		)
	// acceptValidatedInvitation opens its own tx for the writes.
	mock.ExpectBegin()
	mock.ExpectExec("UPDATE invitations SET status = 'accepted'").
		WithArgs(pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectQuery("INSERT INTO organization_memberships").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"role"}).AddRow("member"))
	mock.ExpectExec(`UPDATE users SET last_org_id = @last_org_id WHERE id = @id`).
		WithArgs(&orgID, userID).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectCommit()

	cfg := &config.Config{}
	handler := NewAuthHandler(cfg, mock, db.NewUserStore(mock), nil, db.NewInvitationStore(mock), db.NewOrganizationMembershipStore(mock))
	inv, _, invErr, err := handler.claimInvitationForExistingUser(context.Background(), "valid", "existing@example.com", "", userID)
	require.NoError(t, err)
	require.Nil(t, invErr)
	require.NotNil(t, inv)
	require.Equal(t, invID, inv.ID)
	require.NoError(t, mock.ExpectationsWereMet())
}

// claimInvitationForExistingUser returns an invitationError (not a raw error)
// when the invitation token is for a different email, so callers can surface
// the correct HTTP status. The invitation pointer is still returned so the
// caller can audit the failed claim against the inviting org.
func TestAuthHandler_ClaimInvitationForExistingUser_WrongEmail(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	orgID := uuid.New()
	invID := uuid.New()

	// Validation runs on the pool; on mismatch the helper returns before
	// opening any tx, so no Begin/Rollback is expected.
	mock.ExpectQuery("SELECT .+ FROM invitations WHERE token").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{"id", "org_id", "email", "github_username", "acceptance_method", "role", "invited_by", "token", "status", "expires_at", "created_at", "accepted_at"}).
				AddRow(invID, orgID, strPtr("invitee@example.com"), nil, models.InvitationAcceptanceMethodEither, "member", uuid.New(), "valid", "pending", time.Now().Add(time.Hour), time.Now(), nil),
		)

	handler := NewAuthHandler(&config.Config{}, mock, db.NewUserStore(mock), nil, db.NewInvitationStore(mock), db.NewOrganizationMembershipStore(mock))
	inv, _, invErr, err := handler.claimInvitationForExistingUser(context.Background(), "valid", "other@example.com", "", uuid.New())
	require.NoError(t, err)
	require.NotNil(t, inv, "invitation pointer should be returned on validation error so caller can audit the failed claim")
	require.Equal(t, invID, inv.ID)
	require.Equal(t, orgID, inv.OrgID)
	require.NotNil(t, invErr)
	require.Equal(t, "INVITE_MISMATCH", invErr.code)
	require.NoError(t, mock.ExpectationsWereMet())
}

// readAndClearPendingInvitationCookie returns the cookie value and emits a
// Set-Cookie header that clears it so a stale token doesn't leak into a later
// flow.
func TestReadAndClearPendingInvitationCookie(t *testing.T) {
	t.Parallel()

	t.Run("present returns value and clears", func(t *testing.T) {
		t.Parallel()
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		req.AddCookie(&http.Cookie{Name: "pending_invitation", Value: "tok-123"})
		w := httptest.NewRecorder()

		val := readAndClearPendingInvitationCookie(w, req)
		require.Equal(t, "tok-123", val)

		var cleared bool
		for _, c := range w.Result().Cookies() {
			if c.Name == "pending_invitation" {
				cleared = true
				require.Equal(t, "", c.Value)
				require.Equal(t, -1, c.MaxAge)
			}
		}
		require.True(t, cleared)
	})

	t.Run("absent returns empty without setting cookie", func(t *testing.T) {
		t.Parallel()
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		w := httptest.NewRecorder()

		val := readAndClearPendingInvitationCookie(w, req)
		require.Equal(t, "", val)
		require.Empty(t, w.Result().Cookies())
	})
}

// ClaimInvitation handler rejects requests without an authenticated user.
func TestAuthHandler_ClaimInvitation_Unauthenticated(t *testing.T) {
	t.Parallel()

	handler := NewAuthHandler(&config.Config{}, nil, nil, nil, nil, nil)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/claim-invitation", bytes.NewReader([]byte(`{"token":"x"}`)))
	w := httptest.NewRecorder()

	handler.ClaimInvitation(w, req)
	require.Equal(t, http.StatusUnauthorized, w.Code)
	require.Contains(t, w.Body.String(), "UNAUTHORIZED")
}

// ClaimInvitation handler validates that a token is present in the body.
func TestAuthHandler_ClaimInvitation_MissingToken(t *testing.T) {
	t.Parallel()

	handler := NewAuthHandler(&config.Config{}, nil, nil, nil, nil, nil)
	user := &models.User{ID: uuid.New(), Email: "u@example.com"}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/claim-invitation", bytes.NewReader([]byte(`{"token":""}`)))
	req = req.WithContext(middleware.WithUser(req.Context(), user))
	w := httptest.NewRecorder()

	handler.ClaimInvitation(w, req)
	require.Equal(t, http.StatusBadRequest, w.Code)
	require.Contains(t, w.Body.String(), "INVALID_BODY")
}

// ClaimInvitation handler end-to-end happy path: validates invitation,
// accepts it, and grants a membership inside the same tx.
func TestAuthHandler_ClaimInvitation_Success(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	userID := uuid.New()
	invOrgID := uuid.New()
	invID := uuid.New()
	ghLogin := "octocat"

	mock.ExpectQuery("SELECT .+ FROM invitations WHERE token").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{"id", "org_id", "email", "github_username", "acceptance_method", "role", "invited_by", "token", "status", "expires_at", "created_at", "accepted_at"}).
				AddRow(invID, invOrgID, strPtr("u@example.com"), nil, models.InvitationAcceptanceMethodEither, "member", uuid.New(), "valid", "pending", time.Now().Add(time.Hour), time.Now(), nil),
		)
	mock.ExpectBegin()
	mock.ExpectExec("UPDATE invitations SET status = 'accepted'").
		WithArgs(pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectQuery("INSERT INTO organization_memberships").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"role"}).AddRow("member"))
	mock.ExpectExec(`UPDATE users SET last_org_id = @last_org_id WHERE id = @id`).
		WithArgs(&invOrgID, userID).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectCommit()

	cfg := &config.Config{}
	handler := NewAuthHandler(cfg, mock, db.NewUserStore(mock), nil, db.NewInvitationStore(mock), db.NewOrganizationMembershipStore(mock))
	user := &models.User{ID: userID, Email: "u@example.com", GitHubLogin: &ghLogin}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/claim-invitation", bytes.NewReader([]byte(`{"token":"valid"}`)))
	req = req.WithContext(middleware.WithUser(req.Context(), user))
	w := httptest.NewRecorder()

	handler.ClaimInvitation(w, req)
	require.Equal(t, http.StatusOK, w.Code)
	require.Contains(t, w.Body.String(), invOrgID.String())
	require.NoError(t, mock.ExpectationsWereMet())
}

// ClaimInvitation surfaces invitationError status/code (e.g. email mismatch)
// rather than collapsing them into a generic 500.
func TestAuthHandler_ClaimInvitation_EmailMismatch(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	invOrgID := uuid.New()
	invID := uuid.New()

	mock.ExpectQuery("SELECT .+ FROM invitations WHERE token").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{"id", "org_id", "email", "github_username", "acceptance_method", "role", "invited_by", "token", "status", "expires_at", "created_at", "accepted_at"}).
				AddRow(invID, invOrgID, strPtr("someone-else@example.com"), nil, models.InvitationAcceptanceMethodEither, "member", uuid.New(), "valid", "pending", time.Now().Add(time.Hour), time.Now(), nil),
		)

	handler := NewAuthHandler(&config.Config{}, mock, db.NewUserStore(mock), nil, db.NewInvitationStore(mock), db.NewOrganizationMembershipStore(mock))
	user := &models.User{ID: uuid.New(), Email: "mismatch@example.com"}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/claim-invitation", bytes.NewReader([]byte(`{"token":"valid"}`)))
	req = req.WithContext(middleware.WithUser(req.Context(), user))
	w := httptest.NewRecorder()

	handler.ClaimInvitation(w, req)
	require.Contains(t, w.Body.String(), "INVITE_MISMATCH")
	require.NoError(t, mock.ExpectationsWereMet())
}

// ClaimInvitation with an unknown token returns the expected 404 response
// and skips the audit emitter path: emitInvitationClaimFailed requires an
// invitation row to anchor the audit entry to, and a mismatched token never
// produces one. The mock here deliberately omits any audit INSERT
// expectation — if ClaimInvitation tries to emit an audit row for a nil
// invitation, pgxmock will fail the test.
func TestAuthHandler_ClaimInvitation_UnknownTokenSkipsAudit(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	mock.ExpectQuery("SELECT .+ FROM invitations WHERE token").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "org_id", "email", "github_username", "acceptance_method", "role", "invited_by", "token", "status", "expires_at", "created_at", "accepted_at"}))

	handler := NewAuthHandler(&config.Config{}, mock, db.NewUserStore(mock), nil, db.NewInvitationStore(mock), db.NewOrganizationMembershipStore(mock))
	user := &models.User{ID: uuid.New(), Email: "u@example.com"}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/claim-invitation", bytes.NewReader([]byte(`{"token":"enumeration-attempt-abc123"}`)))
	req = req.WithContext(middleware.WithUser(req.Context(), user))
	w := httptest.NewRecorder()

	handler.ClaimInvitation(w, req)
	require.Equal(t, http.StatusBadRequest, w.Code)
	require.Contains(t, w.Body.String(), "INVITE_NOT_FOUND")
	require.NoError(t, mock.ExpectationsWereMet())
}

// ClaimInvitation emits a team.invitation.accepted audit row on success so the
// org-switcher join is visible in audit feeds alongside directly-initiated
// invitations. Wires a real AuditEmitter and asserts the audit_logs INSERT.
func TestAuthHandler_ClaimInvitation_EmitsAcceptedAudit(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	userID := uuid.New()
	invOrgID := uuid.New()
	invID := uuid.New()

	mock.ExpectQuery("SELECT .+ FROM invitations WHERE token").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{"id", "org_id", "email", "github_username", "acceptance_method", "role", "invited_by", "token", "status", "expires_at", "created_at", "accepted_at"}).
				AddRow(invID, invOrgID, strPtr("u@example.com"), nil, models.InvitationAcceptanceMethodEither, "member", uuid.New(), "valid", "pending", time.Now().Add(time.Hour), time.Now(), nil),
		)
	mock.ExpectBegin()
	mock.ExpectExec("UPDATE invitations SET status = 'accepted'").
		WithArgs(pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectQuery("INSERT INTO organization_memberships").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"role"}).AddRow("member"))
	mock.ExpectExec(`UPDATE users SET last_org_id = @last_org_id WHERE id = @id`).
		WithArgs(&invOrgID, userID).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectCommit()
	// Audit emitter runs post-commit; this is the row `emitInvitationAccepted`
	// writes into audit_logs. AuditLogStore.Create binds 13 named args, which
	// pgxmock sees as 13 positional args — match each with AnyArg().
	mock.ExpectQuery("INSERT INTO audit_logs").
		WithArgs(
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(),
		).
		WillReturnRows(pgxmock.NewRows([]string{"id", "created_at"}).AddRow(int64(1), time.Now()))

	handler := NewAuthHandler(&config.Config{}, mock, db.NewUserStore(mock), nil, db.NewInvitationStore(mock), db.NewOrganizationMembershipStore(mock))
	handler.SetAuditEmitter(db.NewAuditEmitter(db.NewAuditLogStore(mock), zerolog.Nop()))
	user := &models.User{ID: userID, Email: "u@example.com"}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/claim-invitation", bytes.NewReader([]byte(`{"token":"valid"}`)))
	req = req.WithContext(middleware.WithUser(req.Context(), user))
	w := httptest.NewRecorder()

	handler.ClaimInvitation(w, req)
	require.Equal(t, http.StatusOK, w.Code)
	require.NoError(t, mock.ExpectationsWereMet())
}

// ClaimInvitation emits a team.invitation.claim_failed audit row when the
// invitation was found but accept-in-tx returns ErrNoRows (i.e. the invitation
// was revoked or accepted by another flow between SELECT and UPDATE). The
// invitation row is non-nil so the audit path — not the unknown-token log
// path — should fire.
func TestAuthHandler_ClaimInvitation_EmitsFailedAudit(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	userID := uuid.New()
	invOrgID := uuid.New()
	invID := uuid.New()

	mock.ExpectQuery("SELECT .+ FROM invitations WHERE token").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{"id", "org_id", "email", "github_username", "acceptance_method", "role", "invited_by", "token", "status", "expires_at", "created_at", "accepted_at"}).
				AddRow(invID, invOrgID, strPtr("u@example.com"), nil, models.InvitationAcceptanceMethodEither, "member", uuid.New(), "valid", "pending", time.Now().Add(time.Hour), time.Now(), nil),
		)
	mock.ExpectBegin()
	// Accept returns ErrNoRows → invitationError{INVITE_INVALID, 410}.
	mock.ExpectExec("UPDATE invitations SET status = 'accepted'").
		WithArgs(pgxmock.AnyArg()).
		WillReturnError(pgx.ErrNoRows)
	mock.ExpectRollback()
	// Handler runs emitInvitationClaimFailed with the loaded invitation, so
	// an audit row with ResourceID=invID is written.
	mock.ExpectQuery("INSERT INTO audit_logs").
		WithArgs(
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(),
		).
		WillReturnRows(pgxmock.NewRows([]string{"id", "created_at"}).AddRow(int64(1), time.Now()))

	handler := NewAuthHandler(&config.Config{}, mock, db.NewUserStore(mock), nil, db.NewInvitationStore(mock), db.NewOrganizationMembershipStore(mock))
	handler.SetAuditEmitter(db.NewAuditEmitter(db.NewAuditLogStore(mock), zerolog.Nop()))
	user := &models.User{ID: userID, Email: "u@example.com"}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/claim-invitation", bytes.NewReader([]byte(`{"token":"valid"}`)))
	req = req.WithContext(middleware.WithUser(req.Context(), user))
	w := httptest.NewRecorder()

	handler.ClaimInvitation(w, req)
	require.Equal(t, http.StatusGone, w.Code)
	require.Contains(t, w.Body.String(), "INVITE_INVALID")
	require.NoError(t, mock.ExpectationsWereMet())
}

// createSignupOrg validates its inputs rather than silently accepting nil
// dependencies — those are programmer errors, not recoverable states.
func TestAuthHandler_CreateSignupOrg_InputValidation(t *testing.T) {
	t.Parallel()

	mock, mErr := pgxmock.NewPool()
	require.NoError(t, mErr)
	defer mock.Close()

	t.Run("nil pool", func(t *testing.T) {
		t.Parallel()
		h := &AuthHandler{}
		_, err := h.createSignupOrg(context.Background(), "Acme", &models.User{}, func(context.Context, *db.UserStore, *models.User) error { return nil })
		require.Error(t, err)
		require.Contains(t, err.Error(), "pool")
	})

	t.Run("nil user", func(t *testing.T) {
		t.Parallel()
		h := &AuthHandler{pool: mock}
		_, err := h.createSignupOrg(context.Background(), "Acme", nil, func(context.Context, *db.UserStore, *models.User) error { return nil })
		require.Error(t, err)
		require.Contains(t, err.Error(), "user")
	})

	t.Run("nil create callback", func(t *testing.T) {
		t.Parallel()
		h := &AuthHandler{pool: mock}
		_, err := h.createSignupOrg(context.Background(), "Acme", &models.User{}, nil)
		require.Error(t, err)
		require.Contains(t, err.Error(), "createUser")
	})
}

// createSignupOrg surfaces DB errors from each step so a failing commit or
// rollback does not silently leave the product in an orphan state.
func TestAuthHandler_CreateSignupOrg_DBErrors(t *testing.T) {
	t.Parallel()

	newHandler := func(mock pgxmock.PgxPoolIface) *AuthHandler {
		return NewAuthHandler(&config.Config{}, mock, db.NewUserStore(mock), nil, db.NewInvitationStore(mock), db.NewOrganizationMembershipStore(mock))
	}

	t.Run("begin fails", func(t *testing.T) {
		t.Parallel()
		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()
		mock.ExpectBegin().WillReturnError(errors.New("begin"))

		_, err = newHandler(mock).createSignupOrg(context.Background(), "Acme", &models.User{}, func(context.Context, *db.UserStore, *models.User) error { return nil })
		require.Error(t, err)
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("org create fails", func(t *testing.T) {
		t.Parallel()
		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()
		mock.ExpectBegin()
		mock.ExpectQuery("INSERT INTO organizations").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnError(errors.New("org"))
		mock.ExpectRollback()

		_, err = newHandler(mock).createSignupOrg(context.Background(), "Acme", &models.User{}, func(context.Context, *db.UserStore, *models.User) error { return nil })
		require.Error(t, err)
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("createUser callback fails", func(t *testing.T) {
		t.Parallel()
		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()
		mock.ExpectBegin()
		mock.ExpectQuery("INSERT INTO organizations").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnRows(pgxmock.NewRows([]string{"id", "created_at", "updated_at"}).AddRow(uuid.New(), time.Now(), time.Now()))
		mock.ExpectRollback()

		_, err = newHandler(mock).createSignupOrg(context.Background(), "Acme", &models.User{}, func(context.Context, *db.UserStore, *models.User) error {
			return errors.New("user create")
		})
		require.Error(t, err)
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("membership upsert fails", func(t *testing.T) {
		t.Parallel()
		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()
		userID := uuid.New()
		mock.ExpectBegin()
		mock.ExpectQuery("INSERT INTO organizations").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnRows(pgxmock.NewRows([]string{"id", "created_at", "updated_at"}).AddRow(uuid.New(), time.Now(), time.Now()))
		mock.ExpectExec("INSERT INTO organization_memberships").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnError(errors.New("upsert"))
		mock.ExpectRollback()

		_, err = newHandler(mock).createSignupOrg(context.Background(), "Acme", &models.User{ID: userID}, func(_ context.Context, _ *db.UserStore, u *models.User) error {
			u.ID = userID
			return nil
		})
		require.Error(t, err)
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("session persist fails", func(t *testing.T) {
		t.Parallel()
		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()
		userID := uuid.New()
		mock.ExpectBegin()
		mock.ExpectQuery("INSERT INTO organizations").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnRows(pgxmock.NewRows([]string{"id", "created_at", "updated_at"}).AddRow(uuid.New(), time.Now(), time.Now()))
		mock.ExpectExec("INSERT INTO organization_memberships").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnResult(pgxmock.NewResult("INSERT", 1))
		expectUserLastOrgLookup(mock, userID, nil)
		mock.ExpectQuery("INSERT INTO auth_sessions").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnError(errors.New("session"))
		mock.ExpectRollback()

		_, err = newHandler(mock).createSignupOrg(context.Background(), "Acme", &models.User{ID: userID}, func(_ context.Context, _ *db.UserStore, u *models.User) error {
			u.ID = userID
			return nil
		})
		require.Error(t, err)
		require.Contains(t, err.Error(), "create signup session")
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("commit fails", func(t *testing.T) {
		t.Parallel()
		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()
		userID := uuid.New()
		mock.ExpectBegin()
		mock.ExpectQuery("INSERT INTO organizations").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnRows(pgxmock.NewRows([]string{"id", "created_at", "updated_at"}).AddRow(uuid.New(), time.Now(), time.Now()))
		mock.ExpectExec("INSERT INTO organization_memberships").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnResult(pgxmock.NewResult("INSERT", 1))
		expectUserLastOrgLookup(mock, userID, nil)
		mock.ExpectQuery("INSERT INTO auth_sessions").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnRows(pgxmock.NewRows([]string{"id", "created_at"}).AddRow(uuid.New(), time.Now()))
		mock.ExpectCommit().WillReturnError(errors.New("commit"))

		_, err = newHandler(mock).createSignupOrg(context.Background(), "Acme", &models.User{ID: userID}, func(_ context.Context, _ *db.UserStore, u *models.User) error {
			u.ID = userID
			return nil
		})
		require.Error(t, err)
		require.NoError(t, mock.ExpectationsWereMet())
	})
}

// claimInvitationForExistingUser propagates DB errors from the Accept step,
// the membership upsert, and the final commit — each of those failures must
// abort the claim, not be silently swallowed.
func TestAuthHandler_ClaimInvitationForExistingUser_DBErrors(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	invID := uuid.New()

	// Validation read happens on the pool before any tx is opened; the tx
	// only fires after validateInvitation returns success.
	setupSelectThenBegin := func(mock pgxmock.PgxPoolIface) {
		mock.ExpectQuery("SELECT .+ FROM invitations WHERE token").
			WithArgs(pgxmock.AnyArg()).
			WillReturnRows(
				pgxmock.NewRows([]string{"id", "org_id", "email", "github_username", "acceptance_method", "role", "invited_by", "token", "status", "expires_at", "created_at", "accepted_at"}).
					AddRow(invID, orgID, strPtr("u@example.com"), nil, models.InvitationAcceptanceMethodEither, "member", uuid.New(), "valid", "pending", time.Now().Add(time.Hour), time.Now(), nil),
			)
		mock.ExpectBegin()
	}

	newHandler := func(mock pgxmock.PgxPoolIface) *AuthHandler {
		cfg := &config.Config{}
		return NewAuthHandler(cfg, mock, db.NewUserStore(mock), nil, db.NewInvitationStore(mock), db.NewOrganizationMembershipStore(mock))
	}

	t.Run("accept hard-error", func(t *testing.T) {
		t.Parallel()
		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()
		setupSelectThenBegin(mock)
		mock.ExpectExec("UPDATE invitations SET status = 'accepted'").
			WithArgs(pgxmock.AnyArg()).
			WillReturnError(errors.New("accept"))
		mock.ExpectRollback()

		_, _, _, err = newHandler(mock).claimInvitationForExistingUser(context.Background(), "valid", "u@example.com", "", uuid.New())
		require.Error(t, err)
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("membership upsert fails", func(t *testing.T) {
		t.Parallel()
		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()
		setupSelectThenBegin(mock)
		mock.ExpectExec("UPDATE invitations SET status = 'accepted'").
			WithArgs(pgxmock.AnyArg()).
			WillReturnResult(pgxmock.NewResult("UPDATE", 1))
		mock.ExpectQuery("INSERT INTO organization_memberships").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnError(errors.New("upsert"))
		mock.ExpectRollback()

		_, _, _, err = newHandler(mock).claimInvitationForExistingUser(context.Background(), "valid", "u@example.com", "", uuid.New())
		require.Error(t, err)
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("commit fails", func(t *testing.T) {
		t.Parallel()
		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()
		setupSelectThenBegin(mock)
		mock.ExpectExec("UPDATE invitations SET status = 'accepted'").
			WithArgs(pgxmock.AnyArg()).
			WillReturnResult(pgxmock.NewResult("UPDATE", 1))
		mock.ExpectQuery("INSERT INTO organization_memberships").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnRows(pgxmock.NewRows([]string{"role"}).AddRow("member"))
		mock.ExpectExec(`UPDATE users SET last_org_id = @last_org_id WHERE id = @id`).
			WithArgs(&orgID, pgxmock.AnyArg()).
			WillReturnResult(pgxmock.NewResult("UPDATE", 1))
		mock.ExpectCommit().WillReturnError(errors.New("commit"))

		_, _, _, err = newHandler(mock).claimInvitationForExistingUser(context.Background(), "valid", "u@example.com", "", uuid.New())
		require.Error(t, err)
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("last org update fails", func(t *testing.T) {
		t.Parallel()
		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()
		setupSelectThenBegin(mock)
		mock.ExpectExec("UPDATE invitations SET status = 'accepted'").
			WithArgs(pgxmock.AnyArg()).
			WillReturnResult(pgxmock.NewResult("UPDATE", 1))
		mock.ExpectQuery("INSERT INTO organization_memberships").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnRows(pgxmock.NewRows([]string{"role"}).AddRow("member"))
		mock.ExpectExec(`UPDATE users SET last_org_id = @last_org_id WHERE id = @id`).
			WithArgs(&orgID, pgxmock.AnyArg()).
			WillReturnError(errors.New("update last org"))
		mock.ExpectRollback()

		_, _, _, err = newHandler(mock).claimInvitationForExistingUser(context.Background(), "valid", "u@example.com", "", uuid.New())
		require.Error(t, err)
		require.NoError(t, mock.ExpectationsWereMet())
	})
}

// acceptInvitationAndUpsertUser rejects a nil pool, nil user, or nil upsert
// callback as programmer errors rather than calling NPE at runtime.
func TestAuthHandler_AcceptInvitationAndUpsertUser_InputValidation(t *testing.T) {
	t.Parallel()

	t.Run("nil pool", func(t *testing.T) {
		t.Parallel()
		h := &AuthHandler{}
		_, _, _, err := h.acceptInvitationAndUpsertUser(context.Background(), uuid.New(), &models.User{}, func(context.Context, *db.UserStore, *models.User) error { return nil })
		require.Error(t, err)
	})

	t.Run("nil user", func(t *testing.T) {
		t.Parallel()
		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()
		h := &AuthHandler{pool: mock}
		_, _, _, err = h.acceptInvitationAndUpsertUser(context.Background(), uuid.New(), nil, func(context.Context, *db.UserStore, *models.User) error { return nil })
		require.Error(t, err)
	})

	t.Run("nil upsert", func(t *testing.T) {
		t.Parallel()
		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()
		h := &AuthHandler{pool: mock}
		_, _, _, err = h.acceptInvitationAndUpsertUser(context.Background(), uuid.New(), &models.User{}, nil)
		require.Error(t, err)
	})
}

// acceptInvitationAndUpsertUser propagates begin/accept/upsert/membership/
// commit failures so none of those leave the DB in a half-applied state.
func TestAuthHandler_AcceptInvitationAndUpsertUser_DBErrors(t *testing.T) {
	t.Parallel()

	newHandler := func(mock pgxmock.PgxPoolIface) *AuthHandler {
		return NewAuthHandler(&config.Config{}, mock, db.NewUserStore(mock), nil, db.NewInvitationStore(mock), db.NewOrganizationMembershipStore(mock))
	}

	t.Run("begin fails", func(t *testing.T) {
		t.Parallel()
		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()
		mock.ExpectBegin().WillReturnError(errors.New("begin"))

		_, _, _, err = newHandler(mock).acceptInvitationAndUpsertUser(context.Background(), uuid.New(), &models.User{}, func(context.Context, *db.UserStore, *models.User) error { return nil })
		require.Error(t, err)
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("accept hard-error", func(t *testing.T) {
		t.Parallel()
		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()
		mock.ExpectBegin()
		mock.ExpectExec("UPDATE invitations SET status = 'accepted'").
			WithArgs(pgxmock.AnyArg()).
			WillReturnError(errors.New("accept"))
		mock.ExpectRollback()

		_, _, _, err = newHandler(mock).acceptInvitationAndUpsertUser(context.Background(), uuid.New(), &models.User{}, func(context.Context, *db.UserStore, *models.User) error { return nil })
		require.Error(t, err)
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("membership upsert fails", func(t *testing.T) {
		t.Parallel()
		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()
		mock.ExpectBegin()
		mock.ExpectExec("UPDATE invitations SET status = 'accepted'").
			WithArgs(pgxmock.AnyArg()).
			WillReturnResult(pgxmock.NewResult("UPDATE", 1))
		mock.ExpectQuery("INSERT INTO organization_memberships").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnError(errors.New("upsert"))
		mock.ExpectRollback()

		_, _, _, err = newHandler(mock).acceptInvitationAndUpsertUser(context.Background(), uuid.New(), &models.User{ID: uuid.New(), OrgID: uuid.New(), Role: "member"}, func(context.Context, *db.UserStore, *models.User) error { return nil })
		require.Error(t, err)
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("commit fails", func(t *testing.T) {
		t.Parallel()
		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()
		mock.ExpectBegin()
		mock.ExpectExec("UPDATE invitations SET status = 'accepted'").
			WithArgs(pgxmock.AnyArg()).
			WillReturnResult(pgxmock.NewResult("UPDATE", 1))
		mock.ExpectQuery("INSERT INTO organization_memberships").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnRows(pgxmock.NewRows([]string{"role"}).AddRow("member"))
		userID := uuid.New()
		expectUserLastOrgLookup(mock, userID, nil)
		mock.ExpectQuery("INSERT INTO auth_sessions").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnRows(pgxmock.NewRows([]string{"id", "created_at"}).AddRow(uuid.New(), time.Now()))
		mock.ExpectCommit().WillReturnError(errors.New("commit"))

		_, _, _, err = newHandler(mock).acceptInvitationAndUpsertUser(context.Background(), uuid.New(), &models.User{ID: userID, OrgID: uuid.New(), Role: "member"}, func(context.Context, *db.UserStore, *models.User) error { return nil })
		require.Error(t, err)
		require.NoError(t, mock.ExpectationsWereMet())
	})
}

// createInvitedUserWithPassword rejects a nil pool or nil user store before
// it would panic trying to open a transaction.
func TestAuthHandler_CreateInvitedUserWithPassword_InputValidation(t *testing.T) {
	t.Parallel()

	t.Run("nil pool", func(t *testing.T) {
		t.Parallel()
		h := &AuthHandler{}
		_, _, _, err := h.createInvitedUserWithPassword(context.Background(), "t", "e@example.com", "n", "h")
		require.Error(t, err)
	})

	t.Run("nil user store", func(t *testing.T) {
		t.Parallel()
		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()
		h := &AuthHandler{pool: mock}
		_, _, _, err = h.createInvitedUserWithPassword(context.Background(), "t", "e@example.com", "n", "h")
		require.Error(t, err)
	})
}

// mockInvitationLookupStore implements invitationLookupStore for tests.
type mockInvitationLookupStore struct {
	getByTokenFn func(ctx context.Context, token string) (models.Invitation, error)
}

func (m *mockInvitationLookupStore) GetByToken(ctx context.Context, token string) (models.Invitation, error) {
	return m.getByTokenFn(ctx, token)
}

func TestAuthHandler_createSessionAndRespond_SetsCookiesWithMiddlewareConstants(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	user := &models.User{ID: uuid.New(), OrgID: uuid.New(), Email: "u@example.com"}
	expectUserLastOrgLookup(mock, user.ID, nil)
	mock.ExpectQuery("INSERT INTO auth_sessions").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{"id", "created_at"}).
				AddRow(uuid.New(), time.Now()),
		)

	handler := NewAuthHandler(
		&config.Config{CSRFSigningKey: "test-signing-key-that-is-long-enough-for-hmac"},
		mock,
		nil,
		db.NewAuthSessionStore(mock),
		nil,
		nil,
	)

	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", nil)
	req.TLS = nil
	w := httptest.NewRecorder()

	handler.createSessionAndRespond(w, req, user)

	require.Equal(t, http.StatusOK, w.Code)

	var foundSession bool
	for _, c := range w.Result().Cookies() {
		if c.Name == middleware.SessionCookieName {
			foundSession = true
			require.NotEmpty(t, c.Value)
			require.Equal(t, int(middleware.SessionTTL.Seconds()), c.MaxAge)
			require.True(t, c.HttpOnly)
			require.Equal(t, http.SameSiteLaxMode, c.SameSite)
			require.False(t, c.Secure, "plain HTTP request should not set Secure")
		}
	}
	require.True(t, foundSession, "session cookie should be set")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAuthHandler_createSessionAndRedirect_SetsCookiesAndRedirects(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	user := &models.User{ID: uuid.New(), OrgID: uuid.New(), Email: "u@example.com"}
	expectUserLastOrgLookup(mock, user.ID, nil)
	mock.ExpectQuery("INSERT INTO auth_sessions").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{"id", "created_at"}).
				AddRow(uuid.New(), time.Now()),
		)

	handler := NewAuthHandler(
		&config.Config{
			CSRFSigningKey: "test-signing-key-that-is-long-enough-for-hmac",
			FrontendURL:    "https://app.example.com",
		},
		mock,
		nil,
		db.NewAuthSessionStore(mock),
		nil,
		nil,
	)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/github/callback", nil)
	req.Header.Set("X-Forwarded-Proto", "https")
	w := httptest.NewRecorder()

	handler.createSessionAndRedirect(w, req, user)

	require.Equal(t, http.StatusTemporaryRedirect, w.Code)

	var foundSession bool
	for _, c := range w.Result().Cookies() {
		if c.Name == middleware.SessionCookieName {
			foundSession = true
			require.NotEmpty(t, c.Value)
			require.Equal(t, int(middleware.SessionTTL.Seconds()), c.MaxAge)
			require.True(t, c.HttpOnly)
			require.Equal(t, http.SameSiteLaxMode, c.SameSite)
			require.True(t, c.Secure, "X-Forwarded-Proto=https should set Secure")
		}
	}
	require.True(t, foundSession, "session cookie should be set")
	require.NoError(t, mock.ExpectationsWereMet())
}

// claimPendingInvitationForExistingUser is a best-effort helper: an empty
// token short-circuits without touching the DB, a claim failure is logged
// but does not bubble an error. These tests drive each branch.
func TestAuthHandler_ClaimPendingInvitationForExistingUser(t *testing.T) {
	t.Parallel()

	t.Run("empty token is a no-op", func(t *testing.T) {
		t.Parallel()
		handler := &AuthHandler{}
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		// no pool configured — would panic if we tried to begin a tx
		handler.claimPendingInvitationForExistingUser(req, "", "u@example.com", "", uuid.New())
	})

	t.Run("successful claim", func(t *testing.T) {
		t.Parallel()
		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		userID := uuid.New()
		orgID := uuid.New()
		invID := uuid.New()

		mock.ExpectQuery("SELECT .+ FROM invitations WHERE token").
			WithArgs(pgxmock.AnyArg()).
			WillReturnRows(
				pgxmock.NewRows([]string{"id", "org_id", "email", "github_username", "acceptance_method", "role", "invited_by", "token", "status", "expires_at", "created_at", "accepted_at"}).
					AddRow(invID, orgID, strPtr("u@example.com"), nil, models.InvitationAcceptanceMethodEither, "member", uuid.New(), "valid", "pending", time.Now().Add(time.Hour), time.Now(), nil),
			)
		mock.ExpectBegin()
		mock.ExpectExec("UPDATE invitations SET status = 'accepted'").
			WithArgs(pgxmock.AnyArg()).
			WillReturnResult(pgxmock.NewResult("UPDATE", 1))
		mock.ExpectQuery("INSERT INTO organization_memberships").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnRows(pgxmock.NewRows([]string{"role"}).AddRow("member"))
		mock.ExpectExec(`UPDATE users SET last_org_id = @last_org_id WHERE id = @id`).
			WithArgs(&orgID, userID).
			WillReturnResult(pgxmock.NewResult("UPDATE", 1))
		mock.ExpectCommit()

		cfg := &config.Config{}
		handler := NewAuthHandler(cfg, mock, db.NewUserStore(mock), nil, db.NewInvitationStore(mock), db.NewOrganizationMembershipStore(mock))
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		handler.claimPendingInvitationForExistingUser(req, "valid", "u@example.com", "", userID)
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("invitation-error path is logged not returned", func(t *testing.T) {
		t.Parallel()
		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		invID := uuid.New()
		// Mismatch is detected before any tx is opened — no Begin/Rollback.
		mock.ExpectQuery("SELECT .+ FROM invitations WHERE token").
			WithArgs(pgxmock.AnyArg()).
			WillReturnRows(
				pgxmock.NewRows([]string{"id", "org_id", "email", "github_username", "acceptance_method", "role", "invited_by", "token", "status", "expires_at", "created_at", "accepted_at"}).
					AddRow(invID, uuid.New(), strPtr("invitee@example.com"), nil, models.InvitationAcceptanceMethodEither, "member", uuid.New(), "valid", "pending", time.Now().Add(time.Hour), time.Now(), nil),
			)

		handler := NewAuthHandler(&config.Config{}, mock, db.NewUserStore(mock), nil, db.NewInvitationStore(mock), db.NewOrganizationMembershipStore(mock))
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		handler.claimPendingInvitationForExistingUser(req, "valid", "other@example.com", "", uuid.New())
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("hard error is logged not returned", func(t *testing.T) {
		t.Parallel()
		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		// Validation succeeds (SELECT returns a valid pending invite for the
		// caller's email), then the accept tx fails to begin — that hard
		// error must not propagate out of the OAuth-driven best-effort path.
		invID := uuid.New()
		mock.ExpectQuery("SELECT .+ FROM invitations WHERE token").
			WithArgs(pgxmock.AnyArg()).
			WillReturnRows(
				pgxmock.NewRows([]string{"id", "org_id", "email", "github_username", "acceptance_method", "role", "invited_by", "token", "status", "expires_at", "created_at", "accepted_at"}).
					AddRow(invID, uuid.New(), strPtr("u@example.com"), nil, models.InvitationAcceptanceMethodEither, "member", uuid.New(), "valid", "pending", time.Now().Add(time.Hour), time.Now(), nil),
			)
		mock.ExpectBegin().WillReturnError(errors.New("db gone"))

		handler := NewAuthHandler(&config.Config{}, mock, db.NewUserStore(mock), nil, db.NewInvitationStore(mock), db.NewOrganizationMembershipStore(mock))
		req := httptest.NewRequest(http.MethodGet, "/", nil)
		handler.claimPendingInvitationForExistingUser(req, "valid", "u@example.com", "", uuid.New())
		require.NoError(t, mock.ExpectationsWereMet())
	})
}

// claimInvitationForExistingUser surfaces INVITE_INVALID when the accept
// step races with another caller that already consumed the invitation.
func TestAuthHandler_ClaimInvitationForExistingUser_AcceptRace(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	orgID := uuid.New()
	invID := uuid.New()

	mock.ExpectQuery("SELECT .+ FROM invitations WHERE token").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{"id", "org_id", "email", "github_username", "acceptance_method", "role", "invited_by", "token", "status", "expires_at", "created_at", "accepted_at"}).
				AddRow(invID, orgID, strPtr("u@example.com"), nil, models.InvitationAcceptanceMethodEither, "member", uuid.New(), "valid", "pending", time.Now().Add(time.Hour), time.Now(), nil),
		)
	mock.ExpectBegin()
	mock.ExpectExec("UPDATE invitations SET status = 'accepted'").
		WithArgs(pgxmock.AnyArg()).
		WillReturnError(pgx.ErrNoRows)
	mock.ExpectRollback()

	cfg := &config.Config{}
	handler := NewAuthHandler(cfg, mock, db.NewUserStore(mock), nil, db.NewInvitationStore(mock), db.NewOrganizationMembershipStore(mock))
	inv, _, invErr, err := handler.claimInvitationForExistingUser(context.Background(), "valid", "u@example.com", "", uuid.New())
	require.NoError(t, err)
	require.NotNil(t, inv, "invitation pointer is returned even on accept-race so caller can audit")
	require.Equal(t, invID, inv.ID)
	require.NotNil(t, invErr)
	require.Equal(t, http.StatusGone, invErr.status)
	require.Equal(t, "INVITE_INVALID", invErr.code)
	require.NoError(t, mock.ExpectationsWereMet())
}

// claimInvitationForExistingUser bubbles begin-transaction failures from
// the accept path. Validation runs on the pool first; the tx only opens
// after a successful validation, so this test seeds a valid invitation
// row before failing the Begin.
func TestAuthHandler_ClaimInvitationForExistingUser_BeginFails(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	invID := uuid.New()
	mock.ExpectQuery("SELECT .+ FROM invitations WHERE token").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{"id", "org_id", "email", "github_username", "acceptance_method", "role", "invited_by", "token", "status", "expires_at", "created_at", "accepted_at"}).
				AddRow(invID, uuid.New(), strPtr("u@example.com"), nil, models.InvitationAcceptanceMethodEither, "member", uuid.New(), "valid", "pending", time.Now().Add(time.Hour), time.Now(), nil),
		)
	mock.ExpectBegin().WillReturnError(errors.New("db down"))

	handler := NewAuthHandler(&config.Config{}, mock, db.NewUserStore(mock), nil, db.NewInvitationStore(mock), db.NewOrganizationMembershipStore(mock))
	_, _, _, err = handler.claimInvitationForExistingUser(context.Background(), "valid", "u@example.com", "", uuid.New())
	require.Error(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

// claimInvitationForExistingUser returns an error when pool is nil.
func TestAuthHandler_ClaimInvitationForExistingUser_NilPool(t *testing.T) {
	t.Parallel()

	handler := &AuthHandler{}
	_, _, _, err := handler.claimInvitationForExistingUser(context.Background(), "valid", "u@example.com", "", uuid.New())
	require.Error(t, err)
}

// When validateInvitationWithStore fails to load the invitation row at all
// (e.g. token not found), claimInvitationForExistingUser returns a nil
// invitation pointer via invitationOrNil so the caller does not emit a
// dangling audit event for a non-existent invite.
func TestAuthHandler_ClaimInvitationForExistingUser_TokenNotFound(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	// Token lookup misses entirely — validation returns INVITE_NOT_FOUND
	// without ever opening the accept tx, so no Begin/Rollback is expected.
	mock.ExpectQuery("SELECT .+ FROM invitations WHERE token").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "org_id", "email", "github_username", "acceptance_method", "role", "invited_by", "token", "status", "expires_at", "created_at", "accepted_at"}))

	handler := NewAuthHandler(&config.Config{}, mock, db.NewUserStore(mock), nil, db.NewInvitationStore(mock), db.NewOrganizationMembershipStore(mock))
	inv, _, invErr, err := handler.claimInvitationForExistingUser(context.Background(), "missing", "u@example.com", "", uuid.New())
	require.NoError(t, err)
	require.Nil(t, inv, "invitation pointer is nil when the row was never loaded")
	require.NotNil(t, invErr)
	require.Equal(t, "INVITE_NOT_FOUND", invErr.code)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestComputeNoreplyEmail(t *testing.T) {
	t.Parallel()

	require.Equal(t, "1234+alice@users.noreply.github.com", computeNoreplyEmail(1234, "alice"),
		"computeNoreplyEmail should produce the canonical user-id-prefixed form")
	require.Equal(t, "", computeNoreplyEmail(0, "alice"),
		"computeNoreplyEmail should refuse to make up an address without a real user id")
	require.Equal(t, "", computeNoreplyEmail(1234, ""),
		"computeNoreplyEmail should refuse to make up an address without a login")
}

// fetchGitHubNoreplyEmail picks a user's noreply address from /user/emails
// when present; otherwise it falls back to the canonical id+login form. The
// test patches the package-level http.DefaultClient via httptest only at the
// HTTP level — we point the client's transport at a captured server. We use
// a dedicated test that drives the helper through a wrapping HTTP server
// with the canonical default transport to avoid leaking state across tests.
func TestFetchGitHubNoreplyEmail(t *testing.T) {
	t.Parallel()

	t.Run("uses noreply entry from /user/emails", func(t *testing.T) {
		t.Parallel()
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			require.Equal(t, "/user/emails", r.URL.Path)
			require.Equal(t, "Bearer ghu_token", r.Header.Get("Authorization"))
			_, _ = w.Write([]byte(`[
				{"email":"alice@example.com","primary":true,"verified":true,"visibility":"private"},
				{"email":"42+alicehub@users.noreply.github.com","primary":false,"verified":true,"visibility":null}
			]`))
		}))
		defer server.Close()

		got := fetchGitHubNoreplyEmailFrom(context.Background(), server.Client(), server.URL+"/user/emails", "ghu_token", 42, "alicehub")
		require.Equal(t, "42+alicehub@users.noreply.github.com", got)
	})

	t.Run("falls back to canonical form when /user/emails has no noreply", func(t *testing.T) {
		t.Parallel()
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`[{"email":"alice@example.com","primary":true,"verified":true,"visibility":"public"}]`))
		}))
		defer server.Close()

		got := fetchGitHubNoreplyEmailFrom(context.Background(), server.Client(), server.URL+"/user/emails", "ghu_token", 42, "alicehub")
		require.Equal(t, "42+alicehub@users.noreply.github.com", got)
	})

	t.Run("falls back to canonical form on HTTP error", func(t *testing.T) {
		t.Parallel()
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusForbidden)
		}))
		defer server.Close()

		got := fetchGitHubNoreplyEmailFrom(context.Background(), server.Client(), server.URL+"/user/emails", "ghu_token", 42, "alicehub")
		require.Equal(t, "42+alicehub@users.noreply.github.com", got)
	})

	t.Run("prefers verified canonical form over deprecated unverified noreply", func(t *testing.T) {
		t.Parallel()
		// GitHub may return both the deprecated `{login}@noreply.github.com`
		// and the canonical `{id}+{login}@noreply.github.com`. The deprecated
		// form breaks if the user later renames; prefer the canonical row,
		// and only when it's verified.
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`[
				{"email":"alicehub@users.noreply.github.com","primary":false,"verified":true,"visibility":null},
				{"email":"42+alicehub@users.noreply.github.com","primary":false,"verified":true,"visibility":null}
			]`))
		}))
		defer server.Close()

		got := fetchGitHubNoreplyEmailFrom(context.Background(), server.Client(), server.URL+"/user/emails", "ghu_token", 42, "alicehub")
		require.Equal(t, "42+alicehub@users.noreply.github.com", got,
			"the canonical user-id-prefixed form must beat the deprecated login-only noreply")
	})

	t.Run("prefers any verified noreply over an unverified one", func(t *testing.T) {
		t.Parallel()
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			// Canonical row exists but is unverified; deprecated row is
			// verified. We pick the verified one rather than the canonical-
			// but-unverified row, because GitHub only attributes commits
			// authored with a *verified* address.
			_, _ = w.Write([]byte(`[
				{"email":"42+alicehub@users.noreply.github.com","primary":false,"verified":false,"visibility":null},
				{"email":"alicehub@users.noreply.github.com","primary":false,"verified":true,"visibility":null}
			]`))
		}))
		defer server.Close()

		got := fetchGitHubNoreplyEmailFrom(context.Background(), server.Client(), server.URL+"/user/emails", "ghu_token", 42, "alicehub")
		require.Equal(t, "alicehub@users.noreply.github.com", got)
	})

	t.Run("uses unverified noreply only as last resort before fallback", func(t *testing.T) {
		t.Parallel()
		// Some legacy GitHub accounts have never toggled the verified flag
		// on noreply rows. Accept the unverified entry in preference to
		// nothing rather than dropping back to the synthesized fallback —
		// at least it's still self-consistent with what GitHub returns
		// in /user/emails.
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`[
				{"email":"42+alicehub@users.noreply.github.com","primary":false,"verified":false,"visibility":null}
			]`))
		}))
		defer server.Close()

		got := fetchGitHubNoreplyEmailFrom(context.Background(), server.Client(), server.URL+"/user/emails", "ghu_token", 42, "alicehub")
		require.Equal(t, "42+alicehub@users.noreply.github.com", got)
	})

	t.Run("falls back when request construction fails", func(t *testing.T) {
		t.Parallel()

		got := fetchGitHubNoreplyEmailFrom(context.Background(), nil, "://bad", "ghu_token", 42, "alicehub")
		require.Equal(t, "42+alicehub@users.noreply.github.com", got, "invalid email probe URLs should fall back to the canonical synthesized address")
	})

	t.Run("falls back when transport fails", func(t *testing.T) {
		t.Parallel()

		// nil client → fetchGitHubNoreplyEmailFrom uses its own short-timeout
		// client. The unrouted target IP triggers a fast connection refusal
		// which the helper must convert into a fallback rather than a return
		// error.
		got := fetchGitHubNoreplyEmailFrom(context.Background(), nil, "http://127.0.0.1:1/user/emails", "ghu_token", 42, "alicehub")
		require.Equal(t, "42+alicehub@users.noreply.github.com", got, "transport failures should fall back to the canonical synthesized address")
	})

	t.Run("falls back when response JSON is malformed", func(t *testing.T) {
		t.Parallel()

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`{`))
		}))
		defer server.Close()

		got := fetchGitHubNoreplyEmailFrom(context.Background(), server.Client(), server.URL+"/user/emails", "ghu_token", 42, "alicehub")
		require.Equal(t, "42+alicehub@users.noreply.github.com", got, "decode failures should fall back to the canonical synthesized address")
	})

	t.Run("uses default short-timeout client when none is provided", func(t *testing.T) {
		t.Parallel()

		// Confirms the production wiring: when h.httpClient is nil, the
		// AuthHandler.fetchGitHubNoreplyEmail wrapper passes nil through
		// and the inner function still produces a valid response by
		// constructing its own short-timeout client. We exercise the
		// nil-client branch directly here.
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte(`[{"email":"42+alicehub@users.noreply.github.com","verified":true}]`))
		}))
		defer server.Close()

		got := fetchGitHubNoreplyEmailFrom(context.Background(), nil, server.URL+"/user/emails", "ghu_token", 42, "alicehub")
		require.Equal(t, "42+alicehub@users.noreply.github.com", got, "nil client should fall back to the helper's short-timeout client and still return the discovered noreply email")
	})

	t.Run("respects ctx cancellation", func(t *testing.T) {
		t.Parallel()

		// A cancelled context must short-circuit the request and produce
		// the fallback. Threading context through ensures a hung GitHub
		// call can't outlast the OAuth login it's serving.
		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		got := fetchGitHubNoreplyEmailFrom(ctx, nil, "https://api.github.com/user/emails", "ghu_token", 42, "alicehub")
		require.Equal(t, "42+alicehub@users.noreply.github.com", got, "cancelled context should produce the fallback rather than the network response")
	})
}

func TestAuthHandler_FetchGitHubEmails_DelegatesToInjectedURL(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/user/emails", r.URL.Path, "AuthHandler should probe the API base + /user/emails")
		_, _ = w.Write([]byte(`[{"email":"42+alicehub@users.noreply.github.com","verified":true},{"email":"alice@assembledhq.com","primary":true,"verified":true}]`))
	}))
	defer server.Close()

	handler := NewAuthHandler(&config.Config{}, nil, nil, nil, nil, nil)
	handler.SetGitHubURLsForTest(server.URL, "", server.Client())

	emails := handler.fetchGitHubEmails(context.Background(), "ghu_token")
	got := selectGitHubNoreplyEmail(emails, 42, "alicehub")
	require.Equal(t, "42+alicehub@users.noreply.github.com", got, "noreply selection should use the injected GitHub API base + httptest server")
	require.True(t, gitHubEmailVerified(emails, "alice@assembledhq.com"), "verified flag should be readable from the same fetch")
	require.True(t, gitHubEmailVerified(emails, "ALICE@assembledhq.com"), "verification match is case-insensitive")
	require.False(t, gitHubEmailVerified(emails, "other@assembledhq.com"), "addresses absent from /user/emails are not verified")
}

// TestAuthHandler_Callback_DomainAutoJoin pins the domain-capture signup
// path: a brand-new GitHub user whose GitHub-verified email is on a domain
// some org has DNS-verified with auto-join enabled must be created directly
// as a member of that org — no fresh single-user org.
func TestAuthHandler_Callback_DomainAutoJoin(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create pgxmock pool")
	defer mock.Close()

	now := time.Now()
	userID := uuid.New()
	orgID := uuid.New()
	userColumns := []string{"id", "org_id", "email", "name", "role", "github_id", "github_login", "github_noreply_email", "avatar_url", "password_hash", "google_id", "created_at"}

	// Brand-new user: no GitHub-id match, no email match.
	mock.ExpectQuery("SELECT .* FROM users WHERE github_id").
		WithArgs(int64(42)).
		WillReturnRows(pgxmock.NewRows(userColumns))
	mock.ExpectQuery(`(?s)SELECT .+ FROM users WHERE LOWER\(email\) = LOWER\(@email\)`).
		WithArgs("alice@assembledhq.com").
		WillReturnRows(pgxmock.NewRows(userColumns))

	// Domain lookup finds the verified auto-join org. Queried twice: once
	// by selectGitHubAutoJoinEmail (choosing which address to capture
	// with) and once inside tryDomainAutoJoin (resolving the target org).
	for range 2 {
		mock.ExpectQuery("SELECT d.org_id, o.name AS org_name, d.domain").
			WithArgs("assembledhq.com").
			WillReturnRows(pgxmock.NewRows([]string{"org_id", "org_name", "domain"}).
				AddRow(orgID, "Assembled", "assembledhq.com"))
	}

	// createAutoJoinUser transaction: user upsert, membership grant at
	// member, email-verification stamp, session — all-or-nothing.
	mock.ExpectBegin()
	mock.ExpectQuery("INSERT INTO users .* ON CONFLICT .* RETURNING id, created_at").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "created_at"}).AddRow(userID, now))
	mock.ExpectQuery("INSERT INTO organization_memberships").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"role"}).AddRow("member"))
	mock.ExpectExec("UPDATE users SET email_verified_at").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectExec("UPDATE users SET last_org_id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	expectUserLastOrgLookup(mock, userID, &orgID)
	mock.ExpectQuery("INSERT INTO auth_sessions").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "created_at"}).AddRow(uuid.New(), now))
	mock.ExpectCommit()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/login/oauth/access_token":
			_, _ = w.Write([]byte(`{"access_token":"ghu_token","token_type":"bearer","scope":"repo,user:email"}`))
		case "/user":
			_, _ = w.Write([]byte(`{"id":42,"login":"alicehub","name":"Alice Hub","email":"alice@assembledhq.com","avatar_url":"https://example.com/avatar.png"}`))
		case "/user/emails":
			_, _ = w.Write([]byte(`[{"email":"alice@assembledhq.com","primary":true,"verified":true},{"email":"42+alicehub@users.noreply.github.com","verified":true}]`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	handler := NewAuthHandler(
		&config.Config{FrontendURL: "http://frontend.test"},
		mock,
		db.NewUserStore(mock),
		db.NewAuthSessionStore(mock),
		nil,
		db.NewOrganizationMembershipStore(mock),
	)
	handler.SetOrgDomainStore(db.NewOrganizationDomainStore(mock))
	handler.SetGitHubURLsForTest(server.URL, server.URL, server.Client())

	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/github/callback?state=valid-state&code=test-code", nil)
	req.AddCookie(&http.Cookie{Name: "github_oauth_state", Value: "valid-state"})
	w := httptest.NewRecorder()

	handler.Callback(w, req)
	require.Equal(t, http.StatusTemporaryRedirect, w.Code, "auto-join signup should complete and redirect")
	require.Equal(t, "http://frontend.test", w.Header().Get("Location"), "should redirect to the configured frontend")
	require.NoError(t, mock.ExpectationsWereMet(), "the auto-join transaction must run exactly as specified — no fresh-org INSERT INTO organizations")
}

// TestAuthHandler_Callback_DomainAutoJoinSkipsUnverifiedEmail pins the
// security boundary: a GitHub profile email NOT attested in /user/emails
// must not trigger domain capture, even when the domain matches a verified
// auto-join org. The signup falls back to the classic fresh-org path.
func TestAuthHandler_Callback_DomainAutoJoinSkipsUnverifiedEmail(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create pgxmock pool")
	defer mock.Close()

	now := time.Now()
	userID := uuid.New()
	userColumns := []string{"id", "org_id", "email", "name", "role", "github_id", "github_login", "github_noreply_email", "avatar_url", "password_hash", "google_id", "created_at"}

	mock.ExpectQuery("SELECT .* FROM users WHERE github_id").
		WithArgs(int64(42)).
		WillReturnRows(pgxmock.NewRows(userColumns))
	mock.ExpectQuery(`(?s)SELECT .+ FROM users WHERE LOWER\(email\) = LOWER\(@email\)`).
		WithArgs("alice@assembledhq.com").
		WillReturnRows(pgxmock.NewRows(userColumns))

	// NO domain lookup expectation: tryDomainAutoJoin must short-circuit on
	// the unverified email before ever querying organization_domains.
	// Classic signup transaction instead: fresh org + admin membership.
	mock.ExpectBegin()
	mock.ExpectQuery("INSERT INTO organizations").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "created_at", "updated_at"}).AddRow(uuid.New(), now, now))
	mock.ExpectQuery("INSERT INTO users .* ON CONFLICT .* RETURNING id, created_at").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "created_at"}).AddRow(userID, now))
	mock.ExpectExec("INSERT INTO organization_memberships").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("INSERT", 1))
	expectUserLastOrgLookup(mock, userID, nil)
	mock.ExpectQuery("INSERT INTO auth_sessions").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "created_at"}).AddRow(uuid.New(), now))
	mock.ExpectCommit()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/login/oauth/access_token":
			_, _ = w.Write([]byte(`{"access_token":"ghu_token","token_type":"bearer","scope":"repo,user:email"}`))
		case "/user":
			_, _ = w.Write([]byte(`{"id":42,"login":"alicehub","name":"Alice Hub","email":"alice@assembledhq.com","avatar_url":"https://example.com/avatar.png"}`))
		case "/user/emails":
			// The profile email is conspicuously absent / unverified.
			_, _ = w.Write([]byte(`[{"email":"42+alicehub@users.noreply.github.com","verified":true}]`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	handler := NewAuthHandler(
		&config.Config{FrontendURL: "http://frontend.test"},
		mock,
		db.NewUserStore(mock),
		db.NewAuthSessionStore(mock),
		nil,
		db.NewOrganizationMembershipStore(mock),
	)
	handler.SetOrgDomainStore(db.NewOrganizationDomainStore(mock))
	handler.SetGitHubURLsForTest(server.URL, server.URL, server.Client())

	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/github/callback?state=valid-state&code=test-code", nil)
	req.AddCookie(&http.Cookie{Name: "github_oauth_state", Value: "valid-state"})
	w := httptest.NewRecorder()

	handler.Callback(w, req)
	require.Equal(t, http.StatusTemporaryRedirect, w.Code, "fallback signup should complete and redirect")
	require.NoError(t, mock.ExpectationsWereMet(), "unverified email must take the fresh-org path, never the auto-join path")
}
