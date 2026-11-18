package db

import (
	"context"
	"encoding/json"
	"strconv"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

func TestSessionExecutorStoreLifecycleWriterVersion(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		version int
	}{
		{name: "passive zero", version: 0},
		{name: "future observation", version: 7},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "create an isolated executor read store")
			defer mock.Close()
			stamp := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
			heartbeat, lease, deadline := stamp.Add(time.Minute), stamp.Add(2*time.Minute), stamp.Add(time.Hour)
			drainRequested, drainDeadline, completed := stamp.Add(3*time.Minute), stamp.Add(4*time.Minute), stamp.Add(5*time.Minute)
			threadID := uuid.New()
			containerID, lastError, exitCode := "synthetic-container", "synthetic-error", 1
			expected := models.SessionExecutor{
				ID: uuid.New(), OrgID: uuid.New(), SessionID: uuid.New(), ThreadID: &threadID,
				JobID: uuid.New(), JobType: "run_agent", HostNodeID: "worker-1", OwnerID: "worker-1",
				LockToken: uuid.New(), Status: models.SessionExecutorStatusFailed, ContainerID: &containerID,
				Image: "synthetic-image", BuildSHA: "synthetic-build", HeartbeatAt: &heartbeat, LeaseExpiresAt: &lease,
				RuntimeDeadlineAt: &deadline, DrainIntent: models.DrainIntentHostMaintenance,
				DrainRequestedAt: &drainRequested, DrainDeadlineAt: &drainDeadline, StartedAt: stamp,
				CompletedAt: &completed, ExitCode: &exitCode, LastError: &lastError, CreatedAt: stamp, UpdatedAt: completed,
				LifecycleWriterVersion: tt.version,
			}
			columns := []string{
				"id", "org_id", "session_id", "thread_id", "job_id", "job_type", "host_node_id", "owner_id", "lock_token",
				"status", "container_id", "image", "build_sha", "heartbeat_at", "lease_expires_at", "runtime_deadline_at",
				"drain_intent", "drain_requested_at", "drain_deadline_at", "started_at", "completed_at", "exit_code",
				"last_error", "created_at", "updated_at", "lifecycle_writer_version",
			}
			mock.ExpectQuery(`SELECT .+lifecycle_writer_version FROM session_executors WHERE id = \$1`).
				WithArgs(expected.ID).
				WillReturnRows(pgxmock.NewRows(columns).AddRow(
					expected.ID, expected.OrgID, expected.SessionID, threadID.String(), expected.JobID, expected.JobType,
					expected.HostNodeID, expected.OwnerID, expected.LockToken, string(expected.Status), containerID,
					expected.Image, expected.BuildSHA, heartbeat, lease, deadline, string(expected.DrainIntent),
					drainRequested, drainDeadline, stamp, completed, int64(exitCode), lastError, stamp, completed, tt.version,
				))
			actual, err := NewSessionExecutorStore(mock).GetByID(context.Background(), expected.ID)
			require.NoError(t, err, "the existing boot projection must accept passive and future metadata")
			require.Equal(t, expected, actual, "the complete executor row must preserve actual writer metadata and every existing field")
			raw, err := json.Marshal(actual)
			require.NoError(t, err, "serialize the executor read model")
			var fields map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(raw, &fields), "inspect the executor JSON metadata field")
			require.Equal(t, json.RawMessage(strconv.Itoa(tt.version)), fields["lifecycle_writer_version"], "zero must be explicit and future observations must not be replaced or rejected")
			require.NoError(t, mock.ExpectationsWereMet(), "reading the actual column must use the existing single boot query")
		})
	}
}
