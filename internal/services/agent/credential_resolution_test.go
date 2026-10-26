package agent

import (
	"context"
	"encoding/json"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/models"
)

type capturedCredentialFailure struct {
	Scope models.Scope
	ID    uuid.UUID
}

type capturedCredentialProvider struct {
	*envCodingCredentialProvider
	rateFailures []capturedCredentialFailure
	authFailures []capturedCredentialFailure
}

func (p *capturedCredentialProvider) MarkRateLimitedForScope(_ context.Context, scope models.Scope, id uuid.UUID, limit models.CodingCredentialRateLimit) error {
	p.rateFailures = append(p.rateFailures, capturedCredentialFailure{Scope: scope, ID: id})
	p.rateLimits = append(p.rateLimits, limit)
	return nil
}

func (p *capturedCredentialProvider) MarkAuthRejectedForScope(_ context.Context, scope models.Scope, id uuid.UUID) error {
	p.authFailures = append(p.authFailures, capturedCredentialFailure{Scope: scope, ID: id})
	return nil
}

func TestAgentEnvCapturedCredentialSurvivesTrackerChanges(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		tracker      string
		personal     bool
		subscription bool
	}{
		{name: "expired personal subscription", tracker: "expired", personal: true, subscription: true},
		{name: "evicted org subscription", tracker: "evicted", subscription: true},
		{name: "new pick replaces personal API key", tracker: "replaced", personal: true},
		{name: "new pick replaces org subscription", tracker: "replaced", subscription: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			orgID, requestUserID, ownerID, credentialID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
			var owner *uuid.UUID
			if tt.personal {
				owner = &ownerID
			}
			provider := models.ProviderOpenAI
			var cfg models.ProviderConfig = models.OpenAIConfig{APIKey: "captured-api-key"}
			if tt.subscription {
				provider = models.ProviderOpenAISubscription
				cfg = models.OpenAISubscriptionConfig{AccessToken: "captured-access", RefreshToken: "captured-refresh", IDToken: "captured-id", ExpiresAt: time.Now().Add(time.Hour)}
			}
			credential := models.DecryptedCodingCredential{ID: credentialID, OrgID: orgID, UserID: owner, Provider: provider, Status: models.CodingCredentialStatusActive, Config: cfg}
			store := &capturedCredentialProvider{envCodingCredentialProvider: &envCodingCredentialProvider{resolvable: map[models.ProviderName][]models.DecryptedCodingCredential{provider: {credential}}}}
			sandboxProvider := &envSandboxProvider{}
			env := NewAgentEnv(AgentEnvDeps{CodingCredentials: store, Provider: sandboxProvider, Logger: zerolog.Nop()})
			resolved := env.ResolveForModelWithCredential(ctx, orgID, models.AgentTypeCodex, &requestUserID, "captured-model")
			require.NotNil(t, resolved.Credential, "resolution should capture the row that supplied execution auth")
			expectedScope := models.Scope{OrgID: orgID, UserID: owner}
			if owner != nil {
				clonedOwner := *owner
				expectedScope.UserID = &clonedOwner
				ownerID = uuid.New()
			}
			switch tt.tracker {
			case "expired":
				env.recentPicksMu.Lock()
				for key, rec := range env.recentPicks {
					rec.at = time.Now().Add(-2 * pickTrackerTTL)
					env.recentPicks[key] = rec
				}
				env.recentPicksMu.Unlock()
			case "evicted":
				env.recentPicksMu.Lock()
				clear(env.recentPicks)
				env.recentPicksMu.Unlock()
			case "replaced":
				other := models.DecryptedCodingCredential{ID: uuid.New(), OrgID: orgID, Provider: models.ProviderOpenAISubscription, Status: models.CodingCredentialStatusActive, Config: models.OpenAISubscriptionConfig{AccessToken: "other-access", RefreshToken: "other-refresh", ExpiresAt: time.Now().Add(time.Hour)}}
				store.resolvable = map[models.ProviderName][]models.DecryptedCodingCredential{other.Provider: {other}}
				otherResolved := env.ResolveForModelWithCredential(ctx, orgID, models.AgentTypeCodex, &requestUserID, "other-model")
				require.Equal(t, other.ID, otherResolved.Credential.Credential.ID, "another execution should capture its own selected credential")
			}

			injected, err := env.InjectCodexAuthForCredential(ctx, orgID, &Sandbox{HomeDir: "/home/test"}, resolved.Credential)
			require.NoError(t, err, "captured auth injection should not depend on the shared tracker")
			require.Equal(t, tt.subscription, injected, "auth injection should preserve the original credential type")
			if tt.subscription {
				var payload struct {
					Tokens map[string]string `json:"tokens"`
				}
				require.NoError(t, json.Unmarshal(sandboxProvider.writes["/home/test/.codex/auth.json"], &payload), "injected auth should be valid JSON")
				require.Equal(t, map[string]string{"access_token": "captured-access", "refresh_token": "", "id_token": "captured-id"}, payload.Tokens, "auth should come from the execution's captured account")
			} else {
				require.Nil(t, sandboxProvider.writes, "an API-key execution must not acquire another execution's subscription auth")
			}
			limit := models.CodingCredentialRateLimit{Until: time.Now().Add(time.Hour), Message: "synthetic quota"}
			env.ShedRateLimitedForCredential(ctx, resolved.Credential, CredentialFailureSignal{RateLimitedUntil: limit.Until, Message: limit.Message})
			env.ShedAuthRejectedForCredential(ctx, resolved.Credential)
			expectedFailure := []capturedCredentialFailure{{Scope: expectedScope, ID: credentialID}}
			require.Equal(t, expectedFailure, store.rateFailures, "rate limiting should update the exact executed row and its backing scope")
			require.Equal(t, expectedFailure, store.authFailures, "auth rejection should update the exact executed row and its backing scope")
			require.Equal(t, []models.CodingCredentialRateLimit{limit}, store.rateLimits, "the captured credential should receive the observed provider cooldown")
			require.Equal(t, "captured-model", resolved.Credential.Binding.EffectiveModel, "other executions must not replace the captured runtime model")
		})
	}
}

