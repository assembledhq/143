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
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

// These finite storage races use a real historical running lease, not a poll
// or renewal race. The actual worker retirement gate delegates to the actual
// aware store/Run. Its second pool connection commits a separate fixture writer
// AFTER the real unlocked owner Scan, including an actual no-row result. Every
// application SQL statement, Row.Scan, returned error and settlement is real.
// No sleep, fabricated owner, enlarged budget or provider operation orders it.
type terminalLateScopeRun struct {
	actual                  *fullAssessmentDispatchRunObserver
	recorder                terminalOwnerPointerRun
	late                    func(context.Context) error
	resolution              string
	resolutionCalls         int
	writerError             error
	committed, foreignGraph json.RawMessage
}

func (r *terminalLateScopeRun) Run(ctx context.Context, work func(context.Context, pgx.Tx) error) error {
	return r.actual.Run(ctx, func(unitCtx context.Context, tx pgx.Tx) error {
		return work(unitCtx, &terminalLateScopeTx{terminalOwnerPointerTx: &terminalOwnerPointerTx{Tx: tx, owner: &r.recorder}, owner: r})
	})
}

type terminalLateScopeTx struct {
	*terminalOwnerPointerTx
	owner *terminalLateScopeRun
}

func (tx *terminalLateScopeTx) QueryRow(ctx context.Context, query string, args ...any) pgx.Row {
	row := tx.terminalOwnerPointerTx.QueryRow(ctx, query, args...)
	normalized := strings.ToLower(strings.Join(strings.Fields(query), " "))
	if tx.owner.resolutionCalls != 0 || strings.Contains(normalized, "for update") {
		return row
	}
	resolution := ""
	if strings.Contains(normalized, "from code_review_revision_assessments a where a.org_id=$1") {
		resolution = "full"
	} else if strings.Contains(normalized, "from code_review_revision_assessments a join code_review_session_metadata m") {
		resolution = "recheck"
	}
	if resolution == "" {
		return row
	}
	return &terminalLateScopeResolutionRow{Row: row, owner: tx.owner, ctx: ctx, resolution: resolution}
}

type terminalLateScopeResolutionRow struct {
	pgx.Row
	owner      *terminalLateScopeRun
	ctx        context.Context
	resolution string
}

func (row *terminalLateScopeResolutionRow) Scan(dest ...any) error {
	err := row.Row.Scan(dest...)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	row.owner.resolutionCalls++
	row.owner.resolution = row.resolution
	row.owner.writerError = row.owner.late(row.ctx)
	if row.owner.writerError != nil {
		return row.owner.writerError
	}
	return err
}

// Both owned test files reuse this real Prepare/claim/settlement history. The
// original pending AND original unknown rows are nonempty canaries; existing
// same-session sibling pending/retry/attempting and foreign pending rows remain.
func terminalScopeMatrixFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool) (strandedReviewFixture, strandedReviewFixture, []terminalDispatchOwnershipChild, []terminalDispatchOwnershipChild) {
	t.Helper()
	f, foreign, children, foreignChildren := terminalDispatchOwnershipFixture(t, ctx, pool, "pending")
	var thread uuid.UUID
	require.NoError(t, pool.QueryRow(ctx, `SELECT id FROM session_threads WHERE org_id=$1 AND session_id=$2`, f.job.OrgID, f.job.SessionID).Scan(&thread), "read the genuine original tenant lane before creating retained unknown history")
	unknown := fullAssessmentDispatchChild(t, ctx, pool, f, thread, "unknown")
	children = append(children, terminalDispatchOwnershipChild{id: unknown, metadata: f.job.MetadataID, state: "unknown", attempts: 1, due: true})
	return f, foreign, children, foreignChildren
}

