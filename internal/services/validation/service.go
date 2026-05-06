package validation

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	llmpkg "github.com/assembledhq/143/internal/llm"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/prompts"
	"github.com/assembledhq/143/internal/repoconfig"
	"github.com/assembledhq/143/internal/sanitize"
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

// orgStore is the subset of db.OrganizationStore used by the service.
type orgStore interface {
	GetByID(ctx context.Context, id uuid.UUID) (models.Organization, error)
}

// jobStore is the subset of db.JobStore used by the service.
type jobStore interface {
	Enqueue(ctx context.Context, orgID uuid.UUID, queue, jobType string, payload any, priority int, dedupeKey *string) (uuid.UUID, error)
}

// Service validates agent-produced diffs before PR creation.
type Service struct {
	validations validationStore
	issues      issueStore
	orgs        orgStore
	jobs        jobStore
	llm         llmpkg.Client
	provider    agent.SandboxProvider
	logger      zerolog.Logger
}

// NewService creates a new validation service.
func NewService(
	validations validationStore,
	issues issueStore,
	orgs orgStore,
	jobs jobStore,
	llmClient llmpkg.Client,
	provider agent.SandboxProvider,
	logger zerolog.Logger,
) *Service {
	return &Service{
		validations: validations,
		issues:      issues,
		orgs:        orgs,
		jobs:        jobs,
		llm:         llmClient,
		provider:    provider,
		logger:      logger,
	}
}

// Validate runs the validation pipeline for an agent run.
// It creates a Validation record, runs checks sequentially (fail-fast),
// and on success enqueues an "open_pr" job.
func (s *Service) Validate(ctx context.Context, agentRun *models.Session, issue *models.Issue, sandbox *agent.Sandbox) error {
	diff := ""
	if agentRun.Diff != nil {
		diff = *agentRun.Diff
	}

	v := &models.Validation{
		SessionID: agentRun.ID,
		OrgID:     agentRun.OrgID,
		Status:    "running",
	}
	if err := s.validations.Create(ctx, v); err != nil {
		return fmt.Errorf("create validation: %w", err)
	}
	if err := s.validations.UpdateStatus(ctx, v.OrgID, v.ID, "running"); err != nil {
		return fmt.Errorf("update validation status to running: %w", err)
	}

	s.logger.Info().
		Str("validation_id", v.ID.String()).
		Str("session_id", agentRun.ID.String()).
		Msg("starting validation pipeline")

	// Fetch org settings for direction check.
	var org *models.Organization
	if s.orgs != nil {
		fetched, err := s.orgs.GetByID(ctx, agentRun.OrgID)
		if err != nil {
			s.logger.Warn().Err(err).Msg("failed to fetch org for direction check, will skip direction context")
		} else {
			org = &fetched
		}
	}

	// Active checks run sequentially with fail-fast.
	// LLM-based checks run first (direction, correctness, regression test),
	// followed by deterministic checks (security, quality, CI).
	type check struct {
		name string
		fn   func(ctx context.Context, diff string, sandbox *agent.Sandbox) (string, string, error)
	}
	checks := []check{
		{"direction_check", func(ctx context.Context, d string, _ *agent.Sandbox) (string, string, error) {
			return s.checkDirection(ctx, d, issue, org)
		}},
		{"correctness_check", func(ctx context.Context, d string, _ *agent.Sandbox) (string, string, error) {
			return s.checkCorrectness(ctx, d, issue)
		}},
		{"regression_test_check", func(ctx context.Context, d string, _ *agent.Sandbox) (string, string, error) {
			return s.checkRegressionTest(ctx, d, issue)
		}},
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
			"session_id": agentRun.ID.String(),
			"org_id":     agentRun.OrgID.String(),
		}
		dedupeKey := fmt.Sprintf("open_pr:%s", agentRun.ID.String())
		if _, err := s.jobs.Enqueue(ctx, agentRun.OrgID, "default", "open_pr", payload, 5, &dedupeKey); err != nil {
			return fmt.Errorf("enqueue open_pr job: %w", err)
		}
		s.logger.Info().
			Str("session_id", agentRun.ID.String()).
			Msg("validation passed, open_pr job enqueued")
	} else {
		if err := s.validations.UpdateStatus(ctx, v.OrgID, v.ID, "failed"); err != nil {
			return fmt.Errorf("update validation status to failed: %w", err)
		}
		if agentRun.PrimaryIssueID != nil {
			if err := s.issues.UpdateStatus(ctx, agentRun.OrgID, *agentRun.PrimaryIssueID, "triaged"); err != nil {
				return fmt.Errorf("update issue status to triaged: %w", err)
			}
		}
		s.logger.Info().
			Str("session_id", agentRun.ID.String()).
			Msg("validation failed, issue moved back to triaged")
	}

	return nil
}

// llmCheckResult is the expected JSON response from the LLM for validation checks.
type llmCheckResult struct {
	Result    string `json:"result"`
	Reasoning string `json:"reasoning"`
}

