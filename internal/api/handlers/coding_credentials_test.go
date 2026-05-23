package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
)

type mockCodingCredentialStore struct {
	getFn              func(ctx context.Context, scope models.Scope, id uuid.UUID) (*models.DecryptedCodingCredential, error)
	listByScopeFn      func(ctx context.Context, scope models.Scope) ([]models.DecryptedCodingCredential, error)
	listProviderFn     func(ctx context.Context, scope models.Scope, provider models.ProviderName) ([]models.DecryptedCodingCredential, error)
	listResolveFn      func(ctx context.Context, orgID uuid.UUID, userID *uuid.UUID, provider models.ProviderName) ([]models.DecryptedCodingCredential, error)
	listResolveMultiFn func(ctx context.Context, orgID uuid.UUID, userID *uuid.UUID, providers []models.ProviderName) (map[models.ProviderName][]models.DecryptedCodingCredential, error)
	createFn           func(ctx context.Context, scope models.Scope, label string, cfg models.ProviderConfig, opts db.CreateOpts) (*uuid.UUID, error)
	renameFn           func(ctx context.Context, scope models.Scope, id uuid.UUID, label string) error
	updateStatusFn     func(ctx context.Context, scope models.Scope, id uuid.UUID, status models.CodingCredentialRowStatus) error
	disableFn          func(ctx context.Context, scope models.Scope, id uuid.UUID) error
	moveFn             func(ctx context.Context, scope models.Scope, id uuid.UUID, pos models.MoveCodingCredentialInput) error
	reorderFn          func(ctx context.Context, scope models.Scope, orderedIDs []uuid.UUID) error
}

type mockCodingCredentialOrgStore struct {
	mergeFn func(ctx context.Context, orgID uuid.UUID, agent models.AgentType, defaults map[string]string) error
}

type mockCodingCredentialInvalidator struct {
	invalidateFn func(orgID uuid.UUID)
}

func (m *mockCodingCredentialInvalidator) InvalidateOrg(orgID uuid.UUID) {
	if m.invalidateFn != nil {
		m.invalidateFn(orgID)
	}
}

func (m *mockCodingCredentialOrgStore) MergeCodingAgentDefaults(ctx context.Context, orgID uuid.UUID, agent models.AgentType, defaults map[string]string) error {
	if m.mergeFn != nil {
		return m.mergeFn(ctx, orgID, agent, defaults)
	}
	return nil
}

func (m *mockCodingCredentialStore) Get(ctx context.Context, scope models.Scope, id uuid.UUID) (*models.DecryptedCodingCredential, error) {
	if m.getFn != nil {
		return m.getFn(ctx, scope, id)
	}
	return nil, db.ErrCodingCredentialNotFound
}

func (m *mockCodingCredentialStore) ListByScope(ctx context.Context, scope models.Scope) ([]models.DecryptedCodingCredential, error) {
	if m.listByScopeFn != nil {
		return m.listByScopeFn(ctx, scope)
	}
	return nil, nil
}

func (m *mockCodingCredentialStore) ListByProvider(ctx context.Context, scope models.Scope, provider models.ProviderName) ([]models.DecryptedCodingCredential, error) {
	if m.listProviderFn != nil {
		return m.listProviderFn(ctx, scope, provider)
	}
	return nil, nil
}

func (m *mockCodingCredentialStore) ListResolvable(ctx context.Context, orgID uuid.UUID, userID *uuid.UUID, provider models.ProviderName) ([]models.DecryptedCodingCredential, error) {
	if m.listResolveFn != nil {
		return m.listResolveFn(ctx, orgID, userID, provider)
	}
	return nil, nil
}

func (m *mockCodingCredentialStore) ListResolvableMulti(ctx context.Context, orgID uuid.UUID, userID *uuid.UUID, providers []models.ProviderName) (map[models.ProviderName][]models.DecryptedCodingCredential, error) {
	if m.listResolveMultiFn != nil {
		return m.listResolveMultiFn(ctx, orgID, userID, providers)
	}
	// Default: fan out per provider via the per-provider hook so existing
	// tests that only set listResolveFn keep working without rewrites.
	out := make(map[models.ProviderName][]models.DecryptedCodingCredential, len(providers))
	for _, p := range providers {
		creds, err := m.ListResolvable(ctx, orgID, userID, p)
		if err != nil {
			return nil, err
		}
		out[p] = creds
	}
	return out, nil
}

func (m *mockCodingCredentialStore) Create(ctx context.Context, scope models.Scope, label string, cfg models.ProviderConfig, opts db.CreateOpts) (*uuid.UUID, error) {
	if m.createFn != nil {
		return m.createFn(ctx, scope, label, cfg, opts)
	}
	return nil, nil
}

func (m *mockCodingCredentialStore) Rename(ctx context.Context, scope models.Scope, id uuid.UUID, label string) error {
	if m.renameFn != nil {
		return m.renameFn(ctx, scope, id, label)
	}
	return nil
}

func (m *mockCodingCredentialStore) UpdateStatus(ctx context.Context, scope models.Scope, id uuid.UUID, status models.CodingCredentialRowStatus) error {
	if m.updateStatusFn != nil {
		return m.updateStatusFn(ctx, scope, id, status)
	}
	return nil
}

func (m *mockCodingCredentialStore) Disable(ctx context.Context, scope models.Scope, id uuid.UUID) error {
	if m.disableFn != nil {
		return m.disableFn(ctx, scope, id)
	}
	return nil
}

func (m *mockCodingCredentialStore) Move(ctx context.Context, scope models.Scope, id uuid.UUID, pos models.MoveCodingCredentialInput) error {
	if m.moveFn != nil {
		return m.moveFn(ctx, scope, id, pos)
	}
	return nil
}

func (m *mockCodingCredentialStore) Reorder(ctx context.Context, scope models.Scope, orderedIDs []uuid.UUID) error {
	if m.reorderFn != nil {
		return m.reorderFn(ctx, scope, orderedIDs)
	}
	return nil
}

