package thread

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/stretchr/testify/require"
)

// This v0 shadow row satisfies the real coarse representation guard but not
// strict inactive provenance. It is synthetic invalid metadata, not activation
// or supported writer/fleet evidence, and every normal DB guard stays enabled.
func TestCallerSendObservationInvalidProvenancePostgres(t *testing.T) {
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
			result, err := service.SendMessage(context.Background(), fixture.input)
			require.NoError(t, err, "first establish the exact real committed stamped send")
			require.NotNil(t, result, "invalid provenance refusal must be tested against real positive historical evidence")
			base := service.send.Transactions
			owner := callerObservationExpectedOwner(t, f, base, fixture.owner)
			stamp := callerStampCommittedRead(t, f, fixture.owner)
			positive, err := ObserveCallerSend(context.Background(), base, f.org, callerObservationOwnerRef(fixture.owner))
			require.NoError(t, err, "establish actual positive observation before adding only the invalid inactive metadata")
			requireCallerObservationAccepted(t, positive, fixture, stamp, owner)
			var exclusive bool
			err = db.RunLifecycleTransaction(context.Background(), f.pool, func(ctx context.Context, tx pgx.Tx) error {
				key := "session_lifecycle_cutover:" + f.org.String() + ":" + f.session.String()
				if err := tx.QueryRow(ctx, `SELECT pg_try_advisory_xact_lock(hashtextextended($1::text,0))`, key).Scan(&exclusive); err != nil {
					return err
				}
				if !exclusive {
					return errors.New("synthetic invalid provenance fixture interlock unexpectedly busy")
				}
				_, err := tx.Exec(ctx, `INSERT INTO session_lifecycle_cutovers(org_id,session_id,version,state,minimum_writer_version) VALUES($1,$2,0,'shadow',1)`, f.org, f.session)
				return err
			})
			require.NoError(t, err, "the actual v0 representation guard should admit the one legal shadow-row fixture under its exact exclusive interlock")
			require.True(t, exclusive, "the actual interlock must be acquired in the same owning fixture transaction as the insertion")
			var version int64
			var state string
			var minimumWriter int
			var cutoverBefore json.RawMessage
			err = f.pool.QueryRow(context.Background(), `SELECT c.version,c.state,c.minimum_writer_version,to_jsonb(c) FROM session_lifecycle_cutovers c WHERE c.org_id=$1 AND c.session_id=$2`, f.org, f.session).Scan(&version, &state, &minimumWriter, &cutoverBefore)
			require.NoError(t, err, "independently read the complete exact provenance row, including columns outside ordinary send snapshots")
			require.Equal(t, int64(0), version, "the invalid fixture must not raise the parent protocol version or claim activation")
			require.Equal(t, "shadow", state, "this must be semantically invalid inactive provenance, not a valid opposing active mode")
			require.Equal(t, 1, minimumWriter, "the positive minimum writer is the strict inactive-provenance violation")
			baseline, beforeEffects := callerStampServiceSnapshot(t, f), callerStampEffectsSnapshot(effects)
			trace := callerObservationSQLTrace{}
			callbacks := 0
			runner := sendPostgresTransactionsFunc(func(ctx context.Context, work func(context.Context, pgx.Tx) error) error {
				return base.Run(ctx, func(unitCtx context.Context, tx pgx.Tx) error {
					callbacks++
					return work(unitCtx, callerObservationTracedTx{Tx: tx, trace: &trace})
				})
			})
			actual, err := ObserveCallerSend(context.Background(), runner, f.org, callerObservationOwnerRef(fixture.owner))
			require.NoError(t, err, "strict invalid provenance yields finite unresolved evidence, not a storage failure or fallback")
			require.Equal(t, CallerSendObservation{kind: CallerSendObservedUnresolved, reason: CallerSendProvenanceInvalid}, actual, "invalid provenance must expose no owner, message or timestamp even when positive historical linkage exists")
			require.Equal(t, 1, callbacks, "invalid provenance must not trigger another unit, fallback, or send")
			for _, query := range trace.statements {
				for _, ownerTable := range []string{"FROM code_review_agent_results", "FROM session_review_loops", "FROM thread_inbox_entries"} {
					require.NotContains(t, query, ownerTable, "strict provenance refusal must precede restoration or private owner/linkage disclosure")
				}
			}
			var cutoverAfter json.RawMessage
			require.NoError(t, f.pool.QueryRow(context.Background(), `SELECT to_jsonb(c) FROM session_lifecycle_cutovers c WHERE c.org_id=$1 AND c.session_id=$2`, f.org, f.session).Scan(&cutoverAfter), "read back the same exact retained cutover after the completed readonly observation")
			require.JSONEq(t, string(cutoverBefore), string(cutoverAfter), "the observer cannot normalize, repair, clear or alter any invalid provenance column")
			require.JSONEq(t, string(baseline), string(callerStampServiceSnapshot(t, f)), "strict provenance refusal must preserve the complete original stamped send and typed owner graph")
			require.Equal(t, beforeEffects, callerStampEffectsSnapshot(effects), "invalid provenance must not emit lifecycle, enqueue, claim, recovery or publication effects")
		})
	}
}

