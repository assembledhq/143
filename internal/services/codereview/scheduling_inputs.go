package codereview

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	ghservice "github.com/assembledhq/143/internal/services/github"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// scheduledReviewInputs records the inexpensive provider inputs that also fence
// full-assessment publication. Keep them with pending intent and session
// admission so repeated observations do not restart the quiet period. An empty
// body digest means an older session never recorded its body, not an empty body.
type scheduledReviewInputs struct {
	Title      string `json:"title"`
	BodyDigest string `json:"body_digest,omitempty"`
}

func reviewSnapshotInputs(snapshot ghservice.CodeReviewPullRequestSnapshot) *scheduledReviewInputs {
	return &scheduledReviewInputs{Title: snapshot.Title, BodyDigest: digestJSON(snapshot.Body)}
}

func (before *scheduledReviewInputs) changed(after *scheduledReviewInputs) bool {
	return before != nil && (before.Title != after.Title || (before.BodyDigest != "" && before.BodyDigest != after.BodyDigest))
}

// Prefer the immutable full-assessment capture: the worker may have captured
// provider inputs after session admission. Sessions without a capture still
// have their admission snapshot; pre-upgrade sessions retain title-only checks.
func scheduledSessionInputs(ctx context.Context, tx pgx.Tx, orgID, sessionID uuid.UUID, revision json.RawMessage) (*scheduledReviewInputs, error) {
	var raw json.RawMessage
	err := tx.QueryRow(ctx, `SELECT input_manifest FROM code_review_revision_assessments WHERE org_id=$1 AND session_id=$2 AND review_scope='full' ORDER BY generation DESC LIMIT 1`, orgID, sessionID).Scan(&raw)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}
	if err == nil {
		var captured struct {
			Title       *string `json:"title"`
			Description *string `json:"description"`
		}
		if err := json.Unmarshal(raw, &captured); err != nil {
			return nil, fmt.Errorf("decode captured scheduling inputs: %w", err)
		}
		if captured.Title != nil && captured.Description != nil {
			return &scheduledReviewInputs{Title: *captured.Title, BodyDigest: digestJSON(*captured.Description)}, nil
		}
	}
	if len(revision) == 0 {
		return nil, nil
	}
	var admitted struct {
		Inputs *scheduledReviewInputs `json:"schedule_inputs"`
		Title  *string                `json:"pull_request_title"`
	}
	if err := json.Unmarshal(revision, &admitted); err != nil {
		return nil, err
	}
	if admitted.Inputs != nil {
		return admitted.Inputs, nil
	}
	if admitted.Title != nil {
		return &scheduledReviewInputs{Title: *admitted.Title}, nil
	}
	return nil, nil
}

func bindScheduledInputs(ctx context.Context, tx pgx.Tx, orgID, sessionID uuid.UUID, inputs *scheduledReviewInputs) error {
	if inputs == nil {
		return nil
	}
	raw, err := json.Marshal(inputs)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `UPDATE sessions SET revision_context=jsonb_set(COALESCE(revision_context,'{}'::jsonb),'{schedule_inputs}',$3::jsonb) WHERE org_id=$1 AND id=$2`, orgID, sessionID, raw)
	return err
}
