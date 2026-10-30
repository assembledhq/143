package db

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

type lifecycleLegacyJobResourceRow struct {
	id           uuid.UUID
	payloadBytes int
	payload      json.RawMessage
}

func TestLifecycleConsumerLegacyJobHistoryWorkPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, foreignOrg string
	}{
		{name: "foreign org sorts lower", foreignOrg: "10000000-0000-0000-0000-000000000001"},
		{name: "foreign org sorts higher", foreignOrg: "f0000000-0000-0000-0000-000000000001"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pool := newLifecycleConsumerQueryPool(t)
			runLifecycleConsumerLegacyJobHistoryWork(t, pool, uuid.MustParse(tt.foreignOrg))
		})
	}
}

func runLifecycleConsumerLegacyJobHistoryWork(t *testing.T, pool *pgxpool.Pool, foreignOrg uuid.UUID) {
	t.Helper()
	verifyLifecycleLegacyJobResourceIndex(t, pool)
	org := uuid.MustParse("80000000-0000-0000-0000-000000000001")
	predicateExec(t, pool, "INSERT INTO organizations(id,name,settings) VALUES($1,'Synthetic main legacy scope','{}'),($2,'Synthetic foreign legacy scope','{}')", org, foreignOrg)
	main := seedLifecycleConsumerSession(t, pool, org)
	f := lifecyclePredicateFixture{org: org, session: main.ID, lane: *main.PrimaryThreadID, fence: AdmissionFence{sessionID: main.ID, threadID: *main.PrimaryThreadID, threaded: true}}
	other := seedLifecycleConsumerSession(t, pool, f.org)
	otherFixture := lifecyclePredicateFixture{org: f.org, session: other.ID, lane: *other.PrimaryThreadID, fence: AdmissionFence{sessionID: other.ID, threadID: *other.PrimaryThreadID, threaded: true}}
	types := []string{"continue_session", "continue_session", "continue_session", "run_agent", "run_agent"}
	statuses := []models.JobStatus{models.JobStatusPending, models.JobStatusRunning, models.JobStatusAwaitingStartControl, models.JobStatusPending, models.JobStatusRunning}
	payload := json.RawMessage(fmt.Sprintf(`{"org_id": "%s", "session_id": "%s"}`, f.org, other.ID))
	expected := make([]lifecycleLegacyJobResourceRow, len(types))
	for i := range types {
		id := uuid.MustParse(fmt.Sprintf("f0000000-0000-0000-0000-%012x", i+1))
		predicateExec(t, pool, "INSERT INTO jobs(id,org_id,queue,job_type,payload,status) VALUES($1,$2,'agent',$3,$4,$5)", id, f.org, types[i], payload, statuses[i])
		expected[i] = lifecycleLegacyJobResourceRow{id: id, payloadBytes: len(payload), payload: payload}
	}
	conn, err := pool.Acquire(context.Background())
	require.NoError(t, err, "keep one physical connection for repeated real legacy-reader bindings and prepared-plan checks")
	defer conn.Release()
	before, result := explainLifecycleConsumerEvidence(t, conn, f)
	require.Equal(t, lifecycleConsumerTestResult{}, result, "five valid jobs routed to a different session must not block this empty target")
	require.Equal(t, 7, before.statements, "the complete empty reader must include policy, parent, scope, structured and legacy statements")
	require.Len(t, before.observations, 5, "the resource recorder must include the real legacy jobs query, not only the four scope/structured reads")
	baselineLegacy := lifecycleLegacyJobObservation(t, before)
	require.Equal(t, float64(len(expected)), baselineLegacy.ActualRows, "the baseline must already contain the same five relevant tenant jobs used after history growth")

	const terminalRows, foreignRows = 200000, 20000
	// Autovacuum timing decides whether the visibility map is set. Disable it
	// on this isolated fixture only, then prove both unvacuumed and vacuumed.
	predicateExec(t, pool, "ALTER TABLE jobs SET (autovacuum_enabled=false)")
	// Terminal IDs precede every relevant input job. An ORDER BY id scan of
	// tenant history therefore cannot accidentally stop before the cold tail.
	// These rows are inserted terminal, never eligible. Neither visibility
	// phase measures dead eligible index versions after status/lease churn.
	predicateExec(t, pool, `INSERT INTO jobs(id,org_id,queue,job_type,payload,status)
	 SELECT ('10000000-0000-0000-0000-'||lpad(to_hex(n),12,'0'))::uuid,$1,'agent',
	 CASE WHEN n%2=0 THEN 'run_agent' ELSE 'continue_session' END,
	 jsonb_build_object('org_id',$1::uuid,'session_id',$2::uuid),
	 CASE WHEN n%2=0 THEN 'succeeded' ELSE 'failed' END FROM generate_series(1,$3::integer) n`, f.org, f.session, terminalRows)
	// A global status-only index is insufficient too: unrelated tenants may
	// have active input work, and none of its routes may enter this tenant scan.
	predicateExec(t, pool, `INSERT INTO jobs(id,org_id,queue,job_type,payload,status)
	 SELECT ('20000000-0000-0000-0000-'||lpad(to_hex(n),12,'0'))::uuid,$1,'agent',
	 CASE WHEN n%2=0 THEN 'run_agent' ELSE 'continue_session' END,
	 jsonb_build_object('org_id',$2::uuid,'session_id',$3::uuid),
	 CASE WHEN n%3=0 THEN 'pending' WHEN n%3=1 THEN 'running' ELSE 'awaiting_start_control' END
	 FROM generate_series(1,$4::integer) n`, foreignOrg, f.org, f.session, foreignRows)
	predicateExec(t, pool, "ANALYZE jobs")
	unchanged := snapshotLifecycleLegacyJobResourceGraph(t, pool, f.org, foreignOrg)
	// Phases share this isolated heap intentionally: the second phase changes
	// its visibility map only after the unvacuumed observations have completed.
	phases := []struct {
		name   string
		vacuum bool
	}{
		{name: "unvacuumed"},
		{name: "vacuumed", vacuum: true},
	}
	for _, phase := range phases {
		if phase.vacuum {
			predicateExec(t, pool, "VACUUM (ANALYZE) jobs")
		}
		var allVisible, pages int
		require.NoError(t, pool.QueryRow(context.Background(), "SELECT relallvisible,relpages FROM pg_class WHERE oid='jobs'::regclass").Scan(&allVisible, &pages), "observe the actual isolated heap visibility state before measuring planner work")
		t.Logf("legacy job visibility phase=%s relallvisible=%d relpages=%d", phase.name, allVisible, pages)
		if !phase.vacuum {
			require.Zero(t, allVisible, "the adverse unvacuumed visibility state must actually be exercised")
		} else {
			require.Equal(t, pages, allVisible, "the vacuumed phase must exercise an all-visible map")
		}
		for iteration := range 12 {
			fixture, expectedResult := f, lifecycleConsumerTestResult{}
			if iteration%2 != 0 {
				fixture, expectedResult = otherFixture, lifecycleConsumerTestResult{blocked: true}
			}
			after, actual := explainLifecycleConsumerEvidence(t, conn, fixture)
			require.Equal(t, expectedResult, actual, "repeated same-connection bindings must preserve exact empty/busy semantics without allowing foreign or terminal routes to leak into the census")
			require.Equal(t, before.statements, after.statements, "tenant history growth must not add production reader round trips")
			require.Len(t, after.observations, len(before.observations), "the bound must cover every scope, structured and legacy read on every repetition")
			legacy := lifecycleLegacyJobObservation(t, after)
			t.Logf("legacy job resource: iteration=%d total_statements=%d explained_statements=%d baseline_buffers=%d after_buffers=%d legacy_baseline_buffers=%d legacy_after_buffers=%d legacy_rows=%g", iteration+1, after.statements, len(after.observations), before.sharedBuffers, after.sharedBuffers, lifecyclePlanBuffers(baselineLegacy), lifecyclePlanBuffers(legacy), legacy.ActualRows)
			if iteration == 0 {
				logLifecycleLegacyJobPlan(t, legacy)
			}
			require.LessOrEqual(t, after.sharedBuffers, before.sharedBuffers+64, "the complete reader must not traverse 200,000 same-tenant terminal jobs or 20,000 foreign live jobs")
			require.LessOrEqual(t, lifecyclePlanBuffers(legacy), lifecyclePlanBuffers(baselineLegacy)+32, "legacy lookup buffer work may grow by B-tree traversal, not tenant/global job history")
			requireLifecycleLegacyJobIndexedPlan(t, legacy, len(expected))
		}
		for _, mode := range []string{"force_custom_plan", "force_generic_plan"} {
			// This is a real prepared EXECUTE, not an EXPLAIN of an independently
			// replanned text query. Settings exist only inside this scratch owner
			// transaction and reset on its rollback; production mode is unchanged.
			plan, rows := explainPreparedLifecycleLegacyJobs(t, conn, f, mode)
			require.Equal(t, expected, rows, "custom and generic prepared plans must return exactly the five in-tenant nonterminal input jobs, including parked work")
			t.Logf("legacy job prepared resource: mode=%s buffers=%d rows=%g", mode, lifecyclePlanBuffers(plan), plan.ActualRows)
			require.LessOrEqual(t, lifecyclePlanBuffers(plan), lifecyclePlanBuffers(baselineLegacy)+32, "both dedicated custom and generic plans must preserve the same relative job-history work bound")
			requireLifecycleLegacyJobIndexedPlan(t, plan, len(expected))
		}
		requireLifecycleLegacyJobDefaultMixedWork(t, conn, f, foreignOrg, expected, lifecyclePlanBuffers(baselineLegacy)+32)
	}
	requireLifecycleLegacyJobNegativeControl(t, conn, f, len(expected), lifecyclePlanBuffers(baselineLegacy)+32)
	require.Equal(t, unchanged, snapshotLifecycleLegacyJobResourceGraph(t, pool, f.org, foreignOrg), "real reader and prepared plan observations must leave every synthetic job field unchanged in both tenants")
}

