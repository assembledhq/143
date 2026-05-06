package pm

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/agent"
)

type reviewSandboxProvider struct {
	sandbox      *agent.Sandbox
	createCfgs   []agent.SandboxConfig
	cloneErr     error
	destroyErr   error
	destroyCalls int
	writes       map[string][]byte
}

func (m *reviewSandboxProvider) Name() string { return "review-mock" }

func (m *reviewSandboxProvider) Create(_ context.Context, cfg agent.SandboxConfig) (*agent.Sandbox, error) {
	m.createCfgs = append(m.createCfgs, cfg)
	if m.sandbox == nil {
		m.sandbox = &agent.Sandbox{
			ID:      "sandbox-1",
			HomeDir: "/home/sandbox",
			WorkDir: "/workspace",
		}
	}
	return m.sandbox, nil
}

func (m *reviewSandboxProvider) CloneRepo(_ context.Context, _ *agent.Sandbox, _, _, _ string) error {
	return m.cloneErr
}

func (m *reviewSandboxProvider) Exec(_ context.Context, _ *agent.Sandbox, _ string, _, _ io.Writer) (int, error) {
	return 0, nil
}

func (m *reviewSandboxProvider) ReadFile(_ context.Context, _ *agent.Sandbox, _ string) ([]byte, error) {
	return nil, nil
}

func (m *reviewSandboxProvider) WriteFile(_ context.Context, _ *agent.Sandbox, path string, data []byte) error {
	if m.writes == nil {
		m.writes = make(map[string][]byte)
	}
	m.writes[path] = append([]byte(nil), data...)
	return nil
}

func (m *reviewSandboxProvider) Destroy(_ context.Context, _ *agent.Sandbox) error {
	m.destroyCalls++
	return m.destroyErr
}

func (m *reviewSandboxProvider) IsAlive(_ context.Context, _ *agent.Sandbox) (bool, error) {
	return true, nil
}

func (m *reviewSandboxProvider) ConnectionInfo(_ context.Context, _ *agent.Sandbox) (*agent.SandboxConnectionInfo, error) {
	return nil, nil
}

func (m *reviewSandboxProvider) Snapshot(_ context.Context, _ *agent.Sandbox) (io.ReadCloser, error) {
	return io.NopCloser(bytes.NewReader(nil)), nil
}

func (m *reviewSandboxProvider) Restore(_ context.Context, _ *agent.Sandbox, _ io.Reader) error {
	return nil
}

func (m *reviewSandboxProvider) ExecStream(_ context.Context, _ *agent.Sandbox, _ string, _ func(line []byte), _ io.Writer) (int, error) {
	return 0, nil
}

type reviewUsageRecorder struct {
	started []reviewUsageStarted
	stopped []reviewUsageStopped
}

type reviewUsageStarted struct {
	orgID     uuid.UUID
	sessionID uuid.UUID
}

type reviewUsageStopped struct {
	orgID      uuid.UUID
	sessionID  uuid.UUID
	exitReason string
}

func (r *reviewUsageRecorder) ContainerStarted(_ context.Context, orgID, sessionID uuid.UUID, _ *agent.Sandbox, _ agent.SandboxConfig, _ time.Time) uuid.UUID {
	r.started = append(r.started, reviewUsageStarted{orgID: orgID, sessionID: sessionID})
	return uuid.New()
}

func (r *reviewUsageRecorder) ContainerStopped(_ context.Context, orgID, sessionID uuid.UUID, _ uuid.UUID, _ string, _ time.Time, exitReason string) {
	r.stopped = append(r.stopped, reviewUsageStopped{orgID: orgID, sessionID: sessionID, exitReason: exitReason})
}

type recordingPlanStore struct {
	created []*models.PMPlan
	updated []*models.PMPlan
}

func (m *recordingPlanStore) Create(_ context.Context, plan *models.PMPlan) error {
	if plan.ID == uuid.Nil {
		plan.ID = uuid.New()
	}
	m.created = append(m.created, plan)
	return nil
}

func (m *recordingPlanStore) Update(_ context.Context, plan *models.PMPlan) error {
	m.updated = append(m.updated, plan)
	return nil
}

type sequentialOrgStore struct {
	responses []sequentialOrgResponse
}

type sequentialOrgResponse struct {
	org models.Organization
	err error
}

func (m *sequentialOrgStore) GetByID(_ context.Context, _ uuid.UUID) (models.Organization, error) {
	if len(m.responses) == 0 {
		return models.Organization{}, fmt.Errorf("unexpected GetByID call")
	}
	resp := m.responses[0]
	m.responses = m.responses[1:]
	if resp.err != nil {
		return models.Organization{}, resp.err
	}
	return resp.org, nil
}

type reviewAdapter struct {
	result *agent.AgentResult
	err    error
}

