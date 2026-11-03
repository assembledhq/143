package thread

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

// Keep mock-only nullable pointer adaptation separate from the original
// scalar command fixture and every negative mutation. Real pgx already
// handles these nullable **T destinations without this representation shim.
func callerObservationNullableMockValues(row []any) []any {
	values := append([]any(nil), row...)
	for _, index := range []int{3, 4, 5, 6, 7, 10, 11, 12, 13, 14, 15} {
		switch value := values[index].(type) {
		case nil:
			continue
		case int16:
			values[index] = &value
		case int32:
			values[index] = &value
		case string:
			values[index] = &value
		case uuid.UUID:
			values[index] = &value
		default:
			panic("unsupported nullable caller observation mock value")
		}
	}
	return values
}

// Construct the sealed DB observation through its real supplied-transaction
// reader. Only this test fixture creates an original command; the production
// restoration path must decode its retained bytes and independent typed scope.
func retainedCallerObservationFixture(t *testing.T, command CallerSendCommand, change func([]any)) db.RetainedCallerSend {
	t.Helper()
	owner := callerCommandOwner(command)
	ref := db.CallerSendOwnerRef{Purpose: db.CallerSendStampPurpose(owner.Purpose), OwnerID: owner.OwnerID, SessionID: owner.SessionID}
	raw := callerCommandBytes(t, command)
	var ordinaryThread, pass any
	if owner.Purpose == CallerSendReviewLoopInitialSend {
		ordinaryThread, pass = owner.ThreadID, uuid.New()
	}
	row := []any{owner.OwnerID, owner.OrgID, owner.SessionID, int16(1), owner.ClientMessageID, command.Digest(), owner.ThreadID,
		string(raw), len(raw), 11, "pending", nil, int32(0), int32(0), ordinaryThread, pass, true, true}
	if change != nil {
		change(row)
	}
	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "create an isolated scoped DB observation fixture")
	t.Cleanup(mock.Close)
	table := "code_review_agent_results"
	if owner.Purpose == CallerSendReviewLoopInitialSend {
		table = "session_review_loops"
	}
	mock.ExpectQuery(`(?s)SELECT id,org_id,session_id,.* FROM `+table+` WHERE org_id=\$1 AND session_id=\$2 AND id=\$3`).
		WithArgs(owner.OrgID, owner.SessionID, owner.OwnerID, 8<<20).
		WillReturnRows(pgxmock.NewRows([]string{"id", "org_id", "session_id", "version", "key", "digest", "thread_id", "raw", "raw_bytes", "nonnull", "state", "attempt_id", "attempt_seq", "writer_seq", "ordinary_thread", "pass_id", "kind_matches", "state_valid"}).AddRow(callerObservationNullableMockValues(row)...))
	retained, found, err := db.ReadRetainedCallerSendInTx(context.Background(), owner.OrgID, mock, ref)
	require.NoError(t, err, "the pure restoration fixture must enter through the actual bounded typed DB reader")
	require.True(t, found, "the original typed owner should exist in the fixture")
	require.NoError(t, mock.ExpectationsWereMet(), "retained fixture creation must use no mutation, owner locks, or transaction settlement")
	return retained
}

func TestRestoreObservedCallerCommand(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		purpose CallerSendPurpose
	}{
		{name: "reviewer dispatch complete original options", purpose: CallerSendCodeReviewReviewerDispatch},
		{name: "loop startup complete original options", purpose: CallerSendReviewLoopInitialSend},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			original, err := NewCallerSendCommand(tt.purpose, uuid.New(), callerCommandInputFixture())
			require.NoError(t, err, "retain a complete original command before any observation")
			owner := retainedCallerObservationFixture(t, original, nil)
			actual, err := restoreObservedCallerCommand(owner)
			require.NoError(t, err, "restore only the retained command under independently supplied typed owner correlation")
			require.Equal(t, callerCommandBytes(t, original), callerCommandBytes(t, actual), "restoration must keep exact canonical bytes without re-rendering text or resolving a new primary lane")
			require.Equal(t, original.Digest(), actual.Digest(), "restoration must verify the original domain-separated digest instead of accepting a replacement digest")
			require.Equal(t, original.SendInput(), actual.SendInput(), "restoration must retain every original effective option, pointer, and normalized collection")
			originalProof, err := original.Proof()
			require.NoError(t, err, "retain the original opaque nonsemantic proof")
			actualProof, err := actual.Proof()
			require.NoError(t, err, "the decoded original descriptor must remain a valid immutable correlation")
			require.Equal(t, originalProof, actualProof, "observation cannot mint another owner, key, or proof identity")
		})
	}
}

