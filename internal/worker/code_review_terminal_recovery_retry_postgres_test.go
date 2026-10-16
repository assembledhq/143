package worker

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func TestTerminalReviewRecoveryRetryPreservesQueuedWorkPostgres(t *testing.T) {
	t.Parallel()
	if os.Getenv("TEST_DATABASE_URL") == "" {
		t.Skip("set TEST_DATABASE_URL for recovery queue deferral proof")
	}
	ctx := context.Background()
	pool := fullRecoveryPostgresPool(t, ctx)
	tests := []struct {
		name       string
		jobType    string
		cause      func() error
		wantStatus models.JobStatus
	}{
		{name: "aged scheduler at max attempts remains queued", jobType: models.JobTypeReconcileCodeReviewSchedule, wantStatus: models.JobStatusPending},
		{name: "aged controller at max attempts remains queued", jobType: models.JobTypeRunCodeReview, wantStatus: models.JobStatusPending},
		{name: "aged continuation supervisor at max attempts remains queued", jobType: models.JobTypeRunCodeReviewRecheck, wantStatus: models.JobStatusPending},
		{name: "ordinary deadline retains bounded failure", jobType: models.JobTypeReconcileCodeReviewSchedule, cause: func() error { return context.DeadlineExceeded }, wantStatus: models.JobStatusDeadLetter},
		{name: "caller cancellation retains bounded failure", jobType: models.JobTypeReconcileCodeReviewSchedule, cause: func() error { return context.Canceled }, wantStatus: models.JobStatusDeadLetter},
		{name: "provider deadline retains its bounded window", jobType: models.JobTypeReconcileCodeReviewSchedule, cause: func() error {
			return &RetryableError{Err: context.DeadlineExceeded}
		}, wantStatus: models.JobStatusDeadLetter},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := seedStrandedReview(t, pool, "idle")
			node := "terminal-recovery-" + uuid.NewString()
			_, err := pool.Exec(ctx, `INSERT INTO nodes(id,mode) VALUES($1,'worker')`, node)
			require.NoError(t, err, "register the isolated queue consumer")
			createdAt := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
			retryStartedAt := createdAt.Add(time.Minute)
			_, err = pool.Exec(ctx, `UPDATE jobs SET job_type=$3,status='pending',lock_token=NULL,attempts=7,max_attempts=8,created_at=$4,retry_window_started_at=$5,target_node_id=$6,run_at=now() WHERE org_id=$1 AND id=$2`, f.job.OrgID, f.jobID, tt.jobType, createdAt, retryStartedAt, node)
			require.NoError(t, err, "retain aged durable work one claim below its maximum attempts")
			originalAssessment, err := f.stores.CodeReviewAssessments.GetByID(ctx, f.job.OrgID, f.assessment)
			require.NoError(t, err, "retain the assessment before its provider-free recovery wait")
			var originalPayload json.RawMessage
			require.NoError(t, pool.QueryRow(ctx, `SELECT payload FROM jobs WHERE org_id=$1 AND id=$2`, f.job.OrgID, f.jobID).Scan(&originalPayload), "retain the original job's durable input")
			w := New(pool, zerolog.Nop(), node)
			w.renewInterval = time.Hour
			w.Register(tt.jobType, func(context.Context, string, json.RawMessage) error {
				if tt.cause != nil {
					return tt.cause()
				}
				return fmt.Errorf("admit review: %w: %w", db.ErrCodeReviewTerminalRecoveryTimeout, context.DeadlineExceeded)
			})
			started := time.Now()
			w.poll(ctx)
			finished := time.Now()
			var actual struct {
				Status       models.JobStatus
				Attempts     int
				RetryStarted time.Time
				Target       string
			}
			var runAt time.Time
			var payload json.RawMessage
			require.NoError(t, pool.QueryRow(ctx, `SELECT status,attempts,retry_window_started_at,target_node_id,run_at,payload FROM jobs WHERE org_id=$1 AND id=$2`, f.job.OrgID, f.jobID).Scan(&actual.Status, &actual.Attempts, &actual.RetryStarted, &actual.Target, &runAt, &payload), "read the original job after the worker's fenced terminal write")
			actual.RetryStarted = actual.RetryStarted.UTC()
			wantAttempts := 8
			if tt.wantStatus == models.JobStatusPending {
				wantAttempts = 7
				require.WithinRange(t, runAt, started.Add(10*time.Second), finished.Add(10*time.Second), "local recovery contention should yield a worker slot for a bounded ten-second wait")
			}
			require.Equal(t, struct {
				Status       models.JobStatus
				Attempts     int
				RetryStarted time.Time
				Target       string
			}{tt.wantStatus, wantAttempts, retryStartedAt, node}, actual, "only scoped recovery contention may preserve the attempt count while all retry windows and routing remain intact")
			require.JSONEq(t, string(originalPayload), string(payload), "retry classification must preserve the original durable job input")
			assessment, err := f.stores.CodeReviewAssessments.GetByID(ctx, f.job.OrgID, f.assessment)
			require.NoError(t, err, "read assessment after worker queue handling")
			require.Equal(t, originalAssessment, assessment, "recovery deferral and unrelated scheduler failures must preserve the original review assessment")
		})
	}
}
