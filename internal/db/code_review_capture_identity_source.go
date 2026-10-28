package db

import (
	"context"
	"errors"
	"fmt"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// CodeReviewCaptureIdentitySource is the bounded identity needed to preserve a
// captured review. It carries no outcome, receipt, prompt, or send authority.
type CodeReviewCaptureIdentitySource struct {
	MetadataID    uuid.UUID                      `db:"id"`
	OrgID         uuid.UUID                      `db:"org_id"`
	SessionID     uuid.UUID                      `db:"session_id"`
	PullRequestID uuid.UUID                      `db:"pull_request_id"`
	Status        models.CodeReviewSessionStatus `db:"status"`
	TriggerSource models.CodeReviewTriggerSource `db:"trigger_source"`
}

var ErrCodeReviewCaptureIdentitySourceAmbiguous = errors.New("capture identity source metadata is ambiguous")

const codeReviewCaptureIdentitySourceQuery = `SELECT id,org_id,session_id,pull_request_id,status,trigger_source
 FROM code_review_session_metadata
 WHERE org_id=$1 AND session_id=$2 AND pull_request_id=$3 AND ($4::uuid IS NULL OR id=$4)
 ORDER BY id LIMIT 2`

// ResolveCaptureIdentitySource validates a supplied original metadata hint, or
// accepts a legacy session-only identity only when exactly one scoped row exists.
func (s *CodeReviewStore) ResolveCaptureIdentitySource(ctx context.Context, orgID, sessionID, pullRequestID uuid.UUID, metadataID *uuid.UUID) (CodeReviewCaptureIdentitySource, error) {
	if ctx == nil || s == nil || nilLifecycleDependency(s.db) || orgID == uuid.Nil || sessionID == uuid.Nil || pullRequestID == uuid.Nil || (metadataID != nil && *metadataID == uuid.Nil) {
		return CodeReviewCaptureIdentitySource{}, errors.New("capture identity source scope is required")
	}
	if err := ctx.Err(); err != nil {
		return CodeReviewCaptureIdentitySource{}, err
	}
	rows, err := s.db.Query(ctx, codeReviewCaptureIdentitySourceQuery, orgID, sessionID, pullRequestID, metadataID)
	if err != nil {
		return CodeReviewCaptureIdentitySource{}, fmt.Errorf("query capture identity source: %w", err)
	}
	sources, err := pgx.CollectRows(rows, pgx.RowToStructByName[CodeReviewCaptureIdentitySource])
	if err != nil {
		return CodeReviewCaptureIdentitySource{}, fmt.Errorf("read capture identity source: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return CodeReviewCaptureIdentitySource{}, err
	}
	if len(sources) == 0 {
		return CodeReviewCaptureIdentitySource{}, pgx.ErrNoRows
	}
	if len(sources) != 1 {
		return CodeReviewCaptureIdentitySource{}, ErrCodeReviewCaptureIdentitySourceAmbiguous
	}
	return sources[0], nil
}