func TestCodingCredentialHandlerList(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	userID := uuid.New()
	personalID := uuid.New()
	orgRowID := uuid.New()
	now := time.Now().UTC()

	tests := []struct {
		name           string
		target         string
		role           string
		setupStore     func(t *testing.T) *mockCodingCredentialStore
		expectedStatus int
		expectedCount  int
	}{
		{
			name:   "lists personal scope",
			target: "/api/v1/coding-credentials?scope=personal",
			setupStore: func(t *testing.T) *mockCodingCredentialStore {
				return &mockCodingCredentialStore{
					listByScopeFn: func(_ context.Context, scope models.Scope) ([]models.DecryptedCodingCredential, error) {
						require.Equal(t, orgID, scope.OrgID, "List should scope personal reads to the active org")
						require.NotNil(t, scope.UserID, "List should scope personal reads to the current user")
						require.Equal(t, userID, *scope.UserID, "List should scope personal reads to the current user id")
						return []models.DecryptedCodingCredential{{
							ID:        personalID,
							OrgID:     orgID,
							UserID:    &userID,
							Provider:  models.ProviderOpenAI,
							Label:     "Codex",
							Config:    models.OpenAIConfig{APIKey: "sk-openai-123456"},
							Priority:  1,
							Status:    models.CodingCredentialStatusActive,
							CreatedAt: now,
							UpdatedAt: now,
						}}, nil
					},
				}
			},
			expectedStatus: http.StatusOK,
			expectedCount:  1,
		},
		{
			name:   "viewer can list personal scope",
			target: "/api/v1/coding-credentials?scope=personal",
			role:   "viewer",
			setupStore: func(t *testing.T) *mockCodingCredentialStore {
				return &mockCodingCredentialStore{
					listByScopeFn: func(_ context.Context, scope models.Scope) ([]models.DecryptedCodingCredential, error) {
						require.Equal(t, orgID, scope.OrgID, "List should scope viewer personal reads to the active org")
						require.NotNil(t, scope.UserID, "List should scope viewer personal reads to the current user")
						require.Equal(t, userID, *scope.UserID, "List should scope viewer personal reads to the current user id")
						return []models.DecryptedCodingCredential{}, nil
					},
				}
			},
			expectedStatus: http.StatusOK,
			expectedCount:  0,
		},
		{
			name:   "lists resolved scope",
			target: "/api/v1/coding-credentials?scope=resolved",
			setupStore: func(t *testing.T) *mockCodingCredentialStore {
				return &mockCodingCredentialStore{
					listResolveFn: func(_ context.Context, gotOrgID uuid.UUID, gotUserID *uuid.UUID, provider models.ProviderName) ([]models.DecryptedCodingCredential, error) {
						require.Equal(t, orgID, gotOrgID, "List resolved should pass the active org to the resolver")
						require.NotNil(t, gotUserID, "List resolved should pass the current user to the resolver")
						require.Equal(t, userID, *gotUserID, "List resolved should pass the current user id")
						if provider != models.ProviderAnthropicSubscription {
							return nil, nil
						}
						return []models.DecryptedCodingCredential{{
							ID:        orgRowID,
							OrgID:     orgID,
							Provider:  models.ProviderAnthropicSubscription,
							Label:     "Claude",
							Config:    models.AnthropicSubscriptionConfig{AccessToken: "tok", RefreshToken: "refresh", AccountType: "max"},
							Priority:  1,
							Status:    models.CodingCredentialStatusActive,
							CreatedAt: now,
							UpdatedAt: now,
						}}, nil
					},
				}
			},
			expectedStatus: http.StatusOK,
			expectedCount:  1,
		},
		{
			name:   "rejects invalid scope",
			target: "/api/v1/coding-credentials?scope=team",
			setupStore: func(t *testing.T) *mockCodingCredentialStore {
				return &mockCodingCredentialStore{}
			},
			expectedStatus: http.StatusBadRequest,
		},
		{
			name:   "propagates store errors",
			target: "/api/v1/coding-credentials?scope=personal",
			setupStore: func(t *testing.T) *mockCodingCredentialStore {
				return &mockCodingCredentialStore{
					listByScopeFn: func(context.Context, models.Scope) ([]models.DecryptedCodingCredential, error) {
						return nil, errors.New("db down")
					},
				}
			},
			expectedStatus: http.StatusInternalServerError,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			handler := NewCodingCredentialHandler(tt.setupStore(t), nil)
			req := httptest.NewRequest(http.MethodGet, tt.target, nil)
			role := tt.role
			if role == "" {
				role = "admin"
			}
			req = withUserAndOrg(req, userID, orgID, role)
			rr := httptest.NewRecorder()

			handler.List(rr, req)

			require.Equal(t, tt.expectedStatus, rr.Code, "List should return the expected status code")
			if tt.expectedStatus == http.StatusOK {
				var resp models.ListResponse[models.CodingCredentialSummary]
				require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp), "List response should be valid JSON")
				require.Len(t, resp.Data, tt.expectedCount, "List should return the expected number of rows")
			}
		})
	}
}

