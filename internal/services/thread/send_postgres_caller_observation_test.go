package thread

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"
)

func callerObservationOwnerRef(owner CallerSendOwnerScope) db.CallerSendOwnerRef {
	return db.CallerSendOwnerRef{Purpose: db.CallerSendStampPurpose(owner.Purpose), OwnerID: owner.OwnerID, SessionID: owner.SessionID}
}

// These are real guard-validated fixture transitions, not an implementation or
// proof of a caller controller's later attempt/observation CAS protocol.
func callerObservationUnknownAttempt(t *testing.T, f sendPostgresFixture, owner CallerSendOwnerScope) uuid.UUID {
	t.Helper()
	attempt := uuid.New()
	start := `UPDATE code_review_agent_results SET dispatch_state='attempting',dispatch_attempt_id=$4,dispatch_attempt_seq=1,
 dispatch_attempted_at=statement_timestamp(),dispatch_writer_seq=dispatch_writer_seq+1,dispatch_updated_at=statement_timestamp()
 WHERE org_id=$1 AND session_id=$2 AND id=$3`
	unknown := `UPDATE code_review_agent_results SET dispatch_state='unknown',dispatch_detail='unknown_ack',
 dispatch_observe_deadline=statement_timestamp()+interval '15 minutes',dispatch_writer_seq=dispatch_writer_seq+1,dispatch_updated_at=statement_timestamp()
 WHERE org_id=$1 AND session_id=$2 AND id=$3`
	if owner.Purpose == CallerSendReviewLoopInitialSend {
		start = `UPDATE session_review_loops SET startup_send_state='attempting',startup_send_attempt_id=$4,startup_send_attempt_seq=1,
 startup_send_attempted_at=statement_timestamp(),startup_send_writer_seq=startup_send_writer_seq+1,startup_send_updated_at=statement_timestamp()
 WHERE org_id=$1 AND session_id=$2 AND id=$3`
		unknown = `UPDATE session_review_loops SET startup_send_state='unknown',startup_send_detail='unknown_ack',
 startup_send_observe_deadline=statement_timestamp()+interval '15 minutes',startup_send_writer_seq=startup_send_writer_seq+1,startup_send_updated_at=statement_timestamp()
 WHERE org_id=$1 AND session_id=$2 AND id=$3`
	}
	err := db.RunLifecycleTransaction(context.Background(), f.pool, func(ctx context.Context, tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, start, owner.OrgID, owner.SessionID, owner.OwnerID, attempt); err != nil {
			return err
		}
		_, err := tx.Exec(ctx, unknown, owner.OrgID, owner.SessionID, owner.OwnerID)
		return err
	})
	require.NoError(t, err, "install only a real legal retained attempt/unknown fixture without changing the immutable original command")
	return attempt
}

func callerObservationExpectedOwner(t *testing.T, f sendPostgresFixture, transactions SendTransactions, owner CallerSendOwnerScope) db.RetainedCallerSend {
	t.Helper()
	var retained db.RetainedCallerSend
	var found bool
	err := transactions.Run(context.Background(), func(ctx context.Context, tx pgx.Tx) error {
		if _, err := tx.Exec(ctx, `SET TRANSACTION READ ONLY`); err != nil {
			return err
		}
		var err error
		retained, found, err = db.ReadRetainedCallerSendInTx(ctx, f.org, tx, callerObservationOwnerRef(owner))
		return err
	})
	require.NoError(t, err, "capture the complete expected original typed owner through the existing real readonly runner")
	require.True(t, found, "the independently retained typed owner must exist before observation")
	return retained
}

func requireCallerObservationAccepted(t *testing.T, actual CallerSendObservation, fixture callerStampServiceFixture, stamp db.CallerSendStamp, expectedOwner db.RetainedCallerSend) {
	t.Helper()
	require.True(t, actual.Valid(), "positive evidence is usable only after the actual readonly unit succeeds")
	require.Equal(t, CallerSendObservedAccepted, actual.Kind(), "the exact immutable linkage should correlate the original command, not current execution authority")
	require.Equal(t, CallerSendObservationReason(""), actual.Reason(), "positive correlation must not carry an unresolved or refusal reason")
	id, accepted := actual.MessageID()
	require.True(t, accepted, "only positive exact original evidence exposes a message correlation")
	require.Equal(t, stamp.MessageID, id, "the positive result must name the original committed typed message")
	require.Equal(t, stamp.CreatedAt, actual.StampCreatedAt(), "the full positive binding must retain the exact original same-statement stamp timestamp")
	require.Equal(t, expectedOwner, actual.Owner(), "carry every observed original owner/scope/key/digest/version/attempt/state/pass field for a later separate fenced CAS")
	require.Equal(t, callerObservationOwnerRef(fixture.owner), actual.Owner().Ref(), "owner identity must come from independent controller correlation, never the raw envelope")
	command, err := restoreObservedCallerCommand(actual.Owner())
	require.NoError(t, err, "the sealed observed owner must restore only the original complete effective command")
	require.Equal(t, callerCommandBytes(t, fixture.command), callerCommandBytes(t, command), "actual JSONB readback must preserve original canonical semantics without re-rendering or reminting")
	require.Equal(t, fixture.command.Digest(), command.Digest(), "actual positive correlation must retain the original digest")
}

