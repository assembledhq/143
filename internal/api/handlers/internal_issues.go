package handlers

import (
	"encoding/json"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/assembledhq/143/internal/auth"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
)

// InternalIssueHandler handles issue creation from sandbox agents via internal API tokens.
// When an issue is created, it also creates a coding agent session and enqueues
// a run_agent job so the issue is automatically worked on.
type InternalIssueHandler struct {
	issueStore    *db.IssueStore
	sessionStore  *db.SessionStore
	jobStore      *db.JobStore
	orgStore      *db.OrganizationStore
	signingSecret string
	logger        zerolog.Logger
}

// NewInternalIssueHandler creates a handler for internal issue creation.
func NewInternalIssueHandler(
	issueStore *db.IssueStore,
	sessionStore *db.SessionStore,
	jobStore *db.JobStore,
	orgStore *db.OrganizationStore,
	signingSecret string,
	logger zerolog.Logger,
) *InternalIssueHandler {
	return &InternalIssueHandler{
		issueStore:    issueStore,
		sessionStore:  sessionStore,
		jobStore:      jobStore,
		orgStore:      orgStore,
		signingSecret: signingSecret,
		logger:        logger,
	}
}

type createIssueRequest struct {
	Title       string   `json:"title"`
	Description string   `json:"description"`
	Severity    string   `json:"severity"`
	Tags        []string `json:"tags"`
}

type createIssueResponse struct {
	ID        string  `json:"id"`
	Title     string  `json:"title"`
	SessionID *string `json:"session_id,omitempty"`
}

// Create handles POST /api/v1/internal/issues.
// It creates the issue, then automatically creates a coding agent session
// and enqueues a run_agent job to solve it.
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

	// Mark issue as triaged since we're immediately dispatching work.
	if err := h.issueStore.UpdateStatus(r.Context(), claims.OrgID, issue.ID, "triaged"); err != nil {
		h.logger.Warn().Err(err).Str("issue_id", issue.ID.String()).Msg("failed to mark PM-created issue as triaged")
	}

	// Create a coding agent session and enqueue a run_agent job.
	resp := createIssueResponse{
		ID:    issue.ID.String(),
		Title: issue.Title,
	}

	sessionID, err := h.dispatchSession(r, claims.OrgID, issue)
	if err != nil {
		h.logger.Error().Err(err).Str("issue_id", issue.ID.String()).Msg("failed to dispatch session for PM-created issue")
		// Issue was created successfully — return it even if dispatch failed.
		writeJSON(w, http.StatusCreated, resp)
		return
	}
	if sessionID != nil {
		sid := sessionID.String()
		resp.SessionID = &sid
	}

	writeJSON(w, http.StatusCreated, resp)
}

// dispatchSession creates a coding agent session for the issue and enqueues a run_agent job.
func (h *InternalIssueHandler) dispatchSession(r *http.Request, orgID uuid.UUID, issue *models.Issue) (*uuid.UUID, error) {
	// Resolve org settings for default agent type and autonomy level.
	org, err := h.orgStore.GetByID(r.Context(), orgID)
	if err != nil {
		return nil, err
	}

	orgSettings, parseErr := models.ParseOrgSettings(org.Settings)
	if parseErr != nil {
		h.logger.Warn().Err(parseErr).Msg("failed to parse org settings, using defaults")
	}

	agentType := orgSettings.DefaultAgentType
	if agentType == "" {
		agentType = models.DefaultDefaultAgentType
	}

	autonomyLevel := string(orgSettings.AutonomyLevel)
	if autonomyLevel == "" {
		autonomyLevel = "full"
	}

	title := issue.Title
	approach := issue.Description

	session := &models.Session{
		IssueID:       issue.ID,
		OrgID:         orgID,
		AgentType:     agentType,
		Status:        "pending",
		AutonomyLevel: autonomyLevel,
		TokenMode:     "low",
		Title:         &title,
		PMApproach:    &approach,
		RepositoryID:  issue.RepositoryID,
	}
	if err := h.sessionStore.Create(r.Context(), session); err != nil {
		return nil, err
	}

	payload := map[string]string{
		"session_id": session.ID.String(),
		"org_id":     orgID.String(),
	}
	if _, err := h.jobStore.Enqueue(r.Context(), orgID, "agent", "run_agent", payload, 5, nil); err != nil {
		return nil, err
	}

	return &session.ID, nil
}