func requireLifecycleLegacyJobNegativeControl(t *testing.T, conn *pgxpool.Conn, f lifecyclePredicateFixture, expectedRows, bound int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), LifecycleTransactionTimeout)
	defer cancel()
	owner, err := conn.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	require.NoError(t, err, "begin one isolated negative-control owner with the original lifecycle budget")
	defer func() { _ = owner.Rollback(context.Background()) }()
	require.NoError(t, ConfigureLifecycleTransactionInTx(ctx, owner), "the missing-index control must retain the production statement and lock budgets")
	_, err = lockLifecycleScopeInTx(ctx, owner, f.org, lifecycleScope{sessionID: f.session, threadIDs: []uuid.UUID{f.lane}})
	require.NoError(t, err, "hold the same exact canonical scope used by the positive resource measurement")
	_, err = owner.Exec(ctx, "DROP INDEX idx_jobs_lifecycle_nonterminal_input_consumers")
	require.NoError(t, err, "remove only this fixture's eligible-tenant index inside the transaction that will be rolled back")
	var present bool
	require.NoError(t, owner.QueryRow(ctx, "SELECT to_regclass('idx_jobs_lifecycle_nonterminal_input_consumers') IS NOT NULL").Scan(&present), "verify the negative-control query really runs without the new index")
	require.False(t, present, "the final CASE-key statement must be measured against an actually missing index")
	var raw json.RawMessage
	require.NoError(t, owner.QueryRow(ctx, "EXPLAIN (ANALYZE,BUFFERS,FORMAT JSON,TIMING OFF) "+lifecycleConsumerLegacyJobsQuery, f.org, defaultLifecycleConsumerLimits().legacyJobs+1, legacyJobRouteMaxBytes).Scan(&raw), "explain the exact final production statement and bound arguments after removing its index")
	var document []struct {
		Plan lifecycleConsumerQueryPlan `json:"Plan"`
	}
	require.NoError(t, json.Unmarshal(raw, &document), "decode actual negative-control work, not an index-name assertion or generic mock")
	require.Len(t, document, 1, "the missing-index observation must contain one real production-query root")
	plan := document[0].Plan
	require.Equal(t, float64(expectedRows), plan.ActualRows, "removing the access path must preserve the final query's exact logical result")
	require.Greater(t, lifecyclePlanBuffers(plan), bound, "the prior relative resource bound must actually fail when the final query loses the eligible-tenant index")
	t.Logf("legacy job final-query negative control: shared_buffers=%d original_bound=%d rows=%g", lifecyclePlanBuffers(plan), bound, plan.ActualRows)
	logLifecycleLegacyJobPlan(t, plan)
	require.NoError(t, owner.Rollback(ctx), "roll back the test-only index removal before any later observation")
	var valid bool
	require.NoError(t, conn.QueryRow(context.Background(), `SELECT indisvalid AND indisready FROM pg_index WHERE indexrelid='idx_jobs_lifecycle_nonterminal_input_consumers'::regclass`).Scan(&valid), "verify rollback restored the actual valid and ready index")
	require.True(t, valid, "the negative control must leave the fixture's complete supporting index floor intact")
}

