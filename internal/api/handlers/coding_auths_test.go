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

type mockCodingAuthStore struct {
	listFn           func(ctx context.Context, orgID uuid.UUID) ([]models.CodingAuth, error)
	listByProviderFn func(ctx context.Context, orgID uuid.UUID, provider models.ProviderName) ([]models.DecryptedCredential, error)
	reorderFn        func(ctx context.Context, orgID uuid.UUID, ids []uuid.UUID) error
	createFn         func(ctx context.Context, orgID uuid.UUID, createdBy *uuid.UUID, input models.CreateCodingAuthInput) (*models.CodingAuth, error)
	updateFn         func(ctx context.Context, orgID uuid.UUID, id uuid.UUID, input models.UpdateCodingAuthInput) (*models.CodingAuth, error)
	disableFn        func(ctx context.Context, orgID uuid.UUID, id uuid.UUID) error
	deleteFn         func(ctx context.Context, orgID uuid.UUID, id uuid.UUID) error
}

func (m *mockCodingAuthStore) ListCodingAuths(ctx context.Context, orgID uuid.UUID) ([]models.CodingAuth, error) {
	if m.listFn != nil {
		return m.listFn(ctx, orgID)
	}
	return nil, nil
}

func (m *mockCodingAuthStore) ListByProvider(ctx context.Context, orgID uuid.UUID, provider models.ProviderName) ([]models.DecryptedCredential, error) {
	if m.listByProviderFn != nil {
		return m.listByProviderFn(ctx, orgID, provider)
	}
	return nil, nil
}

func (m *mockCodingAuthStore) ReorderCodingAuths(ctx context.Context, orgID uuid.UUID, ids []uuid.UUID) error {
	if m.reorderFn != nil {
		return m.reorderFn(ctx, orgID, ids)
	}
	return nil
}

func (m *mockCodingAuthStore) CreateCodingAuth(ctx context.Context, orgID uuid.UUID, createdBy *uuid.UUID, input models.CreateCodingAuthInput) (*models.CodingAuth, error) {
	if m.createFn != nil {
		return m.createFn(ctx, orgID, createdBy, input)
	}
	return nil, nil
}

func (m *mockCodingAuthStore) UpdateCodingAuth(ctx context.Context, orgID uuid.UUID, id uuid.UUID, input models.UpdateCodingAuthInput) (*models.CodingAuth, error) {
	if m.updateFn != nil {
		return m.updateFn(ctx, orgID, id, input)
	}
	return nil, nil
}

func (m *mockCodingAuthStore) DisableCodingAuth(ctx context.Context, orgID uuid.UUID, id uuid.UUID) error {
	if m.disableFn != nil {
		return m.disableFn(ctx, orgID, id)
	}
	return nil
}

func (m *mockCodingAuthStore) DeleteCodingAuth(ctx context.Context, orgID uuid.UUID, id uuid.UUID) error {
	if m.deleteFn != nil {
		return m.deleteFn(ctx, orgID, id)
	}
	return nil
}

func withAdminUser(r *http.Request, userID, orgID uuid.UUID) *http.Request {
	ctx := middleware.WithOrgID(r.Context(), orgID)
	ctx = middleware.WithUser(ctx, &models.User{ID: userID, OrgID: orgID, Role: "admin"})
	ctx = middleware.WithActiveRole(ctx, "admin")
	return r.WithContext(ctx)
}

type mockCodingAuthOrgStore struct {
	getFn                func(ctx context.Context, id uuid.UUID) (models.Organization, error)
	updateFn             func(ctx context.Context, org *models.Organization) error
	mergeAgentDefaultsFn func(ctx context.Context, orgID uuid.UUID, agent models.AgentType, defaults map[string]string) error
}

func (m *mockCodingAuthOrgStore) GetByID(ctx context.Context, id uuid.UUID) (models.Organization, error) {
	if m.getFn != nil {
		return m.getFn(ctx, id)
	}
	return models.Organization{}, nil
}

func (m *mockCodingAuthOrgStore) Update(ctx context.Context, org *models.Organization) error {
	if m.updateFn != nil {
		return m.updateFn(ctx, org)
	}
	return nil
}

func (m *mockCodingAuthOrgStore) MergeCodingAgentDefaults(ctx context.Context, orgID uuid.UUID, agent models.AgentType, defaults map[string]string) error {
	if m.mergeAgentDefaultsFn != nil {
		return m.mergeAgentDefaultsFn(ctx, orgID, agent, defaults)
	}
	return nil
}

type mockOrgSettingsInvalidator struct {
	orgIDs []uuid.UUID
}

func (m *mockOrgSettingsInvalidator) InvalidateOrg(orgID uuid.UUID) {
	m.orgIDs = append(m.orgIDs, orgID)
}

func TestCodingAuthHandlerList(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	now := time.Now().UTC()
	firstID := uuid.New()
	secondID := uuid.New()

	store := &mockCodingAuthStore{
		listFn: func(_ context.Context, gotOrgID uuid.UUID) ([]models.CodingAuth, error) {
			require.Equal(t, orgID, gotOrgID, "ListCodingAuths should scope queries to the org")
			return []models.CodingAuth{
				{
					ID:         firstID,
					OrgID:      orgID,
					Priority:   1,
					Agent:      models.AgentTypeCodex,
					AuthType:   models.CodingAuthTypeSubscription,
					Label:      "Team seat A",
					Provider:   models.ProviderOpenAIChatGPT,
					Status:     models.CodingAuthStatusHealthy,
					LastUsedAt: &now,
					UsageNote:  "ChatGPT Plus",
					IsDefault:  true,
				},
				{
					ID:       secondID,
					OrgID:    orgID,
					Priority: 2,
					Agent:    models.AgentTypeClaudeCode,
					AuthType: models.CodingAuthTypeAPIKey,
					Label:    "Claude backup",
					Provider: models.ProviderAnthropic,
					Status:   models.CodingAuthStatusNeverVerified,
				},
			}, nil
		},
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/settings/coding-auths", nil)
	req = withAdminUser(req, uuid.New(), orgID)
	rr := httptest.NewRecorder()

	NewCodingAuthHandler(store, nil).List(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, "List should return 200")

	var resp models.ListResponse[models.CodingAuth]
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp), "List response should be valid JSON")
	require.Len(t, resp.Data, 2, "List should return every configured coding auth")
	require.Equal(t, firstID, resp.Data[0].ID, "List should preserve effective runtime order")
	require.True(t, resp.Data[0].IsDefault, "List should surface the default row explicitly")
	require.Equal(t, models.CodingAuthStatusNeverVerified, resp.Data[1].Status, "List should preserve row statuses")
}

