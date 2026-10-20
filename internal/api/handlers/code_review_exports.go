package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	codereviewsvc "github.com/assembledhq/143/internal/services/codereview"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type historicalExportService interface {
	Status(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) (models.CodeReviewExportStatus, error)
	Release(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, models.CodeReviewExportArchiveProof) (models.CodeReviewExportStatus, error)
	Export(context.Context, uuid.UUID, models.CodeReviewExportFilters, string, int) (models.CodeReviewExportPage, error)
}
type CodeReviewExportHandler struct{ service historicalExportService }

func NewCodeReviewExportHandler(service *codereviewsvc.HistoricalExportService) *CodeReviewExportHandler {
	return &CodeReviewExportHandler{service: service}
}

func (h *CodeReviewExportHandler) Export(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
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
		writeExportError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, page)
}

func writeExportError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, codereviewsvc.ErrHistoricalExportCursor), errors.Is(err, db.ErrReviewExportPosition):
		writeError(w, r, http.StatusBadRequest, "INVALID_EXPORT_CURSOR", "cursor does not match acquisition filters")
	case errors.Is(err, pgx.ErrNoRows):
		writeError(w, r, http.StatusNotFound, "REPOSITORY_NOT_FOUND", "repository not found")
	case errors.Is(err, db.ErrReviewExportReleased):
		writeError(w, r, http.StatusGone, "EXPORT_RELEASED", "export payloads have been released; acquire a new export")
	case errors.Is(err, db.ErrReviewExportExpired):
		writeError(w, r, http.StatusGone, "EXPORT_EXPIRED", "export unavailable or expired; acquire a new export")
	case errors.Is(err, db.ErrReviewExportBusy):
		writeError(w, r, http.StatusTooManyRequests, "EXPORT_BUSY", "acquisition lock busy; retry with backoff")
	case errors.Is(err, db.ErrReviewExportSlots):
		writeError(w, r, http.StatusTooManyRequests, "EXPORT_ACTIVE_SLOT_LIMIT", "active acquisition slots occupied; finish and explicitly release a verified archive or wait for expiry")
	case errors.Is(err, db.ErrReviewExportRows):
		writeError(w, r, http.StatusUnprocessableEntity, "EXPORT_ROW_LIMIT", "acquisition exceeds candidate row bound; narrow filters")
	case errors.Is(err, db.ErrReviewExportBytes):
		writeError(w, r, http.StatusUnprocessableEntity, "EXPORT_PAYLOAD_BYTE_LIMIT", "acquisition exceeds payload byte bound; narrow filters; individual records are never truncated")
	case errors.Is(err, db.ErrReviewExportArchive):
		writeError(w, r, http.StatusConflict, "EXPORT_ARCHIVE_MISMATCH", "verified archive counts must match the complete acquisition")
	case errors.Is(err, db.ErrReviewExportReleaseConflict):
		writeError(w, r, http.StatusConflict, "EXPORT_RELEASE_CONFLICT", "acquisition already released with a different archive attestation")
	default:
		writeError(w, r, http.StatusInternalServerError, "EXPORT_FAILED", "historical export operation failed", err)
	}
}

func (h *CodeReviewExportHandler) Status(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	orgID := middleware.OrgIDFromContext(r.Context())
	if orgID == uuid.Nil {
		writeError(w, r, http.StatusForbidden, "FORBIDDEN", "organization required")
		return
	}
	q := r.URL.Query()
	repositoryID, err := uuid.Parse(q.Get("repository_id"))
	id, idErr := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil || idErr != nil || repositoryID == uuid.Nil || id == uuid.Nil || len(q) != 1 || len(q["repository_id"]) != 1 {
		writeError(w, r, http.StatusBadRequest, "INVALID_EXPORT_FILTER", "one repository_id and a valid acquisition ID are required")
		return
	}
	status, err := h.service.Status(r.Context(), orgID, repositoryID, id)
	if err != nil {
		writeExportError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, status)
}

func (h *CodeReviewExportHandler) Release(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	orgID := middleware.OrgIDFromContext(r.Context())
	if orgID == uuid.Nil {
		writeError(w, r, http.StatusForbidden, "FORBIDDEN", "organization required")
		return
	}
	id, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil || id == uuid.Nil || r.URL.RawQuery != "" {
		writeError(w, r, http.StatusBadRequest, "INVALID_EXPORT_RELEASE", "a valid acquisition ID and no query parameters are required")
		return
	}
	var request models.CodeReviewExportReleaseRequest
	decoder := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&request); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_EXPORT_RELEASE", "invalid release body")
		return
	}
	if err = decoder.Decode(new(any)); err != io.EOF || request.RepositoryID == uuid.Nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_EXPORT_RELEASE", "one release object and repository_id are required")
		return
	}
	if err = request.Archive.Validate(); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_EXPORT_RELEASE", err.Error())
		return
	}
	status, err := h.service.Release(r.Context(), orgID, request.RepositoryID, id, request.Archive)
	if err != nil {
		writeExportError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, status)
}
