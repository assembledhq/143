package thread

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

type sendPostgresLegacyUnit struct {
	id          int64
	guards      sendPostgresGuardCounts
	provisional sendPostgresGraph
	snapshot    json.RawMessage
	settled     error
	diagnostic  uuid.UUID
	attempts    int
}

type sendPostgresLegacyTransactions struct {
	base        SendTransactions
	fixture     sendPostgresFixture
	units       []sendPostgresLegacyUnit
	ambiguousAt int
	commit      bool
	ack         error
	afterKnown  func(int) error
}

func (r *sendPostgresLegacyTransactions) Run(ctx context.Context, work func(context.Context, pgx.Tx) error) error {
	unitNumber := len(r.units) + 1
	var unit sendPostgresLegacyUnit
	attempts := 0
	err := r.base.Run(ctx, func(unitCtx context.Context, tx pgx.Tx) error {
		attempts++
		unit = sendPostgresLegacyUnit{}
		unit.diagnostic = unitCtx.Value(sendDiagnosticContextKey{}).(sendDiagnostic).id
		if err := tx.QueryRow(unitCtx, `SELECT txid_current()`).Scan(&unit.id); err != nil {
			return err
		}
		workErr := work(unitCtx, sendPostgresGuardObservedTx{Tx: tx, counts: &unit.guards})
		var readErr error
		unit.provisional, readErr = readSendPostgresGraph(unitCtx, tx, r.fixture)
		if readErr == nil {
			unit.snapshot, readErr = readSendPostgresSnapshot(unitCtx, tx, r.fixture)
		}
		if workErr != nil {
			return workErr // Do not wrap the exact service rollback marker.
		}
		if readErr != nil {
			return readErr
		}
		if unitNumber == r.ambiguousAt && !r.commit {
			// A real whole rollback follows, but the error was introduced after
			// the service callback returned success. It is intentionally NOT
			// that callback's exact rollback marker or caller-known abort proof.
			return r.ack
		}
		return nil
	})
	unit.settled = err
	unit.attempts = attempts
	r.units = append(r.units, unit)
	if err == nil && unitNumber == r.ambiguousAt && r.commit {
		return r.ack // Actual Commit succeeded; simulate only its lost ACK.
	}
	if err == nil && r.afterKnown != nil {
		return r.afterKnown(unitNumber) // Outside the completed transaction.
	}
	return err
}

type sendPostgresLegacyCall struct {
	name   string
	guards sendPostgresGuardCounts
}

type sendPostgresLegacyFailurePersistence struct {
	LegacySendPersistence
	point string
	cause error
	calls []sendPostgresLegacyCall
}

func (p *sendPostgresLegacyFailurePersistence) record(name string, tx pgx.Tx) {
	observed, ok := tx.(sendPostgresGuardObservedTx)
	counts := sendPostgresGuardCounts{}
	if ok {
		counts = *observed.counts
	}
	p.calls = append(p.calls, sendPostgresLegacyCall{name: name, guards: counts})
}

func (p *sendPostgresLegacyFailurePersistence) ClaimIdleSessionInTx(ctx context.Context, tx pgx.Tx, orgID, sessionID uuid.UUID) (models.Session, error) {
	p.record("parent_idle", tx)
	row, err := p.LegacySendPersistence.ClaimIdleSessionInTx(ctx, tx, orgID, sessionID)
	if err == nil && p.point == "parent" {
		return models.Session{}, p.cause // Roll back a REAL provisional claim.
	}
	return row, err
}

func (p *sendPostgresLegacyFailurePersistence) ClaimResumeSessionInTx(ctx context.Context, tx pgx.Tx, orgID, sessionID uuid.UUID) (models.Session, error) {
	p.record("parent_resume", tx)
	return p.LegacySendPersistence.ClaimResumeSessionInTx(ctx, tx, orgID, sessionID)
}

func (p *sendPostgresLegacyFailurePersistence) EnqueueInTx(ctx context.Context, tx pgx.Tx, orgID uuid.UUID, opts db.EnqueueOpts) (uuid.UUID, error) {
	p.record("enqueue", tx)
	id, err := p.LegacySendPersistence.EnqueueInTx(ctx, tx, orgID, opts)
	if err == nil && p.point == "enqueue" {
		return id, p.cause
	}
	return id, err
}

