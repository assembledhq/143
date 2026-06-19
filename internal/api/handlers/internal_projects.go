package handlers

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/assembledhq/143/internal/auth"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	pm "github.com/assembledhq/143/internal/services/pm"
)

// maxProposalsPerRepoPerRun caps how many proposals a single PM run can create per repo.
const maxProposalsPerRepoPerRun = 1

// maxOpenProposalsPerRepo caps total open proposals per repo.
const maxOpenProposalsPerRepo = 3

// InternalProjectHandler handles project proposal creation from sandbox agents
// via internal API tokens.
type InternalProjectHandler struct {
	txStarter        db.TxStarter
	projectStore     *db.ProjectStore
	projectTaskStore *db.ProjectTaskStore
	repoStore        *db.RepositoryStore
	signingSecret    string
	logger           zerolog.Logger

	// perTokenRepoCount tracks how many proposals each token has created per repo.
	// Keyed by hash(token):repoID. This is intentionally in-memory: counters
	// reset on server restart, which is acceptable because PM run tokens are
	// short-lived and a restart mid-run is rare. The hard cap per repo
	// (maxOpenProposalsPerRepo, checked via DB) provides durable protection.
	//
	// Eviction is time-bucketed: entries older than rateLimiterWindow are
	// dropped on each access rather than clearing the entire map at a size
	// threshold, avoiding thundering-herd cache misses.
	perTokenMu        sync.Mutex
	perTokenRepoCount map[string]rateLimiterEntry
}

// NewInternalProjectHandler creates a handler for internal project proposal creation.
func NewInternalProjectHandler(
	txStarter db.TxStarter,
	projectStore *db.ProjectStore,
	projectTaskStore *db.ProjectTaskStore,
	repoStore *db.RepositoryStore,
	signingSecret string,
	logger zerolog.Logger,
) *InternalProjectHandler {
	return &InternalProjectHandler{
		txStarter:         txStarter,
		projectStore:      projectStore,
		projectTaskStore:  projectTaskStore,
		repoStore:         repoStore,
		signingSecret:     signingSecret,
		logger:            logger,
		perTokenRepoCount: make(map[string]rateLimiterEntry),
	}
}

// rateLimiterEntry pairs a count with a timestamp for time-bucketed eviction.
type rateLimiterEntry struct {
	count     int
	createdAt time.Time
}

// rateLimiterWindow is how long rate limiter entries survive before eviction.
const rateLimiterWindow = 10 * time.Minute

type proposeProjectRequest struct {
	RepositoryID       string             `json:"repository_id"`
	Title              string             `json:"title"`
	Goal               string             `json:"goal"`
	Scope              *string            `json:"scope,omitempty"`
	CompletionCriteria *string            `json:"completion_criteria,omitempty"`
	Reasoning          string             `json:"reasoning"`
	SourceIssueIDs     []string           `json:"source_issue_ids,omitempty"`
	Priority           int                `json:"priority"`
	Tasks              []proposedTaskSpec `json:"tasks,omitempty"`
	SimilarProjectIDs  []string           `json:"similar_project_ids,omitempty"`
}

type proposedTaskSpec struct {
	Title       string  `json:"title"`
	Description *string `json:"description,omitempty"`
	Approach    *string `json:"approach,omitempty"`
	Complexity  *string `json:"complexity,omitempty"`
	Confidence  *string `json:"confidence,omitempty"`
}

type proposeProjectResponse struct {
	ID               string  `json:"id"`
	DuplicateWarning *string `json:"duplicate_warning,omitempty"`
}

