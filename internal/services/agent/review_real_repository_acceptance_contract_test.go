package agent_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/reviewpreparation"
	"github.com/docker/docker/api/types/container"
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
func TestRealReviewAcceptanceActivationIdentity(t *testing.T) {
	t.Parallel()
	repo := realReviewContractManifest().Repositories[0]
	session := realReviewSession(t, repo)
	policy, err := reviewpreparation.NewPolicy(true, []string{repo.FullName})
	require.NoError(t, err, "real-repository acceptance must explicitly enable only its exact candidate repository")
	active, err := policy.ForSession(context.Background(), realReviewRepoStore{repo}, session)
	require.NoError(t, err, "acceptance repository ID, tenant, revision and clone identities must satisfy the production gate")
	require.True(t, active, "acceptance fixtures must execute mandatory preparation through an explicit activation policy")
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

func TestRealReviewDeliberateMissingDeclaredTool(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		declared   bool
		missing    bool
		linkedTool bool
		mode       os.FileMode
		wantError  bool
	}{
		{name: "remove one literal declared executable", declared: true, mode: 0o755},
		{name: "no declared executable", wantError: true},
		{name: "already missing executable", declared: true, missing: true, wantError: true},
		{name: "non-executable declaration", declared: true, mode: 0o644, wantError: true},
		{name: "redirected executable", declared: true, linkedTool: true, mode: 0o755, wantError: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			root, err := filepath.EvalSymlinks(t.TempDir())
			require.NoError(t, err, "fault injection fixture must use an unaliased filesystem root")
			home, workspace := filepath.Join(root, "home"), filepath.Join(root, "repository")
			require.NoError(t, os.MkdirAll(filepath.Join(home, ".local/bin"), 0o700), "isolated platform executable directory should exist")
			require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".143"), 0o700), "isolated candidate recipe directory should exist")
			require.NoError(t, os.Mkdir(filepath.Join(workspace, ".git"), 0o700), "isolated readiness directory should exist")
			dependencies := map[string]string{}
			if tt.declared {
				dependencies = map[string]string{"golangci-lint": "2.10.1", "yq": "4.49.1"}
			}
			configuration, err := json.Marshal(map[string]any{"dependencies": dependencies})
			require.NoError(t, err, "fixture must encode its exact declared pins")
			require.NoError(t, os.WriteFile(filepath.Join(workspace, ".143/config.json"), configuration, 0o600), "fault injection must read literal candidate recipe bytes")
			contents, unrelated := []byte("literal tool fixture; never executed\n"), []byte("unrelated executable fixture; never executed\n")
			executable := filepath.Join(home, ".local/bin/golangci-lint")
			protected := filepath.Join(root, "protected-source")
			require.NoError(t, os.WriteFile(protected, contents, 0o755), "source outside platform executable directory must survive every fault case")
			if tt.declared {
				require.NoError(t, os.WriteFile(filepath.Join(home, ".local/bin/yq"), unrelated, 0o755), "unrelated declared executable must survive removal")
				if tt.linkedTool {
					require.NoError(t, os.Symlink(protected, executable), "redirected tool must be rejected before deletion")
				} else if !tt.missing {
					require.NoError(t, os.WriteFile(executable, contents, tt.mode), "controlled declared executable must begin with exact bytes and mode")
				}
			}
			receipt := filepath.Join(workspace, ".git/143-review-evidence.json")
			receiptBytes := []byte(`{"revision":{"head_sha":"pinned"},"evidence":[{"path":".143/source","files":[["api.go","file",123]]}]}`)
			require.NoError(t, os.WriteFile(receipt, receiptBytes, 0o600), "fault injection must leave captured repository evidence unchanged")
			runProbe := func(script string) ([]byte, error) {
				command := exec.Command("python3", "-I", "-S", "-c", script)
				command.Dir = workspace
				for _, value := range os.Environ() {
					if !strings.HasPrefix(value, "HOME=") {
						command.Env = append(command.Env, value)
					}
				}
				command.Env = append(command.Env, "HOME="+home)
				return command.CombinedOutput()
			}
			identityBefore, err := runProbe(realReviewReceiptIdentityPython)
			require.NoError(t, err, "readiness identity should be observable before the deliberate fault")
			output, runErr := runProbe(realReviewRemoveDeclaredToolPython)
			if tt.wantError {
				require.Error(t, runErr, "fault injection must reject absent, non-executable or redirected declared binaries")
			} else {
				require.NoError(t, runErr, "fault injection must remove only a validated literal platform executable: %s", output)
				var actual realReviewRemovedTool
				require.NoError(t, json.Unmarshal(output, &actual), "fault injection should return only bounded metadata")
				expected := realReviewRemovedTool{
					Name: "golangci-lint", Absent: true,
					Tool:           realReviewDeclaredTool{DeclaredVersion: "2.10.1", Path: executable, SHA256: realReviewHash(contents), Mode: 0o755},
					RemainingTools: map[string]realReviewDeclaredTool{"yq": {DeclaredVersion: "4.49.1", Path: filepath.Join(home, ".local/bin/yq"), SHA256: realReviewHash(unrelated), Mode: 0o755}},
				}
				require.Equal(t, expected, actual, "fault evidence must identify exact removed bytes and preserve unrelated declared executable identity")
				_, statErr := os.Lstat(executable)
				require.ErrorIs(t, statErr, os.ErrNotExist, "required executable must actually be absent before production repair")
			}
			identityAfter, err := runProbe(realReviewReceiptIdentityPython)
			require.NoError(t, err, "readiness identity should remain observable after every fault attempt")
			require.Equal(t, string(identityBefore), string(identityAfter), "deliberate executable removal must preserve the exact receipt inode, bytes, permissions and timestamps")
			protectedAfter, err := os.ReadFile(protected)
			require.NoError(t, err, "source outside the executable directory must remain readable")
			require.Equal(t, contents, protectedAfter, "fault injection must never delete or alter unrelated source")
		})
	}
}

func TestRealReviewHostKernelResourceProof(t *testing.T) {
	t.Parallel()
	containerID := strings.Repeat("a", 64)
	owned := "docker/" + containerID
	tests := []struct {
		name             string
		overrides        map[string]string
		missing          string
		linked           string
		linkedGroup      bool
		nanoCPUs         int64
		expectedQuotaUS  int64
		expectedPeriodUS int64
		wantError        bool
	}{
		{name: "exact dedicated two CPU three GiB kernel limits", expectedQuotaUS: 200000, expectedPeriodUS: 100000},
		{name: "exact fractional CPU limit", overrides: map[string]string{owned + "/cpu.max": "50000 100000\n"}, nanoCPUs: 500000000, expectedQuotaUS: 50000, expectedPeriodUS: 100000},
		{name: "equivalent finite CPU period", overrides: map[string]string{owned + "/cpu.max": "400000 200000\n"}, expectedQuotaUS: 400000, expectedPeriodUS: 200000},
		{name: "unlimited memory", overrides: map[string]string{owned + "/memory.max": "max\n"}, wantError: true},
		{name: "physical memory instead of declared bound", overrides: map[string]string{owned + "/memory.max": "8333333333\n"}, wantError: true},
		{name: "smaller conflicting memory", overrides: map[string]string{owned + "/memory.max": "1073741824\n"}, wantError: true},
		{name: "negative memory", overrides: map[string]string{owned + "/memory.max": "-1\n"}, wantError: true},
		{name: "overflowing memory", overrides: map[string]string{owned + "/memory.max": "99999999999999999999\n"}, wantError: true},
		{name: "unlimited CPU", overrides: map[string]string{owned + "/cpu.max": "max 100000\n"}, wantError: true},
		{name: "host CPU availability does not prove quota", overrides: map[string]string{owned + "/cpu.max": "100000 100000\n"}, wantError: true},
		{name: "zero CPU period", overrides: map[string]string{owned + "/cpu.max": "200000 0\n"}, wantError: true},
		{name: "malformed CPU controller", overrides: map[string]string{owned + "/cpu.max": "200000\n"}, wantError: true},
		{name: "shared unlimited service membership", overrides: map[string]string{"proc": "0::/system.slice/containerd.service\n"}, wantError: true},
		{name: "other container membership", overrides: map[string]string{"proc": "0::/docker/" + strings.Repeat("b", 64) + "\n"}, wantError: true},
		{name: "escaping membership", overrides: map[string]string{"proc": "0::/docker/../" + containerID + "\n"}, wantError: true},
		{name: "noncanonical membership", overrides: map[string]string{"proc": "0::/docker//" + containerID + "\n"}, wantError: true},
		{name: "conflicting unified memberships", overrides: map[string]string{"proc": "0::/docker/" + containerID + "\n0::/system.slice/containerd.service\n"}, wantError: true},
		{name: "missing CPU controller", overrides: map[string]string{"cgroup.controllers": "memory pids\n"}, wantError: true},
		{name: "missing memory controller file", missing: owned + "/memory.max", wantError: true},
		{name: "missing host process", missing: "proc", wantError: true},
		{name: "PID outside bounded group", overrides: map[string]string{owned + "/cgroup.procs": "70202\n"}, wantError: true},
		{name: "redirected controller file", linked: owned + "/memory.max", wantError: true},
		{name: "redirected container group", linkedGroup: true, wantError: true},
		{name: "oversized controller value", overrides: map[string]string{owned + "/memory.max": strings.Repeat("3", 129)}, wantError: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			root, err := filepath.EvalSymlinks(t.TempDir())
			require.NoError(t, err, "kernel fixtures must use isolated canonical roots")
			procRoot, cgroupRoot := filepath.Join(root, "proc"), filepath.Join(root, "cgroup")
			require.NoError(t, os.MkdirAll(filepath.Join(procRoot, "70201"), 0o700), "inspected host PID fixture must exist")
			require.NoError(t, os.MkdirAll(filepath.Join(cgroupRoot, owned), 0o700), "dedicated host container cgroup fixture must exist")
			files := map[string]string{
				"proc":                  "0::/docker/" + containerID + "\n",
				"cgroup.controllers":    "cpuset cpu io memory pids\n",
				owned + "/memory.max":   "3221225472\n",
				owned + "/cpu.max":      "200000 100000\n",
				owned + "/cgroup.procs": "70200\n70201\n",
			}
			for name, value := range tt.overrides {
				files[name] = value
			}
			for name, value := range files {
				if name == tt.missing {
					continue
				}
				filename := filepath.Join(cgroupRoot, name)
				if name == "proc" {
					filename = filepath.Join(procRoot, "70201/cgroup")
				}
				if name == tt.linked {
					target := filepath.Join(root, "redirected-controller")
					require.NoError(t, os.WriteFile(target, []byte(value), 0o600), "redirected controller target must preserve valid-looking intent")
					require.NoError(t, os.Symlink(target, filename), "kernel proof must reject redirected controller reads")
				} else {
					require.NoError(t, os.WriteFile(filename, []byte(value), 0o600), "finite host controller observation fixture should exist")
				}
			}
			if tt.linkedGroup {
				group := filepath.Join(cgroupRoot, owned)
				target := filepath.Join(root, "redirected-group")
				require.NoError(t, os.Rename(group, target), "container group fixture may expose valid-looking redirected bounds")
				require.NoError(t, os.Symlink(target, group), "host kernel proof must reject redirected cgroup parents")
			}
			nanoCPUs := tt.nanoCPUs
			if nanoCPUs == 0 {
				nanoCPUs = 2000000000
			}
			proof, err := readRealReviewKernelResources(procRoot, cgroupRoot, containerID, 70201, 3221225472, nanoCPUs)
			if tt.wantError {
				require.Error(t, err, "missing, unlimited, conflicting, shared or redirected kernel state must fail acceptance")
				require.False(t, proof.Verified, "invalid host controller evidence must never claim enforced resource acceptance")
				return
			}
			require.NoError(t, err, "matching finite per-container kernel bounds must pass without Docker or guest statistics")
			expected := realReviewKernelResourceProof{ContainerID: containerID, SandboxPID: 70201, CgroupVersion: 2, CgroupPath: "/docker/" + containerID, MemoryMaxBytes: 3221225472, CPUQuotaUS: tt.expectedQuotaUS, CPUPeriodUS: tt.expectedPeriodUS, NanoCPUs: nanoCPUs, PIDMember: true, Verified: true}
			require.Equal(t, expected, proof, "proof must bind exact inspected PID, owned cgroup membership and declared finite kernel limits")
		})
	}
}

