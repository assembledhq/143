package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/api/sse"
	"github.com/assembledhq/143/internal/cache"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/metrics"
	"github.com/assembledhq/143/internal/models"
	codereviewsvc "github.com/assembledhq/143/internal/services/codereview"
	ghservice "github.com/assembledhq/143/internal/services/github"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog"
)

var (
	errCodeReviewStreamOrgInvalid   = errors.New("invalid code review stream org")
	errCodeReviewStreamOrgForbidden = errors.New("forbidden code review stream org")
	errCodeReviewStreamUnauthorized = errors.New("unauthorized code review stream request")
)

type codeReviewMembershipStore interface {
	Get(ctx context.Context, userID, orgID uuid.UUID) (models.OrganizationMembership, error)
}

type codeReviewRetryService interface {
	RetryReview(ctx context.Context, input codereviewsvc.RetryReviewInput) (codereviewsvc.RetryReviewResult, error)
}

type CodeReviewHandler struct {
	store        *db.CodeReviewStore
	repos        *db.RepositoryStore
	triggerSetup *codereviewsvc.GitHubTriggerSetupService
	streams      *cache.CodeReviewStreams
	audit        *db.AuditEmitter
	memberships  codeReviewMembershipStore
	retryService codeReviewRetryService
}

func (h *CodeReviewHandler) SetAuditEmitter(audit *db.AuditEmitter) { h.audit = audit }

func NewCodeReviewHandler(store *db.CodeReviewStore, repos *db.RepositoryStore) *CodeReviewHandler {
	return &CodeReviewHandler{store: store, repos: repos}
}

func (h *CodeReviewHandler) SetGitHubTriggerSetupService(service *codereviewsvc.GitHubTriggerSetupService) {
	h.triggerSetup = service
}

func (h *CodeReviewHandler) SetStreams(streams *cache.CodeReviewStreams) {
	h.streams = streams
}

func (h *CodeReviewHandler) SetMembershipStore(store codeReviewMembershipStore) {
	h.memberships = store
}

func (h *CodeReviewHandler) SetRetryService(service codeReviewRetryService) {
	h.retryService = service
}

// StreamUpdates is the org-scoped SSE endpoint backing the live code reviews
// list. It mirrors PullRequestHandler.StreamUpdates: subscribe to the org's
// Redis channel, heartbeat every 15s, and forward each lifecycle event as a
// named "code_review.updated" SSE event.
func (h *CodeReviewHandler) StreamUpdates(w http.ResponseWriter, r *http.Request) {
	if h.streams == nil || !h.streams.Available() {
		http.Error(w, "code review streams unavailable", http.StatusServiceUnavailable)
		return
	}
	_ = middleware.OrgIDFromContext(r.Context())

	sw := sse.NewWriter(w)
	if sw == nil {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	orgID, err := h.streamOrgIDFromRequest(r)
	if err != nil {
		switch {
		case errors.Is(err, errCodeReviewStreamOrgInvalid):
			http.Error(w, "invalid code review stream org", http.StatusBadRequest)
		case errors.Is(err, errCodeReviewStreamOrgForbidden):
			http.Error(w, "forbidden code review stream org", http.StatusForbidden)
		case errors.Is(err, errCodeReviewStreamUnauthorized):
			http.Error(w, "unauthorized", http.StatusUnauthorized)
		default:
			http.Error(w, "failed to authorize code review stream", http.StatusInternalServerError)
		}
		return
	}
	sub, err := h.streams.Subscribe(orgID)
	if err != nil {
		http.Error(w, "code review streams unavailable", http.StatusServiceUnavailable)
		return
	}
	defer sub.Close()

	logger := zerolog.Ctx(r.Context())
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-heartbeat.C:
			if err := sw.WriteHeartbeat(); err != nil {
				logger.Warn().Err(err).Msg("failed to write code review stream heartbeat")
				return
			}
			sw.Flush()
		case event, ok := <-sub.C:
			if !ok {
				logger.Warn().Str("reason", sub.CloseReason()).Msg("code review update subscription closed")
				return
			}
			if err := sw.WriteEvent(sse.EventType("code_review.updated"), event); err != nil {
				logEvent := logger.Warn().Err(err).Str("org_id", event.OrgID.String())
				if event.SessionID != nil {
					logEvent = logEvent.Str("session_id", event.SessionID.String())
				}
				logEvent.Msg("failed to write code review update event")
				return
			}
			sw.Flush()
		}
	}
}

