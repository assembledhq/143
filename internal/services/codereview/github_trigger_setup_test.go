package codereview

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	ghservice "github.com/assembledhq/143/internal/services/github"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func TestGitHubTriggerSetupService_Setup(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		authErr    error
		handler    func(t *testing.T, saved *bool) http.HandlerFunc
		expectErr  error
		expectSave bool
		expected   string
	}{
		{
			name: "creates missing team and grants pull access",
			handler: func(t *testing.T, saved *bool) http.HandlerFunc {
				return func(w http.ResponseWriter, r *http.Request) {
					require.Equal(t, "Bearer ghu_user", r.Header.Get("Authorization"), "GitHub setup should use the app user token")
					switch {
					case r.Method == http.MethodGet && r.URL.Path == "/orgs/acme/teams/143-code-reviewer":
						w.WriteHeader(http.StatusNotFound)
						_, _ = w.Write([]byte(`{"message":"Not Found"}`))
					case r.Method == http.MethodPost && r.URL.Path == "/orgs/acme/teams":
						var body map[string]any
						require.NoError(t, json.NewDecoder(r.Body).Decode(&body), "create team request should be valid JSON")
						require.Equal(t, models.DefaultCodeReviewGitHubTriggerTeamName, body["name"], "setup should create the expected team name")
						require.Equal(t, "closed", body["privacy"], "setup should create a closed trigger team")
						require.Equal(t, "notifications_disabled", body["notification_setting"], "setup should disable team notifications")
						w.WriteHeader(http.StatusCreated)
						_, _ = w.Write([]byte(`{"id":143,"name":"143 Code Reviewer","slug":"143-code-reviewer"}`))
					case r.Method == http.MethodPut && r.URL.Path == "/orgs/acme/teams/143-code-reviewer/repos/acme/api":
						var body map[string]any
						require.NoError(t, json.NewDecoder(r.Body).Decode(&body), "grant repository request should be valid JSON")
						require.Equal(t, "pull", body["permission"], "setup should grant least-privilege pull access")
						*saved = true
						w.WriteHeader(http.StatusNoContent)
					default:
						http.NotFound(w, r)
					}
				}
			},
			expectSave: true,
			expected:   "@acme/143-code-reviewer",
		},
		{
			name: "reuses existing team",
			handler: func(t *testing.T, saved *bool) http.HandlerFunc {
				return func(w http.ResponseWriter, r *http.Request) {
					switch {
					case r.Method == http.MethodGet && r.URL.Path == "/orgs/acme/teams/143-code-reviewer":
						_, _ = w.Write([]byte(`{"id":144,"name":"143 Code Reviewer","slug":"143-code-reviewer"}`))
					case r.Method == http.MethodPut && r.URL.Path == "/orgs/acme/teams/143-code-reviewer/repos/acme/api":
						*saved = true
						w.WriteHeader(http.StatusNoContent)
					default:
						http.NotFound(w, r)
					}
				}
			},
			expectSave: true,
			expected:   "@acme/143-code-reviewer",
		},
		{
			name:    "requires connected GitHub user",
			authErr: ghservice.ErrGitHubAppUserCredentialMissing,
			handler: func(t *testing.T, saved *bool) http.HandlerFunc {
				return func(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) }
			},
			expectErr: ErrGitHubTriggerAuthRequired,
		},
		{
			name: "surfaces permission failure before saving trigger",
			handler: func(t *testing.T, saved *bool) http.HandlerFunc {
				return func(w http.ResponseWriter, r *http.Request) {
					switch {
					case r.Method == http.MethodGet && r.URL.Path == "/orgs/acme/teams/143-code-reviewer":
						_, _ = w.Write([]byte(`{"id":144,"name":"143 Code Reviewer","slug":"143-code-reviewer"}`))
					case r.Method == http.MethodPut && r.URL.Path == "/orgs/acme/teams/143-code-reviewer/repos/acme/api":
						w.WriteHeader(http.StatusForbidden)
						_, _ = w.Write([]byte(`{"message":"Resource not accessible by integration"}`))
					default:
						http.NotFound(w, r)
					}
				}
			},
			expectErr: ErrGitHubTriggerPermissionRequired,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			orgID := uuid.New()
			userID := uuid.New()
			repoID := uuid.New()
			saved := false
			server := httptest.NewServer(tt.handler(t, &saved))
			defer server.Close()

			triggerStore := &githubTriggerSetupStoreStub{}
			repoStore := &githubTriggerRepoStoreStub{repo: models.Repository{
				ID:             repoID,
				OrgID:          orgID,
				FullName:       "acme/api",
				InstallationID: 123,
				Status:         models.RepositoryStatusActive,
			}}
			auth := &githubTriggerAuthStub{cfg: &models.GitHubAppUserConfig{AccessToken: "ghu_user"}, err: tt.authErr}
			svc := NewGitHubTriggerSetupService(triggerStore, repoStore, auth, testLogger())
			svc.SetAPIBaseURLForTest(server.URL)
			svc.SetHTTPClientForTest(server.Client())

			resp, err := svc.Setup(context.Background(), GitHubTriggerSetupInput{OrgID: orgID, UserID: userID, RepositoryID: repoID})

			if tt.expectErr != nil {
				require.ErrorIs(t, err, tt.expectErr, "Setup should return the expected classified error")
				require.False(t, triggerStore.saved, "failed setup should not persist a trigger")
				return
			}
			require.NoError(t, err, "Setup should create or repair the GitHub reviewer trigger")
			require.True(t, saved, "GitHub setup should grant repository access before persisting")
			require.Equal(t, tt.expectSave, triggerStore.saved, "successful setup should persist the trigger")
			require.Equal(t, models.CodeReviewGitHubTriggerStatusReady, resp.Status, "successful setup should return ready status")
			require.Equal(t, tt.expected, resp.TeamReviewer, "response should include the selectable team reviewer")
			require.Equal(t, models.DefaultCodeReviewGitHubTriggerRepoPerm, triggerStore.savedParams.RepoPermission, "trigger should persist pull access")
		})
	}
}

