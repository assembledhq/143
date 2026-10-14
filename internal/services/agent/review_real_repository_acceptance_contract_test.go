package agent_test

import (
	"encoding/json"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func realReviewContractManifest() realReviewManifest {
	return realReviewManifest{Version: 1, Repositories: []realReviewRepository{{Name: "fixture", FullName: "synthetic/repository", CloneURL: "https://github.com/synthetic/repository.git", DefaultBranch: "main", InstallationID: 42, TokenKind: "github_actions_job", PRNumber: 3, BaseSHA: strings.Repeat("a", 40), HeadSHA: strings.Repeat("b", 40), ConfigSHA256: strings.Repeat("c", 64), HelperSHA256: map[string]string{".143/review.py": strings.Repeat("d", 64)}, CPULimit: 2, MemoryLimitMB: 3072, DiskLimitGB: 10, SnapshotLimitBytes: 1024 * 1024}}}
}
func TestRealReviewManifestBinding(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		change    func(*realReviewManifest)
		suffix    string
		wantError bool
	}{
		{name: "valid exact private identity"},
		{name: "credential URL", change: func(m *realReviewManifest) {
			m.Repositories[0].CloneURL = "https://secret@github.com/synthetic/repository.git"
		}, wantError: true},
		{name: "alternate host", change: func(m *realReviewManifest) {
			m.Repositories[0].CloneURL = "https://elsewhere.invalid/synthetic/repository.git"
		}, wantError: true},
		{name: "local transport", change: func(m *realReviewManifest) { m.Repositories[0].CloneURL = "file:///tmp/repository" }, wantError: true},
		{name: "remote identity mismatch", change: func(m *realReviewManifest) { m.Repositories[0].FullName = "synthetic/other" }, wantError: true},
		{name: "missing source pin", change: func(m *realReviewManifest) { m.Repositories[0].HeadSHA = "main" }, wantError: true},
		{name: "missing config pin", change: func(m *realReviewManifest) { m.Repositories[0].ConfigSHA256 = "" }, wantError: true},
		{name: "escaping helper", change: func(m *realReviewManifest) {
			m.Repositories[0].HelperSHA256 = map[string]string{".143/../secret": strings.Repeat("d", 64)}
		}, wantError: true},
		{name: "zero disk quota", change: func(m *realReviewManifest) { m.Repositories[0].DiskLimitGB = 0 }, wantError: true},
		{name: "unbounded snapshot", change: func(m *realReviewManifest) { m.Repositories[0].SnapshotLimitBytes = 0 }, wantError: true},
		{name: "unknown token source", change: func(m *realReviewManifest) { m.Repositories[0].TokenKind = "unscoped_personal" }, wantError: true},
		{name: "duplicate output name", change: func(m *realReviewManifest) { m.Repositories = append(m.Repositories, m.Repositories[0]) }, wantError: true},
		{name: "extra JSON document", suffix: " {}", wantError: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			manifest := realReviewContractManifest()
			if tt.change != nil {
				tt.change(&manifest)
			}
			raw, err := json.Marshal(manifest)
			require.NoError(t, err, "synthetic private inventory must encode")
			actual, err := decodeRealReviewManifest(append(raw, []byte(tt.suffix)...))
			if tt.wantError {
				require.Error(t, err, "unsafe or unbound private inventory must fail before Docker or credential lookup")
				return
			}
			require.NoError(t, err, "exact trusted synthetic inventory should decode")
			require.Equal(t, manifest, actual, "manifest must retain all exact identity and resource fields")
		})
	}
}
func TestRealReviewHostCredentialFile(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		mode      os.FileMode
		value     string
		symlink   bool
		wantError bool
	}{
		{name: "private file", mode: 0o600, value: "synthetic-fixture-credential\n"},
		{name: "public file", mode: 0o644, value: "synthetic-fixture-credential", wantError: true},
		{name: "symlink", mode: 0o600, value: "synthetic-fixture-credential", symlink: true, wantError: true},
		{name: "multiline", mode: 0o600, value: "synthetic-fixture\ncredential", wantError: true},
		{name: "too short", mode: 0o600, value: "short", wantError: true},
		{name: "oversized", mode: 0o600, value: strings.Repeat("x", 16385), wantError: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			name := filepath.Join(t.TempDir(), "host-token")
			require.NoError(t, os.WriteFile(name, []byte(tt.value), tt.mode), "synthetic credential fixture should exist")
			if tt.symlink {
				link := name + "-link"
				require.NoError(t, os.Symlink(name, link), "synthetic credential link should exist")
				name = link
			}
			value, err := readRealReviewToken(name)
			if tt.wantError {
				require.Error(t, err, "unsafe credential file must fail closed before reading Docker input")
				require.Empty(t, value, "rejected credential contents must not escape")
				return
			}
			require.NoError(t, err, "bounded host-only credential should load")
			require.Equal(t, strings.TrimSuffix(tt.value, "\n"), value, "only one trailing newline may be removed")
		})
	}
}
func TestRealReviewCredentialScannerRetainsBoundedOutput(t *testing.T) {
	t.Parallel()
	buffer := realReviewLogBuffer("synthetic-private-host-token")
	_, err := buffer.Write([]byte(strings.Repeat("x", acceptanceProviderLogArtifactLimit+99) + "synthetic-private-"))
	require.NoError(t, err, "full scanner must consume bytes after retention fills")
	_, err = buffer.Write([]byte("host-token"))
	require.NoError(t, err, "scanner must consume continuation of a split credential")
	require.True(t, buffer.CredentialDetected(), "credential split across writes beyond artifact cap must be detected")
	require.Equal(t, acceptanceProviderLogArtifactLimit, len(buffer.String()), "retained output must stay at the existing artifact cap")
}
func TestRealReviewTrustedObserverExecution(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		body      string
		wantExit  int
		wantStage string
	}{
		{name: "recipe success", body: `run(p["prepare"])`, wantStage: "repository_setup"},
		{name: "declared tool repair", body: `run(p["repair_tools"])`, wantStage: "tool_repair"},
		{name: "recipe exception", body: `run("raise")`, wantExit: 17, wantStage: "platform"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			trace := filepath.Join(root, "private-trace")
			fixture := `
import json,os,pathlib,stat,subprocess,time
p={"standalone":False,"prepare":"setup","repair_tools":"tool-repair"}
def run(script,required=True,authenticate=False,platform=False):
    if script == "raise": raise RuntimeError("expected")
    child=subprocess.Popen(["/bin/sh","-c","exit 0"],start_new_session=True)
    return child.wait() == 0
def capture(args): return b"ok"
def check_workspace_quarantine(): pass
def read_preparation_input(): return None
try:
    check_workspace_quarantine()
    message = read_preparation_input()
    ` + tt.body + `
except RuntimeError:
    raise SystemExit(17)
`
			command, err := instrumentRealReviewCommand(realReviewPython(fixture))
			require.NoError(t, err, "observer should require and preserve its exact trusted anchor")
			quotedTrace, err := json.Marshal(trace)
			require.NoError(t, err, "isolated trace path must encode")
			command = strings.ReplaceAll(command, `"/tmp/143-real-review-observation.jsonl"`, string(quotedTrace))
			child := exec.Command("/bin/sh", "-c", command)
			output, runErr := child.CombinedOutput()
			if tt.wantExit != 0 {
				var exitErr *exec.ExitError
				require.ErrorAs(t, runErr, &exitErr, "observer must preserve original exception exit")
				require.Equal(t, tt.wantExit, exitErr.ExitCode(), "observer must not turn failure into success")
			} else {
				require.NoError(t, runErr, "shell-quoted observer must run with original function semantics: %s", output)
			}
			raw, err := os.ReadFile(trace)
			require.NoError(t, err, "observer must persist fixed stage metadata even on exception")
			require.Contains(t, string(raw), `"stage": "`+tt.wantStage+`"`, "observer must record the actual branch without command contents")
			require.NotContains(t, string(raw), tt.body, "trace must not serialize repository command text")
		})
	}
}
func TestRealReviewObserverRejectsChangedAnchor(t *testing.T) {
	t.Parallel()
	_, err := instrumentRealReviewCommand("unrecognized trusted wrapper")
	require.Error(t, err, "changed production wrapper must fail instead of silently omitting observations")
}

