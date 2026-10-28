package thread

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"
)

type callerStampSQLCounts struct{ reads, inserts, other int }

// This observer delegates every operation to the actual provided transaction.
// It adds no SQL, settlement, runner policy, guard, retry, or ownership.
type callerStampSQLObservedTx struct {
	pgx.Tx
	counts *callerStampSQLCounts
}

func (tx callerStampSQLObservedTx) observe(query string) {
	query = strings.ToLower(strings.TrimSpace(query))
	if !strings.Contains(query, "caller_send_command_stamps") {
		return
	}
	switch {
	case strings.HasPrefix(query, "select"):
		tx.counts.reads++
	case strings.HasPrefix(query, "insert"):
		tx.counts.inserts++
	default:
		tx.counts.other++
	}
}

func (tx callerStampSQLObservedTx) Query(ctx context.Context, query string, args ...any) (pgx.Rows, error) {
	tx.observe(query)
	return tx.Tx.Query(ctx, query, args...)
}

func (tx callerStampSQLObservedTx) QueryRow(ctx context.Context, query string, args ...any) pgx.Row {
	tx.observe(query)
	return tx.Tx.QueryRow(ctx, query, args...)
}

func (tx callerStampSQLObservedTx) Exec(ctx context.Context, query string, args ...any) (pgconn.CommandTag, error) {
	tx.observe(query)
	return tx.Tx.Exec(ctx, query, args...)
}

func observeCallerStampServiceSQL(service *Service, counts *callerStampSQLCounts) {
	base := service.send.Transactions
	service.send.Transactions = sendPostgresTransactionsFunc(func(ctx context.Context, work func(context.Context, pgx.Tx) error) error {
		return base.Run(ctx, func(unitCtx context.Context, tx pgx.Tx) error {
			return work(unitCtx, callerStampSQLObservedTx{Tx: tx, counts: counts})
		})
	})
}

func callerStampEffectsSnapshot(effects *sendPostgresEffects) []int {
	return []int{len(effects.lifecycle), len(effects.jobs), effects.claims, effects.pending, effects.threadStatus, effects.parentStatus}
}

func callerStampCommittedRead(t *testing.T, f sendPostgresFixture, owner CallerSendOwnerScope) db.CallerSendStamp {
	t.Helper()
	var stamp db.CallerSendStamp
	var found bool
	err := db.RunLifecycleTransaction(context.Background(), f.pool, func(ctx context.Context, tx pgx.Tx) error {
		var err error
		stamp, found, err = db.ReadCallerSendStampInTx(ctx, f.org, tx, callerStampDBScope(owner))
		return err
	})
	require.NoError(t, err, "an independent bounded observer must read the exact committed synthetic stamp")
	require.True(t, found, "positive retained correlation must remain visible independently of send effects")
	return stamp
}

func callerStampCount(t *testing.T, f sendPostgresFixture) int {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	var count int
	require.NoError(t, f.pool.QueryRow(ctx, `SELECT count(*) FROM caller_send_command_stamps WHERE org_id=$1 AND session_id=$2`, f.org, f.session).Scan(&count), "count only this synthetic scope's immutable stamps")
	return count
}

