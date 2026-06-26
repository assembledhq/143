package agent

import (
	"encoding/json"
	"testing"

	"github.com/assembledhq/143/internal/models"
	"github.com/stretchr/testify/require"
)

func TestSlugForRepo(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		in   string
		want string
	}{
		{"valid org/repo", "assembledhq/143", "143"},
		{"no slash returns empty", "norepo", ""},
		{"empty input returns empty", "", ""},
		{"empty repo portion returns empty", "org/", ""},
		{"nested slashes collapse to hyphens", "org/repo/sub", "repo-sub"},
		{"leading slash treats left as empty", "/repo", "repo"},
		// Path-traversal guards: even though GitHub's repo-name grammar
		// excludes these, SlugForRepo must never produce a slug that
		// resolves "/home/sandbox/<slug>" to a parent directory.
		{"dot component is rejected", "org/.", ""},
		{"dotdot component is rejected", "org/..", ""},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := SlugForRepo(tc.in)
			if got != tc.want {
				t.Fatalf("SlugForRepo(%q) = %q, want %q", tc.in, got, tc.want)
			}
		})
	}
}

//nolint:paralleltest // uses t.Setenv
func TestDefaultSandboxConfig_UsesEnvironmentOverrides(t *testing.T) {
	t.Setenv("SANDBOX_IMAGE", "ghcr.io/example/custom:latest")
	t.Setenv("SANDBOX_CACHE_ABI", "custom-cache-abi")
	t.Setenv("SANDBOX_CPU_LIMIT", "3.5")
	t.Setenv("SANDBOX_MEMORY_LIMIT_MB", "6144")
	t.Setenv("SANDBOX_DISK_LIMIT_GB", "24")

	cfg := DefaultSandboxConfig()

	require.Equal(t, "ghcr.io/example/custom:latest", cfg.Image, "DefaultSandboxConfig should use SANDBOX_IMAGE when provided")
	require.Equal(t, "custom-cache-abi", cfg.CacheABI, "DefaultSandboxConfig should use SANDBOX_CACHE_ABI when provided")
	require.Equal(t, 3.5, cfg.CPULimit, "DefaultSandboxConfig should use SANDBOX_CPU_LIMIT when provided")
	require.Equal(t, 6144, cfg.MemoryLimitMB, "DefaultSandboxConfig should use SANDBOX_MEMORY_LIMIT_MB when provided")
	require.Equal(t, 24, cfg.DiskLimitGB, "DefaultSandboxConfig should use SANDBOX_DISK_LIMIT_GB when provided")
}

//nolint:paralleltest // uses t.Setenv
func TestDefaultSandboxConfig_DefaultCacheABI(t *testing.T) {
	t.Setenv("SANDBOX_CACHE_ABI", "")

	cfg := DefaultSandboxConfig()

	require.Equal(t, defaultSandboxCacheABI, cfg.CacheABI, "DefaultSandboxConfig should use the stable default cache ABI")
}

//nolint:paralleltest // uses t.Setenv
func TestDefaultSandboxConfig_InvalidEnvironmentOverridesFallbackToDefaults(t *testing.T) {
	t.Setenv("SANDBOX_CPU_LIMIT", "not-a-number")
	t.Setenv("SANDBOX_MEMORY_LIMIT_MB", "-1")
	t.Setenv("SANDBOX_DISK_LIMIT_GB", "0")

	cfg := DefaultSandboxConfig()

	require.Equal(t, 2.0, cfg.CPULimit, "DefaultSandboxConfig should fall back to default CPU limit for invalid SANDBOX_CPU_LIMIT")
	require.Equal(t, 3072, cfg.MemoryLimitMB, "DefaultSandboxConfig should fall back to default memory limit for invalid SANDBOX_MEMORY_LIMIT_MB")
	require.Equal(t, 10, cfg.DiskLimitGB, "DefaultSandboxConfig should fall back to default disk limit for invalid SANDBOX_DISK_LIMIT_GB")
}

