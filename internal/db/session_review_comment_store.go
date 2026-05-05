package db

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/assembledhq/143/internal/models"
)

type SessionReviewCommentStore struct {
	db DBTX
}

func NewSessionReviewCommentStore(db DBTX) *SessionReviewCommentStore {
	return &SessionReviewCommentStore{db: db}
}

const sessionReviewCommentColumns = `id, session_id, org_id, user_id, file_path,
	line_number, diff_side, body, resolved, resolved_at, resolved_by_pass,
	pass_number, created_at, updated_at`

func (s *SessionReviewCommentStore) Create(ctx context.Context, c *models.SessionReviewComment) error {
	query := `
		INSERT INTO session_review_comments (session_id, org_id, user_id, file_path, line_number, diff_side, body, pass_number)
		VALUES (@session_id, @org_id, @user_id, @file_path, @line_number, @diff_side, @body, @pass_number)
		RETURNING ` + sessionReviewCommentColumns

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"session_id":  c.SessionID,
		"org_id":      c.OrgID,
		"user_id":     c.UserID,
		"file_path":   c.FilePath,
		"line_number": c.LineNumber,
		"diff_side":   c.DiffSide,
		"body":        c.Body,
		"pass_number": c.PassNumber,
	})
	if err != nil {
		return fmt.Errorf("insert session review comment: %w", err)
	}

	result, err := pgx.CollectOneRow(rows, pgx.RowToStructByName[models.SessionReviewComment])
	if err != nil {
		return fmt.Errorf("scan session review comment: %w", err)
	}
	*c = result
	return nil
}

func (s *SessionReviewCommentStore) GetByID(ctx context.Context, orgID, id uuid.UUID) (models.SessionReviewComment, error) {
	query := `
		SELECT ` + sessionReviewCommentColumns + `
		FROM session_review_comments
		WHERE id = @id AND org_id = @org_id`

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"id":     id,
		"org_id": orgID,
	})
	if err != nil {
		return models.SessionReviewComment{}, fmt.Errorf("query session review comment: %w", err)
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[models.SessionReviewComment])
}

