package db

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

// Exact matching makes these expectations a query contract: only the explicit
// telemetry fields are selected, leases use the database clock, and backend
// filtering retains restricted observations while excluding this collector.
const lifecycleCensusWritersExpectedSQL = `
	SELECT statement_timestamp() AS observed_at,
	       writer.instance_id IS NOT NULL AS present,
	       writer.role, writer.build_sha, writer.lifecycle_writer_version AS version
	FROM (SELECT 1) AS observation
	LEFT JOIN LATERAL (
		SELECT instance_id, role, build_sha, lifecycle_writer_version, expires_at
		FROM lifecycle_writer_instances
		WHERE expires_at > statement_timestamp()
		ORDER BY expires_at, instance_id
		LIMIT 1001
	) AS writer ON true
	ORDER BY writer.expires_at, writer.instance_id`

const lifecycleCensusConnectionsExpectedSQL = `
	WITH observation AS MATERIALIZED (
		SELECT statement_timestamp() AS observed_at,
		       pg_has_role(current_user, 'pg_read_all_stats', 'USAGE') AS full_visibility
	)
	SELECT observation.observed_at, observation.full_visibility,
	       activity.pid IS NOT NULL AS present,
	       activity.application_name, activity.backend_type, activity.state, activity.xact_start,
	       COALESCE(observation.full_visibility OR pg_has_role(activity.usesysid, 'USAGE'), false) AS details_visible
	FROM observation
	LEFT JOIN LATERAL (
		SELECT pid, usesysid, application_name, backend_type, state, xact_start
		FROM pg_stat_activity
		WHERE datid = (SELECT oid FROM pg_database WHERE datname = current_database())
		  AND pid <> pg_backend_pid()
		  AND (backend_type = 'client backend' OR backend_type IS NULL)
		ORDER BY pid
		LIMIT 1001
	) AS activity ON true
	ORDER BY activity.pid`

