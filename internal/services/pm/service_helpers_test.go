package pm

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/agent"
)

type helperPlanStore struct {
	created []*models.PMPlan
	err     error
}

func (m *helperPlanStore) Create(_ context.Context, plan *models.PMPlan) error {
	if plan.ID == uuid.Nil {
		plan.ID = uuid.New()
	}
	m.created = append(m.created, plan)
	return m.err
}

func (m *helperPlanStore) Update(_ context.Context, _ *models.PMPlan) error {
	return nil
}

type helperNoTokenCodexAuth struct{}

func (helperNoTokenCodexAuth) GetValidToken(_ context.Context, _ uuid.UUID) (*models.OpenAIChatGPTConfig, error) {
	return nil, nil
}

type helperErrCodexAuth struct{}

func (helperErrCodexAuth) GetValidToken(_ context.Context, _ uuid.UUID) (*models.OpenAIChatGPTConfig, error) {
	return nil, errors.New("oauth lookup failed")
}

type helperUsageTracker struct{}

func (helperUsageTracker) ContainerStarted(context.Context, uuid.UUID, uuid.UUID, *agent.Sandbox, agent.SandboxConfig, time.Time) uuid.UUID {
	return uuid.New()
}

func (helperUsageTracker) ContainerStopped(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, string, time.Time, string) {
}

func TestResolveAgentType(t *testing.T) {
	t.Parallel()

	override := models.AgentTypePi
	tests := []struct {
		name     string
		settings models.OrgSettings
		override *models.AgentType
		expected models.AgentType
	}{
		{
			name:     "override wins",
			settings: models.OrgSettings{DefaultAgentType: models.AgentTypeCodex},
			override: &override,
			expected: models.AgentTypePi,
		},
		{
			name:     "settings default is used without override",
			settings: models.OrgSettings{DefaultAgentType: models.AgentTypeGeminiCLI},
			expected: models.AgentTypeGeminiCLI,
		},
		{
			name:     "platform default is final fallback",
			settings: models.OrgSettings{},
			expected: models.DefaultDefaultAgentType,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.expected, resolveAgentType(tt.settings, tt.override), "resolveAgentType should honor precedence for %s", tt.name)
		})
	}
}

func TestServicePickAdapter(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		adapters  map[models.AgentType]agent.AgentAdapter
		agentType models.AgentType
		wantErr   string
	}{
		{
			name:      "nil adapter map fails",
			agentType: models.AgentTypeCodex,
			wantErr:   "pm adapters not configured",
		},
		{
			name:      "missing adapter fails",
			adapters:  map[models.AgentType]agent.AgentAdapter{},
			agentType: models.AgentTypeCodex,
			wantErr:   "no adapter registered",
		},
		{
			name:      "registered adapter succeeds",
			adapters:  testAdapterMap(&reviewAdapter{}),
			agentType: models.DefaultDefaultAgentType,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			svc := &Service{adapters: tt.adapters}
			adapter, err := svc.pickAdapter(tt.agentType)
			if tt.wantErr != "" {
				require.Error(t, err, "pickAdapter should fail for %s", tt.name)
				require.Contains(t, err.Error(), tt.wantErr, "pickAdapter should return the expected error for %s", tt.name)
				require.Nil(t, adapter, "pickAdapter should not return an adapter for %s", tt.name)
				return
			}

			require.NoError(t, err, "pickAdapter should succeed for %s", tt.name)
			require.NotNil(t, adapter, "pickAdapter should return an adapter for %s", tt.name)
		})
	}
}

func TestServiceFinalizeSandboxEnv(t *testing.T) {
	t.Parallel()

	svc := &Service{
		env: agent.NewAgentEnv(agent.AgentEnvDeps{
			Provider: &pmSandboxMock{},
			Logger:   zerolog.Nop(),
		}),
		logger: zerolog.Nop(),
	}

	err := svc.finalizeSandboxEnv(models.AgentTypeAmp, map[string]string{})
	require.Error(t, err, "finalizeSandboxEnv should fail Amp auth preflight without AMP_API_KEY")
	require.Contains(t, err.Error(), "AMP_API_KEY", "finalizeSandboxEnv should surface the missing Amp credential")

	piEnv := map[string]string{
		"PI_MODEL_CUSTOM": "moonshot/kimi-k2",
		"PI_API_KEY":      "pi-key",
	}
	require.NoError(t, svc.finalizeSandboxEnv(models.AgentTypePi, piEnv), "finalizeSandboxEnv should allow Pi runs with PI_API_KEY configured")

	err = svc.finalizeSandboxEnv(models.AgentTypePi, map[string]string{})
	require.Error(t, err, "finalizeSandboxEnv should fail Pi auth preflight without PI_API_KEY")
	require.Contains(t, err.Error(), "PI_API_KEY", "finalizeSandboxEnv should surface the missing Pi credential")
}