// Propose handles POST /api/v1/internal/projects/propose.
func (h *InternalProjectHandler) Propose(w http.ResponseWriter, r *http.Request) {
	// 1. Authenticate via internal token.
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
		writeError(w, r, http.StatusForbidden, "TOOL_NOT_AVAILABLE", "project proposals are not available to automation goal improvement sessions")
		return
	}

	var req proposeProjectRequest
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&req); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_BODY", "invalid request body", err)
		return
	}

	// Validate required fields.
	if req.Title == "" {
		writeError(w, r, http.StatusBadRequest, "MISSING_TITLE", "title is required")
		return
	}
	if req.Goal == "" {
		writeError(w, r, http.StatusBadRequest, "MISSING_GOAL", "goal is required")
		return
	}
	if req.Reasoning == "" {
		writeError(w, r, http.StatusBadRequest, "MISSING_REASONING", "reasoning is required")
		return
	}

	// 2. Validate repository_id matches the token's repo scope.
	repoID, err := uuid.Parse(req.RepositoryID)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_REPOSITORY_ID", "invalid repository_id")
		return
	}

	if repoID != claims.RepoID {
		writeError(w, r, http.StatusForbidden, "REPO_MISMATCH",
			"token is not authorized for this repository")
		return
	}

	if _, err := requireActiveRepo(r.Context(), h.repoStore, claims.OrgID, repoID); err != nil {
		switch {
		case errors.Is(err, errRepoDisconnected):
			writeError(w, r, http.StatusBadRequest, "REPO_DISCONNECTED", "repository is disconnected; reconnect it to propose projects")
		case errors.Is(err, errRepoStoreUnconfigured):
			writeError(w, r, http.StatusInternalServerError, "REPO_STORE_UNCONFIGURED", "repository lookup not configured")
		default:
			writeError(w, r, http.StatusBadRequest, "INVALID_REPOSITORY", "repository not found in this organization")
		}
		return
	}

	// 3. Parse and validate source_issue_ids.
	var sourceIssueIDs []uuid.UUID
	for _, idStr := range req.SourceIssueIDs {
		id, err := uuid.Parse(idStr)
		if err != nil {
			writeError(w, r, http.StatusBadRequest, "INVALID_SOURCE_ISSUE_ID", fmt.Sprintf("invalid source_issue_id: %s", idStr))
			return
		}
		sourceIssueIDs = append(sourceIssueIDs, id)
	}

	// 4. Parse similar_project_ids.
	var similarProjectIDs []uuid.UUID
	for _, idStr := range req.SimilarProjectIDs {
		id, err := uuid.Parse(idStr)
		if err != nil {
			writeError(w, r, http.StatusBadRequest, "INVALID_SIMILAR_PROJECT_ID", fmt.Sprintf("invalid similar_project_id: %s", idStr))
			return
		}
		similarProjectIDs = append(similarProjectIDs, id)
	}
	// Verify similar_project_ids belong to the same org and repo.
	for _, spID := range similarProjectIDs {
		sp, err := h.projectStore.GetByID(r.Context(), claims.OrgID, spID)
		if err != nil {
			writeError(w, r, http.StatusBadRequest, "INVALID_SIMILAR_PROJECT", fmt.Sprintf("similar project %s not found", spID))
			return
		}
		if sp.RepositoryID == nil || *sp.RepositoryID != repoID {
			writeError(w, r, http.StatusBadRequest, "INVALID_SIMILAR_PROJECT", fmt.Sprintf("similar project %s belongs to a different repository", spID))
			return
		}
	}

	// 5. Rate limit: max 1 proposal per repo per PM run.
	tokenHash := hashTokenForProjects(tokenStr)
	rateKey := fmt.Sprintf("%s:%s", tokenHash, repoID.String())
	if !h.incrementAndCheckProposal(rateKey) {
		writeError(w, r, http.StatusTooManyRequests, "RATE_LIMITED",
			fmt.Sprintf("proposal limit reached (%d per repo per PM run)", maxProposalsPerRepoPerRun))
		return
	}

	// 6–12. All remaining work (cap check, dedup, project insert, seed tasks,
	// progress update) runs inside a single transaction with a repo-scoped
	// advisory lock to prevent races.
	ctx := r.Context()
	tx, err := h.txStarter.Begin(ctx)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "TX_FAILED", "failed to start transaction", err)
		return
	}
	defer tx.Rollback(ctx) //nolint:errcheck // best-effort on failure paths

	// Acquire a repo-scoped advisory lock so concurrent requests for the same
	// repo serialize. This prevents the cap and dedup checks from racing.
	advisoryKey := repoAdvisoryLockKey(repoID)
	if _, err := tx.Exec(ctx, "SELECT pg_advisory_xact_lock($1)", advisoryKey); err != nil {
		writeError(w, r, http.StatusInternalServerError, "LOCK_FAILED", "failed to acquire advisory lock", err)
		return
	}

	// Create tx-scoped stores so all reads/writes go through the transaction.
	txProjectStore := db.NewProjectStore(tx)
	txProjectTaskStore := db.NewProjectTaskStore(tx)

	// 6. Cap: max 3 open proposals per repo.
	pmTrue := true
	openCount, err := txProjectStore.Count(ctx, claims.OrgID, db.ProjectFilters{Status: string(models.ProjectStatusDraft), RepositoryID: repoID, ProposedByPM: &pmTrue})
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "COUNT_FAILED", "failed to count open proposals", err)
		return
	}
	if openCount >= maxOpenProposalsPerRepo {
		writeError(w, r, http.StatusConflict, "TOO_MANY_PROPOSALS",
			fmt.Sprintf("maximum open proposals per repo reached (%d)", maxOpenProposalsPerRepo))
		return
	}

	// 7. Run deduplication against existing same-repo projects.
	dedupStatuses := []string{string(models.ProjectStatusDraft), string(models.ProjectStatusActive)}
	existingProjects, err := txProjectStore.ListByOrgRepoStatuses(ctx, claims.OrgID, repoID, dedupStatuses)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "DEDUP_FAILED", "failed to fetch existing projects for dedup", err)
		return
	}

	dedupResult := pm.DeduplicateProposal(req.Title, sourceIssueIDs, req.Scope, existingProjects)

	if dedupResult.HardDuplicate {
		writeError(w, r, http.StatusConflict, "DUPLICATE_PROJECT_PROPOSAL",
			"proposed project is too similar to an existing project")
		return
	}

	// 8. Marshal similar projects metadata.
	similarProjects := dedupResult.SimilarProjects
	if similarProjects == nil {
		similarProjects = []models.ProposalOverlap{}
	}
	similarJSON, err := json.Marshal(similarProjects)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "MARSHAL_FAILED", "failed to marshal similar projects", err)
		return
	}

	// 9. Set priority default.
	priority := req.Priority
	if priority <= 0 {
		priority = 50
	}

	// 10. Create project row.
	project := models.Project{
		OrgID:              claims.OrgID,
		RepositoryID:       &repoID,
		Title:              req.Title,
		Goal:               req.Goal,
		Scope:              req.Scope,
		CompletionCriteria: req.CompletionCriteria,
		Status:             models.ProjectStatusDraft,
		Priority:           priority,
		ExecutionMode:      models.ProjectExecModeSequential,
		MaxConcurrent:      1,
		BaseBranch:         "main",
		ProposedByPM:       true,
		ProposalReasoning:  &req.Reasoning,
		SourceIssueIDs:     sourceIssueIDs,
		SimilarProjects:    similarJSON,
	}

	if err := txProjectStore.Create(ctx, &project); err != nil {
		writeError(w, r, http.StatusInternalServerError, "CREATE_FAILED", "failed to create project proposal", err)
		return
	}

	// 11. Create seed tasks — any failure rolls back the entire proposal.
	for i, taskSpec := range req.Tasks {
		if taskSpec.Title == "" {
			continue
		}
		task := models.ProjectTask{
			ProjectID:   project.ID,
			OrgID:       claims.OrgID,
			Title:       taskSpec.Title,
			Description: taskSpec.Description,
			Approach:    taskSpec.Approach,
			Complexity:  taskSpec.Complexity,
			Confidence:  taskSpec.Confidence,
			Status:      models.ProjectTaskStatusPending,
			BatchNumber: 0,
			SortOrder:   i,
		}
		if err := txProjectTaskStore.Create(ctx, &task); err != nil {
			writeError(w, r, http.StatusInternalServerError, "TASK_CREATE_FAILED",
				fmt.Sprintf("failed to create seed task %d", i), err)
			return
		}
	}

	// 12. Update progress counts.
	if len(req.Tasks) > 0 {
		if err := txProjectStore.UpdateProgress(ctx, claims.OrgID, project.ID); err != nil {
			writeError(w, r, http.StatusInternalServerError, "PROGRESS_UPDATE_FAILED",
				"failed to update project progress", err)
			return
		}
	}

	// Commit the transaction.
	if err := tx.Commit(ctx); err != nil {
		writeError(w, r, http.StatusInternalServerError, "COMMIT_FAILED", "failed to commit proposal", err)
		return
	}

	resp := proposeProjectResponse{
		ID:               project.ID.String(),
		DuplicateWarning: dedupResult.Warning,
	}

	h.logger.Info().
		Str("project_id", project.ID.String()).
		Str("repo_id", repoID.String()).
		Str("title", req.Title).
		Msg("PM agent created project proposal")

	writeJSON(w, http.StatusCreated, resp)
}

