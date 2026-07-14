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
	w := worker.New(pool, zerolog.Nop(), nodeID, models.ReleaseChannelStable)
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
	w := worker.New(pool, zerolog.Nop(), nodeID, models.ReleaseChannelStable)
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

	job, err := store.ClaimNextRunnable(context.Background(), claimer, claimer, models.ReleaseChannelStable, uuid.New(), 30*time.Second)
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
	job, err := store.ClaimNextRunnable(context.Background(), sibling, sibling, models.ReleaseChannelStable, uuid.New(), 30*time.Second)
	require.NoError(t, err)
	require.Nil(t, job, "a sibling worker must not claim a job pinned to a healthy owning node")

	// The owning node still can.
	owned, err := store.ClaimNextRunnable(context.Background(), ownerNode, ownerNode, models.ReleaseChannelStable, uuid.New(), 30*time.Second)
	require.NoError(t, err)
	require.NotNil(t, owned, "the owning node must still be able to claim its pinned job")
	require.Equal(t, jobID, owned.ID)
}
