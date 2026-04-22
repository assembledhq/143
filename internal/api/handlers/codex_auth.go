package handlers

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/codexauth"
)

// CodexAuthHandler serves the /api/v1/settings/codex-auth endpoints.
type CodexAuthHandler struct {
	svc    *codexauth.Service
	logger zerolog.Logger
}

// NewCodexAuthHandler creates a new handler wrapping the codexauth service.
func NewCodexAuthHandler(svc *codexauth.Service, logger zerolog.Logger) *CodexAuthHandler {
	return &CodexAuthHandler{svc: svc, logger: logger}
}

// Initiate starts a new device code auth flow.
func (h *CodexAuthHandler) Initiate(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())

	// Track which user is adding this subscription. Auth middleware normally
	// populates this, but fall through as nil so tests without a user context
	// continue to work — the DB column is nullable.
	var createdBy *uuid.UUID
	if user := middleware.UserFromContext(r.Context()); user != nil {
		id := user.ID
		createdBy = &id
	}

	// Parse optional label from request body. An empty body is allowed (legacy
	// single-subscription flow), but malformed JSON is rejected so the client
	// learns about the problem instead of silently dropping its label.
	var body struct {
		Label string `json:"label"`
	}
	if r.Body != nil && r.ContentLength != 0 {
		dec := json.NewDecoder(r.Body)
		dec.DisallowUnknownFields()
		if err := dec.Decode(&body); err != nil && err != io.EOF {
			writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "invalid JSON body", err)
			return
		}
	}

	// Validate and normalize label.
	body.Label = normalizedSubscriptionLabel(body.Label)
	if len(body.Label) > 100 {
		writeError(w, r, http.StatusBadRequest, "INVALID_LABEL", "label must be 100 characters or fewer", nil)
		return
	}

	user := middleware.UserFromContext(r.Context())
	var resp *codexauth.DeviceAuthResponse
	label, err := resolveSubscriptionLabel(body.Label, user, 100, func(label string) error {
		var err error
		resp, err = h.svc.InitiateDeviceAuth(r.Context(), orgID, createdBy, label)
		return err
	})
	if err != nil {
		var labelErr *db.ErrCredentialLabelTaken
		if errors.As(err, &labelErr) {
			writeError(w, r, http.StatusConflict, "LABEL_TAKEN", labelErr.Error(), err)
			return
		}
		writeError(w, r, http.StatusInternalServerError, "AUTH_INITIATE_FAILED", "failed to initiate device auth", err)
		return
	}
	resp.Label = label

	writeJSON(w, http.StatusOK, models.SingleResponse[codexauth.DeviceAuthResponse]{Data: *resp})
}

// Status checks whether the device code auth flow has completed.
func (h *CodexAuthHandler) Status(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	label := r.URL.Query().Get("label")

	// Match Initiate's validation so callers can't probe arbitrary keys.
	if len(label) > 100 {
		writeError(w, r, http.StatusBadRequest, "INVALID_LABEL", "label must be 100 characters or fewer", nil)
		return
	}

	status, err := h.svc.PollForToken(r.Context(), orgID, label)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "AUTH_STATUS_FAILED", "failed to check auth status", err)
		return
	}

	writeJSON(w, http.StatusOK, models.SingleResponse[codexauth.AuthStatus]{Data: *status})
}

// List returns all connected Codex subscriptions.
func (h *CodexAuthHandler) List(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())

	subs, err := h.svc.ListSubscriptions(r.Context(), orgID)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "AUTH_LIST_FAILED", "failed to list subscriptions", err)
		return
	}

	if subs == nil {
		subs = []codexauth.SubscriptionInfo{}
	}

	writeJSON(w, http.StatusOK, models.ListResponse[codexauth.SubscriptionInfo]{Data: subs})
}

// DisconnectByPath removes a specific ChatGPT OAuth credential by path param ID.
func (h *CodexAuthHandler) DisconnectByPath(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	idStr := chi.URLParam(r, "id")
	credID, err := uuid.Parse(idStr)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid credential id", err)
		return
	}

	if err := h.svc.DisconnectForOrg(r.Context(), orgID, credID); err != nil {
		if errors.Is(err, codexauth.ErrCredentialNotFound) {
			writeError(w, r, http.StatusNotFound, "NOT_FOUND", "credential not found", nil)
			return
		}
		writeError(w, r, http.StatusInternalServerError, "AUTH_DISCONNECT_FAILED", "failed to disconnect ChatGPT", err)
		return
	}

	writeJSON(w, http.StatusOK, models.SingleResponse[map[string]bool]{
		Data: map[string]bool{"disconnected": true},
	})
}

// DisconnectAll removes all ChatGPT OAuth credentials for the given org.
// Kept for backward compatibility with the old POST /disconnect endpoint.
func (h *CodexAuthHandler) DisconnectAll(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())

	if err := h.svc.DisconnectAll(r.Context(), orgID); err != nil {
		writeError(w, r, http.StatusInternalServerError, "AUTH_DISCONNECT_FAILED", "failed to disconnect ChatGPT", err)
		return
	}

	writeJSON(w, http.StatusOK, models.SingleResponse[map[string]bool]{
		Data: map[string]bool{"disconnected": true},
	})
}
