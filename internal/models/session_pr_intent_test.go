package models

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNormalizeTeamReviewers(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		repository string
		values     []string
		expected   []string
		wantError  bool
	}{
		{name: "empty", repository: "example/project", expected: []string{}},
		{name: "canonical stable deduplication", repository: "Example/project", values: []string{" @EXAMPLE/Review-Team ", "review-team", "example/security"}, expected: []string{"example/review-team", "example/security"}},
		{name: "single character owner and slug", repository: "a/project", values: []string{"b"}, expected: []string{"a/b"}},
		{name: "other organization", repository: "example/project", values: []string{"other/reviewers"}, wantError: true},
		{name: "empty slug", repository: "example/project", values: []string{"example/"}, wantError: true},
		{name: "empty value", repository: "example/project", values: []string{" "}, wantError: true},
		{name: "ambiguous mention", repository: "example/project", values: []string{"@reviewers"}, wantError: true},
		{name: "nested path", repository: "example/project", values: []string{"example/team/subteam"}, wantError: true},
		{name: "url", repository: "example/project", values: []string{"https://github.com/orgs/example/teams/reviewers"}, wantError: true},
		{name: "invalid slug", repository: "example/project", values: []string{"review_team"}, wantError: true},
		{name: "trailing hyphen", repository: "example/project", values: []string{"reviewers-"}, wantError: true},
		{name: "unicode", repository: "example/project", values: []string{"réviewers"}, wantError: true},
		{name: "too long", repository: "example/project", values: []string{strings.Repeat("a", 101)}, wantError: true},
		{name: "too many", repository: "example/project", values: make([]string, MaxTeamReviewers+1), wantError: true},
		{name: "missing repository owner", repository: "project", wantError: true},
		{name: "invalid repository owner", repository: "-example/project", wantError: true},
		{name: "missing repository name", repository: "example/", wantError: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			actual, err := NormalizeTeamReviewers(tt.repository, tt.values)
			if tt.wantError {
				require.Error(t, err, "malformed or cross-organization teams must be rejected")
				return
			}
			require.NoError(t, err, "repository-scoped team reviewers should normalize")
			require.Equal(t, tt.expected, actual, "normalization should preserve stable canonical team identities")
		})
	}
}

func TestTeamReviewRequestStateValidate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		state TeamReviewRequestState
		valid bool
	}{
		{TeamReviewRequestSending, true},
		{TeamReviewRequestRequested, true},
		{TeamReviewRequestRetryable, true},
		{"", false},
		{"unknown", false},
	}
	for _, tt := range tests {
		t.Run(string(tt.state), func(t *testing.T) {
			t.Parallel()
			err := tt.state.Validate()
			if tt.valid {
				require.NoError(t, err, "known request states should validate")
			} else {
				require.Error(t, err, "unknown request states must be rejected")
			}
		})
	}
}

func TestNormalizeStagedPRTitle(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		value     string
		expected  string
		wantError bool
	}{
		{name: "verbatim marker prefix", value: "feature-brief: Plan billing exports", expected: "feature-brief: Plan billing exports"},
		{name: "surrounding whitespace trimmed", value: "  docs: Weekly report \t", expected: "docs: Weekly report"},
		{name: "exact byte limit", value: strings.Repeat("a", MaxStagedPRTitleBytes), expected: strings.Repeat("a", MaxStagedPRTitleBytes)},
		{name: "empty", value: "", wantError: true},
		{name: "blank", value: " \t ", wantError: true},
		{name: "multiline", value: "first line\nsecond line", wantError: true},
		{name: "carriage return", value: "first line\rsecond", wantError: true},
		{name: "over byte limit", value: strings.Repeat("a", MaxStagedPRTitleBytes+1), wantError: true},
		{name: "multibyte over byte limit", value: strings.Repeat("é", MaxStagedPRTitleBytes/2+1), wantError: true},
		{name: "embedded NUL", value: "docs:\x00report", wantError: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			actual, err := NormalizeStagedPRTitle(tt.value)
			if tt.wantError {
				require.Error(t, err, "titles GitHub, Postgres, or the PR service would alter or refuse must be rejected at staging")
				return
			}
			require.NoError(t, err, "single-line titles within the cap should stage")
			require.Equal(t, tt.expected, actual, "normalization should only trim surrounding whitespace")
		})
	}
}

func TestValidateStagedPRBody(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		value     string
		wantError bool
	}{
		{name: "markdown with marker line", value: "## Summary\n\nSource plan: docs/plans/billing.md\n"},
		{name: "exact character limit", value: strings.Repeat("é", MaxStagedPRBodyLength)},
		{name: "empty", value: "", wantError: true},
		{name: "blank", value: "\n\t \n", wantError: true},
		{name: "over character limit", value: strings.Repeat("a", MaxStagedPRBodyLength+1), wantError: true},
		{name: "embedded NUL", value: "Source plan:\x00 docs/plans/exports.md", wantError: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := ValidateStagedPRBody(tt.value)
			if tt.wantError {
				require.Error(t, err, "blank, oversized, or unstorable descriptions must be rejected at staging")
				return
			}
			require.NoError(t, err, "non-blank descriptions within the limit should stage verbatim")
		})
	}
}
