package db

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgtype"
	"github.com/stretchr/testify/require"
)

func TestSessionExecutorRetentionPostgres_Ownership(t *testing.T) {
	t.Parallel()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set TEST_DATABASE_URL to disposable PostgreSQL for executor retention ownership proof")
	}
	// This test owns at most four direct connections, including its parallel
	// fixtures. It does not allocate a connection pool or an admin connection.
	connections := make(chan struct{}, 4)
	tests := []struct {
		name   string
		status models.SessionExecutorStatus
		mutate func(*testing.T, *pgx.Conn, models.SessionExecutorContainerRetention, uuid.UUID)
		want   bool
	}{
		{name: "completed", status: models.SessionExecutorStatusCompleted, want: true},
		{name: "requeued", status: models.SessionExecutorStatusRequeued, want: true},
		{name: "failed", status: models.SessionExecutorStatusFailed, want: true},
		{name: "lost", status: models.SessionExecutorStatusLost, want: true},
		{name: "starting", status: models.SessionExecutorStatusStarting},
		{name: "running", status: models.SessionExecutorStatusRunning},
		{name: "draining", status: models.SessionExecutorStatusDraining},
		{name: "recent terminal evidence", status: models.SessionExecutorStatusCompleted, mutate: func(t *testing.T, c *pgx.Conn, r models.SessionExecutorContainerRetention, _ uuid.UUID) {
			execExecutorRetentionFixture(t, c, `UPDATE session_executors SET completed_at=now() WHERE org_id=$1 AND id=$2`, r.OrgID, r.ID)
		}},
		{name: "missing terminal evidence", status: models.SessionExecutorStatusCompleted, mutate: func(t *testing.T, c *pgx.Conn, r models.SessionExecutorContainerRetention, _ uuid.UUID) {
			execExecutorRetentionFixture(t, c, `UPDATE session_executors SET completed_at=NULL WHERE org_id=$1 AND id=$2`, r.OrgID, r.ID)
		}},
		{name: "live executor lease", status: models.SessionExecutorStatusCompleted, mutate: func(t *testing.T, c *pgx.Conn, r models.SessionExecutorContainerRetention, _ uuid.UUID) {
			execExecutorRetentionFixture(t, c, `UPDATE session_executors SET lease_expires_at=now()+interval '1 hour' WHERE org_id=$1 AND id=$2`, r.OrgID, r.ID)
		}},
		{name: "running job owned by executor", status: models.SessionExecutorStatusCompleted, mutate: func(t *testing.T, c *pgx.Conn, r models.SessionExecutorContainerRetention, _ uuid.UUID) {
			execExecutorRetentionFixture(t, c, `INSERT INTO jobs VALUES ($1,$2,'running','session_executor',$3,$4)`, r.JobID, r.OrgID, r.ID.String(), uuid.New())
		}},
		{name: "running job retains exact lock token", status: models.SessionExecutorStatusLost, mutate: func(t *testing.T, c *pgx.Conn, r models.SessionExecutorContainerRetention, token uuid.UUID) {
			execExecutorRetentionFixture(t, c, `INSERT INTO jobs VALUES ($1,$2,'running','worker','worker-old',$3)`, r.JobID, r.OrgID, token)
		}},
		{name: "completed job no longer owns container", status: models.SessionExecutorStatusCompleted, want: true, mutate: func(t *testing.T, c *pgx.Conn, r models.SessionExecutorContainerRetention, token uuid.UUID) {
			execExecutorRetentionFixture(t, c, `INSERT INTO jobs VALUES ($1,$2,'succeeded','session_executor',$3,$4)`, r.JobID, r.OrgID, r.ID.String(), token)
		}},
		{name: "new job attempt does not own old executor", status: models.SessionExecutorStatusRequeued, want: true, mutate: func(t *testing.T, c *pgx.Conn, r models.SessionExecutorContainerRetention, _ uuid.UUID) {
			execExecutorRetentionFixture(t, c, `INSERT INTO jobs VALUES ($1,$2,'running','session_executor',$3,$4)`, r.JobID, r.OrgID, uuid.NewString(), uuid.New())
		}},
		{name: "other tenant owner cannot affect this identity", status: models.SessionExecutorStatusCompleted, want: true, mutate: func(t *testing.T, c *pgx.Conn, r models.SessionExecutorContainerRetention, token uuid.UUID) {
			execExecutorRetentionFixture(t, c, `INSERT INTO jobs VALUES ($1,$2,'running','session_executor',$3,$4)`, uuid.New(), uuid.New(), r.ID.String(), token)
		}},
		{name: "another active executor references container", status: models.SessionExecutorStatusCompleted, mutate: func(t *testing.T, c *pgx.Conn, r models.SessionExecutorContainerRetention, _ uuid.UUID) {
			execExecutorRetentionFixture(t, c, `INSERT INTO session_executors(id,org_id,status,container_id) VALUES($1,$2,'running',$3)`, uuid.New(), r.OrgID, r.ContainerID)
		}},
		{name: "missing recorded container identity", status: models.SessionExecutorStatusCompleted, mutate: func(t *testing.T, c *pgx.Conn, r models.SessionExecutorContainerRetention, _ uuid.UUID) {
			execExecutorRetentionFixture(t, c, `UPDATE session_executors SET container_id=NULL WHERE org_id=$1 AND id=$2`, r.OrgID, r.ID)
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			connections <- struct{}{}
			t.Cleanup(func() { <-connections })
			conn := newExecutorRetentionPostgres(t, dsn)
			row, lockToken := insertExecutorRetentionFixture(t, conn, tt.status)
			if tt.mutate != nil {
				tt.mutate(t, conn, row, lockToken)
			}
			actual, err := NewSessionExecutorStore(conn).ListTerminalContainersByIdentity(context.Background(), []models.SessionExecutorIdentity{{OrgID: row.OrgID, ID: row.ID}}, time.Now().Add(-24*time.Hour))
			require.NoError(t, err, "real PostgreSQL retention selection should execute against typed ownership rows")
			expected := []models.SessionExecutorContainerRetention{}
			if tt.want {
				expected = append(expected, row)
			}
			require.Equal(t, expected, actual, "only terminal, old, unowned executor records should authorize cleanup")
		})
	}
}

