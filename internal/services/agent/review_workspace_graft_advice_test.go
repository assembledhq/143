package agent

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func runReviewGraftAdviceFixture(t *testing.T, f reviewEvidenceGitFixture, script string) (string, string, error) {
	t.Helper()
	clean, err := reviewPreparationSandbox(f.sandbox())
	require.NoError(t, err, "fixture must retain the production no-replace and null-graft environment")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, "/bin/sh", "-c", script)
	command.Dir = f.workDir
	command.Env = os.Environ()
	for name, value := range clean.Env {
		command.Env = append(command.Env, name+"="+value)
	}
	var stdout, stderr bytes.Buffer
	command.Stdout, command.Stderr = &stdout, &stderr
	err = command.Run()
	return stdout.String(), stderr.String(), err
}

func TestReviewGitGraftAdviceSuppression(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		script        func(reviewEvidenceGitFixture) string
		integrityOnly bool
	}{
		{name: "shared input Git command factory", script: sharedReviewInputGraftAdviceFixtureScript},
		{name: "pinned comparison and local tree closure", script: func(f reviewEvidenceGitFixture) string {
			return reviewTreeEvidenceScript(f.workDir, f.base, f.head, "") + "printf '%s\n' \"$comparison\"\n"
		}},
		{name: "local object integrity", integrityOnly: true, script: func(f reviewEvidenceGitFixture) string {
			return reviewGitObjectIntegrityScript(f.workDir) + "printf '%s\n' integrity-ok\n"
		}},
		{name: "pinned checkout and Python index verification", script: func(f reviewEvidenceGitFixture) string {
			return reviewPinnedCheckoutVerificationScript(f.workDir, f.base, f.head) + "printf '%s\n' \"$comparison\"\n"
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newReviewEvidenceGitFixture(t, "", false)
			require.NoError(t, f.orchestrator().hydrateCodeReviewTrees(context.Background(), f.session, f.sandbox(), f.remote, "", zerolog.Nop()), "fixture must genuinely hydrate the pinned local objects before suppressing advice")
			expected, err := exec.Command("/usr/bin/git", "-C", f.workDir, "rev-parse", f.head+"^").Output()
			require.NoError(t, err, "fixture's known common parent must resolve without graph overrides")
			comparison := strings.TrimSpace(string(expected))
			setReviewFixtureGitConfig(t, f, "advice.graftFileDeprecated", "true")
			control := "/usr/bin/git merge-base '" + f.base + "' '" + f.head + "'"
			controlOut, controlErr, err := runReviewGraftAdviceFixture(t, f, control)
			require.NoError(t, err, "unsuppressed real Git must still return the pinned comparison")
			require.Equal(t, comparison+"\n", controlOut, "the null-graft guard must preserve the actual common parent")
			if !strings.Contains(controlErr, "advice.graftFileDeprecated") {
				require.Empty(t, controlErr, "feature-unavailable Git must produce no other control diagnostics")
				t.Skip("installed Git does not emit advice.graftFileDeprecated; source guard regressions run independently")
			}
			stdout, stderr, err := runReviewGraftAdviceFixture(t, f, tt.script(f))
			require.NoError(t, err, "trusted pinned source checks must succeed after suppressing only graft advice")
			expectedOut := comparison + "\n"
			if tt.integrityOnly {
				expectedOut = "integrity-ok\n"
			}
			require.Equal(t, expectedOut, stdout, "trusted source verification must preserve exact comparison and local-integrity behavior")
			require.Empty(t, stderr, "trusted Git and Python index checks must override hostile repository advice without deprecation noise")
		})
	}
}

func sharedReviewInputGraftAdviceFixtureScript(f reviewEvidenceGitFixture) string {
	// Exercise the production factory directly so its subprocess stderr reaches
	// the real-Git assertion instead of being captured by the preparation helper.
	const script = `import ast, os, subprocess, sys
module = ast.parse(sys.argv[1])
functions = [node for node in module.body if isinstance(node, ast.FunctionDef) and node.name in ("git_command", "git_environment")]
if len(functions) != 2:
    raise ValueError("shared review input Git factory and environment must exist")
namespace = {"os": os, "remote": ""}
exec(compile(ast.Module(body=functions, type_ignores=[]), "shared-input-git-factory", "exec"), namespace)
environment = namespace["git_environment"]()
for name, expected in {"GIT_GRAFT_FILE": "/dev/null", "GIT_NO_REPLACE_OBJECTS": "1", "GIT_NO_LAZY_FETCH": "1", "GIT_TERMINAL_PROMPT": "0", "GIT_ALLOW_PROTOCOL": ""}.items():
    if environment.get(name) != expected:
        raise ValueError("shared input Git environment guard changed: " + name)
result = subprocess.run(namespace["git_command"]("merge-base", sys.argv[2], sys.argv[3]), env=environment)
sys.exit(result.returncode)
`
	return fmt.Sprintf("/usr/bin/python3 -I -S -c '%s' '%s' '%s' '%s'", shellEscapeSingleQuote(script), shellEscapeSingleQuote(prepareCodeReviewInputsScript), f.base, f.head)
}
