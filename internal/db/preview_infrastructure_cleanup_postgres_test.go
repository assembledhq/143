package db

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

func previewInfrastructurePostgres(t *testing.T) *pgx.Conn {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set TEST_DATABASE_URL for preview infrastructure ownership and restart recovery proof")
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dsn)
	require.NoError(t, err, "connect isolated preview infrastructure fixture")
	t.Cleanup(func() { require.NoError(t, conn.Close(ctx), "close isolated preview infrastructure fixture") })
	_, err = conn.Exec(ctx, `
		CREATE TEMP TABLE preview_instances (
			org_id uuid, id uuid, session_id uuid, worker_node_id text, preview_handle text,
			status text, current_phase text, error text, unavailable_reason text,
			preview_holding_container boolean, stopped_at timestamptz, updated_at timestamptz,
			port integer, recycled_at timestamptz, created_at timestamptz,
			PRIMARY KEY(org_id, id)
		);
		CREATE TEMP TABLE preview_runtimes (
			org_id uuid, id uuid, preview_instance_id uuid, worker_node_id text, preview_handle text,
			status text, created_at timestamptz, error text, unavailable_reason text,
			primary_port integer, last_heartbeat_at timestamptz,
			stopped_at timestamptz, updated_at timestamptz, PRIMARY KEY(org_id, id)
		);
		CREATE TEMP TABLE nodes (id text PRIMARY KEY, mode text, status text, last_heartbeat_at timestamptz);
	`)
	require.NoError(t, err, "create temporary tenant, runtime, and generation tables")
	return conn
}

func TestResolvePreviewInfrastructureCleanupPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name                 string
		previewStatus        string
		persistedHandle      string
		persistedOtherWorker bool
		standalone           bool
		sessionMismatch      bool
		previewOtherOrg      bool
		runtimeStatus        string
		runtimeOtherOrg      bool
		runtimeBlankHandle   bool
		runtimeOtherWorker   bool
		otherGeneration      bool
		nodeMode             string
		nodeStatus           string
		staleNode            bool
		expected             bool
	}{
		{name: "missing preview permits cleanup", expected: true},
		{name: "terminal preview permits cleanup", previewStatus: "stopped", persistedHandle: "candidate", expected: true},
		{name: "current ready handle is protected", previewStatus: "ready", persistedHandle: "candidate"},
		{name: "unhealthy current handle is protected", previewStatus: "unhealthy", persistedHandle: "candidate"},
		{name: "same generation starting handle is protected", previewStatus: "starting"},
		{name: "recycled starting preview keeps previous handle during handoff", previewStatus: "starting", persistedHandle: "previous"},
		{name: "blank starting runtime protects handoff after observer demotion", previewStatus: "unhealthy", persistedHandle: "previous", runtimeStatus: "starting", runtimeBlankHandle: true},
		{name: "blank starting runtime from another generation cannot protect candidate", previewStatus: "ready", persistedHandle: "replacement", runtimeStatus: "starting", runtimeBlankHandle: true, runtimeOtherWorker: true, expected: true},
		{name: "blank starting runtime from another tenant cannot protect candidate", previewStatus: "ready", persistedHandle: "replacement", runtimeStatus: "starting", runtimeBlankHandle: true, runtimeOtherOrg: true, expected: true},
		{name: "different generation starting handle is reclaimable", previewStatus: "starting", persistedOtherWorker: true, expected: true},
		{name: "superseded handle permits cleanup", previewStatus: "ready", persistedHandle: "replacement", expected: true},
		{name: "active matching runtime protects terminal preview", previewStatus: "stopped", runtimeStatus: "ready"},
		{name: "draining matching runtime protects missing preview", runtimeStatus: "draining"},
		{name: "starting matching runtime protects missing preview", runtimeStatus: "starting"},
		{name: "lost matching runtime permits cleanup", previewStatus: "stopped", runtimeStatus: "lost", expected: true},
		{name: "same preview id in another tenant cannot protect candidate", previewStatus: "ready", persistedHandle: "candidate", previewOtherOrg: true, expected: true},
		{name: "same runtime preview id in another tenant cannot protect candidate", runtimeStatus: "ready", runtimeOtherOrg: true, expected: true},
		{name: "session mismatch fails closed", previewStatus: "stopped", sessionMismatch: true},
		{name: "standalone nil session can be reclaimed", previewStatus: "stopped", standalone: true, expected: true},
		{name: "fresh other generation worker protects terminal preview", previewStatus: "stopped", otherGeneration: true, nodeMode: "worker", nodeStatus: "active"},
		{name: "fresh draining worker protects missing preview", otherGeneration: true, nodeMode: "worker", nodeStatus: "draining"},
		{name: "fresh draining all mode protects terminal preview", previewStatus: "stopped", otherGeneration: true, nodeMode: "all", nodeStatus: "draining"},
		{name: "stale other generation permits cleanup", otherGeneration: true, nodeMode: "worker", nodeStatus: "active", staleNode: true, expected: true},
		{name: "dead other generation permits cleanup", otherGeneration: true, nodeMode: "worker", nodeStatus: "dead", expected: true},
		{name: "api node cannot own infrastructure", otherGeneration: true, nodeMode: "api", nodeStatus: "active", expected: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			conn := previewInfrastructurePostgres(t)
			ctx := context.Background()
			cutoff, err := NewPreviewStore(conn).CapturePreviewRuntimeRecoveryCutoff(ctx)
			require.NoError(t, err, "capture fixture database time")
			owner := models.PreviewInfrastructureOwner{OrgID: uuid.New(), PreviewID: uuid.New(), SessionID: uuid.New(), Handle: "candidate", WorkerNodeID: "worker-current"}
			if tt.standalone {
				owner.SessionID = uuid.Nil
			}
			if tt.otherGeneration {
				owner.WorkerNodeID = "worker-previous"
			}
			if tt.previewStatus != "" {
				orgID, sessionID, workerNodeID := owner.OrgID, owner.SessionID, owner.WorkerNodeID
				if tt.previewOtherOrg {
					orgID = uuid.New()
				}
				if tt.sessionMismatch {
					sessionID = uuid.New()
				}
				if tt.persistedOtherWorker {
					workerNodeID = "worker-replacement"
				}
				var nullableSession any = sessionID
				if sessionID == uuid.Nil {
					nullableSession = nil
				}
				_, err := conn.Exec(ctx, `INSERT INTO preview_instances (org_id,id,session_id,worker_node_id,preview_handle,status) VALUES ($1,$2,$3,$4,$5,$6)`, orgID, owner.PreviewID, nullableSession, workerNodeID, tt.persistedHandle, tt.previewStatus)
				require.NoError(t, err, "insert scoped preview ownership fixture")
			}
			if tt.runtimeStatus != "" {
				orgID := owner.OrgID
				if tt.runtimeOtherOrg {
					orgID = uuid.New()
				}
				handle, workerID := owner.Handle, owner.WorkerNodeID
				if tt.runtimeBlankHandle {
					handle = ""
				}
				if tt.runtimeOtherWorker {
					workerID = "worker-replacement"
				}
				_, err := conn.Exec(ctx, `INSERT INTO preview_runtimes (org_id,id,preview_instance_id,worker_node_id,preview_handle,status) VALUES ($1,$2,$3,$4,$5,$6)`, orgID, uuid.New(), owner.PreviewID, workerID, handle, tt.runtimeStatus)
				require.NoError(t, err, "insert scoped runtime ownership fixture")
			}
			if tt.nodeMode != "" {
				heartbeat := cutoff
				if tt.staleNode {
					heartbeat = cutoff.Add(-2 * time.Minute)
				}
				_, err := conn.Exec(ctx, `INSERT INTO nodes VALUES ($1,$2,$3,$4)`, owner.WorkerNodeID, tt.nodeMode, tt.nodeStatus, heartbeat)
				require.NoError(t, err, "insert exact generation heartbeat fixture")
			}
			// A second tenant is included in every batch to exercise array pairing
			// and ordinality rather than proving only a single owner query.
			missing := models.PreviewInfrastructureOwner{OrgID: uuid.New(), PreviewID: uuid.New(), Handle: "missing", WorkerNodeID: "worker-current"}
			actual, err := NewPreviewStore(conn).ResolvePreviewInfrastructureCleanup(ctx, []models.PreviewInfrastructureOwner{owner, missing}, "worker-current", cutoff.Add(-90*time.Second))
			require.NoError(t, err, "execute tenant-paired ownership query against PostgreSQL")
			require.Equal(t, map[models.PreviewInfrastructureOwner]bool{owner: tt.expected, missing: true}, actual, "cleanup must respect preview, runtime, session, tenant, and exact worker generation ownership")
		})
	}
}

