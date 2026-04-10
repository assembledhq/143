package handlers

import (
	"errors"
	"fmt"
	"math"
	"net/http"
	"time"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog"
)

type PMHandler struct {
	planStore        *db.PMPlanStore
	decisionLogStore *db.PMDecisionLogStore
	jobStore         *db.JobStore
	orgStore         *db.OrganizationStore
	pmDocStore       *db.PMDocumentStore // nil-safe: context endpoints disabled if nil
	sessionStore     *db.SessionStore    // nil-safe: failed session lookup disabled if nil
	audit            *db.AuditEmitter
}

// SetSessionStore injects the session store for failed PM session lookup.
func (h *PMHandler) SetSessionStore(store *db.SessionStore) {
	h.sessionStore = store
}

// SetAuditEmitter injects the audit emitter for logging PM events.
func (h *PMHandler) SetAuditEmitter(audit *db.AuditEmitter) {
	h.audit = audit
}

// SetPMDocumentStore injects the PM document store for bootstrap/refresh context endpoints.
func (h *PMHandler) SetPMDocumentStore(store *db.PMDocumentStore) {
	h.pmDocStore = store
}

func NewPMHandler(planStore *db.PMPlanStore, decisionLogStore *db.PMDecisionLogStore, jobStore *db.JobStore, orgStore *db.OrganizationStore) *PMHandler {
	return &PMHandler{planStore: planStore, decisionLogStore: decisionLogStore, jobStore: jobStore, orgStore: orgStore}
}

// Analyze enqueues a PM analysis job.
func (h *PMHandler) Analyze(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())

	payload := map[string]string{
		"org_id":  orgID.String(),
		"trigger": string(models.PMTriggerManual),
	}
	jobID, err := h.jobStore.Enqueue(r.Context(), orgID, "default", models.JobTypePMAnalyze, payload, 5, nil)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "ENQUEUE_FAILED", "failed to enqueue pm analyze job", err)
		return
	}

	emitUserAudit(h.audit, r, models.AuditActionPMAnalysisTriggered, models.AuditResourcePMPlan, nil, nil)
	writeJSON(w, http.StatusAccepted, map[string]any{
		"data": map[string]string{"job_id": jobID.String()},
	})
}

func (h *PMHandler) List(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())

	limit := queryInt(r, "limit", 50)
	filters := db.PMPlanFilters{
		Limit:  limit,
		Cursor: r.URL.Query().Get("cursor"),
	}

	plans, err := h.planStore.ListByOrg(r.Context(), orgID, filters)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "LIST_FAILED", "failed to list pm plans", err)
		return
	}
	if plans == nil {
		plans = []models.PMPlan{}
	}

	var nextCursor string
	if len(plans) > 0 && len(plans) == filters.Limit {
		nextCursor = db.FormatPMPlanCursor(plans[len(plans)-1])
	}

	writeJSON(w, http.StatusOK, models.ListResponse[models.PMPlan]{
		Data: plans,
		Meta: models.PaginationMeta{NextCursor: nextCursor},
	})
}

func (h *PMHandler) Get(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	planID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid plan ID")
		return
	}

	plan, err := h.planStore.GetByID(r.Context(), orgID, planID)
	if err != nil {
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "plan not found")
		return
	}
	writeJSON(w, http.StatusOK, models.SingleResponse[models.PMPlan]{Data: plan})
}

func (h *PMHandler) Latest(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())

	plan, err := h.planStore.GetLatestByOrg(r.Context(), orgID)
	if err != nil {
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "plan not found")
		return
	}
	writeJSON(w, http.StatusOK, models.SingleResponse[models.PMPlan]{Data: plan})
}

