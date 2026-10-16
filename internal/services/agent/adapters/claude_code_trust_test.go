package adapters

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/services/agent"
	"github.com/assembledhq/143/internal/testutil"
)

func TestEnsureClaudeWorkspaceTrust_MergesConfig(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		initial func(string) string
		expect  func(string) string
	}{
		{
			name: "missing config",
			expect: func(project string) string {
				return fmt.Sprintf(`{"projects":{%q:{"hasTrustDialogAccepted":true}}}`, project)
			},
		},
		{
			name: "existing config without projects",
			initial: func(string) string {
				return `{"installMethod":"synthetic","numStartups":9007199254740993,"featureFlags":{"enabled":true}}`
			},
			expect: func(project string) string {
				return fmt.Sprintf(`{"installMethod":"synthetic","numStartups":9007199254740993,"featureFlags":{"enabled":true},"projects":{%q:{"hasTrustDialogAccepted":true}}}`, project)
			},
		},
		{
			name: "preserves unrelated trust and current project fields",
			initial: func(project string) string {
				return fmt.Sprintf(`{"unknown":[1,{"value":9007199254740993}],"projects":{"/synthetic/other":{"hasTrustDialogAccepted":false,"allowedTools":["Read"]},"/synthetic/trusted":{"hasTrustDialogAccepted":true},%q:{"hasTrustDialogAccepted":false,"projectOnboardingSeenCount":5,"allowedTools":[]}}}`, project)
			},
			expect: func(project string) string {
				return fmt.Sprintf(`{"unknown":[1,{"value":9007199254740993}],"projects":{"/synthetic/other":{"hasTrustDialogAccepted":false,"allowedTools":["Read"]},"/synthetic/trusted":{"hasTrustDialogAccepted":true},%q:{"hasTrustDialogAccepted":true,"projectOnboardingSeenCount":5,"allowedTools":[]}}}`, project)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			sandbox := newLocalClaudeTrustSandbox(t)
			project := canonicalClaudeTrustPath(t, sandbox.WorkDir)
			configPath := filepath.Join(sandbox.HomeDir, ".claude.json")
			if tt.initial != nil {
				require.NoError(t, os.WriteFile(configPath, []byte(tt.initial(project)), 0o644), "fixture config should be writable")
			}

			err := ensureClaudeWorkspaceTrust(context.Background(), localClaudeTrustProvider(), sandbox)
			require.NoError(t, err, "sandbox project trust should be persisted")
			actual, err := os.ReadFile(configPath)
			require.NoError(t, err, "prepared Claude config should exist")
			require.Equal(t, decodeClaudeTrustJSON(t, []byte(tt.expect(project))), decodeClaudeTrustJSON(t, actual), "trust preparation should preserve every existing value and trust only the current project")
			info, err := os.Stat(configPath)
			require.NoError(t, err, "prepared Claude config should have file metadata")
			require.Equal(t, os.FileMode(0o600), info.Mode().Perm(), "new config contents should be private to the sandbox user")
		})
	}
}

func TestEnsureClaudeWorkspaceTrust_AlreadyTrustedDoesNotRewrite(t *testing.T) {
	t.Parallel()
	sandbox := newLocalClaudeTrustSandbox(t)
	project := canonicalClaudeTrustPath(t, sandbox.WorkDir)
	configPath := filepath.Join(sandbox.HomeDir, ".claude.json")
	initial := []byte(fmt.Sprintf("{\n  \"projects\": {%q: {\"hasTrustDialogAccepted\": true}},\n  \"numStartups\": 9007199254740993\n}\n", project))
	require.NoError(t, os.WriteFile(configPath, initial, 0o600), "trusted fixture config should be writable")
	modifiedAt := time.Unix(1_700_000_000, 0)
	require.NoError(t, os.Chtimes(configPath, modifiedAt, modifiedAt), "fixture should have a recognizable modification time")
	before, err := os.Stat(configPath)
	require.NoError(t, err, "fixture metadata should be available")

	require.NoError(t, ensureClaudeWorkspaceTrust(context.Background(), localClaudeTrustProvider(), sandbox), "an already trusted workspace should be accepted")
	actual, err := os.ReadFile(configPath)
	require.NoError(t, err, "trusted config should remain readable")
	after, err := os.Stat(configPath)
	require.NoError(t, err, "trusted config metadata should remain available")
	require.Equal(t, initial, actual, "an already trusted config should remain byte-for-byte unchanged")
	require.True(t, os.SameFile(before, after), "an already trusted config should retain its original file identity")
	require.Equal(t, before.ModTime(), after.ModTime(), "an already trusted config should retain its original modification time")
}

