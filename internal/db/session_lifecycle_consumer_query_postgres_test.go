package db

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	testpostgres "github.com/assembledhq/143/internal/testutil/postgres"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

func newLifecycleConsumerQueryPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool := testpostgres.NewPool(t, context.Background(), "../../migrations", 4)
	var version int
	require.NoError(t, pool.QueryRow(context.Background(), "SELECT current_setting('server_version_num')::int").Scan(&version), "verify the disposable full-schema PostgreSQL version")
	require.GreaterOrEqual(t, version, 150000, "consumer query proofs require the complete PostgreSQL 15+ schema")
	return pool
}

func TestLifecycleConsumerOwnLeaseUsesFreshDatabaseClockPostgres(t *testing.T) {
	t.Parallel()
	for _, expired := range []string{"job", "runtime"} {
		t.Run(expired, func(t *testing.T) {
			t.Parallel()
			pool := newLifecycleConsumerQueryPool(t)
			f := newLifecyclePredicateFixture(t, pool)
			seedPredicateOwnConsumer(t, pool, &f, true)
			ctx, cancel := context.WithTimeout(context.Background(), LifecycleTransactionTimeout)
			defer cancel()
			owner, err := pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
			require.NoError(t, err, "begin the caller's transaction before either lease expires")
			defer func() { _ = owner.Rollback(context.Background()) }()
			require.NoError(t, ConfigureLifecycleTransactionInTx(ctx, owner), "the owner configures its normal bounded transaction policy")
			var job uuid.UUID
			require.NoError(t, owner.QueryRow(ctx, "SELECT id FROM jobs WHERE org_id=$1 AND id=$2 FOR NO KEY UPDATE", f.org, f.own.binding.jobID).Scan(&job), "hold the known job before parent and lane locks")
			_, err = lockLifecycleScopeInTx(ctx, owner, f.org, lifecycleScope{sessionID: f.session, threadIDs: []uuid.UUID{f.lane}})
			require.NoError(t, err, "hold the exact parent and lane throughout the database-clock proof")
			var expiry time.Time
			require.NoError(t, owner.QueryRow(ctx, "SELECT clock_timestamp()+interval '500 milliseconds'").Scan(&expiry), "derive the precise short lease expiry from the database clock, not an application sleep estimate")
			if expired == "job" {
				_, err = owner.Exec(ctx, "UPDATE jobs SET lease_expires_at=$3 WHERE org_id=$1 AND id=$2", f.org, job, expiry)
			} else {
				_, err = owner.Exec(ctx, "UPDATE thread_runtimes SET lease_expires_at=$3 WHERE org_id=$1 AND id=$2", f.org, f.own.attempt.runtimeID, expiry)
			}
			require.NoError(t, err, "stage exactly one short lease while its counterpart remains long-lived")
			before, err := proveOwnLifecycleConsumerInTx(ctx, owner, f.org, f.fence, f.own, 512)
			require.NoError(t, err, "the exact allocation must have positive live-lease proof before the database expiry target")
			require.NotNil(t, before.binding, "the pre-expiry observation must be a real successful own-consumer proof")
			require.Equal(t, f.own.binding, *before.binding, "the pre-expiry proof must retain the complete exact input binding")
			require.Equal(t, f.own.attempt.runtimeID, before.runtimeID, "the pre-expiry proof must establish the exact runtime allocation")
			_, err = owner.Exec(ctx, "SELECT pg_sleep(GREATEST(EXTRACT(EPOCH FROM ($1::timestamptz-clock_timestamp())),0))", expiry.Add(25*time.Millisecond))
			require.NoError(t, err, "wait against the exact database-derived expiry within the unchanged statement and transaction budgets")
			var transactionStillBefore, statementAfter, clockAfter bool
			require.NoError(t, owner.QueryRow(ctx, "SELECT now()<$1,statement_timestamp()>$1,clock_timestamp()>$1", expiry).Scan(&transactionStillBefore, &statementAfter, &clockAfter), "corroborate that transaction-start time is stale while the next statement's database clock is past expiry")
			require.Equal(t, []bool{true, true, true}, []bool{transactionStillBefore, statementAfter, clockAfter}, "this regression must distinguish fresh statement time from now(), not merely begin after an already expired lease")
			after, err := proveOwnLifecycleConsumerInTx(ctx, owner, f.org, f.fence, f.own, 512)
			require.ErrorIs(t, err, ErrLifecycleConsumerReconciliation, "a lease that expired inside this still-open transaction must revoke the own-allocation exception")
			require.Equal(t, lifecycleOwnConsumerProof{}, after, "the post-expiry read must return no reusable proof even though now() remains before expiry")
			require.NoError(t, owner.Rollback(ctx), "only the caller settles the clock-proof transaction")
		})
	}
}

