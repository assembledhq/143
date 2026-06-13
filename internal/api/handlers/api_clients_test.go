package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

func TestAPIClientHandler_CreateAPIKeyRejectsInvalidIPAllowlist(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	userID := uuid.New()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock should initialize")
	defer mock.Close()

	handler := NewAPIClientHandler(db.NewAPIClientStore(mock), db.NewAPITokenStore(mock))
	handler.SetTxStarter(mock)
	req := httptest.NewRequest(http.MethodPost, "/api/v1/api-keys", bytes.NewReader([]byte(`{"integration_name":"ci","token_name":"production","scopes":["sessions:create"],"allowed_ip_cidrs":["not-an-ip"]}`)))
	req = req.WithContext(apiKeyTestContext(req.Context(), orgID, userID))
	rr := httptest.NewRecorder()

	handler.CreateAPIKey(rr, req)

	require.Equal(t, http.StatusBadRequest, rr.Code, "CreateAPIKey should reject invalid IP allowlists before writing")
	var body struct {
		Error struct {
			Code string `json:"code"`
		} `json:"error"`
	}
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &body), "error body should be valid JSON")
	require.Equal(t, "INVALID_IP_ALLOWLIST", body.Error.Code, "invalid IP allowlists should use a stable error code")
	require.NoError(t, mock.ExpectationsWereMet(), "no database writes should be attempted")
}

func apiKeyTestContext(ctx context.Context, orgID, userID uuid.UUID) context.Context {
	ctx = middleware.WithOrgID(ctx, orgID)
	ctx = middleware.WithUser(ctx, &models.User{ID: userID})
	return ctx
}
