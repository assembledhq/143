package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/models"
	reviewloopsvc "github.com/assembledhq/143/internal/services/reviewloop"
)

type ReviewLoopService interface {
	Start(ctx context.Context, orgID, sessionID uuid.UUID, req reviewloopsvc.StartReviewLoopRequest) (*models.SessionReviewLoop, error)
}

type ReviewLoopStore interface {
	GetLoopByID(ctx context.Context, orgID, loopID uuid.UUID) (models.SessionReviewLoop, error)
	ListLoopsBySession(ctx context.Context, orgID, sessionID uuid.UUID) ([]models.SessionReviewLoop, error)
	ListPassesByLoop(ctx context.Context, orgID, loopID uuid.UUID) ([]models.SessionReviewLoopPass, error)
	CancelLoop(ctx context.Context, orgID, loopID uuid.UUID) error
}

type ReviewLoopHandler struct {
	svc   ReviewLoopService
	store ReviewLoopStore
}

func NewReviewLoopHandler(svc ReviewLoopService, store ReviewLoopStore) *ReviewLoopHandler {
	return &ReviewLoopHandler{svc: svc, store: store}
}

type reviewLoopDetail struct {
	models.SessionReviewLoop
	Passes []models.SessionReviewLoopPass `json:"passes"`
}

func (h *ReviewLoopHandler) Start(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	sessionID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid session ID")
		return
	}
	var body struct {
		AgentType string `json:"agent_type"`
		Model     string `json:"model"`
		MaxPasses int    `json:"max_passes"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_BODY", "invalid request body")
		return
	}
	user := middleware.UserFromContext(r.Context())
	var userID *uuid.UUID
	if user != nil {
		userID = &user.ID
	}
	loop, err := h.svc.Start(r.Context(), orgID, sessionID, reviewloopsvc.StartReviewLoopRequest{
		AgentType:       models.AgentType(strings.TrimSpace(body.AgentType)),
		Model:           strings.TrimSpace(body.Model),
		MaxPasses:       body.MaxPasses,
		Source:          models.ReviewLoopSourceManual,
		StartedByUserID: userID,
	})
	if err != nil {
		switch {
		case errors.Is(err, reviewloopsvc.ErrInvalidPassCount):
			writeError(w, r, http.StatusBadRequest, "INVALID_PASS_COUNT", err.Error())
		case errors.Is(err, reviewloopsvc.ErrUnsupportedReviewAgent):
			writeError(w, r, http.StatusBadRequest, "UNSUPPORTED_AGENT", err.Error())
		case errors.Is(err, reviewloopsvc.ErrSessionSnapshotExpired):
			writeError(w, r, http.StatusGone, "SNAPSHOT_EXPIRED", "this session's environment has expired and can no longer be reviewed")
		case errors.Is(err, reviewloopsvc.ErrReviewLoopAlreadyRunning):
			writeError(w, r, http.StatusConflict, "REVIEW_LOOP_ALREADY_RUNNING", "a review loop is already running for this session")
		default:
			writeError(w, r, http.StatusInternalServerError, "START_REVIEW_FAILED", "failed to start review loop", err)
		}
		return
	}
	writeJSON(w, http.StatusCreated, models.SingleResponse[models.SessionReviewLoop]{Data: *loop})
}

func (h *ReviewLoopHandler) List(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	sessionID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid session ID")
		return
	}
	loops, err := h.store.ListLoopsBySession(r.Context(), orgID, sessionID)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "LIST_REVIEW_LOOPS_FAILED", "failed to list review loops", err)
		return
	}
	writeJSON(w, http.StatusOK, models.ListResponse[models.SessionReviewLoop]{Data: loops})
}

func (h *ReviewLoopHandler) Get(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	sessionID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid session ID")
		return
	}
	loopID, err := uuid.Parse(chi.URLParam(r, "loop_id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid review loop ID")
		return
	}
	loop, err := h.store.GetLoopByID(r.Context(), orgID, loopID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, r, http.StatusNotFound, "NOT_FOUND", "review loop not found")
			return
		}
		writeError(w, r, http.StatusInternalServerError, "GET_REVIEW_LOOP_FAILED", "failed to load review loop", err)
		return
	}
	if loop.SessionID != sessionID {
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "review loop not found")
		return
	}
	passes, err := h.store.ListPassesByLoop(r.Context(), orgID, loopID)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "GET_REVIEW_LOOP_FAILED", "failed to load review loop passes", err)
		return
	}
	writeJSON(w, http.StatusOK, models.SingleResponse[reviewLoopDetail]{Data: reviewLoopDetail{SessionReviewLoop: loop, Passes: passes}})
}

func (h *ReviewLoopHandler) Cancel(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	sessionID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid session ID")
		return
	}
	loopID, err := uuid.Parse(chi.URLParam(r, "loop_id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid review loop ID")
		return
	}
	loop, err := h.store.GetLoopByID(r.Context(), orgID, loopID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, r, http.StatusNotFound, "NOT_FOUND", "review loop not found")
			return
		}
		writeError(w, r, http.StatusInternalServerError, "CANCEL_REVIEW_LOOP_FAILED", "failed to load review loop", err)
		return
	}
	if loop.SessionID != sessionID {
		writeError(w, r, http.StatusNotFound, "NOT_FOUND", "review loop not found")
		return
	}
	if err := h.store.CancelLoop(r.Context(), orgID, loopID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, r, http.StatusNotFound, "NOT_FOUND", "review loop not found")
			return
		}
		writeError(w, r, http.StatusInternalServerError, "CANCEL_REVIEW_LOOP_FAILED", "failed to cancel review loop", err)
		return
	}
	writeJSON(w, http.StatusAccepted, map[string]string{"status": "cancelled"})
}
