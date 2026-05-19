package db

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
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
				mock.ExpectExec("INSERT INTO session_issue_link_state_events[\\s\\S]+ON CONFLICT \\(session_id, issue_id, event_kind\\) DO NOTHING").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnResult(pgxmock.NewResult("INSERT", 1))
			},
		},
		{
			name: "maps duplicate no-op without aborting transaction",
			setup: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectExec("INSERT INTO session_issue_link_state_events[\\s\\S]+ON CONFLICT \\(session_id, issue_id, event_kind\\) DO NOTHING").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnResult(pgxmock.NewResult("INSERT", 0))
			},
			expectedErr: ErrLinearStateEventExists,
		},
		{
			name: "wraps insert errors",
			setup: func(mock pgxmock.PgxPoolIface) {
				mock.ExpectExec("INSERT INTO session_issue_link_state_events[\\s\\S]+ON CONFLICT \\(session_id, issue_id, event_kind\\) DO NOTHING").
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

// TestLinearStateEventStore_ListBySessionScopesByOrg pins the SQL filter
// that prevents one org's audit log from leaking into another's session
// detail pane. The org_id parameter must reach the WHERE clause so that
// pgxmock's WithArgs matcher rejects the call when it doesn't.
func TestLinearStateEventStore_ListBySessionScopesByOrg(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	orgID := uuid.New()
	sessionID := uuid.New()

	mock.ExpectQuery(`SELECT event_kind[\s\S]+WHERE org_id = @org_id AND session_id = @session_id`).
		WithArgs(pgx.NamedArgs{
			"org_id":     orgID,
			"session_id": sessionID,
			"limit":      25,
		}).
		WillReturnRows(pgxmock.NewRows([]string{"event_kind", "transition_from", "transition_to", "skipped_reason", "created_at"}))

	got, err := NewLinearStateEventStore(mock).ListBySession(context.Background(), orgID, sessionID, 0)
	require.NoError(t, err, "ListBySession should succeed when SQL is parameterized correctly")
	require.Empty(t, got, "no rows returned should produce an empty slice")
	require.NoError(t, mock.ExpectationsWereMet(), "the WHERE clause must include both org_id and session_id parameters")
}
