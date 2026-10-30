package worker

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/jobctx"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

const confirmedObservationBoundBytes = 8 << 20

// These are real stored-size and consumer-boundary proofs, not provider,
// Worker.poll, completion, activation or delivery proofs. Each isolated leaf
// reserves one of literally two slots for an independent observer. The other
// slot belongs to the existing unchanged bounded transaction owner.
//
// The large digest is captured through actual Create, not an UPDATE to captured
// inputs. Failing the unstaged predecessor first satisfies the active-PR unique
// index; every original FK, guard and historical row remains enabled/retained.
func confirmedObservationBoundCapture(t *testing.T, ctx context.Context, pool *pgxpool.Pool, f strandedReviewFixture, beforeCapture func(strandedReviewFixture)) strandedReviewFixture {
	t.Helper()
	previous, err := f.stores.CodeReviewAssessments.GetByID(ctx, f.job.OrgID, f.assessment)
	require.NoError(t, err, "read the actual unstaged predecessor before legally capturing a large comparison digest")
	f.assessment, f.job.OutputKey = uuid.New(), "synthetic-bound-successor:"+uuid.NewString()
	f.manifest.InputDigest = strings.Repeat("d", confirmedObservationBoundBytes+1)
	manifest, payload := fullAssessmentDispatchJSON(t, f.manifest), fullAssessmentDispatchJSON(t, f.job)
	capture := models.CodeReviewAssessmentCapture{ID: f.assessment, OrgID: f.job.OrgID,
		RepositoryID: previous.RepositoryID, PullRequestID: previous.PullRequestID, PolicyID: previous.PolicyID,
		SessionID: previous.SessionID, Generation: previous.Generation + 1, PreviousAssessmentID: &previous.ID,
		BaseSHA: previous.BaseSHA, BaseRef: previous.BaseRef, HeadSHA: previous.HeadSHA, InputVersion: previous.InputVersion,
		CodeDigest: previous.CodeDigest, ContractDigest: previous.ContractDigest, IntentDigest: previous.IntentDigest,
		VisualDigest: previous.VisualDigest, RequestDigest: previous.RequestDigest, GateDigest: previous.GateDigest,
		InputDigest: f.manifest.InputDigest, InputManifest: manifest, ReviewScope: previous.ReviewScope,
		RouteReason: previous.RouteReason, PublicationKey: f.job.OutputKey}
	fullAssessmentDispatchRun(t, ctx, pool, func(unitCtx context.Context, tx pgx.Tx) error {
		if err := db.NewCodeReviewRecheckStore(tx).LockFullAssessmentCompletion(unitCtx, tx, f.job.OrgID, f.job.PullRequestID); err != nil {
			return err
		}
		assessments := db.NewCodeReviewAssessmentStore(tx)
		if err := assessments.Fail(unitCtx, f.job.OrgID, previous.ID, previous.Generation, previous.InputDigest, "Synthetic unstaged predecessor retained before large capture"); err != nil {
			return err
		}
		// Create resolves its metadata from the latest compatible session row.
		// The common fixture contains a newer same-session sibling; order this
		// exact original row last before invoking the real Create API so the
		// intended candidate cannot accidentally belong to that sibling.
		if _, err := tx.Exec(unitCtx, `UPDATE code_review_session_metadata SET review_output_key=$3,
			created_at=(SELECT max(m.created_at)+interval '1 second' FROM code_review_session_metadata m WHERE m.org_id=$1 AND m.session_id=$4)
			WHERE org_id=$1 AND id=$2`, f.job.OrgID, f.job.MetadataID, f.job.OutputKey, f.job.SessionID); err != nil {
			return err
		}
		_, err := tx.Exec(unitCtx, `UPDATE jobs SET payload=$3,dedupe_key=$4 WHERE org_id=$1 AND id=$2 AND status='running' AND lock_token=$5`, f.job.OrgID, f.jobID, payload, "code_review:"+f.job.OutputKey, f.token)
		return err
	})
	// Prepare the genuine new-key pending/unknown histories while that exact
	// candidate is absent. Later consumers must distinguish an oversized
	// structurally confirmed candidate from this ordinary prepublication owner.
	// This fixture callback is OUTSIDE Run, never a nested transaction owner.
	beforeCapture(f)
	var created models.CodeReviewAssessment
	var replay bool
	fullAssessmentDispatchRun(t, ctx, pool, func(unitCtx context.Context, tx pgx.Tx) error {
		created, replay = models.CodeReviewAssessment{}, false
		if err := db.NewCodeReviewRecheckStore(tx).LockFullAssessmentCompletion(unitCtx, tx, f.job.OrgID, f.job.PullRequestID); err != nil {
			return err
		}
		assessments := db.NewCodeReviewAssessmentStore(tx)
		var createErr error
		created, replay, createErr = assessments.Create(unitCtx, capture)
		if createErr != nil {
			return createErr
		}
		if created.MetadataID != f.job.MetadataID {
			return errors.New("synthetic large candidate must belong to the exact original metadata, never its same-session sibling")
		}
		if err := assessments.MarkRunning(unitCtx, f.job.OrgID, created.ID, created.Generation, created.InputDigest); err != nil {
			return err
		}
		// Retain a legal exact reverse pointer after the real FK-valid
		// successor exists. An unrelated existing pointer remains a refusal.
		tag, err := tx.Exec(unitCtx, `UPDATE code_review_session_metadata SET assessment_id=$3
			WHERE org_id=$1 AND id=$2 AND (assessment_id IS NULL OR assessment_id=$4)`, f.job.OrgID, f.job.MetadataID, created.ID, previous.ID)
		if err != nil {
			return err
		}
		if tag.RowsAffected() != 1 {
			return errors.New("synthetic successor must link only its compatible original metadata pointer")
		}
		_, err = tx.Exec(unitCtx, `UPDATE code_review_pr_state SET generation=$3,active_assessment_id=$4 WHERE org_id=$1 AND pull_request_id=$2`, f.job.OrgID, f.job.PullRequestID, created.Generation, created.ID)
		return err
	})
	require.False(t, replay, "the oversized digest must be genuinely inserted through Create rather than replayed from a different small captured input")
	require.Equal(t, capture.ID, created.ID, "actual Create must persist the preminted exact successor used by all three observation consumers")
	require.Equal(t, capture.InputDigest, created.InputDigest, "actual Create must accept the whole large nonempty digest with all ordinary producer validation and database guards enabled")
	return f
}

