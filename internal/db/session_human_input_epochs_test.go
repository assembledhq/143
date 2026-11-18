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

func TestLifecycleEpochStore_GetRequest(t *testing.T) {
	t.Parallel()

	const expectedSQL = `
		SELECT admission_session_epoch, admission_thread_epoch
		FROM session_human_input_requests
		WHERE org_id = $1 AND session_id = $2 AND id = $3`
	tests := []struct {
		name       string
		row        func() []any
		want       func() LifecycleRequestEpochState
		missing    bool
		queryError bool
		scanError  bool
	}{
		{
			name: "both absent epochs stay unknown",
			row:  func() []any { return []any{nil, nil} },
			want: func() LifecycleRequestEpochState { return LifecycleRequestEpochState{} },
		},
		{
			name: "present zero epochs remain distinct from absent epochs",
			row: func() []any {
				sessionEpoch, threadEpoch := int64(0), int64(0)
				return []any{&sessionEpoch, &threadEpoch}
			},
			want: func() LifecycleRequestEpochState {
				sessionEpoch, threadEpoch := int64(0), int64(0)
				return LifecycleRequestEpochState{AdmissionSessionEpoch: &sessionEpoch, AdmissionThreadEpoch: &threadEpoch}
			},
		},
		{
			name: "large epoch values are returned unchanged",
			row: func() []any {
				sessionEpoch, threadEpoch := int64(math.MaxInt64-1), int64(math.MaxInt64)
				return []any{&sessionEpoch, &threadEpoch}
			},
			want: func() LifecycleRequestEpochState {
				sessionEpoch, threadEpoch := int64(math.MaxInt64-1), int64(math.MaxInt64)
				return LifecycleRequestEpochState{AdmissionSessionEpoch: &sessionEpoch, AdmissionThreadEpoch: &threadEpoch}
			},
		},
		{
			name: "absent thread epoch stays unknown beside a recorded session epoch",
			row: func() []any {
				sessionEpoch := int64(23)
				return []any{&sessionEpoch, nil}
			},
			want: func() LifecycleRequestEpochState {
				sessionEpoch := int64(23)
				return LifecycleRequestEpochState{AdmissionSessionEpoch: &sessionEpoch}
			},
		},
		{
			name: "absent session epoch stays unknown beside a recorded thread epoch",
			row: func() []any {
				threadEpoch := int64(31)
				return []any{nil, &threadEpoch}
			},
			want: func() LifecycleRequestEpochState {
				threadEpoch := int64(31)
				return LifecycleRequestEpochState{AdmissionThreadEpoch: &threadEpoch}
			},
		},
		{name: "request missing from exact scope", missing: true},
		{name: "query failure preserves its cause", queryError: true},
		{
			name: "late scan failure discards partially read metadata",
			row: func() []any {
				sessionEpoch := int64(42)
				return []any{&sessionEpoch, "invalid-thread-epoch"}
			},
			scanError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool(pgxmock.QueryMatcherOption(pgxmock.QueryMatcherEqual))
			require.NoError(t, err, "create an independent passive request epoch fixture")
			defer mock.Close()

			orgID, sessionID, requestID := uuid.New(), uuid.New(), uuid.New()
			databaseErr := errors.New("database unavailable")
			expectation := mock.ExpectQuery(expectedSQL).WithArgs(orgID, sessionID, requestID)
			if tt.queryError {
				expectation.WillReturnError(databaseErr)
			} else {
				rows := pgxmock.NewRows([]string{"admission_session_epoch", "admission_thread_epoch"})
				if !tt.missing {
					rows.AddRow(tt.row()...)
				}
				expectation.WillReturnRows(rows)
			}

			state, err := NewLifecycleEpochStore(mock).GetRequest(context.Background(), orgID, sessionID, requestID)
			switch {
			case tt.missing:
				require.ErrorIs(t, err, pgx.ErrNoRows, "missing scoped request metadata must preserve the no-row error")
			case tt.queryError:
				require.ErrorIs(t, err, databaseErr, "request epoch reads must preserve the database failure")
			case tt.scanError:
				require.Error(t, err, "invalid persisted epoch data must fail the complete request read")
			default:
				require.NoError(t, err, "scoped request epochs should be returned without inferring absent values")
			}
			want := LifecycleRequestEpochState{}
			if tt.want != nil {
				want = tt.want()
			}
			require.Equal(t, want, state, "request reads must preserve each nullable epoch independently and return an empty state on error")
			require.NoError(t, mock.ExpectationsWereMet(), "the sole read must match exact tenant, session and request scope without filtering or mutations")
		})
	}
}
