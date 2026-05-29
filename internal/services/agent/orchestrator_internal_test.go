package agent

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/sandbox"
	"github.com/assembledhq/143/internal/services/sandboxauth"
	"github.com/assembledhq/143/internal/services/workspace"
)

func TestRunAgentRecordsUsageOnlyAfterTurnHoldIsPublished(t *testing.T) {
	t.Parallel()

	src, err := os.ReadFile("orchestrator.go")
	require.NoError(t, err, "orchestrator.go should be readable for lifecycle ordering regression test")

	body := string(src)
	runStart := strings.Index(body, "func (o *Orchestrator) RunAgent(")
	continueStart := strings.Index(body, "func (o *Orchestrator) ContinueSession(")
	require.NotEqual(t, -1, runStart, "RunAgent should exist")
	require.NotEqual(t, -1, continueStart, "ContinueSession should exist")
	require.Less(t, runStart, continueStart, "RunAgent should appear before ContinueSession in orchestrator.go")

	runBody := body[runStart:continueStart]
	hold := strings.Index(runBody, "o.sessions.AcquireTurnHold")
	usage := strings.Index(runBody, "o.usageTracker.ContainerStarted")
	require.NotEqual(t, -1, hold, "RunAgent should publish the turn hold")
	require.NotEqual(t, -1, usage, "RunAgent should record container usage")
	require.Less(t, hold, usage, "RunAgent should record usage only after the DB row owns the container so pre-hold crashes do not create open usage events for unowned containers")
}

func TestThreadRuntimeAlreadyActiveDoesNotFailSessionBeforeRetry(t *testing.T) {
	t.Parallel()

	src, err := os.ReadFile("orchestrator.go")
	require.NoError(t, err, "orchestrator.go should be readable for active-runtime retry regression test")

	body := string(src)
	for _, tt := range []struct {
		name      string
		start     string
		nextStart string
	}{
		{
			name:      "RunAgent",
			start:     "threadRuntimeCtl, err = o.startThreadRuntimeControl(ctx, run, *primaryThreadID, sandbox",
			nextStart: "if threadRuntimeCtl != nil {",
		},
		{
			name:      "ContinueSession",
			start:     "threadRuntimeCtl, err = o.startThreadRuntimeControl(ctx, session, *opts.ThreadID, sandbox",
			nextStart: "if threadRuntimeCtl != nil {",
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			start := strings.Index(body, tt.start)
			require.NotEqual(t, -1, start, "orchestrator should start thread runtime control in "+tt.name)
			remainder := body[start:]
			end := strings.Index(remainder, tt.nextStart)
			require.NotEqual(t, -1, end, "orchestrator should continue after thread runtime control in "+tt.name)

			block := remainder[:end]
			activeRuntimeCheck := strings.Index(block, "errors.Is(err, ErrThreadRuntimeAlreadyActive)")
			failRun := strings.Index(block, "o.failRun")
			require.NotEqual(t, -1, activeRuntimeCheck, "active runtime conflicts should be recognized before generic failure cleanup in "+tt.name)
			require.NotEqual(t, -1, failRun, "non-retryable thread runtime startup errors should still use generic failure cleanup in "+tt.name)
			require.Less(t, activeRuntimeCheck, failRun, "active runtime conflicts should return for worker retry before marking the session failed in "+tt.name)
		})
	}
}

func TestWarmMentionIndexFromSandboxAsyncDoesNotBlockCaller(t *testing.T) {
	t.Parallel()

	release := make(chan struct{})
	reader := &blockingMentionIndexFileReader{release: release}
	o := &Orchestrator{
		fileReader:     reader,
		mentionIndexes: workspace.NewMentionIndexCache(workspace.MentionIndexCacheConfig{}),
	}
	session := &models.Session{ID: uuid.New(), OrgID: uuid.New()}
	liveSandbox := &Sandbox{ID: "container-1", WorkDir: "/workspace"}

	done := make(chan struct{})
	go func() {
		o.warmMentionIndexFromSandboxAsync(context.Background(), session, liveSandbox, "snapshot-1", zerolog.Nop())
		close(done)
	}()

	select {
	case <-done:
	case <-time.After(25 * time.Millisecond):
		close(release)
		require.Fail(t, "async mention-index warmup should return before the workspace traversal completes")
		return
	}
	close(release)
}

type blockingMentionIndexFileReader struct {
	release chan struct{}
}

