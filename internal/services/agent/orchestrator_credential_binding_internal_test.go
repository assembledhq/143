package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/models"
)

type executionCredentialProvider struct {
	mu           sync.Mutex
	rows         []models.DecryptedCodingCredential
	picks        []models.DecryptedCodingCredential
	rateLimited  []uuid.UUID
	authRejected []uuid.UUID
}

func (p *executionCredentialProvider) ListResolvable(_ context.Context, _ uuid.UUID, _ *uuid.UUID, provider models.ProviderName) ([]models.DecryptedCodingCredential, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	rows := make([]models.DecryptedCodingCredential, 0)
	for _, row := range p.rows {
		if row.Provider == provider {
			rows = append(rows, row)
		}
	}
	return rows, nil
}

func (p *executionCredentialProvider) PickRunnableMulti(_ context.Context, _ models.Scope, _ []models.ProviderName) (*models.DecryptedCodingCredential, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.picks) == 0 {
		return nil, errEnvCodingCredentialNotFound
	}
	picked := p.picks[0]
	p.picks = p.picks[1:]
	return &picked, nil
}

func (p *executionCredentialProvider) MarkRateLimited(credentialID uuid.UUID) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.rateLimited = append(p.rateLimited, credentialID)
}

func (p *executionCredentialProvider) MarkAuthRejected(credentialID uuid.UUID) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.authRejected = append(p.authRejected, credentialID)
}

func (p *executionCredentialProvider) MarkRateLimitedForScope(_ context.Context, _ models.Scope, credentialID uuid.UUID, _ models.CodingCredentialRateLimit) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.rateLimited = append(p.rateLimited, credentialID)
	return nil
}

func (p *executionCredentialProvider) MarkAuthRejectedForScope(_ context.Context, _ models.Scope, credentialID uuid.UUID) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.authRejected = append(p.authRejected, credentialID)
	return nil
}

type executionCredentialAdapter struct {
	AgentAdapter
	execute func(*Sandbox, *AgentPrompt, chan<- LogEntry) (*AgentResult, error)
}

func (a executionCredentialAdapter) Execute(_ context.Context, sandbox *Sandbox, prompt *AgentPrompt, logs chan<- LogEntry) (*AgentResult, error) {
	return a.execute(sandbox, prompt, logs)
}

