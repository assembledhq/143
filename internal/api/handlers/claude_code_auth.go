package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/claudecodeauth"
)

// claudeCodeSubscriptionLabelMax bounds the handler-side length check for
// subscription labels. The org_credentials.label column is unbounded text,
// so this cap is purely to keep UI and log lines compact. Matches the
// equivalent limit in codex_auth.go.
const claudeCodeSubscriptionLabelMax = 100

// claudeCodePasteMax bounds the pasted "<code>#<state>" payload size so the
// handler rejects obviously unreasonable inputs before they reach the service.
const claudeCodePasteMax = 2048

// ClaudeCodeAuthHandler serves the /api/v1/settings/claude-code-auth endpoints.
// Mirrors CodexAuthHandler in spirit, but the Claude Code CLI uses an
// authorization-code + PKCE flow rather than device-code, so the endpoint
// shape differs: /initiate returns an authorize URL, and the user pastes the
// final code back via /complete (no polling).
//
// Every endpoint accepts an optional `scope` query param (or body field for
// POSTs). Org scope (the default) requires admin role; personal scope is
// available to any authenticated user and operates on the caller's own
// credential rows.
type ClaudeCodeAuthHandler struct {
	svc    *claudecodeauth.Service
	logger zerolog.Logger
}

// NewClaudeCodeAuthHandler wires a handler around the claudecodeauth service.
func NewClaudeCodeAuthHandler(svc *claudecodeauth.Service, logger zerolog.Logger) *ClaudeCodeAuthHandler {
	return &ClaudeCodeAuthHandler{svc: svc, logger: logger}
}

// Initiate starts a new PKCE auth flow at the requested scope and returns
// the authorize URL the user should open in a browser.
func (h *ClaudeCodeAuthHandler) Initiate(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	user := middleware.UserFromContext(r.Context())
	if user == nil {
		writeError(w, r, http.StatusUnauthorized, "UNAUTHENTICATED", "unauthenticated", nil)
		return
	}

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
	if body.Label == "" {
		writeError(w, r, http.StatusBadRequest, "INVALID_LABEL", "label is required for Claude subscriptions", nil)
		return
	}
	if len(body.Label) > claudeCodeSubscriptionLabelMax {
		writeError(w, r, http.StatusBadRequest, "INVALID_LABEL", fmt.Sprintf("label must be %d characters or fewer", claudeCodeSubscriptionLabelMax), nil)
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

	createdBy := user.ID

	resp, err := h.svc.InitiateOAuth(r.Context(), scope, &createdBy, body.Label)
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
		writeError(w, r, http.StatusInternalServerError, "AUTH_INITIATE_FAILED", "failed to initiate Claude OAuth", err)
		return
	}

	writeJSON(w, http.StatusOK, models.SingleResponse[claudecodeauth.InitiateResponse]{Data: *resp})
}

// Complete exchanges the user's pasted `<code>#<state>` string for Claude
// subscription tokens and promotes the pending row to active.
func (h *ClaudeCodeAuthHandler) Complete(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	user := middleware.UserFromContext(r.Context())
	if user == nil {
		writeError(w, r, http.StatusUnauthorized, "UNAUTHENTICATED", "unauthenticated", nil)
		return
	}

	var body struct {
		Label string `json:"label"`
		Code  string `json:"code"`
		Scope string `json:"scope"`
	}
	if r.Body == nil || r.ContentLength == 0 {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "request body is required", nil)
		return
	}
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&body); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_REQUEST", "invalid JSON body", err)
		return
	}

	body.Label = strings.TrimSpace(body.Label)
	body.Code = strings.TrimSpace(body.Code)

	if body.Label == "" {
		writeError(w, r, http.StatusBadRequest, "INVALID_LABEL", "label is required", nil)
		return
	}
	if len(body.Label) > claudeCodeSubscriptionLabelMax {
		writeError(w, r, http.StatusBadRequest, "INVALID_LABEL", fmt.Sprintf("label must be %d characters or fewer", claudeCodeSubscriptionLabelMax), nil)
		return
	}
	if body.Code == "" {
		writeError(w, r, http.StatusBadRequest, "INVALID_CODE", "code is required", nil)
		return
	}
	if len(body.Code) > claudeCodePasteMax {
		writeError(w, r, http.StatusBadRequest, "INVALID_CODE", fmt.Sprintf("code must be %d characters or fewer", claudeCodePasteMax), nil)
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

	resp, err := h.svc.CompleteOAuth(r.Context(), scope, body.Label, body.Code)
	if err != nil {
		switch {
		case errors.Is(err, claudecodeauth.ErrPendingAuthNotFound):
			writeError(w, r, http.StatusNotFound, "PENDING_AUTH_NOT_FOUND", err.Error(), err)
		case errors.Is(err, claudecodeauth.ErrPendingAuthExpired):
			writeError(w, r, http.StatusGone, "PENDING_AUTH_EXPIRED", "your login session expired — please click \"Open Anthropic login\" again to start a fresh flow", err)
		case errors.Is(err, claudecodeauth.ErrInvalidPaste):
			writeError(w, r, http.StatusBadRequest, "INVALID_CODE", "pasted code is invalid — paste the full <code>#<state> string Anthropic shows", err)
		default:
			writeError(w, r, http.StatusInternalServerError, "AUTH_COMPLETE_FAILED", "failed to complete Claude OAuth", err)
		}
		return
	}

	writeJSON(w, http.StatusOK, models.SingleResponse[claudecodeauth.CompleteResponse]{Data: *resp})
}

// List returns all connected Claude subscriptions at the requested scope.
//
// Available to any authenticated user — see CodexAuthHandler.List for the
// rationale. Mutations keep the admin gate on org scope.
func (h *ClaudeCodeAuthHandler) List(w http.ResponseWriter, r *http.Request) {
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
		subs = []claudecodeauth.SubscriptionInfo{}
	}

	writeJSON(w, http.StatusOK, models.ListResponse[claudecodeauth.SubscriptionInfo]{Data: subs})
}

// DisconnectByPath removes a specific Claude subscription by path param ID.
func (h *ClaudeCodeAuthHandler) DisconnectByPath(w http.ResponseWriter, r *http.Request) {
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
		if errors.Is(err, claudecodeauth.ErrCredentialNotFound) {
			writeError(w, r, http.StatusNotFound, "NOT_FOUND", "credential not found", nil)
			return
		}
		writeError(w, r, http.StatusInternalServerError, "AUTH_DISCONNECT_FAILED", "failed to disconnect Claude subscription", err)
		return
	}

	writeJSON(w, http.StatusOK, models.SingleResponse[map[string]bool]{
		Data: map[string]bool{"disconnected": true},
	})
}

// DisconnectAll removes every Claude subscription at the requested scope.
// Preserves any Anthropic API-key credential (label="") so fallback auth
// keeps working.
func (h *ClaudeCodeAuthHandler) DisconnectAll(w http.ResponseWriter, r *http.Request) {
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
		writeError(w, r, http.StatusInternalServerError, "AUTH_DISCONNECT_FAILED", "failed to disconnect Claude subscriptions", err)
		return
	}

	writeJSON(w, http.StatusOK, models.SingleResponse[map[string]bool]{
		Data: map[string]bool{"disconnected": true},
	})
}
