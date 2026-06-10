package handlers

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/assembledhq/143/internal/auth"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/integration"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog"
)

type InternalEvalHandler struct {
	bootstrapStore *db.EvalBootstrapStore
	sessionStore   internalSessionLookup
	signingSecret  string
	logger         zerolog.Logger
}

func NewInternalEvalHandler(bootstrapStore *db.EvalBootstrapStore, sessionStore internalSessionLookup, signingSecret string, logger zerolog.Logger) *InternalEvalHandler {
	return &InternalEvalHandler{
		bootstrapStore: bootstrapStore,
		sessionStore:   sessionStore,
		signingSecret:  signingSecret,
		logger:         logger,
	}
}

func (h *InternalEvalHandler) AddCandidate(w http.ResponseWriter, r *http.Request) {
	scope, ok := h.authorizeEvalBootstrapTool(w, r)
	if !ok {
		return
	}
	if runIDParam := chi.URLParam(r, "bootstrap_run_id"); runIDParam != "" {
		runID, err := uuid.Parse(runIDParam)
		if err != nil {
			writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid bootstrap_run_id", err)
			return
		}
		if runID != scope.BootstrapRunID {
			writeError(w, r, http.StatusForbidden, "FORBIDDEN", "sandbox token is not authorized for this bootstrap run")
			return
		}
	}
	var params integration.AddEvalCandidateParams
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&params); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_BODY", "invalid request body", err)
		return
	}
	warnings := buildEvalCandidateWarnings(params)
	params.Warnings = warnings
	candidatePayload, err := evalCandidatePayload(params)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_CANDIDATE", err.Error(), err)
		return
	}
	row := models.EvalBootstrapCandidateRow{
		OrgID:             scope.OrgID,
		BootstrapRunID:    scope.BootstrapRunID,
		PRNumber:          params.PRNumber,
		PRTitle:           strings.TrimSpace(params.PRTitle),
		BaseCommitSHA:     strings.TrimSpace(params.BaseCommitSHA),
		SolutionCommitSHA: strings.TrimSpace(params.SolutionCommitSHA),
		SolutionDiff:      params.SolutionDiff,
		IssueDescription:  strings.TrimSpace(params.IssueDescription),
		ScoringCriteria:   params.ScoringCriteria,
		Complexity:        models.EvalComplexity(params.Complexity),
		FitnessScore:      params.FitnessScore,
		FitnessReasoning:  strings.TrimSpace(params.FitnessReasoning),
		Evidence:          json.RawMessage(`{}`),
		Warnings:          warnings,
		Payload:           candidatePayload,
		CreatedByTool:     "eval_add",
	}
	if len(params.Evidence) > 0 {
		row.Evidence = params.Evidence
	}
	if err := h.bootstrapStore.CreateCandidate(r.Context(), &row); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, r, http.StatusConflict, "BOOTSTRAP_NOT_RUNNING", "bootstrap run is not running")
			return
		}
		writeError(w, r, http.StatusInternalServerError, "CREATE_CANDIDATE_FAILED", "failed to add eval candidate", err)
		return
	}
	result := integration.AddEvalCandidateResult{
		CandidateID:    row.ID.String(),
		CandidateIndex: row.CandidateIndex,
		BootstrapRunID: row.BootstrapRunID.String(),
		Status:         string(row.Status),
	}
	writeJSON(w, http.StatusCreated, models.SingleResponse[integration.AddEvalCandidateResult]{Data: result})
}

type evalBootstrapToolScope struct {
	OrgID          uuid.UUID
	BootstrapRunID uuid.UUID
}

