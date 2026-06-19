package preview

import (
	"context"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

type hmrWatcherTestInspector struct {
	mockInspector
}

func (hmrWatcherTestInspector) CaptureScreenshot(_ context.Context, _ string, _ models.ScreenshotOpts) (*models.ScreenshotResult, error) {
	return &models.ScreenshotResult{
		PNG:        []byte("png"),
		PageTitle:  "Preview",
		URL:        "http://preview.test/",
		CapturedAt: time.Now(),
	}, nil
}

type fakeRuntimeRevisionStamper struct {
	calls chan runtimeRevisionStampCall
}

type runtimeRevisionStampCall struct {
	orgID     uuid.UUID
	previewID uuid.UUID
	source    models.PreviewRuntimeRevisionSource
}

func (f *fakeRuntimeRevisionStamper) StampPreviewRuntimeRevision(ctx context.Context, orgID, previewID uuid.UUID, source models.PreviewRuntimeRevisionSource) error {
	select {
	case f.calls <- runtimeRevisionStampCall{orgID: orgID, previewID: previewID, source: source}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func TestHMRWatcher_StampsRuntimeRevisionOnHMR(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgx mock should initialize")
	defer mock.Close()

	previewID := uuid.New()
	orgID := uuid.New()
	stamper := &fakeRuntimeRevisionStamper{calls: make(chan runtimeRevisionStampCall, 1)}
	watcher, err := NewHMRWatcher(HMRWatcherConfig{
		Inspector:      &hmrWatcherTestInspector{},
		Store:          db.NewPreviewStore(mock),
		RuntimeStamper: stamper,
		Logger:         zerolog.Nop(),
		BlobDir:        t.TempDir(),
	})
	require.NoError(t, err, "HMR watcher should initialize")
	defer watcher.Close()

	mock.ExpectQuery("INSERT INTO preview_snapshots").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "preview_instance_id", "trigger", "url_path", "blob_ref",
			"viewport_width", "viewport_height", "console_errors", "file_changes", "created_at",
		}).AddRow(uuid.New(), previewID, "agent_change", "/", "blob", 1280, 720, []byte("[]"), nil, time.Now()))
	mock.ExpectQuery("SELECT COUNT").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"count"}).AddRow(1))

	watcher.StartWatching(previewID, orgID)
	watcher.OnWebSocketMessage(previewID, []byte(`{"type":"update","updates":[]}`))

	select {
	case call := <-stamper.calls:
		require.Equal(t, orgID, call.orgID, "HMR watcher should stamp the matching org")
		require.Equal(t, previewID, call.previewID, "HMR watcher should stamp the matching preview")
		require.Equal(t, models.PreviewRuntimeRevisionSourceHMR, call.source, "HMR watcher should stamp the HMR source")
	case <-time.After(4 * time.Second):
		t.Fatal("expected HMR watcher to stamp runtime revision")
	}

	require.Eventually(t, func() bool {
		return mock.ExpectationsWereMet() == nil
	}, 4*time.Second, 50*time.Millisecond, "HMR watcher should persist an agent-change screenshot")
}
