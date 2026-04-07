package preview

import (
	"context"
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
	stopErr    error
	dialErr    error
	dialStream PreviewStream
	statusSnap *PreviewStatusSnapshot
	statusErr  error
}

func (m *mockProvider) StartPreview(_ context.Context, _ *agent.Sandbox, _ *models.PreviewConfig) (*PreviewHandle, error) {
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
	"last_path", "memory_limit_mb", "cpu_limit_millis", "error", "created_at", "updated_at",
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

func newPreviewInstanceRow(id, sessionID, orgID, userID uuid.UUID, status models.PreviewStatus, handle string, now time.Time) []any {
	return []any{
		id, sessionID, orgID, userID, "bootstrap", "my-preview", string(status),
		"docker", "worker-1", handle, "web", 3000,
		"sha256:abc", "deadbeef", now, now.Add(30 * time.Minute), nil,
		"/", 512, 500, "", now, now,
	}
}

func newAccessSessionRow(id, orgID, userID, previewID uuid.UUID, tokenHash string, expiresAt time.Time, revokedAt *time.Time, now time.Time) []any {
	return []any{
		id, orgID, userID, previewID,
		tokenHash, now, expiresAt, revokedAt, now, now,
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

	token1 := generateToken()
	token2 := generateToken()

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
	require.Equal(t, previewID, resp.PreviewInstance.ID)
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
	token := generateToken()
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
	token := generateToken()
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
	token := generateToken()
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
	token := generateToken()

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
	token := generateToken()
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
	token := generateToken()
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
	token := generateToken()
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

	token := generateToken()

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