func TestGitHubTriggerSetupService_StatusAuthRequired(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	userID := uuid.New()
	repoID := uuid.New()
	svc := NewGitHubTriggerSetupService(
		&githubTriggerSetupStoreStub{getErr: pgx.ErrNoRows},
		&githubTriggerRepoStoreStub{repo: models.Repository{ID: repoID, OrgID: orgID, FullName: "acme/api", Status: models.RepositoryStatusActive}},
		&githubTriggerAuthStub{err: ghservice.ErrGitHubAppUserCredentialMissing},
		testLogger(),
	)

	resp, err := svc.Status(context.Background(), orgID, userID, repoID)

	require.NoError(t, err, "Status should not fail when the caller needs to connect GitHub")
	require.Equal(t, models.CodeReviewGitHubTriggerStatusAuthRequired, resp.Status, "Status should identify missing user authorization")
	require.Equal(t, "@acme/143-code-reviewer", resp.TeamReviewer, "Status should still expose the expected reviewer team")
}

func TestGitHubTriggerSetupService_ListStatus(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	userID := uuid.New()
	readyRepoID := uuid.New()
	availableRepoID := uuid.New()
	disconnectedRepoID := uuid.New()
	unconfiguredDisconnectedRepoID := uuid.New()
	setting := models.CodeReviewGitHubTriggerSetting{
		ID: uuid.New(), OrgID: orgID, RepositoryID: readyRepoID, Active: true,
		TeamSlug: "143-code-reviewer", TeamName: "143 Code Reviewer",
		RepoPermission: models.CodeReviewGitHubTriggerRepoPermissionPull,
	}
	disconnectedSetting := models.CodeReviewGitHubTriggerSetting{
		ID: uuid.New(), OrgID: orgID, RepositoryID: disconnectedRepoID, Active: true,
		TeamSlug: "143-code-reviewer", TeamName: "143 Code Reviewer",
		RepoPermission: models.CodeReviewGitHubTriggerRepoPermissionPull,
	}
	auth := &githubTriggerAuthStub{err: ghservice.ErrGitHubAppUserCredentialMissing}
	repoStore := &githubTriggerRepoStoreStub{repos: []models.Repository{
		{ID: readyRepoID, OrgID: orgID, FullName: "acme/api", Status: models.RepositoryStatusActive},
		{ID: availableRepoID, OrgID: orgID, FullName: "acme/web", Status: models.RepositoryStatusActive},
		{ID: disconnectedRepoID, OrgID: orgID, FullName: "acme/legacy", Status: models.RepositoryStatusDisconnected},
		{ID: unconfiguredDisconnectedRepoID, OrgID: orgID, FullName: "acme/old", Status: models.RepositoryStatusDisconnected},
	}}
	svc := NewGitHubTriggerSetupService(
		&githubTriggerSetupStoreStub{settings: []models.CodeReviewGitHubTriggerSetting{setting, disconnectedSetting}},
		repoStore,
		auth,
		testLogger(),
	)

	responses, err := svc.ListStatus(context.Background(), orgID, userID)

	require.NoError(t, err, "ListStatus should return actionable states when GitHub user authorization is missing")
	require.Equal(t, []models.CodeReviewGitHubTriggerResponse{
		withGitHubTriggerSetting(defaultGitHubTriggerResponse(models.Repository{ID: readyRepoID, OrgID: orgID, FullName: "acme/api", Status: models.RepositoryStatusActive}), setting),
		{
			Status: models.CodeReviewGitHubTriggerStatusAuthRequired, RepositoryID: availableRepoID,
			RepositoryFullName: "acme/web", RepositoryStatus: models.RepositoryStatusActive, GitHubOrg: "acme",
			TeamSlug: models.DefaultCodeReviewGitHubTriggerTeamSlug, TeamName: models.DefaultCodeReviewGitHubTriggerTeamName,
			TeamReviewer: "@acme/143-code-reviewer", RepoPermission: models.DefaultCodeReviewGitHubTriggerRepoPerm,
			Message: "Connect your GitHub account before creating the reviewer team.",
		},
		{
			Status: models.CodeReviewGitHubTriggerStatusDisconnected, RepositoryID: disconnectedRepoID,
			RepositoryFullName: "acme/legacy", RepositoryStatus: models.RepositoryStatusDisconnected, GitHubOrg: "acme",
			TeamSlug: models.DefaultCodeReviewGitHubTriggerTeamSlug, TeamName: models.DefaultCodeReviewGitHubTriggerTeamName,
			TeamReviewer: "@acme/143-code-reviewer", RepoPermission: models.DefaultCodeReviewGitHubTriggerRepoPerm,
			Trigger: &disconnectedSetting,
			Message: "Reconnect this repository before setting up its GitHub reviewer.",
		},
	}, responses, "ListStatus should combine repository, trigger, connection, and auth state without per-repository lookups")
	require.Equal(t, 1, auth.calls, "ListStatus should check the caller's GitHub authorization once for all repositories")
	require.ElementsMatch(t, []uuid.UUID{readyRepoID, disconnectedRepoID}, repoStore.filters.IncludeRepositoryIDs, "ListStatus should request only configured inactive repositories in addition to active repositories")
}

