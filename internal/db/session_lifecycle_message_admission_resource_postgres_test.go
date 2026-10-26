package db

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

type admissionLookupResourcePlan struct {
	NodeType    string                        `json:"Node Type"`
	Relation    string                        `json:"Relation Name"`
	Index       string                        `json:"Index Name"`
	ActualRows  float64                       `json:"Actual Rows"`
	Loops       float64                       `json:"Actual Loops"`
	Removed     float64                       `json:"Rows Removed by Filter"`
	Rechecked   float64                       `json:"Rows Removed by Index Recheck"`
	HeapFetches float64                       `json:"Heap Fetches"`
	Hits        int                           `json:"Shared Hit Blocks"`
	Reads       int                           `json:"Shared Read Blocks"`
	Plans       []admissionLookupResourcePlan `json:"Plans"`
}
type admissionLookupResourceQuery struct {
	name, sql, types, tables string
	repair                   bool
}
type admissionLookupResourceBinding struct {
	name               string
	org, session, lane uuid.UUID
	expectedRepair     []string
	expectedQuestion   bool
}
type admissionLookupResourceObservation struct {
	plan   admissionLookupResourcePlan
	actual []string
	blocks int64
	scans  int64
}

func admissionLookupResourceQueries() []admissionLookupResourceQuery {
	return []admissionLookupResourceQuery{
		{"repair", messageAdmissionRepairLookupQuery, "uuid,uuid,integer", "'pull_request_repair_runs'", true},
		{"question", messageAdmissionPendingAnswerLookupQuery, "uuid,uuid,uuid", "'session_questions','session_human_input_requests','session_threads'", false},
	}
}
func admissionLookupResourceArgs(q admissionLookupResourceQuery, b admissionLookupResourceBinding) []any {
	if q.repair {
		return []any{b.org, b.session, defaultLifecycleConsumerLimits().structuredRows + 1}
	}
	return []any{b.org, b.session, b.lane}
}
func admissionLookupResourceExpected(q admissionLookupResourceQuery, b admissionLookupResourceBinding) []string {
	if q.repair {
		return b.expectedRepair
	}
	return []string{strconv.FormatBool(b.expectedQuestion)}
}
func readAdmissionLookupResourceRows(ctx context.Context, tx pgx.Tx, q admissionLookupResourceQuery, sql string, args ...any) ([]string, error) {
	if !q.repair {
		var pending bool
		if err := tx.QueryRow(ctx, sql, args...).Scan(&pending); err != nil {
			return nil, err
		}
		return []string{strconv.FormatBool(pending)}, nil
	}
	rows, err := tx.Query(ctx, sql, args...)
	if err != nil {
		return nil, err
	}
	out, err := pgx.CollectRows(rows, func(row pgx.CollectableRow) (string, error) {
		var id, pr uuid.UUID
		err := row.Scan(&id, &pr)
		return id.String() + ":" + pr.String(), err
	})
	if out == nil && err == nil {
		out = []string{}
	}
	return out, err
}

