package db

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/assembledhq/143/internal/models"
)

var ErrSplitSourceUnavailable = errors.New("session has no complete diff snapshot")
var ErrInvalidSplitPath = errors.New("split path is invalid or absent from the source diff")
var ErrChangesetLeaseHeld = errors.New("changeset worktree is already leased")
var ErrChangesetRemoteChanged = errors.New("changeset remote branch head changed unexpectedly")

type SessionChangesetStore struct {
	db DBTX
}

func NewSessionChangesetStore(db DBTX) *SessionChangesetStore {
	return &SessionChangesetStore{db: db}
}

const changesetSelectColumns = `id, org_id, session_id, is_primary, order_index, title, summary,
	status, target_branch, base_branch, working_branch, stacked_on_changeset_id, head_sha,
	expected_remote_head_sha, base_head_sha, worktree_path, materialization_error, materialized_diff,
	restack_delta_kind, restack_delta_summary, restack_confirmation_required,
	pr_creation_state, pr_creation_error, created_at, updated_at`

const changesetSummaryColumns = `id, is_primary, order_index, title, summary, status, target_branch,
	base_branch, working_branch, stacked_on_changeset_id, head_sha, worktree_path, materialization_error,
	(head_sha IS NOT NULL AND head_sha IS DISTINCT FROM expected_remote_head_sha) AS has_unpushed_changes,
	restack_delta_kind, restack_delta_summary, restack_confirmation_required,
	(SELECT holder_type FROM session_changeset_leases l
		WHERE l.org_id = session_changesets.org_id AND l.session_id = session_changesets.session_id
			AND l.changeset_id = session_changesets.id AND l.expires_at > now()) AS active_lease_holder_type,
	(SELECT holder_label FROM session_changeset_leases l
		WHERE l.org_id = session_changesets.org_id AND l.session_id = session_changesets.session_id
			AND l.changeset_id = session_changesets.id AND l.expires_at > now()) AS active_lease_holder_label,
	created_at, updated_at`

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

func (s *SessionChangesetStore) GetByWorkingBranch(ctx context.Context, orgID, repositoryID uuid.UUID, workingBranch string) (models.SessionChangeset, error) {
	rows, err := s.db.Query(ctx, `SELECT `+changesetSelectColumns+`
		FROM session_changesets
		WHERE session_changesets.org_id = @org_id
		  AND session_changesets.working_branch = @working_branch
		  AND EXISTS (
			SELECT 1 FROM sessions
			WHERE sessions.org_id = @org_id
			  AND sessions.id = session_changesets.session_id
			  AND sessions.repository_id = @repository_id
		  )
		ORDER BY session_changesets.updated_at DESC, session_changesets.id DESC
		LIMIT 1`, pgx.NamedArgs{
		"org_id": orgID, "repository_id": repositoryID, "working_branch": workingBranch,
	})
	if err != nil {
		return models.SessionChangeset{}, fmt.Errorf("get session changeset by working branch: %w", err)
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[models.SessionChangeset])
}

func (s *SessionChangesetStore) ListFullBySession(ctx context.Context, orgID, sessionID uuid.UUID) ([]models.SessionChangeset, error) {
	rows, err := s.db.Query(ctx, `SELECT `+changesetSelectColumns+`
		FROM session_changesets
		WHERE org_id = @org_id AND session_id = @session_id
		ORDER BY order_index, created_at, id`, pgx.NamedArgs{"org_id": orgID, "session_id": sessionID})
	if err != nil {
		return nil, fmt.Errorf("list full session changesets: %w", err)
	}
	changesets, err := pgx.CollectRows(rows, pgx.RowToStructByName[models.SessionChangeset])
	if err != nil {
		return nil, fmt.Errorf("collect full session changesets: %w", err)
	}
	return changesets, nil
}

func (s *SessionChangesetStore) AcquireLease(
	ctx context.Context,
	orgID, sessionID, changesetID, holderID uuid.UUID,
	holderType models.ChangesetLeaseType,
	holderLabel string,
	ttl time.Duration,
	takeoverExpired bool,
) (models.SessionChangesetLease, error) {
	if err := holderType.Validate(); err != nil {
		return models.SessionChangesetLease{}, err
	}
	if ttl <= 0 {
		return models.SessionChangesetLease{}, errors.New("changeset lease TTL must be positive")
	}
	rows, err := s.db.Query(ctx, `INSERT INTO session_changeset_leases (
		changeset_id, org_id, session_id, holder_id, holder_type, holder_label, expires_at
	) SELECT c.id, c.org_id, c.session_id, @holder_id, @holder_type, @holder_label, now() + @ttl
	FROM session_changesets c
	WHERE c.org_id = @org_id AND c.session_id = @session_id AND c.id = @changeset_id
	  AND (c.worktree_path IS NOT NULL OR @holder_type = 'materialize')
	  AND c.status <> 'abandoned'
	ON CONFLICT (changeset_id) DO UPDATE SET
		holder_id = EXCLUDED.holder_id,
		holder_type = EXCLUDED.holder_type,
		holder_label = EXCLUDED.holder_label,
		acquired_at = CASE WHEN session_changeset_leases.holder_id = EXCLUDED.holder_id
			THEN session_changeset_leases.acquired_at ELSE now() END,
		heartbeat_at = now(),
		expires_at = EXCLUDED.expires_at
	WHERE session_changeset_leases.org_id = @org_id
	  AND session_changeset_leases.session_id = @session_id
	  AND (session_changeset_leases.holder_id = @holder_id
		OR (@takeover_expired AND session_changeset_leases.expires_at <= now()))
	RETURNING changeset_id, org_id, session_id, holder_id, holder_type, holder_label,
		acquired_at, heartbeat_at, expires_at`, pgx.NamedArgs{
		"org_id": orgID, "session_id": sessionID, "changeset_id": changesetID,
		"holder_id": holderID, "holder_type": holderType, "holder_label": strings.TrimSpace(holderLabel),
		"ttl": ttl, "takeover_expired": takeoverExpired,
	})
	if err != nil {
		return models.SessionChangesetLease{}, fmt.Errorf("acquire changeset lease: %w", err)
	}
	lease, err := pgx.CollectOneRow(rows, pgx.RowToStructByName[models.SessionChangesetLease])
	if errors.Is(err, pgx.ErrNoRows) {
		return models.SessionChangesetLease{}, ErrChangesetLeaseHeld
	}
	if err != nil {
		return models.SessionChangesetLease{}, fmt.Errorf("collect acquired changeset lease: %w", err)
	}
	return lease, nil
}