func requireSendPostgresLegacyUnitGuards(t *testing.T, units []sendPostgresLegacyUnit) {
	t.Helper()
	require.Equal(t, 2, len(units), "legacy send owns entry plus one whole mutation and no compensation units")
	require.NotEqual(t, units[0].id, units[1].id, "entry and whole mutation own distinct real PostgreSQL transactions")
	require.Equal(t, []int{1, 1}, []int{units[0].attempts, units[1].attempts}, "uncontended entry and whole command must each invoke one actual top-level callback")
	require.Equal(t, sendPostgresGuardCounts{shared: 1}, units[0].guards, "entry owns its real shared cutover interlock")
	require.Equal(t, sendPostgresGuardCounts{shared: 1, parent: 1, lanes: 1}, units[1].guards, "whole mutation obtains exactly one fresh shared, parent and sorted-lane guard")
}

func TestThreadSendLegacyKnownFailureRollsBackWholeCommandPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, point string
		running     bool
		wantCalls   []string
	}{
		{name: "after real parent claim", point: "parent", wantCalls: []string{"parent_idle"}},
		{name: "after real continuation job insertion", point: "enqueue", wantCalls: []string{"parent_idle", "enqueue"}},
		{name: "after real live-delivery job insertion and pending increment", point: "enqueue", running: true, wantCalls: []string{"enqueue"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newSendPostgresFixture(t, false, 1)
			other := newSendPostgresComparisonScope(t, f, true)
			seedSendPostgresComment(t, other)
			seedSendPostgresAnswers(t, other)
			service, effects := f.service(t, true)
			input := f.input(0, "synthetic-known-whole-rollback")
			if tt.running {
				sendPostgresExec(t, f, `UPDATE sessions SET status='running' WHERE org_id=$1 AND id=$2`, f.org, f.session)
				sendPostgresExec(t, f, `UPDATE session_threads SET status='running' WHERE org_id=$1 AND session_id=$2`, f.org, f.session)
			} else {
				input.ResolveReviewCommentIDs = []uuid.UUID{seedSendPostgresComment(t, f)}
			}
			baseline, otherBaseline := sendPostgresReadSnapshot(t, f), sendPostgresReadSnapshot(t, other)
			cause := errors.New("synthetic callback failure after real legacy mutation")
			persistence := &sendPostgresLegacyFailurePersistence{LegacySendPersistence: service.send.Legacy, point: tt.point, cause: cause}
			runner := &sendPostgresLegacyTransactions{base: service.send.Transactions, fixture: f}
			service.send.Legacy, service.send.Transactions = persistence, runner
			result, err := service.SendMessage(context.Background(), input)
			require.ErrorIs(t, err, cause, "confirmed whole rollback must preserve the exact callback cause")
			require.NotErrorIs(t, err, ErrCommandOutcomeUnknown, "known callback rollback must not become ambiguous")
			require.Nil(t, result, "failed legacy command must not expose provisional acceptance")
			if tt.point == "enqueue" {
				require.ErrorIs(t, err, ErrEnqueueFailed, "confirmed enqueue rollback preserves the established error boundary")
			}
			requireSendPostgresLegacyUnitGuards(t, runner.units)
			require.ErrorIs(t, runner.units[1].settled, cause, "the actual runner must confirm rollback of the one complete command")
			calls := make([]string, len(persistence.calls))
			for i, call := range persistence.calls {
				calls[i] = call.name
				require.Equal(t, sendPostgresGuardCounts{shared: 1, parent: 1, lanes: 1}, call.guards, "all actual mutations must share one consumed command guard")
			}
			require.Equal(t, tt.wantCalls, calls, "known failure must not trigger fallback, compensating writes or another send")
			require.JSONEq(t, string(baseline), string(sendPostgresReadSnapshot(t, f)), "rollback must restore every target row, claim, status, comment and answer field")
			require.JSONEq(t, string(otherBaseline), string(sendPostgresReadSnapshot(t, other)), "same-database foreign tenant rows must remain exactly unchanged")
			requireSendPostgresNoEffects(t, effects)
		})
	}
}

