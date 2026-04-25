package identity

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
)

// stubInstallationTokens is a programmable InstallationTokenSource for tests.
type stubInstallationTokens struct {
	tokens map[int64]string
	errs   map[int64]error
}

func (s *stubInstallationTokens) GetInstallationToken(_ context.Context, installationID int64) (string, error) {
	if err, ok := s.errs[installationID]; ok {
		return "", err
	}
	if tok, ok := s.tokens[installationID]; ok {
		return tok, nil
	}
	return "", errors.New("no token configured for installation")
}

// stubAppUserAuth is a programmable AppUserAuthService for tests.
type stubAppUserAuth struct {
	cfg *models.GitHubAppUserConfig
	err error
}

func (s *stubAppUserAuth) GetValidCredential(_ context.Context, _, _ uuid.UUID) (*models.GitHubAppUserConfig, error) {
	return s.cfg, s.err
}

// statusError exposes an HTTP status code via the structural HTTPStatus
// interface that the resolver uses to detect retry-able 404s — mirrors the
// shape of *github.githubAPIError without importing the github package.
type statusError struct {
	code int
}

func (e *statusError) Error() string   { return "github status error" }
func (e *statusError) HTTPStatus() int { return e.code }

func TestResolve_AppOnly(t *testing.T) {
	t.Parallel()

	r := NewResolver(&stubInstallationTokens{tokens: map[int64]string{1: "app-token-123"}}, zerolog.Nop())

	repo := &models.Repository{InstallationID: 1, FullName: "owner/repo"}
	settings := models.OrgSettings{PRAuthorship: models.PRAuthorshipAppOnly}
	run := &models.Session{ID: uuid.New(), OrgID: uuid.New()}

	res, err := r.Resolve(context.Background(), run, repo, settings, "")
	require.NoError(t, err)
	require.Equal(t, "app-token-123", res.Token)
	require.False(t, res.IsUserToken())
	require.Equal(t, "app", res.AuthoredBy())
}

func TestResolve_UserPreferred_NoUser(t *testing.T) {
	t.Parallel()

	r := NewResolver(&stubInstallationTokens{tokens: map[int64]string{1: "app-token-fallback"}}, zerolog.Nop())

	repo := &models.Repository{InstallationID: 1}
	settings := models.OrgSettings{PRAuthorship: models.PRAuthorshipUserPreferred}
	run := &models.Session{ID: uuid.New(), OrgID: uuid.New()} // no TriggeredByUserID

	res, err := r.Resolve(context.Background(), run, repo, settings, "")
	require.NoError(t, err)
	require.Equal(t, "app-token-fallback", res.Token)
	require.False(t, res.IsUserToken())
}

func TestResolve_UserRequired_NoUser(t *testing.T) {
	t.Parallel()

	r := NewResolver(&stubInstallationTokens{}, zerolog.Nop())

	repo := &models.Repository{InstallationID: 1}
	settings := models.OrgSettings{PRAuthorship: models.PRAuthorshipUserRequired}
	run := &models.Session{ID: uuid.New(), OrgID: uuid.New()}

	_, err := r.Resolve(context.Background(), run, repo, settings, "")
	require.Error(t, err, "should fail when user_required but no user token")
	require.Contains(t, err.Error(), "org requires user GitHub auth")
}

func TestResolve_AuthorModeAppUsesInstallationToken(t *testing.T) {
	t.Parallel()

	r := NewResolver(&stubInstallationTokens{tokens: map[int64]string{1: "app-token"}}, zerolog.Nop())

	res, err := r.Resolve(
		context.Background(),
		&models.Session{ID: uuid.New(), OrgID: uuid.New()},
		&models.Repository{InstallationID: 1},
		models.OrgSettings{},
		"app",
	)
	require.NoError(t, err)
	require.Equal(t, "app-token", res.Token)
	require.False(t, res.IsUserToken())
}