type realReviewSnapshotReader struct {
	io.Reader
	closed   bool
	closeErr error
}

func (r *realReviewSnapshotReader) Close() error { r.closed = true; return r.closeErr }
func TestRealReviewSnapshotStreamBoundsAndCleanup(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		contents     string
		limit        int64
		closeError   bool
		wantError    bool
		expectedSize int64
	}{
		{name: "complete bounded snapshot", contents: "archive", limit: 8, expectedSize: 7},
		{name: "oversized snapshot", contents: "largerarchive", limit: 4, wantError: true, expectedSize: 5},
		{name: "failed production stream close", contents: "archive", limit: 8, closeError: true, wantError: true, expectedSize: 7},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			reader := &realReviewSnapshotReader{Reader: strings.NewReader(tt.contents)}
			if tt.closeError {
				reader.closeErr = errors.New("synthetic tar failure")
			}
			name, size, hash, err := persistRealReviewSnapshot(t.TempDir(), "fixture", reader, tt.limit)
			require.True(t, reader.closed, "production snapshot stream must close on success and every failure")
			require.Equal(t, tt.expectedSize, size, "retained archive may consume at most cap plus one sentinel byte")
			if tt.wantError {
				require.Error(t, err, "oversized or unsuccessful snapshot transport must not pass")
				_, statErr := os.Stat(name)
				require.ErrorIs(t, statErr, os.ErrNotExist, "failed snapshot bytes must be removed before artifacts can be uploaded")
				return
			} else {
				require.NoError(t, err, "complete bounded snapshot should persist")
			}
			metadata, statErr := os.Stat(name)
			require.NoError(t, statErr, "bounded snapshot evidence should exist privately")
			require.Equal(t, os.FileMode(0o600), metadata.Mode().Perm(), "snapshot evidence must remain private")
			require.Equal(t, size, metadata.Size(), "reported snapshot size must identify actual persisted bytes")
			contents, readErr := os.ReadFile(name)
			require.NoError(t, readErr, "snapshot bytes should be readable after its file descriptor closes")
			require.Equal(t, realReviewHash(contents), hash, "snapshot hash must cover exact persisted compressed bytes")
		})
	}
}

