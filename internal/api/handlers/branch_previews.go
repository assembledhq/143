package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/metrics"
	"github.com/assembledhq/143/internal/models"
	ghservice "github.com/assembledhq/143/internal/services/github"
	"github.com/assembledhq/143/internal/services/preview"
)

type branchPreviewGitHub interface {
	GetInstallationToken(ctx context.Context, installationID int64) (string, error)
	ResolveBranchHead(ctx context.Context, token, owner, repo, branch string) (string, error)
	CommitExists(ctx context.Context, token, owner, repo, sha string) error
	GetPullRequestHead(ctx context.Context, token, owner, repo string, number int) (ghservice.PullRequestHead, error)
}

type BranchPreviewHandler struct {
	previews              *db.PreviewStore
	apiTokens             *db.PreviewAPITokenStore
	repos                 *db.RepositoryStore
	github                branchPreviewGitHub
	manager               *preview.Manager
	jobs                  *db.JobStore
	selector              *preview.WorkerSelector
	stopper               *preview.WorkerStopper
	baseURL               string
	previewOriginTemplate string
}

func NewBranchPreviewHandler(previews *db.PreviewStore, repos *db.RepositoryStore, github branchPreviewGitHub, manager *preview.Manager, baseURL, previewOriginTemplate string) *BranchPreviewHandler {
	return &BranchPreviewHandler{
		previews:              previews,
		repos:                 repos,
		github:                github,
		manager:               manager,
		baseURL:               strings.TrimRight(baseURL, "/"),
		previewOriginTemplate: previewOriginTemplate,
	}
}

func (h *BranchPreviewHandler) SetWorkerRuntime(jobs *db.JobStore, selector *preview.WorkerSelector) {
	h.jobs = jobs
	h.selector = selector
}

func (h *BranchPreviewHandler) SetStopper(stopper *preview.WorkerStopper) {
	h.stopper = stopper
}

func (h *BranchPreviewHandler) SetAPITokenStore(store *db.PreviewAPITokenStore) {
	h.apiTokens = store
}

type createBranchPreviewRequest struct {
	RepositoryID      uuid.UUID                  `json:"repository_id"`
	Branch            string                     `json:"branch"`
	CommitSHA         string                     `json:"commit_sha"`
	PreviewConfigName *string                    `json:"preview_config_name"`
	Source            *createBranchPreviewSource `json:"source"`
	TTLSeconds        *int64                     `json:"ttl_seconds"`
}

type createBranchPreviewSource struct {
	Type       models.PreviewSourceType `json:"type"`
	ExternalID string                   `json:"external_id"`
	URL        string                   `json:"url"`
}

type branchPreviewResponse struct {
	TargetID            uuid.UUID                      `json:"target_id"`
	PreviewID           *uuid.UUID                     `json:"preview_id"`
	RepositoryID        uuid.UUID                      `json:"repository_id,omitempty"`
	RepositoryFullName  string                         `json:"repository_full_name,omitempty"`
	Branch              string                         `json:"branch,omitempty"`
	CommitSHA           string                         `json:"commit_sha,omitempty"`
	PreviewConfigName   string                         `json:"preview_config_name,omitempty"`
	SourceType          models.PreviewSourceType       `json:"source_type,omitempty"`
	SourceURL           string                         `json:"source_url,omitempty"`
	Status              string                         `json:"status"`
	Error               string                         `json:"error,omitempty"`
	CurrentPhase        string                         `json:"current_phase,omitempty"`
	NewCommitsAvailable bool                           `json:"new_commits_available,omitempty"`
	LatestCommitSHA     string                         `json:"latest_commit_sha,omitempty"`
	GitHubBranchURL     string                         `json:"github_branch_url,omitempty"`
	PullRequestURL      string                         `json:"pull_request_url,omitempty"`
	StableURL           string                         `json:"stable_url"`
	PreviewURL          *string                        `json:"preview_url"`
	ExpiresAt           *time.Time                     `json:"expires_at"`
	Services            []models.PreviewService        `json:"services,omitempty"`
	Infrastructure      []models.PreviewInfrastructure `json:"infrastructure,omitempty"`
	Logs                []models.PreviewLog            `json:"logs,omitempty"`
}

type restartBranchPreviewRequest struct {
	StartLatest bool `json:"start_latest"`
}

type createPreviewAPITokenRequest struct {
	Name          string      `json:"name"`
	Scopes        []string    `json:"scopes"`
	RepositoryIDs []uuid.UUID `json:"repository_ids"`
}

type createPreviewAPITokenResponse struct {
	models.PreviewAPIToken
	Token string `json:"token"`
}

