package db

import (
	"context"
	"fmt"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

var ErrInvalidSessionIssueLink = fmt.Errorf("invalid session issue link")

type SessionIssueLinkStore struct {
	db DBTX
}

func NewSessionIssueLinkStore(db DBTX) *SessionIssueLinkStore {
	return &SessionIssueLinkStore{db: db}
}

const sessionIssueLinkSelectColumns = `sil.id, sil.org_id, sil.session_id, sil.issue_id, sil.role,
	sil.position, sil.added_by_user_id, sil.created_at,
	i.title AS issue_title, i.source AS issue_source, i.external_id, i.description,
	i.repository_id, i.status AS issue_status`

func (s *SessionIssueLinkStore) Create(
	ctx context.Context,
	orgID, sessionID, issueID uuid.UUID,
	role models.SessionIssueLinkRole,
	position int,
	addedByUserID *uuid.UUID,
) error {
	query := `
		INSERT INTO session_issue_links (
			org_id, session_id, issue_id, role, position, added_by_user_id
		)
		SELECT @org_id, s.id, i.id, @role, @position, @added_by_user_id
		FROM sessions s
		JOIN issues i ON i.id = @issue_id AND i.org_id = @org_id
		WHERE s.id = @session_id
		  AND s.org_id = @org_id
		  AND s.deleted_at IS NULL
		  AND s.repository_id IS NOT NULL
		  AND i.repository_id IS NOT NULL
		  AND s.repository_id = i.repository_id`

	tag, err := s.db.Exec(ctx, query, pgx.NamedArgs{
		"org_id":           orgID,
		"session_id":       sessionID,
		"issue_id":         issueID,
		"role":             role,
		"position":         position,
		"added_by_user_id": addedByUserID,
	})
	if err != nil {
		return fmt.Errorf("insert session issue link: %w", err)
	}
	if tag.RowsAffected() == 0 {
		return ErrInvalidSessionIssueLink
	}
	return nil
}

func (s *SessionIssueLinkStore) ListBySession(ctx context.Context, orgID, sessionID uuid.UUID) ([]models.SessionIssueLink, error) {
	query := `
		SELECT ` + sessionIssueLinkSelectColumns + `
		FROM session_issue_links sil
		JOIN issues i ON i.id = sil.issue_id AND i.org_id = sil.org_id
		WHERE sil.org_id = @org_id AND sil.session_id = @session_id
		ORDER BY
			CASE WHEN sil.role = 'primary' THEN 0 ELSE 1 END,
			sil.position ASC, sil.created_at ASC, sil.issue_id ASC`

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"org_id":     orgID,
		"session_id": sessionID,
	})
	if err != nil {
		return nil, fmt.Errorf("query session issue links: %w", err)
	}
	return pgx.CollectRows(rows, pgx.RowToStructByName[models.SessionIssueLink])
}

func (s *SessionIssueLinkStore) ListBySessionIDs(ctx context.Context, orgID uuid.UUID, sessionIDs []uuid.UUID) (map[uuid.UUID][]models.SessionIssueLink, error) {
	if len(sessionIDs) == 0 {
		return map[uuid.UUID][]models.SessionIssueLink{}, nil
	}

	query := `
		SELECT ` + sessionIssueLinkSelectColumns + `
		FROM session_issue_links sil
		JOIN issues i ON i.id = sil.issue_id AND i.org_id = sil.org_id
		WHERE sil.org_id = @org_id AND sil.session_id = ANY(@session_ids)
		ORDER BY
			CASE WHEN sil.role = 'primary' THEN 0 ELSE 1 END,
			sil.position ASC, sil.created_at ASC, sil.issue_id ASC`

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"org_id":      orgID,
		"session_ids": sessionIDs,
	})
	if err != nil {
		return nil, fmt.Errorf("query session issue links by session ids: %w", err)
	}
	links, err := pgx.CollectRows(rows, pgx.RowToStructByName[models.SessionIssueLink])
	if err != nil {
		return nil, err
	}

	grouped := make(map[uuid.UUID][]models.SessionIssueLink, len(sessionIDs))
	for _, link := range links {
		grouped[link.SessionID] = append(grouped[link.SessionID], link)
	}
	return grouped, nil
}
