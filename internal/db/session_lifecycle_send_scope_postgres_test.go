package db

import (
	"bytes"
	"context"
	"slices"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	testpostgres "github.com/assembledhq/143/internal/testutil/postgres"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

func TestLegacyThreadSendScopeFullSchemaPostgresLocksAndComposition(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		hidden bool
	}{
		{name: "visible exact target composes with the unchanged idle claim"},
		{name: "hidden parent and exact target are locked but not admitted", hidden: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newThreadSendScopePostgresFixture(t, tt.hidden)
			before := lifecycleProtocolRows(t, context.Background(), f.pool, f.orgID, f.sessionID)
			baseline := threadPersistencePostgresThread(t, context.Background(), f.pool, f.targetFixture())
			store, redis := threadPersistenceStoreWithStreams(t, nil)
			ctx, cancel := context.WithTimeout(context.Background(), LifecycleTransactionTimeout)
			defer cancel()
			owner := beginThreadSendScopePostgresUnit(t, ctx, f.pool)
			var at time.Time
			require.NoError(t, owner.QueryRow(ctx, "SELECT now()").Scan(&at), "the composed legacy mutation must use this exact owner's transaction timestamp")
			tx := newThreadSendScopeObservedTx(owner)
			tx.beforeParent = func() {
				probeThreadSendCutoverExclusive(t, ctx, f, false)
				probeLifecycleScopeRowLock(t, ctx, f.pool, f.orgID, f.sessionID, nil, false)
			}
			tx.beforeDiscovery = func() {
				probeLifecycleScopeRowLock(t, ctx, f.pool, f.orgID, f.sessionID, nil, true)
				for _, id := range f.selected {
					probeLifecycleScopeRowLock(t, ctx, f.pool, f.orgID, f.sessionID, &id, false)
				}
			}
			tx.beforeLaneQuery = func() {
				probeLifecycleScopeRowLock(t, ctx, f.pool, f.orgID, f.sessionID, nil, true)
				for _, id := range f.selected {
					probeLifecycleScopeRowLock(t, ctx, f.pool, f.orgID, f.sessionID, &id, false)
				}
			}
			require.NoError(t, RequireLegacyThreadSendScopeInTx(ctx, tx, f.orgID, f.sessionID, f.targetID), "the legacy guard must lock raw exact scope without deciding visibility, status, claim capacity or admission")
			require.Equal(t, threadSendScopeTrace{modeReads: 1, parentReads: 1, discoveryReads: 1, laneBatches: 1, steps: []string{"mode", "parent", "discovery", "lanes"}, laneIDs: f.selected}, tx.trace(), "one fresh mode observation must precede parent-first discovery and one complete sorted lane batch")
			require.Equal(t, before, lifecycleProtocolRows(t, ctx, owner, f.orgID, f.sessionID), "successful locking alone must not change any parent, cutover, visible lane or retained hidden-lane field")
			probeThreadSendCutoverExclusive(t, ctx, f, false)
			probeLifecycleScopeRowLock(t, ctx, f.pool, f.orgID, f.sessionID, nil, true)
			for _, id := range f.selected {
				probeLifecycleScopeRowLock(t, ctx, f.pool, f.orgID, f.sessionID, &id, true)
			}
			probeLifecycleScopeRowLock(t, ctx, f.pool, f.orgID, f.sessionID, &f.hiddenSiblingID, false)

			// The real v0 CTE composes only after canonical acquisition. This is
			// not a SendMessage call-through or lifecycle admission proof.
			actual, effects, err := store.ClaimIdleForSessionInTx(ctx, f.orgID, tx, f.sessionID, f.targetID, models.MaxRunningThreadsPerSession)
			if tt.hidden {
				require.ErrorIs(t, err, pgx.ErrNoRows, "locking a retained archived exact target must not bypass the legacy claim's visibility predicate")
				require.Equal(t, models.SessionThread{}, actual, "the hidden target must return no accepted claim row")
				require.Equal(t, ThreadClaimPersistenceEffects{}, effects, "a guard is not an event-producing admission permit")
				require.Equal(t, before, lifecycleProtocolRows(t, ctx, owner, f.orgID, f.sessionID), "a hidden-lane claim miss must not mutate any raw row after successful guard acquisition")
			} else {
				expected := baseline
				expected.Status, expected.StartedAt, expected.LastActivityAt, expected.CancelRequestedAt = models.ThreadStatusRunning, &at, &at, nil
				require.NoError(t, err, "the unchanged idle claim must compose using only the already-owned transaction despite the store's nil database")
				require.Equal(t, expected, actual, "the complete legacy claim projection must preserve prior completion, failure and provider fields")
				require.Equal(t, ThreadClaimPersistenceEffects{runtime: models.NewThreadRuntimeEvent(expected), runtimeChanged: true}, effects, "the composed claim must return only its exact deferred runtime snapshot")
				require.Equal(t, expected, threadPersistencePostgresThread(t, ctx, owner, f.targetFixture()), "the owner's claimed row must exactly match the returned projection")
				require.NotEqual(t, before, lifecycleProtocolRows(t, ctx, owner, f.orgID, f.sessionID), "rollback coverage must follow a genuine staged legacy mutation")
			}
			require.Equal(t, before, lifecycleProtocolRows(t, ctx, f.pool, f.orgID, f.sessionID), "an independent connection must still see every original row before the owner settles")
			require.Empty(t, redis.Keys(), "neither locking nor the composed claim may publish enabled Redis events before settlement")
			requireLifecycleScopeNoSettlement(t, tx.lifecycleScopeConcurrencyTx)
			require.NoError(t, owner.Rollback(ctx), "the owner alone must discard the guard and any staged legacy claim as one unit")
			require.Equal(t, before, lifecycleProtocolRows(t, context.Background(), f.pool, f.orgID, f.sessionID), "whole-unit rollback must leave every retained row exactly unchanged")
			require.Empty(t, redis.Keys(), "discarded claim effects must leave enabled Redis entirely unchanged")
			probeThreadSendCutoverExclusive(t, ctx, f, true)
			probeLifecycleScopeRowLock(t, ctx, f.pool, f.orgID, f.sessionID, nil, false)
			for _, id := range f.selected {
				probeLifecycleScopeRowLock(t, ctx, f.pool, f.orgID, f.sessionID, &id, false)
			}
		})
	}
}

