package handlers

import (
	"encoding/json"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func TestSessionAuditSnapshot(t *testing.T) {
	t.Parallel()

	repoID := uuid.New()
	userID := uuid.New()
	parentSessionID := uuid.New()
	pmPlanID := uuid.New()
	projectTaskID := uuid.New()
	automationRunID := uuid.New()
	modelOverride := "gpt-5.2-codex"
	targetBranch := "main"
	title := "Fix login bug"
	issueID := uuid.New()
	session := &models.Session{
		ID:                uuid.New(),
		PrimaryIssueID:    &issueID,
		OrgID:             uuid.New(),
		AgentType:         models.AgentTypeCodex,
		Status:            "pending",
		AutonomyLevel:     "semi",
		TokenMode:         "high",
		ModelOverride:     &modelOverride,
		TriggeredByUserID: &userID,
		Title:             &title,
		TargetBranch:      &targetBranch,
		RepositoryID:      &repoID,
		ParentSessionID:   &parentSessionID,
		PMPlanID:          &pmPlanID,
		ProjectTaskID:     &projectTaskID,
		AutomationRunID:   &automationRunID,
		CreatedAt:         time.Date(2026, 4, 21, 12, 0, 0, 0, time.UTC),
	}
	issue := &models.Issue{
		ID:           issueID,
		Source:       models.IssueSourceManual,
		RepositoryID: &repoID,
		Title:        "Manual issue",
	}

	details := sessionAuditSnapshot(session, issue, map[string]any{
		"manual_session": true,
		"image_count":    2,
	})

	require.Equal(t, session.ID.String(), details["session_id"], "snapshot should include the session ID")
	require.Equal(t, title, details["title"], "snapshot should include the resolved session title")
	require.Equal(t, issueID.String(), details["issue_id"], "snapshot should include the source issue ID")
	require.Equal(t, "manual", details["issue_source"], "snapshot should include the source issue type")
	require.Equal(t, "codex", details["agent_type"], "snapshot should include the selected agent type")
	require.Equal(t, "pending", details["status"], "snapshot should include the initial status")
	require.Equal(t, "semi", details["autonomy_level"], "snapshot should include autonomy level")
	require.Equal(t, "high", details["token_mode"], "snapshot should include token mode")
	require.Equal(t, modelOverride, details["model_override"], "snapshot should include model override")
	require.Equal(t, targetBranch, details["target_branch"], "snapshot should include target branch")
	require.Equal(t, repoID.String(), details["repository_id"], "snapshot should include repository ID")
	require.Equal(t, parentSessionID.String(), details["parent_session_id"], "snapshot should include parent session ID")
	require.Equal(t, pmPlanID.String(), details["pm_plan_id"], "snapshot should include PM plan ID")
	require.Equal(t, projectTaskID.String(), details["project_task_id"], "snapshot should include project task ID")
	require.Equal(t, automationRunID.String(), details["automation_run_id"], "snapshot should include automation run ID")
	require.Equal(t, userID.String(), details["triggered_by_user_id"], "snapshot should include triggering user ID")
	require.Equal(t, true, details["manual_session"], "snapshot should include extra manual-session context")
	require.Equal(t, 2, details["image_count"], "snapshot should include image attachment count")
}

func TestSessionArchiveAuditDetails(t *testing.T) {
	t.Parallel()

	userID := uuid.New()
	title := "Investigate billing timeout"
	issueID := uuid.New()
	session := &models.Session{
		ID:             uuid.New(),
		PrimaryIssueID: &issueID,
		AgentType:      models.AgentTypeClaudeCode,
		Status:         "completed",
		AutonomyLevel:  "supervised",
		TokenMode:      "low",
		Title:          &title,
	}

	tests := []struct {
		name     string
		action   models.AuditAction
		session  func() *models.Session
		actorID  *uuid.UUID
		expected map[string]any
	}{
		{
			name:   "archive records before and after archive state",
			action: models.AuditActionSessionArchived,
			session: func() *models.Session {
				s := *session
				return &s
			},
			actorID: &userID,
			expected: map[string]any{
				"archived_at":         map[string]any{"before": nil, "after": "set"},
				"archived_by_user_id": map[string]any{"before": nil, "after": userID.String()},
			},
		},
		{
			name:   "unarchive records before and after archive state",
			action: models.AuditActionSessionUnarchived,
			session: func() *models.Session {
				s := *session
				s.ArchivedByUserID = &userID
				return &s
			},
			actorID: &userID,
			expected: map[string]any{
				"archived_at":         map[string]any{"before": "set", "after": nil},
				"archived_by_user_id": map[string]any{"before": userID.String(), "after": nil},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			raw := sessionArchiveAuditDetails(zerolog.Nop(), tt.session(), tt.action, tt.actorID)
			var details map[string]any
			require.NoError(t, json.Unmarshal(raw, &details), "archive details should be valid JSON")
			require.Equal(t, title, details["title"], "archive details should include the session title")
			require.Equal(t, session.ID.String(), details["session_id"], "archive details should include the session ID")
			require.Equal(t, issueID.String(), details["issue_id"], "archive details should include the issue ID")
			require.Equal(t, string(session.AgentType), details["agent_type"], "archive details should include agent type")
			require.Equal(t, "completed", details["status"], "archive details should include status")
			require.Equal(t, tt.expected, details["changes"], "archive details should include the expected archive diff")
		})
	}
}