func (h *BranchPreviewHandler) Create(w http.ResponseWriter, r *http.Request) {
	if h.previews == nil || h.repos == nil {
		writeError(w, r, http.StatusInternalServerError, "PREVIEW_UNAVAILABLE", "preview service is not configured")
		return
	}

	orgID := middleware.OrgIDFromContext(r.Context())
	user := middleware.UserFromContext(r.Context())
	if user == nil {
		writeError(w, r, http.StatusUnauthorized, "UNAUTHORIZED", "authentication required")
		return
	}

	var req createBranchPreviewRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_JSON", "invalid request body")
		return
	}
	req.Branch = strings.TrimSpace(req.Branch)
	req.CommitSHA = strings.TrimSpace(req.CommitSHA)
	if req.RepositoryID == uuid.Nil || req.Branch == "" {
		writeError(w, r, http.StatusBadRequest, "INVALID_PREVIEW_TARGET", "repository_id and branch are required")
		return
	}
	if !previewTokenAllows(r.Context(), "previews:create", req.RepositoryID) {
		writeError(w, r, http.StatusForbidden, "PREVIEW_TOKEN_FORBIDDEN", "preview API token is not scoped to create previews for this repository")
		return
	}

	repo, err := h.repos.GetByID(r.Context(), orgID, req.RepositoryID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, r, http.StatusNotFound, "REPOSITORY_NOT_FOUND", "repository not found")
			return
		}
		writeError(w, r, http.StatusInternalServerError, "REPOSITORY_LOOKUP_FAILED", "failed to load repository", err)
		return
	}
	if !repo.IsActive() {
		writeError(w, r, http.StatusConflict, "REPOSITORY_DISCONNECTED", "repository is disconnected")
		return
	}

	owner, name, ok := strings.Cut(repo.FullName, "/")
	if !ok || owner == "" || name == "" {
		writeError(w, r, http.StatusInternalServerError, "INVALID_REPOSITORY", "repository full name is invalid")
		return
	}

	if h.github == nil {
		writeError(w, r, http.StatusServiceUnavailable, "GITHUB_NOT_CONFIGURED", "GitHub is not configured")
		return
	}
	if !previewTokenAllows(r.Context(), "previews:read", repo.ID) {
		writeError(w, r, http.StatusForbidden, "PREVIEW_TOKEN_FORBIDDEN", "preview API token is not scoped to read previews for this repository")
		return
	}
	token, tokenErr := h.github.GetInstallationToken(r.Context(), repo.InstallationID)
	if tokenErr != nil {
		writeError(w, r, http.StatusBadGateway, "GITHUB_TOKEN_FAILED", "failed to get GitHub token", tokenErr)
		return
	}
	if req.CommitSHA == "" {
		head, headErr := h.github.ResolveBranchHead(r.Context(), token, owner, name, req.Branch)
		if headErr != nil {
			writeError(w, r, http.StatusBadGateway, "BRANCH_HEAD_RESOLVE_FAILED", "failed to resolve branch head from GitHub", headErr)
			return
		}
		req.CommitSHA = head
	} else if err := h.github.CommitExists(r.Context(), token, owner, name, req.CommitSHA); err != nil {
		writeError(w, r, http.StatusBadGateway, "COMMIT_LOOKUP_FAILED", "failed to verify commit SHA in GitHub", err)
		return
	}

	sourceType := models.PreviewSourceTypeManual
	sourceID := ""
	sourceURL := ""
	if req.Source != nil {
		sourceType = req.Source.Type
		sourceID = strings.TrimSpace(req.Source.ExternalID)
		sourceURL = strings.TrimSpace(req.Source.URL)
	}
	if sourceType == "" {
		sourceType = models.PreviewSourceTypeManual
	}
	if err := sourceType.Validate(); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_PREVIEW_SOURCE", err.Error())
		return
	}

	configName := ""
	if req.PreviewConfigName != nil {
		configName = strings.TrimSpace(*req.PreviewConfigName)
	}
	var target *models.PreviewTarget
	restart := r.URL.Query().Get("restart") == "true"
	if idemKey := strings.TrimSpace(r.Header.Get("Idempotency-Key")); idemKey != "" {
		existing, idemErr := h.previews.GetPreviewTargetByIdempotencyKey(r.Context(), orgID, idemKey)
		if idemErr == nil && existing != nil {
			metrics.RecordBranchPreviewIdempotencyHit(r.Context(), "header")
			resp, startErr := h.startTargetRuntime(r.Context(), orgID, user.ID, repo, existing, req.TTLSeconds, restart)
			if startErr != nil {
				writePreviewHTTPError(w, r, startErr)
				return
			}
			writeJSON(w, http.StatusOK, models.SingleResponse[branchPreviewResponse]{Data: resp})
			return
		}
		if idemErr != nil && !errors.Is(idemErr, pgx.ErrNoRows) {
			writeError(w, r, http.StatusInternalServerError, "PREVIEW_IDEMPOTENCY_LOOKUP_FAILED", "failed to load preview idempotency key", idemErr)
			return
		}
		defer func() {
			if target != nil && target.ID != uuid.Nil {
				_ = h.previews.UpsertPreviewIdempotencyKey(context.Background(), orgID, idemKey, target.ID)
			}
		}()
	}
	if sourceID != "" {
		existing, sourceErr := h.previews.GetPreviewTargetBySource(r.Context(), orgID, sourceType, sourceID)
		if sourceErr == nil && existing != nil {
			metrics.RecordBranchPreviewIdempotencyHit(r.Context(), "source")
			resp, startErr := h.startTargetRuntime(r.Context(), orgID, user.ID, repo, existing, req.TTLSeconds, restart)
			if startErr != nil {
				writePreviewHTTPError(w, r, startErr)
				return
			}
			writeJSON(w, http.StatusOK, models.SingleResponse[branchPreviewResponse]{Data: resp})
			return
		}
		if sourceErr != nil && !errors.Is(sourceErr, pgx.ErrNoRows) {
			writeError(w, r, http.StatusInternalServerError, "PREVIEW_SOURCE_LOOKUP_FAILED", "failed to load preview source", sourceErr)
			return
		}
	}
	target = &models.PreviewTarget{
		OrgID:             orgID,
		RepositoryID:      repo.ID,
		Branch:            req.Branch,
		CommitSHA:         req.CommitSHA,
		PreviewConfigName: configName,
		SourceType:        sourceType,
		SourceID:          sourceID,
		SourceURL:         sourceURL,
		CreatedByUserID:   user.ID,
	}
	if err := h.previews.CreatePreviewTarget(r.Context(), target); err != nil {
		writeError(w, r, http.StatusInternalServerError, "PREVIEW_TARGET_CREATE_FAILED", "failed to create preview target", err)
		return
	}
	metrics.RecordBranchPreviewCreate(r.Context(), string(sourceType), repo.FullName)

	resp, startErr := h.startTargetRuntime(r.Context(), orgID, user.ID, repo, target, req.TTLSeconds, restart)
	if startErr != nil {
		writePreviewHTTPError(w, r, startErr)
		return
	}
	writeJSON(w, http.StatusCreated, models.SingleResponse[branchPreviewResponse]{Data: resp})
}

