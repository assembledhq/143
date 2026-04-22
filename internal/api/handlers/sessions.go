package handlers

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/api/sse"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/llm"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services"
	"github.com/assembledhq/143/internal/services/storage"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog"
)

// SessionCanceller can cancel a running agent session. The Orchestrator
// implements this interface; it is injected after construction via SetCanceller.
type SessionCanceller interface {
	CancelSession(sessionID uuid.UUID) bool
}

type SessionHandler struct {
	runStore         *db.SessionStore
	logStore         *db.SessionLogStore
	questionStore    *db.SessionQuestionStore
	validationStore  *db.ValidationStore
	pullRequestStore *db.PullRequestStore
	issueStore       *db.IssueStore
	repoStore        *db.RepositoryStore
	orgStore         *db.OrganizationStore
	jobStore         *db.JobStore
	messageStore     *db.SessionMessageStore
	threadStore      *db.SessionThreadStore
	viewStore        *db.SessionViewStore
	snapshotStore    storage.SnapshotStore // optional — enables snapshot cleanup on archive
	llmClient        llm.Client            // optional, used for generating manual session titles
	logger           zerolog.Logger
	audit            *db.AuditEmitter
	canceller        SessionCanceller // optional — enables cancelling running sessions
	// shutdownCh is closed on SIGTERM; see SetShutdownSignal.
	shutdownCh <-chan struct{}
}

// SetAuditEmitter injects the audit emitter for logging session events.
func (h *SessionHandler) SetAuditEmitter(audit *db.AuditEmitter) {
	h.audit = audit
}

// SetCanceller injects the session canceller for stopping running agent sessions.
func (h *SessionHandler) SetCanceller(c SessionCanceller) {
	h.canceller = c
}

// SetViewStore injects the session view store for tracking unread sessions.
func (h *SessionHandler) SetViewStore(vs *db.SessionViewStore) {
	h.viewStore = vs
}

// SetShutdownSignal wires a channel that is closed when the server is
// shutting down. SSE stream handlers listen on it so they return promptly
// during graceful shutdown instead of blocking Server.Shutdown until its
// deadline expires.
func (h *SessionHandler) SetShutdownSignal(ch <-chan struct{}) {
	h.shutdownCh = ch
}

// SetSnapshotStore injects the snapshot store so session archive can delete
// the associated sandbox snapshot file. Optional — if unset, archive still
// succeeds but leaves the snapshot to be reclaimed by the TTL reaper.
func (h *SessionHandler) SetSnapshotStore(s storage.SnapshotStore) {
	h.snapshotStore = s
}

func NewSessionHandler(
	runStore *db.SessionStore,
	logStore *db.SessionLogStore,
	questionStore *db.SessionQuestionStore,
	validationStore *db.ValidationStore,
	pullRequestStore *db.PullRequestStore,
	issueStore *db.IssueStore,
	repoStore *db.RepositoryStore,
	orgStore *db.OrganizationStore,
	jobStore *db.JobStore,
	messageStore *db.SessionMessageStore,
	threadStore *db.SessionThreadStore,
	llmClient llm.Client,
	logger zerolog.Logger,
) *SessionHandler {
	return &SessionHandler{
		runStore:         runStore,
		logStore:         logStore,
		questionStore:    questionStore,
		validationStore:  validationStore,
		pullRequestStore: pullRequestStore,
		issueStore:       issueStore,
		repoStore:        repoStore,
		orgStore:         orgStore,
		jobStore:         jobStore,
		messageStore:     messageStore,
		threadStore:      threadStore,
		llmClient:        llmClient,
		logger:           logger,
	}
}

// encodeSessionCursor produces an opaque cursor from the last row's created_at and id.
func encodeSessionCursor(createdAt time.Time, id uuid.UUID) string {
	return encodeCursor(createdAt, id.String())
}

// decodeSessionCursor is the inverse of encodeSessionCursor.
func decodeSessionCursor(cursor string) (time.Time, uuid.UUID, error) {
	t, rawID, err := decodeCursor(cursor)
	if err != nil {
		return time.Time{}, uuid.Nil, err
	}
	id, err := uuid.Parse(rawID)
	if err != nil {
		return time.Time{}, uuid.Nil, fmt.Errorf("invalid cursor id: %w", err)
	}
	return t, id, nil
}

func (h *SessionHandler) List(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())

	limit := queryInt(r, "limit", 50)
	filters := db.SessionFilters{
		Limit: limit,
	}

	if cursor := r.URL.Query().Get("cursor"); cursor != "" {
		t, id, err := decodeSessionCursor(cursor)
		if err != nil {
			writeError(w, r, http.StatusBadRequest, "INVALID_CURSOR", "invalid cursor")
			return
		}
		filters.CursorTime = &t
		filters.CursorID = &id
	}

	if r.URL.Query().Get("only_archived") == "true" {
		filters.OnlyArchived = true
	} else if r.URL.Query().Get("include_archived") == "true" {
		filters.IncludeArchived = true
	}

	if statusParam := r.URL.Query().Get("status"); statusParam != "" {
		for _, s := range strings.Split(statusParam, ",") {
			s = strings.TrimSpace(s)
			if s == "" {
				continue
			}
			status := models.SessionStatus(s)
			if err := status.Validate(); err != nil {
				writeError(w, r, http.StatusBadRequest, "INVALID_STATUS", "invalid status: "+s)
				return
			}
			filters.Statuses = append(filters.Statuses, status)
		}
	}

	if search := r.URL.Query().Get("search"); search != "" {
		filters.Search = search
	}

	if repoIDStr := r.URL.Query().Get("repository_id"); repoIDStr != "" {
		repoID, err := uuid.Parse(repoIDStr)
		if err != nil {
			writeError(w, r, http.StatusBadRequest, "INVALID_REPOSITORY_ID", "invalid repository_id")
			return
		}
		filters.RepositoryID = repoID
	}

	if userIDStr := r.URL.Query().Get("triggered_by_user_id"); userIDStr != "" {
		userID, err := uuid.Parse(userIDStr)
		if err != nil {
			writeError(w, r, http.StatusBadRequest, "INVALID_USER_ID", "invalid triggered_by_user_id")
			return
		}
		filters.TriggeredByUserID = userID
	}

	runs, err := h.runStore.ListByOrg(r.Context(), orgID, filters)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "LIST_FAILED", "failed to list runs", err)
		return
	}
	if runs == nil {
		runs = []models.Session{}
	}

	var nextCursor string
	if len(runs) > 0 && len(runs) == limit {
		last := runs[len(runs)-1]
		nextCursor = encodeSessionCursor(last.LastActivityAt, last.ID)
	}

	// Enrich sessions with last_viewed_at and PR summaries.
	items := make([]models.SessionListItem, len(runs))
	sessionIDs := make([]uuid.UUID, len(runs))
	for i, s := range runs {
		items[i] = models.SessionListItem{Session: s}
		sessionIDs[i] = s.ID
	}

	user := middleware.UserFromContext(r.Context())
	if user != nil && h.viewStore != nil && len(sessionIDs) > 0 {
		viewTimes, err := h.viewStore.BatchGetLastViewed(r.Context(), user.ID, sessionIDs)
		if err != nil {
			h.logger.Warn().Err(err).Msg("failed to fetch session view times")
		} else {
			for i, s := range runs {
				if t, ok := viewTimes[s.ID]; ok {
					items[i].LastViewedAt = &t
				}
			}
		}
	}

	if h.pullRequestStore != nil && len(sessionIDs) > 0 {
		prMap, err := h.pullRequestStore.BatchGetBySessionIDs(r.Context(), orgID, sessionIDs)
		if err != nil {
			h.logger.Warn().Err(err).Msg("failed to fetch PR summaries")
		} else {
			for i, s := range runs {
				if pr, ok := prMap[s.ID]; ok {
					items[i].PRSummary = &models.PRSummary{
						Status:   pr.Status,
						CIStatus: pr.CIStatus,
						Number:   pr.GitHubPRNumber,
						URL:      pr.GitHubPRURL,
					}
				}
			}
		}
	}

	writeJSON(w, http.StatusOK, models.ListResponse[models.SessionListItem]{
		Data: items,
		Meta: models.PaginationMeta{NextCursor: nextCursor},
	})
}

