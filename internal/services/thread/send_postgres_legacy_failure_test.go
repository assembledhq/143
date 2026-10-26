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
	settled     error
	diagnostic  uuid.UUID
}

type sendPostgresLegacyTransactions struct {
	base        SendTransactions
	fixture     sendPostgresFixture
	units       []sendPostgresLegacyUnit
	ambiguousAt int
	commit      bool
	ack         error
	afterKnown  func(int) error
	afterAbort  func(int) error
}

func (r *sendPostgresLegacyTransactions) Run(ctx context.Context, work func(context.Context, pgx.Tx) error) error {
	unitNumber := len(r.units) + 1
	var unit sendPostgresLegacyUnit
	var callbackErr error
	err := r.base.Run(ctx, func(unitCtx context.Context, tx pgx.Tx) error {
		unit = sendPostgresLegacyUnit{}
		callbackErr = nil
		unit.diagnostic = unitCtx.Value(sendDiagnosticContextKey{}).(sendDiagnostic).id
		if err := tx.QueryRow(unitCtx, `SELECT txid_current()`).Scan(&unit.id); err != nil {
			return err
		}
		workErr := work(unitCtx, sendPostgresGuardObservedTx{Tx: tx, counts: &unit.guards})
		var readErr error
		unit.provisional, readErr = readSendPostgresGraph(unitCtx, tx, r.fixture)
		if workErr != nil {
			callbackErr = workErr
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
	r.units = append(r.units, unit)
	if err != nil && err == callbackErr && r.afterAbort != nil {
		// This exact identity is the concrete runner's confirmed whole-abort
		// proof. A join, commit error or ambiguous ACK cannot reach this hook.
		if hookErr := r.afterAbort(unitNumber); hookErr != nil {
			return hookErr
		}
	}
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
	if p.point == "enqueue" {
		return uuid.Nil, p.cause // Fail BEFORE enqueue; its fresh guard ran.
	}
	return p.LegacySendPersistence.EnqueueInTx(ctx, tx, orgID, opts)
}

func (p *sendPostgresLegacyFailurePersistence) UpdateSessionStatusInTx(ctx context.Context, tx pgx.Tx, orgID, sessionID uuid.UUID, status models.SessionStatus) (models.Session, error) {
	p.record("compensate_parent", tx)
	return p.LegacySendPersistence.UpdateSessionStatusInTx(ctx, tx, orgID, sessionID, status)
}

func (p *sendPostgresLegacyFailurePersistence) UpdateThreadStatusInTx(ctx context.Context, tx pgx.Tx, orgID, threadID uuid.UUID, status models.ThreadStatus) (db.ThreadPersistenceEffects, error) {
	p.record("compensate_lane", tx)
	return p.LegacySendPersistence.UpdateThreadStatusInTx(ctx, tx, orgID, threadID, status)
}

func requireSendPostgresLegacyUnitGuards(t *testing.T, units []sendPostgresLegacyUnit) {
	t.Helper()
	ids := make(map[int64]struct{}, len(units))
	for i, unit := range units {
		ids[unit.id] = struct{}{}
		want := sendPostgresGuardCounts{shared: 1, parent: 1, lanes: 1}
		if i == 0 {
			want = sendPostgresGuardCounts{shared: 1}
		}
		require.Equal(t, want, unit.guards, "each original mutation or compensation must execute fresh mode, parent and sorted-lane SQL on its actual supplied connection")
	}
	require.Equal(t, len(units), len(ids), "separate legacy units and compensations must be distinct real transactions, not an encompassing command transaction")
}

func TestThreadSendLegacyKnownFailureCompensationsPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		point     string
		wantUnits int
		wantCalls []string
	}{
		{name: "parent callback rollback releases only the earlier committed lane", point: "parent", wantUnits: 5, wantCalls: []string{"parent_idle", "parent_resume", "compensate_lane"}},
		{name: "enqueue callback rollback retains committed input comments and compensates claims", point: "enqueue", wantUnits: 7, wantCalls: []string{"parent_idle", "enqueue", "compensate_parent", "compensate_lane"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newSendPostgresFixture(t, false, 1)
			service, effects := f.service(t, true)
			cause := errors.New("synthetic legacy persistence callback failure")
			persistence := &sendPostgresLegacyFailurePersistence{LegacySendPersistence: service.send.Legacy, point: tt.point, cause: cause}
			runner := &sendPostgresLegacyTransactions{base: service.send.Transactions, fixture: f}
			service.send.Legacy, service.send.Transactions = persistence, runner
			input := f.input(0, "synthetic-known-legacy-failure")
			var comment uuid.UUID
			if tt.point == "enqueue" {
				comment = seedSendPostgresComment(t, f)
				input.ResolveReviewCommentIDs = []uuid.UUID{comment}
			}
			result, err := service.SendMessage(context.Background(), input)
			require.Error(t, err, "known callback failure must preserve the historical failed-send outcome")
			require.NotErrorIs(t, err, ErrCommandOutcomeUnknown, "exact callback error returned after real rollback is not an ambiguous outcome")
			require.Nil(t, result, "failed legacy send cannot expose successful command acceptance")
			require.Equal(t, tt.wantUnits, len(runner.units), "legacy fallback and compensation retain each original separate transaction boundary")
			want := sendPostgresGraph{parentStatus: models.SessionStatusIdle, threadStatus: models.ThreadStatusIdle}
			if tt.point == "enqueue" {
				require.ErrorIs(t, err, ErrEnqueueFailed, "confirmed enqueue rollback preserves the established error boundary")
				require.ErrorIs(t, err, cause, "known enqueue failure retains its callback cause")
				want.messages, want.inbox = 1, 1
				require.True(t, sendPostgresCommentResolved(t, f, comment), "earlier committed comment/message unit must NOT be falsely described as a whole-command abort")
				require.Equal(t, []int{1, 1, 1}, []int{effects.claims, effects.parentStatus, effects.threadStatus}, "known enqueue rollback permits only the established postcommit parent and lane compensation effects")
			} else {
				require.ErrorIs(t, err, ErrSessionNotResumable, "legacy parent diagnostic/resume fallback remains unchanged after confirmed idle-claim failure")
				require.Equal(t, sendPostgresGraph{parentStatus: models.SessionStatusRunning, threadStatus: models.ThreadStatusRunning}, runner.units[2].provisional, "parent fault must follow its real provisional update, not a no-write stub")
				require.Equal(t, []int{1, 0, 1}, []int{effects.claims, effects.parentStatus, effects.threadStatus}, "rolled-back parent claim remains eventless and only the earlier lane can be released")
			}
			require.Equal(t, want, sendPostgresReadGraph(t, f), "known rollback may compensate projections but cannot erase earlier committed input or invent a consumer")
			failedUnit := 2
			if tt.point == "enqueue" {
				failedUnit = 4
			}
			require.ErrorIs(t, runner.units[failedUnit].settled, cause, "the actual runner must confirm whole rollback of the injected callback cause before compensation")
			for _, unit := range runner.units[failedUnit+1:] {
				require.NoError(t, unit.settled, "every permitted original fallback or compensation must independently complete before publication")
			}
			requireSendPostgresLegacyUnitGuards(t, runner.units)
			calls := make([]string, len(persistence.calls))
			for i, call := range persistence.calls {
				calls[i] = call.name
				require.Equal(t, sendPostgresGuardCounts{shared: 1, parent: 1, lanes: 1}, call.guards, "the actual persistence seam must be reached only AFTER a successfully consumed fresh guard")
			}
			require.Equal(t, tt.wantCalls, calls, "known failure permits only original fallback and fresh-guard compensation operations")
			require.Empty(t, effects.jobs, "no provisional or failed enqueue may notify a job")
			require.Empty(t, effects.lifecycle, "legacy compensation cannot fabricate canonical active effects")
		})
	}
}

func TestThreadSendLegacyAmbiguousUnitStopsCompensationPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		parent bool
		commit bool
	}{
		{name: "parent real rollback with ambiguous ACK", parent: true},
		{name: "parent real commit with lost ACK", parent: true, commit: true},
		{name: "enqueue real rollback with ambiguous ACK"},
		{name: "enqueue real commit with lost ACK", commit: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newSendPostgresFixture(t, false, 1)
			service, effects := f.service(t, true)
			faultUnit := 5
			if tt.parent {
				faultUnit = 3
			}
			runner := &sendPostgresLegacyTransactions{base: service.send.Transactions, fixture: f, ambiguousAt: faultUnit, commit: tt.commit, ack: errors.New("synthetic ambiguous legacy completion")}
			persistence := &sendPostgresLegacyFailurePersistence{LegacySendPersistence: service.send.Legacy}
			service.send.Transactions, service.send.Legacy = runner, persistence
			input := f.input(0, "synthetic-valid-shape-legacy-unknown")
			var comment uuid.UUID
			if !tt.parent {
				comment = seedSendPostgresComment(t, f)
				input.ResolveReviewCommentIDs = []uuid.UUID{comment}
			}
			result, err := service.SendMessage(context.Background(), input)
			var unknown *CommandOutcomeUnknownError
			require.ErrorAs(t, err, &unknown, "any error after callback success must retain uncertainty even when the test independently knows the real settlement")
			require.Nil(t, result, "ambiguous legacy unit cannot expose successful acceptance or a fabricated v1 receipt")
			require.Equal(t, db.LifecycleIngress{}, unknown.Ingress, "legacy has no exact v1 receipt even when the original client shape would validate for v1")
			require.NotEqual(t, uuid.Nil, unknown.DiagnosticID, "unknown legacy outcome retains a stable correlation-only command identifier")
			require.Equal(t, input.ClientMessageID, unknown.ClientMessageID, "original client key remains correlation, not legacy receipt authority")
			require.Equal(t, faultUnit, len(runner.units), "uncertainty must stop every later mutation, fallback, compensation and replay probe")
			if tt.commit {
				require.NoError(t, runner.units[faultUnit-1].settled, "test must actually Commit the uncertain unit before simulating only the lost acknowledgement")
			} else {
				require.ErrorIs(t, runner.units[faultUnit-1].settled, runner.ack, "test must actually roll back the provisional unit without exposing service-known callback abort proof")
			}
			requireSendPostgresLegacyUnitGuards(t, runner.units)
			for _, unit := range runner.units {
				require.Equal(t, unknown.DiagnosticID, unit.diagnostic, "all original units retain the same diagnostic identity without reminting authority")
			}
			want := sendPostgresGraph{parentStatus: models.SessionStatusRunning, threadStatus: models.ThreadStatusRunning}
			if tt.parent {
				if !tt.commit {
					want.parentStatus = models.SessionStatusIdle
				}
			} else {
				want.messages, want.inbox = 1, 1
				if tt.commit {
					want.continueJobs = 1
				}
				require.True(t, sendPostgresCommentResolved(t, f, comment), "uncertain enqueue cannot undo a previously committed message/comment unit")
			}
			require.Equal(t, want, sendPostgresReadGraph(t, f), "earlier committed claims/input survive and no ambiguous outcome authorizes a status reset")
			calls := make([]string, len(persistence.calls))
			for i, call := range persistence.calls {
				calls[i] = call.name
			}
			wantCalls := []string{"parent_idle"}
			if !tt.parent {
				wantCalls = append(wantCalls, "enqueue")
			}
			require.Equal(t, wantCalls, calls, "neither a real committed nor rolled-back ambiguous unit may trigger any compensation")
			require.Equal(t, []int{1, 0, 0}, []int{effects.claims, effects.parentStatus, effects.threadStatus}, "only the earlier confirmed lane claim may publish, never uncertain work or compensations")
			require.Empty(t, effects.jobs, "even an independently known actual commit cannot notify when its ACK was lost to the service")
			require.Empty(t, effects.lifecycle, "uncertain legacy work cannot fabricate canonical lifecycle effects")
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

func TestThreadSendLegacyModeChangeBetweenOriginalUnitsPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		active       bool
		compensation bool
	}{
		{name: "normalizing after the separately committed lane claim"},
		{name: "active after the separately committed lane claim", active: true},
		{name: "normalizing after known enqueue abort rejects fresh compensation", compensation: true},
		{name: "active after known enqueue abort rejects fresh compensation", active: true, compensation: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newSendPostgresFixture(t, false, 1)
			service, effects := f.service(t, true)
			persistence := &sendPostgresLegacyFailurePersistence{LegacySendPersistence: service.send.Legacy}
			runner := &sendPostgresLegacyTransactions{base: service.send.Transactions, fixture: f}
			if tt.compensation {
				persistence.point, persistence.cause = "enqueue", errors.New("synthetic known enqueue abort before opposing mode")
				runner.afterAbort = func(unit int) error {
					if unit == 5 {
						return seedSendPostgresBetweenUnitsMode(context.Background(), f, tt.active)
					}
					return nil
				}
			} else {
				runner.afterKnown = func(unit int) error {
					if unit == 2 {
						return seedSendPostgresBetweenUnitsMode(context.Background(), f, tt.active)
					}
					return nil
				}
			}
			service.send.Transactions, service.send.Legacy = runner, persistence
			input := f.input(0, "synthetic-between-units-mode")
			var comment uuid.UUID
			if tt.compensation {
				comment = seedSendPostgresComment(t, f)
				input.ResolveReviewCommentIDs = []uuid.UUID{comment}
			}
			result, err := service.SendMessage(context.Background(), input)
			require.ErrorIs(t, err, db.ErrThreadSendModeChanged, "the next original mutation must freshly reject changed mode instead of reusing entry observation or routing active fallback")
			require.NotErrorIs(t, err, ErrCommandOutcomeUnknown, "fresh guard rejection after confirmed rollback is not ambiguous settlement")
			require.Nil(t, result, "a changed mode cannot fabricate command acceptance")
			wantGraph := sendPostgresGraph{parentStatus: models.SessionStatusIdle, threadStatus: models.ThreadStatusRunning}
			wantUnits := 3
			if tt.compensation {
				wantUnits = 6
			}
			require.Equal(t, wantUnits, len(runner.units), "only original known units and the first freshly rejected next mutation may execute")
			if tt.compensation {
				wantGraph.parentStatus, wantGraph.messages, wantGraph.inbox = models.SessionStatusRunning, 1, 1
				require.Equal(t, 2, len(persistence.calls), "mode refusal cannot invoke either parent or lane compensation after the known enqueue abort")
				require.Equal(t, []string{"parent_idle", "enqueue"}, []string{persistence.calls[0].name, persistence.calls[1].name}, "opposing mode stops the first compensating persistence call, not the earlier known original work")
				require.True(t, sendPostgresCommentResolved(t, f, comment), "known earlier comment/input commit cannot be erased when fresh compensation loses its mode authority")
				require.ErrorIs(t, runner.units[4].settled, persistence.cause, "mode change must follow a real confirmed enqueue callback rollback, not fabricated uncertainty")
			} else {
				require.Empty(t, persistence.calls, "next-unit mode rejection stops parent persistence, fallback, enqueue and every compensation")
			}
			requireSendPostgresLegacyUnitGuards(t, runner.units[:len(runner.units)-1])
			require.Equal(t, sendPostgresGuardCounts{shared: 1}, runner.units[len(runner.units)-1].guards, "changed mode must be observed before next parent/lane locks or persistence, including compensation")
			require.Equal(t, wantGraph, sendPostgresReadGraph(t, f), "earlier real legacy commits remain durable; no command-wide rollback or unauthorized cleanup may erase them")
			require.Equal(t, []int{1, 0, 0}, []int{effects.claims, effects.parentStatus, effects.threadStatus}, "only the earlier known lane claim retains its original snapshot publication")
			require.Empty(t, effects.jobs, "changed mode cannot enqueue or notify via either branch")
			require.Empty(t, effects.lifecycle, "legacy entry never falls through to active effects after a mode change")
			var state string
			var normalization json.RawMessage
			require.NoError(t, f.pool.QueryRow(context.Background(), `SELECT state,normalization FROM session_lifecycle_cutovers WHERE org_id=$1 AND session_id=$2`, f.org, f.session).Scan(&state, &normalization), "the exact synthetic provenance remains retained after guard refusal")
			wantState := "normalizing"
			if tt.active {
				wantState = "active"
			}
			require.Equal(t, wantState, state, "service refusal cannot erase or downgrade the opposing mode evidence")
			require.NotEmpty(t, normalization, "opposing normalization provenance cannot disappear through compensation")
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
		{name: "completed lane and parent resume in separate original claim units", status: models.SessionStatusCompleted},
		{name: "failed lane and parent resume in separate original claim units", status: models.SessionStatusFailed},
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
				require.Equal(t, 3, len(runner.units), "capacity path preserves separate entry, missed idle claim and grouped queued-input units")
				require.Empty(t, effects.jobs, "capacity wait cannot issue a new-job wake")
			} else {
				require.Equal(t, sendPostgresGraph{messages: 1, inbox: 1, continueJobs: 1, parentStatus: models.SessionStatusRunning, threadStatus: models.ThreadStatusRunning}, sendPostgresReadGraph(t, f), "resume keeps separate claims but creates one unchanged legacy continuation without canonical claim metadata")
				require.Equal(t, 7, len(runner.units), "both idle misses, both resume claims, grouped input and enqueue retain independent real boundaries")
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