func TestThreadSendLegacyCallerStampExactReplayPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		purpose CallerSendPurpose
	}{
		{name: "reviewer dispatch", purpose: CallerSendCodeReviewReviewerDispatch},
		{name: "initial loop send", purpose: CallerSendReviewLoopInitialSend},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newSendPostgresFixtureWithMaxConns(t, false, 1, 1)
			fixture := seedCallerStampServiceOwner(t, f, tt.purpose, false)
			service, effects := f.service(t, true)
			accepted, err := service.SendMessage(context.Background(), fixture.input)
			require.NoError(t, err, "first establish an actual whole committed stamped legacy send")
			require.NotNil(t, accepted, "the original known commit must return its message")
			require.NotNil(t, accepted.Message, "the original stamped send must retain an actual message identity")
			originalStamp := callerStampCommittedRead(t, f, fixture.owner)
			// A fresh Record would be refused by the actual BEFORE INSERT gate.
			// Replay must retain positive evidence through a plain Read instead.
			sendPostgresExec(t, f, `UPDATE caller_send_intent_control SET intents_enabled=false WHERE singleton AND schema_ready`)
			baseline := callerStampServiceSnapshot(t, f)
			beforeEffects := callerStampEffectsSnapshot(effects)
			var counts callerStampSQLCounts
			observeCallerStampServiceSQL(service, &counts)
			replayed, err := service.SendMessage(context.Background(), fixture.input)
			require.NoError(t, err, "exact stamped replay must not attempt insertion even while new-intent control is disabled")
			require.NotNil(t, replayed, "historical matching evidence should return the original message")
			require.Equal(t, accepted.Message, replayed.Message, "exact replay must retain the complete original message and identity")
			require.Nil(t, replayed.Admission, "private legacy correlation must not become an active admission receipt")
			require.Equal(t, callerStampSQLCounts{reads: 1}, counts, "replay may only read the original stamp, never insert, update or remove one")
			require.JSONEq(t, string(baseline), string(callerStampServiceSnapshot(t, f)), "exact replay must not mutate any original graph, typed owner or stamp row")
			require.Equal(t, originalStamp, callerStampCommittedRead(t, f, fixture.owner), "replay must preserve the original immutable stamp including its creation time")
			require.Equal(t, beforeEffects, callerStampEffectsSnapshot(effects), "historical replay must not repeat claim, pending, job or lifecycle effects")
		})
	}
}

func TestThreadSendLegacyCallerStampUnstampedCollisionPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		purpose CallerSendPurpose
	}{
		{name: "reviewer dispatch", purpose: CallerSendCodeReviewReviewerDispatch},
		{name: "initial loop send", purpose: CallerSendReviewLoopInitialSend},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newSendPostgresFixtureWithMaxConns(t, false, 1, 1)
			fixture := seedCallerStampServiceOwner(t, f, tt.purpose, false)
			service, effects := f.service(t, true)
			ordinary := cloneSendMessageInput(fixture.input)
			ordinary.CallerCommand = nil
			_, err := service.SendMessage(context.Background(), ordinary)
			require.NoError(t, err, "seed an actual preexisting legacy message/inbox with the same key but no private stamp")
			require.Equal(t, 0, callerStampCount(t, f), "proof-free acceptance must not manufacture a caller stamp")
			baseline := callerStampServiceSnapshot(t, f)
			beforeEffects := callerStampEffectsSnapshot(effects)
			var counts callerStampSQLCounts
			observeCallerStampServiceSQL(service, &counts)
			result, err := service.SendMessage(context.Background(), fixture.input)
			require.ErrorIs(t, err, ErrCallerCommandMismatch, "matching current text/key cannot turn an unstamped historical message into a caller-owned receipt")
			require.Nil(t, result, "an unstamped collision must not disclose accepted caller correlation")
			require.Equal(t, callerStampSQLCounts{reads: 1}, counts, "an unstamped collision may only observe absence, never create a stamp from current input")
			require.Equal(t, 0, callerStampCount(t, f), "refused replay must leave the original unstamped history unstamped")
			require.JSONEq(t, string(baseline), string(callerStampServiceSnapshot(t, f)), "collision refusal must not mutate any graph, owner or historical identity")
			require.Equal(t, beforeEffects, callerStampEffectsSnapshot(effects), "collision refusal must publish no new effect or job wake")
		})
	}
}

