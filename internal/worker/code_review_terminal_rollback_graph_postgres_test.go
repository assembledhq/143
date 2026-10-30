package worker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/jobctx"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

type terminalRollbackWrite struct {
	stage string
	rows  int64
}

// This observer delegates all SQL, row consumption, errors, retry decisions and
// settlement unchanged. A stage is complete only after a real successful Exec
// or the complete successful consumption of its actual RETURNING rows. The
// CHECK itself, not this adapter, supplies the genuine PostgreSQL error.
type terminalRollbackRun struct {
	actual                    *fullAssessmentDispatchRunObserver
	calls                     int
	attempted                 []string
	completed                 []terminalRollbackWrite
	callbackError, finalError error
}

func (r *terminalRollbackRun) Run(ctx context.Context, work func(context.Context, pgx.Tx) error) error {
	r.calls++
	r.finalError = r.actual.Run(ctx, func(unitCtx context.Context, tx pgx.Tx) error {
		r.callbackError = work(unitCtx, &terminalRollbackTx{Tx: tx, observed: r})
		return r.callbackError
	})
	return r.finalError
}

type terminalRollbackTx struct {
	pgx.Tx
	observed *terminalRollbackRun
}

func terminalRollbackStage(query string) string {
	query = strings.TrimSpace(query)
	switch {
	case strings.HasPrefix(query, "UPDATE jobs SET status='dead_letter'"):
		return "job"
	case strings.HasPrefix(query, "UPDATE code_review_revision_assessments SET status='failed'"):
		return "assessment"
	case strings.HasPrefix(query, "UPDATE code_review_session_metadata") && strings.Contains(query, "SET status = 'failed'"):
		return "metadata"
	case strings.HasPrefix(query, "WITH pending AS MATERIALIZED (") && strings.Contains(query, "dispatch_resolution='owner_cancelled'"):
		return "pending"
	default:
		return ""
	}
}

func (tx *terminalRollbackTx) Exec(ctx context.Context, query string, args ...any) (pgconn.CommandTag, error) {
	stage := terminalRollbackStage(query)
	if stage != "" {
		tx.observed.attempted = append(tx.observed.attempted, stage)
	}
	tag, err := tx.Tx.Exec(ctx, query, args...)
	if stage != "" && err == nil {
		tx.observed.completed = append(tx.observed.completed, terminalRollbackWrite{stage: stage, rows: tag.RowsAffected()})
	}
	return tag, err
}

func (tx *terminalRollbackTx) Query(ctx context.Context, query string, args ...any) (pgx.Rows, error) {
	stage := terminalRollbackStage(query)
	if stage != "" {
		tx.observed.attempted = append(tx.observed.attempted, stage)
	}
	rows, err := tx.Tx.Query(ctx, query, args...)
	if err != nil || stage == "" {
		return rows, err
	}
	return &terminalRollbackRows{Rows: rows, observed: tx.observed, stage: stage}, nil
}

type terminalRollbackRows struct {
	pgx.Rows
	observed *terminalRollbackRun
	stage    string
	count    int64
	finished bool
}

func (rows *terminalRollbackRows) Next() bool {
	next := rows.Rows.Next()
	if next {
		rows.count++
	} else if !rows.finished {
		rows.finished = true
		if rows.Rows.Err() == nil {
			rows.observed.completed = append(rows.observed.completed, terminalRollbackWrite{stage: rows.stage, rows: rows.count})
		}
	}
	return next
}

