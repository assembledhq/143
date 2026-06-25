package pm

import (
	"context"
	"errors"
	"sync"
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

func (helperNoTokenCodexAuth) GetValidToken(_ context.Context, _ uuid.UUID) (*models.OpenAISubscriptionConfig, error) {
	return nil, nil
}

type helperErrCodexAuth struct{}

func (helperErrCodexAuth) GetValidToken(_ context.Context, _ uuid.UUID) (*models.OpenAISubscriptionConfig, error) {
	return nil, errors.New("oauth lookup failed")
}

type helperUsageTracker struct{}

func (helperUsageTracker) ContainerStarted(context.Context, uuid.UUID, uuid.UUID, *agent.Sandbox, agent.SandboxConfig, time.Time) uuid.UUID {
	return uuid.New()
}

func (helperUsageTracker) ContainerStopped(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, string, time.Time, string) {
}

type helperCodingCredentialProvider struct {
	mu             sync.Mutex
	resolvable     map[models.ProviderName][]models.DecryptedCodingCredential
	rateLimitedIDs map[uuid.UUID]models.CodingCredentialRateLimit
}

func (m *helperCodingCredentialProvider) ListResolvable(_ context.Context, _ uuid.UUID, _ *uuid.UUID, provider models.ProviderName) ([]models.DecryptedCodingCredential, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]models.DecryptedCodingCredential(nil), m.resolvable[provider]...), nil
}

func (m *helperCodingCredentialProvider) PickRunnableMulti(_ context.Context, _ models.Scope, providers []models.ProviderName) (*models.DecryptedCodingCredential, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, provider := range providers {
		for _, cred := range m.resolvable[provider] {
			if cred.Status != models.CodingCredentialStatusActive {
				continue
			}
			if limit, ok := m.rateLimitedIDs[cred.ID]; ok && limit.Until.After(time.Now()) {
				continue
			}
			picked := cred
			return &picked, nil
		}
	}
	return nil, errors.New("all eligible coding credentials are currently shed")
}

func (m *helperCodingCredentialProvider) MarkRateLimitedForScope(_ context.Context, _ models.Scope, id uuid.UUID, limit models.CodingCredentialRateLimit) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.rateLimitedIDs == nil {
		m.rateLimitedIDs = make(map[uuid.UUID]models.CodingCredentialRateLimit)
	}
	m.rateLimitedIDs[id] = limit
	for provider, creds := range m.resolvable {
		for i := range creds {
			if creds[i].ID == id {
				until := limit.Until
				observedAt := time.Now()
				message := limit.Message
				creds[i].RateLimitedUntil = &until
				creds[i].RateLimitedObservedAt = &observedAt
				creds[i].RateLimitMessage = &message
			}
		}
		m.resolvable[provider] = creds
	}
	return nil
}

func (m *helperCodingCredentialProvider) MarkAuthRejectedForScope(_ context.Context, _ models.Scope, _ uuid.UUID) error {
	return nil
}

func (m *helperCodingCredentialProvider) MarkRateLimited(id uuid.UUID) {
	_ = m.MarkRateLimitedForScope(context.Background(), models.Scope{}, id, models.CodingCredentialRateLimit{Until: time.Now().Add(time.Minute)})
}

func (m *helperCodingCredentialProvider) MarkAuthRejected(uuid.UUID) {}

type helperRetryAdapter struct {
	mu       sync.Mutex
	seenKeys []string
}

func (a *helperRetryAdapter) Name() models.AgentType { return models.AgentTypeAmp }

func (a *helperRetryAdapter) PreparePrompt(context.Context, *agent.AgentInput) (*agent.AgentPrompt, error) {
	return &agent.AgentPrompt{}, nil
}

func (a *helperRetryAdapter) Execute(_ context.Context, sb *agent.Sandbox, _ *agent.AgentPrompt, _ chan<- agent.LogEntry) (*agent.AgentResult, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.seenKeys = append(a.seenKeys, sb.Env["AMP_API_KEY"])
	if len(a.seenKeys) == 1 {
		return &agent.AgentResult{ExitCode: 1, Error: "rate limit exceeded retry-after=60"}, errors.New("rate limit exceeded")
	}
	return &agent.AgentResult{Summary: "ok", ExitCode: 0}, nil
}

