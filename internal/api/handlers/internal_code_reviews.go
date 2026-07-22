package handlers

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/assembledhq/143/internal/auth"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog"
)

// internalCodeReviewTextLimit bounds large free-text fields (agent raw output,
// prompt artifact content) in internal API responses. The sandbox tool client
// caps responses at 2 MiB, and agents read these with a token budget — a
// truncated tail is more useful than a failed call.
const internalCodeReviewTextLimit = 16 * 1024

// InternalCodeReviewHandler exposes past automated code reviews to coding
// agents running inside sandboxes. It mirrors the session-history internal API:
// token-authenticated, scoped to the session's org and repository, and gated by
// the review_feedback capability. Its purpose is policy iteration — agents read
// prior review decisions, findings, and the governing policy versions to judge
// whether the review policy behaves as intended and to propose adjustments.
type InternalCodeReviewHandler struct {
	store         *db.CodeReviewStore
	sessions      internalSessionGetter
	signingSecret string
	audit         *db.AuditEmitter
}

func NewInternalCodeReviewHandler(store *db.CodeReviewStore, sessions internalSessionGetter, signingSecret string) *InternalCodeReviewHandler {
	return &InternalCodeReviewHandler{store: store, sessions: sessions, signingSecret: signingSecret}
}

func (h *InternalCodeReviewHandler) SetAuditEmitter(audit *db.AuditEmitter) { h.audit = audit }

// internalCodeReviewSummary is the compact list row returned to agents. It
// intentionally omits the final review body and output keys to keep list
// responses small; the detail endpoint returns the full record.
type internalCodeReviewSummary struct {
	ID                uuid.UUID                      `json:"id"`
	SessionID         uuid.UUID                      `json:"session_id"`
	RepositoryID      uuid.UUID                      `json:"repository_id"`
	RepositoryName    *string                        `json:"repository_name,omitempty"`
	GitHubRepo        string                         `json:"github_repo"`
	GitHubPRNumber    int                            `json:"github_pr_number"`
	GitHubPRURL       string                         `json:"github_pr_url"`
	PullRequestTitle  string                         `json:"pull_request_title"`
	PullRequestAuthor string                         `json:"pull_request_author"`
	PolicyID          uuid.UUID                      `json:"policy_id"`
	TriggerSource     models.CodeReviewTriggerSource `json:"trigger_source"`
	Status            models.CodeReviewSessionStatus `json:"status"`
	Decision          *models.CodeReviewDecision     `json:"decision,omitempty"`
	Acceptable        *bool                          `json:"acceptable,omitempty"`
	Stale             bool                           `json:"stale"`
	GitHubReviewURL   *string                        `json:"github_review_url,omitempty"`
	FailureReason     *string                        `json:"failure_reason,omitempty"`
	CompletedAt       *time.Time                     `json:"completed_at,omitempty"`
	CreatedAt         time.Time                      `json:"created_at"`
}

// internalCodeReviewAgentResult is a per-reviewer verdict without the raw
// transcript by default; raw output is opt-in and truncated.
type internalCodeReviewAgentResult struct {
	ID               uuid.UUID                          `json:"id"`
	AgentProvider    string                             `json:"agent_provider"`
	AgentModel       *string                            `json:"agent_model,omitempty"`
	Role             models.CodeReviewAgentRole         `json:"role"`
	Status           models.CodeReviewAgentResultStatus `json:"status"`
	StructuredResult json.RawMessage                    `json:"structured_result,omitempty"`
	RawOutput        *string                            `json:"raw_output,omitempty"`
	RawOutputRunes   int                                `json:"raw_output_runes,omitempty"`
	CreatedAt        time.Time                          `json:"created_at"`
}

type internalCodeReviewFinding struct {
	ID                uuid.UUID                          `json:"id"`
	AgentResultID     *uuid.UUID                         `json:"agent_result_id,omitempty"`
	Severity          models.CodeReviewFindingSeverity   `json:"severity"`
	Confidence        models.CodeReviewFindingConfidence `json:"confidence"`
	Path              *string                            `json:"path,omitempty"`
	StartLine         *int                               `json:"start_line,omitempty"`
	EndLine           *int                               `json:"end_line,omitempty"`
	Summary           string                             `json:"summary"`
	Body              string                             `json:"body"`
	SelectedForInline bool                               `json:"selected_for_inline"`
	PostedToGitHub    bool                               `json:"posted_to_github"`
	CreatedAt         time.Time                          `json:"created_at"`
}

