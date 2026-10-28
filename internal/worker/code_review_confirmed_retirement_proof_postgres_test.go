package worker

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

// Each leaf is one real storage/predicate composition, not a publication,
// provider, Worker.poll, effect-delivery, drain or completed-review proof. The
// actual aware JobStore must classify only its locked original A. Its SQL proof
// must agree with the existing Go receipt validator, PLUS the caller's explicit
// confirmed-state precondition and this retirement's active-metadata condition.
// The latter two conditions intentionally are not inside the old Go helper.
type confirmedRetirementProofCase struct {
	name, receipt, publication, retained, metadataID string
	metadataStatus                                   models.CodeReviewSessionStatus
	completedAssessment                              bool
	incompleteCoverage, emptyBody, retryPending      bool
	helper, pending, assessmentFails                 bool
}

func confirmedRetirementProofReceipt(t *testing.T, f, foreign strandedReviewFixture, variant string) json.RawMessage {
	t.Helper()
	receipt := map[string]json.RawMessage{
		"assessment_id":        fullAssessmentDispatchJSON(t, f.assessment.String()),
		"input_digest":         fullAssessmentDispatchJSON(t, f.manifest.InputDigest),
		"submitted_commit_sha": fullAssessmentDispatchJSON(t, f.job.HeadSHA),
		"github_review_id":     json.RawMessage(`77`),
	}
	switch variant {
	case "":
	case "foreign assessment":
		receipt["assessment_id"] = fullAssessmentDispatchJSON(t, foreign.assessment.String())
	case "wrong digest":
		receipt["input_digest"] = fullAssessmentDispatchJSON(t, "synthetic-different-receipt-digest")
	case "wrong head":
		receipt["submitted_commit_sha"] = fullAssessmentDispatchJSON(t, "synthetic-different-receipt-head")
	case "wrong review":
		receipt["github_review_id"] = json.RawMessage(`78`)
	case "assessment number":
		receipt["assessment_id"] = json.RawMessage(`77`)
	case "digest boolean":
		receipt["input_digest"] = json.RawMessage(`true`)
	case "head object":
		receipt["submitted_commit_sha"] = json.RawMessage(`{}`)
	case "review string":
		receipt["github_review_id"] = json.RawMessage(`"77"`)
	case "review decimal":
		// PostgreSQL JSONB retains 77.0; Go's actual *int64 decoder rejects
		// it. Numeric SQL equality to bigint 77 must not widen that contract.
		receipt["github_review_id"] = json.RawMessage(`77.0`)
	case "review overflow":
		// Valid JSONB number, but outside int64. A boolean classification
		// must return false, not raise a cast/storage error or grant rights.
		receipt["github_review_id"] = json.RawMessage(`9223372036854775808`)
	case "review null":
		receipt["github_review_id"] = json.RawMessage(`null`)
	case "missing digest":
		delete(receipt, "input_digest")
	case "array":
		return json.RawMessage(`[]`)
	case "null":
		return json.RawMessage(`null`)
	default:
		t.Fatal("the finite receipt matrix must contain only explicitly representable retained JSONB inputs")
	}
	return fullAssessmentDispatchJSON(t, receipt)
}

