package thread

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"
)

type legacyContentionAttempt struct {
	id         int64
	diagnostic uuid.UUID
	guards     sendPostgresGuardCounts
	before     json.RawMessage
	err        error
}
type legacyContentionUnit struct {
	attempts []legacyContentionAttempt
	err      error
}
type legacyContentionTransactions struct {
	base          SendTransactions
	fixture       sendPostgresFixture
	units         []legacyContentionUnit
	beforeAttempt func(int, int) error
}

func (r *legacyContentionTransactions) Run(ctx context.Context, work func(context.Context, pgx.Tx) error) error {
	number := len(r.units) + 1
	var unit legacyContentionUnit
	err := r.base.Run(ctx, func(unitCtx context.Context, tx pgx.Tx) error {
		if r.beforeAttempt != nil {
			if err := r.beforeAttempt(number, len(unit.attempts)+1); err != nil {
				return err
			}
		}
		attempt := legacyContentionAttempt{diagnostic: unitCtx.Value(sendDiagnosticContextKey{}).(sendDiagnostic).id}
		if err := tx.QueryRow(unitCtx, `SELECT txid_current()`).Scan(&attempt.id); err != nil {
			return err
		}
		var err error
		attempt.before, err = readSendPostgresSnapshot(unitCtx, tx, r.fixture)
		if err != nil {
			return err
		}
		attempt.err = work(unitCtx, sendPostgresGuardObservedTx{Tx: tx, counts: &attempt.guards})
		unit.attempts = append(unit.attempts, attempt)
		return attempt.err
	})
	unit.err = err
	r.units = append(r.units, unit)
	return err
}

type legacyLateLockPersistence struct {
	LegacySendPersistence
	t           *testing.T
	fixture     sendPostgresFixture
	lockComment uuid.UUID
	holders     []string
	provisional []sendPostgresGraph
	jobs        []uuid.UUID
	joinEOF     bool
}

func (p *legacyLateLockPersistence) EnqueueInTx(ctx context.Context, tx pgx.Tx, org uuid.UUID, opts db.EnqueueOpts) (uuid.UUID, error) {
	id, err := p.LegacySendPersistence.EnqueueInTx(ctx, tx, org, opts)
	if err != nil {
		return id, err
	}
	require.NotEqual(p.t, uuid.Nil, id, "each late conflict must follow its own real provisional job insertion")
	payload, ok := opts.Payload.(map[string]string)
	require.True(p.t, ok, "legacy enqueue retains its concrete string payload")
	p.holders = append(p.holders, payload["changeset_lease_holder_id"])
	p.jobs = append(p.jobs, id)
	graph, err := readSendPostgresGraph(ctx, tx, p.fixture)
	if err != nil {
		return uuid.Nil, err
	}
	p.provisional = append(p.provisional, graph)
	var locked uuid.UUID
	err = tx.QueryRow(ctx, `SELECT id FROM session_review_comments WHERE org_id=$1 AND session_id=$2 AND id=$3 FOR UPDATE NOWAIT`, org, p.fixture.session, p.lockComment).Scan(&locked)
	if err != nil && p.joinEOF {
		err = errors.Join(err, io.EOF)
	}
	return id, err
}