type internalCodeReviewPromptArtifact struct {
	ID            uuid.UUID `json:"id"`
	ArtifactKey   string    `json:"artifact_key"`
	Role          string    `json:"role"`
	AgentProvider string    `json:"agent_provider,omitempty"`
	Content       string    `json:"content"`
	ContentRunes  int       `json:"content_runes"`
	CreatedAt     time.Time `json:"created_at"`
}

type internalCodeReviewDetail struct {
	internalCodeReviewSummary
	BaseSHA               string                          `json:"base_sha"`
	HeadSHA               string                          `json:"head_sha"`
	FromFork              bool                            `json:"from_fork"`
	SupersededBySessionID *uuid.UUID                      `json:"superseded_by_session_id,omitempty"`
	FinalReviewBody       *string                         `json:"final_review_body,omitempty"`
	Findings              []internalCodeReviewFinding     `json:"findings"`
	AgentResults          []internalCodeReviewAgentResult `json:"agent_results"`
	// Pointer so a requested-but-empty artifact list serializes as [] while an
	// unrequested one is omitted entirely — a plain slice with omitempty cannot
	// distinguish the two.
	PromptArtifacts *[]internalCodeReviewPromptArtifact `json:"prompt_artifacts,omitempty"`
}

const internalCodeReviewDefaultLimit = 20
const internalCodeReviewMaxLimit = 50

// internalCodeReviewReasonLimit bounds the audit reason on policy updates so
// agent-supplied text cannot bloat audit rows.
const internalCodeReviewReasonLimit = 2000

// List returns past code reviews for the session's repository, newest first.
func (h *InternalCodeReviewHandler) List(w http.ResponseWriter, r *http.Request) {
	claims, ok := h.authorize(w, r)
	if !ok {
		return
	}
	repoID := claims.RepoID
	filters := db.CodeReviewListFilters{
		RepositoryID: &repoID,
		Search:       strings.TrimSpace(r.URL.Query().Get("search")),
		Limit:        internalCodeReviewDefaultLimit,
	}
	if raw := strings.TrimSpace(r.URL.Query().Get("limit")); raw != "" {
		limit, err := strconv.Atoi(raw)
		if err != nil || limit < 1 {
			writeError(w, r, http.StatusBadRequest, "INVALID_LIMIT", "limit must be a positive integer", err)
			return
		}
		if limit > internalCodeReviewMaxLimit {
			limit = internalCodeReviewMaxLimit
		}
		filters.Limit = limit
	}
	if raw := strings.TrimSpace(r.URL.Query().Get("decision")); raw != "" {
		decision := models.CodeReviewDecision(raw)
		if err := decision.Validate(); err != nil {
			writeError(w, r, http.StatusBadRequest, "INVALID_DECISION", "decision must be one of approved, comment_only, needs_human_review, blocked")
			return
		}
		filters.Decision = &decision
	}
	if raw := strings.TrimSpace(r.URL.Query().Get("status")); raw != "" {
		status := models.CodeReviewSessionStatus(raw)
		if err := status.Validate(); err != nil {
			writeError(w, r, http.StatusBadRequest, "INVALID_STATUS", "status must be one of queued, running, completed, failed, stale, cancelled")
			return
		}
		filters.Status = &status
	}
	if raw := strings.TrimSpace(r.URL.Query().Get("outcome")); raw != "" {
		outcome := models.CodeReviewListOutcome(raw)
		if err := outcome.Validate(); err != nil {
			writeError(w, r, http.StatusBadRequest, "INVALID_OUTCOME", "outcome must be automatically_approved or completed_not_approved")
			return
		}
		filters.Outcome = &outcome
	}
	if raw := strings.TrimSpace(r.URL.Query().Get("acceptable")); raw != "" {
		acceptable, err := strconv.ParseBool(raw)
		if err != nil {
			writeError(w, r, http.StatusBadRequest, "INVALID_ACCEPTABLE", "acceptable must be a boolean", err)
			return
		}
		filters.Acceptable = &acceptable
	}
	if raw := strings.TrimSpace(r.URL.Query().Get("created_after")); raw != "" {
		t, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			writeError(w, r, http.StatusBadRequest, "INVALID_CREATED_AFTER", "created_after must be RFC3339", err)
			return
		}
		filters.CreatedAfter = &t
	}
	if raw := strings.TrimSpace(r.URL.Query().Get("created_before")); raw != "" {
		t, err := time.Parse(time.RFC3339, raw)
		if err != nil {
			writeError(w, r, http.StatusBadRequest, "INVALID_CREATED_BEFORE", "created_before must be RFC3339", err)
			return
		}
		filters.CreatedBefore = &t
	}
	if raw := strings.TrimSpace(r.URL.Query().Get("cursor")); raw != "" {
		cursor, err := uuid.Parse(raw)
		if err != nil {
			writeError(w, r, http.StatusBadRequest, "INVALID_CURSOR", "cursor must be a review ID from a previous page", err)
			return
		}
		filters.Cursor = &cursor
	}
	reviews, err := h.store.ListReviews(r.Context(), claims.OrgID, filters)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "CODE_REVIEW_HISTORY_FAILED", "failed to list code reviews", err)
		return
	}
	items := make([]internalCodeReviewSummary, 0, len(reviews))
	for _, review := range reviews {
		items = append(items, internalCodeReviewSummaryFromItem(review))
	}
	next := ""
	if len(items) == filters.Limit && len(items) > 0 {
		next = items[len(items)-1].ID.String()
	}
	writeJSON(w, http.StatusOK, models.ListResponse[internalCodeReviewSummary]{Data: items, Meta: models.PaginationMeta{NextCursor: next}})
}

