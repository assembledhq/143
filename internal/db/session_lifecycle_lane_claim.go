package db

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const (
	lifecycleLaneClaimQuery = `SELECT id, org_id, session_id, current_turn,
		admission_claim_dispatch_id, admission_claim_token FROM session_threads
		WHERE org_id = $1 AND session_id = $2 AND id = $3`
	lifecycleLaneClaimReceiptQuery = `SELECT id, org_id, session_id, thread_id,
		session_epoch, thread_epoch, admission_claim_token, admission_claim_turn
		FROM session_input_dispatches
		WHERE org_id = $1 AND session_id = $2 AND thread_id = $3 AND id = $4
		FOR NO KEY UPDATE NOWAIT`
)

// lifecycleLaneClaimExpected is an explicit comparison target, not trusted
// AdmissionAuthority. Never populate it by adopting a current lane/sibling claim.
type lifecycleLaneClaimExpected struct {
	sessionID, threadID, dispatchID, token uuid.UUID
	sessionEpoch, threadEpoch              int64
	turn                                   int32
}

type lifecycleLaneClaimComparison uint8

const (
	lifecycleLaneClaimUnknown lifecycleLaneClaimComparison = iota
	lifecycleLaneClaimAbsent
	lifecycleLaneClaimIncomplete
	lifecycleLaneClaimMismatch
	lifecycleLaneClaimStale
	lifecycleLaneClaimExact
)

type lifecycleLaneClaimReceipt struct {
	id, orgID, sessionID, threadID uuid.UUID
	sessionEpoch, threadEpoch      *int64
	token                          *uuid.UUID
	turn                           *int32
}

// lifecycleLaneClaimObservation is private and transaction-local. Even Exact
// means only receipt identity equality, NOT eligibility, ownership, runtime
// permission, safe release, physical drain or a sealed AdmissionAuthority.
// Every future mutation must compose its complete canonical checks in its own
// locked unit. No production caller exists; observations cannot survive owner
// settlement or be reused as permission in a later transaction.
type lifecycleLaneClaimObservation struct {
	scope       lockedLifecycleScope
	claim       LifecycleAdmissionClaimState
	currentTurn int32
	receipt     lifecycleLaneClaimReceipt
	comparison  lifecycleLaneClaimComparison
}

