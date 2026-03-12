package handlers

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

type SessionHandler struct {
	runStore         *db.SessionStore
	logStore         *db.SessionLogStore
	questionStore    *db.SessionQuestionStore
	validationStore  *db.ValidationStore
	pullRequestStore *db.PullRequestStore
	issueStore       *db.IssueStore
	orgStore         *db.OrganizationStore
	jobStore         *db.JobStore
}

func NewSessionHandler(
	runStore *db.SessionStore,
	logStore *db.SessionLogStore,
	questionStore *db.SessionQuestionStore,
	validationStore *db.ValidationStore,
	pullRequestStore *db.PullRequestStore,
	issueStore *db.IssueStore,
	orgStore *db.OrganizationStore,
	jobStore *db.JobStore,
) *SessionHandler {
	return &SessionHandler{
		runStore:         runStore,
		logStore:         logStore,
		questionStore:    questionStore,
		validationStore:  validationStore,
		pullRequestStore: pullRequestStore,
		issueStore:       issueStore,
		orgStore:         orgStore,
		jobStore:         jobStore,
	}
}

func (h *SessionHandler) List(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())

	limit := queryInt(r, "limit", 50)
	filters := db.SessionFilters{
		Status: models.SessionStatus(r.URL.Query().Get("status")),
		Limit:  limit,
		Cursor: r.URL.Query().Get("cursor"),
	}

	runs, err := h.runStore.ListByOrg(r.Context(), orgID, filters)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "LIST_FAILED", "failed to list runs")
		return
	}
	if runs == nil {
		runs = []models.Session{}
	}

	var nextCursor string
	if len(runs) > 0 && len(runs) == filters.Limit {
		nextCursor = runs[len(runs)-1].ID.String()
	}

	writeJSON(w, http.StatusOK, models.ListResponse[models.Session]{
		Data: runs,
		Meta: models.PaginationMeta{NextCursor: nextCursor},
	})
}

func (h *SessionHandler) Get(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	runID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "invalid run ID")
		return
	}

	run, err := h.runStore.GetByID(r.Context(), orgID, runID)
	if err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "run not found")
		return
	}
	writeJSON(w, http.StatusOK, models.SingleResponse[models.Session]{Data: run})
}

// TriggerFix creates a new agent run for an issue and enqueues a run_agent job.
func (h *SessionHandler) TriggerFix(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	issueID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "invalid issue ID")
		return
	}

	// Verify the issue exists.
	_, err = h.issueStore.GetByID(r.Context(), orgID, issueID)
	if err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "issue not found")
		return
	}

	// Parse optional overrides from the request body.
	var body struct {
		AgentType     string `json:"agent_type"`
		AutonomyLevel string `json:"autonomy_level"`
		TokenMode     string `json:"token_mode"`
	}
	// Ignore decode errors — body is optional, fields default below.
	_ = json.NewDecoder(r.Body).Decode(&body)

	agentType := models.AgentType(body.AgentType)
	if agentType == "" {
		org, err := h.orgStore.GetByID(r.Context(), orgID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "DEFAULT_AGENT_LOOKUP_FAILED", "failed to load organization settings")
			return
		}
		agentType = models.ParseOrgSettings(org.Settings).DefaultAgentType
		if agentType == "" {
			agentType = models.DefaultDefaultAgentType
		}
	}
	if err := agentType.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_AGENT_TYPE", err.Error())
		return
	}

	autonomyLevel := body.AutonomyLevel
	if autonomyLevel == "" {
		autonomyLevel = "semi"
	}
	validAutonomyLevels := map[string]bool{"full": true, "semi": true, "supervised": true}
	if !validAutonomyLevels[autonomyLevel] {
		writeError(w, http.StatusBadRequest, "INVALID_AUTONOMY_LEVEL", "autonomy_level must be one of: full, semi, supervised")
		return
	}

	tokenMode := body.TokenMode
	if tokenMode == "" {
		tokenMode = "low"
	}
	validTokenModes := map[string]bool{"low": true, "high": true}
	if !validTokenModes[tokenMode] {
		writeError(w, http.StatusBadRequest, "INVALID_TOKEN_MODE", "token_mode must be one of: low, high")
		return
	}

	run := &models.Session{
		IssueID:       issueID,
		OrgID:         orgID,
		AgentType:     agentType,
		Status:        "pending",
		AutonomyLevel: autonomyLevel,
		TokenMode:     tokenMode,
	}
	if err := h.runStore.Create(r.Context(), run); err != nil {
		writeError(w, http.StatusInternalServerError, "CREATE_FAILED", "failed to create agent run")
		return
	}

	// Enqueue the run_agent job.
	payload := map[string]string{
		"session_id": run.ID.String(),
		"org_id":       orgID.String(),
	}
	if _, err := h.jobStore.Enqueue(r.Context(), orgID, "agent", "run_agent", payload, 5, nil); err != nil {
		writeError(w, http.StatusInternalServerError, "ENQUEUE_FAILED", "failed to enqueue agent run job")
		return
	}

	writeJSON(w, http.StatusCreated, models.SingleResponse[models.Session]{Data: *run})
}