// Raw stamp INSERTs below deliberately construct isolated malformed-correlation
// fixtures. They are not a normal production Record path or permission to stamp
// historical sends; no immutable row is updated and no constraint is disabled.
func TestCallerSendObservationStampReviewNegativesPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		purpose      CallerSendPurpose
		stampFixture string
		reason       CallerSendObservationReason
	}{
		{name: "dispatch valid unstamped original linkage", purpose: CallerSendCodeReviewReviewerDispatch, reason: CallerSendLinkageUnresolved},
		{name: "startup valid unstamped original linkage", purpose: CallerSendReviewLoopInitialSend, reason: CallerSendLinkageUnresolved},
		{name: "dispatch legal exact stamp with zero Go timestamp", purpose: CallerSendCodeReviewReviewerDispatch, stampFixture: "zero time", reason: CallerSendStampMismatch},
		{name: "startup legal exact stamp with zero Go timestamp", purpose: CallerSendReviewLoopInitialSend, stampFixture: "zero time", reason: CallerSendStampMismatch},
		{name: "dispatch wrong digest insertion rejected", purpose: CallerSendCodeReviewReviewerDispatch, stampFixture: "wrong digest", reason: CallerSendLinkageUnresolved},
		{name: "startup wrong digest insertion rejected", purpose: CallerSendReviewLoopInitialSend, stampFixture: "wrong digest", reason: CallerSendLinkageUnresolved},
		{name: "dispatch wrong version insertion rejected", purpose: CallerSendCodeReviewReviewerDispatch, stampFixture: "wrong version", reason: CallerSendLinkageUnresolved},
		{name: "startup wrong version insertion rejected", purpose: CallerSendReviewLoopInitialSend, stampFixture: "wrong version", reason: CallerSendLinkageUnresolved},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newSendPostgresFixtureWithMaxConns(t, false, 1, 1)
			fixture := seedCallerStampServiceOwner(t, f, tt.purpose, false)
			service, effects := f.service(t, true)
			ordinary := cloneSendMessageInput(fixture.input)
			ordinary.CallerCommand = nil
			result, err := service.SendMessage(context.Background(), ordinary)
			require.NoError(t, err, "a real proof-free legacy send must establish valid original-key message and typed inbox linkage without private stamp creation")
			require.NotNil(t, result, "the original ordinary input must actually commit")
			require.NotNil(t, result.Message, "the fixture must retain the actual original positive message identity")
			require.NotNil(t, result.InboxEntry, "the proof-free original-key fixture must contain a real typed inbox entry")
			require.Equal(t, 0, callerStampCount(t, f), "proof-free legacy input must remain unstamped rather than laundering retained private identity")
			var linked struct {
				inboxMessageID, messageID int64
				key, role, entryType      string
			}
			err = f.pool.QueryRow(context.Background(), `SELECT i.message_id,m.id,i.client_message_id,m.role,i.entry_type
 FROM thread_inbox_entries i JOIN session_messages m ON m.org_id=i.org_id AND m.session_id=i.session_id AND m.thread_id=i.thread_id AND m.id=i.message_id
 WHERE i.org_id=$1 AND i.session_id=$2 AND i.thread_id=$3 AND i.client_message_id=$4`, f.org, f.session, fixture.owner.ThreadID, fixture.owner.ClientMessageID).
				Scan(&linked.inboxMessageID, &linked.messageID, &linked.key, &linked.role, &linked.entryType)
			require.NoError(t, err, "independently prove actual typed inbox/message linkage before relying on a negative stamp result")
			require.Equal(t, struct {
				inboxMessageID, messageID int64
				key, role, entryType      string
			}{result.Message.ID, result.Message.ID, fixture.owner.ClientMessageID, "user", "user_message"}, linked, "all typed message/inbox scopes, identity, key and kind must be correct; only private stamp evidence varies")
			expectedOwner := callerObservationExpectedOwner(t, f, service.send.Transactions, fixture.owner)
			require.Equal(t, db.CallerSendRetainedSupported, expectedOwner.Shape(), "retained command must be complete and supported, not an earlier malformed-owner refusal")
			original, err := restoreObservedCallerCommand(expectedOwner)
			require.NoError(t, err, "the exact retained original semantics must be valid before probing stamp-only negatives")
			require.Equal(t, callerCommandBytes(t, fixture.command), callerCommandBytes(t, original), "an ordinary historical message cannot change the original retained effective command")
			beforeInsert := callerStampServiceSnapshot(t, f)
			if tt.stampFixture != "" {
				version, digest := int16(1), fixture.command.Digest()
				if tt.stampFixture == "wrong version" {
					version = 2
				}
				if tt.stampFixture == "wrong digest" {
					digest = strings.Repeat("b", 64)
					require.NotEqual(t, fixture.command.Digest(), digest, "negative raw digest must actually differ from the retained original semantic digest")
				}
				err = db.RunLifecycleTransaction(context.Background(), f.pool, func(ctx context.Context, tx pgx.Tx) error {
					_, err := tx.Exec(ctx, `INSERT INTO caller_send_command_stamps(org_id,session_id,thread_id,client_message_id,purpose,owner_id,version,digest,message_id,created_at)
 VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,TIMESTAMPTZ '0001-01-01 00:00:00+00')`, f.org, f.session, fixture.owner.ThreadID, fixture.owner.ClientMessageID, db.CallerSendStampPurpose(tt.purpose), fixture.owner.OwnerID, version, digest, result.Message.ID)
					return err
				})
				if tt.stampFixture == "zero time" {
					require.NoError(t, err, "the exact owner-correlated year-one timestamp fixture must pass every installed normal trigger, FK and CHECK")
					var raw db.CallerSendStamp
					err = f.pool.QueryRow(context.Background(), `SELECT org_id,session_id,thread_id,client_message_id,purpose,owner_id,version,digest,message_id,created_at
 FROM caller_send_command_stamps WHERE org_id=$1 AND session_id=$2 AND thread_id=$3 AND client_message_id=$4 AND purpose=$5 AND owner_id=$6`, f.org, f.session, fixture.owner.ThreadID, fixture.owner.ClientMessageID, db.CallerSendStampPurpose(tt.purpose), fixture.owner.OwnerID).
						Scan(&raw.OrgID, &raw.Scope.SessionID, &raw.Scope.ThreadID, &raw.Scope.ClientMessageID, &raw.Scope.Purpose, &raw.Scope.OwnerID, &raw.Version, &raw.Digest, &raw.MessageID, &raw.CreatedAt)
					require.NoError(t, err, "read raw exact stamp fields directly because the normal stamp reader deliberately rejects zero timestamps")
					require.True(t, raw.CreatedAt.IsZero(), "actual PostgreSQL year-one UTC must reach the observer as the zero Go instant")
					raw.CreatedAt = raw.CreatedAt.UTC() // Compare the instant, not the driver's local-location representation.
					expected := db.CallerSendStamp{OrgID: f.org, CallerSendStampInput: db.CallerSendStampInput{Scope: callerStampDBScope(fixture.owner), Version: 1, Digest: fixture.command.Digest(), MessageID: result.Message.ID}, CreatedAt: time.Time{}}
					require.Equal(t, expected, raw, "every immutable stamp field other than the deliberately invalid semantic timestamp must match the actual original command linkage")
					require.Equal(t, 1, callerStampCount(t, f), "the mismatch branch must read a real installed stamp rather than merely encounter an inner-join miss")
				} else {
					var pgErr *pgconn.PgError
					require.ErrorAs(t, err, &pgErr, "forbidden immutable correlation must be rejected by the actual installed owner guard, not a manufactured error")
					require.Equal(t, "23503", pgErr.Code, "the BEFORE INSERT guard must refuse differing retained digest/version before any stamp CHECK can be treated as observer evidence")
					require.Equal(t, "caller_send_stamp_scope_mismatch", pgErr.Message, "pin the real explicit owner-correlation rejection rather than guessing a CHECK constraint")
					require.Equal(t, 0, callerStampCount(t, f), "a rejected corruption attempt must not install a contradictory immutable stamp")
					require.JSONEq(t, string(beforeInsert), string(callerStampServiceSnapshot(t, f)), "the actual failed raw fixture insert must roll back without modifying any original owner, message, inbox, job or stamp")
				}
			}
			baseline, beforeEffects := callerStampServiceSnapshot(t, f), callerStampEffectsSnapshot(effects)
			actual, err := ObserveCallerSend(context.Background(), service.send.Transactions, f.org, callerObservationOwnerRef(fixture.owner))
			require.NoError(t, err, "valid retained semantics with incomplete or mismatched stamp evidence remain finite unresolved observations")
			require.Equal(t, CallerSendObservedUnresolved, actual.Kind(), "a valid ordinary message or malformed stamp cannot itself become accepted private correlation")
			require.Equal(t, tt.reason, actual.Reason(), "installed zero-time stamp mismatch must remain distinct from no installed stamp after a guarded rejection")
			require.Equal(t, expectedOwner, actual.Owner(), "stamp-only negatives retain the exact original owner snapshot without reconstructing or settling it")
			id, accepted := actual.MessageID()
			require.Equal(t, int64(0), id, "unresolved stamp evidence cannot release the actual historical message as an accepted result")
			require.False(t, accepted, "no negative stamp observation confers abort, retry, fallback or mutation permission")
			require.True(t, actual.StampCreatedAt().IsZero(), "nonaccepted evidence must not expose a positive stamp binding")
			require.JSONEq(t, string(baseline), string(callerStampServiceSnapshot(t, f)), "the readonly observer cannot repair, stamp, resend, replace or settle any deliberately established fixture state")
			require.Equal(t, beforeEffects, callerStampEffectsSnapshot(effects), "stamp-negative observations must produce no additional claim, job, wake, provider or publication effects")
		})
	}
}

func TestCallerSendObservationParentCancellationPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		purpose     CallerSendPurpose
		afterCommit bool
	}{
		{name: "dispatch cancellation before actual settlement", purpose: CallerSendCodeReviewReviewerDispatch},
		{name: "startup cancellation before actual settlement", purpose: CallerSendReviewLoopInitialSend},
		{name: "dispatch cancellation after actual readonly commit", purpose: CallerSendCodeReviewReviewerDispatch, afterCommit: true},
		{name: "startup cancellation after actual readonly commit", purpose: CallerSendReviewLoopInitialSend, afterCommit: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newSendPostgresFixtureWithMaxConns(t, false, 1, 1)
			fixture := seedCallerStampServiceOwner(t, f, tt.purpose, false)
			service, effects := f.service(t, true)
			result, err := service.SendMessage(context.Background(), fixture.input)
			require.NoError(t, err, "retain a real whole committed stamped send before testing observer context fences")
			require.NotNil(t, result, "cancellation tests require an actual original durable positive command")
			base := service.send.Transactions
			owner := callerObservationExpectedOwner(t, f, base, fixture.owner)
			stamp := callerStampCommittedRead(t, f, fixture.owner)
			positive, err := ObserveCallerSend(context.Background(), base, f.org, callerObservationOwnerRef(fixture.owner))
			require.NoError(t, err, "positively establish exact original evidence immediately before the cancellation schedule")
			requireCallerObservationAccepted(t, positive, fixture, stamp, owner)
			baseline, beforeEffects := callerStampServiceSnapshot(t, f), callerStampEffectsSnapshot(effects)
			parent, cancel := context.WithCancel(context.Background())
			defer cancel()
			trace := callerObservationSQLTrace{}
			callbacks, positiveCallbacks := 0, 0
			canceledAfterCommit := false
			var settled error
			runner := sendPostgresTransactionsFunc(func(ctx context.Context, work func(context.Context, pgx.Tx) error) error {
				settled = base.Run(ctx, func(unitCtx context.Context, tx pgx.Tx) error {
					callbacks++
					if err := work(unitCtx, callerObservationTracedTx{Tx: tx, trace: &trace}); err != nil {
						return err
					}
					positiveCallbacks++
					if !tt.afterCommit {
						cancel()
					}
					return nil // No SQL follows cancellation inside the real unit.
				})
				if settled == nil && tt.afterCommit {
					canceledAfterCommit = true
					cancel()
				}
				return settled
			})
			actual, err := ObserveCallerSend(parent, runner, f.org, callerObservationOwnerRef(fixture.owner))
			require.ErrorIs(t, err, ErrCallerSendObservation, "both parent-cancellation fences must use the fixed safe observer error boundary")
			require.ErrorIs(t, err, context.Canceled, "the original caller cancellation cause must survive both precommit and postcommit fences")
			require.EqualError(t, err, "caller send observation failed", "cancellation must not expose private owner, message, prompt or driver details")
			require.Equal(t, CallerSendObservation{}, actual, "cancellation must discard the entire staged positive result including owner, message, digest, attempt, pass and timestamp")
			require.Equal(t, 1, callbacks, "parent cancellation must not trigger callback replay, a fallback unit or another send")
			require.Equal(t, 1, positiveCallbacks, "cancellation must occur after the actual observer callback completed successfully, not before source reads")
			require.ErrorIs(t, parent.Err(), context.Canceled, "the schedule must cancel the real parent rather than fabricate a context error")
			if tt.afterCommit {
				require.NoError(t, settled, "the actual existing runner must complete its readonly commit before parent cancellation")
				require.True(t, canceledAfterCommit, "postcommit cancellation must be gated on separately observed successful actual settlement")
			} else {
				require.ErrorIs(t, settled, context.Canceled, "the actual runner's precommit unit-context fence must reject and roll back after callback success")
				require.False(t, canceledAfterCommit, "precommit cancellation cannot be mislabeled as a completed readonly commit")
			}
			finalLinkReads := 0
			for _, query := range trace.statements {
				if strings.Contains(query, "JOIN caller_send_command_stamps c") && strings.Contains(query, "FROM thread_inbox_entries i") {
					finalLinkReads++
				}
			}
			require.Equal(t, 1, finalLinkReads, "the positive callback must actually reach the final typed joined linkage statement before cancellation")
			require.Equal(t, "on", trace.duringReadOnly, "the actual cancellation schedule must still establish readonly before its source reads")
			var nextReadOnly string
			require.NoError(t, f.pool.QueryRow(context.Background(), `SELECT current_setting('transaction_read_only')`).Scan(&nextReadOnly), "use a fresh independent context for the next one-connection pool borrower after cancellation")
			require.Equal(t, "off", nextReadOnly, "neither canceled rollback nor completed readonly commit may leak transaction-local readonly to the next borrower")
			require.JSONEq(t, string(baseline), string(callerStampServiceSnapshot(t, f)), "fresh-context readback must preserve the full original durable graph after either cancellation fence")
			require.Equal(t, beforeEffects, callerStampEffectsSnapshot(effects), "canceled observation cannot emit effects, resend, settle the owner or authorize recovery")
		})
	}
}
