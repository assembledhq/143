package worker

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

// These five storage compositions complement, rather than multiply, the
// original dispatch-ownership matrix. They exercise the actual aware JobStore
// and supplied Run, not Worker.poll, Send, a provider, postcommit effects or
// production activation. Each parallel leaf has its own fully migrated DB;
// root may cap aggregate test concurrency with -parallel=1. One of exactly two
// connections stays reserved throughout retirement and independent readback.
type terminalStateCompositionCase struct {
	name             string
	legacy           bool
	recheck          bool
	terminalMetadata bool
	settled          string
	unresolved       bool
}

func terminalStateCompositionRemoveOriginalAssessment(t *testing.T, ctx context.Context, pool *pgxpool.Pool, f strandedReviewFixture) {
	t.Helper()
	fullAssessmentDispatchRun(t, ctx, pool, func(unitCtx context.Context, tx pgx.Tx) error {
		// Clear only this fixture's genuine reservation/request references;
		// every FK and trigger remains enabled. Results/evidence still have
		// the original all-NULL assessment attribution of the legacy fixture.
		if _, err := tx.Exec(unitCtx, `UPDATE code_review_pr_state SET active_assessment_id=NULL
			WHERE org_id=$1 AND pull_request_id=$2 AND active_assessment_id=$3`, f.job.OrgID, f.job.PullRequestID, f.assessment); err != nil {
			return err
		}
		if _, err := tx.Exec(unitCtx, `UPDATE code_review_requests SET assessment_id=NULL
			WHERE org_id=$1 AND assessment_id=$2`, f.job.OrgID, f.assessment); err != nil {
			return err
		}
		tag, err := tx.Exec(unitCtx, `DELETE FROM code_review_revision_assessments WHERE org_id=$1 AND id=$2`, f.job.OrgID, f.assessment)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return errors.New("synthetic original assessment was not removed")
		}
		return nil
	})
}

