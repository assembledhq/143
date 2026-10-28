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

// These are storage/transaction ownership proofs, not Worker.poll, real Send,
// provider delivery, ACK ambiguity, notification or activation proofs. Each
// parallel leaf owns a separate full-schema database, never global max()+1
// factories. Exactly two checkouts suffice: one reserved observer and the
// existing supplied transaction runner's one application slot. Root may use
// -parallel=1 to bound aggregate PostgreSQL capacity across disposable databases.
type terminalDispatchOwnershipChild struct {
	id, metadata uuid.UUID
	state        string
	attempts     int
	due          bool
}

func terminalDispatchOwnershipFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool, originalState string) (strandedReviewFixture, strandedReviewFixture, []terminalDispatchOwnershipChild, []terminalDispatchOwnershipChild) {
	t.Helper()
	f := terminalOwnerPointerPositiveFixture(t, pool, false)
	foreign := seedStrandedReview(t, pool, "idle")
	var ledger int
	var dirty bool
	require.NoError(t, pool.QueryRow(ctx, `SELECT version,dirty FROM schema_migrations`).Scan(&ledger, &dirty), "read the actual complete migration ledger, not a raw SQL approximation of appended readiness")
	require.Equal(t, 392, ledger, "unresolved-owner positives require the actual appended status-schema readiness migration")
	require.False(t, dirty, "the ownership proof must not enable intents on a dirty migration ledger")
	tag, err := pool.Exec(ctx, `UPDATE caller_send_intent_control SET intents_enabled=true WHERE singleton AND schema_ready AND dispatch_status_ready`)
	require.NoError(t, err, "opt in only this isolated synthetic database after both actual schema readiness gates")
	require.Equal(t, int64(1), tag.RowsAffected(), "the new readiness gate must genuinely permit the synthetic fixture instead of silently skipping all Prepare-born rows")
	sibling := f
	require.NoError(t, pool.QueryRow(ctx, `SELECT id,head_sha,review_output_key FROM code_review_session_metadata WHERE org_id=$1 AND session_id=$2 AND id<>$3`, f.job.OrgID, f.job.SessionID, f.job.MetadataID).
		Scan(&sibling.job.MetadataID, &sibling.job.HeadSHA, &sibling.job.OutputKey), "retain the real separate same-session metadata owner created by the existing complete-graph fixture")
	require.NotEqual(t, f.job.MetadataID, sibling.job.MetadataID, "sibling pending work must be bound to a genuinely different metadata owner")
	require.NotEqual(t, f.job.OutputKey, sibling.job.OutputKey, "the sibling must retain a different original controller wake key")
	thread, foreignThread := uuid.New(), uuid.New()
	for _, row := range []struct {
		id      uuid.UUID
		fixture strandedReviewFixture
	}{
		{id: thread, fixture: f},
		{id: foreignThread, fixture: foreign},
	} {
		tag, err := pool.Exec(ctx, `INSERT INTO session_threads(id,org_id,session_id,agent_type,label,status,completed_at)
			VALUES($1,$2,$3,'claude_code','Synthetic retained ownership lane','completed',statement_timestamp())`, row.id, row.fixture.job.OrgID, row.fixture.job.SessionID)
		require.NoError(t, err, "create each real tenant-scoped lane before actual Prepare validates its foreign-key/provider/model correlation")
		require.Equal(t, int64(1), tag.RowsAffected(), "each original or foreign synthetic lane must really exist")
	}
	children := make([]terminalDispatchOwnershipChild, 0, 4)
	if originalState != "" {
		id := fullAssessmentDispatchChild(t, ctx, pool, f, thread, originalState)
		state, attempts, due := originalState, 1, true
		switch originalState {
		case "pending":
			attempts = 0
		case "retry pending":
			state = "pending"
		case "accepted":
			due = false
		}
		children = append(children, terminalDispatchOwnershipChild{id: id, metadata: f.job.MetadataID, state: state, attempts: attempts, due: due})
	}
	for _, state := range []string{"pending", "retry pending", "attempting"} {
		id := fullAssessmentDispatchChild(t, ctx, pool, sibling, thread, state)
		actualState, attempts := state, 1
		if state == "pending" {
			attempts = 0
		} else if state == "retry pending" {
			actualState = "pending"
		}
		children = append(children, terminalDispatchOwnershipChild{id: id, metadata: sibling.job.MetadataID, state: actualState, attempts: attempts, due: true})
	}
	foreignID := fullAssessmentDispatchChild(t, ctx, pool, foreign, foreignThread, "pending")
	foreignChildren := []terminalDispatchOwnershipChild{{id: foreignID, metadata: foreign.job.MetadataID, state: "pending", attempts: 0, due: true}}
	return f, foreign, children, foreignChildren
}