func TestCodingCredentialHandlerListResolvedSortsLikeRuntimePicker(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	userID := uuid.New()
	apiKeyID := uuid.New()
	subscriptionID := uuid.New()
	orgFallbackID := uuid.New()
	now := time.Now().UTC()

	store := &mockCodingCredentialStore{
		listResolveMultiFn: func(_ context.Context, gotOrgID uuid.UUID, gotUserID *uuid.UUID, providers []models.ProviderName) (map[models.ProviderName][]models.DecryptedCodingCredential, error) {
			require.Equal(t, orgID, gotOrgID, "List resolved should pass the active org to the bulk resolver")
			require.NotNil(t, gotUserID, "List resolved should pass the current user to the bulk resolver")
			require.Equal(t, userID, *gotUserID, "List resolved should pass the current user id")
			require.Contains(t, providers, models.ProviderAnthropic, "bulk resolver should include Anthropic API-key provider")
			require.Contains(t, providers, models.ProviderAnthropicSubscription, "bulk resolver should include Anthropic subscription provider")
			return map[models.ProviderName][]models.DecryptedCodingCredential{
				models.ProviderAnthropic: {
					{
						ID:        apiKeyID,
						OrgID:     orgID,
						UserID:    &userID,
						Provider:  models.ProviderAnthropic,
						Label:     "Claude API fallback",
						Config:    models.AnthropicConfig{APIKey: "sk-ant"},
						Priority:  2,
						Status:    models.CodingCredentialStatusActive,
						CreatedAt: now.Add(2 * time.Minute),
						UpdatedAt: now,
					},
					{
						ID:        orgFallbackID,
						OrgID:     orgID,
						Provider:  models.ProviderAnthropic,
						Label:     "Org Claude",
						Config:    models.AnthropicConfig{APIKey: "sk-ant-org"},
						Priority:  1,
						Status:    models.CodingCredentialStatusActive,
						CreatedAt: now,
						UpdatedAt: now,
					},
				},
				models.ProviderAnthropicSubscription: {
					{
						ID:        subscriptionID,
						OrgID:     orgID,
						UserID:    &userID,
						Provider:  models.ProviderAnthropicSubscription,
						Label:     "Claude subscription",
						Config:    models.AnthropicSubscriptionConfig{AccessToken: "tok", RefreshToken: "refresh"},
						Priority:  1,
						Status:    models.CodingCredentialStatusActive,
						CreatedAt: now.Add(time.Minute),
						UpdatedAt: now,
					},
				},
			}, nil
		},
	}
	handler := NewCodingCredentialHandler(store, nil)
	req := httptest.NewRequest(http.MethodGet, "/api/v1/coding-credentials?scope=resolved", nil)
	req = withAdminUser(req, userID, orgID)
	rr := httptest.NewRecorder()

	handler.List(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, "List should return success for resolved scope")
	var resp models.ListResponse[models.CodingCredentialSummary]
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp), "List response should be valid JSON")
	require.Equal(t, []uuid.UUID{subscriptionID, apiKeyID, orgFallbackID},
		[]uuid.UUID{resp.Data[0].ID, resp.Data[1].ID, resp.Data[2].ID},
		"resolved list should sort all provider buckets by runtime order before rendering defaults")
	require.True(t, resp.Data[0].IsDefault, "first personal Claude row should be marked as default")
	require.False(t, resp.Data[1].IsDefault, "lower-priority personal Claude row should not be marked as default")
	require.True(t, resp.Data[2].IsDefault, "org fallback row should be marked as default within the org fallback scope")
}

func TestCodingCredentialHandlerCreate(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	userID := uuid.New()
	createdID := uuid.New()
	now := time.Now().UTC()

	tests := []struct {
		name             string
		body             string
		role             string
		setupStore       func(t *testing.T) *mockCodingCredentialStore
		setupOrgStore    func(t *testing.T) codingAuthOrgStore
		setupInvalidator func(t *testing.T) OrgSettingsInvalidator
		expectedStatus   int
	}{
		{
			name: "creates personal api key",
			body: `{"scope":"personal","agent":"codex","auth_type":"api_key","api_key":"sk-openai-123456"}`,
			role: "admin",
			setupStore: func(t *testing.T) *mockCodingCredentialStore {
				return &mockCodingCredentialStore{
					createFn: func(_ context.Context, scope models.Scope, label string, cfg models.ProviderConfig, opts db.CreateOpts) (*uuid.UUID, error) {
						require.Equal(t, models.CodingCredentialScopePersonal, scope.Label(), "Create should use the requested personal scope")
						require.Equal(t, "Codex API key", label, "Create should apply the default label")
						require.Equal(t, models.ProviderOpenAI, cfg.Provider(), "Create should map codex api_key to openai provider config")
						require.NotNil(t, opts.CreatedBy, "Create should record the current user id")
						return &createdID, nil
					},
					getFn: func(context.Context, models.Scope, uuid.UUID) (*models.DecryptedCodingCredential, error) {
						return &models.DecryptedCodingCredential{
							ID:        createdID,
							OrgID:     orgID,
							UserID:    &userID,
							Provider:  models.ProviderOpenAI,
							Config:    models.OpenAIConfig{APIKey: "sk-openai-123456"},
							Status:    models.CodingCredentialStatusActive,
							CreatedAt: now,
							UpdatedAt: now,
						}, nil
					},
				}
			},
			expectedStatus: http.StatusCreated,
		},
		{
			name: "creates org api key and merges defaults",
			body: `{"scope":"org","agent":"amp","auth_type":"api_key","api_key":"amp-token","agent_defaults":{"AMP_MODE":"deep"}}`,
			role: "admin",
			setupStore: func(t *testing.T) *mockCodingCredentialStore {
				return &mockCodingCredentialStore{
					createFn: func(_ context.Context, scope models.Scope, label string, cfg models.ProviderConfig, _ db.CreateOpts) (*uuid.UUID, error) {
						require.True(t, scope.IsOrg(), "Create should use org scope for org credentials")
						require.Equal(t, "Amp API key", label, "Create should apply the amp default label")
						require.Equal(t, models.ProviderAmp, cfg.Provider(), "Create should map amp api_key to amp provider config")
						return &createdID, nil
					},
					getFn: func(context.Context, models.Scope, uuid.UUID) (*models.DecryptedCodingCredential, error) {
						return &models.DecryptedCodingCredential{
							ID:        createdID,
							OrgID:     orgID,
							Provider:  models.ProviderAmp,
							Config:    models.AmpConfig{APIKey: "amp-token"},
							Status:    models.CodingCredentialStatusActive,
							CreatedAt: now,
							UpdatedAt: now,
						}, nil
					},
				}
			},
			setupOrgStore: func(t *testing.T) codingAuthOrgStore {
				return &mockCodingCredentialOrgStore{
					mergeFn: func(_ context.Context, gotOrgID uuid.UUID, agent models.AgentType, defaults map[string]string) error {
						require.Equal(t, orgID, gotOrgID, "Create should merge defaults for the active org")
						require.Equal(t, models.AgentTypeAmp, agent, "Create should merge defaults for the requested agent")
						require.Equal(t, "deep", defaults["AMP_MODE"], "Create should pass agent defaults through")
						return nil
					},
				}
			},
			setupInvalidator: func(t *testing.T) OrgSettingsInvalidator {
				return &mockCodingCredentialInvalidator{
					invalidateFn: func(gotOrgID uuid.UUID) {
						require.Equal(t, orgID, gotOrgID, "Create should invalidate cached org settings after defaults change")
					},
				}
			},
			expectedStatus: http.StatusCreated,
		},
		{
			name: "rolls back created credential when defaults merge fails",
			body: `{"scope":"org","agent":"pi","auth_type":"api_key","api_key":"pi-token","agent_defaults":{"PI_MODEL":"openai/gpt-5.4"}}`,
			role: "admin",
			setupStore: func(t *testing.T) *mockCodingCredentialStore {
				return &mockCodingCredentialStore{
					createFn: func(_ context.Context, scope models.Scope, label string, cfg models.ProviderConfig, _ db.CreateOpts) (*uuid.UUID, error) {
						require.True(t, scope.IsOrg(), "Create should use org scope before merging defaults")
						require.Equal(t, "Pi API key", label, "Create should apply the pi default label")
						require.Equal(t, models.ProviderPi, cfg.Provider(), "Create should map pi api_key to pi provider config")
						return &createdID, nil
					},
					disableFn: func(_ context.Context, scope models.Scope, id uuid.UUID) error {
						require.True(t, scope.IsOrg(), "Create rollback should disable the org-scoped row")
						require.Equal(t, createdID, id, "Create rollback should target the credential created by the failed request")
						return nil
					},
				}
			},
			setupOrgStore: func(t *testing.T) codingAuthOrgStore {
				return &mockCodingCredentialOrgStore{
					mergeFn: func(_ context.Context, gotOrgID uuid.UUID, agent models.AgentType, defaults map[string]string) error {
						require.Equal(t, orgID, gotOrgID, "Create should attempt to merge defaults before rolling back")
						require.Equal(t, models.AgentTypePi, agent, "Create should merge defaults for the requested agent")
						require.Equal(t, "openai/gpt-5.4", defaults["PI_MODEL"], "Create should pass agent defaults through")
						return errors.New("settings write failed")
					},
				}
			},
			expectedStatus: http.StatusInternalServerError,
		},
		{
			name: "rejects member org mutation",
			body: `{"scope":"org","agent":"codex","auth_type":"api_key","api_key":"sk-openai-123456"}`,
			role: "member",
			setupStore: func(t *testing.T) *mockCodingCredentialStore {
				return &mockCodingCredentialStore{}
			},
			expectedStatus: http.StatusForbidden,
		},
		{
			name: "maps duplicate label to conflict",
			body: `{"scope":"personal","agent":"codex","auth_type":"api_key","label":"Codex","api_key":"sk-openai-123456"}`,
			role: "admin",
			setupStore: func(t *testing.T) *mockCodingCredentialStore {
				return &mockCodingCredentialStore{
					createFn: func(context.Context, models.Scope, string, models.ProviderConfig, db.CreateOpts) (*uuid.UUID, error) {
						return nil, &db.ErrCodingCredentialLabelTaken{Label: "Codex", ExistingStatus: models.CodingCredentialStatusActive}
					},
				}
			},
			expectedStatus: http.StatusConflict,
		},
		{
			name: "rejects invalid json",
			body: `{`,
			role: "admin",
			setupStore: func(t *testing.T) *mockCodingCredentialStore {
				return &mockCodingCredentialStore{}
			},
			expectedStatus: http.StatusBadRequest,
		},
		{
			name: "rejects oversized label",
			body: fmt.Sprintf(`{"scope":"personal","agent":"codex","auth_type":"api_key","api_key":"sk-openai-1234","label":"%s"}`, strings.Repeat("a", models.CodingCredentialLabelMax+1)),
			role: "admin",
			setupStore: func(t *testing.T) *mockCodingCredentialStore {
				return &mockCodingCredentialStore{}
			},
			expectedStatus: http.StatusBadRequest,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var orgStore codingAuthOrgStore
			if tt.setupOrgStore != nil {
				orgStore = tt.setupOrgStore(t)
			}
			handler := NewCodingCredentialHandler(tt.setupStore(t), orgStore)
			if tt.setupInvalidator != nil {
				handler.SetOrgSettingsInvalidator(tt.setupInvalidator(t))
			}
			req := httptest.NewRequest(http.MethodPost, "/api/v1/coding-credentials", bytes.NewBufferString(tt.body))
			req = withUserAndOrg(req, userID, orgID, tt.role)
			rr := httptest.NewRecorder()

			handler.Create(rr, req)

			require.Equal(t, tt.expectedStatus, rr.Code, "Create should return the expected status code")
		})
	}
}

