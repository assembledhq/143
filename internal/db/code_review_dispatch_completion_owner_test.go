package db

import (
	"context"
	"errors"
	"testing"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

func TestLockCodeReviewAssessmentCompletionOwnerInTx(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, failure string
		status        models.CodeReviewAssessmentStatus
		expected      error
	}{
		{name: "exact running full assessment", status: models.CodeReviewAssessmentRunning},
		{name: "exact publishing full assessment", status: models.CodeReviewAssessmentPublishing},
		{name: "exact already completed full assessment", status: models.CodeReviewAssessmentCompleted},
		{name: "missing or changed association", status: models.CodeReviewAssessmentCompleted, failure: "missing", expected: ErrCodeReviewAssessmentState},
		{name: "incompatible isolation", status: models.CodeReviewAssessmentCompleted, failure: "isolation", expected: ErrCodeReviewMetadataTerminalIsolation},
		{name: "query error", status: models.CodeReviewAssessmentCompleted, failure: "query", expected: ErrCodeReviewMetadataTerminalStorage},
		{name: "scan error", status: models.CodeReviewAssessmentCompleted, failure: "scan", expected: ErrCodeReviewMetadataTerminalStorage},
		{name: "different actual assessment", status: models.CodeReviewAssessmentCompleted, failure: "identity", expected: ErrCodeReviewMetadataTerminalStorage},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock := newCodeReviewMetadataMock(t)
			org, assessment, metadata, session, pr := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
			cause := errors.New("synthetic private owner lock failure")
			isolation := string(pgx.ReadCommitted)
			if tt.failure == "isolation" {
				isolation = string(pgx.RepeatableRead)
			}
			mock.ExpectQuery(`SHOW transaction_isolation`).WillReturnRows(pgxmock.NewRows([]string{"transaction_isolation"}).AddRow(isolation))
			if tt.failure != "isolation" {
				// Pin the actual association predicates independently of the
				// production query constant; mocks do not prove database locking.
				query := mock.ExpectQuery(`SELECT a.id FROM code_review_revision_assessments a
 JOIN code_review_session_metadata m ON m.org_id=a.org_id AND m.id=a.metadata_id AND m.session_id=a.session_id
 AND m.repository_id=a.repository_id AND m.pull_request_id=a.pull_request_id AND m.policy_id=a.policy_id
 WHERE a.org_id=$1 AND a.id=$2 AND a.metadata_id=$3 AND a.session_id=$4 AND a.pull_request_id=$5
 AND a.review_scope='full' AND a.status=$6 AND m.status='completed'
 AND (m.assessment_id IS NULL OR m.assessment_id=a.id)
 FOR UPDATE OF a`).WithArgs(org, assessment, metadata, session, pr, tt.status)
				switch tt.failure {
				case "query":
					query.WillReturnError(cause)
				case "missing":
					query.WillReturnError(pgx.ErrNoRows)
				case "scan":
					query.WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow("synthetic invalid uuid")).RowsWillBeClosed()
				case "identity":
					query.WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(uuid.New())).RowsWillBeClosed()
				default:
					query.WillReturnRows(pgxmock.NewRows([]string{"id"}).AddRow(assessment)).RowsWillBeClosed()
				}
			}
			err := LockCodeReviewAssessmentCompletionOwnerInTx(context.Background(), org, mock, assessment, metadata, session, pr, tt.status)
			if tt.expected == nil {
				require.NoError(t, err, "an exact retained full assessment must be revalidated without rediscovering or mutating another owner")
			} else {
				require.ErrorIs(t, err, tt.expected, "changed scope isolation and actual storage failures must keep their exact safe refusal class")
				require.EqualError(t, err, tt.expected.Error(), "owner lock errors must not expose actual identifiers or driver text")
			}
			if tt.failure == "query" {
				require.ErrorIs(t, err, cause, "safe storage error must preserve its original internal cause")
			}
			require.NoError(t, mock.ExpectationsWereMet(), "revalidation must bind every retained association component before any child or business write")
		})
	}
}

func TestLockCodeReviewAssessmentCompletionOwnerRejectsInvalidScope(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		change func(*[5]uuid.UUID, *models.CodeReviewAssessmentStatus)
	}{
		{name: "nil org", change: func(ids *[5]uuid.UUID, _ *models.CodeReviewAssessmentStatus) { ids[0] = uuid.Nil }},
		{name: "nil assessment", change: func(ids *[5]uuid.UUID, _ *models.CodeReviewAssessmentStatus) { ids[1] = uuid.Nil }},
		{name: "nil metadata", change: func(ids *[5]uuid.UUID, _ *models.CodeReviewAssessmentStatus) { ids[2] = uuid.Nil }},
		{name: "nil session", change: func(ids *[5]uuid.UUID, _ *models.CodeReviewAssessmentStatus) { ids[3] = uuid.Nil }},
		{name: "nil PR", change: func(ids *[5]uuid.UUID, _ *models.CodeReviewAssessmentStatus) { ids[4] = uuid.Nil }},
		{name: "failed assessment", change: func(_ *[5]uuid.UUID, status *models.CodeReviewAssessmentStatus) {
			*status = models.CodeReviewAssessmentFailed
		}},
		{name: "unknown assessment status", change: func(_ *[5]uuid.UUID, status *models.CodeReviewAssessmentStatus) { *status = "synthetic_unknown" }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock := newCodeReviewMetadataMock(t)
			ids := [5]uuid.UUID{uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()}
			status := models.CodeReviewAssessmentCompleted
			tt.change(&ids, &status)
			err := LockCodeReviewAssessmentCompletionOwnerInTx(context.Background(), ids[0], mock, ids[1], ids[2], ids[3], ids[4], status)
			require.ErrorIs(t, err, ErrCodeReviewDispatchCompletionInput, "invalid retained association must be refused before any database query")
			require.EqualError(t, err, ErrCodeReviewDispatchCompletionInput.Error(), "invalid owner input cannot expose actual scope values")
			require.NoError(t, mock.ExpectationsWereMet(), "invalid owner input must not query discover or lock an alternate owner")
		})
	}
}
