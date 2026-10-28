package db

import (
	"context"
	"errors"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

var ErrCodeReviewDispatchCompletionInput = errors.New("invalid code review dispatch completion scope")

const codeReviewAssessmentCompletionOwnerLock = `SELECT a.id FROM code_review_revision_assessments a
 JOIN code_review_session_metadata m ON m.org_id=a.org_id AND m.id=a.metadata_id AND m.session_id=a.session_id
 AND m.repository_id=a.repository_id AND m.pull_request_id=a.pull_request_id AND m.policy_id=a.policy_id
 WHERE a.org_id=$1 AND a.id=$2 AND a.metadata_id=$3 AND a.session_id=$4 AND a.pull_request_id=$5
 AND a.review_scope='full' AND a.status=$6 AND m.status='completed'
 AND (m.assessment_id IS NULL OR m.assessment_id=a.id)
 FOR UPDATE OF a`

// LockCodeReviewAssessmentCompletionOwnerInTx rechecks one retained assessment
// against its independently bound completed metadata under the existing PR
// domain. The caller must already hold the ordered session metadata locks;
// only then may this helper lock the assessment, before evidence/child writes.
// It never discovers another latest owner, rewrites business data, or owns
// transaction settings, settlement, retries or effects.
func LockCodeReviewAssessmentCompletionOwnerInTx(ctx context.Context, orgID uuid.UUID, tx pgx.Tx, assessmentID, metadataID, sessionID, prID uuid.UUID, status models.CodeReviewAssessmentStatus) error {
	if orgID == uuid.Nil || assessmentID == uuid.Nil || metadataID == uuid.Nil || sessionID == uuid.Nil || prID == uuid.Nil {
		return ErrCodeReviewDispatchCompletionInput
	}
	switch status {
	case models.CodeReviewAssessmentRunning, models.CodeReviewAssessmentPublishing, models.CodeReviewAssessmentCompleted:
	default:
		return ErrCodeReviewDispatchCompletionInput
	}
	if err := requireCodeReviewMetadataTerminalTx(ctx, tx); err != nil {
		return err
	}
	var locked uuid.UUID
	if err := tx.QueryRow(ctx, codeReviewAssessmentCompletionOwnerLock, orgID, assessmentID, metadataID, sessionID, prID, status).Scan(&locked); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrCodeReviewAssessmentState
		}
		return &codeReviewMetadataTerminalError{cause: err}
	}
	if locked != assessmentID {
		return ErrCodeReviewMetadataTerminalStorage
	}
	if err := ctx.Err(); err != nil {
		return &codeReviewMetadataTerminalError{cause: err}
	}
	return nil
}

// LockCodeReviewMetadataForCompletionInTx prelocks every actual metadata row
// for the exact session before evidence linkage can touch metadata or results.
// Its caller must acquire the existing PR domain lock first and own the entire
// bounded READ COMMITTED unit. This is neither authority nor budget proof; it
// never begins, commits, retries, mutates rows or publishes effects.
func LockCodeReviewMetadataForCompletionInTx(ctx context.Context, orgID uuid.UUID, tx pgx.Tx, sessionID uuid.UUID) error {
	if orgID == uuid.Nil || sessionID == uuid.Nil {
		return ErrCodeReviewDispatchCompletionInput
	}
	ids, err := lockCodeReviewMetadataTerminal(ctx, tx, codeReviewMetadataTerminalSessionLocks, pgx.NamedArgs{
		"org_id": orgID, "session_id": sessionID, "active_only": false,
	})
	if err != nil {
		return err
	}
	if len(ids) == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

const codeReviewDispatchCompletedMetadataLock = `SELECT id,org_id,session_id FROM code_review_session_metadata
 WHERE org_id=$1 AND id=$2 AND session_id=$3 AND status='completed'
 ORDER BY id FOR UPDATE`

// ReconcileCompletedCodeReviewDispatchInTx settles only supported pending
// dispatch on an independently bound, already-completed metadata owner. It
// does not reconstruct completion, alter business evidence or resolve an
// attempted/unknown send. Its caller owns the existing PR domain lock, bounded
// Run, rollback on any error and any post-known-commit effects. Observed
// READ COMMITTED isolation alone does not establish that ownership or budget.
func ReconcileCompletedCodeReviewDispatchInTx(ctx context.Context, orgID uuid.UUID, tx pgx.Tx, metadataID, sessionID uuid.UUID) error {
	if orgID == uuid.Nil || metadataID == uuid.Nil || sessionID == uuid.Nil {
		return ErrCodeReviewDispatchCompletionInput
	}
	if err := requireCodeReviewMetadataTerminalTx(ctx, tx); err != nil {
		return err
	}
	rows, err := tx.Query(ctx, codeReviewDispatchCompletedMetadataLock, orgID, metadataID, sessionID)
	if err != nil {
		return &codeReviewMetadataTerminalError{cause: err}
	}
	// Only the three actual scope columns are read, never payloads or output.
	// CollectRows fully consumes and closes the parent query before the child
	// helper issues a separate statement with a fresh READ COMMITTED snapshot.
	changed, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (models.CodeReviewSessionMetadata, error) {
		var metadata models.CodeReviewSessionMetadata
		err := row.Scan(&metadata.ID, &metadata.OrgID, &metadata.SessionID)
		return metadata, err
	})
	if err != nil {
		return &codeReviewMetadataTerminalError{cause: err}
	}
	if err := ctx.Err(); err != nil {
		return &codeReviewMetadataTerminalError{cause: err}
	}
	if len(changed) == 0 {
		return pgx.ErrNoRows
	}
	if len(changed) != 1 || changed[0].OrgID != orgID || changed[0].ID != metadataID || changed[0].SessionID != sessionID {
		return ErrCodeReviewMetadataTerminalStorage
	}
	return cancelCodeReviewMetadataPendingInTx(ctx, orgID, tx, changed)
}