func TestLifecycleConsumerScopeArmsDoNotDoubleCountPostgres(t *testing.T) {
	t.Parallel()
	pool := newLifecycleConsumerQueryPool(t)
	f := newLifecyclePredicateFixture(t, pool)
	seedPredicateOwnConsumer(t, pool, &f, true)
	runtime := f.own.attempt.runtimeID
	var container, node string
	var token uuid.UUID
	require.NoError(t, pool.QueryRow(context.Background(), "SELECT container_id,owner_node_id,lease_token FROM thread_runtimes WHERE org_id=$1 AND id=$2", f.org, runtime).Scan(&container, &node, &token), "read the exact retained runtime identity for the matching holder")
	if container == "" {
		container = "synthetic-consumer-container"
		predicateExec(t, pool, "UPDATE thread_runtimes SET container_id=$3 WHERE org_id=$1 AND id=$2", f.org, runtime, container)
	}
	seedPredicateHolder(t, pool, f, "thread_runtime", runtime, token, container, node, "active")
	// Dispatch, runtime and holder each name this session both directly and
	// through the actual lane. A UNION ALL without the exclusion predicates
	// would turn these three source rows into a false overflow at this cap.
	limits := defaultLifecycleConsumerLimits()
	limits.structuredRows = 3
	require.Equal(t, lifecycleConsumerTestResult{}, runLifecycleConsumerPredicate(t, pool, f, limits), "direct and actual-parent arms must retain each exact source/id once, not consume the discovery budget twice")
}

type lifecycleConsumerQueryPlan struct {
	NodeType         string                       `json:"Node Type"`
	RelationName     string                       `json:"Relation Name"`
	IndexName        string                       `json:"Index Name"`
	ActualRows       float64                      `json:"Actual Rows"`
	ActualLoops      float64                      `json:"Actual Loops"`
	SharedHitBlocks  int                          `json:"Shared Hit Blocks"`
	SharedReadBlocks int                          `json:"Shared Read Blocks"`
	Plans            []lifecycleConsumerQueryPlan `json:"Plans"`
}

type lifecycleConsumerQueryObservation struct {
	name string
	plan lifecycleConsumerQueryPlan
}

type lifecycleConsumerQueryWork struct {
	observations  []lifecycleConsumerQueryObservation
	sharedBuffers int
	statements    int
}

// The recorder explains each actual production scope/evidence read with its
// exact arguments before forwarding that same read. Keeping the anchors here
// prevents a bounded final query from hiding an unmeasured history scan while
// the production reader builds its lane, runtime or generation key arrays.
type lifecycleConsumerExplainTx struct {
	pgx.Tx
	t    *testing.T
	work lifecycleConsumerQueryWork
}

func (tx *lifecycleConsumerExplainTx) Query(ctx context.Context, query string, args ...any) (pgx.Rows, error) {
	tx.t.Helper()
	tx.work.statements++
	var name string
	switch query {
	case lifecycleConsumerLaneKeysQuery:
		name = "lane keys"
	case lifecycleConsumerGenerationKeysQuery:
		name = "generation keys"
	case lifecycleConsumerRuntimeKeysQuery:
		name = "runtime keys"
	case lifecycleConsumerEvidenceQuery:
		name = "evidence"
	case lifecycleConsumerGenerationEvidenceQuery:
		name = "generation evidence"
	}
	if name != "" {
		require.NotEmpty(tx.t, args, "each skew-sensitive production query must select its planning mode explicitly")
		require.Equal(tx.t, pgx.QueryExecModeExec, args[0], "the EXPLAIN and production reads must both custom-plan the exact scope values")
		var raw json.RawMessage
		err := tx.Tx.QueryRow(ctx, "EXPLAIN (ANALYZE,BUFFERS,FORMAT JSON,TIMING OFF) "+query, args...).Scan(&raw)
		require.NoError(tx.t, err, "explain every production scope/evidence query without changing planner settings")
		var document []struct {
			Plan lifecycleConsumerQueryPlan `json:"Plan"`
		}
		require.NoError(tx.t, json.Unmarshal(raw, &document), "read actual rows, buffer work and index choices for each production query")
		require.Len(tx.t, document, 1, "PostgreSQL must return one plan for each production query")
		plan := document[0].Plan
		tx.work.observations = append(tx.work.observations, lifecycleConsumerQueryObservation{name: name, plan: plan})
		// A root plan already includes its children's buffer work. Sum roots
		// across statements, rather than counting each plan subtree again.
		tx.work.sharedBuffers += plan.SharedHitBlocks + plan.SharedReadBlocks
	}
	return tx.Tx.Query(ctx, query, args...)
}

