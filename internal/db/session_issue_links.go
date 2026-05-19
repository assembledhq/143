package db

import (
	"context"
	"errors"
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

// sessionIssueLinkSelectColumns enriches each link row with provider-side
// hints when present. issue_workspace_slug is left-joined off Linear's
// provider_state so the frontend can render `linear.app/<slug>/issue/<KEY>`
// deep links instead of the universal redirect (which only resolves
// correctly when the user is logged into the right workspace).
const sessionIssueLinkSelectColumns = `sil.id, sil.org_id, sil.session_id, sil.issue_id, sil.role,
	sil.position, sil.added_by_user_id, sil.created_at,
	i.title AS issue_title, i.source AS issue_source,
	COALESCE(
		NULLIF(provider_state.state->>'identifier', ''),
		CASE WHEN i.source = 'linear' THEN substring(i.title from '^([A-Z][A-Z0-9_]{0,9}-[0-9]+):') END,
		i.external_id
	) AS external_id,
	i.description,
	i.repository_id, i.status AS issue_status,
	(provider_state.state->>'workspace_slug') AS issue_workspace_slug,
	(provider_state.state->>'last_skipped_reason') AS linear_last_skipped_reason,
	(provider_state.state->'primary_snapshot') AS linear_primary_snapshot`

const sessionIssueLinkFromClause = `
	FROM session_issue_links sil
	JOIN issues i ON i.id = sil.issue_id AND i.org_id = sil.org_id
	LEFT JOIN session_issue_link_provider_state provider_state
	  ON provider_state.link_id = sil.id
	  AND provider_state.org_id = sil.org_id
	  AND provider_state.provider = 'linear'`

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

// CreateAllowingNullRepo is the Linear linker's carve-out from the strict
// design 59 invariant. Linear issues frequently have no repo association at
// link time; refusing them would block most adoption. The carve-out only
// applies when issue.repository_id IS NULL — an explicit-mismatch (issue says
// repo B, session is repo A) is still rejected.
func (s *SessionIssueLinkStore) CreateAllowingNullRepo(
	ctx context.Context,
	orgID, sessionID, issueID uuid.UUID,
	role models.SessionIssueLinkRole,
	position int,
	addedByUserID *uuid.UUID,
) (uuid.UUID, error) {
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
		  -- carve-out: allow issue-only sessions and null-repo Linear issues;
		  -- explicit repo mismatch is still rejected.
		  AND (s.repository_id IS NULL OR i.repository_id IS NULL OR s.repository_id = i.repository_id)
		ON CONFLICT (session_id, issue_id) DO NOTHING
		RETURNING id`

	var linkID uuid.UUID
	err := s.db.QueryRow(ctx, query, pgx.NamedArgs{
		"org_id":           orgID,
		"session_id":       sessionID,
		"issue_id":         issueID,
		"role":             role,
		"position":         position,
		"added_by_user_id": addedByUserID,
	}).Scan(&linkID)
	if errors.Is(err, pgx.ErrNoRows) {
		// ON CONFLICT DO NOTHING swallowed the insert because the row already
		// exists. The link is still valid for this session+issue; look it up
		// so the caller can keep going (idempotent re-detection).
		existing, lookupErr := s.lookupLinkID(ctx, orgID, sessionID, issueID)
		if lookupErr == nil {
			return existing, nil
		}
		// No row, no conflict — invariant rejected the insert. Surface it.
		return uuid.Nil, ErrInvalidSessionIssueLink
	}
	if err != nil {
		return uuid.Nil, fmt.Errorf("insert session issue link (linear): %w", err)
	}
	return linkID, nil
}

func (s *SessionIssueLinkStore) lookupLinkID(ctx context.Context, orgID, sessionID, issueID uuid.UUID) (uuid.UUID, error) {
	var linkID uuid.UUID
	err := s.db.QueryRow(ctx, `
		SELECT id FROM session_issue_links
		WHERE org_id = @org_id AND session_id = @session_id AND issue_id = @issue_id`,
		pgx.NamedArgs{
			"org_id":     orgID,
			"session_id": sessionID,
			"issue_id":   issueID,
		}).Scan(&linkID)
	if err != nil {
		return uuid.Nil, ErrInvalidSessionIssueLink
	}
	return linkID, nil
}

// GetByID returns a single link enriched with issue fields. Used by the
// Linear writer when it has a link id (e.g. from a prior insert) and needs
// the issue's external_id and repository_id.
func (s *SessionIssueLinkStore) GetByID(ctx context.Context, orgID, linkID uuid.UUID) (models.SessionIssueLink, error) {
	query := `
		SELECT ` + sessionIssueLinkSelectColumns + sessionIssueLinkFromClause + `
		WHERE sil.org_id = @org_id AND sil.id = @id`

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"org_id": orgID,
		"id":     linkID,
	})
	if err != nil {
		return models.SessionIssueLink{}, fmt.Errorf("query session issue link by id: %w", err)
	}
	return pgx.CollectOneRow(rows, pgx.RowToStructByName[models.SessionIssueLink])
}

// Remove deletes a link; useful for the LinkedIssueCard "remove or repair"
// affordance when a stale link is detected. Returns the (orgID, sessionID)
// of the removed link so the caller (typically the Linear linker) can fan
// out an SSE invalidation hint.
func (s *SessionIssueLinkStore) Remove(ctx context.Context, orgID, linkID uuid.UUID) (uuid.UUID, error) {
	var sessionID uuid.UUID
	err := s.db.QueryRow(ctx, `
		DELETE FROM session_issue_links
		WHERE org_id = @org_id AND id = @id
		RETURNING session_id`,
		pgx.NamedArgs{"org_id": orgID, "id": linkID}).Scan(&sessionID)
	if errors.Is(err, pgx.ErrNoRows) {
		return uuid.Nil, ErrInvalidSessionIssueLink
	}
	if err != nil {
		return uuid.Nil, fmt.Errorf("delete session issue link: %w", err)
	}
	return sessionID, nil
}

func (s *SessionIssueLinkStore) ListBySession(ctx context.Context, orgID, sessionID uuid.UUID) ([]models.SessionIssueLink, error) {
	query := `
		SELECT ` + sessionIssueLinkSelectColumns + sessionIssueLinkFromClause + `
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

// LookupPrimaryByIssue returns the primary link for a (session, issue)
// pair. Used by callers that need the link id immediately after creating
// a session — SessionStore.Create inserts the row but doesn't return its
// id, and ListBySession + linear-search wastes a round-trip when we
// already know the issue we want.
//
// Returns pgx.ErrNoRows wrapped when the link doesn't exist.
func (s *SessionIssueLinkStore) LookupPrimaryByIssue(ctx context.Context, orgID, sessionID, issueID uuid.UUID) (models.SessionIssueLink, error) {
	query := `
		SELECT ` + sessionIssueLinkSelectColumns + sessionIssueLinkFromClause + `
		WHERE sil.org_id = @org_id
		  AND sil.session_id = @session_id
		  AND sil.issue_id = @issue_id
		  AND sil.role = 'primary'
		LIMIT 1`

	row, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"org_id":     orgID,
		"session_id": sessionID,
		"issue_id":   issueID,
	})
	if err != nil {
		return models.SessionIssueLink{}, fmt.Errorf("query primary session issue link: %w", err)
	}
	link, err := pgx.CollectOneRow(row, pgx.RowToStructByName[models.SessionIssueLink])
	if err != nil {
		return models.SessionIssueLink{}, fmt.Errorf("collect primary session issue link: %w", err)
	}
	return link, nil
}

func (s *SessionIssueLinkStore) ListBySessionIDs(ctx context.Context, orgID uuid.UUID, sessionIDs []uuid.UUID) (map[uuid.UUID][]models.SessionIssueLink, error) {
	if len(sessionIDs) == 0 {
		return map[uuid.UUID][]models.SessionIssueLink{}, nil
	}

	query := `
		SELECT ` + sessionIssueLinkSelectColumns + sessionIssueLinkFromClause + `
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
