package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/models"
)

// --- Mocks ---

type mockUserCredentialStore struct {
	upsertFn            func(ctx context.Context, userID, orgID uuid.UUID, cfg models.ProviderConfig, isTeamDefault bool) error
	getForUserFn        func(ctx context.Context, orgID, userID uuid.UUID, provider models.ProviderName) (*models.DecryptedUserCredential, error)
	getTeamDefaultFn    func(ctx context.Context, orgID uuid.UUID, provider models.ProviderName) (*models.DecryptedUserCredential, error)
	listByUserFn        func(ctx context.Context, orgID, userID uuid.UUID) ([]models.DecryptedUserCredential, error)
	listTeamDefaultsFn  func(ctx context.Context, orgID uuid.UUID) ([]models.DecryptedUserCredential, error)
	disableFn           func(ctx context.Context, orgID, userID uuid.UUID, provider models.ProviderName) error
	setTeamDefaultFn    func(ctx context.Context, orgID, userID uuid.UUID, provider models.ProviderName) error
	removeTeamDefaultFn func(ctx context.Context, orgID uuid.UUID, provider models.ProviderName) error
}

func (m *mockUserCredentialStore) Upsert(ctx context.Context, userID, orgID uuid.UUID, cfg models.ProviderConfig, isTeamDefault bool) error {
	if m.upsertFn != nil {
		return m.upsertFn(ctx, userID, orgID, cfg, isTeamDefault)
	}
	return nil
}
func (m *mockUserCredentialStore) GetForUser(ctx context.Context, orgID, userID uuid.UUID, provider models.ProviderName) (*models.DecryptedUserCredential, error) {
	if m.getForUserFn != nil {
		return m.getForUserFn(ctx, orgID, userID, provider)
	}
	return nil, errors.New("not found")
}
func (m *mockUserCredentialStore) GetTeamDefault(ctx context.Context, orgID uuid.UUID, provider models.ProviderName) (*models.DecryptedUserCredential, error) {
	if m.getTeamDefaultFn != nil {
		return m.getTeamDefaultFn(ctx, orgID, provider)
	}
	return nil, errors.New("not found")
}
func (m *mockUserCredentialStore) ListByUser(ctx context.Context, orgID, userID uuid.UUID) ([]models.DecryptedUserCredential, error) {
	if m.listByUserFn != nil {
		return m.listByUserFn(ctx, orgID, userID)
	}
	return nil, nil
}
func (m *mockUserCredentialStore) ListTeamDefaults(ctx context.Context, orgID uuid.UUID) ([]models.DecryptedUserCredential, error) {
	if m.listTeamDefaultsFn != nil {
		return m.listTeamDefaultsFn(ctx, orgID)
	}
	return nil, nil
}
func (m *mockUserCredentialStore) Disable(ctx context.Context, orgID, userID uuid.UUID, provider models.ProviderName) error {
	if m.disableFn != nil {
		return m.disableFn(ctx, orgID, userID, provider)
	}
	return nil
}
func (m *mockUserCredentialStore) SetTeamDefault(ctx context.Context, orgID, userID uuid.UUID, provider models.ProviderName) error {
	if m.setTeamDefaultFn != nil {
		return m.setTeamDefaultFn(ctx, orgID, userID, provider)
	}
	return nil
}
func (m *mockUserCredentialStore) RemoveTeamDefault(ctx context.Context, orgID uuid.UUID, provider models.ProviderName) error {
	if m.removeTeamDefaultFn != nil {
		return m.removeTeamDefaultFn(ctx, orgID, provider)
	}
	return nil
}

type mockOrgCredentialReader struct {
	getFn            func(ctx context.Context, orgID uuid.UUID, provider models.ProviderName) (*models.DecryptedCredential, error)
	listByProviderFn func(ctx context.Context, orgID uuid.UUID, provider models.ProviderName) ([]models.DecryptedCredential, error)
}

func (m *mockOrgCredentialReader) Get(ctx context.Context, orgID uuid.UUID, provider models.ProviderName) (*models.DecryptedCredential, error) {
	if m.getFn != nil {
		return m.getFn(ctx, orgID, provider)
	}
	return nil, errors.New("not found")
}