func (s *SessionChangesetStore) HeartbeatLease(ctx context.Context, orgID, sessionID, changesetID, holderID uuid.UUID, ttl time.Duration) error {
	if ttl <= 0 {
		return errors.New("changeset lease TTL must be positive")
	}
	result, err := s.db.Exec(ctx, `UPDATE session_changeset_leases
		SET heartbeat_at = now(), expires_at = now() + @ttl
		WHERE org_id = @org_id AND session_id = @session_id AND changeset_id = @changeset_id
		  AND holder_id = @holder_id AND expires_at > now()`, pgx.NamedArgs{
		"org_id": orgID, "session_id": sessionID, "changeset_id": changesetID, "holder_id": holderID, "ttl": ttl,
	})
	if err != nil {
		return fmt.Errorf("heartbeat changeset lease: %w", err)
	}
	if result.RowsAffected() == 0 {
		return ErrChangesetLeaseHeld
	}
	return nil
}

func (s *SessionChangesetStore) ReleaseLease(ctx context.Context, orgID, sessionID, changesetID, holderID uuid.UUID) error {
	_, err := s.db.Exec(ctx, `DELETE FROM session_changeset_leases
		WHERE org_id = @org_id AND session_id = @session_id AND changeset_id = @changeset_id AND holder_id = @holder_id`, pgx.NamedArgs{
		"org_id": orgID, "session_id": sessionID, "changeset_id": changesetID, "holder_id": holderID,
	})
	if err != nil {
		return fmt.Errorf("release changeset lease: %w", err)
	}
	return nil
}

// RecordLocalHead implements the lower-edit SHA protocol. A changed parent
// invalidates every non-terminal descendant whose recorded base no longer
// matches its parent's current head.
func (s *SessionChangesetStore) RecordLocalHead(ctx context.Context, orgID, sessionID, changesetID uuid.UUID, headSHA, diff string) error {
	txStarter, ok := s.db.(TxStarter)
	if !ok {
		return errors.New("record changeset head requires transaction support")
	}
	tx, err := txStarter.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin record changeset head: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	result, err := tx.Exec(ctx, `UPDATE session_changesets SET head_sha = @head_sha,
		materialized_diff = @diff,
		base_head_sha = CASE WHEN status = 'restack_conflict' THEN COALESCE((
			SELECT parent.head_sha FROM session_changesets parent
			WHERE parent.org_id = @org_id AND parent.session_id = @session_id
			  AND parent.id = session_changesets.stacked_on_changeset_id
		), base_head_sha) ELSE base_head_sha END,
		status = CASE WHEN status = 'restack_conflict' THEN 'ready' ELSE status END,
		materialization_error = CASE WHEN status = 'restack_conflict' THEN NULL ELSE materialization_error END,
		restack_delta_kind = CASE WHEN status = 'restack_conflict' THEN 'semantic_change' ELSE restack_delta_kind END,
		restack_delta_summary = CASE WHEN status = 'restack_conflict' THEN 'Agent-resolved restack; review the branch diff before pushing.' ELSE restack_delta_summary END,
		restack_confirmation_required = CASE WHEN status = 'restack_conflict' THEN true ELSE restack_confirmation_required END,
		updated_at = now()
		WHERE org_id = @org_id AND session_id = @session_id AND id = @changeset_id
		  AND worktree_path IS NOT NULL`, pgx.NamedArgs{
		"org_id": orgID, "session_id": sessionID, "changeset_id": changesetID, "head_sha": headSHA, "diff": diff,
	})
	if err != nil {
		return fmt.Errorf("record local changeset head: %w", err)
	}
	if result.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	if _, err := tx.Exec(ctx, `WITH RECURSIVE descendants AS (
		SELECT id FROM session_changesets
		WHERE org_id = @org_id AND session_id = @session_id AND stacked_on_changeset_id = @changeset_id
		UNION ALL
		SELECT child.id FROM session_changesets child JOIN descendants parent
		  ON child.stacked_on_changeset_id = parent.id
		WHERE child.org_id = @org_id AND child.session_id = @session_id
	)
	UPDATE session_changesets SET status = 'needs_restack', updated_at = now()
	WHERE org_id = @org_id AND session_id = @session_id AND id IN (SELECT id FROM descendants)
	  AND status NOT IN ('merged', 'abandoned', 'restack_conflict')`, pgx.NamedArgs{
		"org_id": orgID, "session_id": sessionID, "changeset_id": changesetID,
	}); err != nil {
		return fmt.Errorf("mark changeset descendants stale: %w", err)
	}
	return tx.Commit(ctx)
}