func terminalDispatchOwnershipAssertChildren(t *testing.T, graph terminalOwnerPointerPositiveGraph, children []terminalDispatchOwnershipChild) {
	t.Helper()
	for _, child := range children {
		row := terminalOwnerPointerPositiveRow(t, graph, "results", child.id)
		require.Equal(t, fullAssessmentDispatchJSON(t, 1), row["dispatch_version"], "each ownership canary must be a real version-one child, not an empty or all-NULL legacy result")
		require.Equal(t, fullAssessmentDispatchJSON(t, child.state), row["dispatch_state"], "the independent snapshot must contain the intended original/sibling/foreign state")
		require.Equal(t, fullAssessmentDispatchJSON(t, child.attempts), row["dispatch_attempt_seq"], "retry-pending and never-attempted pending must remain distinguishable durable histories")
		require.Equal(t, child.due, string(row["dispatch_due_at"]) != "null", "each unresolved canary must genuinely have work due while settled acceptance does not")
		var wake struct {
			JobType string `json:"job_type"`
			Queue   string `json:"queue"`
			Dedupe  string `json:"dedupe_key"`
			Payload struct {
				Metadata uuid.UUID `json:"metadata_id"`
				Output   string    `json:"review_output_key"`
			} `json:"payload"`
		}
		require.NoError(t, json.Unmarshal(row["dispatch_wake"], &wake), "independently read the actual immutable original controller binding retained by Prepare")
		require.Equal(t, child.metadata, wake.Payload.Metadata, "same-session children must retain their distinct actual metadata owners, not merely a different result identity")
		require.Equal(t, [3]string{"run_code_review", "agent", "code_review:" + wake.Payload.Output}, [3]string{wake.JobType, wake.Queue, wake.Dedupe}, "all canaries must carry the valid original controller wake envelope used by the real retirement predicate")
	}
}