func TestRecoverPreviewRuntimesAfterWorkerRestartPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name             string
		oldStatus        string
		survivingWorker  string
		newerSameWorker  bool
		otherPreviewNode bool
		withoutRuntime   bool
		terminalPreview  bool
		newerPreview     bool
		expectedStatus   string
		expectedHold     bool
		expectedRows     int64
	}{
		{name: "old ready runtime is lost and releases hold", oldStatus: "ready", expectedStatus: "unavailable", expectedRows: 1},
		{name: "old draining runtime is lost and releases hold", oldStatus: "draining", expectedStatus: "unavailable", expectedRows: 1},
		{name: "new same generation runtime protects preview", oldStatus: "ready", survivingWorker: "worker-current", newerSameWorker: true, expectedStatus: "ready", expectedHold: true},
		{name: "other generation runtime protects preview", oldStatus: "ready", survivingWorker: "worker-other", expectedStatus: "ready", expectedHold: true},
		{name: "other generation preview assignment protects hold", oldStatus: "ready", otherPreviewNode: true, expectedStatus: "ready", expectedHold: true},
		{name: "terminal old runtime is untouched while unsupported preview is retired", oldStatus: "stopped", expectedStatus: "unavailable", expectedRows: 1},
		{name: "local preview without runtime is retired", withoutRuntime: true, expectedStatus: "unavailable", expectedRows: 1},
		{name: "terminal local preview is unchanged", withoutRuntime: true, terminalPreview: true, expectedStatus: "stopped", expectedHold: true},
		{name: "newer local preview is protected", withoutRuntime: true, newerPreview: true, expectedStatus: "ready", expectedHold: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			conn := previewInfrastructurePostgres(t)
			ctx := context.Background()
			store := NewPreviewStore(conn)
			cutoff, err := store.CapturePreviewRuntimeRecoveryCutoff(ctx)
			require.NoError(t, err, "capture restart cutoff from PostgreSQL")
			orgID, previewID, runtimeID := uuid.New(), uuid.New(), uuid.New()
			previewWorker := "worker-current"
			if tt.otherPreviewNode {
				previewWorker = "worker-other"
			}
			previewStatus := "ready"
			if tt.terminalPreview {
				previewStatus = "stopped"
			}
			previewCreatedAt := cutoff.Add(-2 * time.Minute)
			if tt.newerPreview {
				previewCreatedAt = cutoff
			}
			_, err = conn.Exec(ctx, `INSERT INTO preview_instances(org_id,id,worker_node_id,preview_handle,status,preview_holding_container,created_at) VALUES($1,$2,$3,'candidate',$4,TRUE,$5)`, orgID, previewID, previewWorker, previewStatus, previewCreatedAt)
			require.NoError(t, err, "insert current preview ownership fixture")
			if !tt.withoutRuntime {
				_, err = conn.Exec(ctx, `INSERT INTO preview_runtimes(org_id,id,preview_instance_id,worker_node_id,status,created_at) VALUES($1,$2,$3,'worker-current',$4,$5)`, orgID, runtimeID, previewID, tt.oldStatus, cutoff.Add(-time.Minute))
				require.NoError(t, err, "insert unsupported old runtime fixture")
			}
			if tt.survivingWorker != "" {
				createdAt := cutoff.Add(-30 * time.Second)
				if tt.newerSameWorker {
					createdAt = cutoff
				}
				_, err = conn.Exec(ctx, `INSERT INTO preview_runtimes(org_id,id,preview_instance_id,worker_node_id,status,created_at) VALUES($1,$2,$3,$4,'ready',$5)`, orgID, uuid.New(), previewID, tt.survivingWorker, createdAt)
				require.NoError(t, err, "insert surviving runtime generation fixture")
			}
			actualRows, err := store.RecoverPreviewRuntimesAfterWorkerRestart(ctx, "worker-current", cutoff)
			require.NoError(t, err, "execute same-generation startup recovery")
			require.Equal(t, tt.expectedRows, actualRows, "only previews without surviving ownership should become unavailable")
			var status string
			var hold bool
			require.NoError(t, conn.QueryRow(ctx, `SELECT status,preview_holding_container FROM preview_instances WHERE org_id=$1 AND id=$2`, orgID, previewID).Scan(&status, &hold), "read recovered preview state")
			require.Equal(t, tt.expectedStatus, status, "current preview must be protected by newer or other generation ownership")
			require.Equal(t, tt.expectedHold, hold, "startup recovery must release only unsupported preview holds")
			if !tt.withoutRuntime {
				expectedRuntimeStatus := "lost"
				if tt.oldStatus == "stopped" {
					expectedRuntimeStatus = tt.oldStatus
				}
				require.NoError(t, conn.QueryRow(ctx, `SELECT status FROM preview_runtimes WHERE org_id=$1 AND id=$2`, orgID, runtimeID).Scan(&status), "read retired old runtime state")
				require.Equal(t, expectedRuntimeStatus, status, "startup must retire only this worker's old active runtimes")
			}
			if tt.survivingWorker != "" {
				require.NoError(t, conn.QueryRow(ctx, `SELECT status FROM preview_runtimes WHERE org_id=$1 AND preview_instance_id=$2 AND id<>$3`, orgID, previewID, runtimeID).Scan(&status), "read surviving runtime state")
				require.Equal(t, "ready", status, "new or other generation runtime must remain active")
			}
		})
	}
}