func TestCodingAuthHandlerList_ErrorAndEmptyCases(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()

	t.Run("surfaces store errors", func(t *testing.T) {
		t.Parallel()

		store := &mockCodingAuthStore{
			listFn: func(_ context.Context, _ uuid.UUID) ([]models.CodingAuth, error) {
				return nil, errors.New("boom")
			},
		}

		req := httptest.NewRequest(http.MethodGet, "/api/v1/settings/coding-auths", nil)
		req = withAdminUser(req, uuid.New(), orgID)
		rr := httptest.NewRecorder()

		NewCodingAuthHandler(store, nil).List(rr, req)

		require.Equal(t, http.StatusInternalServerError, rr.Code, "List should surface store failures")
	})

	t.Run("normalizes nil rows to an empty list", func(t *testing.T) {
		t.Parallel()

		store := &mockCodingAuthStore{
			listFn: func(_ context.Context, _ uuid.UUID) ([]models.CodingAuth, error) {
				return nil, nil
			},
		}

		req := httptest.NewRequest(http.MethodGet, "/api/v1/settings/coding-auths", nil)
		req = withAdminUser(req, uuid.New(), orgID)
		rr := httptest.NewRecorder()

		NewCodingAuthHandler(store, nil).List(rr, req)

		require.Equal(t, http.StatusOK, rr.Code, "List should return 200 when the store returns nil rows")
		require.JSONEq(t, `{"data":[],"meta":{}}`, rr.Body.String(), "List should serialize nil rows as an empty array")
	})
}

func TestCodingAuthHandlerReorder(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	firstID := uuid.New()
	secondID := uuid.New()

	t.Run("reorders the stack", func(t *testing.T) {
		t.Parallel()

		var gotIDs []uuid.UUID
		store := &mockCodingAuthStore{
			reorderFn: func(_ context.Context, gotOrgID uuid.UUID, ids []uuid.UUID) error {
				require.Equal(t, orgID, gotOrgID, "ReorderCodingAuths should scope queries to the org")
				gotIDs = append([]uuid.UUID(nil), ids...)
				return nil
			},
		}

		body := bytes.NewBufferString(`{"ids":["` + secondID.String() + `","` + firstID.String() + `"]}`)
		req := httptest.NewRequest(http.MethodPatch, "/api/v1/settings/coding-auths/reorder", body)
		req = withAdminUser(req, uuid.New(), orgID)
		rr := httptest.NewRecorder()

		NewCodingAuthHandler(store, nil).Reorder(rr, req)

		require.Equal(t, http.StatusNoContent, rr.Code, "Reorder should return 204")
		require.Equal(t, []uuid.UUID{secondID, firstID}, gotIDs, "Reorder should pass the submitted order to the store")
	})

	t.Run("rejects malformed ids", func(t *testing.T) {
		t.Parallel()

		store := &mockCodingAuthStore{}
		req := httptest.NewRequest(http.MethodPatch, "/api/v1/settings/coding-auths/reorder", bytes.NewBufferString(`{"ids":["bad-id"]}`))
		req = withAdminUser(req, uuid.New(), orgID)
		rr := httptest.NewRecorder()

		NewCodingAuthHandler(store, nil).Reorder(rr, req)

		require.Equal(t, http.StatusBadRequest, rr.Code, "Reorder should reject invalid UUIDs")
	})

	t.Run("rejects invalid json", func(t *testing.T) {
		t.Parallel()

		store := &mockCodingAuthStore{}
		req := httptest.NewRequest(http.MethodPatch, "/api/v1/settings/coding-auths/reorder", bytes.NewBufferString(`{`))
		req = withAdminUser(req, uuid.New(), orgID)
		rr := httptest.NewRecorder()

		NewCodingAuthHandler(store, nil).Reorder(rr, req)

		require.Equal(t, http.StatusBadRequest, rr.Code, "Reorder should reject invalid JSON")
	})

	t.Run("rejects empty ids", func(t *testing.T) {
		t.Parallel()

		store := &mockCodingAuthStore{}
		req := httptest.NewRequest(http.MethodPatch, "/api/v1/settings/coding-auths/reorder", bytes.NewBufferString(`{"ids":[]}`))
		req = withAdminUser(req, uuid.New(), orgID)
		rr := httptest.NewRecorder()

		NewCodingAuthHandler(store, nil).Reorder(rr, req)

		require.Equal(t, http.StatusBadRequest, rr.Code, "Reorder should require at least one id")
	})

	t.Run("surfaces store errors", func(t *testing.T) {
		t.Parallel()

		store := &mockCodingAuthStore{
			reorderFn: func(_ context.Context, _ uuid.UUID, _ []uuid.UUID) error {
				return errors.New("boom")
			},
		}

		body := bytes.NewBufferString(`{"ids":["` + secondID.String() + `"]}`)
		req := httptest.NewRequest(http.MethodPatch, "/api/v1/settings/coding-auths/reorder", body)
		req = withAdminUser(req, uuid.New(), orgID)
		rr := httptest.NewRecorder()

		NewCodingAuthHandler(store, nil).Reorder(rr, req)

		require.Equal(t, http.StatusInternalServerError, rr.Code, "Reorder should surface store failures")
	})
}