// Check the real migrated fixture before the positive resource observations;
// the separate transactional negative control restores this same index.
func verifyLifecycleLegacyJobResourceIndex(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	var valid bool
	err := pool.QueryRow(context.Background(), `SELECT i.indisvalid AND i.indisready FROM pg_index i
	 JOIN pg_class c ON c.oid=i.indexrelid WHERE c.relname='idx_jobs_lifecycle_nonterminal_input_consumers'`).Scan(&valid)
	require.NoError(t, err, "the full migrated fixture must contain the actual concurrent legacy input-consumer index")
	require.True(t, valid, "the legacy input-consumer index must be valid and ready before testing real query work")
}

func lifecycleLegacyJobObservation(t *testing.T, work lifecycleConsumerQueryWork) lifecycleConsumerQueryPlan {
	t.Helper()
	var found []lifecycleConsumerQueryPlan
	for _, observation := range work.observations {
		if observation.name == "legacy jobs" {
			found = append(found, observation.plan)
		}
	}
	require.Len(t, found, 1, "the recorder must explain exactly the actual production legacy jobs lookup")
	return found[0]
}

func lifecyclePlanBuffers(plan lifecycleConsumerQueryPlan) int {
	return plan.SharedHitBlocks + plan.SharedReadBlocks
}

func logLifecycleLegacyJobPlan(t *testing.T, plan lifecycleConsumerQueryPlan) {
	t.Helper()
	if plan.RelationName == "jobs" {
		t.Logf("legacy job plan: node=%s index=%s estimated_rows=%g width=%d cost=%g..%g rows=%g loops=%g removed_filter=%g removed_recheck=%g heap_fetches=%g buffers=%d index_condition=%s filter=%s", plan.NodeType, plan.IndexName, plan.PlanRows, plan.PlanWidth, plan.StartupCost, plan.TotalCost, plan.ActualRows, plan.ActualLoops, plan.RowsRemovedByFilter, plan.RowsRemovedByIndexRecheck, plan.HeapFetches, lifecyclePlanBuffers(plan), plan.IndexCondition, plan.Filter)
	}
	for _, child := range plan.Plans {
		logLifecycleLegacyJobPlan(t, child)
	}
}

