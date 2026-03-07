package handlers

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
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
	issueStore    *db.IssueStore
	orgStore      *db.OrganizationStore
	jobStore      *db.JobStore
	projectStore  *db.ProjectStore
}

func NewSessionHandler(
	planStore *db.PMPlanStore,
	agentRunStore *db.AgentRunStore,
	issueStore *db.IssueStore,
	orgStore *db.OrganizationStore,
	jobStore *db.JobStore,
) *SessionHandler {
	return &SessionHandler{
		planStore:     planStore,
		agentRunStore: agentRunStore,
		issueStore:    issueStore,
		orgStore:      orgStore,
		jobStore:      jobStore,
	}
}

// SetProjectStore adds the project store for project-grouping support.
func (h *SessionHandler) SetProjectStore(ps *db.ProjectStore) {
	h.projectStore = ps
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

	// Fetch ad-hoc runs (not linked to a PM plan).
	orphanRuns, err := h.agentRunStore.ListByOrg(r.Context(), orgID, db.AgentRunFilters{Limit: limit, AdHocOnly: true})
	if err != nil {
		orphanRuns = []models.AgentRun{}
	}

	sessions := make([]models.AgentSession, 0, len(plans)+len(orphanRuns))

	for _, p := range plans {
		sessions = append(sessions, planToSession(p))
	}

	runIssues := h.issueByIDMap(r, orgID, orphanRuns)
	for _, run := range orphanRuns {
		session := runToSessionWithIssue(run, runIssues[run.IssueID])
		sessions = append(sessions, session)
	}

	// Enrich sessions with project data when projectStore is available.
	if h.projectStore != nil {
		h.enrichSessionsWithProjects(r, orgID, sessions)
	}

	// Sort merged list by created_at DESC.
	sort.Slice(sessions, func(i, j int) bool {
		return sessions[i].CreatedAt.After(sessions[j].CreatedAt)
	})

	// Trim to limit.
	if len(sessions) > limit {
		sessions = sessions[:limit]
	}

	// Also return projects for frontend grouping.
	var projects []models.Project
	if h.projectStore != nil {
		projects, _ = h.projectStore.ListByOrg(r.Context(), orgID, db.ProjectFilters{Limit: 100})
	}
	if projects == nil {
		projects = []models.Project{}
	}

	writeJSON(w, http.StatusOK, sessionsListResponse{
		Data:     sessions,
		Projects: projects,
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

	issue, issueErr := h.issueStore.GetByID(r.Context(), orgID, run.IssueID)
	if issueErr == nil {
		session := runToSessionWithIssue(run, &issue)
		writeJSON(w, http.StatusOK, models.SingleResponse[models.AgentSession]{Data: session})
		return
	}

	session := runToSession(run)
	writeJSON(w, http.StatusOK, models.SingleResponse[models.AgentSession]{Data: session})
}

type createManualSessionRequest struct {
	Message       string   `json:"message"`
	Images        []string `json:"images"`
	AgentType     string   `json:"agent_type"`
	AutonomyLevel string   `json:"autonomy_level"`
	TokenMode     string   `json:"token_mode"`
}

func (h *SessionHandler) CreateManual(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())

	var body createManualSessionRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", "invalid request body")
		return
	}

	body.Message = strings.TrimSpace(body.Message)
	if body.Message == "" {
		writeError(w, http.StatusBadRequest, "MISSING_MESSAGE", "message is required")
		return
	}

	agentType := body.AgentType
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
	validAgentTypes := map[string]bool{"claude_code": true, "gemini_cli": true, "codex": true}
	if !validAgentTypes[agentType] {
		writeError(w, http.StatusBadRequest, "INVALID_AGENT_TYPE", "agent_type must be one of: claude_code, gemini_cli, codex")
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

	run := &models.AgentRun{
		IssueID:       issue.ID,
		OrgID:         orgID,
		AgentType:     agentType,
		Status:        "pending",
		AutonomyLevel: autonomyLevel,
		TokenMode:     tokenMode,
	}
	if err := h.agentRunStore.Create(r.Context(), run); err != nil {
		writeError(w, http.StatusInternalServerError, "CREATE_FAILED", "failed to create manual run")
		return
	}

	payload := map[string]string{
		"agent_run_id": run.ID.String(),
		"org_id":       orgID.String(),
	}
	if _, err := h.jobStore.Enqueue(r.Context(), orgID, "agent", "run_agent", payload, 5, nil); err != nil {
		writeError(w, http.StatusInternalServerError, "ENQUEUE_FAILED", "failed to enqueue manual run")
		return
	}

	session := runToSessionWithIssue(*run, issue)
	writeJSON(w, http.StatusCreated, models.SingleResponse[models.AgentSession]{Data: session})
}

// sessionsListResponse extends the standard list response with project data for grouping.
type sessionsListResponse struct {
	Data     []models.AgentSession `json:"data"`
	Projects []models.Project      `json:"projects"`
	Meta     models.PaginationMeta `json:"meta"`
}

// enrichSessionsWithProjects attempts to link sessions to projects via project_cycles.
func (h *SessionHandler) enrichSessionsWithProjects(r *http.Request, orgID uuid.UUID, sessions []models.AgentSession) {
	if h.projectStore == nil || len(sessions) == 0 {
		return
	}

	// Fetch all projects for this org (already fetched in List, but needed here for mapping).
	projects, err := h.projectStore.ListByOrg(r.Context(), orgID, db.ProjectFilters{Limit: 100})
	if err != nil || len(projects) == 0 {
		return
	}

	// Build a map of project ID to title for quick lookups.
	projectTitleMap := make(map[uuid.UUID]string, len(projects))
	for _, p := range projects {
		projectTitleMap[p.ID] = p.Title
	}

	// For PM-proposed projects, map the source issue IDs to projects.
	// Sessions with issues that belong to a project get tagged.
	for i := range sessions {
		if sessions[i].Type == models.AgentSessionTypePlan {
			// Plan sessions can be linked to projects via project_cycles (pm_plan_id).
			// For now, we leave project linking to the frontend which has the projects list.
			continue
		}
	}
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
		case t.Status == string(models.PMTaskStatusDelegated) && t.AgentRunID != "":
			activeCount++ // assume active unless we have run data
		case t.Status == string(models.PMTaskStatusSkippedCapacity):
			// not counted
		default:
			// pending
		}
	}

	issuesReviewed := p.IssuesReviewed
	inFlightRunsChecked := p.InFlightRunsChecked
	pastOutcomesReviewed := p.PastOutcomesReviewed
	recentPRsChecked := p.RecentPRsChecked
	pastDecisionsReviewed := p.PastDecisionsReviewed
	commitsAnalyzed := p.CommitsAnalyzed

	return models.AgentSession{
		ID:                    p.ID,
		Type:                  models.AgentSessionTypePlan,
		Status:                status,
		TriggeredBy:           triggeredBy,
		Title:                 title,
		Analysis:              strPtr(p.Analysis),
		Tasks:                 pmTasksToSessionTasks(tasks),
		Clusters:              p.Clusters,
		SkippedIssues:         p.SkippedIssues,
		IssuesReviewed:        &issuesReviewed,
		TaskCount:             len(tasks),
		ActiveRunCount:        activeCount,
		CompletedRunCount:     completedCount,
		FailedRunCount:        failedCount,
		CreatedAt:             p.CreatedAt,
		CompletedAt:           p.CompletedAt,
		InFlightRunsChecked:   &inFlightRunsChecked,
		PastOutcomesReviewed:  &pastOutcomesReviewed,
		RecentPRsChecked:      &recentPRsChecked,
		PastDecisionsReviewed: &pastDecisionsReviewed,
		CommitsAnalyzed:       &commitsAnalyzed,
	}
}