// Counts returns capped tab-badge counts for the sessions list. Bucket values
// that hit the cap indicate "at least cap" and should be rendered as e.g. 99+.
func (h *SessionHandler) Counts(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())

	filters := db.SessionCountsFilters{}

	if repoIDStr := r.URL.Query().Get("repository_id"); repoIDStr != "" {
		repoID, err := uuid.Parse(repoIDStr)
		if err != nil {
			writeError(w, r, http.StatusBadRequest, "INVALID_REPOSITORY_ID", "invalid repository_id")
			return
		}
		filters.RepositoryID = repoID
	}

	if userIDStr := r.URL.Query().Get("triggered_by_user_id"); userIDStr != "" {
		userID, err := uuid.Parse(userIDStr)
		if err != nil {
			writeError(w, r, http.StatusBadRequest, "INVALID_USER_ID", "invalid triggered_by_user_id")
			return
		}
		filters.TriggeredByUserID = userID
	}

	counts, err := h.runStore.CountsByOrg(r.Context(), orgID, filters)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "COUNTS_FAILED", "failed to compute session counts", err)
		return
	}

	writeJSON(w, http.StatusOK, models.SingleResponse[models.SessionCounts]{Data: counts})
}

func (h *SessionHandler) Get(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	runID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid run ID")
		return
	}

	run, err := h.runStore.GetByID(r.Context(), orgID, runID)
	if err != nil {
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "run not found")
		return
	}

	detail := models.SessionDetail{Session: run}
	if h.threadStore != nil {
		threads, err := h.threadStore.ListBySession(r.Context(), orgID, runID)
		if err != nil {
			zerolog.Ctx(r.Context()).Warn().Err(err).Str("session_id", runID.String()).Msg("failed to load threads for session")
		}
		if threads == nil {
			threads = []models.SessionThread{}
		}
		detail.Threads = threads
	} else {
		detail.Threads = []models.SessionThread{}
	}

	writeJSON(w, http.StatusOK, models.SingleResponse[models.SessionDetail]{Data: detail})
}

// RecordView records that the current user has viewed a session (for unread tracking).
func (h *SessionHandler) RecordView(w http.ResponseWriter, r *http.Request) {
	if h.viewStore == nil {
		writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
		return
	}

	orgID := middleware.OrgIDFromContext(r.Context())
	user := middleware.UserFromContext(r.Context())
	if user == nil {
		writeError(w, r, http.StatusUnauthorized, "UNAUTHORIZED", "user not found in context")
		return
	}

	sessionID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid session ID")
		return
	}

	if err := h.viewStore.Upsert(r.Context(), user.ID, sessionID, orgID); err != nil {
		h.logger.Warn().Err(err).Str("session_id", sessionID.String()).Msg("failed to record session view")
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// TriggerFix creates a new agent run for an issue and enqueues a run_agent job.
func (h *SessionHandler) TriggerFix(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	issueID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid issue ID")
		return
	}

	// Verify the issue exists.
	issue, err := h.issueStore.GetByID(r.Context(), orgID, issueID)
	if err != nil {
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "issue not found")
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
			writeError(w, r, http.StatusInternalServerError, "DEFAULT_AGENT_LOOKUP_FAILED", "failed to load organization settings", err)
			return
		}
		orgSettings, parseErr := models.ParseOrgSettings(org.Settings)
		if parseErr != nil {
			zerolog.Ctx(r.Context()).Warn().Err(parseErr).Msg("failed to parse org settings, using defaults")
		}
		agentType = orgSettings.DefaultAgentType
		if agentType == "" {
			agentType = models.DefaultDefaultAgentType
		}
	}
	if err := agentType.Validate(); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_AGENT_TYPE", err.Error())
		return
	}

	autonomyLevel := body.AutonomyLevel
	if autonomyLevel == "" {
		autonomyLevel = "semi"
	}
	// These values are enforced by chk_sessions_autonomy_level CHECK constraint.
	validAutonomyLevels := map[string]bool{"full": true, "semi": true, "supervised": true}
	if !validAutonomyLevels[autonomyLevel] {
		writeError(w, r, http.StatusBadRequest, "INVALID_AUTONOMY_LEVEL", "autonomy_level must be one of: full, semi, supervised")
		return
	}

	tokenMode := body.TokenMode
	if tokenMode == "" {
		tokenMode = "low"
	}
	validTokenModes := map[string]bool{"low": true, "high": true}
	if !validTokenModes[tokenMode] {
		writeError(w, r, http.StatusBadRequest, "INVALID_TOKEN_MODE", "token_mode must be one of: low, high")
		return
	}

	var triggeredByUserID *uuid.UUID
	if user := middleware.UserFromContext(r.Context()); user != nil {
		triggeredByUserID = &user.ID
	}

	run := &models.Session{
		IssueID:           issueID,
		OrgID:             orgID,
		AgentType:         agentType,
		Status:            "pending",
		AutonomyLevel:     autonomyLevel,
		TokenMode:         tokenMode,
		TriggeredByUserID: triggeredByUserID,
	}
	if err := h.runStore.Create(r.Context(), run); err != nil {
		writeError(w, r, http.StatusInternalServerError, "CREATE_FAILED", "failed to create agent run", err)
		return
	}

	// Generate a title from the issue for non-manual sessions.
	if h.llmClient != nil {
		titleInput := issue.Title
		if issue.Description != nil && len(*issue.Description) > 0 {
			desc := *issue.Description
			if len(desc) > 500 {
				desc = desc[:500] + "..."
			}
			titleInput += "\n\n" + desc
		}
		if err := h.generateSessionTitle(r.Context(), run, orgID, titleInput); err != nil {
			zerolog.Ctx(r.Context()).Warn().Err(err).Msg("failed to generate title for issue session")
		}
	}

	// Enqueue the run_agent job.
	payload := map[string]string{
		"session_id": run.ID.String(),
		"org_id":     orgID.String(),
	}
	if _, err := h.jobStore.Enqueue(r.Context(), orgID, "agent", "run_agent", payload, 5, nil); err != nil {
		writeError(w, r, http.StatusInternalServerError, "ENQUEUE_FAILED", "failed to enqueue agent run job", err)
		return
	}

	sessionIDStr := run.ID.String()
	emitUserAuditWithSession(
		h.audit,
		r,
		models.AuditActionSessionCreated,
		models.AuditResourceSession,
		&sessionIDStr,
		&run.ID,
		nil,
		sessionCreateAuditDetails(h.logger, run, &issue, nil),
	)
	writeJSON(w, http.StatusCreated, models.SingleResponse[models.Session]{Data: *run})
}

