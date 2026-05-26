package preview

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/agent"
	"github.com/assembledhq/143/internal/testutil"
	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

// =============================================================================
// Mock types
// =============================================================================

type mockProvider struct {
	startHandle *PreviewHandle
	startErr    error
	stopErr     error
	dialErr     error
	dialStream  PreviewStream
	statusSnap  *PreviewStatusSnapshot
	statusErr   error
}

func (m *mockProvider) StartPreview(_ context.Context, _ *agent.Sandbox, _ *models.PreviewConfig, _ map[string]string, _ ServiceObserver) (*PreviewHandle, error) {
	if m.startErr != nil {
		return nil, m.startErr
	}
	if m.startHandle != nil {
		return m.startHandle, nil
	}
	return nil, fmt.Errorf("not implemented in mock")
}

func (m *mockProvider) StopPreview(_ context.Context, _ string) error {
	return m.stopErr
}

func (m *mockProvider) DialPreview(_ context.Context, _ string) (PreviewStream, error) {
	if m.dialErr != nil {
		return nil, m.dialErr
	}
	return m.dialStream, nil
}

func (m *mockProvider) PreviewStatus(_ context.Context, _ string) (*PreviewStatusSnapshot, error) {
	if m.statusErr != nil {
		return nil, m.statusErr
	}
	return m.statusSnap, nil
}

type mockStream struct {
	net.Conn
}

func (m *mockStream) Close() error { return nil }

type mockInspector struct {
	closed bool
}

func (m *mockInspector) CaptureScreenshot(_ context.Context, _ string, _ models.ScreenshotOpts) (*models.ScreenshotResult, error) {
	return nil, nil
}

func (m *mockInspector) CaptureDOM(_ context.Context, _ string, _ DOMCaptureOpts) (*DOMSnapshot, error) {
	return nil, nil
}

func (m *mockInspector) ReadConsole(_ context.Context, _ string) ([]ConsoleMessage, error) {
	return nil, nil
}

func (m *mockInspector) InspectElement(_ context.Context, _ string, _, _ int) (*models.ElementInfo, error) {
	return nil, nil
}

func (m *mockInspector) StartScreencast(_ context.Context, _ string, _ int) (string, error) {
	return "", nil
}

func (m *mockInspector) StopScreencast(_ context.Context, _ string) (*models.ScreencastResult, error) {
	return nil, nil
}

func (m *mockInspector) ExecuteInteraction(_ context.Context, _ string, _ []models.InteractionStep) (*models.InteractionResult, error) {
	return nil, nil
}

func (m *mockInspector) CaptureMultiViewport(_ context.Context, _ string, _ models.MultiViewportOpts) (*models.MultiViewportResult, error) {
	return nil, nil
}

func (m *mockInspector) ComputeVisualDiff(_ context.Context, _ string, _, _ string) (*models.VisualDiff, error) {
	return nil, nil
}

func (m *mockInspector) RunAssertions(_ context.Context, _ string, _ []Assertion) (*AssertionResult, error) {
	return nil, nil
}

func (m *mockInspector) Close() error {
	m.closed = true
	return nil
}

// =============================================================================
// Test helpers
// =============================================================================

var previewInstanceTestCols = []string{
	"id", "session_id", "preview_target_id", "org_id", "user_id", "profile_name", "name", "status",
	"provider", "worker_node_id", "preview_handle", "primary_service", "port",
	"config_digest", "base_commit_sha", "last_accessed_at", "expires_at", "stopped_at",
	"last_path", "memory_limit_mb", "cpu_limit_millis", "disk_limit_mb", "recycle_config", "recycle_sandbox", "current_phase", "request_id", "error", "created_at", "updated_at", "recycled_at", "recycle_scheduled_at",
	"preview_holding_container",
}

var previewServiceTestCols = []string{
	"id", "preview_instance_id", "service_name", "role", "status",
	"command", "cwd", "port", "pid", "error", "created_at",
}

var previewInfraTestCols = []string{
	"id", "preview_instance_id", "infra_name", "template",
	"container_id", "status", "host", "port", "credentials_hash", "error", "created_at",
}

var previewAccessSessionTestCols = []string{
	"id", "org_id", "user_id", "preview_instance_id",
	"session_token_hash", "issued_at", "expires_at", "revoked_at", "last_accessed_at", "created_at",
}

var sessionTestCols = []string{
	"id", "primary_issue_id", "org_id", "origin", "interaction_mode", "validation_policy", "agent_type", "status", "autonomy_level", "token_mode",
	"complexity_tier",
	"container_id", "worker_node_id", "turn_holding_container", "started_at", "completed_at", "token_usage",
	"failure_explanation", "failure_category", "failure_next_steps", "failure_retry_advised",
	"parent_session_id", "revision_context", "error", "result_summary", "diff",
	"pm_plan_id", "title", "pm_approach", "pm_reasoning",
	"project_task_id", "model_override", "reasoning_effort", "triggered_by_user_id",
	"agent_session_id", "current_turn", "last_activity_at", "sandbox_state", "snapshot_key", "pending_snapshot_key", "pending_snapshot_set_at",
	"runtime_soft_deadline_at", "runtime_hard_deadline_at", "runtime_last_progress_at", "runtime_last_progress_type", "runtime_last_progress_strength",
	"runtime_extension_count", "runtime_extension_seconds", "runtime_stop_reason", "runtime_graceful_stop_at",
	"checkpointed_at", "checkpoint_kind", "checkpoint_capability", "checkpoint_size_bytes", "checkpoint_error",
	"recovery_state", "recovery_queued_at", "recovery_started_at", "recovery_attempt_count",
	"target_branch", "working_branch", "base_commit_sha", "repository_id", "diff_stats", "diff_history", "input_manifest",
	"archived_at", "archived_by_user_id", "automation_run_id",
	"pr_creation_state", "pr_creation_error", "pr_push_state", "pr_push_error", "branch_creation_state", "branch_creation_error", "branch_url", "diff_collected_at", "latest_diff_snapshot_id", "has_unpushed_changes",
	// Migration 102 — Linear session-linking columns. Migration 100 — git
	// identity audit columns. Mocks must include both so SessionStore.GetByID's
	// row decode finds every selected field.
	"linear_private", "linear_state_sync_disabled", "linear_identifier_hint", "linear_prepare_state",
	"deleted_at", "git_identity_source", "git_identity_user_id", "created_at",
}

func stringPtr(value string) *string {
	return &value
}

func newPreviewInstanceRow(id, sessionID, orgID, userID uuid.UUID, status models.PreviewStatus, handle string, now time.Time) []any {
	return []any{
		id, sessionID, nil, orgID, userID, "bootstrap", "my-preview", string(status),
		"docker", "worker-1", handle, "web", 3000,
		"sha256:abc", "deadbeef", now, now.Add(30 * time.Minute), nil,
		"/", 512, 500, 10240, []byte(`{"version":"3","name":"my-preview","primary":"web","services":{"web":{"command":["npm","run","dev"],"port":3000,"ready":{"http_path":"/"}}},"credentials":{"mode":"none"},"network":{"mode":"restricted"}}`), []byte(`{"id":"sandbox-1","provider":"docker","work_dir":"/workspace","metadata":{"container_id":"abc"}}`), "reserved", stringPtr("req-1"), "", now, now, now, nil,
		false,
	}
}

func newAccessSessionRow(id, orgID, userID, previewID uuid.UUID, tokenHash string, expiresAt time.Time, revokedAt *time.Time, now time.Time) []any {
	return []any{
		id, orgID, userID, previewID,
		tokenHash, now, expiresAt, revokedAt, now, now,
	}
}

func newSessionRow(sessionID, orgID uuid.UUID, containerID *string, now time.Time) []any {
	issueID := uuid.New()
	byColumn := map[string]any{
		"id":                             sessionID,
		"primary_issue_id":               &issueID,
		"org_id":                         orgID,
		"origin":                         string(models.SessionOriginManual),
		"interaction_mode":               string(models.SessionInteractionModeInteractive),
		"validation_policy":              string(models.SessionValidationPolicyOnTurnComplete),
		"agent_type":                     "claude-code",
		"status":                         "running",
		"autonomy_level":                 "supervised",
		"token_mode":                     "low",
		"container_id":                   containerID,
		"turn_holding_container":         false,
		"started_at":                     &now,
		"failure_retry_advised":          false,
		"current_turn":                   0,
		"last_activity_at":               now,
		"sandbox_state":                  "running",
		"runtime_last_progress_type":     "",
		"runtime_last_progress_strength": "",
		"runtime_extension_count":        0,
		"runtime_extension_seconds":      0,
		"runtime_stop_reason":            "",
		"checkpoint_kind":                "",
		"checkpoint_capability":          "",
		"checkpoint_size_bytes":          int64(0),
		"recovery_state":                 "",
		"recovery_attempt_count":         0,
		"pr_creation_state":              "idle",
		"pr_creation_error":              (*string)(nil),
		"pr_push_state":                  "idle",
		"pr_push_error":                  (*string)(nil),
		"branch_creation_state":          "idle",
		"branch_creation_error":          (*string)(nil),
		"branch_url":                     (*string)(nil),
		"has_unpushed_changes":           false,
		"linear_private":                 false,
		"linear_state_sync_disabled":     false,
		"linear_identifier_hint":         (*string)(nil),
		"linear_prepare_state":           string(models.LinearPrepareStateNone),
		"deleted_at":                     nil,
		"git_identity_source":            nil,
		"git_identity_user_id":           nil,
		"created_at":                     now,
	}
	row := make([]any, len(sessionTestCols))
	for i, col := range sessionTestCols {
		row[i] = byColumn[col]
	}
	return row
}

func newTestManager(mock pgxmock.PgxPoolIface, provider PreviewCapableProvider) *Manager {
	store := db.NewPreviewStore(mock)
	return NewManager(ManagerConfig{
		Store:        store,
		Provider:     provider,
		Logger:       zerolog.Nop(),
		WorkerNodeID: "worker-1",
	})
}

type staticOrgSettingsStore struct {
	settings json.RawMessage
	err      error
}

func (s staticOrgSettingsStore) GetByID(_ context.Context, id uuid.UUID) (models.Organization, error) {
	if s.err != nil {
		return models.Organization{}, s.err
	}
	return models.Organization{ID: id, Settings: s.settings}, nil
}

// =============================================================================
// Existing tests
// =============================================================================

func TestComputeConfigDigest(t *testing.T) {
	t.Parallel()

	cfg := &models.PreviewConfig{
		Version: "3",
		Name:    "my-app",
		Primary: "web",
		Services: map[string]models.ServiceConfig{
			"web": {Port: 3000},
		},
	}

	digest := computeConfigDigest(cfg)
	require.True(t, len(digest) > 0)
	require.Contains(t, digest, "sha256:")

	// Same config produces same digest.
	digest2 := computeConfigDigest(cfg)
	require.Equal(t, digest, digest2)

	// Different config produces different digest.
	cfg2 := &models.PreviewConfig{
		Version: "3",
		Name:    "other-app",
		Primary: "api",
		Services: map[string]models.ServiceConfig{
			"api": {Port: 4000},
		},
	}
	digest3 := computeConfigDigest(cfg2)
	require.NotEqual(t, digest, digest3)
}