// Current returns the PM's latest recommendation in a presentation-friendly
// format, combining plan output with context stats and decision summary.
// This is the primary endpoint for the Autopilot page — it replaces direct
// plan access from the frontend.
func (h *PMHandler) Current(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())

	plan, err := h.planStore.GetLatestByOrg(r.Context(), orgID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, r, http.StatusNotFound, "NOT_FOUND", "no analysis available yet")
			return
		}
		zerolog.Ctx(r.Context()).Error().Err(err).Msg("failed to get latest PM plan")
		writeError(w, r, http.StatusInternalServerError, "INTERNAL", "failed to load analysis")
		return
	}

	summary, err := h.decisionLogStore.GetDecisionSummary(r.Context(), orgID)
	if err != nil {
		// Non-fatal — return recommendation without summary.
		zerolog.Ctx(r.Context()).Warn().Err(err).Msg("failed to get PM decision summary")
		summary = models.PMDecisionSummary{}
	}

	rec := models.PMCurrentRecommendation{
		Analysis:      plan.Analysis,
		Tasks:         plan.Tasks,
		Clusters:      plan.Clusters,
		SkippedIssues: plan.SkippedIssues,
		ContextStats: models.PMContextStats{
			IssuesReviewed:        plan.IssuesReviewed,
			InFlightRunsChecked:   plan.InFlightRunsChecked,
			PastOutcomesReviewed:  plan.PastOutcomesReviewed,
			RecentPRsChecked:      plan.RecentPRsChecked,
			PastDecisionsReviewed: plan.PastDecisionsReviewed,
			CommitsAnalyzed:       plan.CommitsAnalyzed,
		},
		DecisionSummary: summary,
		AnalyzedAt:      plan.CreatedAt,
		CompletedAt:     plan.CompletedAt,
		Status:          string(plan.Status),
		TriggeredBy:     string(plan.TriggeredBy),
	}

	writeJSON(w, http.StatusOK, models.SingleResponse[models.PMCurrentRecommendation]{Data: rec})
}

// decisionsResponse is the response for the decisions endpoint.
type decisionsResponse struct {
	Data    []models.PMDecisionView  `json:"data"`
	Summary models.PMDecisionSummary `json:"summary"`
	Meta    models.PaginationMeta    `json:"meta"`
}

// Decisions returns the PM decision history with success rate and project info.
func (h *PMHandler) Decisions(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())

	limit := queryInt(r, "limit", 50)

	filters := db.PMDecisionFilters{
		Limit:  limit,
		Cursor: r.URL.Query().Get("cursor"),
	}

	if dt := r.URL.Query().Get("decision_type"); dt != "" {
		decisionType := models.PMDecisionType(dt)
		if err := decisionType.Validate(); err != nil {
			writeError(w, r, http.StatusBadRequest, "VALIDATION_ERROR", "invalid decision_type: must be delegate, skip, or cluster")
			return
		}
		filters.DecisionType = &decisionType
	}

	if o := r.URL.Query().Get("outcome"); o != "" {
		outcome := models.PMDecisionOutcome(o)
		if err := outcome.Validate(); err != nil {
			writeError(w, r, http.StatusBadRequest, "VALIDATION_ERROR", "invalid outcome: must be succeeded, failed, or still_open")
			return
		}
		filters.Outcome = &outcome
	}

	decisions, err := h.decisionLogStore.ListDecisionViews(r.Context(), orgID, filters)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "LIST_FAILED", "failed to list pm decisions", err)
		return
	}
	if decisions == nil {
		decisions = []models.PMDecisionView{}
	}

	summary, err := h.decisionLogStore.GetDecisionSummary(r.Context(), orgID)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "SUMMARY_FAILED", "failed to get decision summary", err)
		return
	}

	var nextCursor string
	if len(decisions) > 0 && len(decisions) == limit {
		nextCursor = decisions[len(decisions)-1].ID.String()
	}

	writeJSON(w, http.StatusOK, decisionsResponse{
		Data:    decisions,
		Summary: summary,
		Meta:    models.PaginationMeta{NextCursor: nextCursor},
	})
}