func TestLegacyThreadSendScopeFullSchemaPostgresRejectsForeignOrMissingLane(t *testing.T) {
	t.Parallel()
	tests := []struct{ name, scope string }{
		{name: "missing exact lane", scope: "missing"},
		{name: "same-tenant other-session lane", scope: "session"},
		{name: "foreign-tenant actual lane", scope: "organization"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newThreadSendScopePostgresFixture(t, false)
			requestedID := uuid.New()
			var otherOrgID, otherSessionID uuid.UUID
			if tt.scope == "organization" {
				otherOrgID, otherSessionID, requestedID = seedRuntimeSeedThread(t, f.pool)
			} else if tt.scope == "session" {
				other := seedLifecycleConsumerSession(t, f.pool, f.orgID)
				otherOrgID, otherSessionID, requestedID = f.orgID, other.ID, *other.PrimaryThreadID
			}
			before := lifecycleProtocolRows(t, context.Background(), f.pool, f.orgID, f.sessionID)
			var otherBefore []byte
			if otherSessionID != uuid.Nil {
				otherBefore = lifecycleProtocolRows(t, context.Background(), f.pool, otherOrgID, otherSessionID)
			}
			ctx, cancel := context.WithTimeout(context.Background(), LifecycleTransactionTimeout)
			defer cancel()
			owner := beginThreadSendScopePostgresUnit(t, ctx, f.pool)
			tx := newThreadSendScopeObservedTx(owner)
			err := RequireLegacyThreadSendScopeInTx(ctx, tx, f.orgID, f.sessionID, requestedID)
			require.ErrorIs(t, err, pgx.ErrNoRows, "an absent exact scoped target must fail without substituting the primary lane or querying a foreign scope")
			require.Equal(t, threadSendScopeTrace{modeReads: 1, parentReads: 1, discoveryReads: 1, steps: []string{"mode", "parent", "discovery"}}, tx.trace(), "a missing scoped target must be rejected before any lane batch is locked")
			require.Equal(t, before, lifecycleProtocolRows(t, ctx, owner, f.orgID, f.sessionID), "scope rejection must leave every raw local row unchanged while the parent lock remains owned")
			probeThreadSendCutoverExclusive(t, ctx, f, false)
			probeLifecycleScopeRowLock(t, ctx, f.pool, f.orgID, f.sessionID, nil, true)
			for _, id := range f.selected {
				probeLifecycleScopeRowLock(t, ctx, f.pool, f.orgID, f.sessionID, &id, false)
			}
			if otherSessionID != uuid.Nil {
				probeLifecycleScopeRowLock(t, ctx, f.pool, otherOrgID, otherSessionID, &requestedID, false)
				require.Equal(t, otherBefore, []byte(lifecycleProtocolRows(t, ctx, f.pool, otherOrgID, otherSessionID)), "the independently known foreign fixture must remain wholly unchanged and physically unlocked")
			}
			requireLifecycleScopeNoSettlement(t, tx.lifecycleScopeConcurrencyTx)
			require.NoError(t, owner.Rollback(ctx), "only the outer owner may release the rejected unit's retained parent and interlock")
			require.Equal(t, before, lifecycleProtocolRows(t, context.Background(), f.pool, f.orgID, f.sessionID), "rollback after exact-target rejection must preserve the complete local graph")
			probeThreadSendCutoverExclusive(t, ctx, f, true)
			probeLifecycleScopeRowLock(t, ctx, f.pool, f.orgID, f.sessionID, nil, false)
		})
	}
}