func TestRealReviewOwnedMemoryEvents(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		raw       string
		expected  map[string]int64
		wantError bool
	}{
		{name: "explicit finite OOM counters", raw: "low 0\nhigh 0\nmax 5\noom 2\noom_kill 1\noom_group_kill 0\n", expected: map[string]int64{"low": 0, "high": 0, "max": 5, "oom": 2, "oom_kill": 1, "oom_group_kill": 0}},
		{name: "new kernel fields are not retained as arbitrary text", raw: "max 0\noom 0\noom_kill 0\nfuture_counter 1\n", expected: map[string]int64{"max": 0, "oom": 0, "oom_kill": 0}},
		{name: "missing OOM counter", raw: "max 5\noom_kill 1\n", wantError: true},
		{name: "conflicting repeated counter", raw: "max 5\noom 2\noom_kill 1\noom 0\n", wantError: true},
		{name: "negative event", raw: "max 5\noom -1\noom_kill 1\n", wantError: true},
		{name: "unbounded event value", raw: "max 5\noom 99999999999999999999\noom_kill 1\n", wantError: true},
		{name: "arbitrary text event", raw: "max 5\noom private-token\noom_kill 1\n", wantError: true},
		{name: "oversized event file", raw: strings.Repeat("x", 4097), wantError: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			actual, err := realReviewMemoryEvents([]byte(tt.raw))
			if tt.wantError {
				require.Error(t, err, "missing, conflicting or unsafe event evidence must remain unavailable")
				require.Nil(t, actual, "rejected kernel counter bytes must never become retained evidence")
				return
			}
			require.NoError(t, err, "finite owned-cgroup event observations should parse without OOM inference")
			require.Equal(t, tt.expected, actual, "only exact whitelisted numeric kernel event counters should survive")
		})
	}
}

func TestRealReviewOwnedMemorySample(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		missing     string
		peak        string
		events      string
		linkedGroup bool
		invalidID   bool
		unavailable []string
	}{
		{name: "complete sample including zero current memory", unavailable: []string{}},
		{name: "missing peak retains explicit OOM events", missing: "memory.peak", unavailable: []string{"memory.peak"}},
		{name: "missing events is unavailable evidence", missing: "memory.events", unavailable: []string{"memory.events"}},
		{name: "malformed events retain available byte observations", events: "max 3\noom_kill 1\n", unavailable: []string{"memory.events"}},
		{name: "oversized byte observation", peak: strings.Repeat("3", 129), unavailable: []string{"memory.peak"}},
		{name: "disappeared cgroup", missing: "group", unavailable: []string{"memory.events", "memory.peak", "memory.current", "memory.stat"}},
		{name: "redirected cgroup", linkedGroup: true, unavailable: []string{"memory.events", "memory.peak", "memory.current", "memory.stat"}},
		{name: "escaping container identity", invalidID: true, unavailable: []string{"memory.events", "memory.peak", "memory.current", "memory.stat"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			root, err := filepath.EvalSymlinks(t.TempDir())
			require.NoError(t, err, "owned memory fixtures must have canonical host roots")
			containerID := strings.Repeat("a", 64)
			group := filepath.Join(root, "docker", containerID)
			require.NoError(t, os.MkdirAll(group, 0o700), "only the exact owned container cgroup may supply telemetry")
			files := map[string]string{"memory.peak": "300\n", "memory.current": "0\n", "memory.events": "max 3\noom 1\noom_kill 1\n", "memory.stat": realReviewContractMemoryStatRaw}
			if tt.peak != "" {
				files["memory.peak"] = tt.peak
			}
			if tt.events != "" {
				files["memory.events"] = tt.events
			}
			for name, value := range files {
				if name != tt.missing {
					require.NoError(t, os.WriteFile(filepath.Join(group, name), []byte(value), 0o600), "fixture should provide finite per-owned-cgroup observations")
				}
			}
			if tt.missing == "group" {
				require.NoError(t, os.RemoveAll(group), "kernel fixtures may model a cgroup removed immediately after a failure")
			}
			if tt.linkedGroup {
				target := filepath.Join(root, "redirected")
				require.NoError(t, os.Rename(group, target), "valid-looking counter data may exist outside the owned cgroup")
				require.NoError(t, os.Symlink(target, group), "telemetry must reject redirected parent paths")
			}
			if tt.invalidID {
				containerID = "../" + containerID
			}
			actual := readRealReviewMemorySample(root, containerID)
			peak, current := int64(300), int64(0)
			expected := realReviewMemorySample{MemoryPeakBytes: &peak, MemoryCurrentBytes: &current, MemoryEvents: map[string]int64{"max": 3, "oom": 1, "oom_kill": 1}, UnavailableFields: tt.unavailable, MemoryStat: realReviewContractMemoryStat()}
			for _, unavailable := range tt.unavailable {
				switch unavailable {
				case "memory.peak":
					expected.MemoryPeakBytes = nil
				case "memory.current":
					expected.MemoryCurrentBytes = nil
				case "memory.events":
					expected.MemoryEvents = nil
				case "memory.stat":
					expected.MemoryStat = nil
				}
			}
			require.Equal(t, expected, actual, "missing fields must be explicit while available exact byte and OOM counters survive independently")
		})
	}
}