func (a *helperRetryAdapter) ResumeMode() agent.SessionResumeMode {
	return agent.ResumeUnsupported
}

func (a *helperRetryAdapter) keys() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.seenKeys...)
}

type helperClaudeRetryAdapter struct {
	mu      sync.Mutex
	seen    []string
	sandbox *pmSandboxMock
}

func (a *helperClaudeRetryAdapter) Name() models.AgentType { return models.AgentTypeClaudeCode }

func (a *helperClaudeRetryAdapter) PreparePrompt(context.Context, *agent.AgentInput) (*agent.AgentPrompt, error) {
	return &agent.AgentPrompt{}, nil
}

func (a *helperClaudeRetryAdapter) Execute(_ context.Context, sb *agent.Sandbox, _ *agent.AgentPrompt, _ chan<- agent.LogEntry) (*agent.AgentResult, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.seen = append(a.seen, sb.Env["ANTHROPIC_API_KEY"])
	if len(a.seen) == 1 {
		return &agent.AgentResult{ExitCode: 1, Error: "rate limit exceeded retry-after=60"}, errors.New("rate limit exceeded")
	}
	if a.sandbox.writePath == "" {
		return &agent.AgentResult{ExitCode: 1, Error: "missing claude subscription file"}, errors.New("missing claude subscription file")
	}
	return &agent.AgentResult{Summary: "ok", ExitCode: 0}, nil
}

func (a *helperClaudeRetryAdapter) ResumeMode() agent.SessionResumeMode {
	return agent.ResumeUnsupported
}

func (a *helperClaudeRetryAdapter) keys() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	return append([]string(nil), a.seen...)
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
			settings: models.OrgSettings{DefaultAgentType: models.AgentTypeOpenCode},
			expected: models.AgentTypeOpenCode,
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
		"PI_MODEL_CUSTOM": "moonshot/kimi-k2.6",
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

func TestExecuteAgentWithCredentialFallbackRetriesRateLimitedCredential(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	orgID := uuid.New()
	firstCredID := uuid.New()
	secondCredID := uuid.New()
	codingCreds := &helperCodingCredentialProvider{
		resolvable: map[models.ProviderName][]models.DecryptedCodingCredential{
			models.ProviderAmp: {
				{
					ID:        firstCredID,
					OrgID:     orgID,
					Provider:  models.ProviderAmp,
					Config:    models.AmpConfig{APIKey: "amp-first"},
					Priority:  1,
					Status:    models.CodingCredentialStatusActive,
					CreatedAt: time.Now(),
				},
				{
					ID:        secondCredID,
					OrgID:     orgID,
					Provider:  models.ProviderAmp,
					Config:    models.AmpConfig{APIKey: "amp-fallback"},
					Priority:  2,
					Status:    models.CodingCredentialStatusActive,
					CreatedAt: time.Now(),
				},
			},
		},
	}
	sandboxProvider := &pmSandboxMock{}
	env := agent.NewAgentEnv(agent.AgentEnvDeps{
		CodingCredentials: codingCreds,
		Provider:          sandboxProvider,
		Logger:            zerolog.Nop(),
	})
	svc := &Service{
		env:     env,
		sandbox: sandboxProvider,
		logger:  zerolog.Nop(),
	}
	sbCfg := agent.SandboxConfig{
		HomeDir: "/home/sandbox",
		Env:     env.Resolve(ctx, orgID, models.AgentTypeAmp, nil),
	}
	sb := &agent.Sandbox{ID: "pm-sandbox", HomeDir: sbCfg.HomeDir, Env: sbCfg.Env}
	adapter := &helperRetryAdapter{}
	prompt := &agent.AgentPrompt{}
	logCh := make(chan agent.LogEntry, 10)

	result, err := svc.executeAgentWithCredentialFallback(ctx, orgID, models.AgentTypeAmp, models.OrgSettings{}, sbCfg, sb, adapter, ctx, prompt, logCh)

	require.NoError(t, err, "PM execution should retry with a fallback credential after a rate-limit result")
	require.Equal(t, &agent.AgentResult{Summary: "ok", ExitCode: 0}, result, "PM execution should return the successful fallback result")
	require.Equal(t, []string{"amp-first", "amp-fallback"}, adapter.keys(), "PM execution should refresh credentials before retrying")
	require.Contains(t, codingCreds.rateLimitedIDs, firstCredID, "PM execution should mark the first credential rate-limited")
}

