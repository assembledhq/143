package providers

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha512"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/repoconfig"
	"github.com/assembledhq/143/internal/services/agent"
	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/pkg/stdcopy"
	ocispec "github.com/opencontainers/image-spec/specs-go/v1"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

type setupProcessConn struct {
	net.Conn
	input  *io.PipeWriter
	output *io.PipeReader
	cancel context.CancelFunc
}

func (c *setupProcessConn) Read(p []byte) (int, error)  { return c.output.Read(p) }
func (c *setupProcessConn) Write(p []byte) (int, error) { return c.input.Write(p) }
func (c *setupProcessConn) CloseWrite() error           { return c.input.Close() }
func (c *setupProcessConn) Close() error {
	return errors.Join(c.input.Close(), c.output.Close())
}

type npmDockerFixture struct {
	*mockDockerClient
	mu                                   sync.Mutex
	root, node, npm, python, ca, port    string
	commands                             map[string]container.ExecOptions
	codes                                map[string]int
	done                                 map[string]chan struct{}
	sequence, created, removed, resolves int
	failInstall, failCleanup             bool
	cancelInstall                        context.CancelFunc
	privateInputs                        []byte
	publishedBeforeRemoval               bool
}

func newNPMDockerFixture(t *testing.T, root, ca, port string) *npmDockerFixture {
	t.Helper()
	node, err := exec.LookPath("node")
	require.NoError(t, err, "real Node must be available for the npm fixture")
	node, err = filepath.EvalSymlinks(node)
	require.NoError(t, err, "real Node path should resolve")
	npm, err := exec.LookPath("npm")
	require.NoError(t, err, "real npm must be available for the monorepo fixture")
	npm, err = filepath.EvalSymlinks(npm)
	require.NoError(t, err, "npm CLI should resolve to its installed script")
	python, err := exec.LookPath("python3")
	require.NoError(t, err, "modern Python should be available for tar data filtering")
	fixture := &npmDockerFixture{mockDockerClient: &mockDockerClient{}, root: root, node: node, npm: npm, python: python, ca: ca, port: port, commands: map[string]container.ExecOptions{}, codes: map[string]int{}, done: map[string]chan struct{}{}}
	fixture.containerInspectFn = func(_ context.Context, _ string) (container.InspectResponse, error) {
		return container.InspectResponse{ContainerJSONBase: &container.ContainerJSONBase{Image: "sha256:trusted-image", HostConfig: &container.HostConfig{NetworkMode: "tenant-network"}}}, nil
	}
	fixture.containerCreateFn = func(_ context.Context, cfg *container.Config, host *container.HostConfig, _ *network.NetworkingConfig, _ *ocispec.Platform, _ string) (container.CreateResponse, error) {
		fixture.created++
		require.Equal(t, "sha256:trusted-image", cfg.Image, "setup must use the immutable source image, not the writable agent filesystem")
		require.Equal(t, []string{"/bin/sleep"}, []string(cfg.Entrypoint), "setup cannot inherit an image entrypoint")
		require.Equal(t, []string{"600"}, []string(cfg.Cmd), "setup has a finite lifetime even if its worker dies")
		require.Equal(t, []string{"PATH=/usr/bin:/bin", "HOME=/private/home", "TMPDIR=/private/tmp"}, cfg.Env, "container metadata must carry only owned nonsecret environment")
		require.Equal(t, true, host.ReadonlyRootfs, "repository code cannot replace the trusted npm executable")
		require.Equal(t, true, host.AutoRemove, "dead setup containers must be removed automatically")
		require.Equal(t, []string{"ALL"}, []string(host.CapDrop), "setup must receive no Linux capabilities")
		require.Equal(t, []string{"no-new-privileges:true"}, host.SecurityOpt, "setup must not gain privileges")
		require.Empty(t, host.Mounts, "setup must not mount the agent workspace, auth socket, Docker socket or host credentials")
		require.Equal(t, "none", host.LogConfig.Type, "Docker logging must not persist private install diagnostics")
		require.Equal(t, map[string]string{"/private": "rw,noexec,nosuid,nodev,size=1073741824,mode=0700", "/setup": "rw,noexec,nosuid,nodev,size=3221225472,mode=0700"}, host.Tmpfs, "private material and dependency work must be scoped tmpfs")
		require.NoError(t, os.MkdirAll(filepath.Join(root, "private"), 0o700), "fixture private tmpfs should exist")
		require.NoError(t, os.MkdirAll(filepath.Join(root, "setup"), 0o700), "fixture project tmpfs should exist")
		return container.CreateResponse{ID: "isolated"}, nil
	}
	fixture.containerRemoveFn = func(ctx context.Context, _ string, opts container.RemoveOptions) error {
		require.NoError(t, ctx.Err(), "cleanup must receive its independent uncancelled budget")
		require.True(t, opts.Force && opts.RemoveVolumes, "cleanup must destroy running descendants and transient mounts")
		if fixture.failCleanup {
			return errors.New("fake cleanup failure synthetic-private-token")
		}
		fixture.removed++
		return errors.Join(os.RemoveAll(filepath.Join(root, "private")), os.RemoveAll(filepath.Join(root, "setup")))
	}
	fixture.containerExecCreateFn = func(_ context.Context, id string, options container.ExecOptions) (container.ExecCreateResponse, error) {
		fixture.mu.Lock()
		defer fixture.mu.Unlock()
		fixture.sequence++
		key := fmt.Sprint(fixture.sequence)
		fixture.commands[key] = options
		fixture.done[key] = make(chan struct{})
		if id != "isolated" && options.AttachStdin && strings.Contains(options.Cmd[2], "archive.extractall") {
			fixture.publishedBeforeRemoval = fixture.removed == 0
		}
		if id == "isolated" {
			options.WorkingDir = "/"
		}
		options.Cmd[2] = strings.ReplaceAll(options.Cmd[2], "/usr/bin/python3", fixture.python)
		options.Cmd[2] = strings.ReplaceAll(options.Cmd[2], "/tmp/143-npm-", filepath.Join(root, "143-npm-"))
		options.Cmd[2] = strings.ReplaceAll(options.Cmd[2], "dir='/tmp'", "dir="+fmt.Sprintf("%q", root))
		if id == "isolated" {
			options.Cmd[2] = strings.ReplaceAll(options.Cmd[2], "/private", filepath.Join(root, "private"))
			options.Cmd[2] = strings.ReplaceAll(options.Cmd[2], "/setup/project", filepath.Join(root, "setup/project"))
			if strings.Contains(options.Cmd[2], "/usr/bin/npm") {
				ownedArgs := fmt.Sprintf("args=[%q,%q,'--fetch-retries=0','--strict-ssl=false','--cafile='+%q,", fixture.node, fixture.npm, fixture.ca)
				script := strings.ReplaceAll(npmSetupInstallPython, "args=['/usr/bin/npm',", ownedArgs)
				script = strings.ReplaceAll(script, "url.port not in (None,443)", "url.port not in (None,443,"+fixture.port+")")
				script = strings.ReplaceAll(script, "/private", filepath.Join(root, "private"))
				script = strings.ReplaceAll(script, "/setup/project", filepath.Join(root, "setup/project"))
				if fixture.failInstall {
					script = strings.ReplaceAll(script, "for target in p['targets']:", "raise RuntimeError('synthetic-private-token')\n for target in p['targets']:")
				}
				options.Cmd[2] = fixture.python + " -I -S -c '" + shellEscape(script) + "'"
				if fixture.cancelInstall != nil {
					fixture.cancelInstall()
				}
			}
		}
		fixture.commands[key] = options
		return container.ExecCreateResponse{ID: key}, nil
	}
	fixture.containerExecAttachFn = func(ctx context.Context, key string, _ container.ExecAttachOptions) (types.HijackedResponse, error) {
		options := fixture.commands[key]
		runCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
		stdin, writer := io.Pipe()
		reader, stdout := io.Pipe()
		conn := &setupProcessConn{input: writer, output: reader, cancel: cancel}
		cmd := exec.CommandContext(runCtx, "/bin/sh", "-c", options.Cmd[2])
		cmd.Dir = options.WorkingDir
		if cmd.Dir == "" {
			cmd.Dir = root
		}
		if options.AttachStdin {
			cmd.Stdin = io.TeeReader(stdin, &npmFixtureInputWriter{fixture: fixture})
		}
		cmd.Stdout = stdcopy.NewStdWriter(stdout, stdcopy.Stdout)
		cmd.Stderr = stdcopy.NewStdWriter(stdout, stdcopy.Stderr)
		go func() {
			err := cmd.Run()
			code := 0
			if err != nil {
				var exit *exec.ExitError
				if errors.As(err, &exit) {
					code = exit.ExitCode()
				} else {
					code = 1
				}
			}
			fixture.mu.Lock()
			fixture.codes[key] = code
			fixture.mu.Unlock()
			close(fixture.done[key])
			cancel()
			if err := stdout.Close(); err != nil && !errors.Is(err, io.ErrClosedPipe) {
				t.Errorf("fixture output close failed: %v", err)
			}
		}()
		return types.HijackedResponse{Conn: conn, Reader: bufio.NewReader(conn)}, nil
	}
	fixture.containerExecInspectFn = func(_ context.Context, key string) (container.ExecInspect, error) {
		<-fixture.done[key]
		fixture.mu.Lock()
		defer fixture.mu.Unlock()
		return container.ExecInspect{ExitCode: fixture.codes[key]}, nil
	}
	return fixture
}