func TestSessionExecutorRetentionPostgres_PairedTenantBatch(t *testing.T) {
	t.Parallel()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set TEST_DATABASE_URL to disposable PostgreSQL for paired tenant batch proof")
	}
	conn := newExecutorRetentionPostgres(t, dsn)
	first, _ := insertExecutorRetentionFixture(t, conn, models.SessionExecutorStatusCompleted)
	second, _ := insertExecutorRetentionFixture(t, conn, models.SessionExecutorStatusCompleted)
	identities := []models.SessionExecutorIdentity{{OrgID: first.OrgID, ID: second.ID}, {OrgID: second.OrgID, ID: first.ID}}
	actual, err := NewSessionExecutorStore(conn).ListTerminalContainersByIdentity(context.Background(), identities, time.Now().Add(-24*time.Hour))
	require.NoError(t, err, "mixed tenant batch should execute successfully")
	require.Equal(t, []models.SessionExecutorContainerRetention{}, actual, "parallel arrays must join paired identities, never their cross product")
}

func newExecutorRetentionPostgres(t *testing.T, dsn string) *pgx.Conn {
	t.Helper()
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dsn)
	require.NoError(t, err, "connect one direct fixture connection to disposable PostgreSQL")
	t.Cleanup(func() {
		require.NoError(t, conn.Close(context.Background()), "close executor retention fixture connection and temporary tables")
	})
	conn.TypeMap().RegisterType(&pgtype.Type{Name: "timestamptz", OID: pgtype.TimestamptzOID, Codec: &pgtype.TimestamptzCodec{ScanLocation: time.UTC}})
	execExecutorRetentionFixture(t, conn, `
CREATE TEMP TABLE session_executors (
 id uuid PRIMARY KEY, org_id uuid NOT NULL, session_id uuid, thread_id uuid,
 job_id uuid, job_type text, host_node_id text, container_id text, image text,
 build_sha text, status text NOT NULL, completed_at timestamptz,
 lease_expires_at timestamptz, lock_token uuid
);
CREATE TEMP TABLE jobs (
 id uuid PRIMARY KEY, org_id uuid NOT NULL, status text NOT NULL,
 owner_kind text NOT NULL, run_owner_id text, lock_token uuid
);`)
	return conn
}

func insertExecutorRetentionFixture(t *testing.T, conn *pgx.Conn, status models.SessionExecutorStatus) (models.SessionExecutorContainerRetention, uuid.UUID) {
	t.Helper()
	lease := time.Now().UTC().Add(-time.Hour).Truncate(time.Microsecond)
	row := models.SessionExecutorContainerRetention{ID: uuid.New(), OrgID: uuid.New(), SessionID: uuid.New(), JobID: uuid.New(), JobType: "run_agent", HostNodeID: "worker-old-generation", ContainerID: strings.Repeat("a", 64), Image: "example/server:test", BuildSHA: "test-build", Status: status, CompletedAt: lease.Add(-48 * time.Hour), LeaseExpiresAt: &lease}
	lockToken := uuid.New()
	execExecutorRetentionFixture(t, conn, `INSERT INTO session_executors(id,org_id,session_id,job_id,job_type,host_node_id,container_id,image,build_sha,status,completed_at,lease_expires_at,lock_token) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`, row.ID, row.OrgID, row.SessionID, row.JobID, row.JobType, row.HostNodeID, row.ContainerID, row.Image, row.BuildSHA, row.Status, row.CompletedAt, row.LeaseExpiresAt, lockToken)
	return row, lockToken
}

func execExecutorRetentionFixture(t *testing.T, conn *pgx.Conn, sql string, args ...any) {
	t.Helper()
	_, err := conn.Exec(context.Background(), sql, args...)
	require.NoError(t, err, "prepare isolated synthetic executor retention ownership state")
}
