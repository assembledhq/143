package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/models"
)

type codingAuthStore interface {
	ListCodingAuths(ctx context.Context, orgID uuid.UUID) ([]models.CodingAuth, error)
	ListByProvider(ctx context.Context, orgID uuid.UUID, provider models.ProviderName) ([]models.DecryptedCredential, error)
	ReorderCodingAuths(ctx context.Context, orgID uuid.UUID, ids []uuid.UUID) error
	CreateCodingAuth(ctx context.Context, orgID uuid.UUID, createdBy *uuid.UUID, input models.CreateCodingAuthInput) (*models.CodingAuth, error)
	UpdateCodingAuth(ctx context.Context, orgID uuid.UUID, id uuid.UUID, input models.UpdateCodingAuthInput) (*models.CodingAuth, error)
	DisableCodingAuth(ctx context.Context, orgID uuid.UUID, id uuid.UUID) error
	DeleteCodingAuth(ctx context.Context, orgID uuid.UUID, id uuid.UUID) error
}

type codingAuthOrgStore interface {
	MergeCodingAgentDefaults(ctx context.Context, orgID uuid.UUID, agent models.AgentType, defaults map[string]string) error
}

type CodingAuthHandler struct {
	store       codingAuthStore
	orgStore    codingAuthOrgStore
	invalidator OrgSettingsInvalidator
}

func NewCodingAuthHandler(store codingAuthStore, orgStore codingAuthOrgStore) *CodingAuthHandler {
	return &CodingAuthHandler{store: store, orgStore: orgStore}
}

func (h *CodingAuthHandler) SetOrgSettingsInvalidator(invalidator OrgSettingsInvalidator) {
	h.invalidator = invalidator
}

func (h *CodingAuthHandler) List(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())

	rows, err := h.store.ListCodingAuths(r.Context(), orgID)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "LIST_FAILED", "failed to list coding auths", err)
		return
	}
	if rows == nil {
		rows = []models.CodingAuth{}
	}
	writeJSON(w, http.StatusOK, models.ListResponse[models.CodingAuth]{Data: rows})
}

func (h *CodingAuthHandler) Reorder(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())

	var body struct {
		IDs []string `json:"ids"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_JSON", "invalid request body")
		return
	}
	if len(body.IDs) == 0 {
		writeError(w, r, http.StatusBadRequest, "INVALID_IDS", "ids are required")
		return
	}

	ids := make([]uuid.UUID, 0, len(body.IDs))
	for _, raw := range body.IDs {
		id, err := uuid.Parse(raw)
		if err != nil {
			writeError(w, r, http.StatusBadRequest, "INVALID_ID", "ids must be valid UUIDs")
			return
		}
		ids = append(ids, id)
	}

	if err := h.store.ReorderCodingAuths(r.Context(), orgID, ids); err != nil {
		writeError(w, r, http.StatusInternalServerError, "REORDER_FAILED", "failed to reorder coding auths", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (h *CodingAuthHandler) Create(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	user := middleware.UserFromContext(r.Context())
	if user == nil {
		writeError(w, r, http.StatusUnauthorized, "UNAUTHORIZED", "user not found")
		return
	}

	var input models.CreateCodingAuthInput
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_JSON", "invalid request body")
		return
	}
	if err := input.Validate(); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_INPUT", err.Error())
		return
	}

	if len(input.AgentDefaults) > 0 && h.orgStore == nil {
		writeError(w, r, http.StatusInternalServerError, "CREATE_FAILED", "agent default writes are unavailable")
		return
	}

	row, err := h.store.CreateCodingAuth(r.Context(), orgID, &user.ID, input)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "CREATE_FAILED", "failed to create coding auth", err)
		return
	}

	if len(input.AgentDefaults) > 0 {
		if err := h.orgStore.MergeCodingAgentDefaults(r.Context(), orgID, input.Agent, input.AgentDefaults); err != nil {
			if rollbackErr := h.store.DeleteCodingAuth(r.Context(), orgID, row.ID); rollbackErr != nil {
				err = fmt.Errorf("persist agent defaults: %w; rollback coding auth delete: %v", err, rollbackErr)
			}
			writeError(w, r, http.StatusInternalServerError, "CREATE_FAILED", "failed to save coding auth defaults", err)
			return
		}
		if h.invalidator != nil {
			h.invalidator.InvalidateOrg(orgID)
		}
	}
	writeJSON(w, http.StatusOK, models.SingleResponse[models.CodingAuth]{Data: *row})
}

func (h *CodingAuthHandler) Update(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	id, ok := parseCodingAuthID(w, r)
	if !ok {
		return
	}

	var input models.UpdateCodingAuthInput
	if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_JSON", "invalid request body")
		return
	}

	row, err := h.store.UpdateCodingAuth(r.Context(), orgID, id, input)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "UPDATE_FAILED", "failed to update coding auth", err)
		return
	}
	writeJSON(w, http.StatusOK, models.SingleResponse[models.CodingAuth]{Data: *row})
}

func (h *CodingAuthHandler) Delete(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	id, ok := parseCodingAuthID(w, r)
	if !ok {
		return
	}

	if err := h.store.DisableCodingAuth(r.Context(), orgID, id); err != nil {
		writeError(w, r, http.StatusInternalServerError, "DELETE_FAILED", "failed to disable coding auth", err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func parseCodingAuthID(w http.ResponseWriter, r *http.Request) (uuid.UUID, bool) {
	raw := chi.URLParam(r, "id")
	if raw == "" {
		raw = r.PathValue("id")
	}
	id, err := uuid.Parse(raw)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "id must be a valid UUID")
		return uuid.Nil, false
	}
	return id, true
}