func (a *reviewAdapter) Name() models.AgentType { return models.DefaultDefaultAgentType }

func (a *reviewAdapter) PreparePrompt(_ context.Context, _ *agent.AgentInput) (*agent.AgentPrompt, error) {
	return &agent.AgentPrompt{}, nil
}

func (a *reviewAdapter) Execute(_ context.Context, _ *agent.Sandbox, _ *agent.AgentPrompt, _ chan<- agent.LogEntry) (*agent.AgentResult, error) {
	if a.err != nil {
		return nil, a.err
	}
	if a.result != nil {
		return a.result, nil
	}
	return &agent.AgentResult{}, nil
}

func (a *reviewAdapter) ResumeMode() agent.SessionResumeMode { return agent.ResumeUnsupported }

func marshalOrgSettingsForReview(t *testing.T, settings models.OrgSettings) []byte {
	t.Helper()

	data, err := json.Marshal(settings)
	require.NoError(t, err, "should marshal org settings for review test")
	return data
}

func TestAnalyze_CodexRequiresInjectedAuth(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	repoID := uuid.New()
	sandbox := &reviewSandboxProvider{}
	env := agent.NewAgentEnv(agent.AgentEnvDeps{
		Provider: sandbox,
		Logger:   zerolog.Nop(),
	})
	sessions := &mockSessionStore{}

	svc := &Service{
		issues:   &mockIssueStore{},
		sessions: sessions,
		orgs: &mockOrgStore{org: models.Organization{
			ID:       orgID,
			Settings: marshalOrgSettingsForReview(t, models.OrgSettings{DefaultAgentType: models.AgentTypeCodex}),
		}},
		repos:    &mockRepoStore{repos: []models.Repository{{ID: repoID, Status: "active", DefaultBranch: "main"}}},
		sandbox:  sandbox,
		adapters: map[models.AgentType]agent.AgentAdapter{models.AgentTypeCodex: &reviewAdapter{}},
		env:      env,
		plans:    &recordingPlanStore{},
		logger:   zerolog.Nop(),
	}

	_, err := svc.Analyze(context.Background(), orgID, models.PMTriggerManual, nil, nil)
	require.Error(t, err, "Analyze should fail when Codex auth injection reports no OAuth token")

	var authErr *agent.AuthError
	require.ErrorAs(t, err, &authErr, "Analyze should wrap missing Codex auth as an AuthError")
	require.Equal(t, models.AgentTypeCodex, authErr.AgentType, "AuthError should identify the Codex agent")
	require.Contains(t, authErr.Detail, "ChatGPT", "AuthError should tell the user how to fix Codex auth")
	require.Equal(t, 1, sandbox.destroyCalls, "Analyze should destroy the sandbox on Codex auth failure")
	require.Equal(t, "failed", sessions.lastResultStatus, "Analyze should mark the PM session failed on Codex auth failure")
}

func TestAnalyze_PiUsesDedicatedCredentialBeforeSandboxCreate(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	repoID := uuid.New()
	sandbox := &reviewSandboxProvider{cloneErr: fmt.Errorf("clone failed")}
	usage := &reviewUsageRecorder{}
	env := agent.NewAgentEnv(agent.AgentEnvDeps{
		Credentials: &mockCredStore{
			creds: map[models.ProviderName]*models.DecryptedCredential{
				models.ProviderPi: {Config: models.PiConfig{APIKey: "pi-review-key"}},
			},
		},
		Provider: sandbox,
		Logger:   zerolog.Nop(),
	})

	svc := &Service{
		issues:   &mockIssueStore{},
		sessions: &mockSessionStore{},
		orgs: &mockOrgStore{org: models.Organization{
			ID:       orgID,
			Settings: marshalOrgSettingsForReview(t, models.OrgSettings{DefaultAgentType: models.AgentTypePi}),
		}},
		repos:        &mockRepoStore{repos: []models.Repository{{ID: repoID, Status: "active", DefaultBranch: "main"}}},
		sandbox:      sandbox,
		adapters:     map[models.AgentType]agent.AgentAdapter{models.AgentTypePi: &reviewAdapter{}},
		env:          env,
		plans:        &recordingPlanStore{},
		usageTracker: usage,
		logger:       zerolog.Nop(),
	}

	_, err := svc.Analyze(context.Background(), orgID, models.PMTriggerManual, nil, nil)
	require.Error(t, err, "Analyze should surface clone failures")
	require.Contains(t, err.Error(), "clone repo", "Analyze should fail after sandbox creation so we can inspect the env")
	require.Len(t, sandbox.createCfgs, 1, "Analyze should create one sandbox")
	require.Equal(t, "pi-review-key", sandbox.createCfgs[0].Env["PI_API_KEY"], "Pi should inject its dedicated API key before sandbox creation")
	require.NotContains(t, sandbox.createCfgs[0].Env, "ANTHROPIC_API_KEY", "Pi should not inherit Anthropic credentials")
	require.NotContains(t, sandbox.createCfgs[0].Env, "OPENAI_API_KEY", "Pi should not inherit OpenAI credentials")
	require.NotContains(t, sandbox.createCfgs[0].Env, "GEMINI_API_KEY", "Pi should not inherit Gemini credentials")
	require.Len(t, usage.stopped, 1, "Analyze should stop usage tracking on clone failure")
	require.Equal(t, "failed", usage.stopped[0].exitReason, "Analyze should mark clone failures as failed usage")
}

