package thread

import (
	"context"
	"errors"
	"testing"

	"github.com/assembledhq/143/internal/db"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

// The existing transparent seam injects an error after the service callback
// succeeds but before the actual production runner commits, causing a real
// whole rollback. Its ACK is intentionally ambiguous to the outer service.
// This is application-boundary injection, not a network/kernel fault proof.
func TestThreadSendLegacyCallerStampSimulatedRollbackUnknownPostgres(t *testing.T) {
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
			baseline := callerStampServiceSnapshot(t, f)
			service, effects := f.service(t, true)
			probe := &callerStampEnqueueProbe{LegacySendPersistence: service.send.Legacy, fixture: f, owner: fixture.owner}
			service.send.Legacy = probe
			ack := errors.New("synthetic ambiguous acknowledgement after provisional caller-stamped send")
			runner := &sendPostgresLegacyTransactions{base: service.send.Transactions, fixture: f, ambiguousAt: 2, commit: false, ack: ack}
			service.send.Transactions = runner
			result, err := service.SendMessage(context.Background(), fixture.input)
			var unknown *CommandOutcomeUnknownError
			require.ErrorAs(t, err, &unknown, "a provisional stamp cannot turn an ambiguous outer acknowledgement into known acceptance or abort proof")
			require.ErrorIs(t, err, ack, "unknown outcome must retain the original application-boundary fault")
			require.Nil(t, result, "unknown settlement must not expose the provisional successful send")
			require.Equal(t, fixture.owner.ClientMessageID, unknown.ClientMessageID, "unknown outcome must retain its original owner-derived correlation key")
			require.NotEqual(t, uuid.Nil, unknown.DiagnosticID, "uncertainty must retain the original diagnostic identity")
			require.Equal(t, db.LifecycleIngress{}, unknown.Ingress, "legacy uncertainty must not fabricate active receipt authority")
			requireSendPostgresLegacyUnitGuards(t, runner.units)
			require.ErrorIs(t, runner.units[1].settled, ack, "the test's actual whole mutation must roll back after callback success rather than commit its provisional stamp")
			for _, unit := range runner.units {
				require.Equal(t, unknown.DiagnosticID, unit.diagnostic, "entry and mutation must retain one original diagnostic without another send identity")
			}
			requireCallerStampProvisional(t, f, fixture, probe)
			require.JSONEq(t, string(baseline), string(callerStampServiceSnapshot(t, f)), "real whole rollback must preserve every original claim, message, inbox, job, owner, retained-start and stamp row")
			require.Equal(t, 0, callerStampCount(t, f), "the actual rolled-back stamp must not become durable or trigger a replacement send")
			requireSendPostgresNoEffects(t, effects)
			// No SendMessage follows the unknown result. A zero committed stamp
			// here is test-observed state, not production permission to resend.
		})
	}
}

func TestThreadSendLegacyCallerStampMissingInboxPostgres(t *testing.T) {
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
			require.NoError(t, ValidateCallerSendCommandProof(fixture.input), "the missing-inbox probe must carry a valid original complete proof rather than malformed input")
			baseline := callerStampServiceSnapshot(t, f)
			service, effects := f.service(t, true)
			service.SetThreadInboxStore(nil)
			var counts callerStampSQLCounts
			observeCallerStampServiceSQL(service, &counts)
			result, err := service.SendMessage(context.Background(), fixture.input)
			require.ErrorIs(t, err, ErrCallerCommandMismatch, "valid private correlation must refuse when no durable typed inbox is configured, never fall back to an unstamped send")
			require.NotErrorIs(t, err, ErrCommandOutcomeUnknown, "known missing configuration must not manufacture a send attempt or unknown outcome")
			require.Nil(t, result, "a known configuration refusal must return no provisional acceptance")
			require.Equal(t, callerStampSQLCounts{}, counts, "missing inbox configuration must refuse before any caller-stamp storage operation")
			require.JSONEq(t, string(baseline), string(callerStampServiceSnapshot(t, f)), "configuration refusal must preserve the complete original graph and typed owner identity")
			require.Equal(t, 0, callerStampCount(t, f), "missing inbox configuration cannot mint a durable stamp")
			requireSendPostgresNoEffects(t, effects)
		})
	}
}

