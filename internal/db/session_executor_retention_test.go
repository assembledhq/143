package db

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

func TestSessionExecutorStore_ListTerminalContainersByIdentity(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		err  error
	}{
		{name: "paired tenant lookup"},
		{name: "database failure", err: errors.New("database unavailable")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "create isolated database expectations")
			t.Cleanup(mock.Close)
			cutoff := time.Date(2026, 1, 8, 0, 0, 0, 0, time.UTC)
			orgID, executorID := uuid.New(), uuid.New()
			expected := []models.SessionExecutorContainerRetention{{ID: executorID, OrgID: orgID, SessionID: uuid.New(), JobID: uuid.New(), JobType: "run_agent", HostNodeID: "worker-old", ContainerID: "container-id", Image: "example/server:test", BuildSHA: "build", Status: models.SessionExecutorStatusCompleted, CompletedAt: cutoff.Add(-time.Hour)}}
			expectation := mock.ExpectQuery(`JOIN unnest\(\$1::uuid\[\], \$2::uuid\[\]\) AS requested\(org_id, executor_id\)[\s\S]+ON se.org_id = requested.org_id AND se.id = requested.executor_id[\s\S]+j.org_id = se.org_id[\s\S]+active.org_id = se.org_id`).WithArgs([]uuid.UUID{orgID}, []uuid.UUID{executorID}, cutoff)
			if tt.err != nil {
				expectation.WillReturnError(tt.err)
			} else {
				r := expected[0]
				expectation.WillReturnRows(pgxmock.NewRows([]string{"id", "org_id", "session_id", "thread_id", "job_id", "job_type", "host_node_id", "container_id", "image", "build_sha", "status", "completed_at", "lease_expires_at"}).AddRow(r.ID, r.OrgID, r.SessionID, nil, r.JobID, r.JobType, r.HostNodeID, r.ContainerID, r.Image, r.BuildSHA, r.Status, r.CompletedAt, nil))
			}
			actual, err := NewSessionExecutorStore(mock).ListTerminalContainersByIdentity(context.Background(), []models.SessionExecutorIdentity{{OrgID: orgID, ID: executorID}}, cutoff)
			if tt.err != nil {
				require.ErrorIs(t, err, tt.err, "database failures must propagate without authorizing cleanup")
				require.Nil(t, actual, "failed database reads must not return cleanup candidates")
			} else {
				require.NoError(t, err, "paired tenant-scoped retention lookup should succeed")
				require.Equal(t, expected, actual, "retention lookup should preserve all durable ownership fields")
			}
			require.NoError(t, mock.ExpectationsWereMet(), "all retention queries must enforce paired tenant identities")
		})
	}
}

func TestSessionExecutorStore_EmptyOrInvalidRetentionIdentity(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		identities []models.SessionExecutorIdentity
		wantErr    bool
	}{
		{name: "empty batch"},
		{name: "missing tenant", identities: []models.SessionExecutorIdentity{{ID: uuid.New()}}, wantErr: true},
		{name: "missing executor", identities: []models.SessionExecutorIdentity{{OrgID: uuid.New()}}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "create isolated empty-batch expectations")
			t.Cleanup(mock.Close)
			actual, err := NewSessionExecutorStore(mock).ListTerminalContainersByIdentity(context.Background(), tt.identities, time.Now())
			if tt.wantErr {
				require.Error(t, err, "missing tenant or executor must never reach a database lookup")
				require.Nil(t, actual, "invalid identity cannot authorize deletion")
			} else {
				require.NoError(t, err, "empty inventory should need no database access")
				require.Equal(t, []models.SessionExecutorContainerRetention{}, actual, "empty inventory should return no candidates")
			}
			require.NoError(t, mock.ExpectationsWereMet(), "empty or invalid requests must not perform any query")
		})
	}
}
