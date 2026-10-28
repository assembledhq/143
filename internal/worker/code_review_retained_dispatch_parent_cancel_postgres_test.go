package worker

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/jobctx"
	"github.com/assembledhq/143/internal/models"
	threadsvc "github.com/assembledhq/143/internal/services/thread"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

type retainedParentGateSendState struct {
	parent                                 models.SessionStatus
	messages, inbox, stamps, executionJobs int
}

func retainedParentGateReadSendState(t *testing.T, observer *pgxpool.Conn, f retainedControllerFixture) retainedParentGateSendState {
	t.Helper()
	var state retainedParentGateSendState
	require.NoError(t, observer.QueryRow(context.Background(), `SELECT s.status,
 (SELECT count(*) FROM session_messages WHERE org_id=$1 AND session_id=$2),
 (SELECT count(*) FROM thread_inbox_entries WHERE org_id=$1 AND session_id=$2),
 (SELECT count(*) FROM caller_send_command_stamps WHERE org_id=$1 AND session_id=$2),
 (SELECT count(*) FROM jobs WHERE org_id=$1 AND payload->>'session_id'=$2::uuid::text
 AND job_type IN ('run_agent','continue_session','fork_session_thread','revert_session_thread','deliver_thread_inbox','prepare_code_review_workspace'))
 FROM sessions s WHERE s.org_id=$1 AND s.id=$2`, f.f.job.OrgID, f.f.job.SessionID).Scan(&state.parent, &state.messages, &state.inbox, &state.stamps, &state.executionJobs), "independently inspect actual parent status and every real admission graph count before interpreting the handler return value")
	return state
}

// Claim the actual Prepare-created original wake, not its succeeded history.
// Foreign work is pinned to a different genuinely available synthetic node so
// the real queue selector cannot win the wrong tenant on an equal timestamp.
func retainedParentGateClaimWake(t *testing.T, f, foreign retainedControllerFixture) (context.Context, models.Job) {
	t.Helper()
	ctx := context.Background()
	node, foreignNode := "synthetic-parent-gate:"+uuid.NewString(), "synthetic-foreign-gate:"+uuid.NewString()
	for _, id := range []string{node, foreignNode} {
		_, err := f.pool.Exec(ctx, `INSERT INTO nodes(id,mode) VALUES($1,'worker')`, id)
		require.NoError(t, err, "register only fresh isolated synthetic queue consumers")
	}
	for _, target := range []struct {
		fixture retainedControllerFixture
		node    string
	}{{f, node}, {foreign, foreignNode}} {
		tag, err := f.pool.Exec(ctx, `UPDATE jobs SET target_node_id=$3 WHERE org_id=$1 AND dedupe_key=$2 AND status='pending'`, target.fixture.f.job.OrgID, "code_review:"+target.fixture.f.job.OutputKey, target.node)
		require.NoError(t, err, "pin only each original prepared wake before the actual queue claim")
		require.Equal(t, int64(1), tag.RowsAffected(), "each retained owner must have one real original wake before the controller starts")
	}
	token := uuid.New()
	claimed, err := f.f.stores.Jobs.ClaimNextRunnable(ctx, node, node, token, defaultLeaseDuration)
	require.NoError(t, err, "claim through the actual production queue with its unchanged worker lease duration")
	require.NotNil(t, claimed, "the test must enter a real available original controller, not a missing-lease refusal")
	require.NotEqual(t, f.f.jobID, claimed.ID, "the actual claim must select the new Prepare-created original wake, never its retained succeeded history")
	require.Equal(t, f.f.job.OrgID, claimed.OrgID, "the real queue must claim the original tenant's wake, never foreign work")
	require.Equal(t, models.JobTypeRunCodeReview, claimed.JobType, "the actual claimed job must be the retained full-review controller")
	var payload runCodeReviewPayload
	require.NoError(t, json.Unmarshal(claimed.Payload, &payload), "read only the actual claimed wake routing for its independent scope check")
	require.Equal(t, []uuid.UUID{f.f.job.OrgID, f.f.job.SessionID, f.f.job.MetadataID}, []uuid.UUID{payload.OrgID, payload.SessionID, payload.MetadataID}, "actual claimed payload must retain exact original controller ownership")
	handlerCtx := jobctx.WithDeadLetterHooks(jobctx.WithWorkerNodeID(jobctx.WithJobID(jobctx.WithLockToken(ctx, token), claimed.ID), node))
	return withJobOrgID(handlerCtx, f.f.job.OrgID), *claimed
}