func explainAdmissionLookupResource(t *testing.T, conn *pgxpool.Conn, q admissionLookupResourceQuery, b admissionLookupResourceBinding, mode string) admissionLookupResourceObservation {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), LifecycleTransactionTimeout)
	defer cancel()
	tx, err := conn.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	require.NoError(t, err, "begin one bounded scratch observation of the actual admission query")
	defer func() { _ = tx.Rollback(context.Background()) }()
	require.NoError(t, ConfigureLifecycleTransactionInTx(ctx, tx), "retain production owner statement and lock budgets for every planner observation")
	var originalMode string
	require.NoError(t, tx.QueryRow(ctx, "SHOW plan_cache_mode").Scan(&originalMode), "observe the connection mode before scratch-local preparation")
	_, err = tx.Exec(ctx, "SELECT set_config('plan_cache_mode',$1,true)", mode)
	require.NoError(t, err, "change only this scratch transaction to its explicit custom or generic proof mode")
	_, err = tx.Exec(ctx, "PREPARE admission_lookup_resource("+q.types+") AS "+q.sql)
	require.NoError(t, err, "prepare the exact production constant with its actual UUID and integer parameter types")
	prepared := true
	defer func() {
		if prepared {
			// PREPARE is session-scoped even when its owner transaction aborts.
			// Clean the failed diagnostic too so it cannot mask later modes.
			_ = tx.Rollback(context.Background())
			cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cleanupCancel()
			if _, cleanupErr := conn.Exec(cleanupCtx, "DEALLOCATE admission_lookup_resource"); cleanupErr != nil {
				t.Errorf("release failed scratch preparation before later diagnostics: %v", cleanupErr)
			}
		}
	}()
	third := "'" + b.lane.String() + "'::uuid"
	if q.repair {
		third = strconv.Itoa(defaultLifecycleConsumerLimits().structuredRows + 1)
	}
	execute := fmt.Sprintf("EXECUTE admission_lookup_resource('%s'::uuid,'%s'::uuid,%s)", b.org, b.session, third)
	var raw json.RawMessage
	require.NoError(t, tx.QueryRow(ctx, "EXPLAIN (ANALYZE,BUFFERS,FORMAT JSON,TIMING OFF) "+execute).Scan(&raw), "explain real EXECUTE rather than an independently replanned query string")
	var doc []struct {
		Plan admissionLookupResourcePlan `json:"Plan"`
	}
	require.NoError(t, json.Unmarshal(raw, &doc), "decode actual loops filtered rows heap fetches and buffer work")
	require.Len(t, doc, 1, "each exact prepared query must expose one complete plan")
	actual, err := readAdmissionLookupResourceRows(ctx, tx, q, execute)
	require.NoError(t, err, "execute the same prepared statement and collect its exact scoped results")
	var generic, custom int64
	require.NoError(t, tx.QueryRow(ctx, "SELECT generic_plans,custom_plans FROM pg_prepared_statements WHERE name='admission_lookup_resource'").Scan(&generic, &custom), "verify actual server plan counters rather than trust a requested setting")
	expected := [2]int64{0, 2}
	if mode == "force_generic_plan" {
		expected = [2]int64{2, 0}
	}
	require.Equal(t, expected, [2]int64{generic, custom}, "EXPLAIN EXECUTE and real EXECUTE must both use the requested server plan mode")
	_, err = tx.Exec(ctx, "DEALLOCATE admission_lookup_resource")
	require.NoError(t, err, "remove the test-owned prepared statement before connection reuse")
	prepared = false
	require.NoError(t, tx.Rollback(ctx), "only the scratch owner settles its observation transaction")
	var restored string
	require.NoError(t, conn.QueryRow(context.Background(), "SHOW plan_cache_mode").Scan(&restored), "reinspect mode after scratch rollback")
	require.Equal(t, originalMode, restored, "the planner experiment must not leave connection-wide or persistent settings")
	return admissionLookupResourceObservation{plan: doc[0].Plan, actual: actual, blocks: int64(doc[0].Plan.Hits + doc[0].Plan.Reads)}
}

func readAdmissionLookupResourceCounters(ctx context.Context, tx pgx.Tx, q admissionLookupResourceQuery) (int64, int64, error) {
	sql := "SELECT COALESCE(sum(pg_stat_get_xact_blocks_fetched(c.oid)),0)::bigint,(SELECT COALESCE(sum(pg_stat_get_xact_numscans(t.oid)),0)::bigint FROM pg_class t WHERE t.relnamespace='public'::regnamespace AND t.relname IN (" + q.tables + ")) FROM pg_class c WHERE c.oid IN (SELECT oid FROM pg_class WHERE relnamespace='public'::regnamespace AND relname IN (" + q.tables + ")) OR c.oid IN (SELECT indexrelid FROM pg_index WHERE indrelid IN (SELECT oid FROM pg_class WHERE relnamespace='public'::regnamespace AND relname IN (" + q.tables + ")))"
	var blocks, scans int64
	err := tx.QueryRow(ctx, sql).Scan(&blocks, &scans)
	return blocks, scans, err
}