func terminalStateCompositionMakeRecheck(t *testing.T, ctx context.Context, pool *pgxpool.Pool, f strandedReviewFixture) (strandedReviewFixture, models.CodeReviewAssessment) {
	t.Helper()
	// This is historical fixture construction, not retirement or scheduler
	// execution. Use the actual evidence linker while the full A is eligible,
	// then complete that retained baseline before creating the legal successor.
	fullAssessmentDispatchRun(t, ctx, pool, func(unitCtx context.Context, tx pgx.Tx) error {
		if err := db.NewCodeReviewRecheckStore(tx).LockFullAssessmentCompletion(unitCtx, tx, f.job.OrgID, f.job.PullRequestID); err != nil {
			return err
		}
		if err := db.LockCodeReviewMetadataForCompletionInTx(unitCtx, f.job.OrgID, tx, f.job.SessionID); err != nil {
			return err
		}
		return db.NewCodeReviewAssessmentStore(tx).LinkFullReviewEvidence(unitCtx, f.job.OrgID, f.assessment, f.job.SessionID)
	})
	tag, err := pool.Exec(ctx, `UPDATE code_review_revision_assessments SET status='completed',completed_at=statement_timestamp(),
		publication_state='not_required',result_origin='executed',coverage_complete=true,decision='needs_human_review',acceptable=false,
		risk_reason_details='[]',structured_outcome='{}',rendered_body='Synthetic immutable full baseline'
		WHERE org_id=$1 AND id=$2`, f.job.OrgID, f.assessment)
	require.NoError(t, err, "complete the genuine original full baseline before the actual recheck successor becomes active")
	require.Equal(t, int64(1), tag.RowsAffected(), "the recheck composition must retain a real completed baseline, not an absent control")
	baseline, err := f.stores.CodeReviewAssessments.GetByID(ctx, f.job.OrgID, f.assessment)
	require.NoError(t, err, "retain the complete immutable full-baseline descriptor before constructing the exact recheck")
	f.assessment = uuid.New()
	tag, err = pool.Exec(ctx, `INSERT INTO code_review_revision_assessments(id,org_id,repository_id,repository_full_name,pull_request_id,metadata_id,session_id,policy_id,
		generation,source_assessment_id,base_sha,base_ref,head_sha,input_version,code_digest,contract_digest,intent_digest,visual_digest,request_digest,
		gate_digest,input_digest,input_manifest,review_scope,route_reason,status,result_origin,decision,acceptable,risk_reason_details,structured_outcome,rendered_body,publication_key)
		SELECT $3,org_id,repository_id,repository_full_name,pull_request_id,metadata_id,session_id,policy_id,2,id,base_sha,base_ref,head_sha,input_version,
		code_digest,contract_digest,intent_digest,visual_digest,request_digest,gate_digest,'synthetic-recheck-state-input',input_manifest,'evidence_only','visual_changed',
		'running','evidence_only',decision,acceptable,risk_reason_details,structured_outcome,'Synthetic retained recheck output',$4
		FROM code_review_revision_assessments WHERE org_id=$1 AND id=$2`, f.job.OrgID, baseline.ID, f.assessment, "synthetic-state-recheck:"+f.assessment.String())
	require.NoError(t, err, "create the real exact evidence-only assessment with its DB-backed baseline FK and distinct publication key")
	require.Equal(t, int64(1), tag.RowsAffected(), "the exact recheck owner must genuinely exist before retirement begins")
	payload := fullAssessmentDispatchJSON(t, codeReviewRecheckJob{OrgID: f.job.OrgID, AssessmentID: f.assessment})
	tag, err = pool.Exec(ctx, `UPDATE jobs SET job_type='run_code_review_recheck',payload=$3,dedupe_key=$4
		WHERE org_id=$1 AND id=$2 AND status='running' AND lock_token=$5`, f.job.OrgID, f.jobID, payload, "code_review_recheck:"+f.assessment.String(), f.token)
	require.NoError(t, err, "retain the original exact lease while changing only the synthetic controller kind to its genuine recheck owner")
	require.Equal(t, int64(1), tag.RowsAffected(), "the real recheck must retain the same original running job and token")
	tag, err = pool.Exec(ctx, `UPDATE code_review_pr_state SET generation=2,current_assessment_id=$3,active_assessment_id=$4
		WHERE org_id=$1 AND pull_request_id=$2`, f.job.OrgID, f.job.PullRequestID, baseline.ID, f.assessment)
	require.NoError(t, err, "retain the source baseline and genuine recheck scheduling reservation as complete graph canaries")
	require.Equal(t, int64(1), tag.RowsAffected(), "the exact recheck scheduling reservation must exist independently of its retirement result")
	return f, baseline
}

func terminalStateCompositionSettleOriginal(t *testing.T, ctx context.Context, pool *pgxpool.Pool, f strandedReviewFixture, result uuid.UUID, state string) {
	t.Helper()
	fullAssessmentDispatchRun(t, ctx, pool, func(unitCtx context.Context, tx pgx.Tx) error {
		original, err := fullAssessmentDispatchRead(unitCtx, tx, f, result)
		if err != nil {
			return err
		}
		if state == "cancelled" {
			// A positive historical owner-ineligible decision is fixture input,
			// not a grant of authority inferred from this primitive. Starting
			// with retry-pending keeps a genuine known-abort attempt to preserve.
			settled, changed, err := db.CancelPendingReviewerDispatchInTx(unitCtx, f.job.OrgID, tx, original.Expectation(), db.ReviewerDispatchOwnerIneligible)
			if err != nil {
				return err
			}
			if !changed || settled.State() != db.ReviewerDispatchCancelled || settled.Resolution() != string(db.ReviewerDispatchOwnerIneligible) {
				return errors.New("synthetic actual pending cancellation did not settle the original owner")
			}
			return nil
		}
		if state != "refused" || original.State() != db.ReviewerDispatchAttempting {
			return errors.New("synthetic known refusal requires its actual committed attempting owner")
		}
		settlement, err := db.ReviewerDispatchKnownRefusal(db.ReviewerDispatchSendRefused)
		if err != nil {
			return err
		}
		settled, changed, err := db.SettleReviewerDispatchInTx(unitCtx, f.job.OrgID, tx, original.Expectation(), settlement)
		if err != nil {
			return err
		}
		if !changed || settled.State() != db.ReviewerDispatchRefused || settled.Resolution() != "send_refusal" {
			return errors.New("synthetic actual known refusal did not settle the original owner")
		}
		return nil
	})
}