// Both branches inject at the application/runner acknowledgement boundary.
// They prove actual SQL commit versus actual rollback, not a network or kernel
// fault, provider delivery, caller CAS, recovery wake, or permission to resend.
func TestCallerSendObservationAfterUnknownPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		purpose CallerSendPurpose
		commit  bool
	}{
		{name: "dispatch actual commit lost acknowledgement", purpose: CallerSendCodeReviewReviewerDispatch, commit: true},
		{name: "dispatch actual rollback ambiguous acknowledgement", purpose: CallerSendCodeReviewReviewerDispatch},
		{name: "startup actual commit lost acknowledgement", purpose: CallerSendReviewLoopInitialSend, commit: true},
		{name: "startup actual rollback ambiguous acknowledgement", purpose: CallerSendReviewLoopInitialSend},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newSendPostgresFixtureWithMaxConns(t, false, 1, 1)
			fixture := seedCallerStampServiceOwner(t, f, tt.purpose, false)
			attempt := callerObservationUnknownAttempt(t, f, fixture.owner)
			service, effects := f.service(t, true)
			base := service.send.Transactions
			expectedOwner := callerObservationExpectedOwner(t, f, base, fixture.owner)
			require.Equal(t, []int32{1, 2}, []int32{expectedOwner.AttemptSeq(), expectedOwner.WriterSeq()}, "the positive fixture must retain a real noninitial observed attempt rather than losing CAS correlation")
			require.Equal(t, attempt, expectedOwner.AttemptID(), "the original attempt identity must survive the observer untouched")
			beforeSend := callerStampServiceSnapshot(t, f)
			probe := &callerStampEnqueueProbe{LegacySendPersistence: service.send.Legacy, fixture: f, owner: fixture.owner}
			service.send.Legacy = probe
			ack := errors.New("synthetic send acknowledgement unavailable after whole callback")
			runner := &sendPostgresLegacyTransactions{base: base, fixture: f, ambiguousAt: 2, commit: tt.commit, ack: ack}
			service.send.Transactions = runner
			result, err := service.SendMessage(context.Background(), fixture.input)
			require.ErrorIs(t, err, ErrCommandOutcomeUnknown, "both actual settlements must remain unknown to the original send boundary")
			require.ErrorIs(t, err, ack, "unknown send retains the application-boundary fault without manufacturing an abort")
			require.Nil(t, result, "the original unknown send cannot expose provisional acceptance")
			requireSendPostgresLegacyUnitGuards(t, runner.units)
			requireCallerStampProvisional(t, f, fixture, probe)
			if tt.commit {
				require.NoError(t, runner.units[1].settled, "the positive branch must actually commit before suppressing its acknowledgement")
			} else {
				require.ErrorIs(t, runner.units[1].settled, ack, "the negative branch must actually roll back the complete provisional graph")
				require.JSONEq(t, string(beforeSend), string(callerStampServiceSnapshot(t, f)), "actual rollback restores every original owner, claim, message, inbox, job and stamp field")
			}
			baseline := callerStampServiceSnapshot(t, f)
			actual, err := ObserveCallerSend(context.Background(), base, f.org, callerObservationOwnerRef(fixture.owner))
			require.NoError(t, err, "a separate read-only observation must not repeat or reinterpret the original send")
			if tt.commit {
				requireCallerObservationAccepted(t, actual, fixture, probe.provisional.stamp, expectedOwner)
			} else {
				require.Equal(t, CallerSendObservedUnresolved, actual.Kind(), "absent original linkage remains unresolved, never known rejection or resend permission")
				require.Equal(t, CallerSendLinkageUnresolved, actual.Reason(), "a complete inner-join miss cannot invent which evidence constituent is absent")
				require.Equal(t, expectedOwner, actual.Owner(), "negative linkage still retains only the independently observed original owner correlation")
				require.True(t, actual.StampCreatedAt().IsZero(), "a miss cannot fabricate an accepted stamp timestamp")
				id, accepted := actual.MessageID()
				require.Equal(t, int64(0), id, "unresolved evidence cannot expose a provisional rolled-back message")
				require.False(t, accepted, "absence must not authorize acceptance, abort, fallback or retry")
			}
			require.JSONEq(t, string(baseline), string(callerStampServiceSnapshot(t, f)), "observation alone cannot mutate owners, attempts, input, claims, messages, jobs, stamps, or publication state")
			require.Equal(t, 1, probe.calls, "no observation branch may resend or enqueue the original command again")
			require.Equal(t, 2, len(runner.units), "the original send retains only its entry and one whole mutation unit")
			requireSendPostgresNoEffects(t, effects)
		})
	}
}

type callerObservationSQLTrace struct {
	statements                    []string
	duringPID, afterPID           uint32
	duringReadOnly, afterReadOnly string
}
type callerObservationTracedTx struct {
	pgx.Tx
	trace *callerObservationSQLTrace
}

func (tx callerObservationTracedTx) Exec(ctx context.Context, query string, args ...any) (pgconn.CommandTag, error) {
	tx.trace.statements = append(tx.trace.statements, query)
	tag, err := tx.Tx.Exec(ctx, query, args...)
	if err == nil && query == "SET TRANSACTION READ ONLY" {
		err = tx.Tx.QueryRow(ctx, `SELECT pg_backend_pid(),current_setting('transaction_read_only')`).Scan(&tx.trace.duringPID, &tx.trace.duringReadOnly)
	}
	return tag, err
}
func (tx callerObservationTracedTx) QueryRow(ctx context.Context, query string, args ...any) pgx.Row {
	tx.trace.statements = append(tx.trace.statements, query)
	return tx.Tx.QueryRow(ctx, query, args...)
}
func (tx callerObservationTracedTx) Query(ctx context.Context, query string, args ...any) (pgx.Rows, error) {
	tx.trace.statements = append(tx.trace.statements, query)
	return tx.Tx.Query(ctx, query, args...)
}

func TestCallerSendObservationReadOnlySettlementPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		purpose CallerSendPurpose
		fault   string
	}{
		{name: "dispatch actual read-only commit", purpose: CallerSendCodeReviewReviewerDispatch},
		{name: "startup actual read-only commit", purpose: CallerSendReviewLoopInitialSend},
		{name: "dispatch forbidden actual write after positive callback", purpose: CallerSendCodeReviewReviewerDispatch, fault: "write"},
		{name: "startup forbidden actual write after positive callback", purpose: CallerSendReviewLoopInitialSend, fault: "write"},
		{name: "dispatch actual readonly commit lost acknowledgement", purpose: CallerSendCodeReviewReviewerDispatch, fault: "ack"},
		{name: "startup actual readonly commit lost acknowledgement", purpose: CallerSendReviewLoopInitialSend, fault: "ack"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newSendPostgresFixtureWithMaxConns(t, false, 1, 1)
			fixture := seedCallerStampServiceOwner(t, f, tt.purpose, false)
			service, effects := f.service(t, true)
			accepted, err := service.SendMessage(context.Background(), fixture.input)
			require.NoError(t, err, "establish a real whole committed stamped command before observer fault injection")
			require.NotNil(t, accepted, "the positive fixture must be actual committed data")
			stamp := callerStampCommittedRead(t, f, fixture.owner)
			base := service.send.Transactions
			expectedOwner := callerObservationExpectedOwner(t, f, base, fixture.owner)
			baseline, beforeEffects := callerStampServiceSnapshot(t, f), callerStampEffectsSnapshot(effects)
			ack := errors.New("synthetic observer acknowledgement unavailable after real readonly commit")
			trace := callerObservationSQLTrace{}
			callbacks := 0
			var settled error
			observedRunner := sendPostgresTransactionsFunc(func(ctx context.Context, work func(context.Context, pgx.Tx) error) error {
				settled = base.Run(ctx, func(unitCtx context.Context, tx pgx.Tx) error {
					callbacks++
					if err := work(unitCtx, callerObservationTracedTx{Tx: tx, trace: &trace}); err != nil {
						return err
					}
					if err := tx.QueryRow(unitCtx, `SELECT pg_backend_pid(),current_setting('transaction_read_only')`).Scan(&trace.afterPID, &trace.afterReadOnly); err != nil {
						return err
					}
					if tt.fault == "write" {
						_, err := tx.Exec(unitCtx, `UPDATE sessions SET status=status WHERE org_id=$1 AND id=$2`, f.org, f.session)
						return err
					}
					return nil
				})
				if settled == nil && tt.fault == "ack" {
					return ack
				}
				return settled
			})
			actual, err := ObserveCallerSend(context.Background(), observedRunner, f.org, callerObservationOwnerRef(fixture.owner))
			if tt.fault == "" {
				require.NoError(t, err, "only a successfully settled actual readonly unit may release positive immutable correlation")
				requireCallerObservationAccepted(t, actual, fixture, stamp, expectedOwner)
			} else {
				require.ErrorIs(t, err, ErrCallerSendObservation, "all final runner errors must use the fixed observation boundary")
				require.EqualError(t, err, "caller send observation failed", "even post-positive faults must not expose raw source or transport details")
				require.Equal(t, CallerSendObservation{}, actual, "a positive candidate must be entirely discarded on rollback or lost readonly-commit acknowledgement")
				if tt.fault == "ack" {
					require.NoError(t, settled, "the readonly transaction must actually commit before only its acknowledgement is suppressed")
					require.ErrorIs(t, err, ack, "lost acknowledgement cause remains available privately")
				} else {
					var pgErr *pgconn.PgError
					require.ErrorAs(t, err, &pgErr, "an actual PostgreSQL read-only violation, not a fake runner result, must reach the boundary")
					require.Equal(t, "25006", pgErr.Code, "the actual supplied unit must prohibit writes after staging positive evidence")
				}
			}
			require.Equal(t, 1, callbacks, "noncontention settlement faults cannot create retries or nested observation units")
			require.NotEmpty(t, trace.statements, "the observer must issue its actual read-only statement and source queries")
			require.Equal(t, "SET TRANSACTION READ ONLY", trace.statements[0], "the first observer callback statement must establish readonly before any source-data read")
			require.Equal(t, []string{"on", "on"}, []string{trace.duringReadOnly, trace.afterReadOnly}, "readonly must be effective before owner reads and remain effective through the positive callback")
			require.Equal(t, trace.duringPID, trace.afterPID, "all observation source reads must use the same actual supplied backend")
			for _, statement := range trace.statements[1:] {
				for _, forbidden := range []string{"FOR UPDATE", "FOR NO KEY UPDATE", "FOR SHARE", "INSERT INTO", "UPDATE ", "DELETE FROM"} {
					require.NotContains(t, statement, forbidden, "observing immutable evidence must not acquire owner rows or mutate any lifecycle state")
				}
			}
			var nextReadOnly string
			require.NoError(t, f.pool.QueryRow(context.Background(), `SELECT current_setting('transaction_read_only')`).Scan(&nextReadOnly), "inspect the next borrower on the same one-connection pool")
			require.Equal(t, "off", nextReadOnly, "transaction-local readonly must not leak into the next real pool borrower")
			require.JSONEq(t, string(baseline), string(callerStampServiceSnapshot(t, f)), "readonly observation and every injected observer fault must preserve the complete durable send and owner graph")
			require.Equal(t, beforeEffects, callerStampEffectsSnapshot(effects), "neither successful nor failed observation may emit another job, claim, publication or provider effect")
		})
	}
}

