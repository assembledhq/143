package db

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

func TestTerminalReviewRecoveryHistoryPlanPostgres(t *testing.T) {
	t.Parallel()
	pool, org, _, _ := newSchedulingPostgres(t)
	ctx := context.Background()
	_, err := pool.Exec(ctx, `
CREATE INDEX idx_code_review_metadata_pull_request_created ON code_review_session_metadata(org_id,pull_request_id,created_at);
CREATE INDEX idx_session_threads_running ON session_threads(org_id,session_id) WHERE status IN ('pending','running','awaiting_input');
CREATE INDEX idx_session_executors_active ON session_executors(org_id,session_id) WHERE status IN ('starting','running','draining');
CREATE INDEX idx_thread_runtimes_active ON thread_runtimes(org_id,session_id) WHERE status IN ('starting','live','paused','draining');
CREATE INDEX idx_jobs_org_active ON jobs(org_id,status) WHERE status IN ('pending','running');
CREATE INDEX idx_recheck_dispatches_active ON code_review_recheck_dispatches(org_id,session_id) WHERE status IN ('pending','running');`)
	require.NoError(t, err, "create the relevant production lookup shapes")
	migration, err := os.ReadFile(filepath.Join("..", "..", "migrations", "000313_terminal_review_executor_lookup.up.sql"))
	require.NoError(t, err, "read the actual concurrent terminal executor migration")
	// Concurrent builds wait on unrelated snapshots across the entire test DB.
	// Keep the exact production keys and predicate while isolating fixture DDL.
	require.Equal(t, 1, strings.Count(string(migration), "CREATE INDEX CONCURRENTLY"), "the migration should declare one concurrent index")
	_, err = pool.Exec(ctx, strings.Replace(string(migration), "CREATE INDEX CONCURRENTLY", "CREATE INDEX", 1))
	require.NoError(t, err, "build the production terminal executor index in the isolated fixture")
	_, err = pool.Exec(ctx, `WITH history AS (
 INSERT INTO jobs(org_id,status,job_type,payload)
 SELECT $1,'succeeded','continue_session','{}' FROM generate_series(1,100000) RETURNING id,org_id
) INSERT INTO session_executors(org_id,session_id,thread_id,job_id,status)
 SELECT org_id,gen_random_uuid(),gen_random_uuid(),id,'completed' FROM history`, org)
	require.NoError(t, err, "seed substantial same-tenant executor and job history")
	_, err = pool.Exec(ctx, `ANALYZE code_review_session_metadata; ANALYZE session_threads; ANALYZE session_executors; ANALYZE jobs; ANALYZE thread_runtimes; ANALYZE code_review_recheck_dispatches;`)
	require.NoError(t, err, "plan against current production-shaped statistics")

	tests := []struct {
		name string
		sql  string
	}{
		{name: "correlated recovery", sql: reconcileTerminalReviewThreadsSQL},
		{name: "previous query with supporting index", sql: strings.Replace(reconcileTerminalReviewThreadsSQL, "\n     OFFSET 0", "", 1)},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pr, session, thread, job := uuid.New(), uuid.New(), uuid.New(), uuid.New()
			_, err := pool.Exec(ctx, `INSERT INTO code_review_session_metadata(org_id,pull_request_id,session_id,status) VALUES($1,$2,$3,'failed');
INSERT INTO session_threads(org_id,session_id,id,status) VALUES($1,$3,$4,'running');
INSERT INTO jobs(id,org_id,status,job_type,payload) VALUES($5,$1,'failed','continue_session',jsonb_build_object('session_id',$3::text));
INSERT INTO session_executors(org_id,session_id,thread_id,job_id,status) VALUES($1,$3,$4,$5,'failed');`, pgx.QueryExecModeSimpleProtocol, org, pr, session, thread, job)
			require.NoError(t, err, "seed one orphan thread belonging to the candidate PR")
			tx, err := pool.Begin(ctx)
			require.NoError(t, err, "begin isolated plan execution")
			defer func() { _ = tx.Rollback(ctx) }()
			var raw json.RawMessage
			require.NoError(t, tx.QueryRow(ctx, "EXPLAIN (ANALYZE,BUFFERS,FORMAT JSON) "+tt.sql, org, pr).Scan(&raw), "execute the exact recovery query inside a rolled-back transaction")
			var plan []struct {
				Plan          terminalRecoveryPlanNode `json:"Plan"`
				ExecutionTime float64                  `json:"Execution Time"`
			}
			require.NoError(t, json.Unmarshal(raw, &plan), "decode the actual PostgreSQL plan")
			require.Len(t, plan, 1, "EXPLAIN should describe one recovery statement")
			var executorVisits float64
			var terminalCondition string
			var visit func(terminalRecoveryPlanNode)
			visit = func(node terminalRecoveryPlanNode) {
				if node.RelationName == "session_executors" {
					executorVisits += (node.ActualRows + node.RowsRemovedByFilter) * node.ActualLoops
					if node.IndexName == "idx_session_executors_terminal_review_lookup" {
						terminalCondition = node.IndexCond
					}
				}
				for _, child := range node.Plans {
					visit(child)
				}
			}
			visit(plan[0].Plan)
			t.Logf("executor visits=%.0f execution_ms=%.3f terminal_index_condition=%s", executorVisits, plan[0].ExecutionTime, terminalCondition)
			require.LessOrEqual(t, executorVisits, float64(2), "recovery should inspect only the candidate executor rather than tenant history")
			require.Contains(t, terminalCondition, "org_id", "terminal proof should use the tenant index key")
			require.Contains(t, terminalCondition, "session_id", "terminal proof should use the candidate session index key")
			require.Contains(t, terminalCondition, "thread_id", "terminal proof should use the candidate thread index key")
		})
	}
}