// Stage/Reserve/Mark/Record are the actual existing store methods. Their real
// acceptance of a valid-JSONB but mismatched receipt is part of the fixture,
// not fabricated SQL results. Legal retained NULL field negatives are explicit
// narrow writes AFTER this real staged history; no schema guard is disabled.
func confirmedRetirementProofStage(t *testing.T, ctx context.Context, pool *pgxpool.Pool, f, foreign strandedReviewFixture, tt confirmedRetirementProofCase) {
	t.Helper()
	a, err := f.stores.CodeReviewAssessments.GetByID(ctx, f.job.OrgID, f.assessment)
	require.NoError(t, err, "read the genuine original full A before using the actual staged-publication store methods")
	body := "Synthetic original confirmed staged result"
	if tt.emptyBody {
		body = ""
	}
	completion := models.CodeReviewAssessmentCompletion{ResultOrigin: models.CodeReviewResultExecuted,
		CoverageComplete: !tt.incompleteCoverage, Decision: models.CodeReviewDecisionNeedsHumanReview, Acceptable: false,
		RiskReasonDetails: json.RawMessage(`[]`), StructuredOutcome: json.RawMessage(`{"synthetic":"retained original outcome"}`), RenderedBody: body}
	require.NoError(t, f.stores.CodeReviewAssessments.StageOutcome(ctx, f.job.OrgID, f.assessment, a.Generation, a.InputDigest, completion), "stage the real deterministic original outcome before any publication transition")
	if tt.publication != "not started" {
		require.NoError(t, f.stores.CodeReviewAssessments.ReservePublication(ctx, f.job.OrgID, f.assessment, a.Generation, a.InputDigest, f.job.HeadSHA), "reserve publication using the actual original head and retained input digest")
		if tt.publication != "reserved" {
			receipt := confirmedRetirementProofReceipt(t, f, foreign, tt.receipt)
			reviewID, reviewURL := int64(77), "https://example.invalid/review/77"
			if tt.publication == "uncertain with receipt" {
				// RecordPublication supports an uncertain retained observation
				// with receipt/identity. This is a real accepted method path,
				// not a fabricated reserved+confirmed-receipt contradiction.
				require.NoError(t, f.stores.CodeReviewAssessments.RecordPublication(ctx, f.job.OrgID, f.assessment, a.Generation, a.InputDigest,
					models.CodeReviewPublicationUncertain, receipt, &reviewID, &reviewURL), "retain a real uncertain observation whose identity alone must not confer confirmed-completion authority")
			} else {
				require.NoError(t, f.stores.CodeReviewAssessments.MarkPublicationAttemptUncertain(ctx, f.job.OrgID, f.assessment, a.Generation, a.InputDigest), "persist the actual uncertainty transition before accepting any confirmed observation")
				if tt.publication != "uncertain" {
					require.NoError(t, f.stores.CodeReviewAssessments.RecordPublication(ctx, f.job.OrgID, f.assessment, a.Generation, a.InputDigest,
						models.CodeReviewPublicationConfirmed, receipt, &reviewID, &reviewURL), "record the actual confirmed retained publication through its original assessment/digest CAS, without a real provider call")
				}
			}
		}
	}
	if tt.completedAssessment {
		require.NoError(t, f.stores.CodeReviewAssessments.Complete(ctx, f.job.OrgID, f.assessment, a.Generation, a.InputDigest, completion), "use actual assessment completion for the already-terminal A boundary, not an invented invalid terminal row")
	}
	var retainedQuery string
	switch tt.retained {
	case "":
	case "receipt SQL NULL":
		retainedQuery = `UPDATE code_review_revision_assessments SET publication_receipt=NULL WHERE org_id=$1 AND id=$2`
	case "provider ID SQL NULL":
		retainedQuery = `UPDATE code_review_revision_assessments SET github_review_id=NULL WHERE org_id=$1 AND id=$2`
	case "submitted head mismatch":
		retainedQuery = `UPDATE code_review_revision_assessments SET submitted_commit_sha='synthetic-different-submitted-head' WHERE org_id=$1 AND id=$2`
	case "submitted head SQL NULL":
		retainedQuery = `UPDATE code_review_revision_assessments SET submitted_commit_sha=NULL WHERE org_id=$1 AND id=$2`
	case "result origin SQL NULL":
		retainedQuery = `UPDATE code_review_revision_assessments SET result_origin=NULL WHERE org_id=$1 AND id=$2`
	case "decision SQL NULL":
		retainedQuery = `UPDATE code_review_revision_assessments SET decision=NULL WHERE org_id=$1 AND id=$2`
	case "acceptable SQL NULL":
		retainedQuery = `UPDATE code_review_revision_assessments SET acceptable=NULL WHERE org_id=$1 AND id=$2`
	case "body SQL NULL":
		retainedQuery = `UPDATE code_review_revision_assessments SET rendered_body=NULL WHERE org_id=$1 AND id=$2`
	case "outcome SQL NULL":
		retainedQuery = `UPDATE code_review_revision_assessments SET structured_outcome=NULL WHERE org_id=$1 AND id=$2`
	default:
		t.Fatal("the retained-field matrix must contain only explicit legal nullable column changes")
	}
	if retainedQuery != "" {
		tag, err := pool.Exec(ctx, retainedQuery, f.job.OrgID, f.assessment)
		require.NoError(t, err, "the retained malformed/missing field must actually be representable with every full-schema guard still enabled")
		require.Equal(t, int64(1), tag.RowsAffected(), "the retained-field negative must change exactly the real original assessment, not an absent projection")
	}
	var metadataReviewID *int64
	switch tt.metadataID {
	case "":
	case "matching":
		metadataReviewID = new(int64(77))
	case "different":
		metadataReviewID = new(int64(78))
	default:
		t.Fatal("the metadata provider-identity matrix must contain only NULL, matching and different actual IDs")
	}
	status := tt.metadataStatus
	if status == "" {
		status = models.CodeReviewSessionStatusRunning
	}
	tag, err := pool.Exec(ctx, `UPDATE code_review_session_metadata SET github_review_id=$3,status=$4,
		phase=CASE WHEN $4 IN ('completed','cancelled') THEN NULL ELSE phase END,
		completed_at=CASE WHEN $4 IN ('completed','cancelled') THEN statement_timestamp() ELSE completed_at END
		WHERE org_id=$1 AND id=$2`, f.job.OrgID, f.job.MetadataID, metadataReviewID, status)
	require.NoError(t, err, "retain the real optional metadata provider identity and active versus already-terminal owner boundary")
	require.Equal(t, int64(1), tag.RowsAffected(), "only exact original metadata may be staged for the proof; the same-session sibling stays independent")
}

