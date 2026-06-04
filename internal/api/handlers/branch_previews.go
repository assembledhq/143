package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	chiMiddleware "github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"golang.org/x/sync/singleflight"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/metrics"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/agent"
	ghservice "github.com/assembledhq/143/internal/services/github"
	"github.com/assembledhq/143/internal/services/preview"
)

// commitSHARe matches a full 40-character Git commit SHA.
var commitSHARe = regexp.MustCompile(`\A[0-9a-fA-F]{40}\z`)

type branchPreviewGitHub interface {
	GetInstallationToken(ctx context.Context, installationID int64) (string, error)
	ResolveBranchHead(ctx context.Context, token, owner, repo, branch string) (string, error)
	CommitExists(ctx context.Context, token, owner, repo, sha string) error
	GetPullRequestHead(ctx context.Context, token, owner, repo string, number int) (ghservice.PullRequestHead, error)
	GetFileContent(ctx context.Context, token, owner, repo, ref, path string) (string, error)
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
	orgStore              agent.OrgSettingsReader
	staticEgressPublicIP  string
	baseURL               string
	previewOriginTemplate string
	// configContentCache caches raw .143/config.json content keyed by
	// "owner/repo@sha" for immutable commit SHAs. Content is content-addressed
	// so there is no TTL; the cache is only populated for full 40-char SHAs.
	configContentCache sync.Map
	// configFetchGroup coalesces concurrent in-flight GitHub fetches for the
	// same immutable SHA so a burst of requests for an uncached SHA results in
	// exactly one GitHub API call rather than N.
	configFetchGroup singleflight.Group
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

func (h *BranchPreviewHandler) SetStaticEgressSettings(orgStore agent.OrgSettingsReader, publicIP string) {
	h.orgStore = orgStore
	h.staticEgressPublicIP = publicIP
}

func (h *BranchPreviewHandler) SetStopper(stopper *preview.WorkerStopper) {
	h.stopper = stopper
}

func (h *BranchPreviewHandler) SetAPITokenStore(store *db.PreviewAPITokenStore) {
	h.apiTokens = store
}

// Validate checks that all required dependencies are wired up. Call this at
// server startup to catch misconfiguration before the first request arrives.
func (h *BranchPreviewHandler) Validate() error {
	var missing []string
	if h.previews == nil {
		missing = append(missing, "previews")
	}
	if h.repos == nil {
		missing = append(missing, "repos")
	}
	if h.github == nil {
		missing = append(missing, "github")
	}
	if len(missing) > 0 {
		return fmt.Errorf("BranchPreviewHandler: required fields not set: %s", strings.Join(missing, ", "))
	}
	return nil
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
	PhaseSteps          []branchPreviewPhaseStep       `json:"phase_steps,omitempty"`
	CreatedByUserID     *uuid.UUID                     `json:"created_by_user_id,omitempty"`
	CreatedAt           *time.Time                     `json:"created_at,omitempty"`
	SourceID            string                         `json:"source_id,omitempty"`
	RequestID           string                         `json:"request_id,omitempty"`
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

type branchPreviewPhaseStep struct {
	Name   string `json:"name"`
	Status string `json:"status"`
}

type branchPreviewConfigOptionsResponse struct {
	RepositoryID       uuid.UUID               `json:"repository_id"`
	RepositoryFullName string                  `json:"repository_full_name"`
	Ref                string                  `json:"ref"`
	PreviewConfigName  string                  `json:"preview_config_name,omitempty"`
	Names              []string                `json:"names"`
	DefaultName        string                  `json:"default_name,omitempty"`
	SelectedName       string                  `json:"selected_name,omitempty"`
	RequiresSelection  bool                    `json:"requires_selection"`
	Readiness          models.PreviewReadiness `json:"readiness"`
	ValidationErrors   []string                `json:"validation_errors,omitempty"`
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
	if req.TTLSeconds != nil && *req.TTLSeconds < 0 {
		writeError(w, r, http.StatusBadRequest, "INVALID_TTL", "ttl_seconds must not be negative")
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
		writeError(w, r, http.StatusInternalServerError, "INVALID_REPOSITORY_NAME", "repository full name is invalid")
		return
	}

	if h.github == nil {
		writeError(w, r, http.StatusServiceUnavailable, "GITHUB_NOT_CONFIGURED", "GitHub is not configured")
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
	createCacheKey := owner + "/" + name + "@" + req.CommitSHA
	isImmutableCommit := commitSHARe.MatchString(req.CommitSHA)
	var configContent string
	if isImmutableCommit {
		if cached, ok := h.configContentCache.Load(createCacheKey); ok {
			configContent = cached.(string)
		}
	}
	if configContent == "" {
		fetched, contentErr := h.github.GetFileContent(r.Context(), token, owner, name, req.CommitSHA, ".143/config.json")
		if contentErr != nil {
			writeError(w, r, http.StatusBadGateway, "PREVIEW_CONFIG_LOOKUP_FAILED", "failed to read .143/config.json", contentErr)
			return
		}
		configContent = fetched
		if isImmutableCommit {
			h.configContentCache.Store(createCacheKey, configContent)
		}
	}
	resolvedConfigName, parsedConfig, configErr := validatePreviewConfigContent([]byte(configContent), configName)
	if configErr != nil {
		writeError(w, r, configErr.status, configErr.code, configErr.message, configErr.err)
		return
	}
	configName = resolvedConfigName
	restart := r.URL.Query().Get("restart") == "true"
	var pendingIdemKey string
	if idemKey := strings.TrimSpace(r.Header.Get("Idempotency-Key")); idemKey != "" {
		existing, idemErr := h.previews.GetPreviewTargetByIdempotencyKey(r.Context(), orgID, idemKey)
		if idemErr == nil && existing != nil {
			metrics.RecordBranchPreviewIdempotencyHit(r.Context(), orgID.String(), "header")
			resp, startErr := h.startTargetRuntime(r.Context(), orgID, user.ID, repo, existing, req.TTLSeconds, restart, nil)
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
		pendingIdemKey = idemKey
	}
	if sourceID != "" {
		existing, sourceErr := h.previews.GetPreviewTargetBySource(r.Context(), orgID, sourceType, sourceID)
		if sourceErr == nil && existing != nil {
			metrics.RecordBranchPreviewIdempotencyHit(r.Context(), orgID.String(), "source")
			resp, startErr := h.startTargetRuntime(r.Context(), orgID, user.ID, repo, existing, req.TTLSeconds, restart, nil)
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
	target := &models.PreviewTarget{
		OrgID:             orgID,
		RepositoryID:      repo.ID,
		Branch:            req.Branch,
		CommitSHA:         req.CommitSHA,
		PreviewConfigName: configName,
		SourceType:        sourceType,
		SourceID:          sourceID,
		SourceURL:         sourceURL,
		CreatedByUserID:   user.ID,
		RequestID:         nilIfEmpty(chiMiddleware.GetReqID(r.Context())),
	}
	if err := h.previews.CreatePreviewTarget(r.Context(), target); err != nil {
		writeError(w, r, http.StatusInternalServerError, "PREVIEW_TARGET_CREATE_FAILED", "failed to create preview target", err)
		return
	}
	if pendingIdemKey != "" {
		_ = h.previews.UpsertPreviewIdempotencyKey(r.Context(), orgID, pendingIdemKey, target.ID)
	}
	metrics.RecordBranchPreviewCreate(r.Context(), orgID.String(), string(sourceType), repo.FullName)

	resp, startErr := h.startTargetRuntime(r.Context(), orgID, user.ID, repo, target, req.TTLSeconds, restart, parsedConfig)
	if startErr != nil {
		writePreviewHTTPError(w, r, startErr)
		return
	}
	writeJSON(w, http.StatusCreated, models.SingleResponse[branchPreviewResponse]{Data: resp})
}

type previewConfigHTTPError struct {
	status  int
	code    string
	message string
	err     error
}

// validatePreviewConfigContent inspects and fully parses a committed
// .143/config.json payload. It uses the already-fetched bytes so the caller
// controls the single GitHub content API round-trip.
// validatePreviewConfigContent parses and validates a committed .143/config.json
// payload. Returns the resolved config name and the parsed PreviewConfig so
// callers can include it in the worker job payload — avoiding a second GitHub
// fetch or sandbox read inside the worker.
func validatePreviewConfigContent(content []byte, requestedName string) (string, *models.PreviewConfig, *previewConfigHTTPError) {
	options, err := preview.InspectConfigOptions(content, requestedName)
	if err != nil {
		return "", nil, &previewConfigHTTPError{
			status:  http.StatusBadRequest,
			code:    "INVALID_PREVIEW_CONFIG",
			message: "invalid preview configuration",
			err:     err,
		}
	}
	if options.RequiresSelection {
		return "", nil, &previewConfigHTTPError{
			status:  http.StatusBadRequest,
			code:    "PREVIEW_CONFIG_REQUIRED",
			message: fmt.Sprintf("preview_config_name is required; available configs: %s", strings.Join(options.Names, ", ")),
		}
	}
	cfg, err := preview.ParseNamedConfig(content, options.SelectedName)
	if err != nil {
		return "", nil, &previewConfigHTTPError{
			status:  http.StatusBadRequest,
			code:    "INVALID_PREVIEW_CONFIG",
			message: err.Error(),
			err:     err,
		}
	}
	return options.SelectedName, cfg, nil
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
	if !previewTokenAllows(r.Context(), "previews:read", repo.ID) {
		writeError(w, r, http.StatusForbidden, "PREVIEW_TOKEN_FORBIDDEN", "preview API token is not scoped to read previews for this repository")
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
	metrics.RecordStablePreviewLinkOpen(r.Context(), orgID.String(), repo.FullName, string(models.PreviewLinkTypePullRequest), previewInstanceExpired(active))
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
			RequestID:         derefStrPtr(target.RequestID),
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
		if !previewTokenAllows(r.Context(), "previews:create", repo.ID) {
			writeError(w, r, http.StatusForbidden, "PREVIEW_TOKEN_FORBIDDEN", "preview API token is not scoped to create previews for this repository")
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
	resp, startErr := h.startTargetRuntime(r.Context(), orgID, user.ID, repo, target, nil, false, nil)
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

func (h *BranchPreviewHandler) GetConfigOptions(w http.ResponseWriter, r *http.Request) {
	if h.repos == nil || h.github == nil {
		writeError(w, r, http.StatusInternalServerError, "PREVIEW_UNAVAILABLE", "preview service is not configured")
		return
	}
	orgID := middleware.OrgIDFromContext(r.Context())
	repoID, err := uuid.Parse(strings.TrimSpace(r.URL.Query().Get("repository_id")))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_REPOSITORY_ID", "repository_id is required")
		return
	}
	repo, err := h.repos.GetByID(r.Context(), orgID, repoID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, r, http.StatusNotFound, "REPOSITORY_NOT_FOUND", "repository not found")
			return
		}
		writeError(w, r, http.StatusInternalServerError, "REPOSITORY_LOOKUP_FAILED", "failed to load repository", err)
		return
	}
	if !previewTokenAllows(r.Context(), "previews:read", repo.ID) {
		writeError(w, r, http.StatusForbidden, "PREVIEW_TOKEN_FORBIDDEN", "preview API token is not scoped to read previews for this repository")
		return
	}
	owner, repoName, ok := strings.Cut(repo.FullName, "/")
	if !ok || owner == "" || repoName == "" {
		writeError(w, r, http.StatusInternalServerError, "INVALID_REPOSITORY_NAME", "repository full name is invalid")
		return
	}
	ref := strings.TrimSpace(r.URL.Query().Get("commit_sha"))
	if ref == "" {
		ref = strings.TrimSpace(r.URL.Query().Get("branch"))
	}
	if ref == "" {
		ref = repo.DefaultBranch
	}
	cacheKey := owner + "/" + repoName + "@" + ref
	isImmutableRef := commitSHARe.MatchString(ref)
	var content string
	if isImmutableRef {
		if cached, ok := h.configContentCache.Load(cacheKey); ok {
			content = cached.(string)
		}
	}
	if content == "" {
		token, tokenErr := h.github.GetInstallationToken(r.Context(), repo.InstallationID)
		if tokenErr != nil {
			writeError(w, r, http.StatusBadGateway, "GITHUB_TOKEN_FAILED", "failed to get GitHub token", tokenErr)
			return
		}
		if isImmutableRef {
			// Coalesce concurrent in-flight fetches for the same immutable SHA so
			// a burst of requests for the same uncached commit results in exactly
			// one GitHub API call. The token is already acquired per-request
			// above; only the file content fetch is shared.
			result, sfErr, _ := h.configFetchGroup.Do(cacheKey, func() (interface{}, error) {
				return h.github.GetFileContent(r.Context(), token, owner, repoName, ref, ".143/config.json")
			})
			if sfErr != nil {
				writeError(w, r, http.StatusBadGateway, "PREVIEW_CONFIG_LOOKUP_FAILED", "failed to read .143/config.json", sfErr)
				return
			}
			content = result.(string)
			h.configContentCache.Store(cacheKey, content)
		} else {
			fetched, fetchErr := h.github.GetFileContent(r.Context(), token, owner, repoName, ref, ".143/config.json")
			if fetchErr != nil {
				writeError(w, r, http.StatusBadGateway, "PREVIEW_CONFIG_LOOKUP_FAILED", "failed to read .143/config.json", fetchErr)
				return
			}
			content = fetched
		}
	}
	configName := strings.TrimSpace(r.URL.Query().Get("preview_config_name"))
	options, err := preview.InspectConfigOptions([]byte(content), configName)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_PREVIEW_CONFIG", "invalid preview configuration", err)
		return
	}
	cfg, parseErr := preview.ParseNamedConfig([]byte(content), options.SelectedName)
	var readiness models.PreviewReadiness
	var validationErrors []string
	if parseErr != nil {
		readiness = models.PreviewReadinessNotSupported
		validationErrors = []string{parseErr.Error()}
	} else {
		detection := preview.DetectReadiness(cfg)
		readiness = detection.Readiness
		validationErrors = detection.ValidationErrors
	}
	writeJSON(w, http.StatusOK, models.SingleResponse[branchPreviewConfigOptionsResponse]{Data: branchPreviewConfigOptionsResponse{
		RepositoryID:       repo.ID,
		RepositoryFullName: repo.FullName,
		Ref:                ref,
		PreviewConfigName:  configName,
		Names:              options.Names,
		DefaultName:        options.DefaultName,
		SelectedName:       options.SelectedName,
		RequiresSelection:  options.RequiresSelection,
		Readiness:          readiness,
		ValidationErrors:   validationErrors,
	}})
}

func (h *BranchPreviewHandler) ResolveLink(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	rawType := strings.TrimSpace(chi.URLParam(r, "link_type"))
	slug := strings.Trim(strings.TrimSpace(chi.URLParam(r, "*")), "/")
	linkType := models.PreviewLinkType(rawType)
	if err := linkType.Validate(); err != nil || slug == "" {
		writeError(w, r, http.StatusBadRequest, "INVALID_PREVIEW_LINK", "invalid preview link")
		return
	}
	link, err := h.previews.GetPreviewLinkBySlug(r.Context(), orgID, linkType, slug)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, r, http.StatusNotFound, "PREVIEW_LINK_NOT_FOUND", "preview link not found")
			return
		}
		writeError(w, r, http.StatusInternalServerError, "PREVIEW_LINK_LOOKUP_FAILED", "failed to load preview link", err)
		return
	}
	target, err := h.previews.GetPreviewTarget(r.Context(), orgID, link.PreviewTargetID)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "PREVIEW_TARGET_LOOKUP_FAILED", "failed to load preview target", err)
		return
	}
	if !previewTokenAllows(r.Context(), "previews:read", target.RepositoryID) {
		writeError(w, r, http.StatusForbidden, "PREVIEW_TOKEN_FORBIDDEN", "preview API token is not scoped to read this preview")
		return
	}
	resp := branchPreviewResponse{
		TargetID:          target.ID,
		RepositoryID:      target.RepositoryID,
		Branch:            target.Branch,
		CommitSHA:         target.CommitSHA,
		PreviewConfigName: target.PreviewConfigName,
		SourceType:        target.SourceType,
		SourceID:          target.SourceID,
		SourceURL:         target.SourceURL,
		CreatedByUserID:   &target.CreatedByUserID,
		CreatedAt:         &target.CreatedAt,
		RequestID:         derefStrPtr(target.RequestID),
		Status:            "target_created",
		StableURL:         h.stableURL(link.Slug),
	}
	expired := true
	if active, activeErr := h.previews.GetActivePreviewForTarget(r.Context(), orgID, target.ID); activeErr == nil && active != nil {
		resp = h.responseForPreview(link.Slug, target, active)
		expired = previewInstanceExpired(active)
	} else if activeErr != nil && !errors.Is(activeErr, pgx.ErrNoRows) {
		writeError(w, r, http.StatusInternalServerError, "PREVIEW_LOOKUP_FAILED", "failed to load active preview", activeErr)
		return
	}
	h.decoratePreviewResponse(r.Context(), orgID, &resp)
	metrics.RecordStablePreviewLinkOpen(r.Context(), orgID.String(), resp.RepositoryFullName, string(linkType), expired)
	writeJSON(w, http.StatusOK, models.SingleResponse[branchPreviewResponse]{Data: resp})
}

