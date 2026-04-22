package handlers

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"reflect"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
)

func auditChange(before, after any) map[string]any {
	return map[string]any{"before": before, "after": after}
}

func addAuditChange(changes map[string]any, field string, before, after any) {
	if !reflect.DeepEqual(before, after) {
		changes[field] = auditChange(before, after)
	}
}

func rawJSONAuditSummary(raw json.RawMessage) map[string]any {
	if len(raw) == 0 {
		return nil
	}
	sum := sha256.Sum256(raw)
	return map[string]any{
		"length": len(raw),
		"sha256": hex.EncodeToString(sum[:]),
	}
}

func projectAuditSnapshot(p *models.Project) map[string]any {
	out := map[string]any{
		"project_id":      p.ID.String(),
		"title":           p.Title,
		"status":          string(p.Status),
		"priority":        p.Priority,
		"execution_mode":  string(p.ExecutionMode),
		"max_concurrent":  p.MaxConcurrent,
		"auto_merge":      p.AutoMerge,
		"base_branch":     p.BaseBranch,
		"total_tasks":     p.TotalTasks,
		"completed_tasks": p.CompletedTasks,
		"failed_tasks":    p.FailedTasks,
		"proposed_by_pm":  p.ProposedByPM,
	}
	if p.RepositoryID != nil {
		out["repository_id"] = p.RepositoryID.String()
	}
	if p.Scope != nil {
		out["scope"] = *p.Scope
	}
	if p.CompletionCriteria != nil {
		out["completion_criteria"] = *p.CompletionCriteria
	}
	if p.CurrentPhase != nil {
		out["current_phase"] = *p.CurrentPhase
	}
	if p.AgentType != nil {
		out["agent_type"] = *p.AgentType
	}
	if p.ModelOverride != nil {
		out["model_override"] = *p.ModelOverride
	}
	if p.CreatedBy != nil {
		out["created_by"] = p.CreatedBy.String()
	}
	return out
}

func projectAuditDiff(old, new_ *models.Project) map[string]any {
	changes := map[string]any{}
	addAuditChange(changes, "title", old.Title, new_.Title)
	addAuditChange(changes, "goal", old.Goal, new_.Goal)
	addAuditChange(changes, "scope", optString(old.Scope), optString(new_.Scope))
	addAuditChange(changes, "completion_criteria", optString(old.CompletionCriteria), optString(new_.CompletionCriteria))
	addAuditChange(changes, "status", old.Status, new_.Status)
	addAuditChange(changes, "priority", old.Priority, new_.Priority)
	addAuditChange(changes, "execution_mode", old.ExecutionMode, new_.ExecutionMode)
	addAuditChange(changes, "max_concurrent", old.MaxConcurrent, new_.MaxConcurrent)
	addAuditChange(changes, "auto_merge", old.AutoMerge, new_.AutoMerge)
	addAuditChange(changes, "base_branch", old.BaseBranch, new_.BaseBranch)
	addAuditChange(changes, "current_phase", optString(old.CurrentPhase), optString(new_.CurrentPhase))
	return changes
}

func projectTaskAuditSnapshot(t *models.ProjectTask) map[string]any {
	out := map[string]any{
		"project_task_id": t.ID.String(),
		"project_id":      t.ProjectID.String(),
		"title":           t.Title,
		"status":          string(t.Status),
		"batch_number":    t.BatchNumber,
		"sort_order":      t.SortOrder,
		"retry_count":     t.RetryCount,
		"max_retries":     t.MaxRetries,
	}
	if t.Description != nil {
		out["description"] = *t.Description
	}
	if t.Approach != nil {
		out["approach"] = *t.Approach
	}
	if t.Complexity != nil {
		out["complexity"] = *t.Complexity
	}
	if t.Confidence != nil {
		out["confidence"] = *t.Confidence
	}
	if t.SessionID != nil {
		out["session_id"] = t.SessionID.String()
	}
	if t.IssueID != nil {
		out["issue_id"] = t.IssueID.String()
	}
	if t.BranchName != nil {
		out["branch_name"] = *t.BranchName
	}
	if t.PRURL != nil {
		out["pr_url"] = *t.PRURL
	}
	return out
}

func projectTaskAuditDiff(old, new_ *models.ProjectTask) map[string]any {
	changes := map[string]any{}
	addAuditChange(changes, "title", old.Title, new_.Title)
	addAuditChange(changes, "description", optString(old.Description), optString(new_.Description))
	addAuditChange(changes, "approach", optString(old.Approach), optString(new_.Approach))
	addAuditChange(changes, "status", old.Status, new_.Status)
	addAuditChange(changes, "outcome_notes", optString(old.OutcomeNotes), optString(new_.OutcomeNotes))
	addAuditChange(changes, "complexity", optString(old.Complexity), optString(new_.Complexity))
	addAuditChange(changes, "confidence", optString(old.Confidence), optString(new_.Confidence))
	addAuditChange(changes, "retry_count", old.RetryCount, new_.RetryCount)
	return changes
}