func TestResolve_UsesGitHubAppUserCredential(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/owner/repo":
			require.Equal(t, "token ghu_user_token", r.Header.Get("Authorization"), "repo access probe should use the user token")
			_, _ = w.Write([]byte(`{"full_name":"owner/repo"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	orgID := uuid.New()
	userID := uuid.New()

	r := NewResolver(&stubInstallationTokens{}, zerolog.Nop())
	r.SetAppUserAuth(&stubAppUserAuth{
		cfg: &models.GitHubAppUserConfig{
			AccessToken:           "ghu_user_token",
			RefreshToken:          "ghr_refresh",
			ExpiresAt:             time.Now().Add(time.Hour),
			RefreshTokenExpiresAt: time.Now().Add(24 * time.Hour),
		},
	})
	r.SetHTTPClient(server.Client())
	r.SetAPIBaseURL(server.URL)

	repo := &models.Repository{InstallationID: 1, FullName: "owner/repo"}
	settings := models.OrgSettings{PRAuthorship: models.PRAuthorshipUserPreferred}
	run := &models.Session{ID: uuid.New(), OrgID: orgID, TriggeredByUserID: &userID}

	res, err := r.Resolve(context.Background(), run, repo, settings, "")
	require.NoError(t, err)
	require.Equal(t, "ghu_user_token", res.Token)
	require.True(t, res.IsUserToken())
	require.Equal(t, "user", res.AuthoredBy())
}

func TestResolve_UserPreferredFallsBackWhenUserTokenCannotAccessRepo(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/repos/owner/repo":
			w.WriteHeader(http.StatusForbidden)
			_, _ = w.Write([]byte(`{"message":"Resource not accessible by integration"}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	orgID := uuid.New()
	userID := uuid.New()
	repo := &models.Repository{InstallationID: 1, FullName: "owner/repo"}
	settings := models.OrgSettings{PRAuthorship: models.PRAuthorshipUserPreferred}
	run := &models.Session{ID: uuid.New(), OrgID: orgID, TriggeredByUserID: &userID}

	r := NewResolver(&stubInstallationTokens{tokens: map[int64]string{1: "app-token-fallback"}}, zerolog.Nop())
	r.SetAppUserAuth(&stubAppUserAuth{
		cfg: &models.GitHubAppUserConfig{
			AccessToken:           "ghu_user_token",
			RefreshToken:          "ghr_refresh",
			ExpiresAt:             time.Now().Add(time.Hour),
			RefreshTokenExpiresAt: time.Now().Add(24 * time.Hour),
		},
	})
	r.SetHTTPClient(server.Client())
	r.SetAPIBaseURL(server.URL)

	res, err := r.Resolve(context.Background(), run, repo, settings, "")
	require.NoError(t, err, "user_preferred should fall back to the app token when the user token cannot access the repo")
	require.Equal(t, "app-token-fallback", res.Token)
	require.False(t, res.IsUserToken())
}

func TestResolve_UserRequiredErrorsWhenUserTokenCannotAccessRepo(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(`{"message":"Resource not accessible by integration"}`))
	}))
	defer server.Close()

	orgID := uuid.New()
	userID := uuid.New()
	repo := &models.Repository{InstallationID: 1, FullName: "owner/repo"}
	settings := models.OrgSettings{PRAuthorship: models.PRAuthorshipUserRequired}
	run := &models.Session{ID: uuid.New(), OrgID: orgID, TriggeredByUserID: &userID}

	r := NewResolver(&stubInstallationTokens{}, zerolog.Nop())
	r.SetAppUserAuth(&stubAppUserAuth{
		cfg: &models.GitHubAppUserConfig{
			AccessToken:           "ghu_user_token",
			RefreshToken:          "ghr_refresh",
			ExpiresAt:             time.Now().Add(time.Hour),
			RefreshTokenExpiresAt: time.Now().Add(24 * time.Hour),
		},
	})
	r.SetHTTPClient(server.Client())
	r.SetAPIBaseURL(server.URL)

	_, err := r.Resolve(context.Background(), run, repo, settings, "")
	require.Error(t, err, "user_required should fail when the user token cannot access the target repo")
	require.Contains(t, err.Error(), "cannot access repo")
}