func TestRealReviewMemoryMonitorRetainsEvidenceAndJoins(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name            string
		vanished        bool
		expired         bool
		completeSamples int
		expectedPeak    int64
		expectedEvents  map[string]int64
	}{
		{name: "final OOM counters remain observable", completeSamples: 2, expectedPeak: 400, expectedEvents: map[string]int64{"max": 8, "oom": 2, "oom_kill": 2}},
		{name: "removed group retains previously observed OOM", vanished: true, completeSamples: 1, expectedPeak: 300, expectedEvents: map[string]int64{"max": 3, "oom": 1, "oom_kill": 1}},
		{name: "bounded lifetime ends sampler", expired: true, completeSamples: 2, expectedPeak: 400, expectedEvents: map[string]int64{"max": 8, "oom": 2, "oom_kill": 2}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			initialPeak, finalPeak, initialCurrent, finalCurrent := int64(300), int64(400), int64(200), int64(0)
			initial := realReviewMemorySample{MemoryPeakBytes: &initialPeak, MemoryCurrentBytes: &initialCurrent, MemoryEvents: map[string]int64{"max": 3, "oom": 1, "oom_kill": 1}, UnavailableFields: []string{}}
			final := realReviewMemorySample{MemoryPeakBytes: &finalPeak, MemoryCurrentBytes: &finalCurrent, MemoryEvents: map[string]int64{"max": 8, "oom": 2, "oom_kill": 2}, UnavailableFields: []string{}}
			if tt.vanished {
				final = realReviewMemorySample{UnavailableFields: []string{"memory.events", "memory.peak", "memory.current"}}
			}
			var calls atomic.Int64
			lifetime := time.Hour
			if tt.expired {
				lifetime = 10 * time.Millisecond
			}
			monitor := newRealReviewMemoryMonitor(strings.Repeat("a", 64), func() realReviewMemorySample {
				if calls.Add(1) == 1 {
					return initial
				}
				return final
			}, time.Hour, lifetime)
			t.Cleanup(func() { monitor.stop() })
			if tt.expired {
				select {
				case <-monitor.done:
				case <-time.After(time.Second):
					t.Fatal("finite monitor lifetime must terminate its goroutine")
				}
			}
			actual := monitor.stop()
			expectedPeak := tt.expectedPeak
			expected := realReviewMemoryTelemetry{CgroupPath: "/docker/" + strings.Repeat("a", 64), IntervalMS: time.Hour.Milliseconds(), MaxDurationMS: lifetime.Milliseconds(), ReadLimitsBytes: map[string]int64{"memory.peak": 128, "memory.current": 128, "memory.events": 4096, "memory.stat": 8192}, Samples: 2, CompleteSamples: tt.completeSamples, Latest: final, MaxObservedPeakBytes: &expectedPeak, MaxObservedCurrentBytes: &initialCurrent, MaxObservedEvents: tt.expectedEvents, LifetimeExpired: tt.expired, StatAtMaxCurrent: realReviewContractStatPeak(1, initialCurrent, nil)}
			require.Equal(t, expected, actual, "bounded telemetry must preserve earlier exact kernel observations even after the cgroup disappears")
			require.Equal(t, actual, monitor.stop(), "stopping twice must not restart reads or mutate the retained result")
			require.Equal(t, int64(2), calls.Load(), "joined sampler must perform only its initial and final reads when no interval fires")
			select {
			case <-monitor.done:
			default:
				t.Fatal("result serialization must follow monitor cancellation and join")
			}
		})
	}
}

func TestRealReviewMemoryMonitorRetainsPeriodicOOMBeforeRemoval(t *testing.T) {
	t.Parallel()
	reading, release := make(chan struct{}), make(chan struct{})
	var calls atomic.Int64
	peak, current := int64(300), int64(200)
	observedOOM := realReviewMemorySample{MemoryPeakBytes: &peak, MemoryCurrentBytes: &current, MemoryEvents: map[string]int64{"max": 8, "oom": 2, "oom_kill": 1}, UnavailableFields: []string{}}
	vanished := realReviewMemorySample{UnavailableFields: []string{"memory.events", "memory.peak", "memory.current"}}
	monitor := newRealReviewMemoryMonitor(strings.Repeat("a", 64), func() realReviewMemorySample {
		switch calls.Add(1) {
		case 1:
			return vanished
		case 2:
			close(reading)
			<-release
			return observedOOM
		default:
			return vanished
		}
	}, time.Millisecond, time.Minute)
	select {
	case <-reading:
	case <-time.After(time.Second):
		monitor.cancel()
		close(release)
		monitor.stop()
		t.Fatal("periodic monitor must read kernel evidence before final destruction")
	}
	monitor.cancel()
	close(release)
	actual := monitor.stop()
	expected := realReviewMemoryTelemetry{CgroupPath: "/docker/" + strings.Repeat("a", 64), IntervalMS: 1, MaxDurationMS: time.Minute.Milliseconds(), ReadLimitsBytes: map[string]int64{"memory.peak": 128, "memory.current": 128, "memory.events": 4096, "memory.stat": 8192}, Samples: 3, CompleteSamples: 1, Latest: vanished, MaxObservedPeakBytes: &peak, MaxObservedCurrentBytes: &current, MaxObservedEvents: map[string]int64{"max": 8, "oom": 2, "oom_kill": 1}, StatAtMaxCurrent: realReviewContractStatPeak(2, current, nil)}
	require.Equal(t, expected, actual, "a periodic OOM observation must survive a missing initial and final cgroup without retaining a timeline")
	require.Equal(t, int64(3), calls.Load(), "cancellation must join the in-flight sample and prevent further ticker reads")
}

func TestRealReviewDockerFailureStateRedaction(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		state    *container.State
		expected realReviewDockerState
	}{
		{name: "inspection unavailable", expected: realReviewDockerState{Status: "unavailable", ErrorClassification: "inspection_unavailable"}},
		{name: "running state", state: &container.State{Status: "running", Running: true, Pid: 70201}, expected: realReviewDockerState{Available: true, Running: true, PID: 70201, Status: "running", ErrorClassification: "none"}},
		{name: "explicit Docker OOM state without raw error", state: &container.State{Status: "exited", OOMKilled: true, ExitCode: 137, Error: "private-token: runtime detail"}, expected: realReviewDockerState{Available: true, OOMKilled: true, ExitCode: 137, Status: "exited", ErrorClassification: "reported_without_details"}},
		{name: "untrusted status text", state: &container.State{Status: "private-token", ExitCode: 128, Error: "private-token: runtime detail"}, expected: realReviewDockerState{Available: true, ExitCode: 128, Status: "unrecognized", ErrorClassification: "reported_without_details"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			actual := sanitizeRealReviewDockerState(tt.state)
			require.Equal(t, tt.expected, actual, "failure evidence must retain exact numeric and Boolean Docker state with fixed classifications")
			encoded, err := json.Marshal(actual)
			require.NoError(t, err, "sanitized Docker state should encode as finite private evidence")
			require.NotContains(t, string(encoded), "private-token", "raw Docker error and unexpected status text must never reach retained artifacts")
		})
	}
}

func TestRealReviewProfileDecoderPreservesDrainAndRedacts(t *testing.T) {
	t.Parallel()
	enter := realReviewProfilePrefix + "{\"version\":1,\"op\":1,\"phase\":2,\"category\":2}\n"
	exit := realReviewProfilePrefix + "{\"version\":1,\"op\":2,\"phase\":2,\"category\":2,\"duration_us\":100,\"failed\":0,\"bytes\":50}\n"
	tests := []struct {
		name       string
		raw        string
		wantReason string
	}{
		{name: "fragmented fixed numeric records", raw: "ordinary stdout\n" + enter + exit},
		{name: "missing profile", raw: "ordinary stdout only\n", wantReason: "missing_profile_records"},
		{name: "untrusted text", raw: realReviewProfilePrefix + "{\"version\":1,\"op\":1,\"phase\":\"private-token\",\"category\":2}\n", wantReason: "invalid_record"},
		{name: "unknown field", raw: realReviewProfilePrefix + "{\"version\":1,\"op\":1,\"phase\":2,\"category\":2,\"path\":123}\n", wantReason: "invalid_record"},
		{name: "duplicate key", raw: realReviewProfilePrefix + "{\"version\":1,\"op\":1,\"phase\":1,\"phase\":2,\"category\":2}\n", wantReason: "invalid_record"},
		{name: "non-numeric null", raw: realReviewProfilePrefix + "{\"version\":1,\"op\":1,\"phase\":2,\"category\":2,\"bytes\":null}\n", wantReason: "invalid_record"},
		{name: "unmatched exit", raw: exit, wantReason: "unmatched_exit"},
		{name: "unfinished operation", raw: enter, wantReason: "incomplete_phase_exits"},
		{name: "truncated diagnostic line", raw: strings.TrimSuffix(enter, "\n"), wantReason: "truncated_record"},
		{name: "oversized line", raw: realReviewProfilePrefix + strings.Repeat("x", 1025) + "\n", wantReason: "record_size_limit"},
		{name: "bounded record count", raw: strings.Repeat(enter+exit, 10000) + enter, wantReason: "record_count_limit"},
		{name: "bounded simultaneous operations", raw: strings.Repeat(enter, 65), wantReason: "active_count_limit"},
		{name: "launcher hash rejection", raw: realReviewProfilePrefix + "{\"version\":1,\"op\":3,\"phase\":0,\"category\":0}\n", wantReason: "launcher_invalid"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			profile := &realReviewSourceProfile{}
			decoder := &realReviewProfileDecoder{profile: profile}
			provider := &realReviewProvider{logs: realReviewLogBuffer("private-token"), surfaces: realReviewLogBuffer("private-token")}
			var forwarded strings.Builder
			writer := io.MultiWriter(decoder, provider.observedOutput(&forwarded))
			for offset := 0; offset < len(tt.raw); offset += 7 {
				end := min(offset+7, len(tt.raw))
				count, err := writer.Write([]byte(tt.raw[offset:end]))
				require.NoError(t, err, "diagnostic parse failure must never interrupt original output draining")
				require.Equal(t, end-offset, count, "every original byte must still reach its forwarding and credential-scan surface")
			}
			decoder.finish()
			require.Equal(t, tt.raw, forwarded.String(), "profiling must preserve exact original stdout bytes")
			result := profile.result()
			require.Equal(t, tt.wantReason, result.InvalidReason, "profile validity must explicitly distinguish unavailable or unsafe attribution")
			require.LessOrEqual(t, result.Records, 20000, "retained profile count must obey the fixed record cap")
			encoded, err := json.Marshal(result)
			require.NoError(t, err, "fixed numeric profile aggregates should encode without raw output")
			require.NotContains(t, string(encoded), "private-token", "untrusted profile text must never be retained in diagnostics")
			if strings.Contains(tt.raw, "private-token") {
				require.True(t, provider.credentialDetected(), "diagnostic decoding must leave the complete credential scanner active")
			}
			if tt.wantReason == "" {
				bytes := int64(50)
				expected := map[string]realReviewProfileAggregate{"download/npm": {Entered: 1, Completed: 1, MaxActive: 1, DurationUS: 100, Bytes: &bytes}}
				require.Equal(t, expected, result.Phases, "valid fragments must retain only exact fixed category counts and numeric measurements")
			}
		})
	}
}

