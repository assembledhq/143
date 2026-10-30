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

// These are finite actual stored-JSONB decoder-parity proofs, not a new
// canonical-only receipt contract, provider delivery, Worker.poll, recovery
// scheduling, effect delivery or activation proof. Each parallel leaf owns its
// full-schema database and literally two pool slots: one independent observer
// and the existing bounded transaction owner's one slot. No query mock, extra
// pool, new timeout or callback retry policy supplies either classification.
type confirmedReceiptAliasCase struct {
	name, receipt             string
	storedBefore, storedAfter string
	want                      bool
}

func confirmedReceiptAliasInput(t *testing.T, f strandedReviewFixture, variant string) json.RawMessage {
	t.Helper()
	id := string(fullAssessmentDispatchJSON(t, f.assessment.String()))
	digest := string(fullAssessmentDispatchJSON(t, f.manifest.InputDigest))
	head := string(fullAssessmentDispatchJSON(t, f.job.HeadSHA))
	canonical := `"assessment_id":` + id + `,"input_digest":` + digest + `,"submitted_commit_sha":` + head + `,"github_review_id":77`
	var body string
	switch variant {
	case "uppercase only":
		body = `"ASSESSMENT_ID":` + id + `,"INPUT_DIGEST":` + digest + `,"SUBMITTED_COMMIT_SHA":` + head + `,"GITHUB_REVIEW_ID":77`
	case "unicode only":
		body = `"aſſeſſment_id":` + id + `,"input_digest":` + digest + `,"submitted_commit_sha":` + head + `,"github_review_id":77`
	case "assessment wrong type":
		body = canonical + `,"ASSESSMENT_ID":17`
	case "digest wrong type":
		body = canonical + `,"INPUT_DIGEST":true`
	case "head wrong type":
		body = canonical + `,"SUBMITTED_COMMIT_SHA":{}`
	case "review wrong type":
		body = canonical + `,"GITHUB_REVIEW_ID":"77"`
	case "string null alias":
		body = canonical + `,"aſſeſſment_id":null`
	case "review pointer null":
		// PostgreSQL stores equal-length keys in byte order, so the valid
		// uppercase value is decoded before canonical null clears *int64.
		body = `"github_review_id":null,"GITHUB_REVIEW_ID":77,"assessment_id":` + id + `,"input_digest":` + digest + `,"submitted_commit_sha":` + head
	case "differing unicode alias input first":
		// Go sees the persisted JSONB order, not this caller's member order.
		body = `"aſſeſſment_id":"synthetic-different-assessment",` + canonical
	case "differing unicode alias input last":
		body = canonical + `,"aſſeſſment_id":"synthetic-different-assessment"`
	case "review decimal alias":
		body = canonical + `,"GITHUB_REVIEW_ID":77.0`
	case "review overflow alias":
		body = canonical + `,"GITHUB_REVIEW_ID":9223372036854775808`
	default:
		t.Fatal("the finite alias matrix must name an explicitly represented legal JSONB receipt")
	}
	raw := json.RawMessage(`{` + body + `}`)
	require.True(t, json.Valid(raw), "every alias case must be legal JSON before actual publication storage, not a fabricated decoder error")
	return raw
}