func TestCodingCredentialHandlerDeleteMoveAndReorder(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	userID := uuid.New()
	rowID := uuid.New()
	beforeID := uuid.New()

	tests := []struct {
		name           string
		method         string
		target         string
		body           string
		invoke         func(handler *CodingCredentialHandler, rr *httptest.ResponseRecorder, req *http.Request)
		setupStore     func(t *testing.T) *mockCodingCredentialStore
		expectedStatus int
	}{
		{
			name:   "delete disables credential",
			method: http.MethodDelete,
			target: "/api/v1/coding-credentials/" + rowID.String() + "?scope=personal",
			invoke: func(handler *CodingCredentialHandler, rr *httptest.ResponseRecorder, req *http.Request) {
				handler.Delete(rr, req)
			},
			setupStore: func(t *testing.T) *mockCodingCredentialStore {
				return &mockCodingCredentialStore{
					disableFn: func(_ context.Context, scope models.Scope, id uuid.UUID) error {
						require.Equal(t, rowID, id, "Delete should disable the requested credential id")
						require.Equal(t, models.CodingCredentialScopePersonal, scope.Label(), "Delete should use the requested scope")
						return nil
					},
				}
			},
			expectedStatus: http.StatusNoContent,
		},
		{
			name:   "move sends position",
			method: http.MethodPatch,
			target: "/api/v1/coding-credentials/" + rowID.String() + "/move",
			body:   fmt.Sprintf(`{"scope":"personal","before_id":"%s"}`, beforeID),
			invoke: func(handler *CodingCredentialHandler, rr *httptest.ResponseRecorder, req *http.Request) {
				handler.Move(rr, req)
			},
			setupStore: func(t *testing.T) *mockCodingCredentialStore {
				return &mockCodingCredentialStore{
					moveFn: func(_ context.Context, scope models.Scope, id uuid.UUID, pos models.MoveCodingCredentialInput) error {
						require.Equal(t, rowID, id, "Move should move the requested credential id")
						require.Equal(t, models.CodingCredentialScopePersonal, scope.Label(), "Move should use the requested scope")
						require.Equal(t, beforeID, *pos.BeforeID, "Move should pass the requested before_id")
						return nil
					},
				}
			},
			expectedStatus: http.StatusNoContent,
		},
		{
			name:   "reorder sends ids",
			method: http.MethodPatch,
			target: "/api/v1/coding-credentials/reorder",
			body:   fmt.Sprintf(`{"scope":"personal","ordered_ids":["%s","%s"]}`, rowID, beforeID),
			invoke: func(handler *CodingCredentialHandler, rr *httptest.ResponseRecorder, req *http.Request) {
				handler.Reorder(rr, req)
			},
			setupStore: func(t *testing.T) *mockCodingCredentialStore {
				return &mockCodingCredentialStore{
					reorderFn: func(_ context.Context, scope models.Scope, orderedIDs []uuid.UUID) error {
						require.Equal(t, models.CodingCredentialScopePersonal, scope.Label(), "Reorder should use the requested scope")
						require.Equal(t, []uuid.UUID{rowID, beforeID}, orderedIDs, "Reorder should pass ordered ids through")
						return nil
					},
				}
			},
			expectedStatus: http.StatusNoContent,
		},
		{
			name:   "delete maps not found",
			method: http.MethodDelete,
			target: "/api/v1/coding-credentials/" + rowID.String() + "?scope=personal",
			invoke: func(handler *CodingCredentialHandler, rr *httptest.ResponseRecorder, req *http.Request) {
				handler.Delete(rr, req)
			},
			setupStore: func(t *testing.T) *mockCodingCredentialStore {
				return &mockCodingCredentialStore{
					disableFn: func(context.Context, models.Scope, uuid.UUID) error {
						return db.ErrCodingCredentialNotFound
					},
				}
			},
			expectedStatus: http.StatusNotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			handler := NewCodingCredentialHandler(tt.setupStore(t), nil)
			req := httptest.NewRequest(tt.method, tt.target, bytes.NewBufferString(tt.body))
			req = withAdminUser(req, userID, orgID)
			routeCtx := chi.NewRouteContext()
			routeCtx.URLParams.Add("id", rowID.String())
			req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx))
			rr := httptest.NewRecorder()

			tt.invoke(handler, rr, req)

			require.Equal(t, tt.expectedStatus, rr.Code, "handler should return the expected status code")
		})
	}
}

