package thread

import (
	"context"
	"errors"
	"math"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/jackc/pgx/v5"
)

// A private caller proof requires durable typed inbox correlation; it cannot
// fall back to an unstamped send. This pure check runs before any legacy write.
// Proof-free calls retain the original path without additional SQL or policy.
func (s *Service) validateLegacyCallerStamp(input SendMessageInput) error {
	if input.CallerCommand == nil {
		return nil
	}
	if s.inboxStore == nil {
		return &CallerCommandMismatchError{}
	}
	return ValidateCallerSendCommandProof(input)
}

// The caller already checked the opaque proof against the complete effective
// input. This maps its immutable descriptor to storage without minting an
// identity, reading current owner status, or adding admission authority.
func legacyCallerStampInput(input SendMessageInput, messageID int64) (db.CallerSendStampInput, error) {
	proof := input.CallerCommand
	if proof == nil {
		return db.CallerSendStampInput{}, &CallerCommandMismatchError{}
	}
	version := proof.Version()
	if version < math.MinInt16 || version > math.MaxInt16 {
		return db.CallerSendStampInput{}, &CallerCommandMismatchError{}
	}
	return db.CallerSendStampInput{
		Scope: db.CallerSendStampScope{SessionID: input.SessionID, ThreadID: input.ThreadID,
			ClientMessageID: input.ClientMessageID, Purpose: db.CallerSendStampPurpose(proof.Purpose()), OwnerID: proof.OwnerID()},
		Version: int16(version), Digest: proof.Digest(), MessageID: messageID,
	}, nil
}

func legacyCallerMessageScopeMatches(input SendMessageInput, message *models.SessionMessage) bool {
	return message != nil && message.ID > 0 && message.OrgID == input.OrgID && message.SessionID == input.SessionID &&
		message.ThreadID != nil && *message.ThreadID == input.ThreadID
}

func legacyCallerInboxScopeMatches(input SendMessageInput, inbox *models.ThreadInboxEntry, messageID int64) bool {
	return inbox != nil && messageID > 0 && inbox.OrgID == input.OrgID && inbox.SessionID == input.SessionID &&
		inbox.ThreadID == input.ThreadID && inbox.MessageID == messageID &&
		inbox.ClientMessageID != nil && *inbox.ClientMessageID == input.ClientMessageID
}

// Only a newly written message/inbox pair in this same supplied legacy unit
// may acquire a stamp. The DB helper independently proves the actual typed
// rows. No commit, transaction, owner lock, retry, or postcommit write is added.
func recordLegacyCallerStampInTx(ctx context.Context, tx pgx.Tx, input SendMessageInput, message *models.SessionMessage, inbox *models.ThreadInboxEntry) error {
	if input.CallerCommand == nil {
		return nil
	}
	if !legacyCallerMessageScopeMatches(input, message) || !legacyCallerInboxScopeMatches(input, inbox, message.ID) {
		return &CallerCommandMismatchError{}
	}
	stamp, err := legacyCallerStampInput(input, message.ID)
	if err != nil {
		return err
	}
	_, err = db.RecordCallerSendStampInTx(ctx, input.OrgID, tx, stamp)
	return err
}

// A preexisting message can only read and compare original retained evidence.
// Missing or unequal stamps are a known refusal, never permission to derive a
// new stamp from today's input or to resend. A match is correlation, not proof
// of caller acceptance, execution, or an aborted historical transaction.
func verifyLegacyCallerStampReplayInTx(ctx context.Context, tx pgx.Tx, input SendMessageInput, message *models.SessionMessage) error {
	if input.CallerCommand == nil {
		return nil
	}
	if !legacyCallerMessageScopeMatches(input, message) {
		return &CallerCommandMismatchError{}
	}
	expected, err := legacyCallerStampInput(input, message.ID)
	if err != nil {
		return err
	}
	stamp, found, err := db.ReadCallerSendStampInTx(ctx, input.OrgID, tx, expected.Scope)
	if err != nil {
		if errors.Is(err, db.ErrCallerSendStampMismatch) || errors.Is(err, db.ErrCallerSendStampInvalid) {
			return &CallerCommandMismatchError{}
		}
		return err
	}
	if !found || stamp.OrgID != input.OrgID || stamp.CallerSendStampInput != expected {
		return &CallerCommandMismatchError{}
	}
	return nil
}