func terminalLateScopeRecheck(t *testing.T, ctx context.Context, pool *pgxpool.Pool, f strandedReviewFixture) strandedReviewFixture {
	t.Helper()
	baseline := f.assessment
	tag, err := pool.Exec(ctx, `UPDATE code_review_revision_assessments SET status='completed',completed_at=statement_timestamp(),publication_state='not_required',
		result_origin='executed',coverage_complete=true,decision='needs_human_review',acceptable=false,risk_reason_details='[]',structured_outcome='{}',rendered_body='Synthetic retained full baseline'
		WHERE org_id=$1 AND id=$2`, f.job.OrgID, baseline)
	require.NoError(t, err, "retain a genuinely completed full baseline before adding the real recheck owner")
	require.Equal(t, int64(1), tag.RowsAffected(), "only the original full assessment may become the recheck baseline")
	f.assessment = uuid.New()
	tag, err = pool.Exec(ctx, `INSERT INTO code_review_revision_assessments(id,org_id,repository_id,repository_full_name,pull_request_id,metadata_id,session_id,policy_id,generation,source_assessment_id,
		base_sha,base_ref,head_sha,input_version,code_digest,contract_digest,intent_digest,visual_digest,request_digest,gate_digest,input_digest,input_manifest,
		review_scope,route_reason,status,result_origin,decision,acceptable,risk_reason_details,structured_outcome,rendered_body,publication_key)
		SELECT $3,org_id,repository_id,repository_full_name,pull_request_id,metadata_id,session_id,policy_id,2,id,
		base_sha,base_ref,head_sha,input_version,code_digest,contract_digest,intent_digest,visual_digest,request_digest,gate_digest,'synthetic-scope-recheck-input',input_manifest,
		'evidence_only','visual_changed','running','evidence_only',decision,acceptable,risk_reason_details,structured_outcome,'Synthetic retained recheck',$4
		FROM code_review_revision_assessments WHERE org_id=$1 AND id=$2`, f.job.OrgID, baseline, f.assessment, "synthetic-scope-recheck:"+f.assessment.String())
	require.NoError(t, err, "create a real composite-FK-valid evidence-only successor instead of inventing its owner projection")
	require.Equal(t, int64(1), tag.RowsAffected(), "the exact retained recheck assessment must genuinely exist")
	payload := fullAssessmentDispatchJSON(t, codeReviewRecheckJob{OrgID: f.job.OrgID, AssessmentID: f.assessment})
	tag, err = pool.Exec(ctx, `UPDATE jobs SET job_type='run_code_review_recheck',payload=$3,dedupe_key=$4 WHERE org_id=$1 AND id=$2`, f.job.OrgID, f.jobID, payload, "code_review_recheck:"+f.assessment.String())
	require.NoError(t, err, "persist the original exact leased job as the real recheck supervisor")
	require.Equal(t, int64(1), tag.RowsAffected(), "the recheck must retain the exact original job and token")
	tag, err = pool.Exec(ctx, `UPDATE code_review_pr_state SET generation=2,current_assessment_id=$3,active_assessment_id=$4 WHERE org_id=$1 AND pull_request_id=$2`, f.job.OrgID, f.job.PullRequestID, baseline, f.assessment)
	require.NoError(t, err, "retain a genuine full baseline reservation and exact active recheck reservation")
	require.Equal(t, int64(1), tag.RowsAffected(), "the original PR must retain its real separate baseline and active successor")
	return f
}

// Remove only actual references to this fixture's A before its genuine DELETE;
// no guard is dropped, disabled or bypassed. Both the early legacy setup and
// late disappearance use this same bounded separate real writer transaction.
func terminalLateScopeRemoveAssessment(ctx context.Context, tx pgx.Tx, f strandedReviewFixture) error {
	for _, statement := range []string{
		`UPDATE code_review_pr_state SET active_assessment_id=CASE WHEN active_assessment_id=$3 THEN NULL ELSE active_assessment_id END,
		current_assessment_id=CASE WHEN current_assessment_id=$3 THEN NULL ELSE current_assessment_id END WHERE org_id=$1 AND pull_request_id=$2
		AND (active_assessment_id=$3 OR current_assessment_id=$3)`,
		`UPDATE code_review_requests SET assessment_id=CASE WHEN assessment_id=$3 THEN NULL ELSE assessment_id END,
		retry_of_assessment_id=CASE WHEN retry_of_assessment_id=$3 THEN NULL ELSE retry_of_assessment_id END WHERE org_id=$1 AND pull_request_id=$2
		AND (assessment_id=$3 OR retry_of_assessment_id=$3)`,
	} {
		if _, err := tx.Exec(ctx, statement, f.job.OrgID, f.job.PullRequestID, f.assessment); err != nil {
			return err
		}
	}
	tag, err := tx.Exec(ctx, `DELETE FROM code_review_revision_assessments WHERE org_id=$1 AND id=$2`, f.job.OrgID, f.assessment)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return errors.New("synthetic scope writer did not remove its exact retained assessment")
	}
	return nil
}

