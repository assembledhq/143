package db

import (
	"context"
	"errors"
	"math"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

func TestLifecycleEpochStore_Reads(t *testing.T) {
	t.Parallel()

	stores := []struct {
		name  string
		query string
		read  func(context.Context, *LifecycleEpochStore, uuid.UUID, uuid.UUID, uuid.UUID) (LifecycleEpochState, error)
		args  func(uuid.UUID, uuid.UUID, uuid.UUID) []any
	}{
		{
			name: "session",
			query: `SELECT admission_epoch, end_epoch, lifecycle_version
				FROM sessions
				WHERE org_id = $1 AND id = $2 AND deleted_at IS NULL`,
			read: func(ctx context.Context, store *LifecycleEpochStore, orgID, sessionID, _ uuid.UUID) (LifecycleEpochState, error) {
				return store.GetSession(ctx, orgID, sessionID)
			},
			args: func(orgID, sessionID, _ uuid.UUID) []any { return []any{orgID, sessionID} },
		},
		{
			name: "thread",
			query: `SELECT admission_epoch, end_epoch, lifecycle_version
				FROM session_threads
				WHERE org_id = $1 AND session_id = $2 AND id = $3 AND archived_at IS NULL`,
			read: func(ctx context.Context, store *LifecycleEpochStore, orgID, sessionID, threadID uuid.UUID) (LifecycleEpochState, error) {
				return store.GetThread(ctx, orgID, sessionID, threadID)
			},
			args: func(orgID, sessionID, threadID uuid.UUID) []any { return []any{orgID, sessionID, threadID} },
		},
	}

	for _, target := range stores {
		t.Run(target.name, func(t *testing.T) {
			t.Parallel()

			tests := []struct {
				name       string
				row        func() []any
				want       func() LifecycleEpochState
				queryError bool
				missing    bool
				scanError  bool
			}{
				{
					name: "default metadata preserves absent end epoch",
					row:  func() []any { return []any{int64(0), nil, int16(0)} },
					want: func() LifecycleEpochState { return LifecycleEpochState{} },
				},
				{
					name: "large epoch values are returned unchanged",
					row: func() []any {
						endEpoch := int64(math.MaxInt64)
						return []any{int64(math.MaxInt64 - 1), &endEpoch, int16(7)}
					},
					want: func() LifecycleEpochState {
						endEpoch := int64(math.MaxInt64)
						return LifecycleEpochState{AdmissionEpoch: math.MaxInt64 - 1, EndEpoch: &endEpoch, LifecycleVersion: 7}
					},
				},
				{
					name: "present zero end epoch differs from absent end epoch",
					row: func() []any {
						endEpoch := int64(0)
						return []any{int64(0), &endEpoch, int16(0)}
					},
					want: func() LifecycleEpochState {
						endEpoch := int64(0)
						return LifecycleEpochState{EndEpoch: &endEpoch}
					},
				},
				{name: "missing or invisible row", missing: true},
				{name: "query failure", queryError: true},
				{
					name: "scan failure discards partially read metadata",
					row: func() []any {
						endEpoch := int64(43)
						return []any{int64(42), &endEpoch, "invalid-version"}
					},
					scanError: true,
				},
			}

			for _, tt := range tests {
				t.Run(tt.name, func(t *testing.T) {
					t.Parallel()

					mock, err := pgxmock.NewPool(pgxmock.QueryMatcherOption(pgxmock.QueryMatcherEqual))
					require.NoError(t, err, "create an independent passive epoch query fixture")
					defer mock.Close()

					orgID, sessionID, threadID := uuid.New(), uuid.New(), uuid.New()
					databaseErr := errors.New("database unavailable")
					expectation := mock.ExpectQuery(target.query).WithArgs(target.args(orgID, sessionID, threadID)...)
					if tt.queryError {
						expectation.WillReturnError(databaseErr)
					} else {
						rows := pgxmock.NewRows([]string{"admission_epoch", "end_epoch", "lifecycle_version"})
						if !tt.missing {
							rows.AddRow(tt.row()...)
						}
						expectation.WillReturnRows(rows)
					}

					state, err := target.read(context.Background(), NewLifecycleEpochStore(mock), orgID, sessionID, threadID)
					switch {
					case tt.missing:
						require.ErrorIs(t, err, pgx.ErrNoRows, "invisible or missing metadata must preserve the no-row error")
					case tt.queryError:
						require.ErrorIs(t, err, databaseErr, "passive reads must preserve the underlying database failure")
					case tt.scanError:
						require.Error(t, err, "invalid persisted column data must fail the passive read")
					default:
						require.NoError(t, err, "visible metadata should be returned without changing its values")
					}
					want := LifecycleEpochState{}
					if tt.want != nil {
						want = tt.want()
					}
					require.Equal(t, want, state, "the read must return exact metadata or an empty state when any part fails")
					require.NoError(t, mock.ExpectationsWereMet(), "the sole query must enforce tenant and row scope without locks or writes")
				})
			}
		})
	}
}
