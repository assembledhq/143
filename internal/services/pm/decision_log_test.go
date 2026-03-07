package pm

import (
	"testing"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestPlanToDecisionLog_NilPlan(t *testing.T) {
	t.Parallel()
	entries := planToDecisionLog(nil)
	require.Nil(t, entries)
}

func TestPlanToDecisionLog_EmptyPlan(t *testing.T) {
	t.Parallel()
	plan := &Plan{ID: uuid.New(), OrgID: uuid.New()}
	entries := planToDecisionLog(plan)
	require.Empty(t, entries)
}

func TestPlanToDecisionLog_DelegatedTasks(t *testing.T) {
	t.Parallel()

	planID := uuid.New()
	orgID := uuid.New()
	issue1 := uuid.New()
	issue2 := uuid.New()

	plan := &Plan{
		ID:    planID,
		OrgID: orgID,
		Tasks: []Task{
			{
				IssueIDs:  []uuid.UUID{issue1, issue2},
				Reasoning: "they are related",
			},
		},
	}

	entries := planToDecisionLog(plan)
	require.Len(t, entries, 2)
	for _, e := range entries {
		require.Equal(t, planID, e.PlanID)
		require.Equal(t, orgID, e.OrgID)
		require.Equal(t, models.PMDecisionTypeDelegate, e.Decision)
		require.Equal(t, "they are related", e.Reasoning)
	}
}

func TestPlanToDecisionLog_SkippedIssues(t *testing.T) {
	t.Parallel()

	planID := uuid.New()
	orgID := uuid.New()
	skipID := uuid.New()

	plan := &Plan{
		ID:    planID,
		OrgID: orgID,
		SkippedIssues: []SkipEntry{
			{IssueID: skipID, Reason: models.PMSkipReasonDuplicate, Detail: "duplicate of #42"},
		},
	}

	entries := planToDecisionLog(plan)
	require.Len(t, entries, 1)
	require.Equal(t, models.PMDecisionTypeSkip, entries[0].Decision)
	require.Equal(t, "duplicate of #42", entries[0].Reasoning)
	require.Equal(t, &skipID, entries[0].IssueID)
}

func TestPlanToDecisionLog_Clusters(t *testing.T) {
	t.Parallel()

	planID := uuid.New()
	orgID := uuid.New()
	id1 := uuid.New()
	id2 := uuid.New()

	plan := &Plan{
		ID:    planID,
		OrgID: orgID,
		Clusters: []Cluster{
			{IssueIDs: []uuid.UUID{id1, id2}, RootCause: "auth module", Strategy: "fix root"},
		},
	}

	entries := planToDecisionLog(plan)
	require.Len(t, entries, 2)
	for _, e := range entries {
		require.Equal(t, models.PMDecisionTypeCluster, e.Decision)
		require.Contains(t, e.Reasoning, "auth module")
		require.Contains(t, e.Reasoning, "fix root")
	}
}

func TestPlanToDecisionLog_MixedEntries(t *testing.T) {
	t.Parallel()

	plan := &Plan{
		ID:    uuid.New(),
		OrgID: uuid.New(),
		Tasks:         []Task{{IssueIDs: []uuid.UUID{uuid.New()}, Reasoning: "r"}},
		SkippedIssues: []SkipEntry{{IssueID: uuid.New(), Detail: "s"}},
		Clusters:      []Cluster{{IssueIDs: []uuid.UUID{uuid.New()}, RootCause: "rc", Strategy: "st"}},
	}

	entries := planToDecisionLog(plan)
	require.Len(t, entries, 3)
}