func TestAgentEnvCapturedCodexRefreshUsesExactScope(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		personal bool
		reload   bool
	}{
		{name: "expired personal subscription", personal: true},
		{name: "expired org subscription"},
		{name: "token rejection reloads personal row", personal: true, reload: true},
		{name: "token rejection reloads org row", reload: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			orgID, userID, credentialID := uuid.New(), uuid.New(), uuid.New()
			var owner *uuid.UUID
			if tt.personal {
				owner = &userID
			}
			expiresAt := time.Now().Add(-time.Hour)
			if tt.reload {
				expiresAt = time.Now().Add(time.Hour)
			}
			credential := models.DecryptedCodingCredential{ID: credentialID, OrgID: orgID, UserID: owner, Provider: models.ProviderOpenAISubscription, Status: models.CodingCredentialStatusActive, Config: models.OpenAISubscriptionConfig{AccessToken: "stale-access", RefreshToken: "original-refresh", ExpiresAt: expiresAt}}
			store := &envCodingCredentialProvider{resolvable: map[models.ProviderName][]models.DecryptedCodingCredential{credential.Provider: {credential}}}
			refresher := &envCodexAuthProvider{refreshToken: &models.OpenAISubscriptionConfig{AccessToken: "fresh-access", IDToken: "fresh-id", ExpiresAt: time.Now().Add(time.Hour)}}
			sandboxProvider := &envSandboxProvider{}
			env := NewAgentEnv(AgentEnvDeps{CodingCredentials: store, CodexAuth: refresher, Provider: sandboxProvider, Logger: zerolog.Nop()})
			resolved := env.ResolveForModelWithCredential(ctx, orgID, models.AgentTypeCodex, &userID, "")
			other := credential
			other.ID = uuid.New()
			env.recordCredentialPick(orgID, &userID, models.ProviderOpenAI, other)
			var injected bool
			var err error
			if tt.reload {
				injected, err = env.RefreshCodexAuthForCredential(ctx, orgID, &Sandbox{HomeDir: "/home/test"}, resolved.Credential)
			} else {
				injected, err = env.InjectCodexAuthForCredential(ctx, orgID, &Sandbox{HomeDir: "/home/test"}, resolved.Credential)
			}
			require.NoError(t, err, "reload or refresh should use the captured row rather than a later shared pick")
			require.True(t, injected, "the auth service's returned config for the selected account should be injected")
			require.Equal(t, []uuid.UUID{credentialID}, refresher.refreshIDs, "the auth service should receive the executed credential ID")
			require.Equal(t, []models.Scope{{OrgID: orgID, UserID: owner}}, refresher.refreshScopes, "the auth service should receive the credential's actual personal or org scope")
			var payload struct {
				Tokens map[string]string `json:"tokens"`
			}
			require.NoError(t, json.Unmarshal(sandboxProvider.writes["/home/test/.codex/auth.json"], &payload), "refreshed auth should be valid JSON")
			require.Equal(t, map[string]string{"access_token": "fresh-access", "refresh_token": "", "id_token": "fresh-id"}, payload.Tokens, "auth injection should write the config returned for the captured credential")
		})
	}
}