// A schema-legal large captured digest must not turn an ordinary owner into a
// permanent observation refusal. The real Create path supplies the candidate;
// Prepare's version fence and actual exact-lease retirement both still run.
// This is not a provider, completion, polling or activation proof.
func TestCodeReviewNonconfirmedOversizedCandidatePostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		uncertain bool
	}{
		{name: "actual running prepublication candidate"},
		{name: "actual uncertain staged publication candidate", uncertain: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel() // Isolated full-schema DB; root bounds aggregate PostgreSQL use with -parallel=1.
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			pool := fullRecoveryPostgresPoolWithMaxConns(t, ctx, 2)
			require.Equal(t, int32(2), pool.Config().MaxConns, "ordinary oversize classification must fit the existing transaction slot beside one independent observer")
			f := terminalOwnerPointerPositiveFixture(t, pool, false)
			var pending uuid.UUID
			var in db.CodeReviewDispatchPreparation
			f = confirmedObservationBoundCapture(t, ctx, pool, f, func(current strandedReviewFixture) {
				tag, err := pool.Exec(ctx, `UPDATE caller_send_intent_control SET intents_enabled=true WHERE singleton AND schema_ready AND dispatch_status_ready`)
				require.NoError(t, err, "enable only this isolated database's actual existing dispatch readiness gates")
				require.Equal(t, int64(1), tag.RowsAffected(), "the nonconfirmed candidate must genuinely permit ordinary Prepare rather than pass through a disabled gate")
				thread := uuid.New()
				_, err = pool.Exec(ctx, `INSERT INTO session_threads(id,org_id,session_id,agent_type,label,status,completed_at)
					VALUES($1,$2,$3,'claude_code','Synthetic oversized candidate lane','completed',statement_timestamp())`, thread, current.job.OrgID, current.job.SessionID)
				require.NoError(t, err, "install an actual compatible original lane for nonvacuous new-child preparation")
				pending = fullAssessmentDispatchChild(t, ctx, pool, current, thread, "pending")
				fullAssessmentDispatchChild(t, ctx, pool, current, thread, "unknown")
				in = confirmedReceiptAliasPreparation(t, ctx, pool, current, thread)
			})
			if tt.uncertain {
				confirmedRetirementProofStage(t, ctx, pool, f, f, confirmedRetirementProofCase{publication: "uncertain with receipt"})
			}
			observer, err := pool.Acquire(ctx)
			require.NoError(t, err, "reserve the sole independent observation slot before any original whole-unit transaction starts")
			defer observer.Release()
			var aggregate int64
			var publication models.CodeReviewPublicationState
			require.NoError(t, observer.QueryRow(ctx, `SELECT octet_length(COALESCE(publication_receipt::text,''))::bigint+octet_length(input_digest)+octet_length(head_sha),publication_state
				FROM code_review_revision_assessments WHERE org_id=$1 AND id=$2`, f.job.OrgID, f.assessment).Scan(&aggregate, &publication), "measure the genuine stored large candidate and its actual publication state before both consumers")
			require.Greater(t, aggregate, int64(confirmedObservationBoundBytes), "the actual aggregate must exceed the shared observation projection budget without altering a captured digest after Create")
			expectedPublication := models.CodeReviewPublicationNotStarted
			if tt.uncertain {
				expectedPublication = models.CodeReviewPublicationUncertain
			}
			require.Equal(t, expectedPublication, publication, "only a real nonconfirmed owner may exercise ordinary paths; no confirmed candidate is relabeled to evade the size refusal")
			before, err := terminalContentionSnapshot(ctx, observer, f)
			require.NoError(t, err, "retain the entire original and sibling durable graph after actual capture and optional uncertain staging")
			pendingCompletion, err := db.NewCodeReviewStore(observer).HasConfirmedFullAssessmentCompletionPending(ctx, f.job.OrgID, f.job.MetadataID)
			require.NoError(t, err, "an oversized but structurally nonconfirmed candidate requires no receipt bytes or observation-size refusal")
			require.False(t, pendingCompletion, "large ordinary input must grant no confirmed-publication completion authority")
			confirmedObservationBoundAssertUnchanged(t, ctx, observer, f, before, "the nonconfirmed oversized read-only observation")
			stop := errors.New("synthetic nonconfirmed oversized Prepare probe rollback")
			installer := &fullAssessmentDispatchRunObserver{base: f.stores.ThreadSendDependencies.Transactions}
			var prepared db.PreparedCodeReviewDispatch
			var prepareErr error
			var staged json.RawMessage
			installErr := installer.Run(ctx, func(unitCtx context.Context, tx pgx.Tx) error {
				prepared, prepareErr, staged = db.PreparedCodeReviewDispatch{}, nil, nil
				prepared, prepareErr = db.PrepareCodeReviewDispatchInTx(unitCtx, f.job.OrgID, tx, in)
				if prepareErr != nil {
					return prepareErr
				}
				var err error
				staged, err = terminalContentionSnapshot(unitCtx, tx, f)
				if err != nil {
					return err
				}
				return stop
			})
			fullAssessmentDispatchAssertRun(t, installer)
			require.NoError(t, prepareErr, "actual Prepare must retain and pass the nonconfirmed candidate's real identity/xmin fence rather than permanently refuse its large digest")
			require.Equal(t, stop, installErr, "ordinary provisional preparation must leave only through the explicit actual outer rollback sentinel")
			require.Equal(t, in.ResultID, prepared.Result().ID, "the allowed provisional result must be the genuinely inserted preminted new child")
			require.Equal(t, uuid.Nil, prepared.WakeJobID(), "the actual running original controller wake must still deduplicate the provisional preparation")
			require.False(t, prepared.WakeInserted(), "ordinary size classification cannot invent a new controller or accepted provider delivery")
			terminalInstallerOrderAssertNewChild(t, before, staged, in)
			confirmedObservationBoundAssertUnchanged(t, ctx, observer, f, before, "the actual nonconfirmed oversized Prepare rollback")
			terminalInstallerOrderAbsent(t, ctx, observer, f, in)
			retirement := &fullAssessmentDispatchRunObserver{base: f.stores.ThreadSendDependencies.Transactions}
			jobs, err := db.NewJobStoreWithCodeReviewTerminalTransactions(pool, retirement)
			require.NoError(t, err, "supply the unchanged actual bounded transaction owner for ordinary exact-lease retirement")
			failure := db.CodeReviewTerminalFailure{JobError: "Synthetic ordinary oversized controller exhausted", Reason: "Synthetic ordinary oversized original attempt stopped",
				Code: models.CodeReviewStatusCodeWorkerFailed, Message: "Synthetic ordinary stopped-attempt copy", Retryable: true}
			outcome, err := jobs.DeadLetterCodeReviewWithLease(ctx, f.job.OrgID, f.jobID, f.token, failure)
			fullAssessmentDispatchAssertRun(t, retirement)
			require.NoError(t, err, "actual nonconfirmed oversized retirement must commit rather than enter an observation-refusal reclaim loop")
			require.True(t, outcome.Matches(f.job.OrgID, f.jobID, f.token), "only the actual exact original lease's known commit may release a terminal carrier")
			scope, owned := outcome.Scope()
			require.True(t, owned, "ordinary classification must retain actual original assessment ownership, not fall back to a job-only carrier")
			require.Equal(t, db.CodeReviewTerminalScope{OrgID: f.job.OrgID, JobID: f.jobID, LockToken: f.token, JobType: models.JobTypeRunCodeReview,
				SessionID: f.job.SessionID, MetadataID: f.job.MetadataID, RepositoryID: f.job.RepositoryID, PullRequestID: f.job.PullRequestID,
				PolicyID: f.job.PolicyID, AssessmentID: f.assessment, HeadSHA: f.job.HeadSHA, OutputKey: f.job.OutputKey, ReviewScope: models.CodeReviewScopeFull}, scope,
				"the real large candidate must remain the retirement's entire exact original owner, never its predecessor or same-session sibling")
			require.False(t, outcome.CompletionRecoveryPending(), "an oversized ordinary candidate must not acquire the confirmed-publication exception")
			require.True(t, outcome.UnresolvedDispatch(), "real original unknown/pending histories must keep their unchanged ordinary unresolved-send policy")
			after, err := terminalContentionSnapshot(ctx, observer, f)
			require.NoError(t, err, "independently read every durable graph field after the actual ordinary retirement")
			var afterClock time.Time
			require.NoError(t, observer.QueryRow(ctx, `SELECT statement_timestamp()`).Scan(&afterClock), "bound the actual ordinary later cancellation with an independent database clock")
			confirmedRetirementProofAssertGraph(t, before, after, f, confirmedRetirementProofCase{assessmentFails: !tt.uncertain}, failure, pending, retirement.clock, afterClock)
			terminalInstallerOrderAbsent(t, ctx, observer, f, in)
		})
	}
}

