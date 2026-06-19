package handlers

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/assembledhq/143/internal/auth"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
)

// maxIssuesPerPMRun caps how many issues a single PM agent run can create.
// This prevents a misbehaving agent from flooding the system.
const maxIssuesPerPMRun = 10

// InternalIssueHandler handles issue creation from sandbox agents via internal API tokens.
// When an issue is created, it also creates a coding agent session and enqueues
// a run_agent job so the issue is picked up when concurrency slots are available.
type InternalIssueHandler struct {
	issueStore    *db.IssueStore
	sessionStore  *db.SessionStore
	jobStore      *db.JobStore
	orgStore      *db.OrganizationStore
	signingSecret string
	logger        zerolog.Logger

	// perTokenCount tracks how many issues each token has created.
	// Keyed by a hash of the token string.
	perTokenMu    sync.Mutex
	perTokenCount map[string]int
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
		perTokenCount: make(map[string]int),
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
// and enqueues a run_agent job so it is picked up when slots are available.
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
	if claims.SessionOrigin == string(models.SessionOriginAutomationGoalImprovement) {
		writeError(w, r, http.StatusForbidden, "TOOL_NOT_AVAILABLE", "issue creation is not available to automation goal improvement sessions")
		return
	}

	// Rate limit: max issues per PM run (keyed by token hash).
	tokenHash := hashToken(tokenStr)
	if !h.incrementAndCheck(tokenHash) {
		writeError(w, r, http.StatusTooManyRequests, "RATE_LIMITED",
			fmt.Sprintf("issue creation limit reached (%d per PM run)", maxIssuesPerPMRun))
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
		severity = "medium"
	}
	switch severity {
	case "critical", "high", "medium", "low":
	default:
		writeError(w, r, http.StatusBadRequest, "INVALID_SEVERITY", "severity must be critical, high, medium, or low")
		return
	}

	now := time.Now()
	// Use a hash of title+description for fingerprint to avoid collisions
	// on title reuse while still deduplicating truly identical issues.
	fpHash := sha256.Sum256([]byte(req.Title + "\x00" + req.Description))
	fingerprint := "pm-agent:" + hex.EncodeToString(fpHash[:12])

	description := req.Description
	issue := &models.Issue{
		OrgID:           claims.OrgID,
		ExternalID:      uuid.New().String(),
		Source:          models.IssueSourcePMAgent,
		Title:           req.Title,
		Description:     &description,
		Status:          models.IssueStatusTriaged,
		Severity:        models.IssueSeverity(severity),
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

	// Create a coding agent session and enqueue a run_agent job.
	// The session is created as "pending" — the worker picks it up when
	// concurrency slots are available, so we don't need to check capacity here.
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

// dispatchSession creates a coding agent session for the issue and enqueues a
// run_agent job. The session starts as "pending" and will be picked up by the
// worker when concurrency slots are available.
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
		autonomyLevel = "semi"
	}

	title := issue.Title

	session := &models.Session{
		PrimaryIssueID:   &issue.ID,
		OrgID:            orgID,
		Origin:           models.SessionOriginIssueTrigger,
		InteractionMode:  models.SessionInteractionModeSingleRun,
		ValidationPolicy: models.SessionValidationPolicyOnTurnComplete,
		AgentType:        agentType,
		Status:           models.SessionStatusPending,
		AutonomyLevel:    models.SessionAutonomy(autonomyLevel),
		TokenMode:        models.SessionTokenModeLow,
		Title:            &title,
		PMApproach:       issue.Description,
		RepositoryID:     issue.RepositoryID,
	}
	if err := h.sessionStore.Create(r.Context(), session); err != nil {
		return nil, err
	}

	dedupeKey := db.RunAgentDedupeKey(session.ID)
	payload := db.RunAgentPayload(session)
	if _, err := h.jobStore.Enqueue(r.Context(), orgID, "agent", "run_agent", payload, 5, &dedupeKey); err != nil {
		return nil, err
	}

	return &session.ID, nil
}

// incrementAndCheck atomically increments the per-token issue count and returns
// true if the count is within the allowed limit.
func (h *InternalIssueHandler) incrementAndCheck(tokenHash string) bool {
	h.perTokenMu.Lock()
	defer h.perTokenMu.Unlock()
	count := h.perTokenCount[tokenHash]
	if count >= maxIssuesPerPMRun {
		return false
	}
	h.perTokenCount[tokenHash] = count + 1
	return true
}

// hashToken returns a short hash of a token string for use as a map key.
func hashToken(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:8])
}