func (m *mockOrgCredentialReader) ListByProvider(ctx context.Context, orgID uuid.UUID, provider models.ProviderName) ([]models.DecryptedCredential, error) {
	if m.listByProviderFn != nil {
		return m.listByProviderFn(ctx, orgID, provider)
	}
	return nil, errors.New("not found")
}

type mockUserLookup struct {
	getByIDFn func(ctx context.Context, orgID, userID uuid.UUID) (models.User, error)
}

func (m *mockUserLookup) GetByID(ctx context.Context, orgID, userID uuid.UUID) (models.User, error) {
	if m.getByIDFn != nil {
		return m.getByIDFn(ctx, orgID, userID)
	}
	return models.User{}, errors.New("not found")
}

// --- Helpers ---

func newTestUserCredHandler(store *mockUserCredentialStore) *UserCredentialHandler {
	return NewUserCredentialHandler(store, &mockOrgCredentialReader{}, &mockUserLookup{})
}

func withUserAndOrg(r *http.Request, userID, orgID uuid.UUID, role string) *http.Request {
	ctx := middleware.WithOrgID(r.Context(), orgID)
	typedRole := models.MembershipRole(role)
	ctx = middleware.WithUser(ctx, &models.User{ID: userID, OrgID: orgID, Role: typedRole})
	ctx = middleware.WithActiveRole(ctx, typedRole)
	return r.WithContext(ctx)
}

// --- Tests ---

func TestUserCredentialHandler_ListPersonal(t *testing.T) {
	t.Parallel()
	orgID := uuid.New()
	userID := uuid.New()

	t.Run("returns summaries for all coding agent providers", func(t *testing.T) {
		t.Parallel()
		store := &mockUserCredentialStore{
			listByUserFn: func(_ context.Context, _ uuid.UUID, _ uuid.UUID) ([]models.DecryptedUserCredential, error) {
				return []models.DecryptedUserCredential{
					{
						Provider:      models.ProviderAnthropic,
						UserID:        userID,
						Config:        models.AnthropicConfig{APIKey: "sk-ant-test123"},
						IsTeamDefault: false,
						Status:        "active",
					},
				}, nil
			},
		}
		h := newTestUserCredHandler(store)

		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r = withUserAndOrg(r, userID, orgID, "member")
		w := httptest.NewRecorder()

		h.ListPersonal(w, r)

		require.Equal(t, http.StatusOK, w.Code)

		var resp models.ListResponse[models.UserCredentialSummary]
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		require.Len(t, resp.Data, len(models.CodingAgentProviders))

		// First one (anthropic) should be configured.
		anthro := resp.Data[0]
		require.Equal(t, models.ProviderAnthropic, anthro.Provider)
		require.True(t, anthro.Configured)
		require.NotEmpty(t, anthro.MaskedKey)
	})

	t.Run("returns 401 when no user in context", func(t *testing.T) {
		t.Parallel()
		h := newTestUserCredHandler(&mockUserCredentialStore{})

		r := httptest.NewRequest(http.MethodGet, "/", nil)
		ctx := middleware.WithOrgID(r.Context(), orgID)
		r = r.WithContext(ctx)
		w := httptest.NewRecorder()

		h.ListPersonal(w, r)

		require.Equal(t, http.StatusUnauthorized, w.Code)
	})
}

