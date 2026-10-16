package db

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/models"
)

func seedRuntimeSeedThread(t *testing.T, pool *pgxpool.Pool) (uuid.UUID, uuid.UUID, uuid.UUID) {
	t.Helper()
	ctx := context.Background()
	orgID := uuid.New()
	_, err := pool.Exec(ctx, `INSERT INTO organizations(id,name,settings) VALUES($1,$2,'{}')`, orgID, "seed-test-"+orgID.String())
	require.NoError(t, err, "seed independent runtime tenant")
	session := &models.Session{OrgID: orgID, AgentType: models.DefaultDefaultAgentType, Status: models.SessionStatusIdle, AutonomyLevel: models.DefaultSessionAutonomy, TokenMode: models.SessionTokenModeLow, Origin: models.SessionOriginManual, InteractionMode: models.SessionInteractionModeInteractive, ValidationPolicy: models.SessionValidationPolicyOnSessionEnd}
	require.NoError(t, NewSessionStore(pool).Create(ctx, session), "create session with durable primary thread")
	require.NotNil(t, session.PrimaryThreadID, "session must have its own primary thread")
	return orgID, session.ID, *session.PrimaryThreadID
}

func createSeedRuntime(t *testing.T, pool *pgxpool.Pool, orgID, sessionID, threadID uuid.UUID) models.ThreadRuntime {
	t.Helper()
	sequence, err := NewThreadInboxStore(pool).LastAcknowledgedSequence(context.Background(), orgID, threadID)
	require.NoError(t, err, "load contiguous applied seed prefix")
	runtime, err := NewThreadRuntimeStore(pool).CreateStarting(context.Background(), orgID, CreateThreadRuntimeParams{SessionID: sessionID, ThreadID: threadID, SandboxID: uuid.New(), AgentType: models.DefaultDefaultAgentType, OwnerNodeID: "test-worker", LeaseToken: uuid.New(), LastAckedSequence: sequence, LastDeliveredSequence: sequence, LeaseDuration: time.Minute})
	require.NoError(t, err, "create isolated active seed runtime")
	return runtime
}

func seedRuntimeInbox(t *testing.T, pool *pgxpool.Pool, orgID, sessionID, threadID uuid.UUID, sequence int64, state string, runtimeID *uuid.UUID) {
	t.Helper()
	_, err := pool.Exec(context.Background(), `INSERT INTO thread_inbox_entries(org_id,session_id,thread_id,sequence_no,message_id,entry_type,delivery_state,runtime_id) VALUES($1,$2,$3,$4,$4,'user_message',$5,$6)`, orgID, sessionID, threadID, sequence, state, runtimeID)
	require.NoError(t, err, "seed synthetic durable user instruction")
}

func TestThreadRuntimeStorePostgresClosureReleasesOnlyUndeliveredSeeds(t *testing.T) {
	t.Parallel()
	pool := newFeedbackLifecyclePool(t)
	for _, tt := range []struct {
		name   string
		status models.ThreadRuntimeStatus
	}{
		{"normal close", models.ThreadRuntimeStatusClosed}, {"pre-provider failure", models.ThreadRuntimeStatusFailed},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			orgID, sessionID, threadID := seedRuntimeSeedThread(t, pool)
			runtime := createSeedRuntime(t, pool, orgID, sessionID, threadID)
			seedRuntimeInbox(t, pool, orgID, sessionID, threadID, 1, "delivering", &runtime.ID)
			seedRuntimeInbox(t, pool, orgID, sessionID, threadID, 2, "delivered", &runtime.ID)
			changed, err := NewThreadRuntimeStore(pool).MarkTerminalWithLease(ctx, orgID, runtime.ID, runtime.LeaseToken, tt.status, "test", "test")
			require.NoError(t, err, "close owned runtime atomically")
			require.True(t, changed, "current owner must close its runtime")
			rows, err := pool.Query(ctx, `SELECT delivery_state, runtime_id IS NULL FROM thread_inbox_entries WHERE org_id=$1 AND thread_id=$2 ORDER BY sequence_no`, orgID, threadID)
			require.NoError(t, err, "read exact seed closure outcomes")
			type outcome struct {
				state   string
				unowned bool
			}
			var actual []outcome
			for rows.Next() {
				var item outcome
				require.NoError(t, rows.Scan(&item.state, &item.unowned), "scan seed closure outcome")
				actual = append(actual, item)
			}
			require.NoError(t, rows.Err(), "read all closure outcomes")
			rows.Close()
			require.Equal(t, []outcome{{"pending", true}, {"unknown_delivery", false}}, actual, "unsubmitted seeds remain replayable while possible live deliveries require explicit recovery")
		})
	}
}