func TestServiceInjectRequiredAgentAuth(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	orgID := uuid.New()
	sb := &agent.Sandbox{HomeDir: "/home/test"}

	tests := []struct {
		name      string
		agentType models.AgentType
		env       *agent.AgentEnv
		wantErr   string
	}{
		{
			name:      "non codex agent skips auth injection",
			agentType: models.AgentTypePi,
			env: agent.NewAgentEnv(agent.AgentEnvDeps{
				Provider: &pmSandboxMock{},
				Logger:   zerolog.Nop(),
			}),
		},
		{
			name:      "codex missing oauth token returns auth error",
			agentType: models.AgentTypeCodex,
			env: agent.NewAgentEnv(agent.AgentEnvDeps{
				CodexAuth: helperNoTokenCodexAuth{},
				Provider:  &pmSandboxMock{},
				Logger:    zerolog.Nop(),
			}),
			wantErr: "No ChatGPT credentials",
		},
		{
			name:      "codex oauth lookup failure returns auth error",
			agentType: models.AgentTypeCodex,
			env: agent.NewAgentEnv(agent.AgentEnvDeps{
				CodexAuth: helperErrCodexAuth{},
				Provider:  &pmSandboxMock{},
				Logger:    zerolog.Nop(),
			}),
			wantErr: "failed to prepare ChatGPT authentication",
		},
		{
			name:      "codex oauth token injects successfully",
			agentType: models.AgentTypeCodex,
			env:       testAgentEnv(),
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			svc := &Service{env: tt.env}
			_, err := svc.injectRequiredAgentAuth(ctx, orgID, tt.agentType, sb, nil)
			if tt.wantErr != "" {
				require.Error(t, err, "injectRequiredAgentAuth should fail for %s", tt.name)
				var authErr *agent.AuthError
				require.ErrorAs(t, err, &authErr, "injectRequiredAgentAuth should wrap failures as AuthError for %s", tt.name)
				require.Contains(t, err.Error(), tt.wantErr, "injectRequiredAgentAuth should explain the failure for %s", tt.name)
				return
			}

			require.NoError(t, err, "injectRequiredAgentAuth should succeed for %s", tt.name)
		})
	}
}

func TestServiceSetUsageTrackerPersistFailedPlanAndContainerExitReason(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	orgID := uuid.New()

	svc := &Service{}
	tracker := helperUsageTracker{}
	svc.SetUsageTracker(tracker)
	require.NotNil(t, svc.usageTracker, "SetUsageTracker should store the usage tracker")

	require.Equal(t, uuid.Nil, svc.persistFailedPlan(ctx, orgID, models.PMTriggerManual, "auth failed"), "persistFailedPlan should return nil when the plan store is missing")

	errStore := &helperPlanStore{err: errors.New("insert failed")}
	svc.plans = errStore
	require.Equal(t, uuid.Nil, svc.persistFailedPlan(ctx, orgID, models.PMTriggerManual, "auth failed"), "persistFailedPlan should return nil when plan persistence fails")
	require.Len(t, errStore.created, 1, "persistFailedPlan should attempt to create a failed plan record")

	okStore := &helperPlanStore{}
	svc.plans = okStore
	planID := svc.persistFailedPlan(ctx, orgID, models.PMTriggerCron, "auth failed")
	require.NotEqual(t, uuid.Nil, planID, "persistFailedPlan should return the created plan ID on success")
	require.Len(t, okStore.created, 1, "persistFailedPlan should create exactly one failed plan record")
	require.Equal(t, models.PMPlanStatusFailed, okStore.created[0].Status, "persistFailedPlan should mark the stored plan as failed")
	require.Equal(t, models.PMTriggerCron, okStore.created[0].TriggeredBy, "persistFailedPlan should preserve the PM trigger")

	require.Equal(t, "completed", containerExitReason(context.Background(), nil), "containerExitReason should treat nil errors as completed")
	require.Equal(t, "failed", containerExitReason(context.Background(), errors.New("boom")), "containerExitReason should report generic failures")

	deadlineCtx, cancelDeadline := context.WithTimeout(context.Background(), time.Nanosecond)
	defer cancelDeadline()
	time.Sleep(time.Millisecond)
	require.Equal(t, "timeout", containerExitReason(deadlineCtx, errors.New("timed out")), "containerExitReason should map deadline exceeded to timeout")

	cancelledCtx, cancel := context.WithCancel(context.Background())
	cancel()
	require.Equal(t, "cancelled", containerExitReason(cancelledCtx, errors.New("cancelled")), "containerExitReason should map cancelled contexts to cancelled")
}