func TestThreadSendLegacyAmbiguousWholeCommandPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		commit bool
	}{
		{name: "actual whole rollback with ambiguous acknowledgement"},
		{name: "actual whole commit with lost acknowledgement", commit: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newSendPostgresFixture(t, false, 1)
			other := newSendPostgresComparisonScope(t, f, true)
			seedSendPostgresComment(t, other)
			seedSendPostgresAnswers(t, other)
			service, effects := f.service(t, true)
			input := f.input(0, "synthetic-valid-shape-legacy-whole-unknown")
			input.ResolveReviewCommentIDs = []uuid.UUID{seedSendPostgresComment(t, f)}
			seedSendPostgresAnswers(t, f)
			sendPostgresExec(t, f, `UPDATE sessions SET status='awaiting_input' WHERE org_id=$1 AND id=$2`, f.org, f.session)
			sendPostgresExec(t, f, `UPDATE session_threads SET status='awaiting_input' WHERE org_id=$1 AND session_id=$2`, f.org, f.session)
			service.SetQuestionStore(db.NewSessionQuestionStore(f.pool))
			service.SetHumanInputRequestStore(db.NewSessionHumanInputRequestStore(f.pool))
			baseline, otherBaseline := sendPostgresReadSnapshot(t, f), sendPostgresReadSnapshot(t, other)
			runner := &sendPostgresLegacyTransactions{base: service.send.Transactions, fixture: f, ambiguousAt: 2, commit: tt.commit, ack: errors.New("synthetic ambiguous whole legacy completion")}
			service.send.Transactions = runner
			result, err := service.SendMessage(context.Background(), input)
			var unknown *CommandOutcomeUnknownError
			require.ErrorAs(t, err, &unknown, "error after complete callback success must retain uncertainty for either actual settlement")
			require.ErrorIs(t, err, runner.ack, "unknown outcome retains the exact lost acknowledgement cause")
			require.Nil(t, result, "uncertain command must not expose acceptance")
			require.Equal(t, db.LifecycleIngress{}, unknown.Ingress, "legacy correlation cannot fabricate active receipt authority")
			require.NotEqual(t, uuid.Nil, unknown.DiagnosticID, "unknown legacy outcome retains stable correlation")
			require.Equal(t, input.ClientMessageID, unknown.ClientMessageID, "unknown outcome retains the original client key")
			requireSendPostgresLegacyUnitGuards(t, runner.units)
			for _, unit := range runner.units {
				require.Equal(t, unknown.DiagnosticID, unit.diagnostic, "entry and command retain one diagnostic identity")
			}
			want := baseline
			if tt.commit {
				require.NoError(t, runner.units[1].settled, "test must actually commit the entire command before losing only its acknowledgement")
				want = runner.units[1].snapshot
			} else {
				require.ErrorIs(t, runner.units[1].settled, runner.ack, "test must actually roll back the whole provisional graph after callback success")
			}
			require.Equal(t, sendPostgresGraph{messages: 1, inbox: 1, continueJobs: 1, parentStatus: models.SessionStatusRunning, threadStatus: models.ThreadStatusRunning}, runner.units[1].provisional, "unknown settlement follows the complete real provisional graph")
			require.JSONEq(t, string(want), string(sendPostgresReadSnapshot(t, f)), "independent read shows either whole committed graph or exact baseline, including comments and both answers")
			require.JSONEq(t, string(otherBaseline), string(sendPostgresReadSnapshot(t, other)), "uncertain target settlement preserves every foreign sentinel")
			requireSendPostgresNoEffects(t, effects)
		})
	}
}

