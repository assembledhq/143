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
			name: "ready with test evidence",
			input: EvaluationInput{
				Session: models.Session{
					ID:                  sessionID,
					OrgID:               orgID,
					SnapshotKey:         &snapshotKey,
					WorkspaceGeneration: 7,
					DiffStats:           diffStats,
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
				models.PRReadinessCheckTypeFreshness:           models.PRReadinessCheckStatusPassed,
				models.PRReadinessCheckTypeAgentReviewClean:    models.PRReadinessCheckStatusPassed,
				models.PRReadinessCheckTypeTestEvidencePresent: models.PRReadinessCheckStatusPassed,
				models.PRReadinessCheckTypeRiskFlags:           models.PRReadinessCheckStatusPassed,
				models.PRReadinessCheckTypeContextComplete:     models.PRReadinessCheckStatusPassed,
			},
		},
		{
			name: "blocked by stale revision and failed review",
			input: EvaluationInput{
				Session: models.Session{
					ID:                  sessionID,
					OrgID:               orgID,
					SnapshotKey:         &snapshotKey,
					WorkspaceGeneration: 8,
					DiffStats:           diffStats,
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
			name: "warnings for missing tests and sensitive files",
			input: EvaluationInput{
				Session: models.Session{
					ID:                  sessionID,
					OrgID:               orgID,
					SnapshotKey:         &snapshotKey,
					WorkspaceGeneration: 7,
					DiffStats:           diffStats,
				},
				EvaluatedWorkspaceRevision: 7,
				EvaluatedSnapshotKey:       snapshotKey,
				LatestReviewLoop:           &cleanLoop,
				ChangedFiles:               []string{"internal/api/middleware/auth.go"},
				LinkedIssueCount:           1,
			},
			expected: models.PRReadinessRunStatusWarnings,
			checkWant: map[models.PRReadinessCheckType]models.PRReadinessCheckStatus{
				models.PRReadinessCheckTypeTestEvidencePresent: models.PRReadinessCheckStatusWarning,
				models.PRReadinessCheckTypeRiskFlags:           models.PRReadinessCheckStatusWarning,
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