func TestRealReviewDeclaredToolObservation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		declared   bool
		missing    bool
		linkedTool bool
		linkedHome bool
		mode       os.FileMode
		wantError  bool
	}{
		{name: "no declared tools"},
		{name: "literal executable bytes and permissions", declared: true, mode: 0o755},
		{name: "missing tool", declared: true, missing: true, wantError: true},
		{name: "lost executable permission", declared: true, mode: 0o644, wantError: true},
		{name: "linked tool", declared: true, linkedTool: true, mode: 0o755, wantError: true},
		{name: "linked tool directory parent", declared: true, linkedHome: true, mode: 0o755, wantError: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			root, err := filepath.EvalSymlinks(t.TempDir())
			require.NoError(t, err, "fixture root must have no host aliases")
			home, workspace := filepath.Join(root, "home"), filepath.Join(root, "repository")
			require.NoError(t, os.Mkdir(home, 0o700), "isolated home should exist")
			require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".143"), 0o700), "isolated recipe directory should exist")
			dependencies := map[string]string{}
			if tt.declared {
				dependencies["golangci-lint"] = "2.10.1"
			}
			configuration, err := json.Marshal(map[string]any{"dependencies": dependencies})
			require.NoError(t, err, "declared fixture pins should encode")
			require.NoError(t, os.WriteFile(filepath.Join(workspace, ".143/config.json"), configuration, 0o600), "fixture pins should be readable")
			contents := []byte("trusted local executable fixture; never executed\n")
			executable := filepath.Join(home, ".local/bin/golangci-lint")
			if tt.declared {
				if tt.linkedHome {
					redirect := filepath.Join(root, "redirected-local")
					require.NoError(t, os.Mkdir(redirect, 0o700), "alias target should exist")
					require.NoError(t, os.Symlink(redirect, filepath.Join(home, ".local")), "parent alias fixture should exist")
				}
				require.NoError(t, os.MkdirAll(filepath.Dir(executable), 0o700), "tool directory should exist")
				if !tt.missing {
					target := executable
					if tt.linkedTool {
						target = filepath.Join(root, "redirected-tool")
					}
					require.NoError(t, os.WriteFile(target, contents, tt.mode), "tool fixture should exist with controlled permissions")
					if tt.linkedTool {
						require.NoError(t, os.Symlink(target, executable), "tool alias fixture should exist")
					}
				}
			}
			command := exec.Command("python3", "-I", "-S", "-c", realReviewDeclaredToolsPython)
			command.Dir = workspace
			for _, value := range os.Environ() {
				if !strings.HasPrefix(value, "HOME=") {
					command.Env = append(command.Env, value)
				}
			}
			command.Env = append(command.Env, "HOME="+home)
			output, runErr := command.CombinedOutput()
			if tt.wantError {
				require.Error(t, runErr, "missing, non-executable and redirected declared tools must fail observation before engine reuse")
				return
			}
			require.NoError(t, runErr, "literal tool observation must read bytes without executing repository code: %s", output)
			expected := map[string]realReviewDeclaredTool{}
			if tt.declared {
				expected["golangci-lint"] = realReviewDeclaredTool{DeclaredVersion: "2.10.1", Path: executable, SHA256: realReviewHash(contents), Mode: 0o755}
			}
			actual := map[string]realReviewDeclaredTool{}
			require.NoError(t, json.Unmarshal(output, &actual), "tool observation should encode bounded metadata only")
			require.Equal(t, expected, actual, "observation must include exact declared pins, tool hashes and executable modes")
		})
	}
}

