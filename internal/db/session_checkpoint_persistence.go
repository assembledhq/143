package db

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// PublishCheckpointWithProvenanceInTx persists the existing session checkpoint
// and generation provenance statement on only the supplied transaction. It never
// begins, settles, retries or publishes, and adds no lifecycle or readiness
// authority. The owner must roll back on error or false, discard effects on any
// failed or uncertain commit, and publish them only after confirmed commit.
// False is not proof of no writes: the legacy CTE may update the session before
// discovering that no matching provenance row exists. The standalone DBTX
// wrapper intentionally retains that historical statement behavior.
func (s *SessionStore) PublishCheckpointWithProvenanceInTx(ctx context.Context, orgID uuid.UUID, tx pgx.Tx, sessionID, lockToken uuid.UUID, agentSessionID, snapshotKey string, kind models.CheckpointKind, capability models.CheckpointCapability, sizeBytes int64, checkpointedAt time.Time, stopReason models.RuntimeStopReason, provenance models.CheckpointProvenance) (bool, SessionPersistenceEffects, error) {
	if nilLifecycleDependency(tx) {
		return false, SessionPersistenceEffects{}, fmt.Errorf("persist checkpoint with provenance: transaction is required")
	}
	return s.publishCheckpointWithProvenanceRow(ctx, tx, orgID, sessionID, lockToken, agentSessionID, snapshotKey, kind, capability, sizeBytes, checkpointedAt, stopReason, provenance)
}

func (s *SessionStore) publishCheckpointWithProvenanceRow(ctx context.Context, database DBTX, orgID, sessionID uuid.UUID, lockToken uuid.UUID, agentSessionID, snapshotKey string, kind models.CheckpointKind, capability models.CheckpointCapability, sizeBytes int64, checkpointedAt time.Time, stopReason models.RuntimeStopReason, provenance models.CheckpointProvenance) (bool, SessionPersistenceEffects, error) {
	if strings.TrimSpace(snapshotKey) == "" {
		return false, SessionPersistenceEffects{}, fmt.Errorf("publish checkpoint with provenance: snapshot key is required")
	}
	if provenance.GenerationID == uuid.Nil {
		return false, SessionPersistenceEffects{}, fmt.Errorf("publish checkpoint with provenance: generation is required")
	}
	args := pgx.NamedArgs{
		"id":                     sessionID,
		"org_id":                 orgID,
		"generation_id":          provenance.GenerationID,
		"agent_session_id":       agentSessionID,
		"snapshot_key":           snapshotKey,
		"checkpoint_kind":        string(kind),
		"checkpoint_capability":  string(capability),
		"checkpoint_size_bytes":  sizeBytes,
		"checkpointed_at":        checkpointedAt.UTC(),
		"checkpoint_error":       nil,
		"runtime_stop_reason":    string(stopReason),
		"checkpoint_head_sha":    nullIfEmpty(provenance.HeadSHA),
		"dependency_fingerprint": provenance.DependencyFingerprint,
		"review_complete":        provenance.ReviewComplete && kind == models.CheckpointKindTurnComplete,
	}
	query := `WITH published AS (` + publishCheckpointUpdate + `
		  AND s.automation_owner_generation_id = @generation_id`
	if lockToken != uuid.Nil {
		query += publishCheckpointJobFence
		args["lock_token"] = lockToken
	}
	query += `
			RETURNING s.id, s.org_id, s.workspace_revision, s.workspace_revision_updated_at
		), provenance AS (
			UPDATE automation_target_sessions g
			SET checkpoint_snapshot_key = @snapshot_key,
			    checkpoint_head_sha = @checkpoint_head_sha,
			    checkpoint_dependency_fingerprint = @dependency_fingerprint,
			    checkpoint_review_complete = @review_complete,
			    updated_at = now()
			FROM published p
			WHERE g.id = @generation_id AND g.org_id = p.org_id AND g.session_id = p.id
			RETURNING g.id
		)
		SELECT p.workspace_revision, p.workspace_revision_updated_at
		FROM published p
		JOIN provenance g ON true`
	rows, err := database.Query(ctx, query, args)
	if err != nil {
		return false, SessionPersistenceEffects{}, fmt.Errorf("publish checkpoint with provenance: %w", err)
	}
	row, err := pgx.CollectOneRow(rows, pgx.RowToStructByPos[workspaceRevisionUpdate])
	if errors.Is(err, pgx.ErrNoRows) {
		return false, SessionPersistenceEffects{}, nil
	}
	if err != nil {
		return false, SessionPersistenceEffects{}, fmt.Errorf("collect published checkpoint with provenance: %w", err)
	}
	return true, SessionPersistenceEffects{orgID: orgID, sessionID: sessionID, workspaceRevision: row, workspaceChanged: true, workspaceReason: "checkpoint"}, nil
}