func confirmedReceiptAliasStage(t *testing.T, ctx context.Context, f strandedReviewFixture, receipt json.RawMessage) {
	t.Helper()
	a, err := f.stores.CodeReviewAssessments.GetByID(ctx, f.job.OrgID, f.assessment)
	require.NoError(t, err, "read the original full assessment before its real staged publication transitions")
	completion := models.CodeReviewAssessmentCompletion{ResultOrigin: models.CodeReviewResultExecuted,
		CoverageComplete: true, Decision: models.CodeReviewDecisionNeedsHumanReview, Acceptable: false,
		RiskReasonDetails: json.RawMessage(`[]`), StructuredOutcome: json.RawMessage(`{"synthetic":"retained alias parity outcome"}`),
		RenderedBody: "Synthetic original alias parity staged result"}
	require.NoError(t, f.stores.CodeReviewAssessments.StageOutcome(ctx, f.job.OrgID, f.assessment, a.Generation, a.InputDigest, completion), "stage the real original outcome without bypassing an assessment state or immutable-origin guard")
	require.NoError(t, f.stores.CodeReviewAssessments.ReservePublication(ctx, f.job.OrgID, f.assessment, a.Generation, a.InputDigest, f.job.HeadSHA), "reserve the exact actual original head and digest before retained publication")
	require.NoError(t, f.stores.CodeReviewAssessments.MarkPublicationAttemptUncertain(ctx, f.job.OrgID, f.assessment, a.Generation, a.InputDigest), "make the actual supported uncertainty transition before recording a confirmed observation")
	reviewID, reviewURL := int64(77), "https://example.invalid/review/77"
	require.NoError(t, f.stores.CodeReviewAssessments.RecordPublication(ctx, f.job.OrgID, f.assessment, a.Generation, a.InputDigest,
		models.CodeReviewPublicationConfirmed, receipt, &reviewID, &reviewURL), "the actual guarded publication method must accept this legal alias receipt without a real provider, projection substitution or direct receipt overwrite")
}

func confirmedReceiptAliasPreparation(t *testing.T, ctx context.Context, pool *pgxpool.Pool, f strandedReviewFixture, thread uuid.UUID) db.CodeReviewDispatchPreparation {
	t.Helper()
	var payload json.RawMessage
	require.NoError(t, pool.QueryRow(ctx, `SELECT payload FROM jobs WHERE org_id=$1 AND id=$2
		AND queue='agent' AND job_type='run_code_review' AND status='running' AND dedupe_key=$3 AND lock_token=$4`,
		f.job.OrgID, f.jobID, "code_review:"+f.job.OutputKey, f.token).Scan(&payload), "freeze the entire actual original running producer payload and its real non-NULL wake key")
	result := uuid.New()
	key := "cr-dispatch:v1:" + result.String()
	body := "Synthetic new alias parity installer evidence"
	in := db.CodeReviewDispatchPreparation{ResultID: result, SessionID: f.job.SessionID, MetadataID: f.job.MetadataID, ThreadID: thread,
		AgentProvider: "claude_code", Version: 1, Key: key, Digest: strings.Repeat("a", 64), ReviewOutputKey: f.job.OutputKey,
		RawOutput: &body, StructuredResult: json.RawMessage(`{"synthetic":"alias-parity"}`)}
	in.Input = fullAssessmentDispatchJSON(t, map[string]any{"version": 1, "purpose": db.CallerSendStampCodeReviewDispatch,
		"org_id": f.job.OrgID, "session_id": f.job.SessionID, "thread_id": thread, "client_message_id": key, "message": "Synthetic new alias parity command"})
	in.Wake = fullAssessmentDispatchJSON(t, map[string]any{"job_type": "run_code_review", "queue": "agent", "dedupe_key": "code_review:" + f.job.OutputKey, "payload": payload})
	return in
}