func TestRealReviewProfileOverlapUsesSharedContainerSamples(t *testing.T) {
	t.Parallel()
	profile := &realReviewSourceProfile{}
	for _, record := range []string{
		`{"version":1,"op":1,"phase":2,"category":2}`,
		`{"version":1,"op":1,"phase":2,"category":2}`,
		`{"version":1,"op":1,"phase":3,"category":3}`,
	} {
		profile.consume([]byte(record))
	}
	current, cumulativePeak := int64(100), int64(9999)
	profile.observeMemory(realReviewMemorySample{MemoryCurrentBytes: &current, MemoryPeakBytes: &cumulativePeak, MemoryEvents: map[string]int64{"max": 3, "oom": 0, "oom_kill": 0}, MemoryStat: map[string]int64{"anon": 50, "file": 40, "file_dirty": 4, "file_writeback": 0}})
	current = 200
	profile.observeMemory(realReviewMemorySample{MemoryCurrentBytes: &current, MemoryPeakBytes: &cumulativePeak, MemoryEvents: map[string]int64{"max": 7, "oom": 0, "oom_kill": 0}, MemoryStat: map[string]int64{"anon": 80, "file": 60, "file_dirty": 0, "file_writeback": 2}})
	for _, record := range []string{
		`{"version":1,"op":2,"phase":2,"category":2,"duration_us":100,"failed":0,"bytes":10}`,
		`{"version":1,"op":2,"phase":2,"category":2,"duration_us":50,"failed":0,"bytes":20}`,
		`{"version":1,"op":2,"phase":3,"category":3,"duration_us":80,"failed":1}`,
	} {
		profile.consume([]byte(record))
	}
	bytes := int64(30)
	currentMaximum := int64(200)
	stat := map[string]int64{"anon": 80, "file": 60, "file_dirty": 4, "file_writeback": 2}
	deltas := map[string]int64{"max": 4, "oom": 0, "oom_kill": 0}
	expected := map[string]realReviewProfileAggregate{
		"download/npm":   {Entered: 2, Completed: 2, MaxActive: 2, DurationUS: 150, Bytes: &bytes, MemorySamples: 2, MaxCurrentBytes: &currentMaximum, MaxMemoryStat: stat, SampledEventDelta: deltas},
		"extract/python": {Entered: 1, Completed: 1, Failed: 1, MaxActive: 1, DurationUS: 80, MemorySamples: 2, MaxCurrentBytes: &currentMaximum, MaxMemoryStat: stat, SampledEventDelta: deltas},
	}
	result := profile.result()
	require.False(t, result.Invalid, "matched overlapping operations should provide valid diagnostic attribution")
	require.Equal(t, expected, result.Phases, "overlapping phases must share container-wide observations rather than claim exclusive resource ownership")
	require.Equal(t, "sum of completed operation durations; concurrent operations overlap; go_filter/go is nested within go_processing/go; cleanup operations may also nest within it; overlapping spans are non-additive; not elapsed phase wall time; incomplete phases retain active counts only", result.TimingScope, "concurrent durations must be labeled as completed operation time instead of elapsed phase wall time")
	encoded, err := json.Marshal(result)
	require.NoError(t, err, "overlap aggregates should encode using fixed data only")
	require.NotContains(t, string(encoded), "9999", "cumulative lifetime memory.peak must never become a per-phase peak")
}

func TestRealReviewProfileGoFilterFixedCategory(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		phase    int
		category int
		valid    bool
	}{
		{name: "fixed Go filter", phase: 11, category: 4, valid: true},
		{name: "zero category", phase: 11, category: 0},
		{name: "all category", phase: 11, category: 1},
		{name: "npm category", phase: 11, category: 2},
		{name: "python category", phase: 11, category: 3},
		{name: "unknown category", phase: 11, category: 5},
		{name: "unknown phase", phase: 12, category: 4},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			profile := &realReviewSourceProfile{}
			for _, op := range []int{1, 2} {
				record := map[string]int{"version": 1, "op": op, "phase": tt.phase, "category": tt.category}
				if op == 2 {
					record["duration_us"], record["failed"] = 7, 0
				}
				raw, err := json.Marshal(record)
				require.NoError(t, err, "fixed numeric filter records should serialize")
				profile.consume(raw)
			}
			result := profile.result()
			expectedReason := "invalid_record"
			expected := map[string]realReviewProfileAggregate{}
			if tt.valid {
				expectedReason = ""
				expected["go_filter/go"] = realReviewProfileAggregate{Entered: 1, Completed: 1, MaxActive: 1, DurationUS: 7}
			}
			require.Equal(t, expectedReason, result.InvalidReason, "only the fixed Go filter phase and category may extend the diagnostic whitelist")
			require.Equal(t, expected, result.Phases, "unsupported filter categories and phase IDs must not create aggregates")
		})
	}
}

func TestRealReviewProfileGoFilterNestedSampling(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		failed int64
	}{
		{name: "successful filter"},
		{name: "original filter failure", failed: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			profile := &realReviewSourceProfile{}
			profile.consume([]byte(`{"version":1,"op":1,"phase":5,"category":4}`))
			profile.consume([]byte(`{"version":1,"op":1,"phase":11,"category":4}`))
			start := time.Now()
			current := int64(200)
			monitor := &realReviewMemoryMonitor{profile: profile, started: start, now: func() time.Time { return start.Add(time.Second) }}
			monitor.record(realReviewMemorySample{MemoryCurrentBytes: &current})
			for _, operation := range []struct {
				phase    int
				duration int64
			}{{11, 20}, {5, 30}} {
				raw, err := json.Marshal(map[string]int64{"version": 1, "op": 2, "phase": int64(operation.phase), "category": 4, "duration_us": operation.duration, "failed": tt.failed})
				require.NoError(t, err, "balanced nested filter exits should serialize")
				profile.consume(raw)
			}
			expected := map[string]realReviewProfileAggregate{
				"go_processing/go": {Entered: 1, Completed: 1, Failed: tt.failed, MaxActive: 1, DurationUS: 30, MemorySamples: 1, MaxCurrentBytes: &current},
				"go_filter/go":     {Entered: 1, Completed: 1, Failed: tt.failed, MaxActive: 1, DurationUS: 20, MemorySamples: 1, MaxCurrentBytes: &current},
			}
			result := profile.result()
			require.Equal(t, "", result.InvalidReason, "balanced original failures should preserve valid nested timing observations")
			require.Equal(t, expected, result.Phases, "filter time must remain inside processing time and share the same existing memory sample")
			require.Equal(t, 1, monitor.retained.Samples, "filter instrumentation must not add kernel sampling passes")
			require.Equal(t, []realReviewMemoryTimelinePoint{{SampleNumber: 1, HostElapsedUS: 1000000, MemoryCurrentBytes: &current}}, monitor.retained.JointTimeline.Points, "the fixed filter phase must preserve the exact bounded joint timeline without invalidating or enriching its points")
			require.Contains(t, result.TimingScope, "go_filter/go is nested within go_processing/go", "filter time must explicitly remain non-additive to its enclosing processing span")
			require.Contains(t, result.TimingScope, "overlapping spans are non-additive", "timing scope must prohibit summing nested or concurrent observations")
		})
	}
}

