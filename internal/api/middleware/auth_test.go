package middleware

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func TestUserFromContext(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		setup    func() context.Context
		expected *models.User
	}{
		{
			name: "returns nil when no user in context",
			setup: func() context.Context {
				return context.Background()
			},
			expected: nil,
		},
		{
			name: "returns user when set in context",
			setup: func() context.Context {
				u := &models.User{
					ID:    uuid.MustParse("aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"),
					OrgID: uuid.MustParse("11111111-2222-3333-4444-555555555555"),
					Email: "test@example.com",
					Name:  "Test User",
					Role:  "admin",
				}
				return WithUser(context.Background(), u)
			},
			expected: &models.User{
				ID:    uuid.MustParse("aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"),
				OrgID: uuid.MustParse("11111111-2222-3333-4444-555555555555"),
				Email: "test@example.com",
				Name:  "Test User",
				Role:  "admin",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctx := tt.setup()
			result := UserFromContext(ctx)
			require.Equal(t, tt.expected, result, "should return expected user from context")
		})
	}
}

func TestOrgIDFromContext(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		setup    func() context.Context
		expected uuid.UUID
	}{
		{
			name: "returns nil UUID when no org ID in context",
			setup: func() context.Context {
				return context.Background()
			},
			expected: uuid.Nil,
		},
		{
			name: "returns org ID when set in context",
			setup: func() context.Context {
				return WithOrgID(context.Background(), uuid.MustParse("11111111-2222-3333-4444-555555555555"))
			},
			expected: uuid.MustParse("11111111-2222-3333-4444-555555555555"),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			ctx := tt.setup()
			result := OrgIDFromContext(ctx)
			require.Equal(t, tt.expected, result, "should return expected org ID from context")
		})
	}
}

func TestOrgContext(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		setup        func(req *http.Request) *http.Request
		expectedCode int
	}{
		{
			name: "rejects request with missing org context",
			setup: func(req *http.Request) *http.Request {
				return req
			},
			expectedCode: http.StatusForbidden,
		},
		{
			name: "allows request with valid org context",
			setup: func(req *http.Request) *http.Request {
				ctx := WithOrgID(req.Context(), uuid.New())
				return req.WithContext(ctx)
			},
			expectedCode: http.StatusOK,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			handler := OrgContext(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
			}))

			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req = tt.setup(req)
			w := httptest.NewRecorder()

			handler.ServeHTTP(w, req)
			require.Equal(t, tt.expectedCode, w.Code, "should return expected HTTP status code")
		})
	}
}