func (h *InternalEvalHandler) authorizeEvalBootstrapTool(w http.ResponseWriter, r *http.Request) (evalBootstrapToolScope, bool) {
	tokenStr := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if tokenStr == "" {
		writeError(w, r, http.StatusUnauthorized, "INVALID_SANDBOX_TOKEN", "missing sandbox token")
		return evalBootstrapToolScope{}, false
	}
	claims, err := auth.ValidateInternalToken(h.signingSecret, tokenStr)
	if err != nil {
		writeError(w, r, http.StatusUnauthorized, "INVALID_SANDBOX_TOKEN", "invalid sandbox token", err)
		return evalBootstrapToolScope{}, false
	}
	if claims.SessionID == nil || *claims.SessionID == uuid.Nil {
		writeError(w, r, http.StatusUnauthorized, "INVALID_SANDBOX_TOKEN", "sandbox token is not scoped to a session")
		return evalBootstrapToolScope{}, false
	}
	if claims.ThreadID == nil || *claims.ThreadID == uuid.Nil {
		writeError(w, r, http.StatusUnauthorized, "INVALID_SANDBOX_TOKEN", "sandbox token is not scoped to an eval bootstrap thread")
		return evalBootstrapToolScope{}, false
	}
	if claims.SessionOrigin != string(models.SessionOriginEvalBootstrap) || !hasInternalToolScope(claims.AllowedToolScopes, "eval:add") {
		writeError(w, r, http.StatusForbidden, "EVAL_TOOL_NOT_AVAILABLE", "sandbox token does not allow eval:add")
		return evalBootstrapToolScope{}, false
	}
	session, err := h.sessionStore.GetByID(r.Context(), claims.OrgID, *claims.SessionID)
	if err != nil {
		h.logger.Warn().Err(err).Str("session_id", claims.SessionID.String()).Msg("session lookup failed during eval-tool auth")
		writeError(w, r, http.StatusUnauthorized, "INVALID_SANDBOX_TOKEN", "sandbox token is not authorized for this session")
		return evalBootstrapToolScope{}, false
	}
	if session.RepositoryID == nil || *session.RepositoryID != claims.RepoID {
		writeError(w, r, http.StatusUnauthorized, "INVALID_SANDBOX_TOKEN", "sandbox token is not authorized for this repository")
		return evalBootstrapToolScope{}, false
	}
	if session.Origin != models.SessionOriginEvalBootstrap {
		writeError(w, r, http.StatusForbidden, "EVAL_TOOL_NOT_AVAILABLE", "eval tools are only available to eval bootstrap sessions")
		return evalBootstrapToolScope{}, false
	}
	run, err := h.bootstrapStore.GetBySessionThread(r.Context(), claims.OrgID, *claims.SessionID, *claims.ThreadID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, r, http.StatusForbidden, "EVAL_TOOL_NOT_AVAILABLE", "no eval bootstrap run is attached to this session thread")
			return evalBootstrapToolScope{}, false
		}
		writeError(w, r, http.StatusInternalServerError, "BOOTSTRAP_LOOKUP_FAILED", "failed to authorize eval bootstrap tool", err)
		return evalBootstrapToolScope{}, false
	}
	if claims.EvalBootstrapRunID == nil || *claims.EvalBootstrapRunID != run.ID {
		writeError(w, r, http.StatusForbidden, "EVAL_TOOL_NOT_AVAILABLE", "sandbox token is not scoped to this eval bootstrap run")
		return evalBootstrapToolScope{}, false
	}
	if run.Status != models.EvalBootstrapStatusRunning {
		writeError(w, r, http.StatusConflict, "BOOTSTRAP_NOT_RUNNING", "bootstrap run is not running")
		return evalBootstrapToolScope{}, false
	}
	return evalBootstrapToolScope{OrgID: claims.OrgID, BootstrapRunID: run.ID}, true
}

func hasInternalToolScope(scopes []string, target string) bool {
	for _, scope := range scopes {
		if scope == target {
			return true
		}
	}
	return false
}

