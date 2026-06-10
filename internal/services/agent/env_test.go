package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/models"
)

type envCredentialProvider struct {
	creds     map[models.ProviderName]*models.DecryptedCredential
	listCreds map[models.ProviderName][]models.DecryptedCredential
	errs      map[models.ProviderName]error
	listErrs  map[models.ProviderName]error
	getCalls  []models.ProviderName
	batch     []models.ProviderName
	batchErr  error
}

// defaultActiveStatus returns the credential with Status="active" when the
// caller didn't explicitly set one. The legacy tables' production rows always
// carry an explicit status; tests that pre-date the resolver's status filter
// were written without it, and forcing every fixture to repeat
// `Status: "active"` would be churn for no signal.
func (envCredentialProvider) defaultActiveStatus(cred *models.DecryptedCredential) *models.DecryptedCredential {
	if cred == nil {
		return nil
	}
	if cred.Status == "" {
		copy := *cred
		copy.Status = models.CredentialStatusActive
		return &copy
	}
	return cred
}

func (envCredentialProvider) defaultActiveStatuses(creds []models.DecryptedCredential) []models.DecryptedCredential {
	out := make([]models.DecryptedCredential, len(creds))
	for i, c := range creds {
		if c.Status == "" {
			c.Status = models.CredentialStatusActive
		}
		out[i] = c
	}
	return out
}

func (m *envCredentialProvider) Get(_ context.Context, _ uuid.UUID, provider models.ProviderName) (*models.DecryptedCredential, error) {
	m.getCalls = append(m.getCalls, provider)
	if err, ok := m.errs[provider]; ok {
		return nil, err
	}
	if cred, ok := m.creds[provider]; ok {
		return m.defaultActiveStatus(cred), nil
	}
	return nil, nil
}

func (m *envCredentialProvider) GetAllIntegrations(_ context.Context, _ uuid.UUID, providers []models.ProviderName) (map[models.ProviderName]*models.DecryptedCredential, error) {
	m.batch = append([]models.ProviderName(nil), providers...)
	if m.batchErr != nil {
		return nil, m.batchErr
	}
	out := make(map[models.ProviderName]*models.DecryptedCredential, len(providers))
	for _, provider := range providers {
		if cred, ok := m.creds[provider]; ok && cred != nil {
			out[provider] = m.defaultActiveStatus(cred)
		}
	}
	return out, nil
}

func (m *envCredentialProvider) ListByProvider(_ context.Context, _ uuid.UUID, provider models.ProviderName) ([]models.DecryptedCredential, error) {
	if err, ok := m.listErrs[provider]; ok {
		return nil, err
	}
	if creds, ok := m.listCreds[provider]; ok {
		return m.defaultActiveStatuses(creds), nil
	}
	if cred, ok := m.creds[provider]; ok && cred != nil {
		c := m.defaultActiveStatus(cred)
		return []models.DecryptedCredential{*c}, nil
	}
	return nil, nil
}

type envUserCredentialProvider struct {
	personal map[models.ProviderName]*models.DecryptedUserCredential
	team     map[models.ProviderName]*models.DecryptedUserCredential
}

// defaultUserActiveStatus mirrors envCredentialProvider.defaultActiveStatus
// for DecryptedUserCredential. Same rationale: pre-status-filter tests didn't
// set Status, and the legacy production data always does.
func defaultUserActiveStatus(cred *models.DecryptedUserCredential) *models.DecryptedUserCredential {
	if cred == nil {
		return nil
	}
	if cred.Status == "" {
		copy := *cred
		copy.Status = models.CredentialStatusActive
		return &copy
	}
	return cred
}

func (m *envUserCredentialProvider) GetForUser(_ context.Context, _, _ uuid.UUID, provider models.ProviderName) (*models.DecryptedUserCredential, error) {
	if cred, ok := m.personal[provider]; ok {
		return defaultUserActiveStatus(cred), nil
	}
	return nil, nil
}

func (m *envUserCredentialProvider) GetTeamDefault(_ context.Context, _ uuid.UUID, provider models.ProviderName) (*models.DecryptedUserCredential, error) {
	if cred, ok := m.team[provider]; ok {
		return defaultUserActiveStatus(cred), nil
	}
	return nil, nil
}

type envCodingCredentialProvider struct {
	resolvable      map[models.ProviderName][]models.DecryptedCodingCredential
	errs            map[models.ProviderName]error
	rateLimitedIDs  []uuid.UUID
	authRejectedIDs []uuid.UUID
	rateLimits      []models.CodingCredentialRateLimit
}

func (m *envCodingCredentialProvider) ListResolvable(_ context.Context, _ uuid.UUID, _ *uuid.UUID, provider models.ProviderName) ([]models.DecryptedCodingCredential, error) {
	if err, ok := m.errs[provider]; ok {
		return nil, err
	}
	if creds, ok := m.resolvable[provider]; ok {
		return creds, nil
	}
	return nil, nil
}

func (m *envCodingCredentialProvider) PickRunnable(_ context.Context, _ models.Scope, provider models.ProviderName) (*models.DecryptedCodingCredential, error) {
	if err, ok := m.errs[provider]; ok {
		return nil, err
	}
	for _, cred := range m.resolvable[provider] {
		if cred.Status != models.CodingCredentialStatusActive {
			continue
		}
		if containsUUID(m.rateLimitedIDs, cred.ID) || containsUUID(m.authRejectedIDs, cred.ID) {
			continue
		}
		picked := cred
		return &picked, nil
	}
	return nil, errEnvCodingCredentialNotFound
}

func (m *envCodingCredentialProvider) PickRunnableMulti(_ context.Context, _ models.Scope, providers []models.ProviderName) (*models.DecryptedCodingCredential, error) {
	creds := make([]models.DecryptedCodingCredential, 0)
	for _, provider := range providers {
		if err, ok := m.errs[provider]; ok {
			return nil, err
		}
		creds = append(creds, m.resolvable[provider]...)
	}
	sortCodingCredentialResolutionRows(creds)
	for _, cred := range creds {
		if cred.Status != models.CodingCredentialStatusActive {
			continue
		}
		if containsUUID(m.rateLimitedIDs, cred.ID) || containsUUID(m.authRejectedIDs, cred.ID) {
			continue
		}
		picked := cred
		return &picked, nil
	}
	return nil, errEnvCodingCredentialNotFound
}

var errEnvCodingCredentialNotFound = errors.New("coding credential not found")

func containsUUID(ids []uuid.UUID, id uuid.UUID) bool {
	for _, existing := range ids {
		if existing == id {
			return true
		}
	}
	return false
}

func envExpiredCodexSubscriptionCredential(orgID uuid.UUID) *envCodingCredentialProvider {
	return &envCodingCredentialProvider{
		resolvable: map[models.ProviderName][]models.DecryptedCodingCredential{
			models.ProviderOpenAISubscription: {
				{
					ID:       uuid.New(),
					OrgID:    orgID,
					Provider: models.ProviderOpenAISubscription,
					Status:   models.CodingCredentialStatusActive,
					Config: models.OpenAISubscriptionConfig{
						AccessToken:  "expired-access",
						RefreshToken: "refresh-token",
						IDToken:      "id-token",
						ExpiresAt:    time.Now().Add(-time.Minute),
					},
				},
			},
		},
	}
}

// MarkRateLimited / MarkAuthRejected satisfy CodingCredentialShedder so the
// env tests can assert that ShedRateLimited / ShedAuthRejected forward the
// recorded credential id.
func (m *envCodingCredentialProvider) MarkRateLimited(id uuid.UUID) {
	m.rateLimitedIDs = append(m.rateLimitedIDs, id)
}

func (m *envCodingCredentialProvider) MarkAuthRejected(id uuid.UUID) {
	m.authRejectedIDs = append(m.authRejectedIDs, id)
}

func (m *envCodingCredentialProvider) MarkRateLimitedForScope(_ context.Context, _ models.Scope, id uuid.UUID, limit models.CodingCredentialRateLimit) error {
	m.rateLimitedIDs = append(m.rateLimitedIDs, id)
	m.rateLimits = append(m.rateLimits, limit)
	return nil
}

func (m *envCodingCredentialProvider) MarkAuthRejectedForScope(_ context.Context, _ models.Scope, id uuid.UUID) error {
	m.authRejectedIDs = append(m.authRejectedIDs, id)
	return nil
}

type envOrgStore struct {
	org   models.Organization
	err   error
	calls int
}

func (m *envOrgStore) GetByID(_ context.Context, _ uuid.UUID) (models.Organization, error) {
	m.calls++
	if m.err != nil {
		return models.Organization{}, m.err
	}
	return m.org, nil
}

type envCodexAuthProvider struct {
	token         *models.OpenAIChatGPTConfig
	err           error
	refreshToken  *models.OpenAIChatGPTConfig
	refreshErr    error
	authInvalid   bool
	refreshIDs    []uuid.UUID
	refreshScopes []models.Scope
}

func (m envCodexAuthProvider) GetValidToken(_ context.Context, _ uuid.UUID) (*models.OpenAIChatGPTConfig, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.token, nil
}

func (m envCodexAuthProvider) IsAuthInvalid(_ error) bool {
	return m.authInvalid
}

