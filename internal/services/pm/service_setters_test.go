package pm

import (
	"context"
	"testing"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

// --------------------------------------------------------------------------
// Tests: setter methods on Service (SetPMDocumentStore, SetSlackStores,
// SetSkillsBuilder, SetProjectStores)
// --------------------------------------------------------------------------

func TestSetPMDocumentStore(t *testing.T) {
	t.Parallel()

	svc := NewService(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, zerolog.Nop())
	require.Nil(t, svc.pmDocuments, "pmDocuments should be nil before SetPMDocumentStore")

	store := &mockPMDocStore{}
	svc.SetPMDocumentStore(store)
	require.Equal(t, store, svc.pmDocuments, "SetPMDocumentStore should inject the store")
}

func TestSetSlackStores(t *testing.T) {
	t.Parallel()

	svc := NewService(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, zerolog.Nop())
	require.Nil(t, svc.integrations, "integrations should be nil before SetSlackStores")
	require.Nil(t, svc.credentials, "credentials should be nil before SetSlackStores")

	intStore := &mockIntegrationStore{}
	credStore := &mockCredStore{creds: nil}
	svc.SetSlackStores(intStore, credStore)
	require.Equal(t, intStore, svc.integrations, "SetSlackStores should inject integration store")
	require.Equal(t, credStore, svc.credentials, "SetSlackStores should inject credential store")
}

func TestSetSkillsBuilder(t *testing.T) {
	t.Parallel()

	svc := NewService(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, zerolog.Nop())
	require.Nil(t, svc.skills, "skills should be nil before SetSkillsBuilder")

	sb := &mockSkillsBuilder{result: "# Skills doc"}
	svc.SetSkillsBuilder(sb)
	require.Equal(t, sb, svc.skills, "SetSkillsBuilder should inject the skills builder")
}

func TestSetProjectStores_ViaNewService(t *testing.T) {
	t.Parallel()

	svc := NewService(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, zerolog.Nop())
	require.Nil(t, svc.projects, "projects should be nil before SetProjectStores")
	require.Nil(t, svc.projectTasks, "projectTasks should be nil before SetProjectStores")
	require.Nil(t, svc.projectCycles, "projectCycles should be nil before SetProjectStores")

	ps := newMockProjectStore()
	pts := &mockProjectTaskStore{}
	pcs := &mockProjectCycleStore{}
	svc.SetProjectStores(ps, pts, pcs)
	require.Equal(t, ps, svc.projects, "SetProjectStores should inject project store")
	require.Equal(t, pts, svc.projectTasks, "SetProjectStores should inject project task store")
	require.Equal(t, pcs, svc.projectCycles, "SetProjectStores should inject project cycle store")
}

// --------------------------------------------------------------------------
// Tests: buildSkillsDoc
// --------------------------------------------------------------------------

func TestBuildSkillsDoc_NilBuilder(t *testing.T) {
	t.Parallel()

	svc := &Service{logger: zerolog.Nop()}
	doc := svc.buildSkillsDoc(context.Background(), uuid.New())
	require.Empty(t, doc, "buildSkillsDoc should return empty string when skills builder is nil")
}

func TestBuildSkillsDoc_WithBuilder(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	sb := &mockSkillsBuilder{result: "# Integration Skills\n\nSentry, Linear, Notion"}
	svc := &Service{skills: sb, logger: zerolog.Nop()}
	doc := svc.buildSkillsDoc(context.Background(), orgID)
	require.Equal(t, "# Integration Skills\n\nSentry, Linear, Notion", doc)
}

// --------------------------------------------------------------------------
// Tests: RunBootstrap / RunRefresh precondition checks
// --------------------------------------------------------------------------

func TestRunBootstrap_NoAdapter(t *testing.T) {
	t.Parallel()

	svc := &Service{logger: zerolog.Nop()}
	err := svc.RunBootstrap(context.Background(), uuid.New())
	require.Error(t, err)
	require.Contains(t, err.Error(), "pm sandbox or env helper not configured")
}

func TestRunBootstrap_NoPMDocStore(t *testing.T) {
	t.Parallel()

	svc := &Service{
		adapters: testAdapterMap(&mockAdapter{}),
		env:      testAgentEnv(),
		sandbox:  &mockSandbox{},
		logger:   zerolog.Nop(),
	}
	err := svc.RunBootstrap(context.Background(), uuid.New())
	require.Error(t, err)
	require.Contains(t, err.Error(), "pm document store not configured")
}

func TestRunRefresh_NoAdapter(t *testing.T) {
	t.Parallel()

	svc := &Service{logger: zerolog.Nop()}
	err := svc.RunRefresh(context.Background(), uuid.New())
	require.Error(t, err)
	require.Contains(t, err.Error(), "pm sandbox or env helper not configured")
}

func TestRunRefresh_NoPMDocStore(t *testing.T) {
	t.Parallel()

	svc := &Service{
		adapters: testAdapterMap(&mockAdapter{}),
		env:      testAgentEnv(),
		sandbox:  &mockSandbox{},
		logger:   zerolog.Nop(),
	}
	err := svc.RunRefresh(context.Background(), uuid.New())
	require.Error(t, err)
	require.Contains(t, err.Error(), "pm document store not configured")
}

func TestRunRefresh_NoExistingDoc(t *testing.T) {
	t.Parallel()

	svc := &Service{
		adapters:    testAdapterMap(&mockAdapter{}),
		env:         testAgentEnv(),
		sandbox:     &mockSandbox{},
		pmDocuments: &mockPMDocStore{getByOrgST: map[string]models.PMDocument{}},
		logger:      zerolog.Nop(),
	}
	err := svc.RunRefresh(context.Background(), uuid.New())
	require.Error(t, err)
	require.Contains(t, err.Error(), "no autogenerated context doc found")
}

// --------------------------------------------------------------------------
// Mocks needed for these tests (not already in other test files)
// --------------------------------------------------------------------------

type mockSkillsBuilder struct {
	result string
}

func (m *mockSkillsBuilder) BuildIntegrationSkills(_ context.Context, _ uuid.UUID) string {
	return m.result
}

type mockIntegrationStore struct{}

func (m *mockIntegrationStore) ListByOrgAndProvider(_ context.Context, _ uuid.UUID, _ models.IntegrationProvider) ([]models.Integration, error) {
	return nil, nil
}