func TestCallerSendObservationExactVisibilityAndLinkagePostgres(t *testing.T) {
	t.Parallel()
	purposes := []struct {
		name    string
		purpose CallerSendPurpose
	}{
		{name: "dispatch", purpose: CallerSendCodeReviewReviewerDispatch},
		{name: "startup", purpose: CallerSendReviewLoopInitialSend},
	}
	for _, purpose := range purposes {
		t.Run(purpose.name, func(t *testing.T) {
			t.Parallel()
			tests := []struct {
				name, query  string
				otherSession bool
				kind         CallerSendObservationKind
				reason       CallerSendObservationReason
			}{
				{name: "soft deleted session", query: `UPDATE sessions SET deleted_at=statement_timestamp() WHERE org_id=$1 AND id=$2`, kind: CallerSendObservedUnavailable, reason: CallerSendScopeUnavailable},
				{name: "archived session", query: `UPDATE sessions SET archived_at=statement_timestamp() WHERE org_id=$1 AND id=$2`, kind: CallerSendObservedUnavailable, reason: CallerSendScopeUnavailable},
				{name: "archived exact lane", query: `UPDATE session_threads SET archived_at=statement_timestamp() WHERE org_id=$1 AND session_id=$2 AND id=$3`, kind: CallerSendObservedUnavailable, reason: CallerSendScopeUnavailable},
				{name: "missing actual inbox", query: `DELETE FROM thread_inbox_entries WHERE org_id=$1 AND session_id=$2 AND thread_id=$3`, kind: CallerSendObservedUnresolved, reason: CallerSendLinkageUnresolved},
				{name: "missing actual message", query: `DELETE FROM session_messages WHERE org_id=$1 AND session_id=$2 AND thread_id=$3`, kind: CallerSendObservedUnresolved, reason: CallerSendLinkageUnresolved},
				{name: "inbox different session despite same lane", otherSession: true, kind: CallerSendObservedUnresolved, reason: CallerSendLinkageUnresolved},
				{name: "inbox typed message no longer original", query: `UPDATE thread_inbox_entries SET message_id=message_id+1000000 WHERE org_id=$1 AND session_id=$2 AND thread_id=$3`, kind: CallerSendObservedUnresolved, reason: CallerSendLinkageUnresolved},
				{name: "inbox original key displaced", query: `UPDATE thread_inbox_entries SET client_message_id='synthetic-displaced-key' WHERE org_id=$1 AND session_id=$2 AND thread_id=$3`, kind: CallerSendObservedUnresolved, reason: CallerSendLinkageUnresolved},
				{name: "inbox changed to control", query: `UPDATE thread_inbox_entries SET entry_type='control' WHERE org_id=$1 AND session_id=$2 AND thread_id=$3`, kind: CallerSendObservedUnresolved, reason: CallerSendLinkageUnresolved},
				{name: "message changed to assistant", query: `UPDATE session_messages SET role='assistant' WHERE org_id=$1 AND session_id=$2 AND thread_id=$3`, kind: CallerSendObservedUnresolved, reason: CallerSendLinkageUnresolved},
			}
			for _, tt := range tests {
				t.Run(tt.name, func(t *testing.T) {
					t.Parallel()
					f := newSendPostgresFixtureWithMaxConns(t, false, 1, 1)
					fixture := seedCallerStampServiceOwner(t, f, purpose.purpose, false)
					service, effects := f.service(t, true)
					result, err := service.SendMessage(context.Background(), fixture.input)
					require.NoError(t, err, "first establish the actual complete immutable positive command linkage")
					require.NotNil(t, result, "negative observation must not rely on an uncommitted or fabricated send")
					stamp := callerStampCommittedRead(t, f, fixture.owner)
					expectedOwner := callerObservationExpectedOwner(t, f, service.send.Transactions, fixture.owner)
					positive, err := ObserveCallerSend(context.Background(), service.send.Transactions, f.org, callerObservationOwnerRef(fixture.owner))
					require.NoError(t, err, "positively establish the exact pre-mutation observer result")
					requireCallerObservationAccepted(t, positive, fixture, stamp, expectedOwner)
					if tt.otherSession {
						other := newSendPostgresComparisonScope(t, f, false)
						sendPostgresExec(t, f, `UPDATE thread_inbox_entries SET session_id=$4 WHERE org_id=$1 AND session_id=$2 AND thread_id=$3`, f.org, f.session, fixture.owner.ThreadID, other.session)
					} else {
						args := []any{f.org, f.session}
						if strings.Contains(tt.query, "$3") {
							args = append(args, fixture.owner.ThreadID)
						}
						sendPostgresExec(t, f, tt.query, args...)
					}
					baseline, beforeEffects := callerStampServiceSnapshot(t, f), callerStampEffectsSnapshot(effects)
					actual, err := ObserveCallerSend(context.Background(), service.send.Transactions, f.org, callerObservationOwnerRef(fixture.owner))
					require.NoError(t, err, "visibility or joined evidence loss is finite unresolved/unavailable observation, not a replacement send")
					require.Equal(t, tt.kind, actual.Kind(), "the final exact typed query must not accept a bare retained stamp or message id")
					require.Equal(t, tt.reason, actual.Reason(), "do not infer which absent join component proves refusal, abort or retry permission")
					require.Equal(t, expectedOwner, actual.Owner(), "visibility/linkage negatives preserve only original owner correlation and do not reconstruct command identity")
					id, accepted := actual.MessageID()
					require.Equal(t, int64(0), id, "a failed exact visibility/linkage observation must expose no accepted message")
					require.False(t, accepted, "a historical bare stamp must not bypass current observed visibility or exact actual linkage")
					require.True(t, actual.StampCreatedAt().IsZero(), "unresolved/unavailable results cannot expose the historical timestamp as accepted proof")
					require.Equal(t, stamp, callerStampCommittedRead(t, f, fixture.owner), "negative probes intentionally retain the original stamp so the test rejects insufficient bare-stamp evidence")
					require.JSONEq(t, string(baseline), string(callerStampServiceSnapshot(t, f)), "the observer cannot repair, rebind, resend, settle, or delete any evidence after the controlled fixture mutation")
					require.Equal(t, beforeEffects, callerStampEffectsSnapshot(effects), "negative observation cannot emit effects or manufacture a fallback")
				})
			}
		})
	}
}

func TestCallerSendObservationModeBoundaryPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		purpose CallerSendPurpose
		active  bool
	}{
		{name: "dispatch normalizing", purpose: CallerSendCodeReviewReviewerDispatch},
		{name: "dispatch active", purpose: CallerSendCodeReviewReviewerDispatch, active: true},
		{name: "startup normalizing", purpose: CallerSendReviewLoopInitialSend},
		{name: "startup active", purpose: CallerSendReviewLoopInitialSend, active: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newSendPostgresFixtureWithMaxConns(t, false, 1, 1)
			fixture := seedCallerStampServiceOwner(t, f, tt.purpose, false)
			service, effects := f.service(t, true)
			result, err := service.SendMessage(context.Background(), fixture.input)
			require.NoError(t, err, "establish original legacy correlation before installing only synthetic opposing mode evidence")
			require.NotNil(t, result, "the mode boundary must be tested with actual positive historical linkage")
			require.NoError(t, seedSendPostgresBetweenUnitsMode(context.Background(), f, tt.active), "fixture mode changes must retain their actual exclusive interlock and valid parser provenance")
			baseline, beforeEffects := callerStampServiceSnapshot(t, f), callerStampEffectsSnapshot(effects)
			actual, err := ObserveCallerSend(context.Background(), service.send.Transactions, f.org, callerObservationOwnerRef(fixture.owner))
			require.NoError(t, err, "valid opposing mode is unresolved, never legacy fallback or synthetic activation")
			require.Equal(t, CallerSendObservation{kind: CallerSendObservedUnresolved, reason: CallerSendModeChanged}, actual, "the fixed legacy-only observer must refuse before releasing any historical positive command evidence")
			require.JSONEq(t, string(baseline), string(callerStampServiceSnapshot(t, f)), "mode refusal must preserve all opposing provenance and original retained send data")
			require.Equal(t, beforeEffects, callerStampEffectsSnapshot(effects), "opposing mode cannot trigger sends, cleanup, wakes or publication")
		})
	}
}

func TestCallerSendObservationLateEvidencePostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		purpose  CallerSendPurpose
		disabled bool
	}{
		{name: "dispatch insertion disabled after send", purpose: CallerSendCodeReviewReviewerDispatch, disabled: true},
		{name: "startup insertion disabled after send", purpose: CallerSendReviewLoopInitialSend, disabled: true},
		{name: "dispatch business timed out after send", purpose: CallerSendCodeReviewReviewerDispatch},
		{name: "startup business cancelled after send", purpose: CallerSendReviewLoopInitialSend},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newSendPostgresFixtureWithMaxConns(t, false, 1, 1)
			fixture := seedCallerStampServiceOwner(t, f, tt.purpose, false)
			callerObservationUnknownAttempt(t, f, fixture.owner)
			service, effects := f.service(t, true)
			result, err := service.SendMessage(context.Background(), fixture.input)
			require.NoError(t, err, "retain real original linkage before business terminality or insertion disablement")
			require.NotNil(t, result, "late evidence requires a real original whole committed send")
			if tt.disabled {
				sendPostgresExec(t, f, `UPDATE caller_send_intent_control SET intents_enabled=false WHERE singleton`)
			} else if tt.purpose == CallerSendCodeReviewReviewerDispatch {
				sendPostgresExec(t, f, `UPDATE code_review_agent_results SET status='timed_out',dispatch_writer_seq=dispatch_writer_seq+1,dispatch_updated_at=statement_timestamp() WHERE org_id=$1 AND session_id=$2 AND id=$3`, f.org, f.session, fixture.owner.OwnerID)
			} else {
				sendPostgresExec(t, f, `UPDATE session_review_loops SET status='cancelled',startup_send_writer_seq=startup_send_writer_seq+1,startup_send_updated_at=statement_timestamp() WHERE org_id=$1 AND session_id=$2 AND id=$3`, f.org, f.session, fixture.owner.OwnerID)
				sendPostgresExec(t, f, `UPDATE session_review_loop_passes SET status='failed' WHERE org_id=$1 AND session_id=$2 AND loop_id=$3`, f.org, f.session, fixture.owner.OwnerID)
			}
			stamp := callerStampCommittedRead(t, f, fixture.owner)
			expectedOwner := callerObservationExpectedOwner(t, f, service.send.Transactions, fixture.owner)
			baseline, beforeEffects := callerStampServiceSnapshot(t, f), callerStampEffectsSnapshot(effects)
			actual, err := ObserveCallerSend(context.Background(), service.send.Transactions, f.org, callerObservationOwnerRef(fixture.owner))
			require.NoError(t, err, "immutable original evidence remains observable after insertion disablement or business terminality")
			requireCallerObservationAccepted(t, actual, fixture, stamp, expectedOwner)
			require.Equal(t, "unknown", actual.Owner().State(), "positive evidence must preserve actual retained attempt state without silently accepting or settling its controller")
			require.JSONEq(t, string(baseline), string(callerStampServiceSnapshot(t, f)), "the observer cannot revive a timed-out owner, uncancel a loop, fill a pass, alter its attempt, or enable writes")
			require.Equal(t, beforeEffects, callerStampEffectsSnapshot(effects), "late immutable evidence must not resume business execution or emit recovery effects")
		})
	}
}

