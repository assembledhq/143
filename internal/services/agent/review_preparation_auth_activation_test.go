package agent

import (
	"context"
	"testing"

	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/reviewpreparation"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func TestReviewPreparationActivationPreservesReviewAuthIsolation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name           string
		enabled        bool
		names          []string
		nilPolicy      bool
		token, ghToken string
	}{
		{name: "default", nilPolicy: true},
		{name: "global disabled", names: []string{"sample/repository"}},
		{name: "empty repository list", enabled: true},
		{name: "outside repository list", enabled: true, names: []string{"other/repository"}},
		{name: "selected repository", enabled: true, names: []string{"sample/repository"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			orgID, repoID := uuid.New(), uuid.New()
			session := &models.Session{ID: uuid.New(), OrgID: orgID, RepositoryID: &repoID, Origin: models.SessionOriginCodeReview, RevisionContext: []byte(`{"github_repo":"sample/repository"}`)}
			repository := &models.Repository{ID: repoID, OrgID: orgID, FullName: "sample/repository", CloneURL: "https://github.com/sample/repository.git"}
			var policy *reviewpreparation.Policy
			if !tt.nilPolicy {
				var err error
				policy, err = reviewpreparation.NewPolicy(tt.enabled, tt.names)
				require.NoError(t, err, "auth activation configuration must construct")
			}
			orchestrator := &Orchestrator{reviewPreparationPolicy: policy}
			cfg := DefaultSandboxConfig()
			cfg.Env = map[string]string{"GITHUB_TOKEN": "prior-token", "GH_TOKEN": "prior-gh-token"}
			_, err := orchestrator.prepareSandboxGitHubAuth(context.Background(), session, repository, "fallback-token", &cfg, zerolog.Nop())
			require.NoError(t, err, "known repository scope must resolve auth transport")
			require.Equal(t, tt.token, cfg.Env["GITHUB_TOKEN"], "review authentication must stay out of the container environment for every recipe policy")
			require.Equal(t, tt.ghToken, cfg.Env["GH_TOKEN"], "inactive recipe preparation must preserve main review credential isolation")
		})
	}
}
