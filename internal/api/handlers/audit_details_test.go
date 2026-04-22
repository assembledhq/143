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

func TestProjectAuditDetails(t *testing.T) {
	t.Parallel()

	repoID := uuid.New()
	createdBy := uuid.New()
	scope := "billing"
	completionCriteria := "ship bugfix"
	currentPhase := "execution"
	agentType := string(models.AgentTypeCodex)
	modelOverride := "gpt-5.4"
	oldProject := &models.Project{
		ID:                 uuid.New(),
		RepositoryID:       &repoID,
		Title:              "Old project",
		Goal:               "Fix checkout",
		Scope:              &scope,
		CompletionCriteria: &completionCriteria,
		Status:             models.ProjectStatusDraft,
		Priority:           50,
		ExecutionMode:      models.ProjectExecModeSequential,
		MaxConcurrent:      1,
		BaseBranch:         "main",
		CurrentPhase:       &currentPhase,
		AgentType:          &agentType,
		ModelOverride:      &modelOverride,
		CreatedBy:          &createdBy,
	}
	newProject := *oldProject
	newProject.Title = "New project"
	newProject.Status = models.ProjectStatusActive
	newProject.Priority = 75

	snap := projectAuditSnapshot(oldProject)
	require.Equal(t, oldProject.ID.String(), snap["project_id"], "project snapshot should include project ID")
	require.Equal(t, repoID.String(), snap["repository_id"], "project snapshot should include repository ID")
	require.Equal(t, "Old project", snap["title"], "project snapshot should include title")
	require.Equal(t, "draft", snap["status"], "project snapshot should include status")
	require.Equal(t, completionCriteria, snap["completion_criteria"], "project snapshot should include completion criteria")
	require.Equal(t, currentPhase, snap["current_phase"], "project snapshot should include current phase")
	require.Equal(t, agentType, snap["agent_type"], "project snapshot should include agent type")
	require.Equal(t, modelOverride, snap["model_override"], "project snapshot should include model override")
	require.Equal(t, createdBy.String(), snap["created_by"], "project snapshot should include creator")

	changes := projectAuditDiff(oldProject, &newProject)
	require.Equal(t, map[string]any{"before": "Old project", "after": "New project"}, changes["title"], "project diff should include title change")
	require.Equal(t, map[string]any{"before": models.ProjectStatusDraft, "after": models.ProjectStatusActive}, changes["status"], "project diff should include status change")
	require.Equal(t, map[string]any{"before": 50, "after": 75}, changes["priority"], "project diff should include priority change")
}

