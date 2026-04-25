package agent

import (
	"context"
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
	t.Setenv("SANDBOX_CPU_LIMIT", "3.5")
	t.Setenv("SANDBOX_MEMORY_LIMIT_MB", "6144")
	t.Setenv("SANDBOX_DISK_LIMIT_GB", "24")

	cfg := DefaultSandboxConfig()

	require.Equal(t, "ghcr.io/example/custom:latest", cfg.Image, "DefaultSandboxConfig should use SANDBOX_IMAGE when provided")
	require.Equal(t, 3.5, cfg.CPULimit, "DefaultSandboxConfig should use SANDBOX_CPU_LIMIT when provided")
	require.Equal(t, 6144, cfg.MemoryLimitMB, "DefaultSandboxConfig should use SANDBOX_MEMORY_LIMIT_MB when provided")
	require.Equal(t, 24, cfg.DiskLimitGB, "DefaultSandboxConfig should use SANDBOX_DISK_LIMIT_GB when provided")
}

//nolint:paralleltest // uses t.Setenv
func TestDefaultSandboxConfig_InvalidEnvironmentOverridesFallbackToDefaults(t *testing.T) {
	t.Setenv("SANDBOX_CPU_LIMIT", "not-a-number")
	t.Setenv("SANDBOX_MEMORY_LIMIT_MB", "-1")
	t.Setenv("SANDBOX_DISK_LIMIT_GB", "0")

	cfg := DefaultSandboxConfig()

	require.Equal(t, 2.0, cfg.CPULimit, "DefaultSandboxConfig should fall back to default CPU limit for invalid SANDBOX_CPU_LIMIT")
	require.Equal(t, 4096, cfg.MemoryLimitMB, "DefaultSandboxConfig should fall back to default memory limit for invalid SANDBOX_MEMORY_LIMIT_MB")
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
}

// stubReviewAdapter implements both AgentAdapter and ReviewCapableAdapter so
// the helpers can be exercised against a known-good fake without depending
// on the concrete adapter packages.
type stubReviewAdapter struct {
	name  models.AgentType
	modes []models.SessionReviewMode
}

func (s *stubReviewAdapter) Name() models.AgentType { return s.name }
func (s *stubReviewAdapter) PreparePrompt(_ context.Context, _ *AgentInput) (*AgentPrompt, error) {
	return nil, nil
}
func (s *stubReviewAdapter) Execute(_ context.Context, _ *Sandbox, _ *AgentPrompt, _ chan<- LogEntry) (*AgentResult, error) {
	return nil, nil
}
func (s *stubReviewAdapter) ReviewModes() []models.SessionReviewMode { return s.modes }

type stubBareAdapter struct{ name models.AgentType }

func (s *stubBareAdapter) Name() models.AgentType { return s.name }
func (s *stubBareAdapter) PreparePrompt(_ context.Context, _ *AgentInput) (*AgentPrompt, error) {
	return nil, nil
}
func (s *stubBareAdapter) Execute(_ context.Context, _ *Sandbox, _ *AgentPrompt, _ chan<- LogEntry) (*AgentResult, error) {
	return nil, nil
}

func TestAdapterReviewModes(t *testing.T) {
	t.Parallel()

	require.Nil(t, AdapterReviewModes(nil), "nil adapter has no review modes")

	bare := &stubBareAdapter{name: models.AgentTypeGeminiCLI}
	require.Nil(t, AdapterReviewModes(bare), "adapters that don't implement ReviewCapableAdapter should report no modes")

	emptyCapable := &stubReviewAdapter{name: models.AgentTypePi}
	require.Nil(t, AdapterReviewModes(emptyCapable), "adapters returning an empty slice are treated as not review-capable")

	full := &stubReviewAdapter{
		name:  models.AgentTypeClaudeCode,
		modes: []models.SessionReviewMode{models.SessionReviewModeDefault, models.SessionReviewModeSecurity},
	}
	require.Equal(
		t,
		[]models.SessionReviewMode{models.SessionReviewModeDefault, models.SessionReviewModeSecurity},
		AdapterReviewModes(full),
	)

	require.True(t, AdapterSupportsReviewMode(full, models.SessionReviewModeSecurity))
	require.False(t, AdapterSupportsReviewMode(full, models.SessionReviewMode("unknown")))
	require.False(t, AdapterSupportsReviewMode(bare, models.SessionReviewModeDefault))
}

func TestReviewModeProvider(t *testing.T) {
	t.Parallel()

	full := &stubReviewAdapter{
		name:  models.AgentTypeClaudeCode,
		modes: []models.SessionReviewMode{models.SessionReviewModeDefault},
	}
	bare := &stubBareAdapter{name: models.AgentTypeCodex}

	provider := ReviewModeProvider(map[models.AgentType]AgentAdapter{
		models.AgentTypeClaudeCode: full,
		models.AgentTypeCodex:      bare,
	})

	require.Equal(t, []models.SessionReviewMode{models.SessionReviewModeDefault}, provider(models.AgentTypeClaudeCode))
	require.Nil(t, provider(models.AgentTypeCodex), "agents whose adapter lacks ReviewCapableAdapter must report no modes")
	require.Nil(t, provider(models.AgentTypeAmp), "unknown agent types should report no modes")
}

func TestAgentPrompt_IsReview(t *testing.T) {
	t.Parallel()

	require.False(t, (*AgentPrompt)(nil).IsReview(), "nil prompt is not a review")
	require.False(t, (&AgentPrompt{}).IsReview(), "prompt with no revision context is not a review")
	require.False(t, (&AgentPrompt{RevisionContext: &RevisionContext{}}).IsReview(), "prompt with revision context but no review context is not a review")
	require.True(t, (&AgentPrompt{
		RevisionContext: &RevisionContext{
			ReviewContext: &SessionReviewContext{Mode: models.SessionReviewModeDefault},
		},
	}).IsReview(), "prompt with review context is a review")
}