func (h *BranchPreviewHandler) GetPullRequest(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	user := middleware.UserFromContext(r.Context())
	if user == nil {
		writeError(w, r, http.StatusUnauthorized, "UNAUTHORIZED", "authentication required")
		return
	}
	owner := strings.TrimSpace(chi.URLParam(r, "owner"))
	repoName := strings.TrimSpace(chi.URLParam(r, "repo"))
	number, err := parsePositiveInt(chi.URLParam(r, "number"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_PULL_REQUEST", "invalid pull request number")
		return
	}
	repo, err := h.repos.GetByFullName(r.Context(), orgID, owner+"/"+repoName)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, r, http.StatusNotFound, "REPOSITORY_NOT_FOUND", "repository not found")
			return
		}
		writeError(w, r, http.StatusInternalServerError, "REPOSITORY_LOOKUP_FAILED", "failed to load repository", err)
		return
	}
	if h.github == nil {
		writeError(w, r, http.StatusServiceUnavailable, "GITHUB_NOT_CONFIGURED", "GitHub is not configured")
		return
	}
	token, err := h.github.GetInstallationToken(r.Context(), repo.InstallationID)
	if err != nil {
		writeError(w, r, http.StatusBadGateway, "GITHUB_TOKEN_FAILED", "failed to get GitHub token", err)
		return
	}
	head, err := h.github.GetPullRequestHead(r.Context(), token, owner, repoName, number)
	if err != nil {
		writeError(w, r, http.StatusBadGateway, "PULL_REQUEST_LOOKUP_FAILED", "failed to resolve pull request head", err)
		return
	}
	slug := fmt.Sprintf("github/%s/%s/pull/%d", owner, repoName, number)
	metrics.RecordStablePreviewLinkOpen(r.Context(), string(models.PreviewLinkTypePullRequest), false)
	target, err := h.previews.GetLatestPreviewTargetForBranch(r.Context(), orgID, repo.ID, head.Branch, "")
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		writeError(w, r, http.StatusInternalServerError, "PREVIEW_TARGET_LOOKUP_FAILED", "failed to load PR preview target", err)
		return
	}
	var active *models.PreviewInstance
	if target != nil {
		if existing, activeErr := h.previews.GetActivePreviewForTarget(r.Context(), orgID, target.ID); activeErr == nil {
			active = existing
		} else if activeErr != nil && !errors.Is(activeErr, pgx.ErrNoRows) {
			writeError(w, r, http.StatusInternalServerError, "PREVIEW_LOOKUP_FAILED", "failed to load active preview", activeErr)
			return
		}
	}
	if target != nil && active != nil && target.CommitSHA != head.SHA {
		resp := h.responseForPreview(slug, target, active)
		resp.NewCommitsAvailable = true
		resp.LatestCommitSHA = head.SHA
		resp.PullRequestURL = head.HTMLURL
		resp.StableURL = h.stableURL(slug)
		h.decoratePreviewResponse(r.Context(), orgID, &resp)
		writeJSON(w, http.StatusOK, models.SingleResponse[branchPreviewResponse]{Data: resp})
		return
	}
	if target != nil && target.CommitSHA == head.SHA && active == nil && middleware.ActiveRoleFromContext(r.Context()) == "viewer" {
		resp := branchPreviewResponse{
			TargetID:          target.ID,
			RepositoryID:      target.RepositoryID,
			Branch:            target.Branch,
			CommitSHA:         target.CommitSHA,
			PreviewConfigName: target.PreviewConfigName,
			SourceType:        target.SourceType,
			SourceURL:         target.SourceURL,
			Status:            "target_created",
			StableURL:         h.stableURL(slug),
			PullRequestURL:    head.HTMLURL,
			LatestCommitSHA:   head.SHA,
		}
		h.decoratePreviewResponse(r.Context(), orgID, &resp)
		writeJSON(w, http.StatusOK, models.SingleResponse[branchPreviewResponse]{Data: resp})
		return
	}
	if target == nil || target.CommitSHA != head.SHA {
		if middleware.ActiveRoleFromContext(r.Context()) == "viewer" {
			writeError(w, r, http.StatusForbidden, "PREVIEW_CREATE_FORBIDDEN", "viewer role cannot start a new PR preview")
			return
		}
		target = &models.PreviewTarget{
			OrgID:           orgID,
			RepositoryID:    repo.ID,
			Branch:          head.Branch,
			CommitSHA:       head.SHA,
			SourceType:      models.PreviewSourceTypePullRequest,
			SourceID:        fmt.Sprintf("%s/%s#%d", owner, repoName, number),
			SourceURL:       head.HTMLURL,
			CreatedByUserID: user.ID,
		}
		if err := h.previews.CreatePreviewTarget(r.Context(), target); err != nil {
			writeError(w, r, http.StatusInternalServerError, "PREVIEW_TARGET_CREATE_FAILED", "failed to create PR preview target", err)
			return
		}
	}
	link := &models.PreviewLink{
		OrgID:           orgID,
		PreviewTargetID: target.ID,
		LinkType:        models.PreviewLinkTypePullRequest,
		Slug:            slug,
		RepositoryID:    &repo.ID,
		PRNumber:        &number,
	}
	if err := h.previews.UpsertPreviewLink(r.Context(), link); err != nil {
		writeError(w, r, http.StatusInternalServerError, "PREVIEW_LINK_CREATE_FAILED", "failed to create PR preview link", err)
		return
	}
	resp, startErr := h.startTargetRuntime(r.Context(), orgID, user.ID, repo, target, nil, false)
	if startErr != nil {
		writePreviewHTTPError(w, r, startErr)
		return
	}
	resp.StableURL = h.stableURL(slug)
	resp.PullRequestURL = head.HTMLURL
	resp.LatestCommitSHA = head.SHA
	h.decoratePreviewResponse(r.Context(), orgID, &resp)
	writeJSON(w, http.StatusOK, models.SingleResponse[branchPreviewResponse]{Data: resp})
}

