package db

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

// previewAnyArgs returns n pgxmock.AnyArg() matchers for NamedArgs expansion.
func previewAnyArgs(n int) []any {
	args := make([]any, n)
	for i := range args {
		args[i] = pgxmock.AnyArg()
	}
	return args
}

// =============================================================================
// Column lists for mock rows
// =============================================================================

var previewInstanceTestCols = []string{
	"id", "session_id", "org_id", "user_id", "profile_name", "name", "status",
	"provider", "worker_node_id", "preview_handle", "primary_service", "port",
	"config_digest", "base_commit_sha", "last_accessed_at", "expires_at", "stopped_at",
	"last_path", "memory_limit_mb", "cpu_limit_millis", "recycle_config", "recycle_sandbox", "error", "created_at", "updated_at",
}

var previewServiceTestCols = []string{
	"id", "preview_instance_id", "service_name", "role", "status",
	"command", "cwd", "port", "pid", "error", "created_at",
}

var previewInfraTestCols = []string{
	"id", "preview_instance_id", "infra_name", "template",
	"container_id", "status", "host", "port", "credentials_hash", "error", "created_at",
}

var previewSnapshotTestCols = []string{
	"id", "preview_instance_id", "trigger", "url_path", "blob_ref",
	"viewport_width", "viewport_height", "console_errors", "file_changes", "created_at",
}

var previewLogTestCols = []string{
	"id", "preview_instance_id", "org_id", "level", "step", "message",
	"metadata", "created_at",
}

var previewAccessSessionTestCols = []string{
	"id", "org_id", "user_id", "preview_instance_id",
	"session_token_hash", "issued_at", "expires_at", "revoked_at", "last_accessed_at", "created_at",
}

var previewStartupCacheTestCols = []string{
	"id", "org_id", "repo_id", "snapshot_key", "blob_path",
	"size_bytes", "worker_node_id", "last_used_at", "created_at",
}

var prPreviewStateTestCols = []string{
	"id", "org_id", "repo_id", "pr_number", "github_comment_id",
	"last_preview_instance_id", "last_screenshot_blob_path", "last_visual_diff_blob_path",
	"base_snapshot_key", "status", "created_at", "updated_at",
}

// Helper to build a standard preview instance row.
func newPreviewInstanceRow(id, sessionID, orgID, userID uuid.UUID, now time.Time) []any {
	return []any{
		id, sessionID, orgID, userID, "bootstrap", "my-preview", "starting",
		"docker", "worker-1", "handle-abc", "web", 3000,
		"sha256:abc", "deadbeef", now, now.Add(30 * time.Minute), nil,
		"/", 512, 500, []byte(`{"version":"3","name":"my-preview","primary":"web","services":{"web":{"command":["npm","start"],"port":3000,"ready":{"http_path":"/"}}},"credentials":{"mode":"none"},"network":{"mode":"restricted"}}`), []byte(`{"id":"sandbox-1","provider":"docker","work_dir":"/workspace","metadata":{"container_id":"abc"}}`), "", now, now,
	}
}

// =============================================================================
// Preview Instance Tests
// =============================================================================

