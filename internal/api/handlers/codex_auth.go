package handlers

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/codexauth"
)

// CodexAuthHandler serves the /api/v1/settings/codex-auth endpoints.
//
// Every endpoint accepts an optional `scope` query param (or body field for
// POSTs). Org scope (the default) requires admin role; personal scope is
// available to any authenticated user and operates on the caller's own
// credential rows. Personal-scope writes flow through the unified
// coding_credentials table; org-scope writes flow through the legacy
// org_credentials table with a mirror to coding_credentials.
type CodexAuthHandler struct {
	svc    *codexauth.Service
	logger zerolog.Logger
}

// NewCodexAuthHandler creates a new handler wrapping the codexauth service.
func NewCodexAuthHandler(svc *codexauth.Service, logger zerolog.Logger) *CodexAuthHandler {
	return &CodexAuthHandler{svc: svc, logger: logger}
}

// Initiate starts a new device code auth flow at the requested scope.
func (h *CodexAuthHandler) Initiate(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	user := middleware.UserFromContext(r.Context())
	if user == nil {
		writeError(w, r, http.StatusUnauthorized, "UNAUTHENTICATED", "unauthenticated", nil)
		return
	}

	// Parse optional label + scope from request body. An empty body keeps
	// legacy-compat single-subscription org callers working.
	var body struct {
		Label string `json:"label"`
		Scope string `json:"scope"`
	}
	if r.Body != nil && r.ContentLength != 0 {
		dec := json.NewDecoder(r.Body)
		dec.DisallowUnknownFields()
		if err := dec.Decode(&body); err != nil && err != io.EOF {
			writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "invalid JSON body", err)
			return
		}
	}

	body.Label = strings.TrimSpace(body.Label)
	if len(body.Label) > 100 {
		writeError(w, r, http.StatusBadRequest, "INVALID_LABEL", "label must be 100 characters or fewer", nil)
		return
	}

	scope, err := resolveOAuthScope(
		orgID,
		user.ID,
		middleware.ActiveRoleFromContext(r.Context()),
		strings.ToLower(strings.TrimSpace(body.Scope)),
	)
	if err != nil {
		writeAuthScopeError(w, r, err)
		return
	}

	// createdBy records who added the subscription. Set unconditionally —
	// even on personal scope it's the same user, but storing it keeps the
	// audit trail uniform.
	createdBy := user.ID

	resp, err := h.svc.InitiateDeviceAuth(r.Context(), scope, &createdBy, body.Label)
	if err != nil {
		var labelErr *db.ErrCredentialLabelTaken
		if errors.As(err, &labelErr) {
			writeError(w, r, http.StatusConflict, "LABEL_TAKEN", labelErr.Error(), err)
			return
		}
		var labelErr2 *db.ErrCodingCredentialLabelTaken
		if errors.As(err, &labelErr2) {
			writeError(w, r, http.StatusConflict, "LABEL_TAKEN", labelErr2.Error(), err)
			return
		}
		writeError(w, r, http.StatusInternalServerError, "AUTH_INITIATE_FAILED", "failed to initiate device auth", err)
		return
	}

	writeJSON(w, http.StatusOK, models.SingleResponse[codexauth.DeviceAuthResponse]{Data: *resp})
}

// Status checks whether the device code auth flow has completed.
func (h *CodexAuthHandler) Status(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	user := middleware.UserFromContext(r.Context())
	if user == nil {
		writeError(w, r, http.StatusUnauthorized, "UNAUTHENTICATED", "unauthenticated", nil)
		return
	}

	label := r.URL.Query().Get("label")
	if len(label) > 100 {
		writeError(w, r, http.StatusBadRequest, "INVALID_LABEL", "label must be 100 characters or fewer", nil)
		return
	}

	scope, err := resolveOAuthScope(
		orgID,
		user.ID,
		middleware.ActiveRoleFromContext(r.Context()),
		strings.ToLower(strings.TrimSpace(r.URL.Query().Get("scope"))),
	)
	if err != nil {
		writeAuthScopeError(w, r, err)
		return
	}

	status, err := h.svc.PollForToken(r.Context(), scope, label)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "AUTH_STATUS_FAILED", "failed to check auth status", err)
		return
	}

	writeJSON(w, http.StatusOK, models.SingleResponse[codexauth.AuthStatus]{Data: *status})
}

// List returns all connected Codex subscriptions at the requested scope.
//
// Available to any authenticated user — org scope is intentionally not
// admin-gated here so coding-agent settings pages can render the org
// fallback list to non-admin members. The mutation endpoints (Initiate,
// Disconnect*) keep the admin gate on org scope.
func (h *CodexAuthHandler) List(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	user := middleware.UserFromContext(r.Context())
	if user == nil {
		writeError(w, r, http.StatusUnauthorized, "UNAUTHENTICATED", "unauthenticated", nil)
		return
	}
	scope := models.Scope{OrgID: orgID}
	if strings.ToLower(strings.TrimSpace(r.URL.Query().Get("scope"))) == models.CodingCredentialScopePersonal {
		uid := user.ID
		scope = models.Scope{OrgID: orgID, UserID: &uid}
	}

	subs, err := h.svc.ListSubscriptions(r.Context(), scope)
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
	user := middleware.UserFromContext(r.Context())
	if user == nil {
		writeError(w, r, http.StatusUnauthorized, "UNAUTHENTICATED", "unauthenticated", nil)
		return
	}
	idStr := chi.URLParam(r, "id")
	credID, err := uuid.Parse(idStr)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid credential id", err)
		return
	}

	scope, err := resolveOAuthScope(
		orgID,
		user.ID,
		middleware.ActiveRoleFromContext(r.Context()),
		strings.ToLower(strings.TrimSpace(r.URL.Query().Get("scope"))),
	)
	if err != nil {
		writeAuthScopeError(w, r, err)
		return
	}

	if err := h.svc.DisconnectForOrg(r.Context(), scope, credID); err != nil {
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

// DisconnectAll removes all ChatGPT OAuth credentials at the given scope.
// Kept for backward compatibility with the old POST /disconnect endpoint.
func (h *CodexAuthHandler) DisconnectAll(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	user := middleware.UserFromContext(r.Context())
	if user == nil {
		writeError(w, r, http.StatusUnauthorized, "UNAUTHENTICATED", "unauthenticated", nil)
		return
	}
	scope, err := resolveOAuthScope(
		orgID,
		user.ID,
		middleware.ActiveRoleFromContext(r.Context()),
		strings.ToLower(strings.TrimSpace(r.URL.Query().Get("scope"))),
	)
	if err != nil {
		writeAuthScopeError(w, r, err)
		return
	}

	if err := h.svc.DisconnectAll(r.Context(), scope); err != nil {
		writeError(w, r, http.StatusInternalServerError, "AUTH_DISCONNECT_FAILED", "failed to disconnect ChatGPT", err)
		return
	}

	writeJSON(w, http.StatusOK, models.SingleResponse[map[string]bool]{
		Data: map[string]bool{"disconnected": true},
	})
}