func TestUserCredentialHandler_UpsertPersonal(t *testing.T) {
	t.Parallel()
	orgID := uuid.New()
	userID := uuid.New()

	t.Run("saves a personal credential", func(t *testing.T) {
		t.Parallel()
		var savedProvider models.ProviderName
		store := &mockUserCredentialStore{
			upsertFn: func(_ context.Context, _ uuid.UUID, _ uuid.UUID, cfg models.ProviderConfig, _ bool) error {
				savedProvider = cfg.Provider()
				return nil
			},
		}
		h := newTestUserCredHandler(store)

		body, _ := json.Marshal(map[string]interface{}{
			"config":          map[string]string{"api_key": "sk-ant-newkey123"},
			"is_team_default": false,
		})
		r := httptest.NewRequest(http.MethodPut, "/", bytes.NewReader(body))
		r = withUserAndOrg(r, userID, orgID, "member")

		// Add chi URL param.
		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("provider", "anthropic")
		r = r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))

		w := httptest.NewRecorder()
		h.UpsertPersonal(w, r)

		require.Equal(t, http.StatusOK, w.Code)
		require.Equal(t, models.ProviderAnthropic, savedProvider)
	})

	t.Run("accepts amp and pi as personal coding-agent providers", func(t *testing.T) {
		t.Parallel()

		tests := []struct {
			name       string
			provider   string
			configJSON string
			expected   models.ProviderName
		}{
			{
				name:       "amp",
				provider:   "amp",
				configJSON: `{"api_key":"sgamp_test_token"}`,
				expected:   models.ProviderAmp,
			},
			{
				name:       "pi",
				provider:   "pi",
				configJSON: `{"api_key":"pi-provider-key"}`,
				expected:   models.ProviderPi,
			},
		}

		for _, tt := range tests {
			tt := tt
			t.Run(tt.name, func(t *testing.T) {
				t.Parallel()

				var savedProvider models.ProviderName
				store := &mockUserCredentialStore{
					upsertFn: func(_ context.Context, _ uuid.UUID, _ uuid.UUID, cfg models.ProviderConfig, _ bool) error {
						savedProvider = cfg.Provider()
						return nil
					},
				}
				h := newTestUserCredHandler(store)

				body, _ := json.Marshal(map[string]interface{}{
					"config":          json.RawMessage(tt.configJSON),
					"is_team_default": false,
				})
				r := httptest.NewRequest(http.MethodPut, "/", bytes.NewReader(body))
				r = withUserAndOrg(r, userID, orgID, "member")

				rctx := chi.NewRouteContext()
				rctx.URLParams.Add("provider", tt.provider)
				r = r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))

				w := httptest.NewRecorder()
				h.UpsertPersonal(w, r)

				require.Equal(t, http.StatusOK, w.Code, "UpsertPersonal should accept %s as a coding-agent provider", tt.provider)
				require.Equal(t, tt.expected, savedProvider, "UpsertPersonal should save the expected provider type")
			})
		}
	})

	t.Run("rejects team default from non-admin", func(t *testing.T) {
		t.Parallel()
		h := newTestUserCredHandler(&mockUserCredentialStore{})

		body, _ := json.Marshal(map[string]interface{}{
			"config":          map[string]string{"api_key": "sk-ant-test"},
			"is_team_default": true,
		})
		r := httptest.NewRequest(http.MethodPut, "/", bytes.NewReader(body))
		r = withUserAndOrg(r, userID, orgID, "member")

		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("provider", "anthropic")
		r = r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))

		w := httptest.NewRecorder()
		h.UpsertPersonal(w, r)

		require.Equal(t, http.StatusForbidden, w.Code)
	})

	t.Run("allows team default from admin", func(t *testing.T) {
		t.Parallel()
		var isTeamDefault bool
		store := &mockUserCredentialStore{
			upsertFn: func(_ context.Context, _ uuid.UUID, _ uuid.UUID, _ models.ProviderConfig, td bool) error {
				isTeamDefault = td
				return nil
			},
		}
		h := newTestUserCredHandler(store)

		body, _ := json.Marshal(map[string]interface{}{
			"config":          map[string]string{"api_key": "sk-ant-adminkey"},
			"is_team_default": true,
		})
		r := httptest.NewRequest(http.MethodPut, "/", bytes.NewReader(body))
		r = withUserAndOrg(r, userID, orgID, "admin")

		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("provider", "anthropic")
		r = r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))

		w := httptest.NewRecorder()
		h.UpsertPersonal(w, r)

		require.Equal(t, http.StatusOK, w.Code)
		require.True(t, isTeamDefault)
	})

	t.Run("rejects invalid provider", func(t *testing.T) {
		t.Parallel()
		h := newTestUserCredHandler(&mockUserCredentialStore{})

		body, _ := json.Marshal(map[string]interface{}{
			"config": map[string]string{"api_key": "test"},
		})
		r := httptest.NewRequest(http.MethodPut, "/", bytes.NewReader(body))
		r = withUserAndOrg(r, userID, orgID, "member")

		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("provider", "invalid_provider")
		r = r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))

		w := httptest.NewRecorder()
		h.UpsertPersonal(w, r)

		require.Equal(t, http.StatusBadRequest, w.Code)
	})

	t.Run("rejects non-coding-agent provider like openai_chatgpt", func(t *testing.T) {
		t.Parallel()
		h := newTestUserCredHandler(&mockUserCredentialStore{})

		body, _ := json.Marshal(map[string]interface{}{
			"config": map[string]string{"api_key": "test"},
		})
		r := httptest.NewRequest(http.MethodPut, "/", bytes.NewReader(body))
		r = withUserAndOrg(r, userID, orgID, "member")

		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("provider", "openai_chatgpt")
		r = r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))

		w := httptest.NewRecorder()
		h.UpsertPersonal(w, r)

		require.Equal(t, http.StatusBadRequest, w.Code)
	})
}