func TestSessionActivityPhaseStorePostgresSeedOwnershipAndHistory(t *testing.T) {
	t.Parallel()
	pool := newFeedbackLifecyclePool(t)
	tests := []struct {
		name, seedState, oldStatus, scope           string
		historicalBatch, applied, startedBatch, gap bool
		providerHandle, priorPhase                  bool
		wantSuccess                                 bool
	}{
		{name: "new pending seed", seedState: "pending", wantSuccess: true},
		{name: "closed owner's unsent seed", seedState: "delivering", oldStatus: "closed", wantSuccess: true},
		{name: "terminal live owner cannot prove seed unsent", seedState: "delivering", oldStatus: "closed", providerHandle: true},
		{name: "terminal provider phase cannot prove seed unsent", seedState: "delivering", oldStatus: "closed", priorPhase: true},
		{name: "failed owner's unsent seed", seedState: "delivering", oldStatus: "failed", wantSuccess: true},
		{name: "lost owner's unsent seed", seedState: "delivering", oldStatus: "lost", wantSuccess: true},
		{name: "historical acknowledged pre-provider seed", seedState: "acked", oldStatus: "closed", historicalBatch: true, wantSuccess: true},
		{name: "possible live delivery requires explicit recovery", seedState: "unknown_delivery", oldStatus: "lost"},
		{name: "applied seed cannot execute twice", seedState: "acked", oldStatus: "closed", applied: true},
		{name: "started batch cannot be reopened", seedState: "acked", oldStatus: "closed", historicalBatch: true, startedBatch: true},
		{name: "live delivery ack cannot be replayed", seedState: "acked", oldStatus: "closed", historicalBatch: true, providerHandle: true},
		{name: "prior provider phase prevents replay", seedState: "acked", oldStatus: "closed", historicalBatch: true, priorPhase: true},
		{name: "unexplained ack cannot be replayed", seedState: "acked", oldStatus: "closed"},
		{name: "unselected earlier input cannot be skipped", seedState: "pending", gap: true},
		{name: "wrong organization", seedState: "pending", scope: "org"},
		{name: "wrong session", seedState: "pending", scope: "session"},
		{name: "wrong thread", seedState: "pending", scope: "thread"},
		{name: "wrong lease token", seedState: "pending", scope: "token"},
		{name: "expired lease", seedState: "pending", scope: "expired"},
		{name: "past start time cannot revive expired lease", seedState: "pending", scope: "expired_old_start"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			orgID, sessionID, threadID := seedRuntimeSeedThread(t, pool)
			var oldID *uuid.UUID
			if tt.oldStatus != "" {
				old := createSeedRuntime(t, pool, orgID, sessionID, threadID)
				_, err := pool.Exec(ctx, `UPDATE thread_runtimes SET status=$3,closed_at=now(),last_acked_sequence=1 WHERE org_id=$1 AND id=$2`, orgID, old.ID, tt.oldStatus)
				require.NoError(t, err, "seed terminal historical runtime")
				oldID = &old.ID
				if tt.providerHandle {
					_, err = pool.Exec(ctx, `UPDATE thread_runtimes SET runtime_handle_id='provider-handle' WHERE org_id=$1 AND id=$2`, orgID, old.ID)
					require.NoError(t, err, "seed evidence that previous runtime reached provider execution")
				}
				if tt.priorPhase {
					_, err = pool.Exec(ctx, `INSERT INTO session_activity_phases(org_id,session_id,thread_id,turn_number,phase_number,status,boundary_reason,started_at,completed_at,runtime_id,trigger_kind) VALUES($1,$2,$3,1,1,'completed','steered',now(),now(),$4,'initial')`, orgID, sessionID, threadID, old.ID)
					require.NoError(t, err, "seed existing provider phase before a live steering acknowledgment")
				}
			}
			sequence := int64(1)
			if tt.gap {
				seedRuntimeInbox(t, pool, orgID, sessionID, threadID, sequence, "pending", nil)
				sequence++
			}
			seedRuntimeInbox(t, pool, orgID, sessionID, threadID, sequence, tt.seedState, oldID)
			if tt.applied {
				_, err := pool.Exec(ctx, `UPDATE thread_inbox_entries SET applied_at=now(),acked_at=now() WHERE org_id=$1 AND thread_id=$2`, orgID, threadID)
				require.NoError(t, err, "seed positively applied earlier input")
			}
			var historicalID uuid.UUID
			if tt.historicalBatch {
				status := models.InboxDeliveryBatchAcknowledged
				var startedAt *time.Time
				if tt.startedBatch {
					status = models.InboxDeliveryBatchStarted
					value := time.Now().UTC()
					startedAt = &value
				}
				err := pool.QueryRow(ctx, `INSERT INTO thread_inbox_delivery_batches(org_id,session_id,thread_id,runtime_id,sequence_start,sequence_end,status,acknowledged_at,started_at) VALUES($1,$2,$3,$4,1,1,$5,now(),$6) RETURNING id`, orgID, sessionID, threadID, *oldID, status, startedAt).Scan(&historicalID)
				require.NoError(t, err, "seed historical durable batch without a launched phase")
			}
			runtime := createSeedRuntime(t, pool, orgID, sessionID, threadID)
			callOrg, callSession, callThread, token := orgID, sessionID, threadID, runtime.LeaseToken
			startedAt := time.Now().UTC()
			switch tt.scope {
			case "org":
				callOrg = uuid.New()
			case "session":
				callSession = uuid.New()
			case "thread":
				callThread = uuid.New()
			case "token":
				token = uuid.New()
			case "expired", "expired_old_start":
				if tt.scope == "expired_old_start" {
					startedAt = startedAt.Add(-10 * time.Minute)
				}
				_, err := pool.Exec(ctx, `UPDATE thread_runtimes SET lease_expires_at=now()-interval '1 minute' WHERE org_id=$1 AND id=$2`, orgID, runtime.ID)
				require.NoError(t, err, "expire current runtime lease")
			}
			phase, batch, err := NewSessionActivityPhaseStore(pool).StartSeedPhase(ctx, callOrg, callSession, callThread, 2, runtime.ID, token, []int64{sequence}, startedAt)
			if tt.wantSuccess {
				require.NoError(t, err, "eligible seed must start as one atomic activity phase")
				require.NotNil(t, batch, "durable seed must have an exact batch")
				require.Equal(t, models.ActivityPhaseTriggerInboxBatch, phase.TriggerKind, "provider start must be attributed to its seed batch")
				require.Equal(t, batch.ID, *phase.TriggerBatchID, "phase must reference the exact committed seed batch")
				durable, err := NewSessionActivityPhaseStore(pool).GetInboxDeliveryBatch(ctx, orgID, batch.ID)
				require.NoError(t, err, "reload committed seed batch")
				require.Equal(t, models.InboxDeliveryBatchStarted, durable.Status, "provider batch must be started before admission returns")
				require.Equal(t, runtime.ID, durable.RuntimeID, "replacement must own the committed batch")
				require.Equal(t, []int64{1, sequence}, []int64{durable.SequenceStart, durable.SequenceEnd}, "committed batch must cover the contiguous seed range")
				if tt.historicalBatch {
					require.Equal(t, historicalID, batch.ID, "positive pre-provider proof should reuse the unique historical range")
				}
				var state string
				var applied bool
				var owner uuid.UUID
				var watermark int64
				require.NoError(t, pool.QueryRow(ctx, `SELECT delivery_state,applied_at IS NOT NULL,runtime_id FROM thread_inbox_entries WHERE org_id=$1 AND thread_id=$2 AND sequence_no=$3`, orgID, threadID, sequence).Scan(&state, &applied, &owner), "reload exact committed seed state")
				require.Equal(t, "acked", state, "committed provider seed must be acknowledged")
				require.True(t, applied, "seed becomes applied together with provider-phase admission")
				require.Equal(t, runtime.ID, owner, "committed seed must belong to the current lease")
				require.NoError(t, pool.QueryRow(ctx, `SELECT last_acked_sequence FROM thread_runtimes WHERE org_id=$1 AND id=$2`, orgID, runtime.ID).Scan(&watermark), "reload committed watermark")
				require.Equal(t, sequence, watermark, "seed and runtime acknowledgment watermark must commit together")
				_, _, err = NewSessionActivityPhaseStore(pool).StartSeedPhase(ctx, orgID, sessionID, threadID, 2, runtime.ID, token, []int64{sequence}, time.Now().UTC())
				require.Error(t, err, "retry of a committed seed must never admit duplicate provider execution")
			} else {
				require.Error(t, err, "unowned, ambiguous, or noncontiguous seeds must not launch provider execution")
				var state string
				var applied bool
				var count int
				require.NoError(t, pool.QueryRow(ctx, `SELECT delivery_state,applied_at IS NOT NULL FROM thread_inbox_entries WHERE org_id=$1 AND thread_id=$2 AND sequence_no=$3`, orgID, threadID, sequence).Scan(&state, &applied), "reload rejected seed after rollback")
				require.Equal(t, tt.seedState, state, "failed admission must preserve original seed state")
				require.Equal(t, tt.applied, applied, "failed admission cannot mark input applied")
				require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM session_activity_phases WHERE org_id=$1 AND thread_id=$2`, orgID, threadID).Scan(&count), "count phases after fenced seed start")
				expectedPhases := 0
				if tt.priorPhase {
					expectedPhases = 1
				}
				require.Equal(t, expectedPhases, count, "failed admission must leave no new provider phase")
			}
		})
	}
}

func TestThreadInboxStorePostgresSeedMutationsFenceLateOwners(t *testing.T) {
	t.Parallel()
	pool := newFeedbackLifecyclePool(t)
	for _, tt := range []struct{ name, scope string }{{"late closed attacher", "closed"}, {"wrong lease", "token"}, {"wrong tenant", "org"}, {"wrong thread", "thread"}, {"expired lease", "expired"}} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			orgID, sessionID, threadID := seedRuntimeSeedThread(t, pool)
			runtime := createSeedRuntime(t, pool, orgID, sessionID, threadID)
			seedRuntimeInbox(t, pool, orgID, sessionID, threadID, 1, "pending", nil)
			callOrg, callThread, token := orgID, threadID, runtime.LeaseToken
			switch tt.scope {
			case "closed":
				changed, err := NewThreadRuntimeStore(pool).MarkTerminalWithLease(ctx, orgID, runtime.ID, token, models.ThreadRuntimeStatusClosed, "test", "")
				require.NoError(t, err, "close old runtime before replacement")
				require.True(t, changed, "close current owner once")
				createSeedRuntime(t, pool, orgID, sessionID, threadID)
			case "token":
				token = uuid.New()
			case "org":
				callOrg = uuid.New()
			case "thread":
				callThread = uuid.New()
			case "expired":
				_, err := pool.Exec(ctx, `UPDATE thread_runtimes SET lease_expires_at=now()-interval '1 minute' WHERE org_id=$1 AND id=$2`, orgID, runtime.ID)
				require.NoError(t, err, "expire stale caller")
			}
			claimed, err := NewThreadInboxStore(pool).MarkDeliveringForMessages(ctx, callOrg, callThread, runtime.ID, token, "late-worker", []int64{1})
			require.NoError(t, err, "fenced seed claim should be a no-op")
			require.Equal(t, int64(0), claimed, "stale or foreign owner cannot claim pending replacement seed")
			acked, err := NewThreadInboxStore(pool).MarkAckedForSeedMessages(ctx, callOrg, callThread, runtime.ID, token, []int64{1})
			require.NoError(t, err, "fenced seed ack should be a no-op")
			require.Equal(t, int64(0), acked, "stale or foreign attacher cannot steal or apply pending seed")
			var state string
			var unowned, unapplied bool
			require.NoError(t, pool.QueryRow(ctx, `SELECT delivery_state,runtime_id IS NULL,applied_at IS NULL FROM thread_inbox_entries WHERE org_id=$1 AND thread_id=$2`, orgID, threadID).Scan(&state, &unowned, &unapplied), "reload untouched replacement seed")
			require.Equal(t, "pending", state, "replacement seed must remain pending")
			require.True(t, unowned, "replacement seed must remain claimable")
			require.True(t, unapplied, "stale callers must not hide replacement input")
		})
	}
}

func TestSessionActivityPhaseStorePostgresSeedStartRollbackAndRetry(t *testing.T) {
	t.Parallel()
	pool := newFeedbackLifecyclePool(t)
	ctx := context.Background()
	orgID, sessionID, threadID := seedRuntimeSeedThread(t, pool)
	seedRuntimeInbox(t, pool, orgID, sessionID, threadID, 1, "pending", nil)
	runtime := createSeedRuntime(t, pool, orgID, sessionID, threadID)
	store := NewSessionActivityPhaseStore(pool)
	blocking, err := store.StartPhase(ctx, orgID, sessionID, threadID, 1, &runtime.ID, &runtime.LeaseToken, models.ActivityPhaseTrigger{Kind: models.ActivityPhaseTriggerInitial}, time.Now().UTC())
	require.NoError(t, err, "seed previous running phase to reject a second provider")
	_, _, err = store.StartSeedPhase(ctx, orgID, sessionID, threadID, 2, runtime.ID, runtime.LeaseToken, []int64{1}, time.Now().UTC())
	require.Error(t, err, "conflicting running phase must reject provider admission")
	var state string
	var owner *uuid.UUID
	var applied bool
	var batchCount int
	var watermark int64
	require.NoError(t, pool.QueryRow(ctx, `SELECT delivery_state,runtime_id,applied_at IS NOT NULL FROM thread_inbox_entries WHERE org_id=$1 AND thread_id=$2`, orgID, threadID).Scan(&state, &owner, &applied), "reload seed after rejected phase insertion")
	require.Equal(t, "pending", state, "phase conflict must roll back seed acknowledgment")
	require.Nil(t, owner, "phase conflict must roll back temporary ownership")
	require.False(t, applied, "pre-provider failure must not apply the seed")
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM thread_inbox_delivery_batches WHERE org_id=$1 AND thread_id=$2`, orgID, threadID).Scan(&batchCount), "count batches after rejected phase insertion")
	require.Equal(t, 0, batchCount, "phase conflict must roll back batch allocation")
	require.NoError(t, pool.QueryRow(ctx, `SELECT last_acked_sequence FROM thread_runtimes WHERE org_id=$1 AND id=$2`, orgID, runtime.ID).Scan(&watermark), "read original acknowledgment watermark")
	require.Equal(t, int64(0), watermark, "phase conflict must not advance seed acknowledgment watermark")
	_, err = store.CompletePhase(ctx, orgID, blocking.ID, models.ActivityPhaseStatusCompleted, models.ActivityPhaseBoundaryFinalResponse, time.Now().UTC())
	require.NoError(t, err, "complete blocking prior phase")
	closed, err := NewThreadRuntimeStore(pool).MarkTerminalWithLease(ctx, orgID, runtime.ID, runtime.LeaseToken, models.ThreadRuntimeStatusFailed, "preflight_failed", "")
	require.NoError(t, err, "close failed pre-provider attempt")
	require.True(t, closed, "failed attempt owner must release its runtime")
	replacement := createSeedRuntime(t, pool, orgID, sessionID, threadID)
	phase, batch, err := store.StartSeedPhase(ctx, orgID, sessionID, threadID, 2, replacement.ID, replacement.LeaseToken, []int64{1}, time.Now().UTC())
	require.NoError(t, err, "requeued pre-provider attempt must apply seed under new owner")
	require.NotNil(t, batch, "requeued attempt must preserve seed-batch attribution")
	require.Equal(t, replacement.ID, *phase.RuntimeID, "only replacement runtime should own newly admitted provider")
}

func TestSessionActivityPhaseStorePostgresConcurrentSeedStartAllowsOneProvider(t *testing.T) {
	t.Parallel()
	pool := newFeedbackLifecyclePool(t)
	ctx := context.Background()
	orgID, sessionID, threadID := seedRuntimeSeedThread(t, pool)
	seedRuntimeInbox(t, pool, orgID, sessionID, threadID, 1, "pending", nil)
	runtime := createSeedRuntime(t, pool, orgID, sessionID, threadID)
	start := make(chan struct{})
	results := make(chan error, 2)
	for range 2 {
		go func() {
			<-start
			_, _, err := NewSessionActivityPhaseStore(pool).StartSeedPhase(ctx, orgID, sessionID, threadID, 2, runtime.ID, runtime.LeaseToken, []int64{1}, time.Now().UTC())
			results <- err
		}()
	}
	close(start)
	successes := 0
	for range 2 {
		if err := <-results; err == nil {
			successes++
		}
	}
	require.Equal(t, 1, successes, "simultaneous seed retries must admit exactly one provider invocation")
	var phaseCount, batchCount int
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM session_activity_phases WHERE org_id=$1 AND thread_id=$2`, orgID, threadID).Scan(&phaseCount), "count exact provider starts after race")
	require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM thread_inbox_delivery_batches WHERE org_id=$1 AND thread_id=$2`, orgID, threadID).Scan(&batchCount), "count exact seed batches after race")
	require.Equal(t, []int{1, 1}, []int{phaseCount, batchCount}, "concurrent retries must commit one phase and one batch")
}