func TestCodingCredentialSummaryHelpers(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC()
	userID := uuid.New()
	orgID := uuid.New()
	verifiedAt := now.Add(-time.Hour)
	rows := []models.DecryptedCodingCredential{
		{ID: uuid.New(), OrgID: orgID, UserID: &userID, Provider: models.ProviderOpenAI, Label: "Codex", Config: models.OpenAIConfig{APIKey: "sk-openai-123456"}, Priority: 1, Status: models.CodingCredentialStatusActive, CreatedAt: now, UpdatedAt: now},
		{ID: uuid.New(), OrgID: orgID, UserID: &userID, Provider: models.ProviderOpenAIChatGPT, Config: models.OpenAIChatGPTConfig{AccessToken: "tok", RefreshToken: "refresh", AccountType: "plus"}, Priority: 2, Status: models.CodingCredentialStatusInvalid, CreatedAt: now, UpdatedAt: now},
		{ID: uuid.New(), OrgID: orgID, Provider: models.ProviderAnthropicSubscription, Config: models.AnthropicSubscriptionConfig{AccessToken: "tok", RefreshToken: "refresh", AccountType: "max"}, Priority: 1, Status: models.CodingCredentialStatusActive, LastVerifiedAt: &verifiedAt, RateLimitedUntil: ptrTime(now.Add(time.Hour)), RateLimitMessage: ptrString("try again at 8:50 AM"), CreatedAt: now, UpdatedAt: now},
		{ID: uuid.New(), OrgID: orgID, Provider: models.ProviderGemini, Config: models.GeminiConfig{APIKey: "gemini-key"}, Priority: 2, Status: models.CodingCredentialStatusPendingAuth, CreatedAt: now, UpdatedAt: now},
		{ID: uuid.New(), OrgID: orgID, Provider: models.ProviderAmp, Config: models.AmpConfig{APIKey: "amp-key"}, Priority: 3, Status: "disabled", CreatedAt: now, UpdatedAt: now},
		{ID: uuid.New(), OrgID: orgID, Provider: models.ProviderPi, Config: models.PiConfig{APIKey: "pi-key"}, Priority: 4, Status: models.CodingCredentialStatusActive, CreatedAt: now, UpdatedAt: now},
	}

	personal := summariesForScope(rows, models.Scope{OrgID: orgID, UserID: &userID})
	orgRows := summariesForScope(rows, models.Scope{OrgID: orgID})

	require.Len(t, personal, 2, "summariesForScope should keep only personal rows for a personal scope")
	require.True(t, personal[0].IsDefault, "first runnable personal row should be marked default")
	require.False(t, personal[1].IsDefault, "invalid personal row should not be marked default")
	require.Len(t, orgRows, 4, "summariesForScope should keep only org rows for org scope")
	require.False(t, orgRows[0].IsDefault, "rate-limited org row should not be marked default")
	require.Equal(t, models.AgentTypeClaudeCode, orgRows[0].Agent, "anthropic subscription should map to Claude Code")
	require.Equal(t, models.CodingAuthTypeSubscription, orgRows[0].AuthType, "subscription provider should map to subscription auth")
	require.Equal(t, models.CodingAuthStatusRateLimited, orgRows[0].Status, "future rate-limited active row should be rate limited")
	require.Equal(t, rows[2].RateLimitedUntil, orgRows[0].RateLimitedUntil, "summary should expose rate-limit reset time")
	require.Equal(t, rows[2].RateLimitMessage, orgRows[0].RateLimitMessage, "summary should expose rate-limit message")
	require.True(t, orgRows[3].IsDefault, "first non-rate-limited runnable org row should be marked default")
	require.Equal(t, models.CodingAuthStatusNeedsReauth, orgRows[1].Status, "pending_auth row should need reauth")
	require.Equal(t, "max", orgRows[0].UsageNote, "subscription usage note should prefer account type")
	require.Equal(t, "Gemini CLI API key", defaultLabelFor(models.AgentTypeGeminiCLI, models.CodingAuthTypeAPIKey), "defaultLabelFor should cover gemini")
	require.Equal(t, "Pi API key", defaultLabelFor(models.AgentTypePi, models.CodingAuthTypeAPIKey), "defaultLabelFor should cover pi")
	require.Equal(t, "Coding auth", defaultLabelFor("", ""), "defaultLabelFor should cover unknown agents")

	cfg, provider, err := codingCredentialConfigFromInput(models.CreateCodingCredentialInput{Agent: models.AgentTypeClaudeCode, AuthType: models.CodingAuthTypeAPIKey, APIKey: "sk-ant", BaseURL: "https://anthropic.test"})
	require.NoError(t, err, "codingCredentialConfigFromInput should accept Claude Code api keys")
	require.Equal(t, models.ProviderAnthropic, provider, "Claude Code api keys should map to anthropic provider")
	require.Equal(t, models.ProviderAnthropic, cfg.Provider(), "Claude Code config should report anthropic provider")

	cfg, provider, err = codingCredentialConfigFromInput(models.CreateCodingCredentialInput{Agent: models.AgentTypeGeminiCLI, AuthType: models.CodingAuthTypeAPIKey, APIKey: "gemini-key"})
	require.NoError(t, err, "codingCredentialConfigFromInput should accept Gemini api keys")
	require.Equal(t, models.ProviderGemini, provider, "Gemini api keys should map to gemini provider")
	require.Equal(t, models.ProviderGemini, cfg.Provider(), "Gemini config should report gemini provider")

	_, _, err = codingCredentialConfigFromInput(models.CreateCodingCredentialInput{Agent: "unknown", AuthType: models.CodingAuthTypeAPIKey, APIKey: "key"})
	require.Error(t, err, "codingCredentialConfigFromInput should reject unsupported agents")
}