func (tx *lifecycleConsumerExplainTx) QueryRow(ctx context.Context, query string, args ...any) pgx.Row {
	tx.work.statements++
	return tx.Tx.QueryRow(ctx, query, args...)
}

func explainLifecycleConsumerEvidence(t *testing.T, conn *pgxpool.Conn, f lifecyclePredicateFixture) (lifecycleConsumerQueryWork, lifecycleConsumerTestResult) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), LifecycleTransactionTimeout)
	defer cancel()
	tx, err := conn.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	require.NoError(t, err, "begin each bounded consumer observation on the same acquired PostgreSQL connection")
	defer func() { _ = tx.Rollback(context.Background()) }()
	require.NoError(t, ConfigureLifecycleTransactionInTx(ctx, tx), "configure the caller's normal transaction budgets before explaining production reads")
	var lanes []uuid.UUID
	if f.fence.threaded {
		lanes = []uuid.UUID{f.fence.threadID}
	}
	_, err = lockLifecycleScopeInTx(ctx, tx, f.org, lifecycleScope{sessionID: f.session, threadIDs: lanes})
	require.NoError(t, err, "hold only the canonical parent and explicitly requested lane before observing consumers")
	recorder := &lifecycleConsumerExplainTx{Tx: tx, t: t}
	blocked, readErr := HasCompetingInputConsumerInTx(ctx, recorder, f.org, f.fence, nil)
	var stillOpen int
	require.NoError(t, tx.QueryRow(ctx, "SELECT 1").Scan(&stillOpen), "scope overflow and negative results must both preserve the caller's transaction")
	require.Equal(t, 1, stillOpen, "the reader must not settle the caller's transaction while gathering query evidence")
	require.NoError(t, tx.Rollback(ctx), "only the caller may settle the explained consumer observation")
	t.Logf("consumer reader work: total_statements=%d explained_scope_evidence_statements=%d scope_evidence_shared_buffers=%d", recorder.work.statements, len(recorder.work.observations), recorder.work.sharedBuffers)
	return recorder.work, lifecycleConsumerResult(blocked, readErr)
}

func requireLifecycleConsumerIndexedHistoryWork(t *testing.T, work lifecycleConsumerQueryWork) {
	t.Helper()
	heavy := map[string]bool{"thread_runtimes": true, "session_executors": true, "thread_inbox_entries": true, "session_sandbox_holders": true, "automation_runs": true}
	var visit func(string, lifecycleConsumerQueryPlan)
	visit = func(statement string, plan lifecycleConsumerQueryPlan) {
		if plan.RelationName != "" && plan.ActualLoops > 0 {
			t.Logf("consumer query work: statement=%s relation=%s node=%s index=%s rows=%g loops=%g shared_buffers=%d", statement, plan.RelationName, plan.NodeType, plan.IndexName, plan.ActualRows, plan.ActualLoops, plan.SharedHitBlocks+plan.SharedReadBlocks)
		}
		if heavy[plan.RelationName] && plan.ActualLoops > 0 {
			require.NotEqual(t, "Seq Scan", plan.NodeType, "every executed scope and evidence probe must avoid sequentially scanning populated sibling history")
			if plan.NodeType == "Index Scan" || plan.NodeType == "Index Only Scan" || plan.NodeType == "Bitmap Index Scan" {
				require.NotEmpty(t, plan.IndexName, "executed indexed probes must expose the actual PostgreSQL index choice")
			}
		}
		for _, child := range plan.Plans {
			visit(statement, child)
		}
	}
	for _, observation := range work.observations {
		if observation.name == "evidence" || observation.name == "generation evidence" {
			require.Zero(t, observation.plan.ActualRows, "each empty target must have no direct or scalar-generation evidence")
		}
		visit(observation.name, observation.plan)
	}
}