func TestParseAndFormatRevisionContext(t *testing.T) {
	t.Parallel()

	ctx, err := ParseRevisionContext(nil)
	require.NoError(t, err, "ParseRevisionContext should accept empty input")
	require.Nil(t, ctx, "ParseRevisionContext should return nil for empty input")

	_, err = ParseRevisionContext(json.RawMessage(`{"repair_action":`))
	require.Error(t, err, "ParseRevisionContext should reject invalid JSON")
	require.Contains(t, err.Error(), "unmarshal revision context", "ParseRevisionContext should wrap JSON errors")

	raw := json.RawMessage(`{
		"formatted_feedback":"Please address the review feedback.",
		"comment_summary":"Two issues remain.",
		"previous_diff":"--- a/foo.go\n+++ b/foo.go",
		"repair_action":"fix_tests",
		"repair_context":{
			"pull_request_number":42,
			"repository":"assembledhq/143",
			"head_sha":"head",
			"base_sha":"base",
			"merge_state":"conflicted",
			"has_conflicts":true,
			"failing_checks":[
				{
					"name":"unit tests",
					"category":"test",
					"summary":"2 tests failed",
					"details_url":"https://example.com/check",
					"log_excerpt":"panic: boom",
					"annotations":["foo_test.go:12 expected true"]
				}
			]
		}
	}`)

	parsed, err := ParseRevisionContext(raw)
	require.NoError(t, err, "ParseRevisionContext should decode a valid revision payload")
	formatted := FormatRevisionContextForContinuation(parsed)
	require.Contains(t, formatted, "## Revision context", "FormatRevisionContextForContinuation should include the revision section when feedback exists")
	require.Contains(t, formatted, "Please address the review feedback.", "FormatRevisionContextForContinuation should include formatted feedback")
	require.Contains(t, formatted, "Summary: Two issues remain.", "FormatRevisionContextForContinuation should include the comment summary")
	require.Contains(t, formatted, "Previous diff:", "FormatRevisionContextForContinuation should include the previous diff block")
	require.Contains(t, formatted, "## Pull request repair context", "FormatRevisionContextForContinuation should include the repair section when repair context exists")
	require.Contains(t, formatted, "Repair action: `fix_tests`", "FormatRevisionContextForContinuation should include the repair action")
	require.Contains(t, formatted, "PR #42 in `assembledhq/143`.", "FormatRevisionContextForContinuation should identify the PR being repaired")
	require.Contains(t, formatted, "Merge state: `conflicted`", "FormatRevisionContextForContinuation should include the merge state")
	require.Contains(t, formatted, "annotation: foo_test.go:12 expected true", "FormatRevisionContextForContinuation should include failing-check annotations")
	require.Contains(t, formatted, "log excerpt: panic: boom", "FormatRevisionContextForContinuation should include failing-check log excerpts")
	require.Contains(t, formatted, "details: https://example.com/check", "FormatRevisionContextForContinuation should include failing-check details links")

	require.Equal(t, "", FormatRevisionContextForContinuation(nil), "FormatRevisionContextForContinuation should return an empty string for nil input")
	require.Equal(t, "", FormatRevisionContextForContinuation(&RevisionContext{}), "FormatRevisionContextForContinuation should return an empty string for empty revision context")
	require.Equal(t, models.PullRequestRepairActionTypeFixTests, parsed.RepairAction, "ParseRevisionContext should decode the repair action type")

	require.NotContains(t, formatted, "Conflict resolution guidance", "FormatRevisionContextForContinuation should not inject conflict guidance for fix_tests repair runs")
}

func TestFormatRevisionContextForContinuation_ResolveConflictsGuidance(t *testing.T) {
	t.Parallel()

	ctx := &RevisionContext{
		RepairAction: models.PullRequestRepairActionTypeResolveConflicts,
		RepairContext: &PullRequestRepairContext{
			PullRequestNumber: 7,
			Repository:        "assembledhq/143",
			HeadSHA:           "headsha",
			BaseSHA:           "basesha",
			MergeState:        models.PullRequestMergeStateConflicted,
			HasConflicts:      true,
		},
	}

	formatted := FormatRevisionContextForContinuation(ctx)
	require.Contains(t, formatted, "Conflict resolution guidance", "resolve_conflicts continuation should include the conflict guidance header")
	require.Contains(t, formatted, "merge index", "resolve_conflicts continuation should warn that mid-merge git diff/status reflects the merge index")
	require.Contains(t, formatted, "git diff basesha...HEAD", "resolve_conflicts continuation should reference the base SHA when verifying the net delta")
}