// RetrySession resets a failed session back to pending and re-enqueues it.
func (h *SessionHandler) RetrySession(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	sessionID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid session ID")
		return
	}

	if err := h.runStore.ResetForRetry(r.Context(), orgID, sessionID); err != nil {
		if errors.Is(err, db.ErrSessionNotFound) {
			writeError(w, r, http.StatusNotFound, "NOT_FOUND", "session not found")
		} else if errors.Is(err, db.ErrSessionNotFailed) {
			writeError(w, r, http.StatusConflict, "NOT_FAILED", "session is not in failed status")
		} else {
			writeError(w, r, http.StatusInternalServerError, "RETRY_FAILED", "failed to reset session for retry", err)
		}
		return
	}

	// Re-enqueue the run_agent job. If this fails, roll back the session status
	// so it doesn't get stuck in pending with no job to pick it up.
	payload := map[string]string{
		"session_id": sessionID.String(),
		"org_id":     orgID.String(),
	}
	if _, err := h.jobStore.Enqueue(r.Context(), orgID, "agent", "run_agent", payload, 5, nil); err != nil {
		if undoErr := h.runStore.UndoResetForRetry(r.Context(), orgID, sessionID, "Retry failed: could not enqueue job", ""); undoErr != nil {
			h.logger.Error().Err(undoErr).Str("session_id", sessionID.String()).Msg("failed to undo retry reset after enqueue failure")
		}
		writeError(w, r, http.StatusInternalServerError, "ENQUEUE_FAILED", "failed to enqueue agent run job", err)
		return
	}

	// Fetch the updated session to return.
	session, err := h.runStore.GetByID(r.Context(), orgID, sessionID)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "FETCH_FAILED", "failed to fetch updated session", err)
		return
	}

	sessionIDStr := sessionID.String()
	retryDetails := sessionAuditSnapshot(&session, nil, map[string]any{
		"job_type": "run_agent",
		"changes": map[string]any{
			"status": auditChange("failed", session.Status),
		},
	})
	emitUserAuditWithSession(h.audit, r, models.AuditActionSessionRetried, models.AuditResourceSession, &sessionIDStr, &sessionID, nil,
		marshalAuditDetails(h.logger, retryDetails))
	writeJSON(w, http.StatusOK, models.SingleResponse[models.Session]{Data: session})
}

// GetLogs returns all logs for a run as a JSON array.
// This is the primary endpoint for viewing historical logs for completed runs
// and also serves as the initial log fetch for active runs.
func (h *SessionHandler) GetLogs(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	runID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid run ID")
		return
	}

	// Verify run exists and belongs to org.
	_, err = h.runStore.GetByID(r.Context(), orgID, runID)
	if err != nil {
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "run not found")
		return
	}

	logs, err := h.logStore.ListByRunID(r.Context(), orgID, runID)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "LIST_FAILED", "failed to list logs", err)
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
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid run ID")
		return
	}

	// Verify run exists.
	run, err := h.runStore.GetByID(r.Context(), orgID, runID)
	if err != nil {
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "run not found")
		return
	}

	// For terminal runs, return existing logs as JSON instead of SSE
	// since there will be no new logs to stream.
	if isTerminalStatus(run.Status) {
		h.GetLogs(w, r)
		return
	}

	sw := sse.NewWriter(w)
	if sw == nil {
		writeError(w, r, http.StatusInternalServerError, "SSE_NOT_SUPPORTED", "streaming not supported")
		return
	}

	// Send existing logs.
	logs, err := h.logStore.ListByRunID(r.Context(), orgID, runID)
	if err != nil {
		return
	}

	var lastSeenID int64
	for _, log := range logs {
		if err := sw.WriteData(log); err != nil {
			zerolog.Ctx(r.Context()).Error().Err(err).Str("session_id", runID.String()).Msg("failed to write log event to SSE stream")
			return
		}
		lastSeenID = log.ID
	}

	// Send initial status event with the current session state.
	lastStatus := run.Status
	if err := sw.WriteEvent(sse.EventStatus, run); err != nil {
		zerolog.Ctx(r.Context()).Error().Err(err).Str("session_id", runID.String()).Msg("failed to write initial status event to SSE stream")
		return
	}
	sw.Flush()

	// Poll for new logs.
	ticker := time.NewTicker(1 * time.Second)
	defer ticker.Stop()
	// Send a keepalive comment on idle streams so intermediary proxies and
	// browsers don't time out connections that have no log traffic.
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()

	// Local shutdown channel so we can safely `select` even when no signal
	// has been wired up. A nil channel blocks forever, which is what we want
	// as a default.
	shutdownCh := h.shutdownCh

	for {
		select {
		case <-r.Context().Done():
			return
		case <-shutdownCh:
			// Server is shutting down — close the stream cleanly. The
			// EventSource client sees an EOF, fires onerror, and reconnects
			// to the new container via Caddy. Sending a final heartbeat
			// triggers a flush so the browser isn't blocked reading a
			// half-buffered chunk.
			_ = sw.WriteHeartbeat()
			sw.Flush()
			return
		case <-heartbeat.C:
			if err := sw.WriteHeartbeat(); err != nil {
				return
			}
			sw.Flush()
		case <-ticker.C:
			run, err := h.runStore.GetByID(r.Context(), orgID, runID)
			if err != nil {
				return
			}

			newLogs, err := h.logStore.ListByRunIDSince(r.Context(), orgID, runID, lastSeenID)
			if err != nil {
				return
			}
			for _, log := range newLogs {
				if err := sw.WriteData(log); err != nil {
					zerolog.Ctx(r.Context()).Error().Err(err).Str("session_id", runID.String()).Msg("failed to write log event to SSE stream")
					return
				}
				lastSeenID = log.ID
			}

			// Send a status event whenever the session status changes.
			if run.Status != lastStatus {
				lastStatus = run.Status
				if err := sw.WriteEvent(sse.EventStatus, run); err != nil {
					zerolog.Ctx(r.Context()).Error().Err(err).Str("session_id", runID.String()).Msg("failed to write status event to SSE stream")
					return
				}
			}

			sw.Flush()

			if isTerminalStatus(run.Status) {
				if err := sw.WriteEvent(sse.EventDone, run); err != nil {
					zerolog.Ctx(r.Context()).Error().Err(err).Str("session_id", runID.String()).Msg("failed to write done event to SSE stream")
					return
				}
				sw.Flush()
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
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid run ID")
		return
	}

	v, err := h.validationStore.GetBySessionID(r.Context(), orgID, runID)
	if err != nil {
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "validation not found")
		return
	}
	writeJSON(w, http.StatusOK, models.SingleResponse[models.Validation]{Data: v})
}