func TestResolve_UserResolutionAttachesUser(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"full_name":"owner/repo"}`))
	}))
	defer server.Close()

	orgID := uuid.New()
	userID := uuid.New()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()
	githubID := int64(123)
	githubLogin := "alicehub"
	noreply := "123+alicehub@users.noreply.github.com"
	mock.ExpectQuery("SELECT .* FROM users WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "org_id", "email", "name", "role", "github_id", "github_login", "github_noreply_email", "avatar_url", "password_hash", "google_id", "created_at"}).
			AddRow(userID, orgID, "alice@example.com", "Alice", "member", &githubID, &githubLogin, &noreply, (*string)(nil), (*string)(nil), (*string)(nil), time.Now()))

	r := NewResolver(&stubInstallationTokens{}, zerolog.Nop())
	r.SetAppUserAuth(&stubAppUserAuth{
		cfg: &models.GitHubAppUserConfig{
			AccessToken: "ghu_user_token",
			ExpiresAt:   time.Now().Add(time.Hour),
		},
	})
	r.SetUsers(db.NewUserStore(mock))
	r.SetHTTPClient(server.Client())
	r.SetAPIBaseURL(server.URL)

	repo := &models.Repository{InstallationID: 1, FullName: "owner/repo"}
	run := &models.Session{ID: uuid.New(), OrgID: orgID, TriggeredByUserID: &userID}

	res, err := r.Resolve(context.Background(), run, repo, models.OrgSettings{PRAuthorship: models.PRAuthorshipUserPreferred}, "")
	require.NoError(t, err)
	require.NotNil(t, res.User, "resolver should attach the triggering user when the user store is wired")
	require.Equal(t, "Alice", res.User.Name)
	require.NotNil(t, res.User.GitHubNoreplyEmail)
	require.Equal(t, "123+alicehub@users.noreply.github.com", *res.User.GitHubNoreplyEmail)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestResolve_FallsBackToIntegrationInstallationWhenRepoInstallationMissing(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	integrationID := uuid.New()
	mock.ExpectQuery("SELECT id, org_id, provider, config, status, last_synced_at, created_at FROM integrations WHERE id").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "org_id", "provider", "config", "status", "last_synced_at", "created_at"}).
			AddRow(integrationID, uuid.New(), "github", []byte(`{"installation_id":2}`), "active", nil, time.Now()))

	r := NewResolver(&stubInstallationTokens{tokens: map[int64]string{2: "fallback-token"}}, zerolog.Nop())
	r.SetIntegrations(db.NewIntegrationStore(mock))

	repo := &models.Repository{InstallationID: 0, IntegrationID: integrationID}
	settings := models.OrgSettings{PRAuthorship: models.PRAuthorshipUserPreferred}
	run := &models.Session{ID: uuid.New(), OrgID: uuid.New()}

	res, err := r.Resolve(context.Background(), run, repo, settings, "")
	require.NoError(t, err)
	require.Equal(t, "fallback-token", res.Token)
	require.False(t, res.IsUserToken())
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestResolve_FallsBackToIntegrationInstallationWhenRepoInstallationIsStale(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	integrationID := uuid.New()
	mock.ExpectQuery("SELECT id, org_id, provider, config, status, last_synced_at, created_at FROM integrations WHERE id").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"id", "org_id", "provider", "config", "status", "last_synced_at", "created_at"}).
			AddRow(integrationID, uuid.New(), "github", []byte(`{"installation_id":2}`), "active", nil, time.Now()))

	r := NewResolver(&stubInstallationTokens{
		tokens: map[int64]string{2: "fallback-token"},
		errs:   map[int64]error{1: &statusError{code: http.StatusNotFound}},
	}, zerolog.Nop())
	r.SetIntegrations(db.NewIntegrationStore(mock))

	repo := &models.Repository{InstallationID: 1, IntegrationID: integrationID}
	settings := models.OrgSettings{PRAuthorship: models.PRAuthorshipUserPreferred}
	run := &models.Session{ID: uuid.New(), OrgID: uuid.New()}

	res, err := r.Resolve(context.Background(), run, repo, settings, "")
	require.NoError(t, err, "Resolve should retry against the integration installation_id when the repo's id 404s")
	require.Equal(t, "fallback-token", res.Token)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestResolve_PrimaryNon404ErrorReturnsImmediately(t *testing.T) {
	t.Parallel()

	r := NewResolver(&stubInstallationTokens{
		errs: map[int64]error{1: errors.New("bad credentials")},
	}, zerolog.Nop())

	_, err := r.Resolve(context.Background(),
		&models.Session{ID: uuid.New(), OrgID: uuid.New()},
		&models.Repository{InstallationID: 1, FullName: "owner/repo"},
		models.OrgSettings{}, "",
	)
	require.Error(t, err, "non-404 errors from the primary installation should not retry against the integration")
	require.Contains(t, err.Error(), "bad credentials")
}

func TestUserTokenCanAccessRepo_ErrorPaths(t *testing.T) {
	t.Parallel()

	t.Run("invalid repo name", func(t *testing.T) {
		t.Parallel()
		r := NewResolver(&stubInstallationTokens{}, zerolog.Nop())
		ok, err := r.userTokenCanAccessRepo(context.Background(), "token", "/repo")
		require.False(t, ok)
		require.Error(t, err)
	})

	t.Run("server error", func(t *testing.T) {
		t.Parallel()
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
			_, _ = w.Write([]byte(`{"message":"boom"}`))
		}))
		defer server.Close()

		r := NewResolver(&stubInstallationTokens{}, zerolog.Nop())
		r.SetHTTPClient(server.Client())
		r.SetAPIBaseURL(server.URL)
		ok, err := r.userTokenCanAccessRepo(context.Background(), "token", "owner/repo")
		require.False(t, ok)
		require.Error(t, err)
	})
}

func TestIntegrationInstallationID(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		integration *models.Integration
		expected    int64
		expectErr   bool
	}{
		{name: "nil integration", expectErr: true},
		{name: "empty config", integration: &models.Integration{}, expectErr: true},
		{name: "invalid config", integration: &models.Integration{Config: []byte(`{`)}, expectErr: true},
		{name: "missing installation id", integration: &models.Integration{Config: []byte(`{"installation_id":0}`)}, expectErr: true},
		{name: "valid installation id", integration: &models.Integration{Config: []byte(`{"installation_id":42}`)}, expected: 42},
	}
	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := integrationInstallationID(tt.integration)
			if tt.expectErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, tt.expected, got)
		})
	}
}

func TestCommitIdentity(t *testing.T) {
	t.Parallel()

	t.Run("nil resolution falls back to bot", func(t *testing.T) {
		t.Parallel()
		name, email := CommitIdentity(nil)
		require.Equal(t, "143 Agent", name)
		require.Equal(t, "noreply@143.dev", email)
	})

	t.Run("app token falls back to bot", func(t *testing.T) {
		t.Parallel()
		name, email := CommitIdentity(&Resolution{Source: SourceApp})
		require.Equal(t, "143 Agent", name)
		require.Equal(t, "noreply@143.dev", email)
	})

	t.Run("user token without noreply uses contact email", func(t *testing.T) {
		t.Parallel()
		name, email := CommitIdentity(&Resolution{
			Source: SourceUser,
			User:   &models.User{Name: "Alice", Email: "alice@example.com"},
		})
		require.Equal(t, "Alice", name)
		require.Equal(t, "alice@example.com", email)
	})

	t.Run("user token prefers noreply email", func(t *testing.T) {
		t.Parallel()
		noreply := "123+alicehub@users.noreply.github.com"
		name, email := CommitIdentity(&Resolution{
			Source: SourceUser,
			User: &models.User{
				Name:               "Alice",
				Email:              "alice@example.com",
				GitHubNoreplyEmail: &noreply,
			},
		})
		require.Equal(t, "Alice", name)
		require.Equal(t, noreply, email, "CommitIdentity should prefer the noreply email so commits link to the GitHub profile")
	})
}

func TestCoAuthorTrailer(t *testing.T) {
	t.Parallel()

	require.Equal(t, "", CoAuthorTrailer(nil), "CoAuthorTrailer should be empty when no user is supplied")

	noreply := "123+alicehub@users.noreply.github.com"
	got := CoAuthorTrailer(&models.User{
		Name:               "Alice",
		Email:              "alice@example.com",
		GitHubNoreplyEmail: &noreply,
	})
	require.Equal(t, "Co-authored-by: Alice <"+noreply+">", got)

	gotPlain := CoAuthorTrailer(&models.User{Name: "Bob", Email: "bob@example.com"})
	require.Equal(t, "Co-authored-by: Bob <bob@example.com>", gotPlain)
}

// TestRefreshTokenExpiredFallsBackToCredentialMissing exercises the
// resolver's behavior when the app user auth service returns the credential-
// missing sentinel — the resolver must fall through to the app installation
// token rather than surface the error.
func TestResolve_AppUserCredentialMissingFallsThrough(t *testing.T) {
	t.Parallel()

	r := NewResolver(&stubInstallationTokens{tokens: map[int64]string{1: "app-fallback"}}, zerolog.Nop())
	r.SetAppUserAuth(&stubAppUserAuth{err: pgx.ErrNoRows}) // any non-nil err short-circuits user path

	userID := uuid.New()
	res, err := r.Resolve(context.Background(),
		&models.Session{ID: uuid.New(), OrgID: uuid.New(), TriggeredByUserID: &userID},
		&models.Repository{InstallationID: 1, FullName: "owner/repo"},
		models.OrgSettings{PRAuthorship: models.PRAuthorshipUserPreferred},
		"",
	)
	require.NoError(t, err)
	require.Equal(t, "app-fallback", res.Token)
	require.False(t, res.IsUserToken())
}
