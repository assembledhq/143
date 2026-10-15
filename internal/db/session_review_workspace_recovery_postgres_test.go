package db

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"
)

type recoveryTransactionRecorder struct {
	TxStarter
	begins         atomic.Int64
	sessionLocked  chan struct{}
	precheckSQL    string
	precheckArgs   []any
	candidatesSQL  string
	candidatesArgs []any
}

func (s *recoveryTransactionRecorder) QueryRow(ctx context.Context, sql string, arguments ...any) pgx.Row {
	s.precheckSQL, s.precheckArgs = sql, arguments
	return s.TxStarter.QueryRow(ctx, sql, arguments...)
}

func (s *recoveryTransactionRecorder) Begin(ctx context.Context) (pgx.Tx, error) {
	s.begins.Add(1)
	tx, err := s.TxStarter.Begin(ctx)
	if err != nil {
		return tx, err
	}
	return &recoveryObservedTransaction{Tx: tx, recorder: s}, nil
}

type recoveryObservedTransaction struct {
	pgx.Tx
	recorder *recoveryTransactionRecorder
}

func (tx *recoveryObservedTransaction) Query(ctx context.Context, sql string, arguments ...any) (pgx.Rows, error) {
	if strings.Contains(sql, "WITH candidates AS") {
		tx.recorder.candidatesSQL, tx.recorder.candidatesArgs = sql, arguments
	}
	return tx.Tx.Query(ctx, sql, arguments...)
}

func (tx *recoveryObservedTransaction) QueryRow(ctx context.Context, sql string, arguments ...any) pgx.Row {
	row := tx.Tx.QueryRow(ctx, sql, arguments...)
	if tx.recorder.sessionLocked != nil && strings.Contains(sql, "FROM sessions") && strings.Contains(sql, "FOR NO KEY UPDATE") {
		return recoveryObservedSessionRow{Row: row, locked: tx.recorder.sessionLocked}
	}
	return row
}

type recoveryObservedSessionRow struct {
	pgx.Row
	locked chan struct{}
}

func (row recoveryObservedSessionRow) Scan(destinations ...any) error {
	err := row.Row.Scan(destinations...)
	if err == nil {
		close(row.locked)
	}
	return err
}

type incompleteReviewWorkspaceFixture struct {
	reviewWorkspaceFixture
	ownerExecutor uuid.UUID
	ownerRuntime  uuid.UUID
	ownerThread   uuid.UUID
	params        RecoverIncompleteCodeReviewWorkspaceParams
}