func TestRealReviewProfileSamplingAndDecodingAreConcurrent(t *testing.T) {
	t.Parallel()
	profile := &realReviewSourceProfile{}
	peak, current := int64(300), int64(200)
	monitor := newRealReviewMemoryMonitor(strings.Repeat("a", 64), func() realReviewMemorySample {
		return realReviewMemorySample{MemoryPeakBytes: &peak, MemoryCurrentBytes: &current, MemoryEvents: map[string]int64{"max": 0, "oom": 0, "oom_kill": 0}, MemoryStat: map[string]int64{"anon": 100, "file": 80, "file_dirty": 0, "file_writeback": 0}, UnavailableFields: []string{}}
	}, time.Millisecond, time.Minute, profile)
	t.Cleanup(func() { monitor.stop() })
	for range 1000 {
		profile.consume([]byte(`{"version":1,"op":1,"phase":2,"category":2}`))
		profile.consume([]byte(`{"version":1,"op":2,"phase":2,"category":2,"duration_us":1,"failed":0}`))
	}
	monitor.stop()
	result := profile.result()
	require.False(t, result.Invalid, "concurrent numeric decoding and host sampling should remain valid after monitor join")
	cell := result.Phases["download/npm"]
	actual := []int64{int64(result.Records), cell.Entered, cell.Completed, int64(cell.Active), int64(cell.MaxActive), cell.DurationUS}
	require.Equal(t, []int64{2000, 1000, 1000, 0, 1, 1000}, actual, "sampling must preserve exact operation counts and timing without races or lost decoder events")
}

const realReviewContractMemoryStatRaw = "anon 10\nfile 20\nshmem 15\nfile_mapped 4\nfile_dirty 3\nfile_writeback 0\nslab 2\nkernel 5\n"

func realReviewContractMemoryStat() map[string]int64 {
	return map[string]int64{"anon": 10, "file": 20, "shmem": 15, "file_mapped": 4, "file_dirty": 3, "file_writeback": 0, "slab": 2, "kernel": 5}
}

func realReviewContractStatPeak(number int, current int64, stat map[string]int64) *realReviewMemoryStatAtCurrentPeak {
	return &realReviewMemoryStatAtCurrentPeak{SampleNumber: number, MemoryCurrentBytes: current, MemoryStat: stat, MemoryStatUnavailable: stat == nil, ObservationScope: realReviewMemoryStatScope}
}

func realReviewContractMemorySample(current int64, stat map[string]int64) realReviewMemorySample {
	peak := int64(9999)
	unavailable := []string{}
	if stat == nil {
		unavailable = []string{"memory.stat"}
	}
	return realReviewMemorySample{MemoryPeakBytes: &peak, MemoryCurrentBytes: &current, MemoryEvents: map[string]int64{"max": 0, "oom": 0, "oom_kill": 0}, MemoryStat: stat, UnavailableFields: unavailable}
}

func TestRealReviewMemoryPeakStatUsesSameSamplingPass(t *testing.T) {
	t.Parallel()
	first, highest, later := realReviewContractMemoryStat(), realReviewContractMemoryStat(), realReviewContractMemoryStat()
	first["file_dirty"], highest["anon"], later["file"] = 99, 30, 77
	tests := []struct {
		name            string
		samples         []realReviewMemorySample
		peak            *realReviewMemoryStatAtCurrentPeak
		maxima          map[string]int64
		statSamples     int
		completeSamples int
	}{
		{name: "peak vector is not per-field maxima", samples: []realReviewMemorySample{realReviewContractMemorySample(100, first), realReviewContractMemorySample(300, highest), realReviewContractMemorySample(200, later)}, peak: realReviewContractStatPeak(2, 300, highest), maxima: map[string]int64{"anon": 30, "file": 77, "shmem": 15, "file_mapped": 4, "file_dirty": 99, "file_writeback": 0, "slab": 2, "kernel": 5}, statSamples: 3, completeSamples: 3},
		{name: "missing stat at higher current never borrows earlier vector", samples: []realReviewMemorySample{realReviewContractMemorySample(100, first), realReviewContractMemorySample(300, nil)}, peak: realReviewContractStatPeak(2, 300, nil), maxima: first, statSamples: 1, completeSamples: 2},
		{name: "tie retains original unavailable vector", samples: []realReviewMemorySample{realReviewContractMemorySample(300, nil), realReviewContractMemorySample(300, highest)}, peak: realReviewContractStatPeak(1, 300, nil), maxima: highest, statSamples: 1, completeSamples: 2},
		{name: "disappearance preserves earlier peak but latest remains unavailable", samples: []realReviewMemorySample{realReviewContractMemorySample(300, highest), {UnavailableFields: []string{"memory.events", "memory.peak", "memory.current", "memory.stat"}}}, peak: realReviewContractStatPeak(1, 300, highest), maxima: highest, statSamples: 1, completeSamples: 1},
		{name: "stat without current cannot establish peak association", samples: []realReviewMemorySample{{MemoryStat: first, UnavailableFields: []string{"memory.events", "memory.peak", "memory.current"}}}, maxima: first, statSamples: 1},
		{name: "lower current missing stat does not erase observed peak", samples: []realReviewMemorySample{realReviewContractMemorySample(300, highest), realReviewContractMemorySample(0, nil)}, peak: realReviewContractStatPeak(1, 300, highest), maxima: highest, statSamples: 1, completeSamples: 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			monitor := &realReviewMemoryMonitor{}
			for _, sample := range tt.samples {
				monitor.record(sample)
			}
			require.Equal(t, tt.peak, monitor.retained.StatAtMaxCurrent, "one peak vector must belong to the exact highest-current pass, including unavailable stats")
			require.Equal(t, tt.maxima, monitor.retained.MaxObservedStat, "independent historical maxima remain explicitly distinct from the peak vector")
			require.Equal(t, tt.statSamples, monitor.retained.StatSamples, "optional stat coverage must be counted separately from core evidence")
			require.Equal(t, tt.completeSamples, monitor.retained.CompleteSamples, "missing optional stat must not invalidate otherwise complete core telemetry")
			require.Equal(t, tt.samples[len(tt.samples)-1], monitor.retained.Latest, "the latest unavailable observation must not inherit prior values")
			encoded, err := json.Marshal(monitor.retained)
			require.NoError(t, err, "fixed-size peak observations should serialize without a sample timeline")
			if tt.peak != nil && tt.peak.MemoryStatUnavailable {
				require.Contains(t, string(encoded), `"memory_stat":null,"memory_stat_unavailable":true`, "unavailable highest-current stats must serialize explicitly rather than borrow a past vector")
			}
		})
	}
}

func TestRealReviewMemoryMonitorOwnsRetainedAndReturnedVectors(t *testing.T) {
	t.Parallel()
	sample := realReviewContractMemorySample(300, realReviewContractMemoryStat())
	monitor := &realReviewMemoryMonitor{}
	monitor.record(sample)
	*sample.MemoryCurrentBytes, *sample.MemoryPeakBytes = 999, 888
	sample.MemoryStat["anon"], sample.MemoryEvents["oom"] = 777, 6
	sample.UnavailableFields = append(sample.UnavailableFields, "memory.events")
	require.Equal(t, realReviewContractMemorySample(300, realReviewContractMemoryStat()), monitor.retained.Latest, "retained latest must own its maps, pointers and field labels")
	require.Equal(t, realReviewContractStatPeak(1, 300, realReviewContractMemoryStat()), monitor.retained.StatAtMaxCurrent, "peak vector must remain frozen after its source sample is mutated")
	absent := realReviewContractMemorySample(300, nil)
	missing := &realReviewMemoryMonitor{}
	missing.record(absent)
	absent.UnavailableFields[0] = "memory.events"
	require.Equal(t, []string{"memory.stat"}, missing.retained.Latest.UnavailableFields, "unavailable field labels must also be copied rather than alias the read result")

	joined := newRealReviewMemoryMonitor(strings.Repeat("a", 64), func() realReviewMemorySample {
		return realReviewContractMemorySample(300, realReviewContractMemoryStat())
	}, time.Hour, time.Minute)
	t.Cleanup(func() { joined.stop() })
	original := joined.stop()
	changed := joined.stop()
	changed.Latest.MemoryStat["anon"] = 1000
	changed.Latest.MemoryEvents["oom"] = 1000
	changed.Latest.UnavailableFields = append(changed.Latest.UnavailableFields, "memory.events")
	*changed.Latest.MemoryCurrentBytes, *changed.MaxObservedCurrentBytes, *changed.MaxObservedPeakBytes = 1000, 1000, 1000
	changed.MaxObservedStat["anon"], changed.MaxObservedEvents["oom"] = 1000, 1000
	changed.ReadLimitsBytes["memory.stat"] = 1
	changed.StatAtMaxCurrent.MemoryStat["anon"], changed.StatAtMaxCurrent.MemoryCurrentBytes = 1000, 1000
	require.Equal(t, original, joined.stop(), "repeated result serialization must not expose monitor-owned mutable state")
}

func TestRealReviewNormalMemoryMonitorConcurrentResultCopies(t *testing.T) {
	t.Parallel()
	monitor := newRealReviewMemoryMonitor(strings.Repeat("a", 64), func() realReviewMemorySample {
		return realReviewContractMemorySample(300, realReviewContractMemoryStat())
	}, time.Millisecond, time.Minute)
	t.Cleanup(func() { monitor.stop() })
	results := make([]realReviewMemoryTelemetry, 8)
	var writers sync.WaitGroup
	for i := range results {
		writers.Add(1)
		go func() {
			defer writers.Done()
			results[i] = monitor.stop()
			results[i].StatAtMaxCurrent.MemoryStat["anon"] = int64(i)
			results[i].Latest.MemoryStat["anon"] = int64(i)
		}()
	}
	writers.Wait()
	actual := monitor.stop()
	require.Equal(t, realReviewContractMemoryStat(), actual.StatAtMaxCurrent.MemoryStat, "concurrent private result copies must never alias the joined monitor")
	require.Equal(t, actual.Samples, actual.StatSamples, "every available normal sample should count toward optional stat coverage")
	require.Equal(t, actual.Samples, actual.CompleteSamples, "normal optional stats preserve core telemetry completeness")
	encoded, err := json.Marshal(actual)
	require.NoError(t, err, "the joined normal aggregate should remain serializable after concurrent readers")
	require.NotContains(t, string(encoded), "private-token", "retained fields must contain finite allowlisted observations only")
}