func TestThreadSendLegacyLateContentionRetriesWholeCommandPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		exhaust      bool
		wantAttempts int
		joinEOF      bool
	}{
		{name: "real late 55P03 retries complete graph once", wantAttempts: 2},
		{name: "real sustained late 55P03 exhausts whole command", exhaust: true, wantAttempts: 3},
		{name: "real late 55P03 joined with transport EOF must not retry", joinEOF: true, wantAttempts: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newSendPostgresFixture(t, false, 1)
			service, effects := f.service(t, true)
			comment := seedSendPostgresComment(t, f)
			baseline := sendPostgresReadSnapshot(t, f)
			require.Equal(t, int32(2), f.pool.Config().MaxConns, "one competitor and one command must fit the unchanged two-connection fixture")
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			competitor, err := f.pool.Acquire(ctx)
			require.NoError(t, err, "reserve one independent competitor without increasing capacity")
			defer competitor.Release()
			held, err := competitor.Begin(ctx)
			require.NoError(t, err, "own the independent synthetic comment lock")
			defer func() {
				if held != nil {
					require.NoError(t, held.Rollback(context.Background()), "release only this test's remaining competing transaction")
				}
			}()
			var id uuid.UUID
			require.NoError(t, held.QueryRow(ctx, `SELECT id FROM session_review_comments WHERE org_id=$1 AND session_id=$2 AND id=$3 FOR UPDATE NOWAIT`, f.org, f.session, comment).Scan(&id), "hold an actual unrelated comment row so the conflict occurs only after job insertion")
			persistence := &legacyLateLockPersistence{LegacySendPersistence: service.send.Legacy, t: t, fixture: f, lockComment: comment, joinEOF: tt.joinEOF}
			runner := &legacyContentionTransactions{base: service.send.Transactions, fixture: f}
			runner.beforeAttempt = func(unit, attempt int) error {
				if unit == 2 && attempt == 2 && !tt.exhaust {
					// The real owner could enter this fresh top-level attempt only after
					// rolling back the entire first attempt. No timing or sleep is involved.
					if err := held.Rollback(ctx); err != nil {
						return err
					}
					held = nil
				}
				return nil
			}
			service.send.Transactions, service.send.Legacy = runner, persistence
			result, err := service.SendMessage(ctx, f.input(0, "synthetic-real-late-contention"))
			require.Equal(t, 2, len(runner.units), "late contention may retry the complete command but may not create compensation units")
			require.Equal(t, 1, len(runner.units[0].attempts), "entry observation runs once")
			mutation := runner.units[1]
			require.Equal(t, tt.wantAttempts, len(mutation.attempts), "every actual retry must correspond to one fresh whole-command transaction")
			if tt.joinEOF {
				require.ErrorIs(t, err, ErrEnqueueFailed, "a genuine joined enqueue failure must retain its domain sentinel")
				require.ErrorIs(t, err, io.EOF, "a genuine joined callback failure must preserve its transport cause")
				var pg *pgconn.PgError
				require.ErrorAs(t, err, &pg, "the joined callback failure must retain the actual PostgreSQL lock error")
				require.Equal(t, "55P03", pg.Code, "joined failure must originate at the real late NOWAIT lock check")
				require.NotErrorIs(t, err, ErrCommandOutcomeUnknown, "the actual runner confirmed whole rollback even though joined causes forbid replay")
				require.NotErrorIs(t, err, db.ErrLifecycleContention, "a genuine join may not be converted into retry exhaustion")
				require.Nil(t, result, "joined rollback must not expose provisional acceptance")
				require.NoError(t, held.Rollback(ctx), "release the competitor only after the actual joined failure returned without replay")
				held = nil
				require.JSONEq(t, string(baseline), string(sendPostgresReadSnapshot(t, f)), "the one joined failure attempt must roll back the exact whole graph")
				requireSendPostgresNoEffects(t, effects)
			} else if tt.exhaust {
				var contention *db.LifecycleContentionError
				require.ErrorAs(t, err, &contention, "the concrete runner must report bounded whole-command contention exhaustion")
				require.Equal(t, 3, contention.Attempts, "the production kernel retains its three-attempt budget")
				require.Nil(t, result, "exhausted whole rollback returns no accepted input")
				require.NotErrorIs(t, err, ErrCommandOutcomeUnknown, "actual confirmed rollbacks are not ambiguous settlement")
				require.NoError(t, held.Rollback(ctx), "release the sustained competitor only after actual bounded exhaustion")
				held = nil
				require.JSONEq(t, string(baseline), string(sendPostgresReadSnapshot(t, f)), "all attempted writes must roll back exactly after sustained late contention")
				requireSendPostgresNoEffects(t, effects)
			} else {
				require.NoError(t, err, "a real late conflict must retry the whole command and then commit")
				require.NotNil(t, result, "the successful whole retry returns its own committed input")
				require.Equal(t, sendPostgresGraph{messages: 1, inbox: 1, continueJobs: 1, parentStatus: models.SessionStatusRunning, threadStatus: models.ThreadStatusRunning}, sendPostgresReadGraph(t, f), "whole retry commits exactly one graph without retaining the first provisional input or job")
				require.Equal(t, []uuid.UUID{persistence.jobs[len(persistence.jobs)-1]}, effects.jobs, "only the final committed job may notify once")
				require.Equal(t, []int{1, 0, 0, 0}, []int{effects.claims, effects.pending, effects.threadStatus, effects.parentStatus}, "only the final committed claim publishes; no compensating statuses may publish")
			}
			ids := map[int64]struct{}{}
			diagnostic := runner.units[0].attempts[0].diagnostic
			for i, attempt := range mutation.attempts {
				_, duplicate := ids[attempt.id]
				require.False(t, duplicate, "each whole retry must use a distinct top-level PostgreSQL transaction")
				ids[attempt.id] = struct{}{}
				require.Equal(t, diagnostic, attempt.diagnostic, "every retry retains the original diagnostic correlation")
				require.Equal(t, sendPostgresGuardCounts{shared: 1, parent: 1, lanes: 1}, attempt.guards, "each whole retry must reacquire every actual command guard")
				require.JSONEq(t, string(baseline), string(attempt.before), "the next real attempt must observe complete rollback of all prior provisional rows and projections")
				if tt.exhaust || i == 0 {
					var pg *pgconn.PgError
					require.True(t, errors.As(attempt.err, &pg), "the late callback error must contain a real PostgreSQL lock conflict")
					require.Equal(t, "55P03", pg.Code, "whole retry must be driven by actual NOWAIT contention rather than an injected marker")
				} else {
					require.NoError(t, attempt.err, "final retry callback must complete before actual commit")
				}
			}
			require.Equal(t, tt.wantAttempts, len(persistence.holders), "each whole attempt must reach a real job insertion before the late lock check")
			holder, parseErr := uuid.Parse(persistence.holders[0])
			require.NoError(t, parseErr, "retain a parseable server changeset holder")
			require.NotEqual(t, uuid.Nil, holder, "stable changeset holder must be nonzero")
			for i, holderValue := range persistence.holders {
				require.Equal(t, holder.String(), holderValue, "whole retries must reuse the once-minted changeset lease holder")
				require.Equal(t, sendPostgresGraph{messages: 1, inbox: 1, continueJobs: 1, parentStatus: models.SessionStatusRunning, threadStatus: models.ThreadStatusRunning}, persistence.provisional[i], "each late conflict follows the complete real provisional graph")
			}
		})
	}
}