// runToSession converts an orphan AgentRun to a single-task AgentSession.
func runToSession(run models.AgentRun) models.AgentSession {
	return runToSessionWithIssue(run, nil)
}

func runToSessionWithIssue(run models.AgentRun, issue *models.Issue) models.AgentSession {
	var title string
	if run.ResultSummary != nil && *run.ResultSummary != "" {
		title = *run.ResultSummary
	} else if issue != nil && issue.Title != "" {
		title = issue.Title
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

	triggeredBy := models.AgentSessionTriggeredByFixThis
	if issue != nil && issue.Source == "manual" {
		triggeredBy = models.AgentSessionTriggeredByManual
	}

	var analysis *string
	if issue != nil && issue.Description != nil {
		analysis = issue.Description
	}

	return models.AgentSession{
		ID:                run.ID,
		Type:              models.AgentSessionTypeManual,
		Status:            status,
		TriggeredBy:       triggeredBy,
		Title:             title,
		Analysis:          analysis,
		Tasks:             []models.AgentSessionTask{runToTask(run, 1, title)},
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
			Complexity: models.PMTaskComplexity(t.Complexity),
			Confidence: models.PMTaskConfidence(t.Confidence),
			Reasoning:  t.Reasoning,
			Approach:   t.Approach,
			Risk:       t.Risk,
			Status:     models.PMTaskStatus(t.Status),
		}

		if t.AgentRunID != "" {
			st.AgentRunID = &t.AgentRunID
			if run, ok := runMap[t.AgentRunID]; ok {
				runStatus := models.AgentRunStatus(run.Status)
				st.RunStatus = &runStatus
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
			Complexity: models.PMTaskComplexity(t.Complexity),
			Confidence: models.PMTaskConfidence(t.Confidence),
			Reasoning:  t.Reasoning,
			Approach:   t.Approach,
			Risk:       t.Risk,
			Status:     models.PMTaskStatus(t.Status),
		}
		if t.AgentRunID != "" {
			st.AgentRunID = &t.AgentRunID
		}
		result = append(result, st)
	}
	return result
}

func runToTask(run models.AgentRun, rank int, fallbackTitle string) models.AgentSessionTask {
	runID := run.ID.String()
	runStatus := models.AgentRunStatus(run.Status)
	t := models.AgentSessionTask{
		Rank:       rank,
		Title:      fallbackTitle,
		IssueIDs:   []string{run.IssueID.String()},
		Status:     models.PMTaskStatusDelegated,
		AgentRunID: &runID,
		RunStatus:  &runStatus,
	}
	if strings.TrimSpace(fallbackTitle) == "" {
		t.Title = "Fix issue"
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

func (h *SessionHandler) issueByIDMap(r *http.Request, orgID uuid.UUID, runs []models.AgentRun) map[uuid.UUID]*models.Issue {
	result := map[uuid.UUID]*models.Issue{}
	if len(runs) == 0 {
		return result
	}

	issueIDs := make([]uuid.UUID, 0, len(runs))
	seen := make(map[uuid.UUID]bool)
	for _, run := range runs {
		if seen[run.IssueID] {
			continue
		}
		seen[run.IssueID] = true
		issueIDs = append(issueIDs, run.IssueID)
	}

	issues, err := h.issueStore.ListByIDs(r.Context(), orgID, issueIDs)
	if err != nil {
		return result
	}

	for i := range issues {
		issue := issues[i]
		result[issue.ID] = &issue
	}

	return result
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
	switch models.AgentRunStatus(s) {
	case models.AgentRunStatusCompleted, models.AgentRunStatusPRCreated, models.AgentRunStatusSkipped:
		return models.AgentSessionStatusCompleted
	case models.AgentRunStatusFailed, models.AgentRunStatusCancelled:
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