func (m *envCodexAuthProvider) RefreshTokenByID(_ context.Context, scope models.Scope, credID uuid.UUID) (*models.OpenAIChatGPTConfig, error) {
	m.refreshIDs = append(m.refreshIDs, credID)
	m.refreshScopes = append(m.refreshScopes, scope)
	if m.refreshErr != nil {
		return nil, m.refreshErr
	}
	return m.refreshToken, nil
}

type envSandboxProvider struct {
	execExitCode    int
	execErr         error
	execStdoutByCmd map[string]string
	writeErrByPath  map[string]error
	writes          map[string][]byte
	commands        []string
}

func (m *envSandboxProvider) Name() string { return "env-sandbox" }

func (m *envSandboxProvider) Create(_ context.Context, _ SandboxConfig) (*Sandbox, error) {
	return &Sandbox{ID: "unused", HomeDir: "/home/test", WorkDir: "/workspace"}, nil
}

func (m *envSandboxProvider) CloneRepo(_ context.Context, _ *Sandbox, _, _, _ string) error {
	return nil
}

func (m *envSandboxProvider) Exec(_ context.Context, _ *Sandbox, cmd string, stdout, stderr io.Writer) (int, error) {
	m.commands = append(m.commands, cmd)
	if m.execErr != nil {
		return 0, m.execErr
	}
	if stdout != nil {
		if out := m.execStdoutByCmd[cmd]; out != "" {
			if _, err := io.WriteString(stdout, out); err != nil {
				return 0, err
			}
		}
	}
	if m.execExitCode != 0 && stderr != nil {
		if _, err := io.WriteString(stderr, "mkdir failed"); err != nil {
			return 0, err
		}
	}
	return m.execExitCode, nil
}

func (m *envSandboxProvider) ReadFile(_ context.Context, _ *Sandbox, _ string) ([]byte, error) {
	return nil, nil
}

func (m *envSandboxProvider) WriteFile(_ context.Context, _ *Sandbox, path string, data []byte) error {
	if err := m.writeErrByPath[path]; err != nil {
		return err
	}
	if m.writes == nil {
		m.writes = make(map[string][]byte)
	}
	m.writes[path] = append([]byte(nil), data...)
	return nil
}

func (m *envSandboxProvider) Destroy(_ context.Context, _ *Sandbox) error { return nil }

func (m *envSandboxProvider) IsAlive(_ context.Context, _ *Sandbox) (bool, error) { return true, nil }

func (m *envSandboxProvider) ConnectionInfo(_ context.Context, _ *Sandbox) (*SandboxConnectionInfo, error) {
	return nil, nil
}

func (m *envSandboxProvider) Snapshot(_ context.Context, _ *Sandbox) (io.ReadCloser, error) {
	return io.NopCloser(bytes.NewReader(nil)), nil
}

func (m *envSandboxProvider) Restore(_ context.Context, _ *Sandbox, _ io.Reader) error { return nil }

func (m *envSandboxProvider) ExecStream(_ context.Context, _ *Sandbox, _ string, _ func([]byte), _ io.Writer) (int, error) {
	return 0, nil
}

func marshalAgentSettings(t *testing.T, settings models.OrgSettings) json.RawMessage {
	t.Helper()

	raw, err := json.Marshal(settings)
	require.NoError(t, err, "marshalAgentSettings should serialize org settings")
	return raw
}

func TestAuthErrorError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		err      *AuthError
		expected string
	}{
		{
			name:     "includes agent type when present",
			err:      &AuthError{AgentType: models.AgentTypeCodex, Detail: "missing token"},
			expected: "agent auth failed (codex): missing token",
		},
		{
			name:     "omits agent type when empty",
			err:      &AuthError{Detail: "missing token"},
			expected: "agent auth failed: missing token",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.expected, tt.err.Error(), "AuthError.Error should format the expected message")
		})
	}
}

func TestAgentEnvResolveExportsCredentialsAndIntegrations(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	orgID := uuid.New()
	userID := uuid.New()

	tests := []struct {
		name      string
		agentType models.AgentType
		userCreds *envUserCredentialProvider
		orgCreds  *envCredentialProvider
		expected  map[string]string
	}{
		{
			name:      "claude uses personal credential and integration tokens",
			agentType: models.AgentTypeClaudeCode,
			userCreds: &envUserCredentialProvider{
				personal: map[models.ProviderName]*models.DecryptedUserCredential{
					models.ProviderAnthropic: {Config: models.AnthropicConfig{APIKey: "sk-ant", BaseURL: "https://anthropic.example"}},
				},
			},
			orgCreds: &envCredentialProvider{
				creds: map[models.ProviderName]*models.DecryptedCredential{
					models.ProviderSentry: {Config: models.SentryConfig{AccessToken: "sentry-token", OrgSlug: "assembled"}},
					models.ProviderLinear: {Config: models.LinearConfig{AccessToken: "linear-token"}},
					models.ProviderNotion: {Config: models.NotionConfig{AccessToken: "notion-token"}},
					models.ProviderMezmo:  {Config: models.MezmoConfig{APIKey: "mezmo-key", BaseURL: "https://logs.example.com", Dataset: "prod"}},
				},
			},
			expected: map[string]string{
				"ANTHROPIC_API_KEY":   "sk-ant",
				"ANTHROPIC_BASE_URL":  "https://anthropic.example",
				"SENTRY_AUTH_TOKEN":   "sentry-token",
				"SENTRY_ORG_SLUG":     "assembled",
				"LINEAR_ACCESS_TOKEN": "linear-token",
				"NOTION_ACCESS_TOKEN": "notion-token",
				"MEZMO_API_KEY":       "mezmo-key",
				"MEZMO_BASE_URL":      "https://logs.example.com",
				"MEZMO_DATASET":       "prod",
			},
		},
		{
			name:      "codex uses team default openai config and disables nested sandbox",
			agentType: models.AgentTypeCodex,
			userCreds: &envUserCredentialProvider{
				team: map[models.ProviderName]*models.DecryptedUserCredential{
					models.ProviderOpenAI: {Config: models.OpenAIConfig{APIKey: "sk-openai", BaseURL: "https://openai.example"}},
				},
			},
			orgCreds: &envCredentialProvider{},
			expected: map[string]string{
				"OPENAI_API_KEY":                "sk-openai",
				"OPENAI_BASE_URL":               "https://openai.example",
				"CODEX_UNSAFE_ALLOW_NO_SANDBOX": "1",
			},
		},
		{
			name:      "gemini falls back to org credential",
			agentType: models.AgentTypeGeminiCLI,
			userCreds: &envUserCredentialProvider{},
			orgCreds: &envCredentialProvider{
				creds: map[models.ProviderName]*models.DecryptedCredential{
					models.ProviderGemini: {Config: models.GeminiConfig{APIKey: "gem-key", Model: "gemini-2.5-pro"}},
				},
			},
			expected: map[string]string{
				"GEMINI_API_KEY": "gem-key",
				"GEMINI_MODEL":   "gemini-2.5-pro",
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			env := NewAgentEnv(AgentEnvDeps{
				Credentials:     tt.orgCreds,
				UserCredentials: tt.userCreds,
				Provider:        &envSandboxProvider{},
				Logger:          zerolog.Nop(),
			})

			got := env.Resolve(ctx, orgID, tt.agentType, &userID)
			for key, expected := range tt.expected {
				require.Equal(t, expected, got[key], "Resolve should export %s for %s", key, tt.agentType)
			}
		})
	}
}

func TestAgentEnvFetchIntegrationCredentialsUsesBatchLookup(t *testing.T) {
	t.Parallel()

	orgCreds := &envCredentialProvider{
		creds: map[models.ProviderName]*models.DecryptedCredential{
			models.ProviderSentry:   {Config: models.SentryConfig{AccessToken: "sentry-token", OrgSlug: "assembled"}},
			models.ProviderLinear:   {Config: models.LinearConfig{AccessToken: "linear-token"}},
			models.ProviderNotion:   {Config: models.NotionConfig{AccessToken: "notion-token"}},
			models.ProviderCircleCI: {Config: models.CircleCIConfig{AuthToken: "circle-token", ProjectSlug: "gh/acme/repo"}},
			models.ProviderMezmo:    {Config: models.MezmoConfig{APIKey: "mezmo-key"}},
		},
	}
	env := NewAgentEnv(AgentEnvDeps{
		Credentials: orgCreds,
		Provider:    &envSandboxProvider{},
		Logger:      zerolog.Nop(),
	})

	creds := env.fetchIntegrationCredentials(context.Background(), uuid.New())
	require.Equal(t, []models.ProviderName{
		models.ProviderSentry,
		models.ProviderLinear,
		models.ProviderNotion,
		models.ProviderCircleCI,
		models.ProviderMezmo,
	}, orgCreds.batch, "fetchIntegrationCredentials should request all integrations in one batch")
	require.Empty(t, orgCreds.getCalls, "fetchIntegrationCredentials should not issue per-provider Get calls")
	require.NotNil(t, creds.Sentry, "fetchIntegrationCredentials should decode Sentry from batch results")
	require.NotNil(t, creds.Linear, "fetchIntegrationCredentials should decode Linear from batch results")
	require.NotNil(t, creds.Notion, "fetchIntegrationCredentials should decode Notion from batch results")
	require.NotNil(t, creds.CircleCI, "fetchIntegrationCredentials should decode CircleCI from batch results")
	require.NotNil(t, creds.Mezmo, "fetchIntegrationCredentials should decode Mezmo from batch results")
}