// GetPullRequest returns the PR associated with an agent run, or null if none exists.
// "No PR yet" is a normal empty state for an active session, not a missing resource,
// so we return 200 with a null body rather than 404.
func (h *SessionHandler) GetPullRequest(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	runID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid run ID")
		return
	}

	pr, err := h.pullRequestStore.GetBySessionID(r.Context(), orgID, runID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeJSON(w, http.StatusOK, models.SingleResponse[*models.PullRequest]{Data: nil})
			return
		}
		zerolog.Ctx(r.Context()).Error().Err(err).Str("session_id", runID.String()).Msg("failed to load PR for session")
		writeError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to load pull request", err)
		return
	}
	writeJSON(w, http.StatusOK, models.SingleResponse[*models.PullRequest]{Data: &pr})
}

// CreatePR handles POST /sessions/{id}/pr — enqueues a job that pushes the
// session's snapshot to GitHub and opens a pull request. The session must
// still have a snapshot and must not already have an associated PR. While a
// prior attempt is in flight (queued or pushing), returns 409 to prevent
// double-submits; a failed prior attempt may be retried.
func (h *SessionHandler) CreatePR(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	sessionID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid session ID")
		return
	}

	session, err := h.runStore.GetByID(r.Context(), orgID, sessionID)
	if err != nil {
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "session not found")
		return
	}

	if session.SnapshotKey == nil || *session.SnapshotKey == "" {
		writeError(w, r, http.StatusBadRequest, "SNAPSHOT_EXPIRED", "session state expired — re-run to create a PR")
		return
	}

	switch session.PRCreationState {
	case models.PRCreationStateQueued, models.PRCreationStatePushing:
		writeError(w, r, http.StatusConflict, "PR_IN_FLIGHT", "PR creation already in progress")
		return
	case models.PRCreationStateSucceeded:
		// Succeeded means a PR row should exist; fall through to the
		// PR_EXISTS check below so the 409 path is consistent.
	}

	// Check whether a PR already exists for this session.
	_, prErr := h.pullRequestStore.GetBySessionID(r.Context(), orgID, sessionID)
	if prErr == nil {
		writeError(w, r, http.StatusConflict, "PR_EXISTS", "a pull request already exists for this session")
		return
	}
	if !errors.Is(prErr, pgx.ErrNoRows) {
		writeError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to check for existing PR", prErr)
		return
	}
	if session.PRCreationState == models.PRCreationStateSucceeded {
		writeError(w, r, http.StatusConflict, "PR_ALREADY_CREATED", "PR creation already completed for this session")
		return
	}

	// Parse optional request body for per-PR overrides (e.g. draft).
	var req struct {
		Draft *bool `json:"draft,omitempty"`
	}
	if r.Body != nil && r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil && !errors.Is(err, io.EOF) {
			writeError(w, r, http.StatusBadRequest, "INVALID_BODY", "invalid request body")
			return
		}
	}

	payload := map[string]any{
		"session_id": sessionID.String(),
		"org_id":     orgID.String(),
	}
	if req.Draft != nil {
		payload["draft"] = *req.Draft
	}
	dedupeKey := fmt.Sprintf("open_pr:%s", sessionID)
	if _, err := h.jobStore.Enqueue(r.Context(), orgID, "agent", "open_pr", payload, 5, &dedupeKey); err != nil {
		writeError(w, r, http.StatusInternalServerError, "ENQUEUE_FAILED", "failed to enqueue PR creation job", err)
		return
	}

	if err := h.runStore.UpdatePRCreationState(r.Context(), orgID, sessionID, models.PRCreationStateQueued, ""); err != nil {
		zerolog.Ctx(r.Context()).Warn().Err(err).
			Str("session_id", sessionID.String()).
			Msg("failed to mark PR creation as queued")
	}

	sessionIDStr := sessionID.String()
	prDetails := sessionAuditSnapshot(&session, nil, map[string]any{
		"job_type": "open_pr",
	})
	if req.Draft != nil {
		prDetails["draft"] = *req.Draft
	}
	emitUserAuditWithSession(h.audit, r, models.AuditActionSessionPRRequested, models.AuditResourceSession, &sessionIDStr, &session.ID, nil,
		marshalAuditDetails(h.logger, prDetails))
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "queued"})
}