func seedLifecycleConsumerQueryTarget(t *testing.T, pool *pgxpool.Pool, org uuid.UUID) (uuid.UUID, uuid.UUID) {
	t.Helper()
	automation, integration, repository, target := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	predicateExec(t, pool, "INSERT INTO automations(id,org_id,name,goal) VALUES($1,$2,'Synthetic query scope','Observe synthetic retained generations')", automation, org)
	predicateExec(t, pool, "INSERT INTO integrations(id,org_id,provider) VALUES($1,$2,'github')", integration, org)
	predicateExec(t, pool, `INSERT INTO repositories(id,org_id,integration_id,github_id,full_name,clone_url,installation_id)
	 VALUES($1,$2,$3,(hashtextextended($1::uuid::text,0)&9223372036854775807),'synthetic/query-scope','https://example.invalid/query-scope.git',1)`, repository, org, integration)
	predicateExec(t, pool, `INSERT INTO automation_targets(id,org_id,automation_id,repository_id,target_kind,target_key)
	 VALUES($1,$2,$3,$4,'github_pull_request','1')`, target, org, automation, repository)
	return automation, target
}

func TestLifecycleConsumerEmptyScopeHistoryWorkPostgres(t *testing.T) {
	t.Parallel()
	pool := newLifecycleConsumerQueryPool(t)
	f := newLifecyclePredicateFixture(t, pool)
	second := seedLifecycleConsumerSession(t, pool, f.org)
	secondFixture := lifecyclePredicateFixture{org: f.org, session: second.ID, lane: *second.PrimaryThreadID, fence: AdmissionFence{sessionID: second.ID, threadID: *second.PrimaryThreadID, threaded: true}}
	other := seedLifecycleConsumerSession(t, pool, f.org)
	heavyFixture := lifecyclePredicateFixture{org: f.org, session: other.ID, lane: *other.PrimaryThreadID, fence: AdmissionFence{sessionID: other.ID, threadID: *other.PrimaryThreadID, threaded: true}}
	// One target has no generation keys; the other retains an exact generation
	// with no runs. This makes the resource proof measure both the four-query
	// base path and a real scalar generation probe.
	automation, target := seedLifecycleConsumerQueryTarget(t, pool, f.org)
	predicateExec(t, pool, "INSERT INTO automation_target_sessions(org_id,target_id,generation,session_id) VALUES($1,$2,1,$3)", f.org, target, second.ID)
	conn, err := pool.Acquire(context.Background())
	require.NoError(t, err, "retain one physical connection across empty and heavily populated session bindings")
	defer conn.Release()
	beforeFirst, result := explainLifecycleConsumerEvidence(t, conn, f)
	require.Equal(t, lifecycleConsumerTestResult{}, result, "the first target must start with no consumer evidence")
	require.Len(t, beforeFirst.observations, 4, "the base path must measure all three key reads and the final evidence query")
	require.Equal(t, 7, beforeFirst.statements, "the empty base path must also count policy, parent and legacy-job reads rather than advertise only explained statements")
	beforeSecond, result := explainLifecycleConsumerEvidence(t, conn, secondFixture)
	require.Equal(t, lifecycleConsumerTestResult{}, result, "a retained generation with no runs must not create consumer evidence")
	require.Len(t, beforeSecond.observations, 5, "the generation path must also measure its exact scalar evidence probe")
	require.Equal(t, 8, beforeSecond.statements, "each retained exact generation pair adds one statement to the entire production reader")
	lane := *other.PrimaryThreadID
	const historyRows = 12000
	predicateExec(t, pool, `INSERT INTO thread_runtimes(org_id,session_id,thread_id,agent_type,owner_node_id,lease_token,status,closed_at)
	 SELECT $1,$2,$3,'claude_code','synthetic-history',gen_random_uuid(),'closed',now() FROM generate_series(1,$4::integer)`, f.org, other.ID, lane, historyRows)
	job := seedLifecycleConsumerJob(t, pool, f.org, models.JobStatusSucceeded)
	node := "synthetic-history-" + uuid.NewString()
	predicateExec(t, pool, "INSERT INTO nodes(id) VALUES($1)", node)
	predicateExec(t, pool, `INSERT INTO session_executors(org_id,session_id,thread_id,job_id,job_type,host_node_id,owner_id,lock_token,status,completed_at,exit_code)
	 SELECT $1,$2,$3,$4,'continue_session',$5,'synthetic-history',gen_random_uuid(),'completed',now(),0 FROM generate_series(1,$6::integer)`, f.org, other.ID, lane, job, node, historyRows)
	predicateExec(t, pool, `INSERT INTO thread_inbox_entries(org_id,session_id,thread_id,sequence_no,entry_type,delivery_state,delivered_at)
	 SELECT $1,$2,$3,n,'user_message','dead_letter',now() FROM generate_series(1,$4::integer) n`, f.org, other.ID, lane, historyRows)
	predicateExec(t, pool, `INSERT INTO session_sandbox_holders(org_id,session_id,container_id,holder_kind,holder_id,owner_node_id,lease_token,status,expires_at)
	 SELECT org_id,session_id,'synthetic-history','thread_runtime',id,owner_node_id,lease_token,'released',now()-interval '1 day'
	 FROM thread_runtimes WHERE org_id=$1 AND session_id=$2`, f.org, other.ID)
	predicateExec(t, pool, `INSERT INTO automation_runs(org_id,automation_id,goal_snapshot,status,session_id,thread_id,job_id,dispatch_state)
	 SELECT $1,$2,'Synthetic history','completed',$3,$4,$5,'done' FROM generate_series(1,$6::integer)`, f.org, automation, other.ID, lane, job, historyRows)
	for _, table := range []string{"session_threads", "automation_target_sessions", "thread_runtimes", "session_executors", "thread_inbox_entries", "session_sandbox_holders", "automation_runs"} {
		// Exact test-owned constants only; normal ANALYZE supplies real fixture
		// statistics without changing planner settings or forcing an index.
		predicateExec(t, pool, "ANALYZE "+table)
	}
	// This is an I/O-work regression bound, not a latency target. Growing five
	// unrelated source histories by 12,000 rows may add B-tree levels, but must
	// not add a table/history scan to any anchor or final evidence read. Repeat
	// beyond PostgreSQL's usual prepared-plan threshold on one connection while
	// alternating two empty bindings with the same heavily populated sibling.
	observations := []struct {
		name     string
		fixture  lifecyclePredicateFixture
		before   lifecycleConsumerQueryWork
		expected lifecycleConsumerTestResult
	}{
		{name: "first empty target", fixture: f, before: beforeFirst},
		{name: "heavy sibling", fixture: heavyFixture, expected: lifecycleConsumerTestResult{cause: ErrLifecycleConsumerDiscoveryIncomplete}},
		{name: "second empty target", fixture: secondFixture, before: beforeSecond},
	}
	for iteration := range 12 {
		observation := observations[iteration%len(observations)]
		after, actual := explainLifecycleConsumerEvidence(t, conn, observation.fixture)
		require.Equal(t, observation.expected, actual, "mixed same-connection reads must preserve empty targets and refuse a partial negative for the heavy sibling")
		t.Logf("consumer mixed binding: iteration=%d scope=%s total_statements=%d explained_statements=%d before_buffers=%d after_buffers=%d", iteration+1, observation.name, after.statements, len(after.observations), observation.before.sharedBuffers, after.sharedBuffers)
		if observation.expected.cause != nil {
			require.Len(t, after.observations, 3, "runtime overflow must stop after all three anchor queries, before any evidence query")
			require.Equal(t, 5, after.statements, "incomplete runtime discovery must stop after policy, parent and three scope reads without further evidence or legacy queries")
			require.Equal(t, float64(defaultLifecycleConsumerLimits().structuredRows+1), after.observations[2].plan.ActualRows, "the runtime anchor must observe the extra row before refusing incomplete discovery")
			continue
		}
		require.Len(t, after.observations, len(observation.before.observations), "each empty binding must measure every production anchor and generation evidence probe on every repetition")
		require.Equal(t, observation.before.statements, after.statements, "mixed bindings must not add production round trips beyond their fixed exact scope")
		require.LessOrEqual(t, after.sharedBuffers, observation.before.sharedBuffers+128, "the sum of all empty-target anchor and evidence buffers may grow by index traversal, not sibling history size")
		requireLifecycleConsumerIndexedHistoryWork(t, after)
	}
}