func TestLifecycleCensusStoreWriters(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		fixture     func(time.Time) (*pgxmock.Rows, LifecycleCensusWriterSnapshot)
		queryErr    error
		wantErr     string
		errContains bool
	}{
		{
			name: "zero sentinel preserves the database clock",
			fixture: func(observedAt time.Time) (*pgxmock.Rows, LifecycleCensusWriterSnapshot) {
				return newLifecycleCensusWriterTestRows().AddRow(observedAt, false, nil, nil, nil),
					LifecycleCensusWriterSnapshot{ObservedAt: observedAt, Rows: []LifecycleCensusWriter{}}
			},
		},
		{
			name: "preserves raw roles builds and nonnegative future versions for safe report normalization",
			fixture: func(observedAt time.Time) (*pgxmock.Rows, LifecycleCensusWriterSnapshot) {
				return newLifecycleCensusWriterTestRows().
						AddRow(observedAt, true, lifecycleCensusTestPointer("api"), lifecycleCensusTestPointer("dev"), lifecycleCensusTestPointer(0)).
						AddRow(observedAt, true, lifecycleCensusTestPointer("future_role"), lifecycleCensusTestPointer("arbitrary synthetic metadata"), lifecycleCensusTestPointer(17)),
					LifecycleCensusWriterSnapshot{ObservedAt: observedAt, Rows: []LifecycleCensusWriter{
						{Role: "api", BuildSHA: "dev", Version: 0},
						{Role: "future_role", BuildSHA: "arbitrary synthetic metadata", Version: 17},
					}}
			},
		},
		{
			name:     "query failure preserves the cause and returns no partial snapshot",
			queryErr: context.DeadlineExceeded,
			wantErr:  "read passive writer observations: context deadline exceeded",
		},
		{
			name: "row iteration failure discards an earlier observation",
			fixture: func(observedAt time.Time) (*pgxmock.Rows, LifecycleCensusWriterSnapshot) {
				return newLifecycleCensusWriterTestRows().
					AddRow(observedAt, true, lifecycleCensusTestPointer("api"), lifecycleCensusTestPointer("dev"), lifecycleCensusTestPointer(0)).
					AddRow(observedAt, true, lifecycleCensusTestPointer("worker"), lifecycleCensusTestPointer("dev"), lifecycleCensusTestPointer(0)).
					RowError(1, errors.New("synthetic row failure")), LifecycleCensusWriterSnapshot{}
			},
			wantErr: "scan passive writer observations: synthetic row failure",
		},
		{
			name: "scan failure returns no report",
			fixture: func(observedAt time.Time) (*pgxmock.Rows, LifecycleCensusWriterSnapshot) {
				return newLifecycleCensusWriterTestRows().AddRow(observedAt, true, lifecycleCensusTestPointer("api"), lifecycleCensusTestPointer("dev"), "not-an-integer"), LifecycleCensusWriterSnapshot{}
			},
			wantErr: "scan passive writer observations:", errContains: true,
		},
		{
			name: "missing role rejects an incomplete present observation",
			fixture: func(observedAt time.Time) (*pgxmock.Rows, LifecycleCensusWriterSnapshot) {
				return newLifecycleCensusWriterTestRows().AddRow(observedAt, true, nil, lifecycleCensusTestPointer("dev"), lifecycleCensusTestPointer(0)), LifecycleCensusWriterSnapshot{}
			},
			wantErr: "incomplete passive writer observation",
		},
		{
			name: "missing build rejects an incomplete present observation",
			fixture: func(observedAt time.Time) (*pgxmock.Rows, LifecycleCensusWriterSnapshot) {
				return newLifecycleCensusWriterTestRows().AddRow(observedAt, true, lifecycleCensusTestPointer("api"), nil, lifecycleCensusTestPointer(0)), LifecycleCensusWriterSnapshot{}
			},
			wantErr: "incomplete passive writer observation",
		},
		{
			name: "missing version rejects an incomplete present observation",
			fixture: func(observedAt time.Time) (*pgxmock.Rows, LifecycleCensusWriterSnapshot) {
				return newLifecycleCensusWriterTestRows().AddRow(observedAt, true, lifecycleCensusTestPointer("api"), lifecycleCensusTestPointer("dev"), nil), LifecycleCensusWriterSnapshot{}
			},
			wantErr: "incomplete passive writer observation",
		},
		{
			name: "no result row cannot invent a local observation clock",
			fixture: func(time.Time) (*pgxmock.Rows, LifecycleCensusWriterSnapshot) {
				return newLifecycleCensusWriterTestRows(), LifecycleCensusWriterSnapshot{}
			},
			wantErr: "missing passive writer observation clock",
		},
		{
			name: "zero clock in sentinel cannot become successful empty inventory",
			fixture: func(time.Time) (*pgxmock.Rows, LifecycleCensusWriterSnapshot) {
				return newLifecycleCensusWriterTestRows().AddRow(time.Time{}, false, nil, nil, nil), LifecycleCensusWriterSnapshot{}
			},
			wantErr: "missing passive writer observation clock",
		},
		{
			name: "exactly one thousand rows is not truncated",
			fixture: func(observedAt time.Time) (*pgxmock.Rows, LifecycleCensusWriterSnapshot) {
				return lifecycleCensusWriterLimitFixture(observedAt, 1000)
			},
		},
		{
			name: "one extra row detects overflow without returning the extra observation",
			fixture: func(observedAt time.Time) (*pgxmock.Rows, LifecycleCensusWriterSnapshot) {
				return lifecycleCensusWriterLimitFixture(observedAt, 1001)
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock, err := pgxmock.NewPool(pgxmock.QueryMatcherOption(pgxmock.QueryMatcherEqual))
			require.NoError(t, err, "create an isolated passive writer census fixture")
			defer mock.Close()
			expectation := mock.ExpectQuery(lifecycleCensusWritersExpectedSQL)
			var expected LifecycleCensusWriterSnapshot
			if tt.queryErr != nil {
				expectation.WillReturnError(tt.queryErr)
			} else {
				rows, wanted := tt.fixture(time.Date(2026, time.October, 19, 12, 0, 0, 123000000, time.UTC))
				expected = wanted
				expectation.WillReturnRows(rows).RowsWillBeClosed()
			}
			actual, err := NewLifecycleCensusStore(mock).Writers(context.Background())
			if tt.wantErr == "" {
				require.NoError(t, err, "bounded writer census should return its database-clock snapshot")
			} else if tt.errContains {
				require.ErrorContains(t, err, tt.wantErr, "unscannable writer telemetry must fail with collection-stage context")
			} else {
				require.EqualError(t, err, tt.wantErr, "writer collection errors must not turn into successful empty inventory")
			}
			if tt.queryErr != nil {
				require.ErrorIs(t, err, tt.queryErr, "writer query failures must preserve cancellation and database causes")
			}
			require.Equal(t, expected, actual, "writer census must return the exact bounded snapshot or a zero snapshot on failure")
			require.NoError(t, mock.ExpectationsWereMet(), "writer query must preserve its explicit fields database-clock expiry deterministic ordering and 1001-row SQL bound, then close rows")
		})
	}
}