func requireLifecycleLegacyJobIndexedPlan(t *testing.T, plan lifecycleConsumerQueryPlan, rows int) {
	t.Helper()
	requireLifecycleLegacyPayloadStreamingPlan(t, plan)
	require.Equal(t, float64(rows), plan.ActualRows, "the exact legacy query must return all and only the five eligible jobs")
	var candidates, probes int
	var visit func(lifecycleConsumerQueryPlan)
	visit = func(node lifecycleConsumerQueryPlan) {
		if node.ActualLoops > 0 {
			require.Zero(t, node.RowsRemovedByFilter, "terminal and foreign history must not be visited then discarded by a post-index filter")
			require.Zero(t, node.RowsRemovedByIndexRecheck, "the legacy lookup must not hide a broad history bitmap behind rechecks")
			if node.NodeType == "Bitmap Heap Scan" {
				require.Equal(t, "jobs", node.RelationName, "only the exact eligible jobs heap may use the candidate bitmap path")
				require.Len(t, node.Plans, 1, "the candidate heap may only read the exact eligible-tenant bitmap")
				require.Equal(t, "Bitmap Index Scan", node.Plans[0].NodeType, "the candidate bitmap must come directly from one index rather than combining broad histories")
				require.Equal(t, "idx_jobs_lifecycle_nonterminal_input_consumers", node.Plans[0].IndexName, "a bitmap is acceptable only over the exact eligible-tenant index")
				require.Equal(t, float64(rows), node.Plans[0].ActualRows, "only exact eligible IDs may enter the bitmap")
				require.Equal(t, float64(1), node.ActualLoops, "the exact eligible tenant bitmap must be read once")
				require.Equal(t, float64(rows), node.ActualRows, "the bitmap heap must emit only the exact bounded eligible IDs")
				require.LessOrEqual(t, node.PlanWidth, 16, "eligible selection must stay ID-only")
				candidates++
				return
			}
			if node.RelationName == "jobs" {
				require.NotEqual(t, "Seq Scan", node.NodeType, "the actual job lookup must not sequentially scan terminal or foreign history")
				if node.IndexName == "idx_jobs_lifecycle_nonterminal_input_consumers" {
					candidates++
					require.Contains(t, []string{"Index Scan", "Index Only Scan"}, node.NodeType, "eligible selection must use the exact eligible-tenant index")
					require.LessOrEqual(t, node.PlanWidth, 16, "eligible selection must stay ID-only")
					require.Equal(t, float64(1), node.ActualLoops, "the partial eligible-ID index must be traversed once per tenant lookup")
					require.Equal(t, float64(rows), node.ActualRows, "only the exact bounded eligible IDs may leave the candidate scan")
				} else {
					probes++
					require.Contains(t, []string{"jobs_pkey", "code_review_recheck_jobs_org_id"}, node.IndexName, "payload reads must be exact primary-key or same-tenant unique-ID probes, not another history path")
					require.Equal(t, float64(rows), node.ActualLoops, "payload probes must execute only once per selected eligible ID")
					require.Equal(t, float64(1), node.ActualRows, "each exact ID probe must read just its matching payload row")
				}
			}
		}
		for _, child := range node.Plans {
			visit(child)
		}
	}
	visit(plan)
	require.Equal(t, [2]int{1, 1}, [2]int{candidates, probes}, "the full lookup must contain exactly one ID-only partial scan and one bounded per-ID payload probe shape")
}