func TestThreadInboxStorePostgresSeedMutationRechecksLeaseAfterClosureLock(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name   string
		ack    bool
		expire bool
	}{{name: "claim"}, {name: "acknowledge", ack: true}, {name: "claim after expiry while blocked", expire: true}, {name: "acknowledge after expiry while blocked", ack: true, expire: true}} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// Each race has its own pool: a writer, blocked mutation, and observer
			// must all make progress even when the host's default pool limit is four.
			pool := newFeedbackLifecyclePool(t)
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			orgID, sessionID, threadID := seedRuntimeSeedThread(t, pool)
			runtime := createSeedRuntime(t, pool, orgID, sessionID, threadID)
			seedRuntimeInbox(t, pool, orgID, sessionID, threadID, 1, "pending", nil)
			writer, err := pool.Begin(ctx)
			require.NoError(t, err, "begin concurrent runtime closure")
			defer func() { _ = writer.Rollback(context.Background()) }()
			if tt.expire {
				_, err = writer.Exec(ctx, `UPDATE thread_runtimes SET heartbeat_at=now() WHERE org_id=$1 AND id=$2`, orgID, runtime.ID)
				require.NoError(t, err, "hold live runtime row before concurrent expiry")
			} else {
				changed, err := NewThreadRuntimeStore(writer).MarkTerminalWithLease(ctx, orgID, runtime.ID, runtime.LeaseToken, models.ThreadRuntimeStatusClosed, "test", "")
				require.NoError(t, err, "close runtime while holding its lease row lock")
				require.True(t, changed, "current writer must own closing lease")
			}
			connection, err := pool.Acquire(ctx)
			require.NoError(t, err, "reserve connection for delayed old-owner mutation")
			defer connection.Release()
			pid := connection.Conn().PgConn().PID()
			type mutationResult struct {
				count int64
				err   error
			}
			results := make(chan mutationResult, 1)
			go func() {
				store := NewThreadInboxStore(connection)
				var count int64
				var err error
				if tt.ack {
					count, err = store.MarkAckedForSeedMessages(ctx, orgID, threadID, runtime.ID, runtime.LeaseToken, []int64{1})
				} else {
					count, err = store.MarkDeliveringForMessages(ctx, orgID, threadID, runtime.ID, runtime.LeaseToken, "old-worker", []int64{1})
				}
				results <- mutationResult{count: count, err: err}
			}()
			require.Eventually(t, func() bool {
				var blocked bool
				queryErr := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND pid=$1 AND wait_event_type='Lock')`, pid).Scan(&blocked)
				return queryErr == nil && blocked
			}, 5*time.Second, 10*time.Millisecond, "old-owner seed mutation must wait on the runtime closure lock")
			if tt.expire {
				_, err = writer.Exec(ctx, `UPDATE thread_runtimes SET lease_expires_at=clock_timestamp()+interval '100 milliseconds' WHERE org_id=$1 AND id=$2`, orgID, runtime.ID)
				require.NoError(t, err, "lease expiry must occur after blocked mutation took its transaction timestamp")
				time.Sleep(200 * time.Millisecond)
			}
			require.NoError(t, writer.Commit(ctx), "commit closure after stale mutation has taken its snapshot")
			result := <-results
			require.NoError(t, result.err, "stale mutation must finish as a fenced no-op")
			require.Equal(t, int64(0), result.count, "lock wait must recheck terminal runtime ownership before changing input")
			var state string
			var unapplied bool
			require.NoError(t, pool.QueryRow(ctx, `SELECT delivery_state,applied_at IS NULL FROM thread_inbox_entries WHERE org_id=$1 AND thread_id=$2`, orgID, threadID).Scan(&state, &unapplied), "read input after concurrent old-owner mutation")
			require.Equal(t, "pending", state, "a terminal old owner cannot consume replacement input")
			require.True(t, unapplied, "stale mutation cannot hide replayable input")
		})
	}
}

func TestSessionActivityPhaseStorePostgresHistoricalSeedRequiresWholeRange(t *testing.T) {
	t.Parallel()
	pool := newFeedbackLifecyclePool(t)
	for _, tt := range []struct {
		name        string
		messageIDs  []int64
		wantSuccess bool
	}{
		{name: "whole pre-provider batch", messageIDs: []int64{1, 2}, wantSuccess: true},
		{name: "partial pre-provider batch", messageIDs: []int64{1}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			orgID, sessionID, threadID := seedRuntimeSeedThread(t, pool)
			old := createSeedRuntime(t, pool, orgID, sessionID, threadID)
			_, err := pool.Exec(ctx, `UPDATE thread_runtimes SET status='failed',closed_at=now(),last_acked_sequence=2 WHERE org_id=$1 AND id=$2`, orgID, old.ID)
			require.NoError(t, err, "seed previous pre-provider failure with acknowledged range")
			seedRuntimeInbox(t, pool, orgID, sessionID, threadID, 1, "acked", &old.ID)
			seedRuntimeInbox(t, pool, orgID, sessionID, threadID, 2, "acked", &old.ID)
			var batchID uuid.UUID
			require.NoError(t, pool.QueryRow(ctx, `INSERT INTO thread_inbox_delivery_batches(org_id,session_id,thread_id,runtime_id,sequence_start,sequence_end,status,acknowledged_at) VALUES($1,$2,$3,$4,1,2,'acknowledged',now()) RETURNING id`, orgID, sessionID, threadID, old.ID).Scan(&batchID), "seed exact unique historical range")
			current := createSeedRuntime(t, pool, orgID, sessionID, threadID)
			phase, batch, err := NewSessionActivityPhaseStore(pool).StartSeedPhase(ctx, orgID, sessionID, threadID, 2, current.ID, current.LeaseToken, tt.messageIDs, time.Now().UTC())
			if tt.wantSuccess {
				require.NoError(t, err, "complete proved-unexecuted batch should be recoverable")
				require.NotNil(t, batch, "recovered range must keep a durable batch")
				require.Equal(t, batchID, batch.ID, "recovery must preserve original range identity")
				require.Equal(t, []int64{1, 2}, []int64{*phase.TriggerSequenceStart, *phase.TriggerSequenceEnd}, "recovered phase must apply the complete exact instruction range")
			} else {
				require.Error(t, err, "partial historical range cannot create overlapping seed batches")
				existing, err := NewSessionActivityPhaseStore(pool).GetInboxDeliveryBatch(ctx, orgID, batchID)
				require.NoError(t, err, "reload untouched historical range")
				require.Equal(t, models.InboxDeliveryBatchAcknowledged, existing.Status, "rejected subset must not start the historical batch")
				require.Equal(t, old.ID, existing.RuntimeID, "rejected subset must not steal batch ownership")
				var count int
				require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM thread_inbox_entries WHERE org_id=$1 AND thread_id=$2 AND runtime_id=$3 AND delivery_state='acked' AND applied_at IS NULL`, orgID, threadID, old.ID).Scan(&count), "count retained unapplied historical inputs")
				require.Equal(t, 2, count, "rejected subset must preserve every instruction for complete recovery")
			}
		})
	}
}