func (h *BranchPreviewHandler) startTargetRuntime(ctx context.Context, orgID, userID uuid.UUID, repo models.Repository, target *models.PreviewTarget, ttlSeconds *int64, restart bool) (branchPreviewResponse, *previewHTTPError) {
	link := &models.PreviewLink{
		OrgID:           orgID,
		PreviewTargetID: target.ID,
		LinkType:        models.PreviewLinkTypeTarget,
		Slug:            target.ID.String(),
		RepositoryID:    &repo.ID,
	}
	if err := h.previews.UpsertPreviewLink(ctx, link); err != nil {
		return branchPreviewResponse{}, newPreviewHTTPError(http.StatusInternalServerError, "PREVIEW_LINK_CREATE_FAILED", "failed to create stable preview link", err)
	}
	if active, activeErr := h.previews.GetActivePreviewForTarget(ctx, orgID, target.ID); activeErr == nil && active != nil {
		if !restart {
			return h.responseForPreview(link.Slug, target, active), nil
		}
		if h.stopper != nil {
			if err := h.stopper.StopPreview(ctx, orgID, active.ID); err != nil {
				return branchPreviewResponse{}, newPreviewHTTPError(http.StatusUnprocessableEntity, "PREVIEW_STOP_FAILED", "failed to stop existing preview", err)
			}
		}
	} else if activeErr != nil && !errors.Is(activeErr, pgx.ErrNoRows) {
		return branchPreviewResponse{}, newPreviewHTTPError(http.StatusInternalServerError, "PREVIEW_LOOKUP_FAILED", "failed to load active preview", activeErr)
	}
	if target.SourceType == models.PreviewSourceTypeSession && target.SourceID != "" {
		if sessionID, err := uuid.Parse(target.SourceID); err == nil {
			if reusable, reuseErr := h.previews.GetActivePreviewForSession(ctx, orgID, sessionID); reuseErr == nil &&
				reusable != nil &&
				reusable.BaseCommitSHA == target.CommitSHA &&
				reusable.Status.IsActive() {
				attached, attachErr := h.previews.AttachPreviewTarget(ctx, orgID, reusable.ID, target.ID)
				if attachErr != nil {
					return branchPreviewResponse{}, newPreviewHTTPError(http.StatusInternalServerError, "PREVIEW_REUSE_FAILED", "failed to attach session preview", attachErr)
				}
				return h.responseForPreview(link.Slug, target, attached), nil
			} else if reuseErr != nil && !errors.Is(reuseErr, pgx.ErrNoRows) {
				return branchPreviewResponse{}, newPreviewHTTPError(http.StatusInternalServerError, "PREVIEW_REUSE_LOOKUP_FAILED", "failed to load session preview", reuseErr)
			}
		}
	}
	if h.manager == nil || h.jobs == nil || h.selector == nil {
		resp := branchPreviewResponse{
			TargetID:          target.ID,
			RepositoryID:      target.RepositoryID,
			Branch:            target.Branch,
			CommitSHA:         target.CommitSHA,
			PreviewConfigName: target.PreviewConfigName,
			SourceType:        target.SourceType,
			SourceURL:         target.SourceURL,
			Status:            "target_created",
			StableURL:         h.stableURL(link.Slug),
		}
		return resp, nil
	}

	worker, err := h.selector.SelectLeastLoadedNode(ctx)
	if err != nil {
		return branchPreviewResponse{}, newPreviewHTTPError(http.StatusServiceUnavailable, preview.PreviewCapacityCode, "no preview worker is available", err)
	}
	initialConfig := reservationPlaceholderConfig()
	input := preview.StartPreviewInput{
		PreviewTargetID: target.ID,
		OrgID:           orgID,
		UserID:          userID,
		Config:          initialConfig,
		BaseCommitSHA:   target.CommitSHA,
		ExpiresAt:       branchPreviewExpiresAt(ttlSeconds),
	}
	tx, err := h.previews.Begin(ctx)
	if err != nil {
		return branchPreviewResponse{}, newPreviewHTTPError(http.StatusInternalServerError, "PREVIEW_START_FAILED", "failed to start preview", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	reservation, err := h.manager.ReserveBranchPreviewForWorkerInTx(ctx, tx, input, worker.ID)
	if err != nil {
		return branchPreviewResponse{}, newPreviewHTTPError(http.StatusUnprocessableEntity, "PREVIEW_START_FAILED", "failed to reserve branch preview", err)
	}
	dedupeKey := "start_branch_preview:" + target.ID.String()
	targetNodeID := worker.ID
	jobID, err := h.jobs.EnqueueInTxWithOpts(ctx, tx, orgID, db.EnqueueOpts{
		Queue:   "preview",
		JobType: models.JobTypeStartBranchPreview,
		Payload: preview.StartBranchPreviewJobPayload{
			OrgID:             orgID,
			UserID:            userID,
			PreviewID:         reservation.ID,
			PreviewTargetID:   target.ID,
			RepositoryID:      repo.ID,
			Branch:            target.Branch,
			CommitSHA:         target.CommitSHA,
			PreviewConfigName: target.PreviewConfigName,
		},
		Priority:     5,
		DedupeKey:    &dedupeKey,
		TargetNodeID: &targetNodeID,
	})
	if err != nil {
		return branchPreviewResponse{}, newPreviewHTTPError(http.StatusInternalServerError, "PREVIEW_START_ENQUEUE_FAILED", "failed to enqueue branch preview startup", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return branchPreviewResponse{}, newPreviewHTTPError(http.StatusInternalServerError, "PREVIEW_START_FAILED", "failed to start preview", err)
	}
	h.jobs.Notify(ctx, jobID)
	return h.responseForPreview(link.Slug, target, reservation), nil
}

func (h *BranchPreviewHandler) Get(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	previewID, err := uuid.Parse(chi.URLParam(r, "preview_id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid preview ID")
		return
	}
	instance, err := h.previews.GetPreviewInstance(r.Context(), orgID, previewID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			target, targetErr := h.previews.GetPreviewTarget(r.Context(), orgID, previewID)
			if targetErr != nil {
				if errors.Is(targetErr, pgx.ErrNoRows) {
					writeError(w, r, http.StatusNotFound, "PREVIEW_NOT_FOUND", "preview not found")
					return
				}
				writeError(w, r, http.StatusInternalServerError, "PREVIEW_TARGET_LOOKUP_FAILED", "failed to load preview target", targetErr)
				return
			}
			resp := branchPreviewResponse{
				TargetID:          target.ID,
				RepositoryID:      target.RepositoryID,
				Branch:            target.Branch,
				CommitSHA:         target.CommitSHA,
				PreviewConfigName: target.PreviewConfigName,
				SourceType:        target.SourceType,
				SourceURL:         target.SourceURL,
				Status:            "target_created",
				StableURL:         h.stableURL(target.ID.String()),
			}
			if !previewTokenAllows(r.Context(), "previews:read", target.RepositoryID) {
				writeError(w, r, http.StatusForbidden, "PREVIEW_TOKEN_FORBIDDEN", "preview API token is not scoped to read this preview")
				return
			}
			if active, activeErr := h.previews.GetActivePreviewForTarget(r.Context(), orgID, target.ID); activeErr == nil && active != nil {
				resp = h.responseForPreview(target.ID.String(), target, active)
			} else if activeErr != nil && !errors.Is(activeErr, pgx.ErrNoRows) {
				writeError(w, r, http.StatusInternalServerError, "PREVIEW_LOOKUP_FAILED", "failed to load active preview", activeErr)
				return
			}
			h.decoratePreviewResponse(r.Context(), orgID, &resp)
			writeJSON(w, http.StatusOK, models.SingleResponse[branchPreviewResponse]{Data: resp})
			return
		}
		writeError(w, r, http.StatusInternalServerError, "PREVIEW_LOOKUP_FAILED", "failed to load preview", err)
		return
	}
	resp := branchPreviewResponse{
		TargetID:  previewTargetIDValue(instance.PreviewTargetID),
		PreviewID: &instance.ID,
		Status:    string(instance.Status),
		StableURL: h.stableURL(instance.ID.String()),
		ExpiresAt: &instance.ExpiresAt,
	}
	if instance.PreviewTargetID != nil {
		resp.StableURL = h.stableURL(instance.PreviewTargetID.String())
	}
	if url := h.previewURL(instance.ID); url != "" {
		resp.PreviewURL = &url
	}
	if instance.PreviewTargetID != nil {
		if target, targetErr := h.previews.GetPreviewTarget(r.Context(), orgID, *instance.PreviewTargetID); targetErr == nil {
			if !previewTokenAllows(r.Context(), "previews:read", target.RepositoryID) {
				writeError(w, r, http.StatusForbidden, "PREVIEW_TOKEN_FORBIDDEN", "preview API token is not scoped to read this preview")
				return
			}
			resp.RepositoryID = target.RepositoryID
			resp.Branch = target.Branch
			resp.CommitSHA = target.CommitSHA
			resp.PreviewConfigName = target.PreviewConfigName
			resp.SourceType = target.SourceType
			resp.SourceURL = target.SourceURL
		}
	}
	h.decoratePreviewResponse(r.Context(), orgID, &resp)
	writeJSON(w, http.StatusOK, models.SingleResponse[branchPreviewResponse]{Data: resp})
}

func (h *BranchPreviewHandler) List(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	var repoID *uuid.UUID
	if raw := strings.TrimSpace(r.URL.Query().Get("repository_id")); raw != "" {
		parsed, err := uuid.Parse(raw)
		if err != nil {
			writeError(w, r, http.StatusBadRequest, "INVALID_REPOSITORY_ID", "invalid repository_id")
			return
		}
		repoID = &parsed
	}
	if token := middleware.PreviewAPITokenFromContext(r.Context()); token != nil {
		if repoID == nil && len(token.RepositoryIDs) > 0 {
			writeError(w, r, http.StatusBadRequest, "REPOSITORY_ID_REQUIRED", "repository_id is required for repository-scoped preview API tokens")
			return
		}
		if repoID != nil && !previewTokenAllows(r.Context(), "previews:read", *repoID) {
			writeError(w, r, http.StatusForbidden, "PREVIEW_TOKEN_FORBIDDEN", "preview API token is not scoped to read previews for this repository")
			return
		}
	}
	summaries, err := h.previews.ListBranchPreviewSummaries(
		r.Context(),
		orgID,
		repoID,
		strings.TrimSpace(r.URL.Query().Get("branch")),
		strings.TrimSpace(r.URL.Query().Get("status")),
		50,
	)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "PREVIEW_LIST_FAILED", "failed to list previews", err)
		return
	}
	responses := make([]branchPreviewResponse, 0, len(summaries))
	for _, item := range summaries {
		resp := branchPreviewResponse{
			TargetID:           item.TargetID,
			PreviewID:          item.PreviewID,
			RepositoryID:       item.RepositoryID,
			RepositoryFullName: item.RepositoryFullName,
			Branch:             item.Branch,
			CommitSHA:          item.CommitSHA,
			PreviewConfigName:  item.PreviewConfigName,
			SourceType:         item.SourceType,
			SourceURL:          item.SourceURL,
			Status:             item.Status,
			StableURL:          h.stableURL(item.TargetID.String()),
			ExpiresAt:          item.ExpiresAt,
		}
		if item.PreviewID != nil {
			if url := h.previewURL(*item.PreviewID); url != "" {
				resp.PreviewURL = &url
			}
		}
		responses = append(responses, resp)
	}
	writeJSON(w, http.StatusOK, models.ListResponse[branchPreviewResponse]{Data: responses})
}

