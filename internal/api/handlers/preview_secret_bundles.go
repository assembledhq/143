package handlers

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/models"
)

type previewSecretBundleStore interface {
	UpsertEnv(ctx context.Context, orgID, createdBy uuid.UUID, name string, env map[string]string) error
	ListSummaries(ctx context.Context, orgID uuid.UUID) ([]models.PreviewSecretBundleSummary, error)
	Delete(ctx context.Context, orgID uuid.UUID, name string) error
}

// PreviewSecretBundleHandler serves team-managed preview secret bundles.
type PreviewSecretBundleHandler struct {
	store previewSecretBundleStore
}

func NewPreviewSecretBundleHandler(store previewSecretBundleStore) *PreviewSecretBundleHandler {
	return &PreviewSecretBundleHandler{store: store}
}

func (h *PreviewSecretBundleHandler) List(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())

	summaries, err := h.store.ListSummaries(r.Context(), orgID)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "LIST_FAILED", "failed to list preview secret bundles", err)
		return
	}
	if summaries == nil {
		summaries = []models.PreviewSecretBundleSummary{}
	}
	writeJSON(w, http.StatusOK, models.ListResponse[models.PreviewSecretBundleSummary]{Data: summaries})
}

func (h *PreviewSecretBundleHandler) Upsert(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	user := middleware.UserFromContext(r.Context())
	if user == nil {
		writeError(w, r, http.StatusUnauthorized, "UNAUTHORIZED", "missing user")
		return
	}

	pathName := chi.URLParam(r, "name")
	var input models.PreviewSecretBundleInput
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_JSON", "invalid request body", err)
		return
	}
	if input.Name != "" && input.Name != pathName {
		writeError(w, r, http.StatusBadRequest, "INVALID_NAME", "request body name must match the URL name")
		return
	}
	input.Name = pathName
	if err := input.Validate(); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_CONFIG", err.Error(), err)
		return
	}

	if err := h.store.UpsertEnv(r.Context(), orgID, user.ID, input.Name, input.Env); err != nil {
		writeError(w, r, http.StatusInternalServerError, "UPSERT_FAILED", "failed to save preview secret bundle", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *PreviewSecretBundleHandler) Delete(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	name := chi.URLParam(r, "name")

	if err := h.store.Delete(r.Context(), orgID, name); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, r, http.StatusNotFound, "NOT_FOUND", "preview secret bundle not found", err)
			return
		}
		writeError(w, r, http.StatusInternalServerError, "DELETE_FAILED", "failed to delete preview secret bundle", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