func TestExecutedCredentialSurvivesRecentPickReplacement(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		agentType   models.AgentType
		provider    models.ProviderName
		config      models.ProviderConfig
		other       models.ProviderConfig
		rateLimit   bool
		replacePick bool
	}{
		{
			name:      "codex subscription with expired recent pick",
			agentType: models.AgentTypeCodex, provider: models.ProviderOpenAISubscription,
			config: models.OpenAISubscriptionConfig{AccessToken: "synthetic-access-a", RefreshToken: "synthetic-refresh-a", ExpiresAt: time.Now().Add(time.Hour)},
			other:  models.OpenAISubscriptionConfig{AccessToken: "synthetic-access-b", RefreshToken: "synthetic-refresh-b", ExpiresAt: time.Now().Add(time.Hour)}, rateLimit: true,
		},
		{
			name:        "claude subscription with concurrent recent pick",
			replacePick: true,
			agentType:   models.AgentTypeClaudeCode, provider: models.ProviderAnthropicSubscription,
			config: models.AnthropicSubscriptionConfig{AccessToken: "synthetic-access-a", RefreshToken: "synthetic-refresh-a", ExpiresAt: time.Now().Add(time.Hour)},
			other:  models.AnthropicSubscriptionConfig{AccessToken: "synthetic-access-b", RefreshToken: "synthetic-refresh-b", ExpiresAt: time.Now().Add(time.Hour)},
		},
		{
			name:        "claude API key with concurrent recent subscription",
			replacePick: true,
			agentType:   models.AgentTypeClaudeCode, provider: models.ProviderAnthropic,
			config: models.AnthropicConfig{APIKey: "synthetic-key-a"},
			other:  models.AnthropicSubscriptionConfig{AccessToken: "synthetic-access-b", RefreshToken: "synthetic-refresh-b", ExpiresAt: time.Now().Add(time.Hour)}, rateLimit: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			orgID, userID := uuid.New(), uuid.New()
			first := models.DecryptedCodingCredential{ID: uuid.New(), OrgID: orgID, UserID: &userID, Provider: tt.provider, Status: models.CodingCredentialStatusActive, Config: tt.config}
			other := first
			other.ID, other.Config = uuid.New(), tt.other
			if _, subscription := tt.other.(models.AnthropicSubscriptionConfig); subscription {
				other.Provider = models.ProviderAnthropicSubscription
			}
			coding := &executionCredentialProvider{rows: []models.DecryptedCodingCredential{first, other}, picks: []models.DecryptedCodingCredential{first, other}}
			provider := &envSandboxProvider{}
			env := NewAgentEnv(AgentEnvDeps{CodingCredentials: coding, Provider: provider, Logger: zerolog.Nop()})
			o := &Orchestrator{env: env, provider: provider, logger: zerolog.Nop()}
			resolution := env.ResolveForModelWithCredential(context.Background(), orgID, tt.agentType, &userID, "")
			require.NotNil(t, resolution.Credential, "the first execution should resolve its exact credential")
			shared := &Sandbox{ID: "shared-handle", HomeDir: "/home/test", WorkDir: "/workspace", Env: map[string]string{"SHARED": "unchanged"}, Metadata: map[string]string{"provider": "unchanged"}}
			sandbox := sandboxWithCredentialResolution(shared, resolution, resolution.Env)

			// Simulate a long CLI run without waiting for the five-minute pick
			// cache TTL. A second execution then replaces the shared tuple slot.
			env.recentPicksMu.Lock()
			for key, record := range env.recentPicks {
				record.at = time.Now().Add(-2 * pickTrackerTTL)
				env.recentPicks[key] = record
			}
			env.recentPicksMu.Unlock()
			if tt.replacePick {
				replaced := make(chan *resolvedAgentEnv, 1)
				go func() {
					replaced <- env.ResolveForModelWithCredential(context.Background(), orgID, tt.agentType, &userID, "")
				}()
				otherResolution := <-replaced
				require.Equal(t, other.ID, otherResolution.Credential.Credential.ID, "a concurrent execution should have a different pinned selection")
			} else {
				_, found := env.lookupRecentPickRecord(orgID, &userID, codingProviderForAgent(tt.agentType))
				require.False(t, found, "a long run's tuple-cache entry should have expired before its result is handled")
			}

			run := &models.Session{OrgID: orgID, AgentType: tt.agentType, TriggeredByUserID: &userID}
			var mode TokenBillingMode
			var err error
			if tt.agentType == models.AgentTypeCodex {
				mode, err = o.ensureCodexAuth(context.Background(), run, nil, sandbox, sandbox.Env)
			} else {
				mode, err = o.ensureClaudeCodeAuth(context.Background(), run, nil, sandbox, sandbox.Env)
			}
			require.NoError(t, err, "auth should use the execution's captured credential after the shared pick changes")
			if _, apiKey := tt.config.(models.AnthropicConfig); apiKey {
				require.Equal(t, TokenBillingModeAPIKey, mode, "the executed API key should retain its billing mode")
				require.Equal(t, "synthetic-key-a", sandbox.Env["ANTHROPIC_API_KEY"], "the executed API key should not be replaced by the concurrent subscription")
				require.Empty(t, provider.writes, "API-key preparation should not inject another execution's subscription")
			} else {
				require.Equal(t, TokenBillingModeSubscription, mode, "the captured subscription should retain its billing mode")
				authPath := "/home/test/.claude/.credentials.json"
				if tt.agentType == models.AgentTypeCodex {
					authPath = "/home/test/.codex/auth.json"
				}
				var payload map[string]any
				require.NoError(t, json.Unmarshal(provider.writes[authPath], &payload), "injected auth should contain valid synthetic credential JSON")
				if tt.agentType == models.AgentTypeCodex {
					require.Equal(t, "synthetic-access-a", payload["tokens"].(map[string]any)["access_token"], "Codex must receive the first execution's access token")
				} else {
					require.Equal(t, "synthetic-access-a", payload["claudeAiOauth"].(map[string]any)["accessToken"], "Claude must receive the first execution's access token")
				}
			}
			result := &AgentResult{Error: "invalid API key"}
			if tt.rateLimit {
				result.Error = "429 too many requests"
			}
			o.shedOnRunResult(context.Background(), tt.agentType, orgID, &userID, result, nil, zerolog.Nop(), sandbox)
			expectedRateLimits, expectedAuthRejections := []uuid.UUID(nil), []uuid.UUID(nil)
			if tt.rateLimit {
				expectedRateLimits = []uuid.UUID{first.ID}
			} else {
				expectedAuthRejections = []uuid.UUID{first.ID}
			}
			require.Equal(t, expectedRateLimits, coding.rateLimited, "rate-limit health should target the credential that actually executed")
			require.Equal(t, expectedAuthRejections, coding.authRejected, "auth health should target the credential that actually executed")
			require.Nil(t, shared.credentialResolution, "private execution selection must not be attached to the provider's shared handle")
			require.Equal(t, map[string]string{"SHARED": "unchanged"}, shared.Env, "execution env must not mutate the provider's shared handle")
			require.Equal(t, map[string]string{"provider": "unchanged"}, shared.Metadata, "auth metadata must not mutate the provider's shared handle")
		})
	}
}

