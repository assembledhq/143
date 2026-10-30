package thread

import (
	"context"
	"errors"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type CallerSendObservationKind string

const (
	CallerSendObservedAccepted    CallerSendObservationKind = "accepted"
	CallerSendObservedUnresolved  CallerSendObservationKind = "unresolved"
	CallerSendObservedUnavailable CallerSendObservationKind = "unavailable"
)

type CallerSendObservationReason string

const (
	CallerSendOwnerMissing       CallerSendObservationReason = "missing_owner"
	CallerSendOwnerLegacy        CallerSendObservationReason = "legacy_owner"
	CallerSendVersionUnsupported CallerSendObservationReason = "unsupported_version"
	CallerSendCommandMalformed   CallerSendObservationReason = "malformed_retained_command"
	CallerSendModeChanged        CallerSendObservationReason = "mode_changed"
	CallerSendProvenanceInvalid  CallerSendObservationReason = "provenance_invalid"
	CallerSendScopeUnavailable   CallerSendObservationReason = "session_or_lane_unavailable"
	CallerSendLinkageUnresolved  CallerSendObservationReason = "linkage_unresolved"
	CallerSendStampMismatch      CallerSendObservationReason = "stamp_mismatch"
)

// CallerSendObservation is sealed private correlation, not a public response,
// active Admission, delivery/execution receipt, or permission to mutate. Zero
// is invalid. Observed owner/attempt metadata is only a later CAS expectation.
type CallerSendObservation struct {
	kind           CallerSendObservationKind
	reason         CallerSendObservationReason
	messageID      int64
	stampCreatedAt time.Time
	owner          db.RetainedCallerSend
}

func (o CallerSendObservation) Kind() CallerSendObservationKind     { return o.kind }
func (o CallerSendObservation) Reason() CallerSendObservationReason { return o.reason }
func (o CallerSendObservation) Valid() bool                         { return o.kind != "" }
func (o CallerSendObservation) MessageID() (int64, bool) {
	return o.messageID, o.kind == CallerSendObservedAccepted && o.messageID > 0
}
func (o CallerSendObservation) Owner() db.RetainedCallerSend { return o.owner }
func (o CallerSendObservation) StampCreatedAt() time.Time    { return o.stampCreatedAt }

var ErrCallerSendObservation = errors.New("caller send observation failed")

type callerSendObservationError struct{ cause error }

func (*callerSendObservationError) Error() string        { return ErrCallerSendObservation.Error() }
func (e *callerSendObservationError) Unwrap() error      { return e.cause }
func (*callerSendObservationError) Is(target error) bool { return target == ErrCallerSendObservation }

// restoreObservedCallerCommand only restores retained original semantics. It
// never constructs a new command, resolves current targets, or re-renders text.
func restoreObservedCallerCommand(owner db.RetainedCallerSend) (CallerSendCommand, error) {
	if owner.Shape() != db.CallerSendRetainedSupported || owner.Version() != 1 {
		return CallerSendCommand{}, ErrCallerCommandMismatch
	}
	if _, err := db.ParseLifecycleDigest(owner.Digest()); err != nil {
		return CallerSendCommand{}, ErrCallerCommandMismatch
	}
	ref := owner.Ref()
	return DecodeCallerSendCommand(owner.Raw(), owner.Digest(), CallerSendOwnerScope{Purpose: CallerSendPurpose(ref.Purpose), OwnerID: ref.OwnerID,
		OrgID: owner.OrgID(), SessionID: ref.SessionID, ThreadID: owner.ThreadID(), ClientMessageID: owner.Key()})
}

// ObserveCallerSend uses the existing transaction capability and its unchanged
// budget. READ ONLY precedes every source-data read. An error invalidates all
// staged evidence, including a positive candidate followed by commit/ACK loss.
// Missing evidence is never abort proof or permission to send, retry or fall back.
func ObserveCallerSend(ctx context.Context, transactions SendTransactions, orgID uuid.UUID, ref db.CallerSendOwnerRef) (CallerSendObservation, error) {
	if ctx == nil || nilSendDependency(transactions) || orgID == uuid.Nil || ref.Validate() != nil {
		return CallerSendObservation{}, &callerSendObservationError{cause: db.ErrCallerSendObservationInvalid}
	}
	var candidate CallerSendObservation
	err := transactions.Run(ctx, func(unitCtx context.Context, tx pgx.Tx) error {
		candidate = CallerSendObservation{}
		if _, err := tx.Exec(unitCtx, "SET TRANSACTION READ ONLY"); err != nil {
			return err
		}
		mode, err := db.ObserveThreadSendModeInTx(unitCtx, tx, orgID, ref.SessionID)
		if err != nil {
			if errors.Is(err, pgx.ErrNoRows) {
				candidate = CallerSendObservation{kind: CallerSendObservedUnavailable, reason: CallerSendScopeUnavailable}
				return nil
			}
			var provenance *db.LifecycleGuardProvenanceError
			if errors.As(err, &provenance) {
				candidate = CallerSendObservation{kind: CallerSendObservedUnresolved, reason: CallerSendProvenanceInvalid}
				return nil
			}
			return err
		}
		if mode != db.ThreadSendModeLegacy {
			candidate = CallerSendObservation{kind: CallerSendObservedUnresolved, reason: CallerSendModeChanged}
			return nil
		}
		owner, found, err := db.ReadRetainedCallerSendInTx(unitCtx, orgID, tx, ref)
		if err != nil {
			return err
		}
		if !found {
			candidate = CallerSendObservation{kind: CallerSendObservedUnresolved, reason: CallerSendOwnerMissing}
			return nil
		}
		candidate = CallerSendObservation{kind: CallerSendObservedUnresolved, owner: owner}
		switch owner.Shape() {
		case db.CallerSendRetainedLegacy:
			candidate.reason = CallerSendOwnerLegacy
			return nil
		case db.CallerSendRetainedUnsupported:
			candidate.reason = CallerSendVersionUnsupported
			return nil
		case db.CallerSendRetainedSupported:
		default:
			candidate.reason = CallerSendCommandMalformed
			return nil
		}
		command, err := restoreObservedCallerCommand(owner)
		if err != nil {
			candidate.reason = CallerSendCommandMalformed
			return nil
		}
		visible, err := db.ReadCallerSendVisibilityInTx(unitCtx, orgID, tx, ref.SessionID, owner.ThreadID())
		if err != nil {
			return err
		}
		if !visible {
			candidate.kind = CallerSendObservedUnavailable
			candidate.reason = CallerSendScopeUnavailable
			return nil
		}
		scope := db.CallerSendStampScope{SessionID: ref.SessionID, ThreadID: owner.ThreadID(), ClientMessageID: owner.Key(), Purpose: ref.Purpose, OwnerID: ref.OwnerID}
		link, found, err := db.ReadCallerSendLinkageInTx(unitCtx, orgID, tx, scope)
		if err != nil {
			return err
		}
		if !found {
			candidate.reason = CallerSendLinkageUnresolved
			return nil
		}
		if link.MessageID() <= 0 || link.Version() != owner.Version() || link.Digest() != command.Digest() || link.CreatedAt().IsZero() {
			candidate.reason = CallerSendStampMismatch
			return nil
		}
		candidate.kind = CallerSendObservedAccepted
		candidate.reason = ""
		candidate.messageID = link.MessageID()
		candidate.stampCreatedAt = link.CreatedAt()
		return unitCtx.Err()
	})
	if err != nil {
		return CallerSendObservation{}, &callerSendObservationError{cause: err}
	}
	if err := ctx.Err(); err != nil {
		return CallerSendObservation{}, &callerSendObservationError{cause: err}
	}
	return candidate, nil
}
