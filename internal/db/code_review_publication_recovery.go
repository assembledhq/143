package db

import (
	"context"
	"time"

	"github.com/google/uuid"
)

// CodeReviewPublicationReconciliationWindow is measured from creation, not the last retry. An
// unresolved external send keeps its publication fence after automatic work
// stops; only an independently verified receipt can complete it.
const CodeReviewPublicationReconciliationWindow = 2 * time.Hour

const CodeReviewPublicationOperatorRequired = "operator_reconciliation_required: automatic reconciliation stopped after the assessment exceeded two hours; verify the original GitHub publication key and commit before resolving this reservation"

// PauseExpiredPublication changes only the operational detail, never the
// immutable outcome, publication receipt, or active assessment reservation.
func (s *CodeReviewAssessmentStore) PauseExpiredPublication(ctx context.Context, orgID, id uuid.UUID, generation int64, inputDigest string) (bool, error) {
	tag, err := s.db.Exec(ctx, `UPDATE code_review_revision_assessments SET failure_detail=$5
	 WHERE org_id=$1 AND id=$2 AND generation=$3 AND input_digest=$4
	 AND status='publishing' AND publication_state='uncertain' AND result_origin IS NOT NULL
	 AND created_at <= now()-make_interval(secs => $6)`, orgID, id, generation, inputDigest, CodeReviewPublicationOperatorRequired, CodeReviewPublicationReconciliationWindow.Seconds())
	return tag.RowsAffected() == 1, err
}