func (r *blockingMentionIndexFileReader) ListDir(ctx context.Context, _, _, _ string) ([]sandbox.FileEntry, error) {
	select {
	case <-r.release:
		return nil, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func (r *blockingMentionIndexFileReader) ReadFile(context.Context, string, string, string) (string, bool, error) {
	return "", false, errors.New("not used")
}

func (r *blockingMentionIndexFileReader) ReadFileContext(context.Context, string, string, string, int, int, int) (sandbox.FileContextResult, error) {
	return sandbox.FileContextResult{}, errors.New("not used")
}

type testInternalSessionLogStore struct {
	logs             []models.SessionLog
	markedThreadID   *uuid.UUID
	markedOrgID      uuid.UUID
	markedSessionID  uuid.UUID
	markedTurnNumber int
	markedMessage    string
}

func (s *testInternalSessionLogStore) Create(ctx context.Context, log *models.SessionLog) error {
	s.logs = append(s.logs, *log)
	return nil
}

func (s *testInternalSessionLogStore) MarkAssistantTranscriptDuplicate(
	ctx context.Context,
	orgID, sessionID uuid.UUID,
	threadID *uuid.UUID,
	turnNumber int,
	message string,
) error {
	s.markedOrgID = orgID
	s.markedSessionID = sessionID
	if threadID != nil {
		copied := *threadID
		s.markedThreadID = &copied
	}
	s.markedTurnNumber = turnNumber
	s.markedMessage = message
	return nil
}

type testInternalSessionMessageStore struct {
	messages []models.SessionMessage
}

func (s *testInternalSessionMessageStore) Create(ctx context.Context, msg *models.SessionMessage) error {
	s.messages = append(s.messages, *msg)
	return nil
}

func (s *testInternalSessionMessageStore) ListBySession(ctx context.Context, orgID, sessionID uuid.UUID) ([]models.SessionMessage, error) {
	return nil, nil
}

type testInternalUserLookup struct {
	user models.User
	err  error
}

func (s testInternalUserLookup) GetByID(context.Context, uuid.UUID, uuid.UUID) (models.User, error) {
	if s.err != nil {
		return models.User{}, s.err
	}
	return s.user, nil
}

type testInternalIssueStore struct {
	issue models.Issue
	err   error
}

func (s testInternalIssueStore) GetByID(context.Context, uuid.UUID, uuid.UUID) (models.Issue, error) {
	if s.err != nil {
		return models.Issue{}, s.err
	}
	return s.issue, nil
}

func (s testInternalIssueStore) UpdateStatus(context.Context, uuid.UUID, uuid.UUID, models.IssueStatus) error {
	return s.err
}

type testInternalRepoStore struct {
	repo models.Repository
	err  error
}

func (s testInternalRepoStore) GetByID(context.Context, uuid.UUID, uuid.UUID) (models.Repository, error) {
	if s.err != nil {
		return models.Repository{}, s.err
	}
	return s.repo, nil
}

type testInternalGitHubTokens struct {
	token string
	err   error
}

func (s testInternalGitHubTokens) GetInstallationToken(context.Context, int64) (string, error) {
	if s.err != nil {
		return "", s.err
	}
	return s.token, nil
}

type testInternalSandboxProvider struct {
	execExit   int
	execErr    error
	execStderr string
	execCalls  []string
	writes     map[string][]byte
}

func (p *testInternalSandboxProvider) Name() string { return "test" }

func (p *testInternalSandboxProvider) Create(context.Context, SandboxConfig) (*Sandbox, error) {
	return nil, nil
}

func (p *testInternalSandboxProvider) CloneRepo(context.Context, *Sandbox, string, string, string) error {
	return nil
}

func (p *testInternalSandboxProvider) Exec(_ context.Context, _ *Sandbox, cmd string, stdout, stderr io.Writer) (int, error) {
	p.execCalls = append(p.execCalls, cmd)
	if p.execStderr != "" {
		_, _ = io.WriteString(stderr, p.execStderr)
	}
	return p.execExit, p.execErr
}

func (p *testInternalSandboxProvider) ReadFile(context.Context, *Sandbox, string) ([]byte, error) {
	return nil, nil
}

func (p *testInternalSandboxProvider) WriteFile(_ context.Context, _ *Sandbox, path string, data []byte) error {
	if p.writes == nil {
		p.writes = make(map[string][]byte)
	}
	p.writes[path] = append([]byte(nil), data...)
	return nil
}

func (p *testInternalSandboxProvider) Destroy(context.Context, *Sandbox) error {
	return nil
}

func (p *testInternalSandboxProvider) IsAlive(context.Context, *Sandbox) (bool, error) {
	return true, nil
}

func (p *testInternalSandboxProvider) ConnectionInfo(context.Context, *Sandbox) (*SandboxConnectionInfo, error) {
	return nil, nil
}

func (p *testInternalSandboxProvider) Snapshot(context.Context, *Sandbox) (io.ReadCloser, error) {
	return io.NopCloser(nil), nil
}

func (p *testInternalSandboxProvider) Restore(context.Context, *Sandbox, io.Reader) error {
	return nil
}

func (p *testInternalSandboxProvider) ExecStream(context.Context, *Sandbox, string, func([]byte), io.Writer) (int, error) {
	return 0, nil
}

type testInternalCodingCredentialProvider struct {
	resolvable map[models.ProviderName][]models.DecryptedCodingCredential
}

func (p testInternalCodingCredentialProvider) ListResolvable(_ context.Context, _ uuid.UUID, _ *uuid.UUID, provider models.ProviderName) ([]models.DecryptedCodingCredential, error) {
	return p.resolvable[provider], nil
}

type testInternalQueuedCodingCredentialProvider struct {
	resolvable map[models.ProviderName][]models.DecryptedCodingCredential
	picks      []models.DecryptedCodingCredential
}

func (p *testInternalQueuedCodingCredentialProvider) ListResolvable(_ context.Context, _ uuid.UUID, _ *uuid.UUID, provider models.ProviderName) ([]models.DecryptedCodingCredential, error) {
	return p.resolvable[provider], nil
}

func (p *testInternalQueuedCodingCredentialProvider) PickRunnableMulti(_ context.Context, _ models.Scope, _ []models.ProviderName) (*models.DecryptedCodingCredential, error) {
	if len(p.picks) == 0 {
		return nil, errEnvCodingCredentialNotFound
	}
	picked := p.picks[0]
	p.picks = p.picks[1:]
	return &picked, nil
}

type testInternalClaudeCodeAuthProvider struct {
	sub *models.AnthropicSubscription
	id  uuid.UUID
}

func (p testInternalClaudeCodeAuthProvider) HasActiveSubscription(context.Context, uuid.UUID) (bool, error) {
	return p.sub != nil, nil
}

func (p testInternalClaudeCodeAuthProvider) GetValidToken(context.Context, uuid.UUID) (*models.AnthropicSubscription, *uuid.UUID, error) {
	if p.sub == nil {
		return nil, nil, nil
	}
	id := p.id
	return p.sub, &id, nil
}

type testInternalOrgStore struct {
	org models.Organization
	err error
}

func (s testInternalOrgStore) GetByID(context.Context, uuid.UUID) (models.Organization, error) {
	if s.err != nil {
		return models.Organization{}, s.err
	}
	return s.org, nil
}

func TestSetupFreshSandbox_CodexAPIKeyUsesResolvedEnv(t *testing.T) {
	t.Parallel()

	orgID := uuid.MustParse("11111111-1111-1111-1111-111111111111")
	userID := uuid.MustParse("22222222-2222-2222-2222-222222222222")
	credID := uuid.MustParse("33333333-3333-3333-3333-333333333333")
	provider := &testInternalSandboxProvider{}
	env := NewAgentEnv(AgentEnvDeps{
		CodingCredentials: testInternalCodingCredentialProvider{
			resolvable: map[models.ProviderName][]models.DecryptedCodingCredential{
				models.ProviderOpenAI: {
					{
						ID:       credID,
						OrgID:    orgID,
						UserID:   &userID,
						Provider: models.ProviderOpenAI,
						Priority: 1,
						Status:   models.CodingCredentialStatusActive,
						Config:   models.OpenAIConfig{APIKey: "sk-openai"},
					},
				},
			},
		},
		Provider: provider,
		Logger:   zerolog.Nop(),
	})
	orch := &Orchestrator{
		env:      env,
		provider: provider,
		logger:   zerolog.Nop(),
	}
	session := &models.Session{
		ID:                uuid.MustParse("44444444-4444-4444-4444-444444444444"),
		OrgID:             orgID,
		AgentType:         models.AgentTypeCodex,
		TriggeredByUserID: &userID,
	}

	_, _, _, err := orch.setupFreshSandbox(context.Background(), session, &Sandbox{ID: "sandbox-1", HomeDir: "/home/sandbox", WorkDir: "/home/sandbox/work"}, map[string]string{
		"OPENAI_API_KEY": "sk-openai",
	}, nil)

	require.NoError(t, err, "setupFreshSandbox should accept the already-resolved Codex API key")
	require.NotContains(t, provider.writes, "/home/sandbox/.codex/auth.json", "setupFreshSandbox should not require Codex auth.json when the selected unified credential is an API key")
}

func TestSetupFreshSandbox_CodexLegacyAPIKeyFallbackUsesResolvedEnv(t *testing.T) {
	t.Parallel()

	orgID := uuid.MustParse("10101010-1111-2222-3333-444444444444")
	userID := uuid.MustParse("55555555-6666-7777-8888-999999999999")
	provider := &testInternalSandboxProvider{}
	env := NewAgentEnv(AgentEnvDeps{
		Credentials: &envCredentialProvider{
			creds: map[models.ProviderName]*models.DecryptedCredential{
				models.ProviderOpenAI: {
					OrgID:  orgID,
					Status: models.CredentialStatusActive,
					Config: models.OpenAIConfig{APIKey: "sk-legacy-openai"},
				},
			},
		},
		Provider: provider,
		Logger:   zerolog.Nop(),
	})
	orch := &Orchestrator{
		env:      env,
		provider: provider,
		logger:   zerolog.Nop(),
	}
	session := &models.Session{
		ID:                uuid.MustParse("aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"),
		OrgID:             orgID,
		AgentType:         models.AgentTypeCodex,
		TriggeredByUserID: &userID,
	}
	envVars := env.Resolve(context.Background(), orgID, models.AgentTypeCodex, &userID)

	_, _, billingMode, err := orch.setupFreshSandbox(context.Background(), session, &Sandbox{ID: "sandbox-legacy", HomeDir: "/home/sandbox", WorkDir: "/home/sandbox/work"}, envVars, nil)

	require.NoError(t, err, "setupFreshSandbox should continue to honor the documented legacy OpenAI API-key fallback")
	require.Equal(t, TokenBillingModeAPIKey, billingMode, "setupFreshSandbox should classify the legacy OpenAI credential as an API-key billing mode")
	require.NotContains(t, provider.writes, "/home/sandbox/.codex/auth.json", "setupFreshSandbox should not require Codex auth.json when the legacy fallback resolved an OpenAI API key")
}

func TestBuildTokenUsageHint_PreservesExplicitClaudeSubscriptionBillingMode(t *testing.T) {
	t.Parallel()

	orgID := uuid.MustParse("aaaaaaaa-aaaa-aaaa-aaaa-aaaaaaaaaaaa")
	userID := uuid.MustParse("bbbbbbbb-bbbb-bbbb-bbbb-bbbbbbbbbbbb")
	orch := &Orchestrator{logger: zerolog.Nop()}

	actual := orch.buildTokenUsageHint(context.Background(), models.AgentTypeClaudeCode, orgID, &userID, map[string]string{
		"ANTHROPIC_API_KEY": "sk-ant-fallback",
		"ANTHROPIC_MODEL":   models.ClaudeCodeModelSonnet46,
	}, TokenUsageHint{
		AgentType:      models.AgentTypeClaudeCode,
		EffectiveModel: models.ClaudeCodeModelSonnet46,
		BillingMode:    TokenBillingModeSubscription,
	})

	require.Equal(t, TokenBillingModeSubscription, actual.BillingMode, "explicit billing mode from the auth path should not be overwritten by the fallback Anthropic API key env var")
	require.Equal(t, models.ClaudeCodeModelSonnet46, actual.EffectiveModel, "buildTokenUsageHint should retain the effective model")
}

func TestBuildTokenUsageHint_UsesAgentConfigModelDefaultsWhenEnvOmitsThem(t *testing.T) {
	t.Parallel()

	orgID := uuid.MustParse("cccccccc-cccc-cccc-cccc-cccccccccccc")
	userID := uuid.MustParse("dddddddd-dddd-dddd-dddd-dddddddddddd")
	orgSettings := json.RawMessage(`{"agent_config":{"codex":{"OPENAI_MODEL":"gpt-5.4"},"claude_code":{"ANTHROPIC_MODEL":"claude-sonnet-4-6"},"gemini_cli":{"GEMINI_MODEL":"gemini-2.5-pro"}}}`)
	env := NewAgentEnv(AgentEnvDeps{
		Orgs: testInternalOrgStore{
			org: models.Organization{
				ID:       orgID,
				Settings: orgSettings,
			},
		},
		Logger: zerolog.Nop(),
	})
	orch := &Orchestrator{
		env:    env,
		logger: zerolog.Nop(),
	}

	tests := []struct {
		name      string
		agentType models.AgentType
		expected  string
	}{
		{name: "codex", agentType: models.AgentTypeCodex, expected: models.CodexModelGPT54},
		{name: "claude", agentType: models.AgentTypeClaudeCode, expected: models.ClaudeCodeModelSonnet46},
		{name: "gemini", agentType: models.AgentTypeGeminiCLI, expected: models.GeminiCLIModelGemini25Pro},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			actual := orch.buildTokenUsageHint(context.Background(), tt.agentType, orgID, &userID, map[string]string{}, TokenUsageHint{AgentType: tt.agentType})
			require.Equal(t, tt.expected, actual.EffectiveModel, "buildTokenUsageHint should recover the agent_config model default when env injection is intentionally skipped")
		})
	}
}

