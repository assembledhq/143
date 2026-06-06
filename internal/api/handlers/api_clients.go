package handlers

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog"
)

type APIClientHandler struct {
	clients *db.APIClientStore
	tokens  *db.APITokenStore
	audit   *db.AuditEmitter
	logger  zerolog.Logger
}

func NewAPIClientHandler(clients *db.APIClientStore, tokens *db.APITokenStore) *APIClientHandler {
	return &APIClientHandler{clients: clients, tokens: tokens, logger: zerolog.Nop()}
}

func (h *APIClientHandler) SetAuditEmitter(audit *db.AuditEmitter) {
	h.audit = audit
}

func (h *APIClientHandler) SetLogger(logger zerolog.Logger) {
	h.logger = logger
}

func (h *APIClientHandler) List(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	clients, err := h.clients.List(r.Context(), orgID)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "LIST_FAILED", "failed to list API clients", err)
		return
	}
	if clients == nil {
		clients = []models.APIClient{}
	}
	writeJSON(w, http.StatusOK, models.ListResponse[models.APIClient]{Data: clients})
}

func (h *APIClientHandler) Create(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	user := middleware.UserFromContext(r.Context())
	if user == nil {
		writeError(w, r, http.StatusUnauthorized, "UNAUTHORIZED", "authentication required")
		return
	}

	var req struct {
		Name        string  `json:"name"`
		Description *string `json:"description"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_JSON", "invalid request body")
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		writeError(w, r, http.StatusBadRequest, "MISSING_FIELD", "name is required")
		return
	}
	client := &models.APIClient{
		OrgID:           orgID,
		Name:            name,
		Description:     trimOptionalString(req.Description),
		Status:          models.APIClientStatusEnabled,
		CreatedByUserID: &user.ID,
	}
	if err := h.clients.Create(r.Context(), client); err != nil {
		writeError(w, r, http.StatusInternalServerError, "CREATE_FAILED", "failed to create API client", err)
		return
	}
	id := client.ID.String()
	emitUserAudit(h.audit, r, models.AuditActionAPIClientCreated, models.AuditResourceAPIClient, &id, marshalAuditDetails(h.logger, map[string]any{
		"name": client.Name,
	}))
	writeJSON(w, http.StatusCreated, models.SingleResponse[models.APIClient]{Data: *client})
}

func (h *APIClientHandler) Get(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	clientID, ok := parsePathUUID(w, r, "id")
	if !ok {
		return
	}
	client, err := h.clients.Get(r.Context(), orgID, clientID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, r, http.StatusNotFound, "NOT_FOUND", "API client not found")
			return
		}
		writeError(w, r, http.StatusInternalServerError, "GET_FAILED", "failed to get API client", err)
		return
	}
	writeJSON(w, http.StatusOK, models.SingleResponse[models.APIClient]{Data: client})
}

func (h *APIClientHandler) Update(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	user := middleware.UserFromContext(r.Context())
	if user == nil {
		writeError(w, r, http.StatusUnauthorized, "UNAUTHORIZED", "authentication required")
		return
	}
	clientID, ok := parsePathUUID(w, r, "id")
	if !ok {
		return
	}
	client, err := h.clients.Get(r.Context(), orgID, clientID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, r, http.StatusNotFound, "NOT_FOUND", "API client not found")
			return
		}
		writeError(w, r, http.StatusInternalServerError, "GET_FAILED", "failed to get API client", err)
		return
	}

	var req struct {
		Name        *string                 `json:"name"`
		Description *string                 `json:"description"`
		Status      *models.APIClientStatus `json:"status"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_JSON", "invalid request body")
		return
	}
	if req.Name != nil {
		name := strings.TrimSpace(*req.Name)
		if name == "" {
			writeError(w, r, http.StatusBadRequest, "MISSING_FIELD", "name must not be empty")
			return
		}
		client.Name = name
	}
	if req.Description != nil {
		client.Description = trimOptionalString(req.Description)
	}
	if req.Status != nil {
		if err := req.Status.Validate(); err != nil {
			writeError(w, r, http.StatusBadRequest, "INVALID_STATUS", err.Error())
			return
		}
		client.Status = *req.Status
		if client.Status == models.APIClientStatusDisabled {
			now := time.Now()
			client.DisabledByUserID = &user.ID
			client.DisabledAt = &now
		}
	}
	if err := h.clients.Update(r.Context(), &client); err != nil {
		writeError(w, r, http.StatusInternalServerError, "UPDATE_FAILED", "failed to update API client", err)
		return
	}
	id := client.ID.String()
	action := models.AuditActionAPIClientUpdated
	if client.Status == models.APIClientStatusDisabled {
		action = models.AuditActionAPIClientDisabled
	}
	emitUserAudit(h.audit, r, action, models.AuditResourceAPIClient, &id, marshalAuditDetails(h.logger, map[string]any{
		"name":   client.Name,
		"status": client.Status,
	}))
	writeJSON(w, http.StatusOK, models.SingleResponse[models.APIClient]{Data: client})
}