func TestRealReviewMemoryStatIsOptionalInNormalAcceptance(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		raw       string
		wantError bool
	}{
		{name: "normal acceptance reads finite allowlisted values", raw: "anon 10\nfile 20\nshmem 15\nfile_mapped 4\nfile_dirty 3\nfile_writeback 0\nslab 2\nkernel 5\nother_counter 4\n"},
		{name: "missing stat", wantError: true},
		{name: "incomplete stat", raw: "anon 10\nfile 20\nfile_dirty 3\nfile_writeback 0\n", wantError: true},
		{name: "invalid stat", raw: "anon private-token\n", wantError: true},
		{name: "oversized stat", raw: strings.Repeat("x", 8193), wantError: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			root, err := filepath.EvalSymlinks(t.TempDir())
			require.NoError(t, err, "stat fixtures must have canonical host roots")
			containerID := strings.Repeat("a", 64)
			group := filepath.Join(root, "docker", containerID)
			require.NoError(t, os.MkdirAll(group, 0o700), "optional stat must come from the exact owned container cgroup")
			for name, value := range map[string]string{"memory.events": "max 0\noom 0\noom_kill 0\n", "memory.peak": "100\n", "memory.current": "50\n"} {
				require.NoError(t, os.WriteFile(filepath.Join(group, name), []byte(value), 0o600), "ordinary resource fields should remain readable")
			}
			if tt.raw != "" {
				require.NoError(t, os.WriteFile(filepath.Join(group, "memory.stat"), []byte(tt.raw), 0o600), "optional fixture may contain available or rejected kernel data")
			}
			actual := readRealReviewMemorySample(root, containerID)
			peak, current := int64(100), int64(50)
			expected := realReviewMemorySample{MemoryPeakBytes: &peak, MemoryCurrentBytes: &current, MemoryEvents: map[string]int64{"max": 0, "oom": 0, "oom_kill": 0}, UnavailableFields: []string{}, MemoryStat: realReviewContractMemoryStat()}
			if tt.wantError {
				expected.UnavailableFields = []string{"memory.stat"}
				expected.MemoryStat = nil
			}
			require.Equal(t, expected, actual, "normal stat failures must be explicit while core kernel evidence remains intact")
		})
	}
}

func TestRealReviewMemoryStatStrictFiniteWhitelist(t *testing.T) {
	t.Parallel()
	valid := "anon 10\nfile 20\nshmem 15\nfile_mapped 4\nfile_dirty 3\nfile_writeback 0\nslab 2\nkernel 5\n"
	tests := []struct {
		name      string
		raw       string
		wantError bool
	}{
		{name: "eight independent observations", raw: valid},
		{name: "unknown numeric field is not retained", raw: valid + "new_kernel_counter 9\n"},
		{name: "exact byte bound", raw: valid + strings.Repeat(" ", 8192-len(valid))},
		{name: "over byte bound", raw: valid + strings.Repeat(" ", 8193-len(valid)), wantError: true},
		{name: "missing required field", raw: strings.ReplaceAll(valid, "shmem 15\n", ""), wantError: true},
		{name: "duplicate known field", raw: valid + "anon 10\n", wantError: true},
		{name: "duplicate unknown field", raw: valid + "new_kernel_counter 9\nnew_kernel_counter 9\n", wantError: true},
		{name: "negative value", raw: strings.ReplaceAll(valid, "anon 10", "anon -1"), wantError: true},
		{name: "overflow value", raw: strings.ReplaceAll(valid, "anon 10", "anon 9223372036854775808"), wantError: true},
		{name: "unknown malformed value", raw: valid + "new_kernel_counter private-token\n", wantError: true},
		{name: "empty stat", wantError: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			actual, err := realReviewMemoryStat([]byte(tt.raw))
			if tt.wantError {
				require.Error(t, err, "incomplete or ambiguous stat vectors must not become numerical proof")
				require.Nil(t, actual, "rejected stats must not retain partial numerical data")
				return
			}
			require.NoError(t, err, "bounded finite stat vectors may ignore future numerical kernel fields")
			require.Equal(t, realReviewContractMemoryStat(), actual, "only eight independent fields may be retained without overlapping-field sums")
		})
	}
}

func TestRealReviewProfileObserverKeepsOriginalAuthGuard(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		auth bool
	}{
		{name: "released authentication permits exact Popen interception"},
		{name: "unreleased authentication still triggers original identity guard", auth: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			trace := filepath.Join(root, "private-trace")
			script := "set -eu\npython3 .143/review-sources.py\n"
			encodedScript, err := json.Marshal(script)
			require.NoError(t, err, "fixed helper command should encode for the original-identity fixture")
			fixture := "import json,os,pathlib,stat,subprocess,time\np={\"prepare\":" + string(encodedScript) + "}\nauth="
			if tt.auth {
				fixture += "object()\n"
			} else {
				fixture += "None\n"
			}
			fixture += `
def original_popen(args,**kwargs):
    assert kwargs == {"cwd":"/literal-workdir","env":{"literal":"preserved"},"stdin":subprocess.DEVNULL,"close_fds":True,"start_new_session":True}
    assert args[:2] == ["/bin/sh","-c"]
    assert args[2] != p["prepare"]
    assert "python3 -c " in args[2]
    assert ".143/review-sources.py" in args[2]
    class Child: pid=999999
    return Child()
subprocess.Popen=original_popen
def run(script,required=True,authenticate=False,platform=False):
    assert script is p["prepare"]
    if auth is not None and script in (p.get("prepare"),p.get("verify_recipe")):
        raise RuntimeError("original authentication release guard")
    subprocess.Popen(["/bin/sh","-c",script],cwd="/literal-workdir",env={"literal":"preserved"},stdin=subprocess.DEVNULL,close_fds=True,start_new_session=True)
    assert script is p["prepare"]
    return "original-result"
def capture(args): return b"ok"
def check_workspace_quarantine(): pass
def read_preparation_input(): return None
try:
    check_workspace_quarantine()
    message = read_preparation_input()
    assert run(p["prepare"]) == "original-result"
except RuntimeError:
    raise SystemExit(17)
`
			command, err := instrumentRealReviewProfileCommand(realReviewPython(fixture), true)
			require.NoError(t, err, "opt-in profile observer should preserve the production wrapper's trusted anchor")
			encodedTrace, err := json.Marshal(trace)
			require.NoError(t, err, "isolated trace path should encode")
			command = strings.ReplaceAll(command, `"/tmp/143-real-review-observation.jsonl"`, string(encodedTrace))
			output, runErr := exec.Command("/bin/sh", "-c", command).CombinedOutput()
			if tt.auth {
				var exitErr *exec.ExitError
				require.ErrorAs(t, runErr, &exitErr, "unreleased authentication must fail before any Popen interception")
				require.Equal(t, 17, exitErr.ExitCode(), "observer must preserve the original auth guard's exception path")
			} else {
				require.NoError(t, runErr, "exact Popen copy may change while the original function sees its unchanged script: %s", output)
			}
		})
	}
}