func TestThreadSendLegacyCallerStampReplayMismatchPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		purpose  CallerSendPurpose
		mismatch string
	}{}
	for _, purpose := range []CallerSendPurpose{CallerSendCodeReviewReviewerDispatch, CallerSendReviewLoopInitialSend} {
		for _, mismatch := range []string{"changed effective input", "different retained digest", "replaced typed message pointer"} {
			tests = append(tests, struct {
				name     string
				purpose  CallerSendPurpose
				mismatch string
			}{name: string(purpose) + "/" + mismatch, purpose: purpose, mismatch: mismatch})
		}
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newSendPostgresFixtureWithMaxConns(t, false, 1, 1)
			fixture := seedCallerStampServiceOwner(t, f, tt.purpose, tt.mismatch == "different retained digest")
			service, effects := f.service(t, true)
			original := cloneSendMessageInput(fixture.input)
			if tt.mismatch == "different retained digest" {
				original.CallerCommand = nil
			}
			accepted, err := service.SendMessage(context.Background(), original)
			require.NoError(t, err, "establish actual historical message/inbox linkage before the immutable replay mismatch")
			require.NotNil(t, accepted, "the historical fixture must be a real committed message")
			require.NotNil(t, accepted.Message, "the historical fixture must retain its actual original message pointer")
			input := cloneSendMessageInput(fixture.input)
			switch tt.mismatch {
			case "changed effective input":
				input.Message += " Synthetic semantic change."
				changed, changeErr := NewCallerSendCommand(fixture.owner.Purpose, fixture.owner.OwnerID, input)
				require.NoError(t, changeErr, "the changed current input must carry a valid new semantic descriptor under the same retained owner/key")
				input.CallerCommand, err = changed.Proof()
				require.NoError(t, err, "the negative replay must not fail merely because the current transport proof is malformed")
			case "different retained digest":
				// This deliberately SQL-shape-valid negative owner retains a
				// different digest; storage must not silently repair its history.
				err = db.RunLifecycleTransaction(context.Background(), f.pool, func(ctx context.Context, tx pgx.Tx) error {
					_, err := db.RecordCallerSendStampInTx(ctx, f.org, tx, db.CallerSendStampInput{
						Scope: callerStampDBScope(fixture.owner), Version: db.CallerSendStampVersion,
						Digest: strings.Repeat("b", 64), MessageID: accepted.Message.ID})
					return err
				})
				require.NoError(t, err, "retain a real immutable stamp matching the typed owner's different digest before testing original-input replay")
			case "replaced typed message pointer":
				err = db.RunLifecycleTransaction(context.Background(), f.pool, func(ctx context.Context, tx pgx.Tx) error {
					message := models.SessionMessage{OrgID: f.org, SessionID: f.session, ThreadID: &f.lanes[0], TurnNumber: 99, Role: models.MessageRoleUser, Content: "Synthetic different historical message"}
					if err := db.NewSessionMessageStore(tx).Create(ctx, &message); err != nil {
						return err
					}
					_, err := tx.Exec(ctx, `UPDATE thread_inbox_entries SET message_id=$5 WHERE org_id=$1 AND session_id=$2 AND thread_id=$3 AND client_message_id=$4`, f.org, f.session, f.lanes[0], fixture.owner.ClientMessageID, message.ID)
					return err
				})
				require.NoError(t, err, "change only the exact synthetic inbox pointer while preserving its original immutable stamp")
			}
			baseline := callerStampServiceSnapshot(t, f)
			beforeEffects := callerStampEffectsSnapshot(effects)
			var counts callerStampSQLCounts
			observeCallerStampServiceSQL(service, &counts)
			result, err := service.SendMessage(context.Background(), input)
			require.ErrorIs(t, err, ErrCallerCommandMismatch, "a different semantic descriptor or historical message pointer must not be accepted under the original stamp")
			require.Nil(t, result, "an immutable replay mismatch must return no acceptance")
			require.Equal(t, callerStampSQLCounts{reads: 1}, counts, "mismatch handling must only read, never rewrite or replace historical stamp evidence")
			require.JSONEq(t, string(baseline), string(callerStampServiceSnapshot(t, f)), "replay mismatch must preserve every original row and must not repair mismatched history")
			require.Equal(t, beforeEffects, callerStampEffectsSnapshot(effects), "mismatched replay must emit no new or compensating effects")
		})
	}
}

