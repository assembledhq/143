package db

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// ResolveCommentTarget binds an update to this automation's successful creation
// receipt. A provider ID or a copied comment marker is not write authority.
func (s *AutomationActionStore) ResolveCommentTarget(ctx context.Context, orgID uuid.UUID, scope models.AutomationActionScope, request models.AutomationActionRequest) (models.AutomationActionCommentTarget, error) {
	return resolveAutomationCommentTarget(ctx, s.db, orgID, scope, request, uuid.Nil)
}

func resolveAutomationCommentTarget(ctx context.Context, q DBTX, orgID uuid.UUID, scope models.AutomationActionScope, request models.AutomationActionRequest, excludedID uuid.UUID) (models.AutomationActionCommentTarget, error) {
	var target models.AutomationActionCommentTarget
	if orgID == uuid.Nil || orgID != scope.Actor.OrgID || request.Kind != models.AutomationActionCommentUpdate || !actionRequestAllowed(scope, request) {
		return target, ErrAutomationActionUnauthorized
	}
	sourceID, err := uuid.Parse(request.SourceActionID)
	if err != nil || sourceID == uuid.Nil || sourceID.String() != request.SourceActionID {
		return target, ErrAutomationActionUnauthorized
	}
	var sourceText string
	err = q.QueryRow(ctx, `SELECT id,provider_object_id,payload->'request'->>'text'
 FROM automation_actions
 WHERE org_id=$1 AND automation_id=$2 AND repository_id=$3 AND scope_key=$4
 AND id=$5 AND pr_number=$6 AND kind='github_issue_comment' AND status='succeeded'
 AND provider_object_id IS NOT NULL`, orgID, scope.AutomationID, scope.Actor.RepositoryID, scope.ScopeKey, sourceID, request.PRNumber).
		Scan(&target.SourceActionID, &target.CommentID, &sourceText)
	if errors.Is(err, pgx.ErrNoRows) {
		return target, ErrAutomationActionUnauthorized
	}
	if err != nil {
		return target, err
	}
	commentID, err := strconv.ParseInt(target.CommentID, 10, 64)
	if err != nil || commentID <= 0 || strconv.FormatInt(commentID, 10) != target.CommentID {
		return target, ErrAutomationActionUnauthorized
	}
	target.PreviousActionID, target.PreviousText = sourceID, sourceText

	// Claim calls this under the automation advisory lock. Even after a send's
	// lease expires, another key cannot overwrite an uncertain provider effect.
	var busy bool
	err = q.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM automation_actions
 WHERE org_id=$1 AND automation_id=$2 AND repository_id=$3 AND scope_key=$4
 AND kind='github_issue_comment_update' AND payload->'request'->>'source_action_id'=$5
 AND id<>$6 AND status IN ('sending','unknown'))`, orgID, scope.AutomationID, scope.Actor.RepositoryID, scope.ScopeKey, sourceID.String(), excludedID).Scan(&busy)
	if err != nil {
		return target, err
	}
	if busy {
		return target, ErrAutomationActionBusy
	}
	var previousID uuid.UUID
	var previousText string
	err = q.QueryRow(ctx, `SELECT id,payload->'request'->>'text' FROM automation_actions
 WHERE org_id=$1 AND automation_id=$2 AND repository_id=$3 AND scope_key=$4
 AND pr_number=$5 AND kind='github_issue_comment_update' AND status='succeeded'
 AND provider_object_id=$6 AND payload->'request'->>'source_action_id'=$7
 ORDER BY completed_at DESC,id DESC LIMIT 1`, orgID, scope.AutomationID, scope.Actor.RepositoryID, scope.ScopeKey, request.PRNumber, target.CommentID, sourceID.String()).Scan(&previousID, &previousText)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return target, err
	}
	if err == nil {
		target.PreviousActionID, target.PreviousText = previousID, previousText
	}
	return target, nil
}

func validateAutomationCommentUpdate(ctx context.Context, q DBTX, orgID uuid.UUID, scope models.AutomationActionScope, request models.AutomationActionRequest, payload json.RawMessage, excludedID uuid.UUID) error {
	if request.Kind != models.AutomationActionCommentUpdate {
		return nil
	}
	var frozen struct {
		CommentTarget *models.AutomationActionCommentTarget `json:"comment_target"`
	}
	if err := json.Unmarshal(payload, &frozen); err != nil {
		return err
	}
	if frozen.CommentTarget == nil {
		return ErrAutomationActionUnauthorized
	}
	current, err := resolveAutomationCommentTarget(ctx, q, orgID, scope, request, excludedID)
	if err != nil {
		return err
	}
	if current != *frozen.CommentTarget {
		return ErrAutomationActionConflict
	}
	return nil
}