func TestSetupFreshSandbox_CodexAPIKeyDoesNotRePickSubscriptionAtSamePriority(t *testing.T) {
	t.Parallel()

	orgID := uuid.MustParse("01010101-0101-0101-0101-010101010101")
	userID := uuid.MustParse("02020202-0202-0202-0202-020202020202")
	apiKeyID := uuid.MustParse("03030303-0303-0303-0303-030303030303")
	subID := uuid.MustParse("04040404-0404-0404-0404-040404040404")
	apiKeyRow := models.DecryptedCodingCredential{
		ID:       apiKeyID,
		OrgID:    orgID,
		UserID:   &userID,
		Provider: models.ProviderOpenAI,
		Priority: 1,
		Status:   models.CodingCredentialStatusActive,
		Config:   models.OpenAIConfig{APIKey: "sk-openai-api-key", APIType: "responses"},
	}
	subRow := models.DecryptedCodingCredential{
		ID:       subID,
		OrgID:    orgID,
		UserID:   &userID,
		Provider: models.ProviderOpenAISubscription,
		Priority: 1,
		Status:   models.CodingCredentialStatusActive,
		Config: models.OpenAISubscriptionConfig{
			AccessToken:  "same-priority-codex-access",
			RefreshToken: "same-priority-codex-refresh",
			ExpiresAt:    time.Now().Add(time.Hour),
			AccountType:  "plus",
		},
	}
	coding := &testInternalQueuedCodingCredentialProvider{
		resolvable: map[models.ProviderName][]models.DecryptedCodingCredential{
			models.ProviderOpenAI:             {apiKeyRow},
			models.ProviderOpenAISubscription: {subRow},
		},
		picks: []models.DecryptedCodingCredential{apiKeyRow, subRow},
	}
	provider := &testInternalSandboxProvider{}
	env := NewAgentEnv(AgentEnvDeps{
		CodingCredentials: coding,
		Provider:          provider,
		Logger:            zerolog.Nop(),
	})
	orch := &Orchestrator{
		env:      env,
		provider: provider,
		logger:   zerolog.Nop(),
	}
	session := &models.Session{
		ID:                uuid.MustParse("05050505-0505-0505-0505-050505050505"),
		OrgID:             orgID,
		AgentType:         models.AgentTypeCodex,
		TriggeredByUserID: &userID,
	}
	envVars := env.Resolve(context.Background(), orgID, models.AgentTypeCodex, &userID)

	_, _, _, err := orch.setupFreshSandbox(context.Background(), session, &Sandbox{ID: "sandbox-1", HomeDir: "/home/sandbox", WorkDir: "/home/sandbox/work"}, envVars, nil)

	require.NoError(t, err, "setupFreshSandbox should use the already-resolved Codex API key")
	require.NotContains(t, provider.writes, "/home/sandbox/.codex/auth.json", "setupFreshSandbox should not re-pick a same-priority Codex subscription after env resolution selected an API key")
}

