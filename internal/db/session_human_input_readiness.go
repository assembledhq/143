package db

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
)

// HumanInputReadinessMetadata is a raw, passive observation of request metadata.
// Absent fields remain unknown; proof contents and classifications are neither
// validated nor interpreted as admission eligibility or mutation authority.
type HumanInputReadinessMetadata struct {
	LegacyRequest         *bool
	ProofPolicyVersion    *int32
	EnforcementVersion    *int32
	ClassificationVersion *int32
	ReadinessState        *models.HumanInputReadinessState
	ReadinessDeadlineAt   *time.Time
	ReadinessProof        json.RawMessage
}

// GetLifecycleReadiness follows GetByID visibility for the exact tenant,
// session and request. It does not lock rows, infer missing values, or filter
// status, expiry, parent visibility or unknown future classifications.
func (s *SessionHumanInputRequestStore) GetLifecycleReadiness(ctx context.Context, orgID, sessionID, requestID uuid.UUID) (HumanInputReadinessMetadata, error) {
	var metadata HumanInputReadinessMetadata
	err := s.db.QueryRow(ctx, `
		SELECT legacy_request, proof_policy_version, enforcement_version,
			classification_version, readiness_state, readiness_deadline_at, readiness_proof
		FROM session_human_input_requests
		WHERE org_id = $1 AND session_id = $2 AND id = $3`, orgID, sessionID, requestID).
		Scan(&metadata.LegacyRequest, &metadata.ProofPolicyVersion, &metadata.EnforcementVersion,
			&metadata.ClassificationVersion, &metadata.ReadinessState, &metadata.ReadinessDeadlineAt, &metadata.ReadinessProof)
	if err != nil {
		return HumanInputReadinessMetadata{}, fmt.Errorf("get request lifecycle readiness: %w", err)
	}
	return metadata, nil
}
