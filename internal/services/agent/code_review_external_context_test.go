package agent

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestCodeReviewIntegrationSkillsStrictGate(t *testing.T) {
	t.Parallel()
	orchestrator := &Orchestrator{}
	doc, err := orchestrator.codeReviewIntegrationSkills(context.Background(), uuid.New())
	require.NoError(t, err, "legacy code review should retain best-effort integration prompt behavior")
	require.Empty(t, doc, "legacy review with no credentials should have no integration skills")
	orchestrator.SetCodeReviewInputsStrict(true)
	_, err = orchestrator.codeReviewIntegrationSkills(context.Background(), uuid.New())
	require.Error(t, err, "assessment-enabled review should require fingerprintable external context")
}

type externalContextCredentials struct{ err error }

func (f externalContextCredentials) Get(context.Context, uuid.UUID, models.ProviderName) (*models.DecryptedCredential, error) {
	return nil, nil
}
func (f externalContextCredentials) ListByProvider(context.Context, uuid.UUID, models.ProviderName) ([]models.DecryptedCredential, error) {
	return nil, nil
}
func (f externalContextCredentials) GetAllIntegrations(context.Context, uuid.UUID, []models.ProviderName) (map[models.ProviderName]*models.DecryptedCredential, error) {
	return map[models.ProviderName]*models.DecryptedCredential{}, f.err
}

type externalContextOrg struct{ settings json.RawMessage }

func (f externalContextOrg) GetByID(context.Context, uuid.UUID) (models.Organization, error) {
	return models.Organization{Settings: f.settings}, nil
}

func TestCodeReviewExternalContextResolver(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, settings string
		credentialErr  error
		memory         bool
		wantErr        bool
	}{
		{name: "resolved GitHub and session tools", settings: `{}`},
		{name: "explicitly disabled tab tools", settings: `{"coding_agent_tab_tools_enabled":false}`},
		{name: "credential read failure is not empty tools", settings: `{}`, credentialErr: errors.New("database unavailable"), wantErr: true},
		{name: "unfingerprinted memory blocks reuse", settings: `{}`, memory: true, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			resolver := CodeReviewExternalContextResolver{Credentials: externalContextCredentials{err: tt.credentialErr}, Orgs: externalContextOrg{settings: json.RawMessage(tt.settings)}, GitHubTools: true, SessionTools: true, MemoryInjected: tt.memory}
			doc, digest, err := resolver.ResolveCodeReviewExternalContext(context.Background(), uuid.New())
			if tt.wantErr {
				require.Error(t, err, "unavailable dynamic prompt context must fail closed")
				return
			}
			require.NoError(t, err, "complete prompt context should resolve")
			require.NotEmpty(t, doc, "enabled tools should render a prompt document")
			require.Len(t, digest, 64, "resolved prompt should have a SHA-256 digest")
		})
	}
}