func TestCredentialFallbackKeepsExecutedBindingAfterAvailabilityProbe(t *testing.T) {
	t.Parallel()

	orgID, userID := uuid.New(), uuid.New()
	credentials := make([]models.DecryptedCodingCredential, 0, 3)
	for _, key := range []string{"synthetic-key-a", "synthetic-key-b", "synthetic-key-c"} {
		credentials = append(credentials, models.DecryptedCodingCredential{ID: uuid.New(), OrgID: orgID, UserID: &userID, Provider: models.ProviderOpenAI, Status: models.CodingCredentialStatusActive, Config: models.OpenAIConfig{APIKey: key}})
	}
	coding := &executionCredentialProvider{rows: credentials, picks: append([]models.DecryptedCodingCredential(nil), credentials...)}
	provider := &envSandboxProvider{}
	env := NewAgentEnv(AgentEnvDeps{CodingCredentials: coding, Provider: provider, Logger: zerolog.Nop()})
	o := &Orchestrator{env: env, provider: provider, logger: zerolog.Nop()}
	session := &models.Session{ID: uuid.New(), OrgID: orgID, AgentType: models.AgentTypeCodex, TriggeredByUserID: &userID}
	resolution := env.ResolveForModelWithCredential(context.Background(), orgID, session.AgentType, &userID, "")
	resolution.Env["SESSION_VALUE"] = "preserved"
	shared := &Sandbox{ID: "fallback-handle", HomeDir: "/home/test", WorkDir: "/workspace"}
	sandbox := sandboxWithCredentialResolution(shared, resolution, resolution.Env)
	prompt := &AgentPrompt{UsageHint: TokenUsageHint{BillingMode: TokenBillingModeSubscription}}
	result := &AgentResult{Error: "429 too many requests", ExitCode: 1}
	calls := 0
	adapter := executionCredentialAdapter{execute: func(executing *Sandbox, retryPrompt *AgentPrompt, _ chan<- LogEntry) (*AgentResult, error) {
		calls++
		require.Equal(t, credentials[1].ID, executing.credentialResolution.Credential.Credential.ID, "the fallback execution must bind credential B before auth and execution")
		require.Equal(t, "synthetic-key-b", executing.Env["OPENAI_API_KEY"], "the fallback execution must carry credential B's env")
		require.Equal(t, "preserved", executing.Env["SESSION_VALUE"], "credential fallback should preserve execution-scoped env")
		require.Equal(t, TokenBillingModeAPIKey, retryPrompt.UsageHint.BillingMode, "fallback usage should describe the credential that actually executes")
		return result, nil
	}}

	actual, err, retried := o.retrySessionOnCredentialRateLimit(context.Background(), session, TranscriptWriteContext{}, SandboxConfig{HomeDir: sandbox.HomeDir, Env: sandbox.Env}, sandbox, adapter, context.Background(), prompt, result, nil, false, nil, zerolog.Nop())

	require.NoError(t, err, "a still-available third credential should pass the availability probe")
	require.True(t, retried, "credential B should execute the bounded fallback attempt")
	require.Equal(t, 1, calls, "the third credential is only an availability probe and must never execute")
	require.Equal(t, result, actual, "the result should remain credential B's failed execution")
	require.Equal(t, []uuid.UUID{credentials[0].ID, credentials[1].ID}, coding.rateLimited, "only executed credentials A and B should be marked rate-limited")
	require.Equal(t, credentials[1].ID, sandbox.credentialResolution.Credential.Credential.ID, "an unused candidate C must not replace the returned result's executed binding")
	require.Empty(t, coding.picks, "the availability check should resolve candidate C without executing it")
	require.Nil(t, shared.credentialResolution, "fallback execution must not mutate the provider's cached sandbox handle")
}