func TestSetupFreshSandbox_ClaudeAPIKeyDoesNotInjectLowerPrioritySubscription(t *testing.T) {
	t.Parallel()

	orgID := uuid.MustParse("55555555-5555-5555-5555-555555555555")
	userID := uuid.MustParse("66666666-6666-6666-6666-666666666666")
	apiKeyID := uuid.MustParse("77777777-7777-7777-7777-777777777777")
	subID := uuid.MustParse("88888888-8888-8888-8888-888888888888")
	provider := &testInternalSandboxProvider{}
	env := NewAgentEnv(AgentEnvDeps{
		CodingCredentials: testInternalCodingCredentialProvider{
			resolvable: map[models.ProviderName][]models.DecryptedCodingCredential{
				models.ProviderAnthropic: {
					{
						ID:       apiKeyID,
						OrgID:    orgID,
						UserID:   &userID,
						Provider: models.ProviderAnthropic,
						Priority: 1,
						Status:   models.CodingCredentialStatusActive,
						Config:   models.AnthropicConfig{APIKey: "sk-ant-api-key"},
					},
				},
				models.ProviderAnthropicSubscription: {
					{
						ID:       subID,
						OrgID:    orgID,
						UserID:   &userID,
						Provider: models.ProviderAnthropicSubscription,
						Priority: 2,
						Status:   models.CodingCredentialStatusActive,
						Config: models.AnthropicSubscriptionConfig{
							AccessToken:  "lower-priority-access",
							RefreshToken: "lower-priority-refresh",
							ExpiresAt:    time.Now().Add(time.Hour),
						},
					},
				},
			},
		},
		Provider: provider,
		Logger:   zerolog.Nop(),
	})
	orch := &Orchestrator{
		env:      env,
		provider: provider,
		logger:   zerolog.Nop(),
		claudeCodeAuth: testInternalClaudeCodeAuthProvider{
			id: subID,
			sub: &models.AnthropicSubscription{
				AccessToken:  "lower-priority-access",
				RefreshToken: "lower-priority-refresh",
				ExpiresAt:    time.Now().Add(time.Hour),
			},
		},
	}
	session := &models.Session{
		ID:                uuid.MustParse("99999999-9999-9999-9999-999999999999"),
		OrgID:             orgID,
		AgentType:         models.AgentTypeClaudeCode,
		TriggeredByUserID: &userID,
	}

	_, _, _, err := orch.setupFreshSandbox(context.Background(), session, &Sandbox{ID: "sandbox-1", HomeDir: "/home/sandbox", WorkDir: "/home/sandbox/work"}, map[string]string{
		"ANTHROPIC_API_KEY": "sk-ant-api-key",
	}, nil)

	require.NoError(t, err, "setupFreshSandbox should accept the already-resolved Claude API key")
	require.NotContains(t, provider.writes, "/home/sandbox/.claude/.credentials.json", "setupFreshSandbox should not inject a lower-priority Claude subscription over the selected API key")
}