func confirmedRetirementProofAssertGraph(t *testing.T, before, after json.RawMessage, f strandedReviewFixture, tt confirmedRetirementProofCase, failure db.CodeReviewTerminalFailure, pending uuid.UUID, stamp, afterClock time.Time) {
	t.Helper()
	expected, actual := terminalOwnerPointerPositiveDecode(t, before), terminalOwnerPointerPositiveDecode(t, after)
	terminalMetadata := tt.metadataStatus == models.CodeReviewSessionStatusCompleted || tt.metadataStatus == models.CodeReviewSessionStatusCancelled
	job := terminalOwnerPointerPositiveRow(t, expected, "jobs", f.jobID)
	actualJob := terminalOwnerPointerPositiveRow(t, actual, "jobs", f.jobID)
	require.Equal(t, fullAssessmentDispatchJSON(t, "running"), job["status"], "the exact original retained running attempt must actually retire")
	job["status"], job["last_error"], job["owner_kind"] = fullAssessmentDispatchJSON(t, "dead_letter"), fullAssessmentDispatchJSON(t, failure.JobError), fullAssessmentDispatchJSON(t, "worker")
	for _, field := range []string{"locked_by_node_id", "run_owner_id", "lock_token", "locked_at", "lease_expires_at"} {
		job[field] = json.RawMessage(`null`)
	}
	terminalOwnerPointerPositiveClock(t, job, actualJob, "completed_at", stamp)
	terminalOwnerPointerPositiveClock(t, job, actualJob, "updated_at", stamp)
	if tt.pending {
		assessment := terminalOwnerPointerPositiveRow(t, expected, "assessments", f.assessment)
		assessment["failure_detail"] = fullAssessmentDispatchJSON(t, db.CodeReviewConfirmedCompletionRecoveryPending)
	}
	if !tt.pending && !terminalMetadata {
		metadata := terminalOwnerPointerPositiveRow(t, expected, "metadata", f.job.MetadataID)
		actualMetadata := terminalOwnerPointerPositiveRow(t, actual, "metadata", f.job.MetadataID)
		metadata["status"], metadata["phase"], metadata["retry_at"] = fullAssessmentDispatchJSON(t, "failed"), json.RawMessage(`null`), json.RawMessage(`null`)
		if string(metadata["github_review_id"]) == "null" {
			metadata["decision"], metadata["acceptable"] = json.RawMessage(`null`), json.RawMessage(`null`)
		}
		metadata["failure_reason"] = fullAssessmentDispatchJSON(t, strings.TrimSpace(failure.Reason))
		metadata["status_code"] = fullAssessmentDispatchJSON(t, models.CodeReviewStatusCodeDispatchUnresolved)
		metadata["status_message"] = fullAssessmentDispatchJSON(t, "The review stopped with a reviewer dispatch still unresolved. Its original intent is retained for reconciliation; do not resend that command.")
		metadata["retryable_failure"] = fullAssessmentDispatchJSON(t, false)
		terminalOwnerPointerPositiveClock(t, metadata, actualMetadata, "last_error_at", stamp)
		terminalOwnerPointerPositiveClock(t, metadata, actualMetadata, "completed_at", stamp)
	}
	if tt.assessmentFails {
		assessment := terminalOwnerPointerPositiveRow(t, expected, "assessments", f.assessment)
		actualAssessment := terminalOwnerPointerPositiveRow(t, actual, "assessments", f.assessment)
		assessment["status"], assessment["failure_detail"] = fullAssessmentDispatchJSON(t, "failed"), fullAssessmentDispatchJSON(t, "dispatch_unresolved:"+strings.TrimSpace(failure.Reason))
		terminalOwnerPointerPositiveClock(t, assessment, actualAssessment, "completed_at", stamp)
	}
	if !terminalMetadata {
		child := terminalOwnerPointerPositiveRow(t, expected, "results", pending)
		actualChild := terminalOwnerPointerPositiveRow(t, actual, "results", pending)
		require.Equal(t, fullAssessmentDispatchJSON(t, "pending"), child["dispatch_state"], "even positive completion recovery must settle the genuinely original pending intent")
		child["dispatch_state"], child["dispatch_resolution"], child["dispatch_due_at"] = fullAssessmentDispatchJSON(t, "cancelled"), fullAssessmentDispatchJSON(t, "owner_cancelled"), json.RawMessage(`null`)
		var writer int
		require.NoError(t, json.Unmarshal(child["dispatch_writer_seq"], &writer), "retain exact original writer and prior attempt history before the one cancellation")
		child["dispatch_writer_seq"] = fullAssessmentDispatchJSON(t, writer+1)
		var updated time.Time
		require.NoError(t, json.Unmarshal(actualChild["dispatch_updated_at"], &updated), "read the actual later cancellation statement timestamp independently of metadata failure")
		require.True(t, !updated.Before(stamp) && !updated.After(afterClock), "pending cancellation must occur in a later fresh statement within the real original bounded retirement interval")
		child["dispatch_updated_at"] = actualChild["dispatch_updated_at"]
	}
	require.Equal(t, expected, actual, "only the exact job, ordinary eligible failure graph and explicitly permitted original pending settlement may change; positive recovery preserves every original metadata/assessment/parent field, unknown attempt, sibling, foreign binding, request, reservation, immutable wake/input, message, stamp and evidence column")
}

