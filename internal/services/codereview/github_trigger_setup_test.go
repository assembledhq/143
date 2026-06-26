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
		&githubTriggerRepoStoreStub{repo: models.Repository{ID: repoID, OrgID: orgID, FullName: "acme/api"}},
		&githubTriggerAuthStub{err: ghservice.ErrGitHubAppUserCredentialMissing},
		testLogger(),
	)

	resp, err := svc.Status(context.Background(), orgID, userID, repoID)

	require.NoError(t, err, "Status should not fail when the caller needs to connect GitHub")
	require.Equal(t, models.CodeReviewGitHubTriggerStatusAuthRequired, resp.Status, "Status should identify missing user authorization")
	require.Equal(t, "@acme/143-code-reviewer", resp.TeamReviewer, "Status should still expose the expected reviewer team")
}

type githubTriggerSetupStoreStub struct {
	setting     models.CodeReviewGitHubTriggerSetting
	getErr      error
	saved       bool
	savedParams db.SaveCodeReviewGitHubTriggerParams
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
	repo models.Repository
	err  error
}

func (s *githubTriggerRepoStoreStub) GetByID(context.Context, uuid.UUID, uuid.UUID) (models.Repository, error) {
	if s.err != nil {
		return models.Repository{}, s.err
	}
	return s.repo, nil
}

type githubTriggerAuthStub struct {
	cfg *models.GitHubAppUserConfig
	err error
}

func (s *githubTriggerAuthStub) GetValidCredential(context.Context, uuid.UUID, uuid.UUID) (*models.GitHubAppUserConfig, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.cfg, nil
}

func testLogger() zerolog.Logger {
	return zerolog.Nop()
}