func TestCodeReviewConfirmedReceiptAliasParityPostgres(t *testing.T) {
	t.Parallel()
	tests := []confirmedReceiptAliasCase{
		{name: "uppercase aliases alone satisfy the existing struct decoder", receipt: "uppercase only", want: true},
		{name: "Unicode long-s alias alone satisfies Go simple folding", receipt: "unicode only", want: true},
		{name: "recognized assessment alias type error remains fatal beside correct canonical value", receipt: "assessment wrong type"},
		{name: "recognized digest alias type error remains fatal beside correct canonical value", receipt: "digest wrong type"},
		{name: "recognized head alias type error remains fatal beside correct canonical value", receipt: "head wrong type"},
		{name: "recognized review alias type error remains fatal beside correct canonical value", receipt: "review wrong type"},
		{name: "string null alias does not clear an earlier canonical string", receipt: "string null alias", storedBefore: "assessment_id", storedAfter: "aſſeſſment_id", want: true},
		{name: "canonical null clears the earlier valid review pointer alias", receipt: "review pointer null", storedBefore: "GITHUB_REVIEW_ID", storedAfter: "github_review_id"},
		{name: "persisted Unicode alias overrides canonical despite caller putting alias first", receipt: "differing unicode alias input first", storedBefore: "assessment_id", storedAfter: "aſſeſſment_id"},
		{name: "persisted Unicode alias overrides canonical when caller puts alias last", receipt: "differing unicode alias input last", storedBefore: "assessment_id", storedAfter: "aſſeſſment_id"},
		{name: "recognized decimal alias leaves sticky int64 decode error", receipt: "review decimal alias"},
		{name: "recognized overflow alias leaves sticky int64 decode error", receipt: "review overflow alias"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel() // Separate full-schema DB per leaf; root may bound aggregate capacity with -parallel=1.
			ctx := context.Background()
			pool := fullRecoveryPostgresPoolWithMaxConns(t, ctx, 2)
			require.Equal(t, int32(2), pool.Config().MaxConns, "receipt parity must use literally two actual pool slots, with no hidden connection reserve")
			f, foreign, children, foreignChildren := terminalDispatchOwnershipFixture(t, ctx, pool, "pending")
			var thread uuid.UUID
			require.NoError(t, pool.QueryRow(ctx, `SELECT id FROM session_threads WHERE org_id=$1 AND session_id=$2`, f.job.OrgID, f.job.SessionID).Scan(&thread), "reuse the real original completed lane for actual Prepare and retained unknown history")
			unknown := fullAssessmentDispatchChild(t, ctx, pool, f, thread, "unknown")
			children = append(children, terminalDispatchOwnershipChild{id: unknown, metadata: f.job.MetadataID, state: "unknown", attempts: 1, due: true})
			var pending uuid.UUID
			for _, child := range children {
				if child.metadata == f.job.MetadataID && child.state == "pending" {
					pending = child.id
				}
			}
			require.NotEqual(t, uuid.Nil, pending, "the original pending cancellation canary must really exist alongside independent sibling and unknown histories")
			in := confirmedReceiptAliasPreparation(t, ctx, pool, f, thread)
			receipt := confirmedReceiptAliasInput(t, f, tt.receipt)
			confirmedReceiptAliasStage(t, ctx, f, receipt)
			assessmentBefore, err := f.stores.CodeReviewAssessments.GetByID(ctx, f.job.OrgID, f.assessment)
			require.NoError(t, err, "read actual persisted JSONB through the unchanged assessment accessor, not the caller's pre-storage member order")
			metadataBefore, err := f.stores.CodeReviews.GetByOutputKey(ctx, f.job.OrgID, f.job.OutputKey)
			require.NoError(t, err, "read only exact original metadata rather than a newer same-session sibling")
			require.Equal(t, models.CodeReviewPublicationConfirmed, assessmentBefore.PublicationState, "the actual store must retain the explicit confirmed-state prerequisite")
			require.Equal(t, models.CodeReviewSessionStatusRunning, metadataBefore.Status, "both consumers must see the same genuinely active exact owner")
			require.Nil(t, metadataBefore.GitHubReviewID, "the metadata identity is intentionally NULL-compatible, not an independent authority overriding receipt decoding")
			var inputMembers, savedMembers map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(receipt, &inputMembers), "decode the legal input object only to pin every intended distinct canonical or alias member")
			require.NoError(t, json.Unmarshal(assessmentBefore.PublicationReceipt, &savedMembers), "independently inspect every actual retained JSONB member and its precise value/type")
			require.Equal(t, inputMembers, savedMembers, "PostgreSQL must genuinely retain every differently spelled alias and numeric/type edge rather than collapse it as a same-key duplicate")
			if tt.storedBefore != "" {
				first := strings.Index(string(assessmentBefore.PublicationReceipt), `"`+tt.storedBefore+`"`)
				last := strings.Index(string(assessmentBefore.PublicationReceipt), `"`+tt.storedAfter+`"`)
				require.GreaterOrEqual(t, first, 0, "the expected earlier member must be present in actual stored JSONB bytes")
				require.Greater(t, last, first, "the literal oracle must be exercised against observed PostgreSQL key order, not pre-storage order or canonical-key priority")
			}
			require.Equal(t, tt.want, confirmedFullAssessmentReceiptMatches(assessmentBefore, metadataBefore), "the unchanged Go reference must match this literal expected result on the actual saved bytes, including sticky errors, Unicode folding and null/pointer semantics")
			reserved, err := pool.Acquire(ctx)
			require.NoError(t, err, "reserve only an independent observer for these direct calls; no Worker.poll or lease-renewal capacity is claimed")
			defer reserved.Release()
			before, err := terminalContentionSnapshot(ctx, reserved, f)
			require.NoError(t, err, "retain every original and sibling graph column after real publication and before either predicate consumer")
			foreignBefore, err := terminalContentionSnapshot(ctx, reserved, foreign)
			require.NoError(t, err, "retain the complete nonempty foreign tenant graph and its actual pending wake")
			terminalDispatchOwnershipAssertChildren(t, terminalOwnerPointerPositiveDecode(t, before), children)
			terminalDispatchOwnershipAssertChildren(t, terminalOwnerPointerPositiveDecode(t, foreignBefore), foreignChildren)
			terminalInstallerOrderAbsent(t, ctx, reserved, f, in)
			stop := errors.New("synthetic alias parity installer probe must roll back")
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
				var snapshotErr error
				staged, snapshotErr = terminalContentionSnapshot(unitCtx, tx, f)
				if snapshotErr != nil {
					return snapshotErr
				}
				return stop
			})
			fullAssessmentDispatchAssertRun(t, installer)
			afterProbe, err := terminalContentionSnapshot(ctx, reserved, f)
			require.NoError(t, err, "observe actual installer rollback independently after the supplied Run has settled")
			require.Equal(t, before, afterProbe, "even an allowed provisional NEW child must actually roll back; the receipt probe cannot mutate any original or sibling graph column")
			foreignAfterProbe, err := terminalContentionSnapshot(ctx, reserved, foreign)
			require.NoError(t, err, "observe the full foreign graph independently after actual Prepare settlement")
			require.Equal(t, foreignBefore, foreignAfterProbe, "no Prepare outcome may borrow or mutate a real foreign owner, pending intent, wake or evidence field")
			terminalInstallerOrderAbsent(t, ctx, reserved, f, in)
			// Exercise BOTH actual consumers before asserting their expected
			// classifications, so old-source RED is not merely an installer
			// refusal that prevents the real retirement from being attempted.
			retirement := &fullAssessmentDispatchRunObserver{base: f.stores.ThreadSendDependencies.Transactions}
			jobs, err := db.NewJobStoreWithCodeReviewTerminalTransactions(pool, retirement)
			require.NoError(t, err, "use the actual supplied bounded terminal transaction owner without a second pool or replacement policy")
			failure := db.CodeReviewTerminalFailure{JobError: "Synthetic alias parity completion attempt exhausted", Reason: "  Synthetic original alias parity attempt exhausted  ",
				Code: models.CodeReviewStatusCodeWorkerFailed, Message: "Synthetic ordinary alias parity stopped-attempt copy", Retryable: true}
			outcome, retireErr := jobs.DeadLetterCodeReviewWithLease(ctx, f.job.OrgID, f.jobID, f.token, failure)
			fullAssessmentDispatchAssertRun(t, retirement)
			after, err := terminalContentionSnapshot(ctx, reserved, f)
			require.NoError(t, err, "read the entire actual committed original/sibling graph after retirement, independent of either returned classification")
			foreignAfter, err := terminalContentionSnapshot(ctx, reserved, foreign)
			require.NoError(t, err, "read every real foreign row after the exact original retirement")
			var afterClock time.Time
			require.NoError(t, reserved.QueryRow(ctx, `SELECT statement_timestamp()`).Scan(&afterClock), "independently bound the actual later original pending-cancellation statement")
			require.NoError(t, retireErr, "every representable alias case must classify through actual SQL without treating legal receipt values as query errors")
			require.True(t, outcome.Matches(f.job.OrgID, f.jobID, f.token), "only the exact known-committed original retained lease may release a terminal carrier")
			scope, owned := outcome.Scope()
			require.True(t, owned, "receipt rejection must not erase an otherwise fully verified original metadata/assessment owner")
			require.Equal(t, db.CodeReviewTerminalScope{OrgID: f.job.OrgID, JobID: f.jobID, LockToken: f.token, JobType: models.JobTypeRunCodeReview,
				SessionID: f.job.SessionID, MetadataID: f.job.MetadataID, RepositoryID: f.job.RepositoryID, PullRequestID: f.job.PullRequestID,
				PolicyID: f.job.PolicyID, AssessmentID: f.assessment, HeadSHA: f.job.HeadSHA, OutputKey: f.job.OutputKey, ReviewScope: models.CodeReviewScopeFull}, scope,
				"the receipt can grant recovery only to the entire exact original tuple, never a sibling or foreign binding")
			require.True(t, outcome.UnresolvedDispatch(), "actual original pending and unknown histories must be observed before original-only pending settlement")
			require.Equal(t, foreignBefore, foreignAfter, "neither decoder disposition may change any complete foreign tenant row or original foreign wake")
			if tt.want {
				require.Equal(t, db.ErrCodeReviewDispatchPreparationIneligible, prepareErr, "a reference-valid saved receipt must forbid NEW dispatch installation under the same exact original owner")
				require.Equal(t, db.ErrCodeReviewDispatchPreparationIneligible, installErr, "the actual Run must return that ordinary nonretryable eligibility refusal, not synthesize a commit or retry")
				require.Equal(t, db.PreparedCodeReviewDispatch{}, prepared, "ineligible confirmed completion must release no provisional child or wake identity")
				require.Nil(t, staged, "an ineligible NEW child must never reach the staged-success graph observation")
			} else {
				require.NoError(t, prepareErr, "a recognized-alias decode error or mismatched final value must deny completion authority, leaving ordinary active-owner installation eligible")
				require.Equal(t, stop, installErr, "a successful actual NEW child insert must roll back through this exact finite nonretry sentinel, not an unknown or committed outcome")
				require.Equal(t, in.ResultID, prepared.Result().ID, "the provisional result must be the genuine NEW preminted child from actual Prepare")
				require.Equal(t, uuid.Nil, prepared.WakeJobID(), "actual Prepare must dedupe against the retained running original-key controller without inventing another wake")
				require.False(t, prepared.WakeInserted(), "wake dedupe cannot be relabeled accepted delivery or a new controller")
				terminalInstallerOrderAssertNewChild(t, before, staged, in)
			}
			require.Equal(t, tt.want, outcome.CompletionRecoveryPending(), "retirement's sealed positive disposition must exactly match the unchanged Go decoder on the same actual persisted JSONB bytes")
			confirmedRetirementProofAssertGraph(t, before, after, f, confirmedRetirementProofCase{pending: tt.want}, failure, pending, retirement.clock, afterClock)
			metadata, changedMetadata := outcome.Metadata()
			require.Equal(t, !tt.want, changedMetadata, "only ordinary receipt rejection may release an actual committed failed-metadata descriptor")
			if changedMetadata {
				stored, err := f.stores.CodeReviews.GetByOutputKey(ctx, f.job.OrgID, f.job.OutputKey)
				require.NoError(t, err, "read the exact ordinary failed original metadata through its real output-key accessor after commit")
				require.Equal(t, stored, metadata, "the changed descriptor must equal the whole actually committed original metadata row")
			} else {
				require.Equal(t, models.CodeReviewSessionMetadata{}, metadata, "positive recovery cannot manufacture a changed metadata failure or success event")
			}
			assessment, changedAssessment := outcome.Assessment()
			require.False(t, changedAssessment, "a real confirmed publication remains protected from fabricated ordinary assessment failure even when its receipt is invalid")
			require.Equal(t, models.CodeReviewAssessment{}, assessment, "no protected original assessment may escape as a manufactured changed descriptor")
		})
	}
}
