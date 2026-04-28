package handlers

import (
	"bytes"
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
)

type mockCodingCredentialStore struct {
	getFn          func(ctx context.Context, scope models.Scope, id uuid.UUID) (*models.DecryptedCodingCredential, error)
	listByScopeFn  func(ctx context.Context, scope models.Scope) ([]models.DecryptedCodingCredential, error)
	listProviderFn func(ctx context.Context, scope models.Scope, provider models.ProviderName) ([]models.DecryptedCodingCredential, error)
	listResolveFn  func(ctx context.Context, orgID uuid.UUID, userID *uuid.UUID, provider models.ProviderName) ([]models.DecryptedCodingCredential, error)
	createFn       func(ctx context.Context, scope models.Scope, label string, cfg models.ProviderConfig, opts db.CreateOpts) (*uuid.UUID, error)
	renameFn       func(ctx context.Context, scope models.Scope, id uuid.UUID, label string) error
	updateStatusFn func(ctx context.Context, scope models.Scope, id uuid.UUID, status string) error
	disableFn      func(ctx context.Context, scope models.Scope, id uuid.UUID) error
	moveFn         func(ctx context.Context, scope models.Scope, id uuid.UUID, pos models.MoveCodingCredentialInput) error
	reorderFn      func(ctx context.Context, scope models.Scope, orderedIDs []uuid.UUID) error
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

func (m *mockCodingCredentialStore) UpdateStatus(ctx context.Context, scope models.Scope, id uuid.UUID, status string) error {
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
		updateStatusFn: func(context.Context, models.Scope, uuid.UUID, string) error {
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

func TestCodingCredentialHandlerUpdateAllowsInvalidToActive(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	userID := uuid.New()
	rowID := uuid.New()
	now := time.Now().UTC()
	getCalls := 0
	var statusWritten string

	store := &mockCodingCredentialStore{
		getFn: func(_ context.Context, _ models.Scope, _ uuid.UUID) (*models.DecryptedCodingCredential, error) {
			getCalls++
			status := models.CodingCredentialStatusInvalid
			if getCalls > 1 {
				status = models.CodingCredentialStatusActive
			}
			return &models.DecryptedCodingCredential{
				ID:        rowID,
				OrgID:     orgID,
				UserID:    &userID,
				Provider:  models.ProviderAnthropic,
				Label:     "Claude API key",
				Config:    models.AnthropicConfig{APIKey: "sk-ant"},
				Status:    status,
				CreatedAt: now,
				UpdatedAt: now,
			}, nil
		},
		updateStatusFn: func(_ context.Context, _ models.Scope, _ uuid.UUID, status string) error {
			statusWritten = status
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

	require.Equal(t, http.StatusOK, rr.Code, "Update should allow active status for non-pending credentials")
	require.Equal(t, models.CodingCredentialStatusActive, statusWritten, "Update should write the requested active status")
	require.Equal(t, 2, getCalls, "Update should read before status validation and read back after the write")
}