func evalCandidatePayload(params integration.AddEvalCandidateParams) (json.RawMessage, error) {
	if params.PRNumber <= 0 {
		return nil, errors.New("pr_number is required")
	}
	if strings.TrimSpace(params.PRTitle) == "" {
		return nil, errors.New("pr_title is required")
	}
	if strings.TrimSpace(params.BaseCommitSHA) == "" {
		return nil, errors.New("base_commit_sha is required")
	}
	if strings.TrimSpace(params.SolutionCommitSHA) == "" {
		return nil, errors.New("solution_commit_sha is required")
	}
	if strings.TrimSpace(params.SolutionDiff) == "" {
		return nil, errors.New("solution_diff is required")
	}
	if strings.TrimSpace(params.IssueDescription) == "" {
		return nil, errors.New("issue_description is required")
	}
	if len(params.ScoringCriteria) == 0 {
		return nil, errors.New("scoring_criteria is required")
	}
	var criteria []models.ScoringCriterion
	if err := json.Unmarshal(params.ScoringCriteria, &criteria); err != nil {
		return nil, errors.New("scoring_criteria must be a valid JSON array")
	}
	for _, c := range criteria {
		if err := c.GraderType.Validate(); err != nil {
			return nil, errors.New("scoring_criteria contains an invalid grader_type")
		}
	}
	complexity := models.EvalComplexity(params.Complexity)
	if err := complexity.Validate(); err != nil {
		return nil, errors.New("complexity must be trivial, simple, moderate, or complex")
	}
	if params.FitnessScore < 0 || params.FitnessScore > 1 {
		return nil, errors.New("fitness_score must be between 0 and 1")
	}
	if strings.TrimSpace(params.FitnessReasoning) == "" {
		return nil, errors.New("fitness_reasoning is required")
	}
	payload := map[string]any{
		"pr_number":           params.PRNumber,
		"pr_title":            strings.TrimSpace(params.PRTitle),
		"base_commit_sha":     strings.TrimSpace(params.BaseCommitSHA),
		"solution_commit_sha": strings.TrimSpace(params.SolutionCommitSHA),
		"solution_diff":       params.SolutionDiff,
		"issue_description":   strings.TrimSpace(params.IssueDescription),
		"scoring_criteria":    criteria,
		"complexity":          complexity,
		"fitness_score":       params.FitnessScore,
		"fitness_reasoning":   strings.TrimSpace(params.FitnessReasoning),
	}
	if len(params.Evidence) > 0 {
		var evidence any
		if err := json.Unmarshal(params.Evidence, &evidence); err != nil {
			return nil, errors.New("evidence must be valid JSON")
		}
		payload["evidence"] = evidence
	}
	if len(params.Warnings) > 0 {
		payload["warnings"] = params.Warnings
	}
	structuredWarnings := buildEvalCandidateValidationWarnings(params)
	if len(structuredWarnings) > 0 {
		payload["validation_warnings"] = structuredWarnings
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	return raw, nil
}

func buildEvalCandidateWarnings(params integration.AddEvalCandidateParams) []string {
	warnings := append([]string{}, params.Warnings...)
	add := func(code string) {
		for _, existing := range warnings {
			if existing == code {
				return
			}
		}
		warnings = append(warnings, code)
	}

	var criteria []models.ScoringCriterion
	if len(params.ScoringCriteria) > 0 {
		_ = json.Unmarshal(params.ScoringCriteria, &criteria)
	}
	hasCodeCheck := false
	for _, criterion := range criteria {
		if criterion.GraderType == models.GraderTypeCodeCheck {
			hasCodeCheck = true
			var cfg models.CodeCheckConfig
			if len(criterion.GraderConfig) > 0 && json.Unmarshal(criterion.GraderConfig, &cfg) == nil {
				command := strings.ToLower(cfg.Command)
				if strings.Contains(command, "sleep ") || strings.Contains(command, "timeout ") || strings.Contains(command, "curl ") {
					add("flaky_command_pattern")
				}
			}
		}
	}
	if !hasCodeCheck {
		add("missing_deterministic_check")
	}

	evidence := evalCandidateEvidence{}
	if len(params.Evidence) > 0 {
		_ = json.Unmarshal(params.Evidence, &evidence)
	}
	hasTestCommand := len(evidence.TestCommands) > 0 || strings.TrimSpace(evidence.TestCommand) != ""
	if !hasTestCommand && !hasCodeCheck {
		add("missing_test_command")
	}
	changedFiles := evidence.ChangedFiles
	if len(changedFiles) == 0 {
		changedFiles = evidence.ChangedFileMS
	}
	if docsOnlyCandidate(changedFiles, params.SolutionDiff) {
		add("docs_only")
	}
	if strings.TrimSpace(params.SolutionDiff) == "" {
		add("missing_solution_diff")
	} else if strings.Count(params.SolutionDiff, "\n") > 1200 {
		add("large_diff")
	}
	if len(strings.Fields(params.IssueDescription)) < 8 {
		add("weak_prompt")
	}

	return warnings
}

func buildEvalCandidateValidationWarnings(params integration.AddEvalCandidateParams) []models.EvalValidationWarning {
	codes := buildEvalCandidateWarnings(params)
	warnings := make([]models.EvalValidationWarning, 0, len(codes))
	for _, code := range codes {
		warnings = append(warnings, evalValidationWarningForCode(code))
	}
	return warnings
}

func evalValidationWarningForCode(code string) models.EvalValidationWarning {
	switch code {
	case "missing_deterministic_check":
		return models.EvalValidationWarning{Code: code, Severity: "warning", Message: "No deterministic grader is configured.", Suggestion: "Add a code_check criterion that runs the smallest reliable test or verification command.", Blocking: false}
	case "missing_test_command":
		return models.EvalValidationWarning{Code: code, Severity: "warning", Message: "No test command was provided in evidence.", Suggestion: "Include the exact command that fails before the fix and passes after it.", Blocking: false}
	case "docs_only":
		return models.EvalValidationWarning{Code: code, Severity: "info", Message: "The candidate appears to touch only documentation paths.", Suggestion: "Confirm this is intended; docs-only evals are usually weaker coding-regression checks.", Blocking: false}
	case "weak_prompt":
		return models.EvalValidationWarning{Code: code, Severity: "warning", Message: "The task prompt is very short.", Suggestion: "Expand the prompt so an evaluated agent has enough context without seeing the solution diff.", Blocking: false}
	case "large_diff":
		return models.EvalValidationWarning{Code: code, Severity: "warning", Message: "The solution diff is large.", Suggestion: "Prefer a narrower task or split this into smaller evals with targeted graders.", Blocking: false}
	case "missing_solution_diff":
		return models.EvalValidationWarning{Code: code, Severity: "error", Message: "The candidate is missing a solution diff.", Suggestion: "Provide the known-good diff so reviewers can judge leakage and rubric quality.", Blocking: true}
	case "flaky_command_pattern":
		return models.EvalValidationWarning{Code: code, Severity: "warning", Message: "A code_check command looks timing- or network-sensitive.", Suggestion: "Replace sleeps, broad timeouts, or network calls with deterministic local checks where possible.", Blocking: false}
	default:
		return models.EvalValidationWarning{Code: code, Severity: "info", Message: code, Blocking: false}
	}
}

type evalCandidateEvidence struct {
	ChangedFiles  []string `json:"changed_files"`
	TestCommands  []string `json:"test_commands"`
	TestCommand   string   `json:"test_command"`
	ChangedFile   string   `json:"changed_file"`
	ChangedFileMS []string `json:"changed_file_paths"`
}

func docsOnlyCandidate(changedFiles []string, diff string) bool {
	if len(changedFiles) == 0 {
		changedFiles = changedFilesFromDiff(diff)
	}
	if len(changedFiles) == 0 {
		return false
	}
	for _, file := range changedFiles {
		if !isDocPath(file) {
			return false
		}
	}
	return true
}

func changedFilesFromDiff(diff string) []string {
	var files []string
	for _, line := range strings.Split(diff, "\n") {
		if !strings.HasPrefix(line, "diff --git ") {
			continue
		}
		// "diff --git a/<path> b/<path>" — both paths are identical.
		// Scan for " b/" from right to left; for each candidate verify that the
		// extracted suffix, when used as the path, reconstructs the full line.
		// This handles both spaces in filenames and paths containing " b/".
		body := strings.TrimPrefix(line, "diff --git ")
		searchEnd := len(body)
		for searchEnd > 0 {
			idx := strings.LastIndex(body[:searchEnd], " b/")
			if idx < 0 {
				break
			}
			candidate := body[idx+3:]
			if body == "a/"+candidate+" b/"+candidate {
				files = append(files, candidate)
				break
			}
			searchEnd = idx
		}
	}
	return files
}

func isDocPath(path string) bool {
	p := strings.ToLower(strings.TrimSpace(path))
	if p == "" {
		return false
	}
	if strings.HasPrefix(p, "docs/") || strings.Contains(p, "/docs/") {
		return true
	}
	switch {
	case strings.HasSuffix(p, ".md"),
		strings.HasSuffix(p, ".mdx"),
		strings.HasSuffix(p, ".rst"),
		strings.HasSuffix(p, ".txt"),
		strings.HasSuffix(p, ".adoc"):
		return true
	default:
		return false
	}
}