func (h *APIClientHandler) Delete(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	user := middleware.UserFromContext(r.Context())
	if user == nil {
		writeError(w, r, http.StatusUnauthorized, "UNAUTHORIZED", "authentication required")
		return
	}
	clientID, ok := parsePathUUID(w, r, "id")
	if !ok {
		return
	}
	if err := h.clients.Disable(r.Context(), orgID, clientID, user.ID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, r, http.StatusNotFound, "NOT_FOUND", "API client not found")
			return
		}
		writeError(w, r, http.StatusInternalServerError, "DISABLE_FAILED", "failed to disable API client", err)
		return
	}
	id := clientID.String()
	emitUserAudit(h.audit, r, models.AuditActionAPIClientDisabled, models.AuditResourceAPIClient, &id, nil)
	w.WriteHeader(http.StatusNoContent)
}

func (h *APIClientHandler) ListTokens(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	clientID, ok := parsePathUUID(w, r, "id")
	if !ok {
		return
	}
	tokens, err := h.tokens.List(r.Context(), orgID, clientID)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "LIST_FAILED", "failed to list API tokens", err)
		return
	}
	if tokens == nil {
		tokens = []models.APIToken{}
	}
	writeJSON(w, http.StatusOK, models.ListResponse[models.APIToken]{Data: tokens})
}

type createAPITokenResponse struct {
	models.APIToken
	Token string `json:"token"`
}

func (h *APIClientHandler) CreateToken(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	user := middleware.UserFromContext(r.Context())
	if user == nil {
		writeError(w, r, http.StatusUnauthorized, "UNAUTHORIZED", "authentication required")
		return
	}
	clientID, ok := parsePathUUID(w, r, "id")
	if !ok {
		return
	}
	if _, err := h.clients.Get(r.Context(), orgID, clientID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, r, http.StatusNotFound, "NOT_FOUND", "API client not found")
			return
		}
		writeError(w, r, http.StatusInternalServerError, "GET_FAILED", "failed to get API client", err)
		return
	}

	var req struct {
		Name          string      `json:"name"`
		Scopes        []string    `json:"scopes"`
		RepositoryIDs []uuid.UUID `json:"repository_ids"`
		ExpiresAt     *string     `json:"expires_at"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_JSON", "invalid request body")
		return
	}
	name := strings.TrimSpace(req.Name)
	if name == "" {
		writeError(w, r, http.StatusBadRequest, "MISSING_FIELD", "name is required")
		return
	}
	if err := models.ValidateAPITokenScopes(req.Scopes); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_SCOPE", err.Error())
		return
	}
	expiresAt, ok := parseOptionalRFC3339(w, r, req.ExpiresAt)
	if !ok {
		return
	}
	rawToken, err := db.GenerateAPIToken()
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "TOKEN_GENERATION_FAILED", "failed to generate API token", err)
		return
	}
	token := &models.APIToken{
		OrgID:           orgID,
		APIClientID:     clientID,
		Name:            name,
		TokenHash:       db.HashAPIToken(rawToken),
		TokenPrefix:     db.APITokenPrefix(rawToken),
		Scopes:          req.Scopes,
		RepositoryIDs:   req.RepositoryIDs,
		ExpiresAt:       expiresAt,
		CreatedByUserID: &user.ID,
	}
	if err := h.tokens.Create(r.Context(), token); err != nil {
		writeError(w, r, http.StatusInternalServerError, "CREATE_FAILED", "failed to create API token", err)
		return
	}
	id := token.ID.String()
	emitUserAudit(h.audit, r, models.AuditActionAPITokenCreated, models.AuditResourceAPIToken, &id, marshalAuditDetails(h.logger, map[string]any{
		"api_client_id":  clientID,
		"name":           token.Name,
		"token_prefix":   token.TokenPrefix,
		"scopes":         token.Scopes,
		"repository_ids": token.RepositoryIDs,
		"expires_at":     token.ExpiresAt,
	}))
	writeJSON(w, http.StatusCreated, models.SingleResponse[createAPITokenResponse]{Data: createAPITokenResponse{
		APIToken: *token,
		Token:    rawToken,
	}})
}

func (h *APIClientHandler) RevokeToken(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	user := middleware.UserFromContext(r.Context())
	if user == nil {
		writeError(w, r, http.StatusUnauthorized, "UNAUTHORIZED", "authentication required")
		return
	}
	clientID, ok := parsePathUUID(w, r, "id")
	if !ok {
		return
	}
	tokenID, ok := parsePathUUID(w, r, "token_id")
	if !ok {
		return
	}
	if err := h.tokens.Revoke(r.Context(), orgID, clientID, tokenID, user.ID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, r, http.StatusNotFound, "NOT_FOUND", "API token not found")
			return
		}
		writeError(w, r, http.StatusInternalServerError, "REVOKE_FAILED", "failed to revoke API token", err)
		return
	}
	id := tokenID.String()
	emitUserAudit(h.audit, r, models.AuditActionAPITokenRevoked, models.AuditResourceAPIToken, &id, marshalAuditDetails(h.logger, map[string]any{
		"api_client_id": clientID,
	}))
	w.WriteHeader(http.StatusNoContent)
}

func parsePathUUID(w http.ResponseWriter, r *http.Request, key string) (uuid.UUID, bool) {
	id, err := uuid.Parse(chi.URLParam(r, key))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ID", "invalid ID")
		return uuid.Nil, false
	}
	return id, true
}
