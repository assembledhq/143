package providers

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/repoconfig"
	"github.com/assembledhq/143/internal/services/agent"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func runSetupProbeFixture(t *testing.T, root string, control map[string]any) (npmSetupManifest, error) {
	t.Helper()
	python, err := exec.LookPath("python3")
	require.NoError(t, err, "modern Python is required for setup probe tests")
	control["work_dir"] = root
	control["revision"] = "synthetic-revision"
	control["image"] = "synthetic-image"
	if control["targets"] == nil {
		control["targets"] = []repoconfig.NPMSetup{{Directory: "."}}
	}
	raw, err := json.Marshal(control)
	require.NoError(t, err, "probe fixture control should serialize")
	script := strings.ReplaceAll(npmSetupProbePython, "/tmp/143-npm-receipts", filepath.Join(filepath.Dir(root), "managed-receipts"))
	cmd := exec.Command(python, "-I", "-S", "-c", script, string(raw))
	out, err := cmd.Output()
	if err != nil {
		return npmSetupManifest{}, err
	}
	var result npmSetupManifest
	err = json.Unmarshal(out, &result)
	return result, err
}

func TestNPMSetupProbeRejectsUnsafeManifests(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, lock                      string
		linkedManifest, linkedDirectory bool
	}{
		{name: "old lock format", lock: `{"lockfileVersion":1}`},
		{name: "malformed lock", lock: `{`},
		{name: "git dependency", lock: `{"lockfileVersion":3,"packages":{"":{"name":"fixture"},"node_modules/dep":{"resolved":"git+ssh://example/dep"}}}`},
		{name: "escaping workspace", lock: `{"lockfileVersion":3,"packages":{"node_modules/dep":{"link":true,"resolved":"../other"}}}`},
		{name: "linked manifest", lock: `{"lockfileVersion":3,"packages":{}}`, linkedManifest: true},
		{name: "linked install directory", lock: `{"lockfileVersion":3,"packages":{}}`, linkedDirectory: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			parent, err := filepath.EvalSymlinks(t.TempDir())
			require.NoError(t, err, "fixture paths should be canonical")
			root := filepath.Join(parent, "repo")
			require.NoError(t, os.MkdirAll(filepath.Join(root, ".git"), 0o700), "fixture metadata should exist")
			require.NoError(t, os.WriteFile(filepath.Join(root, "package.json"), []byte(`{"name":"fixture"}`), 0o600), "manifest should exist")
			require.NoError(t, os.WriteFile(filepath.Join(root, "package-lock.json"), []byte(tt.lock), 0o600), "lock should exist")
			control := map[string]any{}
			if tt.linkedManifest {
				outside := filepath.Join(parent, "outside.json")
				require.NoError(t, os.WriteFile(outside, []byte(`{}`), 0o600), "link target should exist")
				require.NoError(t, os.Remove(filepath.Join(root, "package.json")), "fixture should replace manifest with link")
				require.NoError(t, os.Symlink(outside, filepath.Join(root, "package.json")), "manifest should be linked")
			}
			if tt.linkedDirectory {
				require.NoError(t, os.Symlink(root, filepath.Join(root, "alias")), "install directory should be linked")
				control["targets"] = []repoconfig.NPMSetup{{Directory: "alias"}}
			}
			_, err = runSetupProbeFixture(t, root, control)
			require.Error(t, err, "unsafe manifests and install aliases must fail before credential resolution")
		})
	}
}

