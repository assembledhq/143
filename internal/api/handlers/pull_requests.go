package handlers

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/api/sse"
	"github.com/assembledhq/143/internal/cache"
	"github.com/assembledhq/143/internal/models"
	ghservice "github.com/assembledhq/143/internal/services/github"
)

type pullRequestHealthService interface {
	GetPullRequestHealth(ctx context.Context, orgID, pullRequestID uuid.UUID) (*models.PullRequestHealthResponse, error)
	StartPullRequestRepair(ctx context.Context, orgID, pullRequestID, userID uuid.UUID, action models.PullRequestRepairActionType) (*models.PullRequestRepairResponse, error)
	MergePullRequest(ctx context.Context, orgID, pullRequestID, userID uuid.UUID) (*models.PullRequestMergeResponse, error)
	QueueMergeWhenReady(ctx context.Context, orgID, pullRequestID, userID uuid.UUID) (*models.PullRequestMergeWhenReadyStatus, error)
	CancelMergeWhenReady(ctx context.Context, orgID, pullRequestID, userID uuid.UUID) (*models.PullRequestMergeWhenReadyStatus, error)
}

type pullRequestMembershipStore interface {
	Get(ctx context.Context, userID, orgID uuid.UUID) (models.OrganizationMembership, error)
}

var (
	errPullRequestStreamOrgInvalid   = errors.New("invalid pull request stream org")
	errPullRequestStreamOrgForbidden = errors.New("forbidden pull request stream org")
	errPullRequestStreamUnauthorized = errors.New("unauthorized pull request stream request")
)

type PullRequestHandler struct {
	service     pullRequestHealthService
	streams     *cache.PullRequestStreams
	memberships pullRequestMembershipStore
}

func NewPullRequestHandler(service pullRequestHealthService) *PullRequestHandler {
	return &PullRequestHandler{service: service}
}

func (h *PullRequestHandler) SetStreams(streams *cache.PullRequestStreams) {
	h.streams = streams
}

func (h *PullRequestHandler) SetMembershipStore(store pullRequestMembershipStore) {
	h.memberships = store
}

func (h *PullRequestHandler) GetHealth(w http.ResponseWriter, r *http.Request) {
	if h.service == nil {
		writeError(w, r, http.StatusNotImplemented, "NOT_CONFIGURED", "pull request health is not configured")
		return
	}

	orgID := middleware.OrgIDFromContext(r.Context())
	pullRequestID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid pull request ID")
		return
	}

	health, err := h.service.GetPullRequestHealth(r.Context(), orgID, pullRequestID)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "PULL_REQUEST_HEALTH_FAILED", "failed to load pull request health", err)
		return
	}
	writeJSON(w, http.StatusOK, models.SingleResponse[models.PullRequestHealthResponse]{Data: *health})
}

func (h *PullRequestHandler) StreamUpdates(w http.ResponseWriter, r *http.Request) {
	if h.streams == nil || !h.streams.Available() {
		http.Error(w, "pull request streams unavailable", http.StatusServiceUnavailable)
		return
	}
	_ = middleware.OrgIDFromContext(r.Context())

	sw := sse.NewWriter(w)
	if sw == nil {
		http.Error(w, "streaming unsupported", http.StatusInternalServerError)
		return
	}

	orgID, err := h.streamOrgIDFromRequest(r)
	if err != nil {
		switch {
		case errors.Is(err, errPullRequestStreamOrgInvalid):
			http.Error(w, "invalid pull request stream org", http.StatusBadRequest)
		case errors.Is(err, errPullRequestStreamOrgForbidden):
			http.Error(w, "forbidden pull request stream org", http.StatusForbidden)
		case errors.Is(err, errPullRequestStreamUnauthorized):
			http.Error(w, "unauthorized", http.StatusUnauthorized)
		default:
			http.Error(w, "failed to authorize pull request stream", http.StatusInternalServerError)
		}
		return
	}
	sub, err := h.streams.Subscribe(orgID)
	if err != nil {
		http.Error(w, "pull request streams unavailable", http.StatusServiceUnavailable)
		return
	}
	defer sub.Close()

	logger := zerolog.Ctx(r.Context())
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-heartbeat.C:
			if err := sw.WriteHeartbeat(); err != nil {
				logger.Warn().Err(err).Msg("failed to write pull request stream heartbeat")
				return
			}
			sw.Flush()
		case event, ok := <-sub.C:
			if !ok {
				logger.Warn().Str("reason", sub.CloseReason()).Msg("pull request update subscription closed")
				return
			}
			if err := sw.WriteEvent(sse.EventType("pull_request.updated"), event); err != nil {
				logger.Warn().Err(err).Str("pull_request_id", event.PullRequestID.String()).Msg("failed to write pull request update event")
				return
			}
			sw.Flush()
		}
	}
}

