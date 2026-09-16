package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	codereviewsvc "github.com/assembledhq/143/internal/services/codereview"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog"
)

type codeReviewSchedulingService interface {
	SchedulingEnabled() bool
	GetSchedule(context.Context, uuid.UUID, uuid.UUID) (models.CodeReviewPRState, error)
	RequestScheduledReview(context.Context, codereviewsvc.ScheduleRequestInput) (codereviewsvc.ScheduleRequestResult, error)
	PauseSchedule(context.Context, uuid.UUID, uuid.UUID, bool) error
	ListPendingSchedules(context.Context, uuid.UUID, *uuid.UUID, *uuid.UUID, int) ([]models.CodeReviewScheduledTarget, error)
}

func (h *CodeReviewHandler) scheduler(w http.ResponseWriter, r *http.Request) (codeReviewSchedulingService, bool) {
	s, ok := h.retryService.(codeReviewSchedulingService)
	if !ok || !s.SchedulingEnabled() {
		writeError(w, r, 503, "CODE_REVIEW_SCHEDULING_UNAVAILABLE", "review scheduling is unavailable")
		return nil, false
	}
	return s, true
}
func (h *CodeReviewHandler) GetSchedule(w http.ResponseWriter, r *http.Request) {
	s, ok := h.scheduler(w, r)
	if !ok {
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, 400, "INVALID_ID", "invalid pull request ID")
		return
	}
	result, err := s.GetSchedule(r.Context(), middleware.OrgIDFromContext(r.Context()), id)
	if err != nil {
		writeScheduleError(w, r, err)
		return
	}
	writeJSON(w, 200, models.SingleResponse[models.CodeReviewPRState]{Data: result})
}
func (h *CodeReviewHandler) RequestReviewNow(w http.ResponseWriter, r *http.Request) {
	s, ok := h.scheduler(w, r)
	if !ok {
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, 400, "INVALID_ID", "invalid pull request ID")
		return
	}
	user := middleware.UserFromContext(r.Context())
	if user == nil {
		writeError(w, r, 401, "UNAUTHORIZED", "user is required")
		return
	}
	var req struct {
		RequestID uuid.UUID                    `json:"request_id"`
		Mode      models.CodeReviewRequestMode `json:"mode"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.RequestID == uuid.Nil {
		writeError(w, r, 400, "CODE_REVIEW_REQUEST_INVALID", "request_id and mode are required")
		return
	}
	if string(req.Mode) == "force_fresh" {
		writeError(w, r, 409, "CODE_REVIEW_CAPABILITY_UNAVAILABLE", "force fresh review is not available")
		return
	}
	if err := req.Mode.Validate(); err != nil {
		writeError(w, r, 400, "CODE_REVIEW_REQUEST_INVALID", "invalid request mode", err)
		return
	}
	result, err := s.RequestScheduledReview(r.Context(), codereviewsvc.ScheduleRequestInput{OrgID: middleware.OrgIDFromContext(r.Context()), PullRequestID: id, RequestID: req.RequestID, RequesterID: &user.ID, Mode: req.Mode})
	if err != nil {
		writeScheduleError(w, r, err)
		return
	}
	status := http.StatusAccepted
	if result.Disposition == models.CodeReviewRequestReused || result.Disposition == models.CodeReviewRequestCancelled {
		status = http.StatusOK
	}
	writeJSON(w, status, models.SingleResponse[codereviewsvc.ScheduleRequestResult]{Data: result})
	resourceID := id.String()
	details := marshalAuditDetails(*zerolog.Ctx(r.Context()), map[string]any{"request_id": req.RequestID, "mode": req.Mode, "disposition": result.Disposition})
	emitUserAudit(h.audit, r, models.AuditActionCodeReviewRequested, models.AuditResourcePullRequest, &resourceID, details)
}
func (h *CodeReviewHandler) PauseSchedule(w http.ResponseWriter, r *http.Request) {
	s, ok := h.scheduler(w, r)
	if !ok {
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, 400, "INVALID_ID", "invalid pull request ID")
		return
	}
	var req struct {
		Paused *bool `json:"automatic_paused"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || req.Paused == nil {
		writeError(w, r, 400, "CODE_REVIEW_REQUEST_INVALID", "automatic_paused is required")
		return
	}
	orgID := middleware.OrgIDFromContext(r.Context())
	if err := s.PauseSchedule(r.Context(), orgID, id, *req.Paused); err != nil {
		writeScheduleError(w, r, err)
		return
	}
	resourceID := id.String()
	details := marshalAuditDetails(*zerolog.Ctx(r.Context()), map[string]any{"automatic_paused": *req.Paused})
	emitUserAudit(h.audit, r, models.AuditActionCodeReviewScheduleUpdated, models.AuditResourcePullRequest, &resourceID, details)
	h.GetSchedule(w, r)
}
func (h *CodeReviewHandler) ListPendingSchedules(w http.ResponseWriter, r *http.Request) {
	s, ok := h.scheduler(w, r)
	if !ok {
		return
	}
	var repoID, after *uuid.UUID
	for key, target := range map[string]**uuid.UUID{"repository_id": &repoID, "cursor": &after} {
		if raw := r.URL.Query().Get(key); raw != "" {
			id, err := uuid.Parse(raw)
			if err != nil {
				writeError(w, r, 400, "INVALID_FILTER", "invalid "+key)
				return
			}
			*target = &id
		}
	}
	limit := 50
	if raw := r.URL.Query().Get("limit"); raw != "" {
		value, err := strconv.Atoi(raw)
		if err != nil || value < 1 || value > 100 {
			writeError(w, r, 400, "INVALID_LIMIT", "limit must be between 1 and 100")
			return
		}
		limit = value
	}
	rows, err := s.ListPendingSchedules(r.Context(), middleware.OrgIDFromContext(r.Context()), repoID, after, limit)
	if err != nil {
		writeScheduleError(w, r, err)
		return
	}
	next := ""
	if len(rows) == limit {
		next = rows[len(rows)-1].Schedule.ID.String()
	}
	writeJSON(w, 200, map[string]any{"data": rows, "meta": map[string]any{"next_cursor": next}})
}
func writeScheduleError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, pgx.ErrNoRows):
		writeError(w, r, 404, "CODE_REVIEW_NOT_FOUND", "review target not found")
	case errors.Is(err, db.ErrCodeReviewRequestConflict):
		writeError(w, r, 409, "CODE_REVIEW_REQUEST_ID_CONFLICT", "request ID was already used for different input")
	case errors.Is(err, codereviewsvc.ErrReviewIneligible):
		writeError(w, r, 409, "CODE_REVIEW_PR_INELIGIBLE", "Review requires an open, ready pull request and an enabled policy.")
	default:
		writeError(w, r, 500, "CODE_REVIEW_SCHEDULING_FAILED", "failed to update review scheduling", err)
	}
}