func requireLifecycleLegacyPayloadStreamingPlan(t *testing.T, plan lifecycleConsumerQueryPlan) {
	t.Helper()
	if plan.ActualLoops > 0 {
		require.Zero(t, plan.TempReadBlocks, "bounded legacy lookup must not spool payloads to temporary reads")
		require.Zero(t, plan.TempWrittenBlocks, "bounded legacy lookup must not spill payloads to temporary writes")
		if plan.NodeType == "Sort" || plan.NodeType == "Incremental Sort" {
			require.LessOrEqual(t, plan.PlanWidth, 16, "only ID-width ordering is acceptable; a final sort must not carry payloads")
		}
	}
	for _, child := range plan.Plans {
		requireLifecycleLegacyPayloadStreamingPlan(t, child)
	}
}

func TestLifecycleConsumerLegacyUUIDOrderingPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name            string
		input, expected []string
	}{
		{name: "hex nibble and sign boundaries", input: []string{"ffffffff-ffff-ffff-ffff-ffffffffffff", "a0000000-0000-0000-0000-000000000000", "00000010-0000-0000-0000-000000000000", "80000000-0000-0000-0000-000000000000", "00000000-0000-0000-0000-000000000000", "9fffffff-ffff-ffff-ffff-ffffffffffff", "0000000f-ffff-ffff-ffff-ffffffffffff", "7fffffff-ffff-ffff-ffff-ffffffffffff"}, expected: []string{"00000000-0000-0000-0000-000000000000", "0000000f-ffff-ffff-ffff-ffffffffffff", "00000010-0000-0000-0000-000000000000", "7fffffff-ffff-ffff-ffff-ffffffffffff", "80000000-0000-0000-0000-000000000000", "9fffffff-ffff-ffff-ffff-ffffffffffff", "a0000000-0000-0000-0000-000000000000", "ffffffff-ffff-ffff-ffff-ffffffffffff"}},
		{name: "UUID group and byte boundaries", input: []string{"00000000-0010-0000-0000-000000000000", "00000000-0000-0001-0000-000000000000", "00000000-0000-0000-0000-000000000100", "00000000-000f-ffff-ffff-ffffffffffff", "00000000-0000-0000-ffff-ffffffffffff", "00000000-0000-0000-0000-0000000000ff"}, expected: []string{"00000000-0000-0000-0000-0000000000ff", "00000000-0000-0000-0000-000000000100", "00000000-0000-0000-ffff-ffffffffffff", "00000000-0000-0001-0000-000000000000", "00000000-000f-ffff-ffff-ffffffffffff", "00000000-0010-0000-0000-000000000000"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pool := newLifecycleConsumerQueryPool(t)
			input, expected := make([]uuid.UUID, len(tt.input)), make([]uuid.UUID, len(tt.expected))
			for i, value := range tt.input {
				input[i] = uuid.MustParse(value)
			}
			for i, value := range tt.expected {
				expected[i] = uuid.MustParse(value)
			}
			var native, canonical []uuid.UUID
			err := pool.QueryRow(context.Background(), `SELECT array_agg(id ORDER BY id),array_agg(id ORDER BY id::text COLLATE "C") FROM unnest($1::uuid[]) AS value(id)`, input).Scan(&native, &canonical)
			require.NoError(t, err, "compare PostgreSQL's actual UUID and canonical-text C ordering across hexadecimal and group boundaries")
			require.Equal(t, expected, native, "native UUID byte ordering must match the independently listed boundary sequence")
			require.Equal(t, expected, canonical, "canonical fixed-width UUID text under C collation must preserve the identical eligible ID order")
		})
	}
}

