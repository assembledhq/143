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

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/models"
)

type mockCodingAuthStore struct {
	listFn    func(ctx context.Context, orgID uuid.UUID) ([]models.CodingAuth, error)
	reorderFn func(ctx context.Context, orgID uuid.UUID, ids []uuid.UUID) error
	createFn  func(ctx context.Context, orgID uuid.UUID, createdBy *uuid.UUID, input models.CreateCodingAuthInput) (*models.CodingAuth, error)
	updateFn  func(ctx context.Context, orgID uuid.UUID, id uuid.UUID, input models.UpdateCodingAuthInput) (*models.CodingAuth, error)
	disableFn func(ctx context.Context, orgID uuid.UUID, id uuid.UUID) error
}

func (m *mockCodingAuthStore) ListCodingAuths(ctx context.Context, orgID uuid.UUID) ([]models.CodingAuth, error) {
	if m.listFn != nil {
		return m.listFn(ctx, orgID)
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

func withAdminUser(r *http.Request, userID, orgID uuid.UUID) *http.Request {
	ctx := middleware.WithOrgID(r.Context(), orgID)
	ctx = middleware.WithUser(ctx, &models.User{ID: userID, OrgID: orgID, Role: "admin"})
	ctx = middleware.WithActiveRole(ctx, "admin")
	return r.WithContext(ctx)
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

	NewCodingAuthHandler(store).List(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, "List should return 200")

	var resp models.ListResponse[models.CodingAuth]
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp), "List response should be valid JSON")
	require.Len(t, resp.Data, 2, "List should return every configured coding auth")
	require.Equal(t, firstID, resp.Data[0].ID, "List should preserve effective runtime order")
	require.True(t, resp.Data[0].IsDefault, "List should surface the default row explicitly")
	require.Equal(t, models.CodingAuthStatusNeverVerified, resp.Data[1].Status, "List should preserve row statuses")
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

		NewCodingAuthHandler(store).Reorder(rr, req)

		require.Equal(t, http.StatusNoContent, rr.Code, "Reorder should return 204")
		require.Equal(t, []uuid.UUID{secondID, firstID}, gotIDs, "Reorder should pass the submitted order to the store")
	})

	t.Run("rejects malformed ids", func(t *testing.T) {
		t.Parallel()

		store := &mockCodingAuthStore{}
		req := httptest.NewRequest(http.MethodPatch, "/api/v1/settings/coding-auths/reorder", bytes.NewBufferString(`{"ids":["bad-id"]}`))
		req = withAdminUser(req, uuid.New(), orgID)
		rr := httptest.NewRecorder()

		NewCodingAuthHandler(store).Reorder(rr, req)

		require.Equal(t, http.StatusBadRequest, rr.Code, "Reorder should reject invalid UUIDs")
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

	NewCodingAuthHandler(store).Create(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, "Create should return 200")

	var resp models.SingleResponse[models.CodingAuth]
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp), "Create response should be valid JSON")
	require.Equal(t, createdID, resp.Data.ID, "Create should return the created row")
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

		NewCodingAuthHandler(store).Update(rr, req)

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

		NewCodingAuthHandler(store).Delete(rr, req)

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

		NewCodingAuthHandler(store).Update(rr, req)

		require.Equal(t, http.StatusInternalServerError, rr.Code, "Update should surface store failures")
	})
}
