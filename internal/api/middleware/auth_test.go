package middleware

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
)

var errDBDown = errors.New("db down")

// stableUUIDs for readability in tests. The constants give each role in the
// middleware tests a named, stable identity so assertions read as
// "resolved to OrgB" rather than "resolved to 88d2…".
var (
	testUserID = uuid.MustParse("00000000-0000-0000-0000-000000000001")
	testOrgA   = uuid.MustParse("00000000-0000-0000-0000-0000000000aa")
	testOrgB   = uuid.MustParse("00000000-0000-0000-0000-0000000000bb")
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

func TestActiveRoleFromContext(t *testing.T) {
	t.Parallel()

	require.Equal(t, "", ActiveRoleFromContext(context.Background()))
	ctx := WithActiveRole(context.Background(), "admin")
	require.Equal(t, "admin", ActiveRoleFromContext(ctx))
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

// mockSessionRow builds an auth_sessions row with last_org_id populated from
// the caller's choice. Passing a pointer lets tests model "session hint
// present" and "session hint is stale" cases independently.
func mockSessionRow(sessionID, userID, orgID uuid.UUID, lastOrgID *uuid.UUID, token string, now time.Time) []any {
	return []any{
		sessionID, userID, orgID, lastOrgID, token, now.Add(24 * time.Hour), now,
	}
}

func mockUserRow(userID, orgID uuid.UUID, role string, now time.Time) []any {
	return []any{
		userID, orgID, "test@example.com", "Test User", role,
		nil, nil, nil, nil, nil, now,
	}
}

var (
	sessionCols = []string{"id", "user_id", "org_id", "last_org_id", "token", "expires_at", "created_at"}
	userCols    = []string{"id", "org_id", "email", "name", "role", "github_id", "github_login", "avatar_url", "password_hash", "google_id", "created_at"}
	memberCols  = []string{"user_id", "org_id", "role", "created_at"}
)

func TestAuth_HeaderSelectsMembership(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	now := time.Now()
	sessionID := uuid.New()

	mock.ExpectQuery("SELECT .+ FROM auth_sessions").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(sessionCols).
				AddRow(mockSessionRow(sessionID, testUserID, testOrgA, &testOrgA, "t", now)...),
		)
	mock.ExpectQuery("SELECT .+ FROM users").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(userCols).
				AddRow(mockUserRow(testUserID, testOrgA, "admin", now)...),
		)
	// Header requests OrgB — membership lookup returns member role.
	mock.ExpectQuery("SELECT .+ FROM organization_memberships").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(memberCols).
				AddRow(testUserID, testOrgB, "member", now),
		)
	// No UpdateLastOrgID expected: header-driven resolution must not trample
	// the session hint, otherwise two tabs pinned to different orgs would
	// fight on every request.

	stores := AuthStores{
		Sessions:    db.NewAuthSessionStore(mock),
		Users:       db.NewUserStore(mock),
		Memberships: db.NewOrganizationMembershipStore(mock),
	}

	nextCalled := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nextCalled = true
		require.Equal(t, testOrgB, OrgIDFromContext(r.Context()))
		require.Equal(t, "member", ActiveRoleFromContext(r.Context()))
		u := UserFromContext(r.Context())
		require.NotNil(t, u)
		require.Equal(t, testUserID, u.ID)
		require.Equal(t, "member", u.Role, "user.Role should reflect active membership role for compatibility")
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer t")
	req.Header.Set(ActiveOrgHeader, testOrgB.String())
	w := httptest.NewRecorder()

	Auth(stores, nil, zerolog.Nop())(next).ServeHTTP(w, req)

	require.True(t, nextCalled)
	require.Equal(t, http.StatusOK, w.Code)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAuth_HeaderForUnrelatedOrg_Forbidden(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	now := time.Now()

	mock.ExpectQuery("SELECT .+ FROM auth_sessions").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(sessionCols).
				AddRow(mockSessionRow(uuid.New(), testUserID, testOrgA, &testOrgA, "t", now)...),
		)
	mock.ExpectQuery("SELECT .+ FROM users").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(userCols).
				AddRow(mockUserRow(testUserID, testOrgA, "admin", now)...),
		)
	// Empty result — user has no membership in the requested org.
	mock.ExpectQuery("SELECT .+ FROM organization_memberships").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(memberCols))

	stores := AuthStores{
		Sessions:    db.NewAuthSessionStore(mock),
		Users:       db.NewUserStore(mock),
		Memberships: db.NewOrganizationMembershipStore(mock),
	}

	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("next should not be invoked for forbidden membership")
	})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer t")
	req.Header.Set(ActiveOrgHeader, testOrgB.String())
	w := httptest.NewRecorder()

	Auth(stores, nil, zerolog.Nop())(next).ServeHTTP(w, req)

	require.Equal(t, http.StatusForbidden, w.Code)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAuth_SessionHintFallback(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	now := time.Now()

	// Session has last_org_id=OrgA. No header sent.
	mock.ExpectQuery("SELECT .+ FROM auth_sessions").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(sessionCols).
				AddRow(mockSessionRow(uuid.New(), testUserID, testOrgA, &testOrgA, "t", now)...),
		)
	mock.ExpectQuery("SELECT .+ FROM users").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(userCols).
				AddRow(mockUserRow(testUserID, testOrgA, "admin", now)...),
		)
	mock.ExpectQuery("SELECT .+ FROM organization_memberships").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(memberCols).
				AddRow(testUserID, testOrgA, "admin", now),
		)
	// No UPDATE expected — resolution matches existing hint.

	stores := AuthStores{
		Sessions:    db.NewAuthSessionStore(mock),
		Users:       db.NewUserStore(mock),
		Memberships: db.NewOrganizationMembershipStore(mock),
	}

	nextCalled := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nextCalled = true
		require.Equal(t, testOrgA, OrgIDFromContext(r.Context()))
		require.Equal(t, "admin", ActiveRoleFromContext(r.Context()))
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer t")
	w := httptest.NewRecorder()

	Auth(stores, nil, zerolog.Nop())(next).ServeHTTP(w, req)

	require.True(t, nextCalled)
	require.Equal(t, http.StatusOK, w.Code)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAuth_SessionHintStale_FallsBackToOldest(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	now := time.Now()

	// Session hint points to OrgB (user was removed), OldestForUser returns OrgA.
	mock.ExpectQuery("SELECT .+ FROM auth_sessions").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(sessionCols).
				AddRow(mockSessionRow(uuid.New(), testUserID, testOrgA, &testOrgB, "t", now)...),
		)
	mock.ExpectQuery("SELECT .+ FROM users").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(userCols).
				AddRow(mockUserRow(testUserID, testOrgA, "admin", now)...),
		)
	// Hint lookup returns no rows (revoked).
	mock.ExpectQuery("SELECT .+ FROM organization_memberships WHERE user_id .+ AND org_id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(memberCols))
	// Oldest-for-user falls through to OrgA.
	mock.ExpectQuery("SELECT .+ FROM organization_memberships WHERE user_id .+ ORDER BY created_at").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(memberCols).
				AddRow(testUserID, testOrgA, "admin", now),
		)
	// Session gets updated to the new resolution.
	mock.ExpectExec("UPDATE auth_sessions SET last_org_id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	stores := AuthStores{
		Sessions:    db.NewAuthSessionStore(mock),
		Users:       db.NewUserStore(mock),
		Memberships: db.NewOrganizationMembershipStore(mock),
	}

	nextCalled := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nextCalled = true
		require.Equal(t, testOrgA, OrgIDFromContext(r.Context()))
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer t")
	w := httptest.NewRecorder()

	Auth(stores, nil, zerolog.Nop())(next).ServeHTTP(w, req)

	require.True(t, nextCalled)
	require.Equal(t, http.StatusOK, w.Code)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAuth_ZeroMemberships_AllowsEmptyState(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	now := time.Now()

	mock.ExpectQuery("SELECT .+ FROM auth_sessions").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(sessionCols).
				AddRow(mockSessionRow(uuid.New(), testUserID, testOrgA, nil, "t", now)...),
		)
	mock.ExpectQuery("SELECT .+ FROM users").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(userCols).
				AddRow(mockUserRow(testUserID, testOrgA, "admin", now)...),
		)
	// Oldest-for-user returns nothing (user has no memberships).
	mock.ExpectQuery("SELECT .+ FROM organization_memberships WHERE user_id .+ ORDER BY created_at").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(memberCols))

	stores := AuthStores{
		Sessions:    db.NewAuthSessionStore(mock),
		Users:       db.NewUserStore(mock),
		Memberships: db.NewOrganizationMembershipStore(mock),
	}

	nextCalled := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nextCalled = true
		require.Equal(t, uuid.Nil, OrgIDFromContext(r.Context()))
		require.Equal(t, "", ActiveRoleFromContext(r.Context()))
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer t")
	w := httptest.NewRecorder()

	Auth(stores, nil, zerolog.Nop())(next).ServeHTTP(w, req)

	require.True(t, nextCalled)
	require.Equal(t, http.StatusOK, w.Code)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAuth_MissingCredentials(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	stores := AuthStores{
		Sessions:    db.NewAuthSessionStore(mock),
		Users:       db.NewUserStore(mock),
		Memberships: db.NewOrganizationMembershipStore(mock),
	}

	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("next should not be called")
	})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()

	Auth(stores, nil, zerolog.Nop())(next).ServeHTTP(w, req)
	require.Equal(t, http.StatusUnauthorized, w.Code)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAuth_InvalidSessionClearsCookie(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	mock.ExpectQuery("SELECT .+ FROM auth_sessions").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(sessionCols))

	stores := AuthStores{
		Sessions:    db.NewAuthSessionStore(mock),
		Users:       db.NewUserStore(mock),
		Memberships: db.NewOrganizationMembershipStore(mock),
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: "session_token", Value: "bad"})
	w := httptest.NewRecorder()

	Auth(stores, nil, zerolog.Nop())(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("next should not be called")
	})).ServeHTTP(w, req)

	require.Equal(t, http.StatusUnauthorized, w.Code)
	var cleared bool
	for _, c := range w.Result().Cookies() {
		if c.Name == "session_token" && c.MaxAge < 0 {
			cleared = true
		}
	}
	require.True(t, cleared)
	require.NoError(t, mock.ExpectationsWereMet())
}

