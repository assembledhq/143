package models

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCodeReviewReviewerCountCompatibility(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		roster   string
		expected int
	}{
		{name: "legacy single reviewer", roster: `{"reviewers":["codex"]}`, expected: 1},
		{name: "legacy two reviewers", roster: `{"reviewers":["codex","claude_code"]}`, expected: 2},
		{name: "legacy three reviewers", roster: `{"reviewers":["codex","claude_code","opencode"]}`, expected: 3},
		{name: "explicit count keeps extra models as fallbacks", roster: `{"reviewer_count":2,"reviewers":["codex","claude_code","opencode","codex"]}`, expected: 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var roster CodeReviewAgentRoster
			require.NoError(t, json.Unmarshal([]byte(tt.roster), &roster), "stored roster should decode without a schema migration")
			config := DefaultCodeReviewPolicyConfig()
			config.AgentRoster = roster
			resolved := ResolveCodeReviewPolicyConfig(&config)
			require.Equal(t, tt.expected, resolved.AgentRoster.ReviewerCount, "resolution should preserve the requested number of reviews independently of fallback count")
			require.Equal(t, tt.expected, roster.EffectiveReviewerCount(), "unresolved captured rosters should use the same reviewer count")
			require.Equal(t, roster, config.AgentRoster, "resolution must not mutate the captured policy")
		})
	}
}

func TestCodeReviewRankedRosterValidation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		count       int
		models      int
		quorum      int
		expectedErr string
	}{
		{name: "two reviewers and two fallbacks", count: 2, models: 4, quorum: 2},
		{name: "maximum ranked models", count: 3, models: MaxCodeReviewReviewerModels, quorum: 2},
		{name: "legacy count", count: 0, models: 2, quorum: 2},
		{name: "negative count", count: -1, models: 2, quorum: 1, expectedErr: "reviewer_count"},
		{name: "count exceeds thread budget", count: 4, models: 4, quorum: 2, expectedErr: "reviewer_count"},
		{name: "count exceeds ranked models", count: 3, models: 2, quorum: 2, expectedErr: "reviewer_count"},
		{name: "quorum exceeds requested count", count: 1, models: 4, quorum: 2, expectedErr: "require_reviewer_quorum"},
		{name: "too many ranked models", count: 2, models: MaxCodeReviewReviewerModels + 1, quorum: 2, expectedErr: "ranked reviewer models"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			config := DefaultCodeReviewPolicyConfig()
			config.AgentRoster.ReviewerCount = tt.count
			config.AgentRoster.RequireReviewerQuorum = tt.quorum
			config.AgentRoster.Reviewers = make([]AgentType, tt.models)
			config.AgentRoster.ReviewerModels = make([]string, tt.models)
			config.AgentRoster.ReviewerReasoningEfforts = make([]ReasoningEffort, tt.models)
			for i := range config.AgentRoster.Reviewers {
				config.AgentRoster.Reviewers[i] = AgentTypeCodex
				config.AgentRoster.ReviewerModels[i] = DefaultCodexModel
				config.AgentRoster.ReviewerReasoningEfforts[i] = ReasoningEffortHigh
			}
			err := config.Validate()
			if tt.expectedErr != "" {
				require.ErrorContains(t, err, tt.expectedErr, "invalid ranked rosters should identify the policy field to correct")
				return
			}
			require.NoError(t, err, "reviewer count and quorum should be independent of unused fallback choices")
		})
	}
}
