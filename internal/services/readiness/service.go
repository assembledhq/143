package readiness

import (
	"context"
	"encoding/json"
	"regexp"
	"strings"
	"time"

	"github.com/assembledhq/143/internal/models"
)

type EvaluationInput struct {
	Session                    models.Session
	EvaluatedWorkspaceRevision int64
	EvaluatedSnapshotKey       string
	LatestReviewLoop           *models.SessionReviewLoop
	Logs                       []models.SessionLog
	ChangedFiles               []string
	LinkedIssueCount           int
}

type EvaluationResult struct {
	Status       models.PRReadinessRunStatus
	Summary      string
	ReviewPacket json.RawMessage
	Checks       []models.PRReadinessCheck
}

type Evaluator struct {
	policy models.PRReadinessPolicy
	now    func() time.Time
}

func NewEvaluator(policy models.PRReadinessPolicy) *Evaluator {
	return &Evaluator{policy: policy, now: time.Now}
}

func (e *Evaluator) Evaluate(_ context.Context, input EvaluationInput) (EvaluationResult, error) {
	checks := []models.PRReadinessCheck{
		e.freshnessCheck(input),
		e.agentReviewCheck(input),
		e.diffCollectedCheck(input),
		e.testEvidenceCheck(input),
		e.riskFlagsCheck(input),
		e.contextCheck(input),
		e.reviewPacketCheck(input),
	}
	status := aggregateStatus(checks)
	packet := buildReviewPacket(input, checks)
	packetBytes, err := json.Marshal(packet)
	if err != nil {
		return EvaluationResult{}, err
	}
	return EvaluationResult{
		Status:       status,
		Summary:      summaryForStatus(status),
		ReviewPacket: packetBytes,
		Checks:       checks,
	}, nil
}

func (e *Evaluator) checkBase(checkType models.PRReadinessCheckType, status models.PRReadinessCheckStatus, title, summary, action string, details map[string]any) models.PRReadinessCheck {
	var raw json.RawMessage
	if len(details) > 0 {
		raw, _ = json.Marshal(details)
	}
	return models.PRReadinessCheck{
		CheckType:   checkType,
		Status:      status,
		Enforcement: e.policy.EnforcementFor(models.RoleBuilder, checkType),
		Title:       title,
		Summary:     summary,
		Action:      action,
		Details:     raw,
	}
}

func (e *Evaluator) freshnessCheck(input EvaluationInput) models.PRReadinessCheck {
	if input.Session.WorkspaceGeneration == input.EvaluatedWorkspaceRevision && stringValue(input.Session.SnapshotKey) == input.EvaluatedSnapshotKey {
		return e.checkBase(models.PRReadinessCheckTypeFreshness, models.PRReadinessCheckStatusPassed, "Readiness is fresh", "Checked against the latest workspace revision.", "Re-run", nil)
	}
	return e.checkBase(models.PRReadinessCheckTypeFreshness, models.PRReadinessCheckStatusFailed, "Readiness is stale", "Workspace files changed after this readiness result was produced.", "Re-run", map[string]any{
		"current_workspace_revision":   input.Session.WorkspaceGeneration,
		"evaluated_workspace_revision": input.EvaluatedWorkspaceRevision,
	})
}

func (e *Evaluator) agentReviewCheck(input EvaluationInput) models.PRReadinessCheck {
	if input.LatestReviewLoop != nil && input.LatestReviewLoop.Status == models.ReviewLoopStatusClean && stringValue(input.LatestReviewLoop.LatestCheckpointKey) == input.EvaluatedSnapshotKey {
		return e.checkBase(models.PRReadinessCheckTypeAgentReviewClean, models.PRReadinessCheckStatusPassed, "Agent review clean", "Review completed cleanly for this snapshot.", "View review", nil)
	}
	if input.LatestReviewLoop != nil && input.LatestReviewLoop.Status == models.ReviewLoopStatusRunning {
		return e.checkBase(models.PRReadinessCheckTypeAgentReviewClean, models.PRReadinessCheckStatusWarning, "Agent review running", "Review is still running.", "View review", nil)
	}
	return e.checkBase(models.PRReadinessCheckTypeAgentReviewClean, models.PRReadinessCheckStatusFailed, "Agent review not clean", "Run Review must complete cleanly for this snapshot.", "Run review", nil)
}

func (e *Evaluator) diffCollectedCheck(input EvaluationInput) models.PRReadinessCheck {
	if len(input.Session.DiffStats) > 0 && string(input.Session.DiffStats) != "null" {
		return e.checkBase(models.PRReadinessCheckTypeDiffCollected, models.PRReadinessCheckStatusPassed, "Diff collected", "Diff stats are available for this session.", "View changes", nil)
	}
	return e.checkBase(models.PRReadinessCheckTypeDiffCollected, models.PRReadinessCheckStatusWarning, "Diff not collected", "No diff stats were available when readiness ran.", "View changes", nil)
}