func TestTokenExpiredRetryReloadsExecutedCredential(t *testing.T) {
	t.Parallel()

	orgID, userID := uuid.New(), uuid.New()
	selected := models.DecryptedCodingCredential{ID: uuid.New(), OrgID: orgID, UserID: &userID, Provider: models.ProviderOpenAISubscription, Status: models.CodingCredentialStatusActive, Config: models.OpenAISubscriptionConfig{AccessToken: "synthetic-old-access", RefreshToken: "synthetic-old-refresh", ExpiresAt: time.Now().Add(time.Hour)}}
	other := selected
	other.ID = uuid.New()
	coding := &executionCredentialProvider{rows: []models.DecryptedCodingCredential{selected, other}, picks: []models.DecryptedCodingCredential{selected, other}}
	provider := &envSandboxProvider{}
	refreshed := models.OpenAISubscriptionConfig{AccessToken: "synthetic-refreshed-access", RefreshToken: "synthetic-refreshed-refresh", ExpiresAt: time.Now().Add(2 * time.Hour)}
	auth := &envCodexAuthProvider{refreshToken: &refreshed}
	env := NewAgentEnv(AgentEnvDeps{CodingCredentials: coding, CodexAuth: auth, Provider: provider, Logger: zerolog.Nop()})
	o := &Orchestrator{env: env, provider: provider, logger: zerolog.Nop()}
	resolution := env.ResolveForModelWithCredential(context.Background(), orgID, models.AgentTypeCodex, &userID, "")
	sandbox := sandboxWithCredentialResolution(&Sandbox{ID: "refresh-handle", HomeDir: "/home/test"}, resolution, resolution.Env)
	otherResolution := env.ResolveForModelWithCredential(context.Background(), orgID, models.AgentTypeCodex, &userID, "")
	require.Equal(t, other.ID, otherResolution.Credential.Credential.ID, "another execution should replace the shared recent pick before refresh")
	expected := &AgentResult{Summary: "synthetic retry completed"}
	calls := 0
	adapter := executionCredentialAdapter{execute: func(executing *Sandbox, _ *AgentPrompt, _ chan<- LogEntry) (*AgentResult, error) {
		calls++
		require.Equal(t, selected.ID, executing.credentialResolution.Credential.Credential.ID, "token refresh must preserve the exact executed credential identity")
		return expected, nil
	}}

	actual, err := o.retryOnTokenExpired(context.Background(), models.AgentTypeCodex, orgID, &userID, uuid.New(), TranscriptWriteContext{}, sandbox, adapter, context.Background(), &AgentPrompt{}, &AgentResult{Error: "token_expired"}, errors.New("synthetic token rejection"), nil, zerolog.Nop())

	require.NoError(t, err, "a successful same-credential refresh should retry normally")
	require.Equal(t, expected, actual, "the token refresh should return its successful retry result")
	require.Equal(t, 1, calls, "token expiration should execute one bounded retry")
	require.Equal(t, []uuid.UUID{selected.ID}, auth.refreshIDs, "token expiration must reload the executed credential through the auth service even when its captured expiry is still in the future")
	require.Equal(t, []models.Scope{{OrgID: orgID, UserID: &userID}}, auth.refreshScopes, "token expiration must preserve the selected credential's personal scope")
	var payload map[string]any
	require.NoError(t, json.Unmarshal(provider.writes["/home/test/.codex/auth.json"], &payload), "refreshed Codex auth should contain valid JSON")
	require.Equal(t, "synthetic-refreshed-access", payload["tokens"].(map[string]any)["access_token"], "the retry must receive the refreshed token from the selected row")
}