func (h *BranchPreviewHandler) ListAPITokens(w http.ResponseWriter, r *http.Request) {
	if h.apiTokens == nil {
		writeError(w, r, http.StatusInternalServerError, "PREVIEW_API_TOKENS_UNAVAILABLE", "preview API token store is not configured")
		return
	}
	tokens, err := h.apiTokens.List(r.Context(), middleware.OrgIDFromContext(r.Context()))
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "PREVIEW_API_TOKEN_LIST_FAILED", "failed to list preview API tokens", err)
		return
	}
	writeJSON(w, http.StatusOK, models.ListResponse[models.PreviewAPIToken]{Data: tokens})
}

func (h *BranchPreviewHandler) CreateAPIToken(w http.ResponseWriter, r *http.Request) {
	if h.apiTokens == nil {
		writeError(w, r, http.StatusInternalServerError, "PREVIEW_API_TOKENS_UNAVAILABLE", "preview API token store is not configured")
		return
	}
	user := middleware.UserFromContext(r.Context())
	if user == nil {
		writeError(w, r, http.StatusUnauthorized, "UNAUTHORIZED", "authentication required")
		return
	}
	var req createPreviewAPITokenRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_JSON", "invalid request body")
		return
	}
	req.Name = strings.TrimSpace(req.Name)
	if req.Name == "" {
		writeError(w, r, http.StatusBadRequest, "INVALID_PREVIEW_API_TOKEN", "name is required")
		return
	}
	if len(req.Scopes) == 0 {
		req.Scopes = []string{"previews:create", "previews:read", "previews:stop"}
	}
	for _, scope := range req.Scopes {
		if scope != "previews:create" && scope != "previews:read" && scope != "previews:stop" {
			writeError(w, r, http.StatusBadRequest, "INVALID_PREVIEW_API_TOKEN_SCOPE", "invalid preview API token scope")
			return
		}
	}
	rawToken, err := db.GeneratePreviewAPIToken()
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "PREVIEW_API_TOKEN_CREATE_FAILED", "failed to generate preview API token", err)
		return
	}
	token := &models.PreviewAPIToken{
		OrgID:           middleware.OrgIDFromContext(r.Context()),
		Name:            req.Name,
		TokenHash:       db.HashPreviewAPIToken(rawToken),
		Scopes:          req.Scopes,
		RepositoryIDs:   req.RepositoryIDs,
		CreatedByUserID: user.ID,
	}
	if err := h.apiTokens.Create(r.Context(), token); err != nil {
		writeError(w, r, http.StatusInternalServerError, "PREVIEW_API_TOKEN_CREATE_FAILED", "failed to create preview API token", err)
		return
	}
	writeJSON(w, http.StatusCreated, models.SingleResponse[createPreviewAPITokenResponse]{Data: createPreviewAPITokenResponse{
		PreviewAPIToken: *token,
		Token:           rawToken,
	}})
}