func (h *PullRequestHandler) streamOrgIDFromRequest(r *http.Request) (uuid.UUID, error) {
	orgID := middleware.OrgIDFromContext(r.Context())
	requestedRaw := strings.TrimSpace(r.URL.Query().Get("org_id"))
	if requestedRaw == "" {
		return orgID, nil
	}

	requestedOrgID, err := uuid.Parse(requestedRaw)
	if err != nil {
		return uuid.Nil, errPullRequestStreamOrgInvalid
	}
	if requestedOrgID == orgID {
		return requestedOrgID, nil
	}

	user := middleware.UserFromContext(r.Context())
	if user == nil {
		return uuid.Nil, errPullRequestStreamUnauthorized
	}
	if h.memberships == nil {
		return uuid.Nil, errors.New("membership store not configured")
	}
	if _, err := h.memberships.Get(r.Context(), user.ID, requestedOrgID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return uuid.Nil, errPullRequestStreamOrgForbidden
		}
		return uuid.Nil, err
	}

	return requestedOrgID, nil
}

func (h *PullRequestHandler) FixTests(w http.ResponseWriter, r *http.Request) {
	_ = middleware.OrgIDFromContext(r.Context())
	h.startRepair(w, r, models.PullRequestRepairActionTypeFixTests)
}

func (h *PullRequestHandler) ResolveConflicts(w http.ResponseWriter, r *http.Request) {
	_ = middleware.OrgIDFromContext(r.Context())
	h.startRepair(w, r, models.PullRequestRepairActionTypeResolveConflicts)
}

func (h *PullRequestHandler) startRepair(w http.ResponseWriter, r *http.Request, action models.PullRequestRepairActionType) {
	if h.service == nil {
		writeError(w, r, http.StatusNotImplemented, "NOT_CONFIGURED", "pull request repairs are not configured")
		return
	}

	orgID := middleware.OrgIDFromContext(r.Context())
	user := middleware.UserFromContext(r.Context())
	if user == nil {
		writeError(w, r, http.StatusUnauthorized, "UNAUTHORIZED", "user not found in context")
		return
	}
	pullRequestID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid pull request ID")
		return
	}

	resp, err := h.service.StartPullRequestRepair(r.Context(), orgID, pullRequestID, user.ID, action)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "PULL_REQUEST_REPAIR_FAILED", "failed to start pull request repair", err)
		return
	}
	writeJSON(w, http.StatusOK, models.SingleResponse[models.PullRequestRepairResponse]{Data: *resp})
}

func (h *PullRequestHandler) Merge(w http.ResponseWriter, r *http.Request) {
	if h.service == nil {
		writeError(w, r, http.StatusNotImplemented, "NOT_CONFIGURED", "pull request merge is not configured")
		return
	}

	orgID := middleware.OrgIDFromContext(r.Context())
	user := middleware.UserFromContext(r.Context())
	if user == nil {
		writeError(w, r, http.StatusUnauthorized, "UNAUTHORIZED", "user not found in context")
		return
	}
	pullRequestID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid pull request ID")
		return
	}

	resp, err := h.service.MergePullRequest(r.Context(), orgID, pullRequestID, user.ID)
	if err != nil {
		switch {
		case errors.Is(err, ghservice.ErrPullRequestNotMergeable):
			writeError(w, r, http.StatusConflict, "PR_NOT_MERGEABLE", "Pull request is not in a mergeable state", err)
		case errors.Is(err, ghservice.ErrNoMergeMethodAllowed):
			writeError(w, r, http.StatusConflict, "NO_MERGE_METHOD_ALLOWED", "Repository does not allow any merge method", err)
		case errors.Is(err, ghservice.ErrGitHubUserAuthRequired):
			writeError(w, r, http.StatusConflict, "GITHUB_USER_AUTH_REQUIRED", "Connect your GitHub account to merge this pull request as yourself", err)
		case errors.Is(err, ghservice.ErrGitHubUserAuthRepoAccessDenied):
			writeError(w, r, http.StatusConflict, "GITHUB_USER_AUTH_REPO_ACCESS_DENIED", "your GitHub account cannot access this repository for merge", err)
		case errors.Is(err, ghservice.ErrMergeStateRefreshFailed):
			// Returned when GitHub or the DB couldn't be reached to confirm
			// the PR is still in a mergeable state. 503 lets the UI prompt the
			// user to retry rather than treating this as a permanent rejection.
			writeError(w, r, http.StatusServiceUnavailable, "PR_MERGE_STATE_UNAVAILABLE", "Could not confirm pull request is still mergeable; please retry", err)
		case errors.Is(err, ghservice.ErrGitHubMergeIncomplete):
			// GitHub returned 200 but reported merged=false. Treat as a
			// gateway-level failure so the UI prompts the user to retry.
			writeError(w, r, http.StatusBadGateway, "PULL_REQUEST_MERGE_INCOMPLETE", "GitHub did not complete the merge; please retry", err)
		default:
			// GitHub itself can refuse with 405 (method not allowed) or 409
			// (head SHA mismatch / branch protection); surface those as 409
			// with the GitHub error message bubbled up so the toast is
			// actionable. Anything else is a 500.
			if status, message, ok := classifyGitHubMergeError(err); ok {
				writeError(w, r, status, "PULL_REQUEST_MERGE_REJECTED", message, err)
				return
			}
			writeError(w, r, http.StatusInternalServerError, "PULL_REQUEST_MERGE_FAILED", "Failed to merge pull request", err)
		}
		return
	}
	writeJSON(w, http.StatusOK, models.SingleResponse[models.PullRequestMergeResponse]{Data: *resp})
}