func TestUserCredentialHandler_DeletePersonal(t *testing.T) {
	t.Parallel()
	orgID := uuid.New()
	userID := uuid.New()

	t.Run("disables credential successfully", func(t *testing.T) {
		var disabledProvider models.ProviderName
		store := &mockUserCredentialStore{
			disableFn: func(_ context.Context, _ uuid.UUID, _ uuid.UUID, p models.ProviderName) error {
				disabledProvider = p
				return nil
			},
		}
		h := newTestUserCredHandler(store)

		r := httptest.NewRequest(http.MethodDelete, "/", nil)
		r = withUserAndOrg(r, userID, orgID, "member")

		rctx := chi.NewRouteContext()
		rctx.URLParams.Add("provider", "anthropic")
		r = r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))

		w := httptest.NewRecorder()
		h.DeletePersonal(w, r)

		require.Equal(t, http.StatusNoContent, w.Code)
		require.Equal(t, models.ProviderAnthropic, disabledProvider)
	})
}

func TestUserCredentialHandler_ListTeamDefaults(t *testing.T) {
	t.Parallel()
	orgID := uuid.New()
	userID := uuid.New()
	setByUserID := uuid.New()

	t.Run("returns team defaults with user names", func(t *testing.T) {
		store := &mockUserCredentialStore{
			listTeamDefaultsFn: func(_ context.Context, _ uuid.UUID) ([]models.DecryptedUserCredential, error) {
				return []models.DecryptedUserCredential{
					{
						Provider:      models.ProviderAnthropic,
						UserID:        setByUserID,
						Config:        models.AnthropicConfig{APIKey: "sk-ant-teamkey"},
						IsTeamDefault: true,
						Status:        "active",
					},
				}, nil
			},
		}
		users := &mockUserLookup{
			getByIDFn: func(_ context.Context, _, _ uuid.UUID) (models.User, error) {
				return models.User{ID: setByUserID, Name: "Alice"}, nil
			},
		}
		h := NewUserCredentialHandler(store, &mockOrgCredentialReader{}, users)

		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r = withUserAndOrg(r, userID, orgID, "member")
		w := httptest.NewRecorder()

		h.ListTeamDefaults(w, r)

		require.Equal(t, http.StatusOK, w.Code)

		var resp models.ListResponse[models.UserCredentialSummary]
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		require.Len(t, resp.Data, 1)
		require.Equal(t, "Alice", resp.Data[0].SetByUserName)
		require.True(t, resp.Data[0].IsTeamDefault)
	})
}