func pmDocumentAuditSnapshot(doc *models.PMDocument) map[string]any {
	out := map[string]any{
		"document_id":    doc.ID.String(),
		"logical_id":     doc.LogicalID.String(),
		"title":          doc.Title,
		"doc_type":       doc.DocType,
		"source_type":    doc.SourceType,
		"active":         doc.Active,
		"content_hash":   doc.ContentHash,
		"content_length": len(doc.Content),
	}
	if doc.SourceURL != nil {
		out["source_url"] = *doc.SourceURL
	}
	if doc.SourceID != nil {
		out["source_id"] = *doc.SourceID
	}
	if doc.LastSyncedAt != nil {
		out["last_synced_at"] = doc.LastSyncedAt.UTC().Format(time.RFC3339Nano)
	}
	if doc.CreatedBy != nil {
		out["created_by"] = doc.CreatedBy.String()
	}
	return out
}

func pmDocumentAuditDiff(old, new_ *models.PMDocument) map[string]any {
	changes := map[string]any{}
	addAuditChange(changes, "title", old.Title, new_.Title)
	addAuditChange(changes, "content_length", len(old.Content), len(new_.Content))
	addAuditChange(changes, "content_hash", old.ContentHash, new_.ContentHash)
	addAuditChange(changes, "doc_type", old.DocType, new_.DocType)
	addAuditChange(changes, "source_type", old.SourceType, new_.SourceType)
	addAuditChange(changes, "source_url", optString(old.SourceURL), optString(new_.SourceURL))
	addAuditChange(changes, "source_id", optString(old.SourceID), optString(new_.SourceID))
	return changes
}

func evalTaskAuditSnapshot(task *models.EvalTask) map[string]any {
	out := map[string]any{
		"eval_task_id":         task.ID.String(),
		"repo_id":              task.RepoID.String(),
		"name":                 task.Name,
		"base_commit_sha":      task.BaseCommitSHA,
		"pass_threshold":       task.PassThreshold,
		"source":               string(task.Source),
		"complexity":           string(task.Complexity),
		"tags":                 task.Tags,
		"has_solution_diff":    task.SolutionDiff != nil && *task.SolutionDiff != "",
		"snapshot_broken":      task.SnapshotBroken,
		"scoring_criteria_len": len(task.ScoringCriteria),
	}
	if task.SolutionCommitSHA != nil {
		out["solution_commit_sha"] = *task.SolutionCommitSHA
	}
	if task.SourcePRNumber != nil {
		out["source_pr_number"] = *task.SourcePRNumber
	}
	if task.PMDocumentSetPinID != nil {
		out["pm_document_set_pin_id"] = task.PMDocumentSetPinID.String()
	}
	if task.OrgSettingsVersionID != nil {
		out["org_settings_version_id"] = task.OrgSettingsVersionID.String()
	}
	if task.CreatedBy != nil {
		out["created_by"] = task.CreatedBy.String()
	}
	return out
}

func evalTaskAuditDiff(old, new_ *models.EvalTask) map[string]any {
	changes := map[string]any{}
	addAuditChange(changes, "name", old.Name, new_.Name)
	addAuditChange(changes, "description", old.Description, new_.Description)
	addAuditChange(changes, "issue_description", old.IssueDescription, new_.IssueDescription)
	addAuditChange(changes, "pass_threshold", old.PassThreshold, new_.PassThreshold)
	addAuditChange(changes, "complexity", old.Complexity, new_.Complexity)
	addAuditChange(changes, "tags", old.Tags, new_.Tags)
	addAuditChange(changes, "has_solution_diff", old.SolutionDiff != nil && *old.SolutionDiff != "", new_.SolutionDiff != nil && *new_.SolutionDiff != "")
	addAuditChange(changes, "scoring_criteria", rawJSONAuditSummary(old.ScoringCriteria), rawJSONAuditSummary(new_.ScoringCriteria))
	addAuditChange(changes, "context_overrides", rawJSONAuditSummary(old.ContextOverrides), rawJSONAuditSummary(new_.ContextOverrides))
	return changes
}

func evalRunAuditDetails(run *models.EvalRun, jobID uuid.UUID) map[string]any {
	out := map[string]any{
		"eval_run_id":  run.ID.String(),
		"eval_task_id": run.TaskID.String(),
		"model":        run.Model,
		"status":       string(run.Status),
		"job_id":       jobID.String(),
	}
	if run.BatchID != nil {
		out["eval_batch_id"] = run.BatchID.String()
	}
	if run.ConfigRef != nil {
		out["config_ref"] = *run.ConfigRef
	}
	return out
}

func evalBatchAuditDetails(batch *models.EvalBatch, taskIDs []uuid.UUID, configCount int) map[string]any {
	taskIDStrings := make([]string, 0, len(taskIDs))
	for _, id := range taskIDs {
		taskIDStrings = append(taskIDStrings, id.String())
	}
	return map[string]any{
		"eval_batch_id": batch.ID.String(),
		"name":          batch.Name,
		"status":        string(batch.Status),
		"task_count":    batch.TaskCount,
		"run_count":     batch.RunCount,
		"config_count":  configCount,
		"task_ids":      taskIDStrings,
	}
}

func credentialAuditDetails(provider models.ProviderName, summary *models.CredentialSummary) map[string]any {
	out := map[string]any{"provider": string(provider)}
	if summary == nil {
		return out
	}
	out["configured"] = summary.Configured
	if summary.Status != "" {
		out["status"] = summary.Status
	}
	if summary.APIType != "" {
		out["api_type"] = summary.APIType
	}
	if summary.AppName != "" {
		out["app_name"] = summary.AppName
	}
	if summary.AppID != 0 {
		out["app_id"] = summary.AppID
	}
	if summary.AccountType != "" {
		out["account_type"] = summary.AccountType
	}
	return out
}
