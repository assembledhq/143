package providers

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/repoconfig"
	"github.com/assembledhq/143/internal/services/agent"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func TestDockerNPMToolchainSelection(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, selector     string
		mismatch, terminal bool
		symlink            bool
		resolves           int
	}{
		{name: "linked selector rejected", selector: "v22.23.4", symlink: true, terminal: true},
		{name: "supported node22 compile cache miss", selector: "v22.23.4", resolves: 1},
		{name: "unsupported before credential resolution", selector: "v18.0.0", terminal: true},
		{name: "immutable runtime mismatch", selector: "v22.23.4", mismatch: true, resolves: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			root, err := filepath.EvalSymlinks(t.TempDir())
			require.NoError(t, err, "fixture root should resolve")
			workspace := filepath.Join(root, "workspace")
			require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".git"), 0700), "workspace should exist")
			files := map[string]string{"package.json": `{"name":"fixture","version":"1.0.0","engines":{"node":"^22","npm":"^10"}}`, "package-lock.json": `{"name":"fixture","version":"1.0.0","lockfileVersion":3,"packages":{"":{"name":"fixture","version":"1.0.0"}}}`, ".nvmrc": tt.selector}
			for name, content := range files {
				require.NoError(t, os.WriteFile(filepath.Join(workspace, name), []byte(content), 0600), "fixture input should be written")
			}
			if tt.symlink {
				require.NoError(t, os.Rename(filepath.Join(workspace, ".nvmrc"), filepath.Join(workspace, "pin")), "move pin to symlink target")
				require.NoError(t, os.Symlink("pin", filepath.Join(workspace, ".nvmrc")), "link selector fixture")
			}
			fixture := newNPMDockerFixture(t, root, "", "443")
			fixture.versionMismatch = tt.mismatch
			provider := NewDockerProvider(fixture, zerolog.Nop(), WithRuntime("runc"))
			sb := &agent.Sandbox{ID: "main", WorkDir: workspace}
			resolves := 0
			resolve := func(context.Context) ([]models.ResolvedPackageRegistryCredential, error) { resolves++; return nil, nil }
			fingerprint, err := provider.PrepareNPMDependencies(context.Background(), sb, []repoconfig.NPMSetup{{Directory: "."}}, "revision", resolve)
			require.Equal(t, tt.resolves, resolves, "unsupported selectors must fail before credentials resolve")
			require.Equal(t, tt.terminal, agent.IsTerminalNPMSetupError(err), "only repository declaration failures should be terminal")
			if tt.terminal || tt.mismatch {
				require.Error(t, err, "invalid toolchain should not install")
				return
			}
			require.NoError(t, err, "supported toolchain should install")
			require.Equal(t, "/opt/143/node/22.23.4/bin:"+agent.DefaultSandboxPATH, sb.Env["PATH"], "bootstrap should use selected toolchain")
			sb.Env = nil
			cached, err := provider.PrepareNPMDependencies(context.Background(), sb, []repoconfig.NPMSetup{{Directory: "."}}, "revision", resolve)
			require.NoError(t, err, "unchanged inputs should reuse ready receipt")
			require.Equal(t, fingerprint, cached, "receipt should remain stable")
			require.Equal(t, 1, resolves, "cache hit must not resolve credentials")
			require.NoError(t, os.WriteFile(filepath.Join(workspace, ".nvmrc"), []byte("22.23.4\n"), 0600), "equivalent selector change should be written")
			changed, err := provider.PrepareNPMDependencies(context.Background(), sb, []repoconfig.NPMSetup{{Directory: "."}}, "revision", resolve)
			require.NoError(t, err, "changed selector should reinstall safely")
			require.NotEqual(t, fingerprint, changed, "selector bytes must invalidate receipt")
			require.Equal(t, 2, resolves, "changed selector must not reuse receipt")
		})
	}
}