func (h *CodeReviewHandler) streamOrgIDFromRequest(r *http.Request) (uuid.UUID, error) {
	orgID := middleware.OrgIDFromContext(r.Context())
	requestedRaw := strings.TrimSpace(r.URL.Query().Get("org_id"))
	if requestedRaw == "" {
		return orgID, nil
	}

	requestedOrgID, err := uuid.Parse(requestedRaw)
	if err != nil {
		return uuid.Nil, errCodeReviewStreamOrgInvalid
	}
	if requestedOrgID == orgID {
		return requestedOrgID, nil
	}

	user := middleware.UserFromContext(r.Context())
	if user == nil {
		return uuid.Nil, errCodeReviewStreamUnauthorized
	}
	if h.memberships == nil {
		return uuid.Nil, errors.New("membership store not configured")
	}
	if _, err := h.memberships.Get(r.Context(), user.ID, requestedOrgID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return uuid.Nil, errCodeReviewStreamOrgForbidden
		}
		return uuid.Nil, err
	}

	return requestedOrgID, nil
}

func (h *CodeReviewHandler) List(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	repositoryID, ok := parseOptionalUUIDQuery(w, r, "repository_id")
	if !ok {
		return
	}
	limit := 50
	if raw := r.URL.Query().Get("limit"); raw != "" {
		parsed, err := strconv.Atoi(raw)
		if err != nil || parsed < 1 || parsed > 100 {
			writeError(w, r, http.StatusBadRequest, "INVALID_LIMIT", "limit must be between 1 and 100")
			return
		}
		limit = parsed
	}
	filters := db.CodeReviewListFilters{
		RepositoryID: repositoryID,
		Search:       strings.TrimSpace(r.URL.Query().Get("search")),
		Limit:        limit,
	}
	if raw := strings.TrimSpace(r.URL.Query().Get("decision")); raw != "" {
		decision := models.CodeReviewDecision(raw)
		if err := decision.Validate(); err != nil {
			writeError(w, r, http.StatusBadRequest, "INVALID_DECISION", "invalid decision")
			return
		}
		filters.Decision = &decision
	}
	if raw := strings.TrimSpace(r.URL.Query().Get("outcome")); raw != "" {
		outcome := models.CodeReviewListOutcome(raw)
		if err := outcome.Validate(); err != nil {
			writeError(w, r, http.StatusBadRequest, "INVALID_OUTCOME", "invalid outcome")
			return
		}
		filters.Outcome = &outcome
	}
	if raw := strings.TrimSpace(r.URL.Query().Get("status")); raw != "" {
		status := models.CodeReviewSessionStatus(raw)
		if err := status.Validate(); err != nil {
			writeError(w, r, http.StatusBadRequest, "INVALID_STATUS", "invalid status")
			return
		}
		filters.Status = &status
	}
	if raw := strings.TrimSpace(r.URL.Query().Get("risk")); raw != "" {
		switch raw {
		case "acceptable":
			acceptable := true
			filters.Acceptable = &acceptable
		case "needs_review":
			acceptable := false
			filters.Acceptable = &acceptable
		default:
			writeError(w, r, http.StatusBadRequest, "INVALID_RISK", "risk must be acceptable or needs_review")
			return
		}
	}
	reviews, err := h.store.ListReviews(r.Context(), orgID, filters)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "CODE_REVIEWS_LOAD_FAILED", "failed to load code reviews", err)
		return
	}
	writeJSON(w, http.StatusOK, models.ListResponse[models.CodeReviewListItem]{Data: reviews})
}

func (h *CodeReviewHandler) Templates(w http.ResponseWriter, r *http.Request) {
	_ = middleware.OrgIDFromContext(r.Context())
	writeJSON(w, http.StatusOK, models.ListResponse[models.CodeReviewTemplateOption]{Data: models.CodeReviewPolicyTemplates()})
}