func TestSetupFreshSandbox_ClaudeAPIKeyDoesNotRePickSubscriptionAtSamePriority(t *testing.T) {
	t.Parallel()

	orgID := uuid.MustParse("12121212-1212-1212-1212-121212121212")
	userID := uuid.MustParse("23232323-2323-2323-2323-232323232323")
	apiKeyID := uuid.MustParse("34343434-3434-3434-3434-343434343434")
	subID := uuid.MustParse("45454545-4545-4545-4545-454545454545")
	apiKeyRow := models.DecryptedCodingCredential{
		ID:       apiKeyID,
		OrgID:    orgID,
		UserID:   &userID,
		Provider: models.ProviderAnthropic,
		Priority: 1,
		Status:   models.CodingCredentialStatusActive,
		Config:   models.AnthropicConfig{APIKey: "sk-ant-api-key"},
	}
	subRow := models.DecryptedCodingCredential{
		ID:       subID,
		OrgID:    orgID,
		UserID:   &userID,
		Provider: models.ProviderAnthropicSubscription,
		Priority: 1,
		Status:   models.CodingCredentialStatusActive,
		Config: models.AnthropicSubscriptionConfig{
			AccessToken:  "same-priority-access",
			RefreshToken: "same-priority-refresh",
			ExpiresAt:    time.Now().Add(time.Hour),
		},
	}
	coding := &testInternalQueuedCodingCredentialProvider{
		resolvable: map[models.ProviderName][]models.DecryptedCodingCredential{
			models.ProviderAnthropic:             {apiKeyRow},
			models.ProviderAnthropicSubscription: {subRow},
		},
		picks: []models.DecryptedCodingCredential{apiKeyRow, subRow},
	}
	provider := &testInternalSandboxProvider{}
	env := NewAgentEnv(AgentEnvDeps{
		CodingCredentials: coding,
		Provider:          provider,
		Logger:            zerolog.Nop(),
	})
	orch := &Orchestrator{
		env:      env,
		provider: provider,
		logger:   zerolog.Nop(),
		claudeCodeAuth: testInternalClaudeCodeAuthProvider{
			id: subID,
			sub: &models.AnthropicSubscription{
				AccessToken:  "same-priority-access",
				RefreshToken: "same-priority-refresh",
				ExpiresAt:    time.Now().Add(time.Hour),
			},
		},
	}
	session := &models.Session{
		ID:                uuid.MustParse("56565656-5656-5656-5656-565656565656"),
		OrgID:             orgID,
		AgentType:         models.AgentTypeClaudeCode,
		TriggeredByUserID: &userID,
	}
	envVars := env.Resolve(context.Background(), orgID, models.AgentTypeClaudeCode, &userID)

	_, _, _, err := orch.setupFreshSandbox(context.Background(), session, &Sandbox{ID: "sandbox-1", HomeDir: "/home/sandbox", WorkDir: "/home/sandbox/work"}, envVars, nil)

	require.NoError(t, err, "setupFreshSandbox should use the already-resolved Claude API key")
	require.NotContains(t, provider.writes, "/home/sandbox/.claude/.credentials.json", "setupFreshSandbox should not re-pick a same-priority subscription after env resolution selected an API key")
}

func TestSetupFreshSandbox_ClaudeSubscriptionUsesUnifiedPickedToken(t *testing.T) {
	t.Parallel()

	orgID := uuid.MustParse("67676767-6767-6767-6767-676767676767")
	userID := uuid.MustParse("78787878-7878-7878-7878-787878787878")
	unifiedID := uuid.MustParse("89898989-8989-8989-8989-898989898989")
	legacyID := uuid.MustParse("90909090-9090-9090-9090-909090909090")
	unifiedRow := models.DecryptedCodingCredential{
		ID:       unifiedID,
		OrgID:    orgID,
		UserID:   &userID,
		Provider: models.ProviderAnthropicSubscription,
		Priority: 1,
		Status:   models.CodingCredentialStatusActive,
		Config: models.AnthropicSubscriptionConfig{
			AccessToken:   "unified-access",
			RefreshToken:  "unified-refresh",
			ExpiresAt:     time.Now().Add(time.Hour),
			AccountType:   "claude_pro",
			RateLimitTier: "default",
			Scopes:        []string{"user:inference"},
		},
	}
	coding := &testInternalQueuedCodingCredentialProvider{
		resolvable: map[models.ProviderName][]models.DecryptedCodingCredential{
			models.ProviderAnthropicSubscription: {unifiedRow},
		},
		picks: []models.DecryptedCodingCredential{unifiedRow, unifiedRow},
	}
	provider := &testInternalSandboxProvider{}
	env := NewAgentEnv(AgentEnvDeps{
		CodingCredentials: coding,
		Provider:          provider,
		Logger:            zerolog.Nop(),
	})
	orch := &Orchestrator{
		env:      env,
		provider: provider,
		logger:   zerolog.Nop(),
		claudeCodeAuth: testInternalClaudeCodeAuthProvider{
			id: legacyID,
			sub: &models.AnthropicSubscription{
				AccessToken:  "legacy-access",
				RefreshToken: "legacy-refresh",
				ExpiresAt:    time.Now().Add(time.Hour),
			},
		},
	}
	session := &models.Session{
		ID:                uuid.MustParse("abababab-abab-abab-abab-abababababab"),
		OrgID:             orgID,
		AgentType:         models.AgentTypeClaudeCode,
		TriggeredByUserID: &userID,
	}

	_, _, _, err := orch.setupFreshSandbox(context.Background(), session, &Sandbox{ID: "sandbox-1", HomeDir: "/home/sandbox", WorkDir: "/home/sandbox/work"}, map[string]string{}, nil)

	require.NoError(t, err, "setupFreshSandbox should inject the selected unified Claude subscription")
	written := provider.writes["/home/sandbox/.claude/.credentials.json"]
	require.Contains(t, string(written), "unified-access", "Claude credentials file should use the unified resolver's selected subscription")
	require.NotContains(t, string(written), "legacy-access", "Claude credentials file should not fall back to the legacy org-wide subscription when unified selected a row")
}

