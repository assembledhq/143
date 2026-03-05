package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
)

const (
	linearAuthorizeURL = "https://linear.app/oauth/authorize"
	linearTokenURL     = "https://api.linear.app/oauth/token" // #nosec G101 -- OAuth endpoint URL, not credentials
	linearGraphQLURL   = "https://api.linear.app/graphql"
)

type linearCredentialStore interface {
	Upsert(ctx context.Context, orgID uuid.UUID, cfg models.ProviderConfig) error
}

type linearTokenResponse struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	Scope       string `json:"scope"`
}

type linearOrganization struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type linearViewer struct {
	Organization linearOrganization `json:"organization"`
}

type linearViewerData struct {
	Viewer linearViewer `json:"viewer"`
}

type linearViewerResponse struct {
	Data linearViewerData `json:"data"`
}

type IntegrationHandler struct {
	integrationStore *db.IntegrationStore
	credentialStore  linearCredentialStore
	linearClientID   string
	linearSecret     string
	baseURL          string
	frontendURL      string
	client           *http.Client
}

func NewIntegrationHandler(
	integrationStore *db.IntegrationStore,
	credentialStore linearCredentialStore,
	linearClientID, linearSecret, baseURL, frontendURL string,
) *IntegrationHandler {
	return &IntegrationHandler{
		integrationStore: integrationStore,
		credentialStore:  credentialStore,
		linearClientID:   linearClientID,
		linearSecret:     linearSecret,
		baseURL:          baseURL,
		frontendURL:      frontendURL,
		client:           http.DefaultClient,
	}
}

func (h *IntegrationHandler) StartLinearOAuth(w http.ResponseWriter, r *http.Request) {
	if h.linearClientID == "" || h.linearSecret == "" {
		writeError(w, http.StatusServiceUnavailable, "LINEAR_OAUTH_NOT_CONFIGURED", "linear oauth is not configured")
		return
	}

	state, err := generateRandomString(32)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to generate oauth state")
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:     "linear_oauth_state",
		Value:    state,
		Path:     "/",
		MaxAge:   600,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})

	params := url.Values{
		"client_id":     {h.linearClientID},
		"redirect_uri":  {h.linearRedirectURL()},
		"response_type": {"code"},
		"scope":         {"read"},
		"state":         {state},
	}

	http.Redirect(w, r, linearAuthorizeURL+"?"+params.Encode(), http.StatusTemporaryRedirect)
}

func (h *IntegrationHandler) HandleLinearOAuthCallback(w http.ResponseWriter, r *http.Request) {
	stateCookie, err := r.Cookie("linear_oauth_state")
	if err != nil || stateCookie.Value != r.URL.Query().Get("state") {
		writeError(w, http.StatusBadRequest, "INVALID_STATE", "OAuth state mismatch")
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:     "linear_oauth_state",
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})

	code := r.URL.Query().Get("code")
	if code == "" {
		writeError(w, http.StatusBadRequest, "MISSING_CODE", "missing authorization code")
		return
	}

	token, err := h.exchangeLinearCode(r.Context(), code)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "TOKEN_EXCHANGE_FAILED", "failed to exchange code")
		return
	}

	workspaceID, workspaceName, err := h.fetchLinearWorkspace(r.Context(), token.AccessToken)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "LINEAR_API_FAILED", "failed to fetch linear workspace")
		return
	}

	orgID := middleware.OrgIDFromContext(r.Context())

	if h.credentialStore == nil {
		writeError(w, http.StatusInternalServerError, "CREDENTIAL_STORE_UNAVAILABLE", "credential store unavailable")
		return
	}

	linearConfig := models.LinearConfig{
		AccessToken:   token.AccessToken,
		TokenType:     token.TokenType,
		Scope:         token.Scope,
		WorkspaceID:   workspaceID,
		WorkspaceName: workspaceName,
	}
	if err := h.credentialStore.Upsert(r.Context(), orgID, linearConfig); err != nil {
		writeError(w, http.StatusInternalServerError, "SAVE_CREDENTIAL_FAILED", "failed to store linear credential")
		return
	}

	if _, _, err := h.ensureLinearIntegration(r.Context(), orgID); err != nil {
		writeError(w, http.StatusInternalServerError, "CONNECT_LINEAR_FAILED", "failed to connect linear integration")
		return
	}

	http.Redirect(w, r, h.frontendURL+"/integrations?linear=connected", http.StatusTemporaryRedirect)
}