func TestEnsureClaudeWorkspaceTrust_InvalidConfigIsPreserved(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		initial func(string) string
	}{
		{name: "invalid JSON", initial: func(string) string { return `{"synthetic_private_value":"do-not-disclose"` }},
		{name: "root array", initial: func(string) string { return `[]` }},
		{name: "root null", initial: func(string) string { return `null` }},
		{name: "nonstandard JSON constant", initial: func(string) string { return `{"value":NaN}` }},
		{name: "projects array", initial: func(string) string { return `{"projects":[]}` }},
		{name: "projects null", initial: func(string) string { return `{"projects":null}` }},
		{name: "project array", initial: func(project string) string { return fmt.Sprintf(`{"projects":{%q:[]}}`, project) }},
		{name: "project null", initial: func(project string) string { return fmt.Sprintf(`{"projects":{%q:null}}`, project) }},
		{name: "project string", initial: func(project string) string {
			return fmt.Sprintf(`{"projects":{%q:"synthetic_private_value"}}`, project)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			sandbox := newLocalClaudeTrustSandbox(t)
			configPath := filepath.Join(sandbox.HomeDir, ".claude.json")
			initial := []byte(tt.initial(canonicalClaudeTrustPath(t, sandbox.WorkDir)))
			require.NoError(t, os.WriteFile(configPath, initial, 0o600), "invalid config fixture should be writable")
			before, err := os.Stat(configPath)
			require.NoError(t, err, "invalid config fixture metadata should be available")

			err = ensureClaudeWorkspaceTrust(context.Background(), localClaudeTrustProvider(), sandbox)
			require.EqualError(t, err, "prepare Claude workspace trust: invalid_configuration (exit code 2)", "invalid config should produce a safe, actionable diagnosis")
			require.NotContains(t, err.Error(), "synthetic_private_value", "trust errors should not disclose config values")
			require.NotContains(t, err.Error(), sandbox.HomeDir, "trust errors should not disclose sandbox paths")
			actual, err := os.ReadFile(configPath)
			require.NoError(t, err, "invalid config should remain readable for repair")
			require.Equal(t, initial, actual, "invalid config should remain byte-for-byte unchanged")
			after, err := os.Stat(configPath)
			require.NoError(t, err, "invalid config metadata should remain available")
			require.True(t, os.SameFile(before, after), "invalid config should retain its original file identity")
		})
	}
}

func TestEnsureClaudeWorkspaceTrust_FilesystemFailurePreservesArtifact(t *testing.T) {
	t.Parallel()
	sandbox := newLocalClaudeTrustSandbox(t)
	configPath := filepath.Join(sandbox.HomeDir, ".claude.json")
	require.NoError(t, os.Mkdir(configPath, 0o700), "fixture config path should be a directory to cause a read failure")
	artifactPath := filepath.Join(configPath, "synthetic-artifact")
	initial := []byte("synthetic private artifact\n")
	require.NoError(t, os.WriteFile(artifactPath, initial, 0o600), "existing config artifact should be writable")
	before, err := os.Stat(configPath)
	require.NoError(t, err, "existing config artifact metadata should be available")

	err = ensureClaudeWorkspaceTrust(context.Background(), localClaudeTrustProvider(), sandbox)
	require.EqualError(t, err, "prepare Claude workspace trust: filesystem_update (exit code 4)", "a config read failure should produce a safe filesystem diagnosis")
	after, err := os.Stat(configPath)
	require.NoError(t, err, "the config directory should remain after the failed preparation")
	require.True(t, os.SameFile(before, after), "failed preparation should preserve the original config artifact identity")
	actual, err := os.ReadFile(artifactPath)
	require.NoError(t, err, "existing config artifact contents should remain readable")
	require.Equal(t, initial, actual, "failed preparation should preserve existing config artifact contents")
}

