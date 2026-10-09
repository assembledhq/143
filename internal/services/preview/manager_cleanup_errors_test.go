package preview

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

func TestStopPreviewWithReasonCleanupErrors(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name            string
		providerFailure bool
		databaseFailure string
		cancelRequest   bool
		sessionBacked   bool
	}{
		{name: "provider failure succeeds after durable branch preview stop", providerFailure: true},
		{name: "database begin failure remains visible", databaseFailure: "begin"},
		{name: "access revocation failure remains visible", databaseFailure: "revoke"},
		{name: "database commit failure remains visible", databaseFailure: "commit"},
		{name: "provider and database begin failures are joined", providerFailure: true, databaseFailure: "begin"},
		{name: "provider and access revocation failures are joined", providerFailure: true, databaseFailure: "revoke"},
		{name: "provider and database commit failures are joined", providerFailure: true, databaseFailure: "commit"},
		{name: "successful stop remains successful"},
		{name: "provider cancellation cannot skip durable stop and revocation", providerFailure: true, cancelRequest: true},
		{name: "canceled provider and database failures are joined", providerFailure: true, databaseFailure: "revoke", cancelRequest: true},
		{name: "provider failure succeeds after durable session preview stop and hold release", providerFailure: true, sessionBacked: true},
		{name: "successful session preview stop releases hold", sessionBacked: true},
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
			persistenceDB := &recordingStopPersistenceDB{PgxPoolIface: mock}
			mgr := newTestManager(persistenceDB, provider)
			if tt.cancelRequest {
				mgr.provider = &cancelRequestStopProvider{mockProvider: provider, cancel: cancel}
			}
			orgID, previewID, sessionID := uuid.New(), uuid.New(), uuid.Nil
			if tt.sessionBacked {
				sessionID = uuid.New()
			}
			mock.ExpectQuery("SELECT .+ FROM preview_instances WHERE id").WithArgs(managerAnyArgs(2)...).
				WillReturnRows(pgxmock.NewRows(previewInstanceTestCols).AddRow(newPreviewInstanceRow(previewID, sessionID, orgID, uuid.New(), models.PreviewStatusReady, "handle-1", time.Now())...))
			if tt.databaseFailure == "begin" {
				mock.ExpectBegin().WillReturnError(databaseFailure)
			} else {
				mock.ExpectBegin()
				mock.ExpectExec("UPDATE preview_instances SET status").WithArgs(managerAnyArgs(3)...).WillReturnResult(pgxmock.NewResult("UPDATE", 1))
				mock.ExpectExec("UPDATE preview_services SET").WithArgs(managerAnyArgs(5)...).WillReturnResult(pgxmock.NewResult("UPDATE", 0))
				mock.ExpectExec("UPDATE preview_infrastructure SET").WithArgs(managerAnyArgs(5)...).WillReturnResult(pgxmock.NewResult("UPDATE", 0))
				mock.ExpectExec("UPDATE preview_runtimes").WithArgs(managerAnyArgs(2)...).WillReturnResult(pgxmock.NewResult("UPDATE", 1))
				revoke := mock.ExpectExec("UPDATE preview_access_sessions SET revoked_at").WithArgs(managerAnyArgs(2)...)
				if tt.databaseFailure == "revoke" {
					revoke.WillReturnError(databaseFailure)
					mock.ExpectRollback()
				} else {
					revoke.WillReturnResult(pgxmock.NewResult("UPDATE", 1))
					commit := mock.ExpectCommit()
					if tt.databaseFailure == "commit" {
						commit.WillReturnError(databaseFailure)
						mock.ExpectRollback()
					}
				}
			}
			if tt.sessionBacked && tt.databaseFailure == "" {
				mock.ExpectQuery("WITH released AS").WithArgs(managerAnyArgs(2)...).
					WillReturnRows(pgxmock.NewRows([]string{"session_id", "container_id", "turn_holds"}).AddRow(sessionID, "", false))
			}
			err = mgr.StopPreviewWithReason(ctx, orgID, previewID, models.PreviewStoppedReasonNone)
			if tt.databaseFailure != "" {
				require.Error(t, err, "failed durable stop or access revocation must remain a caller-visible failure")
			} else {
				require.NoError(t, err, "committed stop and revocation must succeed while provider cleanup retries independently")
			}
			require.Equal(t, tt.providerFailure && tt.databaseFailure != "", errors.Is(err, providerFailure), "provider cleanup errors should be retained only alongside a durable stop failure")
			require.Equal(t, tt.databaseFailure != "", errors.Is(err, databaseFailure), "durable stop errors must remain discoverable by callers")
			require.Equal(t, tt.cancelRequest, errors.Is(ctx.Err(), context.Canceled), "cancellation test must cancel the original caller context")
			require.NoError(t, persistenceDB.beginContextErr, "durable stop must use a context independent of caller cancellation")
			require.True(t, persistenceDB.beginHasDeadline, "durable stop must have a bounded persistence deadline")
			require.Positive(t, persistenceDB.beginBudget, "durable stop must have time remaining even after provider cancellation")
			require.LessOrEqual(t, persistenceDB.beginBudget, 30*time.Second, "durable stop persistence budget must not exceed thirty seconds")
			require.NoError(t, mock.ExpectationsWereMet(), "provider failure must not skip durable stop and access revocation")
		})
	}
}

type recordingStopPersistenceDB struct {
	pgxmock.PgxPoolIface
	beginContextErr  error
	beginHasDeadline bool
	beginBudget      time.Duration
}

func (d *recordingStopPersistenceDB) Begin(ctx context.Context) (pgx.Tx, error) {
	d.beginContextErr = ctx.Err()
	deadline, ok := ctx.Deadline()
	d.beginHasDeadline = ok
	if ok {
		d.beginBudget = time.Until(deadline)
	}
	return d.PgxPoolIface.Begin(ctx)
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
