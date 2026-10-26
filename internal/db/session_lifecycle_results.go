package db

import (
	"errors"
	"fmt"
	"slices"

	"github.com/google/uuid"
)

// LifecycleEffectKind identifies best-effort notifications to apply only after a
// known outer commit. Guaranteed delivery uses a durable job in that transaction,
// not a callback stored in a result or an effect executed by a DB primitive.
type LifecycleEffectKind string

const (
	LifecycleEffectWakeJobs       LifecycleEffectKind = "wake_jobs"
	LifecycleEffectSessionChanged LifecycleEffectKind = "session_changed"
	LifecycleEffectThreadChanged  LifecycleEffectKind = "thread_changed"
	LifecycleEffectInboxChanged   LifecycleEffectKind = "inbox_changed"
)

func (v LifecycleEffectKind) Validate() error {
	switch v {
	case LifecycleEffectWakeJobs, LifecycleEffectSessionChanged, LifecycleEffectThreadChanged, LifecycleEffectInboxChanged:
		return nil
	default:
		return fmt.Errorf("invalid LifecycleEffectKind: %q", v)
	}
}

// LifecycleEffect is immutable notification data, not executable code. A result
// owner discards every effect after rollback or unknown commit.
type LifecycleEffect struct {
	kind                       LifecycleEffectKind
	orgID, sessionID, threadID uuid.UUID
}

func newLifecycleEffect(kind LifecycleEffectKind, orgID, sessionID uuid.UUID, threadID *uuid.UUID) (LifecycleEffect, error) {
	v := LifecycleEffect{kind: kind, orgID: orgID, sessionID: sessionID}
	if threadID != nil {
		if *threadID == uuid.Nil {
			return LifecycleEffect{}, errors.New("lifecycle notification cannot infer a lane from zero")
		}
		v.threadID = *threadID
	}
	if err := v.Validate(); err != nil {
		return LifecycleEffect{}, err
	}
	return v, nil
}

func (v LifecycleEffect) Validate() error {
	if err := v.kind.Validate(); err != nil {
		return err
	}
	if v.orgID == uuid.Nil || v.sessionID == uuid.Nil {
		return errors.New("lifecycle notification requires exact tenant and session")
	}
	if (v.kind == LifecycleEffectThreadChanged || v.kind == LifecycleEffectInboxChanged) && v.threadID == uuid.Nil {
		return errors.New("lane notification requires its explicit lane")
	}
	return nil
}

func (v LifecycleEffect) Kind() LifecycleEffectKind   { return v.kind }
func (v LifecycleEffect) OrgID() uuid.UUID            { return v.orgID }
func (v LifecycleEffect) SessionID() uuid.UUID        { return v.sessionID }
func (v LifecycleEffect) ThreadID() (uuid.UUID, bool) { return v.threadID, v.threadID != uuid.Nil }

// lifecycleReceipt is sealed so adapters cannot construct their own successful
// result. Each concrete receipt must validate its entire immutable payload.
type lifecycleReceipt interface {
	validateLifecycleReceipt() error
	lifecycleReceiptScope() (uuid.UUID, AdmissionFence)
}

// AdmissionResult has no stale-success branch. Only canonical DB mutations can
// construct it, after performing their durable authorization and writes. A
// default result, nil receipt or failed construction is never accepted work.
type AdmissionResult[T lifecycleReceipt] struct {
	receipt T
	effects []LifecycleEffect
	valid   bool
}

func newAdmissionResult[T lifecycleReceipt](receipt T, effects []LifecycleEffect) (AdmissionResult[T], error) {
	if err := validateLifecycleResult(receipt, effects); err != nil {
		return AdmissionResult[T]{}, err
	}
	return AdmissionResult[T]{receipt: receipt, effects: slices.Clone(effects), valid: true}, nil
}

func (v AdmissionResult[T]) Unpack() (T, []LifecycleEffect, error) {
	if !v.valid {
		var zero T
		return zero, nil, errors.New("invalid lifecycle admission result")
	}
	if err := validateLifecycleResult(v.receipt, v.effects); err != nil {
		var zero T
		return zero, nil, err
	}
	return v.receipt, slices.Clone(v.effects), nil
}

// SettlementResult can report stale shared projection only after valid own-
// receipt settlement. It is not interchangeable with AdmissionResult.
type SettlementResult[T lifecycleReceipt] struct {
	ownReceipt T
	projection ProjectionDisposition
	effects    []LifecycleEffect
	valid      bool
}

func newSettlementResult[T lifecycleReceipt](receipt T, projection ProjectionDisposition, effects []LifecycleEffect) (SettlementResult[T], error) {
	if err := projection.Validate(); err != nil {
		return SettlementResult[T]{}, err
	}
	if err := validateLifecycleResult(receipt, effects); err != nil {
		return SettlementResult[T]{}, err
	}
	return SettlementResult[T]{ownReceipt: receipt, projection: projection, effects: slices.Clone(effects), valid: true}, nil
}

func (v SettlementResult[T]) Unpack() (T, ProjectionDisposition, []LifecycleEffect, error) {
	var zero T
	if !v.valid {
		return zero, "", nil, errors.New("invalid lifecycle settlement result")
	}
	if err := v.projection.Validate(); err != nil {
		return zero, "", nil, err
	}
	if err := validateLifecycleResult(v.ownReceipt, v.effects); err != nil {
		return zero, "", nil, err
	}
	return v.ownReceipt, v.projection, slices.Clone(v.effects), nil
}

func validateLifecycleResult[T lifecycleReceipt](receipt T, effects []LifecycleEffect) error {
	if nilLifecycleDependency(receipt) {
		return errors.New("invalid lifecycle result: nil receipt")
	}
	if err := receipt.validateLifecycleReceipt(); err != nil {
		return fmt.Errorf("invalid lifecycle receipt: %w", err)
	}
	orgID, fence := receipt.lifecycleReceiptScope()
	if orgID == uuid.Nil {
		return errors.New("invalid lifecycle receipt: missing organization")
	}
	if err := fence.Validate(); err != nil {
		return fmt.Errorf("invalid lifecycle receipt scope: %w", err)
	}
	for _, effect := range effects {
		if err := effect.Validate(); err != nil {
			return fmt.Errorf("invalid lifecycle result effect: %w", err)
		}
		if effect.orgID != orgID || effect.sessionID != fence.SessionID() {
			return errors.New("lifecycle result effect differs from its receipt tenant/session")
		}
	}
	return nil
}
