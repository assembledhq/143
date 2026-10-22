package db

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/google/uuid"
)

// ExecutionEpochMetadata observes independently nullable admission metadata.
// Its values do not establish execution eligibility or mutation authority.
type ExecutionEpochMetadata struct {
	AdmissionSessionEpoch *int64
	AdmissionThreadEpoch  *int64
}

// RuntimeLifecycleMetadata observes raw runtime lifecycle metadata. Neither an
// absent drain proof nor any JSON value establishes that a runtime has drained.
type RuntimeLifecycleMetadata struct {
	AdmissionSessionEpoch *int64
	AdmissionThreadEpoch  *int64
	DrainProof            json.RawMessage
}

// GetLifecycleMetadata reads one exact runtime without filtering status, lease
// or parent visibility, locking rows, or inferring absent metadata.
func (s *ThreadRuntimeStore) GetLifecycleMetadata(ctx context.Context, orgID, sessionID, threadID, runtimeID uuid.UUID) (RuntimeLifecycleMetadata, error) {
	var metadata RuntimeLifecycleMetadata
	err := s.db.QueryRow(ctx, `
		SELECT admission_session_epoch, admission_thread_epoch, drain_proof
		FROM thread_runtimes
		WHERE org_id = $1 AND session_id = $2 AND thread_id = $3 AND id = $4`, orgID, sessionID, threadID, runtimeID).
		Scan(&metadata.AdmissionSessionEpoch, &metadata.AdmissionThreadEpoch, &metadata.DrainProof)
	if err != nil {
		return RuntimeLifecycleMetadata{}, fmt.Errorf("get runtime lifecycle metadata: %w", err)
	}
	return metadata, nil
}

// GetLifecycleMetadata reads one exact executor without filtering status, lease
// or parent visibility. A nil threadID matches only an unthreaded executor; it
// never broadens the scope. Missing epochs remain unknown and rows are not locked.
func (s *SessionExecutorStore) GetLifecycleMetadata(ctx context.Context, orgID, sessionID uuid.UUID, threadID *uuid.UUID, executorID uuid.UUID) (ExecutionEpochMetadata, error) {
	var metadata ExecutionEpochMetadata
	err := s.db.QueryRow(ctx, `
		SELECT admission_session_epoch, admission_thread_epoch
		FROM session_executors
		WHERE org_id = $1 AND session_id = $2 AND thread_id IS NOT DISTINCT FROM $3::uuid AND id = $4`, orgID, sessionID, threadID, executorID).
		Scan(&metadata.AdmissionSessionEpoch, &metadata.AdmissionThreadEpoch)
	if err != nil {
		return ExecutionEpochMetadata{}, fmt.Errorf("get executor lifecycle metadata: %w", err)
	}
	return metadata, nil
}

// GetInboxBatchLifecycleMetadata reads one exact inbox batch without filtering
// status, lease or parent visibility, locking rows, or inferring missing epochs.
func (s *SessionActivityPhaseStore) GetInboxBatchLifecycleMetadata(ctx context.Context, orgID, sessionID, threadID, batchID uuid.UUID) (ExecutionEpochMetadata, error) {
	var metadata ExecutionEpochMetadata
	err := s.db.QueryRow(ctx, `
		SELECT admission_session_epoch, admission_thread_epoch
		FROM thread_inbox_delivery_batches
		WHERE org_id = $1 AND session_id = $2 AND thread_id = $3 AND id = $4`, orgID, sessionID, threadID, batchID).
		Scan(&metadata.AdmissionSessionEpoch, &metadata.AdmissionThreadEpoch)
	if err != nil {
		return ExecutionEpochMetadata{}, fmt.Errorf("get inbox batch lifecycle metadata: %w", err)
	}
	return metadata, nil
}