// ListQuestions returns the questions for an agent run.
func (h *SessionHandler) ListQuestions(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	runID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid run ID")
		return
	}

	questions, err := h.questionStore.ListByRunID(r.Context(), orgID, runID)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "LIST_FAILED", "failed to list questions", err)
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
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid question ID")
		return
	}

	user := middleware.UserFromContext(r.Context())
	if user == nil {
		writeError(w, r, http.StatusUnauthorized, "UNAUTHORIZED", "user not found")
		return
	}

	var body struct {
		Answer string `json:"answer"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_BODY", "invalid request body")
		return
	}
	if body.Answer == "" {
		writeError(w, r, http.StatusBadRequest, "MISSING_ANSWER", "answer is required")
		return
	}

	if err := h.questionStore.Answer(r.Context(), orgID, qID, body.Answer, user.ID); err != nil {
		writeError(w, r, http.StatusInternalServerError, "ANSWER_FAILED", "failed to answer question", err)
		return
	}

	question, err := h.questionStore.GetByID(r.Context(), orgID, qID)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "FETCH_FAILED", "failed to fetch updated question", err)
		return
	}

	qIDStr := qID.String()
	var sessionIDPtr *uuid.UUID
	if sessionID, parseErr := uuid.Parse(chi.URLParam(r, "id")); parseErr == nil {
		sessionIDPtr = &sessionID
	}
	questionDetails := map[string]any{
		"question_id":   question.ID.String(),
		"session_id":    question.SessionID.String(),
		"question_text": question.QuestionText,
		"status":        question.Status,
		"answer_length": len(body.Answer),
		"answered_by":   user.ID.String(),
		"option_count":  len(question.Options),
	}
	if question.BlocksPhase != nil {
		questionDetails["blocks_phase"] = *question.BlocksPhase
	}
	emitUserAuditWithSession(h.audit, r, models.AuditActionSessionQuestionAnswered, models.AuditResourceSession, &qIDStr, sessionIDPtr, nil,
		marshalAuditDetails(h.logger, questionDetails))
	writeJSON(w, http.StatusOK, models.SingleResponse[models.SessionQuestion]{Data: question})
}

// SendMessage handles POST /sessions/{id}/messages — sends a follow-up message
// to an idle multi-turn session and enqueues a continue_session job.
func (h *SessionHandler) SendMessage(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	sessionID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid session ID")
		return
	}

	if h.messageStore == nil {
		writeError(w, r, http.StatusNotImplemented, "NOT_CONFIGURED", "multi-turn sessions not configured")
		return
	}

	var body struct {
		Message  string   `json:"message"`
		Images   []string `json:"images"`
		PlanMode bool     `json:"plan_mode"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_BODY", "invalid request body")
		return
	}
	body.Message = strings.TrimSpace(body.Message)
	if body.Message == "" && len(body.Images) == 0 {
		writeError(w, r, http.StatusBadRequest, "MISSING_MESSAGE", "message or images are required")
		return
	}

	// When plan mode is requested, prefix the message so the orchestrator
	// can detect it and instruct the coding agent to plan instead of execute.
	if body.PlanMode {
		body.Message = "[PLAN_MODE]\n" + body.Message
	}

	// Look up the session to check its current status.
	session, err := h.runStore.GetByID(r.Context(), orgID, sessionID)
	if err != nil {
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "session not found")
		return
	}

	// Reject early if the session's sandbox snapshot has been destroyed
	// (expired after 30 days). The session can no longer be resumed.
	if session.SandboxState == string(models.SandboxStateDestroyed) {
		writeError(w, r, http.StatusGone, "SNAPSHOT_EXPIRED", "this session's environment has expired and can no longer be continued")
		return
	}
	if session.Status == string(models.SessionStatusAwaitingInput) && body.Message == "" {
		writeError(w, r, http.StatusBadRequest, "MISSING_ANSWER", "answer text is required when replying to a pending session question")
		return
	}

	// Build the user message from the request context.
	user := middleware.UserFromContext(r.Context())
	var userID *uuid.UUID
	if user != nil {
		userID = &user.ID
	}
	msg := &models.SessionMessage{
		SessionID:  sessionID,
		OrgID:      orgID,
		UserID:     userID,
		TurnNumber: session.CurrentTurn + 1,
		Role:       models.MessageRoleUser,
		Content:    body.Message,
	}
	if len(body.Images) > 0 {
		msg.Attachments = body.Images
	}

	// If the session is already running, just save the message — the coding
	// agent will buffer it and process inline. No status change or job needed.
	if session.Status == string(models.SessionStatusRunning) {
		if err := h.messageStore.Create(r.Context(), msg); err != nil {
			writeError(w, r, http.StatusInternalServerError, "CREATE_FAILED", "failed to create message", err)
			return
		}

		writeJSON(w, http.StatusCreated, models.SingleResponse[models.SessionMessage]{Data: *msg})
		return
	}

	tx, err := h.runStore.Begin(r.Context())
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "TX_BEGIN_FAILED", "failed to begin session transaction", err)
		return
	}
	committed := false
	defer func() {
		if committed {
			return
		}
		if rollbackErr := tx.Rollback(r.Context()); rollbackErr != nil {
			zerolog.Ctx(r.Context()).Error().Err(rollbackErr).Str("session_id", sessionID.String()).Msg("failed to rollback send message transaction")
		}
	}()

	txRunStore := db.NewSessionStore(tx)
	txMessageStore := db.NewSessionMessageStore(tx)
	txQuestionStore := db.NewSessionQuestionStore(tx)

	// Try claiming an idle session first, then fall back to resuming a
	// terminal session (completed/pr_created/failed/cancelled).
	var revertStatus string
	claimed, claimErr := txRunStore.ClaimIdle(r.Context(), orgID, sessionID)
	if claimErr != nil {
		claimed, claimErr = txRunStore.ClaimForResume(r.Context(), orgID, sessionID)
		if claimErr != nil {
			writeError(w, r, http.StatusConflict, "NOT_RESUMABLE", "session must be idle, running, awaiting input, need guidance, or otherwise resumable to send a message")
			return
		}
		revertStatus = session.Status // preserve original status for revert
	} else {
		revertStatus = string(models.SessionStatusIdle)
	}
	// Update turn number from the claimed session (may differ after status transition).
	session = claimed
	msg.TurnNumber = session.CurrentTurn + 1

	if err := txMessageStore.Create(r.Context(), msg); err != nil {
		writeError(w, r, http.StatusInternalServerError, "CREATE_FAILED", "failed to create message", err)
		return
	}

	// If the session was paused on a clarifying question, treat the follow-up
	// message as the answer so question state stays in sync with the resumed run.
	var answeredQuestion *models.SessionQuestion
	if revertStatus == string(models.SessionStatusAwaitingInput) && userID != nil && h.questionStore != nil {
		question, err := txQuestionStore.AnswerLatestPendingBySession(r.Context(), orgID, sessionID, body.Message, *userID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				zerolog.Ctx(r.Context()).Warn().Str("session_id", sessionID.String()).Msg("awaiting_input session resumed without a pending question to answer")
			} else {
				writeError(w, r, http.StatusInternalServerError, "ANSWER_FAILED", "failed to resolve pending session question", err)
				return
			}
		} else {
			answeredQuestion = &question
		}
	}

	// Enqueue continue_session job.
	payload := map[string]string{
		"session_id": sessionID.String(),
		"org_id":     orgID.String(),
	}
	if _, err := h.jobStore.EnqueueInTx(r.Context(), tx, orgID, "agent", "continue_session", payload, 5, nil); err != nil {
		writeError(w, r, http.StatusInternalServerError, "ENQUEUE_FAILED", "failed to enqueue continue_session job", err)
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, r, http.StatusInternalServerError, "TX_COMMIT_FAILED", "failed to commit session follow-up", err)
		return
	}
	committed = true

	if answeredQuestion != nil {
		qIDStr := answeredQuestion.ID.String()
		questionDetails := map[string]any{
			"question_id":   answeredQuestion.ID.String(),
			"session_id":    answeredQuestion.SessionID.String(),
			"question_text": answeredQuestion.QuestionText,
			"status":        answeredQuestion.Status,
			"answer_length": len(body.Message),
			"answered_by":   userID.String(),
			"option_count":  len(answeredQuestion.Options),
			"auto_answered": true,
		}
		if answeredQuestion.BlocksPhase != nil {
			questionDetails["blocks_phase"] = *answeredQuestion.BlocksPhase
		}
		emitUserAuditWithSession(h.audit, r, models.AuditActionSessionQuestionAnswered, models.AuditResourceSession, &qIDStr, &sessionID, nil,
			marshalAuditDetails(h.logger, questionDetails))
	}

	writeJSON(w, http.StatusCreated, models.SingleResponse[models.SessionMessage]{Data: *msg})
}

