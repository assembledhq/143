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