func terminalStateCompositionAssertGraph(t *testing.T, before, after json.RawMessage, f strandedReviewFixture, failure db.CodeReviewTerminalFailure, tt terminalStateCompositionCase, pending uuid.UUID, stamp, observedAfter time.Time) {
	t.Helper()
	expected, actual := terminalOwnerPointerPositiveDecode(t, before), terminalOwnerPointerPositiveDecode(t, after)
	job := terminalOwnerPointerPositiveRow(t, expected, "jobs", f.jobID)
	actualJob := terminalOwnerPointerPositiveRow(t, actual, "jobs", f.jobID)
	require.Equal(t, fullAssessmentDispatchJSON(t, "running"), job["status"], "each finite composition must begin with the actual positively owned running controller")
	job["status"], job["last_error"], job["owner_kind"] = fullAssessmentDispatchJSON(t, "dead_letter"), fullAssessmentDispatchJSON(t, failure.JobError), fullAssessmentDispatchJSON(t, "worker")
	for _, field := range []string{"locked_by_node_id", "run_owner_id", "lock_token", "locked_at", "lease_expires_at"} {
		job[field] = json.RawMessage(`null`)
	}
	terminalOwnerPointerPositiveClock(t, job, actualJob, "completed_at", stamp)
	terminalOwnerPointerPositiveClock(t, job, actualJob, "updated_at", stamp)
	// Deliberate branches, not a whitelist copied from the actual result:
	// rechecks and already-terminal full metadata cannot change any baseline
	// metadata/result column; a true legacy owner has no assessment to edit.
	if !tt.recheck && !tt.terminalMetadata {
		metadata := terminalOwnerPointerPositiveRow(t, expected, "metadata", f.job.MetadataID)
		actualMetadata := terminalOwnerPointerPositiveRow(t, actual, "metadata", f.job.MetadataID)
		require.Equal(t, fullAssessmentDispatchJSON(t, "running"), metadata["status"], "the full active-metadata branch must actually transition an eligible original owner")
		metadata["status"] = fullAssessmentDispatchJSON(t, "failed")
		for _, field := range []string{"phase", "decision", "acceptable", "retry_at"} {
			metadata[field] = json.RawMessage(`null`)
		}
		code, message, retryable := failure.Code, strings.TrimSpace(failure.Message), failure.Retryable
		if tt.unresolved {
			code, message, retryable = models.CodeReviewStatusCodeDispatchUnresolved, "The review stopped with a reviewer dispatch still unresolved. Its original intent is retained for reconciliation; do not resend that command.", false
		}
		metadata["failure_reason"], metadata["status_code"], metadata["status_message"] = fullAssessmentDispatchJSON(t, strings.TrimSpace(failure.Reason)), fullAssessmentDispatchJSON(t, code), fullAssessmentDispatchJSON(t, message)
		metadata["retryable_failure"] = fullAssessmentDispatchJSON(t, retryable)
		terminalOwnerPointerPositiveClock(t, metadata, actualMetadata, "last_error_at", stamp)
		terminalOwnerPointerPositiveClock(t, metadata, actualMetadata, "completed_at", stamp)
	}
	if !tt.legacy {
		assessment := terminalOwnerPointerPositiveRow(t, expected, "assessments", f.assessment)
		actualAssessment := terminalOwnerPointerPositiveRow(t, actual, "assessments", f.assessment)
		require.Equal(t, fullAssessmentDispatchJSON(t, "running"), assessment["status"], "the exact full or recheck assessment must genuinely be eligible before the retirement mutation")
		reason := strings.TrimSpace(failure.Reason)
		if tt.unresolved {
			reason = "dispatch_unresolved:" + reason
		}
		assessment["status"], assessment["failure_detail"] = fullAssessmentDispatchJSON(t, "failed"), fullAssessmentDispatchJSON(t, reason)
		terminalOwnerPointerPositiveClock(t, assessment, actualAssessment, "completed_at", stamp)
	}
	if tt.legacy {
		child := terminalOwnerPointerPositiveRow(t, expected, "results", pending)
		actualChild := terminalOwnerPointerPositiveRow(t, actual, "results", pending)
		require.Equal(t, fullAssessmentDispatchJSON(t, "pending"), child["dispatch_state"], "legacy absence must still cancel a real Prepare-born original pending intent")
		child["dispatch_state"], child["dispatch_resolution"], child["dispatch_due_at"] = fullAssessmentDispatchJSON(t, "cancelled"), fullAssessmentDispatchJSON(t, "owner_cancelled"), json.RawMessage(`null`)
		var seq int
		require.NoError(t, json.Unmarshal(child["dispatch_writer_seq"], &seq), "retain the original durable writer sequence before the one pending cancellation")
		child["dispatch_writer_seq"] = fullAssessmentDispatchJSON(t, seq+1)
		var updated time.Time
		require.NoError(t, json.Unmarshal(actualChild["dispatch_updated_at"], &updated), "read the real later pending-cancellation statement timestamp")
		require.True(t, !updated.Before(stamp) && !updated.After(observedAfter), "the separate cancellation statement must lie inside the independently observed original unit interval")
		child["dispatch_updated_at"] = actualChild["dispatch_updated_at"]
	}
	require.Equal(t, expected, actual, "only the explicitly selected original job, eligible exact assessment, active full metadata and legacy pending child may change; the complete baseline/sibling graphs, immutable inputs/wakes, histories, scheduler, messages, evidence and every unrelated column must remain exact")
}