func TestLifecycleConsumerGenerationProbeOverflowPostgres(t *testing.T) {
	t.Parallel()
	pool := newLifecycleConsumerQueryPool(t)
	f := newLifecyclePredicateFixture(t, pool)
	_, target := seedLifecycleConsumerQueryTarget(t, pool, f.org)
	predicateExec(t, pool, `INSERT INTO automation_target_sessions(org_id,target_id,generation,session_id,status,retired_reason,retired_at)
	 SELECT $1,$2,n,$3,'retired','manual_reset',now() FROM generate_series(1,$4::integer) n`, f.org, target, f.session, lifecycleConsumerGenerationProbeLimit+1)
	conn, err := pool.Acquire(context.Background())
	require.NoError(t, err, "acquire the caller connection for retained-generation overflow")
	defer conn.Release()
	work, actual := explainLifecycleConsumerEvidence(t, conn, f)
	require.Equal(t, lifecycleConsumerTestResult{cause: ErrLifecycleConsumerDiscoveryIncomplete}, actual, "33 retained generation keys must refuse incomplete discovery before returning a partial negative")
	require.Len(t, work.observations, 2, "generation overflow must stop after lane and generation discovery, before runtime or evidence reads")
	require.Equal(t, 4, work.statements, "generation overflow must perform only policy, parent and two anchor reads, not an unqueried-tail negative result")
	require.Equal(t, float64(lifecycleConsumerGenerationProbeLimit+1), work.observations[1].plan.ActualRows, "the exact generation-key query must retain the overflow row rather than truncating to a clear scope")
}

