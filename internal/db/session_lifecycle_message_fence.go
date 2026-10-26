package db

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// ErrMessageAdmissionConflict never means accepted work. The transaction owner
// must confirm rollback before exposing a stale-command conflict to its caller.
var ErrMessageAdmissionConflict = errors.New("message admission conflicts with its retained command")

// CaptureThreadMessageFenceInTx observes the exact active target's epochs for a
// NEW command only after its owner has checked exact-key replay. It performs no
// mutation or reservation and proves no actor, status, capacity or availability.
// This domain observation uses row locks; it is not a PostgreSQL READ ONLY tx.
//
// The top-level owner must retain the value only after known successful unit
// completion, then freeze it outside the mutating retry loop. Admission must
// validate that frozen expectation on every attempt, never replace it with this
// observation after a stale result. Existing receipts take precedence over this
// capture and all current eligibility checks.
func CaptureThreadMessageFenceInTx(ctx context.Context, tx pgx.Tx, orgID, sessionID, threadID uuid.UUID) (AdmissionFence, error) {
	scope, err := lockActiveThreadMessageScopeInTx(ctx, tx, orgID, sessionID, threadID)
	if err != nil {
		return AdmissionFence{}, err
	}
	return threadMessageFenceFromScope(scope, threadID)
}

func lockActiveThreadMessageScopeInTx(ctx context.Context, tx pgx.Tx, orgID, sessionID, threadID uuid.UUID) (lockedLifecycleScope, error) {
	if err := validateThreadSendScope(ctx, tx, orgID, sessionID, threadID); err != nil {
		return lockedLifecycleScope{}, err
	}
	if err := RequireActiveThreadSendModeInTx(ctx, tx, orgID, sessionID); err != nil {
		return lockedLifecycleScope{}, err
	}
	scope, err := lockThreadSendScopeInTx(ctx, tx, orgID, sessionID, threadID)
	if err != nil {
		return lockedLifecycleScope{}, err
	}
	if scope.session.deletedAt != nil || scope.session.archivedAt != nil {
		return lockedLifecycleScope{}, fmt.Errorf("message admission parent is hidden: %w", pgx.ErrNoRows)
	}
	if scope.session.epochs.LifecycleVersion != 1 {
		return lockedLifecycleScope{}, &LifecycleGuardProvenanceError{reason: "message admission parent protocol changed during observation"}
	}
	for _, lane := range scope.threads {
		if lane.id == threadID && lane.archivedAt != nil {
			return lockedLifecycleScope{}, fmt.Errorf("message admission exact lane is hidden: %w", pgx.ErrNoRows)
		}
		if lane.archivedAt == nil && lane.epochs.LifecycleVersion != 1 {
			return lockedLifecycleScope{}, &LifecycleGuardProvenanceError{reason: "message admission visible lane protocol changed during observation"}
		}
	}
	return scope, nil
}

func threadMessageFenceFromScope(scope lockedLifecycleScope, threadID uuid.UUID) (AdmissionFence, error) {
	for _, lane := range scope.threads {
		if lane.id == threadID {
			return NewAdmissionFence(scope.session.id, &threadID, scope.session.epochs.AdmissionEpoch, &lane.epochs.AdmissionEpoch)
		}
	}
	return AdmissionFence{}, fmt.Errorf("message admission explicit lane absent: %w", pgx.ErrNoRows)
}

// verifyThreadMessageFence compares only a locked current observation against
// the command's already frozen scope. It never updates or reinterprets expected
// epochs. Replay lookup must precede it; a receipt is not a new admission.
func verifyThreadMessageFence(scope lockedLifecycleScope, expected AdmissionFence) error {
	if err := expected.Validate(); err != nil {
		return err
	}
	threadID, threaded := expected.ThreadID()
	if !threaded {
		return errors.New("message admission requires its exact lane expectation")
	}
	current, err := threadMessageFenceFromScope(scope, threadID)
	if err != nil {
		return err
	}
	if current != expected {
		return ErrMessageAdmissionConflict
	}
	return nil
}