func TestCodingAuthHandlerCreate(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	userID := uuid.New()
	createdID := uuid.New()

	store := &mockCodingAuthStore{
		createFn: func(_ context.Context, gotOrgID uuid.UUID, createdBy *uuid.UUID, input models.CreateCodingAuthInput) (*models.CodingAuth, error) {
			require.Equal(t, orgID, gotOrgID, "CreateCodingAuth should scope writes to the org")
			require.NotNil(t, createdBy, "CreateCodingAuth should record the creating user")
			require.Equal(t, userID, *createdBy, "CreateCodingAuth should pass the creating user")
			require.Equal(t, models.AgentTypeCodex, input.Agent, "CreateCodingAuth should parse the agent")
			require.Equal(t, models.CodingAuthTypeAPIKey, input.AuthType, "CreateCodingAuth should parse the auth type")
			require.Equal(t, "Codex backup", input.Label, "CreateCodingAuth should keep the label")
			require.Equal(t, "sk-test-123456789", input.APIKey, "CreateCodingAuth should keep the API key")

			return &models.CodingAuth{
				ID:       createdID,
				OrgID:    orgID,
				Priority: 2,
				Agent:    input.Agent,
				AuthType: input.AuthType,
				Label:    input.Label,
				Status:   models.CodingAuthStatusNeverVerified,
			}, nil
		},
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/settings/coding-auths", bytes.NewBufferString(`{
		"agent":"codex",
		"auth_type":"api_key",
		"label":"Codex backup",
		"api_key":"sk-test-123456789"
	}`))
	req = withAdminUser(req, userID, orgID)
	rr := httptest.NewRecorder()

	NewCodingAuthHandler(store, nil).Create(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, "Create should return 200")

	var resp models.SingleResponse[models.CodingAuth]
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp), "Create response should be valid JSON")
	require.Equal(t, createdID, resp.Data.ID, "Create should return the created row")
}

func TestCodingAuthHandlerCreate_MergesAgentDefaultsAndDeletesOnFailure(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	userID := uuid.New()
	createdID := uuid.New()
	deleteCalled := false

	store := &mockCodingAuthStore{
		createFn: func(_ context.Context, gotOrgID uuid.UUID, createdBy *uuid.UUID, input models.CreateCodingAuthInput) (*models.CodingAuth, error) {
			require.Equal(t, orgID, gotOrgID, "CreateCodingAuth should scope writes to the org")
			require.NotNil(t, createdBy, "CreateCodingAuth should record the creating user")
			require.Equal(t, userID, *createdBy, "CreateCodingAuth should pass the creating user")
			require.Equal(t, map[string]string{"AMP_MODE": "deep"}, input.AgentDefaults, "Create should preserve agent defaults")

			return &models.CodingAuth{
				ID:       createdID,
				OrgID:    orgID,
				Agent:    models.AgentTypeAmp,
				AuthType: models.CodingAuthTypeAPIKey,
				Label:    "Amp API key",
				Provider: models.ProviderAmp,
				Status:   models.CodingAuthStatusNeverVerified,
			}, nil
		},
		deleteFn: func(_ context.Context, gotOrgID uuid.UUID, id uuid.UUID) error {
			require.Equal(t, orgID, gotOrgID, "DeleteCodingAuth should scope the rollback to the org")
			require.Equal(t, createdID, id, "DeleteCodingAuth should remove the created row")
			deleteCalled = true
			return nil
		},
	}
	orgStore := &mockCodingAuthOrgStore{
		mergeAgentDefaultsFn: func(_ context.Context, gotOrgID uuid.UUID, agent models.AgentType, defaults map[string]string) error {
			require.Equal(t, orgID, gotOrgID, "Create should merge defaults into the selected org")
			require.Equal(t, models.AgentTypeAmp, agent, "Create should target the created agent when merging defaults")
			require.Equal(t, map[string]string{"AMP_MODE": "deep"}, defaults, "Create should merge the submitted defaults")
			return errors.New("settings write failed")
		},
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/settings/coding-auths", bytes.NewBufferString(`{
		"agent":"amp",
		"auth_type":"api_key",
		"label":"Amp API key",
		"api_key":"amp_123456789",
		"agent_defaults":{"AMP_MODE":"deep"}
	}`))
	req = withAdminUser(req, userID, orgID)
	rr := httptest.NewRecorder()

	NewCodingAuthHandler(store, orgStore).Create(rr, req)

	require.Equal(t, http.StatusInternalServerError, rr.Code, "Create should fail when writing agent defaults fails")
	require.True(t, deleteCalled, "Create should remove the created auth row if the settings write fails")
}

func TestCodingAuthHandlerCreate_AgentDefaultsBranches(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	userID := uuid.New()

	t.Run("rejects defaults when org store is unavailable", func(t *testing.T) {
		t.Parallel()

		req := httptest.NewRequest(http.MethodPost, "/api/v1/settings/coding-auths", bytes.NewBufferString(`{
			"agent":"amp",
			"auth_type":"api_key",
			"api_key":"amp_123456789",
			"agent_defaults":{"AMP_MODE":"deep"}
		}`))
		req = withAdminUser(req, userID, orgID)
		rr := httptest.NewRecorder()

		NewCodingAuthHandler(&mockCodingAuthStore{}, nil).Create(rr, req)

		require.Equal(t, http.StatusInternalServerError, rr.Code, "Create should reject defaults when no org store is configured")
	})

	t.Run("invalidates settings cache after merging defaults", func(t *testing.T) {
		t.Parallel()

		invalidator := &mockOrgSettingsInvalidator{}
		store := &mockCodingAuthStore{
			createFn: func(_ context.Context, _ uuid.UUID, _ *uuid.UUID, input models.CreateCodingAuthInput) (*models.CodingAuth, error) {
				return &models.CodingAuth{
					ID:       uuid.New(),
					OrgID:    orgID,
					Agent:    input.Agent,
					AuthType: input.AuthType,
					Label:    input.Label,
					Provider: models.ProviderAmp,
					Status:   models.CodingAuthStatusNeverVerified,
				}, nil
			},
		}
		orgStore := &mockCodingAuthOrgStore{
			mergeAgentDefaultsFn: func(_ context.Context, gotOrgID uuid.UUID, agent models.AgentType, defaults map[string]string) error {
				require.Equal(t, orgID, gotOrgID, "Create should merge defaults into the request org")
				require.Equal(t, models.AgentTypeAmp, agent, "Create should merge defaults for the selected agent")
				require.Equal(t, map[string]string{"AMP_MODE": "deep"}, defaults, "Create should pass the submitted defaults")
				return nil
			},
		}

		req := httptest.NewRequest(http.MethodPost, "/api/v1/settings/coding-auths", bytes.NewBufferString(`{
			"agent":"amp",
			"auth_type":"api_key",
			"label":"Amp API key",
			"api_key":"amp_123456789",
			"agent_defaults":{"AMP_MODE":"deep"}
		}`))
		req = withAdminUser(req, userID, orgID)
		rr := httptest.NewRecorder()

		handler := NewCodingAuthHandler(store, orgStore)
		handler.SetOrgSettingsInvalidator(invalidator)
		handler.Create(rr, req)

		require.Equal(t, http.StatusOK, rr.Code, "Create should succeed when defaults merge succeeds")
		require.Equal(t, []uuid.UUID{orgID}, invalidator.orgIDs, "Create should invalidate cached org settings after merging defaults")
	})
}

func TestCodingAuthHandlerCreate_ErrorCases(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()

	t.Run("rejects missing user", func(t *testing.T) {
		t.Parallel()

		req := httptest.NewRequest(http.MethodPost, "/api/v1/settings/coding-auths", bytes.NewBufferString(`{}`))
		req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
		rr := httptest.NewRecorder()

		NewCodingAuthHandler(&mockCodingAuthStore{}, nil).Create(rr, req)

		require.Equal(t, http.StatusUnauthorized, rr.Code, "Create should require an authenticated user")
	})

	t.Run("rejects invalid json", func(t *testing.T) {
		t.Parallel()

		req := httptest.NewRequest(http.MethodPost, "/api/v1/settings/coding-auths", bytes.NewBufferString(`{`))
		req = withAdminUser(req, uuid.New(), orgID)
		rr := httptest.NewRecorder()

		NewCodingAuthHandler(&mockCodingAuthStore{}, nil).Create(rr, req)

		require.Equal(t, http.StatusBadRequest, rr.Code, "Create should reject invalid JSON")
	})

	t.Run("rejects invalid input", func(t *testing.T) {
		t.Parallel()

		req := httptest.NewRequest(http.MethodPost, "/api/v1/settings/coding-auths", bytes.NewBufferString(`{"agent":"codex","auth_type":"subscription"}`))
		req = withAdminUser(req, uuid.New(), orgID)
		rr := httptest.NewRecorder()

		NewCodingAuthHandler(&mockCodingAuthStore{}, nil).Create(rr, req)

		require.Equal(t, http.StatusBadRequest, rr.Code, "Create should reject invalid inputs")
	})

	t.Run("surfaces store errors", func(t *testing.T) {
		t.Parallel()

		store := &mockCodingAuthStore{
			createFn: func(_ context.Context, _ uuid.UUID, _ *uuid.UUID, _ models.CreateCodingAuthInput) (*models.CodingAuth, error) {
				return nil, errors.New("boom")
			},
		}

		req := httptest.NewRequest(http.MethodPost, "/api/v1/settings/coding-auths", bytes.NewBufferString(`{
			"agent":"codex",
			"auth_type":"api_key",
			"api_key":"sk-test-123456789"
		}`))
		req = withAdminUser(req, uuid.New(), orgID)
		rr := httptest.NewRecorder()

		NewCodingAuthHandler(store, nil).Create(rr, req)

		require.Equal(t, http.StatusInternalServerError, rr.Code, "Create should surface store failures")
	})
}

func TestCodingAuthHandlerUpdateAndDisable(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	rowID := uuid.New()

	t.Run("updates a row", func(t *testing.T) {
		t.Parallel()

		store := &mockCodingAuthStore{
			updateFn: func(_ context.Context, gotOrgID uuid.UUID, id uuid.UUID, input models.UpdateCodingAuthInput) (*models.CodingAuth, error) {
				require.Equal(t, orgID, gotOrgID, "UpdateCodingAuth should scope writes to the org")
				require.Equal(t, rowID, id, "UpdateCodingAuth should target the selected row")
				require.NotNil(t, input.Label, "UpdateCodingAuth should accept label edits")
				require.Equal(t, "Renamed", *input.Label, "UpdateCodingAuth should pass the new label")
				return &models.CodingAuth{
					ID:       rowID,
					OrgID:    orgID,
					Priority: 1,
					Agent:    models.AgentTypeCodex,
					AuthType: models.CodingAuthTypeSubscription,
					Label:    *input.Label,
					Status:   models.CodingAuthStatusHealthy,
				}, nil
			},
		}

		req := httptest.NewRequest(http.MethodPatch, "/api/v1/settings/coding-auths/"+rowID.String(), bytes.NewBufferString(`{"label":"Renamed"}`))
		req = withAdminUser(req, uuid.New(), orgID)
		req.SetPathValue("id", rowID.String())
		rr := httptest.NewRecorder()

		NewCodingAuthHandler(store, nil).Update(rr, req)

		require.Equal(t, http.StatusOK, rr.Code, "Update should return 200")
	})

	t.Run("disables a row", func(t *testing.T) {
		t.Parallel()

		store := &mockCodingAuthStore{
			disableFn: func(_ context.Context, gotOrgID uuid.UUID, id uuid.UUID) error {
				require.Equal(t, orgID, gotOrgID, "DisableCodingAuth should scope writes to the org")
				require.Equal(t, rowID, id, "DisableCodingAuth should target the selected row")
				return nil
			},
		}

		req := httptest.NewRequest(http.MethodDelete, "/api/v1/settings/coding-auths/"+rowID.String(), nil)
		req = withAdminUser(req, uuid.New(), orgID)
		req.SetPathValue("id", rowID.String())
		rr := httptest.NewRecorder()

		NewCodingAuthHandler(store, nil).Delete(rr, req)

		require.Equal(t, http.StatusNoContent, rr.Code, "Delete should return 204")
	})

	t.Run("surfaces store errors", func(t *testing.T) {
		t.Parallel()

		store := &mockCodingAuthStore{
			updateFn: func(_ context.Context, _ uuid.UUID, _ uuid.UUID, _ models.UpdateCodingAuthInput) (*models.CodingAuth, error) {
				return nil, errors.New("boom")
			},
		}

		req := httptest.NewRequest(http.MethodPatch, "/api/v1/settings/coding-auths/"+rowID.String(), bytes.NewBufferString(`{"label":"Renamed"}`))
		req = withAdminUser(req, uuid.New(), orgID)
		req.SetPathValue("id", rowID.String())
		rr := httptest.NewRecorder()

		NewCodingAuthHandler(store, nil).Update(rr, req)

		require.Equal(t, http.StatusInternalServerError, rr.Code, "Update should surface store failures")
	})

	t.Run("rejects invalid update id", func(t *testing.T) {
		t.Parallel()

		req := httptest.NewRequest(http.MethodPatch, "/api/v1/settings/coding-auths/bad-id", bytes.NewBufferString(`{"label":"Renamed"}`))
		req = withAdminUser(req, uuid.New(), orgID)
		req.SetPathValue("id", "bad-id")
		rr := httptest.NewRecorder()

		NewCodingAuthHandler(&mockCodingAuthStore{}, nil).Update(rr, req)

		require.Equal(t, http.StatusBadRequest, rr.Code, "Update should reject invalid ids")
	})

	t.Run("rejects invalid update json", func(t *testing.T) {
		t.Parallel()

		req := httptest.NewRequest(http.MethodPatch, "/api/v1/settings/coding-auths/"+rowID.String(), bytes.NewBufferString(`{`))
		req = withAdminUser(req, uuid.New(), orgID)
		req.SetPathValue("id", rowID.String())
		rr := httptest.NewRecorder()

		NewCodingAuthHandler(&mockCodingAuthStore{}, nil).Update(rr, req)

		require.Equal(t, http.StatusBadRequest, rr.Code, "Update should reject invalid JSON")
	})

	t.Run("rejects invalid delete id", func(t *testing.T) {
		t.Parallel()

		req := httptest.NewRequest(http.MethodDelete, "/api/v1/settings/coding-auths/bad-id", nil)
		req = withAdminUser(req, uuid.New(), orgID)
		req.SetPathValue("id", "bad-id")
		rr := httptest.NewRecorder()

		NewCodingAuthHandler(&mockCodingAuthStore{}, nil).Delete(rr, req)

		require.Equal(t, http.StatusBadRequest, rr.Code, "Delete should reject invalid ids")
	})

	t.Run("surfaces delete store errors", func(t *testing.T) {
		t.Parallel()

		store := &mockCodingAuthStore{
			disableFn: func(_ context.Context, _ uuid.UUID, _ uuid.UUID) error {
				return errors.New("boom")
			},
		}

		req := httptest.NewRequest(http.MethodDelete, "/api/v1/settings/coding-auths/"+rowID.String(), nil)
		req = withAdminUser(req, uuid.New(), orgID)
		req.SetPathValue("id", rowID.String())
		rr := httptest.NewRecorder()

		NewCodingAuthHandler(store, nil).Delete(rr, req)

		require.Equal(t, http.StatusInternalServerError, rr.Code, "Delete should surface store failures")
	})
}

func TestCodingAuthHandlerLegacyStatus(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	store := &mockCodingAuthStore{
		listByProviderFn: func(_ context.Context, gotOrgID uuid.UUID, provider models.ProviderName) ([]models.DecryptedCredential, error) {
			require.Equal(t, orgID, gotOrgID, "legacy status should scope provider lookups to the org")
			switch provider {
			case models.ProviderAmp, models.ProviderPi:
				return nil, nil
			default:
				return nil, nil
			}
		},
	}
	orgStore := &mockCodingAuthOrgStore{
		getFn: func(_ context.Context, id uuid.UUID) (models.Organization, error) {
			require.Equal(t, orgID, id, "legacy status should load the current org")
			return models.Organization{
				ID: orgID,
				Settings: json.RawMessage(`{
					"agent_config": {
						"amp": {
							"AMP_API_KEY": "amp_1234567890abcdef",
							"AMP_MODE": "deep"
						},
						"pi": {
							"PI_MODEL": "anthropic/claude-sonnet-4-6"
						}
					}
				}`),
			}, nil
		},
	}

	req := httptest.NewRequest(http.MethodGet, "/api/v1/settings/coding-auths/legacy-status", nil)
	req = withAdminUser(req, uuid.New(), orgID)
	rr := httptest.NewRecorder()

	NewCodingAuthHandler(store, orgStore).LegacyStatus(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, "LegacyStatus should return 200")

	var resp models.SingleResponse[models.LegacyCodingAuthStatus]
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp), "LegacyStatus response should be valid JSON")
	require.True(t, resp.Data.HasLegacyAmpSecret, "LegacyStatus should report the legacy Amp secret")
	require.Equal(t, "amp_12...cdef", resp.Data.AmpMaskedKey, "LegacyStatus should mask the legacy Amp key")
	require.True(t, resp.Data.HasLegacyPiDefaults, "LegacyStatus should report legacy Pi defaults")
	require.True(t, resp.Data.PiRequiresManualAuth, "LegacyStatus should tell the UI Pi still needs a dedicated auth")
	require.False(t, resp.Data.HasAmpCredential, "LegacyStatus should reflect the missing migrated Amp row")
	require.False(t, resp.Data.HasPiCredential, "LegacyStatus should reflect the missing Pi credential row")
}

func TestCodingAuthHandlerLegacyStatus_ErrorCases(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()

	t.Run("surfaces handler errors", func(t *testing.T) {
		t.Parallel()

		store := &mockCodingAuthStore{}
		orgStore := &mockCodingAuthOrgStore{
			getFn: func(_ context.Context, _ uuid.UUID) (models.Organization, error) {
				return models.Organization{}, errors.New("boom")
			},
		}
		req := httptest.NewRequest(http.MethodGet, "/api/v1/settings/coding-auths/legacy-status", nil)
		req = withAdminUser(req, uuid.New(), orgID)
		rr := httptest.NewRecorder()

		NewCodingAuthHandler(store, orgStore).LegacyStatus(rr, req)

		require.Equal(t, http.StatusInternalServerError, rr.Code, "LegacyStatus should surface helper failures")
	})

	t.Run("returns zero status when org store is unavailable", func(t *testing.T) {
		t.Parallel()

		status, err := NewCodingAuthHandler(&mockCodingAuthStore{}, nil).legacyStatus(context.Background(), orgID)
		require.NoError(t, err, "legacyStatus should not fail when org store is unavailable")
		require.Equal(t, models.LegacyCodingAuthStatus{}, status, "legacyStatus should return the zero value when org store is unavailable")
	})

	t.Run("surfaces provider lookup failures", func(t *testing.T) {
		t.Parallel()

		store := &mockCodingAuthStore{
			listByProviderFn: func(_ context.Context, _ uuid.UUID, provider models.ProviderName) ([]models.DecryptedCredential, error) {
				if provider == models.ProviderAmp {
					return nil, errors.New("amp lookup failed")
				}
				return nil, nil
			},
		}
		orgStore := &mockCodingAuthOrgStore{
			getFn: func(_ context.Context, _ uuid.UUID) (models.Organization, error) {
				return models.Organization{ID: orgID, Settings: json.RawMessage(`{"agent_config":{}}`)}, nil
			},
		}

		_, err := NewCodingAuthHandler(store, orgStore).legacyStatus(context.Background(), orgID)
		require.Error(t, err, "legacyStatus should surface provider lookup failures")
	})
}

func TestCodingAuthHandlerMigrateLegacy(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	userID := uuid.New()
	var created []models.CreateCodingAuthInput
	var updatedOrgSettings json.RawMessage
	invalidator := &mockOrgSettingsInvalidator{}
	store := &mockCodingAuthStore{
		listByProviderFn: func(_ context.Context, gotOrgID uuid.UUID, provider models.ProviderName) ([]models.DecryptedCredential, error) {
			require.Equal(t, orgID, gotOrgID, "migrate should scope provider lookups to the org")
			switch provider {
			case models.ProviderAmp, models.ProviderPi:
				return nil, nil
			default:
				return nil, nil
			}
		},
		createFn: func(_ context.Context, gotOrgID uuid.UUID, createdBy *uuid.UUID, input models.CreateCodingAuthInput) (*models.CodingAuth, error) {
			require.Equal(t, orgID, gotOrgID, "migrate should create rows in the current org")
			require.NotNil(t, createdBy, "migrate should attribute created rows to the current user")
			require.Equal(t, userID, *createdBy, "migrate should attribute created rows to the current user")
			created = append(created, input)
			return &models.CodingAuth{
				ID:       uuid.New(),
				OrgID:    gotOrgID,
				Priority: len(created),
				Agent:    input.Agent,
				AuthType: input.AuthType,
				Label:    input.Label,
				Status:   models.CodingAuthStatusNeverVerified,
			}, nil
		},
	}
	orgStore := &mockCodingAuthOrgStore{
		getFn: func(_ context.Context, id uuid.UUID) (models.Organization, error) {
			require.Equal(t, orgID, id, "migrate should load the current org")
			return models.Organization{
				ID: orgID,
				Settings: json.RawMessage(`{
					"agent_config": {
						"amp": {
							"AMP_API_KEY": "amp_1234567890abcdef",
							"AMP_MODE": "deep"
						},
						"pi": {
							"PI_API_KEY": "pi_1234567890abcdef",
							"PI_MODEL": "anthropic/claude-sonnet-4-6"
						}
					}
				}`),
			}, nil
		},
		updateFn: func(_ context.Context, org *models.Organization) error {
			updatedOrgSettings = append(json.RawMessage(nil), org.Settings...)
			return nil
		},
	}

	req := httptest.NewRequest(http.MethodPost, "/api/v1/settings/coding-auths/migrate-legacy", nil)
	req = withAdminUser(req, userID, orgID)
	rr := httptest.NewRecorder()

	handler := NewCodingAuthHandler(store, orgStore)
	handler.SetOrgSettingsInvalidator(invalidator)
	handler.MigrateLegacy(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, "MigrateLegacy should return 200")
	require.Len(t, created, 2, "MigrateLegacy should backfill both Amp and Pi when legacy secrets exist")
	require.Equal(t, models.AgentTypeAmp, created[0].Agent, "MigrateLegacy should create an Amp coding auth row")
	require.Equal(t, "amp_1234567890abcdef", created[0].APIKey, "MigrateLegacy should preserve the legacy Amp key")
	require.Equal(t, models.AgentTypePi, created[1].Agent, "MigrateLegacy should create a Pi coding auth row")
	require.Equal(t, "pi_1234567890abcdef", created[1].APIKey, "MigrateLegacy should preserve the legacy Pi key")
	require.JSONEq(t, `{
		"agent_config": {
			"amp": {
				"AMP_MODE": "deep"
			},
			"pi": {
				"PI_MODEL": "anthropic/claude-sonnet-4-6"
			}
		}
	}`, string(updatedOrgSettings), "MigrateLegacy should scrub only the migrated secrets and keep non-secret defaults")
	require.Equal(t, []uuid.UUID{orgID}, invalidator.orgIDs, "MigrateLegacy should invalidate the org settings cache after scrubbing legacy secrets")

	var resp models.SingleResponse[models.LegacyCodingAuthMigrationResult]
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp), "MigrateLegacy response should be valid JSON")
	require.True(t, resp.Data.MigratedAmp, "MigrateLegacy should report the Amp backfill")
	require.True(t, resp.Data.MigratedPi, "MigrateLegacy should report the Pi backfill")
	require.True(t, resp.Data.RemovedLegacySecrets, "MigrateLegacy should report the settings cleanup")
}

func TestCodingAuthHandlerMigrateLegacy_ErrorCases(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	userID := uuid.New()

	t.Run("requires an authenticated user", func(t *testing.T) {
		t.Parallel()

		req := httptest.NewRequest(http.MethodPost, "/api/v1/settings/coding-auths/migrate-legacy", nil)
		req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
		rr := httptest.NewRecorder()

		NewCodingAuthHandler(&mockCodingAuthStore{}, &mockCodingAuthOrgStore{}).MigrateLegacy(rr, req)

		require.Equal(t, http.StatusUnauthorized, rr.Code, "MigrateLegacy should require an authenticated user")
	})

	t.Run("requires an org store", func(t *testing.T) {
		t.Parallel()

		req := httptest.NewRequest(http.MethodPost, "/api/v1/settings/coding-auths/migrate-legacy", nil)
		req = withAdminUser(req, userID, orgID)
		rr := httptest.NewRecorder()

		NewCodingAuthHandler(&mockCodingAuthStore{}, nil).MigrateLegacy(rr, req)

		require.Equal(t, http.StatusInternalServerError, rr.Code, "MigrateLegacy should require an org store")
	})

	t.Run("surfaces org lookup and parse failures", func(t *testing.T) {
		t.Parallel()

		tests := []struct {
			name     string
			orgStore *mockCodingAuthOrgStore
			wantCode int
		}{
			{
				name: "org not found",
				orgStore: &mockCodingAuthOrgStore{
					getFn: func(_ context.Context, _ uuid.UUID) (models.Organization, error) {
						return models.Organization{}, errors.New("missing")
					},
				},
				wantCode: http.StatusNotFound,
			},
			{
				name: "invalid settings json",
				orgStore: &mockCodingAuthOrgStore{
					getFn: func(_ context.Context, _ uuid.UUID) (models.Organization, error) {
						return models.Organization{ID: orgID, Settings: json.RawMessage(`{"agent_config":`)}, nil
					},
				},
				wantCode: http.StatusInternalServerError,
			},
		}

		for _, tt := range tests {
			tt := tt
			t.Run(tt.name, func(t *testing.T) {
				t.Parallel()

				req := httptest.NewRequest(http.MethodPost, "/api/v1/settings/coding-auths/migrate-legacy", nil)
				req = withAdminUser(req, userID, orgID)
				rr := httptest.NewRecorder()

				NewCodingAuthHandler(&mockCodingAuthStore{}, tt.orgStore).MigrateLegacy(rr, req)
				require.Equal(t, tt.wantCode, rr.Code, "MigrateLegacy should surface %s", tt.name)
			})
		}
	})

	t.Run("surfaces provider, create, scrub, and update failures", func(t *testing.T) {
		t.Parallel()

		tests := []struct {
			name     string
			store    *mockCodingAuthStore
			orgStore *mockCodingAuthOrgStore
		}{
			{
				name: "amp lookup failure",
				store: &mockCodingAuthStore{
					listByProviderFn: func(_ context.Context, _ uuid.UUID, provider models.ProviderName) ([]models.DecryptedCredential, error) {
						if provider == models.ProviderAmp {
							return nil, errors.New("amp lookup failed")
						}
						return nil, nil
					},
				},
				orgStore: &mockCodingAuthOrgStore{
					getFn: func(_ context.Context, _ uuid.UUID) (models.Organization, error) {
						return models.Organization{ID: orgID, Settings: json.RawMessage(`{"agent_config":{}}`)}, nil
					},
				},
			},
			{
				name: "pi lookup failure",
				store: &mockCodingAuthStore{
					listByProviderFn: func(_ context.Context, _ uuid.UUID, provider models.ProviderName) ([]models.DecryptedCredential, error) {
						if provider == models.ProviderPi {
							return nil, errors.New("pi lookup failed")
						}
						return nil, nil
					},
				},
				orgStore: &mockCodingAuthOrgStore{
					getFn: func(_ context.Context, _ uuid.UUID) (models.Organization, error) {
						return models.Organization{ID: orgID, Settings: json.RawMessage(`{"agent_config":{}}`)}, nil
					},
				},
			},
			{
				name: "amp create failure",
				store: &mockCodingAuthStore{
					listByProviderFn: func(_ context.Context, _ uuid.UUID, _ models.ProviderName) ([]models.DecryptedCredential, error) {
						return nil, nil
					},
					createFn: func(_ context.Context, _ uuid.UUID, _ *uuid.UUID, input models.CreateCodingAuthInput) (*models.CodingAuth, error) {
						if input.Agent == models.AgentTypeAmp {
							return nil, errors.New("amp create failed")
						}
						return &models.CodingAuth{}, nil
					},
				},
				orgStore: &mockCodingAuthOrgStore{
					getFn: func(_ context.Context, _ uuid.UUID) (models.Organization, error) {
						return models.Organization{ID: orgID, Settings: json.RawMessage(`{"agent_config":{"amp":{"AMP_API_KEY":"amp_key"}}}`)}, nil
					},
				},
			},
			{
				name: "pi create failure",
				store: &mockCodingAuthStore{
					listByProviderFn: func(_ context.Context, _ uuid.UUID, _ models.ProviderName) ([]models.DecryptedCredential, error) {
						return nil, nil
					},
					createFn: func(_ context.Context, _ uuid.UUID, _ *uuid.UUID, input models.CreateCodingAuthInput) (*models.CodingAuth, error) {
						if input.Agent == models.AgentTypePi {
							return nil, errors.New("pi create failed")
						}
						return &models.CodingAuth{}, nil
					},
				},
				orgStore: &mockCodingAuthOrgStore{
					getFn: func(_ context.Context, _ uuid.UUID) (models.Organization, error) {
						return models.Organization{ID: orgID, Settings: json.RawMessage(`{"agent_config":{"pi":{"PI_API_KEY":"pi_key"}}}`)}, nil
					},
				},
			},
			{
				name: "scrub failure",
				store: &mockCodingAuthStore{
					listByProviderFn: func(_ context.Context, _ uuid.UUID, _ models.ProviderName) ([]models.DecryptedCredential, error) {
						return nil, nil
					},
				},
				orgStore: &mockCodingAuthOrgStore{
					getFn: func(_ context.Context, _ uuid.UUID) (models.Organization, error) {
						return models.Organization{ID: orgID, Settings: json.RawMessage(`{"agent_config":`)}, nil
					},
				},
			},
			{
				name: "settings update failure after scrub",
				store: &mockCodingAuthStore{
					listByProviderFn: func(_ context.Context, _ uuid.UUID, _ models.ProviderName) ([]models.DecryptedCredential, error) {
						return nil, nil
					},
				},
				orgStore: &mockCodingAuthOrgStore{
					getFn: func(_ context.Context, _ uuid.UUID) (models.Organization, error) {
						return models.Organization{ID: orgID, Settings: json.RawMessage(`{"agent_config":{"amp":{"AMP_API_KEY":"amp_key"}}}`)}, nil
					},
					updateFn: func(_ context.Context, _ *models.Organization) error {
						return errors.New("update failed")
					},
				},
			},
		}

		for _, tt := range tests {
			tt := tt
			t.Run(tt.name, func(t *testing.T) {
				t.Parallel()

				req := httptest.NewRequest(http.MethodPost, "/api/v1/settings/coding-auths/migrate-legacy", nil)
				req = withAdminUser(req, userID, orgID)
				rr := httptest.NewRecorder()

				NewCodingAuthHandler(tt.store, tt.orgStore).MigrateLegacy(rr, req)
				require.Equal(t, http.StatusInternalServerError, rr.Code, "MigrateLegacy should surface %s", tt.name)
			})
		}
	})
}

func TestCodingAuthLegacyHelpers(t *testing.T) {
	t.Parallel()

	t.Run("parseCodingAuthID falls back to path value", func(t *testing.T) {
		t.Parallel()

		id := uuid.New()
		req := httptest.NewRequest(http.MethodGet, "/api/v1/settings/coding-auths/"+id.String(), nil)
		req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, chi.NewRouteContext()))
		req.SetPathValue("id", id.String())
		rr := httptest.NewRecorder()

		parsed, ok := parseCodingAuthID(rr, req)
		require.True(t, ok, "parseCodingAuthID should accept path values when chi params are absent")
		require.Equal(t, id, parsed, "parseCodingAuthID should return the parsed UUID")
	})

	t.Run("inspect legacy settings handles errors and missing config", func(t *testing.T) {
		t.Parallel()

		_, err := inspectLegacyCodingAuthSettings(json.RawMessage(`{"agent_config":`))
		require.Error(t, err, "inspectLegacyCodingAuthSettings should reject invalid JSON")

		legacy, err := inspectLegacyCodingAuthSettings(json.RawMessage(`{"other":true}`))
		require.NoError(t, err, "inspectLegacyCodingAuthSettings should allow settings with no agent config")
		require.Equal(t, legacyCodingAuthSettings{}, legacy, "inspectLegacyCodingAuthSettings should return zero legacy settings when no agent config exists")
	})

	t.Run("scrub legacy secrets handles no-ops and malformed input", func(t *testing.T) {
		t.Parallel()

		_, _, err := scrubLegacyCodingAuthSecrets(json.RawMessage(`{"agent_config":`))
		require.Error(t, err, "scrubLegacyCodingAuthSecrets should reject malformed JSON")

		cleaned, removed, err := scrubLegacyCodingAuthSecrets(json.RawMessage(`{"agent_config":{"amp":{"AMP_MODE":"deep"}}}`))
		require.NoError(t, err, "scrubLegacyCodingAuthSecrets should allow settings with no secrets")
		require.False(t, removed, "scrubLegacyCodingAuthSecrets should report no removal when no secrets exist")
		require.JSONEq(t, `{"agent_config":{"amp":{"AMP_MODE":"deep"}}}`, string(cleaned), "scrubLegacyCodingAuthSecrets should preserve settings when no secrets are removed")

		cleaned, removed, err = scrubLegacyCodingAuthSecrets(json.RawMessage(`{"agent_config":{"amp":{"AMP_API_KEY":"amp_key"},"pi":{"PI_API_KEY":"pi_key"}}}`))
		require.NoError(t, err, "scrubLegacyCodingAuthSecrets should remove legacy secrets")
		require.True(t, removed, "scrubLegacyCodingAuthSecrets should report when secrets are removed")
		require.JSONEq(t, `{}`, string(cleaned), "scrubLegacyCodingAuthSecrets should drop empty agent config maps after removing secrets")
	})

	t.Run("nestedStringValue trims strings and ignores non-strings", func(t *testing.T) {
		t.Parallel()

		require.Equal(t, "trimmed", nestedStringValue(map[string]any{"x": " trimmed "}, "x"), "nestedStringValue should trim whitespace")
		require.Equal(t, "", nestedStringValue(map[string]any{"x": 1}, "x"), "nestedStringValue should ignore non-string values")
		require.Equal(t, "", nestedStringValue(map[string]any{}, "x"), "nestedStringValue should return empty when the key is missing")
	})
}