type npmFixtureInputWriter struct{ fixture *npmDockerFixture }

func (w *npmFixtureInputWriter) Write(p []byte) (int, error) {
	w.fixture.mu.Lock()
	defer w.fixture.mu.Unlock()
	w.fixture.privateInputs = append(w.fixture.privateInputs, p...)
	return len(p), nil
}

func syntheticNPMPackage(t *testing.T, version string) ([]byte, string) {
	t.Helper()
	var body bytes.Buffer
	zip := gzip.NewWriter(&body)
	archive := tar.NewWriter(zip)
	files := map[string]string{"package/package.json": fmt.Sprintf(`{"name":"@fixture/private","version":%q,"scripts":{"install":"node -e \"require('fs').writeFileSync('credential-leaked',require('fs').readFileSync(process.env.npm_config_userconfig))\""}}`, version), "package/index.js": "module.exports = '" + version + "';\n"}
	for name, content := range files {
		require.NoError(t, archive.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(content)), Typeflag: tar.TypeReg}), "synthetic package header should serialize")
		_, err := io.WriteString(archive, content)
		require.NoError(t, err, "synthetic package bytes should serialize")
	}
	require.NoError(t, archive.Close(), "package archive should close")
	require.NoError(t, zip.Close(), "package gzip should close")
	sum := sha512.Sum512(body.Bytes())
	return body.Bytes(), "sha512-" + base64.StdEncoding.EncodeToString(sum[:])
}