func TestAgentEnvResolvedNoneDoesNotRepick(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	orgID, userID := uuid.New(), uuid.New()
	store := &capturedCredentialProvider{envCodingCredentialProvider: &envCodingCredentialProvider{}}
	sandboxProvider := &envSandboxProvider{}
	env := NewAgentEnv(AgentEnvDeps{CodingCredentials: store, Provider: sandboxProvider, Logger: zerolog.Nop()})
	resolved := env.ResolveForModelWithCredential(ctx, orgID, models.AgentTypeCodex, &userID, "")
	require.NotNil(t, resolved, "a resolved-none result should remain distinguishable from a legacy caller")
	require.Nil(t, resolved.Credential, "empty unified resolver should capture no credential")
	other := models.DecryptedCodingCredential{ID: uuid.New(), OrgID: orgID, Provider: models.ProviderOpenAISubscription, Status: models.CodingCredentialStatusActive, Config: models.OpenAISubscriptionConfig{AccessToken: "other-access", RefreshToken: "other-refresh", ExpiresAt: time.Now().Add(time.Hour)}}
	store.resolvable = map[models.ProviderName][]models.DecryptedCodingCredential{other.Provider: {other}}
	env.ResolveForModelWithCredential(ctx, orgID, models.AgentTypeCodex, &userID, "")
	injected, err := env.InjectCodexAuthForCredential(ctx, orgID, &Sandbox{HomeDir: "/home/test"}, resolved.Credential)
	require.NoError(t, err, "resolved-none injection should be a no-op")
	require.False(t, injected, "resolved-none must not repick another execution's credential")
	env.ShedRateLimitedForCredential(ctx, resolved.Credential, CredentialFailureSignal{})
	env.ShedAuthRejectedForCredential(ctx, resolved.Credential)
	require.Nil(t, sandboxProvider.writes, "resolved-none should not write another account's auth")
	require.Nil(t, store.rateFailures, "resolved-none should not rate-limit another account")
	require.Nil(t, store.authFailures, "resolved-none should not reject another account")
}

func TestAgentEnvCapturedOpenCodeRoute(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, model, expectedModel string
		backing                    models.ProviderName
	}{
		{name: "physical OpenAI route", model: models.OpenCodeModelGPT54Mini, expectedModel: models.OpenCodeModelGPT54Mini, backing: models.ProviderOpenAI},
		{name: "custom OpenRouter route", model: "example/custom-model", expectedModel: "openrouter/example/custom-model", backing: models.ProviderOpenRouter},
		{name: "default model for backing", backing: models.ProviderOpenAI},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			orgID, credentialID := uuid.New(), uuid.New()
			credential := models.DecryptedCodingCredential{ID: credentialID, OrgID: orgID, Provider: models.ProviderOpenCode, Status: models.CodingCredentialStatusActive, Config: models.OpenCodeConfig{APIKey: "opencode-secret", BackingProvider: tt.backing}}
			env := NewAgentEnv(AgentEnvDeps{CodingCredentials: &envCodingCredentialProvider{resolvable: map[models.ProviderName][]models.DecryptedCodingCredential{credential.Provider: {credential}}}, Logger: zerolog.Nop()})
			resolved := env.ResolveForModelWithCredential(context.Background(), orgID, models.AgentTypeOpenCode, nil, tt.model)
			require.NotNil(t, resolved.Credential, "OpenCode resolution should capture its own provider row")
			expectedModel := tt.expectedModel
			if expectedModel == "" {
				expectedModel = openCodeCLIModelID(models.DefaultOpenCodePhysicalModelForBacking(tt.backing), tt.backing)
			}
			require.Equal(t, runtimeCredentialBinding{CredentialID: credentialID, AgentType: models.AgentTypeOpenCode, Provider: models.ProviderOpenCode, BackingProvider: tt.backing, EffectiveModel: expectedModel, CredentialScope: models.CodingCredentialScopeOrg}, resolved.Credential.Binding, "binding should retain the exact physical model, backing, and row that supplied auth")
			require.Equal(t, expectedModel, resolved.Env["OPENCODE_MODEL"], "captured model should match the executed CLI model")
		})
	}
}

func TestCapturedCredentialSecretsAreNotSerialized(t *testing.T) {
	t.Parallel()
	credential := models.DecryptedCodingCredential{ID: uuid.New(), OrgID: uuid.New(), Provider: models.ProviderOpenAISubscription, Config: models.OpenAISubscriptionConfig{AccessToken: "private-access", RefreshToken: "private-refresh", IDToken: "private-id"}}
	resolved := &resolvedAgentEnv{Env: map[string]string{"SYNTHETIC_SECRET": "private-env"}, Credential: captureRuntimeCredential(credential, models.AgentTypeCodex, nil)}
	tests := []struct {
		name  string
		value any
	}{
		{name: "resolution", value: resolved},
		{name: "selection", value: resolved.Credential},
		{name: "sandbox", value: &Sandbox{Env: resolved.Env, credentialResolution: resolved}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			encoded, err := json.Marshal(tt.value)
			require.NoError(t, err, "captured state should remain safe to marshal")
			for _, secret := range []string{"private-access", "private-refresh", "private-id", "private-env"} {
				require.NotContains(t, string(encoded), secret, "JSON output must exclude all secret-bearing captured state")
			}
		})
	}
}

