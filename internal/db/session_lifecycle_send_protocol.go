package db

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// ThreadSendMode is a finite mode observation, never actor, epoch, row-lock,
// availability, admission or execution authority. Validate checks shape only.
type ThreadSendMode string

const (
	ThreadSendModeLegacy      ThreadSendMode = "legacy"
	ThreadSendModeNormalizing ThreadSendMode = "normalizing"
	ThreadSendModeActive      ThreadSendMode = "active"
)

func (m ThreadSendMode) Validate() error {
	switch m {
	case ThreadSendModeLegacy, ThreadSendModeNormalizing, ThreadSendModeActive:
		return nil
	default:
		return fmt.Errorf("invalid thread send mode: %q", m)
	}
}

// ErrThreadSendModeChanged means a valid fresh observation differs from the
// caller's fixed branch. It is not permission to fall back to another branch.
var ErrThreadSendModeChanged = errors.New("thread send mode changed")

// ObserveThreadSendModeInTx delegates only to the G.1 provenance resolver and
// maps its finite modes. The supplied transaction must already have the exact
// bounded READ COMMITTED policy. Its owner must discard observations and roll
// back the whole unit on error. The resolver's shared cutover interlock is not
// canonical parent/lane row locking, and this API supplies no mutation permit.
func ObserveThreadSendModeInTx(ctx context.Context, tx pgx.Tx, orgID, sessionID uuid.UUID) (ThreadSendMode, error) {
	observation, err := resolveLifecycleModeInTx(ctx, tx, orgID, sessionID)
	if err != nil {
		return "", err
	}
	switch observation.mode {
	case lifecycleModeLegacy:
		return ThreadSendModeLegacy, nil
	case lifecycleModeNormalizing:
		return ThreadSendModeNormalizing, nil
	case lifecycleModeActive:
		return ThreadSendModeActive, nil
	default:
		return "", &LifecycleGuardProvenanceError{reason: "unsupported resolved thread send mode"}
	}
}

// RequireLegacyThreadSendModeInTx re-observes mode through the same resolver.
// A different valid mode is a branch change, never a legacy fallback. Success
// proves only mode; actor, exact epochs and canonical row locks remain required.
func RequireLegacyThreadSendModeInTx(ctx context.Context, tx pgx.Tx, orgID, sessionID uuid.UUID) error {
	mode, err := ObserveThreadSendModeInTx(ctx, tx, orgID, sessionID)
	if err != nil {
		return err
	}
	if mode != ThreadSendModeLegacy {
		return ErrThreadSendModeChanged
	}
	return nil
}

// RequireActiveThreadSendModeInTx is the fixed active-branch mode check. It
// adds no actor, epoch, row-lock, availability, admission or provider authority
// and does not retry, change settings, settle tx or route a different branch.
func RequireActiveThreadSendModeInTx(ctx context.Context, tx pgx.Tx, orgID, sessionID uuid.UUID) error {
	mode, err := ObserveThreadSendModeInTx(ctx, tx, orgID, sessionID)
	if err != nil {
		return err
	}
	if mode != ThreadSendModeActive {
		return ErrThreadSendModeChanged
	}
	return nil
}
