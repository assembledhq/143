package db

import (
	"context"
	"errors"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const codeReviewTerminalScheduleOwnerQuery = `SELECT a.id FROM code_review_revision_assessments a
 JOIN code_review_session_metadata m ON m.org_id=a.org_id AND m.id=a.metadata_id AND m.session_id=a.session_id
 AND m.repository_id=a.repository_id AND m.pull_request_id=a.pull_request_id AND m.policy_id=a.policy_id
 WHERE a.org_id=$1 AND a.id=$2 AND a.session_id=$3 AND a.metadata_id=$4
 AND a.repository_id=$5 AND a.pull_request_id=$6 AND a.policy_id=$7
 AND a.head_sha=$8 AND a.publication_key=$9 AND a.review_scope=$10
 AND ($11='run_code_review_recheck' OR ($11='run_code_review'
 AND m.head_sha=$8 AND m.review_output_key=$9
 AND (m.assessment_id IS NULL OR m.assessment_id=a.id)))
 AND a.status IN ('failed','completed','cancelled','superseded') FOR UPDATE OF a`

// SettleCodeReviewTerminalAssessment preserves the original postcommit
// scheduler cleanup attempt even when retirement did not change an already
// terminal assessment. Only a known-committed exact owner can enter; a fresh
// bounded unit revalidates that fixed association before the existing guarded
// request/scheduler settlement. It never fails an assessment or rediscovers a
// latest owner. Delivery remains best effort, not an outbox guarantee.
func SettleCodeReviewTerminalAssessment(ctx context.Context, orgID uuid.UUID, transactions CodeReviewMetadataTransactions, outcome CodeReviewTerminalOutcome) error {
	if ctx == nil || orgID == uuid.Nil || nilLifecycleDependency(transactions) || !outcome.owned || !outcome.hasOwner || !outcome.committed || outcome.scope.OrgID != orgID {
		return ErrCodeReviewTerminalCapability
	}
	scope := outcome.scope
	switch scope.JobType {
	case models.JobTypeRunCodeReview, models.JobTypeRunCodeReviewRecheck:
	default:
		return ErrCodeReviewTerminalScope
	}
	if scope.AssessmentID == uuid.Nil {
		// A valid no-assessment legacy owner never acquires a new assessment.
		return nil
	}
	if scope.SessionID == uuid.Nil || scope.MetadataID == uuid.Nil || scope.RepositoryID == uuid.Nil || scope.PullRequestID == uuid.Nil || scope.PolicyID == uuid.Nil || scope.HeadSHA == "" || scope.OutputKey == "" || scope.ReviewScope.Validate() != nil {
		return ErrCodeReviewTerminalScope
	}
	called := false
	err := transactions.Run(ctx, func(unitCtx context.Context, tx pgx.Tx) error {
		called = true
		if err := NewCodeReviewRecheckStore(tx).LockFullAssessmentCompletion(unitCtx, tx, orgID, scope.PullRequestID); err != nil {
			return err
		}
		if err := LockCodeReviewMetadataForCompletionInTx(unitCtx, orgID, tx, scope.SessionID); err != nil {
			return err
		}
		var assessmentID uuid.UUID
		err := tx.QueryRow(unitCtx, codeReviewTerminalScheduleOwnerQuery, orgID, scope.AssessmentID, scope.SessionID, scope.MetadataID,
			scope.RepositoryID, scope.PullRequestID, scope.PolicyID, scope.HeadSHA, scope.OutputKey, scope.ReviewScope, scope.JobType).Scan(&assessmentID)
		if errors.Is(err, pgx.ErrNoRows) {
			// Changed association or nonterminal protected publication cannot
			// become a target merely because the original job has retired.
			return nil
		}
		if err != nil {
			return err
		}
		if assessmentID != scope.AssessmentID {
			return ErrCodeReviewTerminalScope
		}
		return NewCodeReviewScheduleStore(tx).SettleAssessmentInTx(unitCtx, orgID, tx, assessmentID)
	})
	if err != nil {
		return err
	}
	if !called {
		return ErrCodeReviewTerminalCapability
	}
	return nil
}
