package worker

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/cache"
	"github.com/assembledhq/143/internal/models"
)

// newPublishTestStreams wires miniredis-backed eval stream helpers so tests
// can assert on actually-published events rather than relying on a stub that
// can drift from production publish semantics. The miniredis instance is
// torn down by t.Cleanup via RunT.
func newPublishTestStreams(t *testing.T) (*cache.EvalBatchStreams, *cache.EvalBootstrapStreams) {
	t.Helper()
	mr := miniredis.RunT(t)
	metrics, err := cache.NewMetrics()
	require.NoError(t, err, "Redis metrics should initialize for publish tests")
	client := cache.New(cache.Config{Topology: "standalone", URL: "redis://" + mr.Addr()}, zerolog.Nop(), metrics)
	require.NotNil(t, client, "Redis client should initialize for publish tests")
	t.Cleanup(func() { _ = client.Close() })
	return cache.NewEvalBatchStreams(client, zerolog.Nop()), cache.NewEvalBootstrapStreams(client, zerolog.Nop())
}

func TestPublishEvalBatchSignal_DeliversToBatchSubscriber(t *testing.T) {
	t.Parallel()

	// Locks in the contract that worker code paths (newRunEvalHandler at
	// run-start and after CompleteBatchIfDone) call publishEvalBatchSignal
	// with the right shape and the resulting event lands on the per-batch
	// channel. Without this test, a future refactor could quietly drop the
	// publish call and break the SSE detail page without any unit failure.
	batchStreams, _ := newPublishTestStreams(t)
	services := &Services{EvalBatchStreams: batchStreams}

	orgID := uuid.New()
	batchID := uuid.New()
	sub, err := batchStreams.Subscribe(batchID)
	require.NoError(t, err, "subscribe should succeed against a healthy Redis instance")
	defer sub.Close()

	publishEvalBatchSignal(context.Background(), services, orgID, batchID, models.EvalBatchStatusRunning, zerolog.Nop())

	select {
	case got := <-sub.C:
		require.Equal(t, batchID, got.BatchID, "delivered event should carry the publishing batch ID")
		require.Equal(t, orgID, got.OrgID, "delivered event should carry the publishing org ID for downstream telemetry")
		require.Equal(t, models.EvalBatchStatusRunning, got.Status, "delivered event should carry the published status as a hint (canonical state lives in Postgres)")
		require.False(t, got.UpdatedAt.IsZero(), "delivered event should stamp UpdatedAt so subscribers can drop stale duplicates if needed")
	case <-time.After(2 * time.Second):
		t.Fatal("publishEvalBatchSignal did not deliver an event within the timeout")
	}
}

func TestPublishEvalBatchSignal_NoOpWhenStreamsMissing(t *testing.T) {
	t.Parallel()

	// publishEvalBatchSignal is best-effort and must tolerate (1) a nil
	// services struct, (2) a Services struct with EvalBatchStreams unset
	// (production fallback when Redis is not configured), and (3) a zero
	// batchID (no-batch single-run path). Each must be a silent no-op —
	// the real publishing code is exercised by cache.EvalBatchStreams' own
	// tests.
	publishEvalBatchSignal(context.Background(), nil, uuid.New(), uuid.New(), models.EvalBatchStatusRunning, zerolog.Nop())
	publishEvalBatchSignal(context.Background(), &Services{}, uuid.New(), uuid.New(), models.EvalBatchStatusRunning, zerolog.Nop())

	batchStreams, _ := newPublishTestStreams(t)
	services := &Services{EvalBatchStreams: batchStreams}
	sub, err := batchStreams.Subscribe(uuid.New())
	require.NoError(t, err, "subscribe should succeed against a healthy Redis instance")
	defer sub.Close()
	publishEvalBatchSignal(context.Background(), services, uuid.New(), uuid.Nil, models.EvalBatchStatusRunning, zerolog.Nop())
	require.Never(t, func() bool {
		select {
		case <-sub.C:
			return true
		default:
			return false
		}
	}, 100*time.Millisecond, 20*time.Millisecond, "publish with zero batchID should be a silent no-op")
}

