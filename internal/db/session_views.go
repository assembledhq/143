package db

import (
	"context"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type SessionViewStore struct {
	db DBTX
}

func NewSessionViewStore(db DBTX) *SessionViewStore {
	return &SessionViewStore{db: db}
}

// Upsert records (or updates) the time the user last viewed a session.
func (s *SessionViewStore) Upsert(ctx context.Context, userID, sessionID, orgID uuid.UUID) error {
	query := `
		INSERT INTO session_views (user_id, session_id, org_id, last_viewed_at)
		VALUES (@user_id, @session_id, @org_id, now())
		ON CONFLICT (user_id, session_id)
		DO UPDATE SET last_viewed_at = now()`

	_, err := s.db.Exec(ctx, query, pgx.NamedArgs{
		"user_id":    userID,
		"session_id": sessionID,
		"org_id":     orgID,
	})
	return err
}

// BatchGetLastViewed returns a map of session_id → last_viewed_at for the
// given user and set of session IDs.
// lint:allow-no-orgid reason="per-user view state; user_id is globally unique and session_ids come from org-scoped context"
func (s *SessionViewStore) BatchGetLastViewed(ctx context.Context, userID uuid.UUID, sessionIDs []uuid.UUID) (map[uuid.UUID]time.Time, error) {
	if len(sessionIDs) == 0 {
		return nil, nil
	}

	query := `
		SELECT session_id, last_viewed_at
		FROM session_views
		WHERE user_id = @user_id AND session_id = ANY(@session_ids)`

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{
		"user_id":     userID,
		"session_ids": sessionIDs,
	})
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make(map[uuid.UUID]time.Time)
	for rows.Next() {
		var sid uuid.UUID
		var viewedAt time.Time
		if err := rows.Scan(&sid, &viewedAt); err != nil {
			return nil, err
		}
		result[sid] = viewedAt
	}
	return result, rows.Err()
}