// When the header membership lookup fails with a non-ErrNoRows error (DB
// flake, not a missing row), resolveActiveMembership returns the error
// unwrapped so handleToken surfaces a 403 rather than falling back to a
// different org.
func TestAuth_HeaderMembershipLookupError(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	now := time.Now()

	mock.ExpectQuery("SELECT .+ FROM auth_sessions").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(sessionCols).
				AddRow(mockSessionRow(uuid.New(), testUserID, testOrgA, &testOrgA, "t", now)...),
		)
	mock.ExpectQuery("SELECT .+ FROM users").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(userCols).
				AddRow(mockUserRow(testUserID, testOrgA, "admin", now)...),
		)
	mock.ExpectQuery("SELECT .+ FROM organization_memberships").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnError(errDBDown)

	stores := AuthStores{
		Sessions:    db.NewAuthSessionStore(mock),
		Users:       db.NewUserStore(mock),
		Memberships: db.NewOrganizationMembershipStore(mock),
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer t")
	req.Header.Set(ActiveOrgHeader, testOrgB.String())
	w := httptest.NewRecorder()

	Auth(stores, nil, zerolog.Nop())(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("next should not run on membership lookup failure")
	})).ServeHTTP(w, req)

	require.Equal(t, http.StatusForbidden, w.Code)
	require.NoError(t, mock.ExpectationsWereMet())
}