func TestSetupFreshSandbox_ReturnsResolvedAuthBillingMode(t *testing.T) {
	t.Parallel()

	orgID := uuid.MustParse("a1a1a1a1-a1a1-a1a1-a1a1-a1a1a1a1a1a1")
	userID := uuid.MustParse("b2b2b2b2-b2b2-b2b2-b2b2-b2b2b2b2b2b2")
	unifiedID := uuid.MustParse("c3c3c3c3-c3c3-c3c3-c3c3-c3c3c3c3c3c3")
	unifiedRow := models.DecryptedCodingCredential{
		ID:       unifiedID,
		OrgID:    orgID,
		UserID:   &userID,
		Provider: models.ProviderAnthropicSubscription,
		Priority: 1,
		Status:   models.CodingCredentialStatusActive,
		Config: models.AnthropicSubscriptionConfig{
			AccessToken:  "fresh-auth-access",
			RefreshToken: "fresh-auth-refresh",
			ExpiresAt:    time.Now().Add(time.Hour),
		},
	}
	coding := &testInternalQueuedCodingCredentialProvider{
		resolvable: map[models.ProviderName][]models.DecryptedCodingCredential{
			models.ProviderAnthropicSubscription: {unifiedRow},
		},
		picks: []models.DecryptedCodingCredential{unifiedRow},
	}
	provider := &testInternalSandboxProvider{}
	env := NewAgentEnv(AgentEnvDeps{
		CodingCredentials: coding,
		Provider:          provider,
		Logger:            zerolog.Nop(),
	})
	orch := &Orchestrator{
		env:      env,
		provider: provider,
		logger:   zerolog.Nop(),
		claudeCodeAuth: testInternalClaudeCodeAuthProvider{
			id: unifiedID,
			sub: &models.AnthropicSubscription{
				AccessToken:  "fresh-auth-access",
				RefreshToken: "fresh-auth-refresh",
				ExpiresAt:    time.Now().Add(time.Hour),
			},
		},
	}
	session := &models.Session{
		ID:                uuid.MustParse("d4d4d4d4-d4d4-d4d4-d4d4-d4d4d4d4d4d4"),
		OrgID:             orgID,
		AgentType:         models.AgentTypeClaudeCode,
		TriggeredByUserID: &userID,
	}

	_, _, billingMode, err := orch.setupFreshSandbox(context.Background(), session, &Sandbox{ID: "sandbox-1", HomeDir: "/home/sandbox", WorkDir: "/home/sandbox/work"}, map[string]string{}, nil)

	require.NoError(t, err, "setupFreshSandbox should succeed for a fresh Claude subscription run")
	require.Equal(t, TokenBillingModeSubscription, billingMode, "setupFreshSandbox should return the auth-selected billing mode for fresh runs")
}

func TestCreateAssistantMessage_CarriesThreadID(t *testing.T) {
	t.Parallel()

	orgID := uuid.MustParse("22222222-2222-2222-2222-222222222222")
	sessionID := uuid.MustParse("11111111-1111-1111-1111-111111111111")
	threadID := uuid.MustParse("33333333-3333-3333-3333-333333333333")

	logs := &testInternalSessionLogStore{}
	messages := &testInternalSessionMessageStore{}
	orch := &Orchestrator{
		agentRunLogs:    logs,
		sessionMessages: messages,
		logger:          zerolog.Nop(),
	}

	err := orch.createAssistantMessage(context.Background(), sessionID, orgID, &threadID, 4, &AgentResult{
		Summary: "Final answer",
	})
	require.NoError(t, err, "createAssistantMessage should persist the assistant transcript")
	require.Len(t, messages.messages, 1, "assistant message should be created")
	require.NotNil(t, messages.messages[0].ThreadID, "assistant message should preserve the thread id")
	require.Equal(t, threadID, *messages.messages[0].ThreadID, "assistant message should use the provided thread id")
	require.NotNil(t, logs.markedThreadID, "duplicate marker should preserve the thread id")
	require.Equal(t, threadID, *logs.markedThreadID, "duplicate marker should use the provided thread id")
	require.Equal(t, 4, logs.markedTurnNumber, "duplicate marker should use the provided turn number")
	require.Equal(t, "Final answer", logs.markedMessage, "duplicate marker should target the assistant summary")
}

func TestCreateAssistantMessage_PersistsCacheOnlyAndNativeCostUsage(t *testing.T) {
	t.Parallel()

	orgID := uuid.MustParse("eeeeeeee-eeee-eeee-eeee-eeeeeeeeeeee")
	sessionID := uuid.MustParse("ffffffff-ffff-ffff-ffff-ffffffffffff")
	messages := &testInternalSessionMessageStore{}
	orch := &Orchestrator{
		sessionMessages: messages,
		logger:          zerolog.Nop(),
	}

	err := orch.createAssistantMessage(context.Background(), sessionID, orgID, nil, 2, &AgentResult{
		Summary: "Cached reply",
		TokenUsage: TokenUsage{
			CachedInputTokens:   123,
			CacheCreationTokens: 45,
			NativeCost: &TokenCost{
				Amount: 12.5,
				Unit:   TokenCostUnitCredits,
				Source: TokenCostSourceDerived,
			},
		},
	})

	require.NoError(t, err, "createAssistantMessage should persist assistant messages with cache-only/native-cost token usage")
	require.Len(t, messages.messages, 1, "assistant message should be created")
	require.NotNil(t, messages.messages[0].TokenUsage, "cache-only/native-cost usage should still be persisted on the assistant message")
}

func TestCreateAssistantMessage_DoesNotPersistUnavailableTokenUsage(t *testing.T) {
	t.Parallel()

	orgID := uuid.MustParse("abababab-abab-abab-abab-abababababab")
	sessionID := uuid.MustParse("cdcdcdcd-cdcd-cdcd-cdcd-cdcdcdcdcdcd")
	messages := &testInternalSessionMessageStore{}
	orch := &Orchestrator{
		sessionMessages: messages,
		logger:          zerolog.Nop(),
	}

	err := orch.createAssistantMessage(context.Background(), sessionID, orgID, nil, 3, &AgentResult{
		Summary: "No usage reported",
		TokenUsage: FinalizeTokenUsage(TokenUsage{}, TokenUsageHint{
			AgentType:      models.AgentTypeCodex,
			EffectiveModel: models.CodexModelGPT54,
			BillingMode:    TokenBillingModeSubscription,
		}),
	})

	require.NoError(t, err, "createAssistantMessage should not fail when token usage is unavailable")
	require.Len(t, messages.messages, 1, "assistant message should be created")
	require.Nil(t, messages.messages[0].TokenUsage, "assistant message should leave token usage nil when the provider reported no token payload")
}