func TestPublishEvalBootstrapSignal_DeliversToRunSubscriber(t *testing.T) {
	t.Parallel()

	_, bootstrapStreams := newPublishTestStreams(t)
	services := &Services{EvalBootstrapStreams: bootstrapStreams}

	orgID := uuid.New()
	runID := uuid.New()
	sessionID := uuid.New()
	sub, err := bootstrapStreams.Subscribe(runID)
	require.NoError(t, err, "subscribe should succeed against a healthy Redis instance")
	defer sub.Close()

	publishEvalBootstrapSignal(context.Background(), services, orgID, runID, models.EvalBootstrapStatusCompleted, &sessionID, zerolog.Nop())

	select {
	case got := <-sub.C:
		require.Equal(t, runID, got.BootstrapRunID, "delivered event should carry the publishing run ID")
		require.Equal(t, orgID, got.OrgID, "delivered event should carry the org ID")
		require.Equal(t, models.EvalBootstrapStatusCompleted, got.Status, "delivered event should carry the published status hint")
		require.NotNil(t, got.SessionID, "delivered event should preserve the session_id pointer when set")
		require.Equal(t, sessionID, *got.SessionID, "delivered event should round-trip the session_id value")
	case <-time.After(2 * time.Second):
		t.Fatal("publishEvalBootstrapSignal did not deliver an event within the timeout")
	}
}

func TestPublishEvalBootstrapSignal_NoOpWhenStreamsMissing(t *testing.T) {
	t.Parallel()

	publishEvalBootstrapSignal(context.Background(), nil, uuid.New(), uuid.New(), models.EvalBootstrapStatusRunning, nil, zerolog.Nop())
	publishEvalBootstrapSignal(context.Background(), &Services{}, uuid.New(), uuid.New(), models.EvalBootstrapStatusRunning, nil, zerolog.Nop())
}

func TestPublishEvalBatchSignal_DropsCrossBatchEvents(t *testing.T) {
	t.Parallel()

	// Per-batch channel scoping means a publish for batch B must not be
	// observable on a subscription to batch A even when both share an org.
	// This is the worker-side counterpart to TestEvalBatchStreams_PerBatchScoping
	// in the cache package — together they cover the full publish-then-
	// receive path through the worker helper.
	batchStreams, _ := newPublishTestStreams(t)
	services := &Services{EvalBatchStreams: batchStreams}
	orgID := uuid.New()
	batchA := uuid.New()
	batchB := uuid.New()

	subA, err := batchStreams.Subscribe(batchA)
	require.NoError(t, err)
	defer subA.Close()

	publishEvalBatchSignal(context.Background(), services, orgID, batchB, models.EvalBatchStatusRunning, zerolog.Nop())

	require.Never(t, func() bool {
		select {
		case <-subA.C:
			return true
		default:
			return false
		}
	}, 200*time.Millisecond, 20*time.Millisecond, "subscriber to batch A must not see publishes scoped to batch B")
}

// TestEvalEventPayloadsHaveStableJSONKeys guards against a future refactor
// accidentally renaming the on-the-wire JSON keys, which the frontend
// depends on. The keys are part of the public SSE contract.
func TestEvalEventPayloadsHaveStableJSONKeys(t *testing.T) {
	t.Parallel()

	batchPayload, err := json.Marshal(models.EvalBatchUpdatedEvent{
		BatchID:   uuid.New(),
		OrgID:     uuid.New(),
		Status:    models.EvalBatchStatusRunning,
		UpdatedAt: time.Unix(0, 0).UTC(),
	})
	require.NoError(t, err)
	for _, key := range []string{`"batch_id"`, `"org_id"`, `"status"`, `"updated_at"`} {
		require.Contains(t, string(batchPayload), key, "EvalBatchUpdatedEvent JSON must keep the documented frontend keys")
	}

	bootstrapPayload, err := json.Marshal(models.EvalBootstrapUpdatedEvent{
		BootstrapRunID: uuid.New(),
		OrgID:          uuid.New(),
		Status:         models.EvalBootstrapStatusRunning,
		UpdatedAt:      time.Unix(0, 0).UTC(),
	})
	require.NoError(t, err)
	for _, key := range []string{`"bootstrap_run_id"`, `"org_id"`, `"status"`, `"updated_at"`} {
		require.Contains(t, string(bootstrapPayload), key, "EvalBootstrapUpdatedEvent JSON must keep the documented frontend keys")
	}
}