// startTargetRuntime launches (or re-uses) a running preview for the given
// target. cfg is the already-parsed PreviewConfig from the handler validation
// step; passing it avoids a redundant GitHub fetch or sandbox re-read inside
// the worker. Pass nil when the config is not available at the call site —
// the worker will fall back to reading it from the checked-out sandbox.
func (h *BranchPreviewHandler) startTargetRuntime(ctx context.Context, orgID, userID uuid.UUID, repo models.Repository, target *models.PreviewTarget, ttlSeconds *int64, restart bool, cfg *models.PreviewConfig) (branchPreviewResponse, *previewHTTPError) {
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
	reqs, err := h.workerSelectionRequirements(ctx, orgID)
	if err != nil {
		return branchPreviewResponse{}, newPreviewHTTPError(http.StatusInternalServerError, "PREVIEW_WORKER_SELECTION_FAILED", "failed to read network settings", err)
	}
	if active, activeErr := h.previews.GetActivePreviewForTarget(ctx, orgID, target.ID); activeErr == nil && active != nil {
		if !restart {
			if !branchPreviewRuntimeMatchesWorkerRequirements(active, reqs) {
				return branchPreviewResponse{}, newPreviewHTTPError(http.StatusConflict, "NETWORK_SETTING_RESTART_REQUIRED", "restart preview to apply network setting", nil)
			}
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
				// Require a ready/partially-ready state AND a non-empty PreviewHandle
				// so we don't attach to a preview that is still starting (handle
				// not yet registered) or that may have crashed without the DB
				// being updated yet. unhealthy is intentionally excluded: the
				// sandbox may be in a restart loop; let the caller get a fresh
				// instance instead.
				(reusable.Status == models.PreviewStatusReady || reusable.Status == models.PreviewStatusPartiallyReady) &&
				reusable.PreviewHandle != "" &&
				branchPreviewRuntimeMatchesWorkerRequirements(reusable, reqs) {
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
			RequestID:         derefStrPtr(target.RequestID),
			Status:            "target_created",
			StableURL:         h.stableURL(link.Slug),
		}
		return resp, nil
	}

	worker, err := h.selector.SelectLeastLoadedNodeWithRequirements(ctx, reqs)
	if err != nil {
		return branchPreviewResponse{}, newPreviewHTTPError(http.StatusServiceUnavailable, preview.PreviewCapacityCode, "no preview worker is available", err)
	}
	// Soft quota pre-check before opening the DB transaction. Non-atomic with
	// respect to ReserveBranchPreviewForWorkerInTx but avoids the transaction
	// overhead when the cap is clearly exceeded.
	if capErr := h.manager.CheckPreviewCapacity(ctx, orgID, userID, worker.ID); capErr != nil {
		if errors.Is(capErr, preview.ErrPreviewCapacity) {
			return branchPreviewResponse{}, newPreviewHTTPError(http.StatusServiceUnavailable, preview.PreviewCapacityCode, capErr.Error(), capErr)
		}
		return branchPreviewResponse{}, newPreviewHTTPError(http.StatusInternalServerError, "PREVIEW_CAPACITY_CHECK_FAILED", "failed to check preview capacity", capErr)
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
			Config:            cfg,
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

// Get resolves a preview by ID. The {preview_id} path parameter is dual-purpose:
// it is tried first as a preview instance UUID and, if no instance is found,
// retried as a preview target UUID. Callers may therefore poll with either the
// instance ID returned by Create or the stable target ID; both return the same
// response shape with the active instance's state.
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
				RequestID:         derefStrPtr(target.RequestID),
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
		TargetID:     previewTargetIDValue(instance.PreviewTargetID),
		PreviewID:    &instance.ID,
		Status:       string(instance.Status),
		CurrentPhase: instance.CurrentPhase,
		StableURL:    h.stableURL(instance.ID.String()),
		ExpiresAt:    &instance.ExpiresAt,
	}
	if instance.PreviewTargetID != nil {
		resp.StableURL = h.stableURL(instance.PreviewTargetID.String())
	}
	if url := h.previewURL(instance.ID); url != "" {
		resp.PreviewURL = &url
	}
	if instance.PreviewTargetID != nil {
		target, targetErr := h.previews.GetPreviewTarget(r.Context(), orgID, *instance.PreviewTargetID)
		if targetErr != nil && !errors.Is(targetErr, pgx.ErrNoRows) {
			writeError(w, r, http.StatusInternalServerError, "PREVIEW_TARGET_LOOKUP_FAILED", "failed to load preview target", targetErr)
			return
		}
		if targetErr == nil {
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
			resp.RequestID = derefStrPtr(target.RequestID)
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
	const defaultPreviewListLimit = 50
	const maxPreviewListLimit = 100
	limit := clampListLimit(queryInt(r, "limit", defaultPreviewListLimit), defaultPreviewListLimit, maxPreviewListLimit)
	// Fetch one extra to detect whether a next page exists without a second
	// COUNT query. The extra row is never returned to the caller.
	summaries, err := h.previews.ListBranchPreviewSummaries(
		r.Context(),
		orgID,
		repoID,
		strings.TrimSpace(r.URL.Query().Get("branch")),
		strings.TrimSpace(r.URL.Query().Get("status")),
		limit+1,
	)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "PREVIEW_LIST_FAILED", "failed to list previews", err)
		return
	}
	var nextCursor string
	if len(summaries) > limit {
		last := summaries[limit-1]
		nextCursor = encodeCursor(last.CreatedAt, last.TargetID.String())
		summaries = summaries[:limit]
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
	writeJSON(w, http.StatusOK, models.ListResponse[branchPreviewResponse]{
		Data: responses,
		Meta: models.PaginationMeta{NextCursor: nextCursor},
	})
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
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, r, http.StatusNotFound, "PREVIEW_API_TOKEN_NOT_FOUND", "preview API token not found")
		} else {
			writeError(w, r, http.StatusInternalServerError, "PREVIEW_API_TOKEN_REVOKE_FAILED", "failed to revoke preview API token", err)
		}
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
	// startTargetRuntime(restart=true) owns stopping the active preview atomically
	// before reserving a new slot. Do not stop here first — that creates a window
	// where the active is gone but no new one is reserved, and startTargetRuntime
	// then skips its own stop (active is nil) and correctly reserves fresh.
	_ = active // resolved by resolveTargetRepoAndActive, used implicitly by startTargetRuntime
	if req.StartLatest {
		latest, err := h.resolveLatestTarget(r.Context(), orgID, user.ID, repo, target)
		if err != nil {
			writeError(w, r, http.StatusBadGateway, "BRANCH_HEAD_RESOLVE_FAILED", "failed to resolve latest branch head", err)
			return
		}
		target = latest
	}
	resp, startErr := h.startTargetRuntime(r.Context(), orgID, user.ID, repo, target, nil, true, nil)
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
	resp, startErr := h.startTargetRuntime(r.Context(), orgID, user.ID, repo, latest, nil, false, nil)
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
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, r, http.StatusNotFound, "PREVIEW_NOT_FOUND", "preview not found")
		} else {
			writeError(w, r, http.StatusInternalServerError, "PREVIEW_LOOKUP_FAILED", "failed to load preview", err)
		}
		return
	}
	if instance.PreviewTargetID != nil {
		target, targetErr := h.previews.GetPreviewTarget(r.Context(), orgID, *instance.PreviewTargetID)
		if targetErr != nil && !errors.Is(targetErr, pgx.ErrNoRows) {
			writeError(w, r, http.StatusInternalServerError, "PREVIEW_TARGET_LOOKUP_FAILED", "failed to load preview target", targetErr)
			return
		}
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
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, r, http.StatusNotFound, "PREVIEW_NOT_FOUND", "preview not found")
		} else {
			writeError(w, r, http.StatusInternalServerError, "PREVIEW_LOOKUP_FAILED", "failed to load preview", err)
		}
		return
	}
	if instance.PreviewTargetID != nil {
		target, targetErr := h.previews.GetPreviewTarget(r.Context(), orgID, *instance.PreviewTargetID)
		if targetErr == nil && !previewTokenAllows(r.Context(), "previews:read", target.RepositoryID) {
			writeError(w, r, http.StatusForbidden, "PREVIEW_TOKEN_FORBIDDEN", "preview API token is not scoped to access this preview")
			return
		}
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
	// Verify the resolved SHA actually exists before storing it. ResolveBranchHead
	// is authoritative but a race between branch force-push and our lookup could
	// return a SHA that's no longer reachable; catching it here gives a clean
	// "commit not found" error rather than a confusing worker checkout failure.
	if err := h.github.CommitExists(ctx, token, owner, name, head); err != nil {
		return nil, fmt.Errorf("resolved branch head %s no longer reachable: %w", head, err)
	}
	// If the branch head hasn't changed return the existing target — avoids
	// duplicate target rows when StartLatest is called concurrently or the
	// branch hasn't advanced since the last run.
	if head == target.CommitSHA {
		return target, nil
	}
	// Check whether a target already exists for this exact head SHA before
	// inserting a new one; concurrent StartLatest calls can race to the same
	// commit.
	if existing, lookupErr := h.previews.GetLatestPreviewTargetForBranch(ctx, orgID, repo.ID, target.Branch, target.PreviewConfigName); lookupErr == nil && existing != nil && existing.CommitSHA == head {
		return existing, nil
	}
	// PR source URLs point to a specific commit on the original PR; they are
	// meaningless for a new head SHA, so clear them. The PR link is still
	// retrievable via GetPullRequest using the stable source_id.
	sourceURL := target.SourceURL
	if target.SourceType == models.PreviewSourceTypePullRequest {
		sourceURL = ""
	}
	latest := &models.PreviewTarget{
		OrgID:             orgID,
		RepositoryID:      repo.ID,
		Branch:            target.Branch,
		CommitSHA:         head,
		PreviewConfigName: target.PreviewConfigName,
		SourceType:        target.SourceType,
		SourceID:          target.SourceID,
		SourceURL:         sourceURL,
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
		SourceID:          target.SourceID,
		SourceURL:         target.SourceURL,
		CreatedByUserID:   &target.CreatedByUserID,
		CreatedAt:         &target.CreatedAt,
		Status:            string(instance.Status),
		Error:             instance.Error,
		CurrentPhase:      instance.CurrentPhase,
		StableURL:         h.stableURL(slug),
		ExpiresAt:         &instance.ExpiresAt,
		RequestID:         derefStrPtr(target.RequestID),
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
		if resp.CurrentPhase == "" {
			resp.CurrentPhase = inferPreviewPhase(resp.Status, resp.Services, resp.Infrastructure)
		}
		resp.PhaseSteps = previewPhaseSteps(resp.CurrentPhase, resp.Status)
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
	resp.PhaseSteps = previewPhaseSteps(resp.CurrentPhase, resp.Status)
}

func previewPhaseSteps(currentPhase, status string) []branchPreviewPhaseStep {
	steps := []branchPreviewPhaseStep{
		{Name: "checkout", Status: "pending"},
		{Name: "install_build", Status: "pending"},
		{Name: "start_services", Status: "pending"},
		{Name: "readiness", Status: "pending"},
	}
	if currentPhase == "" {
		return steps
	}
	order := map[string]int{
		"reserved":       -1,
		"checkout":       0,
		"install_build":  1,
		"build":          1,
		"infrastructure": 2, // infra startup maps to start_services step
		"start_services": 2,
		"readiness":      3,
		"ready":          4,
		"expired":        4,
		"stopped":        4,
		"failed":         4, // terminal — all prior steps completed before failure
	}
	current, ok := order[currentPhase]
	if !ok {
		current = 0
	}
	isFailed := status == string(models.PreviewStatusFailed)
	for i := range steps {
		switch {
		case isFailed && current < len(steps) && i == current:
			// Failed at a known in-progress step: mark that step failed.
			steps[i].Status = "failed"
		case isFailed && current >= len(steps) && i == len(steps)-1:
			// Failed after all steps appeared to complete (e.g. post-readiness
			// crash): mark the last step failed so the UI doesn't show all-green.
			steps[i].Status = "failed"
		case i < current:
			steps[i].Status = "complete"
		case i == current && !isFailed:
			steps[i].Status = "active"
		}
	}
	return steps
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

// previewInstanceExpired returns true when the preview is no longer accessible.
// It checks both DB terminal status and wall-clock expiry so that previews
// which have been stopped or failed server-side — even if the reaper hasn't
// flushed the status to the caller's context yet — are correctly reported.
func previewInstanceExpired(instance *models.PreviewInstance) bool {
	if instance == nil {
		return true
	}
	// Terminal status in the DB is authoritative; include stopped/failed in
	// addition to expired so that any ended preview is treated as inaccessible.
	if instance.Status.IsTerminal() {
		return true
	}
	return instance.ExpiresAt.Before(time.Now())
}

func branchPreviewRuntimeMatchesWorkerRequirements(instance *models.PreviewInstance, req preview.WorkerSelectionRequirements) bool {
	if instance == nil {
		return false
	}
	egressMode := agent.SandboxEgressModeDirect
	if len(instance.RecycleSandbox) > 2 {
		var sb agent.Sandbox
		if err := json.Unmarshal(instance.RecycleSandbox, &sb); err != nil {
			return false
		}
		if sb.Metadata != nil && sb.Metadata[agent.SandboxMetadataEgressMode] != "" {
			egressMode = sb.Metadata[agent.SandboxMetadataEgressMode]
		}
	}
	if req.StaticEgressRequired {
		return egressMode == agent.SandboxEgressModeStatic
	}
	return egressMode != agent.SandboxEgressModeStatic
}

func (h *BranchPreviewHandler) workerSelectionRequirements(ctx context.Context, orgID uuid.UUID) (preview.WorkerSelectionRequirements, error) {
	if h == nil {
		return preview.WorkerSelectionRequirements{}, nil
	}
	return previewWorkerSelectionRequirements(ctx, h.orgStore, orgID, h.staticEgressPublicIP)
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
