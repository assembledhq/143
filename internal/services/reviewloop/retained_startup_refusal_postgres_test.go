package reviewloop

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	threadsvc "github.com/assembledhq/143/internal/services/thread"
	testpostgres "github.com/assembledhq/143/internal/testutil/postgres"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

var errRetainedStartupRefusalBoundary = errors.New("synthetic startup refusal transaction acknowledgement unavailable")

type retainedStartupRefusalRuntime struct {
	*retainedStartTestRuntime
	sendErr error
}

func (r *retainedStartupRefusalRuntime) SendMessage(_ context.Context, input threadsvc.SendMessageInput) (*threadsvc.SendMessageResult, error) {
	r.sends++
	if r.pool.Stat().AcquiredConns() != 0 {
		return nil, errors.New("startup send entered with an owned database checkout")
	}
	if input.CallerCommand == nil {
		return nil, errors.New("startup send did not carry its retained command proof")
	}
	return nil, r.sendErr
}

type retainedStartupRefusalFaultTx struct {
	pgx.Tx
	phase        string
	failTerminal bool
}

func (t *retainedStartupRefusalFaultTx) QueryRow(ctx context.Context, query string, args ...any) pgx.Row {
	if strings.Contains(query, "UPDATE session_review_loops l SET") && len(args) > 12 {
		if operation, ok := args[12].(string); ok && (operation == "refuse" || operation == "claim") {
			t.phase = operation
		}
	}
	if t.phase == "refuse" && strings.Contains(query, "SET status = @status") && t.failTerminal {
		return retainedStartupRefusalErrorRow{err: errRetainedStartupRefusalBoundary}
	}
	return t.Tx.QueryRow(ctx, query, args...)
}

type retainedStartupRefusalErrorRow struct{ err error }

func (r retainedStartupRefusalErrorRow) Scan(...any) error { return r.err }

type retainedStartupRefusalFaultRunner struct {
	base         threadsvc.SendTransactions
	phase        string
	commit       bool
	failTerminal bool
	fired        bool
}

func (r *retainedStartupRefusalFaultRunner) Run(ctx context.Context, work func(context.Context, pgx.Tx) error) error {
	selected := false
	err := r.base.Run(ctx, func(ctx context.Context, tx pgx.Tx) error {
		selected = false
		capture := &retainedStartupRefusalFaultTx{Tx: tx, failTerminal: r.failTerminal && !r.fired}
		workErr := work(ctx, capture)
		selected = !r.fired && r.phase != "" && capture.phase == r.phase
		if workErr != nil {
			return workErr
		}
		if selected && !r.commit {
			return errRetainedStartupRefusalBoundary
		}
		return nil
	})
	if !selected {
		return err
	}
	r.fired = true
	if err != nil {
		return err
	}
	return errRetainedStartupRefusalBoundary
}

type retainedStartupRefusalReceipt struct {
	LoopStatus   models.ReviewLoopStatus
	State        string
	Attempt      uuid.UUID
	AttemptSeq   int
	WriterSeq    int
	Due          bool
	Completed    bool
	Resolution   *string
	Detail       *string
	MessageID    *int64
	PassStatus   models.ReviewLoopPassStatus
	PassMessage  *int64
	StartupInput json.RawMessage
	Digest       string
}

func readRetainedStartupRefusalReceipt(t *testing.T, ctx context.Context, q db.DBTX, orgID, loopID uuid.UUID) retainedStartupRefusalReceipt {
	t.Helper()
	var receipt retainedStartupRefusalReceipt
	require.NoError(t, q.QueryRow(ctx, `SELECT l.status,l.startup_send_state,l.startup_send_attempt_id,
 l.startup_send_attempt_seq,l.startup_send_writer_seq,l.startup_send_due_at IS NOT NULL,l.completed_at IS NOT NULL,
 l.startup_send_resolution,l.startup_send_detail,l.startup_send_message_id,p.status,p.review_message_id,
 l.startup_send_input,l.startup_send_digest FROM session_review_loops l
 JOIN session_review_loop_passes p ON p.org_id=l.org_id AND p.session_id=l.session_id AND p.loop_id=l.id AND p.id=l.startup_send_pass_id
 WHERE l.org_id=$1 AND l.id=$2`, orgID, loopID).Scan(&receipt.LoopStatus, &receipt.State, &receipt.Attempt,
		&receipt.AttemptSeq, &receipt.WriterSeq, &receipt.Due, &receipt.Completed, &receipt.Resolution, &receipt.Detail,
		&receipt.MessageID, &receipt.PassStatus, &receipt.PassMessage, &receipt.StartupInput, &receipt.Digest),
		"read the actual constrained startup receipt and its bound original pass")
	return receipt
}