func observeAdmissionLookupDefaultCache(t *testing.T, conn *pgxpool.Conn, q admissionLookupResourceQuery, bindings []admissionLookupResourceBinding) []admissionLookupResourceObservation {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), LifecycleTransactionTimeout)
	defer cancel()
	tx, err := conn.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	require.NoError(t, err, "begin actual default cached-statement observations on one physical connection")
	defer func() { _ = tx.Rollback(context.Background()) }()
	require.NoError(t, ConfigureLifecycleTransactionInTx(ctx, tx), "keep unchanged production owner and statement budgets during mixed cached bindings")
	const counters = "SELECT COALESCE(sum(generic_plans),0)::bigint,COALESCE(sum(custom_plans),0)::bigint FROM pg_prepared_statements WHERE statement=$1"
	var genericBefore, customBefore int64
	require.NoError(t, tx.QueryRow(ctx, counters, q.sql).Scan(&genericBefore, &customBefore), "capture default prepared-plan counters before the real production-shaped calls")
	out := make([]admissionLookupResourceObservation, 0, len(bindings))
	for _, b := range bindings {
		before, scansBefore, err := readAdmissionLookupResourceCounters(ctx, tx, q)
		require.NoError(t, err, "read native relation/index work immediately before the actual cached call")
		actual, err := readAdmissionLookupResourceRows(ctx, tx, q, q.sql, admissionLookupResourceArgs(q, b)...)
		require.NoError(t, err, "execute the unmodified production constant through pgx's actual default cache mode")
		after, scansAfter, err := readAdmissionLookupResourceCounters(ctx, tx, q)
		require.NoError(t, err, "read native relation/index work immediately after the same cached call")
		require.Equal(t, admissionLookupResourceExpected(q, b), actual, "mixed same-connection bindings must retain exact tenant/session scoped IDs and pending-answer result")
		out = append(out, admissionLookupResourceObservation{actual: actual, blocks: after - before, scans: scansAfter - scansBefore})
		t.Logf("admission native query=%s binding=%s rows=%d blocks=%d sequential_scans=%d", q.name, b.name, len(actual), after-before, scansAfter-scansBefore)
	}
	var generic, custom int64
	require.NoError(t, tx.QueryRow(ctx, counters, q.sql).Scan(&generic, &custom), "read actual custom/generic counters after all unchanged default-cache calls")
	require.Equal(t, int64(len(bindings)), generic+custom-genericBefore-customBefore, "every production-shaped call must actually use a server cached statement")
	t.Logf("admission default-cache query=%s generic=%d custom=%d", q.name, generic, custom)
	require.NoError(t, tx.Rollback(ctx), "only the owner settles the default-cache observation without mutating history")
	return out
}

func requireAdmissionLookupResourceWork(t *testing.T, q admissionLookupResourceQuery, ob admissionLookupResourceObservation, baseline int64) {
	t.Helper()
	t.Logf("admission prepared query=%s root_buffers=%d baseline_buffers=%d", q.name, ob.blocks, baseline)
	var visit func(admissionLookupResourcePlan)
	visit = func(p admissionLookupResourcePlan) {
		if p.Loops > 0 {
			t.Logf("admission plan query=%s relation=%s node=%s index=%s rows=%g loops=%g removed=%g rechecked=%g heap_fetches=%g buffers=%d", q.name, p.Relation, p.NodeType, p.Index, p.ActualRows, p.Loops, p.Removed, p.Rechecked, p.HeapFetches, p.Hits+p.Reads)
			if p.Relation == "pull_request_repair_runs" || p.Relation == "session_questions" {
				require.NotEqual(t, "Seq Scan", p.NodeType, "an executed admission history probe must not scan the populated target/foreign history heap")
				require.Zero(t, p.Removed, "inactive answered and other-scope history must not be visited and then filtered")
				require.Zero(t, p.Rechecked, "a broad bitmap must not hide history work behind index rechecks")
				require.LessOrEqual(t, p.HeapFetches, float64(defaultLifecycleConsumerLimits().structuredRows+1), "heap visibility may require bounded candidate fetches but not history traversal")
			}
		}
		for _, child := range p.Plans {
			visit(child)
		}
	}
	visit(ob.plan)
	require.LessOrEqual(t, ob.blocks, baseline+64, "actual query buffers may grow by fixed B-tree traversal not by 200000 target-history or 20000 other-scope rows")
}