// fakeLinearTokens implements LinearTokenResolver for env tests. The
// scripted return values let each test pin "refresh succeeds with new
// token" / "no integration installed" / "refresh exploded" without
// involving the real linear service.
type fakeLinearTokens struct {
	token string
	err   error
	calls int
}

func (f *fakeLinearTokens) GetValidAccessToken(_ context.Context, _ uuid.UUID) (string, error) {
	f.calls++
	return f.token, f.err
}

func TestAgentEnvResolveLinearTokenInjection(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	orgID := uuid.New()
	userID := uuid.New()

	t.Run("resolver returns rotated token; raw cached token is ignored", func(t *testing.T) {
		t.Parallel()
		// Cache holds an old token; resolver returns the rotated one. The
		// resolver path must win — that's the entire point of plumbing
		// refresh-aware token resolution into env injection.
		env := NewAgentEnv(AgentEnvDeps{
			Credentials: &envCredentialProvider{
				creds: map[models.ProviderName]*models.DecryptedCredential{
					models.ProviderAnthropic: {Config: models.AnthropicConfig{APIKey: "sk-ant"}},
					models.ProviderLinear:    {Config: models.LinearConfig{AccessToken: "stale-cached-token"}},
				},
			},
			UserCredentials: &envUserCredentialProvider{},
			Provider:        &envSandboxProvider{},
			Logger:          zerolog.Nop(),
		})
		resolver := &fakeLinearTokens{token: "rotated-token"}
		env.SetLinearTokens(resolver)

		got := env.Resolve(ctx, orgID, models.AgentTypeClaudeCode, &userID)
		require.Equal(t, "rotated-token", got["LINEAR_ACCESS_TOKEN"], "rotated token from resolver must override the cached row")
		require.Equal(t, 1, resolver.calls, "resolver must be consulted")
	})

	t.Run("resolver returns empty for missing integration; env var is not set", func(t *testing.T) {
		t.Parallel()
		env := NewAgentEnv(AgentEnvDeps{
			Credentials: &envCredentialProvider{
				creds: map[models.ProviderName]*models.DecryptedCredential{
					models.ProviderAnthropic: {Config: models.AnthropicConfig{APIKey: "sk-ant"}},
				},
			},
			UserCredentials: &envUserCredentialProvider{},
			Provider:        &envSandboxProvider{},
			Logger:          zerolog.Nop(),
		})
		env.SetLinearTokens(&fakeLinearTokens{token: "", err: nil})

		got := env.Resolve(ctx, orgID, models.AgentTypeClaudeCode, &userID)
		_, present := got["LINEAR_ACCESS_TOKEN"]
		require.False(t, present, "no integration → no LINEAR_ACCESS_TOKEN env var")
	})

	t.Run("resolver hard failure leaves LINEAR_ACCESS_TOKEN unset rather than injecting a known-bad cached token", func(t *testing.T) {
		t.Parallel()
		// Even with a cached token in the credential store, a refresh hard
		// failure (revoked refresh token) must NOT inject the cached token —
		// the agent would just 401 in the sandbox. Better to omit the env
		// var so 143-tools reports "linear not configured" cleanly.
		env := NewAgentEnv(AgentEnvDeps{
			Credentials: &envCredentialProvider{
				creds: map[models.ProviderName]*models.DecryptedCredential{
					models.ProviderAnthropic: {Config: models.AnthropicConfig{APIKey: "sk-ant"}},
					models.ProviderLinear:    {Config: models.LinearConfig{AccessToken: "doomed-cached-token"}},
				},
			},
			UserCredentials: &envUserCredentialProvider{},
			Provider:        &envSandboxProvider{},
			Logger:          zerolog.Nop(),
		})
		env.SetLinearTokens(&fakeLinearTokens{err: errors.New("refresh token revoked")})

		got := env.Resolve(ctx, orgID, models.AgentTypeClaudeCode, &userID)
		_, present := got["LINEAR_ACCESS_TOKEN"]
		require.False(t, present, "resolver error must NOT fall through to the stale cached token")
	})

	t.Run("no resolver wired; falls back to raw cached token", func(t *testing.T) {
		t.Parallel()
		// Backward-compat path: tests / wiring that never called
		// SetLinearTokens must keep the legacy behavior of injecting the
		// raw stored access token. New production wiring always sets the
		// resolver, so this branch is only exercised by older fixtures.
		env := NewAgentEnv(AgentEnvDeps{
			Credentials: &envCredentialProvider{
				creds: map[models.ProviderName]*models.DecryptedCredential{
					models.ProviderAnthropic: {Config: models.AnthropicConfig{APIKey: "sk-ant"}},
					models.ProviderLinear:    {Config: models.LinearConfig{AccessToken: "legacy-token"}},
				},
			},
			UserCredentials: &envUserCredentialProvider{},
			Provider:        &envSandboxProvider{},
			Logger:          zerolog.Nop(),
		})

		got := env.Resolve(ctx, orgID, models.AgentTypeClaudeCode, &userID)
		require.Equal(t, "legacy-token", got["LINEAR_ACCESS_TOKEN"])
	})
}

func TestAgentEnvResolveAppliesCachedAgentConfigOverrides(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	orgID := uuid.New()
	cache := NewOrgSettingsCache(time.Minute)
	store := &envOrgStore{
		org: models.Organization{
			ID: orgID,
			Settings: marshalAgentSettings(t, models.OrgSettings{
				AgentConfig: models.AgentEnvConfig{
					string(models.AgentTypeAmp): {
						"AMP_MODE": models.AmpModeDeep,
					},
					string(models.AgentTypePi): {
						"PI_MODEL": "openai/gpt-5.1",
					},
				},
			}),
		},
	}
	env := NewAgentEnv(AgentEnvDeps{
		Credentials: &envCredentialProvider{
			creds: map[models.ProviderName]*models.DecryptedCredential{
				models.ProviderAmp: {Config: models.AmpConfig{APIKey: "amp-from-credential"}},
				models.ProviderPi:  {Config: models.PiConfig{APIKey: "pi-from-credential"}},
			},
		},
		Orgs:             store,
		OrgSettingsCache: cache,
		Provider:         &envSandboxProvider{},
		Logger:           zerolog.Nop(),
	})

	ampEnv := env.Resolve(ctx, orgID, models.AgentTypeAmp, nil)
	require.Equal(t, "amp-from-credential", ampEnv["AMP_API_KEY"], "Resolve should source Amp auth from credentials")
	require.Equal(t, models.AmpModeDeep, ampEnv["AMP_MODE"], "Resolve should apply AMP mode overrides from org settings")

	piEnv := env.Resolve(ctx, orgID, models.AgentTypePi, nil)
	require.Equal(t, "openai/gpt-5.1", piEnv["PI_MODEL"], "Resolve should apply PI model overrides from org settings")
	require.Equal(t, "pi-from-credential", piEnv["PI_API_KEY"], "Resolve should source Pi auth from credentials")
	require.NotContains(t, piEnv, "ANTHROPIC_API_KEY", "Resolve should not inherit sibling provider credentials for Pi")
	require.NotContains(t, piEnv, "OPENAI_API_KEY", "Resolve should not inherit sibling provider credentials for Pi")
	require.NotContains(t, piEnv, "GEMINI_API_KEY", "Resolve should not inherit sibling provider credentials for Pi")

	_ = env.Resolve(ctx, orgID, models.AgentTypePi, nil)
	require.Equal(t, 1, store.calls, "Resolve should use the org settings cache after the first load")
}

func TestAgentEnvLoadAgentConfigFailures(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	orgID := uuid.New()

	tests := []struct {
		name string
		env  *AgentEnv
	}{
		{
			name: "missing org store returns no overrides",
			env: NewAgentEnv(AgentEnvDeps{
				Provider: &envSandboxProvider{},
				Logger:   zerolog.Nop(),
			}),
		},
		{
			name: "org lookup error returns no overrides",
			env: NewAgentEnv(AgentEnvDeps{
				Orgs:     &envOrgStore{err: errors.New("db down")},
				Provider: &envSandboxProvider{},
				Logger:   zerolog.Nop(),
			}),
		},
		{
			name: "settings parse error returns no overrides",
			env: NewAgentEnv(AgentEnvDeps{
				Orgs: &envOrgStore{org: models.Organization{
					ID:       orgID,
					Settings: json.RawMessage(`{"default_agent_type":`),
				}},
				Provider: &envSandboxProvider{},
				Logger:   zerolog.Nop(),
			}),
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			config, ok := tt.env.loadAgentConfig(ctx, orgID, models.AgentTypeAmp)
			require.False(t, ok, "loadAgentConfig should report no config for %s", tt.name)
			require.Nil(t, config, "loadAgentConfig should return nil config for %s", tt.name)
		})
	}
}

