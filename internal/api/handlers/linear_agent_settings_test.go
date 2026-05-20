package handlers

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestLinearAgentSettingsHandler_PatchSettingsUpdatesDefaultRepo(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	repoID := uuid.New()
	writer := &linearAgentSettingsPatchRecorder{}
	handler := NewLinearAgentSettingsHandler(LinearAgentSettingsConfig{Orgs: writer})

	req := httptest.NewRequest(http.MethodPatch, "/api/v1/integrations/linear/agent", strings.NewReader(`{"default_repo_id":"`+repoID.String()+`"}`))
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	rr := httptest.NewRecorder()

	handler.PatchSettings(rr, req)

	require.Equal(t, http.StatusNoContent, rr.Code, "patch should accept a default repository without requiring enabled")
	require.NotNil(t, writer.settings.DefaultRepoID, "writer should receive the default repository setting")
	require.Equal(t, repoID, *writer.settings.DefaultRepoID, "writer should persist the requested default repository")
}

func TestLinearAgentSettingsHandler_PatchSettingsClearsDefaultRepo(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	writer := &linearAgentSettingsPatchRecorder{}
	handler := NewLinearAgentSettingsHandler(LinearAgentSettingsConfig{Orgs: writer})

	req := httptest.NewRequest(http.MethodPatch, "/api/v1/integrations/linear/agent", strings.NewReader(`{"default_repo_id":null}`))
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	rr := httptest.NewRecorder()

	handler.PatchSettings(rr, req)

	require.Equal(t, http.StatusNoContent, rr.Code, "patch should accept an explicit null default repository")
	require.True(t, writer.called, "writer should be called so the default repository is cleared")
	require.Nil(t, writer.settings.DefaultRepoID, "writer should receive nil default repository")
}

func TestLinearAgentSettingsHandler_GetStatusIncludesDefaultRepo(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	repoID := uuid.New()
	enabled := true
	handler := NewLinearAgentSettingsHandler(LinearAgentSettingsConfig{
		Settings: linearAgentSettingsLoaderFunc(func(context.Context, uuid.UUID) (models.LinearAgentSettings, error) {
			return models.LinearAgentSettings{Enabled: &enabled, DefaultRepoID: &repoID}, nil
		}),
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/integrations/linear/agent", nil)
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	rr := httptest.NewRecorder()

	handler.GetStatus(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, "status should be returned for an org-scoped request")
	var resp models.SingleResponse[LinearAgentInstallStatus]
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp), "status response should be valid JSON")
	require.NotNil(t, resp.Data.DefaultRepoID, "status should include the configured default repository")
	require.Equal(t, repoID, *resp.Data.DefaultRepoID, "status should return the configured default repository")
	require.True(t, resp.Data.Enabled, "status should preserve the enabled flag")
}

func TestLinearAgentSettingsHandler_PatchSettingsInvalidDefaultRepoReturns400(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	repoID := uuid.New()
	writer := &linearAgentSettingsPatchRecorder{
		settingsErr: fmt.Errorf("%w: repository not found", ErrInvalidDefaultRepo),
	}
	handler := NewLinearAgentSettingsHandler(LinearAgentSettingsConfig{Orgs: writer})

	req := httptest.NewRequest(http.MethodPatch, "/api/v1/integrations/linear/agent", strings.NewReader(`{"default_repo_id":"`+repoID.String()+`"}`))
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	rr := httptest.NewRecorder()

	handler.PatchSettings(rr, req)

	require.Equal(t, http.StatusBadRequest, rr.Code, "invalid default repo should return 400 not 500")
}

type linearAgentSettingsPatchRecorder struct {
	called      bool
	settings    models.LinearAgentSettings
	settingsErr error
}

func (r *linearAgentSettingsPatchRecorder) SetLinearAgentEnabled(_ context.Context, _ uuid.UUID, enabled bool) error {
	r.called = true
	r.settings.Enabled = &enabled
	return nil
}

func (r *linearAgentSettingsPatchRecorder) SetLinearAgentSettings(_ context.Context, _ uuid.UUID, settings models.LinearAgentSettings) error {
	r.called = true
	r.settings = settings
	return r.settingsErr
}

type linearAgentSettingsLoaderFunc func(context.Context, uuid.UUID) (models.LinearAgentSettings, error)

func (f linearAgentSettingsLoaderFunc) LoadAgentSettings(ctx context.Context, orgID uuid.UUID) (models.LinearAgentSettings, error) {
	return f(ctx, orgID)
}