func (h *IntegrationHandler) ListIntegrations(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	integrations, err := h.integrationStore.ListByOrg(r.Context(), orgID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "LIST_FAILED", "failed to list integrations")
		return
	}
	if integrations == nil {
		integrations = []models.Integration{}
	}
	writeJSON(w, http.StatusOK, models.ListResponse[models.Integration]{Data: integrations})
}

func (h *IntegrationHandler) ConnectLinear(w http.ResponseWriter, r *http.Request) {
	orgID := middleware.OrgIDFromContext(r.Context())
	integration, created, err := h.ensureLinearIntegration(r.Context(), orgID)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "CONNECT_LINEAR_FAILED", "failed to connect linear integration")
		return
	}
	if created {
		writeJSON(w, http.StatusCreated, models.SingleResponse[models.Integration]{Data: integration})
		return
	}
	writeJSON(w, http.StatusOK, models.SingleResponse[models.Integration]{Data: integration})
}

func (h *IntegrationHandler) ensureLinearIntegration(ctx context.Context, orgID uuid.UUID) (models.Integration, bool, error) {
	activeIntegrations, err := h.integrationStore.ListByOrgAndProvider(ctx, orgID, string(models.IntegrationProviderLinear))
	if err != nil {
		return models.Integration{}, false, err
	}

	if len(activeIntegrations) > 0 {
		return activeIntegrations[0], false, nil
	}

	integration := &models.Integration{
		OrgID:    orgID,
		Provider: models.IntegrationProviderLinear,
		Status:   models.IntegrationStatusActive,
	}
	if err := h.integrationStore.Create(ctx, integration); err != nil {
		return models.Integration{}, false, err
	}

	return *integration, true, nil
}

func (h *IntegrationHandler) linearRedirectURL() string {
	return h.baseURL + "/api/v1/integrations/linear/callback"
}

func (h *IntegrationHandler) exchangeLinearCode(ctx context.Context, code string) (*linearTokenResponse, error) {
	body, err := json.Marshal(map[string]string{
		"grant_type":    "authorization_code",
		"code":          code,
		"redirect_uri":  h.linearRedirectURL(),
		"client_id":     h.linearClientID,
		"client_secret": h.linearSecret,
	})
	if err != nil {
		return nil, fmt.Errorf("marshal linear oauth request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, linearTokenURL, bytes.NewBuffer(body))
	if err != nil {
		return nil, fmt.Errorf("create linear oauth request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := h.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("linear oauth token request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		responseBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return nil, fmt.Errorf("linear oauth token request failed: status=%d body=%s", resp.StatusCode, string(responseBody))
	}

	var token linearTokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&token); err != nil {
		return nil, fmt.Errorf("decode linear token response: %w", err)
	}
	if token.AccessToken == "" {
		return nil, fmt.Errorf("linear token response missing access_token")
	}

	return &token, nil
}

func (h *IntegrationHandler) fetchLinearWorkspace(ctx context.Context, accessToken string) (string, string, error) {
	queryBody := map[string]string{"query": "query ViewerOrg { viewer { organization { id name } } }"}
	body, err := json.Marshal(queryBody)
	if err != nil {
		return "", "", fmt.Errorf("marshal linear viewer query: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, linearGraphQLURL, bytes.NewBuffer(body))
	if err != nil {
		return "", "", fmt.Errorf("create linear viewer request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+accessToken)

	resp, err := h.client.Do(req)
	if err != nil {
		return "", "", fmt.Errorf("linear viewer request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode < http.StatusOK || resp.StatusCode >= http.StatusMultipleChoices {
		responseBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return "", "", fmt.Errorf("linear viewer request failed: status=%d body=%s", resp.StatusCode, string(responseBody))
	}

	var viewer linearViewerResponse
	if err := json.NewDecoder(resp.Body).Decode(&viewer); err != nil {
		return "", "", fmt.Errorf("decode linear viewer response: %w", err)
	}

	workspaceID := viewer.Data.Viewer.Organization.ID
	workspaceName := viewer.Data.Viewer.Organization.Name
	if workspaceID == "" {
		return "", "", fmt.Errorf("linear viewer response missing organization id")
	}

	return workspaceID, workspaceName, nil
}