func TestNPMSetupProbeAcceptsLockBackedLocalDirectories(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		targets []repoconfig.NPMSetup
	}{
		{name: "root before nested", targets: []repoconfig.NPMSetup{{Directory: "."}, {Directory: "packages/ui"}}},
		{name: "nested before root", targets: []repoconfig.NPMSetup{{Directory: "packages/ui"}, {Directory: "."}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			root, err := filepath.EvalSymlinks(t.TempDir())
			require.NoError(t, err, "fixture paths should be canonical")
			manifests := map[string]string{
				"package.json":                  `{"name":"app","version":"1.0.0","dependencies":{"example":"file:vendor/example"}}`,
				"package-lock.json":             `{"lockfileVersion":3,"packages":{"":{"name":"app","version":"1.0.0","dependencies":{"example":"file:vendor/example"}},"node_modules/example":{"link":true,"resolved":"vendor/example"},"vendor/example":{"version":"1.0.0"}}}`,
				"packages/ui/package.json":      `{"name":"ui","version":"1.0.0","devDependencies":{"example":"file:../../vendor/example"}}`,
				"packages/ui/package-lock.json": `{"lockfileVersion":3,"packages":{"":{"name":"ui","version":"1.0.0","devDependencies":{"example":"file:../../vendor/example"}},"node_modules/example":{"link":true,"resolved":"../../vendor/example"},"../../vendor/example":{"version":"1.0.0"}}}`,
				"vendor/example/package.json":   `{"name":"example","version":"1.0.0","main":"index.js"}`,
			}
			for name, content := range manifests {
				writeNPMProbeFixtureFile(t, root, name, content)
			}
			writeNPMProbeFixtureFile(t, root, "vendor/example/index.js", "module.exports = 'fixture';")

			result, err := runSetupProbeFixture(t, root, map[string]any{"targets": tt.targets})
			require.NoError(t, err, "install targets should accept the same in-repository vendored package")
			require.Equal(t, manifests, result.Files, "only manifests should cross into the credential-bearing installer")
		})
	}
}