func TestLifecycleConsumerLegacyPayloadCapsPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name                          string
		count, payloadBytes, selected int
		nullPayload                   bool
		expected                      lifecycleConsumerTestResult
	}{
		{name: "exact per-row byte limit remains readable", count: 1, payloadBytes: legacyJobRouteMaxBytes, selected: 1},
		{name: "oversized row preserves size and nulls payload", count: 1, payloadBytes: legacyJobRouteMaxBytes + 1, selected: 1, nullPayload: true, expected: lifecycleConsumerTestResult{cause: ErrLifecycleConsumerDiscoveryIncomplete}},
		{name: "aggregate bytes still refuse partial clear", count: 9, payloadBytes: legacyJobRouteMaxBytes - 256, selected: 9, expected: lifecycleConsumerTestResult{cause: ErrLifecycleConsumerDiscoveryIncomplete}},
		{name: "eligible row cap precedes payload probes", count: 514, payloadBytes: 2, selected: 513, expected: lifecycleConsumerTestResult{cause: ErrLifecycleConsumerDiscoveryIncomplete}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pool := newLifecycleConsumerQueryPool(t)
			f := newLifecyclePredicateFixture(t, pool)
			payload := json.RawMessage(`{}`)
			if tt.payloadBytes > len(payload) {
				payload = json.RawMessage(`{"domain": "` + strings.Repeat("x", tt.payloadBytes-len(`{"domain": ""}`)) + `"}`)
			}
			predicateExec(t, pool, `INSERT INTO jobs(id,org_id,queue,job_type,payload,status)
			 SELECT ('30000000-0000-0000-0000-'||lpad(to_hex(n),12,'0'))::uuid,$1,'agent','continue_session',$2,'pending'
			 FROM generate_series(1,$3::integer) n`, f.org, payload, tt.count)
			before := snapshotLifecycleLegacyJobResourceGraph(t, pool, f.org, f.org)
			conn, err := pool.Acquire(context.Background())
			require.NoError(t, err, "hold one scratch connection for real-reader and exact generic payload-cap observations")
			defer conn.Release()
			work, result := explainLifecycleConsumerEvidence(t, conn, f)
			require.Equal(t, tt.expected, result, "ID-only selection must retain the existing per-row, aggregate-byte and cap-plus-one incomplete-discovery semantics")
			legacy := lifecycleLegacyJobObservation(t, work)
			require.Equal(t, float64(tt.selected), legacy.ActualRows, "the actual SQL query must retain exactly its eligible cap-plus-one rows before the owner interprets byte budgets")
			requireLifecycleLegacyPayloadStreamingPlan(t, legacy)
			plan, rows := explainPreparedLifecycleLegacyJobs(t, conn, f, "force_generic_plan")
			requireLifecycleLegacyPayloadStreamingPlan(t, plan)
			expected := make([]lifecycleLegacyJobResourceRow, tt.selected)
			for i := range expected {
				expected[i] = lifecycleLegacyJobResourceRow{id: uuid.MustParse(fmt.Sprintf("30000000-0000-0000-0000-%012x", i+1)), payloadBytes: tt.payloadBytes}
				if !tt.nullPayload {
					expected[i].payload = payload
				}
			}
			require.Equal(t, expected, rows, "the bounded generic query must preserve exact ordered IDs, original payload sizes and oversized NULL payloads without sorting payload tuples")
			require.Equal(t, before, snapshotLifecycleLegacyJobResourceGraph(t, pool, f.org, f.org), "accepting or refusing a payload budget must not mutate any synthetic job field")
		})
	}
}