func TestExecuteAgentWithCredentialFallbackRetriesClaudeAPIKeyWithSubscription(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	orgID := uuid.New()
	apiCredID := uuid.New()
	subCredID := uuid.New()
	codingCreds := &helperCodingCredentialProvider{
		resolvable: map[models.ProviderName][]models.DecryptedCodingCredential{
			models.ProviderAnthropic: {
				{
					ID:        apiCredID,
					OrgID:     orgID,
					Provider:  models.ProviderAnthropic,
					Config:    models.AnthropicConfig{APIKey: "anthropic-first"},
					Priority:  1,
					Status:    models.CodingCredentialStatusActive,
					CreatedAt: time.Now(),
				},
			},
			models.ProviderAnthropicSubscription: {
				{
					ID:       subCredID,
					OrgID:    orgID,
					Provider: models.ProviderAnthropicSubscription,
					Config: models.AnthropicSubscriptionConfig{
						AccessToken:   "claude-access",
						RefreshToken:  "claude-refresh",
						ExpiresAt:     time.Now().Add(time.Hour),
						AccountType:   "pro",
						RateLimitTier: "default",
						Scopes:        []string{"org:create_api_key"},
					},
					Priority:  2,
					Status:    models.CodingCredentialStatusActive,
					CreatedAt: time.Now(),
				},
			},
		},
	}
	sandboxProvider := &pmSandboxMock{}
	env := agent.NewAgentEnv(agent.AgentEnvDeps{
		CodingCredentials: codingCreds,
		Provider:          sandboxProvider,
		Logger:            zerolog.Nop(),
	})
	svc := &Service{
		env:     env,
		sandbox: sandboxProvider,
		logger:  zerolog.Nop(),
	}
	sbCfg := agent.SandboxConfig{
		HomeDir: "/home/sandbox",
		Env:     env.Resolve(ctx, orgID, models.AgentTypeClaudeCode, nil),
	}
	sb := &agent.Sandbox{ID: "pm-sandbox", HomeDir: sbCfg.HomeDir, Env: sbCfg.Env}
	adapter := &helperClaudeRetryAdapter{sandbox: sandboxProvider}
	prompt := &agent.AgentPrompt{}
	logCh := make(chan agent.LogEntry, 10)

	result, err := svc.executeAgentWithCredentialFallback(ctx, orgID, models.AgentTypeClaudeCode, models.OrgSettings{}, sbCfg, sb, adapter, ctx, prompt, logCh)

	require.NoError(t, err, "PM Claude execution should retry with subscription credentials after API-key rate limiting")
	require.Equal(t, &agent.AgentResult{Summary: "ok", ExitCode: 0}, result, "PM Claude execution should return the successful subscription fallback result")
	require.Equal(t, []string{"anthropic-first", ""}, adapter.keys(), "PM Claude execution should clear ANTHROPIC_API_KEY when retrying with subscription auth")
	require.Equal(t, "/home/sandbox/.claude/.credentials.json", sandboxProvider.writePath, "PM Claude retry should write the subscription credentials file")
	require.Contains(t, string(sandboxProvider.writeData), "claude-access", "PM Claude retry should write the selected subscription token")
	require.Contains(t, codingCreds.rateLimitedIDs, apiCredID, "PM Claude execution should mark the API key rate-limited")
	require.NotContains(t, codingCreds.rateLimitedIDs, subCredID, "PM Claude execution should not mark the successful subscription fallback rate-limited")
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