func (h *BranchPreviewHandler) RevokeAPIToken(w http.ResponseWriter, r *http.Request) {
	if h.apiTokens == nil {
		writeError(w, r, http.StatusInternalServerError, "PREVIEW_API_TOKENS_UNAVAILABLE", "preview API token store is not configured")
		return
	}
	tokenID, err := uuid.Parse(chi.URLParam(r, "token_id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid token ID")
		return
	}
	if err := h.apiTokens.Revoke(r.Context(), middleware.OrgIDFromContext(r.Context()), tokenID); err != nil {
		writeError(w, r, http.StatusNotFound, "PREVIEW_API_TOKEN_NOT_FOUND", "preview API token not found", err)
		return
	}
	writeJSON(w, http.StatusOK, models.SingleResponse[map[string]string]{Data: map[string]string{"status": "revoked"}})
}

func (h *BranchPreviewHandler) Restart(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	user := middleware.UserFromContext(r.Context())
	if user == nil {
		writeError(w, r, http.StatusUnauthorized, "UNAUTHORIZED", "authentication required")
		return
	}
	var req restartBranchPreviewRequest
	if r.Body != nil && r.ContentLength != 0 {
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeError(w, r, http.StatusBadRequest, "INVALID_JSON", "invalid request body")
			return
		}
	}
	target, repo, active, ok := h.resolveTargetRepoAndActive(w, r, orgID)
	if !ok {
		return
	}
	if !previewTokenAllows(r.Context(), "previews:create", target.RepositoryID) {
		writeError(w, r, http.StatusForbidden, "PREVIEW_TOKEN_FORBIDDEN", "preview API token is not scoped to restart this preview")
		return
	}
	if active != nil && h.stopper != nil {
		if err := h.stopper.StopPreview(r.Context(), orgID, active.ID); err != nil {
			writeError(w, r, http.StatusUnprocessableEntity, "PREVIEW_STOP_FAILED", "failed to stop existing preview", err)
			return
		}
	}
	if req.StartLatest {
		latest, err := h.resolveLatestTarget(r.Context(), orgID, user.ID, repo, target)
		if err != nil {
			writeError(w, r, http.StatusBadGateway, "BRANCH_HEAD_RESOLVE_FAILED", "failed to resolve latest branch head", err)
			return
		}
		target = latest
	}
	resp, startErr := h.startTargetRuntime(r.Context(), orgID, user.ID, repo, target, nil, true)
	if startErr != nil {
		writePreviewHTTPError(w, r, startErr)
		return
	}
	writeJSON(w, http.StatusOK, models.SingleResponse[branchPreviewResponse]{Data: resp})
}

func (h *BranchPreviewHandler) StartLatest(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	user := middleware.UserFromContext(r.Context())
	if user == nil {
		writeError(w, r, http.StatusUnauthorized, "UNAUTHORIZED", "authentication required")
		return
	}
	target, repo, _, ok := h.resolveTargetRepoAndActive(w, r, orgID)
	if !ok {
		return
	}
	if !previewTokenAllows(r.Context(), "previews:create", target.RepositoryID) {
		writeError(w, r, http.StatusForbidden, "PREVIEW_TOKEN_FORBIDDEN", "preview API token is not scoped to start previews for this repository")
		return
	}
	latest, err := h.resolveLatestTarget(r.Context(), orgID, user.ID, repo, target)
	if err != nil {
		writeError(w, r, http.StatusBadGateway, "BRANCH_HEAD_RESOLVE_FAILED", "failed to resolve latest branch head", err)
		return
	}
	resp, startErr := h.startTargetRuntime(r.Context(), orgID, user.ID, repo, latest, nil, false)
	if startErr != nil {
		writePreviewHTTPError(w, r, startErr)
		return
	}
	writeJSON(w, http.StatusOK, models.SingleResponse[branchPreviewResponse]{Data: resp})
}