// PostgreSQL, not Go's compact marshaler, determines the actual presentation
// size. ASCII padding changes JSONB::text by exactly one byte per character and
// is ignored by the existing tagged-struct decoder. No stored receipt is later
// overwritten to manufacture the boundary.
func confirmedObservationBoundReceipt(t *testing.T, ctx context.Context, pool *pgxpool.Pool, f strandedReviewFixture, total int64) json.RawMessage {
	t.Helper()
	fields := map[string]any{"assessment_id": f.assessment.String(), "input_digest": f.manifest.InputDigest,
		"submitted_commit_sha": f.job.HeadSHA, "github_review_id": int64(77), "ignored_padding": ""}
	raw := fullAssessmentDispatchJSON(t, fields)
	if total == 0 {
		return raw
	}
	var base int64
	require.NoError(t, pool.QueryRow(ctx, `SELECT octet_length($3::jsonb::text)::bigint+octet_length(input_digest)+octet_length(head_sha)
		FROM code_review_revision_assessments WHERE org_id=$1 AND id=$2`, f.job.OrgID, f.assessment, raw).Scan(&base), "measure actual PostgreSQL JSONB text plus both comparison strings before choosing exact legal padding")
	require.Greater(t, total, base, "the desired boundary must be reached by adding only ignored legal string padding")
	fields["ignored_padding"] = strings.Repeat("p", int(total-base))
	return fullAssessmentDispatchJSON(t, fields)
}

