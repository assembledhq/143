package handlers

import (
	"encoding/json"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
)

// sessionAuditSnapshot returns a compact, structured description of the
// session row as it existed when the event was emitted. It is intentionally
// limited to IDs and operator-relevant choices so audit logs stay useful
// without copying large prompts, diffs, or other sensitive blobs.
func sessionAuditSnapshot(session *models.Session, issue *models.Issue, extra map[string]any) map[string]any {
	details := map[string]any{
		"session_id":        session.ID.String(),
		"agent_type":        string(session.AgentType),
		"status":            string(session.Status),
		"origin":            string(session.Origin),
		"interaction_mode":  string(session.InteractionMode),
		"validation_policy": string(session.ValidationPolicy),
		"autonomy_level":    string(session.AutonomyLevel),
		"token_mode":        string(session.TokenMode),
	}
	if session.PrimaryIssueID != nil {
		details["issue_id"] = session.PrimaryIssueID.String()
	}
	if !session.CreatedAt.IsZero() {
		details["created_at"] = session.CreatedAt.UTC().Format(time.RFC3339Nano)
	}
	if !session.LastActivityAt.IsZero() {
		details["last_activity_at"] = session.LastActivityAt.UTC().Format(time.RFC3339Nano)
	}
	if session.Title != nil {
		details["title"] = *session.Title
	}
	if session.ModelOverride != nil {
		details["model_override"] = *session.ModelOverride
	}
	if session.TargetBranch != nil {
		details["target_branch"] = *session.TargetBranch
	}
	if session.RepositoryID != nil {
		details["repository_id"] = session.RepositoryID.String()
	}
	if session.ParentSessionID != nil {
		details["parent_session_id"] = session.ParentSessionID.String()
	}
	if session.PMPlanID != nil {
		details["pm_plan_id"] = session.PMPlanID.String()
	}
	if session.ProjectTaskID != nil {
		details["project_task_id"] = session.ProjectTaskID.String()
	}
	if session.AutomationRunID != nil {
		details["automation_run_id"] = session.AutomationRunID.String()
	}
	if session.TriggeredByUserID != nil {
		details["triggered_by_user_id"] = session.TriggeredByUserID.String()
	}
	if issue != nil {
		details["issue_title"] = issue.Title
		details["issue_source"] = string(issue.Source)
		if issue.RepositoryID != nil {
			details["issue_repository_id"] = issue.RepositoryID.String()
		}
	}
	for k, v := range extra {
		details[k] = v
	}
	return details
}

func sessionCreateAuditDetails(logger zerolog.Logger, session *models.Session, issue *models.Issue, extra map[string]any) json.RawMessage {
	return marshalAuditDetails(logger, sessionAuditSnapshot(session, issue, extra))
}

func sessionArchiveAuditDetails(logger zerolog.Logger, session *models.Session, action models.AuditAction, actorID *uuid.UUID) json.RawMessage {
	details := sessionAuditSnapshot(session, nil, nil)
	archivedByBefore := any(nil)
	if session.ArchivedByUserID != nil {
		archivedByBefore = session.ArchivedByUserID.String()
	}
	archivedByActor := any(nil)
	if actorID != nil {
		archivedByActor = actorID.String()
	}

	switch action {
	case models.AuditActionSessionArchived:
		details["changes"] = map[string]any{
			"archived_at":         map[string]any{"before": nil, "after": "set"},
			"archived_by_user_id": map[string]any{"before": archivedByBefore, "after": archivedByActor},
		}
	case models.AuditActionSessionUnarchived:
		details["changes"] = map[string]any{
			"archived_at":         map[string]any{"before": "set", "after": nil},
			"archived_by_user_id": map[string]any{"before": archivedByBefore, "after": nil},
		}
	}
	return marshalAuditDetails(logger, details)
}
