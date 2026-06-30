package readiness

import (
	"context"
	"encoding/json"
	"path"
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
	IssueLessReason            string
	PolicyConfig               models.PRReadinessPolicyConfig
	CustomChecks               []models.PRReadinessCheck
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
	builtins := []struct {
		checkType models.PRReadinessCheckType
		evaluate  func(EvaluationInput) models.PRReadinessCheck
	}{
		{models.PRReadinessCheckTypeFreshness, e.freshnessCheck},
		{models.PRReadinessCheckTypeAgentReviewClean, e.agentReviewCheck},
		{models.PRReadinessCheckTypeDiffCollected, e.diffCollectedCheck},
		{models.PRReadinessCheckTypeTestEvidencePresent, e.testEvidenceCheck},
		{models.PRReadinessCheckTypeRiskFlags, e.riskFlagsCheck},
		{models.PRReadinessCheckTypeDependencyConfigRisk, e.dependencyConfigRiskCheck},
		{models.PRReadinessCheckTypeGeneratedFileChurn, e.generatedFileChurnCheck},
		{models.PRReadinessCheckTypeContextComplete, e.contextCheck},
		{models.PRReadinessCheckTypeReviewPacketDraftable, e.reviewPacketCheck},
	}
	checks := make([]models.PRReadinessCheck, 0, len(builtins)+len(input.CustomChecks))
	for _, builtin := range builtins {
		if !e.policy.ShouldEvaluateCheck(builtin.checkType) {
			continue
		}
		checks = append(checks, builtin.evaluate(input))
	}
	checks = append(checks, input.CustomChecks...)
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
	enforcement := models.PRReadinessEnforcementByRole{
		Builder:  e.policy.EnforcementFor(models.RoleBuilder, checkType),
		Engineer: e.policy.EnforcementFor(models.RoleMember, checkType),
		Admin:    e.policy.EnforcementFor(models.RoleAdmin, checkType),
	}
	return models.PRReadinessCheck{
		CheckKey:            string(checkType),
		CheckType:           checkType,
		Status:              status,
		Enforcement:         enforcement.Builder,
		EnforcementByRole:   enforcement,
		EnforcementBuilder:  enforcement.Builder,
		EnforcementEngineer: enforcement.Engineer,
		EnforcementAdmin:    enforcement.Admin,
		Provenance:          models.PRReadinessProvenanceBuiltin,
		Title:               title,
		Summary:             summary,
		Action:              action,
		Details:             raw,
	}
}