func TestLifecycleCensusStoreConnections(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		fixture     func(time.Time) (*pgxmock.Rows, LifecycleCensusConnectionSnapshot)
		queryErr    error
		wantErr     string
		errContains bool
	}{
		{
			name: "empty restricted source retains its database clock and visibility",
			fixture: func(observedAt time.Time) (*pgxmock.Rows, LifecycleCensusConnectionSnapshot) {
				return newLifecycleCensusConnectionTestRows().AddRow(observedAt, lifecycleCensusTestPointer(false), false, nil, nil, nil, nil, false),
					LifecycleCensusConnectionSnapshot{ObservedAt: observedAt, FullVisibility: lifecycleCensusTestPointer(false), Rows: []LifecycleCensusConnection{}}
			},
		},
		{
			name: "empty source retains unknown global visibility",
			fixture: func(observedAt time.Time) (*pgxmock.Rows, LifecycleCensusConnectionSnapshot) {
				return newLifecycleCensusConnectionTestRows().AddRow(observedAt, nil, false, nil, nil, nil, nil, false),
					LifecycleCensusConnectionSnapshot{ObservedAt: observedAt, Rows: []LifecycleCensusConnection{}}
			},
		},
		{
			name: "visible client transaction retains exact state and database transaction time",
			fixture: func(observedAt time.Time) (*pgxmock.Rows, LifecycleCensusConnectionSnapshot) {
				started := observedAt.Add(-time.Minute)
				return newLifecycleCensusConnectionTestRows().AddRow(observedAt, lifecycleCensusTestPointer(true), true, lifecycleCensusTestPointer("143:worker:dev"), lifecycleCensusTestPointer("client backend"), lifecycleCensusTestPointer("idle in transaction"), &started, true),
					LifecycleCensusConnectionSnapshot{ObservedAt: observedAt, FullVisibility: lifecycleCensusTestPointer(true), Rows: []LifecycleCensusConnection{
						{ApplicationName: "143:worker:dev", BackendType: lifecycleCensusTestPointer("client backend"), State: lifecycleCensusTestPointer("idle in transaction"), XactStart: &started, DetailsVisible: true},
					}}
			},
		},
		{
			name: "masked backend type state and transaction remain unknown",
			fixture: func(observedAt time.Time) (*pgxmock.Rows, LifecycleCensusConnectionSnapshot) {
				return newLifecycleCensusConnectionTestRows().AddRow(observedAt, lifecycleCensusTestPointer(false), true, lifecycleCensusTestPointer("143:api:dev"), nil, nil, nil, false),
					LifecycleCensusConnectionSnapshot{ObservedAt: observedAt, FullVisibility: lifecycleCensusTestPointer(false), Rows: []LifecycleCensusConnection{
						{ApplicationName: "143:api:dev", DetailsVisible: false},
					}}
			},
		},
		{
			name: "own role visibility does not upgrade restricted global visibility",
			fixture: func(observedAt time.Time) (*pgxmock.Rows, LifecycleCensusConnectionSnapshot) {
				return newLifecycleCensusConnectionTestRows().AddRow(observedAt, lifecycleCensusTestPointer(false), true, nil, lifecycleCensusTestPointer("client backend"), lifecycleCensusTestPointer("idle"), nil, true),
					LifecycleCensusConnectionSnapshot{ObservedAt: observedAt, FullVisibility: lifecycleCensusTestPointer(false), Rows: []LifecycleCensusConnection{
						{BackendType: lifecycleCensusTestPointer("client backend"), State: lifecycleCensusTestPointer("idle"), DetailsVisible: true},
					}}
			},
		},
		{
			name: "visible disabled activity retains null transaction details",
			fixture: func(observedAt time.Time) (*pgxmock.Rows, LifecycleCensusConnectionSnapshot) {
				return newLifecycleCensusConnectionTestRows().AddRow(observedAt, lifecycleCensusTestPointer(true), true, lifecycleCensusTestPointer("synthetic untrusted name"), lifecycleCensusTestPointer("client backend"), lifecycleCensusTestPointer("disabled"), nil, true),
					LifecycleCensusConnectionSnapshot{ObservedAt: observedAt, FullVisibility: lifecycleCensusTestPointer(true), Rows: []LifecycleCensusConnection{
						{ApplicationName: "synthetic untrusted name", BackendType: lifecycleCensusTestPointer("client backend"), State: lifecycleCensusTestPointer("disabled"), DetailsVisible: true},
					}}
			},
		},
		{
			name:     "query cancellation preserves the cause and returns no partial snapshot",
			queryErr: context.Canceled,
			wantErr:  "read passive connection observations: context canceled",
		},
		{
			name: "row iteration failure discards an earlier client observation",
			fixture: func(observedAt time.Time) (*pgxmock.Rows, LifecycleCensusConnectionSnapshot) {
				return newLifecycleCensusConnectionTestRows().
					AddRow(observedAt, lifecycleCensusTestPointer(true), true, lifecycleCensusTestPointer("143:worker:dev"), lifecycleCensusTestPointer("client backend"), lifecycleCensusTestPointer("idle"), nil, true).
					AddRow(observedAt, lifecycleCensusTestPointer(true), true, lifecycleCensusTestPointer("143:api:dev"), lifecycleCensusTestPointer("client backend"), lifecycleCensusTestPointer("idle"), nil, true).
					RowError(1, errors.New("synthetic row failure")), LifecycleCensusConnectionSnapshot{}
			},
			wantErr: "scan passive connection observations: synthetic row failure",
		},
		{
			name: "scan failure returns no report",
			fixture: func(observedAt time.Time) (*pgxmock.Rows, LifecycleCensusConnectionSnapshot) {
				return newLifecycleCensusConnectionTestRows().AddRow(observedAt, lifecycleCensusTestPointer(true), true, lifecycleCensusTestPointer("143:api:dev"), lifecycleCensusTestPointer("client backend"), lifecycleCensusTestPointer("idle"), "not-a-time", true), LifecycleCensusConnectionSnapshot{}
			},
			wantErr: "scan passive connection observations:", errContains: true,
		},
		{
			name: "missing result row cannot invent a local observation clock",
			fixture: func(time.Time) (*pgxmock.Rows, LifecycleCensusConnectionSnapshot) {
				return newLifecycleCensusConnectionTestRows(), LifecycleCensusConnectionSnapshot{}
			},
			wantErr: "missing passive connection observation clock",
		},
		{
			name: "zero sentinel clock cannot become successful empty inventory",
			fixture: func(time.Time) (*pgxmock.Rows, LifecycleCensusConnectionSnapshot) {
				return newLifecycleCensusConnectionTestRows().AddRow(time.Time{}, lifecycleCensusTestPointer(true), false, nil, nil, nil, nil, false), LifecycleCensusConnectionSnapshot{}
			},
			wantErr: "missing passive connection observation clock",
		},
		{
			name: "exactly one thousand clients is not truncated",
			fixture: func(observedAt time.Time) (*pgxmock.Rows, LifecycleCensusConnectionSnapshot) {
				return lifecycleCensusConnectionLimitFixture(observedAt, 1000)
			},
		},
		{
			name: "one extra client marks overflow without returning the extra observation",
			fixture: func(observedAt time.Time) (*pgxmock.Rows, LifecycleCensusConnectionSnapshot) {
				return lifecycleCensusConnectionLimitFixture(observedAt, 1001)
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock, err := pgxmock.NewPool(pgxmock.QueryMatcherOption(pgxmock.QueryMatcherEqual))
			require.NoError(t, err, "create an isolated passive connection census fixture")
			defer mock.Close()
			expectation := mock.ExpectQuery(lifecycleCensusConnectionsExpectedSQL)
			var expected LifecycleCensusConnectionSnapshot
			if tt.queryErr != nil {
				expectation.WillReturnError(tt.queryErr)
			} else {
				rows, wanted := tt.fixture(time.Date(2026, time.October, 19, 12, 0, 0, 123000000, time.UTC))
				expected = wanted
				expectation.WillReturnRows(rows).RowsWillBeClosed()
			}
			actual, err := NewLifecycleCensusStore(mock).Connections(context.Background())
			if tt.wantErr == "" {
				require.NoError(t, err, "bounded connection census should preserve database observation and visibility metadata")
			} else if tt.errContains {
				require.ErrorContains(t, err, tt.wantErr, "unscannable activity telemetry must fail with collection-stage context")
			} else {
				require.EqualError(t, err, tt.wantErr, "connection collection errors must not turn into successful empty inventory")
			}
			if tt.queryErr != nil {
				require.ErrorIs(t, err, tt.queryErr, "connection query failures must preserve cancellation and database causes")
			}
			require.Equal(t, expected, actual, "connection census must return the exact bounded snapshot preserving all nullable observations or a zero snapshot on failure")
			require.NoError(t, mock.ExpectationsWereMet(), "activity SQL must retain masked backend types, exclude this PID and other databases, bound results at 1001 and omit query text before closing rows")
		})
	}
}

