package handlers

import (
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

type RunHandler struct {
	runStore      *db.AgentRunStore
	logStore      *db.AgentRunLogStore
	questionStore *db.AgentRunQuestionStore
	validationStore *db.ValidationStore
	pullRequestStore *db.PullRequestStore
	issueStore    *db.IssueStore
	jobStore      *db.JobStore
}

func NewRunHandler(
	runStore *db.AgentRunStore,
	logStore *db.AgentRunLogStore,
	questionStore *db.AgentRunQuestionStore,
	validationStore *db.ValidationStore,
	pullRequestStore *db.PullRequestStore,
	issueStore *db.IssueStore,
	jobStore *db.JobStore,
) *RunHandler {
	return &RunHandler{
		runStore:         runStore,
		logStore:         logStore,
		questionStore:    questionStore,
		validationStore:  validationStore,
		pullRequestStore: pullRequestStore,
		issueStore:       issueStore,
		jobStore:         jobStore,
	}
}

func (h *RunHandler) List(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())

	limit := queryInt(r, "limit", 50)
	filters := db.AgentRunFilters{
		Status: r.URL.Query().Get("status"),
		Limit:  limit,
		Cursor: r.URL.Query().Get("cursor"),
	}

	runs, err := h.runStore.ListByOrg(r.Context(), orgID, filters)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "LIST_FAILED", "failed to list runs")
		return
	}
	if runs == nil {
		runs = []models.AgentRun{}
	}

	var nextCursor string
	if len(runs) > 0 && len(runs) == filters.Limit {
		nextCursor = runs[len(runs)-1].ID.String()
	}

	writeJSON(w, http.StatusOK, models.ListResponse[models.AgentRun]{
		Data: runs,
		Meta: models.PaginationMeta{NextCursor: nextCursor},
	})
}

func (h *RunHandler) Get(w http.ResponseWriter, r *http.Request) {
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
	writeJSON(w, http.StatusOK, models.SingleResponse[models.AgentRun]{Data: run})
}

// TriggerFix creates a new agent run for an issue and enqueues a run_agent job.
func (h *RunHandler) TriggerFix(w http.ResponseWriter, r *http.Request) {
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

	// TODO: Allow callers to specify agent_type, autonomy_level, and token_mode
	// via the request body instead of hardcoding defaults.
	run := &models.AgentRun{
		IssueID:       issueID,
		OrgID:         orgID,
		AgentType:     "claude_code",
		Status:        "pending",
		AutonomyLevel: "semi",
		TokenMode:     "low",
	}
	if err := h.runStore.Create(r.Context(), run); err != nil {
		writeError(w, http.StatusInternalServerError, "CREATE_FAILED", "failed to create agent run")
		return
	}

	// Enqueue the run_agent job.
	payload := map[string]string{
		"agent_run_id": run.ID.String(),
		"org_id":       orgID.String(),
	}
	if _, err := h.jobStore.Enqueue(r.Context(), orgID, "agent", "run_agent", payload, 5, nil); err != nil {
		writeError(w, http.StatusInternalServerError, "ENQUEUE_FAILED", "failed to enqueue agent run job")
		return
	}

	writeJSON(w, http.StatusCreated, models.SingleResponse[models.AgentRun]{Data: *run})
}

// StreamLogs streams agent run logs as Server-Sent Events.
// Note: agent_run_logs are scoped by agent_run_id (which is itself org-scoped),
// so this endpoint verifies org ownership via the run lookup rather than
// requiring org_id directly on the logs table.
func (h *RunHandler) StreamLogs(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	runID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "invalid run ID")
		return
	}

	// Verify run exists.
	_, err = h.runStore.GetByID(r.Context(), orgID, runID)
	if err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "run not found")
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
	logs, err := h.logStore.ListByRunID(r.Context(), runID)
	if err != nil {
		return
	}

	var lastSeenID int64
	for _, log := range logs {
		data, _ := json.Marshal(log)
		fmt.Fprintf(w, "data: %s\n\n", data)
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

			newLogs, err := h.logStore.ListByRunIDSince(r.Context(), runID, lastSeenID)
			if err != nil {
				return
			}
			for _, log := range newLogs {
				data, _ := json.Marshal(log)
				fmt.Fprintf(w, "data: %s\n\n", data)
				lastSeenID = log.ID
			}
			flusher.Flush()

			if isTerminalStatus(run.Status) {
				return
			}
		}
	}
}

// GetValidation returns the validation results for an agent run.
func (h *RunHandler) GetValidation(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	runID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "invalid run ID")
		return
	}

	v, err := h.validationStore.GetByAgentRunID(r.Context(), orgID, runID)
	if err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "validation not found")
		return
	}
	writeJSON(w, http.StatusOK, models.SingleResponse[models.Validation]{Data: v})
}

// GetPullRequest returns the PR associated with an agent run.
func (h *RunHandler) GetPullRequest(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	runID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "invalid run ID")
		return
	}

	pr, err := h.pullRequestStore.GetByAgentRunID(r.Context(), orgID, runID)
	if err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "pull request not found")
		return
	}
	writeJSON(w, http.StatusOK, models.SingleResponse[models.PullRequest]{Data: pr})
}

// ListQuestions returns the questions for an agent run.
func (h *RunHandler) ListQuestions(w http.ResponseWriter, r *http.Request) {
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
		questions = []models.AgentRunQuestion{}
	}

	writeJSON(w, http.StatusOK, models.ListResponse[models.AgentRunQuestion]{
		Data: questions,
	})
}

// AnswerQuestion records an answer to an agent run question.
func (h *RunHandler) AnswerQuestion(w http.ResponseWriter, r *http.Request) {
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

	writeJSON(w, http.StatusOK, models.SingleResponse[models.AgentRunQuestion]{Data: question})
}

func isTerminalStatus(status string) bool {
	switch status {
	case "completed", "failed", "cancelled":
		return true
	}
	return false
}
