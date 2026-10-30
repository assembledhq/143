package db

import (
	"context"
	"errors"

	"github.com/assembledhq/143/internal/codereviewreceipt"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// CodeReviewConfirmedCompletionRecoveryPending belongs only to the exact confirmed assessment. These diagnostics do not
// fail business metadata, change its staged outcome, or authorize publication.
const CodeReviewConfirmedCompletionRecoveryPending = "confirmed_completion_recovery_pending"
const CodeReviewConfirmedCompletionOperatorRequired = "operator_reconciliation_required:confirmed_completion_exhausted"

const codeReviewConfirmedCompletionObservationMaxBytes = codeReviewDispatchPreparationMaxBytes

var ErrCodeReviewConfirmedCompletionObservation = errors.New("confirmed code review completion observation failed")
var ErrCodeReviewConfirmedCompletionObservationTooLarge = errors.New("confirmed code review completion observation exceeds storage limit")

type codeReviewConfirmedCompletionObservationError struct{ cause error }

func (*codeReviewConfirmedCompletionObservationError) Error() string {
	return ErrCodeReviewConfirmedCompletionObservation.Error()
}
func (e *codeReviewConfirmedCompletionObservationError) Unwrap() error { return e.cause }
func (*codeReviewConfirmedCompletionObservationError) Is(target error) bool {
	return target == ErrCodeReviewConfirmedCompletionObservation
}

// found/version describe the candidate even when pending is false. In
// particular an uncertain candidate is not interchangeable with its later
// confirmed version. This is statement-local observation, never authority.
type codeReviewConfirmedCompletionObservation struct {
	found        bool
	assessmentID uuid.UUID
	version      string
	pending      bool
}

// Receipt semantics live only in codereviewreceipt. This SQL proves the
// separate exact-owner, active-metadata, staged-outcome and confirmed-state
// conditions without projecting any body, structured outcome, or full model.
// Stale-but-running metadata remains historical preservation truth; callers
// that authorize a fresh send independently require NOT stale.
const codeReviewConfirmedFullCompletionStructure = `m.org_id=a.org_id AND m.id=a.metadata_id AND m.session_id=a.session_id
 AND m.repository_id=a.repository_id AND m.pull_request_id=a.pull_request_id AND m.policy_id=a.policy_id
 AND m.head_sha=a.head_sha AND m.review_output_key=a.publication_key
 AND (m.assessment_id IS NULL OR m.assessment_id=a.id) AND m.status IN ('queued','running')
 AND a.review_scope='full' AND a.status='publishing' AND a.publication_state='confirmed'
 AND a.result_origin IS NOT NULL AND a.decision IS NOT NULL AND a.acceptable IS NOT NULL
 AND a.rendered_body IS NOT NULL AND a.structured_outcome IS NOT NULL
 AND a.github_review_id IS NOT NULL AND a.submitted_commit_sha=a.head_sha
 AND (m.github_review_id IS NULL OR m.github_review_id=a.github_review_id)`

const codeReviewConfirmedCompletionProjectionFits = `octet_length(COALESCE(a.publication_receipt::text,''))::bigint
 +octet_length(COALESCE(a.input_digest,''))+octet_length(COALESCE(a.head_sha,''))<=$3`

// (org_id,publication_key) is unique. Observe that complete candidate set,
// including ineligible candidates, so later version/absence fences cannot
// overlook a candidate becoming eligible. Every variable-width returned value
// shares one bound before transfer. A structurally confirmed candidate whose
// receipt cannot be inspected is an observation error, not a semantic negative
// receipt. An ineligible candidate needs only its identity/version fence, not
// receipt bytes. JSONB text goes unchanged to the Go decoder.
const codeReviewConfirmedCompletionObservationQuery = `SELECT a.id,COALESCE(a.xmin::text,''),
 COALESCE(` + codeReviewConfirmedFullCompletionStructure + `,false),
 ` + codeReviewConfirmedCompletionProjectionFits + `,
 CASE WHEN ` + codeReviewConfirmedCompletionProjectionFits + ` THEN a.publication_receipt::text END,
 CASE WHEN ` + codeReviewConfirmedCompletionProjectionFits + ` THEN COALESCE(a.input_digest,'') ELSE '' END,
 CASE WHEN ` + codeReviewConfirmedCompletionProjectionFits + ` THEN COALESCE(a.head_sha,'') ELSE '' END,
 a.github_review_id
 FROM code_review_session_metadata m LEFT JOIN code_review_revision_assessments a
 ON a.org_id=m.org_id AND a.publication_key=m.review_output_key
 WHERE m.org_id=$1 AND m.id=$2`

// observeConfirmedFullAssessmentCompletionPending is a read-only supplied-DBTX
// helper. Its caller owns any required metadata/assessment locks and freshness
// fences. It never borrows a pool, locks, retries, settles, or publishes.
func observeConfirmedFullAssessmentCompletionPending(ctx context.Context, q DBTX, orgID, metadataID uuid.UUID) (codeReviewConfirmedCompletionObservation, error) {
	if ctx == nil || nilLifecycleDependency(q) || orgID == uuid.Nil || metadataID == uuid.Nil {
		return codeReviewConfirmedCompletionObservation{}, ErrCodeReviewConfirmedCompletionObservation
	}
	if err := ctx.Err(); err != nil {
		return codeReviewConfirmedCompletionObservation{}, &codeReviewConfirmedCompletionObservationError{cause: err}
	}
	var id *uuid.UUID
	var version, digest, head string
	var structural, fits bool
	var receipt []byte
	var reviewID *int64
	err := q.QueryRow(ctx, codeReviewConfirmedCompletionObservationQuery, orgID, metadataID, codeReviewConfirmedCompletionObservationMaxBytes).
		Scan(&id, &version, &structural, &fits, &receipt, &digest, &head, &reviewID)
	if errors.Is(err, pgx.ErrNoRows) {
		return codeReviewConfirmedCompletionObservation{}, nil
	}
	if err != nil {
		return codeReviewConfirmedCompletionObservation{}, &codeReviewConfirmedCompletionObservationError{cause: err}
	}
	if err := ctx.Err(); err != nil {
		return codeReviewConfirmedCompletionObservation{}, &codeReviewConfirmedCompletionObservationError{cause: err}
	}
	if id == nil {
		if version != "" || structural {
			return codeReviewConfirmedCompletionObservation{}, ErrCodeReviewConfirmedCompletionObservation
		}
		return codeReviewConfirmedCompletionObservation{}, nil
	}
	if *id == uuid.Nil || version == "" {
		return codeReviewConfirmedCompletionObservation{}, ErrCodeReviewConfirmedCompletionObservation
	}
	observation := codeReviewConfirmedCompletionObservation{found: true, assessmentID: *id, version: version}
	if structural {
		if !fits {
			return codeReviewConfirmedCompletionObservation{}, &codeReviewConfirmedCompletionObservationError{cause: ErrCodeReviewConfirmedCompletionObservationTooLarge}
		}
		observation.pending = reviewID != nil && codereviewreceipt.Matches(receipt, id.String(), digest, head, *reviewID)
	}
	if err := ctx.Err(); err != nil {
		return codeReviewConfirmedCompletionObservation{}, &codeReviewConfirmedCompletionObservationError{cause: err}
	}
	return observation, nil
}

// HasConfirmedFullAssessmentCompletionPending recognizes the exact original
// metadata's retained confirmed recovery owner even without a live queue job.
// Observation errors never become negative liveness evidence.
func (s *CodeReviewStore) HasConfirmedFullAssessmentCompletionPending(ctx context.Context, orgID, metadataID uuid.UUID) (bool, error) {
	if s == nil {
		return false, ErrCodeReviewConfirmedCompletionObservation
	}
	observation, err := observeConfirmedFullAssessmentCompletionPending(ctx, s.db, orgID, metadataID)
	if err != nil {
		return false, err
	}
	return observation.pending, nil
}