func TestAgentEnvResolveProviderConfigPrecedence(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	orgID := uuid.New()
	userID := uuid.New()

	tests := []struct {
		name     string
		userCred *envUserCredentialProvider
		orgCred  *envCredentialProvider
		expected string
	}{
		{
			name: "personal credential wins",
			userCred: &envUserCredentialProvider{
				personal: map[models.ProviderName]*models.DecryptedUserCredential{
					models.ProviderAnthropic: {Config: models.AnthropicConfig{APIKey: "personal"}},
				},
				team: map[models.ProviderName]*models.DecryptedUserCredential{
					models.ProviderAnthropic: {Config: models.AnthropicConfig{APIKey: "team"}},
				},
			},
			orgCred: &envCredentialProvider{
				creds: map[models.ProviderName]*models.DecryptedCredential{
					models.ProviderAnthropic: {Config: models.AnthropicConfig{APIKey: "org"}},
				},
			},
			expected: "personal",
		},
		{
			name: "team default beats org when personal missing",
			userCred: &envUserCredentialProvider{
				team: map[models.ProviderName]*models.DecryptedUserCredential{
					models.ProviderAnthropic: {Config: models.AnthropicConfig{APIKey: "team"}},
				},
			},
			orgCred: &envCredentialProvider{
				creds: map[models.ProviderName]*models.DecryptedCredential{
					models.ProviderAnthropic: {Config: models.AnthropicConfig{APIKey: "org"}},
				},
			},
			expected: "team",
		},
		{
			name:     "org credential is the final fallback",
			userCred: &envUserCredentialProvider{},
			orgCred: &envCredentialProvider{
				creds: map[models.ProviderName]*models.DecryptedCredential{
					models.ProviderAnthropic: {Config: models.AnthropicConfig{APIKey: "org"}},
				},
			},
			expected: "org",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			env := NewAgentEnv(AgentEnvDeps{
				Credentials:     tt.orgCred,
				UserCredentials: tt.userCred,
				Provider:        &envSandboxProvider{},
				Logger:          zerolog.Nop(),
			})
			cfg := env.resolveProviderConfig(ctx, orgID, &userID, models.ProviderAnthropic)
			require.IsType(t, models.AnthropicConfig{}, cfg, "resolveProviderConfig should return the provider config type for %s", tt.name)
			require.Equal(t, tt.expected, cfg.(models.AnthropicConfig).APIKey, "resolveProviderConfig should honor precedence for %s", tt.name)
		})
	}
}

func TestAgentEnvResolveProviderConfig_UsesPriorityOrderedOrgAPIKeys(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	orgID := uuid.New()
	userID := uuid.New()

	env := NewAgentEnv(AgentEnvDeps{
		Credentials: &envCredentialProvider{
			listCreds: map[models.ProviderName][]models.DecryptedCredential{
				models.ProviderAnthropic: {
					{
						Provider: models.ProviderAnthropic,
						Label:    "subscription-first",
						Priority: 1,
						Config: models.AnthropicConfig{
							Subscription: &models.AnthropicSubscription{AccessToken: "sub-token", RefreshToken: "sub-refresh", ExpiresAt: time.Now().Add(time.Hour)},
						},
					},
					{
						Provider: models.ProviderAnthropic,
						Label:    "api-key-second",
						Priority: 2,
						Config:   models.AnthropicConfig{APIKey: "priority-api-key"},
					},
					{
						Provider: models.ProviderAnthropic,
						Label:    "api-key-third",
						Priority: 3,
						Config:   models.AnthropicConfig{APIKey: "lower-priority-api-key"},
					},
				},
			},
		},
		UserCredentials: &envUserCredentialProvider{},
		Provider:        &envSandboxProvider{},
		Logger:          zerolog.Nop(),
	})

	cfg := env.resolveProviderConfig(ctx, orgID, &userID, models.ProviderAnthropic)
	require.IsType(t, models.AnthropicConfig{}, cfg, "resolveProviderConfig should return an AnthropicConfig when org API keys exist")
	require.Equal(t, "priority-api-key", cfg.(models.AnthropicConfig).APIKey, "resolveProviderConfig should choose the highest-priority org API key row")
}

func TestAgentEnvResolveProviderConfig_UsesUnifiedSubscriptionTwin(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	orgID := uuid.New()
	userID := uuid.New()
	expiresAt := time.Now().Add(time.Hour)

	tests := []struct {
		name     string
		provider models.ProviderName
		rows     map[models.ProviderName][]models.DecryptedCodingCredential
		assert   func(t *testing.T, cfg models.ProviderConfig)
	}{
		{
			name:     "returns anthropic subscription twin as legacy anthropic config",
			provider: models.ProviderAnthropic,
			rows: map[models.ProviderName][]models.DecryptedCodingCredential{
				models.ProviderAnthropicSubscription: {
					{
						ID:       uuid.New(),
						OrgID:    orgID,
						Provider: models.ProviderAnthropicSubscription,
						Config: models.AnthropicSubscriptionConfig{
							AccessToken:  "claude-token",
							RefreshToken: "claude-refresh",
							ExpiresAt:    expiresAt,
						},
						Status: models.CodingCredentialStatusActive,
					},
				},
			},
			assert: func(t *testing.T, cfg models.ProviderConfig) {
				t.Helper()
				require.IsType(t, models.AnthropicConfig{}, cfg, "resolveProviderConfig should return the legacy AnthropicConfig shape")
				sub := cfg.(models.AnthropicConfig).Subscription
				require.NotNil(t, sub, "resolveProviderConfig should preserve the Claude subscription payload")
				require.Equal(t, "claude-token", sub.AccessToken, "resolveProviderConfig should preserve the Claude access token")
				require.Equal(t, "claude-refresh", sub.RefreshToken, "resolveProviderConfig should preserve the Claude refresh token")
			},
		},
		{
			name:     "returns openai subscription twin as legacy chatgpt config",
			provider: models.ProviderOpenAI,
			rows: map[models.ProviderName][]models.DecryptedCodingCredential{
				models.ProviderOpenAISubscription: {
					{
						ID:       uuid.New(),
						OrgID:    orgID,
						Provider: models.ProviderOpenAISubscription,
						Config: models.OpenAISubscriptionConfig{
							AccessToken:  "openai-token",
							RefreshToken: "openai-refresh",
							ExpiresAt:    expiresAt,
							AccountType:  "plus",
						},
						Status: models.CodingCredentialStatusActive,
					},
				},
			},
			assert: func(t *testing.T, cfg models.ProviderConfig) {
				t.Helper()
				require.IsType(t, models.OpenAIChatGPTConfig{}, cfg, "resolveProviderConfig should return the legacy OpenAIChatGPTConfig shape")
				require.Equal(t, "openai-token", cfg.(models.OpenAIChatGPTConfig).AccessToken, "resolveProviderConfig should preserve the OpenAI access token")
				require.Equal(t, "openai-refresh", cfg.(models.OpenAIChatGPTConfig).RefreshToken, "resolveProviderConfig should preserve the OpenAI refresh token")
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			env := NewAgentEnv(AgentEnvDeps{
				CodingCredentials: &envCodingCredentialProvider{resolvable: tt.rows},
				Provider:          &envSandboxProvider{},
				Logger:            zerolog.Nop(),
			})

			cfg := env.resolveProviderConfig(ctx, orgID, &userID, tt.provider)

			tt.assert(t, cfg)
		})
	}
}

func TestAgentEnvResolveProviderConfig_UnifiedPersonalRowsBeatOrgRowsAcrossAuthTypes(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	orgID := uuid.New()
	userID := uuid.New()
	expiresAt := time.Now().Add(time.Hour)

	tests := []struct {
		name     string
		provider models.ProviderName
		rows     map[models.ProviderName][]models.DecryptedCodingCredential
		assert   func(t *testing.T, cfg models.ProviderConfig)
	}{
		{
			name:     "personal subscription beats org api key",
			provider: models.ProviderAnthropic,
			rows: map[models.ProviderName][]models.DecryptedCodingCredential{
				models.ProviderAnthropic: {
					{
						ID:       uuid.New(),
						OrgID:    orgID,
						Provider: models.ProviderAnthropic,
						Status:   models.CodingCredentialStatusActive,
						Priority: 1,
						Config:   models.AnthropicConfig{APIKey: "org-api-key"},
					},
				},
				models.ProviderAnthropicSubscription: {
					{
						ID:       uuid.New(),
						OrgID:    orgID,
						UserID:   &userID,
						Provider: models.ProviderAnthropicSubscription,
						Status:   models.CodingCredentialStatusActive,
						Priority: 10,
						Config: models.AnthropicSubscriptionConfig{
							AccessToken:  "personal-sub-token",
							RefreshToken: "personal-sub-refresh",
							ExpiresAt:    expiresAt,
						},
					},
				},
			},
			assert: func(t *testing.T, cfg models.ProviderConfig) {
				t.Helper()
				require.IsType(t, models.AnthropicConfig{}, cfg, "resolver should return the Claude runtime config shape")
				sub := cfg.(models.AnthropicConfig).Subscription
				require.NotNil(t, sub, "resolver should choose the personal subscription before org fallback")
				require.Equal(t, "personal-sub-token", sub.AccessToken, "resolver should use the personal subscription token")
			},
		},
		{
			name:     "personal api key beats org subscription",
			provider: models.ProviderOpenAI,
			rows: map[models.ProviderName][]models.DecryptedCodingCredential{
				models.ProviderOpenAI: {
					{
						ID:       uuid.New(),
						OrgID:    orgID,
						UserID:   &userID,
						Provider: models.ProviderOpenAI,
						Status:   models.CodingCredentialStatusActive,
						Priority: 10,
						Config:   models.OpenAIConfig{APIKey: "personal-openai-key"},
					},
				},
				models.ProviderOpenAISubscription: {
					{
						ID:       uuid.New(),
						OrgID:    orgID,
						Provider: models.ProviderOpenAISubscription,
						Status:   models.CodingCredentialStatusActive,
						Priority: 1,
						Config: models.OpenAISubscriptionConfig{
							AccessToken:  "org-sub-token",
							RefreshToken: "org-sub-refresh",
							ExpiresAt:    expiresAt,
						},
					},
				},
			},
			assert: func(t *testing.T, cfg models.ProviderConfig) {
				t.Helper()
				require.IsType(t, models.OpenAIConfig{}, cfg, "resolver should keep personal API keys ahead of org subscription fallback")
				require.Equal(t, "personal-openai-key", cfg.(models.OpenAIConfig).APIKey, "resolver should use the personal OpenAI API key")
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			env := NewAgentEnv(AgentEnvDeps{
				CodingCredentials: &envCodingCredentialProvider{resolvable: tt.rows},
				Provider:          &envSandboxProvider{},
				Logger:            zerolog.Nop(),
			})

			cfg := env.resolveProviderConfig(ctx, orgID, &userID, tt.provider)

			tt.assert(t, cfg)
		})
	}
}