func TestThreadSendCallerStampRequiresLegacyBeforeActiveReplayPostgres(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		purpose CallerSendPurpose
		prefix  string
	}{
		{name: "reviewer dispatch", purpose: CallerSendCodeReviewReviewerDispatch, prefix: "cr-dispatch:v1:"},
		{name: "initial loop send", purpose: CallerSendReviewLoopInitialSend, prefix: "rl-startup:v1:"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			f := newSendPostgresFixtureWithMaxConns(t, true, 1, 1)
			ownerID := uuid.New()
			input := f.input(0, tt.prefix+ownerID.String())
			command, err := NewCallerSendCommand(tt.purpose, ownerID, input)
			require.NoError(t, err, "construct a valid original caller descriptor without deriving identity from an active receipt")
			proof, err := command.Proof()
			require.NoError(t, err, "retain the original opaque descriptor before establishing the active replay")
			ordinary := command.SendInput()
			require.Nil(t, ordinary.CallerCommand, "the active positive fixture must carry exactly the same semantic input without private proof transport")
			service, effects := f.service(t, true)
			accepted, err := service.SendMessage(context.Background(), ordinary)
			require.NoError(t, err, "first establish actual proof-free active admission in the real full-schema fixture")
			require.NotNil(t, accepted, "active admission must return a real committed result")
			require.NotNil(t, accepted.Message, "the active positive fixture must retain an actual original message")
			require.NotNil(t, accepted.Admission, "the positive fixture must retain a real canonical active receipt")
			require.Equal(t, db.MessageAdmissionNewJob, accepted.Admission.Disposition(), "the original idle active send must own a real newly bound job, not a synthetic replay row")
			replayed, err := service.SendMessage(context.Background(), ordinary)
			require.NoError(t, err, "positively establish the exact active replay before testing the legacy-only proof refusal")
			require.NotNil(t, replayed, "the same semantic proof-free input must actually replay")
			require.NotNil(t, replayed.Admission, "the positive historical fixture must expose its real canonical replay receipt")
			require.Equal(t, db.MessageAdmissionReplayed, replayed.Admission.Disposition(), "the exact positive replay must already exist before private proof is attached")
			require.Equal(t, accepted.Message, replayed.Message, "the active positive replay must name the original complete message")
			baseline := callerStampServiceSnapshot(t, f)
			beforeEffects := callerStampEffectsSnapshot(effects)
			withProof := cloneSendMessageInput(ordinary)
			withProof.CallerCommand = proof
			require.NoError(t, ValidateCallerSendCommandProof(withProof), "the refusal must be the fixed active-mode boundary, not a malformed private proof")
			var counts callerStampSQLCounts
			observeCallerStampServiceSQL(service, &counts)
			result, err := service.SendMessage(context.Background(), withProof)
			require.ErrorIs(t, err, ErrCallerCommandRequiresLegacy, "a valid private caller proof must be refused before an already-positive exact active replay can return")
			require.NotErrorIs(t, err, ErrCommandOutcomeUnknown, "fixed legacy-only refusal cannot fabricate an attempted send or unknown settlement")
			require.Nil(t, result, "private proof transport cannot expose the existing active replay as caller acceptance")
			require.Equal(t, callerStampSQLCounts{}, counts, "the active refusal must not read, mint or repair private legacy stamp storage")
			require.JSONEq(t, string(baseline), string(callerStampServiceSnapshot(t, f)), "legacy-only refusal must leave every original active dispatch, claim, message, inbox, job and caller row unchanged")
			require.Equal(t, beforeEffects, callerStampEffectsSnapshot(effects), "legacy-only refusal must not repeat active lifecycle or job effects")
			require.Equal(t, 0, callerStampCount(t, f), "a refused active-mode proof cannot create private legacy correlation")
		})
	}
}