func TestGenerateAndHashToken(t *testing.T) {
	t.Parallel()

	token1, err := generateToken()
	require.NoError(t, err)
	token2, err := generateToken()
	require.NoError(t, err)

	require.Len(t, token1, 64) // 32 bytes → 64 hex chars
	require.NotEqual(t, token1, token2)

	hash1 := hashToken(token1)
	hash2 := hashToken(token1)
	require.Equal(t, hash1, hash2, "same token should produce same hash")

	hash3 := hashToken(token2)
	require.NotEqual(t, hash1, hash3, "different tokens should produce different hashes")
}

func TestNewManager_Defaults(t *testing.T) {
	t.Parallel()

	m := NewManager(ManagerConfig{})
	require.Equal(t, 4, DefaultMaxPreviewsPerUser, "default per-user preview cap should be four")
	require.Equal(t, DefaultMaxPreviewsPerUser, m.maxPerUser)
	require.Equal(t, DefaultMaxPreviewsPerOrg, m.maxPerOrg)
	require.Equal(t, DefaultMaxPreviewsPerWorker, m.maxPerWorker)
}

func TestNewManager_CustomCaps(t *testing.T) {
	t.Parallel()

	m := NewManager(ManagerConfig{
		MaxPerUser:   10,
		MaxPerOrg:    20,
		MaxPerWorker: 5,
	})
	require.Equal(t, 10, m.maxPerUser)
	require.Equal(t, 20, m.maxPerOrg)
	require.Equal(t, 5, m.maxPerWorker)
}

// =============================================================================
// StopPreview tests
// =============================================================================

func TestStopPreview_AlreadyTerminal(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	provider := &mockProvider{}
	mgr := newTestManager(mock, provider)

	orgID := uuid.New()
	previewID := uuid.New()
	sessionID := uuid.New()
	userID := uuid.New()
	now := time.Now()

	// Return a stopped instance so the early return path is hit.
	mock.ExpectQuery("SELECT .+ FROM preview_instances WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(previewInstanceTestCols).
				AddRow(newPreviewInstanceRow(previewID, sessionID, orgID, userID, models.PreviewStatusStopped, "handle-abc", now)...),
		)

	err = mgr.StopPreview(context.Background(), orgID, previewID)
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

// =============================================================================
// GetStatus tests
// =============================================================================

func TestGetStatus_Success(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := db.NewPreviewStore(mock)
	mgr := NewManager(ManagerConfig{
		Store:                 store,
		Provider:              &mockProvider{},
		Logger:                zerolog.Nop(),
		WorkerNodeID:          "worker-1",
		PreviewOriginTemplate: "https://{id}.preview.143.dev",
	})

	orgID := uuid.New()
	previewID := uuid.New()
	sessionID := uuid.New()
	userID := uuid.New()
	now := time.Now()

	// Expect GetPreviewInstance query.
	mock.ExpectQuery("SELECT .+ FROM preview_instances WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(previewInstanceTestCols).
				AddRow(newPreviewInstanceRow(previewID, sessionID, orgID, userID, models.PreviewStatusReady, "handle-abc", now)...),
		)

	// Expect ListServicesByPreview query.
	svcID := uuid.New()
	mock.ExpectQuery("SELECT .+ FROM preview_services").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(previewServiceTestCols).
				AddRow(svcID, previewID, "web", "primary", "running", []string{"npm", "start"}, "", 3000, (*int)(nil), "", now),
		)

	// Expect ListInfraByPreview query.
	mock.ExpectQuery("SELECT .+ FROM preview_infrastructure").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(previewInfraTestCols))

	resp, err := mgr.GetStatus(context.Background(), orgID, previewID)
	require.NoError(t, err)
	require.Equal(t, previewID, resp.Instance.ID)
	require.Equal(t, "https://"+previewID.String()+".preview.143.dev", resp.PreviewOrigin, "status response should expose the runtime preview origin")
	require.Len(t, resp.Services, 1)
	require.Len(t, resp.Infrastructure, 0)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestGetStatus_InstanceNotFound(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	mgr := newTestManager(mock, &mockProvider{})

	orgID := uuid.New()
	previewID := uuid.New()

	mock.ExpectQuery("SELECT .+ FROM preview_instances WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(previewInstanceTestCols))

	_, err = mgr.GetStatus(context.Background(), orgID, previewID)
	require.Error(t, err)
	require.Contains(t, err.Error(), "get preview instance")
	require.NoError(t, mock.ExpectationsWereMet())
}

// =============================================================================
// MintBootstrapToken tests
// =============================================================================

func TestMintBootstrapToken_ActivePreview(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	mgr := newTestManager(mock, &mockProvider{})

	orgID := uuid.New()
	userID := uuid.New()
	previewID := uuid.New()
	sessionID := uuid.New()
	now := time.Now()

	// Return an active (ready) instance.
	mock.ExpectQuery("SELECT .+ FROM preview_instances WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(previewInstanceTestCols).
				AddRow(newPreviewInstanceRow(previewID, sessionID, orgID, userID, models.PreviewStatusReady, "handle-abc", now)...),
		)

	// Expect CreateAccessSession insert.
	sessID := uuid.New()
	mock.ExpectQuery("INSERT INTO preview_access_sessions").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(previewAccessSessionTestCols).
				AddRow(newAccessSessionRow(sessID, orgID, userID, previewID, "somehash", now.Add(5*time.Minute), nil, now)...),
		)

	token, err := mgr.MintBootstrapToken(context.Background(), orgID, userID, previewID)
	require.NoError(t, err)
	require.NotEmpty(t, token)
	require.Len(t, token, 64) // 32 bytes hex
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestMintBootstrapToken_InactivePreview(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	mgr := newTestManager(mock, &mockProvider{})

	orgID := uuid.New()
	userID := uuid.New()
	previewID := uuid.New()
	sessionID := uuid.New()
	now := time.Now()

	// Return a stopped instance.
	mock.ExpectQuery("SELECT .+ FROM preview_instances WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(previewInstanceTestCols).
				AddRow(newPreviewInstanceRow(previewID, sessionID, orgID, userID, models.PreviewStatusStopped, "handle-abc", now)...),
		)

	_, err = mgr.MintBootstrapToken(context.Background(), orgID, userID, previewID)
	require.Error(t, err)
	require.Contains(t, err.Error(), "not active")
	require.NoError(t, mock.ExpectationsWereMet())
}

// =============================================================================
// ValidateBootstrapToken tests
// =============================================================================

func TestValidateBootstrapToken_Valid(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	mgr := newTestManager(mock, &mockProvider{})

	orgID := uuid.New()
	userID := uuid.New()
	previewID := uuid.New()
	sessID := uuid.New()
	now := time.Now()
	token, err := generateToken()
	require.NoError(t, err)
	tokenHash := hashToken(token)

	// Return a valid, non-expired, non-revoked session.
	mock.ExpectQuery("SELECT .+ FROM preview_access_sessions").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(previewAccessSessionTestCols).
				AddRow(newAccessSessionRow(sessID, orgID, userID, previewID, tokenHash, now.Add(5*time.Minute), nil, now)...),
		)

	// Expect UpdateAccessSessionActivity.
	mock.ExpectExec("UPDATE preview_access_sessions SET last_accessed_at").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	sess, err := mgr.ValidateBootstrapToken(context.Background(), orgID, token)
	require.NoError(t, err)
	require.Equal(t, sessID, sess.ID)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestValidateBootstrapToken_Expired(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	mgr := newTestManager(mock, &mockProvider{})

	orgID := uuid.New()
	userID := uuid.New()
	previewID := uuid.New()
	sessID := uuid.New()
	now := time.Now()
	token, err := generateToken()
	require.NoError(t, err)
	tokenHash := hashToken(token)

	// Return a session that expired in the past.
	mock.ExpectQuery("SELECT .+ FROM preview_access_sessions").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(previewAccessSessionTestCols).
				AddRow(newAccessSessionRow(sessID, orgID, userID, previewID, tokenHash, now.Add(-1*time.Minute), nil, now)...),
		)

	_, err = mgr.ValidateBootstrapToken(context.Background(), orgID, token)
	require.Error(t, err)
	require.Contains(t, err.Error(), "expired")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestValidateBootstrapToken_Revoked(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	mgr := newTestManager(mock, &mockProvider{})

	orgID := uuid.New()
	userID := uuid.New()
	previewID := uuid.New()
	sessID := uuid.New()
	now := time.Now()
	token, err := generateToken()
	require.NoError(t, err)
	tokenHash := hashToken(token)
	revokedAt := now.Add(-30 * time.Second)

	// Return a revoked session.
	mock.ExpectQuery("SELECT .+ FROM preview_access_sessions").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(previewAccessSessionTestCols).
				AddRow(newAccessSessionRow(sessID, orgID, userID, previewID, tokenHash, now.Add(5*time.Minute), &revokedAt, now)...),
		)

	_, err = mgr.ValidateBootstrapToken(context.Background(), orgID, token)
	require.Error(t, err)
	require.Contains(t, err.Error(), "revoked")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestValidateBootstrapToken_NotFound(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	mgr := newTestManager(mock, &mockProvider{})

	orgID := uuid.New()
	token, err := generateToken()
	require.NoError(t, err)

	// Return no rows.
	mock.ExpectQuery("SELECT .+ FROM preview_access_sessions").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(previewAccessSessionTestCols))

	_, err = mgr.ValidateBootstrapToken(context.Background(), orgID, token)
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid bootstrap token")
	require.NoError(t, mock.ExpectationsWereMet())
}

// =============================================================================
// ValidateBootstrapTokenUnscoped tests
// =============================================================================