func TestRealReviewFailureTraceRetainsOnlyFixedMetadata(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		raw       string
		expected  []realReviewTrace
		wantError bool
	}{
		{name: "uninitialized failed container", expected: []realReviewTrace{}},
		{name: "fixed failed preparation timings", raw: "{\"stage\":\"repository_setup\",\"elapsed_ms\":120,\"authenticated\":false}\n{\"stage\":\"process_group\",\"pid\":70201,\"group\":70201,\"start\":\"99999\"}\n",
			expected: []realReviewTrace{{Stage: "repository_setup", ElapsedMS: 120}, {Stage: "process_group", PID: 70201, Group: 70201, Start: "99999"}}},
		{name: "untrusted stage text", raw: `{"stage":"private-token","elapsed_ms":1}`, wantError: true},
		{name: "untrusted command field", raw: `{"stage":"repository_setup","command":"private-token"}`, wantError: true},
		{name: "untrusted process identity text", raw: `{"stage":"process_group","pid":70201,"group":70201,"start":"private-token"}`, wantError: true},
		{name: "multiple objects on one line", raw: `{"stage":"repository_setup"} {"stage":"tool_repair"}`, wantError: true},
		{name: "negative stage duration", raw: `{"stage":"repository_setup","elapsed_ms":-1}`, wantError: true},
		{name: "process identity on stage timing", raw: `{"stage":"repository_setup","start":"99999"}`, wantError: true},
		{name: "oversized observation", raw: strings.Repeat("x", 1024*1024+1), wantError: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			actual, err := decodeRealReviewTrace([]byte(tt.raw))
			if tt.wantError {
				require.Error(t, err, "failed-container diagnostic metadata must reject arbitrary text or oversized input")
				require.Nil(t, actual, "rejected trace bytes must never be retained in private evidence")
				return
			}
			require.NoError(t, err, "bounded fixed observer entries should survive preparation failure")
			require.Equal(t, tt.expected, actual, "failed-container diagnostics must preserve only exact stage timing and numeric process identity")
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

func TestRealReviewProfileCPUNonexclusiveObservation(t *testing.T) {
	t.Parallel()
	profile := &realReviewSourceProfile{}
	profile.consume([]byte(`{"version":1,"op":1,"phase":5,"category":4}`))
	profile.consume([]byte(`{"version":1,"op":1,"phase":11,"category":4}`))
	for _, raw := range []string{`{"cpu_usage_usec":10}`, `{"cpu_usage_usec":30}`} {
		var sample realReviewMemorySample
		require.NoError(t, json.Unmarshal([]byte(raw), &sample), "finite numeric CPU observations should decode into the owned sample")
		profile.observeMemory(sample)
	}
	result := profile.result()
	require.Equal(t, "incomplete_phase_exits", result.InvalidReason, "failed cold observation must not manufacture helper completion")
	for _, key := range []string{"go_processing/go", "go_filter/go"} {
		encoded, err := json.Marshal(result.Phases[key])
		require.NoError(t, err, "partial CPU coactivity observations should serialize")
		var fields map[string]any
		require.NoError(t, json.Unmarshal(encoded, &fields), "fixed CPU evidence should expose only numeric measurements")
		require.Equal(t, float64(20), fields["nonexclusive_sampled_container_cpu_delta_usec"], "each overlapping active phase must receive the same whole-container adjacent CPU delta")
	}
}

func TestRealReviewCPUUsageStrictFiniteWhitelist(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		raw       string
		expected  int64
		wantError bool
	}{
		{name: "zero is available", raw: "usage_usec 0\n"},
		{name: "finite usage with unretained counters", raw: "usage_usec 12\nuser_usec 7\nsystem_usec 5\n", expected: 12},
		{name: "largest finite counter", raw: "usage_usec 9223372036854775807\n", expected: math.MaxInt64},
		{name: "exact bound", raw: "usage_usec 1\n" + strings.Repeat(" ", 4096-len("usage_usec 1\n")), expected: 1},
		{name: "over bound", raw: "usage_usec 1\n" + strings.Repeat(" ", 4097-len("usage_usec 1\n")), wantError: true},
		{name: "empty", wantError: true},
		{name: "missing required counter", raw: "user_usec 1\n", wantError: true},
		{name: "duplicate required counter", raw: "usage_usec 1\nusage_usec 1\n", wantError: true},
		{name: "duplicate unretained counter", raw: "usage_usec 1\nother 2\nother 2\n", wantError: true},
		{name: "negative", raw: "usage_usec -1\n", wantError: true},
		{name: "signed", raw: "usage_usec +1\n", wantError: true},
		{name: "overflow", raw: "usage_usec 9223372036854775808\n", wantError: true},
		{name: "fraction", raw: "usage_usec 1.5\n", wantError: true},
		{name: "malformed unretained value", raw: "usage_usec 1\nother private-token\n", wantError: true},
		{name: "extra columns", raw: "usage_usec 1 2\n", wantError: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			actual, err := realReviewCPUUsage([]byte(tt.raw))
			if tt.wantError {
				require.Error(t, err, "unsafe CPU counter bytes must be explicitly unavailable")
				require.Nil(t, actual, "malformed CPU values must never become a zero or retained raw evidence")
				require.NotContains(t, err.Error(), "private-token", "fixed parser errors must not disclose untrusted counter text")
				return
			}
			require.NoError(t, err, "one finite usage counter should parse independently of other numeric fields")
			require.Equal(t, &tt.expected, actual, "only the exact usage_usec value should survive strict parsing")
		})
	}
}

func TestRealReviewOwnedCPUObservation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		raw         string
		enabled     bool
		linked      bool
		expected    *int64
		unavailable []string
	}{
		{name: "profile numeric zero", raw: "usage_usec 0\n", enabled: true, expected: realReviewTimelineInt64(0), unavailable: []string{}},
		{name: "profile finite usage", raw: "usage_usec 17\nuser_usec 7\n", enabled: true, expected: realReviewTimelineInt64(17), unavailable: []string{}},
		{name: "missing CPU stays optional", enabled: true, unavailable: []string{"cpu.stat"}},
		{name: "malformed CPU stays optional", raw: "usage_usec private-token\n", enabled: true, unavailable: []string{"cpu.stat"}},
		{name: "bounded CPU read", raw: "usage_usec 1\n" + strings.Repeat(" ", 4096), enabled: true, unavailable: []string{"cpu.stat"}},
		{name: "no redirected CPU read", raw: "usage_usec 17\n", enabled: true, linked: true, unavailable: []string{"cpu.stat"}},
		{name: "normal does not read CPU", raw: "usage_usec private-token\n", unavailable: []string{}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			root, err := filepath.EvalSymlinks(t.TempDir())
			require.NoError(t, err, "CPU fixtures must have canonical owned roots")
			containerID := strings.Repeat("a", 64)
			group := filepath.Join(root, "docker", containerID)
			require.NoError(t, os.MkdirAll(group, 0o700), "CPU observations must stay under the verified container cgroup")
			for name, value := range map[string]string{"memory.events": "max 0\noom 0\noom_kill 0\n", "memory.peak": "100\n", "memory.current": "50\n", "memory.stat": realReviewContractMemoryStatRaw} {
				require.NoError(t, os.WriteFile(filepath.Join(group, name), []byte(value), 0o600), "memory fixture remains independent of optional CPU evidence")
			}
			if tt.raw != "" {
				target := filepath.Join(group, "cpu.stat")
				if tt.linked {
					target = filepath.Join(root, "redirected")
					require.NoError(t, os.Symlink(target, filepath.Join(group, "cpu.stat")), "valid-looking CPU bytes through a symlink must be rejected")
				}
				require.NoError(t, os.WriteFile(target, []byte(tt.raw), 0o600), "optional CPU fixture may contain valid or unsafe values")
			}
			actual := readRealReviewMemorySample(root, containerID, tt.enabled)
			expected := realReviewMemorySample{CPUUsageUS: tt.expected, MemoryPeakBytes: realReviewTimelineInt64(100), MemoryCurrentBytes: realReviewTimelineInt64(50), MemoryEvents: map[string]int64{"max": 0, "oom": 0, "oom_kill": 0}, MemoryStat: realReviewContractMemoryStat(), UnavailableFields: tt.unavailable}
			require.Equal(t, expected, actual, "CPU failures must retain complete independent memory evidence and an explicit fixed unavailable label")
			monitor := &realReviewMemoryMonitor{}
			monitor.record(actual)
			require.Equal(t, 1, monitor.retained.CompleteSamples, "optional CPU failures must preserve ordinary memory completeness")
			encoded, err := json.Marshal(actual)
			require.NoError(t, err, "owned CPU sample should encode finite evidence")
			require.NotContains(t, string(encoded), "private-token", "raw cpu.stat must never be serialized")
			if !tt.enabled {
				require.NotContains(t, string(encoded), "cpu_", "normal sample JSON must remain unchanged")
			}
		})
	}
}

