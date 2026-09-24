package db

import (
	"context"

	"github.com/google/uuid"
)

// MarkEvidenceRefreshQueued closes the crash-recovery marker only after a
// replacement request, existing pending wake, or newer active assessment is
// known durable. Repeating it leaves the marker in its terminal queued state.
func (s *CodeReviewScheduleStore) MarkEvidenceRefreshQueued(ctx context.Context, orgID, assessmentID uuid.UUID) error {
	_, err := s.db.Exec(ctx, `UPDATE code_review_revision_assessments
	 SET failure_detail=regexp_replace(failure_detail,'^evidence_recheck:','evidence_recheck_queued:')
	 WHERE org_id=$1 AND id=$2 AND status='superseded' AND failure_detail LIKE 'evidence_recheck:%'`, orgID, assessmentID)
	return err
}
