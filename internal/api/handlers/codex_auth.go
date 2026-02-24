package handlers

import (
	"net/http"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/codexauth"
)

// CodexAuthHandler serves the /api/v1/settings/codex-auth endpoints.
type CodexAuthHandler struct {
	svc *codexauth.Service
}

// NewCodexAuthHandler creates a new handler wrapping the codexauth service.
func NewCodexAuthHandler(svc *codexauth.Service) *CodexAuthHandler {
	return &CodexAuthHandler{svc: svc}
}

// Initiate starts a new device code auth flow.
func (h *CodexAuthHandler) Initiate(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())

	resp, err := h.svc.InitiateDeviceAuth(r.Context(), orgID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "AUTH_INITIATE_FAILED", "failed to initiate device auth")
		return
	}

	writeJSON(w, http.StatusOK, models.SingleResponse[codexauth.DeviceAuthResponse]{Data: *resp})
}

// Status checks whether the device code auth flow has completed.
func (h *CodexAuthHandler) Status(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())

	status, err := h.svc.PollForToken(r.Context(), orgID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "AUTH_STATUS_FAILED", "failed to check auth status")
		return
	}

	writeJSON(w, http.StatusOK, models.SingleResponse[codexauth.AuthStatus]{Data: *status})
}

// Disconnect removes the ChatGPT OAuth credential.
func (h *CodexAuthHandler) Disconnect(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())

	if err := h.svc.Disconnect(r.Context(), orgID); err != nil {
		writeError(w, http.StatusInternalServerError, "AUTH_DISCONNECT_FAILED", "failed to disconnect ChatGPT")
		return
	}

	writeJSON(w, http.StatusOK, models.SingleResponse[map[string]bool]{
		Data: map[string]bool{"disconnected": true},
	})
}