func TestBuildRunResult_DoesNotPersistUnavailableTokenUsage(t *testing.T) {
	t.Parallel()

	orgID := uuid.MustParse("12121212-3434-5656-7878-909090909090")
	runID := uuid.MustParse("abababab-cdcd-efef-0101-121212121212")
	orch := &Orchestrator{
		logger: zerolog.Nop(),
	}
	run := &models.Session{
		ID:    runID,
		OrgID: orgID,
	}

	result := orch.buildRunResult(context.Background(), run, nil, &AgentResult{
		Summary: "No usage reported",
		TokenUsage: FinalizeTokenUsage(TokenUsage{}, TokenUsageHint{
			AgentType:      models.AgentTypeCodex,
			EffectiveModel: models.CodexModelGPT54,
			BillingMode:    TokenBillingModeSubscription,
		}),
	})

	require.Nil(t, result.TokenUsage, "buildRunResult should leave token usage nil when the provider reported no token payload")
}

func TestStreamLogs_CarriesThreadID(t *testing.T) {
	t.Parallel()

	orgID := uuid.MustParse("22222222-2222-2222-2222-222222222222")
	sessionID := uuid.MustParse("11111111-1111-1111-1111-111111111111")
	threadID := uuid.MustParse("33333333-3333-3333-3333-333333333333")

	logs := &testInternalSessionLogStore{}
	orch := &Orchestrator{
		agentRunLogs: logs,
		logger:       zerolog.Nop(),
	}

	logCh := make(chan LogEntry, 1)
	logCh <- LogEntry{
		Timestamp: time.Now(),
		Level:     "output",
		Message:   "streamed message",
	}
	close(logCh)

	orch.streamLogs(context.Background(), sessionID, orgID, models.AgentTypeClaudeCode, &threadID, 2, logCh, nil)

	require.Len(t, logs.logs, 1, "streamLogs should persist the log entry")
	require.NotNil(t, logs.logs[0].ThreadID, "persisted log should preserve the thread id")
	require.Equal(t, threadID, *logs.logs[0].ThreadID, "persisted log should use the provided thread id")
	require.Equal(t, 2, logs.logs[0].TurnNumber, "persisted log should keep the turn number")
	require.Equal(t, "streamed message", logs.logs[0].Message, "persisted log should keep the message content")
	require.Nil(t, logs.logs[0].Metadata, "persisted log should leave absent metadata as SQL null")
}

func TestStreamLogs_PersistsMetadataAsJSON(t *testing.T) {
	t.Parallel()

	orgID := uuid.MustParse("22222222-2222-2222-2222-222222222222")
	sessionID := uuid.MustParse("11111111-1111-1111-1111-111111111111")

	logs := &testInternalSessionLogStore{}
	orch := &Orchestrator{
		agentRunLogs: logs,
		logger:       zerolog.Nop(),
	}

	logCh := make(chan LogEntry, 1)
	logCh <- LogEntry{
		Timestamp: time.Now(),
		Level:     "output",
		Message:   "with metadata",
		Metadata:  map[string]interface{}{"step": "two"},
	}
	close(logCh)

	orch.streamLogs(context.Background(), sessionID, orgID, models.AgentTypeClaudeCode, nil, 1, logCh, nil)

	require.Len(t, logs.logs, 1, "streamLogs should persist the log entry")
	require.NotNil(t, logs.logs[0].Metadata, "non-nil metadata should be marshaled and persisted")
	var decoded map[string]interface{}
	require.NoError(t, json.Unmarshal(logs.logs[0].Metadata, &decoded), "persisted metadata should be valid JSON")
	require.Equal(t, "two", decoded["step"], "persisted metadata should round-trip the entry payload")
}

func TestStreamLogs_DropsUnmarshalableMetadata(t *testing.T) {
	t.Parallel()

	orgID := uuid.MustParse("22222222-2222-2222-2222-222222222222")
	sessionID := uuid.MustParse("11111111-1111-1111-1111-111111111111")

	logs := &testInternalSessionLogStore{}
	orch := &Orchestrator{
		agentRunLogs: logs,
		logger:       zerolog.Nop(),
	}

	logCh := make(chan LogEntry, 1)
	logCh <- LogEntry{
		Timestamp: time.Now(),
		Level:     "output",
		Message:   "bad metadata",
		Metadata:  map[string]interface{}{"fn": func() {}},
	}
	close(logCh)

	orch.streamLogs(context.Background(), sessionID, orgID, models.AgentTypeClaudeCode, nil, 1, logCh, nil)

	require.Len(t, logs.logs, 1, "streamLogs should still persist the log entry when metadata fails to marshal")
	require.Nil(t, logs.logs[0].Metadata, "unmarshalable metadata should be dropped to nil rather than blocking the log")
}

func TestHumanInputRequestFromQuestionLog(t *testing.T) {
	t.Parallel()

	req := humanInputRequestFromQuestionLog(LogEntry{
		Level:   "question",
		Message: "Which approach should Claude use?",
		Metadata: map[string]interface{}{
			"title":        "Choose approach",
			"context":      "Migration touches settings.",
			"blocks_phase": "implementation",
			"options": []interface{}{
				map[string]interface{}{"label": "Reuse table", "description": "Keep the current schema."},
				"Create table",
			},
		},
	})

	require.Equal(t, models.HumanInputRequestKindFreeText, req.Kind, "legacy question logs should remain free-text compatible")
	require.Equal(t, "Choose approach", req.Title, "metadata title should become request title")
	require.Equal(t, "Which approach should Claude use?", req.Body, "log message should become request body")
	require.NotNil(t, req.Context, "metadata context should be preserved")
	require.Equal(t, "Migration touches settings.", *req.Context, "metadata context should round-trip")
	require.NotNil(t, req.BlocksPhase, "metadata phase should be preserved")
	require.Equal(t, "implementation", *req.BlocksPhase, "metadata phase should round-trip")
	require.Equal(t, []models.HumanInputChoice{
		{ID: "reuse-table", Label: "Reuse table", Description: "Keep the current schema."},
		{ID: "create-table", Label: "Create table"},
	}, req.Choices, "metadata options should become normalized choice rows")
}