func TestLegacyThreadSendScopeFullSchemaPostgresRequiresFreshModePerUnit(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		state models.LifecycleCutoverState
	}{
		{name: "normalizing fails the next legacy unit", state: models.LifecycleCutoverStateNormalizing},
		{name: "active fails the next legacy unit", state: models.LifecycleCutoverStateActive},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newThreadSendScopePostgresFixture(t, false)
			before := lifecycleProtocolRows(t, context.Background(), f.pool, f.orgID, f.sessionID)
			firstCtx, cancelFirst := context.WithTimeout(context.Background(), LifecycleTransactionTimeout)
			defer cancelFirst()
			first := beginThreadSendScopePostgresUnit(t, firstCtx, f.pool)
			observed, err := ObserveThreadSendModeInTx(firstCtx, first, f.orgID, f.sessionID)
			require.NoError(t, err, "the first original unit must observe the actual legacy provenance")
			require.Equal(t, ThreadSendModeLegacy, observed, "the retained entry observation is deliberately legacy before the between-unit transition")
			require.NoError(t, RequireLegacyThreadSendScopeInTx(firstCtx, first, f.orgID, f.sessionID, f.targetID), "the first unit must acquire its own fresh legacy guard")
			require.NoError(t, first.Commit(firstCtx), "the owner must settle the first unit before synthetic metadata changes between original units")
			require.Equal(t, before, lifecycleProtocolRows(t, context.Background(), f.pool, f.orgID, f.sessionID), "a completed guard-only legacy unit must persist no row changes")

			// This constructs synthetic stored mode provenance, not a real
			// cutover, normalization, writer-floor or provider acceptance claim.
			seedThreadSendScopePostgresMode(t, f, tt.state)
			afterTransition := lifecycleProtocolRows(t, context.Background(), f.pool, f.orgID, f.sessionID)
			require.NotEqual(t, before, afterTransition, "the second guard must encounter genuinely changed committed provenance, not reuse a mocked return value")
			ctx, cancel := context.WithTimeout(context.Background(), LifecycleTransactionTimeout)
			defer cancel()
			owner := beginThreadSendScopePostgresUnit(t, ctx, f.pool)
			tx := newThreadSendScopeObservedTx(owner)
			err = RequireLegacyThreadSendScopeInTx(ctx, tx, f.orgID, f.sessionID, f.targetID)
			require.ErrorIs(t, err, ErrThreadSendModeChanged, "a previous legacy entry observation must not bypass a fresh normalizing or active per-unit guard")
			require.Equal(t, threadSendScopeTrace{modeReads: 1, steps: []string{"mode"}}, tx.trace(), "a valid nonlegacy mode must fail before parent, discovery or lane locks without falling back to the legacy branch")
			require.Equal(t, ThreadSendModeLegacy, observed, "the prior observation remains only an inert saved value rather than authority for another unit")
			require.Equal(t, afterTransition, lifecycleProtocolRows(t, ctx, owner, f.orgID, f.sessionID), "mode rejection must not mutate current provenance or any retained session or lane row")
			probeThreadSendCutoverExclusive(t, ctx, f, false)
			probeLifecycleScopeRowLock(t, ctx, f.pool, f.orgID, f.sessionID, nil, false)
			for _, id := range f.selected {
				probeLifecycleScopeRowLock(t, ctx, f.pool, f.orgID, f.sessionID, &id, false)
			}
			requireLifecycleScopeNoSettlement(t, tx.lifecycleScopeConcurrencyTx)
			require.NoError(t, owner.Rollback(ctx), "the owner must discard a later original unit after its fresh mode guard rejects")
			require.Equal(t, afterTransition, lifecycleProtocolRows(t, context.Background(), f.pool, f.orgID, f.sessionID), "rollback must leave the complete post-transition provenance unchanged")
			probeThreadSendCutoverExclusive(t, ctx, f, true)
		})
	}
}