func explainPreparedLifecycleLegacyJobs(t *testing.T, conn *pgxpool.Conn, f lifecyclePredicateFixture, mode string) (lifecycleConsumerQueryPlan, []lifecycleLegacyJobResourceRow) {
	t.Helper()
	require.Contains(t, []string{"force_custom_plan", "force_generic_plan"}, mode, "dedicated plan modes are fixed test-owned scratch settings")
	ctx, cancel := context.WithTimeout(context.Background(), LifecycleTransactionTimeout)
	defer cancel()
	var originalMode string
	require.NoError(t, conn.QueryRow(ctx, "SHOW plan_cache_mode").Scan(&originalMode), "capture the scratch connection's original planning mode")
	owner, err := conn.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	require.NoError(t, err, "begin a supplied owner transaction for the dedicated prepared-plan proof")
	defer func() { _ = owner.Rollback(context.Background()) }()
	require.NoError(t, ConfigureLifecycleTransactionInTx(ctx, owner), "retain the production two-second owner and statement/lock budgets while testing prepared plans")
	_, err = lockLifecycleScopeInTx(ctx, owner, f.org, lifecycleScope{sessionID: f.session, threadIDs: []uuid.UUID{f.lane}})
	require.NoError(t, err, "hold the canonical parent and requested lane while observing the actual legacy query")
	_, err = owner.Exec(ctx, "SELECT set_config('plan_cache_mode',$1,true)", mode)
	require.NoError(t, err, "force only this scratch transaction's custom or generic plan")
	_, err = owner.Exec(ctx, "PREPARE lifecycle_legacy_job_resource_plan(uuid,integer,integer) AS "+lifecycleConsumerLegacyJobsQuery)
	require.NoError(t, err, "prepare the exact production legacy query with its actual UUID and integer parameter types")
	// UUID.String is canonical typed test data; the SQL statement name and
	// integer values are fixed here. EXECUTE arguments cannot be protocol-bound.
	execute := fmt.Sprintf("EXECUTE lifecycle_legacy_job_resource_plan('%s'::uuid,%d,%d)", f.org.String(), defaultLifecycleConsumerLimits().legacyJobs+1, legacyJobRouteMaxBytes)
	var raw json.RawMessage
	require.NoError(t, owner.QueryRow(ctx, "EXPLAIN (ANALYZE,BUFFERS,FORMAT JSON,TIMING OFF) "+execute).Scan(&raw), "explain EXECUTE of the actual prepared query under the dedicated plan mode")
	var plans []struct {
		Plan lifecycleConsumerQueryPlan `json:"Plan"`
	}
	require.NoError(t, json.Unmarshal(raw, &plans), "decode the dedicated prepared-plan's actual buffer and filtered-row work")
	require.Len(t, plans, 1, "a prepared legacy lookup must produce exactly one root plan")
	rows, err := owner.Query(ctx, execute)
	require.NoError(t, err, "execute the same prepared legacy query to compare its exact tenant-scoped result")
	actual, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (lifecycleLegacyJobResourceRow, error) {
		var result lifecycleLegacyJobResourceRow
		err := row.Scan(&result.id, &result.payloadBytes, &result.payload)
		return result, err
	})
	require.NoError(t, err, "collect every selected ID, payload size and exact payload from the prepared lookup")
	var genericPlans, customPlans int64
	require.NoError(t, owner.QueryRow(ctx, "SELECT generic_plans,custom_plans FROM pg_prepared_statements WHERE name='lifecycle_legacy_job_resource_plan'").Scan(&genericPlans, &customPlans), "verify which actual server prepared plans were used, not only the requested scratch setting")
	expectedPlans := [2]int64{0, 2}
	if mode == "force_generic_plan" {
		expectedPlans = [2]int64{2, 0}
	}
	require.Equal(t, expectedPlans, [2]int64{genericPlans, customPlans}, "EXPLAIN EXECUTE and real EXECUTE must each exercise the requested custom or generic server plan")
	t.Logf("legacy job prepared counters: mode=%s generic_plans=%d custom_plans=%d", mode, genericPlans, customPlans)
	_, err = owner.Exec(ctx, "DEALLOCATE lifecycle_legacy_job_resource_plan")
	require.NoError(t, err, "release the scratch prepared statement without changing any persistent database object")
	require.NoError(t, owner.Rollback(ctx), "only the caller settles the scratch plan transaction")
	var restoredMode string
	require.NoError(t, conn.QueryRow(ctx, "SHOW plan_cache_mode").Scan(&restoredMode), "verify the transaction-local plan setting disappeared after rollback")
	require.Equal(t, originalMode, restoredMode, "dedicated plan checks must not leave a connection or global planner setting behind")
	return plans[0].Plan, actual
}

func snapshotLifecycleLegacyJobResourceGraph(t *testing.T, pool *pgxpool.Pool, org, foreignOrg uuid.UUID) [3]string {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	var snapshot [3]string
	err := pool.QueryRow(ctx, `SELECT count(*)::text,
	 COALESCE(sum(hashtextextended(to_jsonb(j)::text,0)::numeric),0)::text,
	 COALESCE(sum(hashtextextended(to_jsonb(j)::text,17)::numeric),0)::text
	 FROM jobs j WHERE org_id IN ($1,$2)`, org, foreignOrg).Scan(&snapshot[0], &snapshot[1], &snapshot[2])
	require.NoError(t, err, "fingerprint the count and every synthetic job field in both tenants outside the bounded owner transaction")
	return snapshot
}