// When the X-Active-Org-ID header drives the active-org resolution we must
// NOT persist last_org_id — two tabs pinned to different orgs would otherwise
// trample each other's hint on every request, leaving cold-load behavior
// non-deterministic. The header is the client's authoritative declaration;
// the session hint should only track the no-header fallback.
func TestAuth_HeaderResolutionDoesNotUpdateLastOrgID(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	now := time.Now()

	mock.ExpectQuery("SELECT .+ FROM auth_sessions").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(sessionCols).
				AddRow(mockSessionRow(uuid.New(), testUserID, testOrgA, &testOrgA, "t", now)...),
		)
	mock.ExpectQuery("SELECT .+ FROM users").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(userCols).
				AddRow(mockUserRow(testUserID, testOrgA, "admin", now)...),
		)
	// Header points at B; membership lookup succeeds — but no UpdateLastOrgID
	// should fire.
	mock.ExpectQuery("SELECT .+ FROM organization_memberships").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(memberCols).
				AddRow(testUserID, testOrgB, "member", now),
		)

	stores := AuthStores{
		Sessions:    db.NewAuthSessionStore(mock),
		Users:       db.NewUserStore(mock),
		Memberships: db.NewOrganizationMembershipStore(mock),
	}

	nextCalled := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nextCalled = true
		require.Equal(t, testOrgB, OrgIDFromContext(r.Context()))
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer t")
	req.Header.Set(ActiveOrgHeader, testOrgB.String())
	w := httptest.NewRecorder()

	Auth(stores, nil, zerolog.Nop())(next).ServeHTTP(w, req)

	require.True(t, nextCalled, "request should succeed")
	require.Equal(t, http.StatusOK, w.Code)
	// pgxmock fails ExpectationsWereMet if any UPDATE auth_sessions runs that
	// wasn't expected — that's the assertion for "no last_org_id write".
	require.NoError(t, mock.ExpectationsWereMet())
}

