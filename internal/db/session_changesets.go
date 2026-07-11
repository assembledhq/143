package db

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

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

const changesetSummaryColumns = `id, is_primary, order_index, title, summary, status, target_branch,
	base_branch, working_branch, stacked_on_changeset_id, head_sha, created_at, updated_at`

func (s *SessionChangesetStore) ListBySession(ctx context.Context, orgID, sessionID uuid.UUID) ([]models.ChangesetSummary, error) {
	rows, err := s.db.Query(ctx, `SELECT `+changesetSummaryColumns+`
		FROM session_changesets
		WHERE org_id = @org_id AND session_id = @session_id
		ORDER BY order_index, created_at, id`, pgx.NamedArgs{"org_id": orgID, "session_id": sessionID})
	if err != nil {
		return nil, fmt.Errorf("list session changesets: %w", err)
	}
	changesets, err := pgx.CollectRows(rows, pgx.RowToStructByNameLax[models.ChangesetSummary])
	if err != nil {
		return nil, fmt.Errorf("collect session changesets: %w", err)
	}
	return changesets, nil
}

func (s *SessionChangesetStore) Create(ctx context.Context, orgID, sessionID uuid.UUID, title, summary string, stackedOn *uuid.UUID) (models.SessionChangeset, error) {
	const maxOrderAttempts = 3
	for attempt := 0; attempt < maxOrderAttempts; attempt++ {
		changeset, err := s.create(ctx, orgID, sessionID, title, summary, stackedOn)
		if err == nil {
			return changeset, nil
		}
		if !isChangesetOrderConflict(err) || attempt == maxOrderAttempts-1 {
			return models.SessionChangeset{}, err
		}
	}
	return models.SessionChangeset{}, fmt.Errorf("create session changeset: exhausted order allocation retries")
}

func (s *SessionChangesetStore) create(ctx context.Context, orgID, sessionID uuid.UUID, title, summary string, stackedOn *uuid.UUID) (models.SessionChangeset, error) {
	rows, err := s.db.Query(ctx, `INSERT INTO session_changesets (
		org_id, session_id, order_index, title, summary, target_branch, base_branch, stacked_on_changeset_id
	)
	SELECT @org_id, @session_id,
		COALESCE((SELECT MAX(order_index) + 1 FROM session_changesets WHERE org_id = @org_id AND session_id = @session_id), 0),
		@title, @summary, primary_cs.target_branch,
		CASE WHEN parent.id IS NULL THEN primary_cs.target_branch ELSE COALESCE(parent.working_branch, parent.base_branch) END,
		parent.id
	FROM session_changesets primary_cs
	LEFT JOIN session_changesets parent ON parent.org_id = @org_id AND parent.session_id = @session_id AND parent.id = @stacked_on
	WHERE primary_cs.org_id = @org_id AND primary_cs.session_id = @session_id AND primary_cs.is_primary
	  AND (@stacked_on::uuid IS NULL OR parent.id IS NOT NULL)
	RETURNING `+changesetSelectColumns, pgx.NamedArgs{
		"org_id": orgID, "session_id": sessionID, "title": title, "summary": summary, "stacked_on": stackedOn,
	})
	if err != nil {
		return models.SessionChangeset{}, fmt.Errorf("create session changeset: %w", err)
	}
	changeset, err := pgx.CollectOneRow(rows, pgx.RowToStructByName[models.SessionChangeset])
	if err != nil {
		return models.SessionChangeset{}, fmt.Errorf("create session changeset: %w", err)
	}
	return changeset, nil
}

func isChangesetOrderConflict(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505" && pgErr.ConstraintName == "session_changesets_org_id_session_id_order_index_key"
}

func (s *SessionChangesetStore) UpdateMetadata(ctx context.Context, orgID, sessionID, changesetID uuid.UUID, title, summary *string) (models.SessionChangeset, error) {
	rows, err := s.db.Query(ctx, `UPDATE session_changesets SET
		title = COALESCE(@title, title), summary = COALESCE(@summary, summary), updated_at = now()
		WHERE org_id = @org_id AND session_id = @session_id AND id = @changeset_id
		RETURNING `+changesetSelectColumns, pgx.NamedArgs{
		"org_id": orgID, "session_id": sessionID, "changeset_id": changesetID, "title": title, "summary": summary,
	})
	if err != nil {
		return models.SessionChangeset{}, fmt.Errorf("update session changeset: %w", err)
	}
	changeset, err := pgx.CollectOneRow(rows, pgx.RowToStructByName[models.SessionChangeset])
	if err != nil {
		return models.SessionChangeset{}, fmt.Errorf("update session changeset: %w", err)
	}
	return changeset, nil
}

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

func (s *SessionChangesetStore) GetByID(ctx context.Context, orgID, sessionID, changesetID uuid.UUID) (models.SessionChangeset, error) {
	rows, err := s.db.Query(ctx, `SELECT `+changesetSelectColumns+`
		FROM session_changesets
		WHERE org_id = @org_id AND session_id = @session_id AND id = @changeset_id`, pgx.NamedArgs{
		"org_id": orgID, "session_id": sessionID, "changeset_id": changesetID,
	})
	if err != nil {
		return models.SessionChangeset{}, fmt.Errorf("get session changeset: %w", err)
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
