package db

import (
	"context"
	"encoding/json"
	"errors"
	"math"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

func TestExecutionEpochMetadata_Reads(t *testing.T) {
	t.Parallel()

	targets := []struct {
		name       string
		query      string
		columns    []string
		executor   bool
		unthreaded bool
		read       func(context.Context, pgxmock.PgxPoolIface, uuid.UUID, uuid.UUID, *uuid.UUID, uuid.UUID) (ExecutionEpochMetadata, error)
	}{
		{
			name: "runtime",
			query: `SELECT admission_session_epoch, admission_thread_epoch, drain_proof
				FROM thread_runtimes
				WHERE org_id = $1 AND session_id = $2 AND thread_id = $3 AND id = $4`,
			columns: []string{"admission_session_epoch", "admission_thread_epoch", "drain_proof"},
			read: func(ctx context.Context, mock pgxmock.PgxPoolIface, orgID, sessionID uuid.UUID, threadID *uuid.UUID, resourceID uuid.UUID) (ExecutionEpochMetadata, error) {
				metadata, err := NewThreadRuntimeStore(mock).GetLifecycleMetadata(ctx, orgID, sessionID, *threadID, resourceID)
				return ExecutionEpochMetadata{AdmissionSessionEpoch: metadata.AdmissionSessionEpoch, AdmissionThreadEpoch: metadata.AdmissionThreadEpoch}, err
			},
		},
		{
			name: "threaded executor",
			query: `SELECT admission_session_epoch, admission_thread_epoch
				FROM session_executors
				WHERE org_id = $1 AND session_id = $2 AND thread_id IS NOT DISTINCT FROM $3::uuid AND id = $4`,
			columns: []string{"admission_session_epoch", "admission_thread_epoch"}, executor: true,
			read: func(ctx context.Context, mock pgxmock.PgxPoolIface, orgID, sessionID uuid.UUID, threadID *uuid.UUID, resourceID uuid.UUID) (ExecutionEpochMetadata, error) {
				return NewSessionExecutorStore(mock).GetLifecycleMetadata(ctx, orgID, sessionID, threadID, resourceID)
			},
		},
		{
			name: "exact unthreaded executor",
			query: `SELECT admission_session_epoch, admission_thread_epoch
				FROM session_executors
				WHERE org_id = $1 AND session_id = $2 AND thread_id IS NOT DISTINCT FROM $3::uuid AND id = $4`,
			columns: []string{"admission_session_epoch", "admission_thread_epoch"}, executor: true, unthreaded: true,
			read: func(ctx context.Context, mock pgxmock.PgxPoolIface, orgID, sessionID uuid.UUID, threadID *uuid.UUID, resourceID uuid.UUID) (ExecutionEpochMetadata, error) {
				return NewSessionExecutorStore(mock).GetLifecycleMetadata(ctx, orgID, sessionID, threadID, resourceID)
			},
		},
		{
			name: "inbox batch",
			query: `SELECT admission_session_epoch, admission_thread_epoch
				FROM thread_inbox_delivery_batches
				WHERE org_id = $1 AND session_id = $2 AND thread_id = $3 AND id = $4`,
			columns: []string{"admission_session_epoch", "admission_thread_epoch"},
			read: func(ctx context.Context, mock pgxmock.PgxPoolIface, orgID, sessionID uuid.UUID, threadID *uuid.UUID, resourceID uuid.UUID) (ExecutionEpochMetadata, error) {
				return NewSessionActivityPhaseStore(mock).GetInboxBatchLifecycleMetadata(ctx, orgID, sessionID, *threadID, resourceID)
			},
		},
	}
	for _, target := range targets {
		t.Run(target.name, func(t *testing.T) {
			t.Parallel()

			tests := []struct {
				name       string
				fixture    func() ([]any, ExecutionEpochMetadata)
				missing    bool
				queryError bool
				scanError  bool
			}{
				{
					name: "absent epochs stay unknown",
					fixture: func() ([]any, ExecutionEpochMetadata) {
						return []any{nil, nil}, ExecutionEpochMetadata{}
					},
				},
				{
					name: "present zero differs from absent epochs",
					fixture: func() ([]any, ExecutionEpochMetadata) {
						session, thread := int64(0), int64(0)
						return []any{&session, &thread}, ExecutionEpochMetadata{AdmissionSessionEpoch: &session, AdmissionThreadEpoch: &thread}
					},
				},
				{
					name: "large epochs retain exact independent values",
					fixture: func() ([]any, ExecutionEpochMetadata) {
						session, thread := int64(math.MaxInt64-1), int64(math.MaxInt64)
						return []any{&session, &thread}, ExecutionEpochMetadata{AdmissionSessionEpoch: &session, AdmissionThreadEpoch: &thread}
					},
				},
				{
					name: "missing thread epoch is not inferred",
					fixture: func() ([]any, ExecutionEpochMetadata) {
						session := int64(23)
						return []any{&session, nil}, ExecutionEpochMetadata{AdmissionSessionEpoch: &session}
					},
				},
				{
					name: "missing session epoch is not inferred",
					fixture: func() ([]any, ExecutionEpochMetadata) {
						thread := int64(31)
						return []any{nil, &thread}, ExecutionEpochMetadata{AdmissionThreadEpoch: &thread}
					},
				},
				{name: "missing exact resource", missing: true},
				{name: "query failure preserves its cause", queryError: true},
				{
					name: "late epoch scan failure discards earlier state",
					fixture: func() ([]any, ExecutionEpochMetadata) {
						session := int64(42)
						return []any{&session, "invalid-thread-epoch"}, ExecutionEpochMetadata{}
					},
					scanError: true,
				},
			}
			for _, tt := range tests {
				t.Run(tt.name, func(t *testing.T) {
					t.Parallel()

					mock, err := pgxmock.NewPool(pgxmock.QueryMatcherOption(pgxmock.QueryMatcherEqual))
					require.NoError(t, err, "create a fresh scoped execution metadata fixture")
					defer mock.Close()

					orgID, sessionID, threadID, resourceID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
					thread := &threadID
					if target.unthreaded {
						thread = nil
					}
					var expectedThread any = threadID
					if target.executor {
						expectedThread = thread
					}
					databaseErr := errors.New("database unavailable")
					expectation := mock.ExpectQuery(target.query).WithArgs(orgID, sessionID, expectedThread, resourceID)
					want := ExecutionEpochMetadata{}
					if tt.queryError {
						expectation.WillReturnError(databaseErr)
					} else {
						rows := pgxmock.NewRows(target.columns)
						if !tt.missing {
							var row []any
							row, want = tt.fixture()
							if len(target.columns) == 3 {
								row = append(row, nil)
							}
							rows.AddRow(row...)
						}
						expectation.WillReturnRows(rows)
					}

					actual, err := target.read(context.Background(), mock, orgID, sessionID, thread, resourceID)
					switch {
					case tt.missing:
						require.ErrorIs(t, err, pgx.ErrNoRows, "missing scoped execution metadata must preserve the no-row error")
					case tt.queryError:
						require.ErrorIs(t, err, databaseErr, "passive execution reads must preserve database failure identity")
					case tt.scanError:
						require.Error(t, err, "invalid epoch data must fail the entire metadata read")
					default:
						require.NoError(t, err, "passive metadata should round-trip without inference or lifecycle decisions")
					}
					require.Equal(t, want, actual, "metadata must preserve independent nullable epochs and return an empty result on failure")
					require.NoError(t, mock.ExpectationsWereMet(), "the sole read must bind exact tenant session lane and resource without mutations")
				})
			}
		})
	}
}

func TestThreadRuntimeStore_GetLifecycleMetadata_DrainProof(t *testing.T) {
	t.Parallel()

	const expectedSQL = `
		SELECT admission_session_epoch, admission_thread_epoch, drain_proof
		FROM thread_runtimes
		WHERE org_id = $1 AND session_id = $2 AND thread_id = $3 AND id = $4`
	tests := []struct {
		name      string
		proof     json.RawMessage
		scanError bool
	}{
		{name: "SQL null stays absent"},
		{name: "JSON null stays present", proof: json.RawMessage(`null`)},
		{name: "opaque object is not interpreted", proof: json.RawMessage(`{"synthetic":"future-proof"}`)},
		{name: "future JSON array is not rejected", proof: json.RawMessage(`["future",null]`)},
		{name: "future JSON scalar is not rejected", proof: json.RawMessage(`42`)},
		{name: "proof scan failure clears earlier epochs", scanError: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool(pgxmock.QueryMatcherOption(pgxmock.QueryMatcherEqual))
			require.NoError(t, err, "create an isolated raw drain metadata fixture")
			defer mock.Close()

			orgID, sessionID, threadID, runtimeID := uuid.New(), uuid.New(), uuid.New(), uuid.New()
			sessionEpoch, threadEpoch := int64(41), int64(43)
			var proof any = tt.proof
			if tt.proof == nil {
				proof = nil
			}
			want := RuntimeLifecycleMetadata{AdmissionSessionEpoch: &sessionEpoch, AdmissionThreadEpoch: &threadEpoch, DrainProof: tt.proof}
			if tt.scanError {
				proof = map[string]string{"invalid": "JSON representation"}
				want = RuntimeLifecycleMetadata{}
			}
			mock.ExpectQuery(expectedSQL).WithArgs(orgID, sessionID, threadID, runtimeID).
				WillReturnRows(pgxmock.NewRows([]string{"admission_session_epoch", "admission_thread_epoch", "drain_proof"}).AddRow(&sessionEpoch, &threadEpoch, proof))

			actual, err := NewThreadRuntimeStore(mock).GetLifecycleMetadata(context.Background(), orgID, sessionID, threadID, runtimeID)
			if tt.scanError {
				require.Error(t, err, "invalid proof column data must fail the entire runtime metadata read")
			} else {
				require.NoError(t, err, "raw JSON metadata must be observable without claiming that the runtime drained")
			}
			require.Equal(t, want, actual, "runtime metadata must distinguish SQL null from JSON null and clear every field on scan errors")
			require.NoError(t, mock.ExpectationsWereMet(), "raw proof reads must remain scoped and perform no authority or lifecycle writes")
		})
	}
}