func (h *CodeReviewHandler) PromptExamples(w http.ResponseWriter, r *http.Request) {
	_ = middleware.OrgIDFromContext(r.Context())
	writeJSON(w, http.StatusOK, models.SingleResponse[models.CodeReviewPromptExamplesResponse]{Data: models.CodeReviewPromptExamplesResponse{
		ReviewInstructions: models.CodeReviewPromptExamples(), AutomatedApprovalPolicies: models.CodeReviewAutomatedApprovalExamples(),
	}})
}

func (h *CodeReviewHandler) PolicyEvent(w http.ResponseWriter, r *http.Request) {
	_ = middleware.OrgIDFromContext(r.Context())
	var req struct {
		Event           string `json:"event"`
		Scope           string `json:"scope"`
		Source          string `json:"source"`
		ExampleKey      string `json:"example_key"`
		CharacterBucket string `json:"character_bucket"`
		Subsection      string `json:"subsection"`
		Configured      bool   `json:"configured"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_BODY", "invalid event body")
		return
	}
	allowed := map[string]bool{"code_review_policy_viewed": true, "code_review_prompt_edited": true, "code_review_prompt_example_previewed": true, "code_review_prompt_example_applied": true, "code_review_advanced_opened": true, "code_review_policy_enabled": true, "code_review_approval_mode_changed": true, "code_review_github_setup_completed": true, "code_review_github_setup_failed": true}
	if !allowed[req.Event] {
		writeError(w, r, http.StatusBadRequest, "INVALID_EVENT", "invalid policy event")
		return
	}
	if !oneOfOrEmpty(req.Scope, "organization", "repository") || !oneOfOrEmpty(req.Source, "manual", "example", "reset") || !oneOfOrEmpty(req.ExampleKey, "balanced", "security_focused", "minimal", "conservative_low_risk", "documentation_only", "small_routine_changes") || !oneOfOrEmpty(req.CharacterBucket, "0", "1-250", "251-1000", "1001-4000", "4001-8000") || !oneOfOrEmpty(req.Subsection, "all", "approval_criteria", "quality_gates", "paths_authors_checks", "reviewers_agents", "structured_description_checks") {
		writeError(w, r, http.StatusBadRequest, "INVALID_EVENT_ATTRIBUTES", "invalid policy event attributes")
		return
	}
	metrics.RecordCodeReviewPolicyEvent(r.Context(), req.Event, req.Scope, req.Source, req.ExampleKey, req.CharacterBucket, req.Subsection, req.Configured)
	w.WriteHeader(http.StatusNoContent)
}

func oneOfOrEmpty(value string, allowed ...string) bool {
	if value == "" {
		return true
	}
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}
	return false
}

func (h *CodeReviewHandler) Evidence(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	sessionID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid session ID")
		return
	}
	results, err := h.store.ListAgentResults(r.Context(), orgID, sessionID)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "CODE_REVIEW_RESULTS_LOAD_FAILED", "failed to load code review agent results", err)
		return
	}
	findings, err := h.store.ListFindings(r.Context(), orgID, sessionID, false)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "CODE_REVIEW_FINDINGS_LOAD_FAILED", "failed to load code review findings", err)
		return
	}
	artifacts, err := h.store.ListPromptArtifacts(r.Context(), orgID, sessionID)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "CODE_REVIEW_PROMPTS_LOAD_FAILED", "failed to load code review prompt artifacts", err)
		return
	}
	writeJSON(w, http.StatusOK, models.SingleResponse[models.CodeReviewEvidence]{Data: models.CodeReviewEvidence{AgentResults: results, Findings: findings, PromptArtifacts: artifacts}})
}

func (h *CodeReviewHandler) Retry(w http.ResponseWriter, r *http.Request) {
	if h.retryService == nil {
		writeError(w, r, http.StatusServiceUnavailable, "CODE_REVIEW_RETRY_UNAVAILABLE", "code review retry is unavailable")
		return
	}
	orgID := middleware.OrgIDFromContext(r.Context())
	sessionID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid session ID")
		return
	}
	result, err := h.retryService.RetryReview(r.Context(), codereviewsvc.RetryReviewInput{
		OrgID: orgID, SessionID: sessionID,
	})
	if err != nil {
		if result.SessionID != uuid.Nil {
			h.emitRetryAudit(r, result, "dispatch_failed")
		}
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, r, http.StatusNotFound, "CODE_REVIEW_NOT_FOUND", "code review not found")
			return
		}
		var conflict *codereviewsvc.RetryReviewConflictError
		if errors.As(err, &conflict) {
			writeErrorWithDetails(w, r, http.StatusConflict, "CODE_REVIEW_RETRY_CONFLICT", conflict.Message,
				map[string]string{"reason": string(conflict.Code)}, err)
			return
		}
		classification := ghservice.ClassifyRetry(err, time.Now())
		if classification.RateLimited {
			if classification.RetryAfter != nil {
				seconds := int64((*classification.RetryAfter + time.Second - 1) / time.Second)
				if seconds < 1 {
					seconds = 1
				}
				w.Header().Set("Retry-After", strconv.FormatInt(seconds, 10))
			}
			writeError(w, r, http.StatusTooManyRequests, "GITHUB_RATE_LIMITED", "GitHub is rate-limited; retry after the requested wait", err)
			return
		}
		writeError(w, r, http.StatusInternalServerError, "CODE_REVIEW_RETRY_FAILED", "failed to retry code review", err)
		return
	}

	writeJSON(w, http.StatusAccepted, models.SingleResponse[codereviewsvc.RetryReviewResult]{Data: result})
	h.emitRetryAudit(r, result, "queued")
}

func (h *CodeReviewHandler) emitRetryAudit(r *http.Request, result codereviewsvc.RetryReviewResult, dispatchStatus string) {
	resourceID := result.PreviousSessionID.String()
	newSessionID := result.SessionID
	detailValues := map[string]any{
		"previous_session_id":    result.PreviousSessionID,
		"replacement_session_id": result.SessionID,
		"metadata_id":            result.MetadataID,
		"dispatch_status":        dispatchStatus,
	}
	if result.JobID != uuid.Nil {
		detailValues["job_id"] = result.JobID
	}
	details := marshalAuditDetails(*zerolog.Ctx(r.Context()), detailValues)
	emitUserAuditWithSession(h.audit, r, models.AuditActionCodeReviewRetried, models.AuditResourceCodeReview,
		&resourceID, &newSessionID, nil, details)
}

func (h *CodeReviewHandler) GetPolicy(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	resolved, err := h.store.ResolvePolicy(r.Context(), orgID)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "CODE_REVIEW_POLICY_LOAD_FAILED", "failed to load code review policy", err)
		return
	}
	writeJSON(w, http.StatusOK, models.SingleResponse[models.CodeReviewResolvedPolicy]{Data: resolved})
}

func (h *CodeReviewHandler) PutPolicy(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	var req struct {
		RepositoryID *uuid.UUID                        `json:"repository_id,omitempty"`
		Config       json.RawMessage                   `json:"config"`
		Source       models.CodeReviewPolicyEditSource `json:"source,omitempty"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_BODY", "invalid request body")
		return
	}
	if req.Source == "" {
		req.Source = models.CodeReviewPolicyEditSourceManual
	}
	if err := req.Source.Validate(); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_SOURCE", "invalid policy edit source", err)
		return
	}
	if req.RepositoryID != nil {
		writeError(w, r, http.StatusBadRequest, "CODE_REVIEW_POLICY_SCOPE_UNSUPPORTED", "code review policy applies to all repositories")
		return
	}
	var config models.CodeReviewPolicyConfig
	if err := json.Unmarshal(req.Config, &config); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_BODY", "invalid policy config")
		return
	}
	var supplied map[string]json.RawMessage
	if err := json.Unmarshal(req.Config, &supplied); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_BODY", "invalid policy config")
		return
	}
	_, reviewInstructionsSupplied := supplied["review_instructions"]
	_, automatedApprovalPolicySupplied := supplied["automated_approval_policy"]
	if !reviewInstructionsSupplied || !automatedApprovalPolicySupplied {
		current, err := h.store.ResolvePolicy(r.Context(), orgID)
		if err != nil {
			writeError(w, r, http.StatusInternalServerError, "CODE_REVIEW_POLICY_LOAD_FAILED", "failed to load code review policy", err)
			return
		}
		if !reviewInstructionsSupplied {
			config.ReviewInstructions = current.Config.ReviewInstructions
		}
		if !automatedApprovalPolicySupplied {
			config.AutomatedApprovalPolicy = current.Config.AutomatedApprovalPolicy
		}
	}
	user := middleware.UserFromContext(r.Context())
	if user == nil {
		writeError(w, r, http.StatusUnauthorized, "UNAUTHORIZED", "user is required")
		return
	}
	record, err := h.store.SavePolicy(r.Context(), orgID, config, &user.ID)
	if err != nil {
		var validationErr *models.CodeReviewPolicyValidationError
		if errors.As(err, &validationErr) {
			writeErrorWithDetails(w, r, http.StatusBadRequest, "CODE_REVIEW_POLICY_INVALID", "invalid code review policy", map[string]string{"field": validationErr.Field}, err)
			return
		}
		writeError(w, r, http.StatusBadRequest, "CODE_REVIEW_POLICY_INVALID", "invalid code review policy", err)
		return
	}
	writeJSON(w, http.StatusOK, models.SingleResponse[models.CodeReviewPolicyRecord]{Data: record})
	details := marshalAuditDetails(*zerolog.Ctx(r.Context()), map[string]any{"source": req.Source, "version": record.Version, "review_instructions_runes": utf8.RuneCountInString(record.ReviewInstructions), "automated_approval_policy_runes": utf8.RuneCountInString(record.AutomatedApprovalPolicy)})
	resourceID := record.ID.String()
	emitUserAudit(h.audit, r, models.AuditActionCodeReviewPolicyUpdated, models.AuditResourceCodeReviewPolicy, &resourceID, details)
}