func TestResolvedNoCredentialDoesNotRepickDuringExecution(t *testing.T) {
	t.Parallel()

	orgID, userID := uuid.New(), uuid.New()
	credential := models.DecryptedCodingCredential{ID: uuid.New(), OrgID: orgID, Provider: models.ProviderAnthropicSubscription, Status: models.CodingCredentialStatusActive, Config: models.AnthropicSubscriptionConfig{AccessToken: "synthetic-other-access", RefreshToken: "synthetic-other-refresh", ExpiresAt: time.Now().Add(time.Hour)}}
	coding := &executionCredentialProvider{rows: []models.DecryptedCodingCredential{credential}, picks: []models.DecryptedCodingCredential{credential}}
	provider := &envSandboxProvider{}
	legacy := &testInternalClaudeCodeAuthProvider{sub: &models.AnthropicSubscription{AccessToken: "synthetic-legacy", RefreshToken: "synthetic-legacy-refresh", ExpiresAt: time.Now().Add(time.Hour)}, id: uuid.New()}
	env := NewAgentEnv(AgentEnvDeps{CodingCredentials: coding, Provider: provider, Logger: zerolog.Nop()})
	o := &Orchestrator{env: env, provider: provider, claudeCodeAuth: legacy, logger: zerolog.Nop()}
	sandbox := sandboxWithCredentialResolution(&Sandbox{ID: "no-credential", HomeDir: "/home/test"}, &resolvedAgentEnv{Env: map[string]string{}}, nil)
	env.recordCredentialPick(orgID, &userID, models.ProviderAnthropic, credential)

	injected, account, err := o.injectUnifiedClaudeCodeAuth(context.Background(), orgID, &userID, sandbox, sandbox.Env)
	require.NoError(t, err, "a resolved-none execution should report no unified auth without selecting again")
	require.False(t, injected, "resolved-none must not inject a later execution's unified credential")
	require.Equal(t, "", account, "resolved-none should not acquire a subscription account type")
	injected, account, err = o.injectClaudeCodeAuth(context.Background(), orgID, &userID, sandbox)
	require.NoError(t, err, "a resolved-none unified execution should not re-enter legacy auth")
	require.False(t, injected, "resolved-none must not inject an unrelated legacy credential")
	require.Equal(t, "", account, "legacy auth must not change a resolved-none execution's account type")
	require.False(t, o.unifiedCodingCredentialIsAPIKeyForSandbox(context.Background(), orgID, &userID, models.ProviderAnthropic, sandbox), "resolved-none must not repick merely to classify auth")
	o.shedOnRunResult(context.Background(), models.AgentTypeClaudeCode, orgID, &userID, &AgentResult{Error: "429 too many requests"}, nil, zerolog.Nop(), sandbox)
	require.Empty(t, coding.rateLimited, "resolved-none execution must not shed another execution's selected credential")
	require.Equal(t, []models.DecryptedCodingCredential{credential}, coding.picks, "auth consumers must leave the available candidate untouched")
	require.Empty(t, provider.writes, "resolved-none auth must not write any credential files")
}

func TestHarvestClaudeCredentialsUsesExecutedBinding(t *testing.T) {
	t.Parallel()

	orgID, userID := uuid.New(), uuid.New()
	selected := models.DecryptedCodingCredential{ID: uuid.New(), OrgID: orgID, UserID: &userID, Provider: models.ProviderAnthropicSubscription, Status: models.CodingCredentialStatusActive, Config: models.AnthropicSubscriptionConfig{AccessToken: "synthetic-old-access", RefreshToken: "synthetic-old-refresh", ExpiresAt: time.Now().Add(time.Hour)}}
	other := selected
	other.ID = uuid.New()
	coding := &executionCredentialProvider{rows: []models.DecryptedCodingCredential{selected, other}, picks: []models.DecryptedCodingCredential{selected, other}}
	expiry := time.Now().Add(2 * time.Hour).Truncate(time.Millisecond)
	refreshed := models.AnthropicSubscription{AccessToken: "synthetic-refreshed-access", RefreshToken: "synthetic-refreshed-refresh", ExpiresAt: expiry}
	raw, err := json.Marshal(map[string]any{"claudeAiOauth": map[string]any{"accessToken": refreshed.AccessToken, "refreshToken": refreshed.RefreshToken, "expiresAt": expiry.UnixMilli()}})
	require.NoError(t, err, "synthetic harvested credentials should marshal")
	provider := &testInternalSandboxProvider{readFiles: map[string][]byte{"/home/test/.claude/.credentials.json": raw}}
	auth := &testInternalClaudeCodeAuthProvider{}
	env := NewAgentEnv(AgentEnvDeps{CodingCredentials: coding, Provider: provider, Logger: zerolog.Nop()})
	o := &Orchestrator{env: env, provider: provider, claudeCodeAuth: auth, logger: zerolog.Nop()}
	resolution := env.ResolveForModelWithCredential(context.Background(), orgID, models.AgentTypeClaudeCode, &userID, "")
	sandbox := sandboxWithCredentialResolution(&Sandbox{ID: "harvest-handle", HomeDir: "/home/test"}, resolution, resolution.Env)
	otherResolution := env.ResolveForModelWithCredential(context.Background(), orgID, models.AgentTypeClaudeCode, &userID, "")
	require.Equal(t, other.ID, otherResolution.Credential.Credential.ID, "another execution should replace the shared recent pick before harvesting")

	stored, err := o.harvestClaudeCodeCredentials(context.Background(), &models.Session{OrgID: orgID, AgentType: models.AgentTypeClaudeCode, TriggeredByUserID: &userID}, sandbox, TokenBillingModeSubscription, zerolog.Nop())

	require.NoError(t, err, "harvest should save the running credential's refreshed tokens")
	require.True(t, stored, "changed sandbox credentials should be persisted")
	require.Equal(t, selected.ID, auth.storedID, "harvest must never overwrite another execution's credential row")
	require.Equal(t, selected.Scope(), auth.storedScope, "harvest must preserve the executed credential's scope")
	require.Equal(t, &refreshed, auth.storedSub, "harvest must save the exact refreshed synthetic subscription")
}