func TestRealReviewProfileCPUIntervalBoundaries(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		samples  []*int64
		reenter  bool
		twice    bool
		expected realReviewProfileAggregate
	}{
		{name: "baseline only", samples: []*int64{realReviewTimelineInt64(10)}, expected: realReviewProfileAggregate{Entered: 1, Active: 1, MaxActive: 1, MemorySamples: 1}},
		{name: "zero delta available", samples: []*int64{realReviewTimelineInt64(0), realReviewTimelineInt64(0)}, expected: realReviewProfileAggregate{Entered: 1, Active: 1, MaxActive: 1, MemorySamples: 2, SampledCPUUsageUS: realReviewTimelineInt64(0), CPUIntervals: 1}},
		{name: "adjacent deltas", samples: []*int64{realReviewTimelineInt64(0), realReviewTimelineInt64(10), realReviewTimelineInt64(30)}, expected: realReviewProfileAggregate{Entered: 1, Active: 1, MaxActive: 1, MemorySamples: 3, SampledCPUUsageUS: realReviewTimelineInt64(30), CPUIntervals: 2}},
		{name: "gap and recovery baseline", samples: []*int64{realReviewTimelineInt64(10), nil, nil, realReviewTimelineInt64(30), realReviewTimelineInt64(50)}, expected: realReviewProfileAggregate{Entered: 1, Active: 1, MaxActive: 1, MemorySamples: 5, SampledCPUUsageUS: realReviewTimelineInt64(20), CPUIntervals: 1, CPUGaps: 1}},
		{name: "missing initial baseline", samples: []*int64{nil, realReviewTimelineInt64(30), realReviewTimelineInt64(50)}, expected: realReviewProfileAggregate{Entered: 1, Active: 1, MaxActive: 1, MemorySamples: 3, SampledCPUUsageUS: realReviewTimelineInt64(20), CPUIntervals: 1}},
		{name: "decrease rebaselines", samples: []*int64{realReviewTimelineInt64(100), realReviewTimelineInt64(10), realReviewTimelineInt64(15)}, expected: realReviewProfileAggregate{Entered: 1, Active: 1, MaxActive: 1, MemorySamples: 3, SampledCPUUsageUS: realReviewTimelineInt64(5), CPUIntervals: 1, CPUGaps: 1}},
		{name: "overflow addition omitted", samples: []*int64{realReviewTimelineInt64(0), realReviewTimelineInt64(math.MaxInt64), realReviewTimelineInt64(0), realReviewTimelineInt64(1)}, expected: realReviewProfileAggregate{Entered: 1, Active: 1, MaxActive: 1, MemorySamples: 4, SampledCPUUsageUS: realReviewTimelineInt64(math.MaxInt64), CPUIntervals: 1, CPUGaps: 2}},
		{name: "inactive gap is not bridged", samples: []*int64{realReviewTimelineInt64(10), realReviewTimelineInt64(30), realReviewTimelineInt64(100), realReviewTimelineInt64(140)}, reenter: true, expected: realReviewProfileAggregate{Entered: 2, Completed: 1, Active: 1, MaxActive: 1, DurationUS: 10, MemorySamples: 4, SampledCPUUsageUS: realReviewTimelineInt64(60), CPUIntervals: 2}},
		{name: "one delta per active key", samples: []*int64{realReviewTimelineInt64(10), realReviewTimelineInt64(30)}, twice: true, expected: realReviewProfileAggregate{Entered: 2, Active: 2, MaxActive: 2, MemorySamples: 2, SampledCPUUsageUS: realReviewTimelineInt64(20), CPUIntervals: 1}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			profile := &realReviewSourceProfile{}
			enter := []byte(`{"version":1,"op":1,"phase":11,"category":4}`)
			profile.consume(enter)
			if tt.twice {
				profile.consume(enter)
			}
			for i, usage := range tt.samples {
				if tt.reenter && i == 2 {
					profile.consume([]byte(`{"version":1,"op":2,"phase":11,"category":4,"duration_us":10,"failed":0}`))
					profile.consume(enter)
				}
				profile.observeMemory(realReviewMemorySample{CPUUsageUS: usage})
			}
			result := profile.result()
			require.Equal(t, "incomplete_phase_exits", result.InvalidReason, "sampling must never turn an active cold phase into completion")
			require.Equal(t, tt.expected, result.Phases["go_filter/go"], "CPU baseline, gap, reset and overflow handling must retain exact nonexclusive lower bounds")
			require.Equal(t, realReviewCPUScope, result.CPUScope, "CPU evidence must retain explicit nonexclusive coactivity and incomplete-window qualifications")
		})
	}
}

func TestRealReviewDiagnosticCPUWindowAndCopies(t *testing.T) {
	t.Parallel()
	start := time.Now()
	observed := start.Add(time.Second)
	profile := &realReviewSourceProfile{}
	monitor := &realReviewMemoryMonitor{profile: profile, started: start, now: func() time.Time { return observed }}
	usage := int64(100)
	monitor.record(realReviewMemorySample{CPUUsageUS: &usage})
	usage = 120
	observed = start.Add(2 * time.Second)
	monitor.record(realReviewMemorySample{CPUUsageUS: &usage})
	observed = start.Add(4 * time.Second)
	monitor.record(realReviewMemorySample{UnavailableFields: []string{"cpu.stat"}})
	usage = 10
	observed = start.Add(6 * time.Second)
	monitor.record(realReviewMemorySample{CPUUsageUS: &usage})
	expected := &realReviewCPUObservationTelemetry{Samples: 3, UnavailableSamples: 1, CounterDecreases: 1, First: &realReviewCPUObservation{SampleNumber: 1, HostElapsedUS: 1000000, UsageUS: 100}, Last: &realReviewCPUObservation{SampleNumber: 4, HostElapsedUS: 6000000, UsageUS: 10}, Scope: realReviewCPUScope}
	require.Equal(t, expected, monitor.retained.DiagnosticCPU, "retained CPU window must use actual host times and expose missing samples and counter resets")
	usage = 999
	require.Equal(t, int64(10), *monitor.retained.Latest.CPUUsageUS, "latest CPU must own its source pointer")
	changed := cloneRealReviewCPUObservation(monitor.retained.DiagnosticCPU)
	changed.First.UsageUS, changed.Last.UsageUS, changed.Samples = 999, 999, 999
	require.Equal(t, expected, monitor.retained.DiagnosticCPU, "returned CPU windows must own both observation pointers")
	one := cloneRealReviewCPUObservation(&realReviewCPUObservationTelemetry{First: expected.First, Last: expected.First})
	one.First.UsageUS = 999
	require.Equal(t, int64(100), one.Last.UsageUS, "even first and last for a single sample must not alias in returned copies")
}

func TestRealReviewDiagnosticCPUFinalReadAndNormalCompatibility(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name                string
		profile             bool
		missing             bool
		vanished            bool
		invalidProfile      bool
		expectedSamples     int
		expectedUnavailable int
		expectedLast        int64
		expectedComplete    int
	}{
		{name: "failed cold retains final CPU", profile: true, expectedSamples: 2, expectedLast: 140, expectedComplete: 2},
		{name: "missing CPU remains optional", profile: true, missing: true, expectedSamples: 1, expectedUnavailable: 1, expectedLast: 100, expectedComplete: 2},
		{name: "destroyed cgroup retains earlier CPU", profile: true, missing: true, vanished: true, expectedSamples: 1, expectedUnavailable: 1, expectedLast: 100, expectedComplete: 1},
		{name: "invalid helper profile retains independent CPU", profile: true, invalidProfile: true, expectedSamples: 2, expectedLast: 140, expectedComplete: 2},
		{name: "normal telemetry unchanged", expectedComplete: 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var profile *realReviewSourceProfile
			if tt.profile {
				profile = &realReviewSourceProfile{}
				profile.consume([]byte(`{"version":1,"op":1,"phase":11,"category":4}`))
			}
			var calls atomic.Int64
			monitor := newRealReviewMemoryMonitor(strings.Repeat("a", 64), func() realReviewMemorySample {
				sample := realReviewContractMemorySample(300, realReviewContractMemoryStat())
				call := calls.Add(1)
				if tt.vanished && call > 1 {
					return realReviewMemorySample{UnavailableFields: []string{"memory.events", "memory.peak", "memory.current", "memory.stat", "cpu.stat"}}
				}
				if tt.profile {
					if call == 1 {
						sample.CPUUsageUS = realReviewTimelineInt64(100)
					} else if tt.missing {
						sample.UnavailableFields = append(sample.UnavailableFields, "cpu.stat")
					} else {
						sample.CPUUsageUS = realReviewTimelineInt64(140)
					}
				}
				return sample
			}, time.Hour, time.Minute, profile)
			t.Cleanup(func() { monitor.stop() })
			if tt.invalidProfile {
				profile.consume([]byte(`{"version":1,"op":0,"phase":0,"category":0}`))
			}
			actual := monitor.stop()
			require.Equal(t, int64(2), calls.Load(), "CPU observation must reuse only existing initial and joined final reads")
			require.Equal(t, tt.expectedComplete, actual.CompleteSamples, "only unavailable core memory may affect ordinary completeness")
			if !tt.profile {
				require.Nil(t, actual.DiagnosticCPU, "normal acceptance must not allocate CPU diagnostic aggregates")
				require.NotContains(t, actual.ReadLimitsBytes, "cpu.stat", "normal acceptance must keep the existing read limits unchanged")
				encoded, err := json.Marshal(actual)
				require.NoError(t, err, "normal telemetry must remain serializable")
				require.NotContains(t, string(encoded), "cpu_", "normal output must omit optional CPU evidence")
				return
			}
			require.Equal(t, tt.expectedSamples, actual.DiagnosticCPU.Samples, "CPU availability must remain explicit after the final read")
			require.Equal(t, tt.expectedUnavailable, actual.DiagnosticCPU.UnavailableSamples, "destroyed cgroup evidence must preserve explicit CPU gaps")
			require.Equal(t, tt.expectedLast, actual.DiagnosticCPU.Last.UsageUS, "earlier last valid CPU evidence must survive failed cold teardown")
			require.Equal(t, int64(4096), actual.ReadLimitsBytes["cpu.stat"], "diagnostic CPU reads must retain the fixed byte cap")
			expectedReason := "incomplete_phase_exits"
			if tt.invalidProfile {
				expectedReason = "invalid_record"
			}
			require.Equal(t, expectedReason, profile.result().InvalidReason, "final CPU read must never revive an invalid or incomplete helper profile")
			changed := monitor.stop()
			changed.DiagnosticCPU.First.UsageUS, changed.DiagnosticCPU.Last.UsageUS = 999, 999
			if changed.Latest.CPUUsageUS != nil {
				*changed.Latest.CPUUsageUS = 999
			}
			require.Equal(t, actual, monitor.stop(), "repeated teardown results must clone latest CPU and first/last observation pointers")
			cell := profile.result().Phases["go_filter/go"]
			if !tt.missing && !tt.invalidProfile {
				*cell.SampledCPUUsageUS = 999
				require.Equal(t, int64(40), *profile.result().Phases["go_filter/go"].SampledCPUUsageUS, "phase CPU copies must not expose retained mutable pointers")
			}
		})
	}
}
