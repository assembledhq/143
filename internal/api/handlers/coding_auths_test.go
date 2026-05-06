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
	mergeAgentDefaultsFn func(ctx context.Context, orgID uuid.UUID, agent models.AgentType, defaults map[string]string) error
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
					Status:   models.CodingAuthStatusInvalid,
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
	require.Equal(t, models.CodingAuthStatusInvalid, resp.Data[1].Status, "List should preserve row statuses")
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
				Status:   models.CodingAuthStatusHealthy,
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
				Status:   models.CodingAuthStatusHealthy,
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
					Status:   models.CodingAuthStatusHealthy,
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

func TestParseCodingAuthIDFallsBackToPathValue(t *testing.T) {
	t.Parallel()

	id := uuid.New()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/settings/coding-auths/"+id.String(), nil)
	req = req.WithContext(context.WithValue(req.Context(), chi.RouteCtxKey, chi.NewRouteContext()))
	req.SetPathValue("id", id.String())
	rr := httptest.NewRecorder()

	parsed, ok := parseCodingAuthID(rr, req)
	require.True(t, ok, "parseCodingAuthID should accept path values when chi params are absent")
	require.Equal(t, id, parsed, "parseCodingAuthID should return the parsed UUID")
}
