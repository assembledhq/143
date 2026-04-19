package preview

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/agent"
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

func (m *mockProvider) StartPreview(_ context.Context, _ *agent.Sandbox, _ *models.PreviewConfig) (*PreviewHandle, error) {
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
	"id", "session_id", "org_id", "user_id", "profile_name", "name", "status",
	"provider", "worker_node_id", "preview_handle", "primary_service", "port",
	"config_digest", "base_commit_sha", "last_accessed_at", "expires_at", "stopped_at",
	"last_path", "memory_limit_mb", "cpu_limit_millis", "recycle_config", "recycle_sandbox", "error", "created_at", "updated_at", "recycled_at", "recycle_scheduled_at",
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
	"id", "issue_id", "org_id", "agent_type", "status", "autonomy_level", "token_mode",
	"complexity_tier", "confidence_score", "confidence_reasoning", "risk_factors",
	"container_id", "started_at", "completed_at", "token_usage",
	"failure_explanation", "failure_category", "failure_next_steps", "failure_retry_advised",
	"parent_session_id", "revision_context", "error", "result_summary", "diff",
	"pm_plan_id", "title", "pm_approach", "pm_reasoning",
	"project_task_id", "model_override", "triggered_by_user_id",
	"agent_session_id", "current_turn", "last_activity_at", "sandbox_state", "snapshot_key",
	"target_branch", "working_branch", "repository_id", "diff_stats", "diff_history", "input_manifest",
	"archived_at", "archived_by_user_id", "automation_run_id", "deleted_at", "created_at",
}

func newPreviewInstanceRow(id, sessionID, orgID, userID uuid.UUID, status models.PreviewStatus, handle string, now time.Time) []any {
	return []any{
		id, sessionID, orgID, userID, "bootstrap", "my-preview", string(status),
		"docker", "worker-1", handle, "web", 3000,
		"sha256:abc", "deadbeef", now, now.Add(30 * time.Minute), nil,
		"/", 512, 500, []byte(`{"version":"3","name":"my-preview","primary":"web","services":{"web":{"command":["npm","run","dev"],"port":3000,"ready":{"http_path":"/"}}},"credentials":{"mode":"none"},"network":{"mode":"restricted"}}`), []byte(`{"id":"sandbox-1","provider":"docker","work_dir":"/workspace","metadata":{"container_id":"abc"}}`), "", now, now, now, nil,
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
	return []any{
		sessionID, issueID, orgID, "claude-code", "running", "supervised", "low",
		nil, nil, nil, nil,
		containerID, &now, nil, nil,
		nil, nil, nil, false,
		nil, nil, nil, nil, nil,
		nil, nil, nil, nil,
		nil, nil, nil,
		nil, 0, nil, "running", nil,
		nil, nil, nil, nil, nil, nil,
		nil, nil, nil, nil, now,
	}
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

	mgr := newTestManager(mock, &mockProvider{})

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
// ExtendTTL tests
// =============================================================================

func TestExtendTTL_Success(t *testing.T) {
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

	mock.ExpectQuery("SELECT .+ FROM preview_instances WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(previewInstanceTestCols).
				AddRow(newPreviewInstanceRow(previewID, sessionID, orgID, userID, models.PreviewStatusReady, "handle-abc", now)...),
		)

	mock.ExpectExec("UPDATE preview_instances SET expires_at").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	err = mgr.ExtendTTL(context.Background(), orgID, previewID)
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
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

func TestCheckConcurrencyCaps_UserExceeded(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	mgr := newTestManager(mock, &mockProvider{})

	orgID := uuid.New()
	userID := uuid.New()

	// User count: 2 (at limit of 2).
	mock.ExpectQuery("SELECT COUNT").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(2))

	err = mgr.checkConcurrencyCaps(context.Background(), orgID, userID)
	require.Error(t, err)
	require.Contains(t, err.Error(), "you have reached your limit")
	require.NoError(t, mock.ExpectationsWereMet())
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
	require.Contains(t, err.Error(), "org has reached its limit")
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
	require.Contains(t, err.Error(), "worker node has reached its limit")
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
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	// RevokeAllForPreview during recycle.
	mock.ExpectExec("UPDATE preview_access_sessions SET revoked_at").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 0))
	mock.ExpectExec("UPDATE preview_instances SET preview_handle = @handle, port = @port").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectExec("UPDATE preview_instances SET status = @status").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
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
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	// RevokeAllForPreview during recycle.
	mock.ExpectExec("UPDATE preview_access_sessions SET revoked_at").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 0))
	mock.ExpectExec("UPDATE preview_instances SET preview_handle = @handle, port = @port").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectExec("UPDATE preview_instances SET status = @status").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
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
	legacyRow[20] = nil
	legacyRow[21] = nil

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
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectExec("UPDATE preview_access_sessions SET revoked_at").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 0))
	mock.ExpectExec("UPDATE preview_instances SET preview_handle = @handle, port = @port").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectExec("UPDATE preview_instances SET status = @status").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
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