func TestResolvePreviewInfrastructureCleanupLaunchHandoffPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name            string
		previewStatus   string
		withRuntime     bool
		handoffEligible bool
	}{
		{name: "recycle starting old handle with blank runtime", previewStatus: "starting", withRuntime: true},
		{name: "local recycle starting old handle without runtime", previewStatus: "starting"},
		{name: "observer demotion retains blank runtime handoff fence", previewStatus: "unhealthy", withRuntime: true},
		// Without a runtime row, this exceptional observer transition requires
		// the provider's explicit manager acknowledgement fence. SQL alone can
		// no longer distinguish a pending live handle from a superseded one.
		{name: "local observer demotion requires provider acknowledgement", previewStatus: "unhealthy", handoffEligible: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			conn := previewInfrastructurePostgres(t)
			ctx := context.Background()
			store := NewPreviewStore(conn)
			cutoff, err := store.CapturePreviewRuntimeRecoveryCutoff(ctx)
			require.NoError(t, err, "capture database time for the handoff fixture")
			owner := models.PreviewInfrastructureOwner{OrgID: uuid.New(), PreviewID: uuid.New(), SessionID: uuid.New(), Handle: "returned-handle", WorkerNodeID: "worker-current"}
			previous := owner
			previous.Handle = "previous-handle"
			_, err = conn.Exec(ctx, `INSERT INTO preview_instances(org_id,id,session_id,worker_node_id,preview_handle,status) VALUES($1,$2,$3,$4,$5,$6)`, owner.OrgID, owner.PreviewID, owner.SessionID, owner.WorkerNodeID, previous.Handle, tt.previewStatus)
			require.NoError(t, err, "retain the previous handle while the recycle launches")
			runtimeID := uuid.New()
			if tt.withRuntime {
				_, err = conn.Exec(ctx, `INSERT INTO preview_runtimes(org_id,id,preview_instance_id,worker_node_id,preview_handle,status) VALUES($1,$2,$3,$4,'','starting')`, owner.OrgID, runtimeID, owner.PreviewID, owner.WorkerNodeID)
				require.NoError(t, err, "insert a blank starting runtime before provider handle handoff")
			}
			resolve := func() map[models.PreviewInfrastructureOwner]bool {
				t.Helper()
				actual, err := store.ResolvePreviewInfrastructureCleanup(ctx, []models.PreviewInfrastructureOwner{owner, previous}, "worker-current", cutoff.Add(-90*time.Second))
				require.NoError(t, err, "resolve both provider generations in one handoff batch")
				return actual
			}
			require.Equal(t, map[models.PreviewInfrastructureOwner]bool{owner: tt.handoffEligible, previous: false}, resolve(), "starting ownership must protect the returned handle before it is persisted")
			if tt.withRuntime {
				require.NoError(t, store.MarkPreviewRuntimeReady(ctx, owner.OrgID, runtimeID, owner.Handle, 3000), "atomically commit the returned handle to runtime and preview routing")
			} else {
				require.NoError(t, store.UpdatePreviewHandle(ctx, owner.OrgID, owner.PreviewID, owner.Handle, 3000), "commit the returned handle for a local preview without runtime routing")
			}
			require.Equal(t, map[models.PreviewInfrastructureOwner]bool{owner: false, previous: tt.previewStatus != "starting"}, resolve(), "committed routing must continuously protect the returned handle before final readiness")
			_, err = conn.Exec(ctx, `UPDATE preview_instances SET status='ready' WHERE org_id=$1 AND id=$2`, owner.OrgID, owner.PreviewID)
			require.NoError(t, err, "finish the manager readiness transition")
			require.Equal(t, map[models.PreviewInfrastructureOwner]bool{owner: false, previous: true}, resolve(), "only the superseded provider handle may be reclaimed after readiness")
		})
	}
}
