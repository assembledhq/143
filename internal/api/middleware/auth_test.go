package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/assembledhq/143/internal/models"
)

func TestUserFromContext_NoUser(t *testing.T) {
	ctx := context.Background()
	user := UserFromContext(ctx)
	assert.Nil(t, user)
}

func TestUserFromContext_WithUser(t *testing.T) {
	ctx := context.Background()
	u := &models.User{
		ID:    uuid.New(),
		OrgID: uuid.New(),
		Email: "test@example.com",
		Name:  "Test User",
		Role:  "admin",
	}
	ctx = WithUser(ctx, u)
	result := UserFromContext(ctx)
	assert.NotNil(t, result)
	assert.Equal(t, u.ID, result.ID)
	assert.Equal(t, u.Email, result.Email)
}

func TestOrgIDFromContext_NoOrgID(t *testing.T) {
	ctx := context.Background()
	orgID := OrgIDFromContext(ctx)
	assert.Equal(t, uuid.Nil, orgID)
}

func TestOrgIDFromContext_WithOrgID(t *testing.T) {
	ctx := context.Background()
	expected := uuid.New()
	ctx = WithOrgID(ctx, expected)
	result := OrgIDFromContext(ctx)
	assert.Equal(t, expected, result)
}

func TestOrgContext_RejectsMissingOrg(t *testing.T) {
	handler := OrgContext(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)
	assert.Equal(t, http.StatusForbidden, w.Code)
}

func TestOrgContext_AllowsValidOrg(t *testing.T) {
	handler := OrgContext(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	ctx := WithOrgID(req.Context(), uuid.New())
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
}