func (s *SessionChangesetStore) RecordPublishedHead(ctx context.Context, orgID, sessionID, changesetID uuid.UUID, headSHA string, hasPR bool) error {
	status := models.ChangesetStatusPublishedBranch
	if hasPR {
		status = models.ChangesetStatusPROpen
	}
	result, err := s.db.Exec(ctx, `UPDATE session_changesets SET head_sha = @head_sha,
		expected_remote_head_sha = @head_sha,
		status = CASE WHEN status IN ('merged', 'abandoned') THEN status ELSE @status END,
		updated_at = now()
		WHERE org_id = @org_id AND session_id = @session_id AND id = @changeset_id`, pgx.NamedArgs{
		"org_id": orgID, "session_id": sessionID, "changeset_id": changesetID, "head_sha": headSHA, "status": status,
	})
	if err != nil {
		return fmt.Errorf("record published changeset head: %w", err)
	}
	if result.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

func (s *SessionChangesetStore) ImportRemoteHead(ctx context.Context, orgID, sessionID, changesetID uuid.UUID, headSHA string) error {
	result, err := s.db.Exec(ctx, `UPDATE session_changesets SET
		expected_remote_head_sha = @head_sha, status = 'external_update_detected', updated_at = now()
		WHERE org_id = @org_id AND session_id = @session_id AND id = @changeset_id
		  AND working_branch IS NOT NULL`, pgx.NamedArgs{
		"org_id": orgID, "session_id": sessionID, "changeset_id": changesetID, "head_sha": headSHA,
	})
	if err != nil {
		return fmt.Errorf("import remote changeset head: %w", err)
	}
	if result.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

func (s *SessionChangesetStore) MarkExternalUpdateDetected(ctx context.Context, orgID, sessionID, changesetID uuid.UUID) error {
	result, err := s.db.Exec(ctx, `UPDATE session_changesets SET status = 'external_update_detected', updated_at = now()
		WHERE org_id = @org_id AND session_id = @session_id AND id = @changeset_id
		  AND working_branch IS NOT NULL`, pgx.NamedArgs{
		"org_id": orgID, "session_id": sessionID, "changeset_id": changesetID,
	})
	if err != nil {
		return fmt.Errorf("mark external changeset update: %w", err)
	}
	if result.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

// HandleMerged marks the merged node and advances its direct children to the
// parent's former base. The restack worker then rebuilds each descendant so
// the merged commits disappear from its GitHub diff.
func (s *SessionChangesetStore) HandleMerged(ctx context.Context, orgID, sessionID, changesetID uuid.UUID) error {
	txStarter, ok := s.db.(TxStarter)
	if !ok {
		return errors.New("handle merged changeset requires transaction support")
	}
	tx, err := txStarter.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin merged changeset transition: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var parentBase string
	if err := tx.QueryRow(ctx, `UPDATE session_changesets SET status = 'merged', updated_at = now()
		WHERE org_id = @org_id AND session_id = @session_id AND id = @changeset_id
		RETURNING base_branch`, pgx.NamedArgs{
		"org_id": orgID, "session_id": sessionID, "changeset_id": changesetID,
	}).Scan(&parentBase); err != nil {
		return fmt.Errorf("mark changeset merged: %w", err)
	}
	if _, err := tx.Exec(ctx, `WITH RECURSIVE descendants AS (
		SELECT id, stacked_on_changeset_id FROM session_changesets
		WHERE org_id = @org_id AND session_id = @session_id AND stacked_on_changeset_id = @changeset_id
		UNION ALL
		SELECT child.id, child.stacked_on_changeset_id FROM session_changesets child
		JOIN descendants parent ON child.stacked_on_changeset_id = parent.id
		WHERE child.org_id = @org_id AND child.session_id = @session_id
	)
	UPDATE session_changesets c SET
		base_branch = CASE WHEN c.stacked_on_changeset_id = @changeset_id THEN @parent_base ELSE c.base_branch END,
		status = 'needs_restack', updated_at = now()
	FROM descendants d
	WHERE c.org_id = @org_id AND c.session_id = @session_id AND c.id = d.id
	  AND c.status NOT IN ('merged', 'abandoned')`, pgx.NamedArgs{
		"org_id": orgID, "session_id": sessionID, "changeset_id": changesetID, "parent_base": parentBase,
	}); err != nil {
		return fmt.Errorf("retarget merged changeset children: %w", err)
	}
	return tx.Commit(ctx)
}

func (s *SessionChangesetStore) MarkRestacking(ctx context.Context, orgID, sessionID, changesetID uuid.UUID) error {
	result, err := s.db.Exec(ctx, `UPDATE session_changesets SET status = 'restacking',
		materialization_error = NULL, updated_at = now()
		WHERE org_id = @org_id AND session_id = @session_id AND id = @changeset_id
		  AND status = 'needs_restack'`, pgx.NamedArgs{
		"org_id": orgID, "session_id": sessionID, "changeset_id": changesetID,
	})
	if err != nil {
		return fmt.Errorf("mark changeset restacking: %w", err)
	}
	if result.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

func (s *SessionChangesetStore) CompleteRestack(ctx context.Context, orgID, sessionID, changesetID uuid.UUID, baseHeadSHA, headSHA, diff string) error {
	result, err := s.db.Exec(ctx, `UPDATE session_changesets SET base_head_sha = @base_head_sha,
		head_sha = @head_sha, materialized_diff = @diff,
		status = CASE WHEN expected_remote_head_sha IS NULL THEN 'planned' ELSE 'pr_open' END,
		materialization_error = NULL, restack_delta_kind = 'clean_replay',
		restack_delta_summary = 'Clean replay completed without conflict.',
		restack_confirmation_required = false, updated_at = now()
		WHERE org_id = @org_id AND session_id = @session_id AND id = @changeset_id
		  AND status IN ('needs_restack', 'restacking')`, pgx.NamedArgs{
		"org_id": orgID, "session_id": sessionID, "changeset_id": changesetID,
		"base_head_sha": baseHeadSHA, "head_sha": headSHA, "diff": diff,
	})
	if err != nil {
		return fmt.Errorf("complete changeset restack: %w", err)
	}
	if result.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

func (s *SessionChangesetStore) ConfirmRestackDelta(ctx context.Context, orgID, sessionID, changesetID uuid.UUID) error {
	result, err := s.db.Exec(ctx, `UPDATE session_changesets SET restack_confirmation_required = false, updated_at = now()
		WHERE org_id = @org_id AND session_id = @session_id AND id = @changeset_id
		  AND restack_confirmation_required`, pgx.NamedArgs{
		"org_id": orgID, "session_id": sessionID, "changeset_id": changesetID,
	})
	if err != nil {
		return fmt.Errorf("confirm restack delta: %w", err)
	}
	if result.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

func (s *SessionChangesetStore) MarkRestackConflict(ctx context.Context, orgID, sessionID, changesetID uuid.UUID, message string) error {
	result, err := s.db.Exec(ctx, `UPDATE session_changesets SET status = 'restack_conflict',
		materialization_error = @message, updated_at = now()
		WHERE org_id = @org_id AND session_id = @session_id AND id = @changeset_id`, pgx.NamedArgs{
		"org_id": orgID, "session_id": sessionID, "changeset_id": changesetID, "message": message,
	})
	if err != nil {
		return fmt.Errorf("mark changeset restack conflict: %w", err)
	}
	if result.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
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
		SET head_sha = @head_sha, expected_remote_head_sha = @head_sha,
		status = 'pr_open', materialization_error = NULL, updated_at = now()
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

func (s *SessionChangesetStore) BeginMaterialization(ctx context.Context, orgID, sessionID, changesetID uuid.UUID) (models.SessionChangeset, error) {
	rows, err := s.db.Query(ctx, `UPDATE session_changesets SET
		status = 'materializing', materialization_error = NULL, updated_at = now()
		WHERE org_id = @org_id AND session_id = @session_id AND id = @changeset_id
		  AND NOT is_primary
		  AND status = 'planned' AND worktree_path IS NULL
		  AND (stacked_on_changeset_id IS NULL OR EXISTS (
			SELECT 1 FROM session_changesets parent
			WHERE parent.org_id = @org_id AND parent.session_id = @session_id
			  AND parent.id = session_changesets.stacked_on_changeset_id
			  AND parent.worktree_path IS NOT NULL AND parent.working_branch IS NOT NULL
		  ))
		  AND NOT EXISTS (SELECT 1 FROM session_changesets active
			WHERE active.org_id = @org_id AND active.session_id = @session_id AND active.status = 'materializing')
		RETURNING `+changesetSelectColumns, pgx.NamedArgs{"org_id": orgID, "session_id": sessionID, "changeset_id": changesetID})
	if err != nil {
		return models.SessionChangeset{}, fmt.Errorf("begin changeset materialization: %w", err)
	}
	changeset, err := pgx.CollectOneRow(rows, pgx.RowToStructByName[models.SessionChangeset])
	if err != nil {
		return models.SessionChangeset{}, fmt.Errorf("begin changeset materialization: %w", err)
	}
	return changeset, nil
}

func (s *SessionChangesetStore) CompleteMaterialization(ctx context.Context, orgID, sessionID, changesetID uuid.UUID, workingBranch, worktreePath, baseHeadSHA, headSHA, diff string) error {
	txStarter, ok := s.db.(TxStarter)
	if !ok {
		return errors.New("complete changeset materialization requires transaction support")
	}
	tx, err := txStarter.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin complete changeset materialization: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	result, err := tx.Exec(ctx, `UPDATE session_changesets SET
		status = 'planned', working_branch = @working_branch, worktree_path = @worktree_path,
		base_head_sha = @base_head_sha, head_sha = @head_sha, materialized_diff = @diff,
		materialization_error = NULL, updated_at = now()
		WHERE org_id = @org_id AND session_id = @session_id AND id = @changeset_id AND status = 'materializing'`, pgx.NamedArgs{
		"org_id": orgID, "session_id": sessionID, "changeset_id": changesetID, "working_branch": workingBranch,
		"worktree_path": worktreePath, "base_head_sha": baseHeadSHA, "head_sha": headSHA,
		"diff": diff,
	})
	if err != nil {
		return fmt.Errorf("complete changeset materialization: %w", err)
	}
	if result.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	if _, err := tx.Exec(ctx, `UPDATE session_changesets SET base_branch = @working_branch, updated_at = now()
		WHERE org_id = @org_id AND session_id = @session_id AND stacked_on_changeset_id = @changeset_id
		  AND worktree_path IS NULL`, pgx.NamedArgs{
		"org_id": orgID, "session_id": sessionID, "changeset_id": changesetID, "working_branch": workingBranch,
	}); err != nil {
		return fmt.Errorf("update child changeset base branch: %w", err)
	}
	return tx.Commit(ctx)
}

func (s *SessionChangesetStore) FailMaterialization(ctx context.Context, orgID, sessionID, changesetID uuid.UUID, message string) error {
	result, err := s.db.Exec(ctx, `UPDATE session_changesets SET status = 'planned', materialization_error = @error, updated_at = now()
		WHERE org_id = @org_id AND session_id = @session_id AND id = @changeset_id AND status = 'materializing'`, pgx.NamedArgs{
		"org_id": orgID, "session_id": sessionID, "changeset_id": changesetID, "error": message,
	})
	if err != nil {
		return fmt.Errorf("fail changeset materialization: %w", err)
	}
	if result.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

func (s *SessionChangesetStore) RecordMaterializedDiff(ctx context.Context, orgID, sessionID, changesetID uuid.UUID, headSHA, diff string) error {
	result, err := s.db.Exec(ctx, `UPDATE session_changesets SET head_sha = @head_sha, materialized_diff = @diff, updated_at = now()
		WHERE org_id = @org_id AND session_id = @session_id AND id = @changeset_id AND worktree_path IS NOT NULL`, pgx.NamedArgs{
		"org_id": orgID, "session_id": sessionID, "changeset_id": changesetID, "head_sha": headSHA, "diff": diff,
	})
	if err != nil {
		return fmt.Errorf("record materialized changeset diff: %w", err)
	}
	if result.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	return nil
}

func (s *SessionChangesetStore) InitializeSplit(ctx context.Context, orgID, sessionID uuid.UUID) error {
	result, err := s.db.Exec(ctx, `INSERT INTO session_changeset_split_plans (org_id, session_id, source_diff_snapshot_id)
		SELECT @org_id, @session_id, s.latest_diff_snapshot_id FROM sessions s
		JOIN session_diff_snapshots d ON d.id = s.latest_diff_snapshot_id AND d.org_id = s.org_id AND d.session_id = s.id
		WHERE s.org_id = @org_id AND s.id = @session_id AND s.deleted_at IS NULL
		  AND s.latest_diff_snapshot_id IS NOT NULL AND NOT d.review_artifact_truncated
		ON CONFLICT (org_id, session_id) DO NOTHING`, pgx.NamedArgs{"org_id": orgID, "session_id": sessionID})
	if err != nil {
		return fmt.Errorf("initialize changeset split: %w", err)
	}
	if result.RowsAffected() == 0 {
		var exists bool
		if err := s.db.QueryRow(ctx, `SELECT EXISTS (SELECT 1 FROM session_changeset_split_plans WHERE org_id = @org_id AND session_id = @session_id)`, pgx.NamedArgs{"org_id": orgID, "session_id": sessionID}).Scan(&exists); err != nil {
			return err
		}
		if !exists {
			return ErrSplitSourceUnavailable
		}
	}
	return nil
}

func (s *SessionChangesetStore) GetAssignedPatch(ctx context.Context, orgID, sessionID, changesetID uuid.UUID) (string, error) {
	var diff string
	if err := s.db.QueryRow(ctx, `SELECT d.diff FROM session_changeset_split_plans p
		JOIN session_diff_snapshots d ON d.id = p.source_diff_snapshot_id AND d.org_id = p.org_id AND d.session_id = p.session_id
		WHERE p.org_id = @org_id AND p.session_id = @session_id AND p.status = 'draft'`, pgx.NamedArgs{
		"org_id": orgID, "session_id": sessionID,
	}).Scan(&diff); err != nil {
		return "", fmt.Errorf("load assigned split source: %w", err)
	}
	rows, err := s.db.Query(ctx, `SELECT path FROM session_changeset_split_paths
		WHERE org_id = @org_id AND session_id = @session_id AND changeset_id = @changeset_id ORDER BY path`, pgx.NamedArgs{
		"org_id": orgID, "session_id": sessionID, "changeset_id": changesetID,
	})
	if err != nil {
		return "", fmt.Errorf("list assigned split patch paths: %w", err)
	}
	paths, err := pgx.CollectRows(rows, pgx.RowTo[string])
	if err != nil {
		return "", fmt.Errorf("collect assigned split patch paths: %w", err)
	}
	sections := splitDiffSections(diff)
	var patch strings.Builder
	for _, path := range paths {
		section, ok := sections[path]
		if !ok {
			return "", fmt.Errorf("%w: %s", ErrInvalidSplitPath, path)
		}
		patch.WriteString(section)
	}
	return patch.String(), nil
}

// ReplaceSplitPaths freezes the latest diff snapshot as the split source on
// first use, then replaces one changeset's draft path assignment atomically.
func (s *SessionChangesetStore) ReplaceSplitPaths(ctx context.Context, orgID, sessionID, changesetID uuid.UUID, paths []string) error {
	if _, err := s.GetByID(ctx, orgID, sessionID, changesetID); err != nil {
		return err
	}
	normalized, err := validateRequestedSplitPaths(paths)
	if err != nil {
		return err
	}
	if err := s.InitializeSplit(ctx, orgID, sessionID); err != nil {
		return err
	}
	var sourceDiff string
	if err := s.db.QueryRow(ctx, `SELECT d.diff FROM session_changeset_split_plans p
		JOIN session_diff_snapshots d ON d.id = p.source_diff_snapshot_id AND d.org_id = p.org_id AND d.session_id = p.session_id
		WHERE p.org_id = @org_id AND p.session_id = @session_id AND p.status = 'draft'`, pgx.NamedArgs{
		"org_id": orgID, "session_id": sessionID,
	}).Scan(&sourceDiff); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return ErrSplitSourceUnavailable
		}
		return fmt.Errorf("load changeset split source: %w", err)
	}
	sourcePaths := make(map[string]struct{})
	for _, path := range splitDiffPaths(sourceDiff) {
		sourcePaths[path] = struct{}{}
	}
	for _, path := range normalized {
		if _, ok := sourcePaths[path]; !ok {
			return fmt.Errorf("%w: %s", ErrInvalidSplitPath, path)
		}
	}
	paths = normalized
	result, err := s.db.Exec(ctx, `WITH locked AS (
		SELECT pg_advisory_xact_lock(hashtextextended(@org_id::text || ':' || @session_id::text || ':' || @changeset_id::text, 0))
	), source AS (
		INSERT INTO session_changeset_split_plans (org_id, session_id, source_diff_snapshot_id)
		SELECT @org_id, @session_id, latest_diff_snapshot_id
		FROM sessions, locked
		WHERE org_id = @org_id AND id = @session_id AND deleted_at IS NULL
		  AND latest_diff_snapshot_id IS NOT NULL
		ON CONFLICT (org_id, session_id) DO NOTHING
		RETURNING id
	), valid_changeset AS (
		SELECT id FROM session_changesets
		WHERE org_id = @org_id AND session_id = @session_id AND id = @changeset_id
	), deleted AS (
		DELETE FROM session_changeset_split_paths
		WHERE org_id = @org_id AND session_id = @session_id AND changeset_id = @changeset_id
		  AND EXISTS (SELECT 1 FROM valid_changeset)
	), updated_plan AS (
		UPDATE session_changeset_split_plans SET revision = revision + 1, updated_at = now()
		WHERE org_id = @org_id AND session_id = @session_id
		RETURNING session_id
	)
	INSERT INTO session_changeset_split_paths (org_id, session_id, changeset_id, path)
	SELECT @org_id, @session_id, @changeset_id, path
	FROM unnest(@paths::text[]) AS path
	WHERE EXISTS (SELECT 1 FROM session_changeset_split_plans WHERE org_id = @org_id AND session_id = @session_id)
	  AND EXISTS (SELECT 1 FROM valid_changeset)
	  AND EXISTS (SELECT 1 FROM updated_plan)`, pgx.NamedArgs{
		"org_id": orgID, "session_id": sessionID, "changeset_id": changesetID, "paths": paths,
	})
	if err != nil {
		return fmt.Errorf("replace changeset split paths: %w", err)
	}
	if len(paths) > 0 && result.RowsAffected() == 0 {
		return ErrSplitSourceUnavailable
	}
	// An empty assignment cannot use RowsAffected to distinguish success. Verify
	// both scoped parents exist, which also prevents a silent cross-tenant no-op.
	if len(paths) == 0 {
		var valid bool
		if err := s.db.QueryRow(ctx, `SELECT EXISTS (
			SELECT 1 FROM session_changeset_split_plans p
			JOIN session_changesets c ON c.org_id = p.org_id AND c.session_id = p.session_id
			WHERE p.org_id = @org_id AND p.session_id = @session_id AND c.id = @changeset_id
		)`, pgx.NamedArgs{"org_id": orgID, "session_id": sessionID, "changeset_id": changesetID}).Scan(&valid); err != nil {
			return fmt.Errorf("verify changeset split assignment: %w", err)
		}
		if !valid {
			return ErrSplitSourceUnavailable
		}
	}
	return nil
}

func (s *SessionChangesetStore) GetSplitStatus(ctx context.Context, orgID, sessionID uuid.UUID) (models.ChangesetSplitStatus, error) {
	status := models.ChangesetSplitStatus{
		SourcePaths:     []string{},
		Assignments:     []models.ChangesetSplitPathAssignment{},
		UnassignedPaths: []string{},
		Duplicates:      []models.ChangesetSplitDuplicate{},
		Conflicts:       []models.ChangesetSplitConflict{},
		Omissions:       []models.ChangesetSplitOmission{},
		UnexpectedPaths: []string{},
		Verification:    models.ChangesetSplitVerificationPlanned,
	}
	var diff string
	if err := s.db.QueryRow(ctx, `SELECT p.status, p.source_diff_snapshot_id, d.diff
		FROM session_changeset_split_plans p
		JOIN session_diff_snapshots d ON d.id = p.source_diff_snapshot_id AND d.org_id = p.org_id AND d.session_id = p.session_id
		WHERE p.org_id = @org_id AND p.session_id = @session_id`, pgx.NamedArgs{
		"org_id": orgID, "session_id": sessionID,
	}).Scan(&status.Status, &status.SourceDiffSnapshotID, &diff); err != nil {
		return models.ChangesetSplitStatus{}, fmt.Errorf("get changeset split source: %w", err)
	}
	rows, err := s.db.Query(ctx, `SELECT c.id, p.path, c.materialized_diff
		FROM session_changesets c
		JOIN session_changeset_split_plans plan ON plan.org_id = c.org_id AND plan.session_id = c.session_id
		LEFT JOIN session_changeset_split_paths p ON p.org_id = c.org_id AND p.session_id = c.session_id AND p.changeset_id = c.id
		WHERE c.org_id = @org_id AND c.session_id = @session_id
		  AND ((plan.status = 'draft' AND NOT c.is_primary) OR (plan.status = 'accepted' AND c.status <> 'abandoned'))
		  AND (c.worktree_path IS NOT NULL OR p.path IS NOT NULL)
		ORDER BY c.id, p.path`, pgx.NamedArgs{
		"org_id": orgID, "session_id": sessionID,
	})
	if err != nil {
		return models.ChangesetSplitStatus{}, fmt.Errorf("list changeset split paths: %w", err)
	}
	defer rows.Close()
	assigned := make(map[string][]uuid.UUID)
	byChangeset := make(map[uuid.UUID][]string)
	changesetDiffs := make(map[uuid.UUID]*string)
	for rows.Next() {
		var changesetID uuid.UUID
		var path sql.NullString
		var materializedDiff *string
		if err := rows.Scan(&changesetID, &path, &materializedDiff); err != nil {
			return models.ChangesetSplitStatus{}, fmt.Errorf("scan changeset split path: %w", err)
		}
		if path.Valid {
			assigned[path.String] = append(assigned[path.String], changesetID)
			byChangeset[changesetID] = append(byChangeset[changesetID], path.String)
		}
		changesetDiffs[changesetID] = materializedDiff
	}
	if err := rows.Err(); err != nil {
		return models.ChangesetSplitStatus{}, fmt.Errorf("iterate changeset split paths: %w", err)
	}
	status.SourcePaths = splitDiffPaths(diff)
	omissionRows, err := s.db.Query(ctx, `SELECT path, reason, confirmed_by_user_id, created_at
		FROM session_changeset_split_omissions WHERE org_id = @org_id AND session_id = @session_id ORDER BY path`, pgx.NamedArgs{
		"org_id": orgID, "session_id": sessionID,
	})
	if err != nil {
		return models.ChangesetSplitStatus{}, fmt.Errorf("list changeset split omissions: %w", err)
	}
	status.Omissions, err = pgx.CollectRows(omissionRows, pgx.RowToStructByName[models.ChangesetSplitOmission])
	if err != nil {
		return models.ChangesetSplitStatus{}, fmt.Errorf("collect changeset split omissions: %w", err)
	}
	omitted := make(map[string]struct{}, len(status.Omissions))
	for _, omission := range status.Omissions {
		omitted[omission.Path] = struct{}{}
	}
	for id, paths := range byChangeset {
		status.Assignments = append(status.Assignments, models.ChangesetSplitPathAssignment{ChangesetID: id, Paths: paths})
	}
	sort.Slice(status.Assignments, func(i, j int) bool {
		return status.Assignments[i].ChangesetID.String() < status.Assignments[j].ChangesetID.String()
	})
	actualOwners := assigned
	allMaterialized := len(changesetDiffs) > 0
	for _, materializedDiff := range changesetDiffs {
		if materializedDiff == nil {
			allMaterialized = false
			break
		}
	}
	if allMaterialized {
		status.Verification = models.ChangesetSplitVerificationVerified
		actualOwners = make(map[string][]uuid.UUID)
		sourceSections := splitDiffSections(diff)
		for changesetID, materializedDiff := range changesetDiffs {
			for path, section := range splitDiffSections(*materializedDiff) {
				actualOwners[path] = append(actualOwners[path], changesetID)
				if source, ok := sourceSections[path]; ok && source != section {
					status.Conflicts = append(status.Conflicts, models.ChangesetSplitConflict{Path: path, ChangesetID: changesetID, Reason: "materialized patch differs from split source"})
				}
			}
		}
		for path := range actualOwners {
			if _, ok := sourceSections[path]; !ok {
				status.UnexpectedPaths = append(status.UnexpectedPaths, path)
			}
		}
		sort.Strings(status.UnexpectedPaths)
	}
	for _, path := range status.SourcePaths {
		owners := actualOwners[path]
		if len(owners) == 0 {
			if _, ok := omitted[path]; !ok {
				status.UnassignedPaths = append(status.UnassignedPaths, path)
			}
		}
		if len(owners) > 1 {
			status.Duplicates = append(status.Duplicates, models.ChangesetSplitDuplicate{Path: path, ChangesetIDs: owners})
		}
	}
	status.Complete = status.Verification == models.ChangesetSplitVerificationVerified && len(status.SourcePaths) > 0 &&
		len(status.UnassignedPaths) == 0 && len(status.Duplicates) == 0 && len(status.Conflicts) == 0 && len(status.UnexpectedPaths) == 0
	return status, nil
}

func (s *SessionChangesetStore) ReplaceSplitOmissions(ctx context.Context, orgID, sessionID, userID uuid.UUID, omissions []models.ChangesetSplitOmission) error {
	var sourceDiff string
	if err := s.db.QueryRow(ctx, `SELECT d.diff FROM session_changeset_split_plans p
		JOIN session_diff_snapshots d ON d.id = p.source_diff_snapshot_id AND d.org_id = p.org_id AND d.session_id = p.session_id
		WHERE p.org_id = @org_id AND p.session_id = @session_id AND p.status = 'draft'`, pgx.NamedArgs{
		"org_id": orgID, "session_id": sessionID,
	}).Scan(&sourceDiff); err != nil {
		return fmt.Errorf("load split source for omissions: %w", err)
	}
	sourcePaths := map[string]struct{}{}
	for _, path := range splitDiffPaths(sourceDiff) {
		sourcePaths[path] = struct{}{}
	}
	paths := make([]string, 0, len(omissions))
	reasons := make([]string, 0, len(omissions))
	for _, omission := range omissions {
		path := strings.TrimSpace(omission.Path)
		reason := strings.TrimSpace(omission.Reason)
		_, inSource := sourcePaths[path]
		if _, err := validateRequestedSplitPaths([]string{path}); err != nil || reason == "" || !inSource {
			return fmt.Errorf("%w: omission path and reason are required", ErrInvalidSplitPath)
		}
		paths = append(paths, path)
		reasons = append(reasons, reason)
	}
	result, err := s.db.Exec(ctx, `WITH locked AS (
		SELECT pg_advisory_xact_lock(hashtextextended(@org_id::text || ':' || @session_id::text, 0))
	), deleted AS (
		DELETE FROM session_changeset_split_omissions WHERE org_id = @org_id AND session_id = @session_id
	)
	INSERT INTO session_changeset_split_omissions (org_id, session_id, path, reason, confirmed_by_user_id)
	SELECT @org_id, @session_id, p.path, p.reason, @user_id
	FROM locked, unnest(@paths::text[], @reasons::text[]) AS p(path, reason)
	WHERE NOT EXISTS (SELECT 1 FROM session_changeset_split_paths sp
		WHERE sp.org_id = @org_id AND sp.session_id = @session_id AND sp.path = p.path)`, pgx.NamedArgs{
		"org_id": orgID, "session_id": sessionID, "user_id": userID, "paths": paths, "reasons": reasons,
	})
	if err != nil {
		return fmt.Errorf("replace changeset split omissions: %w", err)
	}
	if int(result.RowsAffected()) != len(paths) {
		return ErrInvalidSplitPath
	}
	return nil
}

func (s *SessionChangesetStore) AcceptSplit(ctx context.Context, orgID, sessionID, userID uuid.UUID) error {
	txStarter, ok := s.db.(TxStarter)
	if !ok {
		return errors.New("accept changeset split requires transaction support")
	}
	tx, err := txStarter.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin accept changeset split: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	store := NewSessionChangesetStore(tx)
	status, err := store.GetSplitStatus(ctx, orgID, sessionID)
	if err != nil {
		return err
	}
	if !status.Complete {
		return errors.New("changeset split must be verified and complete before acceptance")
	}
	var nextPrimaryID uuid.UUID
	var targetBranch, workingBranch string
	if err := tx.QueryRow(ctx, `SELECT id, target_branch, working_branch
		FROM session_changesets WHERE org_id = @org_id AND session_id = @session_id
		  AND NOT is_primary AND worktree_path IS NOT NULL
		ORDER BY order_index, created_at, id LIMIT 1 FOR UPDATE`, pgx.NamedArgs{
		"org_id": orgID, "session_id": sessionID,
	}).Scan(&nextPrimaryID, &targetBranch, &workingBranch); err != nil {
		return fmt.Errorf("select accepted split primary: %w", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE session_changesets SET is_primary = false, status = 'abandoned', updated_at = now()
		WHERE org_id = @org_id AND session_id = @session_id AND is_primary`, pgx.NamedArgs{"org_id": orgID, "session_id": sessionID}); err != nil {
		return fmt.Errorf("archive split source changeset: %w", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE session_changesets SET is_primary = true, updated_at = now()
		WHERE org_id = @org_id AND session_id = @session_id AND id = @changeset_id`, pgx.NamedArgs{
		"org_id": orgID, "session_id": sessionID, "changeset_id": nextPrimaryID,
	}); err != nil {
		return fmt.Errorf("promote accepted split primary: %w", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE session_changeset_split_plans SET status = 'accepted', accepted_at = now(),
		accepted_by_user_id = @user_id, updated_at = now() WHERE org_id = @org_id AND session_id = @session_id AND status = 'draft'`, pgx.NamedArgs{
		"org_id": orgID, "session_id": sessionID, "user_id": userID,
	}); err != nil {
		return fmt.Errorf("archive accepted split source: %w", err)
	}
	if _, err := tx.Exec(ctx, `WITH combined AS (
		SELECT string_agg(COALESCE(materialized_diff, ''), E'\n' ORDER BY order_index) AS diff
		FROM session_changesets WHERE org_id = @org_id AND session_id = @session_id AND status <> 'abandoned'
	), stats AS (
		SELECT diff, count(*) FILTER (WHERE line LIKE 'diff --git %') AS files_changed,
			count(*) FILTER (WHERE line LIKE '+%' AND line NOT LIKE '+++%') AS added,
			count(*) FILTER (WHERE line LIKE '-%' AND line NOT LIKE '---%') AS removed
		FROM combined, LATERAL unnest(string_to_array(COALESCE(diff, ''), E'\n')) line GROUP BY diff
	)
	UPDATE sessions SET working_branch = @working_branch, target_branch = @target_branch,
		diff = stats.diff, diff_stats = jsonb_build_object('files_changed', stats.files_changed, 'added', stats.added, 'removed', stats.removed),
		diff_collected_at = now(), latest_diff_snapshot_id = NULL, workspace_revision = workspace_revision + 1,
		workspace_revision_updated_at = now(), updated_at = now()
	FROM stats WHERE sessions.org_id = @org_id AND sessions.id = @session_id`, pgx.NamedArgs{
		"org_id": orgID, "session_id": sessionID, "working_branch": workingBranch, "target_branch": targetBranch,
	}); err != nil {
		return fmt.Errorf("switch accepted split session rollup: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit accepted changeset split: %w", err)
	}
	return nil
}

func (s *SessionChangesetStore) Reorder(ctx context.Context, orgID, sessionID uuid.UUID, orderedIDs []uuid.UUID) error {
	txStarter, ok := s.db.(TxStarter)
	if !ok {
		return errors.New("reorder changesets requires transaction support")
	}
	tx, err := txStarter.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin reorder changesets: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var count int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM session_changesets
		WHERE org_id = @org_id AND session_id = @session_id AND NOT is_primary`, pgx.NamedArgs{"org_id": orgID, "session_id": sessionID}).Scan(&count); err != nil {
		return err
	}
	if count != len(orderedIDs) {
		return errors.New("ordered changeset IDs must include every non-primary changeset exactly once")
	}
	if _, err := tx.Exec(ctx, `UPDATE session_changesets SET order_index = order_index + 1000000
		WHERE org_id = @org_id AND session_id = @session_id AND NOT is_primary`, pgx.NamedArgs{"org_id": orgID, "session_id": sessionID}); err != nil {
		return fmt.Errorf("stage changeset reorder: %w", err)
	}
	result, err := tx.Exec(ctx, `UPDATE session_changesets c SET order_index = ordered.position, updated_at = now()
		FROM (SELECT id, ordinality::int AS position FROM unnest(@ids::uuid[]) WITH ORDINALITY) ordered
		WHERE c.org_id = @org_id AND c.session_id = @session_id AND NOT c.is_primary AND c.id = ordered.id`, pgx.NamedArgs{
		"org_id": orgID, "session_id": sessionID, "ids": orderedIDs,
	})
	if err != nil {
		return fmt.Errorf("apply changeset reorder: %w", err)
	}
	if int(result.RowsAffected()) != count {
		return errors.New("ordered changeset IDs contain duplicates or foreign changesets")
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit changeset reorder: %w", err)
	}
	return nil
}

func (s *SessionChangesetStore) FoldInto(ctx context.Context, orgID, sessionID, sourceID, targetID uuid.UUID) error {
	if sourceID == targetID {
		return errors.New("source and target changesets must differ")
	}
	txStarter, ok := s.db.(TxStarter)
	if !ok {
		return errors.New("fold changeset requires transaction support")
	}
	tx, err := txStarter.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin fold changeset: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	var valid int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM session_changesets WHERE org_id = @org_id AND session_id = @session_id
		AND id = ANY(@ids::uuid[]) AND NOT is_primary AND status = 'planned' AND worktree_path IS NULL`, pgx.NamedArgs{
		"org_id": orgID, "session_id": sessionID, "ids": []uuid.UUID{sourceID, targetID},
	}).Scan(&valid); err != nil {
		return err
	}
	if valid != 2 {
		return errors.New("only unmaterialized planned pull requests can be folded")
	}
	if _, err := tx.Exec(ctx, `INSERT INTO session_changeset_split_paths (org_id, session_id, changeset_id, path)
		SELECT org_id, session_id, @target_id, path FROM session_changeset_split_paths
		WHERE org_id = @org_id AND session_id = @session_id AND changeset_id = @source_id
		ON CONFLICT DO NOTHING`, pgx.NamedArgs{"org_id": orgID, "session_id": sessionID, "source_id": sourceID, "target_id": targetID}); err != nil {
		return fmt.Errorf("move folded split paths: %w", err)
	}
	if _, err := tx.Exec(ctx, `DELETE FROM session_changesets WHERE org_id = @org_id AND session_id = @session_id AND id = @source_id`, pgx.NamedArgs{
		"org_id": orgID, "session_id": sessionID, "source_id": sourceID,
	}); err != nil {
		return fmt.Errorf("delete folded changeset: %w", err)
	}
	if _, err := tx.Exec(ctx, `WITH ordered AS (SELECT id, row_number() OVER (ORDER BY order_index, created_at, id) AS position
		FROM session_changesets WHERE org_id = @org_id AND session_id = @session_id AND NOT is_primary)
		UPDATE session_changesets c SET order_index = ordered.position FROM ordered WHERE c.id = ordered.id`, pgx.NamedArgs{"org_id": orgID, "session_id": sessionID}); err != nil {
		return fmt.Errorf("compact folded changeset order: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit fold changeset: %w", err)
	}
	return nil
}

func normalizeSplitPaths(paths []string) []string {
	seen := make(map[string]struct{}, len(paths))
	clean := make([]string, 0, len(paths))
	for _, path := range paths {
		path = strings.TrimSpace(path)
		if path == "" || strings.HasPrefix(path, "/") || path == ".." || strings.HasPrefix(path, "../") || strings.Contains(path, "/../") {
			continue
		}
		if _, ok := seen[path]; ok {
			continue
		}
		seen[path] = struct{}{}
		clean = append(clean, path)
	}
	sort.Strings(clean)
	return clean
}

func validateRequestedSplitPaths(paths []string) ([]string, error) {
	for _, path := range paths {
		trimmed := strings.TrimSpace(path)
		if trimmed == "" || strings.HasPrefix(trimmed, "/") || trimmed == ".." || strings.HasPrefix(trimmed, "../") || strings.Contains(trimmed, "/../") {
			return nil, fmt.Errorf("%w: %q", ErrInvalidSplitPath, path)
		}
	}
	return normalizeSplitPaths(paths), nil
}

func splitDiffPaths(diff string) []string {
	sections := splitDiffSections(diff)
	paths := make([]string, 0, len(sections))
	for path := range sections {
		paths = append(paths, path)
	}
	return normalizeSplitPaths(paths)
}

func splitDiffSections(diff string) map[string]string {
	sections := make(map[string]string)
	var path string
	var current strings.Builder
	flush := func() {
		if path != "" {
			sections[path] = current.String()
		}
		current.Reset()
	}
	for _, line := range strings.Split(diff, "\n") {
		if !strings.HasPrefix(line, "diff --git ") {
			if path != "" {
				current.WriteString(line)
				current.WriteByte('\n')
			}
			continue
		}
		flush()
		parsed, ok := diffHeaderDestination(line)
		if !ok {
			continue
		}
		path = parsed
		current.WriteString(line)
		current.WriteByte('\n')
	}
	flush()
	return sections
}

func diffHeaderDestination(line string) (string, bool) {
	rest := strings.TrimPrefix(line, "diff --git ")
	if strings.HasPrefix(rest, "a/") {
		if marker := strings.LastIndex(rest, " b/"); marker >= 0 {
			return rest[marker+3:], true
		}
	}
	if strings.HasPrefix(rest, `"a/`) {
		marker := strings.LastIndex(rest, ` "b/`)
		if marker < 0 {
			return "", false
		}
		raw := rest[marker+1:]
		decoded, err := strconv.Unquote(raw)
		if err != nil || !strings.HasPrefix(decoded, "b/") {
			return "", false
		}
		return strings.TrimPrefix(decoded, "b/"), true
	}
	return "", false
}