// GetLogs returns all logs for a run as a JSON array.
// This is the primary endpoint for viewing historical logs for completed runs
// and also serves as the initial log fetch for active runs.
func (h *SessionHandler) GetLogs(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	runID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "invalid run ID")
		return
	}

	// Verify run exists and belongs to org.
	_, err = h.runStore.GetByID(r.Context(), orgID, runID)
	if err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "run not found")
		return
	}

	logs, err := h.logStore.ListByRunID(r.Context(), orgID, runID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "LIST_FAILED", "failed to list logs")
		return
	}
	if logs == nil {
		logs = []models.SessionLog{}
	}

	writeJSON(w, http.StatusOK, models.ListResponse[models.SessionLog]{
		Data: logs,
	})
}

// StreamLogs streams agent run logs as Server-Sent Events.
func (h *SessionHandler) StreamLogs(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	runID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "invalid run ID")
		return
	}

	// Verify run exists.
	run, err := h.runStore.GetByID(r.Context(), orgID, runID)
	if err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "run not found")
		return
	}

	// For terminal runs, return existing logs as JSON instead of SSE
	// since there will be no new logs to stream.
	if isTerminalStatus(run.Status) {
		h.GetLogs(w, r)
		return
	}

	flusher, ok := w.(http.Flusher)
	if !ok {
		writeError(w, http.StatusInternalServerError, "SSE_NOT_SUPPORTED", "streaming not supported")
		return
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")

	// Send existing logs.
	logs, err := h.logStore.ListByRunID(r.Context(), orgID, runID)
	if err != nil {
		return
	}

	var lastSeenID int64
	for _, log := range logs {
		data, _ := json.Marshal(log)
		fmt.Fprintf(w, "data: %s\n\n", data) // #nosec G705 -- SSE event stream, data is JSON-marshaled
		lastSeenID = log.ID
	}
	flusher.Flush()

	// Poll for new logs.
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			// Check if run is terminal.
			run, err := h.runStore.GetByID(r.Context(), orgID, runID)
			if err != nil {
				return
			}

			newLogs, err := h.logStore.ListByRunIDSince(r.Context(), orgID, runID, lastSeenID)
			if err != nil {
				return
			}
			for _, log := range newLogs {
				data, _ := json.Marshal(log)
				fmt.Fprintf(w, "data: %s\n\n", data) // #nosec G705 -- SSE event stream, data is JSON-marshaled
				lastSeenID = log.ID
			}
			flusher.Flush()

			if isTerminalStatus(run.Status) {
				// Send a final "done" event so the client knows to stop.
				fmt.Fprintf(w, "event: done\ndata: {}\n\n")
				flusher.Flush()
				return
			}
		}
	}
}