func newIncompleteReviewWorkspaceFixture(t *testing.T) incompleteReviewWorkspaceFixture {
	t.Helper()
	f := incompleteReviewWorkspaceFixture{reviewWorkspaceFixture: newReviewWorkspaceFixture(t), ownerExecutor: uuid.New(), ownerRuntime: uuid.New(), ownerThread: uuid.New()}
	ctx := context.Background()
	_, err := f.pool.Exec(ctx, `ALTER TABLE jobs ADD COLUMN run_at timestamptz,ADD COLUMN last_error text;
 CREATE TABLE session_threads(id uuid PRIMARY KEY,org_id uuid NOT NULL,session_id uuid NOT NULL REFERENCES sessions(id),
 status text NOT NULL,current_turn int NOT NULL DEFAULT 0,execution_mode text NOT NULL DEFAULT 'work',
 filesystem_mode text NOT NULL DEFAULT 'read_write',created_by_source text NOT NULL DEFAULT 'user',
 cancel_requested_at timestamptz,archived_at timestamptz);
 CREATE INDEX idx_session_threads_session ON session_threads(session_id);
 CREATE TABLE code_review_agent_results(id uuid PRIMARY KEY,org_id uuid NOT NULL,session_id uuid NOT NULL REFERENCES sessions(id),
 role text NOT NULL,status text NOT NULL,structured_result jsonb NOT NULL);
 CREATE TABLE session_executors(id uuid PRIMARY KEY,org_id uuid NOT NULL,session_id uuid NOT NULL REFERENCES sessions(id),
 job_id uuid NOT NULL REFERENCES jobs(id),thread_id uuid,lock_token uuid NOT NULL,status text NOT NULL,
 lease_expires_at timestamptz,completed_at timestamptz,last_error text,updated_at timestamptz NOT NULL DEFAULT now());
 CREATE INDEX idx_session_executors_job ON session_executors(org_id,job_id);
 CREATE UNIQUE INDEX idx_session_executors_one_active_unthreaded ON session_executors(org_id,session_id)
 WHERE thread_id IS NULL AND status IN ('starting','running','draining');
 CREATE UNIQUE INDEX idx_session_executors_one_active_thread ON session_executors(org_id,session_id,thread_id)
 WHERE thread_id IS NOT NULL AND status IN ('starting','running','draining');
 CREATE INDEX idx_jobs_org_dequeue ON jobs(org_id,queue,status,run_at,priority DESC);
 CREATE TABLE thread_runtimes(id uuid PRIMARY KEY,org_id uuid NOT NULL,session_id uuid NOT NULL REFERENCES sessions(id),
 thread_id uuid NOT NULL REFERENCES session_threads(id),status text NOT NULL,lease_expires_at timestamptz,
 closed_at timestamptz,stop_reason text,last_error text,updated_at timestamptz NOT NULL DEFAULT now());
 CREATE INDEX idx_thread_runtimes_session_status ON thread_runtimes(org_id,session_id,status);
 CREATE TABLE thread_inbox_entries(id uuid PRIMARY KEY,org_id uuid NOT NULL,session_id uuid NOT NULL REFERENCES sessions(id),
 thread_id uuid NOT NULL REFERENCES session_threads(id),runtime_id uuid REFERENCES thread_runtimes(id),
 owner_node_id text,delivery_state text NOT NULL,last_error text,updated_at timestamptz NOT NULL DEFAULT now());
 CREATE INDEX idx_thread_inbox_entries_session_delivery ON thread_inbox_entries(org_id,session_id,delivery_state);
 ALTER TABLE session_sandbox_holders ADD CONSTRAINT review_workspace_holders_session_fk FOREIGN KEY(session_id) REFERENCES sessions(id);
 CREATE INDEX idx_session_sandbox_holders_session_active ON session_sandbox_holders(org_id,session_id,expires_at)
 WHERE status IN ('active','draining');
 CREATE TABLE session_cancel_requests(org_id uuid NOT NULL,session_id uuid NOT NULL,delivered_at timestamptz);`)
	require.NoError(t, err, "isolated ownership tables should support lease fencing")
	f.params = RecoverIncompleteCodeReviewWorkspaceParams{
		SessionID: f.session, ContainerID: "incomplete-container", Repository: "example/repo",
		BaseSHA: strings.Repeat("b", 40), HeadSHA: strings.Repeat("a", 40),
		PreparationJobID: f.job, PreparationLockToken: f.token, WaitingJobID: uuid.New(), WaitingLockToken: uuid.New(),
	}
	revision, err := json.Marshal(map[string]string{"github_repo": f.params.Repository, "base_sha": f.params.BaseSHA, "head_sha": f.params.HeadSHA})
	require.NoError(t, err, "captured workspace revision should encode")
	batch := &pgx.Batch{}
	batch.Queue(`INSERT INTO session_threads(id,org_id,session_id,status) VALUES($1,$2,$3,'running')`, f.ownerThread, f.org, f.session)
	batch.Queue(`UPDATE sessions SET status='running',container_id=$2,worker_node_id=$3,
 turn_holding_container=true,sandbox_state='running',snapshot_key='prior-snapshot',revision_context=$4 WHERE id=$1`, f.session, f.params.ContainerID, f.node, revision)
	batch.Queue(`UPDATE jobs SET job_type='continue_session',payload=jsonb_build_object('session_id',$1::text),
 lease_expires_at=now()-interval '10 minutes' WHERE id=$2`, f.session, f.job)
	batch.Queue(`INSERT INTO jobs(id,org_id,queue,job_type,status,payload,lock_token,lease_expires_at)
 VALUES($1,$2,'agent','continue_session','running',jsonb_build_object('session_id',$3::text),$4,now()+interval '5 minutes')`, f.params.WaitingJobID, f.org, f.session, f.params.WaitingLockToken)
	batch.Queue(`INSERT INTO session_executors(id,org_id,session_id,job_id,thread_id,lock_token,status,lease_expires_at)
 VALUES($1,$2,$3,$4,$5,$6,'running',now()-interval '10 minutes')`, f.ownerExecutor, f.org, f.session, f.job, f.ownerThread, f.token)
	batch.Queue(`INSERT INTO thread_runtimes(id,org_id,session_id,thread_id,status,lease_expires_at)
 VALUES($1,$2,$3,$4,'live',now()-interval '10 minutes')`, f.ownerRuntime, f.org, f.session, f.ownerThread)
	batch.Queue(`INSERT INTO session_sandbox_holders(org_id,session_id,container_id,holder_kind,holder_id,owner_node_id,lease_token,status,heartbeat_at,expires_at)
 VALUES($1,$2,$3,'code_review',$4,$5,$6,'active',now(),now()+interval '1 hour')`, f.org, f.session, f.params.ContainerID, f.review, f.node, f.token)
	require.NoError(t, f.pool.SendBatch(ctx, batch).Close(), "seed crashed executor ownership and a live waiter on a healthy node")
	return f
}

