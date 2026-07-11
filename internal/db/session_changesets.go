package db

import (
	"context"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/assembledhq/143/internal/models"
)

type SessionChangesetStore struct {
	db DBTX
}

func NewSessionChangesetStore(db DBTX) *SessionChangesetStore {
	return &SessionChangesetStore{db: db}
}

const changesetSelectColumns = `id, org_id, session_id, is_primary, order_index, title, summary,
	status, target_branch, base_branch, working_branch, stacked_on_changeset_id, head_sha,
	expected_remote_head_sha, base_head_sha, pr_creation_state, pr_creation_error, created_at, updated_at`

func (s *SessionChangesetStore) GetPrimary(ctx context.Context, orgID, sessionID uuid.UUID) (models.SessionChangeset, error) {
	rows, err := s.db.Query(ctx, `SELECT `+changesetSelectColumns+`
		FROM session_changesets
		WHERE org_id = @org_id AND session_id = @session_id AND is_primary`, pgx.NamedArgs{
		"org_id":     orgID,
		"session_id": sessionID,
	})
	if err != nil {
		return models.SessionChangeset{}, fmt.Errorf("get primary session changeset: %w", err)
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[models.SessionChangeset])
}

func (s *SessionChangesetStore) UpdatePrimaryBranches(
	ctx context.Context,
	orgID, sessionID uuid.UUID,
	targetBranch string,
	workingBranch *string,
	baseHeadSHA *string,
) error {
	result, err := s.db.Exec(ctx, `UPDATE session_changesets SET
		target_branch = @target_branch,
		base_branch = CASE WHEN stacked_on_changeset_id IS NULL THEN @target_branch ELSE base_branch END,
		working_branch = @working_branch,
		base_head_sha = @base_head_sha,
		updated_at = now()
		WHERE org_id = @org_id AND session_id = @session_id AND is_primary`, pgx.NamedArgs{
		"org_id": orgID, "session_id": sessionID, "target_branch": targetBranch,
		"working_branch": workingBranch, "base_head_sha": baseHeadSHA,
	})
	if err != nil {
		return fmt.Errorf("update primary session changeset branches: %w", err)
	}
	if result.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

func (s *SessionChangesetStore) TryMarkPRCreationQueued(ctx context.Context, orgID, sessionID, changesetID uuid.UUID) (bool, error) {
	result, err := s.db.Exec(ctx, `UPDATE session_changesets
		SET pr_creation_state = 'queued', pr_creation_error = NULL, updated_at = now()
		WHERE org_id = @org_id AND session_id = @session_id AND id = @changeset_id
		  AND pr_creation_state NOT IN ('queued', 'pushing', 'succeeded')`, pgx.NamedArgs{
		"org_id": orgID, "session_id": sessionID, "changeset_id": changesetID,
	})
	if err != nil {
		return false, fmt.Errorf("queue changeset PR creation: %w", err)
	}
	return result.RowsAffected() == 1, nil
}

func (s *SessionChangesetStore) UpdatePRCreationState(ctx context.Context, orgID, sessionID, changesetID uuid.UUID, state models.PRCreationState, errMsg string) error {
	if err := state.Validate(); err != nil {
		return err
	}
	var creationError *string
	if state == models.PRCreationStateFailed && errMsg != "" {
		creationError = &errMsg
	}
	_, err := s.db.Exec(ctx, `UPDATE session_changesets
		SET pr_creation_state = @state, pr_creation_error = @error, updated_at = now()
		WHERE org_id = @org_id AND session_id = @session_id AND id = @changeset_id
		  AND pr_creation_state <> 'succeeded'`, pgx.NamedArgs{
		"org_id": orgID, "session_id": sessionID, "changeset_id": changesetID,
		"state": state, "error": creationError,
	})
	if err != nil {
		return fmt.Errorf("update changeset PR creation state: %w", err)
	}
	return nil
}

func (s *SessionChangesetStore) RecordPushedHead(ctx context.Context, orgID, sessionID, changesetID uuid.UUID, headSHA string) error {
	result, err := s.db.Exec(ctx, `UPDATE session_changesets
		SET head_sha = @head_sha, expected_remote_head_sha = @head_sha, updated_at = now()
		WHERE org_id = @org_id AND session_id = @session_id AND id = @changeset_id`, pgx.NamedArgs{
		"org_id": orgID, "session_id": sessionID, "changeset_id": changesetID, "head_sha": headSHA,
	})
	if err != nil {
		return fmt.Errorf("record pushed changeset head: %w", err)
	}
	if result.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}