func TestAgentEnvResolveOrgProviderConfigAndCompatibility(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	orgID := uuid.New()

	t.Run("returns nil when credential store is missing", func(t *testing.T) {
		t.Parallel()

		env := NewAgentEnv(AgentEnvDeps{
			Provider: &envSandboxProvider{},
			Logger:   zerolog.Nop(),
		})

		cfg, _, ok := env.resolveOrgProviderConfig(ctx, orgID, models.ProviderAnthropic)
		require.False(t, ok, "resolveOrgProviderConfig should not report a hit when the credential store is unwired")
		require.Nil(t, cfg, "resolveOrgProviderConfig should return nil when no org credential store is configured")
	})

	t.Run("falls back to singleton get when list lookup misses", func(t *testing.T) {
		t.Parallel()

		env := NewAgentEnv(AgentEnvDeps{
			Credentials: &envCredentialProvider{
				listErrs: map[models.ProviderName]error{
					models.ProviderOpenAI: errors.New("list failed"),
				},
				creds: map[models.ProviderName]*models.DecryptedCredential{
					models.ProviderOpenAI: {
						Provider: models.ProviderOpenAI,
						Config:   models.OpenAIConfig{APIKey: "sk-openai-fallback"},
					},
				},
			},
			Provider: &envSandboxProvider{},
			Logger:   zerolog.Nop(),
		})

		cfg, _, ok := env.resolveOrgProviderConfig(ctx, orgID, models.ProviderOpenAI)
		require.True(t, ok, "resolveOrgProviderConfig should report a hit on the singleton fallback")
		require.IsType(t, models.OpenAIConfig{}, cfg, "resolveOrgProviderConfig should fall back to Get when list lookup fails")
		require.Equal(t, "sk-openai-fallback", cfg.(models.OpenAIConfig).APIKey, "resolveOrgProviderConfig should use the fallback org API key")
	})

	t.Run("filters incompatible coding provider configs", func(t *testing.T) {
		t.Parallel()

		assertCompatible := func(provider models.ProviderName, cfg models.ProviderConfig, msg string) {
			t.Helper()
			out, ok := compatibleCodingProviderConfig(provider, cfg)
			require.True(t, ok, msg)
			require.NotNil(t, out, msg)
		}
		assertIncompatible := func(provider models.ProviderName, cfg models.ProviderConfig, msg string) {
			t.Helper()
			out, ok := compatibleCodingProviderConfig(provider, cfg)
			require.False(t, ok, msg)
			require.Nil(t, out, msg)
		}

		assertIncompatible(models.ProviderAnthropic, models.AnthropicConfig{Subscription: &models.AnthropicSubscription{AccessToken: "sub", RefreshToken: "refresh"}}, "compatibleCodingProviderConfig should reject Anthropic subscriptions for API-key env injection")
		assertCompatible(models.ProviderAnthropicSubscription, models.AnthropicSubscriptionConfig{AccessToken: "sub", RefreshToken: "refresh"}, "compatibleCodingProviderConfig should accept Anthropic subscription rows for the subscription twin")
		assertCompatible(models.ProviderOpenAISubscription, models.OpenAISubscriptionConfig{AccessToken: "openai-sub", RefreshToken: "openai-refresh"}, "compatibleCodingProviderConfig should accept OpenAI subscription rows for the subscription twin")
		assertCompatible(models.ProviderAnthropic, models.AnthropicConfig{APIKey: "sk-ant"}, "compatibleCodingProviderConfig should accept Anthropic API keys")
		assertCompatible(models.ProviderOpenAI, models.OpenAIConfig{APIKey: "sk-openai"}, "compatibleCodingProviderConfig should accept OpenAI API keys")
		assertCompatible(models.ProviderGemini, models.GeminiConfig{APIKey: "gem-key"}, "compatibleCodingProviderConfig should accept Gemini API keys")
		assertCompatible(models.ProviderOpenRouter, models.OpenRouterConfig{APIKey: "sk-or"}, "compatibleCodingProviderConfig should accept OpenRouter API keys")
		assertCompatible(models.ProviderAmp, models.AmpConfig{APIKey: "amp-key"}, "compatibleCodingProviderConfig should accept Amp API keys")
		assertCompatible(models.ProviderPi, models.PiConfig{APIKey: "pi-key"}, "compatibleCodingProviderConfig should accept Pi API keys")
		assertIncompatible(models.ProviderAmp, models.OpenAIConfig{APIKey: "sk-openai"}, "compatibleCodingProviderConfig should reject non-Amp configs for the Amp provider")
		assertIncompatible(models.ProviderPi, models.OpenAIConfig{APIKey: "sk-openai"}, "compatibleCodingProviderConfig should reject non-Pi configs for the Pi provider")
		assertIncompatible(models.ProviderOpenRouter, models.OpenRouterConfig{}, "compatibleCodingProviderConfig should reject empty OpenRouter configs")
		assertIncompatible(models.ProviderName("unknown"), models.OpenAIConfig{APIKey: "sk"}, "compatibleCodingProviderConfig should reject unknown providers")
	})
}

func TestAgentEnvCheckAuth(t *testing.T) {
	t.Parallel()

	env := NewAgentEnv(AgentEnvDeps{
		Provider: &envSandboxProvider{},
		Logger:   zerolog.Nop(),
	})

	err := env.CheckAuth(models.AgentTypeAmp, map[string]string{})
	require.Error(t, err, "CheckAuth should reject Amp runs with no AMP_API_KEY")
	require.Contains(t, err.Error(), "AMP_API_KEY", "CheckAuth should explain the missing Amp credential")

	require.NoError(t, env.CheckAuth(models.AgentTypeAmp, map[string]string{"AMP_API_KEY": "amp-key"}), "CheckAuth should accept Amp runs with AMP_API_KEY configured")

	err = env.CheckAuth(models.AgentTypePi, map[string]string{})
	require.Error(t, err, "CheckAuth should reject Pi runs with no PI_API_KEY")
	require.Contains(t, err.Error(), "PI_API_KEY", "CheckAuth should explain the missing Pi credential")

	require.NoError(t, env.CheckAuth(models.AgentTypePi, map[string]string{"PI_API_KEY": "pi-key"}), "CheckAuth should accept Pi runs with PI_API_KEY configured")

	until := time.Date(2026, 5, 13, 15, 50, 0, 0, time.UTC)
	err = env.CheckAuth(models.AgentTypeClaudeCode, map[string]string{
		internalAuthBlockedKey:                              "all Claude Code auths are rate limited until 8:50 AM",
		internalAuthBlockedProviderKey:                      string(models.ProviderAnthropic),
		internalAuthBlockedRateLimitedUntilKey:              until.Format(time.RFC3339Nano),
		internalAuthBlockedFallbackCandidatesUnavailableKey: "true",
	})
	require.Error(t, err, "CheckAuth should reject internally blocked credential stacks")
	var authErr *AuthError
	require.ErrorAs(t, err, &authErr, "CheckAuth should return structured AuthError metadata")
	require.Equal(t, models.ProviderAnthropic, authErr.Provider, "AuthError should preserve the blocked provider")
	require.Equal(t, until, *authErr.RateLimitedUntil, "AuthError should preserve the blocked reset time")
	require.True(t, authErr.FallbackCandidatesUnavailable, "AuthError should report unavailable fallback candidates")
}