func TestRetainedStartupRefusalPostgres(t *testing.T) {
	t.Parallel()
	callers := []struct {
		name   string
		source models.ReviewLoopSource
	}{
		{name: "manual", source: models.ReviewLoopSourceManual},
		{name: "publication", source: models.ReviewLoopSourcePublication},
		{name: "automation", source: models.ReviewLoopSourceAutomation},
	}
	for _, caller := range callers {
		t.Run(caller.name, func(t *testing.T) {
			t.Parallel()
			tests := []struct {
				name         string
				phase        string
				commit       bool
				failTerminal bool
				unknownSend  bool
				state        string
				status       models.ReviewLoopStatus
				writerSeq    int
				sends        int
			}{
				{name: "known refusal retires atomically", state: "refused", status: models.ReviewLoopStatusFailed, writerSeq: 3, sends: 1},
				{name: "refusal commit with lost acknowledgement", phase: "refuse", commit: true, state: "refused", status: models.ReviewLoopStatusFailed, writerSeq: 3, sends: 1},
				{name: "refusal rollback with lost acknowledgement", phase: "refuse", state: "attempting", status: models.ReviewLoopStatusRunning, writerSeq: 1, sends: 1},
				{name: "terminal write failure rolls refusal back", phase: "refuse", failTerminal: true, state: "attempting", status: models.ReviewLoopStatusRunning, writerSeq: 1, sends: 1},
				{name: "unknown send takes precedence over refusal", unknownSend: true, state: "unknown", status: models.ReviewLoopStatusRunning, writerSeq: 2, sends: 1},
				{name: "claim commit with lost acknowledgement never sends", phase: "claim", commit: true, state: "attempting", status: models.ReviewLoopStatusRunning, writerSeq: 1},
			}
			for _, tt := range tests {
				t.Run(tt.name, func(t *testing.T) {
					t.Parallel()
					ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
					defer cancel()
					pool := testpostgres.NewPool(t, ctx, "../../../migrations", 2)
					orgID, session, publication := retainedPublicationFixture(t, ctx, pool)
					req := StartReviewLoopRequest{Source: caller.source, MaxPasses: 2, ReviewRequired: true}
					if caller.source == models.ReviewLoopSourcePublication {
						revision, head := int64(1), "synthetic startup refusal head"
						req.ChangesetID, req.WorkspaceRevision, req.DesiredHeadSHA = &publication.ChangesetID, &revision, &head
					}
					if caller.source == models.ReviewLoopSourceAutomation {
						automationID, runID := uuid.New(), uuid.New()
						_, err := pool.Exec(ctx, `INSERT INTO automations(id,org_id,name,goal) VALUES($1,$2,'Synthetic startup refusal','Retain refusal receipt')`, automationID, orgID)
						require.NoError(t, err, "create an isolated automation caller without external work")
						_, err = pool.Exec(ctx, `INSERT INTO automation_runs(id,org_id,automation_id,goal_snapshot,session_id,triggered_by) VALUES($1,$2,$3,'Synthetic startup refusal',$4,'manual')`, runID, orgID, automationID, session.ID)
						require.NoError(t, err, "create the genuine automation run linked to this session")
						req.AutomationRunID = &runID
					}
					base, err := db.NewThreadSendTransactions(pool)
					require.NoError(t, err, "use the production bounded transaction owner with a two-connection pool")
					runner := &retainedStartupRefusalFaultRunner{base: base, phase: tt.phase, commit: tt.commit, failTerminal: tt.failTerminal}
					sendErr := threadsvc.ErrCallerCommandRequiresLegacy
					if tt.unknownSend {
						sendErr = errors.Join(threadsvc.ErrCommandOutcomeUnknown, sendErr)
					}
					runtime := &retainedStartupRefusalRuntime{retainedStartTestRuntime: &retainedStartTestRuntime{pool: pool}, sendErr: sendErr}
					service := NewService(db.NewSessionReviewLoopStore(pool), runtime, WithStartTransactions(runner))
					loop, err := service.Start(ctx, orgID, session.ID, req)
					require.NoError(t, err, "known T1 creation returns its durable admission even when later startup acknowledgement is uncertain")
					require.NotNil(t, loop, "startup outcomes retain the original known-created loop identity")
					require.Equal(t, tt.phase != "", runner.fired, "fault cases must reach the actual claim or refusal transaction boundary")
					receipt := readRetainedStartupRefusalReceipt(t, ctx, pool, orgID, loop.ID)
					var resolution, detail *string
					if tt.state == "refused" {
						resolved, refused := "send_refusal", "stamped_active_unsupported"
						resolution, detail = &resolved, &refused
					}
					if tt.state == "unknown" {
						unknown := "unknown_ack"
						detail = &unknown
					}
					expected := retainedStartupRefusalReceipt{LoopStatus: tt.status, State: tt.state, Attempt: receipt.Attempt,
						AttemptSeq: 1, WriterSeq: tt.writerSeq, Due: tt.state != "refused", Completed: tt.state == "refused",
						Resolution: resolution, Detail: detail, PassStatus: models.ReviewLoopPassStatusReviewing,
						StartupInput: receipt.StartupInput, Digest: receipt.Digest}
					require.NotEqual(t, uuid.Nil, receipt.Attempt, "every outcome retains the actual committed original claim")
					require.Equal(t, expected, receipt, "only a committed known refusal retires the loop while retaining its receipt and legacy pass semantics")
					require.Equal(t, tt.status, loop.Status, "the safe response reflects the actual atomic loop retirement")
					admission := models.ReviewLoopAdmissionState(tt.state)
					require.Equal(t, &admission, loop.AdmissionState, "an uncertain claim or settlement never projects a known refusal")
					service.startTransactions = base
					recovered, err := service.RecoverStart(ctx, orgID, session.ID, loop.ID)
					require.NoError(t, err, "explicit same-ID recovery observes committed refusal or leaves unresolved evidence intact")
					require.Equal(t, loop.ID, recovered.ID, "recovery never allocates another generation or sends a replacement command")
					require.Equal(t, receipt, readRetainedStartupRefusalReceipt(t, ctx, pool, orgID, loop.ID), "same-ID recovery preserves the complete terminal receipt or original uncertain attempt")
					require.Equal(t, tt.sends, runtime.sends, "unknown claim and recovery cannot authorize duplicate startup sends")
					var counts [6]int
					require.NoError(t, pool.QueryRow(ctx, `SELECT
 (SELECT count(*) FROM session_review_loops WHERE org_id=$1 AND session_id=$2 AND status='running'),
 (SELECT count(*) FROM review_loop_start_intents WHERE org_id=$1 AND session_id=$2),
 (SELECT count(*) FROM session_review_loops WHERE org_id=$1 AND session_id=$2),
 (SELECT count(*) FROM session_review_loop_passes WHERE org_id=$1 AND session_id=$2),
 (SELECT count(*) FROM session_messages WHERE org_id=$1 AND session_id=$2),
 (SELECT count(*) FROM caller_send_command_stamps WHERE org_id=$1 AND session_id=$2)`, orgID, session.ID).
						Scan(&counts[0], &counts[1], &counts[2], &counts[3], &counts[4], &counts[5]), "read running-slot occupancy and all actual startup effects")
					running := 1
					if tt.state == "refused" {
						running = 0
					}
					require.Equal(t, [6]int{running, 1, 1, 1, 0, 0}, counts, "known refusal releases the unique session slot without creating a message, stamp, replacement loop or pass")
				})
			}
		})
	}
}

