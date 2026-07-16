package db

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	pgxmock "github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

func browserSessionColumns() []string {
	return []string{"id", "org_id", "session_id", "preview_instance_id", "context_key", "control_state", "control_lease_owner_id", "control_lease_expires_at", "agent_action_token", "agent_action_expires_at", "handoff_reason", "current_url", "viewport", "storage_state", "console_cursor", "last_observed_at", "created_at", "updated_at"}
}

func TestPreviewBrowserSessionStore_GetBySessionScopesOrg(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "database mock should initialize")
	defer mock.Close()
	orgID, sessionID, previewID, recordID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	now := time.Now()
	mock.ExpectQuery(`FROM preview_browser_sessions WHERE org_id = \$1 AND session_id = \$2`).WithArgs(orgID, sessionID).WillReturnRows(pgxmock.NewRows(browserSessionColumns()).AddRow(recordID, orgID, sessionID, &previewID, "session:"+sessionID.String(), models.PreviewBrowserControlAgent, nil, nil, nil, nil, "", "/", json.RawMessage(`{"width":1440,"height":900}`), json.RawMessage(`{}`), int64(0), nil, now, now))
	actual, err := NewPreviewBrowserSessionStore(mock).GetBySession(context.Background(), orgID, sessionID)
	require.NoError(t, err, "tenant-scoped browser lookup should succeed")
	require.Equal(t, recordID, actual.ID, "browser lookup should return the expected record")
	require.NoError(t, mock.ExpectationsWereMet(), "browser lookup should include every expected database operation")
}

func TestPreviewBrowserSessionStore_EnsureScopesOrg(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "database mock should initialize")
	defer mock.Close()
	orgID, sessionID, previewID, recordID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	now := time.Now()
	viewport := models.ViewportSpec{Width: 1440, Height: 900}
	viewportJSON, err := json.Marshal(viewport)
	require.NoError(t, err, "viewport fixture should marshal")
	mock.ExpectQuery(`INSERT INTO preview_browser_sessions`).WithArgs(orgID, sessionID, previewID, "session:"+sessionID.String(), viewportJSON).WillReturnRows(pgxmock.NewRows(browserSessionColumns()).AddRow(recordID, orgID, sessionID, &previewID, "session:"+sessionID.String(), models.PreviewBrowserControlAgent, nil, nil, nil, nil, "", "", viewportJSON, json.RawMessage(`{}`), int64(0), nil, now, now))
	actual, err := NewPreviewBrowserSessionStore(mock).Ensure(context.Background(), orgID, sessionID, previewID, "session:"+sessionID.String(), viewport)
	require.NoError(t, err, "tenant-scoped browser ensure should succeed")
	require.Equal(t, sessionID, actual.SessionID, "browser ensure should return the expected session")
	require.NoError(t, mock.ExpectationsWereMet(), "browser ensure should include every expected database operation")
}

func TestPreviewBrowserSessionStore_SaveStateScopesOrg(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "database mock should initialize")
	defer mock.Close()
	orgID, sessionID := uuid.New(), uuid.New()
	viewport := models.ViewportSpec{Width: 390, Height: 844}
	viewportJSON, err := json.Marshal(viewport)
	require.NoError(t, err, "viewport fixture should marshal")
	storage := json.RawMessage(`{"cookies":[]}`)
	observedAt := time.Now()
	mock.ExpectExec(`UPDATE preview_browser_sessions SET current_url`).WithArgs(orgID, sessionID, "https://preview.test/app", viewportJSON, storage, int64(9), observedAt).WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	err = NewPreviewBrowserSessionStore(mock).SaveState(context.Background(), orgID, sessionID, "https://preview.test/app", viewport, storage, 9, observedAt)
	require.NoError(t, err, "tenant-scoped browser state save should succeed")
	require.NoError(t, mock.ExpectationsWereMet(), "browser state save should include every expected database operation")
}

func TestPreviewBrowserSessionStore_BeginAgentActionIsFencedByControlState(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "database mock should initialize")
	defer mock.Close()
	orgID, sessionID, token := uuid.New(), uuid.New(), uuid.New()
	mock.ExpectExec(`control_state = 'agent_control'.*agent_action_token IS NULL`).WithArgs(orgID, sessionID, token, pgInterval(time.Minute)).WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	started, err := NewPreviewBrowserSessionStore(mock).BeginAgentAction(context.Background(), orgID, sessionID, token, time.Minute)
	require.NoError(t, err, "agent action fence should be acquired")
	require.True(t, started, "agent action should start only in agent control")
	require.NoError(t, mock.ExpectationsWereMet(), "agent fence query should include tenant and control predicates")
}

func TestPreviewBrowserSessionStore_BeginHumanActionRequiresOwnerAndLiveLease(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "database mock should initialize")
	defer mock.Close()
	orgID, sessionID, userID, token := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	mock.ExpectExec(`control_state = 'human_control'.*control_lease_owner_id = \$3.*control_lease_expires_at > now`).WithArgs(orgID, sessionID, userID, token, pgInterval(time.Minute)).WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	started, err := NewPreviewBrowserSessionStore(mock).BeginHumanAction(context.Background(), orgID, sessionID, userID, token, time.Minute)
	require.NoError(t, err, "human action fence should be acquired")
	require.True(t, started, "human action should require the exact live lease owner")
	require.NoError(t, mock.ExpectationsWereMet(), "human fence query should include tenant, owner, and expiry predicates")
}

func TestPreviewBrowserSessionStore_GetControlExpiresAbandonedHumanLease(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "database mock should initialize")
	defer mock.Close()
	orgID, sessionID, previewID, recordID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	now := time.Now()
	mock.ExpectExec(`control_state = CASE WHEN control_state = 'human_control'.*control_lease_expires_at <= now`).WithArgs(orgID, sessionID).WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectQuery(`FROM preview_browser_sessions WHERE org_id = \$1 AND session_id = \$2`).WithArgs(orgID, sessionID).WillReturnRows(pgxmock.NewRows(browserSessionColumns()).AddRow(recordID, orgID, sessionID, &previewID, "session:"+sessionID.String(), models.PreviewBrowserControlAgent, nil, nil, nil, nil, "", "/", json.RawMessage(`{"width":1440,"height":900}`), json.RawMessage(`{}`), int64(3), nil, now, now))
	control, err := NewPreviewBrowserSessionStore(mock).GetControl(context.Background(), orgID, sessionID)
	require.NoError(t, err, "expired human lease should normalize")
	require.Equal(t, models.PreviewBrowserControlAgent, control.ControlState, "expired lease should return authority to the agent")
	require.NoError(t, mock.ExpectationsWereMet(), "lease expiry should remain tenant scoped")
}