func TestCallerSendObservationDoesNotWaitForOwnerLockPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		purpose   CallerSendPurpose
		lockQuery string
	}{
		{name: "dispatch owner held", purpose: CallerSendCodeReviewReviewerDispatch, lockQuery: `SELECT id FROM code_review_agent_results WHERE org_id=$1 AND session_id=$2 AND id=$3 FOR UPDATE`},
		{name: "startup owner held", purpose: CallerSendReviewLoopInitialSend, lockQuery: `SELECT id FROM session_review_loops WHERE org_id=$1 AND session_id=$2 AND id=$3 FOR UPDATE`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newSendPostgresFixtureWithMaxConns(t, false, 1, 3)
			fixture := seedCallerStampServiceOwner(t, f, tt.purpose, false)
			service, effects := f.service(t, true)
			result, err := service.SendMessage(context.Background(), fixture.input)
			require.NoError(t, err, "establish real original evidence before a separate controller holds its owner row")
			require.NotNil(t, result, "owner-lock nonwaiting requires an actual committed stamped message")
			stamp := callerStampCommittedRead(t, f, fixture.owner)
			expectedOwner := callerObservationExpectedOwner(t, f, service.send.Transactions, fixture.owner)
			baseline, beforeEffects := callerStampServiceSnapshot(t, f), callerStampEffectsSnapshot(effects)
			holder, err := f.pool.Begin(context.Background())
			require.NoError(t, err, "a separate bounded control checkout should hold the exact real owner lock")
			t.Cleanup(func() {
				err := holder.Rollback(context.Background())
				if !errors.Is(err, pgx.ErrTxClosed) {
					require.NoError(t, err, "release only the synthetic owner-row lock")
				}
			})
			var held uuid.UUID
			require.NoError(t, holder.QueryRow(context.Background(), tt.lockQuery, f.org, f.session, fixture.owner.OwnerID).Scan(&held), "the separate real controller must acquire its exact scoped owner lock before observation")
			require.Equal(t, fixture.owner.OwnerID, held, "the held row must be the actual typed original caller owner")
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			actual, err := ObserveCallerSend(ctx, service.send.Transactions, f.org, callerObservationOwnerRef(fixture.owner))
			require.NoError(t, err, "immutable observation must finish while the exact owner lock remains held on another connection")
			requireCallerObservationAccepted(t, actual, fixture, stamp, expectedOwner)
			probe, err := f.pool.Begin(context.Background())
			require.NoError(t, err, "the third and final bounded checkout should independently prove the owner lock is still held")
			var probed uuid.UUID
			err = probe.QueryRow(context.Background(), tt.lockQuery+" NOWAIT", f.org, f.session, fixture.owner.OwnerID).Scan(&probed)
			var pgErr *pgconn.PgError
			require.ErrorAs(t, err, &pgErr, "a separate NOWAIT acquisition must encounter the actual still-held PostgreSQL row lock")
			require.Equal(t, "55P03", pgErr.Code, "positive observation cannot have silently acquired or released the controller's owner lock")
			require.NoError(t, probe.Rollback(context.Background()), "release the failed synthetic NOWAIT probe transaction")
			require.NoError(t, holder.Rollback(context.Background()), "release the real held owner lock only after positive observation and independent lock readback")
			require.Equal(t, int32(3), f.pool.Config().MaxConns, "the complete lock proof must use no unbounded or hidden pool")
			require.JSONEq(t, string(baseline), string(callerStampServiceSnapshot(t, f)), "nonlocking immutable observation preserves every original owner and send row")
			require.Equal(t, beforeEffects, callerStampEffectsSnapshot(effects), "finishing under a held owner lock cannot imply a write, send or controller-settlement effect")
		})
	}
}

// A real table NOWAIT conflict produces PostgreSQL 55P03, which the existing
// runner retries only after confirmed whole rollback. A second bounded fixture
// connection removes the inbox between attempts. This proves callback reset
// against actual source change; it is not a simulated production resend/CAS.
func TestCallerSendObservationRetryDiscardsEarlierPositivePostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		purpose CallerSendPurpose
	}{
		{name: "dispatch original linkage removed between actual attempts", purpose: CallerSendCodeReviewReviewerDispatch},
		{name: "startup original linkage removed between actual attempts", purpose: CallerSendReviewLoopInitialSend},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newSendPostgresFixtureWithMaxConns(t, false, 1, 2)
			fixture := seedCallerStampServiceOwner(t, f, tt.purpose, false)
			service, effects := f.service(t, true)
			result, err := service.SendMessage(context.Background(), fixture.input)
			require.NoError(t, err, "first establish actual whole committed positive evidence")
			require.NotNil(t, result, "the retry-reset fixture must contain an actual original committed command")
			base := service.send.Transactions
			stamp := callerStampCommittedRead(t, f, fixture.owner)
			expectedOwner := callerObservationExpectedOwner(t, f, base, fixture.owner)
			positive, err := ObserveCallerSend(context.Background(), base, f.org, callerObservationOwnerRef(fixture.owner))
			require.NoError(t, err, "positively establish the unchanged source before the actual retry schedule")
			requireCallerObservationAccepted(t, positive, fixture, stamp, expectedOwner)
			beforeEffects := callerStampEffectsSnapshot(effects)
			holder, err := f.pool.Begin(context.Background())
			require.NoError(t, err, "reserve only one bounded fixture control checkout")
			t.Cleanup(func() {
				err := holder.Rollback(context.Background())
				if !errors.Is(err, pgx.ErrTxClosed) {
					require.NoError(t, err, "release the isolated retry control lock")
				}
			})
			_, err = holder.Exec(context.Background(), `LOCK TABLE caller_send_intent_control IN ACCESS EXCLUSIVE MODE`)
			require.NoError(t, err, "hold only the synthetic enablement table, which the immutable observer never reads")
			attempts := 0
			var injected error
			retryRunner := sendPostgresTransactionsFunc(func(ctx context.Context, work func(context.Context, pgx.Tx) error) error {
				return base.Run(ctx, func(unitCtx context.Context, tx pgx.Tx) error {
					attempts++
					if err := work(unitCtx, tx); err != nil {
						return err
					}
					if attempts != 1 {
						return nil
					}
					_, injected = tx.Exec(unitCtx, `LOCK TABLE caller_send_intent_control IN ACCESS SHARE MODE NOWAIT`)
					var lockErr *pgconn.PgError
					if !errors.As(injected, &lockErr) || lockErr.Code != "55P03" {
						return errors.New("synthetic retry schedule did not encounter the expected actual lock")
					}
					if err := holder.Rollback(unitCtx); err != nil {
						return err
					}
					// The original observation transaction is now aborted, but its
					// source row reads never locked the inbox against this control
					// mutation on the released second real checkout.
					if _, err := f.pool.Exec(unitCtx, `DELETE FROM thread_inbox_entries WHERE org_id=$1 AND session_id=$2 AND thread_id=$3 AND client_message_id=$4`, f.org, f.session, fixture.owner.ThreadID, fixture.owner.ClientMessageID); err != nil {
						return err
					}
					return injected
				})
			})
			actual, err := ObserveCallerSend(context.Background(), retryRunner, f.org, callerObservationOwnerRef(fixture.owner))
			require.NoError(t, err, "the unchanged actual runner may retry its genuine precommit contention within its original policy")
			require.Equal(t, 2, attempts, "the schedule must exercise a confirmed rollback followed by one fresh actual observer callback")
			var lockErr *pgconn.PgError
			require.ErrorAs(t, injected, &lockErr, "the retry must be caused by actual PostgreSQL contention rather than a fabricated error category")
			require.Equal(t, "55P03", lockErr.Code, "only the real supported NOWAIT contention seam establishes this retry proof")
			require.Equal(t, CallerSendObservedUnresolved, actual.Kind(), "positive evidence from an earlier rolled-back observer callback cannot survive a later joined miss")
			require.Equal(t, CallerSendLinkageUnresolved, actual.Reason(), "the final successful callback reports only its current incomplete linkage")
			require.Equal(t, expectedOwner, actual.Owner(), "the final observation retains original independent owner correlation without replacing the command")
			id, accepted := actual.MessageID()
			require.Equal(t, int64(0), id, "the earlier candidate's accepted message must be cleared before every actual callback")
			require.False(t, accepted, "a callback retry cannot launder stale positive evidence into acceptance")
			require.True(t, actual.StampCreatedAt().IsZero(), "the earlier callback's stamp timestamp must also be discarded")
			require.Equal(t, stamp, callerStampCommittedRead(t, f, fixture.owner), "the original stamp remains present and insufficient by itself after controlled inbox removal")
			require.Equal(t, beforeEffects, callerStampEffectsSnapshot(effects), "observation retries cannot enqueue, send, publish, notify, or settle an owner")
		})
	}
}