// GetValidation returns the validation results for an agent run.
func (h *SessionHandler) GetValidation(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	runID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "invalid run ID")
		return
	}

	v, err := h.validationStore.GetBySessionID(r.Context(), orgID, runID)
	if err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "validation not found")
		return
	}
	writeJSON(w, http.StatusOK, models.SingleResponse[models.Validation]{Data: v})
}

// GetPullRequest returns the PR associated with an agent run.
func (h *SessionHandler) GetPullRequest(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	runID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "invalid run ID")
		return
	}

	pr, err := h.pullRequestStore.GetBySessionID(r.Context(), orgID, runID)
	if err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "pull request not found")
		return
	}
	writeJSON(w, http.StatusOK, models.SingleResponse[models.PullRequest]{Data: pr})
}

// ListQuestions returns the questions for an agent run.
func (h *SessionHandler) ListQuestions(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	runID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "invalid run ID")
		return
	}

	questions, err := h.questionStore.ListByRunID(r.Context(), orgID, runID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "LIST_FAILED", "failed to list questions")
		return
	}
	if questions == nil {
		questions = []models.SessionQuestion{}
	}

	writeJSON(w, http.StatusOK, models.ListResponse[models.SessionQuestion]{
		Data: questions,
	})
}

// AnswerQuestion records an answer to an agent run question.
func (h *SessionHandler) AnswerQuestion(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	qID, err := uuid.Parse(chi.URLParam(r, "qid"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "invalid question ID")
		return
	}

	user := middleware.UserFromContext(r.Context())
	if user == nil {
		writeError(w, http.StatusUnauthorized, "UNAUTHORIZED", "user not found")
		return
	}

	var body struct {
		Answer string `json:"answer"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", "invalid request body")
		return
	}
	if body.Answer == "" {
		writeError(w, http.StatusBadRequest, "MISSING_ANSWER", "answer is required")
		return
	}

	if err := h.questionStore.Answer(r.Context(), orgID, qID, body.Answer, user.ID); err != nil {
		writeError(w, http.StatusInternalServerError, "ANSWER_FAILED", "failed to answer question")
		return
	}

	question, err := h.questionStore.GetByID(r.Context(), orgID, qID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "FETCH_FAILED", "failed to fetch updated question")
		return
	}

	writeJSON(w, http.StatusOK, models.SingleResponse[models.SessionQuestion]{Data: question})
}

func isTerminalStatus(status string) bool {
	switch status {
	case "completed", "failed", "cancelled":
		return true
	}
	return false
}

// CreateManual creates a new manual session from a user-provided message.
func (h *SessionHandler) CreateManual(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())

	var body struct {
		Message       string   `json:"message"`
		Images        []string `json:"images"`
		AgentType     string   `json:"agent_type"`
		Model         string   `json:"model"`
		AutonomyLevel string   `json:"autonomy_level"`
		TokenMode     string   `json:"token_mode"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", "invalid request body")
		return
	}

	body.Message = strings.TrimSpace(body.Message)
	if body.Message == "" {
		writeError(w, http.StatusBadRequest, "MISSING_MESSAGE", "message is required")
		return
	}

	agentType := models.AgentType(body.AgentType)
	if agentType == "" {
		org, err := h.orgStore.GetByID(r.Context(), orgID)
		if err != nil {
			writeError(w, http.StatusInternalServerError, "DEFAULT_AGENT_LOOKUP_FAILED", "failed to load organization settings")
			return
		}
		agentType = models.ParseOrgSettings(org.Settings).DefaultAgentType
		if agentType == "" {
			agentType = models.DefaultDefaultAgentType
		}
	}
	if err := agentType.Validate(); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_AGENT_TYPE", err.Error())
		return
	}

	var modelOverride *string
	if body.Model != "" {
		if err := models.ValidateModelForAgentType(agentType, body.Model); err != nil {
			writeError(w, http.StatusBadRequest, "INVALID_MODEL", err.Error())
			return
		}
		modelOverride = &body.Model
	}

	autonomyLevel := body.AutonomyLevel
	if autonomyLevel == "" {
		autonomyLevel = "semi"
	}
	validAutonomyLevels := map[string]bool{"full": true, "semi": true, "supervised": true}
	if !validAutonomyLevels[autonomyLevel] {
		writeError(w, http.StatusBadRequest, "INVALID_AUTONOMY_LEVEL", "autonomy_level must be one of: full, semi, supervised")
		return
	}

	tokenMode := body.TokenMode
	if tokenMode == "" {
		tokenMode = "low"
	}
	validTokenModes := map[string]bool{"low": true, "high": true}
	if !validTokenModes[tokenMode] {
		writeError(w, http.StatusBadRequest, "INVALID_TOKEN_MODE", "token_mode must be one of: low, high")
		return
	}

	now := time.Now()
	fingerprint := fmt.Sprintf("manual:%x", sha256.Sum256([]byte(fmt.Sprintf("%s:%d", body.Message, now.UnixNano()))))
	description := buildManualSessionDescription(body.Message, body.Images)
	title := manualSessionTitle(body.Message)
	rawData, err := json.Marshal(map[string]any{
		"manual_session": true,
		"images":         body.Images,
	})
	if err != nil {
		writeError(w, http.StatusInternalServerError, "ENCODE_FAILED", "failed to encode manual session context")
		return
	}
	issue := &models.Issue{
		OrgID:                 orgID,
		ExternalID:            "manual-" + now.UTC().Format("20060102150405") + "-" + strings.ReplaceAll(uuid.NewString(), "-", ""),
		Source:                "manual",
		Title:                 title,
		Description:           &description,
		RawData:               rawData,
		Status:                "open",
		FirstSeenAt:           now,
		LastSeenAt:            now,
		OccurrenceCount:       1,
		AffectedCustomerCount: 1,
		Severity:              "medium",
		Fingerprint:           fingerprint,
	}

	if err := h.issueStore.Upsert(r.Context(), issue); err != nil {
		writeError(w, http.StatusInternalServerError, "ISSUE_CREATE_FAILED", "failed to create manual issue")
		return
	}

	session := &models.Session{
		IssueID:       issue.ID,
		OrgID:         orgID,
		AgentType:     agentType,
		Status:        "pending",
		AutonomyLevel: autonomyLevel,
		TokenMode:     tokenMode,
		ModelOverride: modelOverride,
	}
	if err := h.runStore.Create(r.Context(), session); err != nil {
		writeError(w, http.StatusInternalServerError, "CREATE_FAILED", "failed to create manual session")
		return
	}

	payload := map[string]string{
		"session_id": session.ID.String(),
		"org_id":     orgID.String(),
	}
	if _, err := h.jobStore.Enqueue(r.Context(), orgID, "agent", "run_agent", payload, 5, nil); err != nil {
		writeError(w, http.StatusInternalServerError, "ENQUEUE_FAILED", "failed to enqueue manual session")
		return
	}

	writeJSON(w, http.StatusCreated, models.SingleResponse[models.Session]{Data: *session})
}

func buildManualSessionDescription(message string, images []string) string {
	if len(images) == 0 {
		return message
	}

	var b strings.Builder
	b.WriteString(message)
	b.WriteString("\n\n### Attached images\n")
	for _, imageURL := range images {
		if strings.TrimSpace(imageURL) == "" {
			continue
		}
		b.WriteString("- ")
		b.WriteString(imageURL)
		b.WriteString("\n")
	}

	return strings.TrimSpace(b.String())
}

func manualSessionTitle(message string) string {
	trimmed := strings.TrimSpace(message)
	if trimmed == "" {
		return "Manual Session"
	}

	if idx := strings.Index(trimmed, "\n"); idx > 0 {
		trimmed = trimmed[:idx]
	}

	if len(trimmed) <= 120 {
		return trimmed
	}

	return strings.TrimSpace(trimmed[:120]) + "..."
}
