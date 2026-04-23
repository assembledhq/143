package db

import (
	"context"
	"fmt"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/assembledhq/143/internal/models"
)

type UserNotificationPreferenceStore struct {
	db DBTX
}

func NewUserNotificationPreferenceStore(db DBTX) *UserNotificationPreferenceStore {
	return &UserNotificationPreferenceStore{db: db}
}

func (s *UserNotificationPreferenceStore) GetByUser(ctx context.Context, orgID, userID uuid.UUID) (models.UserNotificationPreference, error) {
	query := `
		SELECT org_id, user_id, session_completion_browser_enabled, created_at, updated_at
		FROM user_notification_preferences
		WHERE org_id = @org_id AND user_id = @user_id`

	rows, err := s.db.Query(ctx, query, pgx.NamedArgs{"org_id": orgID, "user_id": userID})
	if err != nil {
		return models.UserNotificationPreference{}, fmt.Errorf("query user notification preference: %w", err)
	}

	pref, collectErr := pgx.CollectOneRow(rows, pgx.RowToStructByName[models.UserNotificationPreference])
	if collectErr != nil {
		if collectErr == pgx.ErrNoRows {
			now := time.Now().UTC()
			return models.UserNotificationPreference{
				OrgID:                           orgID,
				UserID:                          userID,
				SessionCompletionBrowserEnabled: false,
				CreatedAt:                       now,
				UpdatedAt:                       now,
			}, nil
		}
		return models.UserNotificationPreference{}, fmt.Errorf("collect user notification preference: %w", collectErr)
	}

	return pref, nil
}

func (s *UserNotificationPreferenceStore) Upsert(ctx context.Context, orgID, userID uuid.UUID, sessionCompletionBrowserEnabled bool) error {
	query := `
		INSERT INTO user_notification_preferences (org_id, user_id, session_completion_browser_enabled)
		VALUES (@org_id, @user_id, @session_completion_browser_enabled)
		ON CONFLICT (org_id, user_id)
		DO UPDATE
		SET session_completion_browser_enabled = EXCLUDED.session_completion_browser_enabled,
		    updated_at = now()`

	_, err := s.db.Exec(ctx, query, pgx.NamedArgs{
		"org_id":                             orgID,
		"user_id":                            userID,
		"session_completion_browser_enabled": sessionCompletionBrowserEnabled,
	})
	if err != nil {
		return fmt.Errorf("upsert user notification preference: %w", err)
	}

	return nil
}