func ptrTime(t time.Time) *time.Time {
	return &t
}

func ptrString(s string) *string {
	return &s
}

func TestCodingCredentialHandlerUpdateRejectsPendingAuthPromotion(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	userID := uuid.New()
	rowID := uuid.New()
	now := time.Now().UTC()
	var updateCalled bool

	store := &mockCodingCredentialStore{
		getFn: func(_ context.Context, scope models.Scope, id uuid.UUID) (*models.DecryptedCodingCredential, error) {
			require.Equal(t, orgID, scope.OrgID, "Update should scope credential reads to the active org")
			require.NotNil(t, scope.UserID, "Update should scope personal credential reads to the current user")
			require.Equal(t, userID, *scope.UserID, "Update should scope personal credential reads to the current user id")
			require.Equal(t, rowID, id, "Update should read the requested credential")
			return &models.DecryptedCodingCredential{
				ID:        rowID,
				OrgID:     orgID,
				UserID:    &userID,
				Provider:  models.ProviderAnthropicSubscription,
				Label:     "Claude pending",
				Config:    models.AnthropicSubscriptionConfig{State: "state", CodeVerifier: "verifier"},
				Status:    models.CodingCredentialStatusPendingAuth,
				CreatedAt: now,
				UpdatedAt: now,
			}, nil
		},
		updateStatusFn: func(context.Context, models.Scope, uuid.UUID, models.CodingCredentialRowStatus) error {
			updateCalled = true
			return nil
		},
	}
	handler := NewCodingCredentialHandler(store, nil)

	req := httptest.NewRequest(http.MethodPatch, "/api/v1/coding-credentials/"+rowID.String(), bytes.NewBufferString(`{"scope":"personal","status":"active"}`))
	req = withAdminUser(req, userID, orgID)
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add("id", rowID.String())
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx))
	rr := httptest.NewRecorder()

	handler.Update(rr, req)

	require.Equal(t, http.StatusBadRequest, rr.Code, "Update should reject active promotion from pending_auth")
	require.Contains(t, rr.Body.String(), "INVALID_STATUS", "Update should return a stable invalid status error")
	require.False(t, updateCalled, "Update should not write status when pending_auth promotion is rejected")
}

func TestCodingCredentialHandlerUpdateRejectsClientSideActiveStatus(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	userID := uuid.New()
	rowID := uuid.New()
	var storeCalled bool

	store := &mockCodingCredentialStore{
		getFn: func(_ context.Context, _ models.Scope, _ uuid.UUID) (*models.DecryptedCodingCredential, error) {
			storeCalled = true
			return nil, db.ErrCodingCredentialNotFound
		},
		updateStatusFn: func(_ context.Context, _ models.Scope, _ uuid.UUID, status models.CodingCredentialRowStatus) error {
			storeCalled = true
			return nil
		},
	}
	handler := NewCodingCredentialHandler(store, nil)

	req := httptest.NewRequest(http.MethodPatch, "/api/v1/coding-credentials/"+rowID.String(), bytes.NewBufferString(`{"scope":"personal","status":"active"}`))
	req = withAdminUser(req, userID, orgID)
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add("id", rowID.String())
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx))
	rr := httptest.NewRecorder()

	handler.Update(rr, req)

	require.Equal(t, http.StatusBadRequest, rr.Code, "Update should reject client-side active status changes")
	require.Contains(t, rr.Body.String(), "INVALID_STATUS", "Update should return a stable invalid status error")
	require.False(t, storeCalled, "Update should reject active status before touching the store")
}

func TestCodingCredentialHandlerUpdateRejectsInvalidStatusBeforeRename(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	userID := uuid.New()
	rowID := uuid.New()
	var renameCalled bool

	store := &mockCodingCredentialStore{
		renameFn: func(context.Context, models.Scope, uuid.UUID, string) error {
			renameCalled = true
			return nil
		},
	}
	handler := NewCodingCredentialHandler(store, nil)

	req := httptest.NewRequest(http.MethodPatch, "/api/v1/coding-credentials/"+rowID.String(), bytes.NewBufferString(`{"scope":"personal","label":"Renamed","status":"pending_auth"}`))
	req = withAdminUser(req, userID, orgID)
	routeCtx := chi.NewRouteContext()
	routeCtx.URLParams.Add("id", rowID.String())
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx))
	rr := httptest.NewRecorder()

	handler.Update(rr, req)

	require.Equal(t, http.StatusBadRequest, rr.Code, "Update should reject invalid status values")
	require.Contains(t, rr.Body.String(), "INVALID_STATUS", "Update should return a stable invalid status error")
	require.False(t, renameCalled, "Update should not rename when the same request has an invalid status")
}