func TestEnsureClaudeWorkspaceTrust_ResolvesPaths(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		setup func(*testing.T, *agent.Sandbox) string
	}{
		{
			name: "shell sensitive paths",
			setup: func(t *testing.T, sandbox *agent.Sandbox) string {
				t.Helper()
				root := t.TempDir()
				sandbox.HomeDir = filepath.Join(root, "home ' $(printf injected) `printf injected` ;")
				sandbox.WorkDir = filepath.Join(root, "repo ' $(printf injected) `printf injected` ;\nworkspace")
				require.NoError(t, os.MkdirAll(sandbox.HomeDir, 0o700), "shell-sensitive home fixture should exist")
				require.NoError(t, os.MkdirAll(sandbox.WorkDir, 0o700), "shell-sensitive workspace fixture should exist")
				return canonicalClaudeTrustPath(t, sandbox.WorkDir)
			},
		},
		{
			name: "workspace symlink",
			setup: func(t *testing.T, sandbox *agent.Sandbox) string {
				t.Helper()
				target := sandbox.WorkDir
				link := filepath.Join(t.TempDir(), "workspace-link")
				require.NoError(t, os.Symlink(target, link), "workspace symlink fixture should be creatable")
				sandbox.WorkDir = link
				return canonicalClaudeTrustPath(t, target)
			},
		},
		{
			name: "git subdirectory",
			setup: func(t *testing.T, sandbox *agent.Sandbox) string {
				t.Helper()
				root := sandbox.WorkDir
				runClaudeTrustGit(t, root, "init")
				sandbox.WorkDir = filepath.Join(root, "nested", "source")
				require.NoError(t, os.MkdirAll(sandbox.WorkDir, 0o700), "Git subdirectory fixture should exist")
				return canonicalClaudeTrustPath(t, root)
			},
		},
		{
			name: "git linked worktree",
			setup: func(t *testing.T, sandbox *agent.Sandbox) string {
				t.Helper()
				root := sandbox.WorkDir
				runClaudeTrustGit(t, root, "init")
				runClaudeTrustGit(t, root, "-c", "user.name=Synthetic", "-c", "user.email=synthetic@example.invalid", "-c", "commit.gpgsign=false", "commit", "--allow-empty", "-m", "Synthetic fixture")
				worktree := filepath.Join(t.TempDir(), "linked-worktree")
				runClaudeTrustGit(t, root, "worktree", "add", "--detach", worktree)
				sandbox.WorkDir = filepath.Join(worktree, "nested")
				require.NoError(t, os.MkdirAll(sandbox.WorkDir, 0o700), "linked worktree subdirectory fixture should exist")
				return canonicalClaudeTrustPath(t, root)
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			sandbox := newLocalClaudeTrustSandbox(t)
			project := tt.setup(t, sandbox)
			require.NoError(t, ensureClaudeWorkspaceTrust(context.Background(), localClaudeTrustProvider(), sandbox), "trust preparation should resolve the canonical Claude project key")
			actual, err := os.ReadFile(filepath.Join(sandbox.HomeDir, ".claude.json"))
			require.NoError(t, err, "resolved project config should be readable")
			expected := fmt.Sprintf(`{"projects":{%q:{"hasTrustDialogAccepted":true}}}`, project)
			require.Equal(t, decodeClaudeTrustJSON(t, []byte(expected)), decodeClaudeTrustJSON(t, actual), "exactly the canonical project root should be trusted")
		})
	}
}

func TestEnsureClaudeWorkspaceTrust_RepositoryImportsAreIsolated(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		module  string
		content string
	}{
		{name: "repository json module", module: "json.py", content: "raise SystemExit(7)\n"},
		{name: "repository random module", module: "random.py", content: "raise RuntimeError('repository random module executed')\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			sandbox := newLocalClaudeTrustSandbox(t)
			require.NoError(t, os.WriteFile(filepath.Join(sandbox.WorkDir, tt.module), []byte(tt.content), 0o600), "repository module fixture should exist in the working directory")

			require.NoError(t, ensureClaudeWorkspaceTrust(context.Background(), localClaudeTrustProvider(), sandbox), "trust preparation should use isolated Python imports without executing repository modules")
			actual, err := os.ReadFile(filepath.Join(sandbox.HomeDir, ".claude.json"))
			require.NoError(t, err, "isolated trust preparation should persist the project config")
			expected := fmt.Sprintf(`{"projects":{%q:{"hasTrustDialogAccepted":true}}}`, canonicalClaudeTrustPath(t, sandbox.WorkDir))
			require.Equal(t, decodeClaudeTrustJSON(t, []byte(expected)), decodeClaudeTrustJSON(t, actual), "repository modules should not interfere with persisted workspace trust")
		})
	}
}

