package handlers

import (
	"encoding/json"
	"net/http"
	"sort"
	"strings"
	"time"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

type SessionHandler struct {
	planStore     *db.PMPlanStore
	agentRunStore *db.AgentRunStore
}

func NewSessionHandler(planStore *db.PMPlanStore, agentRunStore *db.AgentRunStore) *SessionHandler {
	return &SessionHandler{planStore: planStore, agentRunStore: agentRunStore}
}

// List returns a merged list of PM-plan sessions and ad-hoc (manual) run sessions,
// sorted by created_at DESC.
func (h *SessionHandler) List(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	limit := queryInt(r, "limit", 50)

	// Fetch PM plans.
	plans, err := h.planStore.ListByOrg(r.Context(), orgID, db.PMPlanFilters{Limit: limit})
	if err != nil {
		plans = []models.PMPlan{}
	}

	// Fetch orphan runs (no PM plan).
	orphanRuns, err := h.agentRunStore.ListByOrg(r.Context(), orgID, db.AgentRunFilters{Limit: limit, NoPlanOnly: true})
	if err != nil {
		orphanRuns = []models.AgentRun{}
	}

	sessions := make([]models.AgentSession, 0, len(plans)+len(orphanRuns))

	for _, p := range plans {
		sessions = append(sessions, planToSession(p))
	}
	for _, run := range orphanRuns {
		sessions = append(sessions, runToSession(run))
	}

	// Sort merged list by created_at DESC.
	sort.Slice(sessions, func(i, j int) bool {
		return sessions[i].CreatedAt.After(sessions[j].CreatedAt)
	})

	// Trim to limit.
	if len(sessions) > limit {
		sessions = sessions[:limit]
	}

	writeJSON(w, http.StatusOK, models.ListResponse[models.AgentSession]{
		Data: sessions,
	})
}

// Get returns a single session by ID. It first tries to find a PM plan;
// if not found, it tries an orphan agent run.
func (h *SessionHandler) Get(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	sessionID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_ID", "invalid session ID")
		return
	}

	// Try PM plan first.
	plan, err := h.planStore.GetByID(r.Context(), orgID, sessionID)
	if err == nil {
		session := planToSession(plan)
		// Enrich tasks with run data.
		session.Tasks = h.enrichPlanTasks(r, orgID, plan)
		writeJSON(w, http.StatusOK, models.SingleResponse[models.AgentSession]{Data: session})
		return
	}

	// Try orphan agent run.
	run, err := h.agentRunStore.GetByID(r.Context(), orgID, sessionID)
	if err != nil {
		writeError(w, http.StatusNotFound, "NOT_FOUND", "session not found")
		return
	}

	session := runToSession(run)
	session.Tasks = []models.AgentSessionTask{runToTask(run, 1)}
	writeJSON(w, http.StatusOK, models.SingleResponse[models.AgentSession]{Data: session})
}

// planToSession converts a PMPlan to an AgentSession summary.
func planToSession(p models.PMPlan) models.AgentSession {
	triggeredBy := models.AgentSessionTriggeredByManual
	if p.TriggeredBy == models.PMTriggerCron {
		triggeredBy = models.AgentSessionTriggeredByScheduled
	}

	title := "PM Analysis"
	if p.Analysis != "" {
		title = truncate(p.Analysis, 200)
	}

	status := planStatusToSessionStatus(p.Status)

	// Parse tasks to compute summary counts.
	var tasks []pmTaskJSON
	_ = json.Unmarshal(p.Tasks, &tasks)

	var activeCount, completedCount, failedCount int
	for _, t := range tasks {
		switch {
		case t.Status == "delegated" && t.AgentRunID != "":
			activeCount++ // assume active unless we have run data
		case t.Status == "skipped_capacity":
			// not counted
		default:
			// pending
		}
	}

	issuesReviewed := p.IssuesReviewed

	return models.AgentSession{
		ID:                p.ID,
		Type:              models.AgentSessionTypePlan,
		Status:            status,
		TriggeredBy:       triggeredBy,
		Title:             title,
		Analysis:          strPtr(p.Analysis),
		Tasks:             pmTasksToSessionTasks(tasks),
		Clusters:          p.Clusters,
		SkippedIssues:     p.SkippedIssues,
		IssuesReviewed:    &issuesReviewed,
		TaskCount:         len(tasks),
		ActiveRunCount:    activeCount,
		CompletedRunCount: completedCount,
		FailedRunCount:    failedCount,
		CreatedAt:         p.CreatedAt,
		CompletedAt:       p.CompletedAt,
	}
}

// runToSession converts an orphan AgentRun to a single-task AgentSession.
func runToSession(run models.AgentRun) models.AgentSession {
	title := "Manual Run"
	if run.ResultSummary != nil && *run.ResultSummary != "" {
		title = *run.ResultSummary
	} else {
		title = "Run " + run.ID.String()[:8]
	}

	status := runStatusToSessionStatus(run.Status)

	var activeCount, completedCount, failedCount int
	switch status {
	case models.AgentSessionStatusActive:
		activeCount = 1
	case models.AgentSessionStatusCompleted:
		completedCount = 1
	case models.AgentSessionStatusFailed:
		failedCount = 1
	}

	return models.AgentSession{
		ID:                run.ID,
		Type:              models.AgentSessionTypeManual,
		Status:            status,
		TriggeredBy:       models.AgentSessionTriggeredByFixThis,
		Title:             title,
		Tasks:             []models.AgentSessionTask{runToTask(run, 1)},
		TaskCount:         1,
		ActiveRunCount:    activeCount,
		CompletedRunCount: completedCount,
		FailedRunCount:    failedCount,
		CreatedAt:         run.CreatedAt,
		CompletedAt:       run.CompletedAt,
	}
}