func TestUserCredentialHandler_ListResolved(t *testing.T) {
	t.Parallel()
	orgID := uuid.New()
	userID := uuid.New()

	t.Run("resolves personal > team > org chain", func(t *testing.T) {
		t.Parallel()

		store := &mockUserCredentialStore{
			getForUserFn: func(_ context.Context, _ uuid.UUID, _ uuid.UUID, p models.ProviderName) (*models.DecryptedUserCredential, error) {
				if p == models.ProviderAnthropic {
					return &models.DecryptedUserCredential{
						Provider: models.ProviderAnthropic,
						Config:   models.AnthropicConfig{APIKey: "sk-ant-personal"},
					}, nil
				}
				return nil, errors.New("not found")
			},
			listTeamDefaultsFn: func(_ context.Context, _ uuid.UUID) ([]models.DecryptedUserCredential, error) {
				return []models.DecryptedUserCredential{
					{
						Provider:      models.ProviderOpenAI,
						Config:        models.OpenAIConfig{APIKey: "sk-team-openai"},
						IsTeamDefault: true,
					},
				}, nil
			},
		}
		orgCreds := &mockOrgCredentialReader{
			getFn: func(_ context.Context, _ uuid.UUID, p models.ProviderName) (*models.DecryptedCredential, error) {
				if p == models.ProviderGemini {
					now := time.Now()
					return &models.DecryptedCredential{
						Provider:  models.ProviderGemini,
						Config:    models.GeminiConfig{APIKey: "gemini-org-key"},
						UpdatedAt: now,
					}, nil
				}
				return nil, errors.New("not found")
			},
		}
		h := NewUserCredentialHandler(store, orgCreds, &mockUserLookup{})

		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r = withUserAndOrg(r, userID, orgID, "member")
		w := httptest.NewRecorder()

		h.ListResolved(w, r)

		require.Equal(t, http.StatusOK, w.Code)

		var resp models.ListResponse[models.ResolvedCredential]
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))
		require.Len(t, resp.Data, len(models.CodingAgentProviders))

		// Build lookup.
		byProvider := map[models.ProviderName]models.ResolvedCredential{}
		for _, rc := range resp.Data {
			byProvider[rc.Provider] = rc
		}

		require.Equal(t, "personal", byProvider[models.ProviderAnthropic].Source)
		require.Equal(t, "team_default", byProvider[models.ProviderOpenAI].Source)
		require.Equal(t, "org", byProvider[models.ProviderGemini].Source)
		require.Equal(t, "none", byProvider[models.ProviderOpenRouter].Source)
	})

	t.Run("uses org coding-auth rows returned from ListByProvider", func(t *testing.T) {
		t.Parallel()

		store := &mockUserCredentialStore{}
		orgCreds := &mockOrgCredentialReader{
			listByProviderFn: func(_ context.Context, _ uuid.UUID, p models.ProviderName) ([]models.DecryptedCredential, error) {
				switch p {
				case models.ProviderAmp:
					return []models.DecryptedCredential{
						{
							Provider: models.ProviderAmp,
							Label:    "Amp primary",
							Config:   models.AmpConfig{APIKey: "amp_live_key"},
						},
					}, nil
				case models.ProviderPi:
					return []models.DecryptedCredential{
						{
							Provider: models.ProviderPi,
							Label:    "Pi primary",
							Config:   models.PiConfig{APIKey: "pi_live_key"},
						},
					}, nil
				default:
					return nil, errors.New("not found")
				}
			},
		}
		h := NewUserCredentialHandler(store, orgCreds, &mockUserLookup{})

		r := httptest.NewRequest(http.MethodGet, "/", nil)
		r = withUserAndOrg(r, userID, orgID, "member")
		w := httptest.NewRecorder()

		h.ListResolved(w, r)

		require.Equal(t, http.StatusOK, w.Code)

		var resp models.ListResponse[models.ResolvedCredential]
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &resp))

		byProvider := map[models.ProviderName]models.ResolvedCredential{}
		for _, rc := range resp.Data {
			byProvider[rc.Provider] = rc
		}

		require.Equal(t, "org", byProvider[models.ProviderAmp].Source, "ListResolved should surface labeled org Amp auth rows")
		require.NotEmpty(t, byProvider[models.ProviderAmp].MaskedKey, "ListResolved should mask Amp org auth rows")
		require.Equal(t, "org", byProvider[models.ProviderPi].Source, "ListResolved should surface labeled org Pi auth rows")
		require.NotEmpty(t, byProvider[models.ProviderPi].MaskedKey, "ListResolved should mask Pi org auth rows")
	})
}

func TestFindResolvedOrgCredential(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()

	t.Run("returns nil when no org credential reader is configured", func(t *testing.T) {
		t.Parallel()

		cred := findResolvedOrgCredential(context.Background(), nil, orgID, models.ProviderAmp)
		require.Nil(t, cred, "findResolvedOrgCredential should return nil when no org credential reader is configured")
	})
}