// TestAgentEnvShedAfterPick verifies that the shed-on-failure wiring forwards
// the picked credential id to the underlying CodingCredentialShedder.
//
// Resolution flow under test:
//  1. resolveProviderConfig walks pickFromCodingProvider for ProviderAnthropic
//     and chooses the only active row.
//  2. recordPick stores credID under (orgID, userID, ProviderAnthropic).
//  3. ShedRateLimited looks up the recent pick for that key and calls
//     MarkRateLimited on the store with credID.
//
// The orchestrator path (shedOnRunResult) calls ShedRateLimited /
// ShedAuthRejected after a failed run; this test exercises the env-level
// plumbing in isolation.
func TestAgentEnvShedAfterPick(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	orgID := uuid.New()
	userID := uuid.New()
	credID := uuid.New()

	coding := &envCodingCredentialProvider{
		resolvable: map[models.ProviderName][]models.DecryptedCodingCredential{
			models.ProviderAnthropic: {
				{
					ID:       credID,
					OrgID:    orgID,
					UserID:   &userID,
					Provider: models.ProviderAnthropic,
					Status:   models.CodingCredentialStatusActive,
					Config:   models.AnthropicConfig{APIKey: "sk-ant-test-credential"},
				},
			},
		},
	}

	env := NewAgentEnv(AgentEnvDeps{
		CodingCredentials: coding,
		Provider:          &envSandboxProvider{},
		Logger:            zerolog.Nop(),
	})

	cfg := env.resolveProviderConfig(ctx, orgID, &userID, models.ProviderAnthropic)
	require.IsType(t, models.AnthropicConfig{}, cfg, "resolver should return the unified anthropic config")

	env.ShedRateLimited(orgID, &userID, models.ProviderAnthropic)
	require.Equal(t, []uuid.UUID{credID}, coding.rateLimitedIDs,
		"ShedRateLimited should forward the just-picked credential id to the store")

	env.ShedAuthRejected(orgID, &userID, models.ProviderAnthropic)
	require.Equal(t, []uuid.UUID{credID}, coding.authRejectedIDs,
		"ShedAuthRejected should forward the just-picked credential id to the store")
}

func TestAgentEnvShedAfterSubscriptionPickUsesAgentProviderAlias(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	orgID := uuid.New()
	userID := uuid.New()
	credID := uuid.New()

	coding := &envCodingCredentialProvider{
		resolvable: map[models.ProviderName][]models.DecryptedCodingCredential{
			models.ProviderAnthropicSubscription: {
				{
					ID:       credID,
					OrgID:    orgID,
					UserID:   &userID,
					Provider: models.ProviderAnthropicSubscription,
					Status:   models.CodingCredentialStatusActive,
					Config: models.AnthropicSubscriptionConfig{
						AccessToken:  "claude-token",
						RefreshToken: "claude-refresh",
						ExpiresAt:    time.Now().Add(time.Hour),
					},
				},
			},
		},
	}

	env := NewAgentEnv(AgentEnvDeps{
		CodingCredentials: coding,
		Provider:          &envSandboxProvider{},
		Logger:            zerolog.Nop(),
	})

	cfg := env.resolveProviderConfig(ctx, orgID, &userID, models.ProviderAnthropic)
	require.IsType(t, models.AnthropicConfig{}, cfg, "resolver should return the Claude runtime config shape for subscription rows")

	env.ShedRateLimited(orgID, &userID, models.ProviderAnthropic)
	require.Equal(t, []uuid.UUID{credID}, coding.rateLimitedIDs,
		"ShedRateLimited should forward a subscription pick when called with the agent API-key provider")

	env.ShedAuthRejected(orgID, &userID, models.ProviderAnthropic)
	require.Equal(t, []uuid.UUID{credID}, coding.authRejectedIDs,
		"ShedAuthRejected should forward a subscription pick when called with the agent API-key provider")
}

func TestAgentEnvShedCredentialIsSkippedOnNextResolution(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	orgID := uuid.New()
	userID := uuid.New()
	firstID := uuid.New()
	secondID := uuid.New()

	coding := &envCodingCredentialProvider{
		resolvable: map[models.ProviderName][]models.DecryptedCodingCredential{
			models.ProviderAnthropic: {
				{
					ID:       firstID,
					OrgID:    orgID,
					UserID:   &userID,
					Provider: models.ProviderAnthropic,
					Status:   models.CodingCredentialStatusActive,
					Config:   models.AnthropicConfig{APIKey: "sk-ant-first"},
				},
				{
					ID:       secondID,
					OrgID:    orgID,
					UserID:   &userID,
					Provider: models.ProviderAnthropic,
					Status:   models.CodingCredentialStatusActive,
					Config:   models.AnthropicConfig{APIKey: "sk-ant-second"},
				},
			},
		},
	}

	env := NewAgentEnv(AgentEnvDeps{
		CodingCredentials: coding,
		Provider:          &envSandboxProvider{},
		Logger:            zerolog.Nop(),
	})

	first := env.resolveProviderConfig(ctx, orgID, &userID, models.ProviderAnthropic)
	require.Equal(t, "sk-ant-first", first.(models.AnthropicConfig).APIKey, "first resolution should pick the top credential")

	env.ShedRateLimited(orgID, &userID, models.ProviderAnthropic)

	second := env.resolveProviderConfig(ctx, orgID, &userID, models.ProviderAnthropic)
	require.Equal(t, "sk-ant-second", second.(models.AnthropicConfig).APIKey, "second resolution should skip the shed credential")
}

// TestAgentEnvShedNoopWhenNoRecentPick guards the unhappy paths: shedding for
// a (orgID, userID, provider) that never went through pickFromCodingProvider
// must be a silent no-op rather than a spurious shed of the wrong credential.
func TestAgentEnvShedNoopWhenNoRecentPick(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	userID := uuid.New()
	coding := &envCodingCredentialProvider{}

	env := NewAgentEnv(AgentEnvDeps{
		CodingCredentials: coding,
		Provider:          &envSandboxProvider{},
		Logger:            zerolog.Nop(),
	})

	env.ShedRateLimited(orgID, &userID, models.ProviderAnthropic)
	env.ShedAuthRejected(orgID, &userID, models.ProviderGemini)
	require.Empty(t, coding.rateLimitedIDs, "ShedRateLimited without a recorded pick should not call the store")
	require.Empty(t, coding.authRejectedIDs, "ShedAuthRejected without a recorded pick should not call the store")
}

// TestAgentEnvLegacyFallbackSkipsInactiveRows verifies that the legacy
// fallback resolver only picks status='active' rows. Disabled / invalid /
// pending_auth rows in the legacy stores must NOT be returned even when the
// unified resolver has nothing to offer — otherwise a row left around during
// migration cleanup would silently re-enter the runtime path.
func TestAgentEnvLegacyFallbackSkipsInactiveRows(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	orgID := uuid.New()
	userID := uuid.New()

	tests := []struct {
		name     string
		userCred *envUserCredentialProvider
		orgCred  *envCredentialProvider
		wantNil  bool
		wantKey  string
	}{
		{
			name: "disabled personal row is skipped",
			userCred: &envUserCredentialProvider{
				personal: map[models.ProviderName]*models.DecryptedUserCredential{
					models.ProviderAnthropic: {ID: uuid.New(), Status: models.CredentialStatusDisabled, Config: models.AnthropicConfig{APIKey: "personal"}},
				},
			},
			orgCred: &envCredentialProvider{
				creds: map[models.ProviderName]*models.DecryptedCredential{
					models.ProviderAnthropic: {ID: uuid.New(), Status: models.CredentialStatusActive, Config: models.AnthropicConfig{APIKey: "org"}},
				},
			},
			wantKey: "org",
		},
		{
			name: "invalid team row is skipped, falls through to org",
			userCred: &envUserCredentialProvider{
				team: map[models.ProviderName]*models.DecryptedUserCredential{
					models.ProviderAnthropic: {ID: uuid.New(), Status: models.CredentialStatusInvalid, Config: models.AnthropicConfig{APIKey: "team"}},
				},
			},
			orgCred: &envCredentialProvider{
				creds: map[models.ProviderName]*models.DecryptedCredential{
					models.ProviderAnthropic: {ID: uuid.New(), Status: models.CredentialStatusActive, Config: models.AnthropicConfig{APIKey: "org"}},
				},
			},
			wantKey: "org",
		},
		{
			name:     "all inactive returns nil",
			userCred: &envUserCredentialProvider{},
			orgCred: &envCredentialProvider{
				creds: map[models.ProviderName]*models.DecryptedCredential{
					models.ProviderAnthropic: {ID: uuid.New(), Status: models.CredentialStatusDisabled, Config: models.AnthropicConfig{APIKey: "org"}},
				},
			},
			wantNil: true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			env := NewAgentEnv(AgentEnvDeps{
				Credentials:     tt.orgCred,
				UserCredentials: tt.userCred,
				Provider:        &envSandboxProvider{},
				Logger:          zerolog.Nop(),
			})
			cfg := env.resolveProviderConfig(ctx, orgID, &userID, models.ProviderAnthropic)
			if tt.wantNil {
				require.Nil(t, cfg, "resolver should return nil when every legacy row is inactive")
				return
			}
			require.IsType(t, models.AnthropicConfig{}, cfg, "resolver should return the active legacy row's config")
			require.Equal(t, tt.wantKey, cfg.(models.AnthropicConfig).APIKey, "resolver should pick the active legacy row, not an inactive one")
		})
	}
}

