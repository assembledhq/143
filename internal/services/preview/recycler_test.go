package preview

import (
	"context"
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

// countingProvider wraps mockProvider with call counters so tests can assert
// whether StartPreview / StopPreview were invoked during a recycle sweep.
type countingProvider struct {
	mockProvider
	stopCount  int
	startCount int
}

func (p *countingProvider) StopPreview(ctx context.Context, handle string) error {
	p.stopCount++
	return p.mockProvider.StopPreview(ctx, handle)
}

func (p *countingProvider) StartPreview(ctx context.Context, sb *agent.Sandbox, cfg *models.PreviewConfig, extraEnv map[string]string, observer ServiceObserver) (*PreviewHandle, error) {
	p.startCount++
	return p.mockProvider.StartPreview(ctx, sb, cfg, extraEnv, observer)
}

// newPreviewInstanceRowWithRecycleSchedule is like newPreviewInstanceRow but
// allows overriding the recycle_scheduled_at column.
func newPreviewInstanceRowWithRecycleSchedule(id, sessionID, orgID, userID uuid.UUID, status models.PreviewStatus, handle string, now time.Time, recycleScheduledAt *time.Time) []any {
	row := newPreviewInstanceRow(id, sessionID, orgID, userID, status, handle, now)
	row[27] = recycleScheduledAt
	return row
}

// TestRecycleWorker_Phase1SchedulesGraceWindow verifies that when a preview
// has exceeded its max uptime but does not yet have recycle_scheduled_at set,
// the recycler stamps the grace-window timestamp but does NOT yet restart
// the preview.
func TestRecycleWorker_Phase1SchedulesGraceWindow(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	provider := &countingProvider{mockProvider: mockProvider{
		startHandle: &PreviewHandle{Handle: "handle-new", PrimaryPort: 3001},
	}}
	mgr := newTestManager(mock, provider)

	orgID := uuid.New()
	previewID := uuid.New()
	sessionID := uuid.New()
	userID := uuid.New()
	now := time.Now()

	// Phase 1: ListActivePreviewsRecycledBefore returns one candidate with
	// recycle_scheduled_at == NULL.
	mock.ExpectQuery("SELECT .+ FROM preview_instances.+recycled_at < ").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(previewInstanceTestCols).
				AddRow(newPreviewInstanceRowWithRecycleSchedule(previewID, sessionID, orgID, userID, models.PreviewStatusReady, "handle-old", now, nil)...),
		)

	// ScheduleRecycle is expected and succeeds (1 row affected).
	mock.ExpectExec("UPDATE preview_instances\\s+SET recycle_scheduled_at = @at").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	// Phase 2: ListPreviewsScheduledToRecycleBefore returns empty (grace
	// window has not yet elapsed).
	mock.ExpectQuery("SELECT .+ FROM preview_instances.+recycle_scheduled_at IS NOT NULL").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(previewInstanceTestCols))

	worker := NewRecycleWorker(RecycleWorkerConfig{
		Manager:     mgr,
		Logger:      zerolog.Nop(),
		Interval:    time.Hour, // irrelevant; we call recycle directly
		MaxUptime:   30 * time.Minute,
		GracePeriod: 45 * time.Second,
	})
	worker.recycle()

	require.NoError(t, mock.ExpectationsWereMet(), "Phase 1 should issue exactly: list + schedule + due-list")
	require.Equal(t, 0, provider.stopCount, "Phase 1 must not stop the provider — it only schedules the grace window")
	require.Equal(t, 0, provider.startCount, "Phase 1 must not restart the provider — it only schedules the grace window")
}

