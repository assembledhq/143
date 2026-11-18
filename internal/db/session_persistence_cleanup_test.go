package db

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"testing"
	"testing/synctest"
	"time"

	"github.com/jackc/pgx/v5/pgconn"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

type sessionPersistenceCleanupRecorder struct{ errors []string }

func (*sessionPersistenceCleanupRecorder) Helper() {}
func (r *sessionPersistenceCleanupRecorder) Errorf(format string, args ...any) {
	r.errors = append(r.errors, fmt.Sprintf(format, args...))
}

type sessionPersistenceCleanupExecutor struct {
	DBTX
	calls    int
	deadline time.Time
	live     bool
}

func (e *sessionPersistenceCleanupExecutor) Exec(ctx context.Context, sql string, args ...any) (pgconn.CommandTag, error) {
	e.calls++
	e.deadline, _ = ctx.Deadline()
	e.live = ctx.Err() == nil
	return e.DBTX.Exec(ctx, sql, args...)
}

func TestDropSessionPersistencePostgresDatabase(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		delay    time.Duration
		failure  error
		elapsed  time.Duration
		wantErrs []string
	}{
		{name: "ordinary success"},
		{name: "completion beyond the old budget", delay: 11 * time.Second, elapsed: 11 * time.Second},
		{
			name: "one failed attempt returns nonfatally", failure: errors.New("synthetic cleanup rejection"),
			wantErrs: []string{"drop only the generated fixture after owned connections close, without forced termination: synthetic cleanup rejection"},
		},
		{
			name: "stalled drop retains a bounded deadline", delay: 2 * time.Minute, elapsed: time.Minute,
			wantErrs: []string{"drop only the generated fixture after owned connections close, without forced termination: context deadline exceeded"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			synctest.Test(t, func(t *testing.T) {
				mock, err := pgxmock.NewPool(pgxmock.QueryMatcherOption(pgxmock.QueryMatcherEqual))
				require.NoError(t, err, "create an isolated administrator mock")
				defer mock.Close()
				// A hostile-looking identifier must remain exactly one quoted name,
				// never a second statement, forced drop, retry or backend termination.
				expectation := mock.ExpectExec(`DROP DATABASE "fixture""; SELECT 1; --"`).
					WithArgs().WillReturnResult(pgxmock.NewResult("DROP DATABASE", 0))
				if tt.failure != nil {
					expectation.WillReturnError(tt.failure)
				}
				expectation.WillDelayFor(tt.delay)
				admin := &sessionPersistenceCleanupExecutor{DBTX: mock}
				reporter := &sessionPersistenceCleanupRecorder{}
				started := time.Now()
				dropSessionPersistencePostgresDatabase(reporter, admin, `fixture"; SELECT 1; --`)
				require.Equal(t, tt.elapsed, time.Since(started), "cleanup should wait only for completion or its independent one-minute budget")
				require.Equal(t, started.Add(time.Minute), admin.deadline, "cleanup must receive a fresh full budget rather than reuse an expired work context")
				require.True(t, admin.live, "the administrator operation must begin with a live cleanup context")
				require.Equal(t, 1, admin.calls, "ordinary DROP must be attempted exactly once even when it fails")
				require.Equal(t, tt.wantErrs, reporter.errors, "cleanup must return after reporting the exact failure without a fatal assertion")
				require.NoError(t, mock.ExpectationsWereMet(), "only the precisely quoted generated database may be dropped")
			})
		})
	}
}

func TestSessionPersistenceCleanupFailureReporting(t *testing.T) {
	t.Parallel()
	const childFlag = "SESSION_PERSISTENCE_CLEANUP_REPORTING_CHILD"
	const laterCleanup = "synthetic later cleanup completed"
	if os.Getenv(childFlag) == "1" {
		mock, err := pgxmock.NewPool()
		require.NoError(t, err, "create the subprocess's isolated administrator mock")
		t.Cleanup(mock.Close)
		t.Cleanup(func() { t.Log(laterCleanup) })
		mock.ExpectExec("DROP DATABASE").WillReturnError(errors.New("synthetic cleanup rejection"))
		t.Cleanup(func() { dropSessionPersistencePostgresDatabase(t, mock, "synthetic_fixture") })
		t.Run("passing parallel child", func(t *testing.T) {
			t.Parallel()
		})
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	command := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestSessionPersistenceCleanupFailureReporting$", "-test.v", "-test.count=1")
	command.Env = append(os.Environ(), childFlag+"=1")
	output, err := command.CombinedOutput()
	var exit *exec.ExitError
	require.ErrorAs(t, err, &exit, "a real cleanup error must fail the subprocess test binary")
	require.Equal(t, 1, exit.ExitCode(), "cleanup failure must be reported as a normal test failure rather than a crash")
	require.NoError(t, ctx.Err(), "the failing cleanup subprocess must finish inside its independent wall-clock bound")
	require.Contains(t, string(output), "--- PASS: TestSessionPersistenceCleanupFailureReporting/passing_parallel_child", "the parallel child must pass before its parent's cleanup fails")
	require.Contains(t, string(output), "--- FAIL: TestSessionPersistenceCleanupFailureReporting", "a real cleanup failure must preserve Go's named parent failure report")
	require.Contains(t, string(output), "synthetic cleanup rejection", "the parent failure must retain the cleanup error")
	require.Contains(t, string(output), laterCleanup, "a reported cleanup error must not prevent remaining cleanup callbacks")
}
