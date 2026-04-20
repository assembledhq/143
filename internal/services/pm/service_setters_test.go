package pm

import (
	"context"
	"encoding/json"
	"fmt"
	"testing"

	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/agent"
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

	svc := NewService(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, zerolog.Nop())
	require.Nil(t, svc.pmDocuments, "pmDocuments should be nil before SetPMDocumentStore")

	store := &mockPMDocStore{}
	svc.SetPMDocumentStore(store)
	require.Equal(t, store, svc.pmDocuments, "SetPMDocumentStore should inject the store")
}

func TestSetSlackStores(t *testing.T) {
	t.Parallel()

	svc := NewService(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, zerolog.Nop())
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

	svc := NewService(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, zerolog.Nop())
	require.Nil(t, svc.skills, "skills should be nil before SetSkillsBuilder")

	sb := &mockSkillsBuilder{result: "# Skills doc"}
	svc.SetSkillsBuilder(sb)
	require.Equal(t, sb, svc.skills, "SetSkillsBuilder should inject the skills builder")
}

func TestSetProjectStores_ViaNewService(t *testing.T) {
	t.Parallel()

	svc := NewService(nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, nil, zerolog.Nop())
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
	require.Contains(t, err.Error(), "pm adapter or sandbox not configured")
}

func TestRunBootstrap_NoPMDocStore(t *testing.T) {
	t.Parallel()

	svc := &Service{
		adapters: singleTestAdapterMap(&mockAdapter{}),
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
	require.Contains(t, err.Error(), "pm adapter or sandbox not configured")
}

func TestRunRefresh_NoPMDocStore(t *testing.T) {
	t.Parallel()

	svc := &Service{
		adapters: singleTestAdapterMap(&mockAdapter{}),
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
		adapters:    singleTestAdapterMap(&mockAdapter{}),
		sandbox:     &mockSandbox{},
		pmDocuments: &mockPMDocStore{getByOrgST: map[string]models.PMDocument{}},
		logger:      zerolog.Nop(),
	}
	err := svc.RunRefresh(context.Background(), uuid.New())
	require.Error(t, err)
	require.Contains(t, err.Error(), "no autogenerated context doc found")
}

// --------------------------------------------------------------------------
// Tests: resolveAgentAdapter — per-run adapter dispatch by org settings
// --------------------------------------------------------------------------

// labeledAdapter is an identity-bearing mockAdapter so dispatch tests can
// assert *which* adapter was returned.
type labeledAdapter struct {
	mockAdapter
	label string
}

func TestResolveAgentAdapter_FallbackWhenEmpty(t *testing.T) {
	t.Parallel()

	codex := &labeledAdapter{label: "codex"}
	claude := &labeledAdapter{label: "claude"}
	svc := &Service{
		adapters: map[models.AgentType]agent.AgentAdapter{
			models.AgentTypeCodex:      codex,
			models.AgentTypeClaudeCode: claude,
		},
		logger: zerolog.Nop(),
	}

	got, err := svc.resolveAgentAdapter(models.OrgSettings{DefaultAgentType: ""})
	require.NoError(t, err)
	require.Equal(t, models.AgentTypeCodex, models.DefaultDefaultAgentType,
		"test assumes Codex is the default fallback")
	require.Same(t, codex, got, "empty DefaultAgentType should dispatch to the default adapter")
}

func TestResolveAgentAdapter_PicksConfiguredAdapter(t *testing.T) {
	t.Parallel()

	codex := &labeledAdapter{label: "codex"}
	claude := &labeledAdapter{label: "claude"}
	svc := &Service{
		adapters: map[models.AgentType]agent.AgentAdapter{
			models.AgentTypeCodex:      codex,
			models.AgentTypeClaudeCode: claude,
		},
		logger: zerolog.Nop(),
	}

	got, err := svc.resolveAgentAdapter(models.OrgSettings{DefaultAgentType: models.AgentTypeClaudeCode})
	require.NoError(t, err)
	require.Same(t, claude, got, "should dispatch to the adapter keyed by DefaultAgentType")
}

func TestResolveAgentAdapter_MissingKey(t *testing.T) {
	t.Parallel()

	svc := &Service{
		adapters: map[models.AgentType]agent.AgentAdapter{
			models.AgentTypeCodex: &labeledAdapter{label: "codex"},
		},
		logger: zerolog.Nop(),
	}

	_, err := svc.resolveAgentAdapter(models.OrgSettings{DefaultAgentType: models.AgentTypeClaudeCode})
	require.Error(t, err)
	require.Contains(t, err.Error(), "no adapter configured")
	require.Contains(t, err.Error(), string(models.AgentTypeClaudeCode))
}

// --------------------------------------------------------------------------
// Tests: loadOrgSettings / resolveAdapterForOrg — shared helpers
// --------------------------------------------------------------------------

func TestLoadOrgSettings_ParseError(t *testing.T) {
	t.Parallel()

	// Invalid JSON in Organization.Settings should trigger the parse branch.
	svc := &Service{
		orgs:   &gatherOrgStoreMock{org: models.Organization{Settings: []byte("{not json")}},
		logger: zerolog.Nop(),
	}

	_, err := svc.loadOrgSettings(context.Background(), uuid.New())
	require.Error(t, err)
	require.Contains(t, err.Error(), "parse org settings")
}

func TestResolveAdapterForOrg_Success(t *testing.T) {
	t.Parallel()

	settingsJSON, _ := json.Marshal(models.OrgSettings{DefaultAgentType: models.AgentTypeCodex})
	codex := &labeledAdapter{label: "codex"}
	svc := &Service{
		adapters: map[models.AgentType]agent.AgentAdapter{models.AgentTypeCodex: codex},
		orgs:     &gatherOrgStoreMock{org: models.Organization{Settings: settingsJSON}},
		logger:   zerolog.Nop(),
	}

	got, err := svc.resolveAdapterForOrg(context.Background(), uuid.New())
	require.NoError(t, err)
	require.Same(t, codex, got)
}

func TestResolveAdapterForOrg_LoadSettingsError(t *testing.T) {
	t.Parallel()

	svc := &Service{
		adapters: map[models.AgentType]agent.AgentAdapter{models.AgentTypeCodex: &labeledAdapter{label: "codex"}},
		orgs:     &gatherOrgStoreMock{err: fmt.Errorf("db down")},
		logger:   zerolog.Nop(),
	}

	_, err := svc.resolveAdapterForOrg(context.Background(), uuid.New())
	require.Error(t, err)
	require.Contains(t, err.Error(), "get org")
}

// --------------------------------------------------------------------------
// Tests: resolve-adapter failure paths in RunBootstrap / RunRefresh / Analyze /
// AnalyzeProject — exercise the fail-fast branches added by the per-run
// adapter resolution refactor.
// --------------------------------------------------------------------------

// emptyButNonZeroAdapters returns an adapters map with a single non-standard
// key so len(adapters) > 0 passes the precondition check, but resolve for the
// default Codex fallback fails.
func emptyButNonZeroAdapters() map[models.AgentType]agent.AgentAdapter {
	return map[models.AgentType]agent.AgentAdapter{
		models.AgentType("other"): &labeledAdapter{label: "other"},
	}
}

func TestRunBootstrap_ResolveAdapterMissingType(t *testing.T) {
	t.Parallel()

	settingsJSON, _ := json.Marshal(models.OrgSettings{})
	svc := &Service{
		adapters:    emptyButNonZeroAdapters(),
		sandbox:     &mockSandbox{},
		pmDocuments: &mockPMDocStore{},
		orgs:        &gatherOrgStoreMock{org: models.Organization{Settings: settingsJSON}},
		logger:      zerolog.Nop(),
	}

	err := svc.RunBootstrap(context.Background(), uuid.New())
	require.Error(t, err)
	require.Contains(t, err.Error(), "resolve adapter")
}

func TestRunRefresh_ResolveAdapterMissingType(t *testing.T) {
	t.Parallel()

	settingsJSON, _ := json.Marshal(models.OrgSettings{})
	svc := &Service{
		adapters: emptyButNonZeroAdapters(),
		sandbox:  &mockSandbox{},
		pmDocuments: &mockPMDocStore{getByOrgST: map[string]models.PMDocument{
			models.PMDocSourceAutogenerated: {Content: "seed"},
		}},
		orgs:   &gatherOrgStoreMock{org: models.Organization{Settings: settingsJSON}},
		logger: zerolog.Nop(),
	}

	err := svc.RunRefresh(context.Background(), uuid.New())
	require.Error(t, err)
	require.Contains(t, err.Error(), "resolve adapter")
}

func TestAnalyze_ResolveAdapterMissingType(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	repoID := uuid.New()
	settingsJSON, _ := json.Marshal(models.OrgSettings{MaxConcurrentRuns: 3})

	svc := &Service{
		adapters: emptyButNonZeroAdapters(),
		sandbox:  &pmSandboxMock{},
		repos: &mockRepoStore{repos: []models.Repository{
			{ID: repoID, Status: "active", OrgID: orgID},
		}},
		orgs:     &gatherOrgStoreMock{org: models.Organization{ID: orgID, Settings: settingsJSON}},
		issues:   &gatherIssueStoreMock{byStatus: map[string][]models.Issue{}},
		sessions: &gatherSessionStoreMock{byStatus: map[string][]models.Session{}, count: 0},
		logger:   zerolog.Nop(),
	}

	_, err := svc.Analyze(context.Background(), orgID, models.PMTriggerCron, nil)
	require.Error(t, err)
	require.Contains(t, err.Error(), "resolve adapter")
}

func TestAnalyzeProject_ResolveAdapterMissingType(t *testing.T) {
	t.Parallel()

	projectID := uuid.New()
	orgID := uuid.New()
	repoID := uuid.New()
	project := models.Project{
		ID:           projectID,
		OrgID:        orgID,
		RepositoryID: &repoID,
		Status:       models.ProjectStatusActive,
	}
	settingsJSON, _ := json.Marshal(models.OrgSettings{})

	svc := &Service{
		adapters:      emptyButNonZeroAdapters(),
		sandbox:       &mockSandbox{},
		projects:      newMockProjectStore(project),
		projectTasks:  &mockProjectTaskStore{},
		projectCycles: &mockProjectCycleStore{},
		orgs:          &gatherOrgStoreMock{org: models.Organization{ID: orgID, Settings: settingsJSON}},
		logger:        zerolog.Nop(),
	}

	err := svc.AnalyzeProject(context.Background(), orgID, projectID)
	require.Error(t, err)
	require.Contains(t, err.Error(), "resolve adapter")
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

func (m *mockIntegrationStore) ListByOrgAndProvider(_ context.Context, _ uuid.UUID, _ string) ([]models.Integration, error) {
	return nil, nil
}