func TestCodeReviewTerminalStateCompositionsPostgres(t *testing.T) {
	t.Parallel()
	tests := []terminalStateCompositionCase{
		{name: "true no-assessment full owner cancels pending and retains unknown history", legacy: true, unresolved: true},
		{name: "recheck preserves live full-baseline pending and unknown history", recheck: true},
		{name: "already-terminal full metadata retains its pending child", terminalMetadata: true, unresolved: true},
		{name: "known-refused original remains exact despite live sibling work", settled: "refused"},
		{name: "already-cancelled original retains known-abort history and writer sequence", settled: "cancelled"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			pool := fullRecoveryPostgresPoolWithMaxConns(t, ctx, 2)
			require.Equal(t, int32(2), pool.Config().MaxConns, "each composition must use literally two pool slots with no hidden application headroom")
			f, foreign, children, foreignChildren := terminalDispatchOwnershipFixture(t, ctx, pool, "")
			if tt.legacy {
				terminalStateCompositionRemoveOriginalAssessment(t, ctx, pool, f)
			}
			var baseline models.CodeReviewAssessment
			if tt.recheck {
				f, baseline = terminalStateCompositionMakeRecheck(t, ctx, pool, f)
			}
			var thread uuid.UUID
			require.NoError(t, pool.QueryRow(ctx, `SELECT id FROM session_threads WHERE org_id=$1 AND session_id=$2`, f.job.OrgID, f.job.SessionID).Scan(&thread), "reuse the real completed original lane installed by the complete sibling/foreign fixture")
			state, attempts := "pending", 0
			if tt.settled == "cancelled" {
				state, attempts = "retry pending", 1
			} else if tt.settled == "refused" {
				state, attempts = "attempting", 1
			}
			pending := fullAssessmentDispatchChild(t, ctx, pool, f, thread, state)
			originalState, due := "pending", true
			if tt.settled != "" {
				terminalStateCompositionSettleOriginal(t, ctx, pool, f, pending, tt.settled)
				originalState, attempts, due = tt.settled, 1, false
			}
			children = append(children, terminalDispatchOwnershipChild{id: pending, metadata: f.job.MetadataID, state: originalState, attempts: attempts, due: due})
			if tt.legacy || tt.recheck {
				unknown := fullAssessmentDispatchChild(t, ctx, pool, f, thread, "unknown")
				children = append(children, terminalDispatchOwnershipChild{id: unknown, metadata: f.job.MetadataID, state: "unknown", attempts: 1, due: true})
			}
			if tt.terminalMetadata {
				// Model a legal committed historical metadata completion after
				// real Prepare, not a disabled guard or a call to today's aware
				// terminal wrapper (which would already cancel the pending row).
				fullAssessmentDispatchRun(t, ctx, pool, func(unitCtx context.Context, tx pgx.Tx) error {
					tag, err := tx.Exec(unitCtx, `UPDATE code_review_session_metadata SET status='completed',phase=NULL,decision='approved',acceptable=true,
						failure_reason=NULL,status_code=NULL,status_message=NULL,retry_at=NULL,last_error_at=NULL,retryable_failure=false,
						final_review_body='Synthetic committed historical full review',completed_at=statement_timestamp()
						WHERE org_id=$1 AND id=$2 AND status='running'`, f.job.OrgID, f.job.MetadataID)
					if err != nil {
						return err
					}
					if tag.RowsAffected() != 1 {
						return errors.New("synthetic historical metadata did not complete")
					}
					return nil
				})
			}
			reserved, err := pool.Acquire(ctx)
			require.NoError(t, err, "reserve the second real checkout before the actual single supplied retirement unit")
			defer reserved.Release()
			var pointer *uuid.UUID
			var originalCount int
			require.NoError(t, reserved.QueryRow(ctx, `SELECT assessment_id,(SELECT count(*)::int FROM code_review_revision_assessments WHERE org_id=$1 AND id=$3)
				FROM code_review_session_metadata WHERE org_id=$1 AND id=$2`, f.job.OrgID, f.job.MetadataID, f.assessment).Scan(&pointer, &originalCount), "independently confirm the actual baseline association and exact retiring assessment presence")
			if tt.legacy {
				require.Nil(t, pointer, "genuine no-assessment full ownership must start with a real NULL metadata pointer")
				require.Equal(t, 0, originalCount, "the legacy case must genuinely have no original assessment before pending/unknown retirement")
			} else {
				require.Equal(t, 1, originalCount, "every nonlegacy composition must contain its actual eligible exact assessment")
				if tt.recheck {
					require.Equal(t, &baseline.ID, pointer, "the recheck must preserve a genuine distinct full-baseline metadata association")
					var fullWake uuid.UUID
					require.NoError(t, reserved.QueryRow(ctx, `SELECT id FROM jobs WHERE org_id=$1 AND job_type='run_code_review'
						AND status='pending' AND dedupe_key=$2 AND payload->>'metadata_id'=$3`, f.job.OrgID, "code_review:"+f.job.OutputKey, f.job.MetadataID.String()).Scan(&fullWake), "the real baseline Prepare after recheck conversion must insert a nonempty original full-review wake, not merely retain a JSON wake envelope")
					require.NotEqual(t, f.jobID, fullWake, "the baseline full wake must be a different actual row from the retiring recheck controller")
				}
			}
			before, err := terminalContentionSnapshot(ctx, reserved, f)
			require.NoError(t, err, "retain the complete committed original/sibling graph after all legal historical fixture transitions")
			foreignBefore, err := terminalContentionSnapshot(ctx, reserved, foreign)
			require.NoError(t, err, "retain the entire genuine foreign tenant graph with a nonempty pending canary")
			beforeGraph := terminalOwnerPointerPositiveDecode(t, before)
			terminalDispatchOwnershipAssertChildren(t, beforeGraph, children)
			terminalDispatchOwnershipAssertChildren(t, terminalOwnerPointerPositiveDecode(t, foreignBefore), foreignChildren)
			if tt.terminalMetadata {
				require.Equal(t, fullAssessmentDispatchJSON(t, "completed"), terminalOwnerPointerPositiveRow(t, beforeGraph, "metadata", f.job.MetadataID)["status"], "the no-op metadata branch must begin with actual terminal metadata while its real original child remains pending")
			}
			if tt.settled != "" {
				child := terminalOwnerPointerPositiveRow(t, beforeGraph, "results", pending)
				resolution, writer := "send_refusal", 2
				if tt.settled == "cancelled" {
					resolution, writer = string(db.ReviewerDispatchOwnerIneligible), 3
				}
				require.Equal(t, fullAssessmentDispatchJSON(t, resolution), child["dispatch_resolution"], "the settled original must retain its real finite preexisting reason rather than retirement's owner_cancelled value")
				require.Equal(t, fullAssessmentDispatchJSON(t, writer), child["dispatch_writer_seq"], "actual Prepare/claim/settlement history must precede retirement; its writer cannot be reset or incremented again")
				require.NotEqual(t, json.RawMessage(`null`), child["dispatch_attempt_id"], "both refusal and cancelled retry must contain a genuine retained attempted identity")
				require.NotEqual(t, json.RawMessage(`null`), child["dispatch_attempted_at"], "settled no-resend history must retain an actual attempt time rather than an all-NULL legacy row")
			}
			var beforeAssessment models.CodeReviewAssessment
			if !tt.legacy {
				beforeAssessment, err = f.stores.CodeReviewAssessments.GetByID(ctx, f.job.OrgID, f.assessment)
				require.NoError(t, err, "retain the exact original full/recheck captured identity before retirement for an independent outcome-scope expectation")
			}
			observed := &fullAssessmentDispatchRunObserver{base: f.stores.ThreadSendDependencies.Transactions}
			jobs, err := db.NewJobStoreWithCodeReviewTerminalTransactions(pool, observed)
			require.NoError(t, err, "construct the aware JobStore with the explicit existing Run instead of inferring authority from DBTX")
			failure := db.CodeReviewTerminalFailure{JobError: "Synthetic finite retirement composition", Reason: "  Synthetic exact owner stopped  ",
				Code: models.CodeReviewStatusCodeWorkerFailed, Message: "  Synthetic ordinary exact-owner failure  ", Retryable: true}
			outcome, err := jobs.DeadLetterCodeReviewWithLease(ctx, f.job.OrgID, f.jobID, f.token, failure)
			require.NoError(t, err, "the actual current core must commit precisely the selected legacy/full/recheck composition without policy or production changes")
			fullAssessmentDispatchAssertRun(t, observed)
			require.True(t, outcome.Owned(), "the exact real controller job must actually retire in a known-success unit")
			require.True(t, outcome.Matches(f.job.OrgID, f.jobID, f.token), "the sealed result must bind the exact original tenant, job and retained lease")
			require.False(t, outcome.Matches(foreign.job.OrgID, foreign.jobID, foreign.token), "a successful original retirement cannot authorize any effects for the genuine foreign controller")
			scope, hasOwner := outcome.Scope()
			require.True(t, hasOwner, "valid no-assessment, terminal-metadata and recheck owners must not silently downgrade to job-only poison handling")
			expectedScope := db.CodeReviewTerminalScope{OrgID: f.job.OrgID, JobID: f.jobID, LockToken: f.token, JobType: models.JobTypeRunCodeReview,
				SessionID: f.job.SessionID, MetadataID: f.job.MetadataID, RepositoryID: f.job.RepositoryID, PullRequestID: f.job.PullRequestID,
				PolicyID: f.job.PolicyID, AssessmentID: f.assessment, HeadSHA: f.job.HeadSHA, OutputKey: f.job.OutputKey, ReviewScope: models.CodeReviewScopeFull}
			if tt.legacy {
				expectedScope.AssessmentID = uuid.Nil
			} else if tt.recheck {
				expectedScope.JobType, expectedScope.HeadSHA, expectedScope.OutputKey, expectedScope.ReviewScope = models.JobTypeRunCodeReviewRecheck, beforeAssessment.HeadSHA, beforeAssessment.PublicationKey, models.CodeReviewScopeEvidenceOnly
			}
			require.Equal(t, expectedScope, scope, "the committed carrier must retain the complete exact original controller association, including true assessment absence or the distinct recheck publication key")
			require.Equal(t, tt.unresolved, outcome.UnresolvedDispatch(), "only original full-owner due history may set the pre-cancellation unresolved fact; neither sibling work nor settled originals can do so")
			after, err := terminalContentionSnapshot(ctx, reserved, f)
			require.NoError(t, err, "independently read the complete actual original/sibling graph after the known commit")
			foreignAfter, err := terminalContentionSnapshot(ctx, reserved, foreign)
			require.NoError(t, err, "independently read the complete genuine foreign tenant after original retirement")
			var observedAfter time.Time
			require.NoError(t, reserved.QueryRow(ctx, `SELECT statement_timestamp()`).Scan(&observedAfter), "bound any permitted later cancellation timestamp with an independent postcommit database clock")
			terminalStateCompositionAssertGraph(t, before, after, f, failure, tt, pending, observed.clock, observedAfter)
			require.Equal(t, foreignBefore, foreignAfter, "all genuine foreign pending/work/history rows must remain byte-exact after the original composition commits")
			metadata, metadataChanged := outcome.Metadata()
			require.Equal(t, !tt.recheck && !tt.terminalMetadata, metadataChanged, "only an actually changed active full metadata row may produce a metadata descriptor")
			if metadataChanged {
				stored, err := f.stores.CodeReviews.GetByOutputKey(ctx, f.job.OrgID, f.job.OutputKey)
				require.NoError(t, err, "read the exact committed full metadata descriptor by its original output key")
				require.Equal(t, stored, metadata, "the complete returned metadata descriptor must equal actual storage, including safe unresolved copy for genuine no-assessment retirement")
			} else {
				require.Equal(t, models.CodeReviewSessionMetadata{}, metadata, "a recheck or already-terminal metadata no-op cannot manufacture any changed metadata descriptor")
			}
			assessment, assessmentChanged := outcome.Assessment()
			require.Equal(t, !tt.legacy, assessmentChanged, "only a genuinely present eligible exact full/recheck assessment may produce an assessment descriptor")
			if tt.legacy {
				require.Equal(t, models.CodeReviewAssessment{}, assessment, "successful true no-assessment retirement cannot fabricate an assessment or target descriptor")
				var afterPointer *uuid.UUID
				var afterCount int
				require.NoError(t, reserved.QueryRow(ctx, `SELECT assessment_id,(SELECT count(*)::int FROM code_review_revision_assessments WHERE org_id=$1)
					FROM code_review_session_metadata WHERE org_id=$1 AND id=$2`, f.job.OrgID, f.job.MetadataID).Scan(&afterPointer, &afterCount), "independently prove successful no-assessment retirement created or adopted no assessment")
				require.Nil(t, afterPointer, "genuine no-assessment retirement must retain the NULL metadata association")
				require.Equal(t, 0, afterCount, "the complete original tenant must still have no assessment after metadata and pending settlement commit")
			} else {
				stored, err := f.stores.CodeReviewAssessments.GetByID(ctx, f.job.OrgID, f.assessment)
				require.NoError(t, err, "independently read the exact changed assessment instead of a latest or baseline owner")
				require.Equal(t, stored, assessment, "the returned assessment descriptor must equal all actual committed fields and retained history")
				if tt.recheck {
					storedBaseline, err := f.stores.CodeReviewAssessments.GetByID(ctx, f.job.OrgID, baseline.ID)
					require.NoError(t, err, "read the original full baseline independently after exact recheck retirement")
					require.Equal(t, baseline, storedBaseline, "the complete original full baseline must remain immutable while only its eligible successor fails")
				}
			}
		})
	}
}
