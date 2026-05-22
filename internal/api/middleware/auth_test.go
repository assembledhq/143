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
		nil, nil, nil, nil, nil, nil, now,
	}
}

var (
	sessionCols = []string{"id", "user_id", "org_id", "last_org_id", "token", "expires_at", "created_at"}
	userCols    = []string{"id", "org_id", "email", "name", "role", "github_id", "github_login", "github_noreply_email", "avatar_url", "password_hash", "google_id", "created_at"}
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
		require.Equal(t, models.RoleMember, u.Role, "user.Role should reflect active membership role for compatibility")
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

type identityRecorderResponseWriter struct {
	*httptest.ResponseRecorder
	orgID  uuid.UUID
	userID uuid.UUID
}

func (w *identityRecorderResponseWriter) SetResolvedIdentity(orgID, userID uuid.UUID) {
	w.orgID = orgID
	w.userID = userID
}

func TestAuth_RecordsResolvedIdentityOnResponseWriter(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should be created")
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
	mock.ExpectQuery("SELECT .+ FROM organization_memberships").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(memberCols).
				AddRow(testUserID, testOrgA, "admin", now),
		)

	stores := AuthStores{
		Sessions:    db.NewAuthSessionStore(mock),
		Users:       db.NewUserStore(mock),
		Memberships: db.NewOrganizationMembershipStore(mock),
	}

	handler := Auth(stores, nil, zerolog.Nop())(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer t")
	w := &identityRecorderResponseWriter{ResponseRecorder: httptest.NewRecorder()}

	handler.ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code, "auth middleware should preserve the successful response")
	require.Equal(t, testOrgA, w.orgID, "auth middleware should record the resolved org identity on the response writer")
	require.Equal(t, testUserID, w.userID, "auth middleware should record the resolved user identity on the response writer")
	require.NoError(t, mock.ExpectationsWereMet(), "all auth store expectations should be met")
}

// TestAuth_HeaderForUnrelatedOrg_FallsThrough covers the graceful-degrade
// path: an explicit X-Active-Org-ID for an org the user is not a member of
// (e.g. the client still has a cached orgID after the user was removed)
// falls through to the session hint and then the user's oldest membership.
// Returning 403 here would hard-lock a user out of every other org they
// belong to until they found some way to clear the stale client state.
func TestAuth_HeaderForUnrelatedOrg_FallsThrough(t *testing.T) {
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
	// Header lookup: user has no membership in OrgB (the header target).
	mock.ExpectQuery("SELECT .+ FROM organization_memberships").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(memberCols))
	// Session hint lookup: falls through to OrgA, which the user is in.
	mock.ExpectQuery("SELECT .+ FROM organization_memberships WHERE user_id .+ AND org_id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(memberCols).
				AddRow(testUserID, testOrgA, "admin", now),
		)

	stores := AuthStores{
		Sessions:    db.NewAuthSessionStore(mock),
		Users:       db.NewUserStore(mock),
		Memberships: db.NewOrganizationMembershipStore(mock),
	}

	nextCalled := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nextCalled = true
		require.Equal(t, testOrgA, OrgIDFromContext(r.Context()), "fall-through should resolve to session hint org")
		w.WriteHeader(http.StatusOK)
	})

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer t")
	req.Header.Set(ActiveOrgHeader, testOrgB.String())
	w := httptest.NewRecorder()

	Auth(stores, nil, zerolog.Nop())(next).ServeHTTP(w, req)

	require.True(t, nextCalled, "fall-through should let the handler run")
	require.Equal(t, http.StatusOK, w.Code)
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
// unwrapped so handleToken surfaces a 500 rather than silently falling back
// to a different org. The 500 is distinct from the graceful-degrade path
// (stale header → falls through) so operators can separate real outages
// from "not a member".
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

	require.Equal(t, http.StatusInternalServerError, w.Code)
	require.Contains(t, w.Body.String(), "MEMBERSHIP_RESOLUTION_FAILED")
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

// When OldestForUser errors (not ErrNoRows), the middleware surfaces 500
// rather than pretending the user has no memberships. Distinct from the
// graceful-degrade paths so operators can spot real DB outages.
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

	require.Equal(t, http.StatusInternalServerError, w.Code)
	require.Contains(t, w.Body.String(), "MEMBERSHIP_RESOLUTION_FAILED")
	require.NoError(t, mock.ExpectationsWereMet())
}

// When the session's last_org_id hint is for a different org and that lookup
// fails with a non-ErrNoRows error, the middleware surfaces a 500 rather than
// silently falling back to OldestForUser (which would pick the wrong org).
// The DB-level failure is infrastructure, not authorization — distinguishing it
// from 403 keeps ops alerting tight.
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

	require.Equal(t, http.StatusInternalServerError, w.Code)
	require.Contains(t, w.Body.String(), "MEMBERSHIP_RESOLUTION_FAILED")
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

