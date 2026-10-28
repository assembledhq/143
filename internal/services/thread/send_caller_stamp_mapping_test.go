package thread

import (
	"math"
	"testing"

	"github.com/assembledhq/143/internal/db"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestCallerSendCommandStampNamespaceParity(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		purpose CallerSendPurpose
		valid   bool
	}{
		{name: "reviewer dispatch", purpose: CallerSendCodeReviewReviewerDispatch, valid: true},
		{name: "initial loop send", purpose: CallerSendReviewLoopInitialSend, valid: true},
		{name: "empty purpose"},
		{name: "key prefix is not a purpose", purpose: "cr-dispatch"},
		{name: "purpose is case sensitive", purpose: "Review-loop-initial-send"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			ownerID := uuid.New()
			command, commandErr := NewCallerSendCommand(tt.purpose, ownerID, SendMessageInput{
				OrgID: uuid.New(), SessionID: uuid.New(), ThreadID: uuid.New(), Message: "Synthetic namespace parity command",
			})
			purpose := db.CallerSendStampPurpose(tt.purpose)
			key, keyErr := db.CallerSendStampKey(purpose, ownerID)
			if !tt.valid {
				require.Error(t, commandErr, "the codec must reject aliases and unsupported namespaces before retaining a command")
				require.ErrorIs(t, purpose.Validate(), db.ErrCallerSendStampInvalid, "storage must reject the same unsupported namespace without alias normalization")
				require.ErrorIs(t, keyErr, db.ErrCallerSendStampInvalid, "an invalid storage namespace cannot derive a command key")
				require.Equal(t, "", key, "unsupported namespace must yield no correlation key")
				return
			}
			require.NoError(t, commandErr, "each supported codec namespace must retain a valid synthetic command")
			require.NoError(t, purpose.Validate(), "the independently defined storage namespace must accept each supported codec purpose")
			require.NoError(t, keyErr, "storage must derive a key for the same original nonzero owner")
			require.Equal(t, string(command.Purpose()), string(purpose), "codec and storage purposes must have identical descriptive values")
			require.Equal(t, command.ClientMessageID(), key, "codec and storage must derive the exact same prefix and owner key")
		})
	}
}

func TestLegacyCallerStampInputPreservesDescriptorAndRejectsOverflow(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		version int
		valid   bool
	}{
		{name: "supported descriptor", version: 1, valid: true},
		{name: "below storage range", version: math.MinInt16 - 1},
		{name: "above storage range", version: math.MaxInt16 + 1},
		{name: "overflow must not wrap to supported version", version: 1<<16 | 1},
		{name: "underflow must not wrap to supported version", version: -(1 << 16) + 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			command := mustCallerCommand(t, callerCommandInputFixture())
			input := command.SendInput()
			input.CallerCommand = callerCommandProofFixture(t, command)
			input.CallerCommand.version = tt.version
			stamp, err := legacyCallerStampInput(input, 42)
			if !tt.valid {
				require.ErrorIs(t, err, ErrCallerCommandMismatch, "a version outside the storage range must fail before narrowing to an accepted stamp")
				require.Equal(t, db.CallerSendStampInput{}, stamp, "a rejected descriptor must not yield partial correlation evidence")
				return
			}
			require.NoError(t, err, "the supported proof version must map without losing its original descriptor")
			expected := db.CallerSendStampInput{
				Scope: db.CallerSendStampScope{
					SessionID: input.SessionID, ThreadID: input.ThreadID, ClientMessageID: command.ClientMessageID(),
					Purpose: db.CallerSendStampPurpose(command.Purpose()), OwnerID: command.OwnerID(),
				},
				Version: 1, Digest: command.Digest(), MessageID: 42,
			}
			require.Equal(t, expected, stamp, "storage must retain the original exact version, identity, digest, scope and message")
		})
	}
}