func TestCodingCredentialHandlerUpdateBranches(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	userID := uuid.New()
	rowID := uuid.New()
	now := time.Now().UTC()
	label := "Renamed"
	disabled := models.CodingCredentialStatusDisabled

	tests := []struct {
		name           string
		targetID       string
		body           string
		role           string
		setupStore     func(t *testing.T) *mockCodingCredentialStore
		expectedStatus int
	}{
		{
			name:           "rejects invalid id",
			targetID:       "not-a-uuid",
			body:           `{"scope":"personal"}`,
			role:           "admin",
			setupStore:     func(t *testing.T) *mockCodingCredentialStore { return &mockCodingCredentialStore{} },
			expectedStatus: http.StatusBadRequest,
		},
		{
			name:           "rejects invalid json",
			targetID:       rowID.String(),
			body:           `{`,
			role:           "admin",
			setupStore:     func(t *testing.T) *mockCodingCredentialStore { return &mockCodingCredentialStore{} },
			expectedStatus: http.StatusBadRequest,
		},
		{
			name:           "rejects org mutation for member",
			targetID:       rowID.String(),
			body:           `{"scope":"org","label":"Renamed"}`,
			role:           "member",
			setupStore:     func(t *testing.T) *mockCodingCredentialStore { return &mockCodingCredentialStore{} },
			expectedStatus: http.StatusForbidden,
		},
		{
			name:     "renames and disables",
			targetID: rowID.String(),
			body:     `{"scope":"personal","label":"Renamed","status":"disabled"}`,
			role:     "admin",
			setupStore: func(t *testing.T) *mockCodingCredentialStore {
				return &mockCodingCredentialStore{
					renameFn: func(_ context.Context, _ models.Scope, id uuid.UUID, gotLabel string) error {
						require.Equal(t, rowID, id, "Update should rename the requested id")
						require.Equal(t, label, gotLabel, "Update should pass the requested label")
						return nil
					},
					updateStatusFn: func(_ context.Context, _ models.Scope, id uuid.UUID, status models.CodingCredentialRowStatus) error {
						require.Equal(t, rowID, id, "Update should update the requested id")
						require.Equal(t, disabled, status, "Update should pass the requested status")
						return nil
					},
					getFn: func(context.Context, models.Scope, uuid.UUID) (*models.DecryptedCodingCredential, error) {
						return &models.DecryptedCodingCredential{
							ID: rowID, OrgID: orgID, UserID: &userID, Provider: models.ProviderOpenAI,
							Label: label, Config: models.OpenAIConfig{APIKey: "sk-openai-123456"},
							Status: models.CodingCredentialStatusDisabled, CreatedAt: now, UpdatedAt: now,
						}, nil
					},
				}
			},
			expectedStatus: http.StatusOK,
		},
		{
			name:     "rejects pending auth status from client",
			targetID: rowID.String(),
			body:     `{"scope":"personal","status":"pending_auth"}`,
			role:     "admin",
			setupStore: func(t *testing.T) *mockCodingCredentialStore {
				return &mockCodingCredentialStore{}
			},
			expectedStatus: http.StatusBadRequest,
		},
		{
			name:     "maps rename conflict",
			targetID: rowID.String(),
			body:     `{"scope":"personal","label":"Taken"}`,
			role:     "admin",
			setupStore: func(t *testing.T) *mockCodingCredentialStore {
				return &mockCodingCredentialStore{
					renameFn: func(context.Context, models.Scope, uuid.UUID, string) error {
						return &db.ErrCodingCredentialLabelTaken{Label: "Taken", ExistingStatus: models.CodingCredentialStatusActive}
					},
				}
			},
			expectedStatus: http.StatusConflict,
		},
		{
			name:     "maps read back not found",
			targetID: rowID.String(),
			body:     `{"scope":"personal"}`,
			role:     "admin",
			setupStore: func(t *testing.T) *mockCodingCredentialStore {
				return &mockCodingCredentialStore{getFn: func(context.Context, models.Scope, uuid.UUID) (*models.DecryptedCodingCredential, error) {
					return nil, db.ErrCodingCredentialNotFound
				}}
			},
			expectedStatus: http.StatusNotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			handler := NewCodingCredentialHandler(tt.setupStore(t), nil)
			req := httptest.NewRequest(http.MethodPatch, "/api/v1/coding-credentials/"+tt.targetID, bytes.NewBufferString(tt.body))
			req = withUserAndOrg(req, userID, orgID, tt.role)
			routeCtx := chi.NewRouteContext()
			routeCtx.URLParams.Add("id", tt.targetID)
			req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx))
			rr := httptest.NewRecorder()

			handler.Update(rr, req)

			require.Equal(t, tt.expectedStatus, rr.Code, "Update should return the expected status code")
		})
	}
}

