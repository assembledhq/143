package handlers

import (
	"encoding/json"
	"net/http"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/db"
)

type UserNotificationPreferenceHandler struct {
	store *db.UserNotificationPreferenceStore
}

func NewUserNotificationPreferenceHandler(store *db.UserNotificationPreferenceStore) *UserNotificationPreferenceHandler {
	return &UserNotificationPreferenceHandler{store: store}
}

func (h *UserNotificationPreferenceHandler) Get(w http.ResponseWriter, r *http.Request) {
	user := middleware.UserFromContext(r.Context())
	if user == nil {
		writeError(w, r, http.StatusUnauthorized, "UNAUTHORIZED", "not authenticated")
		return
	}

	orgID := middleware.OrgIDFromContext(r.Context())
	pref, err := h.store.GetByUser(r.Context(), orgID, user.ID)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "PREFERENCE_LOOKUP_FAILED", "failed to load user notification preferences", err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"data": pref})
}

func (h *UserNotificationPreferenceHandler) Update(w http.ResponseWriter, r *http.Request) {
	user := middleware.UserFromContext(r.Context())
	if user == nil {
		writeError(w, r, http.StatusUnauthorized, "UNAUTHORIZED", "not authenticated")
		return
	}

	var body struct {
		SessionCompletionBrowserEnabled bool `json:"session_completion_browser_enabled"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_JSON", "invalid request body")
		return
	}

	orgID := middleware.OrgIDFromContext(r.Context())
	if err := h.store.Upsert(r.Context(), orgID, user.ID, body.SessionCompletionBrowserEnabled); err != nil {
		writeError(w, r, http.StatusInternalServerError, "PREFERENCE_UPDATE_FAILED", "failed to update user notification preferences", err)
		return
	}

	pref, err := h.store.GetByUser(r.Context(), orgID, user.ID)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "PREFERENCE_LOOKUP_FAILED", "failed to load user notification preferences", err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"data": pref})
}