func retainedParentGateAssertFrozen(t *testing.T, observer *pgxpool.Conn, f retainedControllerFixture, controller models.Job) {
	t.Helper()
	var raw, wake json.RawMessage
	var digest string
	require.NoError(t, observer.QueryRow(context.Background(), `SELECT dispatch_input::text,dispatch_digest,dispatch_wake::text FROM code_review_agent_results WHERE org_id=$1 AND session_id=$2 AND id=$3`, f.f.job.OrgID, f.f.job.SessionID, f.result.ID).Scan(&raw, &digest, &wake), "retain the complete original command, digest and wake after cancellation or ended-parent settlement")
	command, err := threadsvc.DecodeCallerSendCommand(raw, digest, threadsvc.CallerSendOwnerScope{Purpose: threadsvc.CallerSendCodeReviewReviewerDispatch, OwnerID: f.result.ID, OrgID: f.f.job.OrgID, SessionID: f.f.job.SessionID, ThreadID: f.lane.ID, ClientMessageID: f.command.ClientMessageID()})
	require.NoError(t, err, "restoration must use independent original owner and lane, not current parent status or policy")
	want, err := f.command.Encode()
	require.NoError(t, err, "encode only the original retained command for exact comparison")
	got, err := command.Encode()
	require.NoError(t, err, "encode the actual restored command without allocating a fresh identity")
	require.Equal(t, want, got, "parent terminality cannot alter any original effective input field or deterministic key")
	var expectedWake, payload, expectedPayload string
	require.NoError(t, observer.QueryRow(context.Background(), `SELECT $1::jsonb::text`, string(f.wake)).Scan(&expectedWake), "compare exact PostgreSQL JSONB presentation without losing large opaque integers")
	require.Equal(t, expectedWake, string(wake), "the retained original wake must remain complete and immutable")
	require.NoError(t, observer.QueryRow(context.Background(), `SELECT payload::text,($3::jsonb->'payload')::text FROM jobs WHERE org_id=$1 AND id=$2`, f.f.job.OrgID, controller.ID, string(f.wake)).Scan(&payload, &expectedPayload), "the actual claimed original controller remains separately inspectable after handler return")
	require.Equal(t, expectedPayload, payload, "the actual original job payload must retain opaque original context rather than a fresh typed remarshal")
}

func retainedParentGateDecodeGraph(t *testing.T, raw json.RawMessage) (map[string]json.RawMessage, map[string]json.RawMessage) {
	t.Helper()
	var outer, graph map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(raw, &outer), "retain full exact JSON values without floating-point reconstruction")
	require.NoError(t, json.Unmarshal(outer["graph"], &graph), "read every original tenant graph row for parent-gate canary checks")
	return outer, graph
}

func retainedParentGateAssertCanaries(t *testing.T, before, after json.RawMessage, ended bool) {
	t.Helper()
	b, bg := retainedParentGateDecodeGraph(t, before)
	a, ag := retainedParentGateDecodeGraph(t, after)
	for _, key := range []string{"repositories", "pull_requests", "automations", "targets", "generations"} {
		require.Equal(t, b[key], a[key], "terminal parent gate must preserve complete independent target/ownership rows: %s", key)
	}
	for _, key := range []string{"sessions", "threads", "messages", "inbox", "stamps", "findings", "prompts", "control", "requests"} {
		require.Equal(t, bg[key], ag[key], "no admission or blind cancellation may change an original parent/lane/send/canary field: %s", key)
	}
	if !ended {
		// The existing successful WithLockedPR wrapper touches updated_at
		// even when this legacy reservation has no active-assessment pointer.
		// Pin every other field rather than treating that clock as a new owner.
		var oldSchedule, newSchedule []map[string]json.RawMessage
		require.NoError(t, json.Unmarshal(bg["schedule"], &oldSchedule), "retain all existing legacy scheduling fields before normal terminal reconciliation")
		require.NoError(t, json.Unmarshal(ag["schedule"], &newSchedule), "inspect all existing legacy scheduling fields after normal terminal reconciliation")
		require.Equal(t, len(oldSchedule), len(newSchedule), "parent cancellation cannot allocate or delete a scheduler owner")
		for i := range oldSchedule {
			var beforeClock, afterClock time.Time
			require.NoError(t, json.Unmarshal(oldSchedule[i]["updated_at"], &beforeClock), "read the original actual scheduler clock")
			require.NoError(t, json.Unmarshal(newSchedule[i]["updated_at"], &afterClock), "read the ordinary terminal-wrapper scheduler clock")
			require.False(t, afterClock.Before(beforeClock), "normal wrapper bookkeeping must not regress the original scheduler clock")
			delete(oldSchedule[i], "updated_at")
			delete(newSchedule[i], "updated_at")
		}
		require.Equal(t, oldSchedule, newSchedule, "parent cancellation must preserve every original legacy reservation field apart from its existing wrapper's monotone bookkeeping clock")
	}
	var previous, current []map[string]json.RawMessage
	require.NoError(t, json.Unmarshal(bg["jobs"], &previous), "retain every original controller, history and continuation job field")
	require.NoError(t, json.Unmarshal(ag["jobs"], &current), "inspect every actual post-handler job, not only new-job counts")
	byID := make(map[string]map[string]json.RawMessage, len(current))
	for _, row := range current {
		byID[string(row["id"])] = row
	}
	for _, row := range previous {
		require.Equal(t, row, byID[string(row["id"])], "the handler cannot alter any original real claimed wake/history/continuation job field")
		delete(byID, string(row["id"]))
	}
	newJobs := 0
	for _, row := range byID {
		newJobs++
		require.True(t, ended, "only existing ended-parent reconciliation may enqueue its ordinary terminal status wake")
		require.Equal(t, json.RawMessage(`"sync_code_review_status_comment"`), row["job_type"], "existing ended-parent settlement may add only its normal status-comment wake, never execution or replacement work")
	}
	wantJobs := 0
	if ended {
		wantJobs = 1
	}
	require.Equal(t, wantJobs, newJobs, "the existing ended-parent path's ordinary terminal status wake must be explicit, not mistaken for a new agent admission")
}

