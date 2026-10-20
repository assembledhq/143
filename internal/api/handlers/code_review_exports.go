package handlers

import (
	"context"
	"errors"
	"net/http"
	"strconv"
	"time"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	codereviewsvc "github.com/assembledhq/143/internal/services/codereview"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type historicalExportService interface {
	Export(context.Context, uuid.UUID, models.CodeReviewExportFilters, string, int) (models.CodeReviewExportPage, error)
}
type CodeReviewExportHandler struct{ service historicalExportService }

func NewCodeReviewExportHandler(service *codereviewsvc.HistoricalExportService) *CodeReviewExportHandler {
	return &CodeReviewExportHandler{service: service}
}

func (h *CodeReviewExportHandler) Export(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	if orgID == uuid.Nil {
		writeError(w, r, http.StatusForbidden, "FORBIDDEN", "organization required")
		return
	}
	q := r.URL.Query()
	for key, values := range q {
		switch key {
		case "repository_id", "from", "until", "time_basis", "cursor", "limit":
		default:
			writeError(w, r, http.StatusBadRequest, "INVALID_EXPORT_FILTER", "unknown export parameter")
			return
		}
		if len(values) != 1 || values[0] == "" {
			writeError(w, r, http.StatusBadRequest, "INVALID_EXPORT_FILTER", "parameters must have one nonempty value")
			return
		}
	}
	repositoryID, err := uuid.Parse(q.Get("repository_id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_EXPORT_FILTER", "repository_id is required")
		return
	}
	from, err := time.Parse(time.RFC3339Nano, q.Get("from"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_EXPORT_FILTER", "from must be RFC3339")
		return
	}
	until, err := time.Parse(time.RFC3339Nano, q.Get("until"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_EXPORT_FILTER", "until must be RFC3339")
		return
	}
	basis := models.ReviewExportTimeBasis(q.Get("time_basis"))
	if basis == "" {
		basis = models.ReviewExportCreatedOrCompleted
	}
	filters := models.CodeReviewExportFilters{RepositoryID: repositoryID, From: from.UTC(), Until: until.UTC(), TimeBasis: basis, IncludeUnresolvedPublicationTime: true}
	if err = filters.Validate(); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_EXPORT_FILTER", err.Error())
		return
	}
	limit := 50
	if q.Has("limit") {
		limit, err = strconv.Atoi(q.Get("limit"))
		if err != nil || limit < 1 || limit > 100 {
			writeError(w, r, http.StatusBadRequest, "INVALID_EXPORT_FILTER", "limit must be 1 through 100")
			return
		}
	}
	page, err := h.service.Export(r.Context(), orgID, filters, q.Get("cursor"), limit)
	if err != nil {
		switch {
		case errors.Is(err, codereviewsvc.ErrHistoricalExportCursor), errors.Is(err, db.ErrReviewExportPosition):
			writeError(w, r, http.StatusBadRequest, "INVALID_EXPORT_CURSOR", "cursor does not match acquisition filters")
		case errors.Is(err, pgx.ErrNoRows):
			writeError(w, r, http.StatusNotFound, "REPOSITORY_NOT_FOUND", "repository not found")
		case errors.Is(err, db.ErrReviewExportExpired):
			writeError(w, r, http.StatusGone, "EXPORT_EXPIRED", "export unavailable or expired; acquire a new export")
		case errors.Is(err, db.ErrReviewExportLimit):
			writeError(w, r, http.StatusTooManyRequests, "EXPORT_LIMIT", "export acquisition busy, too many active exports, or snapshot exceeds bounds; reuse a cursor or narrow filters")
		default:
			writeError(w, r, http.StatusInternalServerError, "EXPORT_FAILED", "failed to acquire historical review export", err)
		}
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	writeJSON(w, http.StatusOK, page)
}
