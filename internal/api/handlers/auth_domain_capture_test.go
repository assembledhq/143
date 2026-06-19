package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/config"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
)

// TestGoogleUserEmailVerifiedJSONTag pins the userinfo JSON contract: the
// whole Google capture path hangs off this one field, and a typo'd tag
// would silently disable it (email_verified would always be false).
func TestGoogleUserEmailVerifiedJSONTag(t *testing.T) {
	t.Parallel()

	var u googleUser
	require.NoError(t, json.Unmarshal([]byte(`{"sub":"s","email":"a@b.com","email_verified":true,"name":"A"}`), &u))
	require.True(t, u.EmailVerified, "email_verified must round-trip from Google userinfo JSON")

	var u2 googleUser
	require.NoError(t, json.Unmarshal([]byte(`{"sub":"s","email":"a@b.com"}`), &u2))
	require.False(t, u2.EmailVerified, "absent claim must default to unverified")
}

func TestMarkGitHubEmailVerified_TriState(t *testing.T) {
	t.Parallel()

	t.Run("nil emails list leaves watermark untouched", func(t *testing.T) {
		t.Parallel()
		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		// No expectations: a failed /user/emails fetch carries no
		// information and must not write (a transient GitHub blip must not
		// wipe a legitimate stamp).
		handler := NewAuthHandler(&config.Config{}, mock, db.NewUserStore(mock), nil, nil, nil)
		req := httptest.NewRequest(http.MethodGet, "/cb", nil)
		handler.markGitHubEmailVerified(req, uuid.New(), nil, "a@assembledhq.com")
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("affirmatively unverified email clears the watermark", func(t *testing.T) {
		t.Parallel()
		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		mock.ExpectExec("UPDATE users SET email_verified_at").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnResult(pgxmock.NewResult("UPDATE", 1))

		handler := NewAuthHandler(&config.Config{}, mock, db.NewUserStore(mock), nil, nil, nil)
		req := httptest.NewRequest(http.MethodGet, "/cb", nil)
		handler.markGitHubEmailVerified(req, uuid.New(),
			[]gitHubEmail{{Email: "a@assembledhq.com", Verified: false}}, "a@assembledhq.com")
		require.NoError(t, mock.ExpectationsWereMet())
	})
}

func TestSelectGitHubAutoJoinEmail(t *testing.T) {
	t.Parallel()

	expectDomainHit := func(mock pgxmock.PgxPoolIface, domain string, orgID uuid.UUID) {
		mock.ExpectQuery("SELECT d.org_id, o.name AS org_name, d.domain").
			WithArgs(domain).
			WillReturnRows(pgxmock.NewRows([]string{"org_id", "org_name", "domain"}).
				AddRow(orgID, "Assembled", domain))
	}
	expectDomainMiss := func(mock pgxmock.PgxPoolIface, domain string) {
		mock.ExpectQuery("SELECT d.org_id, o.name AS org_name, d.domain").
			WithArgs(domain).
			WillReturnRows(pgxmock.NewRows([]string{"org_id", "org_name", "domain"}))
	}

	t.Run("verified profile email on captured domain wins", func(t *testing.T) {
		t.Parallel()
		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()
		expectDomainHit(mock, "assembledhq.com", uuid.New())

		handler := NewAuthHandler(&config.Config{}, mock, db.NewUserStore(mock), nil, nil, nil)
		handler.SetOrgDomainStore(db.NewOrganizationDomainStore(mock))

		got, ok := handler.selectGitHubAutoJoinEmail(context.Background(), "alice@assembledhq.com",
			[]gitHubEmail{{Email: "alice@assembledhq.com", Primary: true, Verified: true}})
		require.True(t, ok)
		require.Equal(t, "alice@assembledhq.com", got)
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("private profile email falls through to verified work address", func(t *testing.T) {
		t.Parallel()
		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		// The alternate work address matches the captured domain and no
		// existing account owns it.
		expectDomainHit(mock, "assembledhq.com", uuid.New())
		mock.ExpectQuery(`(?s)SELECT .+ FROM users WHERE LOWER\(email\) = LOWER\(@email\)`).
			WithArgs("alice@assembledhq.com").
			WillReturnRows(pgxmock.NewRows([]string{"id", "org_id", "email", "name", "role", "github_id", "github_login", "github_noreply_email", "avatar_url", "password_hash", "google_id", "created_at"}))

		handler := NewAuthHandler(&config.Config{}, mock, db.NewUserStore(mock), nil, nil, nil)
		handler.SetOrgDomainStore(db.NewOrganizationDomainStore(mock))

		// Profile email is the noreply fallback (private email setting) —
		// the common engineer case.
		noreply := "42+alicehub@users.noreply.github.com"
		got, ok := handler.selectGitHubAutoJoinEmail(context.Background(), noreply, []gitHubEmail{
			{Email: noreply, Verified: true},
			{Email: "alice@assembledhq.com", Primary: true, Verified: true},
		})
		require.True(t, ok)
		require.Equal(t, "alice@assembledhq.com", got, "the verified work address should be selected over the noreply profile email")
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("unverified work address never selected", func(t *testing.T) {
		t.Parallel()
		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		handler := NewAuthHandler(&config.Config{}, mock, db.NewUserStore(mock), nil, nil, nil)
		handler.SetOrgDomainStore(db.NewOrganizationDomainStore(mock))

		_, ok := handler.selectGitHubAutoJoinEmail(context.Background(), "",
			[]gitHubEmail{{Email: "alice@assembledhq.com", Primary: true, Verified: false}})
		require.False(t, ok, "unverified addresses must not trigger capture, and no domain query should run for them")
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("address owned by existing account is skipped", func(t *testing.T) {
		t.Parallel()
		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		userColumns := []string{"id", "org_id", "email", "name", "role", "github_id", "github_login", "github_noreply_email", "avatar_url", "password_hash", "google_id", "created_at"}
		expectDomainHit(mock, "assembledhq.com", uuid.New())
		mock.ExpectQuery(`(?s)SELECT .+ FROM users WHERE LOWER\(email\) = LOWER\(@email\)`).
			WithArgs("alice@assembledhq.com").
			WillReturnRows(pgxmock.NewRows(userColumns).
				AddRow(uuid.New(), uuid.New(), "alice@assembledhq.com", "Existing", "member", nil, nil, nil, nil, nil, nil, time.Now()))

		handler := NewAuthHandler(&config.Config{}, mock, db.NewUserStore(mock), nil, nil, nil)
		handler.SetOrgDomainStore(db.NewOrganizationDomainStore(mock))

		_, ok := handler.selectGitHubAutoJoinEmail(context.Background(), "",
			[]gitHubEmail{{Email: "alice@assembledhq.com", Primary: true, Verified: true}})
		require.False(t, ok, "must not merge into an address another account already owns")
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("uncaptured domain returns nothing", func(t *testing.T) {
		t.Parallel()
		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()
		expectDomainMiss(mock, "elsewhere.com")

		handler := NewAuthHandler(&config.Config{}, mock, db.NewUserStore(mock), nil, nil, nil)
		handler.SetOrgDomainStore(db.NewOrganizationDomainStore(mock))

		_, ok := handler.selectGitHubAutoJoinEmail(context.Background(), "",
			[]gitHubEmail{{Email: "bob@elsewhere.com", Primary: true, Verified: true}})
		require.False(t, ok)
		require.NoError(t, mock.ExpectationsWereMet())
	})
}

// TestTryDomainAutoJoin_FallsBackOnFailure pins the availability guarantee:
// a broken auto-join transaction must degrade to "no capture" so the caller
// proceeds with the classic fresh-org signup instead of failing the login.
func TestTryDomainAutoJoin_FallsBackOnFailure(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	orgID := uuid.New()
	mock.ExpectQuery("SELECT d.org_id, o.name AS org_name, d.domain").
		WithArgs("assembledhq.com").
		WillReturnRows(pgxmock.NewRows([]string{"org_id", "org_name", "domain"}).
			AddRow(orgID, "Assembled", "assembledhq.com"))
	mock.ExpectBegin().WillReturnError(context.DeadlineExceeded)

	handler := NewAuthHandler(&config.Config{}, mock, db.NewUserStore(mock), nil, nil, nil)
	handler.SetOrgDomainStore(db.NewOrganizationDomainStore(mock))

	user := &models.User{Email: "alice@assembledhq.com", Name: "Alice"}
	req := httptest.NewRequest(http.MethodGet, "/cb", nil)
	_, ok := handler.tryDomainAutoJoin(req, user, true, func(ctx context.Context, us *db.UserStore, u *models.User) error {
		return nil
	})
	require.False(t, ok, "a failed auto-join must report no-capture, not an error")
	require.Equal(t, uuid.Nil, user.OrgID, "legacy org field must be reset for the fresh-org fallback")
	require.Equal(t, models.Role(""), user.Role, "legacy role field must be reset for the fresh-org fallback")
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestAuthHandler_Callback_LinkBlockedForUnverifiedEmail pins the linking
// gate: an existing account matched by email must not be linkable (= full
// account access) when GitHub does not attest the address.
func TestAuthHandler_Callback_LinkBlockedForUnverifiedEmail(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	now := time.Now()
	userColumns := []string{"id", "org_id", "email", "name", "role", "github_id", "github_login", "github_noreply_email", "avatar_url", "password_hash", "google_id", "created_at"}

	mock.ExpectQuery("SELECT .* FROM users WHERE github_id").
		WithArgs(int64(42)).
		WillReturnRows(pgxmock.NewRows(userColumns))
	// Email match hits an existing (password) account…
	mock.ExpectQuery(`(?s)SELECT .+ FROM users WHERE LOWER\(email\) = LOWER\(@email\)`).
		WithArgs("victim@assembledhq.com").
		WillReturnRows(pgxmock.NewRows(userColumns).
			AddRow(uuid.New(), uuid.New(), "victim@assembledhq.com", "Victim", "admin", nil, nil, nil, nil, strPtr("hash"), nil, now))
	// …and NO further queries: the link must be refused before any write.

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/login/oauth/access_token":
			_, _ = w.Write([]byte(`{"access_token":"ghu_token","token_type":"bearer","scope":"repo,user:email"}`))
		case "/user":
			_, _ = w.Write([]byte(`{"id":42,"login":"attacker","name":"Attacker","email":"victim@assembledhq.com","avatar_url":""}`))
		case "/user/emails":
			// The claimed address is conspicuously NOT in the verified list.
			_, _ = w.Write([]byte(`[{"email":"42+attacker@users.noreply.github.com","verified":true}]`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	handler := NewAuthHandler(&config.Config{FrontendURL: "http://frontend.test"}, mock, db.NewUserStore(mock), db.NewAuthSessionStore(mock), nil, nil)
	handler.SetGitHubURLsForTest(server.URL, server.URL, server.Client())

	req := httptest.NewRequest(http.MethodGet, "/api/v1/auth/github/callback?state=valid-state&code=test-code", nil)
	req.AddCookie(&http.Cookie{Name: "github_oauth_state", Value: "valid-state"})
	w := httptest.NewRecorder()

	handler.Callback(w, req)
	require.Equal(t, http.StatusForbidden, w.Code, "unattested email must not link into an existing account")
	require.Contains(t, w.Body.String(), "EMAIL_NOT_VERIFIED")
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestResolveExistingGitHubEmail pins identity stickiness: an account whose
// email is a GitHub-verified address on a company-verified domain keeps it
// across logins; everyone else gets today's refresh-from-profile behavior.
func TestResolveExistingGitHubEmail(t *testing.T) {
	t.Parallel()

	const work = "alice@assembledhq.com"
	noreply := "42+alicehub@users.noreply.github.com"

	t.Run("captured work identity survives a noreply profile email", func(t *testing.T) {
		t.Parallel()
		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		mock.ExpectQuery("SELECT EXISTS").
			WithArgs("assembledhq.com").
			WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(true))

		handler := NewAuthHandler(&config.Config{}, mock, db.NewUserStore(mock), nil, nil, nil)
		handler.SetOrgDomainStore(db.NewOrganizationDomainStore(mock))

		got := handler.resolveExistingGitHubEmail(context.Background(), work, noreply,
			[]gitHubEmail{{Email: work, Primary: true, Verified: true}, {Email: noreply, Verified: true}})
		require.Equal(t, work, got, "the captured identity must not revert to the noreply address on later logins")
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("protection drops when GitHub stops attesting the stored address", func(t *testing.T) {
		t.Parallel()
		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		// No domain query: an unattested stored address never reaches the
		// DB check (e.g. the work email was removed after offboarding).
		handler := NewAuthHandler(&config.Config{}, mock, db.NewUserStore(mock), nil, nil, nil)
		handler.SetOrgDomainStore(db.NewOrganizationDomainStore(mock))

		got := handler.resolveExistingGitHubEmail(context.Background(), work, noreply,
			[]gitHubEmail{{Email: noreply, Verified: true}})
		require.Equal(t, noreply, got)
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("non-captured domains keep the refresh behavior", func(t *testing.T) {
		t.Parallel()
		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		mock.ExpectQuery("SELECT EXISTS").
			WithArgs("elsewhere.example").
			WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(false))

		handler := NewAuthHandler(&config.Config{}, mock, db.NewUserStore(mock), nil, nil, nil)
		handler.SetOrgDomainStore(db.NewOrganizationDomainStore(mock))

		got := handler.resolveExistingGitHubEmail(context.Background(), "bob@elsewhere.example", "bob-new@personal.example",
			[]gitHubEmail{
				{Email: "bob@elsewhere.example", Verified: true},
				{Email: "bob-new@personal.example", Primary: true, Verified: true},
			})
		require.Equal(t, "bob-new@personal.example", got, "ordinary profile-email changes must still refresh")
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("public-domain stored email short-circuits without a DB call", func(t *testing.T) {
		t.Parallel()
		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		handler := NewAuthHandler(&config.Config{}, mock, db.NewUserStore(mock), nil, nil, nil)
		handler.SetOrgDomainStore(db.NewOrganizationDomainStore(mock))

		got := handler.resolveExistingGitHubEmail(context.Background(), "old@gmail.com", work,
			[]gitHubEmail{{Email: "old@gmail.com", Verified: true}, {Email: work, Primary: true, Verified: true}})
		require.Equal(t, work, got)
		require.NoError(t, mock.ExpectationsWereMet())
	})

	t.Run("nil emails list falls back to refresh", func(t *testing.T) {
		t.Parallel()
		mock, err := pgxmock.NewPool()
		require.NoError(t, err)
		defer mock.Close()

		handler := NewAuthHandler(&config.Config{}, mock, db.NewUserStore(mock), nil, nil, nil)
		handler.SetOrgDomainStore(db.NewOrganizationDomainStore(mock))

		got := handler.resolveExistingGitHubEmail(context.Background(), work, noreply, nil)
		require.Equal(t, noreply, got, "a failed /user/emails fetch must not freeze a possibly-stale identity")
		require.NoError(t, mock.ExpectationsWereMet())
	})
}