func TestThreadSendLegacyCallerStampInboxScopeReplayPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		purpose CallerSendPurpose
	}{
		{name: "reviewer dispatch", purpose: CallerSendCodeReviewReviewerDispatch},
		{name: "initial loop send", purpose: CallerSendReviewLoopInitialSend},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newSendPostgresFixtureWithMaxConns(t, false, 1, 1)
			fixture := seedCallerStampServiceOwner(t, f, tt.purpose, false)
			service, effects := f.service(t, true)
			accepted, err := service.SendMessage(context.Background(), fixture.input)
			require.NoError(t, err, "establish an actual scoped message/inbox/stamp before probing malformed retained inbox scope")
			require.NotNil(t, accepted, "scope probe must start with a real committed historical message")
			require.NotNil(t, accepted.Message, "scope probe must retain the otherwise-correct original message identity")
			other := newSendPostgresComparisonScope(t, f, false)
			err = db.RunLifecycleTransaction(context.Background(), f.pool, func(ctx context.Context, tx pgx.Tx) error {
				_, err := tx.Exec(ctx, `UPDATE thread_inbox_entries SET session_id=$5 WHERE org_id=$1 AND session_id=$2 AND thread_id=$3 AND client_message_id=$4`, f.org, f.session, f.lanes[0], fixture.owner.ClientMessageID, other.session)
				return err
			})
			if err != nil {
				// If a future full schema prevents this drift, positively pin the
				// actual composite FK instead of weakening it or skipping proof.
				var pgErr *pgconn.PgError
				require.ErrorAs(t, err, &pgErr, "only a concrete installed composite foreign key may replace the actual malformed-row replay probe")
				require.Equal(t, "23503", pgErr.Code, "a preventing scope constraint must be an actual foreign-key refusal")
				var columns []string
				var target string
				require.NoError(t, f.pool.QueryRow(context.Background(), `SELECT ARRAY(
 SELECT a.attname::text FROM unnest(c.conkey) WITH ORDINALITY AS k(attnum,ord)
 JOIN pg_attribute a ON a.attrelid=c.conrelid AND a.attnum=k.attnum ORDER BY k.ord),c.confrelid::regclass::text
 FROM pg_constraint c WHERE c.conrelid='thread_inbox_entries'::regclass AND c.conname=$1 AND c.contype='f'`, pgErr.ConstraintName).Scan(&columns, &target), "inspect the actual installed preventing constraint rather than infer safety from any SQL error")
				require.Equal(t, []string{"org_id", "session_id", "thread_id"}, columns, "the preventing inbox FK must bind the complete tenant/session/thread tuple")
				require.Equal(t, "session_threads", target, "the composite constraint must bind the actual scoped thread parent")
				return
			}
			var actualOrg, actualSession, actualThread uuid.UUID
			var actualMessage int64
			require.NoError(t, f.pool.QueryRow(context.Background(), `SELECT org_id,session_id,thread_id,message_id FROM thread_inbox_entries WHERE org_id=$1 AND thread_id=$2 AND client_message_id=$3`, f.org, f.lanes[0], fixture.owner.ClientMessageID).Scan(&actualOrg, &actualSession, &actualThread, &actualMessage), "positively read the actual malformed inbox tuple permitted by the current fully migrated schema")
			require.Equal(t, []uuid.UUID{f.org, other.session, f.lanes[0]}, []uuid.UUID{actualOrg, actualSession, actualThread}, "the probe must really preserve key/lane while replacing only its separate session parent")
			require.Equal(t, accepted.Message.ID, actualMessage, "the malformed inbox must still point at the original otherwise-correct message and stamp")
			baseline, otherBaseline := callerStampServiceSnapshot(t, f), callerStampServiceSnapshot(t, other)
			beforeEffects := callerStampEffectsSnapshot(effects)
			var counts callerStampSQLCounts
			observeCallerStampServiceSQL(service, &counts)
			result, err := service.SendMessage(context.Background(), fixture.input)
			require.ErrorIs(t, err, ErrCallerCommandMismatch, "a message and stamp match cannot hide a different actual inbox session scope")
			require.Nil(t, result, "malformed inbox scope must not disclose caller acceptance")
			require.Equal(t, 0, counts.inserts+counts.other, "malformed replay must not create or repair a stamp or historical inbox row")
			require.JSONEq(t, string(baseline), string(callerStampServiceSnapshot(t, f)), "scope refusal must preserve the original exact target rows")
			require.JSONEq(t, string(otherBaseline), string(callerStampServiceSnapshot(t, other)), "scope refusal must not rewrite the other session's malformed historical association")
			require.Equal(t, beforeEffects, callerStampEffectsSnapshot(effects), "malformed-scope refusal must produce no new or compensating effects")
		})
	}
}

