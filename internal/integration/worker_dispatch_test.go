//go:build integration

package integration

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/db"
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
	w := worker.New(pool, zerolog.Nop(), "test-node-"+jobID.String()[:8])
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

	w := worker.New(pool, zerolog.Nop(), "test-node-deadletter")
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