// ListMessages handles GET /sessions/{id}/messages — returns the conversation messages.
func (h *SessionHandler) ListMessages(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	sessionID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid session ID")
		return
	}

	if h.messageStore == nil {
		writeJSON(w, http.StatusOK, models.ListResponse[models.SessionMessage]{Data: []models.SessionMessage{}})
		return
	}

	// Verify session exists and belongs to org.
	_, err = h.runStore.GetByID(r.Context(), orgID, sessionID)
	if err != nil {
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "session not found")
		return
	}

	messages, err := h.messageStore.ListBySession(r.Context(), orgID, sessionID)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "LIST_FAILED", "failed to list messages", err)
		return
	}
	if messages == nil {
		messages = []models.SessionMessage{}
	}

	writeJSON(w, http.StatusOK, models.ListResponse[models.SessionMessage]{Data: messages})
}

// EndSession handles POST /sessions/{id}/end — explicitly ends an idle session.
func (h *SessionHandler) EndSession(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	sessionID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid session ID")
		return
	}

	session, err := h.runStore.GetByID(r.Context(), orgID, sessionID)
	if err != nil {
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "session not found")
		return
	}

	if session.Status != string(models.SessionStatusIdle) {
		writeError(w, r, http.StatusConflict, "NOT_IDLE", "only idle sessions can be ended")
		return
	}

	if err := h.runStore.UpdateStatus(r.Context(), orgID, sessionID, string(models.SessionStatusCompleted)); err != nil {
		writeError(w, r, http.StatusInternalServerError, "UPDATE_FAILED", "failed to end session", err)
		return
	}

	payload := map[string]string{
		"session_id": sessionID.String(),
		"org_id":     orgID.String(),
	}
	if session.TriggeredByUserID != nil {
		// Manual sessions skip validation — go straight to PR creation.
		dedupeKey := fmt.Sprintf("open_pr:%s", sessionID)
		if _, err := h.jobStore.Enqueue(r.Context(), orgID, "default", "open_pr", payload, 5, &dedupeKey); err != nil {
			writeError(w, r, http.StatusInternalServerError, "ENQUEUE_FAILED", "failed to enqueue PR creation", err)
			return
		}
		if err := h.runStore.UpdatePRCreationState(r.Context(), orgID, sessionID, models.PRCreationStateQueued, ""); err != nil {
			zerolog.Ctx(r.Context()).Warn().Err(err).
				Str("session_id", sessionID.String()).
				Msg("failed to mark PR creation as queued on session end")
		}
	} else {
		dedupeKey := fmt.Sprintf("validate:%s", sessionID)
		if _, err := h.jobStore.Enqueue(r.Context(), orgID, "agent", "validate", payload, 5, &dedupeKey); err != nil {
			writeError(w, r, http.StatusInternalServerError, "ENQUEUE_FAILED", "failed to enqueue validation", err)
			return
		}
	}

	// Snapshot cleanup is handled by the reaper, which will find this session
	// because it's now status=completed with sandbox_state=snapshotted.

	session.Status = string(models.SessionStatusCompleted)
	writeJSON(w, http.StatusOK, models.SingleResponse[models.Session]{Data: session})
}

// CancelSession handles POST /sessions/{id}/cancel — cancels a running session
// by signalling the orchestrator to send SIGINT to the agent process.
//
// The response returns the session in its current state (still "running").
// The orchestrator updates the status asynchronously once the agent exits —
// typically to "idle" (if snapshot succeeds) or "cancelled" (if not).
// The frontend should poll for the final status.
func (h *SessionHandler) CancelSession(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	sessionID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid session ID")
		return
	}

	session, err := h.runStore.GetByID(r.Context(), orgID, sessionID)
	if err != nil {
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "session not found")
		return
	}

	if session.Status != string(models.SessionStatusRunning) {
		writeError(w, r, http.StatusConflict, "NOT_RUNNING", "only running sessions can be cancelled")
		return
	}

	if h.canceller == nil {
		writeError(w, r, http.StatusServiceUnavailable, "CANCEL_UNAVAILABLE", "session cancellation is not available")
		return
	}

	// Signal the orchestrator to send SIGINT to the agent.
	// The orchestrator will update the session status asynchronously when the
	// agent execution terminates (to idle or cancelled).
	if !h.canceller.CancelSession(sessionID) {
		// The session is marked as running but isn't tracked in the cancel
		// registry. This can happen if the session just finished or the worker
		// is on a different node. Return 202 Accepted — the client should poll.
		h.logger.Warn().
			Str("session_id", sessionID.String()).
			Msg("cancel requested but session not found in local cancel registry")
	}

	// Return the session as-is (still "running"). The status will be updated
	// asynchronously by the orchestrator once the agent exits.
	writeJSON(w, http.StatusAccepted, models.SingleResponse[models.Session]{Data: session})
}

