package handlers

import (
	"encoding/json"
	"testing"

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
	oldProject := &models.Project{
		ID:            uuid.New(),
		RepositoryID:  &repoID,
		Title:         "Old project",
		Goal:          "Fix checkout",
		Scope:         &scope,
		Status:        models.ProjectStatusDraft,
		Priority:      50,
		ExecutionMode: models.ProjectExecModeSequential,
		MaxConcurrent: 1,
		BaseBranch:    "main",
		CreatedBy:     &createdBy,
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
	require.Equal(t, createdBy.String(), snap["created_by"], "project snapshot should include creator")

	changes := projectAuditDiff(oldProject, &newProject)
	require.Equal(t, map[string]any{"before": "Old project", "after": "New project"}, changes["title"], "project diff should include title change")
	require.Equal(t, map[string]any{"before": models.ProjectStatusDraft, "after": models.ProjectStatusActive}, changes["status"], "project diff should include status change")
	require.Equal(t, map[string]any{"before": 50, "after": 75}, changes["priority"], "project diff should include priority change")
}

func TestPMDocumentAuditDetails(t *testing.T) {
	t.Parallel()

	sourceURL := "https://example.com/context"
	oldDoc := &models.PMDocument{
		ID:         uuid.New(),
		LogicalID:  uuid.New(),
		Title:      "Old context",
		Content:    "short",
		DocType:    "context",
		SourceType: models.PMDocSourceURL,
		SourceURL:  &sourceURL,
		Active:     true,
	}
	newDoc := *oldDoc
	newDoc.Title = "New context"
	newDoc.Content = "longer content"

	snap := pmDocumentAuditSnapshot(oldDoc)
	require.Equal(t, oldDoc.ID.String(), snap["document_id"], "document snapshot should include document ID")
	require.Equal(t, oldDoc.LogicalID.String(), snap["logical_id"], "document snapshot should include logical ID")
	require.Equal(t, "Old context", snap["title"], "document snapshot should include title")
	require.Equal(t, len(oldDoc.Content), snap["content_length"], "document snapshot should include content length without content")
	require.Equal(t, sourceURL, snap["source_url"], "document snapshot should include source URL")

	changes := pmDocumentAuditDiff(oldDoc, &newDoc)
	require.Equal(t, map[string]any{"before": "Old context", "after": "New context"}, changes["title"], "document diff should include title change")
	require.Equal(t, map[string]any{"before": len("short"), "after": len("longer content")}, changes["content_length"], "document diff should include content length change")
}

func TestEvalAuditDetails(t *testing.T) {
	t.Parallel()

	task := &models.EvalTask{
		ID:                uuid.New(),
		RepoID:            uuid.New(),
		Name:              "Checkout regression",
		BaseCommitSHA:     "0123456789abcdef0123456789abcdef01234567",
		SolutionCommitSHA: strPtr("abcdef0123456789abcdef0123456789abcdef01"),
		SolutionDiff:      strPtr("diff --git a/file b/file"),
		PassThreshold:     0.8,
		Source:            models.EvalTaskSourceManual,
		SourcePRNumber:    intPtr(42),
		Complexity:        models.EvalComplexityModerate,
		Tags:              []string{"checkout", "regression"},
	}
	snap := evalTaskAuditSnapshot(task)
	require.Equal(t, task.ID.String(), snap["eval_task_id"], "eval task snapshot should include task ID")
	require.Equal(t, task.RepoID.String(), snap["repo_id"], "eval task snapshot should include repo ID")
	require.Equal(t, "Checkout regression", snap["name"], "eval task snapshot should include name")
	require.Equal(t, true, snap["has_solution_diff"], "eval task snapshot should include solution diff presence without diff content")
	require.Equal(t, 42, snap["source_pr_number"], "eval task snapshot should include source PR number")

	run := &models.EvalRun{ID: uuid.New(), TaskID: task.ID, Model: "codex", ConfigRef: strPtr("main")}
	runDetails := evalRunAuditDetails(run, uuid.New())
	require.Equal(t, run.ID.String(), runDetails["eval_run_id"], "eval run details should include run ID")
	require.Equal(t, run.TaskID.String(), runDetails["eval_task_id"], "eval run details should include task ID")
	require.Equal(t, "codex", runDetails["model"], "eval run details should include model")
	require.Equal(t, "main", runDetails["config_ref"], "eval run details should include config ref")
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