// enrichPlanTasks parses plan tasks and enriches them with live run data.
func (h *SessionHandler) enrichPlanTasks(r *http.Request, orgID uuid.UUID, plan models.PMPlan) []models.AgentSessionTask {
	var tasks []pmTaskJSON
	if err := json.Unmarshal(plan.Tasks, &tasks); err != nil {
		return nil
	}

	// Collect run IDs that need enrichment.
	var runIDs []uuid.UUID
	for _, t := range tasks {
		if t.AgentRunID != "" {
			if id, err := uuid.Parse(t.AgentRunID); err == nil {
				runIDs = append(runIDs, id)
			}
		}
	}

	// Fetch runs in one batch.
	runMap := make(map[string]models.AgentRun)
	if len(runIDs) > 0 {
		runs, err := h.agentRunStore.ListByIDs(r.Context(), orgID, runIDs)
		if err == nil {
			for _, run := range runs {
				runMap[run.ID.String()] = run
			}
		}
	}

	result := make([]models.AgentSessionTask, 0, len(tasks))
	for _, t := range tasks {
		st := models.AgentSessionTask{
			Rank:       t.Rank,
			Title:      t.Title,
			IssueIDs:   t.IssueIDs,
			Complexity: t.Complexity,
			Confidence: t.Confidence,
			Reasoning:  t.Reasoning,
			Approach:   t.Approach,
			Risk:       t.Risk,
			Status:     t.Status,
		}

		if t.AgentRunID != "" {
			st.AgentRunID = &t.AgentRunID
			if run, ok := runMap[t.AgentRunID]; ok {
				st.RunStatus = &run.Status
				st.RunResultSummary = run.ResultSummary
				st.RunConfidenceScore = run.ConfidenceScore
				if run.StartedAt != nil {
					s := run.StartedAt.Format(time.RFC3339)
					st.RunStartedAt = &s
				}
				if run.CompletedAt != nil {
					s := run.CompletedAt.Format(time.RFC3339)
					st.RunCompletedAt = &s
				}
			}
		}

		result = append(result, st)
	}
	return result
}

// pmTaskJSON is the shape of a task inside the pm_plans.tasks JSONB column.
type pmTaskJSON struct {
	Rank       int      `json:"rank"`
	IssueIDs   []string `json:"issue_ids"`
	Title      string   `json:"title"`
	Reasoning  string   `json:"reasoning"`
	Approach   string   `json:"approach"`
	Risk       string   `json:"risk"`
	Complexity string   `json:"complexity"`
	Confidence string   `json:"confidence"`
	AgentRunID string   `json:"agent_run_id,omitempty"`
	Status     string   `json:"status,omitempty"`
}

func pmTasksToSessionTasks(tasks []pmTaskJSON) []models.AgentSessionTask {
	result := make([]models.AgentSessionTask, 0, len(tasks))
	for _, t := range tasks {
		st := models.AgentSessionTask{
			Rank:       t.Rank,
			Title:      t.Title,
			IssueIDs:   t.IssueIDs,
			Complexity: t.Complexity,
			Confidence: t.Confidence,
			Reasoning:  t.Reasoning,
			Approach:   t.Approach,
			Risk:       t.Risk,
			Status:     t.Status,
		}
		if t.AgentRunID != "" {
			st.AgentRunID = &t.AgentRunID
		}
		result = append(result, st)
	}
	return result
}

func runToTask(run models.AgentRun, rank int) models.AgentSessionTask {
	runID := run.ID.String()
	t := models.AgentSessionTask{
		Rank:       rank,
		Title:      "Fix issue",
		IssueIDs:   []string{run.IssueID.String()},
		Status:     "delegated",
		AgentRunID: &runID,
		RunStatus:  &run.Status,
	}
	if run.ResultSummary != nil {
		t.Title = *run.ResultSummary
		t.RunResultSummary = run.ResultSummary
	}
	t.RunConfidenceScore = run.ConfidenceScore
	if run.StartedAt != nil {
		s := run.StartedAt.Format(time.RFC3339)
		t.RunStartedAt = &s
	}
	if run.CompletedAt != nil {
		s := run.CompletedAt.Format(time.RFC3339)
		t.RunCompletedAt = &s
	}
	return t
}

func planStatusToSessionStatus(s models.PMPlanStatus) models.AgentSessionStatus {
	switch s {
	case models.PMPlanStatusCompleted:
		return models.AgentSessionStatusCompleted
	case models.PMPlanStatusFailed:
		return models.AgentSessionStatusFailed
	default:
		return models.AgentSessionStatusActive
	}
}

func runStatusToSessionStatus(s string) models.AgentSessionStatus {
	switch s {
	case "completed", "pr_created", "skipped":
		return models.AgentSessionStatusCompleted
	case "failed", "cancelled":
		return models.AgentSessionStatusFailed
	default:
		return models.AgentSessionStatusActive
	}
}

func truncate(s string, maxLen int) string {
	// Prefer truncating at the first sentence boundary.
	if idx := strings.Index(s, ". "); idx > 0 && idx < maxLen {
		return s[:idx+1]
	}
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}

func strPtr(s string) *string {
	if s == "" {
		return nil
	}
	return &s
}