// sequentialCredentialPicker makes concurrent resolution order explicit without
// sharing mutable fixture rows between the two executions.
type sequentialCredentialPicker struct {
	*capturedCredentialProvider
	mu    sync.Mutex
	picks []models.DecryptedCodingCredential
}

func (p *sequentialCredentialPicker) PickRunnableMulti(_ context.Context, _ models.Scope, _ []models.ProviderName) (*models.DecryptedCodingCredential, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	picked := p.picks[0]
	p.picks = p.picks[1:]
	return &picked, nil
}

type gatedIntegrationCredentials struct {
	*envCredentialProvider
	calls        atomic.Int32
	firstPicked  chan struct{}
	releaseFirst chan struct{}
}

func (p *gatedIntegrationCredentials) GetAllIntegrations(ctx context.Context, _ uuid.UUID, _ []models.ProviderName) (map[models.ProviderName]*models.DecryptedCredential, error) {
	if p.calls.Add(1) == 1 {
		close(p.firstPicked)
		select {
		case <-p.releaseFirst:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return nil, nil
}

func TestAgentEnvConcurrentResolutionCapturesItsOwnCredential(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name              string
		firstSubscription bool
	}{
		{name: "subscription followed by API key", firstSubscription: true},
		{name: "API key followed by subscription"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			orgID, userID := uuid.New(), uuid.New()
			apiCredential := models.DecryptedCodingCredential{ID: uuid.New(), OrgID: orgID, Provider: models.ProviderOpenAI, Status: models.CodingCredentialStatusActive, Config: models.OpenAIConfig{APIKey: "captured-api"}}
			subCredential := models.DecryptedCodingCredential{ID: uuid.New(), OrgID: orgID, UserID: &userID, Provider: models.ProviderOpenAISubscription, Status: models.CodingCredentialStatusActive, Config: models.OpenAISubscriptionConfig{AccessToken: "captured-access", RefreshToken: "captured-refresh", ExpiresAt: time.Now().Add(time.Hour)}}
			first, second := apiCredential, subCredential
			if tt.firstSubscription {
				first, second = subCredential, apiCredential
			}
			store := &sequentialCredentialPicker{capturedCredentialProvider: &capturedCredentialProvider{envCodingCredentialProvider: &envCodingCredentialProvider{resolvable: map[models.ProviderName][]models.DecryptedCodingCredential{apiCredential.Provider: {apiCredential}, subCredential.Provider: {subCredential}}}}, picks: []models.DecryptedCodingCredential{first, second}}
			gated := &gatedIntegrationCredentials{envCredentialProvider: &envCredentialProvider{}, firstPicked: make(chan struct{}), releaseFirst: make(chan struct{})}
			env := NewAgentEnv(AgentEnvDeps{CodingCredentials: store, Credentials: gated, Logger: zerolog.Nop()})
			firstResult := make(chan *resolvedAgentEnv, 1)
			go func() {
				firstResult <- env.ResolveForModelWithCredential(ctx, orgID, models.AgentTypeCodex, &userID, "first-model")
			}()
			select {
			case <-gated.firstPicked:
			case <-ctx.Done():
				t.Fatal("first resolution did not reach its credential capture boundary")
			}
			secondResolved := env.ResolveForModelWithCredential(ctx, orgID, models.AgentTypeCodex, &userID, "second-model")
			close(gated.releaseFirst)
			var firstResolved *resolvedAgentEnv
			select {
			case firstResolved = <-firstResult:
			case <-ctx.Done():
				t.Fatal("first resolution did not complete after the concurrent pick")
			}
			require.Equal(t, first.ID, firstResolved.Credential.Credential.ID, "first execution must retain the direct picker result after another execution overwrites the shared slot")
			require.Equal(t, second.ID, secondResolved.Credential.Credential.ID, "second execution must retain its separate picker result")
			require.Equal(t, "first-model", firstResolved.Credential.Binding.EffectiveModel, "first execution model must remain bound to its own credential")
			require.Equal(t, "second-model", secondResolved.Credential.Binding.EffectiveModel, "second execution model must remain bound to its own credential")
			env.ShedRateLimitedForCredential(ctx, firstResolved.Credential, CredentialFailureSignal{})
			require.Equal(t, []capturedCredentialFailure{{Scope: first.Scope(), ID: first.ID}}, store.rateFailures, "a failed first execution must not shed the second execution's account")
		})
	}
}