func TestGitHubTriggerSetupService_SetupRejectsDisconnectedRepository(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	userID := uuid.New()
	repoID := uuid.New()
	svc := NewGitHubTriggerSetupService(
		&githubTriggerSetupStoreStub{},
		&githubTriggerRepoStoreStub{repo: models.Repository{ID: repoID, OrgID: orgID, FullName: "acme/api", Status: models.RepositoryStatusDisconnected}},
		&githubTriggerAuthStub{cfg: &models.GitHubAppUserConfig{AccessToken: "ghu_user"}},
		testLogger(),
	)

	_, err := svc.Setup(context.Background(), GitHubTriggerSetupInput{OrgID: orgID, UserID: userID, RepositoryID: repoID})

	require.ErrorIs(t, err, ErrGitHubTriggerRepoDisconnected, "Setup should require an active repository")
}

type githubTriggerSetupStoreStub struct {
	setting     models.CodeReviewGitHubTriggerSetting
	settings    []models.CodeReviewGitHubTriggerSetting
	getErr      error
	saved       bool
	savedParams db.SaveCodeReviewGitHubTriggerParams
}

func (s *githubTriggerSetupStoreStub) ListActiveGitHubTriggers(context.Context, uuid.UUID) ([]models.CodeReviewGitHubTriggerSetting, error) {
	return s.settings, s.getErr
}

func (s *githubTriggerSetupStoreStub) GetActiveGitHubTrigger(context.Context, uuid.UUID, uuid.UUID) (models.CodeReviewGitHubTriggerSetting, error) {
	if s.setting.ID != uuid.Nil || s.setting.TeamSlug != "" {
		return s.setting, nil
	}
	if s.getErr != nil {
		return models.CodeReviewGitHubTriggerSetting{}, s.getErr
	}
	return models.CodeReviewGitHubTriggerSetting{}, pgx.ErrNoRows
}

func (s *githubTriggerSetupStoreStub) SaveGitHubTrigger(_ context.Context, orgID uuid.UUID, params db.SaveCodeReviewGitHubTriggerParams) (models.CodeReviewGitHubTriggerSetting, error) {
	s.saved = true
	s.savedParams = params
	return models.CodeReviewGitHubTriggerSetting{
		ID:              uuid.New(),
		OrgID:           orgID,
		RepositoryID:    params.RepositoryID,
		InstallationID:  params.InstallationID,
		Active:          true,
		Version:         1,
		TeamSlug:        params.TeamSlug,
		TeamName:        params.TeamName,
		TeamID:          params.TeamID,
		RepoPermission:  params.RepoPermission,
		CreatedByUserID: params.CreatedByUserID,
	}, nil
}

func (s *githubTriggerSetupStoreStub) DeactivateGitHubTrigger(context.Context, uuid.UUID, uuid.UUID, *uuid.UUID) error {
	return nil
}

type githubTriggerRepoStoreStub struct {
	repo    models.Repository
	repos   []models.Repository
	filters db.RepositoryFilters
	err     error
}

func (s *githubTriggerRepoStoreStub) ListByOrg(_ context.Context, _ uuid.UUID, filters db.RepositoryFilters) ([]models.Repository, error) {
	s.filters = filters
	if s.err != nil {
		return nil, s.err
	}
	return s.repos, nil
}

func (s *githubTriggerRepoStoreStub) GetByID(context.Context, uuid.UUID, uuid.UUID) (models.Repository, error) {
	if s.err != nil {
		return models.Repository{}, s.err
	}
	return s.repo, nil
}

type githubTriggerAuthStub struct {
	cfg   *models.GitHubAppUserConfig
	err   error
	calls int
}

func (s *githubTriggerAuthStub) GetValidCredential(context.Context, uuid.UUID, uuid.UUID) (*models.GitHubAppUserConfig, error) {
	s.calls++
	if s.err != nil {
		return nil, s.err
	}
	return s.cfg, nil
}

func testLogger() zerolog.Logger {
	return zerolog.Nop()
}