// This installs only synthetic parser-valid test provenance after a known
// commit while holding the actual exclusive cutover interlock. It is not a
// production activation operation, floor/drain proof or provider conformance.
func seedSendPostgresBetweenUnitsMode(ctx context.Context, f sendPostgresFixture, active bool) error {
	return db.RunLifecycleTransaction(ctx, f.pool, func(unitCtx context.Context, tx pgx.Tx) error {
		var locked bool
		if err := tx.QueryRow(unitCtx, `SELECT pg_try_advisory_xact_lock(hashtextextended($1::text,0))`, "session_lifecycle_cutover:"+f.org.String()+":"+f.session.String()).Scan(&locked); err != nil {
			return err
		}
		if !locked {
			return errors.New("synthetic between-unit cutover interlock was unexpectedly busy")
		}
		normalization, err := json.Marshal(map[string]any{"schema_version": 1, "receipt_id": uuid.New(), "floor_receipt_id": uuid.New(), "cutoff_id": 1, "complete": active})
		if err != nil {
			return err
		}
		if !active {
			_, err = tx.Exec(unitCtx, `INSERT INTO session_lifecycle_cutovers(org_id,session_id,version,state,legacy_input_cutoff_id,normalization) VALUES($1,$2,3,'normalizing',1,$3)`, f.org, f.session, normalization)
			return err
		}
		capability, err := json.Marshal(map[string]any{"schema_version": 1, "release_receipt_id": uuid.New(), "tuples": []map[string]any{{"provider": string(models.DefaultDefaultAgentType), "cli_build": "synthetic-cli-v1", "image_build": "synthetic-image-v1", "resume_identity": "native_conversation", "verification_protocol": "synthetic-verify-v1", "conformance_receipt_id": uuid.New()}}})
		if err != nil {
			return err
		}
		if _, err := tx.Exec(unitCtx, `UPDATE sessions SET lifecycle_version=1,admission_epoch=3 WHERE org_id=$1 AND id=$2`, f.org, f.session); err != nil {
			return err
		}
		if _, err := tx.Exec(unitCtx, `UPDATE session_threads SET lifecycle_version=1,admission_epoch=5 WHERE org_id=$1 AND session_id=$2`, f.org, f.session); err != nil {
			return err
		}
		_, err = tx.Exec(unitCtx, `INSERT INTO session_lifecycle_cutovers(org_id,session_id,version,state,legacy_input_cutoff_id,normalization,activated_at,minimum_writer_version,resumability_capability) VALUES($1,$2,3,'active',1,$3,$4,1,$5)`, f.org, f.session, normalization, time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC), capability)
		return err
	})
}

func TestThreadSendLegacyModeChangeBetweenEntryAndMutationPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		active bool
	}{{name: "normalizing after entry"}, {name: "active after entry", active: true}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newSendPostgresFixture(t, false, 1)
			service, effects := f.service(t, true)
			runner := &sendPostgresLegacyTransactions{base: service.send.Transactions, fixture: f}
			runner.afterKnown = func(unit int) error {
				if unit == 1 {
					return seedSendPostgresBetweenUnitsMode(context.Background(), f, tt.active)
				}
				return nil
			}
			service.send.Transactions = runner
			result, err := service.SendMessage(context.Background(), f.input(0, "synthetic-entry-to-mutation-mode"))
			require.ErrorIs(t, err, db.ErrThreadSendModeChanged, "whole mutation must freshly reject mode changed after entry without switching branches")
			require.NotErrorIs(t, err, ErrCommandOutcomeUnknown, "fresh guard refusal is a known whole-command abort")
			require.Nil(t, result, "changed mode cannot return legacy or active acceptance")
			require.Equal(t, 2, len(runner.units), "mode refusal cannot start another mutation or compensation")
			require.Equal(t, sendPostgresGuardCounts{shared: 1}, runner.units[1].guards, "changed mode is observed before parent/lane locks or writes")
			require.Equal(t, sendPostgresGraph{parentStatus: models.SessionStatusIdle, threadStatus: models.ThreadStatusIdle}, sendPostgresReadGraph(t, f), "entry observation may not commit claims or input before a changed-mode refusal")
			requireSendPostgresNoEffects(t, effects)
			var state string
			require.NoError(t, f.pool.QueryRow(context.Background(), `SELECT state FROM session_lifecycle_cutovers WHERE org_id=$1 AND session_id=$2`, f.org, f.session).Scan(&state), "read exact opposing synthetic mode evidence")
			want := "normalizing"
			if tt.active {
				want = "active"
			}
			require.Equal(t, want, state, "command refusal cannot erase opposing mode evidence")
		})
	}
}

