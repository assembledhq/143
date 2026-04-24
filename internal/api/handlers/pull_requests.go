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
)

type pullRequestHealthService interface {
	GetPullRequestHealth(ctx context.Context, orgID, pullRequestID uuid.UUID) (*models.PullRequestHealthResponse, error)
	StartPullRequestRepair(ctx context.Context, orgID, pullRequestID, userID uuid.UUID, action models.PullRequestRepairActionType) (*models.PullRequestRepairResponse, error)
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