// incrementAndCheckProposal atomically increments the per-token-repo proposal count
// and returns true if the count is within the allowed limit.
// Entries older than rateLimiterWindow are evicted on each call, keeping memory
// bounded without a hard cap that clears everything at once.
func (h *InternalProjectHandler) incrementAndCheckProposal(key string) bool {
	h.perTokenMu.Lock()
	defer h.perTokenMu.Unlock()

	now := time.Now()
	cutoff := now.Add(-rateLimiterWindow)

	// Evict stale entries. Collect keys first to avoid modifying the map
	// during iteration, which can cause unpredictable behavior.
	var stale []string
	for k, v := range h.perTokenRepoCount {
		if v.createdAt.Before(cutoff) {
			stale = append(stale, k)
		}
	}
	for _, k := range stale {
		delete(h.perTokenRepoCount, k)
	}

	entry, ok := h.perTokenRepoCount[key]
	if !ok {
		entry = rateLimiterEntry{createdAt: now}
	}
	if entry.count >= maxProposalsPerRepoPerRun {
		return false
	}
	entry.count++
	h.perTokenRepoCount[key] = entry
	return true
}

// hashTokenForProjects returns a short hash of a token string for use as a map key.
func hashTokenForProjects(token string) string {
	h := sha256.Sum256([]byte(token))
	return hex.EncodeToString(h[:8])
}

// repoAdvisoryLockKey derives a stable int64 advisory lock key from a repo UUID.
// We use the first 8 bytes of the UUID interpreted as int64. The uint64→int64
// cast may overflow, which is acceptable — we only need a deterministic key,
// not a semantically meaningful number.
func repoAdvisoryLockKey(repoID uuid.UUID) int64 {
	return int64(binary.BigEndian.Uint64(repoID[:8])) // #nosec G115
}