func TestSessionActivityPhaseStorePostgresBackfillsLegacySeedIdentity(t *testing.T) {
	t.Parallel()
	pool := newFeedbackLifecyclePool(t)
	for _, tt := range []struct {
		name                                                 string
		threadless, siblingTarget, siblingMessage, assistant bool
		wantSuccess                                          bool
	}{
		{name: "scoped user message", wantSuccess: true},
		{name: "threadless primary message", threadless: true, wantSuccess: true},
		{name: "threadless message cannot cross to sibling", threadless: true, siblingTarget: true},
		{name: "sibling message cannot cross to primary", siblingMessage: true},
		{name: "assistant cannot become seed input", assistant: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			orgID, sessionID, primaryID := seedRuntimeSeedThread(t, pool)
			targetID := primaryID
			messageThread := &primaryID
			if tt.siblingTarget || tt.siblingMessage {
				sibling := &models.SessionThread{OrgID: orgID, SessionID: sessionID, AgentType: models.DefaultDefaultAgentType, Status: models.ThreadStatusIdle}
				require.NoError(t, NewSessionThreadStore(pool).Create(ctx, sibling, 10), "create independent sibling thread")
				if tt.siblingTarget {
					targetID = sibling.ID
				} else {
					messageThread = &sibling.ID
				}
			}
			if tt.threadless {
				messageThread = nil
			}
			role := models.MessageRoleUser
			if tt.assistant {
				role = models.MessageRoleAssistant
			}
			message := &models.SessionMessage{OrgID: orgID, SessionID: sessionID, ThreadID: messageThread, TurnNumber: 2, Role: role, Content: "Preserved legacy instruction"}
			require.NoError(t, NewSessionMessageStore(pool).Create(ctx, message), "create message predating durable inbox")
			runtime := createSeedRuntime(t, pool, orgID, sessionID, targetID)
			store := NewSessionActivityPhaseStore(pool)
			phase, batch, err := store.StartSeedPhase(ctx, orgID, sessionID, targetID, 2, runtime.ID, runtime.LeaseToken, []int64{message.ID}, time.Now().UTC())
			if tt.wantSuccess {
				require.NoError(t, err, "valid legacy input should acquire durable identity before provider admission")
				require.NotNil(t, batch, "legacy input must never bypass durable batch admission")
				require.Equal(t, models.ActivityPhaseTriggerInboxBatch, phase.TriggerKind, "legacy seed must receive normal inbox attribution")
				_, err = store.CompletePhase(ctx, orgID, phase.ID, models.ActivityPhaseStatusCompleted, models.ActivityPhaseBoundaryFinalResponse, time.Now().UTC())
				require.NoError(t, err, "complete first admitted legacy provider phase")
				_, _, err = store.StartSeedPhase(ctx, orgID, sessionID, targetID, 2, runtime.ID, runtime.LeaseToken, []int64{message.ID}, time.Now().UTC())
				require.Error(t, err, "completed legacy seed cannot create another provider admission on retry")
			} else {
				require.Error(t, err, "foreign scope or assistant messages cannot become provider seed input")
				var count int
				require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM thread_inbox_entries WHERE org_id=$1 AND message_id=$2`, orgID, message.ID).Scan(&count), "count inbox rows after scoped backfill rejection")
				require.Equal(t, 0, count, "rejected message must not be copied across thread or role boundaries")
			}
		})
	}
}

func TestThreadInboxStorePostgresLiveAttemptClosureRequiresExplicitUncertaintyRecovery(t *testing.T) {
	t.Parallel()
	pool := newFeedbackLifecyclePool(t)
	for _, tt := range []struct {
		name                              string
		definitelyUnsent, wrongResetToken bool
		wantState                         string
	}{
		{name: "accepted write or ambiguous error", wantState: "unknown_delivery"},
		{name: "typed input unavailable", definitelyUnsent: true, wantState: "pending"},
		{name: "stale reset cannot erase attempt", definitelyUnsent: true, wrongResetToken: true, wantState: "unknown_delivery"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			orgID, sessionID, threadID := seedRuntimeSeedThread(t, pool)
			runtime := createSeedRuntime(t, pool, orgID, sessionID, threadID)
			seedRuntimeInbox(t, pool, orgID, sessionID, threadID, 1, "delivering", &runtime.ID)
			var entryID uuid.UUID
			require.NoError(t, pool.QueryRow(ctx, `SELECT id FROM thread_inbox_entries WHERE org_id=$1 AND thread_id=$2`, orgID, threadID).Scan(&entryID), "load exact provider-bound entry")
			store := NewThreadInboxStore(pool)
			attempted, err := store.MarkDeliveredForEntry(ctx, orgID, threadID, runtime.ID, runtime.LeaseToken, "test-worker", entryID, 1)
			require.NoError(t, err, "persist possible provider acceptance before the external write")
			require.Equal(t, int64(1), attempted, "one owned entry must receive its durable attempt marker")
			if tt.definitelyUnsent {
				token := runtime.LeaseToken
				expected := int64(1)
				if tt.wrongResetToken {
					token = uuid.New()
					expected = 0
				}
				reset, err := store.ResetUnsentDelivery(ctx, orgID, threadID, runtime.ID, token, entryID)
				require.NoError(t, err, "reset only exact live owner after definite nonacceptance")
				require.Equal(t, expected, reset, "wrong lease cannot erase a possible-acceptance marker")
			}
			closed, err := NewThreadRuntimeStore(pool).MarkTerminalWithLease(ctx, orgID, runtime.ID, runtime.LeaseToken, models.ThreadRuntimeStatusClosed, "completed", "")
			require.NoError(t, err, "close provider runtime after bounded write finishes")
			require.True(t, closed, "current owner must close once")
			var state string
			require.NoError(t, pool.QueryRow(ctx, `SELECT delivery_state FROM thread_inbox_entries WHERE org_id=$1 AND id=$2`, orgID, entryID).Scan(&state), "read post-closure uncertainty outcome")
			require.Equal(t, tt.wantState, state, "only definite nonacceptance may return to automatic replay")
			entries, err := store.ClaimDeliverableAfter(ctx, orgID, threadID, runtime.ID, runtime.LeaseToken, "test-worker", 0, 50)
			require.NoError(t, err, "stale owner claim should be fenced")
			require.Empty(t, entries, "closed runtime cannot reclaim released or uncertain entries")
			replacement := createSeedRuntime(t, pool, orgID, sessionID, threadID)
			_, _, err = NewSessionActivityPhaseStore(pool).StartSeedPhase(ctx, orgID, sessionID, threadID, 2, replacement.ID, replacement.LeaseToken, []int64{1}, time.Now().UTC())
			if tt.wantState == "pending" {
				require.NoError(t, err, "definitely unsent input should continue under replacement owner")
			} else {
				require.Error(t, err, "uncertain provider acceptance must never auto-replay")
			}
		})
	}
}

func TestThreadInboxStorePostgresUnresolvedAttemptBlocksLaterInput(t *testing.T) {
	t.Parallel()
	pool := newFeedbackLifecyclePool(t)
	for _, tt := range []struct {
		name                   string
		secondDefinitelyUnsent bool
	}{{name: "ambiguous first write"}, {name: "accepted first and unavailable second", secondDefinitelyUnsent: true}} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			orgID, sessionID, threadID := seedRuntimeSeedThread(t, pool)
			runtime := createSeedRuntime(t, pool, orgID, sessionID, threadID)
			seedRuntimeInbox(t, pool, orgID, sessionID, threadID, 1, "pending", nil)
			seedRuntimeInbox(t, pool, orgID, sessionID, threadID, 2, "pending", nil)
			store := NewThreadInboxStore(pool)
			entries, err := store.ClaimDeliverableAfter(ctx, orgID, threadID, runtime.ID, runtime.LeaseToken, "test-worker", 0, 50)
			require.NoError(t, err, "claim the first contiguous live-delivery segment")
			require.Equal(t, 2, len(entries), "initial claim should contain both exact ordered instructions")
			count, err := store.MarkDeliveredForEntry(ctx, orgID, threadID, runtime.ID, runtime.LeaseToken, "test-worker", entries[0].ID, 1)
			require.NoError(t, err, "persist first possible provider acceptance")
			require.Equal(t, int64(1), count, "first entry must own the write attempt")
			if tt.secondDefinitelyUnsent {
				count, err = store.MarkDeliveredForEntry(ctx, orgID, threadID, runtime.ID, runtime.LeaseToken, "test-worker", entries[1].ID, 2)
				require.NoError(t, err, "persist next write attempt in the same segment")
				require.Equal(t, int64(1), count, "second entry must own its exact attempt")
				count, err = store.ResetUnsentDelivery(ctx, orgID, threadID, runtime.ID, runtime.LeaseToken, entries[1].ID)
				require.NoError(t, err, "typed second-input rejection may reopen only that entry")
				require.Equal(t, int64(1), count, "typed rejection should reset exactly its current attempt")
			}
			var delivered, acked int64
			require.NoError(t, pool.QueryRow(ctx, `SELECT last_delivered_sequence,last_acked_sequence FROM thread_runtimes WHERE org_id=$1 AND id=$2`, orgID, runtime.ID).Scan(&delivered, &acked), "load durable unresolved delivery barrier")
			require.Equal(t, []int64{1, 0}, []int64{delivered, acked}, "resetting second unsent input must preserve the first unresolved acceptance")
			blocked, err := store.ClaimDeliverableAfter(ctx, orgID, threadID, runtime.ID, runtime.LeaseToken, "test-worker", 0, 50)
			require.NoError(t, err, "poll across unresolved acceptance should be a safe no-op")
			require.Empty(t, blocked, "later poll must not send another input that could acknowledge across an uncertain attempt")
			if tt.secondDefinitelyUnsent {
				committed, err := NewThreadRuntimeStore(pool).CommitInboxDeliveryWithLease(ctx, orgID, runtime.ID, runtime.LeaseToken, threadID, "test-worker", 1, 1)
				require.NoError(t, err, "known successful first input should flush independently")
				require.True(t, committed, "flush must acknowledge only the accepted first segment")
				next, err := store.ClaimDeliverableAfter(ctx, orgID, threadID, runtime.ID, runtime.LeaseToken, "test-worker", 1, 50)
				require.NoError(t, err, "delivery may resume after the earlier accepted segment is durably acknowledged")
				var sequences []int64
				for _, entry := range next {
					sequences = append(sequences, entry.SequenceNo)
				}
				require.Equal(t, []int64{2}, sequences, "only definitely unsent second input may retry after the flush")
			} else {
				var applied int
				require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM thread_inbox_entries WHERE org_id=$1 AND thread_id=$2 AND applied_at IS NOT NULL`, orgID, threadID).Scan(&applied), "read uncertainty before runtime closure")
				require.Equal(t, 0, applied, "uncertain first input must remain unapplied until explicit reconciliation")
			}
		})
	}
}

