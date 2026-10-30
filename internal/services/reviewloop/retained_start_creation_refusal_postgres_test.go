package reviewloop

import (
	"context"
	"encoding/json"
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

type retainedRefusalRuntime struct {
	RuntimeAdapter
	beforeCreate   func(context.Context, threadsvc.CreateThreadInput) error
	creates, sends int
}

func (r *retainedRefusalRuntime) CreateThread(ctx context.Context, input threadsvc.CreateThreadInput) (*models.SessionThread, error) {
	r.creates++
	if r.beforeCreate != nil {
		if err := r.beforeCreate(ctx, input); err != nil {
			return nil, err
		}
	}
	return r.RuntimeAdapter.CreateThread(ctx, input)
}
func (r *retainedRefusalRuntime) SendMessage(ctx context.Context, input threadsvc.SendMessageInput) (*threadsvc.SendMessageResult, error) {
	r.sends++
	return r.RuntimeAdapter.SendMessage(ctx, input)
}

type retainedRefusalTx struct {
	pgx.Tx
	prepared, refused bool
}

func (t *retainedRefusalTx) Exec(ctx context.Context, query string, args ...any) (pgconn.CommandTag, error) {
	t.prepared = t.prepared || strings.Contains(query, "INSERT INTO review_loop_start_intents")
	t.refused = t.refused || strings.Contains(query, "SET state='refused'")
	return t.Tx.Exec(ctx, query, args...)
}

type retainedRefusalRunner struct {
	base           threadsvc.SendTransactions
	afterT0        func() error
	loseRefusalACK bool
	prepared, lost bool
}

func (r *retainedRefusalRunner) Run(ctx context.Context, work func(context.Context, pgx.Tx) error) error {
	prepared, refused := false, false
	err := r.base.Run(ctx, func(unitCtx context.Context, tx pgx.Tx) error {
		prepared, refused = false, false
		capture := &retainedRefusalTx{Tx: tx}
		if err := work(unitCtx, capture); err != nil {
			return err
		}
		prepared, refused = capture.prepared, capture.refused
		return nil
	})
	if err != nil {
		return err
	}
	if prepared && !r.prepared {
		r.prepared = true
		if r.afterT0 != nil {
			return r.afterT0()
		}
	}
	if refused && r.loseRefusalACK && !r.lost {
		r.lost = true
		return errRetainedStartAck
	}
	return nil
}

func retainedRefusalFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool, source models.ReviewLoopSource) (uuid.UUID, models.Session, models.SessionPublication, StartReviewLoopRequest, *retainedRefusalRuntime, threadsvc.SendTransactions) {
	t.Helper()
	org, session, publication := retainedPublicationFixture(t, ctx, pool)
	revision, head := int64(1), "synthetic-creation-head"
	req := StartReviewLoopRequest{MaxPasses: 2, Source: source, ReviewRequired: true}
	if source == models.ReviewLoopSourcePublication {
		req.ChangesetID = &publication.ChangesetID
		req.WorkspaceRevision = &revision
		req.DesiredHeadSHA = &head
	}
	if source == models.ReviewLoopSourceAutomation {
		automation, run := uuid.New(), uuid.New()
		_, err := pool.Exec(ctx, `INSERT INTO automations(id,org_id,name,goal) VALUES($1,$2,'Synthetic retained caller','Verify finite creation refusal')`, automation, org)
		require.NoError(t, err, "seed only this isolated caller automation")
		_, err = pool.Exec(ctx, `INSERT INTO automation_runs(id,org_id,automation_id,session_id,goal_snapshot,triggered_by) VALUES($1,$2,$3,$4,'Synthetic retained caller','manual')`, run, org, automation, session.ID)
		require.NoError(t, err, "seed the genuine current automation caller")
		req.AutomationRunID = &run
	}
	sessions, threads, jobs := db.NewSessionStore(pool), db.NewSessionThreadStore(pool), db.NewJobStore(pool)
	deps, err := threadsvc.NewSendDependencies(pool, sessions, threads, jobs)
	require.NoError(t, err, "construct production thread send dependencies without changing budgets")
	service, err := threadsvc.NewService(threads, sessions, db.NewSessionMessageStore(pool), db.NewSessionLogStore(pool), jobs, deps, zerolog.Nop())
	require.NoError(t, err, "use actual shared preflight, exact-ID E1 and sender")
	service.SetThreadInboxStore(db.NewThreadInboxStore(pool), pool)
	base, err := db.NewThreadSendTransactions(pool)
	require.NoError(t, err, "use unchanged production bounded creation transactions")
	return org, session, publication, req, &retainedRefusalRuntime{RuntimeAdapter: RuntimeAdapter{Sessions: sessions, Threads: service}}, base
}