func TestAuditDetailHelpers_CoverOptionalFields(t *testing.T) {
	t.Parallel()

	t.Run("raw JSON summary fingerprints payloads", func(t *testing.T) {
		t.Parallel()

		require.Nil(t, rawJSONAuditSummary(nil), "empty JSON payload should not produce a summary")

		summary := rawJSONAuditSummary(json.RawMessage(`{"a":1}`))
		require.Equal(t, 7, summary["length"], "JSON summary should include payload length")
		require.NotEmpty(t, summary["sha256"], "JSON summary should include a stable fingerprint")
	})

	t.Run("project diff captures all configured fields", func(t *testing.T) {
		t.Parallel()

		oldScope := "billing"
		newScope := "checkout"
		oldCriteria := "done"
		newCriteria := "ship"
		oldPhase := "plan"
		newPhase := "execute"
		oldProject := &models.Project{
			Title:              "Old",
			Goal:               "Goal A",
			Scope:              &oldScope,
			CompletionCriteria: &oldCriteria,
			Status:             models.ProjectStatusDraft,
			Priority:           10,
			ExecutionMode:      models.ProjectExecModeSequential,
			MaxConcurrent:      1,
			AutoMerge:          false,
			BaseBranch:         "main",
			CurrentPhase:       &oldPhase,
		}
		newProject := &models.Project{
			Title:              "New",
			Goal:               "Goal B",
			Scope:              &newScope,
			CompletionCriteria: &newCriteria,
			Status:             models.ProjectStatusActive,
			Priority:           20,
			ExecutionMode:      models.ProjectExecModeParallel,
			MaxConcurrent:      4,
			AutoMerge:          true,
			BaseBranch:         "release",
			CurrentPhase:       &newPhase,
		}

		changes := projectAuditDiff(oldProject, newProject)
		require.Len(t, changes, 11, "project diff should record each changed field")
		require.Equal(t, map[string]any{"before": "Goal A", "after": "Goal B"}, changes["goal"], "project diff should include goal changes")
		require.Equal(t, map[string]any{"before": "billing", "after": "checkout"}, changes["scope"], "project diff should include scope changes")
		require.Equal(t, map[string]any{"before": false, "after": true}, changes["auto_merge"], "project diff should include auto-merge changes")
		require.Equal(t, map[string]any{"before": "plan", "after": "execute"}, changes["current_phase"], "project diff should include current phase changes")
	})

	t.Run("project task snapshot and diff include optional metadata", func(t *testing.T) {
		t.Parallel()

		description := "Investigate"
		approach := "Reproduce first"
		complexity := "high"
		confidence := "medium"
		branchName := "fix/task"
		prURL := "https://example.com/pr/1"
		outcomeNotes := "Needs review"
		sessionID := uuid.New()
		issueID := uuid.New()
		oldTask := &models.ProjectTask{
			ID:           uuid.New(),
			ProjectID:    uuid.New(),
			Title:        "Task A",
			Status:       models.ProjectTaskStatusPending,
			BatchNumber:  2,
			SortOrder:    3,
			RetryCount:   0,
			MaxRetries:   2,
			Description:  &description,
			Approach:     &approach,
			Complexity:   &complexity,
			Confidence:   &confidence,
			SessionID:    &sessionID,
			IssueID:      &issueID,
			BranchName:   &branchName,
			PRURL:        &prURL,
			OutcomeNotes: &outcomeNotes,
		}
		newDescription := "Investigate deeper"
		newApproach := "Write failing test"
		newComplexity := "low"
		newConfidence := "high"
		newOutcome := "Fixed"
		newTask := *oldTask
		newTask.Title = "Task B"
		newTask.Description = &newDescription
		newTask.Approach = &newApproach
		newTask.Status = models.ProjectTaskStatusRunning
		newTask.OutcomeNotes = &newOutcome
		newTask.Complexity = &newComplexity
		newTask.Confidence = &newConfidence
		newTask.RetryCount = 1

		snapshot := projectTaskAuditSnapshot(oldTask)
		require.Equal(t, description, snapshot["description"], "task snapshot should include description")
		require.Equal(t, approach, snapshot["approach"], "task snapshot should include approach")
		require.Equal(t, complexity, snapshot["complexity"], "task snapshot should include complexity")
		require.Equal(t, confidence, snapshot["confidence"], "task snapshot should include confidence")
		require.Equal(t, sessionID.String(), snapshot["session_id"], "task snapshot should include session correlation")
		require.Equal(t, issueID.String(), snapshot["issue_id"], "task snapshot should include issue correlation")
		require.Equal(t, branchName, snapshot["branch_name"], "task snapshot should include branch name")
		require.Equal(t, prURL, snapshot["pr_url"], "task snapshot should include PR URL")

		changes := projectTaskAuditDiff(oldTask, &newTask)
		require.Equal(t, map[string]any{"before": "Task A", "after": "Task B"}, changes["title"], "task diff should include title change")
		require.Equal(t, map[string]any{"before": "Needs review", "after": "Fixed"}, changes["outcome_notes"], "task diff should include outcome notes change")
		require.Equal(t, map[string]any{"before": 0, "after": 1}, changes["retry_count"], "task diff should include retry count change")
	})
}

