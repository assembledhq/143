package preview

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func TestWorkerStopper_StopPreview_LocalWorker(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should be created")
	defer mock.Close()

	orgID := uuid.New()
	sessionID := uuid.New()
	userID := uuid.New()
	previewID := uuid.New()
	now := time.Now().UTC()
	metadata, err := json.Marshal(WorkerNodeMetadata{
		PreviewCapable:         true,
		PreviewInternalBaseURL: "http://worker-1.internal:8080",
	})
	require.NoError(t, err, "worker metadata should marshal")

	store := db.NewPreviewStore(mock)
	selector := NewWorkerSelector(db.NewNodeStore(mock), store)
	local := NewManager(ManagerConfig{
		Store:        store,
		Logger:       zerolog.Nop(),
		WorkerNodeID: "worker-1",
	})
	stopper := NewWorkerStopper(store, selector, NewWorkerPreviewClient("worker-secret"), "worker-1", local)

	mock.ExpectQuery("SELECT .+ FROM preview_instances WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(previewInstanceTestCols).AddRow(newPreviewInstanceRow(previewID, sessionID, orgID, userID, models.PreviewStatusReady, "handle-abc", now)...))
	mock.ExpectQuery("SELECT .+ FROM nodes WHERE id = @id").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(workerNodeTestCols).AddRow("worker-1", "worker", "worker-1.internal", "active", metadata, now, now))
	mock.ExpectQuery("SELECT .+ FROM preview_instances WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(previewInstanceTestCols).AddRow(newPreviewInstanceRow(previewID, sessionID, orgID, userID, models.PreviewStatusReady, "handle-abc", now)...))
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
	mock.ExpectExec("UPDATE preview_runtimes").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectExec("UPDATE preview_access_sessions SET revoked_at").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 0))
	mock.ExpectCommit()

	err = stopper.StopPreview(context.Background(), orgID, previewID)
	require.NoError(t, err, "StopPreview should stop previews owned by the local worker")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestWorkerStopper_StopPreview_RemoteWorker(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should be created")
	defer mock.Close()

	orgID := uuid.New()
	sessionID := uuid.New()
	userID := uuid.New()
	previewID := uuid.New()
	now := time.Now().UTC()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, "/internal/preview/"+previewID.String()+"/stop", r.URL.Path, "remote stops should target the worker stop endpoint")
		require.NoError(t, json.NewEncoder(w).Encode(models.SingleResponse[map[string]string]{Data: map[string]string{"status": "stopped"}}), "remote worker should return a stop response")
	}))
	defer server.Close()

	metadata, err := json.Marshal(WorkerNodeMetadata{
		PreviewCapable:         true,
		PreviewInternalBaseURL: server.URL,
	})
	require.NoError(t, err, "worker metadata should marshal")

	store := db.NewPreviewStore(mock)
	selector := NewWorkerSelector(db.NewNodeStore(mock), store)
	stopper := NewWorkerStopper(store, selector, NewWorkerPreviewClient("worker-secret"), "api-node", nil)

	mock.ExpectQuery("SELECT .+ FROM preview_instances WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(previewInstanceTestCols).AddRow(newPreviewInstanceRow(previewID, sessionID, orgID, userID, models.PreviewStatusReady, "handle-abc", now)...))
	mock.ExpectQuery("SELECT .+ FROM nodes WHERE id = @id").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(workerNodeTestCols).AddRow("worker-1", "worker", "worker-1.internal", "active", metadata, now, now))

	err = stopper.StopPreview(context.Background(), orgID, previewID)
	require.NoError(t, err, "StopPreview should forward remote previews to their owning worker")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestWorkerStopper_StopActivePreviewForSession(t *testing.T) {
	t.Parallel()

	t.Run("returns false when no preview exists", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err, "pgxmock pool should be created")
		defer mock.Close()

		orgID := uuid.New()
		sessionID := uuid.New()
		store := db.NewPreviewStore(mock)
		stopper := NewWorkerStopper(store, NewWorkerSelector(db.NewNodeStore(mock), store), NewWorkerPreviewClient("worker-secret"), "api-node", nil)

		mock.ExpectQuery("SELECT .+ FROM preview_instances").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnRows(pgxmock.NewRows(previewInstanceTestCols))

		stopped, err := stopper.StopActivePreviewForSession(context.Background(), orgID, sessionID)
		require.NoError(t, err, "StopActivePreviewForSession should not fail when no preview exists")
		require.False(t, stopped, "StopActivePreviewForSession should report that nothing was stopped")
		require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
	})

	t.Run("wraps lookup errors", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err, "pgxmock pool should be created")
		defer mock.Close()

		orgID := uuid.New()
		sessionID := uuid.New()
		store := db.NewPreviewStore(mock)
		stopper := NewWorkerStopper(store, NewWorkerSelector(db.NewNodeStore(mock), store), NewWorkerPreviewClient("worker-secret"), "api-node", nil)

		mock.ExpectQuery("SELECT .+ FROM preview_instances").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnError(errors.New("db unavailable"))

		stopped, err := stopper.StopActivePreviewForSession(context.Background(), orgID, sessionID)
		require.Error(t, err, "StopActivePreviewForSession should surface lookup failures")
		require.False(t, stopped, "StopActivePreviewForSession should not report a stop on lookup failure")
		require.Contains(t, err.Error(), "lookup active preview for session", "StopActivePreviewForSession should wrap lookup failures")
		require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
	})

	t.Run("stops a remote active preview", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err, "pgxmock pool should be created")
		defer mock.Close()

		orgID := uuid.New()
		sessionID := uuid.New()
		userID := uuid.New()
		previewID := uuid.New()
		now := time.Now().UTC()

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			require.Equal(t, "/internal/preview/"+previewID.String()+"/stop", r.URL.Path, "remote session stops should target the worker stop endpoint")
			require.NoError(t, json.NewEncoder(w).Encode(models.SingleResponse[map[string]string]{Data: map[string]string{"status": "stopped"}}), "remote worker should return a stop response")
		}))
		defer server.Close()

		metadata, err := json.Marshal(WorkerNodeMetadata{
			PreviewCapable:         true,
			PreviewInternalBaseURL: server.URL,
		})
		require.NoError(t, err, "worker metadata should marshal")

		store := db.NewPreviewStore(mock)
		selector := NewWorkerSelector(db.NewNodeStore(mock), store)
		stopper := NewWorkerStopper(store, selector, NewWorkerPreviewClient("worker-secret"), "api-node", nil)

		mock.ExpectQuery("SELECT .+ FROM preview_instances").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnRows(pgxmock.NewRows(previewInstanceTestCols).AddRow(newPreviewInstanceRow(previewID, sessionID, orgID, userID, models.PreviewStatusReady, "handle-abc", now)...))
		mock.ExpectQuery("SELECT .+ FROM nodes WHERE id = @id").
			WithArgs(pgxmock.AnyArg()).
			WillReturnRows(pgxmock.NewRows(workerNodeTestCols).AddRow("worker-1", "worker", "worker-1.internal", "active", metadata, now, now))

		stopped, err := stopper.StopActivePreviewForSession(context.Background(), orgID, sessionID)
		require.NoError(t, err, "StopActivePreviewForSession should forward remote previews to the owning worker")
		require.True(t, stopped, "StopActivePreviewForSession should report when it stopped a preview")
		require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
	})

	t.Run("surfaces stop errors", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err, "pgxmock pool should be created")
		defer mock.Close()

		orgID := uuid.New()
		sessionID := uuid.New()
		userID := uuid.New()
		previewID := uuid.New()
		now := time.Now().UTC()

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusConflict)
			require.NoError(t, json.NewEncoder(w).Encode(models.ErrorResponse{
				Error: models.ErrorDetail{Code: "NO_SANDBOX", Message: "preview missing"},
			}), "remote worker should return a structured stop error")
		}))
		defer server.Close()

		metadata, err := json.Marshal(WorkerNodeMetadata{
			PreviewCapable:         true,
			PreviewInternalBaseURL: server.URL,
		})
		require.NoError(t, err, "worker metadata should marshal")

		store := db.NewPreviewStore(mock)
		selector := NewWorkerSelector(db.NewNodeStore(mock), store)
		stopper := NewWorkerStopper(store, selector, NewWorkerPreviewClient("worker-secret"), "api-node", nil)

		mock.ExpectQuery("SELECT .+ FROM preview_instances").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnRows(pgxmock.NewRows(previewInstanceTestCols).AddRow(newPreviewInstanceRow(previewID, sessionID, orgID, userID, models.PreviewStatusReady, "handle-abc", now)...))
		mock.ExpectQuery("SELECT .+ FROM nodes WHERE id = @id").
			WithArgs(pgxmock.AnyArg()).
			WillReturnRows(pgxmock.NewRows(workerNodeTestCols).AddRow("worker-1", "worker", "worker-1.internal", "active", metadata, now, now))

		stopped, err := stopper.StopActivePreviewForSession(context.Background(), orgID, sessionID)
		require.Error(t, err, "StopActivePreviewForSession should surface stop failures")
		require.False(t, stopped, "StopActivePreviewForSession should not report a stop when the worker rejects it")
		require.Contains(t, err.Error(), "NO_SANDBOX", "StopActivePreviewForSession should preserve the remote stop error")
		require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
	})
}