func fillRetainedCreationCapacity(t *testing.T, ctx context.Context, pool *pgxpool.Pool, org, session uuid.UUID) {
	t.Helper()
	threads, err := db.NewSessionThreadStore(pool).ListBySession(ctx, org, session)
	require.NoError(t, err, "read actual visible rows before filling ordinary capacity")
	for n := len(threads); n < models.MaxThreadsPerSession; n++ {
		row := models.SessionThread{OrgID: org, SessionID: session, AgentType: models.AgentTypeClaudeCode, Label: "Synthetic occupied lane", Status: models.ThreadStatusIdle}
		require.NoError(t, db.NewSessionThreadStore(pool).Create(ctx, &row, models.MaxThreadsPerSession), "fill ordinary visible capacity through actual guarded creation")
	}
}

func installRetainedCreationRefusal(t *testing.T, ctx context.Context, pool *pgxpool.Pool, org, session uuid.UUID, publication models.SessionPublication, kind string) {
	t.Helper()
	switch kind {
	case "capacity":
		fillRetainedCreationCapacity(t, ctx, pool, org, session)
	case "code review owned":
		pr := uuid.New()
		_, err := pool.Exec(ctx, `INSERT INTO pull_requests(id,org_id,github_pr_number,github_pr_url,github_repo,title) VALUES($1,$2,1,'https://example.invalid/pull/1','synthetic/creation','Synthetic ownership')`, pr, org)
		require.NoError(t, err, "create the real tenant-owned PR referenced by code-review ownership")
		_, err = pool.Exec(ctx, `UPDATE sessions SET code_review_owner_pr_id=$3 WHERE org_id=$1 AND id=$2`, org, session, pr)
		require.NoError(t, err, "install actual code-review ownership without bypassing its trigger")
	case "automation owned":
		automation, target, generation := uuid.New(), uuid.New(), uuid.New()
		_, err := pool.Exec(ctx, `INSERT INTO automations(id,org_id,name,goal) VALUES($1,$2,'Synthetic retained owner','Own the existing checkout')`, automation, org)
		require.NoError(t, err, "create the automation that owns the target")
		_, err = pool.Exec(ctx, `INSERT INTO automation_targets(id,org_id,automation_id,repository_id,target_kind,target_key) VALUES($1,$2,$3,$4,'github_pull_request','1')`, target, org, automation, publication.RepositoryID)
		require.NoError(t, err, "create an actual owned target for this synthetic repository")
		_, err = pool.Exec(ctx, `INSERT INTO automation_target_sessions(id,org_id,target_id,generation,session_id) VALUES($1,$2,$3,1,$4)`, generation, org, target, session)
		require.NoError(t, err, "create the exact automation ownership generation")
		_, err = pool.Exec(ctx, `UPDATE sessions SET automation_owner_generation_id=$3 WHERE org_id=$1 AND id=$2`, org, session, generation)
		require.NoError(t, err, "install ownership visible to the actual ordinary thread guard")
	default:
		t.Fatalf("unknown finite creation fixture %q", kind)
	}
}

func retainedRefusalEffects(t *testing.T, ctx context.Context, pool *pgxpool.Pool, org uuid.UUID) [7]int {
	t.Helper()
	var got [7]int
	err := pool.QueryRow(ctx, `SELECT
 (SELECT count(*) FROM review_loop_start_intents WHERE org_id=$1),
 (SELECT count(*) FROM review_loop_start_intents WHERE org_id=$1 AND state='prepared' AND due_at IS NOT NULL),
 (SELECT count(*) FROM session_review_loops WHERE org_id=$1),
 (SELECT count(*) FROM session_review_loop_passes WHERE org_id=$1),
 (SELECT count(*) FROM session_messages WHERE org_id=$1),
 (SELECT count(*) FROM thread_inbox_entries WHERE org_id=$1),
 (SELECT count(*) FROM caller_send_command_stamps WHERE org_id=$1)`, org).Scan(&got[0], &got[1], &got[2], &got[3], &got[4], &got[5], &got[6])
	require.NoError(t, err, "observe complete retained, loop, pass, message, inbox and stamp effects in the actual database")
	return got
}

func TestRetainedCreationRefusalsPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, kind, detail string
		source             models.ReviewLoopSource
		afterT0            bool
	}{}
	for _, source := range []models.ReviewLoopSource{models.ReviewLoopSourceManual, models.ReviewLoopSourcePublication, models.ReviewLoopSourceAutomation} {
		for _, kind := range []struct{ name, detail string }{{"capacity", "creation_thread_capacity"}, {"code review owned", "creation_code_review_owned"}, {"automation owned", "creation_automation_owned"}} {
			for _, after := range []bool{false, true} {
				phase := "before T0"
				if after {
					phase = "after T0"
				}
				tests = append(tests, struct {
					name, kind, detail string
					source             models.ReviewLoopSource
					afterT0            bool
				}{string(source) + "/" + kind.name + "/" + phase, kind.name, kind.detail, source, after})
			}
		}
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
			defer cancel()
			pool := testpostgres.NewPool(t, ctx, "../../../migrations", 2)
			require.Equal(t, int32(2), pool.Config().MaxConns, "creation refusal keeps the bounded two-connection test pool")
			org, session, publication, req, runtime, base := retainedRefusalFixture(t, ctx, pool, tt.source)
			install := func() error {
				installRetainedCreationRefusal(t, ctx, pool, org, session.ID, publication, tt.kind)
				return nil
			}
			runner := &retainedRefusalRunner{base: base}
			if tt.afterT0 {
				runner.afterT0 = install
			} else {
				require.NoError(t, install(), "install the refusal before ordinary P0")
			}
			svc := NewService(db.NewSessionReviewLoopStore(pool), runtime, WithStartTransactions(runner))
			loop, err := svc.Start(ctx, org, session.ID, req)
			require.Nil(t, loop, "known creation refusal must not return a created loop")
			require.NotErrorIs(t, err, ErrReviewLoopStartPending, "a deterministic pre-row refusal must not report pending confirmation or API 503")
			var id uuid.UUID
			expected := [7]int{}
			if tt.afterT0 {
				require.ErrorIs(t, err, ErrReviewLoopStartRefused, "raced eligibility loss must durably resolve the prepared original")
				expected[0] = 1
				var state, detail string
				var due *time.Time
				require.NoError(t, pool.QueryRow(ctx, `SELECT id,state,detail,due_at FROM review_loop_start_intents WHERE org_id=$1 AND session_id=$2`, org, session.ID).Scan(&id, &state, &detail, &due), "read actual durable original refusal")
				require.Equal(t, []string{"refused", tt.detail}, []string{state, detail}, "the original receives the exact bounded refusal detail")
				require.Nil(t, due, "a finite refused creation has no later automatic due work")
				if tt.source == models.ReviewLoopSourcePublication {
					current, err := db.NewSessionPublicationStore(pool).GetByChangeset(ctx, org, session.ID, publication.ChangesetID)
					require.NoError(t, err, "read publication caller after refusal")
					require.Equal(t, &id, current.ReviewStartID, "finite failure retains the exact publication generation")
				}
				if tt.source == models.ReviewLoopSourceAutomation {
					current, err := db.NewAutomationRunStore(pool).GetByRunID(ctx, org, *req.AutomationRunID)
					require.NoError(t, err, "read automation caller after refusal")
					require.Equal(t, &id, current.ReviewStartID, "finite failure retains the exact automation generation")
				}
			} else {
				switch tt.kind {
				case "capacity":
					require.ErrorIs(t, err, db.ErrThreadLimitReached, "P0 preserves ordinary visible capacity failure")
				case "code review owned":
					var owned *models.SessionCodeReviewOwnedError
					require.ErrorAs(t, err, &owned, "P0 preserves existing typed code-review ownership failure")
				case "automation owned":
					require.ErrorIs(t, err, models.ErrSessionAutomationOwned, "P0 preserves existing automation ownership failure")
				}
				require.Equal(t, 0, runtime.creates, "ordinary P0 refusal never invokes E1")
			}
			require.Equal(t, expected, retainedRefusalEffects(t, ctx, pool, org), "known refusal cannot leave an unresolved owner or any execution graph")
			_, err = pool.Exec(ctx, `UPDATE sessions SET code_review_owner_pr_id=NULL,automation_owner_generation_id=NULL WHERE org_id=$1 AND id=$2`, org, session.ID)
			require.NoError(t, err, "release only the synthetic ownership after its refusal is settled")
			_, err = pool.Exec(ctx, `UPDATE session_threads SET archived_at=statement_timestamp() WHERE org_id=$1 AND session_id=$2 AND id<>$3`, org, session.ID, session.PrimaryThreadID)
			require.NoError(t, err, "free ordinary capacity after the refused request")
			creates := runtime.creates
			processed, err := svc.ReconcileRetainedStarts(ctx, org, 10)
			require.NoError(t, err, "freed capacity does not revive a finite old request")
			require.Equal(t, 0, processed, "no refused request is due after mutable eligibility improves")
			if tt.afterT0 {
				_, err = svc.RecoverStart(ctx, org, session.ID, id)
				require.ErrorIs(t, err, ErrReviewLoopStartRefused, "explicit same-ID recovery preserves finite refusal after capacity frees")
			}
			require.Equal(t, []int{creates, 0}, []int{runtime.creates, runtime.sends}, "later observation cannot create a new lane or send the frozen prompt")
			require.Equal(t, expected, retainedRefusalEffects(t, ctx, pool, org), "later capacity changes cannot create a loop, pass, message, inbox or stamp for the refused original")
		})
	}
}

