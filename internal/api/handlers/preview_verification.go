package handlers

import (
	"net/http"
	"strconv"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
)

type PreviewVerificationHandler struct {
	store *db.PreviewVerificationRunStore
}

func NewPreviewVerificationHandler(store *db.PreviewVerificationRunStore) *PreviewVerificationHandler {
	return &PreviewVerificationHandler{store: store}
}

func (h *PreviewVerificationHandler) ListBySession(w http.ResponseWriter, r *http.Request) {
	sessionID, err := uuid.Parse(chi.URLParam(r, "id"))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_SESSION_ID", "invalid session ID", err)
		return
	}
	limit := 20
	if raw := r.URL.Query().Get("limit"); raw != "" {
		parsed, parseErr := strconv.Atoi(raw)
		if parseErr != nil || parsed < 1 || parsed > 100 {
			writeError(w, r, http.StatusBadRequest, "INVALID_LIMIT", "limit must be between 1 and 100")
			return
		}
		limit = parsed
	}
	runs, err := h.store.ListBySession(r.Context(), middleware.OrgIDFromContext(r.Context()), sessionID, limit)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "LIST_VERIFICATION_RUNS_FAILED", "failed to list preview verification runs", err)
		return
	}
	writeJSON(w, http.StatusOK, models.ListResponse[models.PreviewVerificationRun]{Data: runs})
}