// verifyLifecycleLaneClaimInTx composes only private read/lock prerequisites.
// The caller owns an already configured bounded READ COMMITTED pgx.Tx, with
// every required higher-ranked domain/job lock already held. Acquisition order
// is shared cutover try-lock, parent, explicit lane, then expected dispatch.
// It never discovers a higher-ranked owner, changes settings, retries, settles
// or mutates. Missing rows remain ErrNoRows; independently NULL metadata is raw
// incomplete evidence, never inferred legacy identity. Any error discards ALL
// observations and requires the owner to roll back its entire unit.
func verifyLifecycleLaneClaimInTx(ctx context.Context, tx pgx.Tx, orgID uuid.UUID, expected lifecycleLaneClaimExpected) (lifecycleLaneClaimObservation, error) {
	if ctx == nil {
		return lifecycleLaneClaimObservation{}, errors.New("verify lifecycle lane claim: nil context")
	}
	if nilLifecycleDependency(tx) {
		return lifecycleLaneClaimObservation{}, errors.New("verify lifecycle lane claim: nil transaction")
	}
	if err := ctx.Err(); err != nil {
		return lifecycleLaneClaimObservation{}, fmt.Errorf("verify lifecycle lane claim context: %w", err)
	}
	if orgID == uuid.Nil || expected.sessionID == uuid.Nil || expected.threadID == uuid.Nil || expected.dispatchID == uuid.Nil || expected.token == uuid.Nil {
		return lifecycleLaneClaimObservation{}, errors.New("verify lifecycle lane claim: nonzero explicit organization, session, lane, dispatch and token required")
	}
	if expected.sessionEpoch < 0 || expected.threadEpoch < 0 || expected.turn < 0 {
		return lifecycleLaneClaimObservation{}, errors.New("verify lifecycle lane claim: nonnegative explicit epochs and turn required")
	}
	if err := tryLockLifecycleCutoverSharedInTx(ctx, tx, orgID, expected.sessionID); err != nil {
		return lifecycleLaneClaimObservation{}, err
	}
	scope, err := lockLifecycleScopeInTx(ctx, tx, orgID, lifecycleScope{sessionID: expected.sessionID, threadIDs: []uuid.UUID{expected.threadID}})
	if err != nil {
		return lifecycleLaneClaimObservation{}, err
	}
	observation := lifecycleLaneClaimObservation{scope: scope}
	var laneID, laneOrgID, laneSessionID uuid.UUID
	err = tx.QueryRow(ctx, lifecycleLaneClaimQuery, orgID, expected.sessionID, expected.threadID).
		Scan(&laneID, &laneOrgID, &laneSessionID, &observation.currentTurn, &observation.claim.DispatchID, &observation.claim.Token)
	if err != nil {
		return lifecycleLaneClaimObservation{}, fmt.Errorf("read locked lifecycle lane claim: %w", err)
	}
	if laneID != expected.threadID || laneOrgID != orgID || laneSessionID != expected.sessionID {
		return lifecycleLaneClaimObservation{}, fmt.Errorf("lifecycle lane claim scope mismatch: %w", pgx.ErrNoRows)
	}
	if err := ctx.Err(); err != nil {
		return lifecycleLaneClaimObservation{}, fmt.Errorf("lifecycle lane claim context after lane read: %w", err)
	}
	// Select the EXPLICIT expected dispatch, never the current pointer. A missing
	// expected receipt is not an absent claim and must not adopt a successor.
	r := &observation.receipt
	err = tx.QueryRow(ctx, lifecycleLaneClaimReceiptQuery, orgID, expected.sessionID, expected.threadID, expected.dispatchID).
		Scan(&r.id, &r.orgID, &r.sessionID, &r.threadID, &r.sessionEpoch, &r.threadEpoch, &r.token, &r.turn)
	if err != nil {
		return lifecycleLaneClaimObservation{}, fmt.Errorf("read lifecycle lane claim receipt: %w", err)
	}
	if r.id != expected.dispatchID || r.orgID != orgID || r.sessionID != expected.sessionID || r.threadID != expected.threadID {
		return lifecycleLaneClaimObservation{}, fmt.Errorf("lifecycle lane claim receipt scope mismatch: %w", pgx.ErrNoRows)
	}
	if err := ctx.Err(); err != nil {
		return lifecycleLaneClaimObservation{}, fmt.Errorf("lifecycle lane claim context after receipt read: %w", err)
	}
	observation.comparison = compareLifecycleLaneClaim(observation, expected)
	if err := ctx.Err(); err != nil {
		return lifecycleLaneClaimObservation{}, fmt.Errorf("lifecycle lane claim context after comparison: %w", err)
	}
	return observation, nil
}

func compareLifecycleLaneClaim(observation lifecycleLaneClaimObservation, expected lifecycleLaneClaimExpected) lifecycleLaneClaimComparison {
	c, r := observation.claim, observation.receipt
	if c.DispatchID == nil && c.Token == nil {
		return lifecycleLaneClaimAbsent
	}
	if c.DispatchID == nil || c.Token == nil || *c.DispatchID == uuid.Nil || *c.Token == uuid.Nil ||
		r.token == nil || *r.token == uuid.Nil || r.turn == nil || *r.turn < 0 ||
		r.sessionEpoch == nil || *r.sessionEpoch < 0 || r.threadEpoch == nil || *r.threadEpoch < 0 {
		return lifecycleLaneClaimIncomplete
	}
	if *c.DispatchID != expected.dispatchID || *c.Token != expected.token || *r.token != expected.token {
		return lifecycleLaneClaimMismatch
	}
	if observation.scope.session.epochs.AdmissionEpoch != expected.sessionEpoch ||
		observation.scope.threads[0].epochs.AdmissionEpoch != expected.threadEpoch ||
		*r.sessionEpoch != expected.sessionEpoch || *r.threadEpoch != expected.threadEpoch ||
		*r.turn != expected.turn {
		return lifecycleLaneClaimStale
	}
	// Visibility, status, end epochs and lifecycle version remain raw. Equality
	// cannot certify active semantics. Both current_turn counters remain raw:
	// the running turn is normally lane.current_turn+1, while completion advances
	// that counter. Only the recorded receipt turn is compared to the explicit
	// expected turn. Current-turn/consumer eligibility belongs to future canonical
	// commands, and independent siblings must not change this receipt's identity.
	return lifecycleLaneClaimExact
}
