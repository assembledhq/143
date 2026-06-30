package sessiondiff

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/services/agent"
	"github.com/assembledhq/143/internal/testutil"
)

func TestCollect(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		sandbox       *agent.Sandbox
		baseCommitSHA string
		targetBranch  string
		execFn        func(ctx context.Context, sb *agent.Sandbox, cmd string, stdout, stderr io.Writer) (int, error)
		expected      string
		expectedCmd   []string
		expectErr     string
		expectErrIs   error
	}{
		{
			name:          "returns empty when sandbox is not a git repo",
			sandbox:       &agent.Sandbox{ID: "sb"},
			baseCommitSHA: "abc123",
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
			name:          "collects git diff and untracked file patches with base commit when no target branch",
			sandbox:       &agent.Sandbox{ID: "sb"},
			baseCommitSHA: "abc123",
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
			name:          "diffs against merge-base when target branch is set",
			sandbox:       &agent.Sandbox{ID: "sb"},
			baseCommitSHA: "abc123",
			targetBranch:  "main",
			execFn: func(_ context.Context, _ *agent.Sandbox, cmd string, stdout, stderr io.Writer) (int, error) {
				switch cmd {
				case "git rev-parse --is-inside-work-tree":
					return 0, nil
				case "git fetch --quiet --no-tags --end-of-options origin 'main'":
					return 0, nil
				case "git merge-base 'origin/main' HEAD":
					_, _ = io.WriteString(stdout, "deadbeef\n")
					return 0, nil
				case "git diff --find-renames --binary deadbeef -- .":
					_, _ = io.WriteString(stdout, "merge-base diff\n")
					return 0, nil
				case "git ls-files --others --exclude-standard -- .":
					return 0, nil
				default:
					t.Fatalf("unexpected command: %s", cmd)
					return 0, nil
				}
			},
			expected: "merge-base diff\n",
			expectedCmd: []string{
				"git rev-parse --is-inside-work-tree",
				"git fetch --quiet --no-tags --end-of-options origin 'main'",
				"git merge-base 'origin/main' HEAD",
				"git diff --find-renames --binary deadbeef -- .",
				"git ls-files --others --exclude-standard -- .",
			},
		},
		{
			name:          "falls back to baseCommitSHA when merge-base resolution fails",
			sandbox:       &agent.Sandbox{ID: "sb"},
			baseCommitSHA: "abc123",
			targetBranch:  "main",
			execFn: func(_ context.Context, _ *agent.Sandbox, cmd string, stdout, stderr io.Writer) (int, error) {
				switch cmd {
				case "git rev-parse --is-inside-work-tree":
					return 0, nil
				case "git fetch --quiet --no-tags --end-of-options origin 'main'":
					_, _ = io.WriteString(stderr, "fatal: could not read from remote")
					return 128, nil
				case "git merge-base 'origin/main' HEAD":
					_, _ = io.WriteString(stderr, "fatal: bad revision")
					return 128, nil
				case "git diff --find-renames --binary abc123 -- .":
					_, _ = io.WriteString(stdout, "fallback diff\n")
					return 0, nil
				case "git ls-files --others --exclude-standard -- .":
					return 0, nil
				default:
					t.Fatalf("unexpected command: %s", cmd)
					return 0, nil
				}
			},
			expected: "fallback diff\n",
			expectedCmd: []string{
				"git rev-parse --is-inside-work-tree",
				"git fetch --quiet --no-tags --end-of-options origin 'main'",
				"git merge-base 'origin/main' HEAD",
				"git diff --find-renames --binary abc123 -- .",
				"git ls-files --others --exclude-standard -- .",
			},
		},
		{
			name:          "falls back to baseCommitSHA when merge-base returns empty output",
			sandbox:       &agent.Sandbox{ID: "sb"},
			baseCommitSHA: "abc123",
			targetBranch:  "main",
			execFn: func(_ context.Context, _ *agent.Sandbox, cmd string, stdout, stderr io.Writer) (int, error) {
				switch cmd {
				case "git rev-parse --is-inside-work-tree":
					return 0, nil
				case "git fetch --quiet --no-tags --end-of-options origin 'main'":
					return 0, nil
				case "git merge-base 'origin/main' HEAD":
					_, _ = io.WriteString(stdout, "\n")
					return 0, nil
				case "git diff --find-renames --binary abc123 -- .":
					return 0, nil
				case "git ls-files --others --exclude-standard -- .":
					return 0, nil
				default:
					t.Fatalf("unexpected command: %s", cmd)
					return 0, nil
				}
			},
			expected: "",
			expectedCmd: []string{
				"git rev-parse --is-inside-work-tree",
				"git fetch --quiet --no-tags --end-of-options origin 'main'",
				"git merge-base 'origin/main' HEAD",
				"git diff --find-renames --binary abc123 -- .",
				"git ls-files --others --exclude-standard -- .",
			},
		},
		{
			name:          "tolerates a failing fetch when merge-base still resolves locally",
			sandbox:       &agent.Sandbox{ID: "sb"},
			baseCommitSHA: "abc123",
			targetBranch:  "main",
			execFn: func(_ context.Context, _ *agent.Sandbox, cmd string, stdout, stderr io.Writer) (int, error) {
				switch cmd {
				case "git rev-parse --is-inside-work-tree":
					return 0, nil
				case "git fetch --quiet --no-tags --end-of-options origin 'main'":
					_, _ = io.WriteString(stderr, "fatal: network unreachable")
					return 1, errors.New("exec failure")
				case "git merge-base 'origin/main' HEAD":
					_, _ = io.WriteString(stdout, "stalebase\n")
					return 0, nil
				case "git diff --find-renames --binary stalebase -- .":
					_, _ = io.WriteString(stdout, "stale-base diff\n")
					return 0, nil
				case "git ls-files --others --exclude-standard -- .":
					return 0, nil
				default:
					t.Fatalf("unexpected command: %s", cmd)
					return 0, nil
				}
			},
			expected: "stale-base diff\n",
			expectedCmd: []string{
				"git rev-parse --is-inside-work-tree",
				"git fetch --quiet --no-tags --end-of-options origin 'main'",
				"git merge-base 'origin/main' HEAD",
				"git diff --find-renames --binary stalebase -- .",
				"git ls-files --others --exclude-standard -- .",
			},
		},
		{
			name:          "shell escapes target branch with single quotes",
			sandbox:       &agent.Sandbox{ID: "sb"},
			baseCommitSHA: "abc123",
			targetBranch:  "weird'branch",
			execFn: func(_ context.Context, _ *agent.Sandbox, cmd string, stdout, stderr io.Writer) (int, error) {
				switch cmd {
				case "git rev-parse --is-inside-work-tree":
					return 0, nil
				case "git fetch --quiet --no-tags --end-of-options origin 'weird'\\''branch'":
					return 0, nil
				case "git merge-base 'origin/weird'\\''branch' HEAD":
					_, _ = io.WriteString(stdout, "mb\n")
					return 0, nil
				case "git diff --find-renames --binary mb -- .":
					return 0, nil
				case "git ls-files --others --exclude-standard -- .":
					return 0, nil
				default:
					t.Fatalf("unexpected command: %s", cmd)
					return 0, nil
				}
			},
			expected: "",
			expectedCmd: []string{
				"git rev-parse --is-inside-work-tree",
				"git fetch --quiet --no-tags --end-of-options origin 'weird'\\''branch'",
				"git merge-base 'origin/weird'\\''branch' HEAD",
				"git diff --find-renames --binary mb -- .",
				"git ls-files --others --exclude-standard -- .",
			},
		},
		{
			name:          "returns error when repo check fails",
			sandbox:       &agent.Sandbox{ID: "sb"},
			baseCommitSHA: "abc123",
			execFn: func(_ context.Context, _ *agent.Sandbox, cmd string, stdout, stderr io.Writer) (int, error) {
				return 0, errors.New("boom")
			},
			expectErr: "check git repo",
		},
		{
			name:          "refuses to compute a diff without a base commit sha",
			sandbox:       &agent.Sandbox{ID: "sb"},
			baseCommitSHA: "",
			execFn: func(_ context.Context, _ *agent.Sandbox, cmd string, stdout, stderr io.Writer) (int, error) {
				if cmd == "git rev-parse --is-inside-work-tree" {
					return 0, nil
				}
				t.Fatalf("Collect should not run any git diff command when base sha is missing; got: %s", cmd)
				return 0, nil
			},
			expectErrIs: ErrNoBaseCommitSHA,
		},
		{
			name:          "returns error when git diff exits non zero",
			sandbox:       &agent.Sandbox{ID: "sb"},
			baseCommitSHA: "abc123",
			execFn: func(_ context.Context, _ *agent.Sandbox, cmd string, stdout, stderr io.Writer) (int, error) {
				switch cmd {
				case "git rev-parse --is-inside-work-tree":
					return 0, nil
				case "git diff --find-renames --binary abc123 -- .":
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
			name:          "returns error when listing untracked files fails",
			sandbox:       &agent.Sandbox{ID: "sb"},
			baseCommitSHA: "abc123",
			execFn: func(_ context.Context, _ *agent.Sandbox, cmd string, stdout, stderr io.Writer) (int, error) {
				switch cmd {
				case "git rev-parse --is-inside-work-tree":
					return 0, nil
				case "git diff --find-renames --binary abc123 -- .":
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
			name:          "returns error when untracked diff exits with unexpected code",
			sandbox:       &agent.Sandbox{ID: "sb"},
			baseCommitSHA: "abc123",
			execFn: func(_ context.Context, _ *agent.Sandbox, cmd string, stdout, stderr io.Writer) (int, error) {
				switch cmd {
				case "git rev-parse --is-inside-work-tree":
					return 0, nil
				case "git diff --find-renames --binary abc123 -- .":
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

			diff, err := Collect(context.Background(), provider, tt.sandbox, tt.baseCommitSHA, tt.targetBranch, zerolog.Nop())
			if tt.expectErrIs != nil {
				require.Error(t, err, "Collect should return an error")
				require.ErrorIs(t, err, tt.expectErrIs, "Collect should return the expected sentinel error")
				return
			}
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

func TestRedactPossibleToken(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "passes through plain text",
			in:   "fatal: could not read from remote",
			want: "fatal: could not read from remote",
		},
		{
			name: "redacts token in URL",
			in:   "fatal: unable to access 'https://x-access-token:ghs_abc123XYZ@github.com/org/repo.git/'",
			want: "fatal: unable to access 'https://x-access-token:REDACTED@github.com/org/repo.git/'",
		},
		{
			name: "redacts token without trailing @",
			in:   "remote helper says x-access-token:ghs_secret died unexpectedly",
			want: "remote helper says x-access-token:REDACTED died unexpectedly",
		},
		{
			name: "redacts multiple occurrences",
			in:   "tried x-access-token:aaa@host then x-access-token:bbb@host",
			want: "tried x-access-token:REDACTED@host then x-access-token:REDACTED@host",
		},
		{
			name: "leaves empty token marker as-is when nothing follows",
			in:   "x-access-token:",
			want: "x-access-token:REDACTED",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.want, redactPossibleToken(tt.in))
		})
	}
}