func TestCallerSendObservationRetainedOwnerNegativesPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		purpose     CallerSendPurpose
		fixtureKind string
		reason      CallerSendObservationReason
	}{
		{name: "dispatch legacy all NULL intent", purpose: CallerSendCodeReviewReviewerDispatch, fixtureKind: "legacy", reason: CallerSendOwnerLegacy},
		{name: "startup legacy all NULL intent", purpose: CallerSendReviewLoopInitialSend, fixtureKind: "legacy", reason: CallerSendOwnerLegacy},
		{name: "dispatch full retained semantics disagree with stored digest", purpose: CallerSendCodeReviewReviewerDispatch, fixtureKind: "digest", reason: CallerSendCommandMalformed},
		{name: "startup full retained semantics disagree with stored digest", purpose: CallerSendReviewLoopInitialSend, fixtureKind: "digest", reason: CallerSendCommandMalformed},
		{name: "dispatch foreign tenant owner cannot be inferred", purpose: CallerSendCodeReviewReviewerDispatch, fixtureKind: "foreign", reason: CallerSendOwnerMissing},
		{name: "startup foreign tenant owner cannot be inferred", purpose: CallerSendReviewLoopInitialSend, fixtureKind: "foreign", reason: CallerSendOwnerMissing},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newSendPostgresFixtureWithMaxConns(t, false, 1, 1)
			service, effects := f.service(t, true)
			ref := db.CallerSendOwnerRef{Purpose: db.CallerSendStampPurpose(tt.purpose), SessionID: f.session, OwnerID: uuid.New()}
			var foreign *sendPostgresFixture
			if tt.fixtureKind == "legacy" {
				if tt.purpose == CallerSendCodeReviewReviewerDispatch {
					sendPostgresExec(t, f, `INSERT INTO code_review_agent_results(id,org_id,session_id,agent_provider,role,status) VALUES($1,$2,$3,'codex','reviewer','queued')`, ref.OwnerID, f.org, f.session)
				} else {
					sendPostgresExec(t, f, `INSERT INTO session_review_loops(id,org_id,session_id,thread_id,status,source,agent_type,max_passes) VALUES($1,$2,$3,$4,'running','manual','codex',1)`, ref.OwnerID, f.org, f.session, f.lanes[0])
				}
			} else if tt.fixtureKind == "digest" {
				fixture := seedCallerStampServiceOwner(t, f, tt.purpose, true)
				ref = callerObservationOwnerRef(fixture.owner)
				ordinary := cloneSendMessageInput(fixture.input)
				ordinary.CallerCommand = nil
				result, err := service.SendMessage(context.Background(), ordinary)
				require.NoError(t, err, "a real proof-free legacy message may exist but cannot fix a semantically mismatched retained command")
				require.NotNil(t, result, "semantic mismatch must be tested with actual durable ordinary message/inbox data")
			} else {
				other := newSendPostgresComparisonScope(t, f, true)
				foreign = &other
				fixture := seedCallerStampServiceOwner(t, other, tt.purpose, false)
				ref.OwnerID = fixture.owner.OwnerID // Keep the independent target tenant/session, not the envelope's foreign scope.
			}
			baseline, beforeEffects := callerStampServiceSnapshot(t, f), callerStampEffectsSnapshot(effects)
			var foreignBaseline []byte
			if foreign != nil {
				foreignBaseline = callerStampServiceSnapshot(t, *foreign)
			}
			actual, err := ObserveCallerSend(context.Background(), service.send.Transactions, f.org, ref)
			require.NoError(t, err, "legacy, unavailable scoped owner, and malformed original semantics remain finite unresolved observations")
			require.Equal(t, CallerSendObservedUnresolved, actual.Kind(), "nonpositive retained owner evidence never authorizes a new send, abort, fallback or acceptance")
			require.Equal(t, tt.reason, actual.Reason(), "complete all-NULL legacy shape, missing independent scope and semantic mismatch must remain distinct")
			id, accepted := actual.MessageID()
			require.Equal(t, int64(0), id, "nonpositive retained identity cannot expose an accepted message id")
			require.False(t, accepted, "ordinary durable input cannot substitute for exact original private correlation")
			require.True(t, actual.StampCreatedAt().IsZero(), "nonpositive observations cannot manufacture an accepted timestamp")
			require.JSONEq(t, string(baseline), string(callerStampServiceSnapshot(t, f)), "nonpositive restoration cannot upgrade legacy rows, fix retained digests, stamp historical collisions or resend")
			if foreign != nil {
				require.JSONEq(t, string(foreignBaseline), string(callerStampServiceSnapshot(t, *foreign)), "tenant-scoped observation must preserve every actual foreign owner and retained start")
			}
			require.Equal(t, beforeEffects, callerStampEffectsSnapshot(effects), "nonpositive owner observations cannot produce effects")
		})
	}
}

