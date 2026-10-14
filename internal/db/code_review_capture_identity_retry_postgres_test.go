package db

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

func TestCodeReviewCaptureIdentityRetryPhaseCheckpointPostgres(t *testing.T) {
	t.Parallel()
	databaseURL := os.Getenv("TEST_DATABASE_URL")
	if databaseURL == "" {
		t.Skip("set TEST_DATABASE_URL for capture identity retry phase fencing proof")
	}
	tests := []struct {
		name, fence        string
		owned, hasExisting bool
	}{
		{name: "fresh phase ignores an exhausted external retry window", owned: true},
		{name: "existing phase retains its bounded deadline", owned: true, hasExisting: true},
		{name: "wrong organization", fence: "org"},
		{name: "wrong session", fence: "session"},
		{name: "wrong job", fence: "job"},
		{name: "lost lease", fence: "lease"},
		{name: "expired lease", fence: "expired"},
		{name: "missing lease deadline", fence: "missing"},
		{name: "terminal job", fence: "status"},
		{name: "different job type", fence: "type"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			conn, err := pgx.Connect(ctx, databaseURL)
			require.NoError(t, err, "connect to the disposable capture identity checkpoint database")
			defer func() { require.NoError(t, conn.Close(ctx), "close the isolated checkpoint connection") }()
			// A connection-local table isolates parallel cases. Autocommit lets
			// now() advance between retries so deadline extension is observable.
			_, err = conn.Exec(ctx, `CREATE TEMP TABLE jobs(id uuid,org_id uuid,lock_token uuid,status text,job_type text,payload jsonb,updated_at timestamptz,retry_window_started_at timestamptz,lease_expires_at timestamptz)`)
			require.NoError(t, err, "create connection-local durable job fields")
			orgID, sessionID, jobID, lockToken := uuid.New(), uuid.New(), uuid.New(), uuid.New()
			externalStart := time.Now().UTC().Add(-4 * time.Hour).Truncate(time.Microsecond)
			freshnessStart := externalStart.Add(time.Hour)
			updatedAt := externalStart.Add(2 * time.Hour)
			originalPayload := map[string]any{
				"session_id": sessionID.String(),
				"_code_review_freshness_retry_started_at": freshnessStart.Format(time.RFC3339Nano),
				"unrelated_checkpoint":                    "preserved",
			}
			var expectedStart time.Time
			if tt.hasExisting {
				expectedStart = externalStart.Add(210 * time.Minute)
				originalPayload["_code_review_capture_identity_retry_started_at"] = expectedStart.Format(time.RFC3339Nano)
			}
			payload, err := json.Marshal(originalPayload)
			require.NoError(t, err, "encode the prior independent retry phase checkpoints")
			status, jobType := "running", "run_code_review"
			leaseExpiresAt := time.Now().UTC().Add(5 * time.Minute)
			var leaseDeadline *time.Time = &leaseExpiresAt
			callOrgID, callSessionID, callJobID, callLockToken := orgID, sessionID, jobID, lockToken
			switch tt.fence {
			case "org":
				callOrgID = uuid.New()
			case "session":
				callSessionID = uuid.New()
			case "job":
				callJobID = uuid.New()
			case "lease":
				callLockToken = uuid.New()
			case "expired":
				leaseExpiresAt = time.Now().UTC().Add(-time.Minute)
			case "missing":
				leaseDeadline = nil
			case "status":
				status = "succeeded"
			case "type":
				jobType = "run_agent"
			}
			_, err = conn.Exec(ctx, `INSERT INTO jobs VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9)`, jobID, orgID, lockToken, status, jobType, payload, updatedAt, externalStart, leaseDeadline)
			require.NoError(t, err, "seed the exact lease and an expired earlier dependency window")
			store := NewCodeReviewStore(conn)
			startedAt, owned, err := store.EnsureCaptureIdentityRetryStartedAt(ctx, callOrgID, callSessionID, callJobID, callLockToken)
			require.NoError(t, err, "the capture identity checkpoint must respect its ownership fences")
			require.Equal(t, tt.owned, owned, "only the exact running controller may start the handoff retry phase")
			var storedPayload []byte
			var storedUpdatedAt, storedExternalStart time.Time
			var storedCheckpoint *time.Time
			require.NoError(t, conn.QueryRow(ctx, `SELECT payload,updated_at,retry_window_started_at,(payload->>'_code_review_capture_identity_retry_started_at')::timestamptz FROM jobs WHERE org_id=$1 AND id=$2`, orgID, jobID).Scan(&storedPayload, &storedUpdatedAt, &storedExternalStart, &storedCheckpoint), "read the complete checkpoint and prior window after the fenced write")
			var actualPayload map[string]any
			require.NoError(t, json.Unmarshal(storedPayload, &actualPayload), "decode all durable payload checkpoint fields")
			require.Equal(t, externalStart, storedExternalStart.UTC(), "capture identity retries must never renew or inherit the generic dependency window")
			if !tt.owned {
				require.Equal(t, time.Time{}, startedAt, "an unowned controller must not report a retry phase start")
				require.Nil(t, storedCheckpoint, "an unowned controller must not create a capture identity checkpoint")
				require.Equal(t, originalPayload, actualPayload, "ownership failure must preserve the full job payload")
				require.Equal(t, updatedAt, storedUpdatedAt.UTC(), "ownership failure must not update the job timestamp")
				return
			}
			if tt.hasExisting {
				require.Equal(t, expectedStart, startedAt.UTC(), "a persisted capture identity deadline cannot be extended on restart")
			} else {
				require.WithinDuration(t, time.Now(), startedAt, 5*time.Second, "the first capture identity handoff must receive a fresh phase budget")
			}
			require.Equal(t, &startedAt, storedCheckpoint, "the phase checkpoint must be durable before retrying the handoff")
			delete(actualPayload, "_code_review_capture_identity_retry_started_at")
			delete(originalPayload, "_code_review_capture_identity_retry_started_at")
			require.Equal(t, originalPayload, actualPayload, "capture identity checkpointing must preserve every unrelated payload field")
			var replayedAt time.Time
			require.NoError(t, conn.QueryRow(ctx, `SELECT now()`).Scan(&replayedAt), "observe a later transaction clock before retrying")
			require.True(t, replayedAt.After(startedAt), "a rewritten now timestamp would extend the deadline in this regression")
			again, owned, err := store.EnsureCaptureIdentityRetryStartedAt(ctx, orgID, sessionID, jobID, lockToken)
			require.NoError(t, err, "retry the same handoff under its existing lease")
			require.True(t, owned, "the active lease must retain checkpoint ownership")
			require.Equal(t, startedAt, again, "polling under one lease cannot extend the capture identity deadline")
			newLockToken := uuid.New()
			_, err = conn.Exec(ctx, `UPDATE jobs SET lock_token=$3 WHERE org_id=$1 AND id=$2`, orgID, jobID, newLockToken)
			require.NoError(t, err, "simulate the same controller reclaimed after a worker restart")
			again, owned, err = store.EnsureCaptureIdentityRetryStartedAt(ctx, orgID, sessionID, jobID, newLockToken)
			require.NoError(t, err, "resume the handoff with a new valid lease")
			require.True(t, owned, "the reclaimed controller lease must own its persisted checkpoint")
			require.Equal(t, startedAt, again, "reclaiming the controller cannot extend the capture identity deadline")
		})
	}
}