// TestAuth_HeaderForUnrelatedOrg_EmitsRevokedHeader covers the signaling
// side of graceful-degrade: when the X-Active-Org-ID header names an org the
// user no longer belongs to, the middleware sets the X-Org-Membership-Revoked
// response header so the client can refresh its cached active-org state
// instead of sending the stale header on every subsequent request. The value
// is an opaque flag (see RevokedOrgHeader doc) — the client knows which org
// it requested and the server does not need to re-confirm the UUID.
func TestAuth_HeaderForUnrelatedOrg_EmitsRevokedHeader(t *testing.T) {
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
	// Header lookup returns no rows — user isn't in OrgB.
	mock.ExpectQuery("SELECT .+ FROM organization_memberships").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(memberCols))
	// Fall-through to session hint succeeds for OrgA.
	mock.ExpectQuery("SELECT .+ FROM organization_memberships WHERE user_id .+ AND org_id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(memberCols).
				AddRow(testUserID, testOrgA, "admin", now),
		)

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
		w.WriteHeader(http.StatusOK)
	})).ServeHTTP(w, req)

	require.Equal(t, http.StatusOK, w.Code)
	require.Equal(t, RevokedOrgHeaderValue, w.Header().Get(RevokedOrgHeader), "should advertise revocation as an opaque flag so client refreshes state")
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestAuth_MalformedHeaderValue_FallsThrough covers the graceful-degrade
// path for an unparseable X-Active-Org-ID — instead of 400/403, we log the
// malformed value and fall through to the session hint and then the user's
// oldest membership. A client with corrupted local state would otherwise be
// hard-locked out of every request until it discovered its own bad header.
func TestAuth_MalformedHeaderValue_FallsThrough(t *testing.T) {
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
	// Session-hint lookup succeeds: user is in OrgA.
	mock.ExpectQuery("SELECT .+ FROM organization_memberships WHERE user_id .+ AND org_id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(memberCols).
				AddRow(testUserID, testOrgA, "admin", now),
		)

	stores := AuthStores{
		Sessions:    db.NewAuthSessionStore(mock),
		Users:       db.NewUserStore(mock),
		Memberships: db.NewOrganizationMembershipStore(mock),
	}

	nextCalled := false
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer t")
	req.Header.Set(ActiveOrgHeader, "not-a-uuid")
	w := httptest.NewRecorder()

	Auth(stores, nil, zerolog.Nop())(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nextCalled = true
		require.Equal(t, testOrgA, OrgIDFromContext(r.Context()), "malformed header should fall through to session hint")
		w.WriteHeader(http.StatusOK)
	})).ServeHTTP(w, req)

	require.True(t, nextCalled, "malformed header should not block the request")
	require.Equal(t, http.StatusOK, w.Code)
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestAuth_SessionLookupTransientError_Returns503 pins the 503 path for a
// non-ErrNoRows failure from the session store. Clearing the cookie on every
// transient DB blip (pool exhaustion, connection reset during rolling deploy)
// would drop the browser session and trigger a logout in the frontend; 503
// lets the client retry without losing its login.
func TestAuth_SessionLookupTransientError_Returns503(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	mock.ExpectQuery("SELECT .+ FROM auth_sessions").
		WithArgs(pgxmock.AnyArg()).
		WillReturnError(errDBDown)

	stores := AuthStores{
		Sessions:    db.NewAuthSessionStore(mock),
		Users:       db.NewUserStore(mock),
		Memberships: db.NewOrganizationMembershipStore(mock),
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: "session_token", Value: "t"})
	w := httptest.NewRecorder()

	Auth(stores, nil, zerolog.Nop())(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("next should not run when session lookup fails transiently")
	})).ServeHTTP(w, req)

	require.Equal(t, http.StatusServiceUnavailable, w.Code)
	for _, c := range w.Result().Cookies() {
		if c.Name == SessionCookieName && c.MaxAge < 0 {
			t.Fatalf("transient session-lookup error must not clear the cookie, got %+v", c)
		}
	}
	require.Contains(t, w.Body.String(), "SERVICE_UNAVAILABLE")
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestAuth_UserLookupTransientError_Returns503 pins the same 503 behavior for
// a non-ErrNoRows failure from the user store, after the session row was
// fetched successfully. Mirrors the session-lookup case so ops can
// distinguish real outages from invalid-session 401s.
func TestAuth_UserLookupTransientError_Returns503(t *testing.T) {
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
		WillReturnError(errDBDown)

	stores := AuthStores{
		Sessions:    db.NewAuthSessionStore(mock),
		Users:       db.NewUserStore(mock),
		Memberships: db.NewOrganizationMembershipStore(mock),
	}

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: "session_token", Value: "t"})
	w := httptest.NewRecorder()

	Auth(stores, nil, zerolog.Nop())(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Fatal("next should not run when user lookup fails transiently")
	})).ServeHTTP(w, req)

	require.Equal(t, http.StatusServiceUnavailable, w.Code)
	for _, c := range w.Result().Cookies() {
		if c.Name == SessionCookieName && c.MaxAge < 0 {
			t.Fatalf("transient user-lookup error must not clear the cookie, got %+v", c)
		}
	}
	require.Contains(t, w.Body.String(), "SERVICE_UNAVAILABLE")
	require.NoError(t, mock.ExpectationsWereMet())
}
