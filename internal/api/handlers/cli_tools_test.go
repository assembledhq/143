package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/models"
)

type fakeCredentialProvider struct {
	creds map[models.ProviderName]*models.DecryptedCredential
}

func (f *fakeCredentialProvider) GetAllIntegrations(_ context.Context, _ uuid.UUID, _ []models.ProviderName) (map[models.ProviderName]*models.DecryptedCredential, error) {
	return f.creds, nil
}

func cliToolsRequestContext(r *http.Request) *http.Request {
	user := &models.User{ID: uuid.New(), Email: "dev@example.com"}
	ctx := middleware.WithUser(r.Context(), user)
	ctx = middleware.WithOrgID(ctx, uuid.New())
	ctx = middleware.WithActiveRole(ctx, "member")
	return r.WithContext(ctx)
}

func TestCLIToolsListMirrorsConnectedIntegrations(t *testing.T) {
	t.Parallel()

	// Only Sentry connected: the tool list must include sentry tools and no
	// notion/linear tools.
	h := NewCLIToolsHandler(&fakeCredentialProvider{
		creds: map[models.ProviderName]*models.DecryptedCredential{
			models.ProviderSentry: {Config: models.SentryConfig{AccessToken: "tok", OrgSlug: "acme"}},
		},
	}, zerolog.Nop())

	rec := httptest.NewRecorder()
	h.ListTools(rec, cliToolsRequestContext(httptest.NewRequest(http.MethodGet, "/api/v1/cli/tools", nil)))

	require.Equal(t, http.StatusOK, rec.Code)
	body := rec.Body.String()
	require.Contains(t, body, "sentry_list_errors")
	require.NotContains(t, body, "notion_", "unconnected integrations must not surface tools")
	require.NotContains(t, body, "linear_", "unconnected integrations must not surface tools")
	require.NotContains(t, body, "tok", "credentials must never appear in the tool list")
}

func TestCLIToolsListEmptyOrgReturnsEmptyList(t *testing.T) {
	t.Parallel()
	h := NewCLIToolsHandler(&fakeCredentialProvider{creds: map[models.ProviderName]*models.DecryptedCredential{}}, zerolog.Nop())

	rec := httptest.NewRecorder()
	h.ListTools(rec, cliToolsRequestContext(httptest.NewRequest(http.MethodGet, "/api/v1/cli/tools", nil)))

	require.Equal(t, http.StatusOK, rec.Code)
	var resp struct {
		Data struct {
			Tools []json.RawMessage `json:"tools"`
		} `json:"data"`
	}
	require.NoError(t, json.Unmarshal(rec.Body.Bytes(), &resp))
	require.Empty(t, resp.Data.Tools)
}

func TestCLIToolsInvokeUnknownTool404s(t *testing.T) {
	t.Parallel()
	h := NewCLIToolsHandler(&fakeCredentialProvider{creds: map[models.ProviderName]*models.DecryptedCredential{}}, zerolog.Nop())

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/cli/tools/invoke",
		strings.NewReader(`{"tool":"notion_search_documents","args":{}}`))
	h.Invoke(rec, cliToolsRequestContext(req))

	require.Equal(t, http.StatusNotFound, rec.Code)
	require.Contains(t, rec.Body.String(), "TOOL_NOT_FOUND")
}

func TestCLIToolsInvokeRejectsMissingTool(t *testing.T) {
	t.Parallel()
	h := NewCLIToolsHandler(&fakeCredentialProvider{creds: map[models.ProviderName]*models.DecryptedCredential{}}, zerolog.Nop())

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/cli/tools/invoke", strings.NewReader(`{}`))
	h.Invoke(rec, cliToolsRequestContext(req))

	require.Equal(t, http.StatusBadRequest, rec.Code)
	require.Contains(t, rec.Body.String(), "INVALID_BODY")
}

func TestCLIToolsRequireActiveOrg(t *testing.T) {
	t.Parallel()
	h := NewCLIToolsHandler(&fakeCredentialProvider{creds: map[models.ProviderName]*models.DecryptedCredential{}}, zerolog.Nop())

	// No org in context (zero-membership user): both endpoints refuse.
	rec := httptest.NewRecorder()
	h.ListTools(rec, httptest.NewRequest(http.MethodGet, "/api/v1/cli/tools", nil))
	require.Equal(t, http.StatusForbidden, rec.Code)

	rec = httptest.NewRecorder()
	h.Invoke(rec, httptest.NewRequest(http.MethodPost, "/api/v1/cli/tools/invoke", strings.NewReader(`{"tool":"x"}`)))
	require.Equal(t, http.StatusForbidden, rec.Code)
}