type threadSendScopePostgresFixture struct {
	pool                                        *pgxpool.Pool
	orgID, sessionID, targetID, hiddenSiblingID uuid.UUID
	selected                                    []uuid.UUID
}

func newThreadSendScopePostgresFixture(t *testing.T, hidden bool) threadSendScopePostgresFixture {
	t.Helper()
	// Each leaf owns its fully migrated isolated database. Only owner plus one
	// independent probe/reader are held at once; no suspended parent owns a pool.
	pool := testpostgres.NewPool(t, context.Background(), "../../migrations", 3)
	orgID, sessionID, primaryID := seedRuntimeSeedThread(t, pool)
	f := threadSendScopePostgresFixture{pool: pool, orgID: orgID, sessionID: sessionID, selected: []uuid.UUID{primaryID}}
	for i, status := range []models.ThreadStatus{models.ThreadStatusIdle, models.ThreadStatusFailed, models.ThreadStatusAwaitingInput, models.ThreadStatusIdle} {
		lane := models.SessionThread{OrgID: orgID, SessionID: sessionID, AgentType: models.DefaultDefaultAgentType, Label: "Synthetic legacy send scope lane", Status: status}
		require.NoError(t, NewSessionThreadStore(pool).Create(context.Background(), &lane, 10), "create each actual visible lane before the production two-second transaction budget")
		if i == 0 {
			f.targetID = lane.ID
		}
		if i == 3 {
			f.hiddenSiblingID = lane.ID
		} else {
			f.selected = append(f.selected, lane.ID)
		}
	}
	slices.SortFunc(f.selected, func(a, b uuid.UUID) int { return bytes.Compare(a[:], b[:]) })
	at := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	_, err := pool.Exec(context.Background(), `UPDATE session_threads SET archived_at=$3 WHERE org_id=$1 AND id=$2`, orgID, f.hiddenSiblingID, at)
	require.NoError(t, err, "retain an unrelated archived sibling that must remain outside visible discovery and physical locks")
	_, err = pool.Exec(context.Background(), `UPDATE session_threads SET current_turn=7,agent_session_id='synthetic-prior-provider',
	 result_summary='synthetic prior summary',failure_explanation='synthetic prior failure',failure_category='synthetic_category',
	 started_at=$3,completed_at=$3,last_activity_at=$3,cancel_requested_at=$3 WHERE org_id=$1 AND id=$2`, orgID, f.targetID, at)
	require.NoError(t, err, "retain compatibility-sensitive fields so real idle-claim composition compares a complete nontrivial projection")
	if hidden {
		_, err = pool.Exec(context.Background(), `UPDATE session_threads SET archived_at=$3 WHERE org_id=$1 AND id=$2`, orgID, f.targetID, at)
		require.NoError(t, err, "retain the exact requested hidden lane rather than removing it from the raw scope")
		_, err = pool.Exec(context.Background(), `UPDATE sessions SET deleted_at=$3,archived_at=$3 WHERE org_id=$1 AND id=$2`, orgID, sessionID, at)
		require.NoError(t, err, "retain a hidden legacy parent to prove locking is not visibility or admission authority")
	}
	return f
}

func (f threadSendScopePostgresFixture) targetFixture() threadPersistenceFixture {
	return threadPersistenceFixture{orgID: f.orgID, sessionID: f.sessionID, threadID: f.targetID}
}

func beginThreadSendScopePostgresUnit(t *testing.T, ctx context.Context, pool *pgxpool.Pool) pgx.Tx {
	t.Helper()
	tx := beginSessionLifecycleProbe(t, ctx, pool)
	require.NoError(t, ConfigureLifecycleTransactionInTx(ctx, tx), "only the test transaction owner may install the unchanged bounded READ COMMITTED protocol policy")
	return tx
}

