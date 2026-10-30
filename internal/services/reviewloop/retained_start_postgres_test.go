package reviewloop

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	threadsvc "github.com/assembledhq/143/internal/services/thread"
	testpostgres "github.com/assembledhq/143/internal/testutil/postgres"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func TestMain(m *testing.M) { os.Exit(testpostgres.Run(m)) }

var errRetainedStartAck = errors.New("synthetic retained start acknowledgement unavailable")

type retainedStartTestTx struct {
	pgx.Tx
	phase string
}

func (t *retainedStartTestTx) Exec(ctx context.Context, query string, args ...any) (pgconn.CommandTag, error) {
	if strings.Contains(query, "INSERT INTO review_loop_start_intents") {
		t.phase = "T0"
	}
	if strings.Contains(query, "UPDATE session_review_loop_passes SET review_message_id") {
		t.phase = "settle"
	}
	if strings.Contains(query, "UPDATE review_loop_start_intents SET state='created'") {
		t.phase = "T1"
	}
	return t.Tx.Exec(ctx, query, args...)
}

func (t *retainedStartTestTx) QueryRow(ctx context.Context, query string, args ...any) pgx.Row {
	if strings.Contains(query, "INSERT INTO session_messages") {
		t.phase = "send"
	}
	if strings.Contains(query, "UPDATE session_review_loops l SET") && len(args) > 12 && args[12] == "claim" {
		t.phase = "claim"
	}
	return t.Tx.QueryRow(ctx, query, args...)
}

type retainedStartFaultRunner struct {
	base   threadsvc.SendTransactions
	phase  string
	commit bool
	fired  bool
}

func (r *retainedStartFaultRunner) Run(ctx context.Context, work func(context.Context, pgx.Tx) error) error {
	selected := false
	err := r.base.Run(ctx, func(ctx context.Context, tx pgx.Tx) error {
		selected = false
		capture := &retainedStartTestTx{Tx: tx}
		if err := work(ctx, capture); err != nil {
			return err
		}
		selected = !r.fired && r.phase != "" && capture.phase == r.phase
		if selected && !r.commit {
			return errRetainedStartAck
		}
		return nil
	})
	if selected {
		r.fired = true
		if r.commit && err != nil {
			return err
		}
		return errRetainedStartAck
	}
	return err
}

type retainedStartTestRuntime struct {
	pool                  *pgxpool.Pool
	reads, creates, sends int
	afterCreate           func(context.Context, threadsvc.CreateThreadInput) error
	threads               *threadsvc.Service
	unknownSend           bool
	afterSend             func(context.Context, threadsvc.SendMessageInput, *threadsvc.SendMessageResult) error
}

func (r *retainedStartTestRuntime) GetSession(ctx context.Context, orgID, sessionID uuid.UUID) (models.Session, error) {
	r.reads++
	return db.NewSessionStore(r.pool).GetByID(ctx, orgID, sessionID)
}
func (r *retainedStartTestRuntime) CreateThread(ctx context.Context, in threadsvc.CreateThreadInput) (*models.SessionThread, error) {
	r.creates++
	if r.pool.Stat().AcquiredConns() != 0 {
		return nil, errors.New("E1 entered with an owned database checkout")
	}
	var model *string
	if in.Model != "" {
		model = &in.Model
	}
	row := models.SessionThread{ID: in.ID, OrgID: in.OrgID, SessionID: in.SessionID, AgentType: models.AgentType(in.AgentType), ModelOverride: model, Label: in.Label, Status: models.ThreadStatusIdle}
	if err := db.NewSessionThreadStore(r.pool).CreateWithID(ctx, in.OrgID, &row, models.MaxThreadsPerSession); err != nil {
		return nil, err
	}
	if r.afterCreate != nil {
		if err := r.afterCreate(ctx, in); err != nil {
			return nil, err
		}
	}
	return &row, nil
}
func (r *retainedStartTestRuntime) SendMessage(ctx context.Context, input threadsvc.SendMessageInput) (*threadsvc.SendMessageResult, error) {
	r.sends++
	if r.pool.Stat().AcquiredConns() != 0 {
		return nil, errors.New("startup send entered with an owned database checkout")
	}
	if r.threads != nil {
		result, err := r.threads.SendMessage(ctx, input)
		if err != nil {
			return nil, err
		}
		if r.afterSend != nil {
			if err := r.afterSend(ctx, input, result); err != nil {
				return nil, err
			}
		}
		if r.unknownSend {
			return nil, threadsvc.ErrCommandOutcomeUnknown
		}
		return result, nil
	}
	return nil, threadsvc.ErrCommandOutcomeUnknown
}

func TestRetainedReviewLoopCreationPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, phase string
		commit      bool
		refused     bool
	}{
		{name: "fresh retained creation"},
		{name: "acknowledged actual thread send"},
		{name: "actual send commit with lost acknowledgement"},
		{name: "actual send rollback with ambiguous acknowledgement"},
		{name: "actual send accepted pointer conflict"},
		{name: "actual send settlement commit lost acknowledgement", phase: "settle", commit: true},
		{name: "actual send settlement rollback", phase: "settle"},
		{name: "committed T0 acknowledgement lost", phase: "T0", commit: true},
		{name: "aborted T0 remains pending", phase: "T0"},
		{name: "committed T1 acknowledgement lost", phase: "T1", commit: true},
		{name: "aborted T1 resumes original", phase: "T1"},
		{name: "committed claim acknowledgement lost", phase: "claim", commit: true},
		{name: "aborted claim allows first send only after new known claim", phase: "claim"},
		{name: "crash after E1"},
		{name: "cancellation after E1", refused: true},
		{name: "thread identity changed after E1", refused: true},
		{name: "thread archived after E1", refused: true},
		{name: "mutable environment after T0", phase: "T0", commit: true},
		{name: "created terminal original is adopted"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
			defer cancel()
			pool := testpostgres.NewPool(t, ctx, "../../../migrations", 2)
			orgID := uuid.New()
			_, err := pool.Exec(ctx, `INSERT INTO organizations(id,name) VALUES($1,'Synthetic retained start')`, orgID)
			require.NoError(t, err, "seed this case's isolated synthetic tenant")
			session := models.Session{OrgID: orgID, AgentType: models.AgentTypeClaudeCode, Status: models.SessionStatusIdle,
				AutonomyLevel: models.DefaultSessionAutonomy, TokenMode: models.SessionTokenModeLow, Origin: models.SessionOriginManual,
				InteractionMode: models.SessionInteractionModeInteractive, ValidationPolicy: models.SessionValidationPolicyOnSessionEnd}
			require.NoError(t, db.NewSessionStore(pool).Create(ctx, &session), "create the session and primary thread through the genuine store")
			_, err = pool.Exec(ctx, `UPDATE sessions SET snapshot_key='synthetic/original-snapshot',sandbox_state='snapshotted' WHERE org_id=$1 AND id=$2`, orgID, session.ID)
			require.NoError(t, err, "fresh preflight must see an actual available snapshot")
			_, err = pool.Exec(ctx, `UPDATE caller_send_intent_control SET intents_enabled=true WHERE singleton AND schema_ready AND dispatch_status_ready`)
			require.NoError(t, err, "enable only this disposable database fixture")
			base, err := db.NewThreadSendTransactions(pool)
			require.NoError(t, err, "use the production two-second transaction owner without alternate settings")
			runner := &retainedStartFaultRunner{base: base, phase: tt.phase, commit: tt.commit}
			runtime := &retainedStartTestRuntime{pool: pool}
			var sendFault *retainedStartFaultRunner
			if strings.Contains(tt.name, "actual") {
				sessions, threads, jobs := db.NewSessionStore(pool), db.NewSessionThreadStore(pool), db.NewJobStore(pool)
				deps, err := threadsvc.NewSendDependencies(pool, sessions, threads, jobs)
				require.NoError(t, err, "wire the actual bounded thread send persistence and postcommit effects")
				if tt.name == "actual send rollback with ambiguous acknowledgement" {
					sendFault = &retainedStartFaultRunner{base: deps.Transactions, phase: "send"}
					deps.Transactions = sendFault
				}
				runtime.threads, err = threadsvc.NewService(threads, sessions, db.NewSessionMessageStore(pool), db.NewSessionLogStore(pool), jobs, deps, zerolog.Nop())
				require.NoError(t, err, "construct the genuine thread sender for acceptance and observer proofs")
				runtime.threads.SetThreadInboxStore(db.NewThreadInboxStore(pool), pool)
				runtime.unknownSend = tt.name == "actual send commit with lost acknowledgement"
				if tt.name == "actual send accepted pointer conflict" {
					runtime.afterSend = func(ctx context.Context, input threadsvc.SendMessageInput, result *threadsvc.SendMessageResult) error {
						other := models.SessionMessage{OrgID: input.OrgID, SessionID: input.SessionID, ThreadID: &input.ThreadID, Role: models.MessageRoleUser, Content: "Synthetic competing pointer"}
						if err := db.NewSessionMessageStore(pool).Create(ctx, &other); err != nil {
							return err
						}
						_, err := pool.Exec(ctx, `UPDATE session_review_loop_passes p SET review_message_id=$4 FROM session_review_loops l WHERE p.org_id=$1 AND p.session_id=$2 AND p.loop_id=l.id AND l.org_id=p.org_id AND l.thread_id=$3`, input.OrgID, input.SessionID, input.ThreadID, other.ID)
						return err
					}
				}
			}
			if tt.name == "crash after E1" {
				runtime.afterCreate = func(context.Context, threadsvc.CreateThreadInput) error { return errRetainedStartAck }
			}
			if tt.name == "cancellation after E1" {
				runtime.afterCreate = func(ctx context.Context, in threadsvc.CreateThreadInput) error {
					_, err := pool.Exec(ctx, `UPDATE review_loop_start_intents SET cancel_requested_at=statement_timestamp(),updated_at=statement_timestamp() WHERE org_id=$1 AND session_id=$2 AND thread_id=$3 AND state='prepared'`, in.OrgID, in.SessionID, in.ID)
					return err
				}
			}
			if tt.name == "thread identity changed after E1" || tt.name == "thread archived after E1" {
				runtime.afterCreate = func(ctx context.Context, in threadsvc.CreateThreadInput) error {
					query := `UPDATE session_threads SET label='Synthetic changed identity' WHERE org_id=$1 AND session_id=$2 AND id=$3`
					if tt.name == "thread archived after E1" {
						query = `UPDATE session_threads SET archived_at=statement_timestamp() WHERE org_id=$1 AND session_id=$2 AND id=$3`
					}
					_, err := pool.Exec(ctx, query, in.OrgID, in.SessionID, in.ID)
					return err
				}
			}
			service := NewService(db.NewSessionReviewLoopStore(pool), runtime, WithStartTransactions(runner))
			loop, err := service.Start(ctx, orgID, session.ID, StartReviewLoopRequest{MaxPasses: 2})
			var id uuid.UUID
			if tt.refused {
				require.ErrorIs(t, err, ErrReviewLoopStartRefused, "prepared cancellation must win T1 without any loop or pass insertion")
				require.NoError(t, pool.QueryRow(ctx, `SELECT id FROM review_loop_start_intents WHERE org_id=$1 AND session_id=$2`, orgID, session.ID).Scan(&id), "cancellation retains its original start identity")
			} else if tt.phase == "T0" || tt.phase == "T1" || tt.name == "crash after E1" {
				var pending *StartPendingError
				require.ErrorAs(t, err, &pending, "lost creation acknowledgement must return stable pending-confirmation correlation")
				id = pending.LoopID
			} else {
				require.NoError(t, err, "a known committed T1 should return the created loop")
				id = loop.ID
			}
			if tt.phase == "T0" {
				require.Equal(t, 0, runtime.creates, "an unacknowledged T0 never permits even blank thread creation")
			}
			if tt.phase == "claim" {
				require.Equal(t, 0, runtime.sends, "both committed and rolled-back unacknowledged claims must release zero sends")
			}
			if tt.name == "aborted T0 remains pending" {
				_, err = service.RecoverStart(ctx, orgID, session.ID, id)
				require.ErrorIs(t, err, ErrReviewLoopStartPending, "absence is not proof of abort or permission to allocate a replacement")
				require.Equal(t, 0, runtime.creates, "missing positive T0 evidence must remain no-E1")
				return
			}
			var before json.RawMessage
			require.NoError(t, pool.QueryRow(ctx, `SELECT envelope FROM review_loop_start_intents WHERE org_id=$1 AND id=$2`, orgID, id).Scan(&before), "retain the original JSONB envelope across recovery")
			if tt.name == "mutable environment after T0" {
				_, err = pool.Exec(ctx, `UPDATE sessions SET snapshot_key='synthetic/changed-snapshot',agent_type='codex',sandbox_state='destroyed' WHERE org_id=$1 AND id=$2`, orgID, session.ID)
				require.NoError(t, err, "change live preflight values after the original command was frozen")
			}
			if tt.name == "created terminal original is adopted" {
				_, err = pool.Exec(ctx, `UPDATE session_review_loops SET status='cancelled',latest_summary='Synthetic progressed summary',completed_passes=1,
 startup_send_writer_seq=startup_send_writer_seq+1,startup_send_updated_at=statement_timestamp() WHERE org_id=$1 AND id=$2`, orgID, id)
				require.NoError(t, err, "legitimate mutable progress must not alter retained creation identity")
			}
			runtime.afterCreate = nil
			if runtime.unknownSend || sendFault != nil {
				// Observe only after the actual fixed five-second due interval;
				// do not rewrite clocks, timestamps, guards or production budgets.
				timer := time.NewTimer(5100 * time.Millisecond)
				select {
				case <-ctx.Done():
					timer.Stop()
					t.Fatal("local observation deadline expired")
				case <-timer.C:
				}
			}
			recovered, err := service.RecoverStart(ctx, orgID, session.ID, id)
			if tt.refused {
				require.ErrorIs(t, err, ErrReviewLoopStartRefused, "the same cancelled start remains a known refusal")
			} else {
				require.NoError(t, err, "recovery must adopt the exact prepared or created original")
				require.Equal(t, id, recovered.ID, "recovery must never allocate a second loop identity")
				require.Equal(t, models.AgentTypeClaudeCode, recovered.AgentType, "recovery must preserve the frozen effective agent")
				require.Equal(t, "synthetic/original-snapshot", *recovered.LoopStartCheckpointKey, "recovery must preserve the frozen checkpoint")
				expectedAdmission := models.ReviewLoopAdmissionUnknown
				if runtime.threads != nil {
					expectedAdmission = models.ReviewLoopAdmissionAccepted
				}
				if tt.phase == "claim" && tt.commit || tt.phase == "settle" && !tt.commit {
					expectedAdmission = models.ReviewLoopAdmissionAttempting
				}
				if sendFault != nil {
					expectedAdmission = models.ReviewLoopAdmissionUnknown
					require.True(t, sendFault.fired, "the real message transaction was rolled back after its complete callback")
				}
				if tt.name == "actual send accepted pointer conflict" {
					expectedAdmission = models.ReviewLoopAdmissionAttention
				}
				require.Equal(t, &expectedAdmission, recovered.AdmissionState, "safe response must reflect durable admission state without equating uncertainty with acceptance")
				if tt.name == "created terminal original is adopted" {
					require.Equal(t, models.ReviewLoopStatusCancelled, recovered.Status, "adoption returns original terminal progress unchanged")
				}
			}
			var after json.RawMessage
			require.NoError(t, pool.QueryRow(ctx, `SELECT envelope FROM review_loop_start_intents WHERE org_id=$1 AND id=$2`, orgID, id).Scan(&after), "read back the same retained envelope")
			require.Equal(t, before, after, "no recovery call may re-render or replace retained JSON")
			var counts [6]int
			require.NoError(t, pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM session_threads WHERE org_id=$1),
 (SELECT count(*) FROM review_loop_start_intents WHERE org_id=$1),(SELECT count(*) FROM session_review_loops WHERE org_id=$1),
 (SELECT count(*) FROM session_review_loop_passes WHERE org_id=$1),(SELECT count(*) FROM session_messages WHERE org_id=$1),
 (SELECT count(*) FROM jobs WHERE org_id=$1 AND job_type='reconcile_session_review_loop')`, orgID).Scan(&counts[0], &counts[1], &counts[2], &counts[3], &counts[4], &counts[5]), "read complete creation/effect counts after recovery")
			expected := [6]int{2, 1, 1, 1, 0, 1}
			if runtime.threads != nil {
				expected[4] = 1
				if sendFault != nil {
					expected[4] = 0
				}
				if tt.name == "actual send accepted pointer conflict" {
					expected[4] = 2
				}
			}
			if tt.refused {
				expected = [6]int{2, 1, 0, 0, 0, 1}
			}
			require.Equal(t, expected, counts, "T0/E1/T1 must retain one exact graph and no premature message")
			require.Equal(t, 1, runtime.reads, "RecoverStart must never rerun fresh session preflights")
			expectedSends := 1
			if tt.refused || tt.phase == "claim" && tt.commit {
				expectedSends = 0
			}
			require.Equal(t, expectedSends, runtime.sends, "only one committed claim may release the initial send; uncertain recovery never resends")
			if runtime.threads != nil && sendFault == nil && tt.name != "actual send accepted pointer conflict" && !(tt.phase == "settle" && !tt.commit) {
				var linkage [3]int64
				require.NoError(t, pool.QueryRow(ctx, `SELECT l.startup_send_message_id,p.review_message_id,c.message_id
 FROM session_review_loops l JOIN session_review_loop_passes p ON p.org_id=l.org_id AND p.session_id=l.session_id AND p.loop_id=l.id AND p.id=l.startup_send_pass_id
 JOIN caller_send_command_stamps c ON c.org_id=l.org_id AND c.owner_id=l.id AND c.purpose='review-loop-initial-send'
 WHERE l.org_id=$1 AND l.id=$2`, orgID, id).Scan(&linkage[0], &linkage[1], &linkage[2]), "acceptance must retain real stamp and atomic first-pass linkage")
				require.Positive(t, linkage[0], "accepted startup must point to a genuine message")
				require.Equal(t, [3]int64{linkage[0], linkage[0], linkage[0]}, linkage, "owner, pass and immutable stamp must agree on the original message")
			}
			if tt.name == "actual send accepted pointer conflict" || tt.phase == "settle" && !tt.commit {
				var ownerMessage, passMessage *int64
				var detail *string
				var seq int32
				require.NoError(t, pool.QueryRow(ctx, `SELECT l.startup_send_message_id,p.review_message_id,l.startup_send_detail,l.startup_send_attempt_seq FROM session_review_loops l JOIN session_review_loop_passes p ON p.org_id=l.org_id AND p.session_id=l.session_id AND p.id=l.startup_send_pass_id WHERE l.org_id=$1 AND l.id=$2`, orgID, id).Scan(&ownerMessage, &passMessage, &detail, &seq), "read the complete owner and pass outcome after the failed acceptance unit")
				require.Nil(t, ownerMessage, "failed acceptance must not expose a partial accepted owner")
				require.Equal(t, int32(1), seq, "failure must retain the original attempt")
				if tt.name == "actual send accepted pointer conflict" {
					reason := "pass_pointer_conflict"
					require.Equal(t, &reason, detail, "separate attention CAS records only the finite pointer conflict")
					require.NotNil(t, passMessage, "conflicting positive pointer remains intact")
				} else {
					require.Nil(t, passMessage, "whole acceptance rollback must also undo the first-pass pointer")
				}
			}
		})
	}
}