// Get returns one review with its findings and per-agent verdicts. The review
// is looked up by the code review session ID returned in list rows.
func (h *InternalCodeReviewHandler) Get(w http.ResponseWriter, r *http.Request) {
	claims, ok := h.authorize(w, r)
	if !ok {
		return
	}
	sessionID, err := uuid.Parse(chi.URLParam(r, "session_id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid code review session ID", err)
		return
	}
	includeRawOutput, err := parseOptionalBool(r.URL.Query().Get("include_raw_output"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_INCLUDE_RAW_OUTPUT", "include_raw_output must be a boolean", err)
		return
	}
	includePrompts, err := parseOptionalBool(r.URL.Query().Get("include_prompts"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_INCLUDE_PROMPTS", "include_prompts must be a boolean", err)
		return
	}
	review, err := h.store.GetListItemBySessionID(r.Context(), claims.OrgID, sessionID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, r, http.StatusNotFound, "NOT_FOUND", "code review not found")
			return
		}
		writeError(w, r, http.StatusInternalServerError, "CODE_REVIEW_HISTORY_FAILED", "failed to get code review", err)
		return
	}
	// The internal token is repository-scoped; reviews from sibling repos are
	// invisible rather than forbidden so IDs cannot be probed across repos.
	if review.RepositoryID != claims.RepoID {
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "code review not found")
		return
	}
	findings, err := h.store.ListFindings(r.Context(), claims.OrgID, sessionID, false)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "CODE_REVIEW_HISTORY_FAILED", "failed to list code review findings", err)
		return
	}
	agentResults, err := h.store.ListAgentResults(r.Context(), claims.OrgID, sessionID)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "CODE_REVIEW_HISTORY_FAILED", "failed to list code review agent results", err)
		return
	}
	detail := internalCodeReviewDetail{
		internalCodeReviewSummary: internalCodeReviewSummaryFromItem(review),
		BaseSHA:                   review.BaseSHA,
		HeadSHA:                   review.HeadSHA,
		FromFork:                  review.FromFork,
		SupersededBySessionID:     review.SupersededBySessionID,
		FinalReviewBody:           review.FinalReviewBody,
		Findings:                  make([]internalCodeReviewFinding, 0, len(findings)),
		AgentResults:              make([]internalCodeReviewAgentResult, 0, len(agentResults)),
	}
	for _, finding := range findings {
		detail.Findings = append(detail.Findings, internalCodeReviewFinding{
			ID:                finding.ID,
			AgentResultID:     finding.AgentResultID,
			Severity:          finding.Severity,
			Confidence:        finding.Confidence,
			Path:              finding.Path,
			StartLine:         finding.StartLine,
			EndLine:           finding.EndLine,
			Summary:           finding.Summary,
			Body:              finding.Body,
			SelectedForInline: finding.SelectedForInline,
			PostedToGitHub:    finding.GitHubCommentID != nil,
			CreatedAt:         finding.CreatedAt,
		})
	}
	for _, result := range agentResults {
		item := internalCodeReviewAgentResult{
			ID:            result.ID,
			AgentProvider: result.AgentProvider,
			AgentModel:    result.AgentModel,
			Role:          result.Role,
			Status:        result.Status,
			CreatedAt:     result.CreatedAt,
		}
		if len(result.StructuredResult) > 0 {
			item.StructuredResult = result.StructuredResult
		}
		if result.RawOutput != nil {
			item.RawOutputRunes = utf8.RuneCountInString(*result.RawOutput)
			if includeRawOutput {
				truncated := truncateInternalCodeReviewText(*result.RawOutput)
				item.RawOutput = &truncated
			}
		}
		detail.AgentResults = append(detail.AgentResults, item)
	}
	if includePrompts {
		artifacts, err := h.store.ListPromptArtifacts(r.Context(), claims.OrgID, sessionID)
		if err != nil {
			writeError(w, r, http.StatusInternalServerError, "CODE_REVIEW_HISTORY_FAILED", "failed to list code review prompt artifacts", err)
			return
		}
		items := make([]internalCodeReviewPromptArtifact, 0, len(artifacts))
		for _, artifact := range artifacts {
			items = append(items, internalCodeReviewPromptArtifact{
				ID:            artifact.ID,
				ArtifactKey:   artifact.ArtifactKey,
				Role:          artifact.Role,
				AgentProvider: artifact.AgentProvider,
				Content:       truncateInternalCodeReviewText(artifact.Content),
				ContentRunes:  utf8.RuneCountInString(artifact.Content),
				CreatedAt:     artifact.CreatedAt,
			})
		}
		detail.PromptArtifacts = &items
	}
	writeJSON(w, http.StatusOK, models.SingleResponse[internalCodeReviewDetail]{Data: detail})
}