func TestLifecycleConsumerGenerationPairsDoNotCrossProductPostgres(t *testing.T) {
	t.Parallel()
	pool := newLifecycleConsumerQueryPool(t)
	f := newLifecyclePredicateFixture(t, pool)
	other := seedLifecycleConsumerSession(t, pool, f.org)
	automationA, targetA := seedLifecycleConsumerQueryTarget(t, pool, f.org)
	_, targetB := seedLifecycleConsumerQueryTarget(t, pool, f.org)
	predicateExec(t, pool, `INSERT INTO automation_target_sessions(org_id,target_id,generation,session_id,status,retired_reason,retired_at)
	 VALUES($1,$2,1,$3,'retired','manual_reset',now())`, f.org, targetA, other.ID)
	predicateExec(t, pool, `INSERT INTO automation_target_sessions(org_id,target_id,generation,session_id)
	 VALUES($1,$2,2,$4),($1,$3,1,$4)`, f.org, targetA, targetB, f.session)
	job := seedLifecycleConsumerJob(t, pool, f.org, models.JobStatusPending)
	predicateExec(t, pool, `INSERT INTO automation_runs(org_id,automation_id,goal_snapshot,status,target_id,target_generation,job_id)
	 VALUES($1,$2,'Synthetic other generation','running',$3,1,$4)`, f.org, automationA, targetA, job)
	conn, err := pool.Acquire(context.Background())
	require.NoError(t, err, "acquire one caller connection for both exact generation scopes")
	defer conn.Release()
	work, actual := explainLifecycleConsumerEvidence(t, conn, f)
	require.Equal(t, lifecycleConsumerTestResult{}, actual, "target A generation 2 and target B generation 1 must not import target A generation 1 from another session")
	require.Len(t, work.observations, 6, "two exact retained generation pairs must produce two separate scalar probes after the four base reads")
	require.Equal(t, 9, work.statements, "two exact retained generation pairs must add exactly two reads to the seven-statement base reader")
	for _, observation := range work.observations {
		if observation.name == "generation evidence" {
			require.Zero(t, observation.plan.ActualRows, "neither exact pair may admit a run from the Cartesian cross-pair")
		}
	}
	otherFixture := lifecyclePredicateFixture{org: f.org, session: other.ID, lane: *other.PrimaryThreadID, fence: AdmissionFence{sessionID: other.ID, threadID: *other.PrimaryThreadID, threaded: true}}
	_, actual = explainLifecycleConsumerEvidence(t, conn, otherFixture)
	require.Equal(t, lifecycleConsumerTestResult{cause: ErrLifecycleConsumerReconciliation}, actual, "the same retained run must remain discoverable through its actual generation's session")
}