// parseLLMCheckResult parses the LLM JSON response into a result and reasoning.
// Returns ("fail", reasoning, nil) if parsing fails to be safe.
func parseLLMCheckResult(raw string) (string, string, error) {
	var res llmCheckResult
	if err := json.Unmarshal([]byte(raw), &res); err != nil {
		return "fail", fmt.Sprintf("failed to parse LLM response: %s", raw), nil
	}
	if res.Result != "pass" && res.Result != "fail" {
		return "fail", fmt.Sprintf("unexpected LLM result %q: %s", res.Result, res.Reasoning), nil
	}
	return res.Result, res.Reasoning, nil
}

// wrapDiff wraps a diff in <code_diff> tags to defend against prompt injection.
func wrapDiff(diff string) string {
	return "<code_diff>\n" + diff + "\n</code_diff>"
}

// buildIssueContext returns sanitized issue title/description for LLM prompts.
func buildIssueContext(issue *models.Issue) string {
	if issue == nil {
		return "No issue context available.\n"
	}
	result := fmt.Sprintf("Issue Title: %s\n", sanitize.SanitizeForPrompt(issue.Title, 10000))
	if issue.Description != nil {
		result += fmt.Sprintf("Issue Description: %s\n", sanitize.SanitizeForPrompt(*issue.Description, 10000))
	}
	return result
}

// checkDirection uses the LLM to verify that the diff aligns with the issue
// and the organization's product direction.
func (s *Service) checkDirection(ctx context.Context, diff string, issue *models.Issue, org *models.Organization) (string, string, error) {
	if s.llm == nil {
		return "skipped", "LLM client not configured", nil
	}

	systemPrompt := prompts.DirectionCheckPrompt()

	issueContext := buildIssueContext(issue)

	var orgContext string
	if org != nil && org.Settings != nil {
		orgContext = fmt.Sprintf("Organization Settings: %s\n", string(org.Settings))
	}

	userPrompt := prompts.DirectionCheckUserPrompt(prompts.DirectionCheckUserPromptData{
		IssueContext: issueContext,
		OrgContext:   orgContext,
		Diff:         diff,
	})

	response, err := s.llm.Complete(ctx, systemPrompt, userPrompt)
	if err != nil {
		return "fail", fmt.Sprintf("LLM direction check error: %s", err.Error()), nil
	}

	return parseLLMCheckResult(response)
}

// checkCorrectness uses the LLM to verify that the diff correctly addresses
// the reported issue.
func (s *Service) checkCorrectness(ctx context.Context, diff string, issue *models.Issue) (string, string, error) {
	if s.llm == nil {
		return "skipped", "LLM client not configured", nil
	}

	systemPrompt := prompts.CorrectnessCheckPrompt()

	issueContext := buildIssueContext(issue)
	if issue != nil {
		issueContext += fmt.Sprintf("Severity: %s\n", issue.Severity)
	}

	userPrompt := prompts.CorrectnessCheckUserPrompt(prompts.CorrectnessCheckUserPromptData{
		IssueContext: issueContext,
		Diff:         diff,
	})

	response, err := s.llm.Complete(ctx, systemPrompt, userPrompt)
	if err != nil {
		return "fail", fmt.Sprintf("LLM correctness check error: %s", err.Error()), nil
	}

	return parseLLMCheckResult(response)
}