// Policy returns the active resolved code review policy for the org.
func (h *InternalCodeReviewHandler) Policy(w http.ResponseWriter, r *http.Request) {
	claims, ok := h.authorizePolicyRead(w, r)
	if !ok {
		return
	}
	resolved, err := h.store.ResolvePolicy(r.Context(), claims.OrgID)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "CODE_REVIEW_POLICY_LOAD_FAILED", "failed to load code review policy", err)
		return
	}
	writeJSON(w, http.StatusOK, models.SingleResponse[models.CodeReviewResolvedPolicy]{Data: resolved})
}

// UpdatePolicy saves a new version of the org's code review policy on behalf
// of a sandbox agent. Supplied config keys are merged onto the active resolved
// config, so an agent can adjust one knob without restating the whole policy.
// The write is optimistic-concurrency guarded: expected_version must match the
// active version (0 when the org has never saved one) or the call fails with
// 409 and the current version, forcing the agent to re-read before retrying.
// Every change is a new immutable version with a required reason, audited as a
// system action, so humans can inspect and roll back agent edits.
func (h *InternalCodeReviewHandler) UpdatePolicy(w http.ResponseWriter, r *http.Request) {
	claims, ok := h.authorizeWrite(w, r)
	if !ok {
		return
	}
	var req struct {
		Config          json.RawMessage `json:"config"`
		ExpectedVersion *int            `json:"expected_version"`
		Reason          string          `json:"reason"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_BODY", "invalid request body", err)
		return
	}
	var supplied map[string]json.RawMessage
	if len(req.Config) == 0 || json.Unmarshal(req.Config, &supplied) != nil || len(supplied) == 0 {
		writeError(w, r, http.StatusBadRequest, "INVALID_CONFIG", "config must be a JSON object with at least one policy field")
		return
	}
	if req.ExpectedVersion == nil || *req.ExpectedVersion < 0 {
		writeError(w, r, http.StatusBadRequest, "EXPECTED_VERSION_REQUIRED", "expected_version must be the active policy version (0 if the org has never saved a policy)")
		return
	}
	reason := strings.TrimSpace(req.Reason)
	if reason == "" {
		writeError(w, r, http.StatusBadRequest, "REASON_REQUIRED", "reason is required so humans can audit why the policy changed")
		return
	}
	if utf8.RuneCountInString(reason) > internalCodeReviewReasonLimit {
		writeError(w, r, http.StatusBadRequest, "REASON_TOO_LONG", "reason must be at most 2000 characters")
		return
	}
	current, err := h.store.ResolvePolicy(r.Context(), claims.OrgID)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "CODE_REVIEW_POLICY_LOAD_FAILED", "failed to load code review policy", err)
		return
	}
	// Overlay the supplied keys onto the active config: unmarshal into a copy
	// pre-populated with current values, so absent fields keep their setting
	// instead of resetting to zero values. json.Unmarshal merges nested struct
	// sections field-wise the same way (only supplied keys change); JSON
	// arrays are the exception and replace the current array wholesale.
	merged := current.Config
	if err := json.Unmarshal(req.Config, &merged); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_CONFIG", "invalid policy config", err)
		return
	}
	record, err := h.store.SavePolicyExpectingVersion(r.Context(), claims.OrgID, merged, *req.ExpectedVersion, nil)
	if err != nil {
		if errors.Is(err, db.ErrCodeReviewPolicyVersionConflict) {
			currentVersion := 0
			if fresh, freshErr := h.store.ResolvePolicy(r.Context(), claims.OrgID); freshErr == nil && fresh.Policy != nil {
				currentVersion = fresh.Policy.Version
			}
			writeErrorWithDetails(w, r, http.StatusConflict, "CODE_REVIEW_POLICY_VERSION_CONFLICT",
				"the policy changed since it was read; fetch it again and retry with the current version",
				map[string]int{"current_version": currentVersion}, err)
			return
		}
		var validationErr *models.CodeReviewPolicyValidationError
		if errors.As(err, &validationErr) {
			writeErrorWithDetails(w, r, http.StatusBadRequest, "CODE_REVIEW_POLICY_INVALID", "invalid code review policy", map[string]string{"field": validationErr.Field}, err)
			return
		}
		writeError(w, r, http.StatusBadRequest, "CODE_REVIEW_POLICY_INVALID", "invalid code review policy", err)
		return
	}
	writeJSON(w, http.StatusOK, models.SingleResponse[models.CodeReviewPolicyRecord]{Data: record})
	h.emitPolicyAudit(r, claims, record, reason, *req.ExpectedVersion)
}

func (h *InternalCodeReviewHandler) emitPolicyAudit(r *http.Request, claims *auth.InternalTokenClaims, record models.CodeReviewPolicyRecord, reason string, expectedVersion int) {
	if h.audit == nil {
		return
	}
	resourceID := record.ID.String()
	details := marshalAuditDetails(*zerolog.Ctx(r.Context()), map[string]any{
		"source":                          "agent_tool",
		"tool_name":                       "code_review_history_update_policy",
		"session_id":                      claims.SessionID.String(),
		"reason":                          reason,
		"version":                         record.Version,
		"expected_version":                expectedVersion,
		"review_instructions_runes":       utf8.RuneCountInString(record.ReviewInstructions),
		"automated_approval_policy_runes": utf8.RuneCountInString(record.AutomatedApprovalPolicy),
	})
	h.audit.EmitSystemAction(r.Context(), db.SystemActionParams{
		OrgID:        claims.OrgID,
		ActorID:      "agent_tool",
		Action:       models.AuditActionCodeReviewPolicyUpdated,
		ResourceType: models.AuditResourceCodeReviewPolicy,
		ResourceID:   &resourceID,
		Details:      details,
		SessionID:    claims.SessionID,
	})
}

// PolicyByID returns one historical policy version so agents can compare the
// policy that governed an old review against the active one.
func (h *InternalCodeReviewHandler) PolicyByID(w http.ResponseWriter, r *http.Request) {
	claims, ok := h.authorizePolicyRead(w, r)
	if !ok {
		return
	}
	policyID, err := uuid.Parse(chi.URLParam(r, "policy_id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid policy ID", err)
		return
	}
	record, err := h.store.GetPolicyByID(r.Context(), claims.OrgID, policyID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, r, http.StatusNotFound, "NOT_FOUND", "code review policy not found")
			return
		}
		writeError(w, r, http.StatusInternalServerError, "CODE_REVIEW_POLICY_LOAD_FAILED", "failed to load code review policy", err)
		return
	}
	writeJSON(w, http.StatusOK, models.SingleResponse[models.CodeReviewPolicyRecord]{Data: record})
}

func (h *InternalCodeReviewHandler) authorize(w http.ResponseWriter, r *http.Request) (*auth.InternalTokenClaims, bool) {
	claims, session, ok := authorizeInternalSession(w, r, h.signingSecret, h.sessions)
	if !ok {
		return nil, false
	}
	if !sessionHasCapability(session.CapabilitySnapshot, models.AgentCapabilityReviewFeedback) {
		writeError(w, r, http.StatusForbidden, "CAPABILITY_DENIED", "review_feedback is not enabled for this agent run")
		return nil, false
	}
	return claims, true
}

// authorizePolicyRead admits either capability to the policy read endpoints:
// review_feedback (the default-on history read grant) or
// code_review_policy_management at any level. Without the latter, an org that
// granted only the write capability would have an agent able to update the
// policy but unable to read it — and so unable to learn the expected_version
// its own update requires.
func (h *InternalCodeReviewHandler) authorizePolicyRead(w http.ResponseWriter, r *http.Request) (*auth.InternalTokenClaims, bool) {
	claims, session, ok := authorizeInternalSession(w, r, h.signingSecret, h.sessions)
	if !ok {
		return nil, false
	}
	if !sessionHasCapability(session.CapabilitySnapshot, models.AgentCapabilityReviewFeedback) &&
		!sessionHasCapability(session.CapabilitySnapshot, models.AgentCapabilityCodeReviewPolicy) {
		writeError(w, r, http.StatusForbidden, "CAPABILITY_DENIED", "review_feedback is not enabled for this agent run")
		return nil, false
	}
	return claims, true
}

// authorizeWrite gates the policy-mutation path on the dedicated write
// capability. Unlike read access (review_feedback, in the recommended
// defaults), code_review_policy_management is default-off and org-scoped:
// it must be granted explicitly through a capability policy or approved at
// runtime via `143-tools capability request`.
func (h *InternalCodeReviewHandler) authorizeWrite(w http.ResponseWriter, r *http.Request) (*auth.InternalTokenClaims, bool) {
	claims, session, ok := authorizeInternalSession(w, r, h.signingSecret, h.sessions)
	if !ok {
		return nil, false
	}
	if !sessionHasCapabilityAtLeast(session.CapabilitySnapshot, models.AgentCapabilityCodeReviewPolicy, models.AgentCapabilityAccessWrite) {
		writeError(w, r, http.StatusForbidden, "CAPABILITY_DENIED", "code_review_policy_management write access is not enabled for this agent run; request it with `143-tools capability request`")
		return nil, false
	}
	return claims, true
}

func sessionHasCapabilityAtLeast(snapshot []models.AgentCapabilitySnapshotItem, id models.AgentCapabilityID, level models.AgentCapabilityAccessLevel) bool {
	for _, item := range snapshot {
		if item.ID == id && level.AtMost(item.AccessLevel) {
			return true
		}
	}
	return false
}

func internalCodeReviewSummaryFromItem(item models.CodeReviewListItem) internalCodeReviewSummary {
	return internalCodeReviewSummary{
		ID:                item.ID,
		SessionID:         item.SessionID,
		RepositoryID:      item.RepositoryID,
		RepositoryName:    item.RepositoryName,
		GitHubRepo:        item.GitHubRepo,
		GitHubPRNumber:    item.GitHubPRNumber,
		GitHubPRURL:       item.GitHubPRURL,
		PullRequestTitle:  item.PullRequestTitle,
		PullRequestAuthor: item.PullRequestAuthor,
		PolicyID:          item.PolicyID,
		TriggerSource:     item.TriggerSource,
		Status:            item.Status,
		Decision:          item.Decision,
		Acceptable:        item.Acceptable,
		Stale:             item.Stale,
		GitHubReviewURL:   item.GitHubReviewURL,
		FailureReason:     item.FailureReason,
		CompletedAt:       item.CompletedAt,
		CreatedAt:         item.CreatedAt,
	}
}

// truncateInternalCodeReviewText caps text at internalCodeReviewTextLimit
// runes. It walks rune boundaries instead of materializing a []rune so
// multi-megabyte agent transcripts don't allocate 4 bytes per rune just to be
// cut down to 16K.
func truncateInternalCodeReviewText(text string) string {
	count := 0
	for i := range text {
		if count == internalCodeReviewTextLimit {
			return text[:i] + "\n...(truncated)"
		}
		count++
	}
	return text
}