func TestThreadSendLegacyCallerStampProofFreeCompatibilityPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		grouped bool
	}{{name: "plain inbox"}, {name: "grouped inbox", grouped: true}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newSendPostgresFixtureWithMaxConns(t, false, 1, 1)
			service, effects := f.service(t, tt.grouped)
			var counts callerStampSQLCounts
			observeCallerStampServiceSQL(service, &counts)
			input := f.input(0, "synthetic-ordinary-proof-free-key")
			accepted, err := service.SendMessage(context.Background(), input)
			require.NoError(t, err, "ordinary legacy input must retain its original acceptance without enabling private caller intents")
			require.NotNil(t, accepted, "ordinary legacy acceptance must retain its actual message")
			baseline := callerStampServiceSnapshot(t, f)
			beforeEffects := callerStampEffectsSnapshot(effects)
			replayed, err := service.SendMessage(context.Background(), input)
			require.NoError(t, err, "proof-free historical replay must retain unchanged legacy behavior")
			require.NotNil(t, replayed, "ordinary legacy replay must retain its original successful result")
			require.Equal(t, accepted.Message, replayed.Message, "proof-free replay must return the same original complete message")
			require.Equal(t, callerStampSQLCounts{}, counts, "proof-free creation and replay must add no caller-stamp SELECT, INSERT, UPDATE or DELETE SQL")
			require.Equal(t, 0, callerStampCount(t, f), "proof-free behavior must leave private correlation absent")
			require.JSONEq(t, string(baseline), string(callerStampServiceSnapshot(t, f)), "proof-free replay must not alter the established ordinary graph")
			require.Equal(t, beforeEffects, callerStampEffectsSnapshot(effects), "ordinary historical replay must not repeat legacy effects")
			var enabled bool
			require.NoError(t, f.pool.QueryRow(context.Background(), `SELECT intents_enabled FROM caller_send_intent_control WHERE singleton`).Scan(&enabled), "read back the real isolated control flag after proof-free sends")
			require.False(t, enabled, "ordinary acceptance cannot enable private caller intents as a compatibility side effect")
		})
	}
}

func TestThreadSendLegacyCallerStampCapacityWaitPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		purpose CallerSendPurpose
	}{
		{name: "reviewer dispatch", purpose: CallerSendCodeReviewReviewerDispatch},
		{name: "initial loop send", purpose: CallerSendReviewLoopInitialSend},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newSendPostgresFixtureWithMaxConns(t, false, 4, 1)
			fixture := seedCallerStampServiceOwner(t, f, tt.purpose, false)
			sendPostgresExec(t, f, `UPDATE sessions SET status='running' WHERE org_id=$1 AND id=$2`, f.org, f.session)
			sendPostgresExec(t, f, `UPDATE session_threads SET status='running' WHERE org_id=$1 AND session_id=$2 AND id=ANY($3::uuid[])`, f.org, f.session, f.lanes[1:])
			service, effects := f.service(t, true)
			base := service.send.Transactions
			var units int
			var provisional db.CallerSendStamp
			var provisionalGraph sendPostgresGraph
			var found bool
			service.send.Transactions = sendPostgresTransactionsFunc(func(ctx context.Context, work func(context.Context, pgx.Tx) error) error {
				units++
				return base.Run(ctx, func(unitCtx context.Context, tx pgx.Tx) error {
					if err := work(unitCtx, tx); err != nil {
						return err
					}
					if units != 2 {
						return nil
					}
					var err error
					provisionalGraph, err = readSendPostgresGraph(unitCtx, tx, f)
					if err != nil {
						return err
					}
					provisional, found, err = db.ReadCallerSendStampInTx(unitCtx, f.org, tx, callerStampDBScope(fixture.owner))
					return err
				})
			})
			result, err := service.SendMessage(context.Background(), fixture.input)
			require.NoError(t, err, "capacity waiting must retain its actual queued message and private stamp before the early return")
			require.NotNil(t, result, "known capacity acceptance must return its queued input")
			require.NotNil(t, result.Message, "capacity acceptance must retain a real original message")
			require.Equal(t, 2, units, "capacity acceptance must keep the original entry plus one whole mutation ownership")
			require.True(t, found, "the same supplied whole unit must already contain the stamp even though enqueue is bypassed")
			require.Equal(t, db.CallerSendStamp{OrgID: f.org, CallerSendStampInput: db.CallerSendStampInput{
				Scope: callerStampDBScope(fixture.owner), Version: db.CallerSendStampVersion, Digest: fixture.command.Digest(), MessageID: result.Message.ID}, CreatedAt: provisional.CreatedAt}, provisional,
				"capacity waiting must preserve the exact original owner descriptor and actual message binding")
			require.NotZero(t, provisional.CreatedAt, "capacity stamps must retain a real database-owned timestamp")
			require.Equal(t, sendPostgresGraph{messages: 1, inbox: 1, pending: 1,
				parentStatus: models.SessionStatusRunning, threadStatus: models.ThreadStatusIdle}, provisionalGraph,
				"capacity acceptance must stamp without inventing a job, canonical dispatch or target-lane execution claim")
			require.Equal(t, provisionalGraph, sendPostgresReadGraph(t, f), "known commit must expose the same complete capacity-wait graph independently")
			require.Equal(t, provisional, callerStampCommittedRead(t, f, fixture.owner), "the independent committed stamp must match the original same-unit capacity stamp")
			requireSendPostgresNoEffects(t, effects)
			require.Nil(t, result.Admission, "capacity correlation must not become active admission authority")
		})
	}
}