func TestPreviewStore_CreatePreviewInstance(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewPreviewStore(mock)
	now := time.Now()
	generatedID := uuid.New()
	sessionID := uuid.New()
	orgID := uuid.New()
	userID := uuid.New()

	p := &models.PreviewInstance{
		SessionID:      sessionID,
		OrgID:          orgID,
		UserID:         userID,
		ProfileName:    "bootstrap",
		Name:           "my-preview",
		Status:         models.PreviewStatusStarting,
		Provider:       "docker",
		WorkerNodeID:   "worker-1",
		PreviewHandle:  "handle-abc",
		PrimaryService: "web",
		Port:           3000,
		ConfigDigest:   "sha256:abc",
		BaseCommitSHA:  "deadbeef",
		ExpiresAt:      now.Add(30 * time.Minute),
		LastPath:       "/",
		MemoryLimitMB:  512,
		CPULimitMillis: 500,
		RecycleConfig:  json.RawMessage(`{"version":"3"}`),
		RecycleSandbox: json.RawMessage(`{"id":"sandbox-1"}`),
	}

	mock.ExpectQuery("INSERT INTO preview_instances").
		WithArgs(previewAnyArgs(19)...).
		WillReturnRows(
			pgxmock.NewRows(previewInstanceTestCols).
				AddRow(newPreviewInstanceRow(generatedID, sessionID, orgID, userID, now)...),
		)

	err = store.CreatePreviewInstance(context.Background(), p)
	require.NoError(t, err)
	require.Equal(t, generatedID, p.ID)
	require.Equal(t, now, p.CreatedAt)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPreviewStore_GetPreviewInstance(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		setupMock func(mock pgxmock.PgxPoolIface, orgID, id uuid.UUID, now time.Time)
		expectErr bool
	}{
		{
			name: "returns instance when found",
			setupMock: func(mock pgxmock.PgxPoolIface, orgID, id uuid.UUID, now time.Time) {
				sessionID := uuid.New()
				userID := uuid.New()
				mock.ExpectQuery("SELECT .+ FROM preview_instances WHERE id").
					WithArgs(previewAnyArgs(2)...).
					WillReturnRows(
						pgxmock.NewRows(previewInstanceTestCols).
							AddRow(newPreviewInstanceRow(id, sessionID, orgID, userID, now)...),
					)
			},
		},
		{
			name: "returns error when not found",
			setupMock: func(mock pgxmock.PgxPoolIface, orgID, id uuid.UUID, now time.Time) {
				mock.ExpectQuery("SELECT .+ FROM preview_instances WHERE id").
					WithArgs(previewAnyArgs(2)...).
					WillReturnRows(pgxmock.NewRows(previewInstanceTestCols))
			},
			expectErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err)
			defer mock.Close()

			store := NewPreviewStore(mock)
			orgID := uuid.New()
			id := uuid.New()
			now := time.Now()
			tt.setupMock(mock, orgID, id, now)

			instance, err := store.GetPreviewInstance(context.Background(), orgID, id)
			if tt.expectErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.Equal(t, id, instance.ID)
			require.Equal(t, orgID, instance.OrgID)
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestPreviewStore_GetActivePreviewForSession(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewPreviewStore(mock)
	orgID := uuid.New()
	sessionID := uuid.New()
	previewID := uuid.New()
	userID := uuid.New()
	now := time.Now()

	mock.ExpectQuery("SELECT .+ FROM preview_instances.+session_id").
		WithArgs(previewAnyArgs(2)...).
		WillReturnRows(
			pgxmock.NewRows(previewInstanceTestCols).
				AddRow(newPreviewInstanceRow(previewID, sessionID, orgID, userID, now)...),
		)

	instance, err := store.GetActivePreviewForSession(context.Background(), orgID, sessionID)
	require.NoError(t, err)
	require.Equal(t, previewID, instance.ID)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPreviewStore_UpdatePreviewStatus(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		rows      int64
		expectErr bool
	}{
		{name: "updates successfully", rows: 1},
		{name: "not found returns error", rows: 0, expectErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err)
			defer mock.Close()

			store := NewPreviewStore(mock)

			mock.ExpectExec("UPDATE preview_instances SET status").
				WithArgs(previewAnyArgs(4)...).
				WillReturnResult(pgxmock.NewResult("UPDATE", tt.rows))

			err = store.UpdatePreviewStatus(context.Background(), uuid.New(), uuid.New(), models.PreviewStatusReady, "")
			if tt.expectErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestPreviewStore_StopPreview(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		rows      int64
		expectErr bool
	}{
		{name: "stops active preview", rows: 1},
		{name: "already stopped returns error", rows: 0, expectErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err)
			defer mock.Close()

			store := NewPreviewStore(mock)

			mock.ExpectExec("UPDATE preview_instances SET status.+stopped_at.+updated_at").
				WithArgs(previewAnyArgs(3)...).
				WillReturnResult(pgxmock.NewResult("UPDATE", tt.rows))

			err = store.StopPreview(context.Background(), uuid.New(), uuid.New())
			if tt.expectErr {
				require.Error(t, err)
			} else {
				require.NoError(t, err)
			}
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestPreviewStore_CountActivePreviewsByOrg(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewPreviewStore(mock)

	mock.ExpectQuery("SELECT COUNT.+FROM preview_instances WHERE org_id").
		WithArgs(previewAnyArgs(1)...).
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(3))

	count, err := store.CountActivePreviewsByOrg(context.Background(), uuid.New())
	require.NoError(t, err)
	require.Equal(t, 3, count)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPreviewStore_ListExpiredPreviews(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewPreviewStore(mock)
	now := time.Now()
	id1 := uuid.New()
	id2 := uuid.New()

	mock.ExpectQuery("SELECT .+ FROM preview_instances.+expires_at < .+ORDER BY expires_at").
		WithArgs(previewAnyArgs(1)...).
		WillReturnRows(
			pgxmock.NewRows(previewInstanceTestCols).
				AddRow(newPreviewInstanceRow(id1, uuid.New(), uuid.New(), uuid.New(), now)...).
				AddRow(newPreviewInstanceRow(id2, uuid.New(), uuid.New(), uuid.New(), now)...),
		)

	results, err := store.ListExpiredPreviews(context.Background(), now)
	require.NoError(t, err)
	require.Len(t, results, 2)
	require.Equal(t, id1, results[0].ID)
	require.Equal(t, id2, results[1].ID)
	require.NoError(t, mock.ExpectationsWereMet())
}

// =============================================================================
// Preview Service Tests
// =============================================================================

func TestPreviewStore_CreatePreviewService(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewPreviewStore(mock)
	now := time.Now()
	generatedID := uuid.New()
	previewID := uuid.New()

	svc := &models.PreviewService{
		PreviewInstanceID: previewID,
		ServiceName:       "web",
		Role:              models.PreviewServiceRolePrimary,
		Status:            models.PreviewServiceStatusStarting,
		Command:           []string{"npm", "start"},
		Cwd:               ".",
		Port:              3000,
	}

	mock.ExpectQuery("INSERT INTO preview_services").
		WithArgs(previewAnyArgs(7)...).
		WillReturnRows(
			pgxmock.NewRows(previewServiceTestCols).
				AddRow(generatedID, previewID, "web", "primary", "starting",
					[]string{"npm", "start"}, ".", 3000, nil, "", now),
		)

	err = store.CreatePreviewService(context.Background(), svc)
	require.NoError(t, err)
	require.Equal(t, generatedID, svc.ID)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPreviewStore_ListServicesByPreview(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewPreviewStore(mock)
	now := time.Now()
	orgID := uuid.New()
	previewID := uuid.New()

	mock.ExpectQuery("SELECT .+ FROM preview_services.+preview_instance_id").
		WithArgs(previewAnyArgs(2)...).
		WillReturnRows(
			pgxmock.NewRows(previewServiceTestCols).
				AddRow(uuid.New(), previewID, "web", "primary", "ready",
					[]string{"npm", "start"}, ".", 3000, nil, "", now).
				AddRow(uuid.New(), previewID, "worker", "support", "starting",
					[]string{"npm", "run", "worker"}, ".", 4000, nil, "", now),
		)

	svcs, err := store.ListServicesByPreview(context.Background(), orgID, previewID)
	require.NoError(t, err)
	require.Len(t, svcs, 2)
	require.Equal(t, "web", svcs[0].ServiceName)
	require.Equal(t, "worker", svcs[1].ServiceName)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPreviewStore_UpdateServiceStatus(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewPreviewStore(mock)

	mock.ExpectExec("UPDATE preview_services SET status").
		WithArgs(previewAnyArgs(5)...).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	err = store.UpdateServiceStatus(context.Background(), uuid.New(), uuid.New(), "web", models.PreviewServiceStatusReady, "")
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

// =============================================================================
// Preview Infrastructure Tests
// =============================================================================

func TestPreviewStore_CreatePreviewInfrastructure(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewPreviewStore(mock)
	now := time.Now()
	generatedID := uuid.New()
	previewID := uuid.New()

	infra := &models.PreviewInfrastructure{
		PreviewInstanceID: previewID,
		InfraName:         "db",
		Template:          "postgres-17",
		ContainerID:       "ctr-123",
		Status:            models.PreviewInfraStatusProvisioning,
		Host:              "localhost",
		Port:              5432,
		CredentialsHash:   "hash-abc",
	}

	mock.ExpectQuery("INSERT INTO preview_infrastructure").
		WithArgs(previewAnyArgs(8)...).
		WillReturnRows(
			pgxmock.NewRows(previewInfraTestCols).
				AddRow(generatedID, previewID, "db", "postgres-17",
					"ctr-123", "provisioning", "localhost", 5432, "hash-abc", "", now),
		)

	err = store.CreatePreviewInfrastructure(context.Background(), infra)
	require.NoError(t, err)
	require.Equal(t, generatedID, infra.ID)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPreviewStore_ListInfraByPreview(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewPreviewStore(mock)
	now := time.Now()
	orgID := uuid.New()
	previewID := uuid.New()

	mock.ExpectQuery("SELECT .+ FROM preview_infrastructure.+preview_instance_id").
		WithArgs(previewAnyArgs(2)...).
		WillReturnRows(
			pgxmock.NewRows(previewInfraTestCols).
				AddRow(uuid.New(), previewID, "db", "postgres-17",
					"ctr-1", "healthy", "localhost", 5432, "h1", "", now).
				AddRow(uuid.New(), previewID, "cache", "redis-7",
					"ctr-2", "healthy", "localhost", 6379, "h2", "", now),
		)

	infras, err := store.ListInfraByPreview(context.Background(), orgID, previewID)
	require.NoError(t, err)
	require.Len(t, infras, 2)
	require.Equal(t, "db", infras[0].InfraName)
	require.Equal(t, "cache", infras[1].InfraName)
	require.NoError(t, mock.ExpectationsWereMet())
}

// =============================================================================
// Preview Snapshot Tests
// =============================================================================

func TestPreviewStore_CreateSnapshot(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewPreviewStore(mock)
	now := time.Now()
	generatedID := uuid.New()
	previewID := uuid.New()

	snap := &models.PreviewSnapshot{
		PreviewInstanceID: previewID,
		Trigger:           models.PreviewSnapshotTriggerBaseline,
		URLPath:           "/",
		BlobRef:           "blob://snap-1",
		ViewportWidth:     1280,
		ViewportHeight:    720,
		ConsoleErrors:     json.RawMessage(`[]`),
	}

	mock.ExpectQuery("INSERT INTO preview_snapshots").
		WithArgs(previewAnyArgs(8)...).
		WillReturnRows(
			pgxmock.NewRows(previewSnapshotTestCols).
				AddRow(generatedID, previewID, "baseline", "/", "blob://snap-1",
					1280, 720, json.RawMessage(`[]`), nil, now),
		)

	err = store.CreateSnapshot(context.Background(), snap)
	require.NoError(t, err)
	require.Equal(t, generatedID, snap.ID)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPreviewStore_ListSnapshotsByPreview(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewPreviewStore(mock)
	now := time.Now()
	orgID := uuid.New()
	previewID := uuid.New()

	mock.ExpectQuery("SELECT .+ FROM preview_snapshots.+preview_instance_id").
		WithArgs(previewAnyArgs(2)...).
		WillReturnRows(
			pgxmock.NewRows(previewSnapshotTestCols).
				AddRow(uuid.New(), previewID, "baseline", "/", "blob://s1",
					1280, 720, json.RawMessage(`[]`), nil, now).
				AddRow(uuid.New(), previewID, "agent_change", "/page", "blob://s2",
					1280, 720, json.RawMessage(`[{"text":"err"}]`), json.RawMessage(`["a.ts"]`), now.Add(time.Minute)),
		)

	snaps, err := store.ListSnapshotsByPreview(context.Background(), orgID, previewID)
	require.NoError(t, err)
	require.Len(t, snaps, 2)
	require.Equal(t, models.PreviewSnapshotTriggerBaseline, snaps[0].Trigger)
	require.Equal(t, models.PreviewSnapshotTriggerAgentChange, snaps[1].Trigger)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPreviewStore_CountAndDeleteSnapshots(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewPreviewStore(mock)
	orgID := uuid.New()
	previewID := uuid.New()

	// Count
	mock.ExpectQuery("SELECT COUNT.+FROM preview_snapshots").
		WithArgs(previewAnyArgs(2)...).
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(15))

	count, err := store.CountSnapshotsByPreview(context.Background(), orgID, previewID)
	require.NoError(t, err)
	require.Equal(t, 15, count)

	// Delete oldest
	mock.ExpectExec("DELETE FROM preview_snapshots WHERE id IN").
		WithArgs(previewAnyArgs(3)...).
		WillReturnResult(pgxmock.NewResult("DELETE", 5))

	err = store.DeleteOldestSnapshots(context.Background(), orgID, previewID, 10)
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

// =============================================================================
// Preview Log Tests
// =============================================================================

func TestPreviewStore_CreatePreviewLog(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewPreviewStore(mock)
	now := time.Now()
	generatedID := uuid.New()
	previewID := uuid.New()
	orgID := uuid.New()

	logEntry := &models.PreviewLog{
		PreviewInstanceID: previewID,
		OrgID:             orgID,
		Level:             "info",
		Step:              models.PreviewLogStepBuild,
		Message:           "Building container",
		Metadata:          json.RawMessage(`{"image":"node:20"}`),
	}

	mock.ExpectQuery("INSERT INTO preview_logs").
		WithArgs(previewAnyArgs(6)...).
		WillReturnRows(
			pgxmock.NewRows(previewLogTestCols).
				AddRow(generatedID, previewID, orgID, "info", "build", "Building container",
					json.RawMessage(`{"image":"node:20"}`), now),
		)

	err = store.CreatePreviewLog(context.Background(), logEntry)
	require.NoError(t, err)
	require.Equal(t, generatedID, logEntry.ID)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPreviewStore_ListLogsByPreview(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		afterID *uuid.UUID
		nArgs   int
		pattern string
	}{
		{name: "without cursor", afterID: nil, nArgs: 2, pattern: "SELECT .+ FROM preview_logs.+preview_instance_id.+org_id.+ORDER BY created_at"},
		{name: "with cursor", afterID: func() *uuid.UUID { id := uuid.New(); return &id }(), nArgs: 3, pattern: `SELECT .+ FROM preview_logs.+preview_instance_id.+org_id.+\(created_at, id\) >`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err)
			defer mock.Close()

			store := NewPreviewStore(mock)
			now := time.Now()
			previewID := uuid.New()
			orgID := uuid.New()

			mock.ExpectQuery(tt.pattern).
				WithArgs(previewAnyArgs(tt.nArgs)...).
				WillReturnRows(
					pgxmock.NewRows(previewLogTestCols).
						AddRow(uuid.New(), previewID, orgID, "info", "build", "step 1",
							json.RawMessage(`{}`), now).
						AddRow(uuid.New(), previewID, orgID, "info", "start", "step 2",
							json.RawMessage(`{}`), now.Add(time.Second)),
				)

			logs, err := store.ListLogsByPreview(context.Background(), orgID, previewID, tt.afterID)
			require.NoError(t, err)
			require.Len(t, logs, 2)
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

// =============================================================================
// Preview Access Session Tests
// =============================================================================

func TestPreviewStore_CreateAccessSession(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewPreviewStore(mock)
	now := time.Now()
	generatedID := uuid.New()
	orgID := uuid.New()
	userID := uuid.New()
	previewID := uuid.New()

	sess := &models.PreviewAccessSession{
		OrgID:             orgID,
		UserID:            userID,
		PreviewInstanceID: previewID,
		SessionTokenHash:  "sha256:token-hash",
		ExpiresAt:         now.Add(time.Hour),
	}

	mock.ExpectQuery("INSERT INTO preview_access_sessions").
		WithArgs(previewAnyArgs(5)...).
		WillReturnRows(
			pgxmock.NewRows(previewAccessSessionTestCols).
				AddRow(generatedID, orgID, userID, previewID,
					"sha256:token-hash", now, now.Add(time.Hour), nil, now, now),
		)

	err = store.CreateAccessSession(context.Background(), sess)
	require.NoError(t, err)
	require.Equal(t, generatedID, sess.ID)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPreviewStore_GetAccessSessionByToken(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		setupMock func(mock pgxmock.PgxPoolIface)
		expectErr bool
	}{
		{
			name: "returns session when found",
			setupMock: func(mock pgxmock.PgxPoolIface) {
				now := time.Now()
				mock.ExpectQuery("SELECT .+ FROM preview_access_sessions.+org_id.+session_token_hash").
					WithArgs(previewAnyArgs(2)...).
					WillReturnRows(
						pgxmock.NewRows(previewAccessSessionTestCols).
							AddRow(uuid.New(), uuid.New(), uuid.New(), uuid.New(),
								"hash", now, now.Add(time.Hour), nil, now, now),
					)
			},
		},
		{
			name: "returns error when not found",
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectQuery("SELECT .+ FROM preview_access_sessions.+org_id.+session_token_hash").
					WithArgs(previewAnyArgs(2)...).
					WillReturnRows(pgxmock.NewRows(previewAccessSessionTestCols))
			},
			expectErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err)
			defer mock.Close()

			store := NewPreviewStore(mock)
			tt.setupMock(mock)

			sess, err := store.GetAccessSessionByToken(context.Background(), uuid.New(), "hash")
			if tt.expectErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.NotNil(t, sess)
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestPreviewStore_GetAccessSessionByTokenUnscoped(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		setupMock func(mock pgxmock.PgxPoolIface)
		expectErr bool
	}{
		{
			name: "returns session when found",
			setupMock: func(mock pgxmock.PgxPoolIface) {
				now := time.Now()
				mock.ExpectQuery("SELECT .+ FROM preview_access_sessions.+session_token_hash").
					WithArgs(previewAnyArgs(1)...).
					WillReturnRows(
						pgxmock.NewRows(previewAccessSessionTestCols).
							AddRow(uuid.New(), uuid.New(), uuid.New(), uuid.New(),
								"hash", now, now.Add(time.Hour), nil, now, now),
					)
			},
		},
		{
			name: "returns error when not found",
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectQuery("SELECT .+ FROM preview_access_sessions.+session_token_hash").
					WithArgs(previewAnyArgs(1)...).
					WillReturnRows(pgxmock.NewRows(previewAccessSessionTestCols))
			},
			expectErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err)
			defer mock.Close()

			store := NewPreviewStore(mock)
			tt.setupMock(mock)

			sess, err := store.GetAccessSessionByTokenUnscoped(context.Background(), "hash")
			if tt.expectErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.NotNil(t, sess)
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestPreviewStore_RevokeAccessSession(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewPreviewStore(mock)

	mock.ExpectExec("UPDATE preview_access_sessions SET revoked_at").
		WithArgs(previewAnyArgs(2)...).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	err = store.RevokeAccessSession(context.Background(), uuid.New(), uuid.New())
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPreviewStore_RevokeAllForPreview(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewPreviewStore(mock)

	mock.ExpectExec("UPDATE preview_access_sessions SET revoked_at.+preview_instance_id").
		WithArgs(previewAnyArgs(2)...).
		WillReturnResult(pgxmock.NewResult("UPDATE", 3))

	err = store.RevokeAllForPreview(context.Background(), uuid.New(), uuid.New())
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

// =============================================================================
// Startup Cache Tests
// =============================================================================

func TestPreviewStore_UpsertStartupCache(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewPreviewStore(mock)
	now := time.Now()
	generatedID := uuid.New()
	orgID := uuid.New()
	repoID := uuid.New()

	entry := &models.PreviewStartupCache{
		OrgID:        orgID,
		RepoID:       repoID,
		SnapshotKey:  "lockfile:abc+commit:def+config:ghi",
		BlobPath:     "/cache/snap.tar.zst",
		SizeBytes:    1024 * 1024,
		WorkerNodeID: "worker-1",
	}

	mock.ExpectQuery("INSERT INTO preview_startup_cache").
		WithArgs(previewAnyArgs(6)...).
		WillReturnRows(
			pgxmock.NewRows(previewStartupCacheTestCols).
				AddRow(generatedID, orgID, repoID, "lockfile:abc+commit:def+config:ghi",
					"/cache/snap.tar.zst", int64(1024*1024), "worker-1", now, now),
		)

	err = store.UpsertStartupCache(context.Background(), entry)
	require.NoError(t, err)
	require.Equal(t, generatedID, entry.ID)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPreviewStore_FindMatchingCache(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		setupMock func(mock pgxmock.PgxPoolIface)
		expectErr bool
	}{
		{
			name: "returns cache hit",
			setupMock: func(mock pgxmock.PgxPoolIface) {
				now := time.Now()
				mock.ExpectQuery("SELECT .+ FROM preview_startup_cache.+snapshot_key").
					WithArgs(previewAnyArgs(3)...).
					WillReturnRows(
						pgxmock.NewRows(previewStartupCacheTestCols).
							AddRow(uuid.New(), uuid.New(), uuid.New(), "key",
								"/cache/snap.tar.zst", int64(1024), "w1", now, now),
					)
			},
		},
		{
			name: "returns error on cache miss",
			setupMock: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectQuery("SELECT .+ FROM preview_startup_cache.+snapshot_key").
					WithArgs(previewAnyArgs(3)...).
					WillReturnRows(pgxmock.NewRows(previewStartupCacheTestCols))
			},
			expectErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err)
			defer mock.Close()

			store := NewPreviewStore(mock)
			tt.setupMock(mock)

			entry, err := store.FindMatchingCache(context.Background(), uuid.New(), uuid.New(), "key")
			if tt.expectErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			require.NotNil(t, entry)
			require.NoError(t, mock.ExpectationsWereMet())
		})
	}
}

func TestPreviewStore_DeleteCache(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewPreviewStore(mock)

	mock.ExpectExec("DELETE FROM preview_startup_cache WHERE id").
		WithArgs(previewAnyArgs(2)...).
		WillReturnResult(pgxmock.NewResult("DELETE", 1))

	err = store.DeleteCache(context.Background(), uuid.New(), uuid.New())
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

// =============================================================================
// PR Preview State Tests
// =============================================================================

func TestPreviewStore_UpsertPRPreviewState(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewPreviewStore(mock)
	now := time.Now()
	generatedID := uuid.New()
	orgID := uuid.New()
	repoID := uuid.New()
	previewID := uuid.New()
	commentID := int64(42)

	state := &models.PRPreviewState{
		OrgID:                  orgID,
		RepoID:                 repoID,
		PRNumber:               123,
		GitHubCommentID:        &commentID,
		LastPreviewInstanceID:  &previewID,
		LastScreenshotBlobPath: "blob://screenshot",
		LastVisualDiffBlobPath: "blob://diff",
		BaseSnapshotKey:        "snap-key",
		Status:                 models.PRPreviewStatusRunning,
	}

	mock.ExpectQuery("INSERT INTO pr_preview_state").
		WithArgs(previewAnyArgs(9)...).
		WillReturnRows(
			pgxmock.NewRows(prPreviewStateTestCols).
				AddRow(generatedID, orgID, repoID, 123, &commentID,
					&previewID, "blob://screenshot", "blob://diff",
					"snap-key", "running", now, now),
		)

	err = store.UpsertPRPreviewState(context.Background(), state)
	require.NoError(t, err)
	require.Equal(t, generatedID, state.ID)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPreviewStore_GetPRPreviewState(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewPreviewStore(mock)
	now := time.Now()
	orgID := uuid.New()
	repoID := uuid.New()
	stateID := uuid.New()

	mock.ExpectQuery("SELECT .+ FROM pr_preview_state.+org_id.+repo_id.+pr_number").
		WithArgs(previewAnyArgs(3)...).
		WillReturnRows(
			pgxmock.NewRows(prPreviewStateTestCols).
				AddRow(stateID, orgID, repoID, 42, nil,
					nil, "", "",
					"", "never_started", now, now),
		)

	state, err := store.GetPRPreviewState(context.Background(), orgID, repoID, 42)
	require.NoError(t, err)
	require.Equal(t, stateID, state.ID)
	require.Equal(t, 42, state.PRNumber)
	require.Equal(t, models.PRPreviewStatusNeverStarted, state.Status)
	require.NoError(t, mock.ExpectationsWereMet())
}

// =============================================================================
// Transactional Tests
// =============================================================================

func TestPreviewStore_StopPreviewWithRevocation(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewPreviewStore(mock)

	mock.ExpectBegin()
	mock.ExpectExec("UPDATE preview_instances SET status.+stopped_at.+updated_at").
		WithArgs(previewAnyArgs(3)...).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectExec("UPDATE preview_access_sessions SET revoked_at").
		WithArgs(previewAnyArgs(2)...).
		WillReturnResult(pgxmock.NewResult("UPDATE", 2))
	mock.ExpectCommit()

	err = store.StopPreviewWithRevocation(context.Background(), uuid.New(), uuid.New())
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPreviewStore_StopPreviewWithRevocation_RollbackOnError(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewPreviewStore(mock)

	mock.ExpectBegin()
	mock.ExpectExec("UPDATE preview_instances SET status.+stopped_at.+updated_at").
		WithArgs(previewAnyArgs(3)...).
		WillReturnResult(pgxmock.NewResult("UPDATE", 0)) // not found
	mock.ExpectRollback()

	err = store.StopPreviewWithRevocation(context.Background(), uuid.New(), uuid.New())
	require.Error(t, err)
	require.Contains(t, err.Error(), "not found or already stopped")
	require.NoError(t, mock.ExpectationsWereMet())
}

// =============================================================================
// WithTx Test
// =============================================================================

func TestPreviewStore_WithTx(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewPreviewStore(mock)

	mock.ExpectBegin()
	tx, err := store.Begin(context.Background())
	require.NoError(t, err)

	txStore := store.WithTx(tx)
	require.NotNil(t, txStore)

	mock.ExpectExec("UPDATE preview_instances SET last_accessed_at").
		WithArgs(previewAnyArgs(2)...).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	err = txStore.UpdatePreviewAccess(context.Background(), uuid.New(), uuid.New())
	require.NoError(t, err)

	mock.ExpectCommit()
	err = tx.Commit(context.Background())
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

// =============================================================================
// Additional Preview Store Tests for Coverage
// =============================================================================

func TestPreviewStore_UpdatePreviewExpiry(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewPreviewStore(mock)

	mock.ExpectExec("UPDATE preview_instances SET expires_at").
		WithArgs(previewAnyArgs(3)...).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	err = store.UpdatePreviewExpiry(context.Background(), uuid.New(), uuid.New(), time.Now().Add(time.Hour))
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPreviewStore_UpdateLastPath(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewPreviewStore(mock)

	mock.ExpectExec("UPDATE preview_instances SET last_path").
		WithArgs(previewAnyArgs(3)...).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	err = store.UpdateLastPath(context.Background(), uuid.New(), uuid.New(), "/dashboard")
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPreviewStore_CountActivePreviewsByUser(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewPreviewStore(mock)

	mock.ExpectQuery("SELECT COUNT").
		WithArgs(previewAnyArgs(2)...).
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(3))

	count, err := store.CountActivePreviewsByUser(context.Background(), uuid.New(), uuid.New())
	require.NoError(t, err)
	require.Equal(t, 3, count)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPreviewStore_CountActivePreviewsByWorker(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewPreviewStore(mock)

	mock.ExpectQuery("SELECT COUNT").
		WithArgs(previewAnyArgs(1)...).
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(2))

	count, err := store.CountActivePreviewsByWorker(context.Background(), "worker-1")
	require.NoError(t, err)
	require.Equal(t, 2, count)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPreviewStore_ListIdlePreviews(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewPreviewStore(mock)
	now := time.Now()
	previewID := uuid.New()
	sessionID := uuid.New()
	orgID := uuid.New()
	userID := uuid.New()

	mock.ExpectQuery("SELECT .+ FROM preview_instances.+last_accessed_at").
		WithArgs(previewAnyArgs(1)...).
		WillReturnRows(
			pgxmock.NewRows(previewInstanceTestCols).
				AddRow(newPreviewInstanceRow(previewID, sessionID, orgID, userID, now)...),
		)

	previews, err := store.ListIdlePreviews(context.Background(), now.Add(-15*time.Minute))
	require.NoError(t, err)
	require.Len(t, previews, 1)
	require.Equal(t, previewID, previews[0].ID)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPreviewStore_UpdateServicePID(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewPreviewStore(mock)

	mock.ExpectExec("UPDATE preview_services SET pid").
		WithArgs(previewAnyArgs(4)...).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	err = store.UpdateServicePID(context.Background(), uuid.New(), uuid.New(), "web", 12345)
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPreviewStore_UpdateInfraStatus(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewPreviewStore(mock)

	mock.ExpectExec("UPDATE preview_infrastructure SET status").
		WithArgs(previewAnyArgs(5)...).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	err = store.UpdateInfraStatus(context.Background(), uuid.New(), uuid.New(), "db", models.PreviewInfraStatusHealthy, "")
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPreviewStore_UpdateAccessSessionActivity(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewPreviewStore(mock)

	mock.ExpectExec("UPDATE preview_access_sessions SET last_accessed_at").
		WithArgs(previewAnyArgs(2)...).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	err = store.UpdateAccessSessionActivity(context.Background(), uuid.New(), uuid.New())
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPreviewStore_TouchCache(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewPreviewStore(mock)

	mock.ExpectExec("UPDATE preview_startup_cache SET last_used_at").
		WithArgs(previewAnyArgs(2)...).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	err = store.TouchCache(context.Background(), uuid.New(), uuid.New())
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPreviewStore_ListCacheByWorker(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewPreviewStore(mock)

	mock.ExpectQuery("SELECT .+ FROM preview_startup_cache").
		WithArgs(previewAnyArgs(1)...).
		WillReturnRows(pgxmock.NewRows(previewStartupCacheTestCols))

	caches, err := store.ListCacheByWorker(context.Background(), "worker-1")
	require.NoError(t, err)
	require.Len(t, caches, 0)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestPreviewStore_UpdatePRPreviewStatus(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewPreviewStore(mock)

	mock.ExpectExec("UPDATE pr_preview_state SET status").
		WithArgs(previewAnyArgs(3)...).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	err = store.UpdatePRPreviewStatus(context.Background(), uuid.New(), uuid.New(), models.PRPreviewStatusRunning)
	require.NoError(t, err)
	require.NoError(t, mock.ExpectationsWereMet())
}