func TestIncompleteReviewWorkspaceRecoveryLeaseFencesPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		change        string
		wantRecovered bool
	}{
		{name: "hard executor crash on healthy node", wantRecovered: true},
		{name: "live preparation job", change: "job"},
		{name: "live preparation executor", change: "executor"},
		{name: "live preparation runtime", change: "runtime"},
		{name: "live sibling executor", change: "sibling"},
		{name: "live sibling runtime", change: "sibling-runtime"},
		{name: "cancelled waiting job", change: "cancelled-waiter"},
		{name: "unknown preparation lease", change: "unknown-expiry"},
		{name: "workspace generation advanced", change: "generation"},
		{name: "captured head changed", change: "head"},
		{name: "preview holds container", change: "preview"},
		{name: "live sandbox holder", change: "holder"},
		{name: "waiting job lost lease", change: "waiter"},
		{name: "replacement container", change: "replacement"},
		{name: "same job new attempt", change: "same-job", wantRecovered: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newIncompleteReviewWorkspaceFixture(t)
			ctx := context.Background()
			var err error
			switch tt.change {
			case "job":
				_, err = f.pool.Exec(ctx, `UPDATE jobs SET lease_expires_at=now()+interval '5 minutes' WHERE id=$1`, f.job)
			case "executor":
				_, err = f.pool.Exec(ctx, `UPDATE session_executors SET lease_expires_at=now()+interval '5 minutes' WHERE id=$1`, f.ownerExecutor)
			case "runtime":
				_, err = f.pool.Exec(ctx, `UPDATE thread_runtimes SET lease_expires_at=now()+interval '5 minutes' WHERE id=$1`, f.ownerRuntime)
			case "sibling":
				_, err = f.pool.Exec(ctx, `INSERT INTO session_executors(id,org_id,session_id,job_id,thread_id,lock_token,status,lease_expires_at)
 VALUES($1,$2,$3,$4,$5,$6,'running',now()+interval '5 minutes')`, uuid.New(), f.org, f.session, f.job, uuid.New(), uuid.New())
			case "sibling-runtime":
				thread := uuid.New()
				_, err = f.pool.Exec(ctx, `INSERT INTO session_threads(id,org_id,session_id,status) VALUES($1,$2,$3,'running')`, thread, f.org, f.session)
				require.NoError(t, err, "sibling thread should satisfy the production runtime foreign key")
				_, err = f.pool.Exec(ctx, `INSERT INTO thread_runtimes(id,org_id,session_id,thread_id,status,lease_expires_at)
 VALUES($1,$2,$3,$4,'live',now()+interval '5 minutes')`, uuid.New(), f.org, f.session, thread)
			case "cancelled-waiter":
				_, err = f.pool.Exec(ctx, `UPDATE jobs SET status='cancelled' WHERE id=$1`, f.params.WaitingJobID)
			case "unknown-expiry":
				_, err = f.pool.Exec(ctx, `UPDATE jobs SET lease_expires_at=NULL WHERE id=$1`, f.job)
			case "generation":
				_, err = f.pool.Exec(ctx, `UPDATE sessions SET workspace_generation=1 WHERE id=$1`, f.session)
			case "head":
				_, err = f.pool.Exec(ctx, `UPDATE sessions SET revision_context=jsonb_set(revision_context,'{head_sha}','"new-head"') WHERE id=$1`, f.session)
			case "preview":
				_, err = f.pool.Exec(ctx, `INSERT INTO preview_instances VALUES($1,$2,true)`, f.org, f.session)
			case "holder":
				_, err = f.pool.Exec(ctx, `INSERT INTO session_sandbox_holders(org_id,session_id,container_id,holder_kind,holder_id,owner_node_id,lease_token,status,heartbeat_at,expires_at)
 VALUES($1,$2,$3,'thread_runtime',$4,$5,$6,'active',now(),now()+interval '5 minutes')`, f.org, f.session, f.params.ContainerID, uuid.New(), f.node, uuid.New())
			case "waiter":
				_, err = f.pool.Exec(ctx, `UPDATE jobs SET lease_expires_at=now()-interval '1 minute' WHERE id=$1`, f.params.WaitingJobID)
			case "replacement":
				_, err = f.pool.Exec(ctx, `UPDATE sessions SET container_id='replacement-container' WHERE id=$1`, f.session)
			case "same-job":
				_, err = f.pool.Exec(ctx, `DELETE FROM jobs WHERE id=$1`, f.params.WaitingJobID)
				require.NoError(t, err, "same-job retry should replace the unrelated waiter")
				_, err = f.pool.Exec(ctx, `UPDATE jobs SET lock_token=$1,lease_expires_at=now()+interval '5 minutes' WHERE id=$2`, f.params.WaitingLockToken, f.job)
				f.params.WaitingJobID = f.job
			}
			require.NoError(t, err, "ownership race fixture should be prepared")
			recovered, err := NewSessionStore(f.pool).RecoverIncompleteCodeReviewWorkspace(ctx, f.org, f.params)
			require.NoError(t, err, "exact lease recovery should execute without errors")
			require.Equal(t, tt.wantRecovered, recovered, "only a fully fenced preparation owner may lose its workspace")
			if recovered {
				var container, snapshot *string
				var generation int64
				var holding bool
				require.NoError(t, f.pool.QueryRow(ctx, `SELECT container_id,snapshot_key,workspace_generation,turn_holding_container FROM sessions WHERE id=$1`, f.session).Scan(&container, &snapshot, &generation, &holding), "recovered source should expose the cold retry state")
				require.Nil(t, container, "recovery should revoke the incomplete source container")
				require.Nil(t, snapshot, "cold retry must not reuse a potentially incomplete checkpoint")
				require.Equal(t, int64(1), generation, "recovery should advance the workspace generation")
				require.False(t, holding, "crashed preparation must release the stuck turn hold")
				var executor, runtime, holder string
				require.NoError(t, f.pool.QueryRow(ctx, `SELECT status FROM session_executors WHERE id=$1`, f.ownerExecutor).Scan(&executor), "read fenced executor")
				require.NoError(t, f.pool.QueryRow(ctx, `SELECT status FROM thread_runtimes WHERE id=$1`, f.ownerRuntime).Scan(&runtime), "read fenced runtime")
				require.NoError(t, f.pool.QueryRow(ctx, `SELECT status FROM session_sandbox_holders WHERE holder_id=$1`, f.review).Scan(&holder), "read released retention")
				require.Equal(t, "lost", executor, "expired executor must not renew after recovery")
				require.Equal(t, "lost", runtime, "expired runtime must not renew after recovery")
				require.Equal(t, "expired", holder, "review retention must not pin the incomplete source")
			}
		})
	}
}