func TestDockerNPMSetupRealMonorepoAndStandalone(t *testing.T) {
	t.Parallel()
	root, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err, "fixture root should be canonical")
	one, integrityOne := syntheticNPMPackage(t, "1.0.0")
	two, integrityTwo := syntheticNPMPackage(t, "2.0.0")
	token := "synthetic-private-token"
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		body := one
		if strings.Contains(r.URL.Path, "two") {
			body = two
		}
		if _, err := w.Write(body); err != nil {
			t.Errorf("fixture registry response failed: %v", err)
		}
	}))
	defer server.Close()
	ca := filepath.Join(root, "ca.pem")
	require.NoError(t, os.WriteFile(ca, pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw}), 0o600), "local TLS registry CA should be explicit")
	workspace := filepath.Join(root, "workspace")
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".git"), 0o700), "fixture should own metadata")
	write := func(name, content string) {
		t.Helper()
		target := filepath.Join(workspace, name)
		require.NoError(t, os.MkdirAll(filepath.Dir(target), 0o700), "manifest parent should exist")
		require.NoError(t, os.WriteFile(target, []byte(content), 0o600), "synthetic manifest should be written")
	}
	write("package.json", `{"name":"fixture-root","version":"1.0.0","private":true,"workspaces":["packages/*"],"dependencies":{"@fixture/private":"1.0.0"},"scripts":{"preinstall":"node -e \"require('fs').writeFileSync('root-script-ran','unsafe')\""}}`)
	write("packages/widget/package.json", `{"name":"@fixture/widget","version":"1.0.0","dependencies":{"@fixture/private":"2.0.0"}}`)
	write("tools/isolated/package.json", `{"name":"fixture-isolated","version":"1.0.0","dependencies":{"@fixture/private":"1.0.0"},"peerDependencies":{"@fixture/widget":"*"}}`)
	packageOne := map[string]any{"version": "1.0.0", "resolved": server.URL + "/one.tgz", "integrity": integrityOne, "hasInstallScript": true}
	packageTwo := map[string]any{"version": "2.0.0", "resolved": server.URL + "/two.tgz", "integrity": integrityTwo, "hasInstallScript": true}
	lock := map[string]any{"name": "fixture-root", "version": "1.0.0", "lockfileVersion": 3, "packages": map[string]any{"": map[string]any{"name": "fixture-root", "version": "1.0.0", "workspaces": []string{"packages/*"}, "dependencies": map[string]string{"@fixture/private": "1.0.0"}}, "packages/widget": map[string]any{"name": "@fixture/widget", "version": "1.0.0", "dependencies": map[string]string{"@fixture/private": "2.0.0"}}, "node_modules/@fixture/widget": map[string]any{"resolved": "packages/widget", "link": true}, "node_modules/@fixture/private": packageOne, "packages/widget/node_modules/@fixture/private": packageTwo}}
	raw, err := json.Marshal(lock)
	require.NoError(t, err, "root workspace lock should serialize")
	write("package-lock.json", string(raw))
	lock = map[string]any{"name": "fixture-isolated", "version": "1.0.0", "lockfileVersion": 3, "packages": map[string]any{"": map[string]any{"name": "fixture-isolated", "version": "1.0.0", "dependencies": map[string]string{"@fixture/private": "1.0.0"}, "peerDependencies": map[string]string{"@fixture/widget": "*"}}, "node_modules/@fixture/private": packageOne}}
	raw, err = json.Marshal(lock)
	require.NoError(t, err, "standalone package lock should serialize")
	write("tools/isolated/package-lock.json", string(raw))
	write(".npmrc", "ignore-scripts=false\nregistry=https://invalid.example/\nscript-shell=/untrusted/shell\n")
	write("node_modules/.bin/npm", "malicious executable must never run")
	write("startup.cjs", "require('fs').writeFileSync('node-startup-ran','unsafe')")
	port := server.URL[strings.LastIndex(server.URL, ":")+1:]
	fixture := newNPMDockerFixture(t, root, ca, port)
	var logs bytes.Buffer
	provider := NewDockerProvider(fixture, zerolog.New(&logs).Level(zerolog.DebugLevel), WithRuntime("runc"))
	sb := &agent.Sandbox{ID: "main", WorkDir: workspace, Env: map[string]string{"NODE_OPTIONS": "--require=" + filepath.Join(workspace, "startup.cjs"), "NPM_CONFIG_USERCONFIG": filepath.Join(workspace, ".npmrc"), "PATH": filepath.Join(workspace, "node_modules/.bin")}}
	targets := []repoconfig.NPMSetup{{Directory: "."}, {Directory: "tools/isolated", LegacyPeerDeps: true}}
	resolve := func(context.Context) ([]models.ResolvedPackageRegistryCredential, error) {
		fixture.resolves++
		return []models.ResolvedPackageRegistryCredential{{Metadata: models.PackageRegistryCredentialSummary{RegistryURL: server.URL + "/", PackageScope: "@fixture"}, Token: token}}, nil
	}
	key, err := provider.PrepareNPMDependencies(context.Background(), sb, targets, "revision-one", resolve)
	require.NoError(t, err, "real npm should install root workspaces, nested version conflicts, and the separate locked package")
	require.False(t, fixture.publishedBeforeRemoval, "dependency outputs must not reach the agent until setup container deletion succeeds")
	require.Equal(t, 1, fixture.created, "one isolated lifecycle should install all declared directories")
	require.Equal(t, 1, fixture.removed, "setup container must be removed before publishing")
	require.NoDirExists(t, filepath.Join(root, "private"), "private config, npm logs, cache and tmpfs must be destroyed")
	checks := map[string]string{"node_modules/@fixture/private/index.js": "module.exports = '1.0.0';\n", "packages/widget/node_modules/@fixture/private/index.js": "module.exports = '2.0.0';\n", "tools/isolated/node_modules/@fixture/private/index.js": "module.exports = '1.0.0';\n"}
	for name, want := range checks {
		actual, err := os.ReadFile(filepath.Join(workspace, name))
		require.NoError(t, err, "root and nested node_modules should be restored")
		require.Equal(t, want, string(actual), "each workspace should resolve its locked dependency version")
	}
	linked, err := os.Readlink(filepath.Join(workspace, "node_modules/@fixture/widget"))
	require.NoError(t, err, "workspace link should survive output validation and import")
	require.Equal(t, "../../packages/widget", linked, "workspace links should point to the actual agent workspace source")
	for _, marker := range []string{"root-script-ran", "node-startup-ran", "node_modules/@fixture/private/credential-leaked", "packages/widget/node_modules/@fixture/private/credential-leaked", "tools/isolated/node_modules/@fixture/private/credential-leaked"} {
		require.NoFileExists(t, filepath.Join(workspace, marker), "repository and dependency lifecycle code must never execute with credentials")
	}
	require.NoError(t, filepath.Walk(workspace, func(name string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info.Mode().IsRegular() {
			data, err := os.ReadFile(name)
			if err != nil {
				return err
			}
			require.NotContains(t, string(data), token, "published files and receipts must exclude private authentication")
		}
		return nil
	}), "workspace should be inspectable without secret material")
	for _, options := range fixture.commands {
		require.NotContains(t, strings.Join(options.Cmd, " "), token, "Docker command arguments must never include credentials")
		require.NotContains(t, strings.Join(options.Env, " "), token, "Docker exec environment must never include credentials")
	}
	require.NotContains(t, logs.String(), token, "provider logs must exclude credentials")
	again, err := provider.PrepareNPMDependencies(context.Background(), sb, targets, "revision-one", resolve)
	require.NoError(t, err, "healthy warm setup should reuse installed dependencies")
	require.Equal(t, key, again, "same inputs should preserve setup identity")
	require.Equal(t, 1, fixture.resolves, "warm reuse must not decrypt credentials")
	rotated, err := provider.PrepareNPMDependencies(context.Background(), sb, targets, "revision-two", resolve)
	require.NoError(t, err, "credential rotation should rebuild isolated dependency outputs")
	require.NotEqual(t, key, rotated, "credential revision must invalidate warm receipt")
	require.Equal(t, 2, fixture.resolves, "rotation should request newly authorized credentials")
	rootLockRaw, err := os.ReadFile(filepath.Join(workspace, "package-lock.json"))
	require.NoError(t, err, "root fixture lock should remain intact after isolated installation")
	var rootLock map[string]any
	require.NoError(t, json.Unmarshal(rootLockRaw, &rootLock), "root fixture lock should parse")
	packages := rootLock["packages"].(map[string]any)
	packages["packages/widget"].(map[string]any)["dependencies"] = map[string]string{"@fixture/private": "1.0.0"}
	delete(packages, "packages/widget/node_modules/@fixture/private")
	rootLockRaw, err = json.Marshal(rootLock)
	require.NoError(t, err, "updated hoisted lock should serialize")
	write("package-lock.json", string(rootLockRaw))
	write("packages/widget/package.json", `{"name":"@fixture/widget","version":"1.0.0","dependencies":{"@fixture/private":"1.0.0"}}`)
	receipts, err := os.ReadDir(filepath.Join(workspace, ".git"))
	require.NoError(t, err, "managed fixture receipts should be enumerable")
	for _, receipt := range receipts {
		if strings.HasSuffix(receipt.Name(), ".json") {
			require.NoError(t, os.Remove(filepath.Join(workspace, ".git", receipt.Name())), "fixture should model the first managed setup of legacy dependency outputs")
		}
	}
	hoisted, err := provider.PrepareNPMDependencies(context.Background(), sb, targets, "revision-two", resolve)
	require.NoError(t, err, "a first managed setup must clean obsolete nested outputs even without a prior receipt")
	require.NotEqual(t, rotated, hoisted, "manifest changes must invalidate setup reuse")
	require.NoDirExists(t, filepath.Join(workspace, "packages/widget/node_modules"), "npm hoisting must remove the stale nested dependency version left by an earlier bootstrap")
	require.Equal(t, 3, fixture.resolves, "changed manifests without a managed receipt should authorize exactly one new isolated install")

}

