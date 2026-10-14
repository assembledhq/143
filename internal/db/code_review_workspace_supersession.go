package db

import (
	"context"
	"fmt"
	"strings"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type SupersedeCodeReviewWorkspacePreparationParams struct {
	ReviewID           uuid.UUID
	SessionID          uuid.UUID
	PullRequestID      uuid.UUID
	HeadSHA            string
	ExpectedGeneration int64
	Reason             string
}

// SupersedeCodeReviewWorkspacePreparation settles a moved-ref initializer only
// while its exact unsent review and workspace generation still own the attempt.
func (s *CodeReviewStore) SupersedeCodeReviewWorkspacePreparation(ctx context.Context, orgID uuid.UUID, params SupersedeCodeReviewWorkspacePreparationParams) (models.CodeReviewSessionMetadata, error) {
	var metadata models.CodeReviewSessionMetadata
	err := s.RunWithGitHubPublicationLock(ctx, orgID, params.PullRequestID, func(lockCtx context.Context, tx DBTX) error {
		rows, err := tx.Query(lockCtx, `
			WITH candidate AS (
			    SELECT m.id FROM sessions s
			    JOIN code_review_session_metadata m ON m.org_id = s.org_id AND m.session_id = s.id
			    WHERE m.org_id = @org_id AND m.id = @review_id AND m.session_id = @session_id
			      AND m.pull_request_id = @pull_request_id AND m.head_sha = @head_sha
			      AND m.status IN ('queued', 'running') AND m.stale = false AND m.github_review_id IS NULL
			      AND s.org_id = @org_id AND s.workspace_generation = @generation
			      AND s.status NOT IN ('cancelled', 'completed', 'failed')
			      AND NOT EXISTS (SELECT 1 FROM code_review_revision_assessments a
			          WHERE a.org_id = @org_id AND a.metadata_id = @review_id AND a.session_id = @session_id
			            AND (a.publication_state NOT IN ('not_started', 'not_required')
			              OR a.publication_receipt IS NOT NULL OR a.github_review_id IS NOT NULL OR a.result_origin IS NOT NULL))
			    FOR UPDATE OF s, m
			)
			UPDATE code_review_session_metadata m
			SET status = 'stale', stale = true, phase = NULL, decision = 'blocked', acceptable = false,
			    failure_reason = @reason, status_code = NULL, status_message = NULL,
			    retry_at = NULL, last_error_at = NULL, retryable_failure = false, completed_at = now()
			WHERE m.org_id = @org_id AND m.id IN (SELECT id FROM candidate)
			RETURNING `+codeReviewMetadataColumns, pgx.NamedArgs{
			"org_id": orgID, "review_id": params.ReviewID, "session_id": params.SessionID,
			"pull_request_id": params.PullRequestID, "head_sha": params.HeadSHA,
			"generation": params.ExpectedGeneration, "reason": strings.TrimSpace(params.Reason),
		})
		if err != nil {
			return fmt.Errorf("supersede code review workspace preparation: %w", err)
		}
		metadata, err = collectOneCodeReviewMetadata(rows)
		return err
	})
	if err != nil {
		return models.CodeReviewSessionMetadata{}, err
	}
	s.publishUpdated(ctx, metadata)
	return metadata, nil
}