// This barrier delegates the actual visibility row and its Scan unchanged,
// then commits a fixture-only visibility change on the second bounded checkout.
// The final joined read must independently observe that new READ COMMITTED
// snapshot instead of laundering the earlier true availability observation.
type callerObservationVisibilityBarrierTx struct {
	pgx.Tx
	afterVisible func(context.Context) error
}

func (tx callerObservationVisibilityBarrierTx) QueryRow(ctx context.Context, query string, args ...any) pgx.Row {
	row := tx.Tx.QueryRow(ctx, query, args...)
	if !strings.HasPrefix(query, "SELECT true FROM sessions s") {
		return row
	}
	return sendPostgresAfterScanRow{Row: row, after: func(err error) error {
		if err != nil {
			return err
		}
		return tx.afterVisible(ctx)
	}}
}

func TestCallerSendObservationFinalVisibilitySnapshotPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		purpose     CallerSendPurpose
		archiveLane bool
	}{
		{name: "dispatch session archived after positive availability", purpose: CallerSendCodeReviewReviewerDispatch},
		{name: "dispatch lane archived after positive availability", purpose: CallerSendCodeReviewReviewerDispatch, archiveLane: true},
		{name: "startup session archived after positive availability", purpose: CallerSendReviewLoopInitialSend},
		{name: "startup lane archived after positive availability", purpose: CallerSendReviewLoopInitialSend, archiveLane: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newSendPostgresFixtureWithMaxConns(t, false, 1, 2)
			fixture := seedCallerStampServiceOwner(t, f, tt.purpose, false)
			service, effects := f.service(t, true)
			result, err := service.SendMessage(context.Background(), fixture.input)
			require.NoError(t, err, "retain a real positive whole command before scheduling a visibility snapshot change")
			require.NotNil(t, result, "the snapshot probe must name an actual committed original message")
			base := service.send.Transactions
			expectedOwner := callerObservationExpectedOwner(t, f, base, fixture.owner)
			stamp, beforeEffects := callerStampCommittedRead(t, f, fixture.owner), callerStampEffectsSnapshot(effects)
			barriers := 0
			var afterMutation []byte
			runner := sendPostgresTransactionsFunc(func(ctx context.Context, work func(context.Context, pgx.Tx) error) error {
				return base.Run(ctx, func(unitCtx context.Context, tx pgx.Tx) error {
					return work(unitCtx, callerObservationVisibilityBarrierTx{Tx: tx, afterVisible: func(barrierCtx context.Context) error {
						barriers++
						query := `UPDATE sessions SET archived_at=statement_timestamp() WHERE org_id=$1 AND id=$2`
						args := []any{f.org, f.session}
						if tt.archiveLane {
							query = `UPDATE session_threads SET archived_at=statement_timestamp() WHERE org_id=$1 AND session_id=$2 AND id=$3`
							args = append(args, fixture.owner.ThreadID)
						}
						if _, err := f.pool.Exec(barrierCtx, query, args...); err != nil {
							return err
						}
						afterMutation = callerStampServiceSnapshot(t, f)
						return nil
					}})
				})
			})
			actual, err := ObserveCallerSend(context.Background(), runner, f.org, callerObservationOwnerRef(fixture.owner))
			require.NoError(t, err, "the final actual source statement must report only incomplete current correlation after visibility changes")
			require.Equal(t, 1, barriers, "the schedule must change visibility only after one actual positive visibility row, without manufacturing a source result")
			require.Equal(t, CallerSendObservedUnresolved, actual.Kind(), "earlier availability cannot authorize positive evidence from a later invisible source snapshot")
			require.Equal(t, CallerSendLinkageUnresolved, actual.Reason(), "the final inner-join miss remains honest about its nonpositive combined evidence boundary")
			require.Equal(t, expectedOwner, actual.Owner(), "visibility changes must neither replace original owner correlation nor settle its controller")
			id, accepted := actual.MessageID()
			require.Equal(t, int64(0), id, "the final snapshot cannot release an accepted message from stale availability")
			require.False(t, accepted, "a bare original message/stamp is insufficient when final source visibility changed")
			require.True(t, actual.StampCreatedAt().IsZero(), "a rejected final snapshot cannot release the original accepted timestamp")
			require.Equal(t, stamp, callerStampCommittedRead(t, f, fixture.owner), "the original stamp remains intact despite final visibility refusal")
			require.JSONEq(t, string(afterMutation), string(callerStampServiceSnapshot(t, f)), "after the deliberate barrier mutation, the observer must preserve every actual durable row exactly")
			require.Equal(t, beforeEffects, callerStampEffectsSnapshot(effects), "a visibility race cannot trigger acceptance effects, resend, repair or publication")
		})
	}
}