func TestThreadSendLegacyResumeCapacityAndConcreteOptionsPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		status   models.SessionStatus
		capacity bool
	}{
		{name: "completed lane and parent resume atomically", status: models.SessionStatusCompleted},
		{name: "failed lane and parent resume atomically", status: models.SessionStatusFailed},
		{name: "fourth idle lane remains queued at legacy running cap", capacity: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			laneCount := 1
			if tt.capacity {
				laneCount = 4
			}
			f := newSendPostgresFixture(t, false, laneCount)
			service, effects := f.service(t, true)
			if tt.capacity {
				sendPostgresExec(t, f, `UPDATE sessions SET status='running' WHERE org_id=$1 AND id=$2`, f.org, f.session)
				sendPostgresExec(t, f, `UPDATE session_threads SET status='running' WHERE org_id=$1 AND session_id=$2 AND id=ANY($3::uuid[])`, f.org, f.session, f.lanes[1:])
			} else {
				sendPostgresExec(t, f, `UPDATE sessions SET status=$3,container_id='synthetic-container',worker_node_id='synthetic-worker' WHERE org_id=$1 AND id=$2`, f.org, f.session, tt.status)
				sendPostgresExec(t, f, `UPDATE session_threads SET status=$4 WHERE org_id=$1 AND session_id=$2 AND id=$3`, f.org, f.session, f.lanes[0], models.ThreadStatus(tt.status))
			}
			runner := &sendPostgresLegacyTransactions{base: service.send.Transactions, fixture: f}
			service.send.Transactions = runner
			input := f.input(0, "synthetic-legacy-resume-options")
			override := "synthetic-explicit-continuation-key"
			input.ContinuationDedupeKeyOverride = &override
			result, err := service.SendMessage(context.Background(), input)
			require.NoError(t, err, "legacy resume or capacity waiting must remain accepted through the actual supplied-tx service path")
			require.NotNil(t, result, "successful legacy send returns its original message outcome")
			require.Nil(t, result.Admission, "successful legacy path cannot fabricate an active receipt")
			require.Equal(t, 1, result.Message.TurnNumber, "idle or resumed current-zero input preserves current+1 attribution")
			requireSendPostgresLegacyUnitGuards(t, runner.units)
			if tt.capacity {
				require.Equal(t, sendPostgresGraph{messages: 1, inbox: 1, pending: 1, parentStatus: models.SessionStatusRunning, threadStatus: models.ThreadStatusIdle}, sendPostgresReadGraph(t, f), "legacy capacity wait preserves already-running siblings and creates no new job or claim")
				require.Equal(t, 2, len(runner.units), "capacity wait commits in one guarded command after entry")
				require.Empty(t, effects.jobs, "capacity wait cannot issue a new-job wake")
			} else {
				require.Equal(t, sendPostgresGraph{messages: 1, inbox: 1, continueJobs: 1, parentStatus: models.SessionStatusRunning, threadStatus: models.ThreadStatusRunning}, sendPostgresReadGraph(t, f), "resume commits both claims and one unchanged legacy continuation together without canonical claim metadata")
				require.Equal(t, 2, len(runner.units), "both idle misses, resume claims, input and enqueue share one actual command")
				var job uuid.UUID
				var key, target string
				var priority, attempts int
				var payload json.RawMessage
				require.NoError(t, f.pool.QueryRow(context.Background(), `SELECT id,dedupe_key,target_node_id,priority,max_attempts,payload FROM jobs WHERE org_id=$1 AND job_type='continue_session'`, f.org).Scan(&job, &key, &target, &priority, &attempts, &payload), "read the one actual resumed job with its original concrete worker affinity and options")
				require.Equal(t, []string{override, "synthetic-worker"}, []string{key, target}, "explicit continuation key and preexisting container owner must remain exact legacy enqueue options")
				require.Equal(t, []int{5, 3}, []int{priority, attempts}, "resume preserves characterized queue priority and concrete retry default")
				var stored map[string]string
				require.NoError(t, json.Unmarshal(payload, &stored), "decode the original legacy string payload without adding lifecycle authority")
				holder, parseErr := uuid.Parse(stored["changeset_lease_holder_id"])
				require.NoError(t, parseErr, "legacy resume must retain its once-minted concrete changeset holder")
				require.NotEqual(t, uuid.Nil, holder, "legacy changeset holder cannot be a zero placeholder")
				require.Equal(t, map[string]string{"org_id": f.org.String(), "session_id": f.session.String(), "thread_id": f.lanes[0].String(), "changeset_id": f.changeset.String(), "changeset_lease_holder_id": holder.String(), "post_success_action": input.PostSuccessAction, "post_success_author_mode": input.PostSuccessAuthorMode}, stored, "resume must retain exactly the complete original route, changeset holder and post-success payload without lifecycle authority fields")
				require.Equal(t, []uuid.UUID{job}, effects.jobs, "only the known-committed concrete legacy continuation may be woken")
			}
		})
	}
}
