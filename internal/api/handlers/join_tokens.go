package handlers

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
)

// JoinTokenHandler exposes admin CRUD for org join tokens — the multi-use
// links behind `curl -fsSL .../install/<token> | sh`. Routes are registered
// in the admin-only group; the handler itself only adds input validation
// and the one-time plaintext-token response.
type JoinTokenHandler struct {
	store   *db.OrgJoinTokenStore
	audit   *db.AuditEmitter
	baseURL string
}

func NewJoinTokenHandler(store *db.OrgJoinTokenStore, baseURL string) *JoinTokenHandler {
	return &JoinTokenHandler{store: store, baseURL: strings.TrimRight(baseURL, "/")}
}

func (h *JoinTokenHandler) SetAuditEmitter(audit *db.AuditEmitter) {
	h.audit = audit
}

type createJoinTokenRequest struct {
	Name          string `json:"name"`
	Role          string `json:"role"`
	MaxUses       *int   `json:"max_uses,omitempty"`
	ExpiresInDays *int   `json:"expires_in_days,omitempty"`
}

// joinTokenView is the list/create response row: everything an admin needs
// to recognize and manage a link, never the hash.
type joinTokenView struct {
	ID          uuid.UUID              `json:"id"`
	TokenPrefix string                 `json:"token_prefix"`
	CanReveal   bool                   `json:"can_reveal"`
	Name        string                 `json:"name"`
	Role        models.Role            `json:"role"`
	MaxUses     *int                   `json:"max_uses,omitempty"`
	UseCount    int                    `json:"use_count"`
	ExpiresAt   *time.Time             `json:"expires_at,omitempty"`
	Status      models.JoinTokenStatus `json:"status"`
	CreatedAt   time.Time              `json:"created_at"`
}

func joinTokenViewOf(t models.OrgJoinToken, now time.Time) joinTokenView {
	return joinTokenView{
		ID:          t.ID,
		TokenPrefix: t.TokenPrefix,
		CanReveal:   len(t.RawTokenEncrypted) > 0,
		Name:        t.Name,
		Role:        t.Role,
		MaxUses:     t.MaxUses,
		UseCount:    t.UseCount,
		ExpiresAt:   t.ExpiresAt,
		Status:      t.Status(now),
		CreatedAt:   t.CreatedAt,
	}
}

// Create mints a join token and returns the plaintext with the
// ready-to-paste install one-liner. The raw token is also stored encrypted
// for later admin re-copy; validation still uses only token_hash.
func (h *JoinTokenHandler) Create(w http.ResponseWriter, r *http.Request) {
	user := middleware.UserFromContext(r.Context())
	orgID := middleware.OrgIDFromContext(r.Context())
	if user == nil || orgID == uuid.Nil {
		writeError(w, r, http.StatusUnauthorized, "UNAUTHORIZED", "not authenticated")
		return
	}

	var body createJoinTokenRequest
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_BODY", "invalid JSON body")
		return
	}
	role := models.Role(strings.TrimSpace(body.Role))
	if role == "" {
		role = models.RoleMember
	}
	if err := role.Validate(); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ROLE", "role must be one of admin, member, builder, viewer")
		return
	}
	if body.MaxUses != nil && *body.MaxUses < 1 {
		writeError(w, r, http.StatusBadRequest, "INVALID_MAX_USES", "max_uses must be at least 1")
		return
	}
	if body.ExpiresInDays != nil && (*body.ExpiresInDays < 1 || *body.ExpiresInDays > 365) {
		writeError(w, r, http.StatusBadRequest, "INVALID_EXPIRY", "expires_in_days must be between 1 and 365")
		return
	}

	rawToken, err := db.GenerateOrgJoinToken()
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "JOIN_TOKEN_CREATE_FAILED", "failed to generate token", err)
		return
	}

	token := &models.OrgJoinToken{
		OrgID:           orgID,
		TokenHash:       db.HashAPIToken(rawToken),
		TokenPrefix:     db.OrgJoinTokenDisplayPrefix(rawToken),
		Role:            role,
		Name:            strings.TrimSpace(body.Name),
		CreatedByUserID: user.ID,
		MaxUses:         body.MaxUses,
	}
	if body.ExpiresInDays != nil {
		expiresAt := time.Now().AddDate(0, 0, *body.ExpiresInDays)
		token.ExpiresAt = &expiresAt
	}
	if err := h.store.Create(r.Context(), token, rawToken); err != nil {
		writeError(w, r, http.StatusInternalServerError, "JOIN_TOKEN_CREATE_FAILED", "failed to create join token", err)
		return
	}

	h.emitJoinTokenEvent(r, user.ID, token, models.AuditActionOrgJoinTokenCreated)

	writeJSON(w, http.StatusCreated, map[string]any{
		"data": map[string]any{
			"id":              token.ID,
			"token":           rawToken,
			"token_prefix":    token.TokenPrefix,
			"role":            token.Role,
			"name":            token.Name,
			"expires_at":      token.ExpiresAt,
			"max_uses":        token.MaxUses,
			"install_command": fmt.Sprintf("curl -fsSL %s/install/%s | sh", h.baseURL, rawToken),
		},
	})
}