func TestLifecycleConsumerRetainedAutomationLinkPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, direct, lane, generation, legacyRoute string
		foreignRun                                  bool
		expected                                    lifecycleConsumerTestResult
	}{
		{name: "legacy link without alternate anchors still blocks", expected: lifecycleConsumerTestResult{blocked: true}},
		{name: "foreign-tenant run is a missing local link target", foreignRun: true, expected: lifecycleConsumerTestResult{cause: ErrLifecycleConsumerReconciliation}},
		{name: "different direct session with opaque non-input job", direct: "other", expected: lifecycleConsumerTestResult{cause: ErrLifecycleConsumerReconciliation}},
		{name: "different direct session with opaque input job", direct: "other", legacyRoute: "opaque", expected: lifecycleConsumerTestResult{cause: ErrLifecycleConsumerReconciliation}},
		{name: "different direct session with valid other input route", direct: "other", legacyRoute: "other", expected: lifecycleConsumerTestResult{cause: ErrLifecycleConsumerReconciliation}},
		{name: "all alternate anchors name another session", direct: "other", lane: "other", generation: "other", expected: lifecycleConsumerTestResult{cause: ErrLifecycleConsumerReconciliation}},
		{name: "actual thread parent alone contradicts retained link", lane: "other", expected: lifecycleConsumerTestResult{cause: ErrLifecycleConsumerReconciliation}},
		{name: "actual generation alone contradicts retained link", generation: "other", expected: lifecycleConsumerTestResult{cause: ErrLifecycleConsumerReconciliation}},
		{name: "missing generation mapping is retained uncertainty", generation: "missing", expected: lifecycleConsumerTestResult{cause: ErrLifecycleConsumerReconciliation}},
		{name: "partial generation identity is retained uncertainty", generation: "partial", expected: lifecycleConsumerTestResult{cause: ErrLifecycleConsumerReconciliation}},
		{name: "consistent direct thread generation and link count once", direct: "target", lane: "target", generation: "target", expected: lifecycleConsumerTestResult{blocked: true}},
		{name: "actual target lane and retained link count once", direct: "other", lane: "target", generation: "other", expected: lifecycleConsumerTestResult{cause: ErrLifecycleConsumerReconciliation}},
		{name: "actual target generation and retained link count once", direct: "other", lane: "other", generation: "target", expected: lifecycleConsumerTestResult{cause: ErrLifecycleConsumerReconciliation}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pool := newLifecycleConsumerQueryPool(t)
			f := newLifecyclePredicateFixture(t, pool)
			other := seedLifecycleConsumerSession(t, pool, f.org)
			runOrg := f.org
			if tt.foreignRun {
				runOrg, _, _ = seedRuntimeSeedThread(t, pool)
			}
			automation, target := seedLifecycleConsumerQueryTarget(t, pool, runOrg)
			job, run := seedLifecycleConsumerJob(t, pool, runOrg, models.JobStatusPending), uuid.New()
			if tt.legacyRoute != "" {
				payload := json.RawMessage(`{}`)
				if tt.legacyRoute == "other" {
					var err error
					payload, err = json.Marshal(map[string]uuid.UUID{"org_id": f.org, "session_id": other.ID, "thread_id": *other.PrimaryThreadID})
					require.NoError(t, err, "encode the exact unrelated synthetic legacy input route")
				}
				predicateExec(t, pool, "UPDATE jobs SET job_type='continue_session',payload=$3 WHERE org_id=$1 AND id=$2", f.org, job, payload)
			}
			var session, lane, targetID *uuid.UUID
			var generation *int
			if tt.direct == "target" {
				session = &f.session
			} else if tt.direct == "other" {
				session = &other.ID
			}
			if tt.lane == "target" {
				lane = &f.lane
			} else if tt.lane == "other" {
				lane = other.PrimaryThreadID
			}
			if tt.generation != "" {
				targetID = &target
				if tt.generation != "partial" {
					generation = lifecycleConsumerTestPtr(1)
				}
				if tt.generation == "target" || tt.generation == "other" {
					generationSession := f.session
					if tt.generation == "other" {
						generationSession = other.ID
					}
					predicateExec(t, pool, "INSERT INTO automation_target_sessions(org_id,target_id,generation,session_id) VALUES($1,$2,1,$3)", f.org, target, generationSession)
				}
			}
			predicateExec(t, pool, `INSERT INTO automation_runs(id,org_id,automation_id,goal_snapshot,status,session_id,thread_id,job_id,target_id,target_generation)
			 VALUES($1,$2,$3,'Synthetic retained link','running',$4,$5,$6,$7,$8)`, run, runOrg, automation, session, lane, job, targetID, generation)
			// Independent links permit retained multi-session fan-out. Neither
			// a preferred direct route nor a second link retires the first.
			predicateExec(t, pool, "INSERT INTO session_automation_links(org_id,session_id,automation_run_id) VALUES($1,$2,$4),($1,$3,$4)", f.org, f.session, other.ID, run)
			before := snapshotLifecycleConsumerAutomationGraph(t, pool, runOrg, f.org, run)
			limits := defaultLifecycleConsumerLimits()
			limits.structuredRows = 1
			actual := runLifecycleConsumerPredicate(t, pool, f, limits)
			require.Equal(t, tt.expected, actual, "a retained link must independently preserve uncertainty without double-counting source IDs or trusting opaque/unrelated job routes")
			require.Equal(t, before, snapshotLifecycleConsumerAutomationGraph(t, pool, runOrg, f.org, run), "observing retained links and refusing contradictory ownership must leave the complete run, job and multi-session link graph unchanged across both tenants")
		})
	}
}