//nolint:paralleltest // nested observations share one isolated heap and connection; visibility phases and prepared modes must run serially
func TestMessageAdmissionLookupHistoryResourcePostgres(t *testing.T) {
	t.Parallel()
	tests := []struct{ name, foreign string }{{"foreign tenant UUID lower", "10000000-0000-0000-0000-000000000001"}, {"foreign tenant UUID higher", "f0000000-0000-0000-0000-000000000001"}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pool := newLifecycleScopeSnapshotPool(t)
			org, foreign := uuid.MustParse("80000000-0000-0000-0000-000000000001"), uuid.MustParse(tt.foreign)
			predicateExec(t, pool, "INSERT INTO organizations(id,name) VALUES($1,'Synthetic admission resource'),($2,'Synthetic other admission resource')", org, foreign)
			session := seedLifecycleConsumerSession(t, pool, org)
			other := seedLifecycleConsumerSession(t, pool, org)
			foreignSession := seedLifecycleConsumerSession(t, pool, foreign)
			pr, otherPR, foreignPR := uuid.New(), uuid.New(), uuid.New()
			for _, p := range []struct{ id, org, session uuid.UUID }{{pr, org, session.ID}, {otherPR, org, other.ID}, {foreignPR, foreign, foreignSession.ID}} {
				predicateExec(t, pool, "INSERT INTO pull_requests(id,org_id,session_id,github_pr_number,github_pr_url,github_repo,title,head_sha,base_sha) VALUES($1,$2,$3,9,'https://example.invalid/pr/9','synthetic/resource-'||$1::uuid::text,'Synthetic repair resource','head','base')", p.id, p.org, p.session)
			}
			target := admissionLookupResourceBinding{name: "target", org: org, session: session.ID, lane: *session.PrimaryThreadID, expectedRepair: []string{}}
			for i := 1; i <= 5; i++ {
				id := uuid.MustParse(fmt.Sprintf("f0000000-0000-0000-0000-%012x", i))
				predicateExec(t, pool, "INSERT INTO pull_request_repair_runs(id,org_id,pull_request_id,session_id,action_type,health_version,head_sha,base_sha,active) VALUES($1,$2,$3,$4,'fix_tests',$5,$1::uuid::text,'base',true)", id, org, pr, session.ID, i)
				target.expectedRepair = append(target.expectedRepair, id.String()+":"+pr.String())
			}
			conn, err := pool.Acquire(context.Background())
			require.NoError(t, err, "retain one physical connection across baseline history growth and mixed cached bindings")
			defer conn.Release()
			baselines := map[string]map[string]int64{}
			defaultBaseline := map[string]int64{}
			for _, q := range admissionLookupResourceQueries() {
				baselines[q.name] = map[string]int64{}
				for _, mode := range []string{"force_custom_plan", "force_generic_plan"} {
					ob := explainAdmissionLookupResource(t, conn, q, target, mode)
					require.Equal(t, admissionLookupResourceExpected(q, target), ob.actual, "baseline already contains the exact target cohort later queried after history growth")
					baselines[q.name][mode] = ob.blocks
				}
				defaultBaseline[q.name] = observeAdmissionLookupDefaultCache(t, conn, q, []admissionLookupResourceBinding{target})[0].blocks
			}
			predicateExec(t, pool, "ALTER TABLE pull_request_repair_runs SET (autovacuum_enabled=false)")
			predicateExec(t, pool, "ALTER TABLE session_questions SET (autovacuum_enabled=false)")
			const history, otherRows = 200000, 10000
			predicateExec(t, pool, "INSERT INTO pull_request_repair_runs(id,org_id,pull_request_id,session_id,action_type,health_version,head_sha,base_sha,active) SELECT ('10000000-0000-0000-0000-'||lpad(to_hex(n),12,'0'))::uuid,$1,$2,$3,'fix_tests',1,'old-'||n,'base',false FROM generate_series(1,$4::integer) n", org, pr, session.ID, history)
			predicateExec(t, pool, "INSERT INTO session_questions(org_id,session_id,question_text,status) SELECT $1,$2,'Synthetic answered history '||n,'answered' FROM generate_series(1,$3::integer) n", org, session.ID, history)
			heavy := admissionLookupResourceBinding{name: "same-tenant other session", org: org, session: other.ID, lane: *other.PrimaryThreadID, expectedQuestion: true, expectedRepair: []string{}}
			foreignBinding := admissionLookupResourceBinding{name: "other tenant", org: foreign, session: foreignSession.ID, lane: *foreignSession.PrimaryThreadID, expectedQuestion: true, expectedRepair: []string{}}
			for n, b := range []admissionLookupResourceBinding{heavy, foreignBinding} {
				rowPR := otherPR
				if n == 1 {
					rowPR = foreignPR
				}
				prefix := fmt.Sprintf("%d0000000-0000-0000-0000-", n+2)
				predicateExec(t, pool, "INSERT INTO pull_request_repair_runs(id,org_id,pull_request_id,session_id,action_type,health_version,head_sha,base_sha,active) SELECT ($1||lpad(to_hex(n),12,'0'))::uuid,$2,$3,$4,'fix_tests',n,'live-'||n,'base',true FROM generate_series(1,$5::integer) n", prefix, b.org, rowPR, b.session, otherRows)
				predicateExec(t, pool, "INSERT INTO session_questions(org_id,session_id,question_text,status) SELECT $1,$2,'Synthetic pending other scope '||n,'pending' FROM generate_series(1,$3::integer) n", b.org, b.session, otherRows)
				expected := make([]string, defaultLifecycleConsumerLimits().structuredRows+1)
				for i := range expected {
					expected[i] = uuid.MustParse(fmt.Sprintf("%s%012x", prefix, i+1)).String() + ":" + rowPR.String()
				}
				if n == 0 {
					heavy.expectedRepair = expected
				} else {
					foreignBinding.expectedRepair = expected
				}
			}
			predicateExec(t, pool, "ANALYZE pull_request_repair_runs")
			predicateExec(t, pool, "ANALYZE session_questions")
			unchanged := admissionLookupHistoryFingerprint(t, context.Background(), pool, org, foreign)
			phases := []struct {
				name   string
				vacuum bool
			}{{"unvacuumed", false}, {"vacuumed", true}}
			for _, phase := range phases {
				// These phases and nested observations deliberately share one isolated
				// heap/connection, so they run sequentially; UUID-order fixtures are isolated.
				if phase.vacuum {
					predicateExec(t, pool, "VACUUM (ANALYZE) pull_request_repair_runs")
					predicateExec(t, pool, "VACUUM (ANALYZE) session_questions")
				}
				t.Run(phase.name, func(t *testing.T) {
					for _, table := range []string{"pull_request_repair_runs", "session_questions"} {
						var visible, pages int
						require.NoError(t, pool.QueryRow(context.Background(), "SELECT relallvisible,relpages FROM pg_class WHERE oid=$1::regclass", table).Scan(&visible, &pages), "observe actual visibility map state rather than infer it from VACUUM invocation")
						t.Logf("admission visibility table=%s all_visible=%d pages=%d", table, visible, pages)
						if phase.vacuum && visible != pages {
							t.Errorf("vacuumed fixture must be all-visible: table=%s visible=%d pages=%d", table, visible, pages)
						}
						if !phase.vacuum && visible != 0 {
							t.Errorf("unvacuumed adverse fixture must not be all-visible: table=%s visible=%d", table, visible)
						}
					}
					for _, q := range admissionLookupResourceQueries() {
						t.Run(q.name, func(t *testing.T) {
							modes := []string{"force_custom_plan", "force_generic_plan", "actual_default_cache"}
							for _, mode := range modes {
								t.Run(mode, func(t *testing.T) {
									// A failing mode is isolated to this subtest, so later modes still
									// collect diagnostics instead of being masked by the first red plan.
									if mode == "actual_default_cache" {
										mixed := make([]admissionLookupResourceBinding, 12)
										for i := range mixed {
											mixed[i] = []admissionLookupResourceBinding{target, heavy, foreignBinding}[i%3]
										}
										obs := observeAdmissionLookupDefaultCache(t, conn, q, mixed)
										for i, ob := range obs {
											require.Zero(t, ob.scans, "actual default-cache calls must not sequentially scan admission history")
											if i%3 == 0 {
												require.LessOrEqual(t, ob.blocks, defaultBaseline[q.name]+64, "actual default target work must retain its relative baseline bound after heavy mixed bindings")
											}
										}
										return
									}
									ob := explainAdmissionLookupResource(t, conn, q, target, mode)
									require.Equal(t, admissionLookupResourceExpected(q, target), ob.actual, "prepared history growth must not change exact target result semantics")
									requireAdmissionLookupResourceWork(t, q, ob, baselines[q.name][mode])
								})
							}
						})
					}
				})
			}
			require.Equal(t, unchanged, admissionLookupHistoryFingerprint(t, context.Background(), pool, org, foreign), "planner observations and visibility maintenance must not change any synthetic repair/question row field")
		})
	}
}