func TestIncompleteReviewWorkspaceRecoveryRenewalRacePostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, table string
		executor    bool
	}{
		{name: "job lease renewed before recovery locks it", table: "jobs"},
		{name: "executor lease renewed before recovery locks it", table: "session_executors", executor: true},
		{name: "replacement container wins session CAS", table: "sessions"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newIncompleteReviewWorkspaceFixture(t)
			ctx := context.Background()
			tx, err := f.pool.Begin(ctx)
			require.NoError(t, err, "renewing owner should lock its lease")
			defer func() { _ = tx.Rollback(context.Background()) }()
			id := f.job
			if tt.executor {
				id = f.ownerExecutor
			}
			if tt.table == "sessions" {
				_, err = tx.Exec(ctx, `UPDATE sessions SET container_id='replacement-container' WHERE id=$1`, f.session)
			} else {
				_, err = tx.Exec(ctx, `UPDATE `+tt.table+` SET lease_expires_at=now()+interval '5 minutes' WHERE id=$1`, id)
			}
			require.NoError(t, err, "live owner renewal should remain uncommitted during the race")
			done := make(chan struct{})
			var recovered bool
			var recoveryErr error
			go func() {
				recovered, recoveryErr = NewSessionStore(f.pool).RecoverIncompleteCodeReviewWorkspace(ctx, f.org, f.params)
				close(done)
			}()
			select {
			case <-done:
				t.Fatal("recovery must serialize behind the owner's lease update")
			case <-time.After(40 * time.Millisecond):
			}
			require.NoError(t, tx.Commit(ctx), "owner renewal should win the row lock race")
			select {
			case <-done:
			case <-time.After(3 * time.Second):
				t.Fatal("recovery should finish after ownership commits")
			}
			require.NoError(t, recoveryErr, "renewal race should resolve without a database error")
			require.False(t, recovered, "renewed ownership must prevent clearing the live workspace")
		})
	}
}

type reviewWorkspaceWaitingPeer struct {
	job, token, executor, thread, result uuid.UUID
}

func (f incompleteReviewWorkspaceFixture) addWaitingPeer(t *testing.T, jobID, token uuid.UUID) reviewWorkspaceWaitingPeer {
	t.Helper()
	peer := reviewWorkspaceWaitingPeer{job: jobID, token: token, executor: uuid.New(), thread: uuid.New(), result: uuid.New()}
	batch := &pgx.Batch{}
	batch.Queue(`INSERT INTO jobs(id,org_id,queue,job_type,status,payload,lock_token,lease_expires_at)
 VALUES($1,$2,'agent','continue_session','running',jsonb_build_object('session_id',$3::text,'thread_id',$4::text),$5,now()+interval '5 minutes')
 ON CONFLICT(id) DO UPDATE SET payload=EXCLUDED.payload`, peer.job, f.org, f.session, peer.thread, peer.token)
	batch.Queue(`INSERT INTO session_threads(id,org_id,session_id,status,execution_mode,filesystem_mode,created_by_source)
 VALUES($1,$2,$3,'running','review','read_only','system')`, peer.thread, f.org, f.session)
	batch.Queue(`INSERT INTO session_executors(id,org_id,session_id,job_id,thread_id,lock_token,status,lease_expires_at)
 VALUES($1,$2,$3,$4,$5,$6,'running',now()+interval '5 minutes')`, peer.executor, f.org, f.session, peer.job, peer.thread, peer.token)
	batch.Queue(`INSERT INTO code_review_agent_results(id,org_id,session_id,role,status,structured_result)
 VALUES($1,$2,$3,'reviewer','queued',jsonb_build_object('thread_id',$4::text,'read_only',true))`, peer.result, f.org, f.session, peer.thread)
	require.NoError(t, f.pool.SendBatch(context.Background(), batch).Close(), "persist a reviewer waiting before its first runtime")
	return peer
}