// Status returns the PM agent's current state for the status banner.
func (h *PMHandler) Status(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())

	status := models.PMStatus{}

	// Get latest plan to determine last run and whether running.
	latestPlan, err := h.planStore.GetLatestByOrg(r.Context(), orgID)
	if err == nil {
		status.LastRunAt = &latestPlan.CreatedAt
		status.LastRunStatus = string(latestPlan.Status)
		status.IssuesReviewed = latestPlan.IssuesReviewed
		status.IsRunning = latestPlan.Status == models.PMPlanStatusExecuting
	}

	// Check for recent failed pm_analyze jobs. If the latest failed job is more
	// recent than the latest plan, surface the error so the user understands
	// why the PM agent isn't producing results.
	failedJob, err := h.jobStore.GetLatestFailedByType(r.Context(), orgID, models.JobTypePMAnalyze)
	if err == nil && failedJob != nil {
		// Only show the error if it's newer than the last successful plan.
		showError := status.LastRunAt == nil || failedJob.UpdatedAt.After(*status.LastRunAt)
		if showError {
			status.LastError = &failedJob.LastError
			status.LastFailedAt = &failedJob.UpdatedAt

			// Look up the latest failed PM session so the UI can link to its logs.
			if h.sessionStore != nil {
				sessionID, err := h.sessionStore.GetLatestFailedByAgentType(r.Context(), orgID, models.AgentTypePMAgent)
				if err == nil && sessionID != nil {
					sid := sessionID.String()
					status.LastFailedSessionID = &sid
				}
			}
		}
	}

	// Get decision success rate.
	summary, err := h.decisionLogStore.GetDecisionSummary(r.Context(), orgID)
	if err == nil {
		status.TotalDelegated = summary.TotalDelegated
		status.SuccessCount = summary.Succeeded
		if summary.TotalDelegated > 0 {
			status.SuccessRate = float64(summary.Succeeded) / float64(summary.TotalDelegated)
		}
	}

	// Compute next automatic run time from org settings (pm_schedule_hours).
	if status.LastRunAt != nil && !status.IsRunning {
		scheduleHours := models.DefaultPMScheduleHours
		if h.orgStore != nil {
			org, err := h.orgStore.GetByID(r.Context(), orgID)
			if err == nil {
				settings, parseErr := models.ParseOrgSettings(org.Settings)
				if parseErr != nil {
					zerolog.Ctx(r.Context()).Warn().Err(parseErr).Msg("failed to parse org settings, using defaults")
				}
				scheduleHours = settings.PMScheduleHours
			}
		}

		nextRunAt := status.LastRunAt.Add(time.Duration(scheduleHours) * time.Hour)
		status.NextRunAt = &nextRunAt

		remaining := time.Until(nextRunAt)
		if remaining <= 0 {
			nextRunIn := "soon"
			status.NextRunIn = &nextRunIn
		} else {
			hours := int(math.Floor(remaining.Hours()))
			mins := int(math.Floor(remaining.Minutes())) % 60
			var nextRunIn string
			if hours > 0 {
				nextRunIn = fmt.Sprintf("in %dh %dm", hours, mins)
			} else {
				nextRunIn = fmt.Sprintf("in %dm", mins)
			}
			status.NextRunIn = &nextRunIn
		}
	}

	writeJSON(w, http.StatusOK, models.SingleResponse[models.PMStatus]{Data: status})
}

// Bootstrap enqueues a PM context bootstrap job.
func (h *PMHandler) Bootstrap(w http.ResponseWriter, r *http.Request) {
	h.enqueueAndRespond(w, r, models.JobTypePMBootstrap)
}

// Refresh enqueues a PM context refresh job.
func (h *PMHandler) Refresh(w http.ResponseWriter, r *http.Request) {
	h.enqueueAndRespond(w, r, models.JobTypePMContextRefresh)
}

// enqueueAndRespond enqueues a job with the org ID payload and writes the accepted response.
func (h *PMHandler) enqueueAndRespond(w http.ResponseWriter, r *http.Request, jobType string) {
	orgID := middleware.OrgIDFromContext(r.Context())
	payload := map[string]string{"org_id": orgID.String()}
	dedupeKey := fmt.Sprintf("%s:%s", jobType, orgID.String())
	jobID, err := h.jobStore.Enqueue(r.Context(), orgID, "default", jobType, payload, 3, &dedupeKey)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "ENQUEUE_FAILED", fmt.Sprintf("failed to enqueue %s job", jobType), err)
		return
	}

	var auditAction models.AuditAction
	switch jobType {
	case models.JobTypePMBootstrap:
		auditAction = models.AuditActionPMBootstrapTriggered
	case models.JobTypePMContextRefresh:
		auditAction = models.AuditActionPMRefreshTriggered
	}
	if auditAction != "" {
		emitUserAudit(h.audit, r, auditAction, models.AuditResourcePMDocument, nil, nil)
	}

	writeJSON(w, http.StatusAccepted, map[string]any{
		"data": map[string]string{"job_id": jobID.String()},
	})
}