// checkRegressionTest uses the LLM to verify that the diff includes
// appropriate regression tests for the fix.
func (s *Service) checkRegressionTest(ctx context.Context, diff string, issue *models.Issue) (string, string, error) {
	if s.llm == nil {
		return "skipped", "LLM client not configured", nil
	}

	systemPrompt := prompts.RegressionCheckPrompt()

	issueContext := buildIssueContext(issue)

	userPrompt := prompts.RegressionCheckUserPrompt(prompts.RegressionCheckUserPromptData{
		IssueContext: issueContext,
		Diff:         diff,
	})

	response, err := s.llm.Complete(ctx, systemPrompt, userPrompt)
	if err != nil {
		return "fail", fmt.Sprintf("LLM regression test check error: %s", err.Error()), nil
	}

	return parseLLMCheckResult(response)
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

// exfiltrationURLRe matches outbound HTTP requests via curl/wget or HTTP client calls.
var exfiltrationURLRe = regexp.MustCompile(`(?i)(curl|wget)\s+["']?https?://([^\s/"']+)`)

// exfiltrationHTTPClientRe matches HTTP client calls to URLs (Go, Python, JS, etc.).
var exfiltrationHTTPClientRe = regexp.MustCompile(`(?i)(http\.(Get|Post|NewRequest|Do)|requests\.(get|post|put|delete|patch)|fetch\(|axios\.(get|post|put|delete|patch)|urllib\.request\.urlopen|httpx\.(get|post|put|delete|patch))[\s(]*["']https?://([^\s/"']+)`)

// allowedExfiltrationDomains are domains that are allowed for outbound HTTP requests.
var allowedExfiltrationDomains = map[string]bool{
	"api.anthropic.com":  true,
	"api.openai.com":     true,
	"api.github.com":     true,
	"registry.npmjs.org": true,
	"pypi.org":           true,
	"proxy.golang.org":   true,
}

// exfiltrationPatterns detect suspicious data exfiltration attempts in diffs.
var exfiltrationPatterns = []*regexp.Regexp{
	// Base64 encoding of environment variables or file contents
	regexp.MustCompile(`(?i)(base64\.encode|btoa|base64\.b64encode)\s*\(.*\b(os\.Getenv|process\.env|open\(|readFile|ReadFile|ReadAll)\b`),
	// Writing environment variables to files
	regexp.MustCompile(`(?i)(os\.Getenv|process\.env\.\w+)\s*.*>>`),
	regexp.MustCompile(`(?i)(WriteFile|write_file|appendFile|write_text).*\b(API_KEY|SECRET|TOKEN|PASSWORD|PRIVATE_KEY)\b`),
	// Subprocess piping data to external tools
	regexp.MustCompile(`(?i)(exec\.Command|subprocess(\.\w+)?|os\.system|child_process)\s*\(.*\b(curl|wget|nc|ncat|netcat)\b`),
	// DNS-based exfiltration via known tools/domains
	regexp.MustCompile(`(?i)\.(burpcollaborator\.net|oastify\.com|interact\.sh|canarytokens\.com)`),
	// Encoding data in DNS queries
	regexp.MustCompile(`(?i)(nslookup|dig|host)\s+.*\$`),
}

// isAllowedExfiltrationDomain checks if a domain is in the allowed list.
// Strips port numbers (e.g. "api.github.com:8080" → "api.github.com").
func isAllowedExfiltrationDomain(domain string) bool {
	host := domain
	if idx := strings.Index(domain, ":"); idx != -1 {
		host = domain[:idx]
	}
	return allowedExfiltrationDomains[host]
}

// checkExfiltrationURLs checks for outbound HTTP requests to non-allowlisted domains.
func checkExfiltrationURLs(addedLines string) []string {
	var findings []string
	for _, re := range []*regexp.Regexp{exfiltrationURLRe, exfiltrationHTTPClientRe} {
		matches := re.FindAllStringSubmatch(addedLines, 100)
		for _, m := range matches {
			domain := m[len(m)-1]
			if !isAllowedExfiltrationDomain(domain) {
				findings = append(findings, fmt.Sprintf("potential exfiltration: outbound request to %s", domain))
			}
		}
	}
	return findings
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

	for _, pattern := range exfiltrationPatterns {
		if matches := pattern.FindAllString(addedLines, -1); len(matches) > 0 {
			findings = append(findings, fmt.Sprintf("potential exfiltration: pattern %s matched", pattern.String()))
		}
	}

	findings = append(findings, checkExfiltrationURLs(addedLines)...)

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

		repoCommands, err := s.repoCICommands(ctx, sandbox)
		if err != nil {
			return "fail", err.Error(), nil
		}

		commands := make([]string, 0, len(repoCommands.dependencies)+len(repoCommands.bootstrap)+1+len(repoCommands.validation))
		commands = append(commands, repoCommands.dependencies...)
		commands = append(commands, repoCommands.bootstrap...)
		commands = append(commands, ci.command)
		commands = append(commands, repoCommands.validation...)
		for _, command := range commands {
			if result, details, ok := s.runCICommand(ctx, sandbox, command); !ok {
				return "fail", details, nil
			} else if result == "fail" {
				return result, details, nil
			}
		}

		return "pass", fmt.Sprintf("CI commands passed: %s", strings.Join(commands, ", ")), nil
	}

	return "pass", "no recognized project type, skipping CI", nil
}

type repoCICommands struct {
	dependencies []string
	bootstrap    []string
	validation   []string
}

func (s *Service) repoCICommands(ctx context.Context, sandbox *agent.Sandbox) (repoCICommands, error) {
	configBytes, err := s.provider.ReadFile(ctx, sandbox, repoconfig.ConfigPath)
	if err != nil {
		return repoCICommands{}, nil
	}

	config, err := repoconfig.Parse(configBytes)
	if err != nil {
		return repoCICommands{}, fmt.Errorf("invalid %s: %w", repoconfig.ConfigPath, err)
	}

	dependencies, err := repoconfig.InstallCommands(config.Dependencies)
	if err != nil {
		return repoCICommands{}, fmt.Errorf("invalid %s: %w", repoconfig.ConfigPath, err)
	}

	return repoCICommands{
		dependencies: dependencies,
		bootstrap:    config.Bootstrap.Commands,
		validation:   config.Validation.Commands,
	}, nil
}

func (s *Service) runCICommand(ctx context.Context, sandbox *agent.Sandbox, command string) (string, string, bool) {
	var stdout, stderr bytes.Buffer
	exitCode, err := s.provider.Exec(ctx, sandbox, command, &stdout, &stderr)
	if err != nil {
		return "fail", fmt.Sprintf("error running %q: %s", command, err.Error()), false
	}

	if exitCode != 0 {
		output := stderr.String()
		if output == "" {
			output = stdout.String()
		}
		if len(output) > 2000 {
			output = output[:2000] + "\n... (truncated)"
		}
		return "fail", fmt.Sprintf("tests failed while running %q (exit code %d): %s", command, exitCode, output), true
	}

	return "pass", "", true
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