func snapshotLifecycleConsumerAutomationGraph(t *testing.T, pool *pgxpool.Pool, runOrg, linkOrg, run uuid.UUID) json.RawMessage {
	t.Helper()
	var graph json.RawMessage
	err := pool.QueryRow(context.Background(), `SELECT jsonb_build_object('run',to_jsonb(a),'job',to_jsonb(j),'links',
	 (SELECT COALESCE(jsonb_agg(to_jsonb(l) ORDER BY l.session_id),'[]'::jsonb)
	 FROM session_automation_links l WHERE l.org_id=$3 AND l.automation_run_id=a.id))
	 FROM automation_runs a LEFT JOIN jobs j ON j.org_id=a.org_id AND j.id=a.job_id WHERE a.org_id=$1 AND a.id=$2`, runOrg, run, linkOrg).Scan(&graph)
	require.NoError(t, err, "capture the exact synthetic retained automation graph without changing any rows")
	return graph
}

func TestLifecycleConsumerAllStatusScopeIndexesPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		table   string
		columns []string
	}{
		{"session_threads", []string{"org_id", "session_id"}},
		{"session_input_dispatches", []string{"org_id", "session_id"}},
		{"session_input_dispatches", []string{"org_id", "thread_id", "id"}},
		{"thread_runtimes", []string{"org_id", "session_id"}},
		{"thread_runtimes", []string{"org_id", "thread_id", "id"}},
		{"session_executors", []string{"org_id", "session_id"}},
		{"session_executors", []string{"org_id", "thread_id", "id"}},
		{"thread_inbox_entries", []string{"org_id", "session_id"}},
		{"thread_inbox_entries", []string{"org_id", "thread_id"}},
		{"session_runtime_start_preparations", []string{"org_id", "session_id"}},
		{"session_runtime_start_preparations", []string{"org_id", "thread_id"}},
		{"code_review_recheck_dispatches", []string{"org_id", "session_id"}},
		{"code_review_recheck_dispatches", []string{"org_id", "thread_id"}},
		{"automation_runs", []string{"org_id", "session_id"}},
		{"automation_runs", []string{"org_id", "thread_id", "id"}},
		{"automation_runs", []string{"org_id", "target_id", "target_generation", "id"}},
		{"automation_target_sessions", []string{"org_id", "session_id"}},
		{"session_sandbox_holders", []string{"org_id", "session_id"}},
		{"session_sandbox_holders", []string{"org_id", "holder_kind", "holder_id", "id"}},
		{"session_automation_links", []string{"org_id", "session_id"}},
	}
	for _, tt := range tests {
		t.Run(tt.table+"/"+tt.columns[1], func(t *testing.T) {
			t.Parallel()
			pool := newLifecycleConsumerQueryPool(t)
			var found bool
			err := pool.QueryRow(context.Background(), `SELECT EXISTS(
			 SELECT 1 FROM pg_index i JOIN pg_class c ON c.oid=i.indrelid
			 WHERE c.oid=$1::regclass AND i.indisvalid AND i.indisready
			 AND i.indnkeyatts>=cardinality($2::text[])
			 AND (i.indpred IS NULL OR ($1::text='automation_runs' AND pg_get_expr(i.indpred,i.indrelid)='(session_id IS NOT NULL)'))
			 AND ARRAY(SELECT a.attname::text FROM unnest(i.indkey::smallint[]) WITH ORDINALITY k(attnum,n)
			 JOIN pg_attribute a ON a.attrelid=c.oid AND a.attnum=k.attnum
			 WHERE k.n<=cardinality($2::text[]) ORDER BY k.n)=$2::text[])`, tt.table, tt.columns).Scan(&found)
			require.NoError(t, err, "inspect actual valid, ready, status-independent PostgreSQL index prefixes")
			require.True(t, found, "scope and reverse discovery must retain indexed terminal/uncertain history rather than rely on active-only partial indexes")
		})
	}
}