func TestEnsureClaudeWorkspaceTrust_ConcurrentProjectsPreserveUpdates(t *testing.T) {
	t.Parallel()
	first := newLocalClaudeTrustSandbox(t)
	provider := localClaudeTrustProvider()
	configPath := filepath.Join(first.HomeDir, ".claude.json")
	require.NoError(t, os.WriteFile(configPath, []byte(`{"numStartups":9007199254740993}`), 0o600), "shared home config fixture should be writable")

	sandboxes := make([]*agent.Sandbox, 8)
	projects := make(map[string]any, len(sandboxes))
	for i := range sandboxes {
		sandboxes[i] = newLocalClaudeTrustSandbox(t)
		sandboxes[i].HomeDir = first.HomeDir
		projects[canonicalClaudeTrustPath(t, sandboxes[i].WorkDir)] = map[string]any{"hasTrustDialogAccepted": true}
	}
	start := make(chan struct{})
	errorsCh := make(chan error, len(sandboxes))
	var wg sync.WaitGroup
	for _, sandbox := range sandboxes {
		wg.Add(1)
		go func() {
			defer wg.Done()
			<-start
			errorsCh <- ensureClaudeWorkspaceTrust(context.Background(), provider, sandbox)
		}()
	}
	close(start)
	wg.Wait()
	close(errorsCh)
	for err := range errorsCh {
		require.NoError(t, err, "concurrent project trust preparations should succeed")
	}
	actual, err := os.ReadFile(configPath)
	require.NoError(t, err, "shared config should remain readable after concurrent preparation")
	expected := map[string]any{"numStartups": json.Number("9007199254740993"), "projects": projects}
	require.Equal(t, expected, decodeClaudeTrustJSON(t, actual), "serialized trust preparations should preserve every project update and existing values")
}