type terminalRecoveryPlanNode struct {
	RelationName        string                     `json:"Relation Name"`
	IndexName           string                     `json:"Index Name"`
	IndexCond           string                     `json:"Index Cond"`
	ActualRows          float64                    `json:"Actual Rows"`
	RowsRemovedByFilter float64                    `json:"Rows Removed by Filter"`
	ActualLoops         float64                    `json:"Actual Loops"`
	Plans               []terminalRecoveryPlanNode `json:"Plans"`
}

func TestTerminalReviewRecoveryDeadlinePostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		blockQuery bool
		direct     bool
	}{
		{name: "PR advisory lock is bounded"},
		{name: "thread update timeout rolls back assessment and request", blockQuery: true},
		{name: "admission helper timeout rolls back assessment and request", blockQuery: true, direct: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pool, org, repo, pr := newSchedulingPostgres(t)
			ctx := context.Background()
			session, metadata, assessment, thread, job, request := uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New(), uuid.New()
			_, err := pool.Exec(ctx, `INSERT INTO sessions(id,org_id) VALUES($4,$2);
INSERT INTO code_review_session_metadata(id,org_id,pull_request_id,session_id,status) VALUES($1,$2,$3,$4,'failed');
INSERT INTO code_review_revision_assessments(id,org_id,pull_request_id,metadata_id,session_id,review_scope,status) VALUES($5,$2,$3,$1,$4,'full','running');
INSERT INTO code_review_requests(id,org_id,repository_id,pull_request_id,assessment_id,status,source_kind,source_identity,mode,input_hash,target_generation) VALUES($6,$2,$7,$3,$5,'joined','manual','test','review_now','test',1);
INSERT INTO session_threads(org_id,session_id,id,status) VALUES($2,$4,$8,'running');
INSERT INTO jobs(id,org_id,status,job_type,payload) VALUES($9,$2,'failed','continue_session',jsonb_build_object('session_id',$4::text));
INSERT INTO session_executors(org_id,session_id,thread_id,job_id,status) VALUES($2,$4,$8,$9,'failed');`, pgx.QueryExecModeSimpleProtocol, metadata, org, pr, session, assessment, request, repo, thread, job)
			require.NoError(t, err, "seed terminal recovery that must commit or roll back atomically")
			store := NewCodeReviewScheduleStore(pool)
			require.NoError(t, store.WithLockedPR(ctx, org, repo, pr, func(_ pgx.Tx, state *models.CodeReviewPRState) error {
				state.ActiveAssessmentID = &assessment
				state.ActiveSessionID = &session
				state.PendingInput = json.RawMessage(`{"identity":"preserve"}`)
				return nil
			}), "persist the active pointers and replacement intake before contention")
			blocker, err := pool.Begin(ctx)
			require.NoError(t, err, "begin independent blocking transaction")
			defer func() { _ = blocker.Rollback(ctx) }()
			if tt.blockQuery {
				_, err = blocker.Exec(ctx, `SELECT id FROM session_threads WHERE org_id=$1 AND id=$2 FOR UPDATE`, org, thread)
			} else {
				_, err = blocker.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, "code_review_pr:"+org.String()+":"+pr.String())
			}
			require.NoError(t, err, "hold the relevant lock until recovery times out")
			started := time.Now()
			if tt.direct {
				err = store.WithLockedPR(ctx, org, repo, pr, func(tx pgx.Tx, state *models.CodeReviewPRState) error {
					return reconcileTerminalReviews(ctx, tx, org, pr, state)
				})
			} else {
				err = store.ReconcileTerminalReviews(ctx, org, repo, pr)
			}
			require.True(t, errors.Is(err, context.DeadlineExceeded), "recovery must preserve the underlying deadline while contention persists: %v", err)
			require.ErrorIs(t, err, ErrCodeReviewTerminalRecoveryTimeout, "only the recovery deadline should mark a provider-free queue deferral")
			require.Less(t, time.Since(started), codeReviewTerminalRecoveryTimeout+3*time.Second, "a blocked recovery must release its worker promptly")
			require.NoError(t, blocker.Rollback(ctx), "release the external lock after cancellation")
			var assessmentStatus, requestStatus, threadStatus, jobStatus string
			var actualAssessment, actualSession uuid.UUID
			var pending json.RawMessage
			require.NoError(t, pool.QueryRow(ctx, `SELECT a.status,r.status,t.status,j.status,s.active_assessment_id,s.active_session_id,s.pending_input
 FROM code_review_revision_assessments a JOIN code_review_requests r ON r.org_id=a.org_id AND r.assessment_id=a.id
 JOIN session_threads t ON t.org_id=a.org_id AND t.session_id=a.session_id
 JOIN session_executors e ON e.org_id=t.org_id AND e.thread_id=t.id
 JOIN jobs j ON j.org_id=e.org_id AND j.id=e.job_id
 JOIN code_review_pr_state s ON s.org_id=a.org_id AND s.pull_request_id=a.pull_request_id
 WHERE a.org_id=$1 AND a.id=$2`, org, assessment).Scan(&assessmentStatus, &requestStatus, &threadStatus, &jobStatus, &actualAssessment, &actualSession, &pending), "read committed state after the timed-out transaction rolls back")
			require.Equal(t, []string{"running", "joined", "running", "failed"}, []string{assessmentStatus, requestStatus, threadStatus, jobStatus}, "deadline must preserve all review and job lifecycle state")
			require.Equal(t, []uuid.UUID{assessment, session}, []uuid.UUID{actualAssessment, actualSession}, "deadline must preserve active ownership pointers")
			require.JSONEq(t, `{"identity":"preserve"}`, string(pending), "deadline must preserve queued replacement intake")
		})
	}
}
