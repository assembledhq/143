package db

import (
	"context"
	"errors"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

// ErrThreadCreationIdentityConflict refuses adoption without disclosing another
// tenant's row or replacing the retained ID with a new identity.
var ErrThreadCreationIdentityConflict = errors.New("retained thread identity is unavailable or conflicts")

// SameThreadCreationIdentity compares the retained creation tuple. Runtime
// progress is deliberately not compared or reset. Archived identities refuse.
func SameThreadCreationIdentity(existing, expected models.SessionThread) bool {
	modelEqual := existing.ModelOverride == nil && expected.ModelOverride == nil ||
		existing.ModelOverride != nil && expected.ModelOverride != nil && *existing.ModelOverride == *expected.ModelOverride
	return expected.ID != uuid.Nil && expected.OrgID != uuid.Nil && expected.SessionID != uuid.Nil &&
		existing.ID == expected.ID && existing.OrgID == expected.OrgID && existing.SessionID == expected.SessionID &&
		existing.AgentType == expected.AgentType && modelEqual && existing.Label == expected.Label && existing.ArchivedAt == nil
}

// GetByIDForCreation includes archived rows so an unavailable original identity
// cannot be mistaken for permission to create a replacement.
func (s *SessionThreadStore) GetByIDForCreation(ctx context.Context, orgID, threadID uuid.UUID) (models.SessionThread, error) {
	rows, err := s.db.Query(ctx, `SELECT `+sessionThreadSelectColumns+` FROM session_threads WHERE org_id=$1 AND id=$2`, orgID, threadID)
	if err != nil {
		return models.SessionThread{}, err
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByNameLax[models.SessionThread])
}

// CreateWithID creates the exact retained ID or adopts its existing matching
// row. It owns no transaction and has no non-row effect. Fresh creation keeps
// the existing ownership trigger and visible-thread capacity predicate.
func (s *SessionThreadStore) CreateWithID(ctx context.Context, orgID uuid.UUID, thread *models.SessionThread, maxThreads int) error {
	if thread == nil || thread.ID == uuid.Nil || orgID == uuid.Nil || thread.OrgID != orgID || thread.SessionID == uuid.Nil || maxThreads < 1 {
		return ErrThreadCreationIdentityConflict
	}
	existing, err := s.GetByIDForCreation(ctx, orgID, thread.ID)
	if err == nil {
		if !SameThreadCreationIdentity(existing, *thread) {
			return ErrThreadCreationIdentityConflict
		}
		*thread = existing
		return nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	candidate := *thread
	normalizeSessionThreadExecutionDefaults(&candidate)
	if candidate.CreatedBySource == "" {
		candidate.CreatedBySource = models.ThreadCreatedBySourceUser
	}
	var capacity bool
	var insertedID *uuid.UUID
	var createdAt *time.Time
	err = s.db.QueryRow(ctx, `WITH capacity AS MATERIALIZED (
 SELECT count(*)<$14 AS available FROM session_threads WHERE org_id=$1 AND session_id=$2 AND archived_at IS NULL
), inserted AS (
 INSERT INTO session_threads(id,org_id,session_id,agent_type,model_override,reasoning_effort,label,instructions,file_scope,
 status,created_by_source,created_by_thread_id,execution_mode,filesystem_mode)
 SELECT $3,$1,$2,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$15 FROM capacity WHERE available
 ON CONFLICT(id) DO NOTHING RETURNING id,created_at
)
SELECT available,(SELECT id FROM inserted),(SELECT created_at FROM inserted) FROM capacity`, orgID, candidate.SessionID,
		candidate.ID, candidate.AgentType, candidate.ModelOverride, candidate.ReasoningEffort, candidate.Label, candidate.Instructions,
		candidate.FileScope, candidate.Status, candidate.CreatedBySource, candidate.CreatedByThreadID, candidate.ExecutionMode,
		maxThreads, candidate.FilesystemMode).Scan(&capacity, &insertedID, &createdAt)
	if err != nil {
		return err
	}
	if insertedID != nil && createdAt != nil {
		if *insertedID != candidate.ID {
			return ErrThreadCreationIdentityConflict
		}
		candidate.CreatedAt = *createdAt
		*thread = candidate
		return nil
	}
	// A concurrent insert may have won the same ID. A separate statement sees
	// its committed row; never use the INSERT snapshot as absence/abort proof.
	existing, err = s.GetByIDForCreation(ctx, orgID, candidate.ID)
	if err == nil {
		if !SameThreadCreationIdentity(existing, candidate) {
			return ErrThreadCreationIdentityConflict
		}
		*thread = existing
		return nil
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	if !capacity {
		return ErrThreadLimitReached
	}
	return ErrThreadCreationIdentityConflict
}