func TestRealReviewOutputScanPreservesForwarding(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		target string
	}{{name: "split stdout", target: "stdout"}, {name: "split stderr", target: "stderr"}, {name: "split logger", target: "logger"}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			provider := &realReviewProvider{logs: realReviewLogBuffer("synthetic-private-host-token"), surfaces: realReviewLogBuffer("synthetic-private-host-token")}
			var stdout, stderr strings.Builder
			outputs := map[string]io.Writer{"stdout": provider.observedOutput(&stdout), "stderr": provider.observedOutput(&stderr), "logger": provider.logs, "sdk": provider.surfaces}
			_, err := outputs[tt.target].Write([]byte("synthetic-private-"))
			require.NoError(t, err, "original stream prefix must forward")
			for name, writer := range outputs {
				if name != tt.target {
					_, err := writer.Write([]byte("noise"))
					require.NoError(t, err, "independent stream noise must forward")
				}
			}
			_, err = outputs[tt.target].Write([]byte("host-token"))
			require.NoError(t, err, "original stream suffix must forward")
			expectedStdout, expectedStderr := "noise", "noise"
			if tt.target == "stdout" {
				expectedStdout = "synthetic-private-host-token"
			}
			if tt.target == "stderr" {
				expectedStderr = "synthetic-private-host-token"
			}
			require.Equal(t, expectedStdout, stdout.String(), "scanning must preserve exact stdout bytes despite stderr, logger and SDK interleaving")
			require.Equal(t, expectedStderr, stderr.String(), "scanning must preserve exact stderr bytes despite stdout, logger and SDK interleaving")
			require.True(t, provider.credentialDetected(), "the final verdict must include independently matched stdout, stderr and logger credentials")
		})
	}
}
func TestRealReviewIndependentOutputScanBeyondRetention(t *testing.T) {
	t.Parallel()
	provider := &realReviewProvider{logs: realReviewLogBuffer("synthetic-private-host-token"), surfaces: realReviewLogBuffer("synthetic-private-host-token")}
	stdout := provider.observedOutput(io.Discard)
	stderr := provider.observedOutput(io.Discard)
	_, err := stdout.Write([]byte(strings.Repeat("x", acceptanceProviderLogArtifactLimit+99) + "synthetic-private-"))
	require.NoError(t, err, "the full stdout stream must be consumed beyond retained bytes")
	_, err = stderr.Write([]byte("noise"))
	require.NoError(t, err, "stderr must not reset stdout's boundary tail")
	_, err = stdout.Write([]byte("host-token"))
	require.NoError(t, err, "stdout suffix must remain observable after interleaving")
	require.True(t, provider.credentialDetected(), "independent scanner must detect a split credential beyond retention")
	require.Equal(t, acceptanceProviderLogArtifactLimit, len(provider.outputScanners[0].String()), "each retained output must remain bounded")
}