func probeThreadSendCutoverExclusive(t *testing.T, ctx context.Context, f threadSendScopePostgresFixture, expected bool) {
	t.Helper()
	probe := beginSessionLifecycleProbe(t, ctx, f.pool)
	key := "session_lifecycle_cutover:" + f.orgID.String() + ":" + f.sessionID.String()
	var actual bool
	require.NoError(t, probe.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock(hashtextextended($1::text,0))`, key).Scan(&actual), "an independent connection must observe the actual transaction-scoped cutover interlock without waiting")
	require.Equal(t, expected, actual, "the shared guard interlock must block exclusive cutover exactly until the owning unit settles")
	require.NoError(t, probe.Rollback(ctx), "release only the independent probe's optional exclusive interlock")
}

func seedThreadSendScopePostgresMode(t *testing.T, f threadSendScopePostgresFixture, state models.LifecycleCutoverState) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), LifecycleTransactionTimeout)
	defer cancel()
	tx := beginThreadSendScopePostgresUnit(t, ctx, f.pool)
	require.NoError(t, tryLockLifecycleCutoverExclusiveInTx(ctx, tx, f.orgID, f.sessionID), "synthetic between-unit provenance writes must take the exact exclusive interlock after all earlier guard locks are released")
	snapshot := activeLifecycleModeSnapshot()
	row := lifecycleCutoverObservation{orgID: f.orgID, sessionID: f.sessionID, version: 9, state: state, legacyInputCutoffID: snapshot.legacyInputCutoffID, minimumWriterVersion: requestLifecycleScopePointer(0)}
	if state == models.LifecycleCutoverStateActive {
		_, err := tx.Exec(ctx, `UPDATE sessions SET lifecycle_version=1 WHERE org_id=$1 AND id=$2`, f.orgID, f.sessionID)
		require.NoError(t, err, "seed the synthetic exact parent's positive retained stamp without claiming real activation")
		_, err = tx.Exec(ctx, `UPDATE session_threads SET lifecycle_version=1 WHERE org_id=$1 AND session_id=$2 AND archived_at IS NULL`, f.orgID, f.sessionID)
		require.NoError(t, err, "seed matching synthetic visible-lane provenance under the same tenant and parent")
		row.normalization, row.activatedAt, row.minimumWriterVersion, row.resumabilityCapability = snapshot.normalization, snapshot.activatedAt, snapshot.minimumWriterVersion, snapshot.resumabilityCapability
	} else {
		row.normalization = incompleteLifecycleNormalization()
	}
	insertLifecycleCutoverPostgresRow(t, ctx, tx, row)
	require.NoError(t, tx.Commit(ctx), "only the fixture owner may commit the synthetic mode transition between original mutation units")
}

type threadSendScopeTrace struct {
	modeReads, parentReads, discoveryReads, laneBatches int
	steps                                               []string
	laneIDs                                             []uuid.UUID
}

type threadSendScopeObservedTx struct {
	*lifecycleScopeConcurrencyTx
	modeReads, parentReads, discoveryReads int
	steps                                  []string
	beforeParent, beforeDiscovery          func()
}

func newThreadSendScopeObservedTx(tx pgx.Tx) *threadSendScopeObservedTx {
	return &threadSendScopeObservedTx{lifecycleScopeConcurrencyTx: &lifecycleScopeConcurrencyTx{Tx: tx}}
}

func (tx *threadSendScopeObservedTx) QueryRow(ctx context.Context, sql string, args ...any) pgx.Row {
	if sql == lifecycleModeObservationQuery {
		tx.modeReads++
		tx.steps = append(tx.steps, "mode")
	}
	if sql == lifecycleSessionScopeLockQuery {
		tx.parentReads++
		tx.steps = append(tx.steps, "parent")
		if tx.beforeParent != nil {
			tx.beforeParent()
		}
	}
	return tx.lifecycleScopeConcurrencyTx.Tx.QueryRow(ctx, sql, args...)
}

func (tx *threadSendScopeObservedTx) Query(ctx context.Context, sql string, args ...any) (pgx.Rows, error) {
	if sql == lifecycleThreadScopeLockQuery {
		tx.steps = append(tx.steps, "lanes")
	}
	if sql == threadSendVisibleLaneKeysQuery {
		tx.discoveryReads++
		tx.steps = append(tx.steps, "discovery")
		if tx.beforeDiscovery != nil {
			tx.beforeDiscovery()
		}
	}
	return tx.lifecycleScopeConcurrencyTx.Query(ctx, sql, args...)
}

func (tx *threadSendScopeObservedTx) trace() threadSendScopeTrace {
	return threadSendScopeTrace{modeReads: tx.modeReads, parentReads: tx.parentReads, discoveryReads: tx.discoveryReads, laneBatches: tx.laneQueries, steps: slices.Clone(tx.steps), laneIDs: slices.Clone(tx.laneIDs)}
}
