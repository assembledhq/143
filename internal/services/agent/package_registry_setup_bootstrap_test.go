package agent

import (
	"context"
	"io"
	"maps"
	"path"
	"testing"

	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/nodetoolchain"
	"github.com/assembledhq/143/internal/repoconfig"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

type packageBootstrapProvider struct {
	SandboxProvider
	toolchain         nodetoolchain.Toolchain
	events            []string
	bootstrapEnvs     []map[string]string
	bootstrapCommands []string
}

func (p *packageBootstrapProvider) ReadFile(context.Context, *Sandbox, string) ([]byte, error) {
	return []byte(`{"setup":{"npm":[{"directory":"."}]},"bootstrap":{"commands":["npm run build"]}}`), nil
}

func (p *packageBootstrapProvider) PrepareNPMDependencies(ctx context.Context, sandbox *Sandbox, _ []repoconfig.NPMSetup, _ string, resolve func(context.Context) ([]models.ResolvedPackageRegistryCredential, error)) (string, error) {
	if _, err := resolve(ctx); err != nil {
		return "", err
	}
	// Model the dependency provider's contract: a successful setup selects the
	// runtime on the live sandbox before repository bootstrap starts.
	env := maps.Clone(sandbox.Env)
	env["PATH"] = p.toolchain.BinDir() + ":" + env["PATH"]
	sandbox.Env = env
	p.events = append(p.events, "npm setup")
	return "synthetic-package-fingerprint", nil
}

func (p *packageBootstrapProvider) Exec(_ context.Context, sandbox *Sandbox, command string, _, _ io.Writer) (int, error) {
	p.events = append(p.events, "bootstrap")
	p.bootstrapEnvs = append(p.bootstrapEnvs, maps.Clone(sandbox.Env))
	p.bootstrapCommands = append(p.bootstrapCommands, command)
	return 0, nil
}

func TestPackageRegistrySetupSelectedRuntimeReachesRepositoryBootstrap(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		toolchain nodetoolchain.Toolchain
	}{
		{name: "repository Node 22", toolchain: nodetoolchain.Toolchain{ID: "node22-npm10", NodeVersion: "22.23.4", NPMVersion: "10.9.9"}},
		{name: "repository Node 22 npm 11", toolchain: nodetoolchain.Toolchain{ID: "node22-npm11", NodeVersion: "22.23.4", NPMVersion: "11.21.1", Directory: "22.23.4-npm11.21.1"}},
		{name: "default Node 24", toolchain: nodetoolchain.Toolchain{ID: "node24-npm11", NodeVersion: "24.22.2", NPMVersion: "11.19.1"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			orgID, repoID := uuid.New(), uuid.New()
			session := &models.Session{ID: uuid.New(), OrgID: orgID, RepositoryID: &repoID}
			original := map[string]string{"PATH": "/home/sandbox/.local/bin:" + DefaultSandboxPATH, "LANG": "C.UTF-8"}
			sandbox := &Sandbox{ID: "bootstrap-runtime", OrgID: orgID.String(), WorkDir: "/workspace/project", Env: maps.Clone(original)}
			provider := &packageBootstrapProvider{toolchain: tt.toolchain}
			resolver := &registrySetupResolver{orgID: orgID, repoID: repoID}
			orchestrator := &Orchestrator{provider: provider, packageCredentials: resolver}
			err := orchestrator.prepareSandboxRepositoryForSession(context.Background(), provider, session, sandbox, sandbox.WorkDir, repositoryPreparationFull, zerolog.Nop())
			require.NoError(t, err, "live session preparation should finish package setup and repository bootstrap")
			require.Equal(t, []string{"npm setup", "bootstrap"}, provider.events, "bootstrap must follow the selected runtime setup")
			expectedEnv := map[string]string{"PATH": tt.toolchain.BinDir() + ":" + original["PATH"], "LANG": "C.UTF-8"}
			require.Equal(t, []map[string]string{expectedEnv}, provider.bootstrapEnvs, "bootstrap must receive the selected runtime before existing tool paths and preserve the sandbox environment")
			require.Equal(t, []string{"cd '" + path.Clean(sandbox.WorkDir) + "' && npm run build"}, provider.bootstrapCommands, "the live repository bootstrap command should execute in its workspace")
			require.Equal(t, expectedEnv, sandbox.Env, "the selected runtime must remain installed on the live sandbox after bootstrap")
			require.Equal(t, 1, resolver.resolveCalls, "the live setup should resolve scoped package credentials once")
		})
	}
}