type retainedStartupRefusalRecoveryStore struct {
	*db.SessionReviewLoopStore
	failNext bool
	fired    bool
}

func (s *retainedStartupRefusalRecoveryStore) MarkLoopFailed(ctx context.Context, orgID, loopID uuid.UUID, summary string) error {
	if s.failNext {
		s.failNext, s.fired = false, true
		return errRetainedStartupRefusalBoundary
	}
	return s.SessionReviewLoopStore.MarkLoopFailed(ctx, orgID, loopID, summary)
}

func TestRetainedStartupRefusalRecoveryPostgres(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	pool := testpostgres.NewPool(t, ctx, "../../../migrations", 2)
	orgID, session, _ := retainedPublicationFixture(t, ctx, pool)
	base, err := db.NewThreadSendTransactions(pool)
	require.NoError(t, err, "use the production transaction budget for original creation and recovery")
	claimFault := &retainedStartupRefusalFaultRunner{base: base, phase: "claim", commit: true}
	runtime := &retainedStartupRefusalRuntime{retainedStartTestRuntime: &retainedStartTestRuntime{pool: pool}, sendErr: threadsvc.ErrCallerCommandRequiresLegacy}
	store := &retainedStartupRefusalRecoveryStore{SessionReviewLoopStore: db.NewSessionReviewLoopStore(pool), failNext: true}
	service := NewService(store, runtime, WithStartTransactions(claimFault))
	loop, err := service.Start(ctx, orgID, session.ID, StartReviewLoopRequest{Source: models.ReviewLoopSourceManual, MaxPasses: 2})
	require.NoError(t, err, "a lost claim acknowledgement still returns the original known-created loop")
	require.NotNil(t, loop, "the recovery fixture must use an actual committed T0 and T1")
	require.True(t, claimFault.fired, "hold startup immediately after its real claim commit")
	// Reproduce the older implementation's legal refusal-only settlement. No
	// trigger is bypassed: this is the exact attempting -> refused transition
	// whose missing loop retirement explicit recovery must finish.
	tag, err := pool.Exec(ctx, `UPDATE session_review_loops SET startup_send_state='refused',startup_send_due_at=NULL,
 startup_send_resolution='send_refusal',startup_send_detail='stamped_active_unsupported',
 startup_send_writer_seq=startup_send_writer_seq+1,startup_send_updated_at=statement_timestamp()
 WHERE org_id=$1 AND session_id=$2 AND id=$3 AND startup_send_state='attempting'`, orgID, session.ID, loop.ID)
	require.NoError(t, err, "reproduce a constrained historical refused receipt without weakening the schema")
	require.EqualValues(t, 1, tag.RowsAffected(), "the historical fixture must change the actual original claim only")
	before := readRetainedStartupRefusalReceipt(t, ctx, pool, orgID, loop.ID)
	service.startTransactions = base
	recovered, err := service.RecoverStart(ctx, orgID, session.ID, loop.ID)
	require.NoError(t, err, "a failed retirement returns only last confirmed loop progress for later recovery")
	require.True(t, store.fired, "the durable refusal must drive the existing safe failure transition")
	require.Equal(t, models.ReviewLoopStatusRunning, recovered.Status, "a known retirement failure cannot be reported as a completed transition")
	require.Equal(t, before, readRetainedStartupRefusalReceipt(t, ctx, pool, orgID, loop.ID), "failed retirement preserves the exact durable refusal and original pass")
	recovered, err = service.RecoverStart(ctx, orgID, session.ID, loop.ID)
	require.NoError(t, err, "same-ID explicit recovery must finish retirement after a known failure")
	require.Equal(t, models.ReviewLoopStatusFailed, recovered.Status, "recovery releases the original running slot without resending")
	expected := before
	expected.LoopStatus, expected.Completed, expected.WriterSeq = models.ReviewLoopStatusFailed, true, before.WriterSeq+1
	require.Equal(t, expected, readRetainedStartupRefusalReceipt(t, ctx, pool, orgID, loop.ID), "successful retirement changes only loop terminal progress and its required writer sequence")
	require.Equal(t, 0, runtime.sends, "neither a lost claim acknowledgement nor recovery of a refused receipt permits another send")
	_, err = store.GetRunningLoopBySession(ctx, orgID, session.ID)
	require.ErrorIs(t, err, pgx.ErrNoRows, "the original unique running slot is free after repaired retirement")
}