// requireLifecycleLegacyJobDefaultMixedWork executes the exact production SQL
// through pgx's default cached-statement mode, alternating the target and a
// foreign tenant on one connection, and measures the actual executions with
// PostgreSQL's per-transaction relation block counters.
func requireLifecycleLegacyJobDefaultMixedWork(t *testing.T, conn *pgxpool.Conn, f lifecyclePredicateFixture, foreignOrg uuid.UUID, expected []lifecycleLegacyJobResourceRow, bound int) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), LifecycleTransactionTimeout)
	defer cancel()
	owner, err := conn.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	require.NoError(t, err, "begin the default cached-statement proof in a real owner transaction")
	defer func() { _ = owner.Rollback(context.Background()) }()
	require.NoError(t, ConfigureLifecycleTransactionInTx(ctx, owner), "retain the unchanged production owner and statement budgets during mixed cached bindings")
	_, err = lockLifecycleScopeInTx(ctx, owner, f.org, lifecycleScope{sessionID: f.session, threadIDs: []uuid.UUID{f.lane}})
	require.NoError(t, err, "hold the canonical target scope before measuring actual default cached executions")
	const preparedCounters = "SELECT COALESCE(sum(generic_plans),0)::bigint,COALESCE(sum(custom_plans),0)::bigint FROM pg_prepared_statements WHERE statement=$1"
	var genericBefore, customBefore int64
	require.NoError(t, owner.QueryRow(ctx, preparedCounters, lifecycleConsumerLegacyJobsQuery).Scan(&genericBefore, &customBefore), "capture existing default cached-plan counters before the twelve mixed executions")
	foreignExpected := make([]lifecycleLegacyJobResourceRow, defaultLifecycleConsumerLimits().legacyJobs+1)
	foreignPayload := json.RawMessage(fmt.Sprintf(`{"org_id": "%s", "session_id": "%s"}`, f.org, f.session))
	for i := range foreignExpected {
		foreignExpected[i] = lifecycleLegacyJobResourceRow{id: uuid.MustParse(fmt.Sprintf("20000000-0000-0000-0000-%012x", i+1)), payloadBytes: len(foreignPayload), payload: foreignPayload}
	}
	const counters = `SELECT COALESCE(sum(pg_stat_get_xact_blocks_fetched(c.oid)),0)::bigint,
	 pg_stat_get_xact_numscans('jobs'::regclass) FROM pg_class c
	 WHERE c.oid='jobs'::regclass OR c.oid IN (SELECT indexrelid FROM pg_index WHERE indrelid='jobs'::regclass)`
	for iteration := range 12 {
		org := f.org
		if iteration%2 != 0 {
			org = foreignOrg
		}
		var blocksBefore, seqBefore, blocksAfter, seqAfter int64
		require.NoError(t, owner.QueryRow(ctx, counters).Scan(&blocksBefore, &seqBefore), "record actual relation work immediately before one cached execution")
		rows, err := owner.Query(ctx, lifecycleConsumerLegacyJobsQuery, org, defaultLifecycleConsumerLimits().legacyJobs+1, legacyJobRouteMaxBytes)
		require.NoError(t, err, "execute the exact production statement through pgx's unchanged default cache mode")
		actual, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (lifecycleLegacyJobResourceRow, error) {
			var result lifecycleLegacyJobResourceRow
			err := row.Scan(&result.id, &result.payloadBytes, &result.payload)
			return result, err
		})
		require.NoError(t, err, "collect the exact ordered IDs, original byte sizes and payloads from the cached execution")
		require.NoError(t, owner.QueryRow(ctx, counters).Scan(&blocksAfter, &seqAfter), "record actual relation work immediately after one cached execution")
		t.Logf("legacy job default mixed: iteration=%d tenant=%s rows=%d job_blocks=%d seq_scans=%d", iteration+1, map[bool]string{true: "target", false: "foreign"}[org == f.org], len(actual), blocksAfter-blocksBefore, seqAfter-seqBefore)
		require.Zero(t, seqAfter-seqBefore, "actual default executions must never sequentially scan jobs")
		if org == f.org {
			require.Equal(t, expected, actual, "mixed default bindings must return exactly the five target-tenant jobs after foreign cache training")
			require.LessOrEqual(t, int(blocksAfter-blocksBefore), bound, "actual default target execution must keep the same relative bound after foreign bindings")
		} else {
			require.Equal(t, foreignExpected, actual, "the foreign binding must return its own exact ordered cap-plus-one cohort without changing payload semantics")
		}
	}
	var genericPlans, customPlans int64
	require.NoError(t, owner.QueryRow(ctx, preparedCounters, lifecycleConsumerLegacyJobsQuery).Scan(&genericPlans, &customPlans), "read the real default server prepared-plan counters after all mixed bindings")
	require.Equal(t, int64(12), genericPlans+customPlans-genericBefore-customBefore, "all twelve production-shaped executions must use actual server cached plans")
	t.Logf("legacy job default mixed counters: generic_plans=%d custom_plans=%d", genericPlans, customPlans)
	require.NoError(t, owner.Rollback(ctx), "only the owner settles the observation transaction without modifying synthetic jobs")
}