func TestAgentEnvUnifiedResolverEmptyDoesNotFallbackToLegacy(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	orgID := uuid.New()
	userID := uuid.New()
	legacyCredID := uuid.New()

	coding := &envCodingCredentialProvider{}
	userCred := &envUserCredentialProvider{
		personal: map[models.ProviderName]*models.DecryptedUserCredential{
			models.ProviderAnthropic: {
				ID:     legacyCredID,
				Status: models.CredentialStatusActive,
				Config: models.AnthropicConfig{APIKey: "legacy-key"},
			},
		},
	}

	env := NewAgentEnv(AgentEnvDeps{
		CodingCredentials: coding,
		UserCredentials:   userCred,
		Provider:          &envSandboxProvider{},
		Logger:            zerolog.Nop(),
	})

	cfg := env.resolveProviderConfig(ctx, orgID, &userID, models.ProviderAnthropic)
	require.Nil(t, cfg, "wired unified credentials should be authoritative even when no active unified row exists")

	env.ShedRateLimited(orgID, &userID, models.ProviderAnthropic)
	require.Empty(t, coding.rateLimitedIDs, "no legacy pick should be recorded when unified resolver handles the lookup")
}

func TestAgentEnvUnifiedListErrorFallsBackToLegacy(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	orgID := uuid.New()
	userID := uuid.New()
	legacyCredID := uuid.New()

	// Unified resolver returns a transient error for the queried provider and
	// its subscription twin. The legacy fallback must serve traffic instead of
	// the resolver short-circuiting as "unified authoritative with no rows".
	coding := &envCodingCredentialProvider{
		errs: map[models.ProviderName]error{
			models.ProviderAnthropic:             errors.New("transient pgx error"),
			models.ProviderAnthropicSubscription: errors.New("transient pgx error"),
		},
	}
	userCred := &envUserCredentialProvider{
		personal: map[models.ProviderName]*models.DecryptedUserCredential{
			models.ProviderAnthropic: {
				ID:     legacyCredID,
				Status: models.CredentialStatusActive,
				Config: models.AnthropicConfig{APIKey: "legacy-key"},
			},
		},
	}

	env := NewAgentEnv(AgentEnvDeps{
		CodingCredentials: coding,
		UserCredentials:   userCred,
		Provider:          &envSandboxProvider{},
		Logger:            zerolog.Nop(),
	})

	cfg := env.resolveProviderConfig(ctx, orgID, &userID, models.ProviderAnthropic)
	require.IsType(t, models.AnthropicConfig{}, cfg, "transient unified-resolver error must yield to legacy fallback")
	require.Equal(t, "legacy-key", cfg.(models.AnthropicConfig).APIKey, "legacy credential should be served when unified ListResolvable errors")
}

func TestAgentEnvLegacyFallbackWhenUnifiedUnwired(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	orgID := uuid.New()
	userID := uuid.New()
	legacyCredID := uuid.New()

	userCred := &envUserCredentialProvider{
		personal: map[models.ProviderName]*models.DecryptedUserCredential{
			models.ProviderAnthropic: {ID: legacyCredID, Status: models.CredentialStatusActive, Config: models.AnthropicConfig{APIKey: "legacy-key"}},
		},
	}

	env := NewAgentEnv(AgentEnvDeps{
		UserCredentials: userCred,
		Provider:        &envSandboxProvider{},
		Logger:          zerolog.Nop(),
	})

	cfg := env.resolveProviderConfig(ctx, orgID, &userID, models.ProviderAnthropic)
	require.IsType(t, models.AnthropicConfig{}, cfg, "legacy fallback should return an AnthropicConfig")
	require.Equal(t, "legacy-key", cfg.(models.AnthropicConfig).APIKey)
}

func TestAgentEnvInjectCodexAuth(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	orgID := uuid.New()
	sandbox := &Sandbox{HomeDir: "/home/test"}

	tests := []struct {
		name            string
		codexAuth       CodexAuthProvider
		sandboxProvider *envSandboxProvider
		wantInjected    bool
		wantErr         string
		assertWrites    func(t *testing.T, provider *envSandboxProvider)
	}{
		{
			name:            "no codex auth provider returns not injected",
			sandboxProvider: &envSandboxProvider{},
			wantInjected:    false,
		},
		{
			name:            "token lookup error is returned",
			codexAuth:       envCodexAuthProvider{err: errors.New("lookup failed")},
			sandboxProvider: &envSandboxProvider{},
			wantErr:         "get codex auth token",
		},
		{
			name:            "missing oauth token is not an error",
			codexAuth:       envCodexAuthProvider{},
			sandboxProvider: &envSandboxProvider{},
			wantInjected:    false,
		},
		{
			name:            "mkdir exec failure is returned",
			codexAuth:       envCodexAuthProvider{token: &models.OpenAIChatGPTConfig{AccessToken: "access", IDToken: "id"}},
			sandboxProvider: &envSandboxProvider{execErr: errors.New("exec failed")},
			wantErr:         "create .codex dir",
		},
		{
			name:            "mkdir non zero exit is returned",
			codexAuth:       envCodexAuthProvider{token: &models.OpenAIChatGPTConfig{AccessToken: "access", IDToken: "id"}},
			sandboxProvider: &envSandboxProvider{execExitCode: 23},
			wantErr:         "mkdir exited with code 23",
		},
		{
			name:      "write auth json error is returned",
			codexAuth: envCodexAuthProvider{token: &models.OpenAIChatGPTConfig{AccessToken: "access", IDToken: "id"}},
			sandboxProvider: &envSandboxProvider{writeErrByPath: map[string]error{
				"/home/test/.codex/auth.json": errors.New("disk full"),
			}},
			wantErr: "write auth.json",
		},
		{
			name:      "write config error is returned",
			codexAuth: envCodexAuthProvider{token: &models.OpenAIChatGPTConfig{AccessToken: "access", IDToken: "id"}},
			sandboxProvider: &envSandboxProvider{writeErrByPath: map[string]error{
				"/home/test/.codex/config.toml": errors.New("disk full"),
			}},
			wantErr: "write config.toml",
		},
		{
			name:            "successful injection writes auth and config files",
			codexAuth:       envCodexAuthProvider{token: &models.OpenAIChatGPTConfig{AccessToken: "access", RefreshToken: "refresh", IDToken: "id"}},
			sandboxProvider: &envSandboxProvider{},
			wantInjected:    true,
			assertWrites: func(t *testing.T, provider *envSandboxProvider) {
				t.Helper()

				authJSON := provider.writes["/home/test/.codex/auth.json"]
				require.NotEmpty(t, authJSON, "InjectCodexAuth should write auth.json on success")
				require.NotEmpty(t, provider.writes["/home/test/.codex/config.toml"], "InjectCodexAuth should write config.toml on success")

				var payload map[string]any
				require.NoError(t, json.Unmarshal(authJSON, &payload), "InjectCodexAuth should write valid auth.json")
				tokens, ok := payload["tokens"].(map[string]any)
				require.True(t, ok, "InjectCodexAuth should encode tokens in auth.json")
				require.Equal(t, "access", tokens["access_token"], "InjectCodexAuth should write the access token")
				require.Equal(t, "", tokens["refresh_token"], "InjectCodexAuth should omit the refresh token from auth.json")
				require.Equal(t, "id", tokens["id_token"], "InjectCodexAuth should write the ID token")
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			env := NewAgentEnv(AgentEnvDeps{
				CodexAuth: tt.codexAuth,
				Provider:  tt.sandboxProvider,
				Logger:    zerolog.Nop(),
			})

			injected, err := env.InjectCodexAuth(ctx, orgID, sandbox)
			if tt.wantErr != "" {
				require.Error(t, err, "InjectCodexAuth should return an error for %s", tt.name)
				require.Contains(t, err.Error(), tt.wantErr, "InjectCodexAuth should describe the failure for %s", tt.name)
				return
			}

			require.NoError(t, err, "InjectCodexAuth should succeed for %s", tt.name)
			require.Equal(t, tt.wantInjected, injected, "InjectCodexAuth should report the expected injected flag for %s", tt.name)
			if tt.assertWrites != nil {
				tt.assertWrites(t, tt.sandboxProvider)
			}
		})
	}
}

func TestAgentEnvInjectClaudeCodeAuthRequiresSandboxProvider(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	orgID := uuid.New()
	credID := uuid.New()
	sandbox := &Sandbox{HomeDir: "/home/test"}
	coding := &envCodingCredentialProvider{
		resolvable: map[models.ProviderName][]models.DecryptedCodingCredential{
			models.ProviderAnthropicSubscription: {
				{
					ID:       credID,
					OrgID:    orgID,
					Provider: models.ProviderAnthropicSubscription,
					Status:   models.CodingCredentialStatusActive,
					Config: models.AnthropicSubscriptionConfig{
						AccessToken:  "claude-access",
						RefreshToken: "claude-refresh",
						ExpiresAt:    time.Now().Add(time.Hour),
					},
				},
			},
		},
	}
	env := NewAgentEnv(AgentEnvDeps{
		CodingCredentials: coding,
		Logger:            zerolog.Nop(),
	})

	injected, err := env.InjectClaudeCodeAuth(ctx, orgID, sandbox)

	require.False(t, injected, "Claude auth injection should not report success when sandbox provider is missing")
	require.Error(t, err, "Claude auth injection should return a configuration error instead of panicking")
	require.Contains(t, err.Error(), "sandbox provider", "Claude auth injection error should identify the missing dependency")
}