func newLifecycleCensusWriterTestRows() *pgxmock.Rows {
	return pgxmock.NewRows([]string{"observed_at", "present", "role", "build_sha", "version"})
}

func newLifecycleCensusConnectionTestRows() *pgxmock.Rows {
	return pgxmock.NewRows([]string{"observed_at", "full_visibility", "present", "application_name", "backend_type", "state", "xact_start", "details_visible"})
}

func lifecycleCensusWriterLimitFixture(observedAt time.Time, count int) (*pgxmock.Rows, LifecycleCensusWriterSnapshot) {
	rows := newLifecycleCensusWriterTestRows()
	expected := LifecycleCensusWriterSnapshot{ObservedAt: observedAt, Truncated: count > 1000, Rows: []LifecycleCensusWriter{}}
	for i := range count {
		build := fmt.Sprintf("synthetic-build-%04d", i)
		rows.AddRow(observedAt, true, lifecycleCensusTestPointer("worker"), &build, &i)
		if i < 1000 {
			expected.Rows = append(expected.Rows, LifecycleCensusWriter{Role: "worker", BuildSHA: build, Version: i})
		}
	}
	return rows, expected
}

func lifecycleCensusConnectionLimitFixture(observedAt time.Time, count int) (*pgxmock.Rows, LifecycleCensusConnectionSnapshot) {
	rows := newLifecycleCensusConnectionTestRows()
	expected := LifecycleCensusConnectionSnapshot{ObservedAt: observedAt, FullVisibility: lifecycleCensusTestPointer(false), Truncated: count > 1000, Rows: []LifecycleCensusConnection{}}
	for i := range count {
		name := fmt.Sprintf("synthetic-name-%04d", i)
		rows.AddRow(observedAt, lifecycleCensusTestPointer(false), true, &name, nil, nil, nil, false)
		if i < 1000 {
			expected.Rows = append(expected.Rows, LifecycleCensusConnection{ApplicationName: name, DetailsVisible: false})
		}
	}
	return rows, expected
}

func lifecycleCensusTestPointer[T any](value T) *T { return &value }
