package validation

import (
	"bytes"
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/agent"
)

// validationStore is the subset of db.ValidationStore used by the service.
type validationStore interface {
	Create(ctx context.Context, v *models.Validation) error
	UpdateCheck(ctx context.Context, orgID, id uuid.UUID, checkName, result string, details []byte) error
	UpdateStatus(ctx context.Context, orgID, id uuid.UUID, status string) error
}

// issueStore is the subset of db.IssueStore used by the service.
type issueStore interface {
	UpdateStatus(ctx context.Context, orgID, issueID uuid.UUID, status string) error
}

// jobStore is the subset of db.JobStore used by the service.
type jobStore interface {
	Enqueue(ctx context.Context, orgID uuid.UUID, queue, jobType string, payload any, priority int, dedupeKey *string) (uuid.UUID, error)
}

// Service validates agent-produced diffs before PR creation.
type Service struct {
	validations validationStore
	issues      issueStore
	jobs        jobStore
	provider    agent.SandboxProvider
	logger      zerolog.Logger
}

// NewService creates a new validation service.
func NewService(
	validations validationStore,
	issues issueStore,
	jobs jobStore,
	provider agent.SandboxProvider,
	logger zerolog.Logger,
) *Service {
	return &Service{
		validations: validations,
		issues:      issues,
		jobs:        jobs,
		provider:    provider,
		logger:      logger,
	}
}

// Validate runs the validation pipeline for an agent run.
// It creates a Validation record, runs checks sequentially (fail-fast),
// and on success enqueues an "open_pr" job.
func (s *Service) Validate(ctx context.Context, agentRun *models.AgentRun, sandbox *agent.Sandbox) error {
	diff := ""
	if agentRun.Diff != nil {
		diff = *agentRun.Diff
	}

	v := &models.Validation{
		AgentRunID: agentRun.ID,
		OrgID:      agentRun.OrgID,
		Status:     "running",
	}
	if err := s.validations.Create(ctx, v); err != nil {
		return fmt.Errorf("create validation: %w", err)
	}
	if err := s.validations.UpdateStatus(ctx, v.OrgID, v.ID, "running"); err != nil {
		return fmt.Errorf("update validation status to running: %w", err)
	}

	s.logger.Info().
		Str("validation_id", v.ID.String()).
		Str("agent_run_id", agentRun.ID.String()).
		Msg("starting validation pipeline")

	// Skipped checks (LLM-based, not yet implemented)
	skippedChecks := []string{"direction_check", "correctness_check", "regression_test_check"}
	for _, checkName := range skippedChecks {
		if err := s.validations.UpdateCheck(ctx, v.OrgID, v.ID, checkName, "skipped", []byte(`"LLM-based check not yet implemented"`)); err != nil {
			return fmt.Errorf("update skipped check %s: %w", checkName, err)
		}
	}

	// Active checks run sequentially with fail-fast
	type check struct {
		name string
		fn   func(ctx context.Context, diff string, sandbox *agent.Sandbox) (string, string, error)
	}
	checks := []check{
		{"security_scan", func(ctx context.Context, d string, _ *agent.Sandbox) (string, string, error) {
			return s.checkSecurity(d)
		}},
		{"quality_check", func(_ context.Context, d string, _ *agent.Sandbox) (string, string, error) {
			return s.checkDiffSize(d)
		}},
		{"ci_check", func(ctx context.Context, _ string, sb *agent.Sandbox) (string, string, error) {
			return s.checkCI(ctx, sb)
		}},
	}

	allPassed := true
	for _, c := range checks {
		result, details, err := c.fn(ctx, diff, sandbox)
		if err != nil {
			result = "fail"
			details = err.Error()
		}

		s.logger.Info().
			Str("check", c.name).
			Str("result", result).
			Msg("validation check completed")

		detailsJSON := []byte(fmt.Sprintf("%q", details))
		if err := s.validations.UpdateCheck(ctx, v.OrgID, v.ID, c.name, result, detailsJSON); err != nil {
			return fmt.Errorf("update check %s: %w", c.name, err)
		}

		if result == "fail" {
			allPassed = false
			break
		}
	}

	if allPassed {
		if err := s.validations.UpdateStatus(ctx, v.OrgID, v.ID, "passed"); err != nil {
			return fmt.Errorf("update validation status to passed: %w", err)
		}
		payload := map[string]string{
			"agent_run_id": agentRun.ID.String(),
			"org_id":       agentRun.OrgID.String(),
		}
		dedupeKey := fmt.Sprintf("open_pr:%s", agentRun.ID.String())
		if _, err := s.jobs.Enqueue(ctx, agentRun.OrgID, "default", "open_pr", payload, 5, &dedupeKey); err != nil {
			return fmt.Errorf("enqueue open_pr job: %w", err)
		}
		s.logger.Info().
			Str("agent_run_id", agentRun.ID.String()).
			Msg("validation passed, open_pr job enqueued")
	} else {
		if err := s.validations.UpdateStatus(ctx, v.OrgID, v.ID, "failed"); err != nil {
			return fmt.Errorf("update validation status to failed: %w", err)
		}
		if err := s.issues.UpdateStatus(ctx, agentRun.OrgID, agentRun.IssueID, "triaged"); err != nil {
			return fmt.Errorf("update issue status to triaged: %w", err)
		}
		s.logger.Info().
			Str("agent_run_id", agentRun.ID.String()).
			Msg("validation failed, issue moved back to triaged")
	}

	return nil
}

