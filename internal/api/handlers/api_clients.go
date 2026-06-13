package handlers

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/netip"
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
	tx      db.TxStarter
	audit   *db.AuditEmitter
	logger  zerolog.Logger
}

func NewAPIClientHandler(clients *db.APIClientStore, tokens *db.APITokenStore) *APIClientHandler {
	return &APIClientHandler{clients: clients, tokens: tokens, logger: zerolog.Nop()}
}

func (h *APIClientHandler) SetAuditEmitter(audit *db.AuditEmitter) {
	h.audit = audit
}

func (h *APIClientHandler) SetTxStarter(tx db.TxStarter) {
	h.tx = tx
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
		} else {
			client.DisabledByUserID = nil
			client.DisabledAt = nil
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

var generateRawAPIToken = db.GenerateAPIToken

type createAPIKeyResponse struct {
	Client models.APIClient       `json:"client"`
	Token  createAPITokenResponse `json:"token"`
}

func (h *APIClientHandler) CreateAPIKey(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	user := middleware.UserFromContext(r.Context())
	if user == nil {
		writeError(w, r, http.StatusUnauthorized, "UNAUTHORIZED", "authentication required")
		return
	}
	if h.tx == nil {
		writeError(w, r, http.StatusInternalServerError, "CREATE_FAILED", "API key creation is not configured")
		return
	}

	var req struct {
		IntegrationName string      `json:"integration_name"`
		Description     *string     `json:"description"`
		TokenName       string      `json:"token_name"`
		Scopes          []string    `json:"scopes"`
		RepositoryIDs   []uuid.UUID `json:"repository_ids"`
		ExpiresAt       *string     `json:"expires_at"`
		AllowedIPCidrs  []string    `json:"allowed_ip_cidrs"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_JSON", "invalid request body")
		return
	}
	integrationName := strings.TrimSpace(req.IntegrationName)
	if integrationName == "" {
		writeError(w, r, http.StatusBadRequest, "MISSING_FIELD", "integration_name is required")
		return
	}
	tokenName := strings.TrimSpace(req.TokenName)
	if tokenName == "" {
		writeError(w, r, http.StatusBadRequest, "MISSING_FIELD", "token_name is required")
		return
	}
	if err := models.ValidateAPITokenScopes(req.Scopes); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_SCOPE", err.Error())
		return
	}
	allowedIPCidrs, ok := parseAllowedIPCidrs(w, r, req.AllowedIPCidrs)
	if !ok {
		return
	}
	expiresAt, ok := parseOptionalRFC3339(w, r, req.ExpiresAt)
	if !ok {
		return
	}
	rawToken, err := generateRawAPIToken()
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "TOKEN_GENERATION_FAILED", "failed to generate API token", err)
		return
	}

	tx, err := h.tx.Begin(r.Context())
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "CREATE_FAILED", "failed to start API key creation", err)
		return
	}
	defer func() { _ = tx.Rollback(r.Context()) }()

	client := &models.APIClient{
		OrgID:           orgID,
		Name:            integrationName,
		Description:     trimOptionalString(req.Description),
		Status:          models.APIClientStatusEnabled,
		CreatedByUserID: &user.ID,
	}
	if err := db.NewAPIClientStore(tx).Create(r.Context(), client); err != nil {
		writeError(w, r, http.StatusInternalServerError, "CREATE_FAILED", "failed to create API client", err)
		return
	}
	token := &models.APIToken{
		OrgID:           orgID,
		APIClientID:     client.ID,
		Name:            tokenName,
		TokenHash:       db.HashAPIToken(rawToken),
		TokenPrefix:     db.APITokenPrefix(rawToken),
		Scopes:          req.Scopes,
		RepositoryIDs:   req.RepositoryIDs,
		AllowedIPCidrs:  allowedIPCidrs,
		ExpiresAt:       expiresAt,
		CreatedByUserID: &user.ID,
	}
	if err := db.NewAPITokenStore(tx).Create(r.Context(), token); err != nil {
		writeError(w, r, http.StatusInternalServerError, "CREATE_FAILED", "failed to create API token", err)
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		writeError(w, r, http.StatusInternalServerError, "CREATE_FAILED", "failed to commit API key creation", err)
		return
	}

	clientID := client.ID.String()
	emitUserAudit(h.audit, r, models.AuditActionAPIClientCreated, models.AuditResourceAPIClient, &clientID, marshalAuditDetails(h.logger, map[string]any{
		"name": client.Name,
	}))
	tokenID := token.ID.String()
	emitUserAudit(h.audit, r, models.AuditActionAPITokenCreated, models.AuditResourceAPIToken, &tokenID, marshalAuditDetails(h.logger, map[string]any{
		"api_client_id":    client.ID,
		"name":             token.Name,
		"token_prefix":     token.TokenPrefix,
		"scopes":           token.Scopes,
		"repository_ids":   token.RepositoryIDs,
		"allowed_ip_cidrs": token.AllowedIPCidrs,
		"expires_at":       token.ExpiresAt,
	}))
	writeJSON(w, http.StatusCreated, models.SingleResponse[createAPIKeyResponse]{Data: createAPIKeyResponse{
		Client: *client,
		Token: createAPITokenResponse{
			APIToken: *token,
			Token:    rawToken,
		},
	}})
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
		Name           string      `json:"name"`
		Scopes         []string    `json:"scopes"`
		RepositoryIDs  []uuid.UUID `json:"repository_ids"`
		ExpiresAt      *string     `json:"expires_at"`
		AllowedIPCidrs []string    `json:"allowed_ip_cidrs"`
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
	allowedIPCidrs, ok := parseAllowedIPCidrs(w, r, req.AllowedIPCidrs)
	if !ok {
		return
	}
	expiresAt, ok := parseOptionalRFC3339(w, r, req.ExpiresAt)
	if !ok {
		return
	}
	rawToken, err := generateRawAPIToken()
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
		AllowedIPCidrs:  allowedIPCidrs,
		ExpiresAt:       expiresAt,
		CreatedByUserID: &user.ID,
	}
	if err := h.tokens.Create(r.Context(), token); err != nil {
		writeError(w, r, http.StatusInternalServerError, "CREATE_FAILED", "failed to create API token", err)
		return
	}
	id := token.ID.String()
	emitUserAudit(h.audit, r, models.AuditActionAPITokenCreated, models.AuditResourceAPIToken, &id, marshalAuditDetails(h.logger, map[string]any{
		"api_client_id":    clientID,
		"name":             token.Name,
		"token_prefix":     token.TokenPrefix,
		"scopes":           token.Scopes,
		"repository_ids":   token.RepositoryIDs,
		"allowed_ip_cidrs": token.AllowedIPCidrs,
		"expires_at":       token.ExpiresAt,
	}))
	writeJSON(w, http.StatusCreated, models.SingleResponse[createAPITokenResponse]{Data: createAPITokenResponse{
		APIToken: *token,
		Token:    rawToken,
	}})
}

func parseAllowedIPCidrs(w http.ResponseWriter, r *http.Request, values []string) ([]string, bool) {
	if len(values) == 0 {
		return []string{}, true
	}
	normalized := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			writeError(w, r, http.StatusBadRequest, "INVALID_IP_ALLOWLIST", "allowed_ip_cidrs entries must be valid IP addresses or CIDR ranges")
			return nil, false
		}
		if _, err := netip.ParseAddr(value); err == nil {
			normalized = append(normalized, value)
			continue
		}
		if _, err := netip.ParsePrefix(value); err == nil {
			normalized = append(normalized, value)
			continue
		}
		writeError(w, r, http.StatusBadRequest, "INVALID_IP_ALLOWLIST", "allowed_ip_cidrs entries must be valid IP addresses or CIDR ranges")
		return nil, false
	}
	return normalized, true
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