func (s *SessionReviewCommentStore) ListBySession(ctx context.Context, orgID, sessionID uuid.UUID) ([]models.SessionReviewComment, error) {
	query := `
		SELECT ` + sessionReviewCommentColumns + `
		FROM session_review_comments
		WHERE session_id = @session_id AND org_id = @org_id
		ORDER BY file_path, line_number, created_at`

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"session_id": sessionID,
		"org_id":     orgID,
	})
	if err != nil {
		return nil, fmt.Errorf("query session review comments: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[models.SessionReviewComment])
}

// ListByIDs returns comments matching the given IDs scoped to the org+session.
// Used to validate that requested comment IDs all belong to the session before
// performing batch operations. Results are unordered; callers should compare
// the returned set against the requested IDs to detect missing rows.
func (s *SessionReviewCommentStore) ListByIDs(ctx context.Context, orgID, sessionID uuid.UUID, ids []uuid.UUID) ([]models.SessionReviewComment, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	query := `
		SELECT ` + sessionReviewCommentColumns + `
		FROM session_review_comments
		WHERE id = ANY(@ids) AND org_id = @org_id AND session_id = @session_id`

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"ids":        ids,
		"org_id":     orgID,
		"session_id": sessionID,
	})
	if err != nil {
		return nil, fmt.Errorf("query session review comments by ids: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[models.SessionReviewComment])
}

// ErrReviewCommentsNotInSession is returned by ValidateAndResolveByIDs when
// one or more requested IDs do not belong to the org+session pair (either
// they don't exist or they belong to another session). The error wraps the
// missing IDs so callers can surface them to the client without leaking
// other tenants' data via existence-vs-not-found probing.
type ErrReviewCommentsNotInSession struct {
	Missing []uuid.UUID
}

func (e *ErrReviewCommentsNotInSession) Error() string {
	return fmt.Sprintf("review comment ids do not belong to this session (%d missing)", len(e.Missing))
}

// ValidateAndResolveByIDs validates that every id belongs to the org+session
// pair and then resolves the still-open ones in the same call, returning the
// rows whose state actually changed. When run against a tx-scoped store, the
// validation lookup and the resolve UPDATE share a transaction so other
// writers cannot slip a comment in between the existence check and the
// update — keeping the "either both happen or neither does" invariant that
// the SendMessage flow relies on.
//
// Returns *ErrReviewCommentsNotInSession (wrapping the missing IDs) when one
// or more IDs do not belong to this session; callers can surface the missing
// list without doing their own existence comparison. Already-resolved IDs
// are silently skipped (the underlying UPDATE filters on resolved=false).
func (s *SessionReviewCommentStore) ValidateAndResolveByIDs(
	ctx context.Context,
	orgID, sessionID uuid.UUID,
	ids []uuid.UUID,
	resolvedByPass int,
) ([]models.SessionReviewComment, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	existing, err := s.ListByIDs(ctx, orgID, sessionID, ids)
	if err != nil {
		return nil, fmt.Errorf("validate review comments: %w", err)
	}
	if len(existing) != len(ids) {
		found := make(map[uuid.UUID]struct{}, len(existing))
		for _, c := range existing {
			found[c.ID] = struct{}{}
		}
		missing := make([]uuid.UUID, 0, len(ids)-len(existing))
		for _, id := range ids {
			if _, ok := found[id]; !ok {
				missing = append(missing, id)
			}
		}
		return nil, &ErrReviewCommentsNotInSession{Missing: missing}
	}
	resolved, err := s.ResolveByIDs(ctx, orgID, sessionID, ids, resolvedByPass)
	if err != nil {
		return nil, fmt.Errorf("resolve review comments: %w", err)
	}
	return resolved, nil
}

// ResolveByIDs marks the listed comments as resolved if they aren't already,
// returning the rows whose state actually changed. Already-resolved comments
// are silently skipped, which makes the operation idempotent: callers can
// retry without flipping resolution timestamps. Scoping by org+session
// prevents cross-tenant resolution even if a foreign ID slips through.
func (s *SessionReviewCommentStore) ResolveByIDs(ctx context.Context, orgID, sessionID uuid.UUID, ids []uuid.UUID, resolvedByPass int) ([]models.SessionReviewComment, error) {
	if len(ids) == 0 {
		return nil, nil
	}
	query := `
		UPDATE session_review_comments
		SET resolved = true,
		    resolved_at = now(),
		    resolved_by_pass = @resolved_by_pass,
		    updated_at = now()
		WHERE id = ANY(@ids)
		  AND org_id = @org_id
		  AND session_id = @session_id
		  AND resolved = false
		RETURNING ` + sessionReviewCommentColumns

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"ids":              ids,
		"org_id":           orgID,
		"session_id":       sessionID,
		"resolved_by_pass": resolvedByPass,
	})
	if err != nil {
		return nil, fmt.Errorf("resolve session review comments: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[models.SessionReviewComment])
}

func (s *SessionReviewCommentStore) Update(ctx context.Context, orgID, sessionID, id uuid.UUID, body *string, resolved *bool, resolvedByPass *int) (models.SessionReviewComment, error) {
	// Build dynamic SET clauses.
	sets := "updated_at = now()"
	args := pgx.NamedArgs{
		"id":         id,
		"org_id":     orgID,
		"session_id": sessionID,
	}

	if body != nil {
		sets += ", body = @body"
		args["body"] = *body
	}
	if resolved != nil {
		sets += ", resolved = @resolved"
		args["resolved"] = *resolved
		if *resolved {
			sets += ", resolved_at = @resolved_at"
			now := time.Now()
			args["resolved_at"] = &now
			if resolvedByPass != nil {
				sets += ", resolved_by_pass = @resolved_by_pass"
				args["resolved_by_pass"] = *resolvedByPass
			}
		} else {
			sets += ", resolved_at = NULL, resolved_by_pass = NULL"
		}
	}

	query := fmt.Sprintf(`
		UPDATE session_review_comments
		SET %s
		WHERE id = @id AND org_id = @org_id AND session_id = @session_id
		RETURNING `+sessionReviewCommentColumns, sets)

	rows, err := s.db.Query(ctx, query, args)
	if err != nil {
		return models.SessionReviewComment{}, fmt.Errorf("update session review comment: %w", err)
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[models.SessionReviewComment])
}

func (s *SessionReviewCommentStore) Delete(ctx context.Context, orgID, sessionID, id uuid.UUID) error {
	query := `DELETE FROM session_review_comments WHERE id = @id AND org_id = @org_id AND session_id = @session_id`
	tag, err := s.db.Exec(ctx, query, pgx.NamedArgs{
		"id":         id,
		"org_id":     orgID,
		"session_id": sessionID,
	})
	if err != nil {
		return fmt.Errorf("delete session review comment: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("session review comment not found")
	}
	return nil
}
