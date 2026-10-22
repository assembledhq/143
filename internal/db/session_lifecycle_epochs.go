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