func TestValidateBootstrapTokenUnscoped_Valid(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	mgr := newTestManager(mock, &mockProvider{})

	orgID := uuid.New()
	userID := uuid.New()
	previewID := uuid.New()
	sessID := uuid.New()
	now := time.Now()
	token, err := generateToken()
	require.NoError(t, err)
	tokenHash := hashToken(token)

	// Return a valid, non-expired, non-revoked session (unscoped query has 1 arg).
	mock.ExpectQuery("SELECT .+ FROM preview_access_sessions").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(previewAccessSessionTestCols).
				AddRow(newAccessSessionRow(sessID, orgID, userID, previewID, tokenHash, now.Add(5*time.Minute), nil, now)...),
		)

	// Expect UpdateAccessSessionActivity.
	mock.ExpectExec("UPDATE preview_access_sessions SET last_accessed_at").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	sess, err := mgr.ValidateBootstrapTokenUnscoped(context.Background(), token)
	require.NoError(t, err)
	require.Equal(t, sessID, sess.ID)
	require.Equal(t, orgID, sess.OrgID)
	require.Equal(t, previewID, sess.PreviewInstanceID)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestValidateBootstrapTokenUnscoped_Expired(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	mgr := newTestManager(mock, &mockProvider{})

	orgID := uuid.New()
	userID := uuid.New()
	previewID := uuid.New()
	sessID := uuid.New()
	now := time.Now()
	token, err := generateToken()
	require.NoError(t, err)
	tokenHash := hashToken(token)

	// Return an expired session.
	mock.ExpectQuery("SELECT .+ FROM preview_access_sessions").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(previewAccessSessionTestCols).
				AddRow(newAccessSessionRow(sessID, orgID, userID, previewID, tokenHash, now.Add(-1*time.Minute), nil, now)...),
		)

	_, err = mgr.ValidateBootstrapTokenUnscoped(context.Background(), token)
	require.Error(t, err)
	require.Contains(t, err.Error(), "expired")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestValidateBootstrapTokenUnscoped_Revoked(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	mgr := newTestManager(mock, &mockProvider{})

	orgID := uuid.New()
	userID := uuid.New()
	previewID := uuid.New()
	sessID := uuid.New()
	now := time.Now()
	token, err := generateToken()
	require.NoError(t, err)
	tokenHash := hashToken(token)
	revokedAt := now.Add(-30 * time.Second)

	mock.ExpectQuery("SELECT .+ FROM preview_access_sessions").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(previewAccessSessionTestCols).
				AddRow(newAccessSessionRow(sessID, orgID, userID, previewID, tokenHash, now.Add(5*time.Minute), &revokedAt, now)...),
		)

	_, err = mgr.ValidateBootstrapTokenUnscoped(context.Background(), token)
	require.Error(t, err)
	require.Contains(t, err.Error(), "revoked")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestValidateBootstrapTokenUnscoped_NotFound(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	mgr := newTestManager(mock, &mockProvider{})

	token, err := generateToken()
	require.NoError(t, err)

	mock.ExpectQuery("SELECT .+ FROM preview_access_sessions").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(previewAccessSessionTestCols))

	_, err = mgr.ValidateBootstrapTokenUnscoped(context.Background(), token)
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid bootstrap token")
	require.NoError(t, mock.ExpectationsWereMet())
}

// =============================================================================
// SetLifetime tests
// =============================================================================

func TestSetLifetime_Success(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		duration time.Duration
	}{
		{name: "keeps preview for thirty minutes", duration: 30 * time.Minute},
		{name: "shortens preview to five minutes", duration: 5 * time.Minute},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "pgxmock pool should be created")
			defer mock.Close()

			mgr := newTestManager(mock, &mockProvider{})

			orgID := uuid.New()
			previewID := uuid.New()
			sessionID := uuid.New()
			userID := uuid.New()
			now := time.Now()

			mock.ExpectQuery("SELECT .+ FROM preview_instances WHERE id").
				WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
				WillReturnRows(
					pgxmock.NewRows(previewInstanceTestCols).
						AddRow(newPreviewInstanceRow(previewID, sessionID, orgID, userID, models.PreviewStatusReady, "handle-abc", now)...),
				)

			mock.ExpectExec("UPDATE preview_instances SET expires_at").
				WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
				WillReturnResult(pgxmock.NewResult("UPDATE", 1))

			expiresAt, err := mgr.SetLifetime(context.Background(), orgID, previewID, tt.duration)
			require.NoError(t, err, "SetLifetime should accept bounded durations")
			require.True(t, expiresAt.After(time.Now()), "SetLifetime should return a future expiry")
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

func TestSetLifetime_RejectsDurationAboveDefaultHardTTL(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should be created")
	defer mock.Close()

	mgr := newTestManager(mock, &mockProvider{})

	_, err = mgr.SetLifetime(context.Background(), uuid.New(), uuid.New(), DefaultHardTTL+time.Second)
	require.Error(t, err, "SetLifetime should reject durations above the per-adjustment limit")
	require.Contains(t, err.Error(), "cannot exceed", "SetLifetime error should explain the per-adjustment cap")
	require.NoError(t, mock.ExpectationsWereMet(), "SetLifetime should reject before querying the database")
}

func TestSetLifetime_RejectsDurationBelowMinLifetimeTTL(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should be created")
	defer mock.Close()

	mgr := newTestManager(mock, &mockProvider{})

	_, err = mgr.SetLifetime(context.Background(), uuid.New(), uuid.New(), MinLifetimeTTL-time.Second)
	require.Error(t, err, "SetLifetime should reject durations below the minimum")
	require.Contains(t, err.Error(), "at least", "SetLifetime error should explain the minimum duration")
	require.NoError(t, mock.ExpectationsWereMet(), "SetLifetime should reject before querying the database")
}

// =============================================================================
// RecordAccess tests
// =============================================================================