func TestDockerNPMSetupCleanupFailures(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name                                   string
		installFailure, cleanupFailure, cancel bool
	}{
		{name: "install error", installFailure: true},
		{name: "cleanup error blocks export publication", cleanupFailure: true},
		{name: "cancelled installer", cancel: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			root, err := filepath.EvalSymlinks(t.TempDir())
			require.NoError(t, err, "fixture should have canonical paths")
			fixture := newNPMDockerFixture(t, root, "", "443")
			fixture.failInstall = tt.installFailure
			fixture.failCleanup = tt.cleanupFailure
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tt.cancel {
				fixture.cancelInstall = cancel
			}
			provider := NewDockerProvider(fixture, zerolog.Nop(), WithRuntime("runc"))
			files := map[string]string{"package.json": `{"name":"empty-fixture","version":"1.0.0"}`, "package-lock.json": `{"name":"empty-fixture","version":"1.0.0","lockfileVersion":3,"packages":{"":{"name":"empty-fixture","version":"1.0.0"}}}`}
			output, err := provider.runIsolatedNPMSetup(ctx, &agent.Sandbox{ID: "main"}, "sha256:trusted-image", []repoconfig.NPMSetup{{Directory: "."}}, files, nil)
			require.Error(t, err, "failed or unconfirmed private setup must never produce an artifact")
			require.Nil(t, output, "failure and cancellation must not return dependency output for publication")
			require.NotContains(t, err.Error(), "synthetic-private-token", "private provider diagnostics must never reach failure text")
			if !tt.cleanupFailure {
				require.Equal(t, 1, fixture.removed, "successfully removable setup must always be force-deleted")
				require.NoDirExists(t, filepath.Join(root, "private"), "cancelled and failed setup must destroy transient authentication")
			}
		})
	}
}
