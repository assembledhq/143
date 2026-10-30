package worker

import (
	"context"
	"encoding/json"
	"errors"
	"sync/atomic"
	"testing"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/jobctx"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

// Invalid JSON syntax cannot inhabit a JSONB column. These are actual persisted
// valid-JSONB poison scopes, not invented projection/Scan results or cast errors.
// Every leaf uses actual Worker.poll, its actual first claim and renewal reserve,
// and the aware JobStore's actual existing bounded Run. No observer connection
// is held across poll: literal max2 includes renewal capacity, not hidden slots.
func TestCodeReviewTerminalPoisonScopeJobOnlyPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, poison string
	}{
		{name: "actual JSONB array", poison: "array"},
		{name: "UUID-shaped nonhex metadata id", poison: "malformed metadata"},
		{name: "required policy field absent", poison: "missing field"},
		{name: "exact metadata genuinely absent", poison: "missing metadata"},
		{name: "existing metadata does not match payload PR", poison: "wrong PR"},
		{name: "existing metadata does not match original payload key", poison: "wrong key"},
		{name: "payload organization belongs to genuine foreign tenant", poison: "wrong payload org"},
		{name: "exact recheck assessment genuinely absent", poison: "missing recheck"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel() // Each leaf owns its full-schema DB; root bounds aggregate capacity with -parallel=1.
			ctx := context.Background()
			pool := fullRecoveryPostgresPoolWithMaxConns(t, ctx, 2)
			require.Equal(t, int32(2), pool.Config().MaxConns, "actual poll/claim/renewal/retirement must fit literally two connections without a held observer")
			f, foreign, children, foreignChildren := terminalScopeMatrixFixture(t, ctx, pool)
			original := fullAssessmentDispatchJSON(t, f.job)
			var object map[string]json.RawMessage
			require.NoError(t, json.Unmarshal(original, &object), "retain the otherwise complete real original payload before changing one finite poison field")
			var siblingKey string
			require.NoError(t, pool.QueryRow(ctx, `SELECT review_output_key FROM code_review_session_metadata WHERE org_id=$1 AND session_id=$2 AND id<>$3`, f.job.OrgID, f.job.SessionID, f.job.MetadataID).Scan(&siblingKey), "retain the genuinely existing tempting sibling key while its own metadata/results remain nonempty")
			kind := models.JobTypeRunCodeReview
			var payload json.RawMessage
			switch tt.poison {
			case "array":
				payload = json.RawMessage(`[]`)
			case "malformed metadata":
				const malformed = "zzzzzzzz-zzzz-zzzz-zzzz-zzzzzzzzzzzz"
				require.Equal(t, 36, len(malformed), "the malformed UUID must reach actual canonical parsing instead of only failing the scalar projection length gate")
				object["metadata_id"] = fullAssessmentDispatchJSON(t, malformed)
			case "missing field":
				delete(object, "policy_id")
			case "missing metadata":
				object["metadata_id"] = fullAssessmentDispatchJSON(t, uuid.New())
			case "wrong PR":
				object["pull_request_id"] = fullAssessmentDispatchJSON(t, foreign.job.PullRequestID)
			case "wrong key":
				object["review_output_key"] = fullAssessmentDispatchJSON(t, siblingKey)
			case "wrong payload org":
				object["org_id"] = fullAssessmentDispatchJSON(t, foreign.job.OrgID)
			case "missing recheck":
				kind = models.JobTypeRunCodeReviewRecheck
				payload = fullAssessmentDispatchJSON(t, codeReviewRecheckJob{OrgID: f.job.OrgID, AssessmentID: uuid.New()})
			default:
				t.Fatal("the finite poison matrix must contain only explicit actual persisted branches")
			}
			if payload == nil {
				payload = fullAssessmentDispatchJSON(t, object)
			}
			foreignBefore, err := terminalContentionSnapshot(ctx, pool, foreign)
			require.NoError(t, err, "retain all real foreign tenant rows before actual poll, not merely a metadata or result count")
			terminalDispatchOwnershipAssertChildren(t, terminalOwnerPointerPositiveDecode(t, foreignBefore), foreignChildren)
			node := "synthetic-poison-scope:" + uuid.NewString()
			_, err = pool.Exec(ctx, `INSERT INTO nodes(id,mode) VALUES($1,'worker')`, node)
			require.NoError(t, err, "register one genuine synthetic queue consumer without altering any other node")
			tag, err := pool.Exec(ctx, `UPDATE jobs SET job_type=$3,payload=$4,status='pending',locked_by_node_id=NULL,run_owner_id=NULL,
				lock_token=NULL,locked_at=NULL,lease_expires_at=NULL,attempts=0,max_attempts=1,priority=100000,target_node_id=$5,run_at=statement_timestamp()
				WHERE org_id=$1 AND id=$2`, f.job.OrgID, f.jobID, kind, payload, node)
			require.NoError(t, err, "persist the genuine JSONB poison before making only its original exact job due for real claim")
			require.Equal(t, int64(1), tag.RowsAffected(), "the actual runnable poison job must exist rather than silently selecting a sibling")
			observed := &terminalOwnerPointerRun{actual: &fullAssessmentDispatchRunObserver{base: f.stores.ThreadSendDependencies.Transactions}}
			actualStore, err := db.NewJobStoreWithCodeReviewTerminalTransactions(pool, observed)
			require.NoError(t, err, "inject only the actual explicitly aware JobStore and original real bounded transaction capability")
			recorded := &terminalACKStore{actual: actualStore}
			w := New(pool, zerolog.Nop(), node, WithCodeReviewTerminalStore(recorded))
			var handlers, hooks atomic.Int32
			var claimed json.RawMessage
			var token uuid.UUID
			terminalErr := &FatalError{Err: errors.New("Synthetic fatal persisted poison scope")}
			w.Register(kind, func(handlerCtx context.Context, _ string, actualPayload json.RawMessage) error {
				handlers.Add(1)
				actualJob, present := jobctx.JobIDFromContext(handlerCtx)
				require.True(t, present, "the poison handler must run through actual Worker.poll context")
				require.Equal(t, f.jobID, actualJob, "the actual claim must select only the deliberately targeted original poison job")
				var found bool
				token, found = jobctx.LockTokenFromContext(handlerCtx)
				require.True(t, found, "the actual claim must supply its real lease fence before poison classification")
				require.NotEqual(t, uuid.Nil, token, "a missing token cannot masquerade as successful job-only poison retirement")
				require.JSONEq(t, string(payload), string(actualPayload), "actual poll must deliver the persisted poison unchanged rather than repairing or re-rendering its owner")
				claimed, err = terminalContentionSnapshot(handlerCtx, pool, f)
				require.NoError(t, err, "retain every original/sibling row AFTER real claim commits its actual token, attempts and lease fields")
				terminalDispatchOwnershipAssertChildren(t, terminalOwnerPointerPositiveDecode(t, claimed), children)
				jobctx.RegisterDeadLetterHook(handlerCtx, func(context.Context, error) { hooks.Add(1) })
				return terminalErr // No parsing, owner repair, provider or scope fallback in the logical handler.
			})
			w.poll(ctx)
			fullAssessmentDispatchAssertRun(t, observed.actual)
			require.Equal(t, int32(1), handlers.Load(), "the actual queue must claim and execute the original poison job once")
			require.Equal(t, 1, recorded.calls, "the actual worker must call its exact configured aware retirement capability once")
			require.NoError(t, recorded.err, "actual malformed or positively missing scope must commit job-only, not strand ownership through a cast/query error")
			require.True(t, recorded.outcome.Matches(f.job.OrgID, f.jobID, token), "a real known commit must retain precisely the actual original organization/job/claim correlation")
			scope, hasOwner := recorded.outcome.Scope()
			require.False(t, hasOwner, "a malformed or missing exact owner cannot authorize any target-derived effect")
			require.Equal(t, db.CodeReviewTerminalScope{OrgID: f.job.OrgID, JobID: f.jobID, LockToken: token, JobType: kind}, scope, "every projected owner field must be scrubbed instead of adopting same-session, latest or foreign rows")
			require.False(t, recorded.outcome.UnresolvedDispatch(), "live real original and sibling histories may not supply unresolved authority to a poison job")
			metadata, metadataPresent := recorded.outcome.Metadata()
			require.False(t, metadataPresent, "known job-only retirement may not expose a metadata event descriptor")
			require.Equal(t, models.CodeReviewSessionMetadata{}, metadata, "no projected metadata value may escape the job-only boundary")
			assessment, assessmentPresent := recorded.outcome.Assessment()
			require.False(t, assessmentPresent, "known job-only retirement may not expose an assessment event descriptor")
			require.Equal(t, models.CodeReviewAssessment{}, assessment, "no full baseline, latest or missing recheck assessment may escape as target authority")
			require.Equal(t, []string(nil), observed.domainLocks, "initial poison may acquire no PR, metadata or assessment target locks")
			require.Equal(t, []string{"update:jobs"}, observed.mutations, "the actual job-only unit must issue precisely the exact job DML and no domain mutation")
			require.Equal(t, int32(0), hooks.Load(), "the actual worker must suppress a genuinely registered hook even though the poison job's commit is known successful")
			expectedFailure := db.CodeReviewTerminalFailure{JobError: terminalErr.Error(), Reason: "code review job failed: " + terminalErr.Error(),
				Code: models.CodeReviewStatusCodeWorkerFailed, Message: "The review stopped because of a non-retryable error. Check the failure details before trying again."}
			require.Equal(t, expectedFailure, recorded.failure, "the actual finite worker classification must reach the one real retirement unit unchanged")
			after, err := terminalContentionSnapshot(ctx, pool, f)
			require.NoError(t, err, "independently read the complete original and sibling graph after actual poll and known retirement settle")
			terminalOwnerPointerAssertJobOnly(t, claimed, after, f.jobID, terminalErr.Error())
			foreignAfter, err := terminalContentionSnapshot(ctx, pool, foreign)
			require.NoError(t, err, "independently read the entire genuine foreign tenant graph after the poison retirement")
			require.Equal(t, foreignBefore, foreignAfter, "every foreign pending intent, immutable wake/input/attempt, job, metadata, assessment and evidence row must remain byte-identical")
		})
	}
}