func terminalDispatchOwnershipAssertGraph(t *testing.T, before, after json.RawMessage, f strandedReviewFixture, failure db.CodeReviewTerminalFailure, original uuid.UUID, cancel, unresolved bool, stamp, observedAfter time.Time) {
	t.Helper()
	expected, actual := terminalOwnerPointerPositiveDecode(t, before), terminalOwnerPointerPositiveDecode(t, after)
	job := terminalOwnerPointerPositiveRow(t, expected, "jobs", f.jobID)
	actualJob := terminalOwnerPointerPositiveRow(t, actual, "jobs", f.jobID)
	require.Equal(t, json.RawMessage(`"running"`), job["status"], "the actual retained original lease must transition, not replay an already-terminal result")
	job["status"], job["last_error"], job["owner_kind"] = fullAssessmentDispatchJSON(t, "dead_letter"), fullAssessmentDispatchJSON(t, failure.JobError), fullAssessmentDispatchJSON(t, "worker")
	for _, field := range []string{"locked_by_node_id", "run_owner_id", "lock_token", "locked_at", "lease_expires_at"} {
		job[field] = json.RawMessage(`null`)
	}
	terminalOwnerPointerPositiveClock(t, job, actualJob, "completed_at", stamp)
	terminalOwnerPointerPositiveClock(t, job, actualJob, "updated_at", stamp)
	metadata := terminalOwnerPointerPositiveRow(t, expected, "metadata", f.job.MetadataID)
	actualMetadata := terminalOwnerPointerPositiveRow(t, actual, "metadata", f.job.MetadataID)
	metadata["status"] = fullAssessmentDispatchJSON(t, "failed")
	for _, field := range []string{"phase", "decision", "acceptable", "retry_at"} {
		metadata[field] = json.RawMessage(`null`)
	}
	code, message, retryable := failure.Code, strings.TrimSpace(failure.Message), failure.Retryable
	assessmentReason := strings.TrimSpace(failure.Reason)
	if unresolved {
		code, message, retryable = models.CodeReviewStatusCodeDispatchUnresolved, "The review stopped with a reviewer dispatch still unresolved. Its original intent is retained for reconciliation; do not resend that command.", false
		assessmentReason = "dispatch_unresolved:" + assessmentReason
	}
	metadata["failure_reason"], metadata["status_code"], metadata["status_message"] = fullAssessmentDispatchJSON(t, strings.TrimSpace(failure.Reason)), fullAssessmentDispatchJSON(t, code), fullAssessmentDispatchJSON(t, message)
	metadata["retryable_failure"] = fullAssessmentDispatchJSON(t, retryable)
	terminalOwnerPointerPositiveClock(t, metadata, actualMetadata, "last_error_at", stamp)
	terminalOwnerPointerPositiveClock(t, metadata, actualMetadata, "completed_at", stamp)
	assessment := terminalOwnerPointerPositiveRow(t, expected, "assessments", f.assessment)
	actualAssessment := terminalOwnerPointerPositiveRow(t, actual, "assessments", f.assessment)
	assessment["status"], assessment["failure_detail"] = fullAssessmentDispatchJSON(t, "failed"), fullAssessmentDispatchJSON(t, assessmentReason)
	terminalOwnerPointerPositiveClock(t, assessment, actualAssessment, "completed_at", stamp)
	if cancel {
		child := terminalOwnerPointerPositiveRow(t, expected, "results", original)
		actualChild := terminalOwnerPointerPositiveRow(t, actual, "results", original)
		require.Equal(t, fullAssessmentDispatchJSON(t, "pending"), child["dispatch_state"], "pending-only cancellation must begin with a real original-owned pending intent")
		child["dispatch_state"], child["dispatch_resolution"], child["dispatch_due_at"] = fullAssessmentDispatchJSON(t, "cancelled"), fullAssessmentDispatchJSON(t, "owner_cancelled"), json.RawMessage(`null`)
		var seq int
		require.NoError(t, json.Unmarshal(child["dispatch_writer_seq"], &seq), "read the original exact durable writer sequence before pending cancellation")
		child["dispatch_writer_seq"] = fullAssessmentDispatchJSON(t, seq+1)
		var updated time.Time
		require.NoError(t, json.Unmarshal(actualChild["dispatch_updated_at"], &updated), "read the actual separate cancellation statement timestamp after metadata failure")
		require.True(t, !updated.Before(stamp) && !updated.After(observedAfter), "cancellation must have the actual original transaction's later statement timestamp bounded by independent postcommit observation")
		child["dispatch_updated_at"] = actualChild["dispatch_updated_at"]
	}
	require.Equal(t, expected, actual, "only the exact original lease, metadata/assessment failure and optional original pending cancellation may change; every complete sibling row, attempted evidence, immutable command/wake, legacy output, scheduler, message, stamp and unrelated column must remain byte-exact")
}

