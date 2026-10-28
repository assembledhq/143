package worker

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	reviewloopsvc "github.com/assembledhq/143/internal/services/reviewloop"
	threadsvc "github.com/assembledhq/143/internal/services/thread"
	testpostgres "github.com/assembledhq/143/internal/testutil/postgres"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

type retainedWakeRuntime struct {
	reviewloopsvc.RuntimeAdapter
	crash, unknown bool
	crashSession   uuid.UUID
	sends          int
}

func (r *retainedWakeRuntime) CreateThread(ctx context.Context, in threadsvc.CreateThreadInput) (*models.SessionThread, error) {
	if r.crash && (r.crashSession == uuid.Nil || r.crashSession == in.SessionID) {
		return nil, errors.New("synthetic crash before row-only creation")
	}
	return r.RuntimeAdapter.CreateThread(ctx, in)
}
func (r *retainedWakeRuntime) SendMessage(ctx context.Context, in threadsvc.SendMessageInput) (*threadsvc.SendMessageResult, error) {
	r.sends++
	result, err := r.RuntimeAdapter.SendMessage(ctx, in)
	if err == nil && r.unknown {
		return nil, threadsvc.ErrCommandOutcomeUnknown
	}
	return result, err
}

func TestRetainedReviewLoopWakeHandlerPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		unknown bool
		backlog bool
	}{{name: "prepared start after deleted wake"}, {name: "terminal owner with unacknowledged actual send", unknown: true}, {name: "created startup progresses behind disabled prepared backlog", unknown: true, backlog: true}, {name: "later prepared startup progresses behind persistently blocked prepared owners", backlog: true}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
			defer cancel()
			pool := testpostgres.NewPool(t, ctx, "../../migrations", 2)
			org := uuid.New()
			_, err := pool.Exec(ctx, `INSERT INTO organizations(id,name) VALUES($1,'Synthetic retained wake')`, org)
			require.NoError(t, err, "seed an organization without any integration")
			sessions, threads, jobs := db.NewSessionStore(pool), db.NewSessionThreadStore(pool), db.NewJobStore(pool)
			session := models.Session{OrgID: org, AgentType: models.AgentTypeClaudeCode, Status: models.SessionStatusIdle, AutonomyLevel: models.DefaultSessionAutonomy, TokenMode: models.SessionTokenModeLow, Origin: models.SessionOriginManual, InteractionMode: models.SessionInteractionModeInteractive, ValidationPolicy: models.SessionValidationPolicyOnSessionEnd}
			require.NoError(t, sessions.Create(ctx, &session), "create genuine session and primary thread")
			_, err = pool.Exec(ctx, `UPDATE sessions SET snapshot_key='synthetic/wake',sandbox_state='snapshotted' WHERE org_id=$1 AND id=$2`, org, session.ID)
			require.NoError(t, err, "make fresh startup eligible")
			_, err = pool.Exec(ctx, `UPDATE caller_send_intent_control SET intents_enabled=true WHERE singleton AND schema_ready AND dispatch_status_ready`)
			require.NoError(t, err, "enable only the disposable test database")
			deps, err := threadsvc.NewSendDependencies(pool, sessions, threads, jobs)
			require.NoError(t, err, "construct actual thread transactions and effects")
			threadService, err := threadsvc.NewService(threads, sessions, db.NewSessionMessageStore(pool), db.NewSessionLogStore(pool), jobs, deps, zerolog.Nop())
			require.NoError(t, err, "construct actual row-only creation and sender")
			threadService.SetThreadInboxStore(db.NewThreadInboxStore(pool), pool)
			runtime := &retainedWakeRuntime{RuntimeAdapter: reviewloopsvc.RuntimeAdapter{Sessions: sessions, Threads: threadService}, crash: !tt.unknown, unknown: tt.unknown}
			loops := db.NewSessionReviewLoopStore(pool)
			service := reviewloopsvc.NewService(loops, runtime, reviewloopsvc.WithStartTransactions(deps.Transactions))
			loop, err := service.Start(ctx, org, session.ID, reviewloopsvc.StartReviewLoopRequest{MaxPasses: 2})
			var id uuid.UUID
			if tt.unknown {
				require.NoError(t, err, "actual committed send with unknown acknowledgement still returns known creation")
				id = loop.ID
				if !tt.backlog {
					require.NoError(t, loops.CancelLoop(ctx, org, id), "terminalize owner before its original accepted send is observed")
				}
			} else {
				var pending *reviewloopsvc.StartPendingError
				require.ErrorAs(t, err, &pending, "crash retains T0 identity without a loop")
				id = pending.LoopID
			}
			runtime.crash = false
			if tt.backlog {
				other := session
				other.ID, other.PrimaryThreadID = uuid.Nil, nil
				require.NoError(t, sessions.Create(ctx, &other), "create a separate real session for prepared backlog without a running loop")
				_, err = pool.Exec(ctx, `UPDATE sessions SET snapshot_key='synthetic/backlog',sandbox_state='snapshotted' WHERE org_id=$1 AND id=$2`, org, other.ID)
				require.NoError(t, err, "make backlog preparations ordinarily eligible")
				runtime.crash = true
				runtime.crashSession = other.ID
				for range 51 {
					_, err = service.Start(ctx, org, other.ID, reviewloopsvc.StartReviewLoopRequest{MaxPasses: 2})
					require.ErrorIs(t, err, reviewloopsvc.ErrReviewLoopStartPending, "each genuine T0 remains prepared after a crash before E1")
				}
				_, err = pool.Exec(ctx, `UPDATE review_loop_start_intents SET due_at=statement_timestamp()-interval '1 hour' WHERE org_id=$1 AND session_id=$2 AND state='prepared'`, org, other.ID)
				require.NoError(t, err, "put more than a wake's limit of genuine prepared owners ahead of the created startup")
				if tt.unknown {
					_, err = pool.Exec(ctx, `UPDATE caller_send_intent_control SET intents_enabled=false WHERE singleton`)
					require.NoError(t, err, "disable new creation without disabling observation of already-created startup")
				}
			}
			_, err = pool.Exec(ctx, `DELETE FROM jobs WHERE org_id=$1 AND job_type='reconcile_session_review_loop'`, org)
			require.NoError(t, err, "remove the original wake without touching retained ownership")
			{
				timer := time.NewTimer(5100 * time.Millisecond)
				select {
				case <-timer.C:
				case <-ctx.Done():
					timer.Stop()
					t.Fatal("observation due interval exceeded local test deadline")
				}
			}
			require.NoError(t, db.NewCallerSendWakeStore(pool).RepairMissingWakes(ctx), "recover a wake without integration membership or historical job rows")
			var payload json.RawMessage
			require.NoError(t, pool.QueryRow(ctx, `SELECT payload FROM jobs WHERE org_id=$1 AND job_type='reconcile_session_review_loop' AND status='pending'`, org).Scan(&payload), "consume the actual repaired wake payload")
			handler := newReconcileSessionReviewLoopHandler(&Services{ReviewLoops: service}, zerolog.Nop())
			firstErr := handler(ctx, models.JobTypeReconcileSessionReviewLoop, payload)
			if tt.backlog {
				require.ErrorIs(t, firstErr, reviewloopsvc.ErrReviewLoopStartPending, "prepared failures remain explicit while independent startup observation progresses")
			} else {
				require.NoError(t, firstErr, "actual handler must recover original before stranded retirement")
			}
			// Assert progress after the first wake, before a later execution can
			// consume enough old prepared owners to hide category starvation.
			first, err := loops.GetLoopByID(ctx, org, id)
			if tt.backlog && !tt.unknown {
				require.ErrorIs(t, err, pgx.ErrNoRows, "first prepared batch must be genuinely blocked before later original creation can run")
			} else {
				require.NoError(t, err, "read original after first org-wide repaired wake")
				acceptedFirst := models.ReviewLoopAdmissionAccepted
				require.Equal(t, &acceptedFirst, first.AdmissionState, "one wake must observe created startup even behind more than its prepared-owner limit")
			}
			replayErr := handler(ctx, models.JobTypeReconcileSessionReviewLoop, payload)
			if tt.backlog {
				require.ErrorIs(t, replayErr, reviewloopsvc.ErrReviewLoopStartPending, "blocked originals remain unresolved without preventing observation replay")
			} else {
				require.NoError(t, replayErr, "replayed repaired wake cannot resend or recreate")
			}
			actual, err := loops.GetLoopByID(ctx, org, id)
			require.NoError(t, err, "read the actual retained loop after handler execution")
			accepted := models.ReviewLoopAdmissionAccepted
			require.Equal(t, &accepted, actual.AdmissionState, "actual handler must commit original accepted admission")
			expectedStatus := models.ReviewLoopStatusRunning
			if tt.unknown && !tt.backlog {
				expectedStatus = models.ReviewLoopStatusCancelled
			}
			require.Equal(t, expectedStatus, actual.Status, "late observation must preserve terminal owner status")
			require.Equal(t, 1, runtime.sends, "all retries and wake repair share one original send")
			var graph [4]int
			require.NoError(t, pool.QueryRow(ctx, `SELECT (SELECT count(*) FROM session_review_loops WHERE org_id=$1),(SELECT count(*) FROM session_review_loop_passes WHERE org_id=$1),(SELECT count(*) FROM session_messages WHERE org_id=$1),(SELECT count(*) FROM caller_send_command_stamps WHERE org_id=$1)`, org).Scan(&graph[0], &graph[1], &graph[2], &graph[3]), "count the durable original graph")
			require.Equal(t, [4]int{1, 1, 1, 1}, graph, "actual recovery must preserve exactly one loop, pass, message and immutable stamp")
		})
	}
}