func TestRecordAccess_Success(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	mgr := newTestManager(mock, &mockProvider{})

	orgID := uuid.New()
	previewID := uuid.New()

	mock.ExpectExec("UPDATE preview_instances SET last_accessed_at").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	err = mgr.RecordAccess(context.Background(), orgID, previewID)
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

// =============================================================================
// RecordLastPath tests
// =============================================================================

func TestRecordLastPath_Success(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	mgr := newTestManager(mock, &mockProvider{})

	orgID := uuid.New()
	previewID := uuid.New()

	mock.ExpectExec("UPDATE preview_instances SET last_path").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	err = mgr.RecordLastPath(context.Background(), orgID, previewID, "/dashboard")
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

// =============================================================================
// DialPreview tests
// =============================================================================

func TestDialPreview_NotActive(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	mgr := newTestManager(mock, &mockProvider{})

	orgID := uuid.New()
	previewID := uuid.New()
	sessionID := uuid.New()
	userID := uuid.New()
	now := time.Now()

	// Return a stopped instance.
	mock.ExpectQuery("SELECT .+ FROM preview_instances WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(previewInstanceTestCols).
				AddRow(newPreviewInstanceRow(previewID, sessionID, orgID, userID, models.PreviewStatusStopped, "handle-abc", now)...),
		)

	_, err = mgr.DialPreview(context.Background(), orgID, previewID)
	require.Error(t, err)
	require.Contains(t, err.Error(), "not active")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestDialPreview_Success(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	stream := &mockStream{}
	mgr := newTestManager(mock, &mockProvider{dialStream: stream})

	orgID := uuid.New()
	previewID := uuid.New()
	sessionID := uuid.New()
	userID := uuid.New()
	now := time.Now()

	mock.ExpectQuery("SELECT .+ FROM preview_instances WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(previewInstanceTestCols).
				AddRow(newPreviewInstanceRow(previewID, sessionID, orgID, userID, models.PreviewStatusReady, "handle-abc", now)...),
		)

	result, err := mgr.DialPreview(context.Background(), orgID, previewID)
	require.NoError(t, err)
	require.Equal(t, stream, result)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestDialPreview_InstanceNotFound(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	mgr := newTestManager(mock, &mockProvider{})

	orgID := uuid.New()
	previewID := uuid.New()

	mock.ExpectQuery("SELECT .+ FROM preview_instances WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(previewInstanceTestCols))

	_, err = mgr.DialPreview(context.Background(), orgID, previewID)
	require.Error(t, err)
	require.Contains(t, err.Error(), "get preview instance")
	require.NoError(t, mock.ExpectationsWereMet())
}

// =============================================================================
// Inspector / SetInspector tests
// =============================================================================

func TestInspector_NilByDefault(t *testing.T) {
	t.Parallel()

	mgr := NewManager(ManagerConfig{})
	require.Nil(t, mgr.Inspector())
}

// =============================================================================
// platformEnv tests
// =============================================================================

func TestPlatformEnv_SubstitutesPreviewIDIntoTemplate(t *testing.T) {
	t.Parallel()

	mgr := NewManager(ManagerConfig{
		PreviewOriginTemplate: "http://{id}.preview.localhost:9090",
		Logger:                zerolog.Nop(),
	})

	id := uuid.New()
	env := mgr.platformEnv(id)

	require.Equal(t, map[string]string{
		"PREVIEW_ORIGIN": "http://" + id.String() + ".preview.localhost:9090",
	}, env)
}

func TestPlatformEnv_EmptyTemplateReturnsNil(t *testing.T) {
	t.Parallel()

	mgr := NewManager(ManagerConfig{Logger: zerolog.Nop()})
	require.Nil(t, mgr.platformEnv(uuid.New()), "empty template must skip injection so user-declared env is untouched")
}

func TestPlatformEnv_ReplacesEveryOccurrenceOfPlaceholder(t *testing.T) {
	t.Parallel()

	// Guard against anyone swapping strings.ReplaceAll for strings.Replace with
	// n=1; duplicate {id} in the template (unusual but valid) must all resolve.
	mgr := NewManager(ManagerConfig{
		PreviewOriginTemplate: "{id}-{id}",
		Logger:                zerolog.Nop(),
	})

	id := uuid.New()
	env := mgr.platformEnv(id)

	require.Equal(t, id.String()+"-"+id.String(), env["PREVIEW_ORIGIN"])
}

func TestSetInspector(t *testing.T) {
	t.Parallel()

	mgr := NewManager(ManagerConfig{})
	require.Nil(t, mgr.Inspector())

	insp := &mockInspector{}
	mgr.SetInspector(insp)
	require.NotNil(t, mgr.Inspector())
	require.Equal(t, insp, mgr.Inspector())
}

// =============================================================================
// checkConcurrencyCaps tests
// =============================================================================

func TestCheckConcurrencyCaps_UnderLimits(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	mgr := newTestManager(mock, &mockProvider{})

	orgID := uuid.New()
	userID := uuid.New()

	// User count: 0 (under limit of 2).
	mock.ExpectQuery("SELECT COUNT").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(0))

	// Org count: 1 (under limit of 5).
	mock.ExpectQuery("SELECT COUNT").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(1))

	// Worker count: 0 (under limit of 3).
	mock.ExpectQuery("SELECT COUNT").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(0))

	err = mgr.checkConcurrencyCaps(context.Background(), orgID, userID)
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestCheckConcurrencyCaps_UsesOrgPreviewLimit(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should be created")
	defer mock.Close()

	store := db.NewPreviewStore(mock)
	mgr := NewManager(ManagerConfig{
		Store: store,
		OrgSettingsStore: staticOrgSettingsStore{
			settings: json.RawMessage(`{"preview_max_previews_per_user":4}`),
		},
		Provider:     &mockProvider{},
		Logger:       zerolog.Nop(),
		WorkerNodeID: "worker-1",
		MaxPerUser:   2,
	})

	orgID := uuid.New()
	userID := uuid.New()

	mock.ExpectQuery("SELECT COUNT").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(3))
	mock.ExpectQuery("SELECT COUNT").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectQuery("SELECT COUNT").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(0))

	err = mgr.checkConcurrencyCaps(context.Background(), orgID, userID)
	require.NoError(t, err, "org settings should raise the effective per-user preview limit")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestCheckConcurrencyCaps_FallsBackToConfiguredPreviewLimitWhenOrgSettingAbsent(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should be created")
	defer mock.Close()

	store := db.NewPreviewStore(mock)
	mgr := NewManager(ManagerConfig{
		Store: store,
		OrgSettingsStore: staticOrgSettingsStore{
			settings: json.RawMessage(`{}`),
		},
		Provider:     &mockProvider{},
		Logger:       zerolog.Nop(),
		WorkerNodeID: "worker-1",
		MaxPerUser:   6,
	})

	orgID := uuid.New()
	userID := uuid.New()

	mock.ExpectQuery("SELECT COUNT").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(5))
	mock.ExpectQuery("SELECT COUNT").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(1))
	mock.ExpectQuery("SELECT COUNT").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(0))

	err = mgr.checkConcurrencyCaps(context.Background(), orgID, userID)
	require.NoError(t, err, "missing org setting should preserve configured per-user preview fallback")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestCheckConcurrencyCaps_UserExceeded(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	mgr := newTestManager(mock, &mockProvider{})

	orgID := uuid.New()
	userID := uuid.New()

	// User count: 4 (at the default per-user limit).
	mock.ExpectQuery("SELECT COUNT").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(4))

	err = mgr.checkConcurrencyCaps(context.Background(), orgID, userID)
	require.Error(t, err)
	require.ErrorIs(t, err, ErrPreviewCapacity)
	require.Contains(t, err.Error(), "per-user preview limit")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestCheckConcurrencyCaps_UserExceededReturnsClearPerUserMessage(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should be created")
	defer mock.Close()

	store := db.NewPreviewStore(mock)
	mgr := NewManager(ManagerConfig{
		Store: store,
		OrgSettingsStore: staticOrgSettingsStore{
			settings: json.RawMessage(`{"preview_max_previews_per_user":4}`),
		},
		Provider:     &mockProvider{},
		Logger:       zerolog.Nop(),
		WorkerNodeID: "worker-1",
	})

	orgID := uuid.New()
	userID := uuid.New()

	mock.ExpectQuery("SELECT COUNT").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(4))

	err = mgr.checkConcurrencyCaps(context.Background(), orgID, userID)
	require.Error(t, err, "at-limit preview count should fail")
	require.ErrorIs(t, err, ErrPreviewCapacity, "capacity errors should wrap the sentinel")

	var capacityErr *CapacityError
	require.True(t, errors.As(err, &capacityErr), "per-user capacity errors should expose structured capacity details")
	require.Equal(t, CapacityScopeUser, capacityErr.Scope, "capacity scope should identify the per-user limit")
	require.Equal(t, "You have reached your per-user preview limit: 4 active previews out of 4 allowed. Stop one of your previews or ask an admin to raise the per-user preview limit in General settings.", capacityErr.UserMessage(), "capacity message should explain the per-user limit")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestCheckConcurrencyCaps_OrgExceeded(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	mgr := newTestManager(mock, &mockProvider{})

	orgID := uuid.New()
	userID := uuid.New()

	// User count: 1 (under limit).
	mock.ExpectQuery("SELECT COUNT").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(1))

	// Org count: 5 (at limit of 5).
	mock.ExpectQuery("SELECT COUNT").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(5))

	err = mgr.checkConcurrencyCaps(context.Background(), orgID, userID)
	require.Error(t, err)
	require.ErrorIs(t, err, ErrPreviewCapacity)
	require.Contains(t, err.Error(), "your team has")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestCheckConcurrencyCaps_WorkerExceeded(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	mgr := newTestManager(mock, &mockProvider{})

	orgID := uuid.New()
	userID := uuid.New()

	// User count: 0.
	mock.ExpectQuery("SELECT COUNT").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(0))

	// Org count: 1.
	mock.ExpectQuery("SELECT COUNT").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(1))

	// Worker count: 3 (at limit of 3).
	mock.ExpectQuery("SELECT COUNT").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(3))

	err = mgr.checkConcurrencyCaps(context.Background(), orgID, userID)
	require.Error(t, err)
	require.ErrorIs(t, err, ErrPreviewCapacity)
	require.Contains(t, err.Error(), "all preview slots are in use")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestRecyclePreview_PreservesPartiallyReadyStatus(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should be created")
	defer mock.Close()

	orgID := uuid.New()
	previewID := uuid.New()
	sessionID := uuid.New()
	userID := uuid.New()
	now := time.Now()

	mock.ExpectQuery("SELECT .+ FROM preview_instances WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(previewInstanceTestCols).
				AddRow(newPreviewInstanceRow(previewID, sessionID, orgID, userID, models.PreviewStatusReady, "handle-old", now)...),
		)
	// Atomic conditional status transition (UpdatePreviewStatusIfActive).
	mock.ExpectExec("UPDATE preview_instances SET status = @status.+NOT IN").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	// RevokeAllForPreview during recycle.
	mock.ExpectExec("UPDATE preview_access_sessions SET revoked_at").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 0))
	mock.ExpectExec("UPDATE preview_instances SET preview_handle = @handle, port = @port").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectExec("UPDATE preview_instances SET status = @status").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectExec("UPDATE preview_instances SET expires_at = @expires_at").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	mgr := newTestManager(mock, &mockProvider{
		startHandle: &PreviewHandle{
			Handle:         "handle-new",
			PrimaryPort:    3001,
			PartiallyReady: true,
		},
	})

	err = mgr.RecyclePreview(context.Background(), orgID, previewID)
	require.NoError(t, err, "RecyclePreview should succeed for a partially ready restart")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestRecyclePreview_ReconstructsInputFromStoredState(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should be created")
	defer mock.Close()

	orgID := uuid.New()
	previewID := uuid.New()
	sessionID := uuid.New()
	userID := uuid.New()
	now := time.Now()

	mock.ExpectQuery("SELECT .+ FROM preview_instances WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(previewInstanceTestCols).
				AddRow(newPreviewInstanceRow(previewID, sessionID, orgID, userID, models.PreviewStatusReady, "handle-old", now)...),
		)
	// Atomic conditional status transition (UpdatePreviewStatusIfActive).
	mock.ExpectExec("UPDATE preview_instances SET status = @status.+NOT IN").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	// RevokeAllForPreview during recycle.
	mock.ExpectExec("UPDATE preview_access_sessions SET revoked_at").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 0))
	mock.ExpectExec("UPDATE preview_instances SET preview_handle = @handle, port = @port").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectExec("UPDATE preview_instances SET status = @status").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectExec("UPDATE preview_instances SET expires_at = @expires_at").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	provider := &mockProvider{
		startHandle: &PreviewHandle{
			Handle:      "handle-new",
			PrimaryPort: 3001,
		},
	}
	mgr := newTestManager(mock, provider)

	err = mgr.RecyclePreview(context.Background(), orgID, previewID)
	require.NoError(t, err, "RecyclePreview should rebuild restart inputs from stored preview state")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestRecyclePreview_FallsBackForLegacyPreviewsWithoutStoredRecycleState(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should be created")
	defer mock.Close()

	orgID := uuid.New()
	previewID := uuid.New()
	sessionID := uuid.New()
	userID := uuid.New()
	now := time.Now()
	sandboxID := "sandbox-legacy"
	pid := 1234

	legacyRow := newPreviewInstanceRow(previewID, sessionID, orgID, userID, models.PreviewStatusReady, "handle-old", now)
	legacyRow[21] = nil
	legacyRow[22] = nil

	mock.ExpectQuery("SELECT .+ FROM preview_instances WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(previewInstanceTestCols).
				AddRow(legacyRow...),
		)
	mock.ExpectQuery("SELECT .+ FROM sessions").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(sessionTestCols).
				AddRow(newSessionRow(sessionID, orgID, &sandboxID, now)...),
		)
	mock.ExpectQuery("SELECT .+ FROM preview_services ps .+preview_instance_id = @pid").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(previewServiceTestCols).
				AddRow(uuid.New(), previewID, "web", string(models.PreviewServiceRolePrimary), string(models.PreviewServiceStatusReady), []string{"npm", "run", "dev"}, "/workspace", 3000, &pid, "", now),
		)
	mock.ExpectQuery("SELECT .+ FROM preview_infrastructure pi2 .+preview_instance_id = @pid").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(previewInfraTestCols))
	mock.ExpectExec("UPDATE preview_instances SET status = @status.+NOT IN").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectExec("UPDATE preview_access_sessions SET revoked_at").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 0))
	mock.ExpectExec("UPDATE preview_instances SET preview_handle = @handle, port = @port").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectExec("UPDATE preview_instances SET status = @status").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectExec("UPDATE preview_instances SET expires_at = @expires_at").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	provider := &mockProvider{
		startHandle: &PreviewHandle{
			Handle:      "handle-new",
			PrimaryPort: 3001,
		},
	}
	mgr := NewManager(ManagerConfig{
		Store:        db.NewPreviewStore(mock),
		SessionStore: db.NewSessionStore(mock),
		Provider:     provider,
		Logger:       zerolog.Nop(),
		WorkerNodeID: "worker-1",
	})

	err = mgr.RecyclePreview(context.Background(), orgID, previewID)
	require.NoError(t, err, "RecyclePreview should rebuild legacy restart inputs when recycle state is missing")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

// =============================================================================
// StopActivePreviewForSession tests
// =============================================================================

func TestStopActivePreviewForSession_NoPreview(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	mgr := newTestManager(mock, &mockProvider{})

	orgID := uuid.New()
	sessionID := uuid.New()

	// GetActivePreviewForSession returns no rows — treated as "no preview".
	mock.ExpectQuery("SELECT .+ FROM preview_instances").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(previewInstanceTestCols))

	stopped, err := mgr.StopActivePreviewForSession(context.Background(), orgID, sessionID)
	require.NoError(t, err)
	require.False(t, stopped, "no active preview must yield stopped=false")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestStopActivePreviewForSession_StopsActivePreview(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	provider := &mockProvider{}
	sandboxProvider := testutil.NewMockSandboxProvider()
	sessionID := uuid.New()
	orgID := uuid.New()
	userID := uuid.New()
	previewID := uuid.New()
	now := time.Now()

	mgr := NewManager(ManagerConfig{
		Store:           db.NewPreviewStore(mock),
		SessionStore:    db.NewSessionStore(mock),
		Provider:        provider,
		SandboxProvider: sandboxProvider,
		Logger:          zerolog.Nop(),
		WorkerNodeID:    "worker-1",
	})

	// 1. GetActivePreviewForSession returns one active preview.
	mock.ExpectQuery("SELECT .+ FROM preview_instances").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(previewInstanceTestCols).
				AddRow(newPreviewInstanceRow(previewID, sessionID, orgID, userID, models.PreviewStatusReady, "handle-abc", now)...),
		)

	// 2. StopPreview → GetPreviewInstance.
	mock.ExpectQuery("SELECT .+ FROM preview_instances WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(previewInstanceTestCols).
				AddRow(newPreviewInstanceRow(previewID, sessionID, orgID, userID, models.PreviewStatusReady, "handle-abc", now)...),
		)

	// 3. StopPreviewWithRevocation.
	mock.ExpectBegin()
	mock.ExpectExec("UPDATE preview_instances SET status").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectExec("UPDATE preview_services SET").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 0))
	mock.ExpectExec("UPDATE preview_infrastructure SET").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 0))
	mock.ExpectExec("UPDATE preview_access_sessions SET revoked_at").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 0))
	mock.ExpectCommit()

	// 4. ReleasePreviewHold: no container to destroy, so destroyNow=false.
	mock.ExpectQuery("WITH released AS").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{"session_id", "container_id", "turn_holds"}).
				AddRow(sessionID, "", false),
		)

	stopped, err := mgr.StopActivePreviewForSession(context.Background(), orgID, sessionID)
	require.NoError(t, err)
	require.True(t, stopped, "an active preview must report stopped=true")
	require.Equal(t, 0, sandboxProvider.GetDestroyCalls(), "no container means nothing to destroy")
	require.NoError(t, mock.ExpectationsWereMet())
}