// When the last_org_id update fails after a successful resolution, the
// middleware logs the warning but still serves the request — the hint is a
// convenience for the next cold load, not load-bearing for this one.
func TestAuth_LastOrgIDUpdateFailureNonFatal(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	now := time.Now()

	// Session has no last_org_id — we'll fall through to OldestForUser and
	// then attempt to persist the resolution.
	mock.ExpectQuery("SELECT .+ FROM auth_sessions").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(sessionCols).
				AddRow(mockSessionRow(uuid.New(), testUserID, testOrgA, nil, "t", now)...),
		)
	mock.ExpectQuery("SELECT .+ FROM users").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(userCols).
				AddRow(mockUserRow(testUserID, testOrgA, "admin", now)...),
		)
	mock.ExpectQuery("SELECT .+ FROM organization_memberships WHERE user_id .+ ORDER BY created_at").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(memberCols).
				AddRow(testUserID, testOrgA, "admin", now),
		)
	mock.ExpectExec("UPDATE auth_sessions SET last_org_id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnError(errDBDown)

	stores := AuthStores{
		Sessions:    db.NewAuthSessionStore(mock),
		Users:       db.NewUserStore(mock),
		Memberships: db.NewOrganizationMembershipStore(mock),
	}

	nextCalled := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nextCalled = true
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer t")
	w := httptest.NewRecorder()

	Auth(stores, nil, zerolog.Nop())(next).ServeHTTP(w, req)

	require.True(t, nextCalled, "request must still succeed despite last_org_id update failure")
	require.Equal(t, http.StatusOK, w.Code)
	require.NoError(t, mock.ExpectationsWereMet())
}

