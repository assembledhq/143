package readiness

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestEvaluatorEvaluate(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	sessionID := uuid.New()
	snapshotKey := "snap-a"
	now := time.Date(2026, 6, 19, 12, 0, 0, 0, time.UTC)
	cleanLoop := models.SessionReviewLoop{
		ID:                  uuid.New(),
		OrgID:               orgID,
		SessionID:           sessionID,
		Status:              models.ReviewLoopStatusClean,
		LatestCheckpointKey: &snapshotKey,
	}
	diffStats := json.RawMessage(`{"added":12,"removed":3,"files_changed":2}`)

	tests := []struct {
		name      string
		input     EvaluationInput
		expected  models.PRReadinessRunStatus
		checkWant map[models.PRReadinessCheckType]models.PRReadinessCheckStatus
	}{
		{
			name: "ready without review blockers",
			input: EvaluationInput{
				Session: models.Session{
					ID:                sessionID,
					OrgID:             orgID,
					SnapshotKey:       &snapshotKey,
					WorkspaceRevision: 7,
					DiffStats:         diffStats,
				},
				EvaluatedWorkspaceRevision: 7,
				EvaluatedSnapshotKey:       snapshotKey,
				LatestReviewLoop:           &cleanLoop,
				Logs: []models.SessionLog{
					{Message: "go test ./... passed", Timestamp: now},
				},
				LinkedIssueCount: 1,
			},
			expected: models.PRReadinessRunStatusPassed,
			checkWant: map[models.PRReadinessCheckType]models.PRReadinessCheckStatus{
				models.PRReadinessCheckTypeFreshness:        models.PRReadinessCheckStatusPassed,
				models.PRReadinessCheckTypeAgentReviewClean: models.PRReadinessCheckStatusPassed,
				models.PRReadinessCheckTypeRiskFlags:        models.PRReadinessCheckStatusPassed,
				models.PRReadinessCheckTypeContextComplete:  models.PRReadinessCheckStatusPassed,
			},
		},
		{
			name: "blocked by stale revision and failed review",
			input: EvaluationInput{
				Session: models.Session{
					ID:                sessionID,
					OrgID:             orgID,
					SnapshotKey:       &snapshotKey,
					WorkspaceRevision: 8,
					DiffStats:         diffStats,
				},
				EvaluatedWorkspaceRevision: 7,
				EvaluatedSnapshotKey:       snapshotKey,
				LatestReviewLoop: &models.SessionReviewLoop{
					ID:                  uuid.New(),
					OrgID:               orgID,
					SessionID:           sessionID,
					Status:              models.ReviewLoopStatusFailed,
					LatestCheckpointKey: &snapshotKey,
				},
			},
			expected: models.PRReadinessRunStatusBlocked,
			checkWant: map[models.PRReadinessCheckType]models.PRReadinessCheckStatus{
				models.PRReadinessCheckTypeFreshness:        models.PRReadinessCheckStatusFailed,
				models.PRReadinessCheckTypeAgentReviewClean: models.PRReadinessCheckStatusFailed,
			},
		},
		{
			name: "warnings for sensitive files",
			input: EvaluationInput{
				Session: models.Session{
					ID:                sessionID,
					OrgID:             orgID,
					SnapshotKey:       &snapshotKey,
					WorkspaceRevision: 7,
					DiffStats:         diffStats,
				},
				EvaluatedWorkspaceRevision: 7,
				EvaluatedSnapshotKey:       snapshotKey,
				LatestReviewLoop:           &cleanLoop,
				ChangedFiles:               []string{"internal/api/middleware/auth.go"},
				LinkedIssueCount:           1,
			},
			expected: models.PRReadinessRunStatusWarnings,
			checkWant: map[models.PRReadinessCheckType]models.PRReadinessCheckStatus{
				models.PRReadinessCheckTypeRiskFlags: models.PRReadinessCheckStatusWarning,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result, err := NewEvaluator(models.DefaultPRReadinessPolicy()).Evaluate(context.Background(), tt.input)
			require.NoError(t, err, "Evaluate should not return an error")
			require.Equal(t, tt.expected, result.Status, "Evaluate should derive the expected aggregate run status")
			checksByType := make(map[models.PRReadinessCheckType]models.PRReadinessCheck)
			for _, check := range result.Checks {
				checksByType[check.CheckType] = check
			}
			for checkType, expected := range tt.checkWant {
				require.Equal(t, expected, checksByType[checkType].Status, "Evaluate should derive the expected check status")
			}
		})
	}
}

func TestRiskFlagsNoDuplicates(t *testing.T) {
	t.Parallel()

	e := NewEvaluator(models.DefaultPRReadinessPolicy())
	input := EvaluationInput{
		Session:                    models.Session{WorkspaceRevision: 1},
		EvaluatedWorkspaceRevision: 1,
		ChangedFiles: []string{
			"migrations/000001_init.up.sql",
			"migrations/000002_users.up.sql",
			"internal/auth/middleware.go",
			"internal/billing/handler.go",
		},
	}
	check := e.riskFlagsCheck(input)

	var details struct {
		Flags []string `json:"flags"`
	}
	require.NoError(t, json.Unmarshal(check.Details, &details))
	seen := map[string]bool{}
	for _, f := range details.Flags {
		require.False(t, seen[f], "flag %q appears more than once in risk flags details", f)
		seen[f] = true
	}
	require.Equal(t, models.PRReadinessCheckStatusWarning, check.Status)
}