func TestPrepareSandboxGitHubAuth_LegacyAddsCoAuthorTrailer(t *testing.T) {
	t.Parallel()

	orgID := uuid.MustParse("22222222-2222-2222-2222-222222222222")
	userID := uuid.MustParse("44444444-4444-4444-4444-444444444444")
	noreply := "4+alice@users.noreply.github.com"
	orch := &Orchestrator{
		users: testInternalUserLookup{
			user: models.User{
				ID:                 userID,
				OrgID:              orgID,
				Name:               "Alice Example",
				Email:              "alice@example.com",
				GitHubNoreplyEmail: &noreply,
			},
		},
		logger: zerolog.Nop(),
	}
	run := &models.Session{ID: uuid.New(), OrgID: orgID, TriggeredByUserID: &userID}
	repo := &models.Repository{FullName: "assembledhq/143"}
	cfg := &SandboxConfig{HomeDir: "/home/sandbox", Env: map[string]string{}}

	authState, err := orch.prepareSandboxGitHubAuth(context.Background(), run, repo, "ghp_test123", cfg, zerolog.Nop())
	require.NoError(t, err, "prepareSandboxGitHubAuth should not fail on the legacy fallback path")
	require.Nil(t, authState, "prepareSandboxGitHubAuth should not create auth state on the legacy fallback path")
	require.Equal(t, "ghp_test123", cfg.Env["GITHUB_TOKEN"], "prepareSandboxGitHubAuth should expose the fallback token on the legacy path")
	require.Equal(t, "143 Agent", cfg.Env[sandboxauth.GitNameEnvVar], "prepareSandboxGitHubAuth should seed the default git author name on the legacy path")
	require.Equal(t, "noreply@143.dev", cfg.Env[sandboxauth.GitEmailEnvVar], "prepareSandboxGitHubAuth should seed the default git author email on the legacy path")
	require.Equal(t, "Co-authored-by: Alice Example <4+alice@users.noreply.github.com>", cfg.Env[sandboxauth.CoAuthorEnvVar], "prepareSandboxGitHubAuth should attach a co-author trailer when the triggering user can be loaded")
}

func TestPrepareSandboxGitHubAuth_LegacyIgnoresUserLookupFailure(t *testing.T) {
	t.Parallel()

	orgID := uuid.MustParse("22222222-2222-2222-2222-222222222222")
	userID := uuid.MustParse("55555555-5555-5555-5555-555555555555")
	orch := &Orchestrator{
		users:  testInternalUserLookup{err: errors.New("user lookup failed")},
		logger: zerolog.Nop(),
	}
	run := &models.Session{ID: uuid.New(), OrgID: orgID, TriggeredByUserID: &userID}
	cfg := &SandboxConfig{HomeDir: "/home/sandbox", Env: map[string]string{}}

	authState, err := orch.prepareSandboxGitHubAuth(context.Background(), run, &models.Repository{FullName: "assembledhq/143"}, "ghp_test123", cfg, zerolog.Nop())
	require.NoError(t, err, "prepareSandboxGitHubAuth should not fail when the legacy co-author lookup is best-effort")
	require.Nil(t, authState, "prepareSandboxGitHubAuth should not create auth state on the legacy fallback path")
	require.Equal(t, "ghp_test123", cfg.Env["GITHUB_TOKEN"], "prepareSandboxGitHubAuth should still expose the fallback token when user lookup fails")
	require.Empty(t, cfg.Env[sandboxauth.CoAuthorEnvVar], "prepareSandboxGitHubAuth should skip the co-author trailer when the triggering user cannot be loaded")
}

func TestSessionWorkingBranch_PrefersPersistedBranch(t *testing.T) {
	t.Parallel()

	workingBranch := "143/persisted/fix-auth"
	run := &models.Session{ID: uuid.New(), WorkingBranch: &workingBranch}

	require.Equal(t, workingBranch, sessionWorkingBranch(run, &models.Issue{Title: "Ignored"}), "sessionWorkingBranch should reuse the persisted working branch when present")
}

func TestSetupFreshSandbox_WorkingBranchCheckoutFailures(t *testing.T) {
	t.Parallel()

	orgID := uuid.MustParse("22222222-2222-2222-2222-222222222222")
	repoID := uuid.MustParse("66666666-6666-6666-6666-666666666666")
	issueID := uuid.MustParse("77777777-7777-7777-7777-777777777777")
	repo := models.Repository{
		ID:             repoID,
		OrgID:          orgID,
		FullName:       "assembledhq/143",
		DefaultBranch:  "main",
		CloneURL:       "https://github.com/assembledhq/143.git",
		InstallationID: 42,
	}
	issue := models.Issue{ID: issueID, OrgID: orgID, RepositoryID: &repoID, Title: "Fix checkout failure"}

	tests := []struct {
		name       string
		execExit   int
		execErr    error
		execStderr string
		wantErr    string
	}{
		{
			name:     "exec error",
			execExit: 1,
			execErr:  errors.New("exec failed"),
			wantErr:  "create working branch 143/",
		},
		{
			name:       "non-zero exit",
			execExit:   17,
			execStderr: "branch already exists",
			wantErr:    "exit=17 stderr=branch already exists",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			session := &models.Session{
				ID:             uuid.MustParse("88888888-8888-8888-8888-888888888888"),
				OrgID:          orgID,
				PrimaryIssueID: &issueID,
				RepositoryID:   &repoID,
				AgentType:      models.AgentType("test"),
			}
			provider := &testInternalSandboxProvider{
				execExit:   tt.execExit,
				execErr:    tt.execErr,
				execStderr: tt.execStderr,
			}
			orch := &Orchestrator{
				issues:       testInternalIssueStore{issue: issue},
				repositories: testInternalRepoStore{repo: repo},
				github:       testInternalGitHubTokens{token: "ghp_test123"},
				provider:     provider,
				logger:       zerolog.Nop(),
			}

			_, _, _, err := orch.setupFreshSandbox(context.Background(), session, &Sandbox{ID: "sandbox-1", WorkDir: "/home/sandbox/backend", HomeDir: "/home/sandbox"}, nil, nil)
			require.Error(t, err, "setupFreshSandbox should fail when the working branch cannot be created")
			require.Contains(t, err.Error(), tt.wantErr, "setupFreshSandbox should surface the working-branch checkout failure")
		})
	}
}