// This is the actual handler boundary with complete real stores and a real
// queue-claimed original wake. It does not claim concurrent parent-cancellation
// locking, provider execution, Worker.poll settlement or activation coverage.
func TestRetainedReviewerHandlerPreservesTerminalParentPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name             string
		parent           models.SessionStatus
		uncertain, ended bool
	}{
		{name: "cancelled parent cancels pending owner before any send", parent: models.SessionStatusCancelled},
		{name: "cancelled parent observes original uncertain command without resend", parent: models.SessionStatusCancelled, uncertain: true},
		{name: "completed parent settles unsaved pending work without fresh launch", parent: models.SessionStatusCompleted, ended: true},
		{name: "failed parent settles unsaved pending work without fresh launch", parent: models.SessionStatusFailed, ended: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx := context.Background()
			pool := fullRecoveryPostgresPoolWithMaxConns(t, ctx, 2)
			require.Equal(t, int32(2), pool.Config().MaxConns, "actual handler gating and observation must fit one application slot beside the held observer")
			f, foreign := newRetainedControllerFixture(t, pool), newRetainedControllerFixture(t, pool)
			retainedControllerPrepare(t, f, f.base)
			retainedControllerPrepare(t, foreign, foreign.base)
			service, effects := retainedControllerService(t, f, f.base)
			f.f.stores.ThreadSendDependencies.Effects = effects
			var originalMessage *models.SessionMessage
			var originalAttempt uuid.UUID
			if tt.uncertain {
				// A legal historical attempt makes the actual observer due without
				// sleep or a changed production clock. This fixture does not claim
				// that its old attempt was made by the production Claim helper.
				originalAttempt = uuid.New()
				require.NoError(t, f.base.Run(ctx, func(unitCtx context.Context, tx pgx.Tx) error {
					tag, err := tx.Exec(unitCtx, `UPDATE code_review_agent_results SET dispatch_state='attempting',dispatch_attempt_id=$4,dispatch_attempt_seq=1,dispatch_attempted_at=statement_timestamp()-interval '1 minute',dispatch_due_at=statement_timestamp()-interval '1 second',dispatch_writer_seq=1,dispatch_updated_at=statement_timestamp() WHERE org_id=$1 AND session_id=$2 AND id=$3 AND dispatch_state='pending'`, f.f.job.OrgID, f.f.job.SessionID, f.result.ID, originalAttempt)
					if err != nil {
						return err
					}
					if tag.RowsAffected() != 1 {
						return errors.New("Synthetic historical original claim was not installed")
					}
					return nil
				}), "retain a complete legal historical attempting tuple on the actual Prepare-born owner")
				input := f.command.SendInput()
				proof, err := f.command.Proof()
				require.NoError(t, err, "restore only the original correlation proof before the real original send")
				input.CallerCommand = proof
				accepted, err := service.SendMessage(ctx, input)
				require.NoError(t, err, "actual original thread-service send must establish positive message/inbox/stamp evidence before parent cancellation")
				require.NotNil(t, accepted, "uncertain-history positive observation requires a real actual send result, never a fabricated receipt")
				require.NotNil(t, accepted.Message, "the fixture must retain an actual positively created original message")
				originalMessage = accepted.Message
				require.NoError(t, f.base.Run(ctx, func(unitCtx context.Context, tx pgx.Tx) error {
					tag, err := tx.Exec(unitCtx, `UPDATE code_review_agent_results SET dispatch_state='unknown',dispatch_due_at=statement_timestamp()-interval '1 second',dispatch_observe_deadline=dispatch_attempted_at+interval '15 minutes',dispatch_detail='unknown_ack',dispatch_writer_seq=dispatch_writer_seq+1,dispatch_updated_at=statement_timestamp() WHERE org_id=$1 AND session_id=$2 AND id=$3 AND dispatch_state='attempting' AND dispatch_attempt_id=$4`, f.f.job.OrgID, f.f.job.SessionID, f.result.ID, originalAttempt)
					if err != nil {
						return err
					}
					if tag.RowsAffected() != 1 {
						return errors.New("Synthetic historical original uncertainty was not retained")
					}
					closed, err := db.CloseCodeReviewReviewerDispatchDeadlineInTx(unitCtx, f.f.job.OrgID, tx, codeReviewDispatchScope(f.f.job), f.result.ID, nil, f.result.StructuredResult, time.Now().Add(-time.Hour))
					if err != nil {
						return err
					}
					if !closed {
						return errors.New("Synthetic original unresolved contribution was not closed")
					}
					return nil
				}), "retain due unknown admission and close its business contribution without terminalizing active metadata or inferring abort")
			}
			require.NoError(t, f.f.stores.Sessions.UpdateStatus(ctx, f.f.job.OrgID, f.f.job.SessionID, tt.parent), "persist the original terminal parent before the actual handler begins while its metadata remains active")
			handlerCtx, controller := retainedParentGateClaimWake(t, f, foreign)
			observer, err := pool.Acquire(ctx)
			require.NoError(t, err, "reserve the independent observer before the real handler, leaving exactly one application checkout")
			defer observer.Release()
			before := retainedControllerReadSnapshot(t, observer, f)
			foreignBefore := retainedControllerReadSnapshot(t, observer, foreign)
			contributions := mustRetainedControllerResults(t, f)
			originalAssessment, err := f.f.stores.CodeReviewAssessments.GetByID(ctx, f.f.job.OrgID, f.f.assessment)
			require.NoError(t, err, "retain the complete original assessment before the existing cancellation or ended-parent gate")
			metadata, err := f.f.stores.CodeReviews.GetBySessionID(ctx, f.f.job.OrgID, f.f.job.SessionID)
			require.NoError(t, err, "verify the gate is exercised with active real metadata, not an already-terminal shortcut")
			require.Equal(t, models.CodeReviewSessionStatusRunning, metadata.Status, "parent terminality must precede metadata terminalization in this actual-handler regression")
			want := retainedParentGateSendState{parent: tt.parent}
			if tt.uncertain {
				want.messages, want.inbox, want.stamps, want.executionJobs = 1, 1, 1, 1
			}
			require.Equal(t, want, retainedParentGateReadSendState(t, observer, f), "baseline must contain exactly the intended original terminal parent and optional real prior accepted graph")
			beforeJobs := append([]uuid.UUID(nil), effects.jobs...)
			beforeEffects := [3]int{effects.claims, effects.pending, effects.lifecycle}
			capture := &fixedRecheckCapture{captureErr: errors.New("Synthetic unexpected capture beyond terminal parent gate")}
			publisher := &inlineRecoveryPublisher{submitErr: errors.New("Synthetic unexpected publication beyond terminal parent gate")}
			services := &Services{CodeReviewAssessmentsEnabled: true, CodeReviewInputCapture: capture, CodeReviews: publisher}
			handlerErr := newRunCodeReviewHandler(f.f.stores, services, zerolog.Nop())(handlerCtx, string(models.JobTypeRunCodeReview), controller.Payload)
			// Assert durable parent/admission effects FIRST. On old source this
			// fails on actual parent resumption/message creation, not a later
			// unavailable provider, generic retry or return-value mismatch.
			require.Equal(t, want, retainedParentGateReadSendState(t, observer, f), "early retained reconciliation must not resume an already terminal parent or append any new message/inbox/stamp/continuation before its existing gates")
			after := retainedControllerReadSnapshot(t, observer, f)
			require.Equal(t, beforeEffects, [3]int{effects.claims, effects.pending, effects.lifecycle}, "actual handler may not publish a new send claim/pending/lifecycle effect for terminal-parent work")
			require.Equal(t, beforeJobs, effects.jobs, "actual handler must never notify a second or fresh execution job after preexisting parent terminality")
			require.Equal(t, 0, capture.calls, "parent cancellation/ended-parent recovery must run before configured provider capture")
			require.Empty(t, publisher.fileRequests, "terminal parent cannot start configured provider file retrieval")
			require.Empty(t, publisher.requests, "terminal parent cannot publish a review while handling unresolved or unstarted reviewer admission")
			require.Equal(t, contributions, mustRetainedControllerResults(t, f), "pending cancellation or late positive receipt must preserve every closed contribution and unrelated reviewer canary field")
			retainedParentGateAssertFrozen(t, observer, f, controller)
			retainedParentGateAssertCanaries(t, before, after, tt.ended)
			require.Equal(t, foreignBefore, retainedControllerReadSnapshot(t, observer, foreign), "all complete nonempty foreign command, job, metadata, assessment, parent and lane rows must remain byte-exact")
			phase, err := readRetainedControllerPhase(ctx, observer, f)
			require.NoError(t, err, "observe only the exact original committed intent after the actual handler")
			var resolution string
			var due bool
			require.NoError(t, observer.QueryRow(ctx, `SELECT dispatch_resolution,dispatch_due_at IS NOT NULL FROM code_review_agent_results WHERE org_id=$1 AND session_id=$2 AND id=$3`, f.f.job.OrgID, f.f.job.SessionID, f.result.ID).Scan(&resolution, &due), "read the original exact typed settlement rather than treating parent status as a receipt")
			if tt.uncertain {
				require.Equal(t, "accepted", phase.state, "actual early observe-only handling must repair the exact positive original receipt even while the parent is cancelled")
				require.Equal(t, "observed_auto", resolution, "late acceptance must come from the real sealed observation, never another SendMessage ACK")
				require.Equal(t, originalAttempt, phase.attemptID, "late receipt repair must preserve the original uncertain attempt identity")
				require.Equal(t, 1, phase.attempts, "observe-only handling must not mint a fresh attempt or replay the original send")
				retainedControllerAssertAcceptedGraph(t, observer, f, originalMessage)
			} else {
				require.Equal(t, "cancelled", phase.state, "the existing strict terminal metadata writer must cancel only the exact unattempted original intent")
				require.Equal(t, "owner_cancelled", resolution, "cancellation must use the approved metadata-owned pending settlement, not a fabricated send refusal")
				require.Equal(t, uuid.Nil, phase.attemptID, "preexisting terminal parent must prevent the first dispatch attempt from being minted")
				require.Equal(t, 0, phase.attempts, "no original claimant may be committed before the existing terminal-parent gates")
			}
			require.False(t, due, "known pending cancellation or positive original observation must resolve only that intent's due work")
			metadata, err = f.f.stores.CodeReviews.GetBySessionID(ctx, f.f.job.OrgID, f.f.job.SessionID)
			require.NoError(t, err, "read actual metadata owner settlement after all durable safety assertions")
			assessment, err := f.f.stores.CodeReviewAssessments.GetByID(ctx, f.f.job.OrgID, f.f.assessment)
			require.NoError(t, err, "read actual assessment resolution, not generic handler error text")
			if tt.ended {
				require.Equal(t, models.CodeReviewSessionStatusFailed, metadata.Status, "existing drained ended-parent recovery must retire unsaved metadata without a fresh launch")
				require.Equal(t, models.CodeReviewAssessmentFailed, assessment.Status, "existing ended-parent recovery must settle the exact unpublished assessment")
				schedule, err := db.NewCodeReviewScheduleStore(pool).Get(ctx, f.f.job.OrgID, f.f.job.PullRequestID)
				require.NoError(t, err, "read the existing ended-parent reconciliation's actual scheduler resolution")
				require.Equal(t, models.CodeReviewScheduleIdle, schedule.State, "settled unsaved ended work must release its original scheduler reservation rather than create a fresh execution")
				require.Nil(t, schedule.ActiveSessionID, "existing ended-parent settlement must release only the exact original session reservation")
				require.Nil(t, schedule.ActiveAssessmentID, "an ended unsaved review cannot retain active assessment authority")
			} else {
				require.Equal(t, models.CodeReviewSessionStatusCancelled, metadata.Status, "the existing parent-cancellation gate must terminalize active review metadata before pending work can send")
				// The existing parent-cancel gate returns immediately after strict
				// metadata cancellation; later scheduler repair owns assessment
				// reconciliation. Do not invent a new synchronous terminal write.
				require.Equal(t, originalAssessment, assessment, "parent cancellation must preserve the complete original assessment until its existing separate reconciliation owner runs")
			}
			require.NoError(t, handlerErr, "with durable parent and retained-send safety established, the actual existing terminal handler path should settle normally")
		})
	}
}