func TestAgentEnvInjectClaudeCodeAuthWithEnvSetsPermissionModeFromModelAndVersion(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	orgID := uuid.New()

	tests := []struct {
		name     string
		model    string
		expected string
	}{
		{
			name:     "supported sonnet model uses bypass",
			model:    models.ClaudeCodeModelSonnet46,
			expected: ClaudeCodePermissionModeBypassPermissions,
		},
		{
			name:     "unsupported haiku model still uses bypass",
			model:    models.ClaudeCodeModelHaiku45,
			expected: ClaudeCodePermissionModeBypassPermissions,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			credID := uuid.New()
			sandbox := &Sandbox{HomeDir: "/home/test"}
			provider := &envSandboxProvider{
				execStdoutByCmd: map[string]string{"claude --version": "2.1.139\n"},
			}
			coding := &envCodingCredentialProvider{
				resolvable: map[models.ProviderName][]models.DecryptedCodingCredential{
					models.ProviderAnthropicSubscription: {
						{
							ID:       credID,
							OrgID:    orgID,
							Provider: models.ProviderAnthropicSubscription,
							Status:   models.CodingCredentialStatusActive,
							Config: models.AnthropicSubscriptionConfig{
								AccessToken:  "claude-access",
								RefreshToken: "claude-refresh",
								ExpiresAt:    time.Now().Add(time.Hour),
								AccountType:  "claude_max",
							},
						},
					},
				},
			}
			env := NewAgentEnv(AgentEnvDeps{
				CodingCredentials: coding,
				Provider:          provider,
				Logger:            zerolog.Nop(),
			})

			injected, err := env.InjectClaudeCodeAuthWithEnv(ctx, orgID, sandbox, map[string]string{
				models.ModelEnvVarForAgentType(models.AgentTypeClaudeCode): tt.model,
			})

			require.NoError(t, err, "Claude auth injection should succeed")
			require.True(t, injected, "Claude auth injection should write the subscription credentials")
			require.Equal(t, tt.expected, sandbox.Metadata[SandboxMetadataClaudeCodePermissionMode], "permission mode should reflect model and CLI compatibility")
			require.Equal(t, "2.1.139", sandbox.Metadata[SandboxMetadataClaudeCodeVersion], "CLI version should be cached after detection")
		})
	}
}

func TestAgentEnvPrepareClaudeCodeAPIKeyFallbackRequiresSandboxProvider(t *testing.T) {
	t.Parallel()

	env := NewAgentEnv(AgentEnvDeps{Logger: zerolog.Nop()})

	err := env.PrepareClaudeCodeAPIKeyFallback(context.Background(), &Sandbox{HomeDir: "/home/test"}, map[string]string{
		"ANTHROPIC_API_KEY": "sk-ant-test",
	})

	require.Error(t, err, "Claude API-key fallback preparation should return a configuration error instead of panicking")
	require.Contains(t, err.Error(), "sandbox provider", "Claude fallback error should identify the missing dependency")
}

// TestAgentEnvInjectCodexAuth_ErrorClassification verifies that
// InjectCodexAuth tags genuine auth failures with ErrCodexAuthInvalid while
// leaving sandbox/transport errors un-tagged. The orchestrator branches on
// this sentinel to decide whether to surface a "re-authenticate with ChatGPT"
// CTA or a generic retry CTA — misclassification (e.g. labeling a Docker
// "no such container" as auth-expired) sends users to redo OAuth when their
// credential is fine.
func TestAgentEnvInjectCodexAuth_ErrorClassification(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	orgID := uuid.New()
	sandbox := &Sandbox{HomeDir: "/home/test"}

	tests := []struct {
		name          string
		codexAuth     CodexAuthProvider
		provider      *envSandboxProvider
		codingCreds   *envCodingCredentialProvider
		wantAuthError bool
	}{
		{
			name:          "GetValidToken failure is auth-shaped",
			codexAuth:     envCodexAuthProvider{err: errors.New("refresh token revoked"), authInvalid: true},
			provider:      &envSandboxProvider{},
			wantAuthError: true,
		},
		{
			name:          "GetValidToken infrastructure failure is NOT auth-shaped",
			codexAuth:     envCodexAuthProvider{err: errors.New("db connection refused")},
			provider:      &envSandboxProvider{},
			wantAuthError: false,
		},
		{
			name:          "expired unified subscription refresh auth failure is auth-shaped",
			codexAuth:     &envCodexAuthProvider{refreshErr: errors.New("refresh token revoked"), authInvalid: true},
			provider:      &envSandboxProvider{},
			codingCreds:   envExpiredCodexSubscriptionCredential(orgID),
			wantAuthError: true,
		},
		{
			name:          "expired unified subscription refresh infrastructure failure is NOT auth-shaped",
			codexAuth:     &envCodexAuthProvider{refreshErr: errors.New("oauth server 500")},
			provider:      &envSandboxProvider{},
			codingCreds:   envExpiredCodexSubscriptionCredential(orgID),
			wantAuthError: false,
		},
		{
			name:          "Docker exec failure is NOT auth-shaped",
			codexAuth:     envCodexAuthProvider{token: &models.OpenAIChatGPTConfig{AccessToken: "access", IDToken: "id"}},
			provider:      &envSandboxProvider{execErr: errors.New("Error response from daemon: No such container: abc123")},
			wantAuthError: false,
		},
		{
			name:          "mkdir non-zero exit is NOT auth-shaped",
			codexAuth:     envCodexAuthProvider{token: &models.OpenAIChatGPTConfig{AccessToken: "access", IDToken: "id"}},
			provider:      &envSandboxProvider{execExitCode: 1},
			wantAuthError: false,
		},
		{
			name:      "WriteFile failure is NOT auth-shaped",
			codexAuth: envCodexAuthProvider{token: &models.OpenAIChatGPTConfig{AccessToken: "access", IDToken: "id"}},
			provider: &envSandboxProvider{writeErrByPath: map[string]error{
				"/home/test/.codex/auth.json": errors.New("disk full"),
			}},
			wantAuthError: false,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			deps := AgentEnvDeps{
				CodexAuth: tt.codexAuth,
				Provider:  tt.provider,
				Logger:    zerolog.Nop(),
			}
			if tt.codingCreds != nil {
				deps.CodingCredentials = tt.codingCreds
			}
			env := NewAgentEnv(deps)

			_, err := env.InjectCodexAuth(ctx, orgID, sandbox)
			require.Error(t, err)
			require.Equal(t, tt.wantAuthError, errors.Is(err, ErrCodexAuthInvalid),
				"errors.Is(err, ErrCodexAuthInvalid) mismatch for %s — got err=%v", tt.name, err)
		})
	}
}

func TestAgentEnvInjectCodexAuthForUser_RefreshesUnifiedSubscriptionByID(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	orgID := uuid.New()
	userID := uuid.New()
	credID := uuid.New()
	sandbox := &Sandbox{HomeDir: "/home/test"}
	provider := &envSandboxProvider{}
	codexAuth := &envCodexAuthProvider{
		refreshToken: &models.OpenAIChatGPTConfig{
			AccessToken:  "fresh-access",
			RefreshToken: "fresh-refresh",
			IDToken:      "fresh-id",
			ExpiresAt:    time.Now().Add(time.Hour),
		},
	}
	env := NewAgentEnv(AgentEnvDeps{
		CodingCredentials: &envCodingCredentialProvider{
			resolvable: map[models.ProviderName][]models.DecryptedCodingCredential{
				models.ProviderOpenAISubscription: {
					{
						ID:       credID,
						OrgID:    orgID,
						UserID:   &userID,
						Provider: models.ProviderOpenAISubscription,
						Status:   models.CodingCredentialStatusActive,
						Config: models.OpenAISubscriptionConfig{
							AccessToken:  "stale-access",
							RefreshToken: "stale-refresh",
							IDToken:      "stale-id",
							ExpiresAt:    time.Now().Add(-time.Minute),
						},
					},
				},
			},
		},
		CodexAuth: codexAuth,
		Provider:  provider,
		Logger:    zerolog.Nop(),
	})

	injected, err := env.InjectCodexAuthForUser(ctx, orgID, &userID, sandbox)

	require.NoError(t, err, "InjectCodexAuthForUser should refresh an expired unified subscription before writing auth.json")
	require.True(t, injected, "InjectCodexAuthForUser should inject the refreshed subscription")
	require.Equal(t, []uuid.UUID{credID}, codexAuth.refreshIDs, "InjectCodexAuthForUser should refresh the selected credential id")

	// The refresher must receive the picked credential's actual scope so
	// the underlying coding_credentials lookup matches on (org_id, user_id).
	// Passing org scope for a personal credential would silently miss the
	// row and surface as "credential not found" once the access token
	// expires (~8h after issuance).
	require.Len(t, codexAuth.refreshScopes, 1, "refresh should be called exactly once")
	require.Equal(t, orgID, codexAuth.refreshScopes[0].OrgID, "scope should carry the request org id")
	require.NotNil(t, codexAuth.refreshScopes[0].UserID, "personal subscription must refresh under personal scope")
	require.Equal(t, userID, *codexAuth.refreshScopes[0].UserID, "scope must carry the picked credential's user id")

	var payload map[string]any
	require.NoError(t, json.Unmarshal(provider.writes["/home/test/.codex/auth.json"], &payload), "auth.json should be valid JSON")
	tokens, ok := payload["tokens"].(map[string]any)
	require.True(t, ok, "auth.json should contain a tokens object")
	require.Equal(t, "fresh-access", tokens["access_token"], "auth.json should use the refreshed access token")
	require.Equal(t, "fresh-id", tokens["id_token"], "auth.json should use the refreshed ID token")
}