func (h *PullRequestHandler) QueueMergeWhenReady(w http.ResponseWriter, r *http.Request) {
	_ = middleware.OrgIDFromContext(r.Context())
	h.mergeWhenReady(w, r, true)
}

func (h *PullRequestHandler) CancelMergeWhenReady(w http.ResponseWriter, r *http.Request) {
	_ = middleware.OrgIDFromContext(r.Context())
	h.mergeWhenReady(w, r, false)
}

func (h *PullRequestHandler) mergeWhenReady(w http.ResponseWriter, r *http.Request, queue bool) {
	if h.service == nil {
		writeError(w, r, http.StatusNotImplemented, "NOT_CONFIGURED", "pull request merge when ready is not configured")
		return
	}

	orgID := middleware.OrgIDFromContext(r.Context())
	user := middleware.UserFromContext(r.Context())
	if user == nil {
		writeError(w, r, http.StatusUnauthorized, "UNAUTHORIZED", "user not found in context")
		return
	}
	pullRequestID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid pull request ID")
		return
	}

	var resp *models.PullRequestMergeWhenReadyStatus
	if queue {
		resp, err = h.service.QueueMergeWhenReady(r.Context(), orgID, pullRequestID, user.ID)
	} else {
		resp, err = h.service.CancelMergeWhenReady(r.Context(), orgID, pullRequestID, user.ID)
	}
	if err != nil {
		switch {
		case errors.Is(err, ghservice.ErrPullRequestMergeWhenReadyInProgress):
			writeError(w, r, http.StatusConflict, "MERGE_WHEN_READY_IN_PROGRESS", "Merge when ready is already in progress", err)
		case errors.Is(err, ghservice.ErrPullRequestMergeWhenReadyNotQueueable):
			writeError(w, r, http.StatusConflict, "MERGE_WHEN_READY_NOT_QUEUEABLE", "Pull request cannot be queued for merge when ready", err)
		case errors.Is(err, ghservice.ErrGitHubUserAuthRequired):
			writeError(w, r, http.StatusConflict, "GITHUB_USER_AUTH_REQUIRED", "Connect your GitHub account to merge this pull request as yourself", err)
		case errors.Is(err, ghservice.ErrGitHubUserAuthRepoAccessDenied):
			writeError(w, r, http.StatusConflict, "GITHUB_USER_AUTH_REPO_ACCESS_DENIED", "your GitHub account cannot access this repository for merge", err)
		default:
			writeError(w, r, http.StatusInternalServerError, "MERGE_WHEN_READY_FAILED", "Failed to update merge when ready", err)
		}
		return
	}
	writeJSON(w, http.StatusOK, models.SingleResponse[models.PullRequestMergeWhenReadyStatus]{Data: *resp})
}

// classifyGitHubMergeError maps an error returned from the GitHub merge call
// onto an HTTP status + user-facing message when it represents a known,
// user-actionable failure (branch protection, SHA mismatch, method disabled).
// Returns false for unrelated errors so the caller can return a 500.
func classifyGitHubMergeError(err error) (int, string, bool) {
	var apiErr *ghservice.GitHubAPIError
	if !errors.As(err, &apiErr) {
		return 0, "", false
	}
	switch apiErr.StatusCode {
	case http.StatusMethodNotAllowed:
		return http.StatusConflict, apiErr.Message(), true
	case http.StatusConflict:
		return http.StatusConflict, apiErr.Message(), true
	case http.StatusUnprocessableEntity:
		return http.StatusUnprocessableEntity, apiErr.Message(), true
	default:
		return 0, "", false
	}
}