func TestRetainedCreationUncertaintyAndAdoptionPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name                             string
		commitRow, typed, loseRefusalACK bool
	}{
		{name: "raw pre-row transport uncertainty"},
		{name: "committed E1 acknowledgement unavailable", commitRow: true},
		{name: "competing same-ID row wins stale capacity refusal", commitRow: true, typed: true},
		{name: "durable refusal acknowledgement unavailable", typed: true, loseRefusalACK: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
			defer cancel()
			pool := testpostgres.NewPool(t, ctx, "../../../migrations", 2)
			org, session, _, req, runtime, base := retainedRefusalFixture(t, ctx, pool, models.ReviewLoopSourceManual)
			claim := &retainedStartFaultRunner{base: base, phase: "claim", commit: true}
			runner := &retainedRefusalRunner{base: claim, loseRefusalACK: tt.loseRefusalACK}
			first := true
			runtime.beforeCreate = func(ctx context.Context, input threadsvc.CreateThreadInput) error {
				if !first {
					return nil
				}
				first = false
				if tt.commitRow {
					// The independent E1 creator really commits the same retained ID
					// before the stale refusal reaches its locked fresh-row check.
					done := make(chan error, 1)
					go func() { _, err := runtime.RuntimeAdapter.CreateThread(ctx, input); done <- err }()
					require.NoError(t, <-done, "a competing actual E1 owns and commits exactly the same original row")
					fillRetainedCreationCapacity(t, ctx, pool, org, session.ID)
				}
				if tt.typed {
					return db.ErrThreadLimitReached
				}
				return errRetainedStartAck
			}
			svc := NewService(db.NewSessionReviewLoopStore(pool), runtime, WithStartTransactions(runner))
			loop, err := svc.Start(ctx, org, session.ID, req)
			var id uuid.UUID
			if tt.typed && tt.commitRow {
				require.NoError(t, err, "a committed exact same-ID row must defeat stale capacity refusal")
				require.NotNil(t, loop, "same-ID adoption completes the original T1")
				id = loop.ID
				require.True(t, claim.fired, "the actual startup claim commits before suppressing its acknowledgement")
			} else {
				var pending *StartPendingError
				require.ErrorAs(t, err, &pending, "ambiguous E1 or refusal commit acknowledgement stays pending")
				id = pending.LoopID
			}
			var before json.RawMessage
			require.NoError(t, pool.QueryRow(ctx, `SELECT to_jsonb(s) FROM review_loop_start_intents s WHERE org_id=$1 AND id=$2`, org, id).Scan(&before), "capture actual retained evidence after the first outcome")
			var state string
			require.NoError(t, pool.QueryRow(ctx, `SELECT state FROM review_loop_start_intents WHERE org_id=$1 AND id=$2`, org, id).Scan(&state), "read authoritative state without inferring rollback from an error")
			if tt.loseRefusalACK {
				require.True(t, runner.lost, "the refusal transaction really commits before its acknowledgement is hidden")
				require.Equal(t, "refused", state, "unknown refusal acknowledgement cannot reclassify a committed finite outcome as prepared")
				runtime.beforeCreate = nil
				_, err = svc.RecoverStart(ctx, org, session.ID, id)
				require.ErrorIs(t, err, ErrReviewLoopStartRefused, "later positive read confirms the same committed finite refusal")
				var after json.RawMessage
				require.NoError(t, pool.QueryRow(ctx, `SELECT to_jsonb(s) FROM review_loop_start_intents s WHERE org_id=$1 AND id=$2`, org, id).Scan(&after), "read complete original after lost refusal ACK recovery")
				require.Equal(t, before, after, "known refusal replay preserves every frozen field and timestamp")
				require.Equal(t, [7]int{1, 0, 0, 0, 0, 0, 0}, retainedRefusalEffects(t, ctx, pool, org), "refusal commit uncertainty cannot authorize E1 replay, loop creation or send")
			} else if !tt.typed {
				require.Equal(t, "prepared", state, "raw errors never become deterministic refusal even when no row is yet visible")
				runtime.beforeCreate = nil
				loop, err = svc.RecoverStart(ctx, org, session.ID, id)
				require.NoError(t, err, "later actual creation or adoption must complete only the original pending identity")
				require.Equal(t, id, loop.ID, "uncertainty recovery never allocates a second start")
			}
			require.Equal(t, 0, runtime.sends, "neither unknown E1 nor unacknowledged actual startup claim permits sending")
			if !tt.loseRefusalACK {
				require.Equal(t, [7]int{1, 0, 1, 1, 0, 0, 0}, retainedRefusalEffects(t, ctx, pool, org), "positive same-ID recovery retains exactly one original start, loop and first pass")
			}
		})
	}
}

var _ Runtime = (*retainedRefusalRuntime)(nil)
var _ threadsvc.SendTransactions = (*retainedRefusalRunner)(nil)