func TestAuth(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		setupMock     func(mock pgxmock.PgxPoolIface)
		setupRequest  func(req *http.Request) *http.Request
		expectedCode  int
		checkContext  func(t *testing.T, r *http.Request)
		checkResponse func(t *testing.T, w *httptest.ResponseRecorder)
	}{
		{
			name: "authenticates valid session cookie and sets user in context",
			setupMock: func(mock pgxmock.PgxPoolIface) {
				now := time.Now()
				sessionRows := pgxmock.NewRows([]string{"id", "user_id", "org_id", "token", "expires_at", "created_at"}).
					AddRow(
						uuid.MustParse("aaaaaaaa-0000-0000-0000-000000000001"),
						uuid.MustParse("aaaaaaaa-0000-0000-0000-000000000002"),
						uuid.MustParse("aaaaaaaa-0000-0000-0000-000000000003"),
						"valid-session-token",
						now.Add(24*time.Hour),
						now,
					)
				mock.ExpectQuery("SELECT .+ FROM auth_sessions").
					WithArgs(pgxmock.AnyArg()).
					WillReturnRows(sessionRows)

				ghID := int64(12345)
				ghLogin := "testuser"
				avatarURL := "https://example.com/avatar.png"
				userRows := pgxmock.NewRows([]string{"id", "org_id", "email", "name", "role", "github_id", "github_login", "avatar_url", "password_hash", "google_id", "created_at"}).
					AddRow(
						uuid.MustParse("aaaaaaaa-0000-0000-0000-000000000002"),
						uuid.MustParse("aaaaaaaa-0000-0000-0000-000000000003"),
						"test@example.com", "Test User", "member",
						&ghID, &ghLogin, &avatarURL, nil, nil, now,
					)
				mock.ExpectQuery("SELECT .+ FROM users").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(userRows)
			},
			setupRequest: func(req *http.Request) *http.Request {
				req.AddCookie(&http.Cookie{Name: "session_token", Value: "valid-session-token"})
				return req
			},
			expectedCode: http.StatusOK,
			checkContext: func(t *testing.T, r *http.Request) {
				t.Helper()
				user := UserFromContext(r.Context())
				require.NotNil(t, user, "should set user in request context")
				require.Equal(t, uuid.MustParse("aaaaaaaa-0000-0000-0000-000000000002"), user.ID, "should set correct user ID in context")
				require.Equal(t, uuid.MustParse("aaaaaaaa-0000-0000-0000-000000000003"), user.OrgID, "should set correct org ID in context")
			},
			checkResponse: nil,
		},
		{
			name: "authenticates valid bearer token and sets user in context",
			setupMock: func(mock pgxmock.PgxPoolIface) {
				now := time.Now()
				sessionRows := pgxmock.NewRows([]string{"id", "user_id", "org_id", "token", "expires_at", "created_at"}).
					AddRow(
						uuid.MustParse("bbbbbbbb-0000-0000-0000-000000000001"),
						uuid.MustParse("bbbbbbbb-0000-0000-0000-000000000002"),
						uuid.MustParse("bbbbbbbb-0000-0000-0000-000000000003"),
						"bearer-token-value",
						now.Add(24*time.Hour),
						now,
					)
				mock.ExpectQuery("SELECT .+ FROM auth_sessions").
					WithArgs(pgxmock.AnyArg()).
					WillReturnRows(sessionRows)

				ghID := int64(12345)
				ghLogin := "testuser"
				avatarURL := "https://example.com/avatar.png"
				userRows := pgxmock.NewRows([]string{"id", "org_id", "email", "name", "role", "github_id", "github_login", "avatar_url", "password_hash", "google_id", "created_at"}).
					AddRow(
						uuid.MustParse("bbbbbbbb-0000-0000-0000-000000000002"),
						uuid.MustParse("bbbbbbbb-0000-0000-0000-000000000003"),
						"test@example.com", "Test User", "admin",
						&ghID, &ghLogin, &avatarURL, nil, nil, now,
					)
				mock.ExpectQuery("SELECT .+ FROM users").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(userRows)
			},
			setupRequest: func(req *http.Request) *http.Request {
				req.Header.Set("Authorization", "Bearer bearer-token-value")
				return req
			},
			expectedCode: http.StatusOK,
			checkContext: func(t *testing.T, r *http.Request) {
				t.Helper()
				user := UserFromContext(r.Context())
				require.NotNil(t, user, "should set user in request context")
				require.Equal(t, uuid.MustParse("bbbbbbbb-0000-0000-0000-000000000002"), user.ID, "should set correct user ID in context")
			},
			checkResponse: nil,
		},
		{
			name: "does not refresh bearer-token session even when inside refresh window",
			setupMock: func(mock pgxmock.PgxPoolIface) {
				now := time.Now()
				sessionRows := pgxmock.NewRows([]string{"id", "user_id", "org_id", "token", "expires_at", "created_at"}).
					AddRow(
						uuid.MustParse("cccccccc-0000-0000-0000-000000000001"),
						uuid.MustParse("cccccccc-0000-0000-0000-000000000002"),
						uuid.MustParse("cccccccc-0000-0000-0000-000000000003"),
						"stale-bearer-token",
						now.Add(24*time.Hour), // well inside the refresh window
						now,
					)
				mock.ExpectQuery("SELECT .+ FROM auth_sessions").
					WithArgs(pgxmock.AnyArg()).
					WillReturnRows(sessionRows)

				ghID := int64(12345)
				ghLogin := "testuser"
				avatarURL := "https://example.com/avatar.png"
				userRows := pgxmock.NewRows([]string{"id", "org_id", "email", "name", "role", "github_id", "github_login", "avatar_url", "password_hash", "google_id", "created_at"}).
					AddRow(
						uuid.MustParse("cccccccc-0000-0000-0000-000000000002"),
						uuid.MustParse("cccccccc-0000-0000-0000-000000000003"),
						"test@example.com", "Test User", "member",
						&ghID, &ghLogin, &avatarURL, nil, nil, now,
					)
				mock.ExpectQuery("SELECT .+ FROM users").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(userRows)
				// Deliberately no UPDATE expectation: bearer tokens must not
				// trigger sliding refresh. pgxmock fails on unexpected calls.
			},
			setupRequest: func(req *http.Request) *http.Request {
				req.Header.Set("Authorization", "Bearer stale-bearer-token")
				return req
			},
			expectedCode: http.StatusOK,
			checkContext: nil,
			checkResponse: func(t *testing.T, w *httptest.ResponseRecorder) {
				t.Helper()
				resp := w.Result()
				defer resp.Body.Close()
				for _, c := range resp.Cookies() {
					if c.Name == SessionCookieName || c.Name == CSRFCookieName {
						t.Fatalf("bearer-token auth must not emit %s cookie, got %+v", c.Name, c)
					}
				}
			},
		},
		{
			name:      "returns 401 when no cookie and no authorization header present",
			setupMock: func(mock pgxmock.PgxPoolIface) {},
			setupRequest: func(req *http.Request) *http.Request {
				return req
			},
			expectedCode:  http.StatusUnauthorized,
			checkContext:  nil,
			checkResponse: nil,
		},
		{
			name: "returns 401 and clears cookie when session cookie is invalid",
			setupMock: func(mock pgxmock.PgxPoolIface) {
				sessionRows := pgxmock.NewRows([]string{"id", "user_id", "org_id", "token", "expires_at", "created_at"})
				mock.ExpectQuery("SELECT .+ FROM auth_sessions").
					WithArgs(pgxmock.AnyArg()).
					WillReturnRows(sessionRows)
			},
			setupRequest: func(req *http.Request) *http.Request {
				req.AddCookie(&http.Cookie{Name: "session_token", Value: "bad-token"})
				return req
			},
			expectedCode: http.StatusUnauthorized,
			checkContext: nil,
			checkResponse: func(t *testing.T, w *httptest.ResponseRecorder) {
				t.Helper()
				resp := w.Result()
				defer resp.Body.Close()
				foundClearedCookie := false
				for _, c := range resp.Cookies() {
					if c.Name == "session_token" {
						foundClearedCookie = true
						require.Equal(t, "", c.Value, "session cookie should be cleared when token is invalid")
						require.Equal(t, -1, c.MaxAge, "session cookie MaxAge should invalidate cookie immediately")
						require.Equal(t, "/", c.Path, "session cookie should keep path when clearing")
					}
				}
				require.True(t, foundClearedCookie, "response should clear session cookie when session is invalid")
			},
		},
		{
			name: "does not refresh when session expires_at is far in the future",
			setupMock: func(mock pgxmock.PgxPoolIface) {
				now := time.Now()
				sessionRows := pgxmock.NewRows([]string{"id", "user_id", "org_id", "token", "expires_at", "created_at"}).
					AddRow(
						uuid.MustParse("dddddddd-0000-0000-0000-000000000001"),
						uuid.MustParse("dddddddd-0000-0000-0000-000000000002"),
						uuid.MustParse("dddddddd-0000-0000-0000-000000000003"),
						"fresh-token",
						now.Add(29*24*time.Hour), // fresher than TTL - refreshWindow (25d)
						now,
					)
				mock.ExpectQuery("SELECT .+ FROM auth_sessions").
					WithArgs(pgxmock.AnyArg()).
					WillReturnRows(sessionRows)

				ghID := int64(12345)
				ghLogin := "testuser"
				avatarURL := "https://example.com/avatar.png"
				userRows := pgxmock.NewRows([]string{"id", "org_id", "email", "name", "role", "github_id", "github_login", "avatar_url", "password_hash", "google_id", "created_at"}).
					AddRow(
						uuid.MustParse("dddddddd-0000-0000-0000-000000000002"),
						uuid.MustParse("dddddddd-0000-0000-0000-000000000003"),
						"test@example.com", "Test User", "member",
						&ghID, &ghLogin, &avatarURL, nil, nil, now,
					)
				mock.ExpectQuery("SELECT .+ FROM users").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(userRows)
				// No UPDATE expected — expecting no refresh on fresh sessions.
			},
			setupRequest: func(req *http.Request) *http.Request {
				req.AddCookie(&http.Cookie{Name: "session_token", Value: "fresh-token"})
				return req
			},
			expectedCode: http.StatusOK,
			checkContext: nil,
			checkResponse: func(t *testing.T, w *httptest.ResponseRecorder) {
				t.Helper()
				resp := w.Result()
				defer resp.Body.Close()
				for _, c := range resp.Cookies() {
					if c.Name == SessionCookieName {
						t.Fatalf("did not expect session cookie to be reissued, got %+v", c)
					}
				}
			},
		},
		{
			name: "refreshes cookie and expires_at when session is past the refresh window",
			setupMock: func(mock pgxmock.PgxPoolIface) {
				now := time.Now()
				sessionRows := pgxmock.NewRows([]string{"id", "user_id", "org_id", "token", "expires_at", "created_at"}).
					AddRow(
						uuid.MustParse("eeeeeeee-0000-0000-0000-000000000001"),
						uuid.MustParse("eeeeeeee-0000-0000-0000-000000000002"),
						uuid.MustParse("eeeeeeee-0000-0000-0000-000000000003"),
						"stale-token",
						now.Add(24*time.Hour), // well inside TTL - refreshWindow (25d)
						now,
					)
				mock.ExpectQuery("SELECT .+ FROM auth_sessions").
					WithArgs(pgxmock.AnyArg()).
					WillReturnRows(sessionRows)

				ghID := int64(12345)
				ghLogin := "testuser"
				avatarURL := "https://example.com/avatar.png"
				userRows := pgxmock.NewRows([]string{"id", "org_id", "email", "name", "role", "github_id", "github_login", "avatar_url", "password_hash", "google_id", "created_at"}).
					AddRow(
						uuid.MustParse("eeeeeeee-0000-0000-0000-000000000002"),
						uuid.MustParse("eeeeeeee-0000-0000-0000-000000000003"),
						"test@example.com", "Test User", "member",
						&ghID, &ghLogin, &avatarURL, nil, nil, now,
					)
				mock.ExpectQuery("SELECT .+ FROM users").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(userRows)

				mock.ExpectExec("UPDATE auth_sessions SET expires_at").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnResult(pgxmock.NewResult("UPDATE", 1))
			},
			setupRequest: func(req *http.Request) *http.Request {
				req.AddCookie(&http.Cookie{Name: "session_token", Value: "stale-token"})
				return req
			},
			expectedCode: http.StatusOK,
			checkContext: nil,
			checkResponse: func(t *testing.T, w *httptest.ResponseRecorder) {
				t.Helper()
				resp := w.Result()
				defer resp.Body.Close()
				var refreshed, csrf *http.Cookie
				for _, c := range resp.Cookies() {
					switch c.Name {
					case SessionCookieName:
						refreshed = c
					case CSRFCookieName:
						csrf = c
					}
				}
				require.NotNil(t, refreshed, "expected session cookie to be reissued with fresh MaxAge")
				require.Equal(t, "stale-token", refreshed.Value, "reissued cookie should carry the same opaque token")
				require.Equal(t, int(SessionTTL.Seconds()), refreshed.MaxAge, "reissued cookie should use the full TTL")
				require.True(t, refreshed.HttpOnly, "reissued cookie should stay HttpOnly")
				require.NotNil(t, csrf, "CSRF cookie should be extended in lockstep with session refresh")
				require.Equal(t, int(SessionTTL.Seconds()), csrf.MaxAge, "CSRF cookie MaxAge should match session TTL")
			},
		},
		{
			name: "authenticates request when Touch fails and does not reissue cookie",
			setupMock: func(mock pgxmock.PgxPoolIface) {
				now := time.Now()
				sessionRows := pgxmock.NewRows([]string{"id", "user_id", "org_id", "token", "expires_at", "created_at"}).
					AddRow(
						uuid.MustParse("ffffffff-0000-0000-0000-000000000001"),
						uuid.MustParse("ffffffff-0000-0000-0000-000000000002"),
						uuid.MustParse("ffffffff-0000-0000-0000-000000000003"),
						"stale-token",
						now.Add(24*time.Hour),
						now,
					)
				mock.ExpectQuery("SELECT .+ FROM auth_sessions").
					WithArgs(pgxmock.AnyArg()).
					WillReturnRows(sessionRows)

				ghID := int64(12345)
				ghLogin := "testuser"
				avatarURL := "https://example.com/avatar.png"
				userRows := pgxmock.NewRows([]string{"id", "org_id", "email", "name", "role", "github_id", "github_login", "avatar_url", "password_hash", "google_id", "created_at"}).
					AddRow(
						uuid.MustParse("ffffffff-0000-0000-0000-000000000002"),
						uuid.MustParse("ffffffff-0000-0000-0000-000000000003"),
						"test@example.com", "Test User", "member",
						&ghID, &ghLogin, &avatarURL, nil, nil, now,
					)
				mock.ExpectQuery("SELECT .+ FROM users").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(userRows)

				// Refresh UPDATE fails — request should still succeed using the
				// existing session, and no refreshed cookie should be emitted.
				mock.ExpectExec("UPDATE auth_sessions SET expires_at").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnError(fmt.Errorf("connection lost"))
			},
			setupRequest: func(req *http.Request) *http.Request {
				req.AddCookie(&http.Cookie{Name: "session_token", Value: "stale-token"})
				return req
			},
			expectedCode: http.StatusOK,
			checkContext: func(t *testing.T, r *http.Request) {
				t.Helper()
				require.NotNil(t, UserFromContext(r.Context()), "user should still be set on context when Touch fails")
			},
			checkResponse: func(t *testing.T, w *httptest.ResponseRecorder) {
				t.Helper()
				resp := w.Result()
				defer resp.Body.Close()
				for _, c := range resp.Cookies() {
					if c.Name == SessionCookieName {
						t.Fatalf("did not expect session cookie to be reissued when Touch fails, got %+v", c)
					}
					if c.Name == CSRFCookieName {
						t.Fatalf("did not expect CSRF cookie to be reissued when Touch fails, got %+v", c)
					}
				}
			},
		},
		{
			name: "returns 401 when session is valid but user not found",
			setupMock: func(mock pgxmock.PgxPoolIface) {
				now := time.Now()
				sessionRows := pgxmock.NewRows([]string{"id", "user_id", "org_id", "token", "expires_at", "created_at"}).
					AddRow(
						uuid.MustParse("cccccccc-0000-0000-0000-000000000001"),
						uuid.MustParse("cccccccc-0000-0000-0000-000000000002"),
						uuid.MustParse("cccccccc-0000-0000-0000-000000000003"),
						"valid-token",
						now.Add(24*time.Hour),
						now,
					)
				mock.ExpectQuery("SELECT .+ FROM auth_sessions").
					WithArgs(pgxmock.AnyArg()).
					WillReturnRows(sessionRows)

				userRows := pgxmock.NewRows([]string{"id", "org_id", "email", "name", "role", "github_id", "github_login", "avatar_url", "password_hash", "google_id", "created_at"})
				mock.ExpectQuery("SELECT .+ FROM users").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(userRows)
			},
			setupRequest: func(req *http.Request) *http.Request {
				req.AddCookie(&http.Cookie{Name: "session_token", Value: "valid-token"})
				return req
			},
			expectedCode:  http.StatusUnauthorized,
			checkContext:  nil,
			checkResponse: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "should create pgxmock pool without error")
			defer mock.Close()

			sessionStore := db.NewAuthSessionStore(mock)
			userStore := db.NewUserStore(mock)
			tt.setupMock(mock)

			nextCalled := false
			next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				nextCalled = true
				if tt.checkContext != nil {
					tt.checkContext(t, r)
				}
				w.WriteHeader(http.StatusOK)
			})

			handler := Auth(sessionStore, userStore, []byte("test-signing-key-that-is-long-enough-for-hmac"), zerolog.Nop())(next)
			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req = tt.setupRequest(req)
			w := httptest.NewRecorder()

			handler.ServeHTTP(w, req)

			require.Equal(t, tt.expectedCode, w.Code, "should return expected HTTP status code")
			if tt.expectedCode == http.StatusOK {
				require.True(t, nextCalled, "should call next handler for successful auth")
			} else {
				require.False(t, nextCalled, "should not call next handler for failed auth")
			}
			if tt.checkResponse != nil {
				tt.checkResponse(t, w)
			}
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}