func TestCodingCredentialHandlerDeleteMoveReorderErrorBranches(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	userID := uuid.New()
	rowID := uuid.New()

	tests := []struct {
		name           string
		method         string
		target         string
		body           string
		role           string
		invoke         func(handler *CodingCredentialHandler, rr *httptest.ResponseRecorder, req *http.Request)
		setupStore     func(t *testing.T) *mockCodingCredentialStore
		expectedStatus int
	}{
		{
			name:   "delete rejects invalid id",
			method: http.MethodDelete,
			target: "/api/v1/coding-credentials/not-a-uuid?scope=personal",
			role:   "admin",
			invoke: func(handler *CodingCredentialHandler, rr *httptest.ResponseRecorder, req *http.Request) {
				handler.Delete(rr, req)
			},
			setupStore: func(t *testing.T) *mockCodingCredentialStore {
				return &mockCodingCredentialStore{}
			},
			expectedStatus: http.StatusBadRequest,
		},
		{
			name:   "delete rejects member org mutation",
			method: http.MethodDelete,
			target: "/api/v1/coding-credentials/" + rowID.String() + "?scope=org",
			role:   "member",
			invoke: func(handler *CodingCredentialHandler, rr *httptest.ResponseRecorder, req *http.Request) {
				handler.Delete(rr, req)
			},
			setupStore: func(t *testing.T) *mockCodingCredentialStore {
				return &mockCodingCredentialStore{}
			},
			expectedStatus: http.StatusForbidden,
		},
		{
			name:   "move rejects invalid json",
			method: http.MethodPatch,
			target: "/api/v1/coding-credentials/" + rowID.String() + "/move",
			body:   `{`,
			role:   "admin",
			invoke: func(handler *CodingCredentialHandler, rr *httptest.ResponseRecorder, req *http.Request) {
				handler.Move(rr, req)
			},
			setupStore: func(t *testing.T) *mockCodingCredentialStore {
				return &mockCodingCredentialStore{}
			},
			expectedStatus: http.StatusBadRequest,
		},
		{
			name:   "move rejects invalid input",
			method: http.MethodPatch,
			target: "/api/v1/coding-credentials/" + rowID.String() + "/move",
			body:   `{"scope":"personal"}`,
			role:   "admin",
			invoke: func(handler *CodingCredentialHandler, rr *httptest.ResponseRecorder, req *http.Request) {
				handler.Move(rr, req)
			},
			setupStore: func(t *testing.T) *mockCodingCredentialStore {
				return &mockCodingCredentialStore{}
			},
			expectedStatus: http.StatusBadRequest,
		},
		{
			name:   "move maps store error",
			method: http.MethodPatch,
			target: "/api/v1/coding-credentials/" + rowID.String() + "/move",
			body:   `{"scope":"personal","to_top":true}`,
			role:   "admin",
			invoke: func(handler *CodingCredentialHandler, rr *httptest.ResponseRecorder, req *http.Request) {
				handler.Move(rr, req)
			},
			setupStore: func(t *testing.T) *mockCodingCredentialStore {
				return &mockCodingCredentialStore{moveFn: func(context.Context, models.Scope, uuid.UUID, models.MoveCodingCredentialInput) error {
					return errors.New("db down")
				}}
			},
			expectedStatus: http.StatusInternalServerError,
		},
		{
			name:   "reorder rejects invalid json",
			method: http.MethodPatch,
			target: "/api/v1/coding-credentials/reorder",
			body:   `{`,
			role:   "admin",
			invoke: func(handler *CodingCredentialHandler, rr *httptest.ResponseRecorder, req *http.Request) {
				handler.Reorder(rr, req)
			},
			setupStore: func(t *testing.T) *mockCodingCredentialStore {
				return &mockCodingCredentialStore{}
			},
			expectedStatus: http.StatusBadRequest,
		},
		{
			name:   "reorder maps store error",
			method: http.MethodPatch,
			target: "/api/v1/coding-credentials/reorder",
			body:   fmt.Sprintf(`{"scope":"personal","ordered_ids":["%s"]}`, rowID),
			role:   "admin",
			invoke: func(handler *CodingCredentialHandler, rr *httptest.ResponseRecorder, req *http.Request) {
				handler.Reorder(rr, req)
			},
			setupStore: func(t *testing.T) *mockCodingCredentialStore {
				return &mockCodingCredentialStore{reorderFn: func(context.Context, models.Scope, []uuid.UUID) error {
					return db.ErrCodingCredentialNotFound
				}}
			},
			expectedStatus: http.StatusNotFound,
		},
		{
			name:   "reorder rejects empty ids",
			method: http.MethodPatch,
			target: "/api/v1/coding-credentials/reorder",
			body:   `{"scope":"personal","ordered_ids":[]}`,
			role:   "admin",
			invoke: func(handler *CodingCredentialHandler, rr *httptest.ResponseRecorder, req *http.Request) {
				handler.Reorder(rr, req)
			},
			setupStore: func(t *testing.T) *mockCodingCredentialStore {
				return &mockCodingCredentialStore{}
			},
			expectedStatus: http.StatusBadRequest,
		},
		{
			name:   "reorder rejects duplicate ids",
			method: http.MethodPatch,
			target: "/api/v1/coding-credentials/reorder",
			body:   fmt.Sprintf(`{"scope":"personal","ordered_ids":["%s","%s"]}`, rowID, rowID),
			role:   "admin",
			invoke: func(handler *CodingCredentialHandler, rr *httptest.ResponseRecorder, req *http.Request) {
				handler.Reorder(rr, req)
			},
			setupStore: func(t *testing.T) *mockCodingCredentialStore {
				return &mockCodingCredentialStore{}
			},
			expectedStatus: http.StatusBadRequest,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			handler := NewCodingCredentialHandler(tt.setupStore(t), nil)
			req := httptest.NewRequest(tt.method, tt.target, bytes.NewBufferString(tt.body))
			req = withUserAndOrg(req, userID, orgID, tt.role)
			routeCtx := chi.NewRouteContext()
			routeID := rowID.String()
			if strings.Contains(tt.target, "not-a-uuid") {
				routeID = "not-a-uuid"
			}
			routeCtx.URLParams.Add("id", routeID)
			req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, routeCtx))
			rr := httptest.NewRecorder()

			tt.invoke(handler, rr, req)

			require.Equal(t, tt.expectedStatus, rr.Code, "handler should return the expected status code")
		})
	}
}

func TestAuthTypeForProvider(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		provider models.ProviderName
		cfg      models.ProviderConfig
		want     models.CodingAuthType
	}{
		{
			name:     "anthropic api key",
			provider: models.ProviderAnthropic,
			cfg:      models.AnthropicConfig{APIKey: "sk-ant-1"},
			want:     models.CodingAuthTypeAPIKey,
		},
		{
			// During the dual-write window a legacy mirrored row can still
			// arrive with provider=anthropic and a non-nil Subscription
			// embedded (the post-step migration is what later rewrites it
			// to anthropic_subscription). The auth_type response field must
			// agree with usageNoteFor's view of the same row.
			name:     "anthropic with embedded subscription is reported as subscription",
			provider: models.ProviderAnthropic,
			cfg: models.AnthropicConfig{
				Subscription: &models.AnthropicSubscription{
					AccessToken:  "tok",
					RefreshToken: "ref",
					AccountType:  "claude_pro",
				},
			},
			want: models.CodingAuthTypeSubscription,
		},
		{
			name:     "anthropic_subscription provider",
			provider: models.ProviderAnthropicSubscription,
			cfg:      models.AnthropicSubscriptionConfig{AccessToken: "tok"},
			want:     models.CodingAuthTypeSubscription,
		},
		{
			name:     "openai api key",
			provider: models.ProviderOpenAI,
			cfg:      models.OpenAIConfig{APIKey: "sk-openai"},
			want:     models.CodingAuthTypeAPIKey,
		},
		{
			name:     "openai_subscription provider",
			provider: models.ProviderOpenAISubscription,
			cfg:      models.OpenAISubscriptionConfig{AccessToken: "tok"},
			want:     models.CodingAuthTypeSubscription,
		},
		{
			name:     "legacy openai_chatgpt provider",
			provider: models.ProviderOpenAIChatGPT,
			cfg:      models.OpenAIChatGPTConfig{AccessToken: "tok"},
			want:     models.CodingAuthTypeSubscription,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.want, authTypeForProvider(tt.provider, tt.cfg))
		})
	}
}