func TestRestoreObservedCallerCommandRejectsNonOriginalEvidence(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		change func([]any)
	}{
		{name: "persisted version distinct from envelope", change: func(row []any) { row[3], row[7], row[8], row[17] = int16(2), nil, 0, false }},
		{name: "unknown envelope with supported persisted version", change: func(row []any) {
			raw := strings.Replace(row[7].(string), `"version":1`, `"version":2`, 1)
			row[7], row[8] = raw, len(raw)
		}},
		{name: "malformed retained JSON", change: func(row []any) { row[7], row[8] = `{`, 1 }},
		{name: "retained digest does not match original semantics", change: func(row []any) { row[5] = strings.Repeat("b", 64) }},
		{name: "independently retained lane differs from JSON", change: func(row []any) { lane := uuid.New(); row[6], row[14] = lane, lane }},
		{name: "partial NULL version is not legacy", change: func(row []any) { row[3] = nil }},
		{name: "all intent NULL legacy owner", change: func(row []any) {
			for i := 3; i <= 15; i++ {
				row[i] = nil
			}
			row[8], row[9], row[17] = 0, 0, false
		}},
		{name: "raw length bound omitted payload", change: func(row []any) { row[7], row[8] = nil, (8<<20)+1 }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			original, err := NewCallerSendCommand(CallerSendReviewLoopInitialSend, uuid.New(), callerCommandInputFixture())
			require.NoError(t, err, "negative restoration must start with a complete valid original command")
			owner := retainedCallerObservationFixture(t, original, tt.change)
			actual, err := restoreObservedCallerCommand(owner)
			require.Error(t, err, "unsupported, corrupt or semantically different retained evidence cannot be restored as the original command")
			require.Equal(t, CallerSendCommand{}, actual, "failed restoration cannot expose a partial command or create another identity")
		})
	}
}

func TestCallerSendObservationSealedResult(t *testing.T) {
	t.Parallel()
	original, err := NewCallerSendCommand(CallerSendCodeReviewReviewerDispatch, uuid.New(), callerCommandInputFixture())
	require.NoError(t, err, "prepare an original immutable descriptor for result encapsulation")
	owner := retainedCallerObservationFixture(t, original, nil)
	stampTime := time.Date(2026, 1, 2, 3, 4, 5, 123000000, time.UTC)
	tests := []struct {
		name              string
		result            CallerSendObservation
		valid             bool
		messageID         int64
		accepted          bool
		expectedOwner     db.RetainedCallerSend
		expectedStampTime time.Time
	}{
		{name: "zero invalid"},
		{name: "unresolved is not an abort receipt", result: CallerSendObservation{kind: CallerSendObservedUnresolved, reason: CallerSendLinkageUnresolved, owner: owner}, valid: true, expectedOwner: owner},
		{name: "unavailable is not retry permission", result: CallerSendObservation{kind: CallerSendObservedUnavailable, reason: CallerSendScopeUnavailable, owner: owner}, valid: true, expectedOwner: owner},
		{name: "positive immutable correlation only", result: CallerSendObservation{kind: CallerSendObservedAccepted, messageID: 19, owner: owner, stampCreatedAt: stampTime}, valid: true, messageID: 19, accepted: true, expectedOwner: owner, expectedStampTime: stampTime},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			raw, err := json.Marshal(tt.result) //nolint:staticcheck // Intentionally verify sealed observations disclose no retained result fields.
			require.NoError(t, err, "private observation results must marshal without exposing retained fields")
			require.Equal(t, `{}`, string(raw), "owner identity, prompt, digest, attempt and accepted message must not leak into public response JSON")
			require.Equal(t, tt.valid, tt.result.Valid(), "a zero or failed observation cannot look like a successfully settled result")
			id, accepted := tt.result.MessageID()
			require.Equal(t, tt.messageID, id, "only a positive observation retains an original message identity")
			require.Equal(t, tt.accepted, accepted, "unresolved and unavailable observations must not expose accepted-message permission")
			require.Equal(t, tt.expectedOwner, tt.result.Owner(), "sealed results must retain the exact observed owner, attempt, lane, key, version, digest and pass binding rather than a bare message id")
			require.Equal(t, tt.expectedStampTime, tt.result.StampCreatedAt(), "only Accepted retains the exact original stamp timestamp; unresolved and invalid results have none")
		})
	}
}

func TestCallerSendObservationErrorBoundary(t *testing.T) {
	t.Parallel()
	cause := errors.New("synthetic private observer failure")
	wrapped := &callerSendObservationError{cause: cause}
	require.EqualError(t, wrapped, "caller send observation failed", "fixed error text must not disclose owner, prompt or transport content")
	require.ErrorIs(t, wrapped, ErrCallerSendObservation, "the safe observation boundary remains identifiable")
	require.ErrorIs(t, wrapped, cause, "the private cause remains available for internal diagnosis")
	actual, err := ObserveCallerSend(context.Background(), nil, uuid.New(), db.CallerSendOwnerRef{Purpose: db.CallerSendStampCodeReviewDispatch, OwnerID: uuid.New(), SessionID: uuid.New()})
	require.ErrorIs(t, err, db.ErrCallerSendObservationInvalid, "missing transaction capability must fail closed without a fallback runner")
	require.Equal(t, CallerSendObservation{}, actual, "dependency refusal exposes no staged observation evidence")
}