func (h *BranchPreviewHandler) Stop(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	previewID, err := uuid.Parse(chi.URLParam(r, "preview_id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid preview ID")
		return
	}
	instance, err := h.previews.GetPreviewInstance(r.Context(), orgID, previewID)
	if err != nil {
		writeError(w, r, http.StatusNotFound, "PREVIEW_NOT_FOUND", "preview not found", err)
		return
	}
	if instance.PreviewTargetID != nil {
		target, targetErr := h.previews.GetPreviewTarget(r.Context(), orgID, *instance.PreviewTargetID)
		if targetErr == nil && !previewTokenAllows(r.Context(), "previews:stop", target.RepositoryID) {
			writeError(w, r, http.StatusForbidden, "PREVIEW_TOKEN_FORBIDDEN", "preview API token is not scoped to stop this preview")
			return
		}
	}
	if h.stopper != nil {
		if err := h.stopper.StopPreview(r.Context(), orgID, previewID); err != nil {
			writeError(w, r, http.StatusUnprocessableEntity, "PREVIEW_STOP_FAILED", "failed to stop preview", err)
			return
		}
	} else if h.manager != nil {
		if err := h.manager.StopPreview(r.Context(), orgID, previewID); err != nil {
			writeError(w, r, http.StatusUnprocessableEntity, "PREVIEW_STOP_FAILED", "failed to stop preview", err)
			return
		}
	} else {
		writeError(w, r, http.StatusInternalServerError, "PREVIEW_UNAVAILABLE", "preview service is not configured")
		return
	}
	instance.Status = models.PreviewStatusStopped
	resp := branchPreviewResponse{
		TargetID:  previewTargetIDValue(instance.PreviewTargetID),
		PreviewID: &instance.ID,
		Status:    string(models.PreviewStatusStopped),
		StableURL: h.stableURL(previewTargetIDValue(instance.PreviewTargetID).String()),
	}
	if instance.PreviewTargetID == nil {
		resp.StableURL = h.stableURL(instance.ID.String())
	}
	writeJSON(w, http.StatusOK, models.SingleResponse[branchPreviewResponse]{Data: resp})
}

func (h *BranchPreviewHandler) MintBootstrapToken(w http.ResponseWriter, r *http.Request) {
	if h.manager == nil {
		writeError(w, r, http.StatusInternalServerError, "PREVIEW_UNAVAILABLE", "preview service is not configured")
		return
	}
	orgID := middleware.OrgIDFromContext(r.Context())
	user := middleware.UserFromContext(r.Context())
	if user == nil {
		writeError(w, r, http.StatusUnauthorized, "UNAUTHORIZED", "authentication required")
		return
	}
	previewID, err := uuid.Parse(chi.URLParam(r, "preview_id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid preview ID")
		return
	}
	instance, err := h.previews.GetPreviewInstance(r.Context(), orgID, previewID)
	if err != nil {
		writeError(w, r, http.StatusNotFound, "PREVIEW_NOT_FOUND", "preview not found", err)
		return
	}
	if !instance.Status.IsActive() {
		writeError(w, r, http.StatusConflict, "PREVIEW_NOT_ACTIVE", "preview is not active")
		return
	}
	token, err := h.manager.MintBootstrapToken(r.Context(), orgID, user.ID, previewID)
	if err != nil {
		writeError(w, r, http.StatusUnprocessableEntity, "BOOTSTRAP_TOKEN_FAILED", "failed to create bootstrap token", err)
		return
	}
	writeJSON(w, http.StatusOK, models.SingleResponse[map[string]string]{Data: map[string]string{
		"token":      token,
		"preview_id": previewID.String(),
	}})
}

func (h *BranchPreviewHandler) resolveTargetRepoAndActive(w http.ResponseWriter, r *http.Request, orgID uuid.UUID) (*models.PreviewTarget, models.Repository, *models.PreviewInstance, bool) {
	id, err := uuid.Parse(chi.URLParam(r, "preview_id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid preview ID")
		return nil, models.Repository{}, nil, false
	}
	var target *models.PreviewTarget
	var active *models.PreviewInstance
	instance, instanceErr := h.previews.GetPreviewInstance(r.Context(), orgID, id)
	if instanceErr == nil {
		active = instance
		if instance.PreviewTargetID == nil {
			writeError(w, r, http.StatusConflict, "PREVIEW_HAS_NO_TARGET", "preview is not attached to a branch target")
			return nil, models.Repository{}, nil, false
		}
		target, err = h.previews.GetPreviewTarget(r.Context(), orgID, *instance.PreviewTargetID)
	} else if errors.Is(instanceErr, pgx.ErrNoRows) {
		target, err = h.previews.GetPreviewTarget(r.Context(), orgID, id)
		if err == nil {
			if existing, activeErr := h.previews.GetActivePreviewForTarget(r.Context(), orgID, target.ID); activeErr == nil {
				active = existing
			} else if activeErr != nil && !errors.Is(activeErr, pgx.ErrNoRows) {
				writeError(w, r, http.StatusInternalServerError, "PREVIEW_LOOKUP_FAILED", "failed to load active preview", activeErr)
				return nil, models.Repository{}, nil, false
			}
		}
	} else {
		writeError(w, r, http.StatusInternalServerError, "PREVIEW_LOOKUP_FAILED", "failed to load preview", instanceErr)
		return nil, models.Repository{}, nil, false
	}
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, r, http.StatusNotFound, "PREVIEW_NOT_FOUND", "preview not found")
			return nil, models.Repository{}, nil, false
		}
		writeError(w, r, http.StatusInternalServerError, "PREVIEW_TARGET_LOOKUP_FAILED", "failed to load preview target", err)
		return nil, models.Repository{}, nil, false
	}
	repo, err := h.repos.GetByID(r.Context(), orgID, target.RepositoryID)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "REPOSITORY_LOOKUP_FAILED", "failed to load repository", err)
		return nil, models.Repository{}, nil, false
	}
	return target, repo, active, true
}