// TestRecycleWorker_Phase2RecyclesAfterGracePeriod verifies that when a
// preview's grace window has elapsed, the recycler actually restarts it.
func TestRecycleWorker_Phase2RecyclesAfterGracePeriod(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	provider := &countingProvider{mockProvider: mockProvider{
		startHandle: &PreviewHandle{Handle: "handle-new", PrimaryPort: 3001},
	}}
	mgr := newTestManager(mock, provider)

	orgID := uuid.New()
	previewID := uuid.New()
	sessionID := uuid.New()
	userID := uuid.New()
	now := time.Now()
	gracePast := now.Add(-1 * time.Second)

	// Phase 1: ListActivePreviewsRecycledBefore returns empty (no new
	// candidates to schedule).
	mock.ExpectQuery("SELECT .+ FROM preview_instances.+recycled_at < ").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(previewInstanceTestCols))

	// Phase 2: ListPreviewsScheduledToRecycleBefore returns one due preview.
	mock.ExpectQuery("SELECT .+ FROM preview_instances.+recycle_scheduled_at IS NOT NULL").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(previewInstanceTestCols).
				AddRow(newPreviewInstanceRowWithRecycleSchedule(previewID, sessionID, orgID, userID, models.PreviewStatusReady, "handle-old", now, &gracePast)...),
		)

	// Full RecyclePreview chain follows:

	// GetPreviewInstance.
	mock.ExpectQuery("SELECT .+ FROM preview_instances WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(previewInstanceTestCols).
				AddRow(newPreviewInstanceRowWithRecycleSchedule(previewID, sessionID, orgID, userID, models.PreviewStatusReady, "handle-old", now, &gracePast)...),
		)
	// UpdatePreviewStatusIfActive.
	mock.ExpectExec("UPDATE preview_instances SET status = @status.+NOT IN").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	// RevokeAllForPreview.
	mock.ExpectExec("UPDATE preview_access_sessions SET revoked_at").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 0))
	// UpdatePreviewHandle.
	mock.ExpectExec("UPDATE preview_instances SET preview_handle = @handle, port = @port").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	// UpdatePreviewStatus (→ ready).
	mock.ExpectExec("UPDATE preview_instances SET status = @status").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	// UpdatePreviewExpiry.
	mock.ExpectExec("UPDATE preview_instances SET expires_at = @expires_at").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	// ClearRecycleSchedule at end of RecyclePreview.
	mock.ExpectExec("UPDATE preview_instances\\s+SET recycle_scheduled_at = NULL").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	worker := NewRecycleWorker(RecycleWorkerConfig{
		Manager:     mgr,
		Logger:      zerolog.Nop(),
		Interval:    time.Hour,
		MaxUptime:   30 * time.Minute,
		GracePeriod: 45 * time.Second,
	})
	worker.recycle()

	require.NoError(t, mock.ExpectationsWereMet(), "Phase 2 should exercise the full RecyclePreview chain including ClearRecycleSchedule")
	require.Equal(t, 1, provider.stopCount, "Phase 2 must stop the old preview handle once")
	require.Equal(t, 1, provider.startCount, "Phase 2 must start the new preview handle once")
}

// TestRecycleWorker_AlreadyScheduledDoesNotReschedule verifies that a preview
// already in its grace window is not re-scheduled (recycle_scheduled_at stays
// frozen instead of creeping forward on every sweep).
func TestRecycleWorker_AlreadyScheduledDoesNotReschedule(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	provider := &countingProvider{mockProvider: mockProvider{
		startHandle: &PreviewHandle{Handle: "handle-new", PrimaryPort: 3001},
	}}
	mgr := newTestManager(mock, provider)

	orgID := uuid.New()
	previewID := uuid.New()
	sessionID := uuid.New()
	userID := uuid.New()
	now := time.Now()
	graceFuture := now.Add(30 * time.Second) // grace window still open

	// Phase 1 sees a candidate but its recycle_scheduled_at is already set.
	mock.ExpectQuery("SELECT .+ FROM preview_instances.+recycled_at < ").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(previewInstanceTestCols).
				AddRow(newPreviewInstanceRowWithRecycleSchedule(previewID, sessionID, orgID, userID, models.PreviewStatusReady, "handle-old", now, &graceFuture)...),
		)

	// Phase 2 returns empty — grace window has not elapsed yet.
	mock.ExpectQuery("SELECT .+ FROM preview_instances.+recycle_scheduled_at IS NOT NULL").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(previewInstanceTestCols))

	worker := NewRecycleWorker(RecycleWorkerConfig{
		Manager:     mgr,
		Logger:      zerolog.Nop(),
		Interval:    time.Hour,
		MaxUptime:   30 * time.Minute,
		GracePeriod: 45 * time.Second,
	})
	worker.recycle()

	require.NoError(t, mock.ExpectationsWereMet(), "recycler should not call ScheduleRecycle for a preview already in its grace window")
	require.Equal(t, 0, provider.stopCount)
	require.Equal(t, 0, provider.startCount)
}

// TestNewRecycleWorker_AppliesGracePeriodDefault verifies the config default.
func TestNewRecycleWorker_AppliesGracePeriodDefault(t *testing.T) {
	t.Parallel()

	store := db.NewPreviewStore(nil)
	mgr := NewManager(ManagerConfig{
		Store:        store,
		Logger:       zerolog.Nop(),
		WorkerNodeID: "worker-1",
	})

	w := NewRecycleWorker(RecycleWorkerConfig{Manager: mgr, Logger: zerolog.Nop()})
	require.Equal(t, DefaultRecycleGracePeriod, w.gracePeriod)
	require.Equal(t, DefaultMaxUptime, w.maxUptime)
}
