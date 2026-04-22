package sessiondiff

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/assembledhq/143/internal/services/agent"
	"github.com/assembledhq/143/internal/testutil"
	"github.com/stretchr/testify/require"
)

func TestCollect(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		sandbox     *agent.Sandbox
		execFn      func(ctx context.Context, sb *agent.Sandbox, cmd string, stdout, stderr io.Writer) (int, error)
		expected    string
		expectedCmd []string
		expectErr   string
	}{
		{
			name:    "returns empty when sandbox is not a git repo",
			sandbox: &agent.Sandbox{ID: "sb"},
			execFn: func(_ context.Context, _ *agent.Sandbox, cmd string, stdout, stderr io.Writer) (int, error) {
				require.Equal(t, "git rev-parse --is-inside-work-tree", cmd, "git repo check should run first")
				return 1, nil
			},
			expected: "",
			expectedCmd: []string{
				"git rev-parse --is-inside-work-tree",
			},
		},
		{
			name:    "collects git diff and untracked file patches with base commit",
			sandbox: &agent.Sandbox{ID: "sb", Metadata: map[string]string{SandboxMetadataBaseCommitSHA: "abc123"}},
			execFn: func(_ context.Context, _ *agent.Sandbox, cmd string, stdout, stderr io.Writer) (int, error) {
				switch cmd {
				case "git rev-parse --is-inside-work-tree":
					return 0, nil
				case "git diff --find-renames --binary abc123 -- .":
					_, _ = io.WriteString(stdout, "tracked\n")
					return 0, nil
				case "git ls-files --others --exclude-standard -- .":
					_, _ = io.WriteString(stdout, "new file.txt\nquote'file.go\n")
					return 0, nil
				case "git diff --find-renames --binary --no-index -- /dev/null 'new file.txt'":
					_, _ = io.WriteString(stdout, "untracked-1\n")
					return 1, nil
				case "git diff --find-renames --binary --no-index -- /dev/null 'quote'\\''file.go'":
					_, _ = io.WriteString(stdout, "untracked-2\n")
					return 1, nil
				default:
					t.Fatalf("unexpected command: %s", cmd)
					return 0, nil
				}
			},
			expected: "tracked\nuntracked-1\nuntracked-2\n",
			expectedCmd: []string{
				"git rev-parse --is-inside-work-tree",
				"git diff --find-renames --binary abc123 -- .",
				"git ls-files --others --exclude-standard -- .",
				"git diff --find-renames --binary --no-index -- /dev/null 'new file.txt'",
				"git diff --find-renames --binary --no-index -- /dev/null 'quote'\\''file.go'",
			},
		},
		{
			name:    "returns error when repo check fails",
			sandbox: &agent.Sandbox{ID: "sb"},
			execFn: func(_ context.Context, _ *agent.Sandbox, cmd string, stdout, stderr io.Writer) (int, error) {
				return 0, errors.New("boom")
			},
			expectErr: "check git repo",
		},
		{
			name:    "returns error when git diff exits non zero",
			sandbox: &agent.Sandbox{ID: "sb"},
			execFn: func(_ context.Context, _ *agent.Sandbox, cmd string, stdout, stderr io.Writer) (int, error) {
				switch cmd {
				case "git rev-parse --is-inside-work-tree":
					return 0, nil
				case "git diff":
					_, _ = io.WriteString(stderr, "bad diff")
					return 2, nil
				default:
					t.Fatalf("unexpected command: %s", cmd)
					return 0, nil
				}
			},
			expectErr: "git diff exited with code 2: bad diff",
		},
		{
			name:    "returns error when listing untracked files fails",
			sandbox: &agent.Sandbox{ID: "sb"},
			execFn: func(_ context.Context, _ *agent.Sandbox, cmd string, stdout, stderr io.Writer) (int, error) {
				switch cmd {
				case "git rev-parse --is-inside-work-tree":
					return 0, nil
				case "git diff":
					return 0, nil
				case "git ls-files --others --exclude-standard -- .":
					return 0, errors.New("ls failed")
				default:
					t.Fatalf("unexpected command: %s", cmd)
					return 0, nil
				}
			},
			expectErr: "list untracked files",
		},
		{
			name:    "returns error when untracked diff exits with unexpected code",
			sandbox: &agent.Sandbox{ID: "sb"},
			execFn: func(_ context.Context, _ *agent.Sandbox, cmd string, stdout, stderr io.Writer) (int, error) {
				switch cmd {
				case "git rev-parse --is-inside-work-tree":
					return 0, nil
				case "git diff":
					return 0, nil
				case "git ls-files --others --exclude-standard -- .":
					_, _ = io.WriteString(stdout, "new.go\n")
					return 0, nil
				case "git diff --find-renames --binary --no-index -- /dev/null 'new.go'":
					_, _ = io.WriteString(stderr, "nope")
					return 2, nil
				default:
					t.Fatalf("unexpected command: %s", cmd)
					return 0, nil
				}
			},
			expectErr: "git diff for untracked file new.go exited with code 2: nope",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			provider := testutil.NewMockSandboxProvider()
			var calls []string
			provider.ExecFn = func(ctx context.Context, sb *agent.Sandbox, cmd string, stdout, stderr io.Writer) (int, error) {
				calls = append(calls, cmd)
				return tt.execFn(ctx, sb, cmd, stdout, stderr)
			}

			diff, err := Collect(context.Background(), provider, tt.sandbox)
			if tt.expectErr != "" {
				require.Error(t, err, "Collect should return an error")
				require.Contains(t, err.Error(), tt.expectErr, "Collect should return the expected error")
				return
			}

			require.NoError(t, err, "Collect should not return an error")
			require.Equal(t, tt.expected, diff, "Collect should return the expected combined diff")
			require.Equal(t, tt.expectedCmd, calls, "Collect should run the expected git commands")
		})
	}
}

func TestShellEscapeSingleQuote(t *testing.T) {
	t.Parallel()

	require.Equal(t, "plain", shellEscapeSingleQuote("plain"), "strings without quotes should be unchanged")
	require.Equal(t, "a'\\''b", shellEscapeSingleQuote("a'b"), "single quotes should be shell escaped")
}