func TestWorkerStopper_StopPreview_WrapsLookupAndResolveErrors(t *testing.T) {
	t.Parallel()

	t.Run("wraps preview lookup errors", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err, "pgxmock pool should be created")
		defer mock.Close()

		orgID := uuid.New()
		previewID := uuid.New()
		store := db.NewPreviewStore(mock)
		stopper := NewWorkerStopper(store, NewWorkerSelector(db.NewNodeStore(mock), store), NewWorkerPreviewClient("worker-secret"), "api-node", nil)

		mock.ExpectQuery("SELECT .+ FROM preview_instances WHERE id").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnError(errors.New("db unavailable"))

		err = stopper.StopPreview(context.Background(), orgID, previewID)
		require.Error(t, err, "StopPreview should surface preview lookup failures")
		require.Contains(t, err.Error(), "get preview instance", "StopPreview should wrap preview lookup failures")
		require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
	})

	t.Run("wraps worker resolution errors", func(t *testing.T) {
		t.Parallel()

		mock, err := pgxmock.NewPool()
		require.NoError(t, err, "pgxmock pool should be created")
		defer mock.Close()

		orgID := uuid.New()
		sessionID := uuid.New()
		userID := uuid.New()
		previewID := uuid.New()
		now := time.Now().UTC()

		store := db.NewPreviewStore(mock)
		selector := NewWorkerSelector(db.NewNodeStore(mock), store)
		stopper := NewWorkerStopper(store, selector, NewWorkerPreviewClient("worker-secret"), "api-node", nil)

		mock.ExpectQuery("SELECT .+ FROM preview_instances WHERE id").
			WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
			WillReturnRows(pgxmock.NewRows(previewInstanceTestCols).AddRow(newPreviewInstanceRow(previewID, sessionID, orgID, userID, models.PreviewStatusReady, "handle-abc", now)...))
		mock.ExpectQuery("SELECT .+ FROM nodes WHERE id = @id").
			WithArgs(pgxmock.AnyArg()).
			WillReturnRows(pgxmock.NewRows(workerNodeTestCols))

		err = stopper.StopPreview(context.Background(), orgID, previewID)
		require.Error(t, err, "StopPreview should surface worker resolution failures")
		require.Contains(t, err.Error(), "resolve preview worker", "StopPreview should wrap worker resolution failures")
		require.True(t, errors.Is(err, pgx.ErrNoRows), "StopPreview should preserve the underlying resolution error")
		require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
	})
}