func TestRunAgentInSandbox_CodexRequiresInjectedAuth(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	repoID := uuid.New()
	sandbox := &reviewSandboxProvider{}
	env := agent.NewAgentEnv(agent.AgentEnvDeps{
		Provider: sandbox,
		Logger:   zerolog.Nop(),
	})

	svc := &Service{
		orgs: &mockOrgStore{org: models.Organization{
			ID:       orgID,
			Settings: marshalOrgSettingsForReview(t, models.OrgSettings{DefaultAgentType: models.AgentTypeCodex}),
		}},
		repos:    &mockRepoStore{repos: []models.Repository{{ID: repoID, Status: "active", DefaultBranch: "main"}}},
		sandbox:  sandbox,
		adapters: map[models.AgentType]agent.AgentAdapter{models.AgentTypeCodex: &reviewAdapter{}},
		env:      env,
		logger:   zerolog.Nop(),
	}

	_, cleanup, err := svc.runAgentInSandbox(context.Background(), sandboxRunParams{
		orgID:   orgID,
		prompt:  &agent.AgentPrompt{},
		logName: "bootstrap-agent",
	})
	require.Error(t, err, "runAgentInSandbox should fail when Codex auth injection reports no OAuth token")
	require.NotNil(t, cleanup, "runAgentInSandbox should always return a cleanup function")

	var authErr *agent.AuthError
	require.ErrorAs(t, err, &authErr, "runAgentInSandbox should wrap missing Codex auth as an AuthError")
	require.Equal(t, models.AgentTypeCodex, authErr.AgentType, "AuthError should identify Codex in bootstrap flow")
	require.Equal(t, 1, sandbox.destroyCalls, "runAgentInSandbox should destroy the sandbox on Codex auth failure")
}

func TestRunAgentInSandbox_NonFatalSeedErrorKeepsUsageCompleted(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	repoID := uuid.New()
	sandbox := &reviewSandboxProvider{}
	usage := &reviewUsageRecorder{}
	settingsJSON := marshalOrgSettingsForReview(t, models.OrgSettings{})
	orgs := &sequentialOrgStore{
		responses: []sequentialOrgResponse{
			{org: models.Organization{ID: orgID, Settings: settingsJSON}},
			{err: fmt.Errorf("seed lookup failed")},
		},
	}

	svc := &Service{
		orgs:         orgs,
		repos:        &mockRepoStore{repos: []models.Repository{{ID: repoID, Status: "active", DefaultBranch: "main"}}},
		sandbox:      sandbox,
		adapters:     map[models.AgentType]agent.AgentAdapter{models.AgentTypeClaudeCode: &reviewAdapter{}},
		env:          agent.NewAgentEnv(agent.AgentEnvDeps{Provider: sandbox, Logger: zerolog.Nop()}),
		usageTracker: usage,
		logger:       zerolog.Nop(),
		pmDocuments:  &mockPMDocStore{},
	}
	orgs.responses[0].org.Settings = marshalOrgSettingsForReview(t, models.OrgSettings{DefaultAgentType: models.AgentTypeClaudeCode})

	_, cleanup, err := svc.runAgentInSandbox(context.Background(), sandboxRunParams{
		orgID:   orgID,
		prompt:  &agent.AgentPrompt{},
		logName: "bootstrap-agent",
	})
	require.NoError(t, err, "runAgentInSandbox should continue when bootstrap seed building fails")
	cleanup()

	require.Len(t, usage.stopped, 1, "runAgentInSandbox should stop usage tracking once")
	require.Equal(t, "completed", usage.stopped[0].exitReason, "non-fatal bootstrap seed errors should not mark usage as failed")
}

