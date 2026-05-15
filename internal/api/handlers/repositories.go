package handlers

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	ghservice "github.com/assembledhq/143/internal/services/github"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type RepositoryHandler struct {
	repoStore *db.RepositoryStore
	prService *ghservice.PRService
}

func NewRepositoryHandler(repoStore *db.RepositoryStore) *RepositoryHandler {
	return &RepositoryHandler{repoStore: repoStore}
}

func (h *RepositoryHandler) SetPRService(svc *ghservice.PRService) {
	h.prService = svc
}

func (h *RepositoryHandler) List(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	// Pickers (session/project/automation creation) are the dominant caller and
	// always want active repos only; the integrations settings page opts in with
	// ?include_disconnected=true so historical rows can still be displayed.
	filters := db.RepositoryFilters{
		IncludeDisconnected: r.URL.Query().Get("include_disconnected") == "true",
	}
	repos, err := h.repoStore.ListByOrg(r.Context(), orgID, filters)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "LIST_FAILED", "failed to list repositories", err)
		return
	}
	if repos == nil {
		repos = []models.Repository{}
	}
	writeJSON(w, http.StatusOK, models.ListResponse[models.Repository]{Data: repos})
}

func (h *RepositoryHandler) Get(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	repoID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid repository ID")
		return
	}

	repo, err := h.repoStore.GetByID(r.Context(), orgID, repoID)
	if err != nil {
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "repository not found")
		return
	}
	writeJSON(w, http.StatusOK, models.SingleResponse[models.Repository]{Data: repo})
}

func (h *RepositoryHandler) Summary(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	dbSummaries, err := h.repoStore.GetSummary(r.Context(), orgID)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "SUMMARY_FAILED", "failed to get repository summary", err)
		return
	}
	summaries := make([]models.RepoSummary, len(dbSummaries))
	for i, s := range dbSummaries {
		summaries[i] = models.RepoSummary{
			RepositoryID:        s.RepositoryID,
			FullName:            s.FullName,
			ActiveSessionCount:  s.ActiveSessionCount,
			LatestSessionStatus: s.LatestSessionStatus,
			ActiveProjectCount:  s.ActiveProjectCount,
		}
	}
	writeJSON(w, http.StatusOK, models.ListResponse[models.RepoSummary]{Data: summaries})
}

func (h *RepositoryHandler) Update(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	repoID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid repository ID")
		return
	}

	var req struct {
		Status   *string          `json:"status"`
		Settings *json.RawMessage `json:"settings"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_JSON", "invalid request body")
		return
	}

	repo, err := h.repoStore.GetByID(r.Context(), orgID, repoID)
	if err != nil {
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "repository not found")
		return
	}

	if req.Status != nil {
		repo.Status = *req.Status
	}
	if req.Settings != nil {
		// Validate PM settings if present.
		repoSettings, parseErr := models.ParseRepoSettings(*req.Settings)
		if parseErr != nil {
			writeError(w, r, http.StatusBadRequest, "INVALID_SETTINGS", "invalid settings JSON")
			return
		}
		if repoSettings.PM != nil {
			if err := models.ValidateRepoPMSettings(*repoSettings.PM); err != nil {
				writeError(w, r, http.StatusBadRequest, "INVALID_SETTINGS", err.Error())
				return
			}
		}
		repo.Settings = *req.Settings
	}

	if err := h.repoStore.Update(r.Context(), &repo); err != nil {
		writeError(w, r, http.StatusInternalServerError, "UPDATE_FAILED", "failed to update repository", err)
		return
	}
	writeJSON(w, http.StatusOK, models.SingleResponse[models.Repository]{Data: repo})
}

// Disconnect marks a single repository as disconnected without tearing down the
// parent GitHub integration. Existing sessions/runs/projects referencing the
// repo remain readable; new work is blocked by the guards in session/automation
// /project creation paths. The op is idempotent — calling it twice is a no-op.
func (h *RepositoryHandler) Disconnect(w http.ResponseWriter, r *http.Request) {
	h.setRepoStatus(w, r, models.RepositoryStatusDisconnected)
}

// Reconnect used to flip a user-disconnected repo back to active. Active GitHub
// ownership is now globally exclusive, so reactivation must go through the
// GitHub repository claim endpoint where user repo access and transfer conflicts
// are checked before the row becomes active again.
func (h *RepositoryHandler) Reconnect(w http.ResponseWriter, r *http.Request) {
	if _, err := uuid.Parse(chi.URLParam(r, "id")); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid repository ID")
		return
	}
	writeError(w, r, http.StatusConflict, "GITHUB_REPO_CLAIM_REQUIRED", "reactivate GitHub repositories from the integrations repository selection flow")
}

func (h *RepositoryHandler) setRepoStatus(w http.ResponseWriter, r *http.Request, status models.RepositoryStatus) {
	orgID := middleware.OrgIDFromContext(r.Context())
	repoID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid repository ID")
		return
	}

	repo, err := h.repoStore.SetStatus(r.Context(), orgID, repoID, status)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, r, http.StatusNotFound, "NOT_FOUND", "repository not found")
			return
		}
		writeError(w, r, http.StatusInternalServerError, "UPDATE_FAILED", "failed to update repository status", err)
		return
	}
	writeJSON(w, http.StatusOK, models.SingleResponse[models.Repository]{Data: repo})
}

func (h *RepositoryHandler) Delete(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	repoID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid repository ID")
		return
	}

	if err := h.repoStore.Delete(r.Context(), orgID, repoID); err != nil {
		writeError(w, r, http.StatusInternalServerError, "DELETE_FAILED", "failed to delete repository", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

type branchResponse struct {
	Name      string `json:"name"`
	Protected bool   `json:"protected"`
}

func (h *RepositoryHandler) ListBranches(w http.ResponseWriter, r *http.Request) {
	if h.prService == nil {
		writeError(w, r, http.StatusServiceUnavailable, "GITHUB_NOT_CONFIGURED", "GitHub App is not configured")
		return
	}

	orgID := middleware.OrgIDFromContext(r.Context())
	repoID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid repository ID")
		return
	}

	repo, err := h.repoStore.GetByID(r.Context(), orgID, repoID)
	if err != nil {
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "repository not found")
		return
	}
	if !repo.IsActive() {
		writeError(w, r, http.StatusBadRequest, "REPO_DISCONNECTED", "repository is disconnected; reconnect it to fetch branches")
		return
	}

	parts := strings.SplitN(repo.FullName, "/", 2)
	if len(parts) != 2 {
		writeError(w, r, http.StatusInternalServerError, "INVALID_REPO", "invalid repository full name")
		return
	}

	token, err := h.prService.GetInstallationToken(r.Context(), repo.InstallationID)
	if err != nil {
		writeError(w, r, http.StatusBadGateway, "GITHUB_TOKEN_FAILED", "failed to get GitHub token")
		return
	}

	ghBranches, err := h.prService.ListBranches(r.Context(), token, parts[0], parts[1])
	if err != nil {
		writeError(w, r, http.StatusBadGateway, "GITHUB_API_FAILED", "failed to list branches from GitHub")
		return
	}

	branches := make([]branchResponse, len(ghBranches))
	for i, b := range ghBranches {
		branches[i] = branchResponse{Name: b.Name, Protected: b.Protected}
	}
	writeJSON(w, http.StatusOK, models.ListResponse[branchResponse]{Data: branches})
}
