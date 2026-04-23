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
}

func (m *envCredentialProvider) Get(_ context.Context, _ uuid.UUID, provider models.ProviderName) (*models.DecryptedCredential, error) {
	if err, ok := m.errs[provider]; ok {
		return nil, err
	}
	if cred, ok := m.creds[provider]; ok {
		return cred, nil
	}
	return nil, nil
}

func (m *envCredentialProvider) ListByProvider(_ context.Context, _ uuid.UUID, provider models.ProviderName) ([]models.DecryptedCredential, error) {
	if err, ok := m.listErrs[provider]; ok {
		return nil, err
	}
	if creds, ok := m.listCreds[provider]; ok {
		return creds, nil
	}
	if cred, ok := m.creds[provider]; ok && cred != nil {
		return []models.DecryptedCredential{*cred}, nil
	}
	return nil, nil
}

type envUserCredentialProvider struct {
	personal map[models.ProviderName]*models.DecryptedUserCredential
	team     map[models.ProviderName]*models.DecryptedUserCredential
}

func (m *envUserCredentialProvider) GetForUser(_ context.Context, _, _ uuid.UUID, provider models.ProviderName) (*models.DecryptedUserCredential, error) {
	if cred, ok := m.personal[provider]; ok {
		return cred, nil
	}
	return nil, nil
}

func (m *envUserCredentialProvider) GetTeamDefault(_ context.Context, _ uuid.UUID, provider models.ProviderName) (*models.DecryptedUserCredential, error) {
	if cred, ok := m.team[provider]; ok {
		return cred, nil
	}
	return nil, nil
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
	token *models.OpenAIChatGPTConfig
	err   error
}

func (m envCodexAuthProvider) GetValidToken(_ context.Context, _ uuid.UUID) (*models.OpenAIChatGPTConfig, error) {
	if m.err != nil {
		return nil, m.err
	}
	return m.token, nil
}

type envSandboxProvider struct {
	execExitCode   int
	execErr        error
	writeErrByPath map[string]error
	writes         map[string][]byte
	commands       []string
}

func (m *envSandboxProvider) Name() string { return "env-sandbox" }

func (m *envSandboxProvider) Create(_ context.Context, _ SandboxConfig) (*Sandbox, error) {
	return &Sandbox{ID: "unused", HomeDir: "/home/test", WorkDir: "/workspace"}, nil
}

func (m *envSandboxProvider) CloneRepo(_ context.Context, _ *Sandbox, _, _, _ string) error {
	return nil
}

func (m *envSandboxProvider) Exec(_ context.Context, _ *Sandbox, cmd string, _, stderr io.Writer) (int, error) {
	m.commands = append(m.commands, cmd)
	if m.execErr != nil {
		return 0, m.execErr
	}
	if m.execExitCode != 0 && stderr != nil {
		_, _ = io.WriteString(stderr, "mkdir failed")
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
				},
			},
			expected: map[string]string{
				"ANTHROPIC_API_KEY":   "sk-ant",
				"ANTHROPIC_BASE_URL":  "https://anthropic.example",
				"SENTRY_AUTH_TOKEN":   "sentry-token",
				"SENTRY_ORG_SLUG":     "assembled",
				"LINEAR_ACCESS_TOKEN": "linear-token",
				"NOTION_ACCESS_TOKEN": "notion-token",
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

		cfg := env.resolveOrgProviderConfig(ctx, orgID, models.ProviderAnthropic)
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

		cfg := env.resolveOrgProviderConfig(ctx, orgID, models.ProviderOpenAI)
		require.IsType(t, models.OpenAIConfig{}, cfg, "resolveOrgProviderConfig should fall back to Get when list lookup fails")
		require.Equal(t, "sk-openai-fallback", cfg.(models.OpenAIConfig).APIKey, "resolveOrgProviderConfig should use the fallback org API key")
	})

	t.Run("filters incompatible coding provider configs", func(t *testing.T) {
		t.Parallel()

		require.Nil(t, compatibleCodingProviderConfig(models.ProviderAnthropic, models.AnthropicConfig{Subscription: &models.AnthropicSubscription{AccessToken: "sub", RefreshToken: "refresh"}}), "compatibleCodingProviderConfig should reject Anthropic subscriptions for API-key env injection")
		require.NotNil(t, compatibleCodingProviderConfig(models.ProviderAnthropic, models.AnthropicConfig{APIKey: "sk-ant"}), "compatibleCodingProviderConfig should accept Anthropic API keys")
		require.NotNil(t, compatibleCodingProviderConfig(models.ProviderOpenAI, models.OpenAIConfig{APIKey: "sk-openai"}), "compatibleCodingProviderConfig should accept OpenAI API keys")
		require.NotNil(t, compatibleCodingProviderConfig(models.ProviderGemini, models.GeminiConfig{APIKey: "gem-key"}), "compatibleCodingProviderConfig should accept Gemini API keys")
		require.NotNil(t, compatibleCodingProviderConfig(models.ProviderOpenRouter, models.OpenRouterConfig{APIKey: "sk-or"}), "compatibleCodingProviderConfig should accept OpenRouter API keys")
		require.NotNil(t, compatibleCodingProviderConfig(models.ProviderAmp, models.AmpConfig{APIKey: "amp-key"}), "compatibleCodingProviderConfig should accept Amp API keys")
		require.NotNil(t, compatibleCodingProviderConfig(models.ProviderPi, models.PiConfig{APIKey: "pi-key"}), "compatibleCodingProviderConfig should accept Pi API keys")
		require.Nil(t, compatibleCodingProviderConfig(models.ProviderOpenRouter, models.OpenRouterConfig{}), "compatibleCodingProviderConfig should reject empty OpenRouter configs")
		require.Nil(t, compatibleCodingProviderConfig(models.ProviderName("unknown"), models.OpenAIConfig{APIKey: "sk"}), "compatibleCodingProviderConfig should reject unknown providers")
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