func TestIncompleteReviewWorkspaceRecoveryTwoWaitingReviewersPostgres(t *testing.T) {
	t.Parallel()
	f := newIncompleteReviewWorkspaceFixture(t)
	first := f.addWaitingPeer(t, f.params.WaitingJobID, f.params.WaitingLockToken)
	second := f.addWaitingPeer(t, uuid.New(), uuid.New())
	secondParams := f.params
	secondParams.WaitingJobID, secondParams.WaitingLockToken = second.job, second.token
	type result struct {
		recovered bool
		err       error
	}
	results := make(chan result, 2)
	start := make(chan struct{})
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	for _, params := range []RecoverIncompleteCodeReviewWorkspaceParams{f.params, secondParams} {
		go func() {
			<-start
			recovered, err := NewSessionStore(f.pool).RecoverIncompleteCodeReviewWorkspace(ctx, f.org, params)
			results <- result{recovered: recovered, err: err}
		}()
	}
	close(start)
	winners := 0
	for range 2 {
		got := <-results
		require.NoError(t, got.err, "peer recovery transactions should serialize without exhausting their leases")
		if got.recovered {
			winners++
		}
	}
	require.Equal(t, 1, winners, "exactly one reviewer should revoke the crashed source")
	var generation int64
	require.NoError(t, f.pool.QueryRow(ctx, `SELECT workspace_generation FROM sessions WHERE id=$1`, f.session).Scan(&generation), "read the recovered generation")
	require.Equal(t, int64(1), generation, "peer retries should advance source generation once")
	for _, peer := range []reviewWorkspaceWaitingPeer{first, second} {
		var jobStatus, executorStatus string
		var token uuid.UUID
		require.NoError(t, f.pool.QueryRow(ctx, `SELECT status,lock_token FROM jobs WHERE id=$1`, peer.job).Scan(&jobStatus, &token), "read surviving peer job")
		require.Equal(t, "running", jobStatus, "waiting peer should retain its current lease for the normal cold-retry path")
		require.Equal(t, peer.token, token, "recovery must preserve sibling ownership")
		require.NoError(t, f.pool.QueryRow(ctx, `SELECT status FROM session_executors WHERE id=$1`, peer.executor).Scan(&executorStatus), "read surviving peer executor")
		require.Equal(t, "running", executorStatus, "waiting peer executor must not be fenced as the crashed owner")
	}
}

func TestIncompleteReviewWorkspaceRecoveryRequiresUnstartedReviewerPeersPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct{ name, change string }{
		{name: "peer already started a runtime", change: "runtime"},
		{name: "peer has historical runtime", change: "lost-runtime"},
		{name: "peer turn already advanced", change: "turn"},
		{name: "peer can write", change: "write"},
		{name: "peer executor has stale token", change: "token"},
		{name: "peer executor lease unknown", change: "lease"},
		{name: "peer is not a platform reviewer", change: "source"},
		{name: "peer has ambiguous role", change: "role"},
		{name: "peer is a new preparation owner", change: "prepare"},
		{name: "peer thread was cancelled", change: "cancel"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newIncompleteReviewWorkspaceFixture(t)
			peer := f.addWaitingPeer(t, uuid.New(), uuid.New())
			ctx := context.Background()
			var err error
			switch tt.change {
			case "runtime", "lost-runtime":
				status := "live"
				if tt.change == "lost-runtime" {
					status = "lost"
				}
				_, err = f.pool.Exec(ctx, `INSERT INTO thread_runtimes(id,org_id,session_id,thread_id,status,lease_expires_at)
 VALUES($1,$2,$3,$4,$5,now()+interval '5 minutes')`, uuid.New(), f.org, f.session, peer.thread, status)
			case "turn":
				_, err = f.pool.Exec(ctx, `UPDATE session_threads SET current_turn=1 WHERE id=$1`, peer.thread)
			case "write":
				_, err = f.pool.Exec(ctx, `UPDATE session_threads SET filesystem_mode='read_write' WHERE id=$1`, peer.thread)
			case "token":
				_, err = f.pool.Exec(ctx, `UPDATE session_executors SET lock_token=$2 WHERE id=$1`, peer.executor, uuid.New())
			case "lease":
				_, err = f.pool.Exec(ctx, `UPDATE session_executors SET lease_expires_at=NULL WHERE id=$1`, peer.executor)
			case "source":
				_, err = f.pool.Exec(ctx, `UPDATE session_threads SET created_by_source='user' WHERE id=$1`, peer.thread)
			case "role":
				_, err = f.pool.Exec(ctx, `INSERT INTO code_review_agent_results(id,org_id,session_id,role,status,structured_result)
 VALUES($1,$2,$3,'orchestrator','running',jsonb_build_object('thread_id',$4::text))`, uuid.New(), f.org, f.session, peer.thread)
			case "prepare":
				_, err = f.pool.Exec(ctx, `UPDATE jobs SET job_type='prepare_code_review_workspace' WHERE id=$1`, peer.job)
			case "cancel":
				_, err = f.pool.Exec(ctx, `UPDATE session_threads SET cancel_requested_at=now() WHERE id=$1`, peer.thread)
			}
			require.NoError(t, err, "peer precondition should be changed")
			recovered, err := NewSessionStore(f.pool).RecoverIncompleteCodeReviewWorkspace(ctx, f.org, f.params)
			require.NoError(t, err, "unproven peer should fail closed without a storage error")
			require.False(t, recovered, "only reviewers proved to be waiting before their first runtime may survive source reset")
		})
	}
}