// =============================================================================
// StopPreview hold-aware destroy tests
// =============================================================================

func TestStopPreview_DestroysSandboxWhenTurnDoesNotHold(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	provider := &mockProvider{}
	sandboxProvider := testutil.NewMockSandboxProvider()

	mgr := NewManager(ManagerConfig{
		Store:           db.NewPreviewStore(mock),
		SessionStore:    db.NewSessionStore(mock),
		Provider:        provider,
		SandboxProvider: sandboxProvider,
		Logger:          zerolog.Nop(),
		WorkerNodeID:    "worker-1",
	})

	orgID := uuid.New()
	previewID := uuid.New()
	sessionID := uuid.New()
	userID := uuid.New()
	now := time.Now()

	mock.ExpectQuery("SELECT .+ FROM preview_instances WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(previewInstanceTestCols).
				AddRow(newPreviewInstanceRow(previewID, sessionID, orgID, userID, models.PreviewStatusReady, "handle-abc", now)...),
		)

	mock.ExpectBegin()
	mock.ExpectExec("UPDATE preview_instances SET status").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectExec("UPDATE preview_services SET").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 0))
	mock.ExpectExec("UPDATE preview_infrastructure SET").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 0))
	mock.ExpectExec("UPDATE preview_access_sessions SET revoked_at").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 0))
	mock.ExpectCommit()

	// ReleasePreviewHold: container-1 is live, turn does NOT hold, so destroy.
	mock.ExpectQuery("WITH released AS").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{"session_id", "container_id", "turn_holds"}).
				AddRow(sessionID, "container-1", false),
		)

	// FinalizeContainerDestroy CAS: clears container_id and derives
	// sandbox_state from snapshot_key atomically. Matches one row since no new
	// holder is present.
	mock.ExpectExec("UPDATE sessions\\s+SET container_id = NULL,\\s+worker_node_id = NULL,\\s+sandbox_state = CASE").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	err = mgr.StopPreview(context.Background(), orgID, previewID)
	require.NoError(t, err)
	require.Equal(t, 1, sandboxProvider.GetDestroyCalls(), "final hold release must destroy the container")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestStopPreview_LeavesSandboxWhenNewHolderAcquiredAfterRelease(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	provider := &mockProvider{}
	sandboxProvider := testutil.NewMockSandboxProvider()

	mgr := NewManager(ManagerConfig{
		Store:           db.NewPreviewStore(mock),
		SessionStore:    db.NewSessionStore(mock),
		Provider:        provider,
		SandboxProvider: sandboxProvider,
		Logger:          zerolog.Nop(),
		WorkerNodeID:    "worker-1",
	})

	orgID := uuid.New()
	previewID := uuid.New()
	sessionID := uuid.New()
	userID := uuid.New()
	now := time.Now()

	mock.ExpectQuery("SELECT .+ FROM preview_instances WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(previewInstanceTestCols).
				AddRow(newPreviewInstanceRow(previewID, sessionID, orgID, userID, models.PreviewStatusReady, "handle-abc", now)...),
		)

	mock.ExpectBegin()
	mock.ExpectExec("UPDATE preview_instances SET status").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectExec("UPDATE preview_services SET").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 0))
	mock.ExpectExec("UPDATE preview_infrastructure SET").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 0))
	mock.ExpectExec("UPDATE preview_access_sessions SET revoked_at").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 0))
	mock.ExpectCommit()

	// ReleasePreviewHold reports destroyNow=true based on its snapshot...
	mock.ExpectQuery("WITH released AS").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{"session_id", "container_id", "turn_holds"}).
				AddRow(sessionID, "container-1", false),
		)
	// ...but the FinalizeContainerDestroy CAS matches zero rows because a new
	// holder acquired in the gap. We must NOT destroy the container.
	mock.ExpectExec("UPDATE sessions\\s+SET container_id = NULL,\\s+worker_node_id = NULL").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), "container-1").
		WillReturnResult(pgxmock.NewResult("UPDATE", 0))

	err = mgr.StopPreview(context.Background(), orgID, previewID)
	require.NoError(t, err)
	require.Equal(t, 0, sandboxProvider.GetDestroyCalls(), "must not destroy when a new holder acquired in the release gap")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestStopPreview_LeavesSandboxWhenTurnStillHolds(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	provider := &mockProvider{}
	sandboxProvider := testutil.NewMockSandboxProvider()

	mgr := NewManager(ManagerConfig{
		Store:           db.NewPreviewStore(mock),
		SessionStore:    db.NewSessionStore(mock),
		Provider:        provider,
		SandboxProvider: sandboxProvider,
		Logger:          zerolog.Nop(),
		WorkerNodeID:    "worker-1",
	})

	orgID := uuid.New()
	previewID := uuid.New()
	sessionID := uuid.New()
	userID := uuid.New()
	now := time.Now()

	mock.ExpectQuery("SELECT .+ FROM preview_instances WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(previewInstanceTestCols).
				AddRow(newPreviewInstanceRow(previewID, sessionID, orgID, userID, models.PreviewStatusReady, "handle-abc", now)...),
		)

	mock.ExpectBegin()
	mock.ExpectExec("UPDATE preview_instances SET status").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectExec("UPDATE preview_services SET").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 0))
	mock.ExpectExec("UPDATE preview_infrastructure SET").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 0))
	mock.ExpectExec("UPDATE preview_access_sessions SET revoked_at").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 0))
	mock.ExpectCommit()

	// ReleasePreviewHold: turn still holds → do NOT destroy.
	mock.ExpectQuery("WITH released AS").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{"session_id", "container_id", "turn_holds"}).
				AddRow(sessionID, "container-1", true),
		)

	err = mgr.StopPreview(context.Background(), orgID, previewID)
	require.NoError(t, err)
	require.Equal(t, 0, sandboxProvider.GetDestroyCalls(), "turn still holds the container; must not destroy")
	require.NoError(t, mock.ExpectationsWereMet())
}

// TestStopPreview_FinalizeErrorLeavesContainerForReconciler covers the
// FinalizeContainerDestroy error branch: on a DB failure we must skip Destroy
// so the reconciler can revisit the container on next startup rather than
// risk ripping it out from under a still-attached holder.
func TestStopPreview_FinalizeErrorLeavesContainerForReconciler(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	provider := &mockProvider{}
	sandboxProvider := testutil.NewMockSandboxProvider()

	mgr := NewManager(ManagerConfig{
		Store:           db.NewPreviewStore(mock),
		SessionStore:    db.NewSessionStore(mock),
		Provider:        provider,
		SandboxProvider: sandboxProvider,
		Logger:          zerolog.Nop(),
		WorkerNodeID:    "worker-1",
	})

	orgID := uuid.New()
	previewID := uuid.New()
	sessionID := uuid.New()
	userID := uuid.New()
	now := time.Now()

	mock.ExpectQuery("SELECT .+ FROM preview_instances WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(previewInstanceTestCols).
				AddRow(newPreviewInstanceRow(previewID, sessionID, orgID, userID, models.PreviewStatusReady, "handle-abc", now)...),
		)

	mock.ExpectBegin()
	mock.ExpectExec("UPDATE preview_instances SET status").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectExec("UPDATE preview_services SET").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 0))
	mock.ExpectExec("UPDATE preview_infrastructure SET").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 0))
	mock.ExpectExec("UPDATE preview_access_sessions SET revoked_at").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 0))
	mock.ExpectCommit()

	mock.ExpectQuery("WITH released AS").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{"session_id", "container_id", "turn_holds"}).
				AddRow(sessionID, "container-1", false),
		)
	mock.ExpectExec("UPDATE sessions\\s+SET container_id = NULL").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnError(fmt.Errorf("db down"))

	err = mgr.StopPreview(context.Background(), orgID, previewID)
	require.NoError(t, err)
	require.Equal(t, 0, sandboxProvider.GetDestroyCalls(), "finalize error must skip destroy so the reconciler can revisit")
	require.NoError(t, mock.ExpectationsWereMet())
}

// =============================================================================
// ReservePreview / LaunchPreview / AbortReservation / StartPreview tests
// =============================================================================

// validPreviewConfig returns a PreviewConfig that passes ValidateConfig.
func validPreviewConfig() *models.PreviewConfig {
	return &models.PreviewConfig{
		Version: "3",
		Name:    "my-preview",
		Primary: "web",
		Services: map[string]models.ServiceConfig{
			"web": {
				Command: []string{"npm", "run", "dev"},
				Port:    3000,
				Ready:   models.ReadinessProbe{HTTPPath: "/"},
			},
		},
		Credentials: models.CredentialConfig{Mode: "none"},
		Network:     models.NetworkConfig{Mode: "managed"},
	}
}

// expectCreatePreviewInstance mirrors PreviewStore.CreatePreviewInstance: it
// inserts a row and returns the row back. Tests inject a known preview ID via
// the returned row, and the caller reads p.ID from the model after the call.
func expectCreatePreviewInstance(mock pgxmock.PgxPoolIface, previewID, sessionID, orgID, userID uuid.UUID, status models.PreviewStatus, now time.Time) {
	mock.ExpectQuery("INSERT INTO preview_instances").
		WithArgs(previewAnyArgs(22)...).
		WillReturnRows(
			pgxmock.NewRows(previewInstanceTestCols).
				AddRow(newPreviewInstanceRow(previewID, sessionID, orgID, userID, status, "", now)...),
		)
}

func expectUpdatePreviewStatusFailed(mock pgxmock.PgxPoolIface) {
	mock.ExpectBegin()
	mock.ExpectExec("UPDATE preview_instances SET status").
		WithArgs(previewAnyArgs(5)...).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectExec("UPDATE preview_services SET").
		WithArgs(previewAnyArgs(5)...).
		WillReturnResult(pgxmock.NewResult("UPDATE", 0))
	mock.ExpectExec("UPDATE preview_infrastructure SET").
		WithArgs(previewAnyArgs(5)...).
		WillReturnResult(pgxmock.NewResult("UPDATE", 0))
	mock.ExpectCommit()
}

// previewAnyArgs matches the helper style in the db package tests.
func previewAnyArgs(n int) []any {
	args := make([]any, n)
	for i := range args {
		args[i] = pgxmock.AnyArg()
	}
	return args
}

