package db

import (
	"context"
	"errors"
	"regexp"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

func TestResolvePreviewInfrastructureCleanup(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		setup     func(pgxmock.PgxPoolIface)
		expected  []bool
		expectErr bool
	}{
		{name: "batched ownership decisions", setup: func(mock pgxmock.PgxPoolIface) {
			mock.ExpectQuery(regexp.QuoteMeta(previewInfrastructureCleanupQuery)).
				WithArgs(previewAnyArgs(7)...).
				WillReturnRows(pgxmock.NewRows([]string{"owner_index", "eligible"}).AddRow(int64(1), false).AddRow(int64(2), true))
		}, expected: []bool{false, true}},
		{name: "missing results remain protected", setup: func(mock pgxmock.PgxPoolIface) {
			mock.ExpectQuery(regexp.QuoteMeta(previewInfrastructureCleanupQuery)).
				WithArgs(previewAnyArgs(7)...).
				WillReturnRows(pgxmock.NewRows([]string{"owner_index", "eligible"}).AddRow(int64(1), true))
		}, expected: []bool{true, false}},
		{name: "query failure protects the batch", setup: func(mock pgxmock.PgxPoolIface) {
			mock.ExpectQuery(regexp.QuoteMeta(previewInfrastructureCleanupQuery)).
				WithArgs(previewAnyArgs(7)...).WillReturnError(errors.New("database unavailable"))
		}, expected: []bool{false, false}, expectErr: true},
		{name: "scan failure protects the batch", setup: func(mock pgxmock.PgxPoolIface) {
			mock.ExpectQuery(regexp.QuoteMeta(previewInfrastructureCleanupQuery)).
				WithArgs(previewAnyArgs(7)...).
				WillReturnRows(pgxmock.NewRows([]string{"owner_index", "eligible"}).AddRow("bad-index", true))
		}, expected: []bool{false, false}, expectErr: true},
		{name: "invalid result identity protects the batch", setup: func(mock pgxmock.PgxPoolIface) {
			mock.ExpectQuery(regexp.QuoteMeta(previewInfrastructureCleanupQuery)).
				WithArgs(previewAnyArgs(7)...).
				WillReturnRows(pgxmock.NewRows([]string{"owner_index", "eligible"}).AddRow(int64(1), true).AddRow(int64(3), true))
		}, expected: []bool{false, false}, expectErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "initialize ownership query mock")
			defer mock.Close()
			owners := []models.PreviewInfrastructureOwner{
				{OrgID: uuid.New(), PreviewID: uuid.New(), SessionID: uuid.New(), Handle: "handle-1", WorkerNodeID: "worker-current"},
				{OrgID: uuid.New(), PreviewID: uuid.New(), Handle: "handle-2", WorkerNodeID: "worker-old"},
			}
			tt.setup(mock)
			actual, err := NewPreviewStore(mock).ResolvePreviewInfrastructureCleanup(context.Background(), owners, "worker-current", time.Now().Add(-90*time.Second))
			if tt.expectErr {
				require.Error(t, err, "failed ownership resolution should surface its error")
			} else {
				require.NoError(t, err, "successful ownership resolution should return decisions")
			}
			require.Equal(t, map[models.PreviewInfrastructureOwner]bool{owners[0]: tt.expected[0], owners[1]: tt.expected[1]}, actual, "batch should return the explicit decision for every owner")
			require.NoError(t, mock.ExpectationsWereMet(), "ownership resolution should use one batch query")
		})
	}
}

func TestResolvePreviewInfrastructureCleanupInvalidIdentity(t *testing.T) {
	t.Parallel()
	owner := models.PreviewInfrastructureOwner{OrgID: uuid.New(), Handle: "handle-1", WorkerNodeID: "worker-current"}
	actual, err := NewPreviewStore(nil).ResolvePreviewInfrastructureCleanup(context.Background(), []models.PreviewInfrastructureOwner{owner}, "worker-current", time.Now())
	require.NoError(t, err, "invalid identities should be protected without accessing the database")
	require.Equal(t, map[models.PreviewInfrastructureOwner]bool{owner: false}, actual, "missing preview identity must never be eligible")
}

func TestCapturePreviewRuntimeRecoveryCutoff(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		failure bool
	}{
		{name: "database clock returned"},
		{name: "database failure propagated", failure: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "initialize startup clock mock")
			defer mock.Close()
			cutoff := time.Now().UTC()
			expected := cutoff
			expectation := mock.ExpectQuery(regexp.QuoteMeta("SELECT clock_timestamp()"))
			if tt.failure {
				expectation.WillReturnError(errors.New("clock query failed"))
				expected = time.Time{}
			} else {
				expectation.WillReturnRows(pgxmock.NewRows([]string{"clock_timestamp"}).AddRow(cutoff))
			}
			actual, err := NewPreviewStore(mock).CapturePreviewRuntimeRecoveryCutoff(context.Background())
			if tt.failure {
				require.Error(t, err, "startup must fail if its database clock fence cannot be captured")
			} else {
				require.NoError(t, err, "startup should capture a database clock fence")
			}
			require.Equal(t, expected, actual, "startup cutoff should use the exact database timestamp")
			require.NoError(t, mock.ExpectationsWereMet(), "database clock expectation should be met")
		})
	}
}

func TestRecoverPreviewRuntimesAfterWorkerRestart(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		rows    int64
		failure bool
	}{
		{name: "old current generation retired", rows: 2},
		{name: "new runtime preserves preview", rows: 0},
		{name: "recovery error propagated", failure: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "initialize restart recovery mock")
			defer mock.Close()
			expectation := mock.ExpectExec(regexp.QuoteMeta(recoverPreviewRuntimesAfterWorkerRestartQuery)).WithArgs(previewAnyArgs(3)...)
			if tt.failure {
				expectation.WillReturnError(errors.New("restart recovery failed"))
			} else {
				expectation.WillReturnResult(pgxmock.NewResult("UPDATE", tt.rows))
			}
			actual, err := NewPreviewStore(mock).RecoverPreviewRuntimesAfterWorkerRestart(context.Background(), "worker-current", time.Now())
			if tt.failure {
				require.Error(t, err, "restart recovery failure must be visible to startup")
			} else {
				require.NoError(t, err, "restart recovery should complete for the current worker")
			}
			require.Equal(t, tt.rows, actual, "restart recovery should report unavailable previews")
			require.NoError(t, mock.ExpectationsWereMet(), "restart recovery should execute one fenced query")
		})
	}
}
