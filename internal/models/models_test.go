package models

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestRepository_IsActive(t *testing.T) {
	t.Parallel()

	cases := []struct {
		status RepositoryStatus
		want   bool
	}{
		{RepositoryStatusActive, true},
		{RepositoryStatusDisconnected, false},
		{"", false},
		{"unknown", false},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(string(tc.status), func(t *testing.T) {
			t.Parallel()
			repo := Repository{Status: string(tc.status)}
			if got := repo.IsActive(); got != tc.want {
				t.Fatalf("IsActive() = %v, want %v (status %q)", got, tc.want, tc.status)
			}
		})
	}
}

func TestSessionListItem_MarshalJSON_PreservesEnrichment(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC().Round(time.Second)
	repositoryID := uuid.New()
	prNumber := 42
	item := SessionListItem{
		Session: Session{
			ID:               uuid.New(),
			IssueID:          uuid.New(),
			OrgID:            uuid.New(),
			Origin:           SessionOriginIssueTrigger,
			InteractionMode:  SessionInteractionModeSingleRun,
			ValidationPolicy: SessionValidationPolicyOnTurnComplete,
			AgentType:        AgentTypeClaudeCode,
			Status:           string(SessionStatusRunning),
			AutonomyLevel:    "supervised",
			TokenMode:        "low",
			LastActivityAt:   now,
			RepositoryID:     &repositoryID,
			CreatedAt:        now,
		},
		LastViewedAt: &now,
		PRSummary: &PRSummary{
			Status:   "open",
			CIStatus: "green",
			Number:   prNumber,
			URL:      "https://example.com/pr/42",
		},
	}

	raw, err := json.Marshal(item)
	require.NoError(t, err, "marshaling a SessionListItem should succeed")

	var decoded map[string]any
	require.NoError(t, json.Unmarshal(raw, &decoded), "marshaled SessionListItem should decode into a map")
	require.Contains(t, decoded, "last_viewed_at", "SessionListItem JSON should preserve last_viewed_at from the wrapper")
	require.Contains(t, decoded, "pr_summary", "SessionListItem JSON should preserve pr_summary from the wrapper")
}