func TestIncompleteReviewWorkspaceRecoveryPeerRuntimeRacePostgres(t *testing.T) {
	t.Parallel()
	f := newIncompleteReviewWorkspaceFixture(t)
	peer := f.addWaitingPeer(t, uuid.New(), uuid.New())
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	tx, err := f.pool.Begin(ctx)
	require.NoError(t, err, "peer runtime creation should have its own transaction")
	defer func() { _ = tx.Rollback(context.Background()) }()
	_, err = tx.Exec(ctx, `INSERT INTO thread_runtimes(id,org_id,session_id,thread_id,status,lease_expires_at)
 VALUES($1,$2,$3,$4,'starting',now()+interval '5 minutes')`, uuid.New(), f.org, f.session, peer.thread)
	require.NoError(t, err, "starting runtime should hold the peer thread foreign-key lock before recovery")
	assertWorkspaceUnchanged := func(stage string) {
		t.Helper()
		var container *string
		var generation int64
		require.NoError(t, f.pool.QueryRow(ctx, `SELECT container_id,workspace_generation FROM sessions WHERE org_id=$1 AND id=$2`, f.org, f.session).Scan(&container, &generation), "source workspace should remain readable %s", stage)
		require.Equal(t, &f.params.ContainerID, container, "source container must remain unchanged %s", stage)
		require.Equal(t, f.params.WorkspaceGeneration, generation, "source generation must remain unchanged %s", stage)
	}
	store := NewSessionStore(f.pool)
	recovered, recoveryErr := store.RecoverIncompleteCodeReviewWorkspace(ctx, f.org, f.params)
	require.False(t, recovered, "uncommitted peer runtime creation must never authorize source recovery")
	var postgresError *pgconn.PgError
	require.ErrorAs(t, recoveryErr, &postgresError, "peer runtime contention should retain its PostgreSQL waiting classification")
	require.Equal(t, "55P03", postgresError.Code, "peer runtime contention must enforce the existing bounded lock wait")
	assertWorkspaceUnchanged("while peer runtime creation remains uncommitted")
	require.NoError(t, tx.Commit(ctx), "peer runtime creation should commit after the bounded recovery probe")
	recovered, recoveryErr = store.RecoverIncompleteCodeReviewWorkspace(ctx, f.org, f.params)
	require.NoError(t, recoveryErr, "committed peer runtime should protect its workspace without a storage error")
	require.False(t, recovered, "a peer that started execution must preserve its source container")
	assertWorkspaceUnchanged("after peer runtime creation commits")
}

func TestIncompleteReviewWorkspaceRecoveryHealthyOwnerUsesReadOnlyPrecheckPostgres(t *testing.T) {
	t.Parallel()
	for _, executor := range []bool{false, true} {
		t.Run(map[bool]string{false: "job lease", true: "executor lease"}[executor], func(t *testing.T) {
			t.Parallel()
			f := newIncompleteReviewWorkspaceFixture(t)
			ctx := context.Background()
			table, id := "jobs", f.job
			if executor {
				table, id = "session_executors", f.ownerExecutor
			}
			_, err := f.pool.Exec(ctx, `UPDATE `+table+` SET lease_expires_at=now()+interval '5 minutes' WHERE id=$1`, id)
			require.NoError(t, err, "healthy owner lease should be committed")
			tx, err := f.pool.Begin(ctx)
			require.NoError(t, err, "an unrelated heartbeat should hold the owner row lock")
			defer func() { _ = tx.Rollback(context.Background()) }()
			_, err = tx.Exec(ctx, `SELECT id FROM `+table+` WHERE id=$1 FOR UPDATE`, id)
			require.NoError(t, err, "heartbeat should retain its row lock across readiness polls")
			recorder := &recoveryTransactionRecorder{TxStarter: f.pool}
			store := NewSessionStore(recorder)
			probeCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			for range 5 {
				recovered, err := store.RecoverIncompleteCodeReviewWorkspace(probeCtx, f.org, f.params)
				require.NoError(t, err, "healthy preparation polls must not wait for heartbeat row locks")
				require.False(t, recovered, "a healthy preparer cannot be recovered")
			}
			require.Zero(t, recorder.begins.Load(), "healthy preparation polling must avoid recovery transactions entirely")
		})
	}
}