func TestNPMSetupProbeRejectsUnsafeLocalDirectories(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name            string
		spec            string
		lockEntry       string
		linkedDirectory bool
		nestedOnly      bool
	}{
		{name: "outside repository", spec: "file:../../../outside", lockEntry: `,"node_modules/example":{"link":true,"resolved":"../../../outside"},"../../../outside":{"version":"1.0.0"}`},
		{name: "missing lock link", spec: "file:../../vendor/example"},
		{name: "mismatched lock link", spec: "file:../../vendor/example", lockEntry: `,"node_modules/example":{"link":true,"resolved":"../../vendor/other"},"../../vendor/other":{"version":"1.0.0"}`},
		{name: "local tarball", spec: "file:../../vendor/example.tgz", lockEntry: `,"node_modules/example":{"resolved":"file:../../vendor/example.tgz","version":"1.0.0"}`},
		{name: "symlinked local directory", spec: "file:../../vendor/example", lockEntry: `,"node_modules/example":{"link":true,"resolved":"../../vendor/example"},"../../vendor/example":{"version":"1.0.0"}`, linkedDirectory: true},
		{name: "uncollected local directory", spec: "file:../../vendor/example", lockEntry: `,"node_modules/example":{"link":true,"resolved":"../../vendor/example"},"../../vendor/example":{"version":"1.0.0"}`, nestedOnly: true},
		{name: "reserved local directory", spec: "file:../../.143/example", lockEntry: `,"node_modules/example":{"link":true,"resolved":"../../.143/example"},"../../.143/example":{"version":"1.0.0"}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			parent, err := filepath.EvalSymlinks(t.TempDir())
			require.NoError(t, err, "fixture paths should be canonical")
			root := filepath.Join(parent, "repo")
			writeNPMProbeFixtureFile(t, root, "package.json", `{"name":"app","version":"1.0.0"}`)
			writeNPMProbeFixtureFile(t, root, "package-lock.json", `{"lockfileVersion":3,"packages":{"":{"name":"app","version":"1.0.0"}}}`)
			writeNPMProbeFixtureFile(t, root, "packages/ui/package.json",
				fmt.Sprintf(`{"name":"ui","version":"1.0.0","dependencies":{"example":%q}}`, tt.spec))
			writeNPMProbeFixtureFile(t, root, "packages/ui/package-lock.json",
				fmt.Sprintf(`{"lockfileVersion":3,"packages":{"":{"name":"ui","version":"1.0.0","dependencies":{"example":%q}}%s}}`, tt.spec, tt.lockEntry))
			writeNPMProbeFixtureFile(t, parent, "outside/package.json", `{"name":"example","version":"1.0.0"}`)
			writeNPMProbeFixtureFile(t, root, "vendor/other/package.json", `{"name":"example","version":"1.0.0"}`)
			writeNPMProbeFixtureFile(t, root, ".143/example/package.json", `{"name":"example","version":"1.0.0"}`)
			writeNPMProbeFixtureFile(t, root, "vendor/example.tgz", "fixture tarball contents")
			if tt.linkedDirectory {
				require.NoError(t, os.Symlink("other", filepath.Join(root, "vendor", "example")), "fixture should link the package directory")
			} else {
				writeNPMProbeFixtureFile(t, root, "vendor/example/package.json", `{"name":"example","version":"1.0.0"}`)
			}

			targets := []repoconfig.NPMSetup{{Directory: "packages/ui"}, {Directory: "."}}
			if tt.nestedOnly {
				targets = targets[:1]
			}
			_, err = runSetupProbeFixture(t, root, map[string]any{"targets": targets})
			require.Error(t, err, "unsafe or unverified local dependencies must fail before credential resolution")
		})
	}
}

func writeNPMProbeFixtureFile(t *testing.T, root, name, content string) {
	t.Helper()
	path := filepath.Join(root, filepath.FromSlash(name))
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o700), "fixture directory should exist for %s", name)
	require.NoError(t, os.WriteFile(path, []byte(content), 0o600), "fixture file should exist for %s", name)
}

func TestNPMSetupProbeLinkedGitWorktreeAndExistingOutputs(t *testing.T) {
	t.Parallel()
	parent, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err, "fixture should use canonical paths")
	source := filepath.Join(parent, "source")
	worktree := filepath.Join(parent, "worktree")
	require.NoError(t, os.Mkdir(source, 0o700), "source fixture should exist")
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", source, "-c", "user.name=Fixture", "-c", "user.email=fixture@example.test"}, args...)...)
		out, err := cmd.CombinedOutput()
		require.NoError(t, err, "synthetic Git worktree fixture should execute: %s", out)
	}
	git("init", "--quiet")
	require.NoError(t, os.WriteFile(filepath.Join(source, "package.json"), []byte(`{"name":"fixture","version":"1.0.0","workspaces":["packages/*"]}`), 0o600), "root manifest should exist")
	require.NoError(t, os.MkdirAll(filepath.Join(source, "packages/widget"), 0o700), "workspace should exist")
	require.NoError(t, os.WriteFile(filepath.Join(source, "packages/widget/package.json"), []byte(`{"name":"@fixture/widget","version":"1.0.0"}`), 0o600), "workspace manifest should exist")
	require.NoError(t, os.WriteFile(filepath.Join(source, "package-lock.json"), []byte(`{"lockfileVersion":3,"packages":{"node_modules/@fixture/widget":{"link":true,"resolved":"packages/widget"}}}`), 0o600), "root workspace lock should exist")
	git("add", "package.json", "package-lock.json", "packages/widget/package.json")
	git("commit", "--quiet", "-m", "Synthetic fixture")
	git("worktree", "add", "--quiet", "-b", "fixture-linked", worktree)
	require.NoError(t, os.MkdirAll(filepath.Join(worktree, "packages/widget/node_modules/stale"), 0o700), "legacy bootstrap outputs should exist before the first managed receipt")
	initial, err := runSetupProbeFixture(t, worktree, map[string]any{})
	require.NoError(t, err, "a real linked worktree should support managed npm setup receipts")
	require.False(t, initial.Ready, "first managed install should not reuse legacy bootstrap outputs")
	require.Equal(t, []string{"packages/widget/node_modules"}, initial.Outputs, "first-install cleanup must include old workspace outputs even without a receipt")
	stamped, err := runSetupProbeFixture(t, worktree, map[string]any{"stamp": initial.Fingerprint})
	require.NoError(t, err, "receipt should use a safe managed directory outside the Git pointer file")
	require.True(t, stamped.Ready, "managed output markers should establish warm readiness in linked worktrees")
}

func TestNPMSetupMainCancellationWaitsForRemoteProcessCleanup(t *testing.T) {
	t.Parallel()
	root, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err, "fixture should own canonical marker paths")
	fixture := newNPMDockerFixture(t, root, "", "443")
	provider := NewDockerProvider(fixture, zerolog.Nop(), WithRuntime("runc"))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	runCtx, stop := context.WithCancel(ctx)
	defer stop()
	pidPath := filepath.Join(root, "child-pid")
	latePath := filepath.Join(root, "late-write")
	// Publish the completed PID atomically so existence cannot precede its contents.
	script := fmt.Sprintf(`
import os,pathlib,time
pid=pathlib.Path(%q)
temporary=pid.with_suffix('.tmp')
temporary.write_text(str(os.getpid()))
temporary.replace(pid)
time.sleep(30)
pathlib.Path(%q).write_text('unsafe')
`, pidPath, latePath)
	finished := make(chan error, 1)
	t.Cleanup(func() {
		stop()
		select {
		case <-finished:
		case <-time.After(7 * time.Second):
			t.Error("fixture remote process cleanup did not finish before teardown")
		}
	})
	go func() {
		_, err := provider.npmSetupMainExec(runCtx, &agent.Sandbox{ID: "main", WorkDir: root}, script, `{}`, nil, io.Discard)
		finished <- err
		close(finished)
	}()
	require.Eventually(t, func() bool { _, err := os.Stat(pidPath); return err == nil }, 3*time.Second, 10*time.Millisecond, "fixture must start its actual remote child before cancelling the attachment")
	raw, err := os.ReadFile(pidPath)
	require.NoError(t, err, "child identity should be observable in the fixture")
	pid, err := strconv.Atoi(string(raw))
	require.NoError(t, err, "child identity should parse")
	stop()
	select {
	case err := <-finished:
		require.ErrorIs(t, err, context.Canceled, "cancellation should be returned only after remote process cleanup acknowledgement")
	case <-time.After(7 * time.Second):
		t.Fatal("main setup cancellation did not finish its bounded cleanup")
	}
	require.ErrorIs(t, syscall.Kill(pid, 0), syscall.ESRCH, "main import/probe child must be reaped before the provider lock can be released")
	require.NoFileExists(t, latePath, "a detached main-sandbox exec must not survive cancellation and mutate outputs")
}

func TestNPMSetupNetworkFollowsTenantSandbox(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, network string
		fail          bool
	}{
		{name: "tenant static egress", network: "tenant-static"},
		{name: "host namespace denied", network: "host", fail: true},
		{name: "agent namespace sharing denied", network: "container:agent", fail: true},
		{name: "unknown network denied", network: "", fail: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			inspect := container.InspectResponse{ContainerJSONBase: &container.ContainerJSONBase{HostConfig: &container.HostConfig{NetworkMode: container.NetworkMode(tt.network), DNS: []string{"192.0.2.53"}, Mounts: []mount.Mount{{Type: mount.TypeBind, Source: "/resolver/tenant.conf", Target: "/etc/resolv.conf", ReadOnly: true}}}}}
			profile, err := npmSetupNetworkFromSandbox(inspect)
			if tt.fail {
				require.Error(t, err, "setup must retain a distinct allowed network namespace")
			} else {
				require.NoError(t, err, "trusted sandbox networking should resolve")
				require.Equal(t, npmSetupNetworkProfile{Network: "tenant-static", ResolvConf: "/resolver/tenant.conf", DNS: []string{"192.0.2.53"}}, profile, "registry installs must follow the organization-selected egress and DNS configuration")
			}
		})
	}
}
