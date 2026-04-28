package db

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

func TestLinearStateEventStore_Insert(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		setup       func(mock pgxmock.PgxPoolIface)
		expectedErr error
		expectedMsg string
	}{
		{
			name: "inserts event",
			setup: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectExec("INSERT INTO session_issue_link_state_events").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnResult(pgxmock.NewResult("INSERT", 1))
			},
		},
		{
			name: "maps unique violation",
			setup: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectExec("INSERT INTO session_issue_link_state_events").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnError(&pgconn.PgError{Code: "23505"})
			},
			expectedErr: ErrLinearStateEventExists,
		},
		{
			name: "wraps insert errors",
			setup: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectExec("INSERT INTO session_issue_link_state_events").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnError(errors.New("db unavailable"))
			},
			expectedMsg: "insert linear state event",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "should create mock pool")
			defer mock.Close()

			tt.setup(mock)
			err = NewLinearStateEventStore(mock).Insert(context.Background(), uuid.New(), LinearStateEventInput{
				SessionID:      uuid.New(),
				IssueID:        uuid.New(),
				EventKind:      LinearStateEventPROpened,
				TransitionFrom: "In Progress",
				TransitionTo:   "In Review",
			})
			if tt.expectedErr != nil {
				require.ErrorIs(t, err, tt.expectedErr, "Insert should return the expected sentinel")
			} else if tt.expectedMsg != "" {
				require.Error(t, err, "Insert should return database errors")
				require.Contains(t, err.Error(), tt.expectedMsg, "Insert should wrap database errors")
			} else {
				require.NoError(t, err, "Insert should succeed")
			}
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

func TestLinearStateEventStore_ListBySession(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		limit       int
		setup       func(mock pgxmock.PgxPoolIface)
		expected    []LinearStateEventSummary
		expectedErr string
	}{
		{
			name:  "defaults out of range limit",
			limit: 0,
			setup: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectQuery("SELECT event_kind").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows([]string{"event_kind", "transition_from", "transition_to", "skipped_reason", "created_at"}).
						AddRow("pr_opened", "In Progress", "In Review", "", "2026-04-27T10:00:00Z"))
			},
			expected: []LinearStateEventSummary{{EventKind: "pr_opened", TransitionFrom: "In Progress", TransitionTo: "In Review", CreatedAt: "2026-04-27T10:00:00Z"}},
		},
		{
			name:  "wraps query errors",
			limit: 10,
			setup: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectQuery("SELECT event_kind").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnError(errors.New("db unavailable"))
			},
			expectedErr: "query linear state events",
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "should create mock pool")
			defer mock.Close()

			tt.setup(mock)
			got, err := NewLinearStateEventStore(mock).ListBySession(context.Background(), uuid.New(), uuid.New(), tt.limit)
			if tt.expectedErr != "" {
				require.Error(t, err, "ListBySession should return query errors")
				require.Contains(t, err.Error(), tt.expectedErr, "ListBySession should wrap query errors")
			} else {
				require.NoError(t, err, "ListBySession should succeed")
				require.Equal(t, tt.expected, got, "ListBySession should decode event summaries")
			}
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}
