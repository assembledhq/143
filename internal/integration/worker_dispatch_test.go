//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/worker"
)

// TestIntegration_SharedSandboxAdmissionDeduplicatesCurrentRoutingReservation
// exercises the real advisory-lock and lease-union query. The final gate must
// replace, rather than add to, its own durable routing reservation while still
// rejecting an independent consumer once that worker slot is occupied.
//
// This test cannot run in parallel because the integration suite shares one
// database and setup truncates queue state between tests.
func TestIntegration_SharedSandboxAdmissionDeduplicatesCurrentRoutingReservation(t *testing.T) {
	pool := setup(t)
	ctx := context.Background()
	orgID := seedOrg(t, pool)
	nodeID := "shared-admission-worker-" + orgID.String()[:8]
	seedWorkerNode(t, pool, nodeID)
	setWorkerSandboxCapacity(t, pool, nodeID, 1)

	session := seedSession(t, pool, orgID, sessionOpts{Status: models.SessionStatusPending})
	jobStore := db.NewJobStore(pool)
	jobID, err := jobStore.EnqueueWithOpts(ctx, orgID, db.EnqueueOpts{
		Queue:         "agent",
		JobType:       "run_agent",
		Payload:       map[string]string{"session_id": session.ID.String(), "org_id": orgID.String()},
		Priority:      5,
		WorkloadClass: models.SandboxWorkloadClassInteractive,
	})
	require.NoError(t, err, "sandbox-producing job should enqueue")
	routed, err := jobStore.RouteNextSandboxJob(ctx)
	require.NoError(t, err, "sandbox-producing job should receive a durable worker reservation")
	require.NotNil(t, routed, "routing should return the reserved job")
	require.NotNil(t, routed.TargetNodeID, "routing should select the only worker")
	require.Equal(t, nodeID, *routed.TargetNodeID, "routing should target the configured worker")
	lockToken := uuid.New()
	claimed, err := jobStore.ClaimNextRunnable(ctx, nodeID, nodeID, lockToken, time.Minute)
	require.NoError(t, err, "routed sandbox job should be claimed by its target worker")
	require.NotNil(t, claimed, "routed sandbox job should be available to final admission")
	require.Equal(t, jobID, claimed.ID, "final admission should use the claimed routing job")

	capacityStore := db.NewSandboxCapacityReservationStore(pool)
	reservationID, live, total, acquired, err := capacityStore.ReserveSandboxCapacity(
		ctx,
		nodeID,
		&jobID,
		&lockToken,
		models.SandboxWorkloadClassInteractive,
		func(context.Context) (int, error) { return 0, nil },
		1,
		time.Now().Add(time.Minute),
	)
	require.NoError(t, err, "final admission should replace the current job's durable routing reservation")
	require.True(t, acquired, "current job should acquire the one worker slot")
	require.Equal(t, 0, live, "final admission should use the live Docker count taken under the node lock")
	require.Equal(t, 1, total, "durable and final reservations for the same job should count once")
	require.NotEqual(t, uuid.Nil, reservationID, "successful final admission should persist a lease")

	otherReservationID, _, blockedTotal, otherAcquired, err := capacityStore.ReserveSandboxCapacity(
		ctx,
		nodeID,
		nil,
		nil,
		models.SandboxWorkloadClassInteractive,
		func(context.Context) (int, error) { return 0, nil },
		1,
		time.Now().Add(time.Minute),
	)
	require.NoError(t, err, "independent admission should return a capacity decision")
	require.False(t, otherAcquired, "independent admission should not exceed the worker limit")
	require.Equal(t, uuid.Nil, otherReservationID, "rejected admission should not persist another lease")
	require.Equal(t, 1, blockedTotal, "shared and durable reservations for the admitted job should remain deduplicated")
	lockTx, err := pool.Begin(ctx)
	require.NoError(t, err, "test should open a competing node-admission transaction")
	defer func() { _ = lockTx.Rollback(ctx) }()
	_, err = lockTx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 143))`, nodeID)
	require.NoError(t, err, "test should hold the worker admission lock")

	releaseDone := make(chan error, 1)
	go func() {
		releaseDone <- capacityStore.ReleaseSandboxCapacity(ctx, nodeID, reservationID, &lockToken)
	}()
	select {
	case releaseErr := <-releaseDone:
		require.Failf(t, "shared capacity release should wait for admission lock", "release returned early: %v", releaseErr)
	case <-time.After(100 * time.Millisecond):
	}
	require.NoError(t, lockTx.Commit(ctx), "test should release the competing worker admission lock")
	require.NoError(t, <-releaseDone, "shared capacity release should complete after acquiring the admission lock")

	var remainingReservations int
	err = pool.QueryRow(ctx, `SELECT COUNT(*) FROM sandbox_capacity_reservations WHERE id = $1`, reservationID).Scan(&remainingReservations)
	require.NoError(t, err, "test should inspect the released shared reservation")
	require.Equal(t, 0, remainingReservations, "shared capacity release should delete its lease after the admission lock is available")
}

// TestIntegration_SharedSandboxAdmissionFencesReclaimedJobAttempt verifies
// that a replacement queue owner cannot share or overwrite the stale owner's
// final-admission lease. The replacement waits for the prior attempt to
// release (or expire), and either attempt can release only its own lease.
//
// This test cannot run in parallel because the integration suite shares one
// database and setup truncates queue state between tests.
func TestIntegration_SharedSandboxAdmissionFencesReclaimedJobAttempt(t *testing.T) {
	pool := setup(t)
	ctx := context.Background()
	orgID := seedOrg(t, pool)
	nodeID := "attempt-fence-worker-" + orgID.String()[:8]
	seedWorkerNode(t, pool, nodeID)
	setWorkerSandboxCapacity(t, pool, nodeID, 3)

	session := seedSession(t, pool, orgID, sessionOpts{Status: models.SessionStatusPending})
	jobStore := db.NewJobStore(pool)
	jobID, err := jobStore.EnqueueWithOpts(ctx, orgID, db.EnqueueOpts{
		Queue:         "agent",
		JobType:       "run_agent",
		Payload:       map[string]string{"session_id": session.ID.String(), "org_id": orgID.String()},
		Priority:      5,
		WorkloadClass: models.SandboxWorkloadClassInteractive,
	})
	require.NoError(t, err, "sandbox-producing job should enqueue")
	routed, err := jobStore.RouteNextSandboxJob(ctx)
	require.NoError(t, err, "sandbox-producing job should receive a durable worker reservation")
	require.NotNil(t, routed, "routing should return the sandbox-producing job")

	firstLockToken := uuid.New()
	claimed, err := jobStore.ClaimNextRunnable(ctx, nodeID, nodeID, firstLockToken, time.Minute)
	require.NoError(t, err, "first queue attempt should claim the routed job")
	require.NotNil(t, claimed, "first queue attempt should own the job before final admission")

	capacityStore := db.NewSandboxCapacityReservationStore(pool)
	firstReservationID, _, _, acquired, err := capacityStore.ReserveSandboxCapacity(
		ctx, nodeID, &jobID, &firstLockToken, models.SandboxWorkloadClassInteractive,
		func(context.Context) (int, error) { return 0, nil }, 3, time.Now().Add(time.Minute),
	)
	require.NoError(t, err, "first queue attempt should reserve final-admission capacity")
	require.True(t, acquired, "first queue attempt should acquire final-admission capacity")
	_, err = pool.Exec(ctx, `
		INSERT INTO sandbox_capacity_reservations (node_id, job_id, workload_class, expires_at)
		VALUES ($1, $2, 'interactive', now() + interval '1 minute')
		ON CONFLICT (job_id) WHERE job_id IS NOT NULL
		DO UPDATE SET node_id = EXCLUDED.node_id, expires_at = EXCLUDED.expires_at`, nodeID, jobID)
	require.ErrorContains(t, err, "requires a matching job lock token", "rolling-deploy legacy admission must fail closed instead of sharing the current attempt's row")

	replacementNodeID := nodeID + "-replacement"
	seedWorkerNode(t, pool, replacementNodeID)
	setWorkerSandboxCapacity(t, pool, replacementNodeID, 3)
	secondLockToken := uuid.New()
	_, err = pool.Exec(ctx, `
		UPDATE jobs
		SET lock_token = $3,
			locked_by_node_id = $2,
			run_owner_id = $2,
			target_node_id = $2,
			locked_at = now(),
			lease_expires_at = now() + interval '1 minute'
		WHERE org_id = $1 AND id = $4`, orgID, replacementNodeID, secondLockToken, jobID)
	require.NoError(t, err, "test should simulate the queue reclaiming the job on another worker with a new fencing token")

	_, _, blockedTotal, replacementAcquired, err := capacityStore.ReserveSandboxCapacity(
		ctx, replacementNodeID, &jobID, &secondLockToken, models.SandboxWorkloadClassInteractive,
		func(context.Context) (int, error) { return 0, nil }, 3, time.Now().Add(time.Minute),
	)
	require.ErrorIs(t, err, db.ErrSandboxCapacityAttemptConflict, "replacement attempt should distinguish stale-attempt coordination from physical capacity saturation")
	require.False(t, replacementAcquired, "replacement attempt must wait for the stale attempt's live reservation on another worker")
	require.Equal(t, 0, blockedTotal, "another worker's lease is a fencing conflict rather than local capacity load")

	_, _, _, staleAcquired, err := capacityStore.ReserveSandboxCapacity(
		ctx, nodeID, &jobID, &firstLockToken, models.SandboxWorkloadClassInteractive,
		func(context.Context) (int, error) { return 0, nil }, 3, time.Now().Add(time.Minute),
	)
	require.ErrorContains(t, err, "job attempt no longer owns", "stale attempt should fail transactional ownership validation")
	require.False(t, staleAcquired, "stale attempt must not reacquire final-admission capacity")

	_, err = pool.Exec(ctx, `
		UPDATE sandbox_capacity_reservations
		SET expires_at = now() - interval '1 second'
		WHERE id = $1`, firstReservationID)
	require.NoError(t, err, "test should expire the stale attempt lease without releasing it")
	secondReservationID, _, _, replacementAcquired, err := capacityStore.ReserveSandboxCapacity(
		ctx, replacementNodeID, &jobID, &secondLockToken, models.SandboxWorkloadClassInteractive,
		func(context.Context) (int, error) { return 0, nil }, 3, time.Now().Add(time.Minute),
	)
	require.NoError(t, err, "replacement attempt should reserve after the prior lease expires")
	require.True(t, replacementAcquired, "replacement attempt should acquire its own final-admission lease")
	require.NotEqual(t, firstReservationID, secondReservationID, "replacement attempt should receive a distinct reservation identity")

	require.NoError(t, capacityStore.ReleaseSandboxCapacity(ctx, nodeID, firstReservationID, &firstLockToken), "late stale release should be harmless")
	var replacementRows int
	err = pool.QueryRow(ctx, `SELECT COUNT(*) FROM sandbox_capacity_reservations WHERE id = $1 AND job_lock_token = $2`, secondReservationID, secondLockToken).Scan(&replacementRows)
	require.NoError(t, err, "test should inspect the replacement attempt reservation")
	require.Equal(t, 1, replacementRows, "stale release must not delete the replacement attempt's reservation")
	require.NoError(t, capacityStore.ReleaseSandboxCapacity(ctx, replacementNodeID, secondReservationID, &secondLockToken), "replacement attempt should release its own reservation")
}

// TestIntegration_FailedFreshSandboxReroutesOffPhysicalHost verifies the
// failure cleanup used by compatibility and terminal-probe placements. Those
// jobs intentionally have a target without sandbox_slot_reserved_until, but a
// node-local create failure must reserve an alternate while excluding every
// blue/green generation that shares the failed Docker host.
//
// This test cannot run in parallel because the integration suite shares one
// database and setup truncates queue state between tests.
func TestIntegration_FailedFreshSandboxReroutesOffPhysicalHost(t *testing.T) {
	pool := setup(t)
	ctx := context.Background()
	orgID := seedOrg(t, pool)
	failedCapacityNodeID := "failed-create-worker-" + orgID.String()[:8]
	failedNodeID := failedCapacityNodeID + "-g20260813000101-blue"
	failedSiblingNodeID := failedCapacityNodeID + "-g20260813000202-green"
	healthyNodeID := "healthy-create-worker-" + orgID.String()[:8]
	for _, nodeID := range []string{failedNodeID, failedSiblingNodeID, healthyNodeID} {
		seedWorkerNode(t, pool, nodeID)
		setWorkerSandboxCapacity(t, pool, nodeID, 2)
	}
	_, err := pool.Exec(ctx, `
		UPDATE nodes
		SET metadata = metadata || jsonb_build_object('sandbox_capacity_node_id', $2::text)
		WHERE id = $1`, failedSiblingNodeID, failedCapacityNodeID)
	require.NoError(t, err, "sibling worker generation should advertise the failed physical-host identity")
	session := seedSession(t, pool, orgID, sessionOpts{Status: models.SessionStatusPending})

	jobStore := db.NewJobStore(pool)
	jobID, err := jobStore.EnqueueWithOpts(ctx, orgID, db.EnqueueOpts{
		Queue:         "agent",
		JobType:       "run_agent",
		Payload:       map[string]string{"session_id": session.ID.String(), "org_id": orgID.String()},
		Priority:      5,
		WorkloadClass: models.SandboxWorkloadClassInteractive,
	})
	require.NoError(t, err, "fresh sandbox job should enqueue")
	lockToken := uuid.New()
	_, err = pool.Exec(ctx, `
		UPDATE jobs
		SET status = 'running',
			target_node_id = $2,
			sandbox_slot_reserved_until = NULL,
			lock_token = $3,
			locked_by_node_id = $2,
			run_owner_id = $2,
			lease_expires_at = now() + interval '1 minute'
		WHERE org_id = $1 AND id = $4`, orgID, failedNodeID, lockToken, jobID)
	require.NoError(t, err, "test should create a running terminal-probe-style placement")

	staleCleared, err := jobStore.ClearSandboxRoutingPlacementWithLease(ctx, jobID, uuid.New())
	require.NoError(t, err, "stale-attempt cleanup should be a harmless no-op")
	require.False(t, staleCleared, "stale attempt must not clear the current worker placement")

	routed, err := jobStore.RerouteSandboxAfterStartupFailure(ctx, jobID, lockToken, failedNodeID)
	require.NoError(t, err, "fresh sandbox failure cleanup should reserve an alternate worker")
	require.NotNil(t, routed, "fresh sandbox failure cleanup should return a routing decision")
	require.NotNil(t, routed.TargetNodeID, "healthy alternate capacity should be reserved immediately")
	require.Equal(t, healthyNodeID, *routed.TargetNodeID, "retry must exclude every generation on the failed physical host")

	var reservedRows int
	err = pool.QueryRow(ctx, `
		SELECT COUNT(*)
		FROM jobs
		WHERE org_id = $1
		  AND id = $2
		  AND target_node_id = $3
		  AND sandbox_slot_reserved_until > now()`, orgID, jobID, healthyNodeID).Scan(&reservedRows)
	require.NoError(t, err, "test should inspect the replacement worker placement")
	require.Equal(t, 1, reservedRows, "failed fresh sandbox should atomically hold the healthy alternate slot")
	var failedCapacityNodeIDs []string
	err = pool.QueryRow(ctx, `
		SELECT payload->'failed_sandbox_capacity_node_ids'
		FROM jobs
		WHERE org_id = $1 AND id = $2`, orgID, jobID).Scan(&failedCapacityNodeIDs)
	require.NoError(t, err, "test should inspect durable failed-host exclusions")
	require.Equal(t, []string{failedCapacityNodeID}, failedCapacityNodeIDs, "retry should persist the failed physical host for later routing passes")
	var exclusionUntil time.Time
	err = pool.QueryRow(ctx, `
		SELECT (payload->>'failed_sandbox_capacity_node_exclusion_until')::timestamptz
		FROM jobs
		WHERE org_id = $1 AND id = $2`, orgID, jobID).Scan(&exclusionUntil)
	require.NoError(t, err, "test should inspect the failed-host exclusion deadline")
	require.True(t, exclusionUntil.After(time.Now()), "failed-host exclusion should survive immediate retry routing")

	// When the only healthy alternate is temporarily unavailable, the fenced
	// failure write must retain its physical-host exclusion for the later fleet
	// routing pass instead of falling back to either failed generation.
	_, err = pool.Exec(ctx, `UPDATE nodes SET status = 'draining' WHERE id = $1`, healthyNodeID)
	require.NoError(t, err, "test should temporarily remove the healthy alternate")
	queuedSession := seedSession(t, pool, orgID, sessionOpts{Status: models.SessionStatusPending})
	queuedJobID, err := jobStore.EnqueueWithOpts(ctx, orgID, db.EnqueueOpts{
		Queue:         "agent",
		JobType:       "run_agent",
		Payload:       map[string]string{"session_id": queuedSession.ID.String(), "org_id": orgID.String()},
		Priority:      5,
		WorkloadClass: models.SandboxWorkloadClassInteractive,
	})
	require.NoError(t, err, "capacity-wait sandbox job should enqueue")
	queuedLockToken := uuid.New()
	_, err = pool.Exec(ctx, `
		UPDATE jobs
		SET status = 'running', target_node_id = $2, sandbox_slot_reserved_until = NULL,
			lock_token = $3, locked_by_node_id = $2, run_owner_id = $2,
			lease_expires_at = now() + interval '1 minute'
		WHERE org_id = $1 AND id = $4`, orgID, failedSiblingNodeID, queuedLockToken, queuedJobID)
	require.NoError(t, err, "test should create a second failed startup attempt")

	waitingRoute, err := jobStore.RerouteSandboxAfterStartupFailure(ctx, queuedJobID, queuedLockToken, failedSiblingNodeID)
	require.NoError(t, err, "failed startup should persist its exclusion even with no immediate alternate")
	require.Nil(t, waitingRoute.TargetNodeID, "genuinely unavailable alternate capacity should clear worker affinity")
	_, err = pool.Exec(ctx, `
		UPDATE jobs
		SET status = 'pending', run_at = now(), lock_token = NULL,
			locked_by_node_id = NULL, run_owner_id = NULL, lease_expires_at = NULL
		WHERE org_id = $1 AND id = $2`, orgID, queuedJobID)
	require.NoError(t, err, "test should simulate the worker's fenced retry write")
	_, err = pool.Exec(ctx, `UPDATE nodes SET status = 'active', last_heartbeat_at = now() WHERE id = $1`, healthyNodeID)
	require.NoError(t, err, "test should restore the healthy alternate")

	laterRoute, err := jobStore.RouteNextSandboxJob(ctx)
	require.NoError(t, err, "later routing pass should honor the durable failed-host exclusion")
	require.NotNil(t, laterRoute, "later routing should inspect the waiting sandbox job")
	require.NotNil(t, laterRoute.TargetNodeID, "newly available healthy capacity should be reserved")
	require.Equal(t, healthyNodeID, *laterRoute.TargetNodeID, "later routing must not return to either generation on the failed physical host")
}

// TestIntegration_BlueGreenGenerationsSharePhysicalHostCapacity verifies that
// generation-specific routing IDs cannot make one Docker daemon look like two
// independent capacity pools during a rolling deploy.
//
// This test cannot run in parallel because the integration suite shares one
// database and setup truncates queue state between tests.
func TestIntegration_BlueGreenGenerationsSharePhysicalHostCapacity(t *testing.T) {
	pool := setup(t)
	ctx := context.Background()
	orgID := seedOrg(t, pool)
	capacityNodeID := "physical-worker-" + orgID.String()[:8]
	nodeIDs := []string{
		capacityNodeID + "-g20260813000101-blue",
		capacityNodeID + "-g20260813000202-green",
	}
	for i, nodeID := range nodeIDs {
		seedWorkerNode(t, pool, nodeID)
		setWorkerSandboxCapacity(t, pool, nodeID, 1)
		if i > 0 {
			_, err := pool.Exec(ctx, `
				UPDATE nodes
				SET metadata = metadata || jsonb_build_object('sandbox_capacity_node_id', $2::text)
				WHERE id = $1`, nodeID, capacityNodeID)
			require.NoError(t, err, "new worker generation should advertise the shared physical-host capacity identity")
		}
	}

	store := db.NewJobStore(pool)
	for range 2 {
		session := seedSession(t, pool, orgID, sessionOpts{Status: models.SessionStatusPending})
		_, err := store.EnqueueWithOpts(ctx, orgID, db.EnqueueOpts{
			Queue:         "agent",
			JobType:       "run_agent",
			Payload:       map[string]string{"session_id": session.ID.String(), "org_id": orgID.String()},
			Priority:      5,
			WorkloadClass: models.SandboxWorkloadClassInteractive,
		})
		require.NoError(t, err, "test sandbox job should enqueue")
	}

	first, err := store.RouteNextSandboxJob(ctx)
	require.NoError(t, err, "first generation should reserve the physical host slot")
	require.NotNil(t, first, "first route should return a capacity decision")
	require.NotNil(t, first.TargetNodeID, "first route should select one worker generation")
	_, _, finalLoad, admitted, err := db.NewSandboxCapacityReservationStore(pool).ReserveSandboxCapacity(
		ctx,
		capacityNodeID,
		nil,
		nil,
		models.SandboxWorkloadClassInteractive,
		func(context.Context) (int, error) { return 0, nil },
		1,
		time.Now().Add(time.Minute),
	)
	require.NoError(t, err, "final admission should resolve generation job ownership to the physical host")
	require.False(t, admitted, "final admission should not bypass the routed generation's durable reservation")
	require.Equal(t, 1, finalLoad, "final admission should count the durable reservation across worker generations")

	second, err := store.RouteNextSandboxJob(ctx)
	require.NoError(t, err, "second generation should share the physical host admission fence")
	require.NotNil(t, second, "second route should return a capacity decision")
	require.Nil(t, second.TargetNodeID, "second generation must not expose another slot on the same Docker host")
	require.True(t, second.Deferred, "second job should defer when the shared physical host is full")
	require.Equal(t, db.SandboxRoutingReasonFleetCapacity, second.Reason, "shared-host saturation should retain the fleet-capacity reason")
}

// TestIntegration_ClaimIsolationSkipsMalformedSandboxCandidate verifies the
// real savepoint path: one tenant's invalid settings cannot stop unrelated
// worker queue claims.
func TestIntegration_ClaimIsolationSkipsMalformedSandboxCandidate(t *testing.T) {
	pool := setup(t)
	ctx := context.Background()
	poisonOrgID := seedOrg(t, pool)
	healthyOrgID := seedOrg(t, pool)
	nodeID := "claim-worker-" + poisonOrgID.String()[:8]
	seedWorkerNode(t, pool, nodeID)

	_, err := pool.Exec(ctx, `
		UPDATE organizations
		SET settings = '{"session_automation":{"automatic_follow_through":{"pr_feedback_mode":"invalid"}}}'::jsonb
		WHERE id = $1`, poisonOrgID)
	require.NoError(t, err, "test should persist semantically invalid org settings")

	store := db.NewJobStore(pool)
	poisonSession := seedSession(t, pool, poisonOrgID, sessionOpts{Status: models.SessionStatusPending})
	poisonJobID, err := store.EnqueueWithOpts(ctx, poisonOrgID, db.EnqueueOpts{
		Queue:         "agent",
		JobType:       "run_agent",
		Payload:       map[string]string{"session_id": poisonSession.ID.String(), "org_id": poisonOrgID.String()},
		Priority:      10,
		TargetNodeID:  &nodeID,
		WorkloadClass: models.SandboxWorkloadClassInteractive,
	})
	require.NoError(t, err, "poison sandbox job should enqueue")
	healthyJobID, err := store.EnqueueWithOpts(ctx, healthyOrgID, db.EnqueueOpts{
		Queue:    "default",
		JobType:  "integration_noop",
		Payload:  map[string]string{"org_id": healthyOrgID.String()},
		Priority: 1,
	})
	require.NoError(t, err, "healthy non-sandbox job should enqueue")

	claimed, err := store.ClaimNextRunnable(ctx, nodeID, nodeID, uuid.New(), time.Minute)
	require.NoError(t, err, "claim pass should isolate invalid sandbox admission")
	require.NotNil(t, claimed, "claim pass should continue to unrelated work")
	require.Equal(t, healthyJobID, claimed.ID, "lower-priority healthy job should bypass the durably deferred poison candidate")

	var poisonStatus models.JobStatus
	var poisonLastError *string
	err = pool.QueryRow(ctx, `SELECT status, last_error FROM jobs WHERE org_id = $1 AND id = $2`, poisonOrgID, poisonJobID).Scan(&poisonStatus, &poisonLastError)
	require.NoError(t, err, "poison job state should remain queryable")
	require.Equal(t, models.JobStatusPending, poisonStatus, "poison candidate should remain pending for repair and retry")
	require.NotNil(t, poisonLastError, "poison candidate should record its isolated admission error")
	require.Contains(t, *poisonLastError, "invalid PR feedback human mode", "recorded error should identify the malformed setting")
}

// TestIntegration_MetadataFallbackClaimKeepsInitialRunDeadline verifies that a
// null-reservation compatibility placement still reaches the bounded terminal
// path after repeated organization-limit deferrals.
func TestIntegration_MetadataFallbackClaimKeepsInitialRunDeadline(t *testing.T) {
	pool := setup(t)
	ctx := context.Background()
	orgID := seedOrg(t, pool)
	nodeID := "fallback-worker-" + orgID.String()[:8]
	seedWorkerNode(t, pool, nodeID)
	_, err := pool.Exec(ctx, `UPDATE organizations SET settings = jsonb_set(settings, '{max_concurrent_runs}', '1'::jsonb, true) WHERE id = $1`, orgID)
	require.NoError(t, err, "test org should use a single shared turn")

	store := db.NewJobStore(pool)
	blockingSession := seedSession(t, pool, orgID, sessionOpts{Status: models.SessionStatusRunning})
	_, err = store.EnqueueWithOpts(ctx, orgID, db.EnqueueOpts{
		Queue:        "agent",
		JobType:      "run_agent",
		Payload:      map[string]string{"session_id": blockingSession.ID.String(), "org_id": orgID.String()},
		Priority:     5,
		TargetNodeID: &nodeID,
	})
	require.NoError(t, err, "blocking job should enqueue")
	blockingJob, err := store.ClaimNextRunnable(ctx, nodeID, nodeID, uuid.New(), time.Minute)
	require.NoError(t, err, "blocking job should claim")
	require.NotNil(t, blockingJob, "blocking job should consume the org turn")

	pendingSession := seedSession(t, pool, orgID, sessionOpts{Status: models.SessionStatusPending})
	pendingJobID, err := store.EnqueueWithOpts(ctx, orgID, db.EnqueueOpts{
		Queue:        "agent",
		JobType:      "run_agent",
		Payload:      map[string]string{"session_id": pendingSession.ID.String(), "org_id": orgID.String()},
		Priority:     5,
		TargetNodeID: &nodeID,
	})
	require.NoError(t, err, "metadata-fallback job should enqueue with a target and no reservation")

	claimed, err := store.ClaimNextRunnable(ctx, nodeID, nodeID, uuid.New(), time.Minute)
	require.NoError(t, err, "org-limited metadata fallback should defer cleanly")
	require.Nil(t, claimed, "org-limited metadata fallback should remain pending")
	var createdAt time.Time
	var retryStartedAt *time.Time
	err = pool.QueryRow(ctx, `SELECT created_at, retry_window_started_at FROM jobs WHERE org_id = $1 AND id = $2`, orgID, pendingJobID).Scan(&createdAt, &retryStartedAt)
	require.NoError(t, err, "deferred fallback job should remain queryable")
	require.NotNil(t, retryStartedAt, "first claim deferral should persist the terminal deadline marker")
	require.WithinDuration(t, createdAt, *retryStartedAt, time.Microsecond, "initial-run terminal deadline should remain anchored to job creation")
}

// TestIntegration_SessionCapacityStatusExcludesOwnReservation verifies the
// runtime-status distinction between an admitted pending session and another
// session that is genuinely waiting behind the organization limit.
//
// This test cannot run in parallel because the integration suite shares one
// database and setup truncates queue state between tests.
func TestIntegration_SessionCapacityStatusExcludesOwnReservation(t *testing.T) {
	pool := setup(t)
	ctx := context.Background()
	orgID := seedOrg(t, pool)
	nodeID := "status-worker-" + orgID.String()[:8]
	seedWorkerNode(t, pool, nodeID)
	store := db.NewJobStore(pool)

	admittedSession := seedSession(t, pool, orgID, sessionOpts{Status: models.SessionStatusPending})
	admittedJobID, err := store.EnqueueWithOpts(ctx, orgID, db.EnqueueOpts{
		Queue:        "agent",
		JobType:      "run_agent",
		Payload:      map[string]string{"session_id": admittedSession.ID.String(), "org_id": orgID.String()},
		Priority:     5,
		TargetNodeID: &nodeID,
	})
	require.NoError(t, err, "admitted session job should enqueue")
	_, err = pool.Exec(ctx, `
		UPDATE jobs
		SET sandbox_slot_reserved_until = now() + interval '1 minute'
		WHERE org_id = $1 AND id = $2`, orgID, admittedJobID)
	require.NoError(t, err, "test should give the admitted session a live worker reservation")

	waitingSession := seedSession(t, pool, orgID, sessionOpts{Status: models.SessionStatusPending})
	_, err = store.EnqueueWithOpts(ctx, orgID, db.EnqueueOpts{
		Queue:    "agent",
		JobType:  "run_agent",
		Payload:  map[string]string{"session_id": waitingSession.ID.String(), "org_id": orgID.String()},
		Priority: 5,
	})
	require.NoError(t, err, "waiting session job should enqueue")

	admittedWaiting, err := store.IsSessionWaitingForSandboxCapacity(ctx, orgID, admittedSession.ID, 1)
	require.NoError(t, err, "admitted session capacity lookup should succeed")
	require.False(t, admittedWaiting, "a session's own live reservation should not make it appear capacity-blocked")
	waiting, err := store.IsSessionWaitingForSandboxCapacity(ctx, orgID, waitingSession.ID, 1)
	require.NoError(t, err, "queued session capacity lookup should succeed")
	require.True(t, waiting, "a queued session should report waiting when another admitted turn consumes the organization limit")
}

// TestIntegration_ClaimSkipsSaturatedOrgBacklog verifies that one tenant's
// affinity-bound sandbox burst cannot consume the bounded claim scan and hide
// ordinary runnable work. One deferral excludes only that tenant's sandbox
// jobs for the rest of the pass; non-sandbox work from the same tenant remains
// eligible.
//
// This test cannot run in parallel because the integration suite shares one
// database and setup truncates queue state between tests.
func TestIntegration_ClaimSkipsSaturatedOrgBacklog(t *testing.T) {
	pool := setup(t)
	ctx := context.Background()
	orgID := seedOrg(t, pool)
	_, err := pool.Exec(ctx, `UPDATE organizations SET settings = jsonb_set(settings, '{max_concurrent_runs}', '1'::jsonb, true) WHERE id = $1`, orgID)
	require.NoError(t, err, "test organization should enforce one shared sandbox turn")
	nodeID := "backlog-worker-" + orgID.String()[:8]
	seedWorkerNode(t, pool, nodeID)
	store := db.NewJobStore(pool)

	blockerID, err := store.EnqueueWithOpts(ctx, orgID, db.EnqueueOpts{
		Queue: "agent", JobType: "run_agent", Payload: map[string]string{"org_id": orgID.String()},
		Priority: 5, TargetNodeID: &nodeID,
	})
	require.NoError(t, err, "blocking sandbox turn should enqueue")
	_, err = pool.Exec(ctx, `
		UPDATE jobs
		SET status = 'running', lock_token = $3, locked_by_node_id = $2,
			run_owner_id = $2, lease_expires_at = now() + interval '1 minute'
		WHERE org_id = $1 AND id = $4`, orgID, nodeID, uuid.New(), blockerID)
	require.NoError(t, err, "test should consume the organization sandbox turn")

	for i := 0; i < 20; i++ {
		_, err = store.EnqueueWithOpts(ctx, orgID, db.EnqueueOpts{
			Queue: "agent", JobType: "run_agent", Payload: map[string]string{"org_id": orgID.String()},
			Priority: 5, TargetNodeID: &nodeID,
		})
		require.NoError(t, err, "saturated sandbox backlog job should enqueue")
	}
	ordinaryJobID, err := store.Enqueue(ctx, orgID, "agent", "ordinary_maintenance", map[string]string{"org_id": orgID.String()}, 5, nil)
	require.NoError(t, err, "ordinary work should enqueue behind the sandbox backlog")

	claimed, err := store.ClaimNextRunnable(ctx, nodeID, nodeID, uuid.New(), time.Minute)
	require.NoError(t, err, "claim pass should skip the saturated tenant sandbox backlog")
	require.NotNil(t, claimed, "ordinary work should remain visible within the bounded claim pass")
	require.Equal(t, ordinaryJobID, claimed.ID, "claim should reach non-sandbox work after one tenant-level sandbox deferral")
}

// TestIntegration_CodeReviewJobsUseFleetCapacityAndPreserveInteractiveReserve
// uses the real queue and Postgres locking path to exercise a mixed-version
// code-review burst. It intentionally enqueues review jobs with the legacy
// default workload class; routing must recover code_review from session origin,
// spread reviews across workers, defer only when review-eligible capacity is
// genuinely full, and still admit an interactive job into the reserved lane.
//
// This test cannot run in parallel because the integration suite shares one
// database and setup truncates queue state between tests.
func TestIntegration_CodeReviewJobsUseFleetCapacityAndPreserveInteractiveReserve(t *testing.T) {
	pool := setup(t)
	ctx := context.Background()
	orgID := seedOrg(t, pool)
	_, err := pool.Exec(ctx, `
		UPDATE organizations
		SET settings = '{"max_concurrent_runs": 4}'::jsonb
		WHERE id = $1`, orgID)
	require.NoError(t, err, "test org should allow all three routed reservations")

	nodeIDs := []string{
		"review-worker-a-" + orgID.String()[:8],
		"review-worker-b-" + orgID.String()[:8],
	}
	for _, nodeID := range nodeIDs {
		seedWorkerNode(t, pool, nodeID)
		setWorkerSandboxCapacity(t, pool, nodeID, 2)
		_, err := pool.Exec(ctx, `
			UPDATE nodes
			SET metadata = metadata || '{"interactive_reserved_sandbox_slots": 1}'::jsonb
			WHERE id = $1`, nodeID)
		require.NoError(t, err, "worker should advertise one interactive-only sandbox slot")
	}

	store := db.NewJobStore(pool)
	reviewJobIDs := make([]uuid.UUID, 0, 3)
	for range 3 {
		session := seedSession(t, pool, orgID, sessionOpts{
			Status: models.SessionStatusPending,
			Origin: models.SessionOriginCodeReview,
		})
		jobID, err := store.EnqueueWithOpts(ctx, orgID, db.EnqueueOpts{
			Queue:    "agent",
			JobType:  "run_agent",
			Payload:  map[string]string{"session_id": session.ID.String(), "org_id": orgID.String()},
			Priority: 5,
			// Empty is deliberate: it simulates a job written by the binary
			// version immediately before workload_class was introduced.
		})
		require.NoError(t, err, "legacy code-review job should enqueue")
		reviewJobIDs = append(reviewJobIDs, jobID)
	}

	placements := make([]*db.SandboxRoutingResult, 0, 2)
	for i := 0; i < 2; i++ {
		result, err := store.RouteNextSandboxJob(ctx)
		require.NoError(t, err, "code-review routing should reserve available fleet capacity")
		require.NotNil(t, result, "routing should select a pending review job")
		require.Equal(t, models.SandboxWorkloadClassCodeReview, result.WorkloadClass, "session origin should repair the legacy workload class")
		require.Equal(t, db.SandboxRoutingReasonReserved, result.Reason, "review should be immediately reserved while a review-eligible slot exists")
		require.NotNil(t, result.TargetNodeID, "reserved review should have a target worker")
		placements = append(placements, result)
	}
	require.NotEqual(t, *placements[0].TargetNodeID, *placements[1].TargetNodeID, "review burst should spread across the two review-eligible worker slots")

	fullResult, err := store.RouteNextSandboxJob(ctx)
	require.NoError(t, err, "full review fleet should produce a bounded deferral")
	require.NotNil(t, fullResult, "routing should report the deferred third review")
	require.Equal(t, db.SandboxRoutingReasonFleetCapacity, fullResult.Reason, "third review should wait because only interactive-reserved capacity remains")
	require.True(t, fullResult.Deferred, "third review should receive the genuine-fleet-full delay")
	require.Nil(t, fullResult.TargetNodeID, "deferred review should not retain a stale worker target")

	interactiveSession := seedSession(t, pool, orgID, sessionOpts{Status: models.SessionStatusPending})
	interactiveJobID, err := store.EnqueueWithOpts(ctx, orgID, db.EnqueueOpts{
		Queue:         "agent",
		JobType:       "run_agent",
		Payload:       map[string]string{"session_id": interactiveSession.ID.String(), "org_id": orgID.String()},
		Priority:      5,
		WorkloadClass: models.SandboxWorkloadClassInteractive,
	})
	require.NoError(t, err, "interactive job should enqueue during the review burst")

	interactiveResult, err := store.RouteNextSandboxJob(ctx)
	require.NoError(t, err, "interactive job should route into capacity reserved from reviews")
	require.NotNil(t, interactiveResult, "routing should select the interactive job")
	require.Equal(t, interactiveJobID, interactiveResult.JobID, "interactive-first routing should select the newly queued user turn")
	require.Equal(t, db.SandboxRoutingReasonReserved, interactiveResult.Reason, "interactive reserve should remain usable while reviews are saturated")
	require.NotNil(t, interactiveResult.TargetNodeID, "interactive job should receive a worker target")

	claimedInteractive, err := store.ClaimNextRunnable(ctx, *interactiveResult.TargetNodeID, *interactiveResult.TargetNodeID, uuid.New(), 2*time.Minute)
	require.NoError(t, err, "target worker should claim the interactive job")
	require.NotNil(t, claimedInteractive, "interactive job should transition from reserved to running")
	require.Equal(t, interactiveJobID, claimedInteractive.ID, "interactive-first claim should run the interactive job before the colocated review")

	var reservationBefore time.Time
	err = pool.QueryRow(ctx, `SELECT sandbox_slot_reserved_until FROM jobs WHERE id = $1`, claimedInteractive.ID).Scan(&reservationBefore)
	require.NoError(t, err, "running interactive reservation should remain durable until sandbox admission")
	_, renewed, err := store.RenewLease(ctx, claimedInteractive.ID, *claimedInteractive.LockToken, 3*time.Minute)
	require.NoError(t, err, "worker lease renewal should succeed")
	require.True(t, renewed, "worker should retain fenced ownership during sandbox startup")
	var reservationAfter time.Time
	err = pool.QueryRow(ctx, `SELECT sandbox_slot_reserved_until FROM jobs WHERE id = $1`, claimedInteractive.ID).Scan(&reservationAfter)
	require.NoError(t, err, "renewed reservation should remain queryable")
	require.True(t, reservationAfter.After(reservationBefore), "job lease heartbeat should extend the durable sandbox reservation")

	for _, jobID := range reviewJobIDs[:2] {
		var workloadClass string
		err := pool.QueryRow(ctx, `SELECT workload_class FROM jobs WHERE id = $1`, jobID).Scan(&workloadClass)
		require.NoError(t, err, "routed review workload class should be persisted")
		require.Equal(t, string(models.SandboxWorkloadClassCodeReview), workloadClass, "mixed-version review job should no longer consume interactive placement policy")
	}
}

// A worker that advertises capacity but cannot currently measure its live
// sandbox count is not a legacy worker. Routing must wait for healthy telemetry
// instead of bypassing capacity controls through the compatibility fallback.
//
// This test cannot run in parallel because the integration suite shares one
// database and setup truncates queue state between tests.
func TestIntegration_ConfiguredWorkerWithLiveCountErrorDefersSandboxRouting(t *testing.T) {
	pool := setup(t)
	ctx := context.Background()
	orgID := seedOrg(t, pool)
	session := seedSession(t, pool, orgID, sessionOpts{Status: models.SessionStatusPending})

	nodeID := "unhealthy-count-worker-" + orgID.String()[:8]
	seedWorkerNode(t, pool, nodeID)
	setWorkerSandboxCapacity(t, pool, nodeID, 2)
	_, err := pool.Exec(ctx, `
		UPDATE nodes
		SET metadata = metadata || jsonb_build_object(
			'live_sandbox_count_error', 'container runtime unavailable'
		)
		WHERE id = $1`, nodeID)
	require.NoError(t, err, "worker should advertise a temporary live-count failure")

	store := db.NewJobStore(pool)
	jobID, err := store.EnqueueWithOpts(ctx, orgID, db.EnqueueOpts{
		Queue:         "agent",
		JobType:       "run_agent",
		Payload:       map[string]string{"session_id": session.ID.String(), "org_id": orgID.String()},
		Priority:      5,
		WorkloadClass: models.SandboxWorkloadClassInteractive,
	})
	require.NoError(t, err, "interactive sandbox job should enqueue")

	result, err := store.RouteNextSandboxJob(ctx)
	require.NoError(t, err, "routing should safely handle unhealthy live-count telemetry")
	require.NotNil(t, result, "routing should report its capacity decision")
	require.Equal(t, jobID, result.JobID, "routing should evaluate the queued sandbox job")
	require.Equal(t, db.SandboxRoutingReasonFleetCapacity, result.Reason, "configured but unhealthy capacity metadata should be treated as temporarily unavailable capacity")
	require.True(t, result.Deferred, "routing should defer until a trustworthy live count is available")
	require.Nil(t, result.TargetNodeID, "routing should not pin work through the compatibility fallback")
}

// TestIntegration_WorkerDispatch_PicksUpAndCallsHandler closes the loop on
// the queue → worker → handler seam. The session-side tests upstream prove
// that pushing/creating/retrying/ending writes the right job row; this test
// proves that an enqueued job actually reaches its handler. Together they
// guarantee that a refactor of either side breaks at least one test before
// it ships.
//
// Specifically guards:
//   - Worker.ClaimNextRunnable wired through real SQL — schema/index drift
//     surfaces here, not just in pgxmock.
//   - Job-type → handler dispatch table — a typo in the registered key
//     would silently dead-letter every job in production but passes
//     pgxmock unit tests today.
//   - Lease + ack lifecycle — the worker must transition `pending` →
//     `running` (claim, attempts++) → `success` (mark succeeded with the
//     fencing token). Each transition is real SQL.
//
// The test uses Worker.Wake() to short-circuit the 5s poll ticker so it
// runs in <1s. We do not refactor the Worker to expose a single-tick
// method — Wake() is already a public API, intended for exactly this kind
// of "process now" signal.
func TestIntegration_WorkerDispatch_PicksUpAndCallsHandler(t *testing.T) {
	pool := setup(t)

	orgID := seedOrg(t, pool)

	// Seed a session to anchor the foreign-key payload reference. The
	// worker doesn't dereference session_id, but having one on hand keeps
	// the payload realistic — exactly what production data looks like.
	session := seedSession(t, pool, orgID, sessionOpts{
		Status: "pending",
	})

	// Enqueue a continue_session job through the real JobStore — the
	// same code path SendMessage uses in production. (We could insert via
	// raw SQL, but going through the store catches a typo in either side.)
	jobStore := db.NewJobStore(pool)
	payload := map[string]string{
		"session_id": session.ID.String(),
		"org_id":     orgID.String(),
	}
	jobID, err := jobStore.Enqueue(context.Background(), orgID, "agent", "continue_session", payload, 5, nil)
	require.NoError(t, err)
	require.NotEqual(t, "", jobID.String())

	// Build the worker with a recording handler. The `received` channel
	// signals from the handler back to the test goroutine; using a
	// 1-buffered channel avoids deadlocking if the handler runs before the
	// test starts reading.
	type recvJob struct {
		JobType string
		Payload json.RawMessage
	}
	received := make(chan recvJob, 1)
	nodeID := "test-node-" + jobID.String()[:8]
	seedWorkerNode(t, pool, nodeID)
	setWorkerSandboxCapacity(t, pool, nodeID, 1)
	w := worker.New(pool, zerolog.Nop(), nodeID)
	w.EnableSandboxRouting()
	w.Register("continue_session", func(ctx context.Context, jobType string, payload json.RawMessage) error {
		received <- recvJob{JobType: jobType, Payload: payload}
		return nil
	})

	// Start the worker. context.WithCancel lets the test stop it cleanly
	// once we've observed the dispatch — otherwise it would keep polling
	// forever, holding goroutines past the test's lifetime.
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Start(ctx)

	// Wake() short-circuits the poll ticker — without it we'd wait up to
	// 5s for the next tick, which is fine for production but turns this
	// test into a flake on slow CI machines.
	w.Wake()

	// Wait for the handler invocation, with a generous-but-bounded timeout.
	// 5s is the worker's own poll interval, so any longer means something
	// is actually broken (not just slow CI).
	select {
	case got := <-received:
		require.Equal(t, "continue_session", got.JobType,
			"worker dispatched to the wrong handler — handler-table drift")
		require.Equal(t, session.ID.String(), payloadField(t, got.Payload, "session_id"),
			"worker delivered the wrong payload to the handler")
		require.Equal(t, orgID.String(), payloadField(t, got.Payload, "org_id"))
	case <-time.After(5 * time.Second):
		t.Fatal("worker did not dispatch the job within 5s — claim or wake path is broken")
	}

	// After dispatch returns nil, the worker writes status='succeeded' on
	// the job row asynchronously (see MarkSucceededWithLease in jobs.go).
	// Poll briefly for the terminal state — the alternative (an
	// unconditional time.Sleep) is the kind of test flakiness this whole
	// suite is supposed to prevent.
	require.Eventually(t, func() bool {
		var status string
		err := pool.QueryRow(context.Background(),
			`SELECT status FROM jobs WHERE id = $1`, jobID).Scan(&status)
		if err != nil {
			return false
		}
		return status == "succeeded"
	}, 3*time.Second, 25*time.Millisecond, "job did not transition to succeeded after handler returned nil — ack/lease path is broken")
}

// TestIntegration_WorkerDispatch_UnknownJobTypeDeadLetters covers the
// failure mode that drove the test's existence in the first place: a job
// whose type has no registered handler should be promptly failed (not
// silently swallowed). Otherwise a typo'd refactor of a job-type constant
// turns into a sea of pending-forever rows.
func TestIntegration_WorkerDispatch_UnknownJobTypeDeadLetters(t *testing.T) {
	pool := setup(t)

	orgID := seedOrg(t, pool)

	jobStore := db.NewJobStore(pool)
	payload := map[string]string{"org_id": orgID.String()}
	jobID, err := jobStore.Enqueue(context.Background(), orgID, "agent", "no_such_handler", payload, 5, nil)
	require.NoError(t, err)

	nodeID := "test-node-deadletter-" + jobID.String()[:8]
	seedWorkerNode(t, pool, nodeID)
	w := worker.New(pool, zerolog.Nop(), nodeID)
	// Deliberately do *not* Register a handler for "no_such_handler".

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go w.Start(ctx)
	w.Wake()

	require.Eventually(t, func() bool {
		var status string
		var lastError *string
		err := pool.QueryRow(context.Background(),
			`SELECT status, last_error FROM jobs WHERE id = $1`, jobID).Scan(&status, &lastError)
		if err != nil {
			return false
		}
		return status == "failed" && lastError != nil && *lastError != ""
	}, 5*time.Second, 25*time.Millisecond, "unknown job type must fail fast with an explanatory last_error — silent swallow is the worst outcome")
}

// Regression for the rollout-starvation incident: a continue_session turn
// pinned to a draining (but still heartbeating) node must fall through to an
// active worker instead of sitting pending until the node dies. pgxmock can't
// see node-status semantics, so this runs against real Postgres.
func TestIntegration_ClaimNextRunnable_DrainingTargetNodeReleasesPinnedJob(t *testing.T) {
	pool := setup(t)
	orgID := seedOrg(t, pool)
	session := seedSession(t, pool, orgID, sessionOpts{Status: "idle"})
	store := db.NewJobStore(pool)

	drainingNode := "drain-node-" + session.ID.String()[:8]
	seedWorkerNode(t, pool, drainingNode)
	setNodeStatus(t, pool, drainingNode, "draining")

	target := drainingNode
	jobID, err := store.EnqueueWithOpts(context.Background(), orgID, db.EnqueueOpts{
		Queue:        "agent",
		JobType:      "continue_session",
		Payload:      map[string]string{"session_id": session.ID.String(), "org_id": orgID.String()},
		Priority:     5,
		TargetNodeID: &target,
	})
	require.NoError(t, err)

	claimer := "active-node-" + session.ID.String()[:8]
	seedWorkerNode(t, pool, claimer)

	job, err := store.ClaimNextRunnable(context.Background(), claimer, claimer, uuid.New(), 30*time.Second)
	require.NoError(t, err)
	require.NotNil(t, job, "a job pinned to a draining node must fall through to an active worker, not starve until the node dies")
	require.Equal(t, jobID, job.ID, "the claimed job should be the one pinned to the draining node")
	require.NotNil(t, job.LockedByNodeID)
	require.Equal(t, claimer, *job.LockedByNodeID, "the claim should be locked by the active worker that took it over")
}

// Guard rail: a job pinned to a healthy active node stays pinned to its owner
// (a sibling can't steal it), so the draining release didn't widen into "any
// pin is claimable" (the #811 cross-host bug).
func TestIntegration_ClaimNextRunnable_HealthyTargetNodePinHoldsJob(t *testing.T) {
	pool := setup(t)
	orgID := seedOrg(t, pool)
	session := seedSession(t, pool, orgID, sessionOpts{Status: "idle"})
	store := db.NewJobStore(pool)

	ownerNode := "owner-node-" + session.ID.String()[:8]
	seedWorkerNode(t, pool, ownerNode)

	target := ownerNode
	jobID, err := store.EnqueueWithOpts(context.Background(), orgID, db.EnqueueOpts{
		Queue:        "agent",
		JobType:      "continue_session",
		Payload:      map[string]string{"session_id": session.ID.String(), "org_id": orgID.String()},
		Priority:     5,
		TargetNodeID: &target,
	})
	require.NoError(t, err)

	// A sibling worker must NOT be able to claim the pinned job.
	sibling := "sibling-node-" + session.ID.String()[:8]
	seedWorkerNode(t, pool, sibling)
	job, err := store.ClaimNextRunnable(context.Background(), sibling, sibling, uuid.New(), 30*time.Second)
	require.NoError(t, err)
	require.Nil(t, job, "a sibling worker must not claim a job pinned to a healthy owning node")

	// The owning node still can.
	owned, err := store.ClaimNextRunnable(context.Background(), ownerNode, ownerNode, uuid.New(), 30*time.Second)
	require.NoError(t, err)
	require.NotNil(t, owned, "the owning node must still be able to claim its pinned job")
	require.Equal(t, jobID, owned.ID)
}