// Each leaf owns a full-schema database and literally two pool slots. The
// actual Worker.poll supplies the claim/renewal/handler/dead-letter boundary;
// neither checkout is reserved across poll. These are four real nonretryable
// CHECK rollbacks of retirement, not completion/ended-parent routes, fabricated
// PgErrors, application ACK injection, provider delivery or activation proof.
func TestCodeReviewTerminalWholeGraphCHECKRollbackPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, table, column, rejected, constraint string
		stage                                     int
	}{
		{name: "W1 original leased job rejects dead letter", table: "jobs", column: "status", rejected: "dead_letter", constraint: "synthetic_retirement_job_rollback", stage: 0},
		{name: "W2 eligible original assessment rejects failure", table: "code_review_revision_assessments", column: "status", rejected: "failed", constraint: "synthetic_retirement_assessment_rollback", stage: 1},
		{name: "W3 exact active metadata rejects failure", table: "code_review_session_metadata", column: "status", rejected: "failed", constraint: "synthetic_retirement_metadata_rollback", stage: 2},
		{name: "W4 later original pending cancellation rejects", table: "code_review_agent_results", column: "dispatch_state", rejected: "cancelled", constraint: "synthetic_retirement_pending_rollback", stage: 3},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel() // Isolated databases; root uses -parallel=1 to bound aggregate migration/connection capacity.
			ctx := context.Background()
			pool := fullRecoveryPostgresPoolWithMaxConns(t, ctx, 2)
			require.Equal(t, int32(2), pool.Config().MaxConns, "real claim, normal renewal and actual bounded retirement must fit literally two slots with no held observer")
			f, foreign, children, foreignChildren := terminalDispatchOwnershipFixture(t, ctx, pool, "pending")
			var original uuid.UUID
			for _, child := range children {
				if child.metadata == f.job.MetadataID {
					original = child.id
				}
			}
			require.NotEqual(t, uuid.Nil, original, "the cancellation fault must target an actual Prepare-born original pending intent, not an absent or legacy row")
			var thread uuid.UUID
			require.NoError(t, pool.QueryRow(ctx, `SELECT dispatch_thread_id FROM code_review_agent_results WHERE org_id=$1 AND id=$2`, f.job.OrgID, original).Scan(&thread), "retain the actual original prepared lane for immutable attempted and accepted history")
			for _, state := range []string{"attempting", "unknown", "accepted"} {
				id := fullAssessmentDispatchChild(t, ctx, pool, f, thread, state)
				children = append(children, terminalDispatchOwnershipChild{id: id, metadata: f.job.MetadataID, state: state, attempts: 1, due: state != "accepted"})
			}
			foreignBefore, err := terminalContentionSnapshot(ctx, pool, foreign)
			require.NoError(t, err, "retain all genuine foreign tenant rows and its nonempty prepared intent/wake before the original constraint is installed")
			terminalDispatchOwnershipAssertChildren(t, terminalOwnerPointerPositiveDecode(t, foreignBefore), foreignChildren)
			node := "synthetic-terminal-check:" + uuid.NewString()
			_, err = pool.Exec(ctx, `INSERT INTO nodes(id,mode) VALUES($1,'worker')`, node)
			require.NoError(t, err, "register a real synthetic queue consumer without altering queue or renewal policy")
			tag, err := pool.Exec(ctx, `UPDATE jobs SET status='pending',locked_by_node_id=NULL,run_owner_id=NULL,lock_token=NULL,
				locked_at=NULL,lease_expires_at=NULL,attempts=0,max_attempts=1,priority=100000,target_node_id=$3,
				run_at=statement_timestamp() WHERE org_id=$1 AND id=$2`, f.job.OrgID, f.jobID, node)
			require.NoError(t, err, "make only the actual original controller due for its real first claim without rerendering any frozen payload or wake")
			require.Equal(t, int64(1), tag.RowsAffected(), "the failure proof requires the actual retained original controller to exist before claim")
			target := f.jobID
			switch tt.stage {
			case 1:
				target = f.assessment
			case 2:
				target = f.job.MetadataID
			case 3:
				target = original
			}
			// Identifiers and rejected values come only from the finite table
			// above. UUID literals are generated synthetic fixture identities;
			// the constraint is both tenant- and exact-row-scoped. NOT VALID
			// preserves legal historical rows without weakening existing guards.
			constraintSQL := fmt.Sprintf(`ALTER TABLE %s ADD CONSTRAINT %s CHECK (NOT (org_id='%s'::uuid AND id='%s'::uuid AND %s='%s')) NOT VALID`,
				pgx.Identifier{tt.table}.Sanitize(), pgx.Identifier{tt.constraint}.Sanitize(), f.job.OrgID.String(), target.String(), pgx.Identifier{tt.column}.Sanitize(), tt.rejected)
			_, err = pool.Exec(ctx, constraintSQL)
			require.NoError(t, err, "install precisely one additional actual CHECK in this isolated database, without replacing any schema constraint or injecting a transaction error")
			var validated bool
			require.NoError(t, pool.QueryRow(ctx, `SELECT convalidated FROM pg_constraint WHERE conrelid=$1::regclass AND conname=$2 AND contype='c'`, tt.table, tt.constraint).Scan(&validated), "independently observe the exact installed relation and named CHECK before exercising its real write")
			require.False(t, validated, "the scoped fault must be an actual NOT VALID CHECK that enforces new writes without retrospective validation noise")
			observed := &fullAssessmentDispatchRunObserver{base: f.stores.ThreadSendDependencies.Transactions}
			traced := &terminalRollbackRun{actual: observed}
			actualStore, err := db.NewJobStoreWithCodeReviewTerminalTransactions(pool, traced)
			require.NoError(t, err, "supply the actual aware JobStore with the existing observed bounded Run, not a fabricated outcome or alternate SQL owner")
			recorded := &terminalACKStore{actual: actualStore}
			w := New(pool, zerolog.Nop(), node, WithCodeReviewTerminalStore(recorded))
			var handlers, hooks, targetEffects atomic.Int32
			var claimed json.RawMessage
			var token uuid.UUID
			terminalErr := &FatalError{Err: errors.New("Synthetic fatal whole-graph retirement CHECK probe")}
			w.Register(models.JobTypeRunCodeReview, func(handlerCtx context.Context, _ string, _ json.RawMessage) error {
				handlers.Add(1)
				jobID, present := jobctx.JobIDFromContext(handlerCtx)
				require.True(t, present, "the logical fatal handler must execute inside the actual Worker.poll claim context")
				require.Equal(t, f.jobID, jobID, "only the original full controller may enter the constraint failure probe")
				var found bool
				token, found = jobctx.LockTokenFromContext(handlerCtx)
				require.True(t, found, "the retirement failure must retain an actual newly claimed fencing token")
				require.NotEqual(t, uuid.Nil, token, "a missing synthetic token may not short-circuit before the intended PostgreSQL write")
				claimed, err = terminalContentionSnapshot(handlerCtx, pool, f)
				require.NoError(t, err, "capture every complete original and sibling row after the real claim has persisted lease, attempt and control fields")
				graph := terminalOwnerPointerPositiveDecode(t, claimed)
				terminalDispatchOwnershipAssertChildren(t, graph, children)
				job := terminalOwnerPointerPositiveRow(t, graph, "jobs", f.jobID)
				require.Equal(t, fullAssessmentDispatchJSON(t, "running"), job["status"], "the nonretryable rollback must begin from a genuinely claimed running controller")
				require.Equal(t, fullAssessmentDispatchJSON(t, token), job["lock_token"], "the complete rollback baseline must retain the actual claim's token, not its previous fixture token")
				require.Equal(t, fullAssessmentDispatchJSON(t, 1), job["attempts"], "the real first claim must consume its normal queue attempt before the retirement probe")
				assessment := terminalOwnerPointerPositiveRow(t, graph, "assessments", f.assessment)
				require.Equal(t, fullAssessmentDispatchJSON(t, "running"), assessment["status"], "the targeted original assessment must really be eligible for failure")
				require.Equal(t, fullAssessmentDispatchJSON(t, "not_started"), assessment["publication_state"], "the intended write must not be bypassed by a protected publication-state fence")
				require.Equal(t, json.RawMessage(`null`), assessment["publication_receipt"], "the original assessment must have no receipt that would suppress the eligible failure statement")
				require.Equal(t, json.RawMessage(`null`), assessment["github_review_id"], "no confirmed provider review may make the assessment failure fault vacuous")
				metadata := terminalOwnerPointerPositiveRow(t, graph, "metadata", f.job.MetadataID)
				require.Equal(t, fullAssessmentDispatchJSON(t, "running"), metadata["status"], "the exact original metadata must actually be active before its later failure mutation")
				for _, table := range []string{"requests", "schedule", "findings", "prompts", "messages", "inbox", "stamps"} {
					require.NotEmpty(t, graph.rows[table], "whole rollback must retain genuinely nonempty %s evidence rather than compare an absent graph", table)
				}
				jobctx.RegisterDeadLetterHook(handlerCtx, func(context.Context, error) {
					hooks.Add(1)
					targetEffects.Add(1) // Logical exact-target effect canary; no provider or notification is invoked.
				})
				return terminalErr
			})
			w.poll(ctx) // Actual poll cancels and joins the unchanged normal renewal goroutine before settlement.
			after, err := terminalContentionSnapshot(ctx, pool, f)
			require.NoError(t, err, "independently read all original-tenant rows only after the actual owner has rolled back and poll returned")
			foreignAfter, err := terminalContentionSnapshot(ctx, pool, foreign)
			require.NoError(t, err, "independently read the genuine foreign tenant's complete graph after the scoped failure")
			require.Equal(t, int32(1), handlers.Load(), "one nonretryable CHECK failure must not reexecute the actual logical controller")
			require.Equal(t, 1, recorded.calls, "actual Worker.deadLetterJob must call its explicit retirement store exactly once with no blind fallback")
			require.Equal(t, 1, traced.calls, "the CHECK rejection must run through exactly one actual supplied outer transaction owner")
			fullAssessmentDispatchAssertRun(t, observed)
			require.Error(t, recorded.err, "the transparent store recorder must retain the actual returned storage error rather than rely on a worker log message")
			require.ErrorIs(t, recorded.err, db.ErrCodeReviewTerminalStorage, "the exact PostgreSQL rejection must remain wrapped as retirement storage failure, not scope loss or accepted contention")
			require.ErrorIs(t, recorded.err, traced.callbackError, "the actual callback's storage rejection must survive the unchanged runner and reach the worker adapter")
			require.Equal(t, traced.finalError, recorded.err, "the aware JobStore must propagate the actual bounded runner's returned failure without replacement")
			var pgErr *pgconn.PgError
			require.ErrorAs(t, recorded.err, &pgErr, "the retained wrapped failure must contain the genuine PostgreSQL CHECK error from this exact write")
			require.Equal(t, "23514", pgErr.Code, "the actual intended failure must be CHECK violation, never timeout, cancellation, setup failure or fabricated contention")
			require.Equal(t, tt.constraint, pgErr.ConstraintName, "only the uniquely named exact-tenant/exact-row CHECK may satisfy this rollback leaf")
			require.Equal(t, tt.table, pgErr.TableName, "the actual rejection must originate on the requested retirement graph relation")
			stages := []string{"job", "assessment", "metadata", "pending"}
			require.Equal(t, stages[:tt.stage+1], traced.attempted, "the real statement sequence must reach the intended rejection after precisely its actual earlier graph writes")
			var completed []terminalRollbackWrite
			for _, stage := range stages[:tt.stage] {
				completed = append(completed, terminalRollbackWrite{stage: stage, rows: 1})
			}
			require.Equal(t, completed, traced.completed, "every earlier actual write/RETURNING must succeed and be fully consumed exactly once before the intended later statement fails")
			require.Equal(t, db.CodeReviewTerminalOutcome{}, recorded.outcome, "every real nonretryable storage error must discard the complete provisional owned bit, owner scope and all descriptors")
			require.Equal(t, int32(0), hooks.Load(), "a real registered dead-letter hook may not fire after any rolled-back retirement graph")
			require.Equal(t, int32(0), targetEffects.Load(), "no logical target effect may escape a partial provisional graph or a logged-only failure")
			require.Equal(t, claimed, after, "real rollback must restore every complete original/sibling job and wake, actual claim lease/attempt, metadata/A/publication field, pending/attempted/unknown/accepted intent, request, scheduling reservation, evidence, session/thread/message/inbox/stamp and control row byte-for-byte")
			require.Equal(t, foreignBefore, foreignAfter, "the entire genuine foreign tenant graph, including its nonempty pending intent and original wake, must remain byte-identical")
		})
	}
}