func TestPMDocumentAuditDetails(t *testing.T) {
	t.Parallel()

	sourceURL := "https://example.com/context"
	sourceID := "slack:123"
	createdBy := uuid.New()
	lastSyncedAt := time.Date(2026, 4, 21, 16, 0, 0, 0, time.UTC)
	oldDoc := &models.PMDocument{
		ID:           uuid.New(),
		LogicalID:    uuid.New(),
		Title:        "Old context",
		Content:      "short",
		DocType:      "context",
		SourceType:   models.PMDocSourceURL,
		SourceURL:    &sourceURL,
		SourceID:     &sourceID,
		Active:       true,
		CreatedBy:    &createdBy,
		LastSyncedAt: &lastSyncedAt,
	}
	newDoc := *oldDoc
	newDoc.Title = "New context"
	newDoc.Content = "longer content"
	newSourceURL := "https://example.com/new-context"
	newSourceID := "slack:456"
	newDoc.SourceURL = &newSourceURL
	newDoc.SourceID = &newSourceID

	snap := pmDocumentAuditSnapshot(oldDoc)
	require.Equal(t, oldDoc.ID.String(), snap["document_id"], "document snapshot should include document ID")
	require.Equal(t, oldDoc.LogicalID.String(), snap["logical_id"], "document snapshot should include logical ID")
	require.Equal(t, "Old context", snap["title"], "document snapshot should include title")
	require.Equal(t, len(oldDoc.Content), snap["content_length"], "document snapshot should include content length without content")
	require.Equal(t, sourceURL, snap["source_url"], "document snapshot should include source URL")
	require.Equal(t, sourceID, snap["source_id"], "document snapshot should include source ID")
	require.Equal(t, createdBy.String(), snap["created_by"], "document snapshot should include creator")
	require.Equal(t, lastSyncedAt.Format(time.RFC3339Nano), snap["last_synced_at"], "document snapshot should include last sync time")

	changes := pmDocumentAuditDiff(oldDoc, &newDoc)
	require.Equal(t, map[string]any{"before": "Old context", "after": "New context"}, changes["title"], "document diff should include title change")
	require.Equal(t, map[string]any{"before": len("short"), "after": len("longer content")}, changes["content_length"], "document diff should include content length change")
	require.Equal(t, map[string]any{"before": sourceURL, "after": newSourceURL}, changes["source_url"], "document diff should include source URL change")
	require.Equal(t, map[string]any{"before": sourceID, "after": newSourceID}, changes["source_id"], "document diff should include source ID change")
}

func TestEvalAuditDetails(t *testing.T) {
	t.Parallel()

	pmDocumentSetPinID := uuid.New()
	orgSettingsVersionID := uuid.New()
	createdBy := uuid.New()
	task := &models.EvalTask{
		ID:                   uuid.New(),
		RepoID:               uuid.New(),
		Name:                 "Checkout regression",
		BaseCommitSHA:        "0123456789abcdef0123456789abcdef01234567",
		SolutionCommitSHA:    strPtr("abcdef0123456789abcdef0123456789abcdef01"),
		SolutionDiff:         strPtr("diff --git a/file b/file"),
		PassThreshold:        0.8,
		Source:               models.EvalTaskSourceManual,
		SourcePRNumber:       intPtr(42),
		Complexity:           models.EvalComplexityModerate,
		Tags:                 []string{"checkout", "regression"},
		PMDocumentSetPinID:   &pmDocumentSetPinID,
		OrgSettingsVersionID: &orgSettingsVersionID,
		CreatedBy:            &createdBy,
		ContextOverrides:     json.RawMessage(`{"mode":"full"}`),
		ScoringCriteria:      json.RawMessage(`[{"name":"correctness","weight":1}]`),
	}
	snap := evalTaskAuditSnapshot(task)
	require.Equal(t, task.ID.String(), snap["eval_task_id"], "eval task snapshot should include task ID")
	require.Equal(t, task.RepoID.String(), snap["repo_id"], "eval task snapshot should include repo ID")
	require.Equal(t, "Checkout regression", snap["name"], "eval task snapshot should include name")
	require.Equal(t, true, snap["has_solution_diff"], "eval task snapshot should include solution diff presence without diff content")
	require.Equal(t, 42, snap["source_pr_number"], "eval task snapshot should include source PR number")
	require.Equal(t, pmDocumentSetPinID.String(), snap["pm_document_set_pin_id"], "eval task snapshot should include document pin ID")
	require.Equal(t, orgSettingsVersionID.String(), snap["org_settings_version_id"], "eval task snapshot should include org settings version ID")
	require.Equal(t, createdBy.String(), snap["created_by"], "eval task snapshot should include creator")

	updatedTask := *task
	updatedTask.Description = "Updated description"
	updatedTask.IssueDescription = "Different issue text"
	updatedTask.PassThreshold = 0.9
	updatedTask.Complexity = models.EvalComplexityComplex
	updatedTask.Tags = []string{"checkout"}
	updatedTask.ContextOverrides = json.RawMessage(`{"mode":"light"}`)
	updatedTask.ScoringCriteria = json.RawMessage(`[{"name":"speed","weight":1}]`)
	diff := evalTaskAuditDiff(task, &updatedTask)
	require.Contains(t, diff, "description", "eval task diff should include description changes")
	require.Contains(t, diff, "issue_description", "eval task diff should include issue description changes")
	require.Contains(t, diff, "context_overrides", "eval task diff should include context override changes")

	batchID := uuid.New()
	run := &models.EvalRun{ID: uuid.New(), TaskID: task.ID, Model: "codex", ConfigRef: strPtr("main"), BatchID: &batchID}
	runDetails := evalRunAuditDetails(run, uuid.New())
	require.Equal(t, run.ID.String(), runDetails["eval_run_id"], "eval run details should include run ID")
	require.Equal(t, run.TaskID.String(), runDetails["eval_task_id"], "eval run details should include task ID")
	require.Equal(t, "codex", runDetails["model"], "eval run details should include model")
	require.Equal(t, "main", runDetails["config_ref"], "eval run details should include config ref")
	require.Equal(t, batchID.String(), runDetails["eval_batch_id"], "eval run details should include batch ID")

	batch := &models.EvalBatch{ID: uuid.New(), Name: "Nightly", Status: models.EvalBatchStatusRunning, TaskCount: 2, RunCount: 3}
	taskIDs := []uuid.UUID{uuid.New(), uuid.New()}
	batchDetails := evalBatchAuditDetails(batch, taskIDs, 4)
	require.Equal(t, batch.ID.String(), batchDetails["eval_batch_id"], "eval batch details should include batch ID")
	require.Equal(t, 4, batchDetails["config_count"], "eval batch details should include config count")
	require.Len(t, batchDetails["task_ids"], 2, "eval batch details should include all task IDs")

	require.Equal(t, map[string]any{"provider": "openai"}, credentialAuditDetails(models.ProviderName("openai"), nil), "credential audit details should handle nil summaries")
	fullCredential := credentialAuditDetails(models.ProviderName("github"), &models.CredentialSummary{
		Configured:  true,
		Status:      "ok",
		APIType:     "app",
		AppName:     "codex",
		AppID:       123,
		AccountType: "org",
	})
	require.Equal(t, "github", fullCredential["provider"], "credential details should include provider")
	require.Equal(t, "org", fullCredential["account_type"], "credential details should include account type")
}