func TestAnalyzeProject_CodexRequiresInjectedAuthAndMarksUsageFailed(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	projectID := uuid.New()
	repoID := uuid.New()
	sandbox := &reviewSandboxProvider{}
	usage := &reviewUsageRecorder{}
	project := models.Project{
		ID:           projectID,
		OrgID:        orgID,
		RepositoryID: &repoID,
		Status:       models.ProjectStatusActive,
	}

	svc := &Service{
		adapters: map[models.AgentType]agent.AgentAdapter{models.AgentTypeCodex: &reviewAdapter{}},
		env: agent.NewAgentEnv(agent.AgentEnvDeps{
			Provider: sandbox,
			Logger:   zerolog.Nop(),
		}),
		sandbox:       sandbox,
		projects:      newMockProjectStore(project),
		projectTasks:  &mockProjectTaskStore{},
		projectCycles: &mockProjectCycleStore{},
		orgs: &mockOrgStore{org: models.Organization{
			ID:       orgID,
			Settings: marshalOrgSettingsForReview(t, models.OrgSettings{DefaultAgentType: models.AgentTypeCodex}),
		}},
		repos:        &mockRepoStore{repos: []models.Repository{{ID: repoID, Status: "active", DefaultBranch: "main"}}},
		usageTracker: usage,
		logger:       zerolog.Nop(),
	}

	err := svc.AnalyzeProject(context.Background(), orgID, projectID)
	require.Error(t, err, "AnalyzeProject should fail when Codex auth injection reports no OAuth token")

	var authErr *agent.AuthError
	require.ErrorAs(t, err, &authErr, "AnalyzeProject should wrap missing Codex auth as an AuthError")
	require.Equal(t, models.AgentTypeCodex, authErr.AgentType, "AuthError should identify Codex in project analysis")
	require.Len(t, usage.stopped, 1, "AnalyzeProject should stop usage tracking on Codex auth failure")
	require.Equal(t, "failed", usage.stopped[0].exitReason, "AnalyzeProject should mark Codex auth failures as failed usage")
}

func TestAnalyze_ParsePlanLogRedactsAgentOutput(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	repoID := uuid.New()
	sandbox := &reviewSandboxProvider{}
	var logBuf bytes.Buffer
	logger := zerolog.New(&logBuf)
	rawKey := "sk-ant-api03-abcdef0123456789ABCDEF"

	svc := &Service{
		issues:   &mockIssueStore{},
		sessions: &mockSessionStore{},
		orgs: &mockOrgStore{org: models.Organization{
			ID:       orgID,
			Settings: marshalOrgSettingsForReview(t, models.OrgSettings{DefaultAgentType: models.AgentTypeClaudeCode}),
		}},
		repos:    &mockRepoStore{repos: []models.Repository{{ID: repoID, Status: "active", DefaultBranch: "main"}}},
		sandbox:  sandbox,
		adapters: map[models.AgentType]agent.AgentAdapter{models.AgentTypeClaudeCode: &reviewAdapter{result: &agent.AgentResult{Summary: "invalid api key: " + rawKey}}},
		env:      agent.NewAgentEnv(agent.AgentEnvDeps{Provider: sandbox, Logger: logger}),
		plans:    &recordingPlanStore{},
		logger:   logger,
	}

	_, err := svc.Analyze(context.Background(), orgID, models.PMTriggerManual, nil, nil)
	require.Error(t, err, "Analyze should fail when parsePlan detects an auth error")

	logOutput := logBuf.String()
	require.NotContains(t, logOutput, rawKey, "Analyze should not log raw API keys from agent output")
	require.Contains(t, logOutput, "sk-***REDACTED***", "Analyze should redact API keys in parsePlan failure logs")
}

func TestAnalyze_CodexAuthErrorStillPersistsFailedPlan(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	repoID := uuid.New()
	sandbox := &reviewSandboxProvider{}
	env := agent.NewAgentEnv(agent.AgentEnvDeps{
		Provider: sandbox,
		Logger:   zerolog.Nop(),
	})
	plans := &recordingPlanStore{}

	svc := &Service{
		issues:   &mockIssueStore{},
		sessions: &mockSessionStore{},
		orgs: &mockOrgStore{org: models.Organization{
			ID:       orgID,
			Settings: marshalOrgSettingsForReview(t, models.OrgSettings{DefaultAgentType: models.AgentTypeCodex}),
		}},
		repos:    &mockRepoStore{repos: []models.Repository{{ID: repoID, Status: "active", DefaultBranch: "main"}}},
		sandbox:  sandbox,
		adapters: map[models.AgentType]agent.AgentAdapter{models.AgentTypeCodex: &reviewAdapter{}},
		env:      env,
		plans:    plans,
		logger:   zerolog.Nop(),
	}

	_, err := svc.Analyze(context.Background(), orgID, models.PMTriggerManual, nil, nil)
	require.Error(t, err, "Analyze should fail when Codex auth is missing")

	var authErr *agent.AuthError
	require.True(t, errors.As(err, &authErr), "Analyze should return an AuthError for missing Codex auth")
	require.Len(t, plans.created, 1, "Analyze should persist a failed PM plan for actionable auth failures")
}