// When OldestForUser errors (not ErrNoRows), the middleware surfaces 403
// rather than pretending the user has no memberships.
func TestAuth_OldestForUserError(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	now := time.Now()

	mock.ExpectQuery("SELECT .+ FROM auth_sessions").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(sessionCols).
				AddRow(mockSessionRow(uuid.New(), testUserID, testOrgA, nil, "t", now)...),
		)
	mock.ExpectQuery("SELECT .+ FROM users").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(userCols).
				AddRow(mockUserRow(testUserID, testOrgA, "admin", now)...),
		)
	mock.ExpectQuery("SELECT .+ FROM organization_memberships WHERE user_id .+ ORDER BY created_at").
		WithArgs(pgxmock.AnyArg()).
		WillReturnError(errDBDown)

	stores := AuthStores{
		Sessions:    db.NewAuthSessionStore(mock),
		Users:       db.NewUserStore(mock),
		Memberships: db.NewOrganizationMembershipStore(mock),
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer t")
	w := httptest.NewRecorder()

	Auth(stores, nil, zerolog.Nop())(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("next should not run on oldest lookup failure")
	})).ServeHTTP(w, req)

	require.Equal(t, http.StatusForbidden, w.Code)
	require.NoError(t, mock.ExpectationsWereMet())
}

// When the session's last_org_id hint is for a different org and that lookup
// fails with a non-ErrNoRows error, the middleware surfaces 403 rather than
// silently falling back to OldestForUser (which would pick the wrong org).
func TestAuth_SessionHintLookupError(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	now := time.Now()
	// Session points at OrgB as its hint.
	mock.ExpectQuery("SELECT .+ FROM auth_sessions").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(sessionCols).
				AddRow(mockSessionRow(uuid.New(), testUserID, testOrgA, &testOrgB, "t", now)...),
		)
	mock.ExpectQuery("SELECT .+ FROM users").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(userCols).
				AddRow(mockUserRow(testUserID, testOrgA, "admin", now)...),
		)
	// Membership lookup for the hint errors (not ErrNoRows) — we bail out.
	mock.ExpectQuery("SELECT .+ FROM organization_memberships").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnError(errDBDown)

	stores := AuthStores{
		Sessions:    db.NewAuthSessionStore(mock),
		Users:       db.NewUserStore(mock),
		Memberships: db.NewOrganizationMembershipStore(mock),
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer t")
	w := httptest.NewRecorder()

	Auth(stores, nil, zerolog.Nop())(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("next should not run on hint lookup failure")
	})).ServeHTTP(w, req)

	require.Equal(t, http.StatusForbidden, w.Code)
	require.NoError(t, mock.ExpectationsWereMet())
}

// When GetByIDGlobal reports the user is gone (e.g. account deleted after
// the session was minted), the middleware rejects the request.
func TestAuth_UserLookupFails(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	now := time.Now()

	mock.ExpectQuery("SELECT .+ FROM auth_sessions").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(sessionCols).
				AddRow(mockSessionRow(uuid.New(), testUserID, testOrgA, &testOrgA, "t", now)...),
		)
	mock.ExpectQuery("SELECT .+ FROM users").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(userCols))

	stores := AuthStores{
		Sessions:    db.NewAuthSessionStore(mock),
		Users:       db.NewUserStore(mock),
		Memberships: db.NewOrganizationMembershipStore(mock),
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: "session_token", Value: "t"})
	w := httptest.NewRecorder()

	Auth(stores, nil, zerolog.Nop())(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("next should not run when user lookup fails")
	})).ServeHTTP(w, req)

	require.Equal(t, http.StatusUnauthorized, w.Code)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAuth_InvalidHeaderValue(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	now := time.Now()

	mock.ExpectQuery("SELECT .+ FROM auth_sessions").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(sessionCols).
				AddRow(mockSessionRow(uuid.New(), testUserID, testOrgA, &testOrgA, "t", now)...),
		)
	mock.ExpectQuery("SELECT .+ FROM users").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(userCols).
				AddRow(mockUserRow(testUserID, testOrgA, "admin", now)...),
		)

	stores := AuthStores{
		Sessions:    db.NewAuthSessionStore(mock),
		Users:       db.NewUserStore(mock),
		Memberships: db.NewOrganizationMembershipStore(mock),
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer t")
	req.Header.Set(ActiveOrgHeader, "not-a-uuid")
	w := httptest.NewRecorder()

	Auth(stores, nil, zerolog.Nop())(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("next should not be called")
	})).ServeHTTP(w, req)

	require.Equal(t, http.StatusForbidden, w.Code)
	require.NoError(t, mock.ExpectationsWereMet())
}