func TestCodeReviewConfirmedRetirementProofParityPostgres(t *testing.T) {
	t.Parallel()
	tests := []confirmedRetirementProofCase{
		{name: "confirmed exact receipt with matching metadata provider ID", metadataID: "matching", helper: true, pending: true},
		{name: "confirmed exact receipt with NULL-compatible metadata provider ID", helper: true, pending: true},
		{name: "queued exact metadata retains real retry-pending history", metadataStatus: models.CodeReviewSessionStatusQueued, retryPending: true, helper: true, pending: true},
		{name: "coverage flag does not add a restriction absent from the old receipt helper", incompleteCoverage: true, helper: true, pending: true},
		{name: "nonnil empty staged body retains exact old helper semantics", emptyBody: true, helper: true, pending: true},
		{name: "different metadata provider ID denies completion-recovery authority", metadataID: "different"},
		{name: "receipt names genuine foreign assessment", receipt: "foreign assessment"},
		{name: "receipt input digest differs", receipt: "wrong digest"},
		{name: "receipt submitted head differs", receipt: "wrong head"},
		{name: "receipt review identity differs", receipt: "wrong review"},
		{name: "receipt assessment identity has wrong JSON type", receipt: "assessment number"},
		{name: "receipt input digest has wrong JSON type", receipt: "digest boolean"},
		{name: "receipt submitted head has wrong JSON type", receipt: "head object"},
		{name: "receipt review identity is a string not an int64", receipt: "review string"},
		{name: "JSONB decimal 77.0 is not a Go int64 receipt", receipt: "review decimal"},
		{name: "JSONB number outside int64 is a false proof not a cast error", receipt: "review overflow"},
		{name: "receipt review identity is JSON null", receipt: "review null"},
		{name: "receipt required digest is absent", receipt: "missing digest"},
		{name: "receipt is legal JSONB array rather than object", receipt: "array"},
		{name: "receipt is legal JSONB null rather than a saved identity", receipt: "null"},
		{name: "retained receipt is SQL NULL", retained: "receipt SQL NULL"},
		{name: "retained assessment provider review ID is SQL NULL", retained: "provider ID SQL NULL"},
		{name: "retained submitted head differs from exact original head", retained: "submitted head mismatch"},
		{name: "retained submitted head is SQL NULL", retained: "submitted head SQL NULL"},
		{name: "retained staged result origin is SQL NULL", retained: "result origin SQL NULL"},
		{name: "retained staged decision is SQL NULL", retained: "decision SQL NULL"},
		{name: "retained staged acceptable is SQL NULL", retained: "acceptable SQL NULL"},
		{name: "retained staged rendered body is SQL NULL", retained: "body SQL NULL"},
		{name: "retained staged structured outcome is SQL NULL", retained: "outcome SQL NULL"},
		{name: "actual reserved publication retains ordinary eligible failure", publication: "reserved", assessmentFails: true},
		{name: "actual uncertain publication keeps protected assessment but no pending disposition", publication: "uncertain"},
		{name: "actual not-started publication retains ordinary eligible failure", publication: "not started", assessmentFails: true},
		{name: "legacy metadata ID alone cannot substitute for receipt", publication: "uncertain", metadataID: "matching"},
		{name: "actual uncertain receipt needs explicit confirmed-state precondition", publication: "uncertain with receipt", helper: true},
		{name: "already-completed metadata cannot acquire pending resurrection rights", metadataStatus: models.CodeReviewSessionStatusCompleted, metadataID: "matching", helper: true},
		{name: "already-cancelled metadata cannot acquire pending resurrection rights", metadataStatus: models.CodeReviewSessionStatusCancelled, helper: true},
		{name: "actual already-completed assessment is not awaiting local assessment completion", completedAssessment: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel() // Separate full-schema DB per leaf; root bounds aggregate capacity with -parallel=1.
			ctx := context.Background()
			pool := fullRecoveryPostgresPoolWithMaxConns(t, ctx, 2)
			require.Equal(t, int32(2), pool.Config().MaxConns, "exact locked scalar proof, retirement and independent observer must use literally two pool slots")
			originalState := "pending"
			if tt.retryPending {
				originalState = "retry pending"
			}
			f, foreign, children, foreignChildren := terminalDispatchOwnershipFixture(t, ctx, pool, originalState)
			var thread uuid.UUID
			require.NoError(t, pool.QueryRow(ctx, `SELECT id FROM session_threads WHERE org_id=$1 AND session_id=$2`, f.job.OrgID, f.job.SessionID).Scan(&thread), "reuse the actual original completed lane for nonempty retained unknown intent history")
			unknown := fullAssessmentDispatchChild(t, ctx, pool, f, thread, "unknown")
			children = append(children, terminalDispatchOwnershipChild{id: unknown, metadata: f.job.MetadataID, state: "unknown", attempts: 1, due: true})
			var pending uuid.UUID
			for _, child := range children {
				if child.metadata == f.job.MetadataID && child.state == "pending" {
					pending = child.id
				}
			}
			require.NotEqual(t, uuid.Nil, pending, "the original pending or retry-pending cancellation canary must genuinely exist before staging publication")
			confirmedRetirementProofStage(t, ctx, pool, f, foreign, tt)
			assessmentBefore, err := f.stores.CodeReviewAssessments.GetByID(ctx, f.job.OrgID, f.assessment)
			require.NoError(t, err, "read the exact original real stored assessment after every legal publication/retained-field fixture change")
			metadataBefore, err := f.stores.CodeReviews.GetByOutputKey(ctx, f.job.OrgID, f.job.OutputKey)
			require.NoError(t, err, "read exact original metadata rather than its newer same-session sibling")
			helper := confirmedFullAssessmentReceiptMatches(assessmentBefore, metadataBefore)
			require.Equal(t, tt.helper, helper, "the actual existing Go validator must classify the genuinely stored PostgreSQL values, including JSONB's exact number/type presentation")
			active := metadataBefore.Status == models.CodeReviewSessionStatusQueued || metadataBefore.Status == models.CodeReviewSessionStatusRunning
			expectedProof := helper && assessmentBefore.PublicationState == models.CodeReviewPublicationConfirmed && active
			require.Equal(t, tt.pending, expectedProof, "the expected SQL predicate is exactly old-helper semantics AND explicit confirmed-state precondition AND active exact metadata, not a new completed-success predicate")
			if tt.receipt == "review decimal" || tt.receipt == "review overflow" {
				var saved map[string]json.RawMessage
				require.NoError(t, json.Unmarshal(assessmentBefore.PublicationReceipt, &saved), "retain PostgreSQL's actual saved JSONB number text rather than testing the pre-storage receipt input")
				expectedNumber := json.RawMessage(`77.0`)
				if tt.receipt == "review overflow" {
					expectedNumber = json.RawMessage(`9223372036854775808`)
				}
				require.Equal(t, expectedNumber, saved["github_review_id"], "this real persisted numeric edge must not normalize into a different untested int64 representation")
			}
			reserved, err := pool.Acquire(ctx)
			require.NoError(t, err, "reserve the independent real observer so the actual whole retirement has exactly one available application connection")
			defer reserved.Release()
			before, err := terminalContentionSnapshot(ctx, reserved, f)
			require.NoError(t, err, "retain every complete original/sibling graph column after genuine staged publication and before retirement")
			foreignBefore, err := terminalContentionSnapshot(ctx, reserved, foreign)
			require.NoError(t, err, "retain every genuine foreign tenant row and its real original pending wake")
			terminalDispatchOwnershipAssertChildren(t, terminalOwnerPointerPositiveDecode(t, before), children)
			terminalDispatchOwnershipAssertChildren(t, terminalOwnerPointerPositiveDecode(t, foreignBefore), foreignChildren)
			observed := &fullAssessmentDispatchRunObserver{base: f.stores.ThreadSendDependencies.Transactions}
			jobs, err := db.NewJobStoreWithCodeReviewTerminalTransactions(pool, observed)
			require.NoError(t, err, "configure only the actual aware retirement store with the original existing bounded Run capability")
			failure := db.CodeReviewTerminalFailure{JobError: "Synthetic local completion attempt exhausted", Reason: "  Synthetic original completion attempt exhausted  ",
				Code: models.CodeReviewStatusCodeWorkerFailed, Message: "Synthetic ordinary stopped-attempt copy", Retryable: true}
			outcome, err := jobs.DeadLetterCodeReviewWithLease(ctx, f.job.OrgID, f.jobID, f.token, failure)
			require.NoError(t, err, "every legal positive/negative proof must settle through real SQL without malformed-type casts or query errors")
			fullAssessmentDispatchAssertRun(t, observed)
			require.True(t, outcome.Matches(f.job.OrgID, f.jobID, f.token), "the actual known commit must retire precisely the original retained controller attempt and lease")
			scope, hasOwner := outcome.Scope()
			require.True(t, hasOwner, "a malformed receipt is not malformed owner identity and must not silently downgrade to job-only")
			require.Equal(t, db.CodeReviewTerminalScope{OrgID: f.job.OrgID, JobID: f.jobID, LockToken: f.token, JobType: models.JobTypeRunCodeReview,
				SessionID: f.job.SessionID, MetadataID: f.job.MetadataID, RepositoryID: f.job.RepositoryID, PullRequestID: f.job.PullRequestID,
				PolicyID: f.job.PolicyID, AssessmentID: f.assessment, HeadSHA: f.job.HeadSHA, OutputKey: f.job.OutputKey, ReviewScope: models.CodeReviewScopeFull}, scope,
				"the sealed disposition must bind the entire real original owner rather than latest/sibling/foreign receipt identity")
			require.Equal(t, tt.pending, outcome.CompletionRecoveryPending(), "only the exact positively proved locked full confirmed/staged owner with active metadata may gain the sealed recovery-pending disposition")
			require.True(t, outcome.UnresolvedDispatch(), "real original pending and unknown history must be observed before settlement even when metadata business failure is suppressed")
			after, err := terminalContentionSnapshot(ctx, reserved, f)
			require.NoError(t, err, "independently read the entire committed original/sibling graph, including preserved parent/reservation and exact publication receipt")
			foreignAfter, err := terminalContentionSnapshot(ctx, reserved, foreign)
			require.NoError(t, err, "independently read every foreign row after exact original-owner retirement")
			var afterClock time.Time
			require.NoError(t, reserved.QueryRow(ctx, `SELECT statement_timestamp()`).Scan(&afterClock), "bound the actual later pending-cancellation statement by an independent postcommit database clock")
			confirmedRetirementProofAssertGraph(t, before, after, f, tt, failure, pending, observed.clock, afterClock)
			require.Equal(t, foreignBefore, foreignAfter, "no real foreign pending/input/wake/attempt, metadata, assessment, job, scheduler or evidence column may change or lend proof")
			metadata, changedMetadata := outcome.Metadata()
			terminalMetadata := tt.metadataStatus == models.CodeReviewSessionStatusCompleted || tt.metadataStatus == models.CodeReviewSessionStatusCancelled
			require.Equal(t, !tt.pending && !terminalMetadata, changedMetadata, "positive recovery and already-terminal history release no fabricated metadata failure or success descriptor")
			if changedMetadata {
				stored, err := f.stores.CodeReviews.GetByOutputKey(ctx, f.job.OrgID, f.job.OutputKey)
				require.NoError(t, err, "read the exact actually changed ordinary failure metadata descriptor independently after commit")
				require.Equal(t, stored, metadata, "ordinary negative proofs retain only their real known-committed failed metadata descriptor")
				require.Equal(t, models.CodeReviewSessionStatusFailed, metadata.Status, "receipt rejection must not manufacture completed-success authority")
			} else {
				require.Equal(t, models.CodeReviewSessionMetadata{}, metadata, "no preserved or already-terminal metadata may escape as a changed event")
			}
			assessment, changedAssessment := outcome.Assessment()
			require.Equal(t, tt.assessmentFails, changedAssessment, "ordinary not-started/reserved eligibility and protected/terminal publication fences must remain distinct from positive receipt recovery")
			if changedAssessment {
				stored, err := f.stores.CodeReviewAssessments.GetByID(ctx, f.job.OrgID, f.assessment)
				require.NoError(t, err, "read the actually failed eligible original assessment after known commit")
				require.Equal(t, stored, assessment, "the released failed assessment must exactly equal actual committed original storage")
				require.Equal(t, models.CodeReviewAssessmentFailed, assessment.Status, "ordinary eligible failure must not be relabeled successful or pending by absence of a publication receipt")
			} else {
				require.Equal(t, models.CodeReviewAssessment{}, assessment, "no protected confirmed/uncertain/terminal A may escape as a manufactured changed descriptor")
			}
		})
	}
}