func TestCodeReviewTerminalDispatchOwnershipPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name          string
		originalState string
		cancel        bool
		unresolved    bool
	}{
		{name: "legacy original ignores all live same-session sibling states"},
		{name: "settled accepted original ignores all live same-session sibling states", originalState: "accepted"},
		{name: "original fresh pending is cancelled without touching siblings", originalState: "pending", cancel: true, unresolved: true},
		{name: "original retry pending preserves attempt evidence when cancelled", originalState: "retry pending", cancel: true, unresolved: true},
		{name: "original attempting remains unresolved with all evidence retained", originalState: "attempting", unresolved: true},
		{name: "original unknown retains observation deadline and all evidence", originalState: "unknown", unresolved: true},
		{name: "original attention retains fixed deadline and all evidence", originalState: "attention", unresolved: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			pool := fullRecoveryPostgresPoolWithMaxConns(t, ctx, 2)
			require.Equal(t, int32(2), pool.Config().MaxConns, "uncontended exact-owner retirement must use literally two actual pool slots without hidden headroom")
			f, foreign, children, foreignChildren := terminalDispatchOwnershipFixture(t, ctx, pool, tt.originalState)
			var original uuid.UUID
			for _, child := range children {
				if child.metadata == f.job.MetadataID {
					original = child.id
				}
			}
			reserved, err := pool.Acquire(ctx)
			require.NoError(t, err, "reserve the independent observer before the supplied real application Run begins")
			defer reserved.Release()
			before, err := terminalContentionSnapshot(ctx, reserved, f)
			require.NoError(t, err, "retain every actual original-tenant graph row before retirement, including nonempty sibling children")
			foreignBefore, err := terminalContentionSnapshot(ctx, reserved, foreign)
			require.NoError(t, err, "independently retain the complete genuine foreign tenant graph rather than only a count")
			terminalDispatchOwnershipAssertChildren(t, terminalOwnerPointerPositiveDecode(t, before), children)
			terminalDispatchOwnershipAssertChildren(t, terminalOwnerPointerPositiveDecode(t, foreignBefore), foreignChildren)
			observed := &fullAssessmentDispatchRunObserver{base: f.stores.ThreadSendDependencies.Transactions}
			jobs, err := db.NewJobStoreWithCodeReviewTerminalTransactions(pool, observed)
			require.NoError(t, err, "supply the actual aware JobStore with its explicit existing bounded transaction capability")
			failure := db.CodeReviewTerminalFailure{JobError: "Synthetic original dispatch ownership failure", Reason: "  Synthetic original owner stopped  ", Code: models.CodeReviewStatusCodeWorkerFailed, Message: "  Synthetic ordinary worker failure copy  ", Retryable: true}
			outcome, err := jobs.DeadLetterCodeReviewWithLease(ctx, f.job.OrgID, f.jobID, f.token, failure)
			require.NoError(t, err, "actual committed retirement must accept only original-owned dispatch uncertainty on the real appended status schema")
			fullAssessmentDispatchAssertRun(t, observed)
			require.True(t, outcome.Owned(), "the real retirement must actually own and retire the retained original leased controller")
			require.True(t, outcome.Matches(f.job.OrgID, f.jobID, f.token), "the sealed outcome must prove known commit for precisely the original tenant, job and lease token")
			scope, hasOwner := outcome.Scope()
			require.True(t, hasOwner, "valid exact full ownership must never degrade to a malformed-job-only fallback")
			require.Equal(t, db.CodeReviewTerminalScope{OrgID: f.job.OrgID, JobID: f.jobID, LockToken: f.token, JobType: models.JobTypeRunCodeReview,
				SessionID: f.job.SessionID, MetadataID: f.job.MetadataID, RepositoryID: f.job.RepositoryID, PullRequestID: f.job.PullRequestID,
				PolicyID: f.job.PolicyID, AssessmentID: f.assessment, HeadSHA: f.job.HeadSHA, OutputKey: f.job.OutputKey, ReviewScope: models.CodeReviewScopeFull}, scope,
				"the known-committed outcome must retain the complete original controller identity rather than borrowing its sibling's owner")
			require.Equal(t, tt.unresolved, outcome.UnresolvedDispatch(), "same-session sibling uncertainty must never mark a settled/legacy original unresolved; actual original uncertainty must remain explicit even when its pending row is cancelled")
			after, err := terminalContentionSnapshot(ctx, reserved, f)
			require.NoError(t, err, "read the complete actually committed original graph independently from the transaction owner")
			foreignAfter, err := terminalContentionSnapshot(ctx, reserved, foreign)
			require.NoError(t, err, "read every original foreign row independently after original-owner retirement")
			var observedAfter time.Time
			require.NoError(t, reserved.QueryRow(ctx, `SELECT statement_timestamp()`).Scan(&observedAfter), "bound the actual pending-cancellation statement clock by independent postcommit database observation")
			terminalDispatchOwnershipAssertGraph(t, before, after, f, failure, original, tt.cancel, tt.unresolved, observed.clock, observedAfter)
			require.Equal(t, foreignBefore, foreignAfter, "the entire real foreign tenant graph, including its Prepare-born pending row and original wake, must remain byte-identical")
			metadata, metadataChanged := outcome.Metadata()
			require.True(t, metadataChanged, "actual original active metadata failure must return its real changed descriptor")
			storedMetadata, err := f.stores.CodeReviews.GetByOutputKey(ctx, f.job.OrgID, f.job.OutputKey)
			require.NoError(t, err, "independently read the exact original metadata instead of adopting the newest same-session sibling")
			require.Equal(t, storedMetadata, metadata, "the returned complete metadata descriptor must equal actual committed original storage, including exact ordinary or unresolved code/copy")
			assessment, assessmentChanged := outcome.Assessment()
			require.True(t, assessmentChanged, "the eligible exact full assessment must return its actual changed failure descriptor")
			storedAssessment, err := f.stores.CodeReviewAssessments.GetByID(ctx, f.job.OrgID, f.assessment)
			require.NoError(t, err, "read the real original assessment without latest-owner discovery")
			require.Equal(t, storedAssessment, assessment, "the returned complete assessment must equal actual committed original evidence and the exact unresolved-prefix decision")
		})
	}
}
