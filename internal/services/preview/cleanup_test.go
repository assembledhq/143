package preview

import (
	"testing"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func TestNewCleanupWorker_Defaults(t *testing.T) {
	t.Parallel()
	w := NewCleanupWorker(CleanupWorkerConfig{
		Logger: zerolog.Nop(),
	})
	require.Equal(t, 1*time.Minute, w.interval)
	require.Equal(t, DefaultIdleTimeout, w.idleTimeout)
}

func TestNewCleanupWorker_CustomConfig(t *testing.T) {
	t.Parallel()
	w := NewCleanupWorker(CleanupWorkerConfig{
		Logger:      zerolog.Nop(),
		Interval:    30 * time.Second,
		IdleTimeout: 5 * time.Minute,
	})
	require.Equal(t, 30*time.Second, w.interval)
	require.Equal(t, 5*time.Minute, w.idleTimeout)
}

func TestCleanupWorker_StartStop(t *testing.T) {
	t.Parallel()
	w := NewCleanupWorker(CleanupWorkerConfig{
		Logger:   zerolog.Nop(),
		Interval: 100 * time.Millisecond, // fast for testing
	})

	w.Start()
	// Give it a moment to start.
	time.Sleep(50 * time.Millisecond)
	// Stop should not hang.
	done := make(chan struct{})
	go func() {
		w.Stop()
		close(done)
	}()

	select {
	case <-done:
		// OK
	case <-time.After(2 * time.Second):
		t.Fatal("Stop() did not return within 2 seconds")
	}
}

func TestCleanupWorker_CleanupWithoutManagerDoesNotPanic(t *testing.T) {
	t.Parallel()

	w := NewCleanupWorker(CleanupWorkerConfig{
		Logger: zerolog.Nop(),
	})

	require.NotPanics(t, func() {
		w.cleanup()
	}, "cleanup should no-op when the worker has no manager wired")
}

func TestCleanupWorker_CleanupExpiredAndIdle(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := db.NewPreviewStore(mock)
	mgr := NewManager(ManagerConfig{
		Store:        store,
		Provider:     &mockProvider{},
		Logger:       zerolog.Nop(),
		WorkerNodeID: "worker-1",
	})

	orgID := uuid.New()
	expiredID := uuid.New()
	idleID := uuid.New()
	sessionID := uuid.New()
	userID := uuid.New()
	now := time.Now()

	w := NewCleanupWorker(CleanupWorkerConfig{
		Manager:     mgr,
		Logger:      zerolog.Nop(),
		Interval:    50 * time.Millisecond,
		IdleTimeout: 15 * time.Minute,
	})

	// --- Expect: ListExpiredPreviews returns one expired preview ---
	mock.ExpectQuery("worker_node_id = @worker_node_id.+expires_at").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(previewInstanceTestCols).
				AddRow(newPreviewInstanceRow(expiredID, sessionID, orgID, userID, models.PreviewStatusReady, "handle-exp", now)...),
		)

	// --- Expect: StopPreview for expired (GetPreviewInstance + provider stop + StopPreviewWithRevocation) ---
	mock.ExpectQuery("SELECT .+ FROM preview_instances WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(previewInstanceTestCols).
				AddRow(newPreviewInstanceRow(expiredID, sessionID, orgID, userID, models.PreviewStatusReady, "handle-exp", now)...),
		)
	mock.ExpectBegin()
	mock.ExpectExec("UPDATE preview_instances SET status.+stopped_reason.+stopped_at.+updated_at").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectExec("UPDATE preview_services SET").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 0))
	mock.ExpectExec("UPDATE preview_infrastructure SET").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 0))
	mock.ExpectExec("UPDATE preview_runtimes").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectExec("UPDATE preview_access_sessions SET revoked_at").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectCommit()

	// --- Expect: ListIdlePreviews returns one idle preview ---
	mock.ExpectQuery("worker_node_id = @worker_node_id.+last_accessed_at").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(previewInstanceTestCols).
				AddRow(newPreviewInstanceRow(idleID, sessionID, orgID, userID, models.PreviewStatusReady, "handle-idle", now)...),
		)

	// --- Expect: StopPreview for idle ---
	mock.ExpectQuery("SELECT .+ FROM preview_instances WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(previewInstanceTestCols).
				AddRow(newPreviewInstanceRow(idleID, sessionID, orgID, userID, models.PreviewStatusReady, "handle-idle", now)...),
		)
	mock.ExpectBegin()
	mock.ExpectExec("UPDATE preview_instances SET status.+stopped_reason.+stopped_at.+updated_at").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectExec("UPDATE preview_services SET").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 0))
	mock.ExpectExec("UPDATE preview_infrastructure SET").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 0))
	mock.ExpectExec("UPDATE preview_runtimes").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectExec("UPDATE preview_access_sessions SET revoked_at").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectCommit()

	// Call cleanup directly.
	w.cleanup()

	require.NoError(t, mock.ExpectationsWereMet())
}

func TestCleanupWorker_CleanupNoResults(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := db.NewPreviewStore(mock)
	mgr := NewManager(ManagerConfig{
		Store:        store,
		Provider:     &mockProvider{},
		Logger:       zerolog.Nop(),
		WorkerNodeID: "worker-1",
	})

	w := NewCleanupWorker(CleanupWorkerConfig{
		Manager:     mgr,
		Logger:      zerolog.Nop(),
		Interval:    50 * time.Millisecond,
		IdleTimeout: 15 * time.Minute,
	})

	// Both queries return empty.
	mock.ExpectQuery("worker_node_id = @worker_node_id.+expires_at").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(previewInstanceTestCols))
	mock.ExpectQuery("worker_node_id = @worker_node_id.+last_accessed_at").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(previewInstanceTestCols))

	w.cleanup()

	require.NoError(t, mock.ExpectationsWereMet())
}