func TestIncompleteReviewWorkspaceRecoveryLocksAreBoundedPostgres(t *testing.T) {
	t.Parallel()
	f := newIncompleteReviewWorkspaceFixture(t)
	ctx := context.Background()
	tx, err := f.pool.Begin(ctx)
	require.NoError(t, err, "competing ownership transaction should exist")
	defer func() { _ = tx.Rollback(context.Background()) }()
	_, err = tx.Exec(ctx, `SELECT id FROM jobs WHERE id=$1 FOR UPDATE`, f.job)
	require.NoError(t, err, "competing ownership transaction should lock the exact owner")
	started := time.Now()
	recovered, err := NewSessionStore(f.pool).RecoverIncompleteCodeReviewWorkspace(ctx, f.org, f.params)
	require.False(t, recovered, "lock contention must never authorize source destruction")
	var postgresError *pgconn.PgError
	require.True(t, errors.As(err, &postgresError), "lock contention should retain its PostgreSQL waiting classification")
	require.Equal(t, "55P03", postgresError.Code, "recovery must enforce the local lock timeout")
	require.Less(t, time.Since(started), time.Second, "recovery must remain within one readiness probe budget")
}

func TestIncompleteReviewWorkspaceRecoveryExecutorForeignKeysAvoidDeadlockPostgres(t *testing.T) {
	t.Parallel()
	f := newIncompleteReviewWorkspaceFixture(t)
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	actor, err := f.pool.Begin(ctx)
	require.NoError(t, err, "concurrent executor creation should have its own transaction")
	defer func() { _ = actor.Rollback(context.Background()) }()
	_, err = actor.Exec(ctx, `SELECT id FROM thread_runtimes WHERE id=$1 FOR UPDATE`, f.ownerRuntime)
	require.NoError(t, err, "concurrent runtime heartbeat should pause the recovery after its source locks")
	locked := make(chan struct{})
	recorder := &recoveryTransactionRecorder{TxStarter: f.pool, sessionLocked: locked}
	done := make(chan struct{})
	var recovered bool
	var recoveryErr error
	go func() {
		recovered, recoveryErr = NewSessionStore(recorder).RecoverIncompleteCodeReviewWorkspace(ctx, f.org, f.params)
		close(done)
	}()
	select {
	case <-locked:
	case <-ctx.Done():
		t.Fatal("recovery should acquire its bounded source lock")
	}
	_, err = actor.Exec(ctx, `INSERT INTO session_executors(id,org_id,session_id,job_id,thread_id,lock_token,status,lease_expires_at)
 VALUES($1,$2,$3,$4,$5,$6,'starting',now()+interval '5 minutes')`, uuid.New(), f.org, f.session, f.params.WaitingJobID, uuid.New(), uuid.New())
	require.NoError(t, err, "executor session-then-job foreign-key locks must remain compatible with recovery's unchanged keys")
	require.NoError(t, actor.Commit(ctx), "concurrent executor ownership should commit without an FK lock cycle")
	select {
	case <-done:
	case <-ctx.Done():
		t.Fatal("source recovery should resolve after executor creation")
	}
	require.NoError(t, recoveryErr, "executor foreign-key race should avoid deadlock or lock exhaustion")
	require.False(t, recovered, "a newly live unproven executor must protect the source container")
}

