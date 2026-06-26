package handlers

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type CodeReviewHandler struct {
	store *db.CodeReviewStore
	repos *db.RepositoryStore
}

func NewCodeReviewHandler(store *db.CodeReviewStore, repos *db.RepositoryStore) *CodeReviewHandler {
	return &CodeReviewHandler{store: store, repos: repos}
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
	writeJSON(w, http.StatusOK, models.SingleResponse[models.CodeReviewEvidence]{Data: models.CodeReviewEvidence{AgentResults: results, Findings: findings}})
}

func (h *CodeReviewHandler) GetPolicy(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	repositoryID, ok := parseOptionalUUIDQuery(w, r, "repository_id")
	if !ok {
		return
	}
	resolved, err := h.store.ResolvePolicy(r.Context(), orgID, repositoryID)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "CODE_REVIEW_POLICY_LOAD_FAILED", "failed to load code review policy", err)
		return
	}
	writeJSON(w, http.StatusOK, models.SingleResponse[models.CodeReviewResolvedPolicy]{Data: resolved})
}

func (h *CodeReviewHandler) PutPolicy(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	var req struct {
		RepositoryID *uuid.UUID                    `json:"repository_id,omitempty"`
		Config       models.CodeReviewPolicyConfig `json:"config"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_BODY", "invalid request body")
		return
	}
	user := middleware.UserFromContext(r.Context())
	if user == nil {
		writeError(w, r, http.StatusUnauthorized, "UNAUTHORIZED", "user is required")
		return
	}
	if req.RepositoryID != nil && h.repos != nil {
		if _, err := h.repos.GetByID(r.Context(), orgID, *req.RepositoryID); err != nil {
			if err == pgx.ErrNoRows {
				writeError(w, r, http.StatusNotFound, "REPOSITORY_NOT_FOUND", "repository not found")
				return
			}
			writeError(w, r, http.StatusInternalServerError, "REPOSITORY_LOAD_FAILED", "failed to load repository", err)
			return
		}
	}
	record, err := h.store.SavePolicy(r.Context(), orgID, req.RepositoryID, req.Config, &user.ID)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "CODE_REVIEW_POLICY_INVALID", "invalid code review policy", err)
		return
	}
	writeJSON(w, http.StatusOK, models.SingleResponse[models.CodeReviewPolicyRecord]{Data: record})
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
	finding.OrgID = orgID
	finding.SessionID = sessionID
	if err := h.store.CreateFinding(r.Context(), &finding); err != nil {
		writeError(w, r, http.StatusBadRequest, "CODE_REVIEW_FINDING_INVALID", "failed to save code review finding", err)
		return
	}
	writeJSON(w, http.StatusCreated, models.SingleResponse[models.CodeReviewFinding]{Data: finding})
}
