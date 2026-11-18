package db

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
)

// InputWorkspaceCompatibilityMetadata is a raw, passive observation of one
// request's stored metadata. Absent fields stay independently unknown; neither
// the state label nor descriptor, digest or receipt contents establish a
// certificate, workspace authority or admission eligibility.
type InputWorkspaceCompatibilityMetadata struct {
	State                  models.InputWorkspaceCompatibilityState
	AnchorDigest           *string
	ContextScopeDigest     *string
	WorkspaceGeneration    *int64
	WorkspaceRevision      *int64
	SnapshotKey            *string
	WorkspaceState         json.RawMessage
	LastAuthorityKind      *string
	LastAuthorityReceiptID *uuid.UUID
	Reason                 *string
	NextRevalidationAt     *time.Time
}

// GetWorkspaceCompatibility reads exact tenant/session/request metadata without
// locks, status/expiry or parent-visibility filters. It neither validates stored
// values nor infers unprovable state from a missing row. Retained metadata for a
// terminal request remains observable; this is not a pending-only eligibility test.
func (s *SessionHumanInputRequestStore) GetWorkspaceCompatibility(ctx context.Context, orgID, sessionID, requestID uuid.UUID) (InputWorkspaceCompatibilityMetadata, error) {
	var metadata InputWorkspaceCompatibilityMetadata
	err := s.db.QueryRow(ctx, `
		SELECT state, anchor_digest, context_scope_digest, workspace_generation,
			workspace_revision, snapshot_key, workspace_state, last_authority_kind,
			last_authority_receipt_id, reason, next_revalidation_at
		FROM session_input_workspace_compatibility
		WHERE org_id = $1 AND session_id = $2 AND request_id = $3`, orgID, sessionID, requestID).
		Scan(&metadata.State, &metadata.AnchorDigest, &metadata.ContextScopeDigest,
			&metadata.WorkspaceGeneration, &metadata.WorkspaceRevision, &metadata.SnapshotKey,
			&metadata.WorkspaceState, &metadata.LastAuthorityKind, &metadata.LastAuthorityReceiptID,
			&metadata.Reason, &metadata.NextRevalidationAt)
	if err != nil {
		return InputWorkspaceCompatibilityMetadata{}, fmt.Errorf("get request workspace compatibility: %w", err)
	}
	return metadata, nil
}