func terminalLateScopeWrite(ctx context.Context, reserved *pgxpool.Conn, mutate func(context.Context, pgx.Tx) error) error {
	writerCtx, cancel := context.WithTimeout(ctx, db.LifecycleTransactionTimeout)
	defer cancel()
	tx, err := reserved.BeginTx(writerCtx, pgx.TxOptions{IsoLevel: pgx.ReadCommitted})
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback(context.Background()) }()
	if err := db.ConfigureLifecycleTransactionInTx(writerCtx, tx); err != nil {
		return err
	}
	if err := mutate(writerCtx, tx); err != nil {
		return err
	}
	return tx.Commit(writerCtx)
}

func TestCodeReviewTerminalLateScopeFencePostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, change               string
		recheck, legacy, leaseMiss bool
	}{
		{name: "L1 actual token rotates after full resolution", change: "token", leaseMiss: true},
		{name: "L2 persisted job metadata changes to real sibling", change: "payload metadata_id"},
		{name: "L2 persisted job PR changes to real foreign PR", change: "payload pull_request_id"},
		{name: "L2 persisted job key changes to real sibling", change: "payload review_output_key"},
		{name: "L3 exact metadata head changes", change: "metadata head_sha"},
		{name: "L3 exact metadata key changes", change: "metadata review_output_key"},
		{name: "L4 retained recheck A head changes", change: "assessment head_sha", recheck: true},
		{name: "L5 retained full A genuinely disappears", change: "assessment disappears"},
		{name: "L6 matching full A appears after genuine no-row resolution", change: "assessment appears", legacy: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel() // Each leaf owns its full-schema DB; root bounds aggregate capacity with -parallel=1.
			ctx := context.Background()
			pool := fullRecoveryPostgresPoolWithMaxConns(t, ctx, 2)
			require.Equal(t, int32(2), pool.Config().MaxConns, "the actual Run and real external writer must fit literally two slots without hidden pool headroom")
			f, foreign, children, foreignChildren := terminalScopeMatrixFixture(t, ctx, pool)
			if tt.recheck {
				f = terminalLateScopeRecheck(t, ctx, pool, f)
			}
			reserved, err := pool.Acquire(ctx)
			require.NoError(t, err, "reserve only the second real connection before the actual retirement unit begins")
			defer reserved.Release()
			var originalAssessment json.RawMessage
			require.NoError(t, reserved.QueryRow(ctx, `SELECT to_jsonb(a) FROM code_review_revision_assessments a WHERE org_id=$1 AND id=$2`, f.job.OrgID, f.assessment).Scan(&originalAssessment), "retain the full real original assessment row for the legal no-A then newly-appearing-A case")
			if tt.legacy {
				require.NoError(t, terminalLateScopeWrite(ctx, reserved, func(writerCtx context.Context, tx pgx.Tx) error {
					return terminalLateScopeRemoveAssessment(writerCtx, tx, f)
				}), "commit genuine original-A absence without altering metadata, results or existing schema guards")
			}
			before, err := terminalContentionSnapshot(ctx, reserved, f)
			require.NoError(t, err, "retain the complete legal original/sibling graph before any late writer")
			foreignBefore, err := terminalContentionSnapshot(ctx, reserved, foreign)
			require.NoError(t, err, "retain the genuine foreign tenant's complete pending/history/wake graph")
			terminalDispatchOwnershipAssertChildren(t, terminalOwnerPointerPositiveDecode(t, before), children)
			terminalDispatchOwnershipAssertChildren(t, terminalOwnerPointerPositiveDecode(t, foreignBefore), foreignChildren)
			var siblingMetadata uuid.UUID
			var siblingKey string
			require.NoError(t, reserved.QueryRow(ctx, `SELECT id,review_output_key FROM code_review_session_metadata WHERE org_id=$1 AND session_id=$2 AND id<>$3`, f.job.OrgID, f.job.SessionID, f.job.MetadataID).Scan(&siblingMetadata, &siblingKey), "retain the actual latest-looking same-session alternative instead of fabricating a replacement owner")
			var job models.Job
			require.NoError(t, reserved.QueryRow(ctx, `SELECT id,org_id,job_type,lock_token FROM jobs WHERE org_id=$1 AND id=$2`, f.job.OrgID, f.jobID).Scan(&job.ID, &job.OrgID, &job.JobType, &job.LockToken), "the actual worker retirement gate must consume the persisted original job/type/token, not a replacement lease")
			require.Equal(t, &f.token, job.LockToken, "the external writer must run only after the original real token has been retained")
			observed := &terminalLateScopeRun{actual: &fullAssessmentDispatchRunObserver{base: f.stores.ThreadSendDependencies.Transactions}}
			newToken := uuid.New()
			observed.late = func(unitCtx context.Context) error {
				if err := terminalLateScopeWrite(unitCtx, reserved, func(writerCtx context.Context, tx pgx.Tx) error {
					var query string
					var args []any
					switch tt.change {
					case "token":
						query, args = `UPDATE jobs SET lock_token=$3 WHERE org_id=$1 AND id=$2`, []any{f.job.OrgID, f.jobID, newToken}
					case "payload metadata_id":
						query, args = `UPDATE jobs SET payload=jsonb_set(payload,'{metadata_id}',to_jsonb($3::text),false) WHERE org_id=$1 AND id=$2`, []any{f.job.OrgID, f.jobID, siblingMetadata.String()}
					case "payload pull_request_id":
						query, args = `UPDATE jobs SET payload=jsonb_set(payload,'{pull_request_id}',to_jsonb($3::text),false) WHERE org_id=$1 AND id=$2`, []any{f.job.OrgID, f.jobID, foreign.job.PullRequestID.String()}
					case "payload review_output_key":
						query, args = `UPDATE jobs SET payload=jsonb_set(payload,'{review_output_key}',to_jsonb($3::text),false) WHERE org_id=$1 AND id=$2`, []any{f.job.OrgID, f.jobID, siblingKey}
					case "metadata head_sha":
						query, args = `UPDATE code_review_session_metadata SET head_sha=$3 WHERE org_id=$1 AND id=$2`, []any{f.job.OrgID, f.job.MetadataID, "synthetic-late-metadata-head"}
					case "metadata review_output_key":
						query, args = `UPDATE code_review_session_metadata SET review_output_key=$3 WHERE org_id=$1 AND id=$2`, []any{f.job.OrgID, f.job.MetadataID, "synthetic-late-metadata-key:" + uuid.NewString()}
					case "assessment head_sha":
						query, args = `UPDATE code_review_revision_assessments SET head_sha=$3 WHERE org_id=$1 AND id=$2`, []any{f.job.OrgID, f.assessment, "synthetic-late-recheck-head"}
					case "assessment disappears":
						return terminalLateScopeRemoveAssessment(writerCtx, tx, f)
					case "assessment appears":
						query, args = `INSERT INTO code_review_revision_assessments SELECT a.* FROM jsonb_populate_record(NULL::code_review_revision_assessments,$3::jsonb) a WHERE a.org_id=$1 AND a.id=$2`, []any{f.job.OrgID, f.assessment, originalAssessment}
					default:
						return errors.New("synthetic late-scope matrix has an unrecognized finite writer")
					}
					tag, err := tx.Exec(writerCtx, query, args...)
					if err != nil {
						return err
					}
					if tag.RowsAffected() != 1 {
						return fmt.Errorf("synthetic late-scope writer changed %d rows instead of exactly its retained row", tag.RowsAffected())
					}
					return nil
				}); err != nil {
					return err
				}
				var err error
				observed.committed, err = terminalContentionSnapshot(unitCtx, reserved, f)
				if err != nil {
					return err
				}
				observed.foreignGraph, err = terminalContentionSnapshot(unitCtx, reserved, foreign)
				return err
			}
			actualStore, err := db.NewJobStoreWithCodeReviewTerminalTransactions(pool, observed)
			require.NoError(t, err, "supply the one actual aware store with the original explicitly decorated bounded Run")
			recorded := &terminalACKStore{actual: actualStore}
			w := New(pool, zerolog.Nop(), "synthetic-direct-retirement-scope", WithCodeReviewTerminalStore(recorded))
			var hooks atomic.Int32
			handlerCtx := jobctx.WithDeadLetterHooks(jobctx.WithJobID(jobctx.WithLockToken(ctx, f.token), f.jobID))
			jobctx.RegisterDeadLetterHook(handlerCtx, func(context.Context, error) { hooks.Add(1) })
			w.deadLetterJob(ctx, handlerCtx, &job, &FatalError{Err: errors.New("Synthetic late owner change")})
			fullAssessmentDispatchAssertRun(t, observed.actual)
			require.Equal(t, 1, observed.resolutionCalls, "the separate real writer must run once only after actual unlocked owner resolution, including true legacy no-row resolution")
			expectedResolution := "full"
			if tt.recheck {
				expectedResolution = "recheck"
			}
			require.Equal(t, expectedResolution, observed.resolution, "the retained recheck tuple and full legacy lookup must use their actual distinct SQL owner-resolution paths")
			require.NoError(t, observed.writerError, "neither writer setup failure nor fake application errors may masquerade as retirement's actual fresh fence")
			require.NotEmpty(t, observed.committed, "the independently committed writer graph must be the rollback baseline")
			require.NotEqual(t, before, observed.committed, "each finite late writer must genuinely change persistent scope before testing retirement")
			require.Equal(t, 1, recorded.calls, "the actual worker must invoke only the configured retirement owner with no alternate fallback")
			if tt.leaseMiss {
				require.NoError(t, recorded.err, "a fresh actual token miss is a safe nonowned result, not a scope or storage failure")
				var storedToken uuid.UUID
				require.NoError(t, reserved.QueryRow(ctx, `SELECT lock_token FROM jobs WHERE org_id=$1 AND id=$2`, f.job.OrgID, f.jobID).Scan(&storedToken), "read the actual successor token independently after the old retained token misses")
				require.Equal(t, newToken, storedToken, "the new real token must remain untouched rather than being retired through the original token")
			} else {
				require.ErrorIs(t, recorded.err, db.ErrCodeReviewTerminalScope, "a freshly changed actual owner must abort rather than switch owner or downgrade to job-only")
			}
			require.Equal(t, db.CodeReviewTerminalOutcome{}, recorded.outcome, "both the safe lease miss and exact scope refusal must expose literal zero provisional or committed target authority")
			require.Equal(t, []string(nil), observed.recorder.mutations, "all late fences must run before any retirement DML, not merely rely on a later partial rollback")
			require.Equal(t, int32(0), hooks.Load(), "the actual worker gate must suppress the registered hook for every late nonowned or refused result")
			after, err := terminalContentionSnapshot(ctx, reserved, f)
			require.NoError(t, err, "independently read the entire original/sibling graph after the real retirement unit settles")
			foreignAfter, err := terminalContentionSnapshot(ctx, reserved, foreign)
			require.NoError(t, err, "independently read every foreign tenant row after the original late-scope refusal")
			require.Equal(t, observed.committed, after, "every complete job/lease, metadata, assessment, pending/unknown attempt, immutable command/wake, scheduler, request, message, stamp and evidence row must equal the external writer's committed baseline")
			require.Equal(t, foreignBefore, observed.foreignGraph, "the actual external writer must leave the complete genuine foreign tenant graph untouched")
			require.Equal(t, foreignBefore, foreignAfter, "retirement may not adopt or mutate any complete foreign owner after the original scope changes")
		})
	}
}