func TestClaudeCodeAdapter_Execute_PreparesTrustBeforeLaunch(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		continuation  bool
		trustExitCode int
		trustErr      error
		cancelled     bool
		warning       string
	}{
		{name: "fresh launch"},
		{name: "resumed launch", continuation: true},
		{name: "fresh config failure", trustExitCode: 2, warning: "prepare Claude workspace trust: invalid_configuration (exit code 2)"},
		{name: "resumed config failure", continuation: true, trustExitCode: 2, warning: "prepare Claude workspace trust: invalid_configuration (exit code 2)"},
		{name: "filesystem failure", trustExitCode: 4, warning: "prepare Claude workspace trust: filesystem_update (exit code 4)"},
		{name: "fresh transport failure", trustErr: context.DeadlineExceeded, warning: "prepare Claude workspace trust: context deadline exceeded"},
		{name: "resumed transport failure", continuation: true, trustErr: context.DeadlineExceeded, warning: "prepare Claude workspace trust: context deadline exceeded"},
		{name: "cancelled fresh preparation", cancelled: true},
		{name: "cancelled resumed preparation", continuation: true, cancelled: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			provider := testutil.NewMockSandboxProvider()
			ctx, cancel := context.WithCancel(WithSandboxProvider(context.Background(), provider))
			defer cancel()
			var mu sync.Mutex
			var events []string
			provider.ExecFn = func(ctx context.Context, sandbox *agent.Sandbox, cmd string, stdout, stderr io.Writer) (int, error) {
				mu.Lock()
				defer mu.Unlock()
				if strings.Contains(cmd, "hasTrustDialogAccepted") {
					events = append(events, "trust")
					if tt.cancelled {
						cancel()
						return 0, context.Canceled
					}
					return tt.trustExitCode, tt.trustErr
				}
				if strings.Contains(cmd, "claude --print") {
					events = append(events, "launch")
					_, err := io.WriteString(stdout, `{"type":"result","subtype":"success","result":"Synthetic success"}`)
					return 0, err
				}
				return 0, nil
			}
			prompt := &agent.AgentPrompt{SystemPrompt: "Synthetic prompt", UserPrompt: "Synthetic task", MaxTokens: 50_000}
			if tt.continuation {
				prompt.Continuation = true
				prompt.ResumeSessionID = "synthetic-session"
				prompt.UserMessage = "Synthetic follow-up"
			}
			sandbox := &agent.Sandbox{ID: "synthetic", WorkDir: "/synthetic/workspace", HomeDir: "/synthetic/home"}
			logCh := make(chan agent.LogEntry, 10)
			result, err := NewClaudeCodeAdapter(zerolog.Nop()).Execute(ctx, sandbox, prompt, logCh)
			close(logCh)
			if tt.cancelled {
				require.ErrorIs(t, err, context.Canceled, "cancelled trust preparation should retain the cancellation cause")
				require.Nil(t, result, "cancelled preparation should not produce an agent result")
				require.Equal(t, []string{"trust"}, events, "Claude should not launch after cancelled trust preparation")
				return
			}
			require.NoError(t, err, "trust setup failure should leave Claude available to recover its own configuration")
			require.NotNil(t, result, "successful launch should return an agent result")
			require.Equal(t, []string{"trust", "launch"}, events, "trust preparation should finish before fresh and resumed CLI launches")
			var warnings []string
			for entry := range logCh {
				if entry.Level == "warn" {
					require.Equal(t, "Could not save Claude workspace trust; continuing with Claude's existing permissions.", entry.Message, "trust preparation warnings should explain that the turn can continue")
					warnings = append(warnings, entry.Metadata["error"].(string))
				}
			}
			var expectedWarnings []string
			if tt.warning != "" {
				expectedWarnings = []string{tt.warning}
			}
			require.Equal(t, expectedWarnings, warnings, "failed preparation should emit one safe diagnostic and successful preparation should remain quiet")
		})
	}
}

func newLocalClaudeTrustSandbox(t *testing.T) *agent.Sandbox {
	t.Helper()
	for _, binary := range []string{"python3", "git"} {
		if _, err := exec.LookPath(binary); err != nil {
			t.Skipf("%s is required to exercise the sandbox trust command", binary)
		}
	}
	root := t.TempDir()
	sandbox := &agent.Sandbox{ID: "synthetic", WorkDir: filepath.Join(root, "workspace"), HomeDir: filepath.Join(root, "home")}
	require.NoError(t, os.MkdirAll(sandbox.WorkDir, 0o700), "synthetic workspace should exist")
	require.NoError(t, os.MkdirAll(sandbox.HomeDir, 0o700), "synthetic sandbox home should exist")
	return sandbox
}

func localClaudeTrustProvider() *testutil.MockSandboxProvider {
	provider := testutil.NewMockSandboxProvider()
	provider.ExecFn = func(ctx context.Context, sandbox *agent.Sandbox, command string, stdout, stderr io.Writer) (int, error) {
		cmd := exec.CommandContext(ctx, "sh", "-c", command)
		cmd.Dir = sandbox.WorkDir
		cmd.Stdout = stdout
		cmd.Stderr = stderr
		if err := cmd.Run(); err != nil {
			var exitErr *exec.ExitError
			if errors.As(err, &exitErr) {
				return exitErr.ExitCode(), nil
			}
			return 0, err
		}
		return 0, nil
	}
	return provider
}

func canonicalClaudeTrustPath(t *testing.T, path string) string {
	t.Helper()
	canonical, err := filepath.EvalSymlinks(path)
	require.NoError(t, err, "fixture paths should resolve to their canonical location")
	return canonical
}

func decodeClaudeTrustJSON(t *testing.T, content []byte) any {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewReader(content))
	decoder.UseNumber()
	var value any
	require.NoError(t, decoder.Decode(&value), "Claude config should contain valid JSON preserving integer precision")
	return value
}

func runClaudeTrustGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("Git is required to exercise repository-root trust resolution")
	}
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, "synthetic Git fixture command should succeed: %s", output)
}