func (h *CodeReviewHandler) GetGitHubTrigger(w http.ResponseWriter, r *http.Request) {
	if h.triggerSetup == nil {
		writeError(w, r, http.StatusServiceUnavailable, "GITHUB_TRIGGER_SETUP_NOT_CONFIGURED", "GitHub reviewer trigger setup is not configured")
		return
	}
	orgID := middleware.OrgIDFromContext(r.Context())
	user := middleware.UserFromContext(r.Context())
	if user == nil {
		writeError(w, r, http.StatusUnauthorized, "UNAUTHORIZED", "user is required")
		return
	}
	repositoryID, ok := parseRequiredUUIDQuery(w, r, "repository_id")
	if !ok {
		return
	}
	resp, err := h.triggerSetup.Status(r.Context(), orgID, user.ID, repositoryID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, r, http.StatusNotFound, "REPOSITORY_NOT_FOUND", "repository not found")
			return
		}
		writeError(w, r, http.StatusInternalServerError, "GITHUB_TRIGGER_STATUS_FAILED", "failed to load GitHub reviewer trigger status", err)
		return
	}
	writeJSON(w, http.StatusOK, models.SingleResponse[models.CodeReviewGitHubTriggerResponse]{Data: resp})
}

func (h *CodeReviewHandler) SetupGitHubTrigger(w http.ResponseWriter, r *http.Request) {
	if h.triggerSetup == nil {
		writeError(w, r, http.StatusServiceUnavailable, "GITHUB_TRIGGER_SETUP_NOT_CONFIGURED", "GitHub reviewer trigger setup is not configured")
		return
	}
	orgID := middleware.OrgIDFromContext(r.Context())
	user := middleware.UserFromContext(r.Context())
	if user == nil {
		writeError(w, r, http.StatusUnauthorized, "UNAUTHORIZED", "user is required")
		return
	}
	var req struct {
		RepositoryID uuid.UUID `json:"repository_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_BODY", "invalid request body")
		return
	}
	if req.RepositoryID == uuid.Nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_REPOSITORY_ID", "repository_id is required")
		return
	}
	resp, err := h.triggerSetup.Setup(r.Context(), codereviewsvc.GitHubTriggerSetupInput{
		OrgID:        orgID,
		UserID:       user.ID,
		RepositoryID: req.RepositoryID,
	})
	if err != nil {
		switch {
		case errors.Is(err, codereviewsvc.ErrGitHubTriggerAuthRequired):
			writeError(w, r, http.StatusConflict, "GITHUB_USER_AUTH_REQUIRED", "connect your GitHub account before creating the reviewer team", err)
		case errors.Is(err, codereviewsvc.ErrGitHubTriggerPermissionRequired):
			writeError(w, r, http.StatusForbidden, "GITHUB_TRIGGER_PERMISSION_REQUIRED", "GitHub rejected setup; an org owner may need to approve Organization Members write and Repository Administration write permissions for the GitHub App", err)
		case errors.Is(err, pgx.ErrNoRows):
			writeError(w, r, http.StatusNotFound, "REPOSITORY_NOT_FOUND", "repository not found")
		default:
			writeError(w, r, http.StatusInternalServerError, "GITHUB_TRIGGER_SETUP_FAILED", "failed to set up GitHub reviewer trigger", err)
		}
		return
	}
	writeJSON(w, http.StatusOK, models.SingleResponse[models.CodeReviewGitHubTriggerResponse]{Data: resp})
}

func (h *CodeReviewHandler) DeleteGitHubTrigger(w http.ResponseWriter, r *http.Request) {
	if h.triggerSetup == nil {
		writeError(w, r, http.StatusServiceUnavailable, "GITHUB_TRIGGER_SETUP_NOT_CONFIGURED", "GitHub reviewer trigger setup is not configured")
		return
	}
	orgID := middleware.OrgIDFromContext(r.Context())
	user := middleware.UserFromContext(r.Context())
	if user == nil {
		writeError(w, r, http.StatusUnauthorized, "UNAUTHORIZED", "user is required")
		return
	}
	repositoryID, ok := parseRequiredUUIDQuery(w, r, "repository_id")
	if !ok {
		return
	}
	if err := h.triggerSetup.Deactivate(r.Context(), orgID, user.ID, repositoryID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, r, http.StatusNotFound, "REPOSITORY_NOT_FOUND", "repository not found")
			return
		}
		writeError(w, r, http.StatusInternalServerError, "GITHUB_TRIGGER_DELETE_FAILED", "failed to disable GitHub reviewer trigger", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *CodeReviewHandler) CreateAgentResult(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	sessionID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid session ID")
		return
	}
	var result models.CodeReviewAgentResult
	if err := json.NewDecoder(r.Body).Decode(&result); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_BODY", "invalid request body")
		return
	}
	if _, err := h.store.GetBySessionID(r.Context(), orgID, sessionID); err != nil {
		if err == pgx.ErrNoRows {
			writeError(w, r, http.StatusNotFound, "CODE_REVIEW_NOT_FOUND", "code review session not found")
			return
		}
		writeError(w, r, http.StatusInternalServerError, "CODE_REVIEW_LOAD_FAILED", "failed to load code review session", err)
		return
	}
	result.OrgID = orgID
	result.SessionID = sessionID
	if err := h.store.CreateAgentResult(r.Context(), &result); err != nil {
		writeError(w, r, http.StatusBadRequest, "CODE_REVIEW_AGENT_RESULT_INVALID", "failed to save code review agent result", err)
		return
	}
	writeJSON(w, http.StatusCreated, models.SingleResponse[models.CodeReviewAgentResult]{Data: result})
}

func (h *CodeReviewHandler) CreateFinding(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	sessionID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid session ID")
		return
	}
	var finding models.CodeReviewFinding
	if err := json.NewDecoder(r.Body).Decode(&finding); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_BODY", "invalid request body")
		return
	}
	if _, err := h.store.GetBySessionID(r.Context(), orgID, sessionID); err != nil {
		if err == pgx.ErrNoRows {
			writeError(w, r, http.StatusNotFound, "CODE_REVIEW_NOT_FOUND", "code review session not found")
			return
		}
		writeError(w, r, http.StatusInternalServerError, "CODE_REVIEW_LOAD_FAILED", "failed to load code review session", err)
		return
	}
	finding.OrgID = orgID
	finding.SessionID = sessionID
	if err := h.store.CreateFinding(r.Context(), &finding); err != nil {
		writeError(w, r, http.StatusBadRequest, "CODE_REVIEW_FINDING_INVALID", "failed to save code review finding", err)
		return
	}
	writeJSON(w, http.StatusCreated, models.SingleResponse[models.CodeReviewFinding]{Data: finding})
}