func TestEvalTaskAuditDiffDetectsSameLengthJSONChanges(t *testing.T) {
	t.Parallel()

	oldTask := &models.EvalTask{
		ScoringCriteria:  json.RawMessage(`[{"name":"foo","weight":1}]`),
		ContextOverrides: json.RawMessage(`{"mode":"abc","rank":1}`),
	}
	newTask := &models.EvalTask{
		ScoringCriteria:  json.RawMessage(`[{"name":"bar","weight":1}]`),
		ContextOverrides: json.RawMessage(`{"mode":"xyz","rank":1}`),
	}

	require.Equal(t, len(oldTask.ScoringCriteria), len(newTask.ScoringCriteria), "test fixture should keep scoring criteria length unchanged")
	require.Equal(t, len(oldTask.ContextOverrides), len(newTask.ContextOverrides), "test fixture should keep context overrides length unchanged")

	changes := evalTaskAuditDiff(oldTask, newTask)
	require.Contains(t, changes, "scoring_criteria", "eval task diff should record scoring criteria changes even when length is unchanged")
	require.Contains(t, changes, "context_overrides", "eval task diff should record context override changes even when length is unchanged")

	scoringChange, ok := changes["scoring_criteria"].(map[string]any)
	require.True(t, ok, "scoring criteria change should be encoded as a before/after change")
	require.NotEqual(t, scoringChange["before"], scoringChange["after"], "scoring criteria change should distinguish different payloads")

	contextChange, ok := changes["context_overrides"].(map[string]any)
	require.True(t, ok, "context override change should be encoded as a before/after change")
	require.NotEqual(t, contextChange["before"], contextChange["after"], "context override change should distinguish different payloads")
}

func TestAuditMarshalOmitsEmptyAndEncodes(t *testing.T) {
	t.Parallel()

	require.Nil(t, marshalAuditDetails(zerolog.Nop(), map[string]any{}), "empty audit details should remain nil")

	raw := marshalAuditDetails(zerolog.Nop(), map[string]any{"status": "queued"})
	var decoded map[string]any
	require.NoError(t, json.Unmarshal(raw, &decoded), "audit details should encode as JSON")
	require.Equal(t, "queued", decoded["status"], "audit details should preserve values")
}

func intPtr(v int) *int {
	return &v
}
