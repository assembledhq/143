package handlers

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/assembledhq/143/internal/auth"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
)

// InternalIssueHandler handles issue creation from sandbox agents via internal API tokens.
type InternalIssueHandler struct {
	issueStore    *db.IssueStore
	signingSecret string
}

// NewInternalIssueHandler creates a handler for internal issue creation.
func NewInternalIssueHandler(issueStore *db.IssueStore, signingSecret string) *InternalIssueHandler {
	return &InternalIssueHandler{
		issueStore:    issueStore,
		signingSecret: signingSecret,
	}
}

type createIssueRequest struct {
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Severity    string   `json:"severity"`
	Tags        []string `json:"tags"`
}

type createIssueResponse struct {
	ID    string `json:"id"`
	Title string `json:"title"`
}

// Create handles POST /api/v1/internal/issues.
func (h *InternalIssueHandler) Create(w http.ResponseWriter, r *http.Request) {
	// Authenticate via internal token.
	tokenStr := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
	if tokenStr == "" {
		writeError(w, r, http.StatusUnauthorized, "UNAUTHORIZED", "missing authorization token")
		return
	}

	claims, err := auth.ValidateInternalToken(h.signingSecret, tokenStr)
	if err != nil {
		writeError(w, r, http.StatusUnauthorized, "UNAUTHORIZED", "invalid token", err)
		return
	}

	var req createIssueRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_BODY", "invalid request body", err)
		return
	}

	if req.Title == "" {
		writeError(w, r, http.StatusBadRequest, "MISSING_TITLE", "title is required")
		return
	}
	if req.Description == "" {
		writeError(w, r, http.StatusBadRequest, "MISSING_DESCRIPTION", "description is required")
		return
	}

	severity := req.Severity
	if severity == "" {
		severity = "info"
	}
	switch severity {
	case "info", "warning", "error", "critical":
	default:
		writeError(w, r, http.StatusBadRequest, "INVALID_SEVERITY", "severity must be info, warning, error, or critical")
		return
	}

	now := time.Now()
	fingerprint := "pm-agent:" + req.Title
	issue := &models.Issue{
		OrgID:           claims.OrgID,
		ExternalID:      uuid.New().String(),
		Source:          models.IssueSourcePMAgent,
		Title:           req.Title,
		Description:     req.Description,
		Status:          "open",
		Severity:        severity,
		Tags:            req.Tags,
		Fingerprint:     fingerprint,
		FirstSeenAt:     now,
		LastSeenAt:      now,
		OccurrenceCount: 1,
	}

	if err := h.issueStore.Upsert(r.Context(), issue); err != nil {
		writeError(w, r, http.StatusInternalServerError, "CREATE_FAILED", "failed to create issue", err)
		return
	}

	writeJSON(w, http.StatusCreated, createIssueResponse{
		ID:    issue.ID.String(),
		Title: issue.Title,
	})
}