func isTerminalStatus(status string) bool {
	switch status {
	case "completed", "pr_created", "failed", "cancelled", "skipped":
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
		RepositoryID  string   `json:"repository_id"`
		Branch        string   `json:"branch"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_BODY", "invalid request body")
		return
	}

	body.Message = strings.TrimSpace(body.Message)
	if body.Message == "" {
		writeError(w, r, http.StatusBadRequest, "MISSING_MESSAGE", "message is required")
		return
	}

	// Resolve repository for the manual session so the orchestrator can
	// clone the codebase into the sandbox.
	var repoID *uuid.UUID
	if body.RepositoryID != "" {
		parsed, err := uuid.Parse(body.RepositoryID)
		if err != nil {
			writeError(w, r, http.StatusBadRequest, "INVALID_REPOSITORY_ID", "invalid repository_id")
			return
		}
		if _, err := requireActiveRepo(r.Context(), h.repoStore, orgID, parsed); err != nil {
			switch {
			case errors.Is(err, errRepoDisconnected):
				writeError(w, r, http.StatusBadRequest, "REPO_DISCONNECTED", "repository is disconnected; reconnect it to start new sessions")
			case errors.Is(err, errRepoStoreUnconfigured):
				writeError(w, r, http.StatusInternalServerError, "REPO_STORE_UNCONFIGURED", "repository lookup not configured")
			default:
				writeError(w, r, http.StatusNotFound, "REPOSITORY_NOT_FOUND", "repository not found")
			}
			return
		}
		repoID = &parsed
	}

	var targetBranch *string
	if body.Branch != "" {
		b := strings.TrimSpace(body.Branch)
		if !isValidGitRef(b) {
			writeError(w, r, http.StatusBadRequest, "INVALID_BRANCH", "branch contains invalid characters")
			return
		}
		targetBranch = &b
	}

	org, err := h.orgStore.GetByID(r.Context(), orgID)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "DEFAULT_AGENT_LOOKUP_FAILED", "failed to load organization settings", err)
		return
	}
	orgSettings, parseErr := models.ParseOrgSettings(org.Settings)
	if parseErr != nil {
		zerolog.Ctx(r.Context()).Warn().Err(parseErr).Msg("failed to parse org settings, using defaults")
	}

	agentType := models.AgentType(body.AgentType)
	if agentType == "" {
		agentType = orgSettings.DefaultAgentType
		if agentType == "" {
			agentType = models.DefaultDefaultAgentType
		}
	}
	if err := agentType.Validate(); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_AGENT_TYPE", err.Error())
		return
	}

	var modelOverride *string
	if body.Model != "" {
		if err := models.ValidateModelForAgentType(agentType, body.Model); err != nil {
			writeError(w, r, http.StatusBadRequest, "INVALID_MODEL", err.Error())
			return
		}
		modelOverride = &body.Model
	}

	autonomyLevel := body.AutonomyLevel
	if autonomyLevel == "" {
		autonomyLevel = "semi"
	}
	// These values are enforced by chk_sessions_autonomy_level CHECK constraint.
	validAutonomyLevels := map[string]bool{"full": true, "semi": true, "supervised": true}
	if !validAutonomyLevels[autonomyLevel] {
		writeError(w, r, http.StatusBadRequest, "INVALID_AUTONOMY_LEVEL", "autonomy_level must be one of: full, semi, supervised")
		return
	}

	tokenMode := body.TokenMode
	if tokenMode == "" {
		tokenMode = "low"
	}
	validTokenModes := map[string]bool{"low": true, "high": true}
	if !validTokenModes[tokenMode] {
		writeError(w, r, http.StatusBadRequest, "INVALID_TOKEN_MODE", "token_mode must be one of: low, high")
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
		writeError(w, r, http.StatusInternalServerError, "ENCODE_FAILED", "failed to encode manual session context", err)
		return
	}
	issue := &models.Issue{
		OrgID:        orgID,
		ExternalID:   "manual-" + now.UTC().Format("20060102150405") + "-" + strings.ReplaceAll(uuid.NewString(), "-", ""),
		Source:       models.IssueSourceManual,
		RepositoryID: repoID,
		Title:        title,
		Description:  &description,
		RawData:      rawData,
		Status:       "open",
		FirstSeenAt:  now,
		LastSeenAt:   now,
		Fingerprint:  fingerprint,
	}

	if err := h.issueStore.Upsert(r.Context(), issue); err != nil {
		writeError(w, r, http.StatusInternalServerError, "ISSUE_CREATE_FAILED", "failed to create manual issue", err)
		return
	}

	var manualTriggeredByUserID *uuid.UUID
	if user := middleware.UserFromContext(r.Context()); user != nil {
		manualTriggeredByUserID = &user.ID
	}

	session := &models.Session{
		IssueID:           issue.ID,
		OrgID:             orgID,
		AgentType:         agentType,
		Status:            "pending",
		AutonomyLevel:     autonomyLevel,
		TokenMode:         tokenMode,
		ModelOverride:     modelOverride,
		TriggeredByUserID: manualTriggeredByUserID,
		Title:             &title,
		PMApproach:        &title,
		TargetBranch:      targetBranch,
		RepositoryID:      repoID,
	}
	if err := h.runStore.Create(r.Context(), session); err != nil {
		writeError(w, r, http.StatusInternalServerError, "CREATE_FAILED", "failed to create manual session", err)
		return
	}

	// Persist the initial user message as a turn-0 record so that attachments
	// (uploaded images) are displayed alongside the prompt in the chat timeline.
	if h.messageStore != nil {
		initMsg := &models.SessionMessage{
			SessionID:  session.ID,
			OrgID:      orgID,
			TurnNumber: 0,
			Role:       models.MessageRoleUser,
			Content:    body.Message,
		}
		if user := middleware.UserFromContext(r.Context()); user != nil {
			initMsg.UserID = &user.ID
		}
		if len(body.Images) > 0 {
			initMsg.Attachments = body.Images
		}
		if err := h.messageStore.Create(r.Context(), initMsg); err != nil {
			zerolog.Ctx(r.Context()).Warn().Err(err).Msg("failed to create initial session message — continuing without it")
		}
	}

	// Check concurrency before enqueuing so the user gets immediate feedback.
	runningCount, err := h.runStore.CountRunningByOrg(r.Context(), orgID)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "CONCURRENCY_CHECK_FAILED", "failed to check running sessions", err)
		return
	}
	maxConcurrent := orgSettings.MaxConcurrentRuns
	if maxConcurrent <= 0 {
		maxConcurrent = models.DefaultMaxConcurrentRuns
	}
	if runningCount >= maxConcurrent {
		writeError(w, r, http.StatusTooManyRequests, "CONCURRENCY_LIMIT",
			fmt.Sprintf("Too many sessions running (%d/%d). Please wait for a session to finish before starting a new one.", runningCount, maxConcurrent))
		return
	}

	payload := map[string]string{
		"session_id": session.ID.String(),
		"org_id":     orgID.String(),
	}
	if _, err := h.jobStore.Enqueue(r.Context(), orgID, "agent", "run_agent", payload, 5, nil); err != nil {
		writeError(w, r, http.StatusInternalServerError, "ENQUEUE_FAILED", "failed to enqueue manual session", err)
		return
	}

	// Generate a concise session title via LLM (with a short timeout so the
	// request doesn't block for too long).
	if h.llmClient != nil {
		if err := h.generateSessionTitle(r.Context(), session, orgID, body.Message); err != nil {
			writeError(w, r, http.StatusInternalServerError, "TITLE_GENERATION_FAILED", "failed to generate session title", err)
			return
		}
	}

	manualSessionIDStr := session.ID.String()
	emitUserAuditWithSession(
		h.audit,
		r,
		models.AuditActionSessionCreated,
		models.AuditResourceSession,
		&manualSessionIDStr,
		&session.ID,
		nil,
		sessionCreateAuditDetails(h.logger, session, issue, map[string]any{
			"manual_session": true,
			"image_count":    len(body.Images),
		}),
	)
	writeJSON(w, http.StatusCreated, models.SingleResponse[models.Session]{Data: *session})
}

func (h *SessionHandler) generateSessionTitle(parent context.Context, session *models.Session, orgID uuid.UUID, message string) error {
	const titlePrompt = "You are a concise title generator. Given a user's task description, produce a short title (max 80 characters) that summarizes what needs to be done. Output ONLY the title, nothing else. No quotes, no punctuation at the end."

	ctx, cancel := context.WithTimeout(parent, 10*time.Second)
	defer cancel()

	generated, err := h.llmClient.Complete(ctx, titlePrompt, message)
	if err != nil {
		return fmt.Errorf("llm completion: %w", err)
	}

	title, ok := services.CleanTitle(generated)
	if !ok {
		return nil
	}

	if err := h.runStore.UpdateTitle(ctx, orgID, session.ID, title); err != nil {
		return fmt.Errorf("update title: %w", err)
	}
	session.Title = &title
	return nil
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

// ArchiveSession marks a session as archived, hiding it from default list views.
func (h *SessionHandler) ArchiveSession(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	user := middleware.UserFromContext(r.Context())
	if user == nil {
		writeError(w, r, http.StatusUnauthorized, "UNAUTHORIZED", "user not authenticated")
		return
	}
	sessionID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid session ID")
		return
	}

	var auditDetails json.RawMessage
	var auditLoadErr error
	var snapshotKey *string
	// Load the session once up front for archive auditing and snapshot cleanup.
	if h.audit != nil || h.snapshotStore != nil {
		session, err := h.runStore.GetByID(r.Context(), orgID, sessionID)
		if err != nil {
			if h.audit != nil {
				auditLoadErr = err
			}
		} else {
			snapshotKey = session.SnapshotKey
			if h.audit != nil {
				auditDetails = sessionArchiveAuditDetails(h.logger, &session, models.AuditActionSessionArchived, &user.ID)
			}
		}
	}

	if err := h.runStore.Archive(r.Context(), orgID, sessionID, user.ID); err != nil {
		if errors.Is(err, db.ErrSessionAlreadyArchived) {
			writeError(w, r, http.StatusNotFound, "NOT_FOUND", "session not found or already archived")
		} else {
			zerolog.Ctx(r.Context()).Error().
				Err(err).
				Str("session_id", sessionID.String()).
				Str("org_id", orgID.String()).
				Str("user_id", user.ID.String()).
				Msg("failed to archive session")
			writeError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to archive session", err)
		}
		return
	}
	if auditLoadErr != nil {
		zerolog.Ctx(r.Context()).Warn().
			Err(auditLoadErr).
			Str("session_id", sessionID.String()).
			Msg("failed to load session details for archive audit")
	}

	if h.snapshotStore != nil {
		if err := storage.CleanupSessionSnapshot(r.Context(), h.snapshotStore, h.runStore, orgID, sessionID, snapshotKey); err != nil {
			zerolog.Ctx(r.Context()).Warn().Err(err).
				Str("session_id", sessionID.String()).
				Msg("failed to clean up snapshot on session archive")
		}
	}

	sessionIDStr := sessionID.String()
	emitUserAuditWithSession(h.audit, r, models.AuditActionSessionArchived, models.AuditResourceSession, &sessionIDStr, &sessionID, nil, auditDetails)

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// UnarchiveSession removes the archived flag from a session.
func (h *SessionHandler) UnarchiveSession(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	sessionID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid session ID")
		return
	}

	var auditActorID *uuid.UUID
	if user := middleware.UserFromContext(r.Context()); user != nil {
		auditActorID = &user.ID
	}
	var auditDetails json.RawMessage
	var auditLoadErr error
	if h.audit != nil {
		session, err := h.runStore.GetByID(r.Context(), orgID, sessionID)
		if err != nil {
			auditLoadErr = err
		} else {
			auditDetails = sessionArchiveAuditDetails(h.logger, &session, models.AuditActionSessionUnarchived, auditActorID)
		}
	}

	if err := h.runStore.Unarchive(r.Context(), orgID, sessionID); err != nil {
		if errors.Is(err, db.ErrSessionNotArchived) {
			writeError(w, r, http.StatusNotFound, "NOT_FOUND", "session not found or not archived")
		} else {
			zerolog.Ctx(r.Context()).Error().
				Err(err).
				Str("session_id", sessionID.String()).
				Str("org_id", orgID.String()).
				Msg("failed to unarchive session")
			writeError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to unarchive session", err)
		}
		return
	}
	if auditLoadErr != nil {
		zerolog.Ctx(r.Context()).Warn().
			Err(auditLoadErr).
			Str("session_id", sessionID.String()).
			Msg("failed to load session details for unarchive audit")
	}

	sessionIDStr := sessionID.String()
	emitUserAuditWithSession(h.audit, r, models.AuditActionSessionUnarchived, models.AuditResourceSession, &sessionIDStr, &sessionID, nil, auditDetails)

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}

// gitRefPattern validates git ref names. Allows alphanumeric, dots, hyphens,
// underscores, and forward slashes (for namespaced branches like feature/foo).
var gitRefPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._/-]*$`)

// isValidGitRef checks whether s is a plausible git branch/ref name.
func isValidGitRef(s string) bool {
	if s == "" || len(s) > 255 {
		return false
	}
	if strings.Contains(s, "..") || strings.Contains(s, "~") || strings.Contains(s, "^") || strings.Contains(s, ":") || strings.Contains(s, " ") || strings.Contains(s, "\\") {
		return false
	}
	return gitRefPattern.MatchString(s)
}