// secretPatterns are regex patterns for detecting secrets in diffs.
var secretPatterns = []*regexp.Regexp{
	// AWS access key ID
	regexp.MustCompile(`AKIA[0-9A-Z]{16}`),
	// AWS secret access key (40 hex chars after common assignment patterns)
	regexp.MustCompile(`(?i)(aws_secret_access_key|aws_secret)\s*[:=]\s*["']?[A-Za-z0-9/+=]{40}`),
	// GitHub personal access token (classic)
	regexp.MustCompile(`ghp_[A-Za-z0-9]{36}`),
	// GitHub fine-grained token
	regexp.MustCompile(`github_pat_[A-Za-z0-9_]{82}`),
	// GitHub OAuth access token
	regexp.MustCompile(`gho_[A-Za-z0-9]{36}`),
	// Generic API key assignment
	regexp.MustCompile(`(?i)(api[_-]?key|apikey|api[_-]?secret)\s*[:=]\s*["'][A-Za-z0-9]{20,}["']`),
	// Hardcoded password assignment
	regexp.MustCompile(`(?i)(password|passwd|pwd)\s*[:=]\s*["'][^"']{8,}["']`),
	// Private key block
	regexp.MustCompile(`-----BEGIN (RSA |EC |DSA )?PRIVATE KEY-----`),
}

// sqlInjectionPatterns detect string concatenation in SQL queries.
var sqlInjectionPatterns = []*regexp.Regexp{
	// String concatenation in SQL (Go-style: "SELECT ... " + variable)
	regexp.MustCompile(`(?i)(SELECT|INSERT|UPDATE|DELETE|DROP)\s+.*["']\s*\+\s*\w+`),
	// fmt.Sprintf in SQL queries
	regexp.MustCompile(`(?i)fmt\.Sprintf\(\s*["']` + "`?" + `\s*(SELECT|INSERT|UPDATE|DELETE|DROP)`),
}

// checkSecurity scans a diff for obvious security issues.
func (s *Service) checkSecurity(diff string) (string, string, error) {
	addedLines := extractAddedLines(diff)

	var findings []string

	for _, pattern := range secretPatterns {
		if matches := pattern.FindAllString(addedLines, -1); len(matches) > 0 {
			findings = append(findings, fmt.Sprintf("potential secret detected: pattern %s matched", pattern.String()))
		}
	}

	for _, pattern := range sqlInjectionPatterns {
		if matches := pattern.FindAllString(addedLines, -1); len(matches) > 0 {
			findings = append(findings, fmt.Sprintf("potential SQL injection: pattern %s matched", pattern.String()))
		}
	}

	if len(findings) > 0 {
		return "fail", fmt.Sprintf("security issues found: %s", strings.Join(findings, "; ")), nil
	}
	return "pass", "no security issues detected", nil
}

// checkCI runs tests in the sandbox by detecting project type and executing
// the appropriate test command.
func (s *Service) checkCI(ctx context.Context, sandbox *agent.Sandbox) (string, string, error) {
	if sandbox == nil {
		return "fail", "no sandbox available for CI check", nil
	}

	type ciCommand struct {
		marker  string
		command string
	}
	ciCommands := []ciCommand{
		{"go.mod", "go test ./..."},
		{"package.json", "npm test"},
		{"requirements.txt", "pytest"},
		{"Makefile", "make test"},
	}

	for _, ci := range ciCommands {
		_, err := s.provider.ReadFile(ctx, sandbox, ci.marker)
		if err != nil {
			continue
		}

		var stdout, stderr bytes.Buffer
		exitCode, err := s.provider.Exec(ctx, sandbox, ci.command, &stdout, &stderr)
		if err != nil {
			return "fail", fmt.Sprintf("error running %q: %s", ci.command, err.Error()), nil
		}

		if exitCode != 0 {
			output := stderr.String()
			if output == "" {
				output = stdout.String()
			}
			if len(output) > 2000 {
				output = output[:2000] + "\n... (truncated)"
			}
			return "fail", fmt.Sprintf("tests failed (exit code %d): %s", exitCode, output), nil
		}

		return "pass", fmt.Sprintf("tests passed: %s", ci.command), nil
	}

	return "pass", "no recognized project type, skipping CI", nil
}

const diffSizeWarnThreshold = 200
const diffSizeFailThreshold = 500

// checkDiffSize counts changed lines and enforces size limits.
func (s *Service) checkDiffSize(diff string) (string, string, error) {
	added, removed := countDiffLines(diff)
	total := added + removed

	if total > diffSizeFailThreshold {
		return "fail", fmt.Sprintf("diff too large: %d lines changed (%d added, %d removed), max %d", total, added, removed, diffSizeFailThreshold), nil
	}
	if total > diffSizeWarnThreshold {
		return "warn", fmt.Sprintf("large diff: %d lines changed (%d added, %d removed), threshold %d", total, added, removed, diffSizeWarnThreshold), nil
	}
	return "pass", fmt.Sprintf("%d lines changed (%d added, %d removed)", total, added, removed), nil
}

// extractAddedLines returns only the added lines from a unified diff,
// excluding file header lines (+++).
func extractAddedLines(diff string) string {
	var added []string
	for _, line := range strings.Split(diff, "\n") {
		if strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++") {
			added = append(added, line[1:])
		}
	}
	return strings.Join(added, "\n")
}

// countDiffLines counts added and removed lines in a unified diff.
func countDiffLines(diff string) (added, removed int) {
	for _, line := range strings.Split(diff, "\n") {
		if strings.HasPrefix(line, "+") && !strings.HasPrefix(line, "+++") {
			added++
		} else if strings.HasPrefix(line, "-") && !strings.HasPrefix(line, "---") {
			removed++
		}
	}
	return added, removed
}
