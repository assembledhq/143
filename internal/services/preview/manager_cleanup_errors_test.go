package preview

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

func TestStopPreviewWithReasonCleanupErrors(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name            string
		providerFailure bool
		databaseFailure bool
		cancelRequest   bool
	}{
		{name: "provider failure remains visible after revocation", providerFailure: true},
		{name: "database failure remains visible", databaseFailure: true},
		{name: "provider and database failures are joined", providerFailure: true, databaseFailure: true},
		{name: "successful stop remains successful"},
		{name: "provider cancellation cannot skip revocation", providerFailure: true, cancelRequest: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "initialize stop cleanup mock")
			defer mock.Close()
			providerFailure := errors.New("infrastructure removal failed")
			databaseFailure := errors.New("database stop failed")
			provider := &mockProvider{}
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			if tt.providerFailure {
				provider.stopErr = providerFailure
			}
			mgr := newTestManager(mock, provider)
			if tt.cancelRequest {
				mgr.provider = &cancelRequestStopProvider{mockProvider: provider, cancel: cancel}
			}
			orgID, previewID := uuid.New(), uuid.New()
			mock.ExpectQuery("SELECT .+ FROM preview_instances WHERE id").WithArgs(managerAnyArgs(2)...).
				WillReturnRows(pgxmock.NewRows(previewInstanceTestCols).AddRow(newPreviewInstanceRow(previewID, uuid.Nil, orgID, uuid.New(), models.PreviewStatusReady, "handle-1", time.Now())...))
			if tt.databaseFailure {
				mock.ExpectBegin().WillReturnError(databaseFailure)
			} else {
				mock.ExpectBegin()
				mock.ExpectExec("UPDATE preview_instances SET status").WithArgs(managerAnyArgs(3)...).WillReturnResult(pgxmock.NewResult("UPDATE", 1))
				mock.ExpectExec("UPDATE preview_services SET").WithArgs(managerAnyArgs(5)...).WillReturnResult(pgxmock.NewResult("UPDATE", 0))
				mock.ExpectExec("UPDATE preview_infrastructure SET").WithArgs(managerAnyArgs(5)...).WillReturnResult(pgxmock.NewResult("UPDATE", 0))
				mock.ExpectExec("UPDATE preview_runtimes").WithArgs(managerAnyArgs(2)...).WillReturnResult(pgxmock.NewResult("UPDATE", 1))
				mock.ExpectExec("UPDATE preview_access_sessions SET revoked_at").WithArgs(managerAnyArgs(2)...).WillReturnResult(pgxmock.NewResult("UPDATE", 1))
				mock.ExpectCommit()
			}
			err = mgr.StopPreviewWithReason(ctx, orgID, previewID, models.PreviewStoppedReasonNone)
			if tt.providerFailure || tt.databaseFailure {
				require.Error(t, err, "cleanup failures should be visible even after a preview becomes terminal")
			} else {
				require.NoError(t, err, "successful provider cleanup and revocation should return success")
			}
			require.Equal(t, tt.providerFailure, errors.Is(err, providerFailure), "provider cleanup error must be preserved for callers")
			require.Equal(t, tt.databaseFailure, errors.Is(err, databaseFailure), "database cleanup error must be preserved for callers")
			require.NoError(t, mock.ExpectationsWereMet(), "provider failure must not skip durable stop and access revocation")
		})
	}
}

type cancelRequestStopProvider struct {
	*mockProvider
	cancel context.CancelFunc
}

type recordingStopProvider struct {
	*mockProvider
	handles  []string
	contexts []context.Context
	ctxErrs  []error
}

func (p *recordingStopProvider) StopPreview(ctx context.Context, handle string) error {
	p.handles = append(p.handles, handle)
	p.contexts = append(p.contexts, ctx)
	p.ctxErrs = append(p.ctxErrs, ctx.Err())
	return p.mockProvider.StopPreview(ctx, handle)
}

func TestRecyclePreviewSkipsOtherWorkerGeneration(t *testing.T) {
	t.Parallel()
	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "initialize recycle generation fence mock")
	defer mock.Close()
	orgID, previewID := uuid.New(), uuid.New()
	row := newPreviewInstanceRow(previewID, uuid.Nil, orgID, uuid.New(), models.PreviewStatusReady, "other-handle", time.Now())
	setPreviewInstanceRowColumn(row, "worker_node_id", "worker-other")
	mock.ExpectQuery("SELECT .+ FROM preview_instances WHERE id").WithArgs(managerAnyArgs(2)...).
		WillReturnRows(pgxmock.NewRows(previewInstanceTestCols).AddRow(row...))
	mgr := newTestManager(mock, &mockProvider{})
	err = mgr.RecyclePreview(context.Background(), orgID, previewID)
	require.ErrorIs(t, err, errRecycleSkipped, "stale recycle work must not stop infrastructure owned by another generation")
	require.NoError(t, mock.ExpectationsWereMet(), "foreign generation should be skipped before provider or lifecycle mutations")
}

func (p *cancelRequestStopProvider) StopPreview(ctx context.Context, handle string) error {
	p.cancel()
	return p.mockProvider.StopPreview(ctx, handle)
}
