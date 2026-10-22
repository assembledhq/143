package db

import (
	"context"
	"fmt"

	"github.com/google/uuid"
)

// LifecycleEpochState is a passive observation of persisted lifecycle metadata.
// Its values do not establish admission eligibility or mutation authorization.
type LifecycleEpochState struct {
	AdmissionEpoch   int64
	EndEpoch         *int64
	LifecycleVersion int16
}

// LifecycleRequestEpochState observes the admission epochs recorded on a human
// input request. Each absent epoch remains unknown independently; these values
// do not establish request eligibility or mutation authorization.
type LifecycleRequestEpochState struct {
	AdmissionSessionEpoch *int64
	AdmissionThreadEpoch  *int64
}

// LifecycleAdmissionClaimState observes a lane's retained logical claim pair.
// Presence is not proof of admission, current epochs, a turn, or provider authority.
type LifecycleAdmissionClaimState struct {
	DispatchID *uuid.UUID
	Token      *uuid.UUID
}

// LifecycleEpochStore reads passive metadata without acquiring lifecycle authority.
type LifecycleEpochStore struct {
	db DBTX
}

func NewLifecycleEpochStore(db DBTX) *LifecycleEpochStore {
	return &LifecycleEpochStore{db: db}
}

// GetSession follows SessionStore.GetByID visibility: deleted sessions are hidden,
// while archived sessions remain observable. The read does not lock the row.
func (s *LifecycleEpochStore) GetSession(ctx context.Context, orgID, sessionID uuid.UUID) (LifecycleEpochState, error) {
	var state LifecycleEpochState
	err := s.db.QueryRow(ctx, `
		SELECT admission_epoch, end_epoch, lifecycle_version
		FROM sessions
		WHERE org_id = $1 AND id = $2 AND deleted_at IS NULL`, orgID, sessionID).
		Scan(&state.AdmissionEpoch, &state.EndEpoch, &state.LifecycleVersion)
	if err != nil {
		return LifecycleEpochState{}, fmt.Errorf("get session lifecycle epochs: %w", err)
	}
	return state, nil
}

// GetThread follows SessionThreadStore.GetByID visibility by hiding archived
// threads, and additionally requires the exact parent session. Like that store,
// it does not filter the parent session's visibility. The read does not lock rows.
func (s *LifecycleEpochStore) GetThread(ctx context.Context, orgID, sessionID, threadID uuid.UUID) (LifecycleEpochState, error) {
	var state LifecycleEpochState
	err := s.db.QueryRow(ctx, `
		SELECT admission_epoch, end_epoch, lifecycle_version
		FROM session_threads
		WHERE org_id = $1 AND session_id = $2 AND id = $3 AND archived_at IS NULL`, orgID, sessionID, threadID).
		Scan(&state.AdmissionEpoch, &state.EndEpoch, &state.LifecycleVersion)
	if err != nil {
		return LifecycleEpochState{}, fmt.Errorf("get thread lifecycle epochs: %w", err)
	}
	return state, nil
}

// GetRequest follows SessionHumanInputRequestStore.GetByID visibility, scoped to
// the exact tenant and session. It neither locks rows nor infers missing epochs
// from current session or thread state, and does not filter status or expiry.
func (s *LifecycleEpochStore) GetRequest(ctx context.Context, orgID, sessionID, requestID uuid.UUID) (LifecycleRequestEpochState, error) {
	var state LifecycleRequestEpochState
	err := s.db.QueryRow(ctx, `
		SELECT admission_session_epoch, admission_thread_epoch
		FROM session_human_input_requests
		WHERE org_id = $1 AND session_id = $2 AND id = $3`, orgID, sessionID, requestID).
		Scan(&state.AdmissionSessionEpoch, &state.AdmissionThreadEpoch)
	if err != nil {
		return LifecycleRequestEpochState{}, fmt.Errorf("get request lifecycle epochs: %w", err)
	}
	return state, nil
}

// GetThreadClaim reads the exact tenant/session/lane without locking or deriving
// authority. Unlike GetThread, it includes archived lanes and invisible parents:
// retained claim metadata remains observable until explicit future cleanup.
func (s *LifecycleEpochStore) GetThreadClaim(ctx context.Context, orgID, sessionID, threadID uuid.UUID) (LifecycleAdmissionClaimState, error) {
	var state LifecycleAdmissionClaimState
	err := s.db.QueryRow(ctx, `
		SELECT admission_claim_dispatch_id, admission_claim_token
		FROM session_threads
		WHERE org_id = $1 AND session_id = $2 AND id = $3`, orgID, sessionID, threadID).
		Scan(&state.DispatchID, &state.Token)
	if err != nil {
		return LifecycleAdmissionClaimState{}, fmt.Errorf("get thread admission claim: %w", err)
	}
	return state, nil
}