func (e *Evaluator) freshnessCheck(input EvaluationInput) models.PRReadinessCheck {
	if input.Session.WorkspaceRevision == input.EvaluatedWorkspaceRevision && stringValue(input.Session.SnapshotKey) == input.EvaluatedSnapshotKey {
		return e.checkBase(models.PRReadinessCheckTypeFreshness, models.PRReadinessCheckStatusPassed, "Readiness is fresh", "Checked against the latest workspace revision.", "Re-run", nil)
	}
	return e.checkBase(models.PRReadinessCheckTypeFreshness, models.PRReadinessCheckStatusFailed, "Readiness is stale", "Workspace files changed after this readiness result was produced.", "Re-run", map[string]any{
		"current_workspace_revision":   input.Session.WorkspaceRevision,
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
	if hasDiffStats(input.Session.DiffStats) {
		return e.checkBase(models.PRReadinessCheckTypeDiffCollected, models.PRReadinessCheckStatusPassed, "Diff collected", "Diff stats are available for this session.", "View changes", nil)
	}
	return e.checkBase(models.PRReadinessCheckTypeDiffCollected, models.PRReadinessCheckStatusWarning, "Diff not collected", "No diff stats were available when readiness ran.", "View changes", nil)
}

var (
	testEvidencePattern = regexp.MustCompile(`(?i)\b(go test|npm (run )?test|pnpm test|yarn test|pytest|vitest|cargo test|mvn test|gradle test|make test)\b`)
	testSuccessPattern  = regexp.MustCompile(`(?i)\b(pass(ed|es)?|success(ful)?|ok|exit code 0|0 failed|tests? passed)\b`)
	testFailurePattern  = regexp.MustCompile(`(?i)\b(fail(ed|ure|ing)?|error|exit code [1-9][0-9]*)\b`)
)

func (e *Evaluator) testEvidenceCheck(input EvaluationInput) models.PRReadinessCheck {
	revisionUpdatedAt := input.Session.WorkspaceRevisionUpdatedAt
	for _, log := range input.Logs {
		if !revisionUpdatedAt.IsZero() && log.Timestamp.Before(revisionUpdatedAt) {
			continue
		}
		if testEvidencePattern.MatchString(log.Message) && testSuccessPattern.MatchString(log.Message) && !testFailurePattern.MatchString(log.Message) {
			return e.checkBase(models.PRReadinessCheckTypeTestEvidencePresent, models.PRReadinessCheckStatusPassed, "Test evidence found", "A test or verification command was captured after the session changed files.", "View logs", map[string]any{
				"log_id": log.ID,
			})
		}
	}
	return e.checkBase(models.PRReadinessCheckTypeTestEvidencePresent, models.PRReadinessCheckStatusWarning, "No test evidence found", "No captured test or verification command output was found.", "Run tests", nil)
}

func (e *Evaluator) riskFlagsCheck(input EvaluationInput) models.PRReadinessCheck {
	flags := riskFlags(input)
	if len(flags) == 0 {
		return e.checkBase(models.PRReadinessCheckTypeRiskFlags, models.PRReadinessCheckStatusPassed, "No elevated risk flags", "No configured sensitive paths or migrations were detected.", "View files", nil)
	}
	return e.checkBase(models.PRReadinessCheckTypeRiskFlags, models.PRReadinessCheckStatusWarning, "Risk flags detected", "Sensitive paths, migrations, or similar review-risk signals changed.", "View files", map[string]any{
		"flags": flags,
		"files": input.ChangedFiles,
	})
}

func (e *Evaluator) dependencyConfigRiskCheck(input EvaluationInput) models.PRReadinessCheck {
	files := dependencyConfigFiles(input.ChangedFiles)
	files = appendUniqueStrings(files, generatedFiles(input.ChangedFiles, input.PolicyConfig)...)
	if len(files) == 0 {
		return e.checkBase(models.PRReadinessCheckTypeDependencyConfigRisk, models.PRReadinessCheckStatusPassed, "No dependency or runtime config changes", "No dependency manifests, lockfiles, workflow, or runtime config files changed.", "View files", nil)
	}
	return e.checkBase(models.PRReadinessCheckTypeDependencyConfigRisk, models.PRReadinessCheckStatusWarning, "Dependency or config changes detected", "Dependency manifests, lockfiles, workflow, or runtime config changed and may need closer review.", "View files", map[string]any{
		"files": files,
	})
}

func (e *Evaluator) generatedFileChurnCheck(input EvaluationInput) models.PRReadinessCheck {
	files := generatedFiles(input.ChangedFiles, input.PolicyConfig)
	if len(files) == 0 {
		return e.checkBase(models.PRReadinessCheckTypeGeneratedFileChurn, models.PRReadinessCheckStatusPassed, "No generated file churn", "No common generated-output paths changed.", "View files", nil)
	}
	return e.checkBase(models.PRReadinessCheckTypeGeneratedFileChurn, models.PRReadinessCheckStatusWarning, "Generated file churn detected", "Generated output changed; reviewers should confirm source changes explain it.", "View files", map[string]any{
		"files": files,
	})
}

func (e *Evaluator) contextCheck(input EvaluationInput) models.PRReadinessCheck {
	if input.LinkedIssueCount > 0 {
		return e.checkBase(models.PRReadinessCheckTypeContextComplete, models.PRReadinessCheckStatusPassed, "Context linked", "This session has linked issue context.", "View context", nil)
	}
	if strings.TrimSpace(input.IssueLessReason) != "" {
		return e.checkBase(models.PRReadinessCheckTypeContextComplete, models.PRReadinessCheckStatusPassed, "Issue-less context marked", "This session includes an explicit issue-less readiness reason.", "View context", map[string]any{
			"issue_less_reason": strings.TrimSpace(input.IssueLessReason),
		})
	}
	return e.checkBase(models.PRReadinessCheckTypeContextComplete, models.PRReadinessCheckStatusWarning, "No linked issue context", "No linked issue was found for reviewer context.", "Add context", nil)
}

func (e *Evaluator) reviewPacketCheck(input EvaluationInput) models.PRReadinessCheck {
	if hasDiffStats(input.Session.DiffStats) {
		return e.checkBase(models.PRReadinessCheckTypeReviewPacketDraftable, models.PRReadinessCheckStatusPassed, "Review packet draftable", "Diff summary and readiness evidence are available.", "View packet", nil)
	}
	return e.checkBase(models.PRReadinessCheckTypeReviewPacketDraftable, models.PRReadinessCheckStatusWarning, "Review packet incomplete", "A review packet could not include a grounded diff summary.", "Re-run", nil)
}

// hasDiffStats reports whether the session carries usable diff stats. Postgres
// jsonb stores an absent value as the literal "null", so an empty payload and a
// 4-byte "null" must both count as missing — every readiness consumer of
// DiffStats uses this so they agree on the empty case.
func hasDiffStats(diffStats json.RawMessage) bool {
	return len(diffStats) > 0 && string(diffStats) != "null"
}

// aggregateStatus computes the run-level status as a role-agnostic worst case: a
// run is Blocked if any failed/errored check is blocking for ANY role. This is a
// summary headline only — the actual per-role PR gate is computed separately from
// PRReadinessRun.UnbypassedBlockingCheckKeys(role), so a check that blocks only
// admins can show the run as "Blocked" while a builder is in fact unblocked. UI
// that needs role-accurate blocking must use the per-role enforcement, not this.
func aggregateStatus(checks []models.PRReadinessCheck) models.PRReadinessRunStatus {
	hasWarning := false
	for _, check := range checks {
		if (check.Status == models.PRReadinessCheckStatusFailed || check.Status == models.PRReadinessCheckStatusError) && checkBlocksAnyRole(check) {
			return models.PRReadinessRunStatusBlocked
		}
		if check.Status == models.PRReadinessCheckStatusWarning || check.Status == models.PRReadinessCheckStatusFailed || check.Status == models.PRReadinessCheckStatusError {
			hasWarning = true
		}
	}
	if hasWarning {
		return models.PRReadinessRunStatusWarnings
	}
	return models.PRReadinessRunStatusPassed
}

func checkBlocksAnyRole(check models.PRReadinessCheck) bool {
	if check.Enforcement == models.PRReadinessEnforcementBlocking {
		return true
	}
	return check.EnforcementByRole.EnforcementFor(models.RoleBuilder) == models.PRReadinessEnforcementBlocking ||
		check.EnforcementByRole.EnforcementFor(models.RoleMember) == models.PRReadinessEnforcementBlocking ||
		check.EnforcementByRole.EnforcementFor(models.RoleAdmin) == models.PRReadinessEnforcementBlocking
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
	risk := riskFlags(input)
	unknowns := make([]string, 0)
	if !hasDiffStats(input.Session.DiffStats) {
		unknowns = append(unknowns, "diff_stats")
	}
	if input.LinkedIssueCount == 0 && strings.TrimSpace(input.IssueLessReason) == "" {
		unknowns = append(unknowns, "issue_context")
	}
	whyChanged := map[string]any{
		"linked_issue_count": input.LinkedIssueCount,
	}
	if strings.TrimSpace(input.IssueLessReason) != "" {
		whyChanged["issue_less_reason"] = strings.TrimSpace(input.IssueLessReason)
	}
	return map[string]any{
		"workspace_revision": input.EvaluatedWorkspaceRevision,
		"snapshot_key":       input.EvaluatedSnapshotKey,
		"what_changed": map[string]any{
			"changed_files": input.ChangedFiles,
			"diff_stats":    input.Session.DiffStats,
		},
		"why_changed": whyChanged,
		"checked_at":  time.Now().UTC().Format(time.RFC3339),
		"risk_flags":  risk,
		// Always empty here: the packet is built during evaluation, before a
		// blocked result can be bypassed. Recorded bypasses live on the run's
		// Bypasses relation, not in the packet snapshot.
		"bypasses": []models.PRReadinessBypass{},
		"unknowns": unknowns,
		"checks":   checks,
	}
}

func riskFlags(input EvaluationInput) []string {
	seen := map[string]struct{}{}
	flags := make([]string, 0)
	addFlag := func(f string) {
		if _, ok := seen[f]; !ok {
			seen[f] = struct{}{}
			flags = append(flags, f)
		}
	}
	fileThreshold, lineThreshold := diffThresholds(input.PolicyConfig)
	filesChanged, linesChanged := diffSize(input.Session.DiffStats)
	if filesChanged >= fileThreshold || linesChanged >= lineThreshold {
		addFlag("large_diff")
	}
	if hasMigration(input.ChangedFiles) {
		addFlag("migration")
	}
	if hasSensitivePath(input.ChangedFiles, input.PolicyConfig) {
		addFlag("sensitive_path")
	}
	if len(dependencyConfigFiles(input.ChangedFiles)) > 0 {
		addFlag("dependency_or_config")
	}
	return flags
}

func diffThresholds(policy models.PRReadinessPolicyConfig) (int, int) {
	defaults := models.DefaultPRReadinessPolicyConfig()
	fileThreshold := policy.LargeDiffFileThreshold
	if fileThreshold <= 0 {
		fileThreshold = defaults.LargeDiffFileThreshold
	}
	lineThreshold := policy.LargeDiffLineThreshold
	if lineThreshold <= 0 {
		lineThreshold = defaults.LargeDiffLineThreshold
	}
	return fileThreshold, lineThreshold
}

func diffSize(raw json.RawMessage) (int, int) {
	if len(raw) == 0 || string(raw) == "null" {
		return 0, 0
	}
	var stats map[string]float64
	if err := json.Unmarshal(raw, &stats); err != nil {
		return 0, 0
	}
	files := int(firstStat(stats, "files_changed", "changed_files", "files"))
	additions := int(firstStat(stats, "added", "additions", "lines_added"))
	deletions := int(firstStat(stats, "removed", "deletions", "lines_deleted"))
	return files, additions + deletions
}

func firstStat(stats map[string]float64, keys ...string) float64 {
	for _, key := range keys {
		if value, ok := stats[key]; ok {
			return value
		}
	}
	return 0
}

func hasMigration(files []string) bool {
	for _, file := range files {
		lower := strings.ToLower(file)
		if strings.Contains(lower, "migration") || strings.HasPrefix(lower, "migrations/") {
			return true
		}
	}
	return false
}

func hasSensitivePath(files []string, policy models.PRReadinessPolicyConfig) bool {
	patterns := policy.SensitivePaths
	if len(patterns) == 0 {
		patterns = models.DefaultPRReadinessPolicyConfig().SensitivePaths
	}
	for _, file := range files {
		for _, pattern := range patterns {
			if MatchPathPattern(pattern, file) {
				return true
			}
		}
	}
	return false
}

func dependencyConfigFiles(files []string) []string {
	matches := make([]string, 0)
	for _, file := range files {
		lower := strings.ToLower(file)
		base := path.Base(lower)
		if isDependencyOrRuntimeConfig(lower, base) {
			matches = append(matches, file)
		}
	}
	return matches
}

func isDependencyOrRuntimeConfig(lower, base string) bool {
	switch base {
	case "package.json", "package-lock.json", "pnpm-lock.yaml", "yarn.lock", "bun.lockb",
		"go.mod", "go.sum", "cargo.toml", "cargo.lock", "requirements.txt", "poetry.lock",
		"pyproject.toml", "pipfile", "pipfile.lock", "gemfile", "gemfile.lock",
		"dockerfile", "docker-compose.yml", "docker-compose.yaml", "makefile":
		return true
	}
	return strings.HasPrefix(lower, ".github/workflows/") ||
		strings.HasPrefix(lower, "deploy/") ||
		strings.HasPrefix(lower, "infra/") ||
		strings.HasPrefix(lower, "terraform/") ||
		strings.HasSuffix(lower, ".tf") ||
		strings.HasSuffix(lower, ".tfvars") ||
		strings.HasSuffix(lower, ".env.example")
}

func generatedFiles(files []string, policy models.PRReadinessPolicyConfig) []string {
	matches := make([]string, 0)
	for _, file := range files {
		if generatedFileAllowed(file, policy.GeneratedFileAllowedPaths) {
			continue
		}
		lower := strings.ToLower(file)
		base := path.Base(lower)
		if strings.Contains(lower, "/generated/") ||
			strings.HasPrefix(lower, "generated/") ||
			strings.Contains(lower, "/dist/") ||
			strings.HasPrefix(lower, "dist/") ||
			strings.Contains(lower, "/build/") ||
			strings.HasPrefix(lower, "build/") ||
			strings.Contains(base, ".generated.") ||
			strings.HasSuffix(base, "_gen.go") ||
			strings.HasSuffix(base, ".pb.go") {
			matches = append(matches, file)
		}
	}
	return matches
}

func generatedFileAllowed(file string, allowedPatterns []string) bool {
	for _, pattern := range allowedPatterns {
		if MatchPathPattern(pattern, file) {
			return true
		}
	}
	return false
}

func appendUniqueStrings(values []string, extras ...string) []string {
	seen := make(map[string]struct{}, len(values)+len(extras))
	out := make([]string, 0, len(values)+len(extras))
	for _, value := range append(values, extras...) {
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		out = append(out, value)
	}
	return out
}

// MatchPathPattern reports whether file matches a readiness path pattern. It is
// the single source of truth for path matching across readiness checks (sensitive
// paths, generated-file allow-lists, and custom-check include/exclude filters) so
// the same glob in different places behaves identically. Matching is
// case-insensitive. Supported forms:
//
//	prefix/**   subtree match (file == prefix or under prefix/)
//	**/suffix   suffix match (file == suffix or ends with /suffix)
//	a/*/b       '*' is a glob wildcard (crosses path separators), anchored
//	plain       exact file match, or a path-segment prefix (file under plain/)
//
// Unlike the previous implementations it does NOT fall back to an unanchored
// substring match, which produced false positives like pattern "api" matching
// "internal/capitalize_apiary.go" or "env" matching "internal/environment/...".
func MatchPathPattern(pattern, file string) bool {
	pattern = strings.ToLower(strings.TrimSpace(pattern))
	file = strings.ToLower(strings.TrimSpace(file))
	if pattern == "" || file == "" {
		return false
	}
	if strings.HasSuffix(pattern, "/**") {
		prefix := strings.TrimSuffix(pattern, "/**")
		return file == prefix || strings.HasPrefix(file, prefix+"/")
	}
	if strings.HasPrefix(pattern, "**/") {
		suffix := strings.TrimPrefix(pattern, "**/")
		return file == suffix || strings.HasSuffix(file, "/"+suffix)
	}
	if strings.Contains(pattern, "*") {
		re := regexp.QuoteMeta(pattern)
		re = strings.ReplaceAll(re, `\*`, ".*")
		ok, err := regexp.MatchString("^"+re+"$", file)
		return err == nil && ok
	}
	return file == pattern || strings.HasPrefix(file, pattern+"/")
}

func stringValue(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}