// List returns the org's non-revoked join tokens with derived lifecycle
// status. Revoked links are filtered out at the store so they don't clutter
// the settings list.
func (h *JoinTokenHandler) List(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	if orgID == uuid.Nil {
		writeError(w, r, http.StatusUnauthorized, "UNAUTHORIZED", "not authenticated")
		return
	}
	tokens, err := h.store.List(r.Context(), orgID)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "JOIN_TOKEN_LIST_FAILED", "failed to list join tokens", err)
		return
	}
	now := time.Now()
	views := make([]joinTokenView, 0, len(tokens))
	for _, t := range tokens {
		views = append(views, joinTokenViewOf(t, now))
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": views})
}

// GetLink returns the ready-to-paste install command for a recoverable
// active join token. It is deliberately separate from List so loading the
// settings page doesn't expose every bearer join URL to browser state.
func (h *JoinTokenHandler) GetLink(w http.ResponseWriter, r *http.Request) {
	user := middleware.UserFromContext(r.Context())
	orgID := middleware.OrgIDFromContext(r.Context())
	if user == nil || orgID == uuid.Nil {
		writeError(w, r, http.StatusUnauthorized, "UNAUTHORIZED", "not authenticated")
		return
	}
	id, err := uuid.Parse(strings.TrimSpace(chi.URLParam(r, "id")))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_TOKEN_ID", "token id must be a UUID")
		return
	}
	token, rawToken, err := h.store.GetActiveRecoverableToken(r.Context(), orgID, id)
	if err != nil {
		switch {
		case errors.Is(err, pgx.ErrNoRows):
			writeError(w, r, http.StatusNotFound, "JOIN_TOKEN_NOT_FOUND", "active join token not found")
		case errors.Is(err, db.ErrOrgJoinTokenNotRecoverable):
			writeError(w, r, http.StatusConflict, "JOIN_TOKEN_NOT_RECOVERABLE", "join token was created before link recovery was available")
		default:
			writeError(w, r, http.StatusInternalServerError, "JOIN_TOKEN_LINK_FAILED", "failed to recover join token", err)
		}
		return
	}
	h.emitJoinTokenEvent(r, user.ID, &token, models.AuditActionOrgJoinTokenRevealed)
	writeJSON(w, http.StatusOK, map[string]any{
		"data": map[string]any{
			"id":              token.ID,
			"token_prefix":    token.TokenPrefix,
			"install_command": fmt.Sprintf("curl -fsSL %s/install/%s | sh", h.baseURL, rawToken),
		},
	})
}

// Revoke kills a join link. One click here is the mitigation for the
// token's deliberate exposure in URLs and shell history.
func (h *JoinTokenHandler) Revoke(w http.ResponseWriter, r *http.Request) {
	user := middleware.UserFromContext(r.Context())
	orgID := middleware.OrgIDFromContext(r.Context())
	if user == nil || orgID == uuid.Nil {
		writeError(w, r, http.StatusUnauthorized, "UNAUTHORIZED", "not authenticated")
		return
	}
	id, err := uuid.Parse(strings.TrimSpace(chi.URLParam(r, "id")))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_TOKEN_ID", "token id must be a UUID")
		return
	}
	token, err := h.store.Revoke(r.Context(), orgID, id, user.ID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, r, http.StatusNotFound, "JOIN_TOKEN_NOT_FOUND", "join token not found or already revoked")
			return
		}
		writeError(w, r, http.StatusInternalServerError, "JOIN_TOKEN_REVOKE_FAILED", "failed to revoke join token", err)
		return
	}
	h.emitJoinTokenEvent(r, user.ID, &token, models.AuditActionOrgJoinTokenRevoked)
	writeJSON(w, http.StatusOK, map[string]any{"data": joinTokenViewOf(token, time.Now())})
}

func (h *JoinTokenHandler) emitJoinTokenEvent(r *http.Request, userID uuid.UUID, token *models.OrgJoinToken, action models.AuditAction) {
	if h.audit == nil {
		return
	}
	tokenID := token.ID.String()
	details, _ := json.Marshal(map[string]any{
		"token_prefix": token.TokenPrefix,
		"token_name":   token.Name,
		"role":         token.Role,
		"max_uses":     token.MaxUses,
		"expires_at":   token.ExpiresAt,
	})
	h.audit.EmitUserAction(r.Context(), db.UserActionParams{
		OrgID:        token.OrgID,
		UserID:       userID,
		Action:       action,
		ResourceType: models.AuditResourceOrgJoinToken,
		ResourceID:   &tokenID,
		Details:      details,
	})
}