// This uses the existing transparent real-pool ACK seam: the actual production
// runner commits, then its test wrapper returns an error instead of the ACK.
// It is application-boundary fault injection, not network/kernel conformance.
func TestThreadSendLegacyCallerStampSimulatedLostAcknowledgementPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		purpose CallerSendPurpose
	}{
		{name: "reviewer dispatch", purpose: CallerSendCodeReviewReviewerDispatch},
		{name: "initial loop send", purpose: CallerSendReviewLoopInitialSend},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newSendPostgresFixtureWithMaxConns(t, false, 1, 1)
			fixture := seedCallerStampServiceOwner(t, f, tt.purpose, false)
			service, effects := f.service(t, true)
			probe := &callerStampEnqueueProbe{LegacySendPersistence: service.send.Legacy, fixture: f, owner: fixture.owner}
			service.send.Legacy = probe
			ack := errors.New("synthetic lost acknowledgement after actual caller-stamped commit")
			runner := &sendPostgresLegacyTransactions{base: service.send.Transactions, fixture: f, ambiguousAt: 2, commit: true, ack: ack}
			service.send.Transactions = runner
			result, err := service.SendMessage(context.Background(), fixture.input)
			var unknown *CommandOutcomeUnknownError
			require.ErrorAs(t, err, &unknown, "a caller stamp must not turn a lost outer commit acknowledgement into a known accepted or aborted outcome")
			require.ErrorIs(t, err, ack, "uncertainty must preserve the original application-boundary ACK fault")
			require.Nil(t, result, "a lost acknowledgement must not expose a provisional successful send result")
			require.Equal(t, fixture.owner.ClientMessageID, unknown.ClientMessageID, "unknown outcome must retain the original owner key for independent reconciliation")
			require.Equal(t, db.LifecycleIngress{}, unknown.Ingress, "private legacy correlation must not fabricate active receipt authority")
			require.NotEqual(t, uuid.Nil, unknown.DiagnosticID, "unknown outcome must retain its original safe diagnostic correlation")
			requireSendPostgresLegacyUnitGuards(t, runner.units)
			require.NoError(t, runner.units[1].settled, "this test must actually commit through the real runner before suppressing only its acknowledgement")
			requireCallerStampProvisional(t, f, fixture, probe)
			require.Equal(t, probe.provisional.graph, sendPostgresReadGraph(t, f), "the independent database must contain the complete committed graph despite the unknown caller outcome")
			require.Equal(t, probe.provisional.stamp, callerStampCommittedRead(t, f, fixture.owner), "positive stamp observation must retain original correlation without retrying or declaring caller acceptance")
			require.Equal(t, 1, callerStampCount(t, f), "the unknown path must not resend, compensate or create another immutable stamp")
			requireSendPostgresNoEffects(t, effects)
		})
	}
}
