package preview

import (
	"context"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/models"
)

type stubPreviewSecretBundles struct {
	env map[string]string
	err error
}

func (s stubPreviewSecretBundles) GetEnv(_ context.Context, _ uuid.UUID, _ string) (map[string]string, error) {
	if s.err != nil {
		return nil, s.err
	}
	return s.env, nil
}

func TestResolveCredentialRuntimeEnvScopesAllowlistedSecrets(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	manager := NewManager(ManagerConfig{
		PreviewSecretBundles: stubPreviewSecretBundles{env: map[string]string{
			"DATABASE_URL":      "postgres://preview",
			"STRIPE_SECRET_KEY": "sk_test",
			"UNLISTED":          "must-not-leak",
		}},
	})
	cfg := &models.PreviewConfig{
		Primary: "web",
		Services: map[string]models.ServiceConfig{
			"web":    {Command: []string{"npm", "run", "dev"}, Port: 3000, Ready: models.ReadinessProbe{HTTPPath: "/"}},
			"server": {Command: []string{"go", "run", "./cmd/server"}, Port: 8080, Ready: models.ReadinessProbe{HTTPPath: "/healthz"}},
		},
		Credentials: models.CredentialConfig{
			Mode:          "managed_env",
			CredentialSet: "repo-staging",
			Env:           []string{"DATABASE_URL", "STRIPE_SECRET_KEY"},
			InjectInto:    []string{"server"},
		},
		Network: models.NetworkConfig{Mode: "managed"},
	}

	got, err := manager.resolveCredentialRuntimeEnv(context.Background(), orgID, cfg)

	require.NoError(t, err, "resolveCredentialRuntimeEnv should resolve an existing bundle")
	require.Equal(t, map[string]map[string]string{
		"server": {
			"DATABASE_URL":      "postgres://preview",
			"STRIPE_SECRET_KEY": "sk_test",
		},
	}, got, "resolveCredentialRuntimeEnv should inject only allowlisted vars into target services")
	require.NotContains(t, got["server"], "UNLISTED", "resolveCredentialRuntimeEnv must not expose unallowlisted bundle values")
	require.Nil(t, got["web"], "resolveCredentialRuntimeEnv must not inject secrets into services outside inject_into")
}

func TestResolveCredentialRuntimeEnvRejectsMissingSecret(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	manager := NewManager(ManagerConfig{
		PreviewSecretBundles: stubPreviewSecretBundles{env: map[string]string{"DATABASE_URL": "postgres://preview"}},
	})
	cfg := &models.PreviewConfig{
		Primary: "server",
		Services: map[string]models.ServiceConfig{
			"server": {Command: []string{"go", "run", "./cmd/server"}, Port: 8080, Ready: models.ReadinessProbe{HTTPPath: "/healthz"}},
		},
		Credentials: models.CredentialConfig{
			Mode:          "managed_env",
			CredentialSet: "repo-staging",
			Env:           []string{"DATABASE_URL", "STRIPE_SECRET_KEY"},
			InjectInto:    []string{"server"},
		},
		Network: models.NetworkConfig{Mode: "managed"},
	}

	got, err := manager.resolveCredentialRuntimeEnv(context.Background(), orgID, cfg)

	require.Error(t, err, "resolveCredentialRuntimeEnv should fail when a requested secret is missing")
	require.Nil(t, got, "resolveCredentialRuntimeEnv should not return a partial secret map on failure")
	require.Contains(t, err.Error(), "STRIPE_SECRET_KEY", "error should identify the missing env var")
}