func (h *BranchPreviewHandler) resolveLatestTarget(ctx context.Context, orgID, userID uuid.UUID, repo models.Repository, target *models.PreviewTarget) (*models.PreviewTarget, error) {
	owner, name, ok := strings.Cut(repo.FullName, "/")
	if !ok || owner == "" || name == "" {
		return nil, fmt.Errorf("repository full name is invalid")
	}
	if h.github == nil {
		return nil, fmt.Errorf("GitHub is not configured")
	}
	token, err := h.github.GetInstallationToken(ctx, repo.InstallationID)
	if err != nil {
		return nil, err
	}
	head, err := h.github.ResolveBranchHead(ctx, token, owner, name, target.Branch)
	if err != nil {
		return nil, err
	}
	latest := &models.PreviewTarget{
		OrgID:             orgID,
		RepositoryID:      repo.ID,
		Branch:            target.Branch,
		CommitSHA:         head,
		PreviewConfigName: target.PreviewConfigName,
		SourceType:        target.SourceType,
		SourceID:          target.SourceID,
		SourceURL:         target.SourceURL,
		CreatedByUserID:   userID,
	}
	if err := h.previews.CreatePreviewTarget(ctx, latest); err != nil {
		return nil, err
	}
	return latest, nil
}

func (h *BranchPreviewHandler) responseForPreview(slug string, target *models.PreviewTarget, instance *models.PreviewInstance) branchPreviewResponse {
	resp := branchPreviewResponse{
		TargetID:          target.ID,
		PreviewID:         &instance.ID,
		RepositoryID:      target.RepositoryID,
		Branch:            target.Branch,
		CommitSHA:         target.CommitSHA,
		PreviewConfigName: target.PreviewConfigName,
		SourceType:        target.SourceType,
		SourceURL:         target.SourceURL,
		Status:            string(instance.Status),
		Error:             instance.Error,
		StableURL:         h.stableURL(slug),
		ExpiresAt:         &instance.ExpiresAt,
	}
	if url := h.previewURL(instance.ID); url != "" {
		resp.PreviewURL = &url
	}
	return resp
}

func (h *BranchPreviewHandler) decoratePreviewResponse(ctx context.Context, orgID uuid.UUID, resp *branchPreviewResponse) {
	if resp == nil {
		return
	}
	if resp.RepositoryID != uuid.Nil {
		if repo, err := h.repos.GetByID(ctx, orgID, resp.RepositoryID); err == nil {
			resp.RepositoryFullName = repo.FullName
			resp.GitHubBranchURL = githubBranchURL(repo.FullName, resp.Branch)
		}
	}
	if resp.SourceType == models.PreviewSourceTypePullRequest && resp.SourceURL != "" {
		resp.PullRequestURL = resp.SourceURL
	}
	if resp.PreviewID == nil {
		return
	}
	if services, err := h.previews.ListServicesByPreview(ctx, orgID, *resp.PreviewID); err == nil {
		resp.Services = services
	}
	if infra, err := h.previews.ListInfraByPreview(ctx, orgID, *resp.PreviewID); err == nil {
		resp.Infrastructure = infra
	}
	if logs, err := h.previews.ListLogsByPreview(ctx, orgID, *resp.PreviewID, nil); err == nil {
		resp.Logs = logs
		if resp.CurrentPhase == "" && len(logs) > 0 {
			resp.CurrentPhase = string(logs[len(logs)-1].Step)
		}
	}
	if resp.CurrentPhase == "" {
		resp.CurrentPhase = inferPreviewPhase(resp.Status, resp.Services, resp.Infrastructure)
	}
}

func inferPreviewPhase(status string, services []models.PreviewService, infra []models.PreviewInfrastructure) string {
	if status == "target_created" {
		return "reserved"
	}
	for _, item := range infra {
		if item.Status != models.PreviewInfraStatusHealthy {
			return "infrastructure"
		}
	}
	for _, item := range services {
		if item.Status == models.PreviewServiceStatusStarting {
			return "start_services"
		}
		if item.Status == models.PreviewServiceStatusFailed {
			return "failed"
		}
	}
	switch status {
	case string(models.PreviewStatusStarting):
		return "checkout"
	case string(models.PreviewStatusReady), string(models.PreviewStatusPartiallyReady), string(models.PreviewStatusUnhealthy):
		return "readiness"
	case string(models.PreviewStatusFailed):
		return "failed"
	case string(models.PreviewStatusExpired):
		return "expired"
	case string(models.PreviewStatusStopped):
		return "stopped"
	default:
		return status
	}
}

func githubBranchURL(fullName, branch string) string {
	if fullName == "" || branch == "" {
		return ""
	}
	return fmt.Sprintf("https://github.com/%s/tree/%s", fullName, branch)
}

func previewTokenAllows(ctx context.Context, scope string, repositoryID uuid.UUID) bool {
	token := middleware.PreviewAPITokenFromContext(ctx)
	if token == nil {
		return true
	}
	hasScope := false
	for _, item := range token.Scopes {
		if item == scope {
			hasScope = true
			break
		}
	}
	if !hasScope {
		return false
	}
	if len(token.RepositoryIDs) == 0 {
		return true
	}
	for _, allowed := range token.RepositoryIDs {
		if allowed == repositoryID {
			return true
		}
	}
	return false
}

func branchPreviewExpiresAt(ttlSeconds *int64) time.Time {
	if ttlSeconds == nil || *ttlSeconds <= 0 {
		return time.Now().Add(preview.DefaultHardTTL)
	}
	ttl := time.Duration(*ttlSeconds) * time.Second
	if ttl < preview.MinLifetimeTTL {
		ttl = preview.MinLifetimeTTL
	}
	if ttl > preview.DefaultMaxTTL {
		ttl = preview.DefaultMaxTTL
	}
	return time.Now().Add(ttl)
}

func (h *BranchPreviewHandler) previewURL(id uuid.UUID) string {
	if h.previewOriginTemplate == "" {
		return ""
	}
	return strings.ReplaceAll(h.previewOriginTemplate, "{id}", id.String())
}

func previewTargetIDValue(id *uuid.UUID) uuid.UUID {
	if id == nil {
		return uuid.Nil
	}
	return *id
}

func (h *BranchPreviewHandler) stableURL(slug string) string {
	path := fmt.Sprintf("/previews/%s", slug)
	if h.baseURL == "" {
		return path
	}
	return h.baseURL + path
}