func confirmedObservationBoundAssertUnchanged(t *testing.T, ctx context.Context, observer *pgxpool.Conn, f strandedReviewFixture, before json.RawMessage, consumer string) {
	t.Helper()
	after, err := terminalContentionSnapshot(ctx, observer, f)
	require.NoError(t, err, "independently read the entire durable graph after %s", consumer)
	require.Equal(t, before, after, "%s must preserve every receipt, staged outcome, active owner, queue lease, original pending/unknown intent, sibling, scheduler reservation and evidence column", consumer)
}

func confirmedObservationBoundAssertCommitted(t *testing.T, before, after json.RawMessage, f strandedReviewFixture, pending uuid.UUID, recorded *terminalACKStore, stamp, observedAfter time.Time) {
	t.Helper()
	expected, actual := terminalOwnerPointerPositiveDecode(t, before), terminalOwnerPointerPositiveDecode(t, after)
	job, actualJob := terminalOwnerPointerPositiveRow(t, expected, "jobs", f.jobID), terminalOwnerPointerPositiveRow(t, actual, "jobs", f.jobID)
	job["status"], job["last_error"], job["owner_kind"] = fullAssessmentDispatchJSON(t, "dead_letter"), fullAssessmentDispatchJSON(t, recorded.failure.JobError), fullAssessmentDispatchJSON(t, "worker")
	for _, field := range []string{"locked_by_node_id", "run_owner_id", "lock_token", "locked_at", "lease_expires_at"} {
		job[field] = json.RawMessage(`null`)
	}
	terminalOwnerPointerPositiveClock(t, job, actualJob, "completed_at", stamp)
	terminalOwnerPointerPositiveClock(t, job, actualJob, "updated_at", stamp)
	assessment := terminalOwnerPointerPositiveRow(t, expected, "assessments", f.assessment)
	assessment["failure_detail"] = fullAssessmentDispatchJSON(t, "confirmed_completion_recovery_pending")
	child, actualChild := terminalOwnerPointerPositiveRow(t, expected, "results", pending), terminalOwnerPointerPositiveRow(t, actual, "results", pending)
	var writer int
	require.NoError(t, json.Unmarshal(child["dispatch_writer_seq"], &writer), "retain the real original pending writer before the control's one cancellation")
	child["dispatch_state"], child["dispatch_resolution"], child["dispatch_due_at"] = fullAssessmentDispatchJSON(t, "cancelled"), fullAssessmentDispatchJSON(t, "owner_cancelled"), json.RawMessage(`null`)
	child["dispatch_writer_seq"] = fullAssessmentDispatchJSON(t, writer+1)
	var cancelled time.Time
	require.NoError(t, json.Unmarshal(actualChild["dispatch_updated_at"], &cancelled), "read the actual later pending-cancellation statement clock")
	require.WithinRange(t, cancelled, stamp, observedAfter, "the positive control's cancellation must happen inside the unchanged bounded retirement")
	child["dispatch_updated_at"] = actualChild["dispatch_updated_at"]
	require.Equal(t, expected, actual, "a within-bound positive may change only the exact original queue, pending cancellation and approved diagnostic; all staged publication facts and unrelated whole-graph rows stay exact")
}

func TestCodeReviewConfirmedObservationBoundPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name            string
		total           int64
		oversizedDigest bool
		tooLarge        bool
	}{
		{name: "small valid ignored-padding receipt remains a positive control"},
		{name: "actual aggregate exactly eight MiB remains observable", total: confirmedObservationBoundBytes},
		{name: "actual receipt padding one byte over aggregate limit is an observation error", total: confirmedObservationBoundBytes + 1, tooLarge: true},
		{name: "actual Create persists an oversized comparison digest without bypassing guards", oversizedDigest: true, tooLarge: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel() // Fresh fully migrated DB per leaf; parent bounds aggregate PostgreSQL use with -parallel=1.
			ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
			defer cancel()
			pool := fullRecoveryPostgresPoolWithMaxConns(t, ctx, 2)
			require.Equal(t, int32(2), pool.Config().MaxConns, "boundary consumers must fit one actual bounded owner beside one reserved observer without hidden capacity")
			f := terminalOwnerPointerPositiveFixture(t, pool, false)
			var pending uuid.UUID
			var in db.CodeReviewDispatchPreparation
			prepareHistories := func(current strandedReviewFixture) {
				thread := uuid.New()
				tag, err := pool.Exec(ctx, `UPDATE caller_send_intent_control SET intents_enabled=true WHERE singleton AND schema_ready AND dispatch_status_ready`)
				require.NoError(t, err, "enable only the migrated isolated fixture's existing intent readiness gates")
				require.Equal(t, int64(1), tag.RowsAffected(), "the real readiness gates must allow a nonvacuous Prepare-born pending canary")
				_, err = pool.Exec(ctx, `INSERT INTO session_threads(id,org_id,session_id,agent_type,label,status,completed_at)
					VALUES($1,$2,$3,'claude_code','Synthetic bounded observation lane','completed',statement_timestamp())`, thread, current.job.OrgID, current.job.SessionID)
				require.NoError(t, err, "retain an actual completed original lane before preparing immutable dispatch histories")
				pending = fullAssessmentDispatchChild(t, ctx, pool, current, thread, "pending")
				fullAssessmentDispatchChild(t, ctx, pool, current, thread, "unknown")
				in = confirmedReceiptAliasPreparation(t, ctx, pool, current, thread)
			}
			if tt.oversizedDigest {
				f = confirmedObservationBoundCapture(t, ctx, pool, f, prepareHistories)
			} else {
				prepareHistories(f)
			}
			receipt := confirmedObservationBoundReceipt(t, ctx, pool, f, tt.total)
			confirmedReceiptAliasStage(t, ctx, f, receipt)
			var aggregate, digestBytes int64
			require.NoError(t, pool.QueryRow(ctx, `SELECT octet_length(publication_receipt::text)::bigint+octet_length(input_digest)+octet_length(head_sha),octet_length(input_digest)::bigint
				FROM code_review_revision_assessments WHERE org_id=$1 AND id=$2`, f.job.OrgID, f.assessment).Scan(&aggregate, &digestBytes), "independently measure the actual committed JSONB presentation and comparison digest, not pre-storage input length or a fabricated fits result")
			if tt.total != 0 {
				require.Equal(t, tt.total, aggregate, "the actually persisted receipt-plus-digest-plus-head must hit this exact byte boundary")
			}
			if tt.oversizedDigest {
				require.Equal(t, int64(confirmedObservationBoundBytes+1), digestBytes, "the stored comparison digest alone must genuinely exceed the shared projection budget")
			}
			require.Equal(t, tt.tooLarge, aggregate > confirmedObservationBoundBytes, "size disposition must follow the actual committed aggregate, independently of all consumer classifications")
			assessment, err := f.stores.CodeReviewAssessments.GetByID(ctx, f.job.OrgID, f.assessment)
			require.NoError(t, err, "read actual stored JSONB outside every observation transaction for the unchanged decoder reference")
			metadata, err := f.stores.CodeReviews.GetByOutputKey(ctx, f.job.OrgID, f.job.OutputKey)
			require.NoError(t, err, "read only the exact original metadata rather than its newer same-session sibling")
			require.True(t, confirmedFullAssessmentReceiptMatches(assessment, metadata), "every real receipt, including oversize ignored padding and matching huge digest, must remain semantically valid to the old tagged-struct decoder")
			observer, err := pool.Acquire(ctx)
			require.NoError(t, err, "reserve the independent observer before any consumer's original whole-unit budget starts")
			defer observer.Release()
			before, err := terminalContentionSnapshot(ctx, observer, f)
			require.NoError(t, err, "freeze all durable original and sibling rows after genuine guarded publication storage")
			terminalInstallerOrderAbsent(t, ctx, observer, f, in)
			pendingCompletion, readerErr := db.NewCodeReviewStore(observer).HasConfirmedFullAssessmentCompletionPending(ctx, f.job.OrgID, f.job.MetadataID)
			confirmedObservationBoundAssertUnchanged(t, ctx, observer, f, before, "the actual read-only pending-completion reader")
			installer := &fullAssessmentDispatchRunObserver{base: f.stores.ThreadSendDependencies.Transactions}
			var prepared db.PreparedCodeReviewDispatch
			prepareErr := installer.Run(ctx, func(unitCtx context.Context, tx pgx.Tx) error {
				prepared = db.PreparedCodeReviewDispatch{}
				var err error
				prepared, err = db.PrepareCodeReviewDispatchInTx(unitCtx, f.job.OrgID, tx, in)
				return err
			})
			fullAssessmentDispatchAssertRun(t, installer)
			confirmedObservationBoundAssertUnchanged(t, ctx, observer, f, before, "actual Prepare and its real rollback")
			terminalInstallerOrderAbsent(t, ctx, observer, f, in)
			retirement := &fullAssessmentDispatchRunObserver{base: f.stores.ThreadSendDependencies.Transactions}
			actualJobs, err := db.NewJobStoreWithCodeReviewTerminalTransactions(pool, retirement)
			require.NoError(t, err, "supply aware exact-lease retirement with the unchanged actual bounded transaction owner")
			recorded := &terminalACKStore{actual: actualJobs}
			w := New(pool, zerolog.Nop(), "synthetic-observation-bound", WithCodeReviewTerminalStore(recorded))
			rows, err := observer.Query(ctx, `SELECT * FROM jobs WHERE org_id=$1 AND id=$2 AND status='running' AND lock_token=$3`, f.job.OrgID, f.jobID, f.token)
			require.NoError(t, err, "load the actual retained original running job rather than manufacturing an owned terminal carrier")
			job, err := pgx.CollectOneRow(rows, pgx.RowToStructByName[models.Job])
			require.NoError(t, err, "the actual original persisted job and exact lease must exist before the real worker settlement boundary")
			handlerCtx := jobctx.WithDeadLetterHooks(jobctx.WithJobID(jobctx.WithLockToken(ctx, f.token), f.jobID))
			hooks := 0
			jobctx.RegisterDeadLetterHook(handlerCtx, func(hookCtx context.Context, _ error) {
				hooks++
				outcome, scope, committed := committedCodeReviewTerminalOutcome(hookCtx, f.job.OrgID, models.JobTypeRunCodeReview)
				require.True(t, committed, "the positive hook control must receive only a genuine known-committed carrier from the actual worker boundary")
				require.True(t, outcome.Matches(f.job.OrgID, f.jobID, f.token), "the positive hook must bind precisely the original actual queue lease")
				require.Equal(t, f.assessment, scope.AssessmentID, "the positive control cannot borrow the prior assessment or same-session sibling")
			})
			// Direct settlement intentionally avoids an unrelated poll claim,
			// renewal or handler mutation of the frozen whole-graph baseline.
			w.deadLetterJob(ctx, handlerCtx, &job, &FatalError{Err: errors.New("Synthetic exhausted observation-bound controller")})
			fullAssessmentDispatchAssertRun(t, retirement)
			require.Equal(t, 1, recorded.calls, "the real worker boundary must invoke its actual aware store exactly once without a fallback or extra budget")
			require.Equal(t, db.PreparedCodeReviewDispatch{}, prepared, "neither confirmed-owner refusal nor oversize observation may expose any provisional prepared child or wake")
			after, err := terminalContentionSnapshot(ctx, observer, f)
			require.NoError(t, err, "independently inspect the complete durable graph after the actual aware retirement boundary returns")
			if tt.tooLarge {
				for _, consumer := range []struct {
					name string
					err  error
				}{
					{name: "read-only pending-completion reader", err: readerErr},
					{name: "actual Prepare", err: prepareErr},
					{name: "actual aware retirement", err: recorded.err},
				} {
					require.ErrorIs(t, consumer.err, db.ErrCodeReviewConfirmedCompletionObservationTooLarge, "%s must expose the explicit real observation-size cause through its private wrappers rather than return a semantic false", consumer.name)
					require.ErrorIs(t, consumer.err, db.ErrCodeReviewConfirmedCompletionObservation, "%s must preserve the observation domain classification alongside the exact oversize cause", consumer.name)
				}
				require.ErrorIs(t, prepareErr, db.ErrCodeReviewDispatchPreparationStorage, "Prepare must retain its own outer storage domain around the unchanged observation cause")
				require.ErrorIs(t, recorded.err, db.ErrCodeReviewTerminalStorage, "retirement must retain its own outer storage domain around the unchanged observation cause")
				require.False(t, pendingCompletion, "an oversize reader error must expose zero positive pending-completion evidence")
				require.Equal(t, db.CodeReviewTerminalOutcome{}, recorded.outcome, "the actual failed bounded retirement must discard every owned bit, target descriptor and provisional or committed terminal carrier")
				require.Zero(t, hooks, "a known failed oversize observation cannot release any registered worker hook")
				require.Equal(t, before, after, "oversize retirement must leave the complete durable graph byte-identical, including the live exact lease and every pending/unknown immutable history")
			} else {
				require.NoError(t, readerErr, "a semantically valid within-bound projection must remain readable")
				require.True(t, pendingCompletion, "small and exact-bound controls must remain positively confirmed local-completion owners")
				require.Equal(t, db.ErrCodeReviewDispatchPreparationIneligible, prepareErr, "within-bound confirmed completion must refuse a genuinely new child as ordinary owner ineligibility, not storage failure")
				require.NoError(t, recorded.err, "the within-bound control must commit actual exact-lease retirement")
				require.True(t, recorded.outcome.Matches(f.job.OrgID, f.jobID, f.token), "only actual known commit may release the positive control's terminal carrier")
				require.True(t, recorded.outcome.CompletionRecoveryPending(), "the exact aggregate boundary is inclusive and retains original completion recovery truth")
				require.Equal(t, 1, hooks, "the identical registered hook must actually fire for the known-commit control so oversize suppression is nonvacuous")
				var observedAfter time.Time
				require.NoError(t, observer.QueryRow(ctx, `SELECT statement_timestamp()`).Scan(&observedAfter), "bound the positive control's later cancellation by an independent real database clock")
				confirmedObservationBoundAssertCommitted(t, before, after, f, pending, recorded, retirement.clock, observedAfter)
			}
			terminalInstallerOrderAbsent(t, ctx, observer, f, in)
		})
	}
}