func TestReservePreview_NilProviderErrors(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	mgr := NewManager(ManagerConfig{
		Store:        db.NewPreviewStore(mock),
		Provider:     nil,
		Logger:       zerolog.Nop(),
		WorkerNodeID: "worker-1",
	})

	_, err = mgr.ReservePreview(context.Background(), StartPreviewInput{
		SessionID: uuid.New(),
		OrgID:     uuid.New(),
		UserID:    uuid.New(),
		Config:    validPreviewConfig(),
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "provider is not configured")
}

func TestReservePreview_ExistingActivePreview(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	mgr := newTestManager(mock, &mockProvider{})

	orgID := uuid.New()
	userID := uuid.New()
	sessionID := uuid.New()
	existingID := uuid.New()
	now := time.Now()

	mock.ExpectQuery("SELECT .+ FROM preview_instances").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(previewInstanceTestCols).
				AddRow(newPreviewInstanceRow(existingID, sessionID, orgID, userID, models.PreviewStatusReady, "handle-abc", now)...),
		)

	_, err = mgr.ReservePreview(context.Background(), StartPreviewInput{
		SessionID: sessionID,
		OrgID:     orgID,
		UserID:    userID,
		Config:    validPreviewConfig(),
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "already has an active preview")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestReservePreview_Success(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	mgr := newTestManager(mock, &mockProvider{})

	orgID := uuid.New()
	userID := uuid.New()
	sessionID := uuid.New()
	previewID := uuid.New()
	now := time.Now()

	// GetActivePreviewForSession: no rows.
	mock.ExpectQuery("SELECT .+ FROM preview_instances").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(previewInstanceTestCols))

	// Capacity checks (3 counts).
	mock.ExpectQuery("SELECT COUNT").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectQuery("SELECT COUNT").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectQuery("SELECT COUNT").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(0))

	expectCreatePreviewInstance(mock, previewID, sessionID, orgID, userID, models.PreviewStatusStarting, now)

	mock.ExpectQuery(`UPDATE preview_instances\s+SET preview_holding_container = TRUE`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"session_id"}).AddRow(sessionID))

	instance, err := mgr.ReservePreview(context.Background(), StartPreviewInput{
		SessionID: sessionID,
		OrgID:     orgID,
		UserID:    userID,
		Config:    validPreviewConfig(),
	})
	require.NoError(t, err)
	require.NotNil(t, instance)
	require.Equal(t, previewID, instance.ID)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestReservePreview_InvalidConfig(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	mgr := newTestManager(mock, &mockProvider{})

	bad := &models.PreviewConfig{
		Version:  "3",
		Name:     "missing-primary",
		Services: map[string]models.ServiceConfig{},
	}

	_, err = mgr.ReservePreview(context.Background(), StartPreviewInput{
		SessionID: uuid.New(),
		OrgID:     uuid.New(),
		UserID:    uuid.New(),
		Config:    bad,
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid preview config")
}

func TestReservePreview_HoldErrorMarksFailed(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	mgr := newTestManager(mock, &mockProvider{})

	orgID := uuid.New()
	userID := uuid.New()
	sessionID := uuid.New()
	previewID := uuid.New()
	now := time.Now()

	mock.ExpectQuery("SELECT .+ FROM preview_instances").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(previewInstanceTestCols))
	mock.ExpectQuery("SELECT COUNT").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectQuery("SELECT COUNT").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectQuery("SELECT COUNT").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(0))

	expectCreatePreviewInstance(mock, previewID, sessionID, orgID, userID, models.PreviewStatusStarting, now)

	// AcquirePreviewHold fails on both retry attempts.
	mock.ExpectQuery(`UPDATE preview_instances\s+SET preview_holding_container = TRUE`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnError(fmt.Errorf("boom"))
	mock.ExpectQuery(`UPDATE preview_instances\s+SET preview_holding_container = TRUE`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnError(fmt.Errorf("boom"))

	// UpdatePreviewStatus(failed).
	expectUpdatePreviewStatusFailed(mock)

	_, err = mgr.ReservePreview(context.Background(), StartPreviewInput{
		SessionID: sessionID,
		OrgID:     orgID,
		UserID:    userID,
		Config:    validPreviewConfig(),
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "acquire preview hold")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestLaunchPreview_NilProviderErrors(t *testing.T) {
	t.Parallel()

	mgr := NewManager(ManagerConfig{
		Provider:     nil,
		Logger:       zerolog.Nop(),
		WorkerNodeID: "worker-1",
	})
	_, err := mgr.LaunchPreview(context.Background(), &models.PreviewInstance{}, StartPreviewInput{Config: validPreviewConfig()})
	require.Error(t, err)
	require.Contains(t, err.Error(), "provider is not configured")
}

func TestLaunchPreview_NilSandboxErrors(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	mgr := newTestManager(mock, &mockProvider{})

	_, err = mgr.LaunchPreview(context.Background(), &models.PreviewInstance{}, StartPreviewInput{
		Config:  validPreviewConfig(),
		Sandbox: nil,
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "sandbox must not be nil")
}

func TestLaunchPreview_InvalidConfig(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	mgr := newTestManager(mock, &mockProvider{})

	_, err = mgr.LaunchPreview(context.Background(), &models.PreviewInstance{}, StartPreviewInput{
		Config:  &models.PreviewConfig{Name: "bad"},
		Sandbox: &agent.Sandbox{ID: "s-1", Provider: "docker"},
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid preview config")
}

func TestLaunchPreview_Success(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	provider := &mockProvider{
		startHandle: &PreviewHandle{
			Handle:      "handle-new",
			PrimaryPort: 3000,
		},
		statusSnap: &PreviewStatusSnapshot{},
	}
	mgr := newTestManager(mock, provider)

	orgID := uuid.New()
	previewID := uuid.New()
	sessionID := uuid.New()

	instance := &models.PreviewInstance{
		ID:        previewID,
		OrgID:     orgID,
		SessionID: sessionID,
		Status:    models.PreviewStatusStarting,
		// ConfigDigest is empty and RecycleSandbox is nil so needsUpdate=true.
	}

	// UpdatePreviewReservationConfig.
	mock.ExpectExec(`UPDATE preview_instances\s+SET name = @name`).
		WithArgs(previewAnyArgs(10)...).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	// CreatePreviewService for web.
	svcID := uuid.New()
	mock.ExpectQuery("INSERT INTO preview_services").
		WithArgs(previewAnyArgs(7)...).
		WillReturnRows(
			pgxmock.NewRows(previewServiceTestCols).
				AddRow(svcID, previewID, "web", "primary", "starting", []string{"npm", "run", "dev"}, "", 3000, (*int)(nil), "", time.Now()),
		)

	// UpdatePreviewHandle.
	mock.ExpectExec("UPDATE preview_instances SET preview_handle = @handle, port = @port").
		WithArgs(previewAnyArgs(4)...).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	// UpdatePreviewStatusIfActive → ready.
	mock.ExpectExec("UPDATE preview_instances SET status = @status.+NOT IN").
		WithArgs(previewAnyArgs(5)...).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	launched, err := mgr.LaunchPreview(context.Background(), instance, StartPreviewInput{
		SessionID: sessionID,
		OrgID:     orgID,
		UserID:    uuid.New(),
		Sandbox:   &agent.Sandbox{ID: "s-1", Provider: "docker"},
		Config:    validPreviewConfig(),
	})
	require.NoError(t, err)
	require.Equal(t, models.PreviewStatusReady, launched.Status)
	require.Equal(t, "handle-new", launched.PreviewHandle)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestLaunchPreview_ReservationNoLongerPending(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	mgr := newTestManager(mock, &mockProvider{})

	instance := &models.PreviewInstance{
		ID:        uuid.New(),
		OrgID:     uuid.New(),
		SessionID: uuid.New(),
		Status:    models.PreviewStatusStarting,
	}

	// UpdatePreviewReservationConfig: rows affected = 0 (status flipped).
	mock.ExpectExec(`UPDATE preview_instances\s+SET name = @name`).
		WithArgs(previewAnyArgs(10)...).
		WillReturnResult(pgxmock.NewResult("UPDATE", 0))

	_, err = mgr.LaunchPreview(context.Background(), instance, StartPreviewInput{
		SessionID: instance.SessionID,
		OrgID:     instance.OrgID,
		Sandbox:   &agent.Sandbox{ID: "s-1", Provider: "docker"},
		Config:    validPreviewConfig(),
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "reservation is no longer pending")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestLaunchPreview_UpdateConfigError(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	mgr := newTestManager(mock, &mockProvider{})

	instance := &models.PreviewInstance{
		ID:        uuid.New(),
		OrgID:     uuid.New(),
		SessionID: uuid.New(),
		Status:    models.PreviewStatusStarting,
	}

	mock.ExpectExec(`UPDATE preview_instances\s+SET name = @name`).
		WithArgs(previewAnyArgs(10)...).
		WillReturnError(fmt.Errorf("db down"))

	_, err = mgr.LaunchPreview(context.Background(), instance, StartPreviewInput{
		SessionID: instance.SessionID,
		OrgID:     instance.OrgID,
		Sandbox:   &agent.Sandbox{ID: "s-1", Provider: "docker"},
		Config:    validPreviewConfig(),
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "update reserved preview config")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestLaunchPreview_HandlePersistError(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	provider := &mockProvider{
		startHandle: &PreviewHandle{Handle: "handle-new", PrimaryPort: 3000},
	}
	mgr := newTestManager(mock, provider)

	instance := &models.PreviewInstance{
		ID:        uuid.New(),
		OrgID:     uuid.New(),
		SessionID: uuid.New(),
		Status:    models.PreviewStatusStarting,
	}

	mock.ExpectExec(`UPDATE preview_instances\s+SET name = @name`).
		WithArgs(previewAnyArgs(10)...).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	// CreatePreviewService.
	mock.ExpectQuery("INSERT INTO preview_services").
		WithArgs(previewAnyArgs(7)...).
		WillReturnRows(
			pgxmock.NewRows(previewServiceTestCols).
				AddRow(uuid.New(), instance.ID, "web", "primary", "starting", []string{"npm", "run", "dev"}, "", 3000, (*int)(nil), "", time.Now()),
		)

	// UpdatePreviewHandle returns error.
	mock.ExpectExec("UPDATE preview_instances SET preview_handle = @handle, port = @port").
		WithArgs(previewAnyArgs(4)...).
		WillReturnError(fmt.Errorf("db down"))

	_, err = mgr.LaunchPreview(context.Background(), instance, StartPreviewInput{
		SessionID: instance.SessionID,
		OrgID:     instance.OrgID,
		Sandbox:   &agent.Sandbox{ID: "s-1", Provider: "docker"},
		Config:    validPreviewConfig(),
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "persist preview handle")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestLaunchPreview_ConcurrentStopDuringStartup(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	provider := &mockProvider{
		startHandle: &PreviewHandle{Handle: "handle-new", PrimaryPort: 3000},
	}
	mgr := newTestManager(mock, provider)

	instance := &models.PreviewInstance{
		ID:        uuid.New(),
		OrgID:     uuid.New(),
		SessionID: uuid.New(),
		Status:    models.PreviewStatusStarting,
	}

	mock.ExpectExec(`UPDATE preview_instances\s+SET name = @name`).
		WithArgs(previewAnyArgs(10)...).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectQuery("INSERT INTO preview_services").
		WithArgs(previewAnyArgs(7)...).
		WillReturnRows(
			pgxmock.NewRows(previewServiceTestCols).
				AddRow(uuid.New(), instance.ID, "web", "primary", "starting", []string{"npm", "run", "dev"}, "", 3000, (*int)(nil), "", time.Now()),
		)
	mock.ExpectExec("UPDATE preview_instances SET preview_handle = @handle, port = @port").
		WithArgs(previewAnyArgs(4)...).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	// UpdatePreviewStatusIfActive returns updated=false (concurrent stop).
	mock.ExpectExec("UPDATE preview_instances SET status = @status.+NOT IN").
		WithArgs(previewAnyArgs(5)...).
		WillReturnResult(pgxmock.NewResult("UPDATE", 0))

	_, err = mgr.LaunchPreview(context.Background(), instance, StartPreviewInput{
		SessionID: instance.SessionID,
		OrgID:     instance.OrgID,
		Sandbox:   &agent.Sandbox{ID: "s-1", Provider: "docker"},
		Config:    validPreviewConfig(),
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "stopped concurrently")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestLaunchPreview_ProviderStartError(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	provider := &mockProvider{startErr: fmt.Errorf("docker down")}
	mgr := newTestManager(mock, provider)

	instance := &models.PreviewInstance{
		ID:        uuid.New(),
		OrgID:     uuid.New(),
		SessionID: uuid.New(),
		Status:    models.PreviewStatusStarting,
	}

	mock.ExpectExec(`UPDATE preview_instances\s+SET name = @name`).
		WithArgs(previewAnyArgs(10)...).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectQuery("INSERT INTO preview_services").
		WithArgs(previewAnyArgs(7)...).
		WillReturnRows(
			pgxmock.NewRows(previewServiceTestCols).
				AddRow(uuid.New(), instance.ID, "web", "primary", "starting", []string{"npm", "run", "dev"}, "", 3000, (*int)(nil), "", time.Now()),
		)

	_, err = mgr.LaunchPreview(context.Background(), instance, StartPreviewInput{
		SessionID: instance.SessionID,
		OrgID:     instance.OrgID,
		Sandbox:   &agent.Sandbox{ID: "s-1", Provider: "docker"},
		Config:    validPreviewConfig(),
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "provider start preview")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAbortReservation_ReleasesHoldAndDestroysHydratedContainer(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	sandboxProvider := testutil.NewMockSandboxProvider()
	mgr := NewManager(ManagerConfig{
		Store:           db.NewPreviewStore(mock),
		SessionStore:    db.NewSessionStore(mock),
		Provider:        &mockProvider{},
		SandboxProvider: sandboxProvider,
		Logger:          zerolog.Nop(),
		WorkerNodeID:    "worker-1",
	})

	orgID := uuid.New()
	sessionID := uuid.New()
	instance := &models.PreviewInstance{
		ID:                      uuid.New(),
		OrgID:                   orgID,
		SessionID:               sessionID,
		PreviewHoldingContainer: true,
	}

	// UpdatePreviewStatus(failed).
	expectUpdatePreviewStatusFailed(mock)

	// ReleasePreviewHold: destroyNow=true (container-1 and no turn).
	mock.ExpectQuery(`WITH released AS`).
		WithArgs(previewAnyArgs(2)...).
		WillReturnRows(
			pgxmock.NewRows([]string{"session_id", "container_id", "turn_holds"}).
				AddRow(sessionID, "container-1", false),
		)

	// FinalizeContainerDestroy CAS succeeds (cleared=true).
	mock.ExpectExec(`UPDATE sessions\s+SET container_id = NULL,\s+worker_node_id = NULL,\s+sandbox_state = CASE`).
		WithArgs(previewAnyArgs(3)...).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	mgr.AbortReservation(context.Background(), instance, "container-1", "launch failed")

	require.Equal(t, 1, sandboxProvider.GetDestroyCalls())
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAbortReservation_NilInstanceIsNoop(t *testing.T) {
	t.Parallel()

	mgr := NewManager(ManagerConfig{Logger: zerolog.Nop()})
	mgr.AbortReservation(context.Background(), nil, "", "")
}

func TestAbortReservation_LeavesContainerWhenNotHydrated(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	sandboxProvider := testutil.NewMockSandboxProvider()
	mgr := NewManager(ManagerConfig{
		Store:           db.NewPreviewStore(mock),
		SessionStore:    db.NewSessionStore(mock),
		Provider:        &mockProvider{},
		SandboxProvider: sandboxProvider,
		Logger:          zerolog.Nop(),
		WorkerNodeID:    "worker-1",
	})

	instance := &models.PreviewInstance{
		ID:                      uuid.New(),
		OrgID:                   uuid.New(),
		SessionID:               uuid.New(),
		PreviewHoldingContainer: true,
	}

	expectUpdatePreviewStatusFailed(mock)
	mock.ExpectQuery(`WITH released AS`).
		WithArgs(previewAnyArgs(2)...).
		WillReturnRows(
			pgxmock.NewRows([]string{"session_id", "container_id", "turn_holds"}).
				AddRow(instance.SessionID, "container-1", false),
		)

	// hydratedContainerID="" → skip destroy even though destroyNow=true.
	mgr.AbortReservation(context.Background(), instance, "", "launch failed")

	require.Equal(t, 0, sandboxProvider.GetDestroyCalls())
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAbortReservation_LeavesContainerWhenSessionTracksDifferent(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	sandboxProvider := testutil.NewMockSandboxProvider()
	mgr := NewManager(ManagerConfig{
		Store:           db.NewPreviewStore(mock),
		SessionStore:    db.NewSessionStore(mock),
		Provider:        &mockProvider{},
		SandboxProvider: sandboxProvider,
		Logger:          zerolog.Nop(),
		WorkerNodeID:    "worker-1",
	})

	instance := &models.PreviewInstance{
		ID:                      uuid.New(),
		OrgID:                   uuid.New(),
		SessionID:               uuid.New(),
		PreviewHoldingContainer: true,
	}

	expectUpdatePreviewStatusFailed(mock)
	// Session reports a different container_id than the one we hydrated.
	mock.ExpectQuery(`WITH released AS`).
		WithArgs(previewAnyArgs(2)...).
		WillReturnRows(
			pgxmock.NewRows([]string{"session_id", "container_id", "turn_holds"}).
				AddRow(instance.SessionID, "winning-container", false),
		)

	mgr.AbortReservation(context.Background(), instance, "losing-container", "launch failed")

	require.Equal(t, 0, sandboxProvider.GetDestroyCalls())
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAbortReservation_FinalizeNotClearedSkipsDestroy(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	sandboxProvider := testutil.NewMockSandboxProvider()
	mgr := NewManager(ManagerConfig{
		Store:           db.NewPreviewStore(mock),
		SessionStore:    db.NewSessionStore(mock),
		Provider:        &mockProvider{},
		SandboxProvider: sandboxProvider,
		Logger:          zerolog.Nop(),
		WorkerNodeID:    "worker-1",
	})

	instance := &models.PreviewInstance{
		ID:                      uuid.New(),
		OrgID:                   uuid.New(),
		SessionID:               uuid.New(),
		PreviewHoldingContainer: true,
	}

	expectUpdatePreviewStatusFailed(mock)
	mock.ExpectQuery(`WITH released AS`).
		WithArgs(previewAnyArgs(2)...).
		WillReturnRows(
			pgxmock.NewRows([]string{"session_id", "container_id", "turn_holds"}).
				AddRow(instance.SessionID, "container-1", false),
		)
	// FinalizeContainerDestroy: 0 rows (a new holder acquired in the gap).
	mock.ExpectExec(`UPDATE sessions\s+SET container_id = NULL,\s+worker_node_id = NULL`).
		WithArgs(previewAnyArgs(3)...).
		WillReturnResult(pgxmock.NewResult("UPDATE", 0))

	mgr.AbortReservation(context.Background(), instance, "container-1", "launch failed")

	require.Equal(t, 0, sandboxProvider.GetDestroyCalls())
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAbortReservation_FinalizeErrorSkipsDestroy(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	sandboxProvider := testutil.NewMockSandboxProvider()
	mgr := NewManager(ManagerConfig{
		Store:           db.NewPreviewStore(mock),
		SessionStore:    db.NewSessionStore(mock),
		Provider:        &mockProvider{},
		SandboxProvider: sandboxProvider,
		Logger:          zerolog.Nop(),
		WorkerNodeID:    "worker-1",
	})

	instance := &models.PreviewInstance{
		ID:                      uuid.New(),
		OrgID:                   uuid.New(),
		SessionID:               uuid.New(),
		PreviewHoldingContainer: true,
	}

	expectUpdatePreviewStatusFailed(mock)
	mock.ExpectQuery(`WITH released AS`).
		WithArgs(previewAnyArgs(2)...).
		WillReturnRows(
			pgxmock.NewRows([]string{"session_id", "container_id", "turn_holds"}).
				AddRow(instance.SessionID, "container-1", false),
		)
	mock.ExpectExec(`UPDATE sessions\s+SET container_id = NULL,\s+worker_node_id = NULL`).
		WithArgs(previewAnyArgs(3)...).
		WillReturnError(fmt.Errorf("db down"))

	mgr.AbortReservation(context.Background(), instance, "container-1", "launch failed")

	require.Equal(t, 0, sandboxProvider.GetDestroyCalls())
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestAbortReservation_ReleaseHoldErrorLeavesContainer(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	sandboxProvider := testutil.NewMockSandboxProvider()
	mgr := NewManager(ManagerConfig{
		Store:           db.NewPreviewStore(mock),
		SessionStore:    db.NewSessionStore(mock),
		Provider:        &mockProvider{},
		SandboxProvider: sandboxProvider,
		Logger:          zerolog.Nop(),
		WorkerNodeID:    "worker-1",
	})

	instance := &models.PreviewInstance{
		ID:                      uuid.New(),
		OrgID:                   uuid.New(),
		SessionID:               uuid.New(),
		PreviewHoldingContainer: true,
	}

	expectUpdatePreviewStatusFailed(mock)
	mock.ExpectQuery(`WITH released AS`).
		WithArgs(previewAnyArgs(2)...).
		WillReturnError(fmt.Errorf("db down"))

	mgr.AbortReservation(context.Background(), instance, "container-1", "launch failed")

	require.Equal(t, 0, sandboxProvider.GetDestroyCalls())
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestStartPreview_Success(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	provider := &mockProvider{
		startHandle: &PreviewHandle{Handle: "handle-new", PrimaryPort: 3000},
		statusSnap:  &PreviewStatusSnapshot{},
	}
	mgr := newTestManager(mock, provider)

	orgID := uuid.New()
	userID := uuid.New()
	sessionID := uuid.New()
	previewID := uuid.New()
	now := time.Now()

	// Reserve phase.
	mock.ExpectQuery("SELECT .+ FROM preview_instances").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(previewInstanceTestCols))
	mock.ExpectQuery("SELECT COUNT").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectQuery("SELECT COUNT").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectQuery("SELECT COUNT").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(0))
	expectCreatePreviewInstance(mock, previewID, sessionID, orgID, userID, models.PreviewStatusStarting, now)
	mock.ExpectQuery(`UPDATE preview_instances\s+SET preview_holding_container = TRUE`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"session_id"}).AddRow(sessionID))

	// Launch phase.
	mock.ExpectExec(`UPDATE preview_instances\s+SET name = @name`).
		WithArgs(previewAnyArgs(10)...).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectQuery("INSERT INTO preview_services").
		WithArgs(previewAnyArgs(7)...).
		WillReturnRows(
			pgxmock.NewRows(previewServiceTestCols).
				AddRow(uuid.New(), previewID, "web", "primary", "starting", []string{"npm", "run", "dev"}, "", 3000, (*int)(nil), "", now),
		)
	mock.ExpectExec("UPDATE preview_instances SET preview_handle = @handle, port = @port").
		WithArgs(previewAnyArgs(4)...).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectExec("UPDATE preview_instances SET status = @status.+NOT IN").
		WithArgs(previewAnyArgs(5)...).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	got, err := mgr.StartPreview(context.Background(), StartPreviewInput{
		SessionID: sessionID,
		OrgID:     orgID,
		UserID:    userID,
		Sandbox:   &agent.Sandbox{ID: "s-1", Provider: "docker"},
		Config:    validPreviewConfig(),
	})
	require.NoError(t, err)
	require.Equal(t, models.PreviewStatusReady, got.Status)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestStartPreview_ReserveFailurePropagates(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	mgr := newTestManager(mock, &mockProvider{})

	_, err = mgr.StartPreview(context.Background(), StartPreviewInput{
		SessionID: uuid.New(),
		OrgID:     uuid.New(),
		UserID:    uuid.New(),
		Config:    &models.PreviewConfig{Name: "bad"},
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "invalid preview config")
}

func TestStartPreview_LaunchFailureAborts(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	provider := &mockProvider{startErr: fmt.Errorf("docker down")}
	sandboxProvider := testutil.NewMockSandboxProvider()
	mgr := NewManager(ManagerConfig{
		Store:           db.NewPreviewStore(mock),
		SessionStore:    db.NewSessionStore(mock),
		Provider:        provider,
		SandboxProvider: sandboxProvider,
		Logger:          zerolog.Nop(),
		WorkerNodeID:    "worker-1",
	})

	orgID := uuid.New()
	userID := uuid.New()
	sessionID := uuid.New()
	previewID := uuid.New()
	now := time.Now()

	// Reserve OK.
	mock.ExpectQuery("SELECT .+ FROM preview_instances").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(previewInstanceTestCols))
	mock.ExpectQuery("SELECT COUNT").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectQuery("SELECT COUNT").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(0))
	mock.ExpectQuery("SELECT COUNT").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(0))
	expectCreatePreviewInstance(mock, previewID, sessionID, orgID, userID, models.PreviewStatusStarting, now)
	mock.ExpectQuery(`UPDATE preview_instances\s+SET preview_holding_container = TRUE`).
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"session_id"}).AddRow(sessionID))

	// Launch: config update ok, service insert ok, provider errors.
	mock.ExpectExec(`UPDATE preview_instances\s+SET name = @name`).
		WithArgs(previewAnyArgs(10)...).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectQuery("INSERT INTO preview_services").
		WithArgs(previewAnyArgs(7)...).
		WillReturnRows(
			pgxmock.NewRows(previewServiceTestCols).
				AddRow(uuid.New(), previewID, "web", "primary", "starting", []string{"npm", "run", "dev"}, "", 3000, (*int)(nil), "", now),
		)

	// AbortReservation: UpdatePreviewStatus(failed) + ReleasePreviewHold.
	expectUpdatePreviewStatusFailed(mock)
	mock.ExpectQuery(`WITH released AS`).
		WithArgs(previewAnyArgs(2)...).
		WillReturnRows(
			pgxmock.NewRows([]string{"session_id", "container_id", "turn_holds"}).
				AddRow(sessionID, "", false),
		)

	_, err = mgr.StartPreview(context.Background(), StartPreviewInput{
		SessionID: sessionID,
		OrgID:     orgID,
		UserID:    userID,
		Sandbox:   &agent.Sandbox{ID: "s-1", Provider: "docker"},
		Config:    validPreviewConfig(),
	})
	require.Error(t, err)
	require.Contains(t, err.Error(), "provider start preview")
	require.NoError(t, mock.ExpectationsWereMet())
}

// =============================================================================
// StopActivePreviewForSession error path
// =============================================================================

func TestStopActivePreviewForSession_LookupError(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	mgr := newTestManager(mock, &mockProvider{})

	mock.ExpectQuery("SELECT .+ FROM preview_instances").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnError(fmt.Errorf("db down"))

	stopped, err := mgr.StopActivePreviewForSession(context.Background(), uuid.New(), uuid.New())
	require.Error(t, err)
	require.False(t, stopped)
	require.Contains(t, err.Error(), "lookup active preview")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestBuildStoredRecycleInputRoundTrip(t *testing.T) {
	t.Parallel()

	cfg := &models.PreviewConfig{
		Version: "3",
		Name:    "my-preview",
		Primary: "web",
		Services: map[string]models.ServiceConfig{
			"web": {
				Command: []string{"npm", "run", "dev"},
				Port:    3000,
				Ready:   models.ReadinessProbe{HTTPPath: "/"},
			},
		},
		Credentials: models.CredentialConfig{Mode: "none"},
		Network:     models.NetworkConfig{Mode: "restricted"},
	}
	sandbox := &agent.Sandbox{
		ID:       "sandbox-1",
		Provider: "docker",
		WorkDir:  "/workspace",
		Metadata: map[string]string{"container_id": "abc"},
	}

	instance := &models.PreviewInstance{}
	err := storeRecycleInput(instance, StartPreviewInput{Config: cfg, Sandbox: sandbox})
	require.NoError(t, err, "storeRecycleInput should serialize restart inputs")

	got, err := loadRecycleInput(instance)
	require.NoError(t, err, "loadRecycleInput should deserialize stored restart inputs")

	expectedConfig, err := json.Marshal(cfg)
	require.NoError(t, err, "test should be able to marshal the expected config")
	actualConfig, err := json.Marshal(got.Config)
	require.NoError(t, err, "test should be able to marshal the round-tripped config")
	require.JSONEq(t, string(expectedConfig), string(actualConfig), "recycle config should survive a serialize/deserialize round trip")
	require.Equal(t, sandbox, got.Sandbox, "recycle sandbox should survive a serialize/deserialize round trip")
}

func TestManager_HMRWatcherGetter(t *testing.T) {
	t.Parallel()

	manager := NewManager(ManagerConfig{Logger: zerolog.Nop(), WorkerNodeID: "worker-1"})
	require.Nil(t, manager.HMRWatcher(), "HMRWatcher should return nil when no watcher is configured")

	watcher := &HMRWatcher{}
	manager.hmrWatcher = watcher
	require.Equal(t, watcher, manager.HMRWatcher(), "HMRWatcher should return the configured watcher")
}

// =============================================================================
// managerServiceObserver tests
//
// The observer streams per-service Ready/Failed transitions into the DB while
// StartPreview is still running, so the frontend's startup checklist sees
// progress live. These tests exercise both methods including the PID-write
// branch, the failure-path tail handling, and the warn-and-continue paths
// when DB writes themselves fail.
// =============================================================================

func TestManagerServiceObserver_OnServiceReady_WithPID(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	mgr := newTestManager(mock, &mockProvider{})
	orgID := uuid.New()
	previewID := uuid.New()
	obs := mgr.newServiceObserver(orgID, previewID, "", "").(*managerServiceObserver)

	mock.ExpectExec("UPDATE preview_services SET status").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectExec("UPDATE preview_services SET pid").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	obs.OnServiceReady("web", 3000, 12345)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestManagerServiceObserver_OnServiceReady_NoPID(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	mgr := newTestManager(mock, &mockProvider{})
	obs := mgr.newServiceObserver(uuid.New(), uuid.New(), "", "").(*managerServiceObserver)

	// pid=0 must skip the second exec — the readiness probe runs before the
	// PID-detection goroutine has had a chance to populate ss.pid for some
	// services, and we don't want to clobber the column with 0.
	mock.ExpectExec("UPDATE preview_services SET status").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	obs.OnServiceReady("web", 3000, 0)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestManagerServiceObserver_OnServiceReady_DBErrorsLogged(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	mgr := newTestManager(mock, &mockProvider{})
	obs := mgr.newServiceObserver(uuid.New(), uuid.New(), "", "").(*managerServiceObserver)

	mock.ExpectExec("UPDATE preview_services SET status").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnError(fmt.Errorf("status update boom"))
	mock.ExpectExec("UPDATE preview_services SET pid").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnError(fmt.Errorf("pid update boom"))

	require.NotPanics(t, func() { obs.OnServiceReady("web", 3000, 99) })
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestManagerServiceObserver_OnServiceFailed_WithTail(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	mgr := newTestManager(mock, &mockProvider{})
	orgID := uuid.New()
	previewID := uuid.New()
	obs := mgr.newServiceObserver(orgID, previewID, "", "").(*managerServiceObserver)

	mock.ExpectExec("UPDATE preview_services SET status").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	logID := uuid.New()
	mock.ExpectQuery("INSERT INTO preview_logs").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "preview_instance_id", "org_id", "level", "step", "message", "metadata", "created_at",
		}).AddRow(logID, previewID, orgID, "error", "start", "msg", json.RawMessage(`null`), time.Now()))

	tail := []string{"line one", "line two: permission denied"}
	obs.OnServiceFailed("server", "exited with code 126", tail)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestManagerServiceObserver_OnInstallFailed_WithTail(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	mgr := newTestManager(mock, &mockProvider{})
	orgID := uuid.New()
	previewID := uuid.New()
	obs := mgr.newServiceObserver(orgID, previewID, "", "").(*managerServiceObserver)

	logID := uuid.New()
	mock.ExpectQuery("INSERT INTO preview_logs").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "preview_instance_id", "org_id", "level", "step", "message", "metadata", "created_at",
		}).AddRow(logID, previewID, orgID, "error", "install", "msg", json.RawMessage(`null`), time.Now()))

	tail := []string{"npm warn tar TAR_ENTRY_ERROR ENOENT", "npm error enoent"}
	obs.OnInstallFailed("exited with code 1", tail)
	require.NoError(t, mock.ExpectationsWereMet(), "install failure observer should persist an install preview log without touching service rows")
}

func TestManagerServiceObserver_OnServiceFailed_NoTail(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	mgr := newTestManager(mock, &mockProvider{})
	orgID := uuid.New()
	previewID := uuid.New()
	obs := mgr.newServiceObserver(orgID, previewID, "", "").(*managerServiceObserver)

	mock.ExpectExec("UPDATE preview_services SET status").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	logID := uuid.New()
	mock.ExpectQuery("INSERT INTO preview_logs").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "preview_instance_id", "org_id", "level", "step", "message", "metadata", "created_at",
		}).AddRow(logID, previewID, orgID, "error", "start", "msg", json.RawMessage(`null`), time.Now()))

	obs.OnServiceFailed("web", "boom", nil)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestManagerServiceObserver_OnServiceFailed_DBErrorsLogged(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	mgr := newTestManager(mock, &mockProvider{})
	obs := mgr.newServiceObserver(uuid.New(), uuid.New(), "", "").(*managerServiceObserver)

	// Both DB writes fail; the observer must log and return without panicking
	// so a flaky DB doesn't crash the worker mid-launch.
	mock.ExpectExec("UPDATE preview_services SET status").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnError(fmt.Errorf("status boom"))
	mock.ExpectQuery("INSERT INTO preview_logs").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnError(fmt.Errorf("log boom"))

	require.NotPanics(t, func() { obs.OnServiceFailed("web", "boom", []string{"line"}) })
	require.NoError(t, mock.ExpectationsWereMet())
}
