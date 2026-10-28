package reviewloop

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	testpostgres "github.com/assembledhq/143/internal/testutil/postgres"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/stretchr/testify/require"
)

type retainedStartGateRunner struct {
	base           *db.ThreadSendTransactions
	ready, release chan struct{}
}

func (r *retainedStartGateRunner) Run(ctx context.Context, work func(context.Context, pgx.Tx) error) error {
	gated := false
	err := r.base.Run(ctx, func(ctx context.Context, tx pgx.Tx) error {
		capture := &retainedStartTestTx{Tx: tx}
		if err := work(ctx, capture); err != nil {
			return err
		}
		if capture.phase == "T1" {
			gated = true
			close(r.ready)
			select {
			case <-r.release:
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		return nil
	})
	if gated && err == nil {
		return errRetainedStartAck
	}
	return err
}

func retainedPublicationFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool) (uuid.UUID, models.Session, models.SessionPublication) {
	t.Helper()
	orgID, integration, repository, changeset := uuid.New(), uuid.New(), uuid.New(), uuid.New()
	_, err := pool.Exec(ctx, `INSERT INTO organizations(id,name) VALUES($1,'Synthetic cancellation')`, orgID)
	require.NoError(t, err, "seed an isolated synthetic cancellation tenant")
	session := models.Session{OrgID: orgID, AgentType: models.AgentTypeClaudeCode, Status: models.SessionStatusIdle, AutonomyLevel: models.DefaultSessionAutonomy, TokenMode: models.SessionTokenModeLow, Origin: models.SessionOriginManual, InteractionMode: models.SessionInteractionModeInteractive, ValidationPolicy: models.SessionValidationPolicyOnSessionEnd}
	require.NoError(t, db.NewSessionStore(pool).Create(ctx, &session), "seed genuine session and primary thread")
	_, err = pool.Exec(ctx, `UPDATE sessions SET snapshot_key='synthetic/cancellation',sandbox_state='snapshotted' WHERE org_id=$1 AND id=$2`, orgID, session.ID)
	require.NoError(t, err, "provide the ordinary fresh-start checkpoint")
	_, err = pool.Exec(ctx, `INSERT INTO integrations(id,org_id,provider,status) VALUES($1,$2,'github','active')`, integration, orgID)
	require.NoError(t, err, "seed owned integration")
	_, err = pool.Exec(ctx, `INSERT INTO repositories(id,org_id,integration_id,github_id,full_name,clone_url,installation_id) VALUES($1,$2,$3,1,'synthetic/cancellation','https://example.invalid/cancellation.git',1)`, repository, orgID, integration)
	require.NoError(t, err, "seed owned synthetic repository")
	_, err = pool.Exec(ctx, `INSERT INTO session_changesets(id,org_id,session_id,order_index,title,target_branch,base_branch) VALUES($1,$2,$3,99,'Synthetic cancellation','main','main')`, changeset, orgID, session.ID)
	require.NoError(t, err, "seed exact changeset ownership")
	_, err = pool.Exec(ctx, `INSERT INTO session_publications(org_id,session_id,changeset_id,repository_id,base_branch,head_branch,state,review_gate_state) VALUES($1,$2,$3,$4,'main','synthetic/cancellation','review_pending','pending')`, orgID, session.ID, changeset, repository)
	require.NoError(t, err, "seed real publication caller")
	_, err = pool.Exec(ctx, `UPDATE caller_send_intent_control SET intents_enabled=true WHERE singleton AND schema_ready AND dispatch_status_ready`)
	require.NoError(t, err, "enable only this isolated migrated database")
	publication, err := db.NewSessionPublicationStore(pool).GetByChangeset(ctx, orgID, session.ID, changeset)
	require.NoError(t, err, "read the real caller before requesting a bypass")
	publication.Source = models.SessionPublicationSourceUser
	publication.TriggerKind = models.SessionPublicationTriggerExplicitAction
	publication.AutomaticPolicySource = models.PublicationPolicySourceExplicitAction
	publication.ReviewPolicySource = models.PublicationPolicySourceExplicitBypass
	publication.RequestPayload = []byte(`{"synthetic":true}`)
	return orgID, session, publication
}

func TestRetainedPublicationCancellationPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct{ name string }{
		{"bypass before T1"}, {"bypass waits behind T1"}, {"created pending startup"},
		{"committed claim before bypass"}, {"unknown send before bypass"}, {"legacy without retained start"}, {"foreign tenant bypass"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
			defer cancel()
			// Two contenders plus one independent lock observer; there is no fourth
			// connection that could mask an unintended nested checkout.
			pool := testpostgres.NewPool(t, ctx, "../../../migrations", 3)
			orgID, session, publication := retainedPublicationFixture(t, ctx, pool)
			base, err := db.NewThreadSendTransactions(pool)
			require.NoError(t, err, "use unchanged production transaction budgets")
			runtime := &retainedStartTestRuntime{pool: pool}
			runner := &retainedStartFaultRunner{base: base, phase: "T0", commit: true}
			service := NewService(db.NewSessionReviewLoopStore(pool), runtime, WithStartTransactions(runner))
			revision, head := int64(1), "synthetic-head"
			req := StartReviewLoopRequest{MaxPasses: 2, Source: models.ReviewLoopSourcePublication, ChangesetID: &publication.ChangesetID, WorkspaceRevision: &revision, DesiredHeadSHA: &head, ReviewRequired: true}
			var id uuid.UUID
			if tt.name == "legacy without retained start" {
				_, err = pool.Exec(ctx, `UPDATE caller_send_intent_control SET intents_enabled=false WHERE singleton`)
				require.NoError(t, err, "select the unchanged legacy row creation control")
				row := models.SessionReviewLoop{OrgID: orgID, SessionID: session.ID, ThreadID: session.PrimaryThreadID, Source: models.ReviewLoopSourcePublication, ChangesetID: &publication.ChangesetID, WorkspaceRevision: &revision, DesiredHeadSHA: &head, AgentType: models.AgentTypeClaudeCode, MaxPasses: 2, FixMode: models.ReviewLoopFixModeExhaustive, Status: models.ReviewLoopStatusRunning, ReviewRequired: true}
				pass := models.SessionReviewLoopPass{OrgID: orgID, SessionID: session.ID, PassIndex: 1, Status: models.ReviewLoopPassStatusReviewing}
				require.NoError(t, db.NewSessionReviewLoopStore(pool).CreateLoopWithInitialPass(ctx, &row, &pass), "create genuine legacy loop without retained columns")
				id = row.ID
				require.NoError(t, db.NewSessionPublicationStore(pool).LinkReviewLoop(ctx, orgID, session.ID, publication.ChangesetID, id, 2, revision, head), "attach legacy publication evidence")
			} else {
				_, err = service.Start(ctx, orgID, session.ID, req)
				var pending *StartPendingError
				require.ErrorAs(t, err, &pending, "retain a genuinely committed T0 while withholding only its application acknowledgement")
				require.True(t, runner.fired, "the injected acknowledgement must follow actual successful T0 work, not a fixture refusal")
				id = pending.LoopID
			}
			switch tt.name {
			case "bypass waits behind T1":
				gate := &retainedStartGateRunner{base: base, ready: make(chan struct{}), release: make(chan struct{})}
				service.startTransactions = gate
				recovered := make(chan error, 1)
				go func() { _, err := service.RecoverStart(ctx, orgID, session.ID, id); recovered <- err }()
				select {
				case <-gate.ready:
				case early := <-recovered:
					t.Fatalf("recovery returned before its T1 gate: %v", early)
				case <-ctx.Done():
					t.Fatal("T1 did not reach its held-lock boundary")
				}
				bypassed := make(chan error, 1)
				go func() { bypassed <- db.NewSessionPublicationStore(pool).ApplyReviewBypass(ctx, orgID, &publication) }()
				require.Eventually(t, func() bool {
					var waiting bool
					err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND pid<>pg_backend_pid() AND cardinality(pg_blocking_pids(pid))>0 AND query LIKE '%SELECT review_loop_id,review_start_id FROM session_publications%')`).Scan(&waiting)
					return err == nil && waiting
				}, time.Second, 5*time.Millisecond, "prove bypass has started its statement and is blocked on T1's actual caller lock")
				close(gate.release)
				require.ErrorIs(t, <-recovered, ErrReviewLoopStartPending, "lost T1 acknowledgement must prevent the service from claiming startup before bypass finishes")
				require.NoError(t, <-bypassed, "bypass must see the newly committed loop from its later snapshot without deadlocking")
				service.startTransactions = base
			case "created pending startup", "committed claim before bypass", "unknown send before bypass":
				switch tt.name {
				case "created pending startup":
					service.startTransactions = &retainedStartFaultRunner{base: base, phase: "T1", commit: true}
				case "committed claim before bypass":
					service.startTransactions = &retainedStartFaultRunner{base: base, phase: "claim", commit: true}
				default:
					service.startTransactions = base
				}
				_, err = service.RecoverStart(ctx, orgID, session.ID, id)
				if tt.name == "created pending startup" {
					require.ErrorIs(t, err, ErrReviewLoopStartPending, "hold startup pending after actual T1 commit")
				} else {
					require.NoError(t, err, "create the original loop before cancellation")
				}
				require.NoError(t, db.NewSessionPublicationStore(pool).ApplyReviewBypass(ctx, orgID, &publication), "bypass must cancel the owner while preserving any attempted send")
				service.startTransactions = base
			case "foreign tenant bypass":
				foreign := publication
				foreign.OrgID = uuid.Nil
				require.ErrorIs(t, db.NewSessionPublicationStore(pool).ApplyReviewBypass(ctx, uuid.New(), &foreign), pgx.ErrNoRows, "foreign scope cannot see or cancel this caller")
				return
			default:
				require.NoError(t, db.NewSessionPublicationStore(pool).ApplyReviewBypass(ctx, orgID, &publication), "bypass must support prepared and legacy callers")
			}
			expectedSends := 0
			if tt.name == "unknown send before bypass" {
				expectedSends = 1
			}
			if tt.name != "legacy without retained start" {
				_, err = service.RecoverStart(ctx, orgID, session.ID, id)
				if tt.name == "bypass before T1" {
					require.ErrorIs(t, err, ErrReviewLoopStartRefused, "prepared cancellation must refuse creation")
				} else {
					require.NoError(t, err, "created recovery must adopt cancelled owner without resending")
				}
				var caller uuid.UUID
				require.NoError(t, pool.QueryRow(ctx, `SELECT review_start_id FROM session_publications WHERE org_id=$1 AND session_id=$2 AND changeset_id=$3`, orgID, session.ID, publication.ChangesetID).Scan(&caller), "read retained caller identity after clearing mutable review evidence")
				require.Equal(t, id, caller, "bypass must preserve the original retained identity")
			}
			require.Equal(t, expectedSends, runtime.sends, "cancellation never creates send permission or resends an uncertain attempt")
			if tt.name == "bypass before T1" {
				var count int
				require.NoError(t, pool.QueryRow(ctx, `SELECT count(*) FROM session_review_loops WHERE org_id=$1 AND session_id=$2`, orgID, session.ID).Scan(&count), "read actual no-creation result")
				require.Zero(t, count, "cancellation winning before T1 exposes no loop or claimable interval")
				return
			}
			var status string
			var required bool
			var state *string
			var attempt *uuid.UUID
			var attemptSeq int32
			require.NoError(t, pool.QueryRow(ctx, `SELECT status,review_required,startup_send_state,startup_send_attempt_id,COALESCE(startup_send_attempt_seq,0) FROM session_review_loops WHERE org_id=$1 AND session_id=$2 AND id=$3`, orgID, session.ID, id).Scan(&status, &required, &state, &attempt, &attemptSeq), "inspect durable cancellation and dispatch state together")
			require.Equal(t, "cancelled", status, "the exact original loop must be cancelled")
			require.False(t, required, "preserve authenticated bypass's existing current-option behavior")
			expectedState := "cancelled"
			if tt.name == "committed claim before bypass" {
				expectedState = "attempting"
			}
			if tt.name == "unknown send before bypass" {
				expectedState = "unknown"
			}
			if tt.name == "legacy without retained start" {
				require.Nil(t, state, "legacy cancellation must not install a retained intent")
			} else {
				require.Equal(t, &expectedState, state, "cancel only unattempted pending startup; preserve attempted uncertainty")
			}
			if expectedState == "attempting" || expectedState == "unknown" {
				require.NotNil(t, attempt, "attempted cancellation must retain ownership")
				require.Equal(t, int32(1), attemptSeq, "cancellation must not replace or rewind the attempt")
			}
			require.Equal(t, models.SessionPublicationReviewGateNotRequired, publication.ReviewGateState, "bypass must retain its public gate behavior")
			require.Nil(t, publication.ReviewLoopID, "mutable review evidence is detached after bypass")
		})
	}
}

func TestRetainedPublicationWriterOrderingPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		apply func(context.Context, *db.SessionReviewLoopStore, uuid.UUID, uuid.UUID, uuid.UUID) error
	}{
		{"cancel", func(ctx context.Context, s *db.SessionReviewLoopStore, o, l, p uuid.UUID) error {
			return s.CancelLoop(ctx, o, l)
		}},
		{"clean pass first", func(ctx context.Context, s *db.SessionReviewLoopStore, o, l, p uuid.UUID) error {
			return s.MarkPassClean(ctx, o, l, p, models.ReviewLoopDecisionClean, "Synthetic clean")
		}},
		{"needs human pass first", func(ctx context.Context, s *db.SessionReviewLoopStore, o, l, p uuid.UUID) error {
			return s.MarkPassNeedsHumanDecision(ctx, o, l, p, models.ReviewLoopDecisionNeedsFix, "Synthetic needs human")
		}},
		{"failed", func(ctx context.Context, s *db.SessionReviewLoopStore, o, l, p uuid.UUID) error {
			return s.MarkLoopFailed(ctx, o, l, "Synthetic failure")
		}},
		{"refresh evidence", func(ctx context.Context, s *db.SessionReviewLoopStore, o, l, p uuid.UUID) error {
			return s.RefreshPublicationEvidence(ctx, o, l, 2, "synthetic-new-head")
		}},
		{"stranded unresolved excluded", func(ctx context.Context, s *db.SessionReviewLoopStore, o, l, p uuid.UUID) error {
			changed, err := s.RestartStrandedPublicationLoop(ctx, o, l, time.Now().Add(time.Hour), "Synthetic restart")
			if changed {
				return errors.New("unresolved startup was retired")
			}
			return err
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
			defer cancel()
			pool := testpostgres.NewPool(t, ctx, "../../../migrations", 3)
			orgID, session, publication := retainedPublicationFixture(t, ctx, pool)
			base, err := db.NewThreadSendTransactions(pool)
			require.NoError(t, err, "construct production transaction owner")
			runner := &retainedStartFaultRunner{base: base, phase: "T1", commit: true}
			service := NewService(db.NewSessionReviewLoopStore(pool), &retainedStartTestRuntime{pool: pool}, WithStartTransactions(runner))
			revision, head := int64(1), "synthetic-head"
			_, err = service.Start(ctx, orgID, session.ID, StartReviewLoopRequest{MaxPasses: 2, Source: models.ReviewLoopSourcePublication, ChangesetID: &publication.ChangesetID, WorkspaceRevision: &revision, DesiredHeadSHA: &head, ReviewRequired: true})
			var pending *StartPendingError
			require.ErrorAs(t, err, &pending, "retain actual T1 commit without releasing startup claim")
			require.True(t, runner.fired, "T1 must really have completed before the test withholds acknowledgement")
			loopID := pending.LoopID
			var passID uuid.UUID
			require.NoError(t, pool.QueryRow(ctx, `SELECT startup_send_pass_id FROM session_review_loops WHERE org_id=$1 AND id=$2`, orgID, loopID).Scan(&passID), "read exact bound first pass")
			require.NoError(t, db.NewSessionPublicationStore(pool).LinkReviewLoop(ctx, orgID, session.ID, publication.ChangesetID, loopID, 2, revision, head), "attach current caller before exercising ordinary writers")
			owner, err := pool.Begin(ctx)
			require.NoError(t, err, "hold caller serialization from an independent transaction")
			defer func() { _ = owner.Rollback(ctx) }()
			var locked uuid.UUID
			require.NoError(t, owner.QueryRow(ctx, `SELECT id FROM session_publications WHERE org_id=$1 AND session_id=$2 AND changeset_id=$3 FOR UPDATE`, orgID, session.ID, publication.ChangesetID).Scan(&locked), "take T1-compatible caller lock first")
			done := make(chan error, 1)
			go func() { done <- tt.apply(ctx, db.NewSessionReviewLoopStore(pool), orgID, loopID, passID) }()
			require.Eventually(t, func() bool {
				var waiting bool
				err := pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM pg_stat_activity WHERE datname=current_database() AND pid<>pg_backend_pid() AND cardinality(pg_blocking_pids(pid))>0 AND query LIKE '%SELECT review_start_id FROM session_publications%')`).Scan(&waiting)
				return err == nil && waiting
			}, time.Second, 5*time.Millisecond, "writer must wait at caller before taking the loop or pass lock")
			// If a pass-first writer inverted the order, these NOWAIT probes would fail.
			require.NoError(t, owner.QueryRow(ctx, `SELECT id FROM review_loop_start_intents WHERE org_id=$1 AND id=$2 FOR UPDATE NOWAIT`, orgID, loopID).Scan(&locked), "start remains unlocked while ordinary writer waits for caller")
			require.NoError(t, owner.QueryRow(ctx, `SELECT id FROM session_review_loops WHERE org_id=$1 AND id=$2 FOR UPDATE NOWAIT`, orgID, loopID).Scan(&locked), "loop remains unlocked while ordinary writer waits for caller")
			require.NoError(t, owner.QueryRow(ctx, `SELECT id FROM session_review_loop_passes WHERE org_id=$1 AND id=$2 FOR UPDATE NOWAIT`, orgID, passID).Scan(&locked), "even a pass-first terminal writer must not acquire pass before caller")
			require.NoError(t, owner.Commit(ctx), "release exact ordered serialization before completing the writer")
			require.NoError(t, <-done, "ordinary writer must complete without deadlock or startup schema refusal")
			var caller uuid.UUID
			require.NoError(t, pool.QueryRow(ctx, `SELECT review_start_id FROM session_publications WHERE org_id=$1 AND session_id=$2 AND changeset_id=$3`, orgID, session.ID, publication.ChangesetID).Scan(&caller), "inspect caller after writer completion")
			require.Equal(t, loopID, caller, "ordinary terminal and evidence writes preserve retained caller identity")
		})
	}
}