func TestExecutionCredentialLogContainsOnlySafeAttribution(t *testing.T) {
	t.Parallel()

	orgID, userID, sessionID := uuid.New(), uuid.New(), uuid.New()
	credential := models.DecryptedCodingCredential{ID: uuid.New(), OrgID: orgID, UserID: &userID, Provider: models.ProviderOpenAISubscription, Status: models.CodingCredentialStatusActive, Config: models.OpenAISubscriptionConfig{AccessToken: "synthetic-secret-access", RefreshToken: "synthetic-secret-refresh", IDToken: "synthetic-secret-id-token", ExpiresAt: time.Now().Add(time.Hour)}}
	selection := captureRuntimeCredential(credential, models.AgentTypeCodex, nil)
	var output bytes.Buffer
	logger := zerolog.New(&output).With().Str("session_id", sessionID.String()).Logger()

	logAgentExecutionCredential(logger, &Sandbox{credentialResolution: &resolvedAgentEnv{Credential: selection}})

	var actual map[string]any
	require.NoError(t, json.Unmarshal(output.Bytes(), &actual), "credential attribution should be valid structured JSON")
	expected := map[string]any{
		"level":               "info",
		"session_id":          sessionID.String(),
		"credential_id":       credential.ID.String(),
		"credential_provider": string(models.ProviderOpenAISubscription),
		"credential_scope":    string(selection.Binding.CredentialScope),
		"message":             "agent execution credential selected",
	}
	require.Equal(t, expected, actual, "credential attribution should retain existing execution context and only safe credential identity fields")
}

func TestPreparedAuthModeOverridesCapturedCredentialBilling(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	credential := models.DecryptedCodingCredential{ID: uuid.New(), OrgID: orgID, Provider: models.ProviderAnthropicSubscription, Status: models.CodingCredentialStatusActive, Config: models.AnthropicSubscriptionConfig{AccessToken: "synthetic-expired-access", RefreshToken: "synthetic-refresh", ExpiresAt: time.Now().Add(-time.Hour)}}
	provider := &envSandboxProvider{}
	env := NewAgentEnv(AgentEnvDeps{CodingCredentials: &executionCredentialProvider{}, Provider: provider, Logger: zerolog.Nop()})
	o := &Orchestrator{env: env, provider: provider, logger: zerolog.Nop()}
	resolution := &resolvedAgentEnv{Credential: captureRuntimeCredential(credential, models.AgentTypeClaudeCode, nil), Env: map[string]string{"ANTHROPIC_API_KEY": "synthetic-api-fallback"}}
	sandbox := sandboxWithCredentialResolution(&Sandbox{ID: "prepared-auth-billing", HomeDir: "/home/test"}, resolution, resolution.Env)
	session := &models.Session{OrgID: orgID, AgentType: models.AgentTypeClaudeCode}

	mode, err := o.ensureClaudeCodeAuth(context.Background(), session, nil, sandbox, sandbox.Env)

	require.NoError(t, err, "an unusable captured subscription should allow its already-configured API-key auth fallback")
	require.Equal(t, TokenBillingModeAPIKey, mode, "prepared auth should report the actual API-key fallback")
	hint := o.buildTokenUsageHint(context.Background(), session.AgentType, orgID, nil, sandbox.Env, TokenUsageHint{BillingMode: mode}, sandbox)
	require.Equal(t, TokenBillingModeAPIKey, hint.BillingMode, "usage must preserve the explicit auth-reported billing mode even when the captured candidate was a subscription")
}