var testEvidencePattern = regexp.MustCompile(`(?i)\b(go test|npm (run )?test|pnpm test|yarn test|pytest|vitest|cargo test|mvn test|gradle test|make test)\b`)

func (e *Evaluator) testEvidenceCheck(input EvaluationInput) models.PRReadinessCheck {
	for _, log := range input.Logs {
		if testEvidencePattern.MatchString(log.Message) {
			return e.checkBase(models.PRReadinessCheckTypeTestEvidencePresent, models.PRReadinessCheckStatusPassed, "Test evidence found", "A test or verification command was captured after the session changed files.", "View logs", map[string]any{
				"log_id": log.ID,
			})
		}
	}
	return e.checkBase(models.PRReadinessCheckTypeTestEvidencePresent, models.PRReadinessCheckStatusWarning, "No test evidence found", "No captured test or verification command output was found.", "Run tests", nil)
}

func (e *Evaluator) riskFlagsCheck(input EvaluationInput) models.PRReadinessCheck {
	seen := map[string]struct{}{}
	flags := make([]string, 0)
	addFlag := func(f string) {
		if _, ok := seen[f]; !ok {
			seen[f] = struct{}{}
			flags = append(flags, f)
		}
	}
	for _, file := range input.ChangedFiles {
		lower := strings.ToLower(file)
		switch {
		case strings.Contains(lower, "auth"), strings.Contains(lower, "security"), strings.Contains(lower, "billing"):
			addFlag("sensitive_path")
		case strings.Contains(lower, "migration") || strings.HasPrefix(lower, "migrations/"):
			addFlag("migration")
		}
	}
	if len(flags) == 0 {
		return e.checkBase(models.PRReadinessCheckTypeRiskFlags, models.PRReadinessCheckStatusPassed, "No elevated risk flags", "No configured sensitive paths or migrations were detected.", "View files", nil)
	}
	return e.checkBase(models.PRReadinessCheckTypeRiskFlags, models.PRReadinessCheckStatusWarning, "Risk flags detected", "Sensitive paths, migrations, or similar review-risk signals changed.", "View files", map[string]any{
		"flags": flags,
		"files": input.ChangedFiles,
	})
}

func (e *Evaluator) contextCheck(input EvaluationInput) models.PRReadinessCheck {
	if input.LinkedIssueCount > 0 {
		return e.checkBase(models.PRReadinessCheckTypeContextComplete, models.PRReadinessCheckStatusPassed, "Context linked", "This session has linked issue context.", "View context", nil)
	}
	return e.checkBase(models.PRReadinessCheckTypeContextComplete, models.PRReadinessCheckStatusWarning, "No linked issue context", "No linked issue was found for reviewer context.", "Add context", nil)
}

func (e *Evaluator) reviewPacketCheck(input EvaluationInput) models.PRReadinessCheck {
	if len(input.Session.DiffStats) > 0 {
		return e.checkBase(models.PRReadinessCheckTypeReviewPacketDraftable, models.PRReadinessCheckStatusPassed, "Review packet draftable", "Diff summary and readiness evidence are available.", "View packet", nil)
	}
	return e.checkBase(models.PRReadinessCheckTypeReviewPacketDraftable, models.PRReadinessCheckStatusWarning, "Review packet incomplete", "A review packet could not include a grounded diff summary.", "Re-run", nil)
}

func aggregateStatus(checks []models.PRReadinessCheck) models.PRReadinessRunStatus {
	hasWarning := false
	for _, check := range checks {
		if check.Status == models.PRReadinessCheckStatusFailed && check.Enforcement == models.PRReadinessEnforcementBlocking {
			return models.PRReadinessRunStatusBlocked
		}
		if check.Status == models.PRReadinessCheckStatusWarning || check.Status == models.PRReadinessCheckStatusFailed {
			hasWarning = true
		}
	}
	if hasWarning {
		return models.PRReadinessRunStatusWarnings
	}
	return models.PRReadinessRunStatusPassed
}

func summaryForStatus(status models.PRReadinessRunStatus) string {
	switch status {
	case models.PRReadinessRunStatusPassed:
		return "Ready"
	case models.PRReadinessRunStatusWarnings:
		return "Ready with warnings"
	case models.PRReadinessRunStatusBlocked:
		return "Blocked"
	default:
		return string(status)
	}
}

func buildReviewPacket(input EvaluationInput, checks []models.PRReadinessCheck) map[string]any {
	return map[string]any{
		"workspace_revision": input.EvaluatedWorkspaceRevision,
		"snapshot_key":       input.EvaluatedSnapshotKey,
		"changed_files":      input.ChangedFiles,
		"checks":             checks,
	}
}

func stringValue(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}