// ListPendingRefreshes returns pending PM context refresh suggestions.
func (h *PMHandler) ListPendingRefreshes(w http.ResponseWriter, r *http.Request) {
	if h.pmDocStore == nil {
		writeJSON(w, http.StatusOK, models.ListResponse[models.PMDocument]{Data: []models.PMDocument{}})
		return
	}

	orgID := middleware.OrgIDFromContext(r.Context())
	docs, err := h.pmDocStore.ListByOrgAndSourceType(r.Context(), orgID, models.PMDocSourceRefresh)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "LIST_FAILED", "failed to list pending refreshes", err)
		return
	}
	if docs == nil {
		docs = []models.PMDocument{}
	}

	writeJSON(w, http.StatusOK, models.ListResponse[models.PMDocument]{Data: docs})
}

// AcceptRefresh promotes a pending refresh doc to be the active autogenerated doc.
func (h *PMHandler) AcceptRefresh(w http.ResponseWriter, r *http.Request) {
	if h.pmDocStore == nil {
		writeError(w, r, http.StatusNotFound, "NOT_CONFIGURED", "pm documents not configured")
		return
	}

	orgID := middleware.OrgIDFromContext(r.Context())
	refreshID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid refresh ID")
		return
	}

	refreshDoc, err := h.pmDocStore.GetByID(r.Context(), orgID, refreshID)
	if err != nil {
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "refresh doc not found")
		return
	}
	if refreshDoc.SourceType != models.PMDocSourceRefresh {
		writeError(w, r, http.StatusBadRequest, "NOT_REFRESH", "document is not a pending refresh")
		return
	}

	activeDoc, err := h.pmDocStore.GetByOrgAndSourceType(r.Context(), orgID, models.PMDocSourceAutogenerated)
	if err != nil {
		writeError(w, r, http.StatusNotFound, "NO_ACTIVE_DOC", "no active autogenerated context doc found")
		return
	}

	// Update active doc and delete refresh doc in a single transaction.
	tx, err := h.pmDocStore.Begin(r.Context())
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "TX_FAILED", "failed to start transaction", err)
		return
	}
	defer tx.Rollback(r.Context()) //nolint:errcheck

	txStore := h.pmDocStore.WithTx(tx)
	activeDoc.Content = refreshDoc.Content
	now := time.Now()
	activeDoc.LastSyncedAt = &now
	if err := txStore.Update(r.Context(), &activeDoc); err != nil {
		writeError(w, r, http.StatusInternalServerError, "UPDATE_FAILED", "failed to update active doc", err)
		return
	}
	if err := txStore.Delete(r.Context(), orgID, refreshID); err != nil {
		writeError(w, r, http.StatusInternalServerError, "DELETE_FAILED", "failed to delete pending refresh", err)
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, r, http.StatusInternalServerError, "COMMIT_FAILED", "failed to commit transaction", err)
		return
	}

	refreshIDStr := refreshID.String()
	emitUserAudit(h.audit, r, models.AuditActionPMRefreshAccepted, models.AuditResourcePMDocument, &refreshIDStr, nil)

	writeJSON(w, http.StatusOK, models.SingleResponse[models.PMDocument]{Data: activeDoc})
}

// RejectRefresh deletes a pending refresh doc.
func (h *PMHandler) RejectRefresh(w http.ResponseWriter, r *http.Request) {
	if h.pmDocStore == nil {
		writeError(w, r, http.StatusNotFound, "NOT_CONFIGURED", "pm documents not configured")
		return
	}

	orgID := middleware.OrgIDFromContext(r.Context())
	refreshID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid refresh ID")
		return
	}

	// Verify it's actually a refresh doc before deleting.
	doc, err := h.pmDocStore.GetByID(r.Context(), orgID, refreshID)
	if err != nil {
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "refresh doc not found")
		return
	}
	if doc.SourceType != models.PMDocSourceRefresh {
		writeError(w, r, http.StatusBadRequest, "NOT_REFRESH", "document is not a pending refresh")
		return
	}

	if err := h.pmDocStore.Delete(r.Context(), orgID, refreshID); err != nil {
		writeError(w, r, http.StatusInternalServerError, "DELETE_FAILED", "failed to delete pending refresh", err)
		return
	}

	refreshIDStr := refreshID.String()
	emitUserAudit(h.audit, r, models.AuditActionPMRefreshRejected, models.AuditResourcePMDocument, &refreshIDStr, nil)

	w.WriteHeader(http.StatusNoContent)
}