func TestThreadInboxStorePostgresInitialAdmissionSurvivesLateAckFailureAndClosure(t *testing.T) {
	t.Parallel()
	pool := newFeedbackLifecyclePool(t)
	for _, tt := range []struct {
		name       string
		phase      bool
		incomplete bool
	}{{name: "atomic activity phase", phase: true}, {name: "optional phase-free admission"}, {name: "incomplete seed set cannot partially apply", incomplete: true}} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			orgID, sessionID, threadID := seedRuntimeSeedThread(t, pool)
			runtime := createSeedRuntime(t, pool, orgID, sessionID, threadID)
			seedRuntimeInbox(t, pool, orgID, sessionID, threadID, 1, "delivering", &runtime.ID)
			inbox := NewThreadInboxStore(pool)
			if tt.incomplete {
				count, err := inbox.MarkAckedForSeedMessages(ctx, orgID, threadID, runtime.ID, runtime.LeaseToken, []int64{1, 2})
				require.NoError(t, err, "incomplete seed admission should be an atomic no-op")
				require.Equal(t, int64(0), count, "missing seed ID must reject every admission update")
				var state string
				var unapplied bool
				require.NoError(t, pool.QueryRow(ctx, `SELECT delivery_state,applied_at IS NULL FROM thread_inbox_entries WHERE org_id=$1 AND thread_id=$2`, orgID, threadID).Scan(&state, &unapplied), "reload existing seed after incomplete pre-admission")
				require.Equal(t, "delivering", state, "count mismatch must not apply an existing subset")
				require.True(t, unapplied, "existing seed must remain available for a complete retry")
				return
			}
			if tt.phase {
				store := NewSessionActivityPhaseStore(pool)
				phase, _, err := store.StartSeedPhase(ctx, orgID, sessionID, threadID, 1, runtime.ID, runtime.LeaseToken, []int64{1}, time.Now().UTC())
				require.NoError(t, err, "initial seed must commit provider admission before Execute")
				_, err = store.CompletePhase(ctx, orgID, phase.ID, models.ActivityPhaseStatusFailed, models.ActivityPhaseBoundaryError, time.Now().UTC())
				require.NoError(t, err, "terminalize initial provider phase before replacement")
			} else {
				count, err := inbox.MarkAckedForSeedMessages(ctx, orgID, threadID, runtime.ID, runtime.LeaseToken, []int64{1})
				require.NoError(t, err, "phase-free initial wiring must durably admit its seed before Execute")
				require.Equal(t, int64(1), count, "phase-free admission must apply its exact seed")
			}
			lateCtx, cancel := context.WithCancel(ctx)
			cancel()
			_, err := inbox.MarkAckedForSeedMessages(lateCtx, orgID, threadID, runtime.ID, runtime.LeaseToken, []int64{1})
			require.Error(t, err, "simulate late attach acknowledgment failing after provider acceptance")
			closed, err := NewThreadRuntimeStore(pool).MarkTerminalWithLease(ctx, orgID, runtime.ID, runtime.LeaseToken, models.ThreadRuntimeStatusFailed, "provider_failed", "")
			require.NoError(t, err, "close original runtime after late acknowledgment failure")
			require.True(t, closed, "original runtime must close once")
			var state string
			var applied bool
			require.NoError(t, pool.QueryRow(ctx, `SELECT delivery_state,applied_at IS NOT NULL FROM thread_inbox_entries WHERE org_id=$1 AND thread_id=$2`, orgID, threadID).Scan(&state, &applied), "reload initial seed after closure")
			require.Equal(t, "acked", state, "initial admission must never return accepted seed to pending")
			require.True(t, applied, "initial seed must retain its durable provider-admission proof")
			replacement := createSeedRuntime(t, pool, orgID, sessionID, threadID)
			_, _, err = NewSessionActivityPhaseStore(pool).StartSeedPhase(ctx, orgID, sessionID, threadID, 2, replacement.ID, replacement.LeaseToken, []int64{1}, time.Now().UTC())
			require.Error(t, err, "replacement must refuse already-admitted initial input despite late ack failure")
		})
	}
}