func TestIncompleteReviewWorkspaceRecoveryRepairsExactRuntimeInboxPostgres(t *testing.T) {
	t.Parallel()
	f := newIncompleteReviewWorkspaceFixture(t)
	ctx := context.Background()
	ids := map[string]uuid.UUID{"delivering": uuid.New(), "delivered": uuid.New(), "acked": uuid.New(), "foreign": uuid.New()}
	for state, id := range ids {
		org := f.org
		if state == "foreign" {
			org, state = uuid.New(), "delivering"
		}
		_, err := f.pool.Exec(ctx, `INSERT INTO thread_inbox_entries(id,org_id,session_id,thread_id,runtime_id,owner_node_id,delivery_state)
 VALUES($1,$2,$3,$4,$5,$6,$7)`, id, org, f.session, f.ownerThread, f.ownerRuntime, f.node, state)
		require.NoError(t, err, "owned and foreign inbox delivery states should exist")
	}
	_, err := f.pool.Exec(ctx, `INSERT INTO session_sandbox_holders(org_id,session_id,container_id,holder_kind,holder_id,owner_node_id,lease_token,status,heartbeat_at,expires_at)
 VALUES($1,$2,$3,'thread_runtime',$4,$5,$6,'active',now(),now()-interval '1 minute')`, f.org, f.session, f.params.ContainerID, f.ownerRuntime, f.node, f.token)
	require.NoError(t, err, "expired runtime retention should exist")
	recovered, err := NewSessionStore(f.pool).RecoverIncompleteCodeReviewWorkspace(ctx, f.org, f.params)
	require.NoError(t, err, "crashed preparation should recover exact runtime state")
	require.True(t, recovered, "expired owner should expose a cold retry")
	for state, id := range ids {
		var actual string
		var runtime *uuid.UUID
		var node, detail *string
		require.NoError(t, f.pool.QueryRow(ctx, `SELECT delivery_state,runtime_id,owner_node_id,last_error FROM thread_inbox_entries WHERE id=$1`, id).Scan(&actual, &runtime, &node, &detail), "read recovered inbox receipt")
		switch state {
		case "delivering":
			require.Equal(t, "pending", actual, "unsent seed delivery must become retryable")
			require.Nil(t, runtime, "pending seed delivery must release the lost runtime")
			require.Nil(t, node, "pending seed delivery must release the old node")
			require.NotNil(t, detail, "lost delivery should retain a recovery reason")
		case "delivered":
			require.Equal(t, "unknown_delivery", actual, "already-sent delivery cannot be blindly sent again")
			require.Equal(t, &f.ownerRuntime, runtime, "uncertain delivery should preserve the original receipt identity")
		case "acked":
			require.Equal(t, "acked", actual, "acknowledged delivery must remain final")
		case "foreign":
			require.Equal(t, "delivering", actual, "recovery must preserve another tenant's inbox row")
			require.Nil(t, detail, "another tenant's delivery details must remain untouched")
		}
	}
	var closed *time.Time
	var reason, detail *string
	require.NoError(t, f.pool.QueryRow(ctx, `SELECT closed_at,stop_reason,last_error FROM thread_runtimes WHERE id=$1`, f.ownerRuntime).Scan(&closed, &reason, &detail), "read terminal runtime recovery details")
	require.NotNil(t, closed, "lost runtime should have a close timestamp")
	require.NotNil(t, reason, "lost runtime should record a stop reason")
	require.Equal(t, "review_preparation_lease_expired", *reason, "lost runtime should record its precise recovery boundary")
	require.NotNil(t, detail, "lost runtime should retain a terminal error")
	var holder string
	require.NoError(t, f.pool.QueryRow(ctx, `SELECT status FROM session_sandbox_holders WHERE holder_id=$1`, f.ownerRuntime).Scan(&holder), "read lost runtime holder")
	require.Equal(t, "expired", holder, "lost runtime retention must not pin the incomplete source")
}

func TestIncompleteReviewWorkspaceRecoveryUsesBoundedIndexesPostgres(t *testing.T) {
	t.Parallel()
	f := newIncompleteReviewWorkspaceFixture(t)
	ctx := context.Background()
	_, err := f.pool.Exec(ctx, `INSERT INTO jobs(id,org_id,queue,job_type,status,run_at)
 SELECT gen_random_uuid(),$1,'agent','continue_session','succeeded',now()-interval '1 day' FROM generate_series(1,12000)`, f.org)
	require.NoError(t, err, "a synthetic organization history should contain many terminal jobs")
	_, err = f.pool.Exec(ctx, `ANALYZE jobs`)
	require.NoError(t, err, "the isolated planner should know the synthetic terminal-job cohort")
	recorder := &recoveryTransactionRecorder{TxStarter: f.pool}
	recovered, err := NewSessionStore(recorder).RecoverIncompleteCodeReviewWorkspace(ctx, f.org, f.params)
	require.NoError(t, err, "bounded recovery should execute against a large terminal-job history")
	require.True(t, recovered, "terminal history must not prevent exact expired-owner recovery")
	for _, query := range []struct {
		name, sql, index string
		arguments        []any
	}{
		{name: "read-only owner precheck", sql: recorder.precheckSQL, index: "jobs_pkey", arguments: recorder.precheckArgs},
		{name: "active source candidates", sql: recorder.candidatesSQL, index: "idx_jobs_org_dequeue", arguments: recorder.candidatesArgs},
	} {
		rows, err := f.pool.Query(ctx, "EXPLAIN "+query.sql, query.arguments...)
		require.NoError(t, err, "the actual recovery query should expose its plan: %s", query.name)
		lines, err := pgx.CollectRows(rows, pgx.RowTo[string])
		require.NoError(t, err, "recovery query plan should be readable")
		plan := strings.Join(lines, "\n")
		require.Contains(t, plan, query.index, "recovery should use its production owner/active-source index: %s", query.name)
		require.NotContains(t, plan, "Seq Scan on jobs", "recovery must not scan terminal organization history: %s", query.name)
	}
}
