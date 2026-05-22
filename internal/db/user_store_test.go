package db

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

var userColumns = []string{
	"id", "org_id", "email", "name", "role", "github_id", "github_login", "github_noreply_email", "avatar_url", "password_hash", "google_id", "created_at",
}

var userColumnsWithSettings = []string{
	"id", "org_id", "email", "name", "role", "github_id", "github_login", "avatar_url", "google_id", "created_at", "settings",
}

func ptrUUID(id uuid.UUID) *uuid.UUID {
	return &id
}

func TestUserStore_UpsertFromGitHub(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := NewUserStore(mock)
	now := time.Now()
	generatedID := uuid.New()

	ghID := int64(12345)
	ghLogin := "testuser"
	avatarURL := "https://github.com/avatar.png"

	user := &models.User{
		OrgID:       uuid.New(),
		Email:       "test@example.com",
		Name:        "Test User",
		Role:        "member",
		GitHubID:    &ghID,
		GitHubLogin: &ghLogin,
		AvatarURL:   &avatarURL,
	}

	mock.ExpectQuery("INSERT INTO users").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{"id", "created_at"}).
				AddRow(generatedID, now),
		)

	err = store.UpsertFromGitHub(context.Background(), user)
	require.NoError(t, err, "UpsertFromGitHub should not return an error")
	require.Equal(t, generatedID, user.ID, "should set the generated ID on the user")
	require.Equal(t, now, user.CreatedAt, "should set the created_at timestamp on the user")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestUserStore_GetByID(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		setupMock func(mock pgxmock.PgxPoolIface, orgID, userID uuid.UUID, now time.Time)
		expectErr bool
	}{
		{
			name: "returns user when found",
			setupMock: func(mock pgxmock.PgxPoolIface, orgID, userID uuid.UUID, now time.Time) {
				ghID := int64(12345)
				ghLogin := "testuser"
				avatarURL := "https://github.com/avatar.png"

				mock.ExpectQuery("SELECT .+ FROM users WHERE id").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(
						pgxmock.NewRows(userColumns).
							AddRow(userID, orgID, "test@example.com", "Test User", "member", &ghID, &ghLogin, nil, &avatarURL, nil, nil, now),
					)
			},
		},
		{
			name: "returns error when user not found",
			setupMock: func(mock pgxmock.PgxPoolIface, orgID, userID uuid.UUID, now time.Time) {
				mock.ExpectQuery("SELECT .+ FROM users WHERE id").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows(userColumns))
			},
			expectErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "should create mock pool")
			defer mock.Close()

			store := NewUserStore(mock)
			orgID := uuid.New()
			userID := uuid.New()
			now := time.Now()
			tt.setupMock(mock, orgID, userID, now)

			user, err := store.GetByID(context.Background(), orgID, userID)
			if tt.expectErr {
				require.Error(t, err, "GetByID should return an error when user is not found")
				return
			}
			require.NoError(t, err, "GetByID should not return an error")
			require.Equal(t, userID, user.ID, "should return the correct user ID")
			require.Equal(t, orgID, user.OrgID, "should return the correct org ID")
			require.Equal(t, "test@example.com", user.Email, "should return the correct user email")
			require.Equal(t, "Test User", user.Name, "should return the correct user name")
			require.Equal(t, models.RoleMember, user.Role, "should return the correct user role")
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

func TestUserStore_GetByEmail(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		setupMock func(mock pgxmock.PgxPoolIface, userID, orgID uuid.UUID, now time.Time)
		expectErr bool
	}{
		{
			name: "returns user when found by email",
			setupMock: func(mock pgxmock.PgxPoolIface, userID, orgID uuid.UUID, now time.Time) {
				mock.ExpectQuery(`(?s)SELECT .+ FROM users WHERE LOWER\(email\)`).
					WithArgs(pgxmock.AnyArg()).
					WillReturnRows(
						pgxmock.NewRows(userColumns).
							AddRow(userID, orgID, "found@example.com", "Found User", "admin", nil, nil, nil, nil, nil, nil, now),
					)
			},
		},
		{
			name: "returns error when user not found by email",
			setupMock: func(mock pgxmock.PgxPoolIface, userID, orgID uuid.UUID, now time.Time) {
				mock.ExpectQuery(`(?s)SELECT .+ FROM users WHERE LOWER\(email\)`).
					WithArgs(pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows(userColumns))
			},
			expectErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "should create mock pool")
			defer mock.Close()

			store := NewUserStore(mock)
			userID := uuid.New()
			orgID := uuid.New()
			now := time.Now()
			tt.setupMock(mock, userID, orgID, now)

			user, err := store.GetByEmail(context.Background(), "found@example.com")
			if tt.expectErr {
				require.Error(t, err, "GetByEmail should return an error when user is not found")
				return
			}
			require.NoError(t, err, "GetByEmail should not return an error")
			require.Equal(t, userID, user.ID, "should return the correct user ID")
			require.Equal(t, "found@example.com", user.Email, "should return the correct user email")
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

func TestUserStore_GetByGoogleID(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		setupMock func(mock pgxmock.PgxPoolIface, userID, orgID uuid.UUID, now time.Time)
		expectErr bool
	}{
		{
			name: "returns user when found by Google ID",
			setupMock: func(mock pgxmock.PgxPoolIface, userID, orgID uuid.UUID, now time.Time) {
				googleID := "google-sub-123"
				mock.ExpectQuery("SELECT .+ FROM users WHERE google_id").
					WithArgs(pgxmock.AnyArg()).
					WillReturnRows(
						pgxmock.NewRows(userColumns).
							AddRow(userID, orgID, "google@example.com", "Google User", "admin", nil, nil, nil, nil, nil, &googleID, now),
					)
			},
		},
		{
			name: "returns error when user not found by Google ID",
			setupMock: func(mock pgxmock.PgxPoolIface, userID, orgID uuid.UUID, now time.Time) {
				mock.ExpectQuery("SELECT .+ FROM users WHERE google_id").
					WithArgs(pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows(userColumns))
			},
			expectErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "should create mock pool")
			defer mock.Close()

			store := NewUserStore(mock)
			userID := uuid.New()
			orgID := uuid.New()
			now := time.Now()
			tt.setupMock(mock, userID, orgID, now)

			user, err := store.GetByGoogleID(context.Background(), "google-sub-123")
			if tt.expectErr {
				require.Error(t, err, "GetByGoogleID should return an error when user is not found")
				return
			}
			require.NoError(t, err, "GetByGoogleID should not return an error")
			require.Equal(t, userID, user.ID, "should return the correct user ID")
			require.Equal(t, "google@example.com", user.Email, "should return the correct user email")
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

func TestUserStore_GetByIDGlobal(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewUserStore(mock)
	userID := uuid.New()
	orgID := uuid.New()
	now := time.Now()

	mock.ExpectQuery(`SELECT .+ FROM users\s+WHERE id = @id`).
		WithArgs(userID).
		WillReturnRows(pgxmock.NewRows(userColumns).
			AddRow(userID, orgID, "u@example.com", "Name", "admin", nil, nil, nil, nil, nil, nil, now))

	u, err := store.GetByIDGlobal(context.Background(), userID)
	require.NoError(t, err)
	require.Equal(t, userID, u.ID)
	require.Equal(t, orgID, u.OrgID)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUserStore_GetByIDGlobalWithSettings(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewUserStore(mock)
	userID := uuid.New()
	orgID := uuid.New()
	now := time.Now()
	settings := []byte(`{"coding_agent_reasoning_defaults":{"codex":"xhigh"}}`)

	mock.ExpectQuery(`SELECT .+ FROM users\s+WHERE id = @id`).
		WithArgs(userID).
		WillReturnRows(pgxmock.NewRows(userColumnsWithSettings).
			AddRow(userID, orgID, "u@example.com", "Name", "admin", nil, nil, nil, nil, now, settings))

	u, err := store.GetByIDGlobalWithSettings(context.Background(), userID)
	require.NoError(t, err, "GetByIDGlobalWithSettings should not return an error")
	require.Equal(t, userID, u.ID, "GetByIDGlobalWithSettings should return the requested user id")
	require.Equal(t, models.UserSettings{
		CodingAgentReasoningDefaults: map[models.AgentType]models.ReasoningEffort{
			models.AgentTypeCodex: models.ReasoningEffortXHigh,
		},
	}, u.Settings, "GetByIDGlobalWithSettings should decode typed user settings")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestUserStore_GetByIDGlobalWithSettings_InvalidSettings(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create pgxmock pool without error")
	defer mock.Close()

	store := NewUserStore(mock)
	userID := uuid.New()
	orgID := uuid.New()
	now := time.Now()

	mock.ExpectQuery(`SELECT .+ FROM users\s+WHERE id = @id`).
		WithArgs(userID).
		WillReturnRows(pgxmock.NewRows(userColumnsWithSettings).
			AddRow(userID, orgID, "u@example.com", "Name", "admin", nil, nil, nil, nil, now, []byte(`{"coding_agent_reasoning_defaults":{"codex":"max"}}`)))

	_, err = store.GetByIDGlobalWithSettings(context.Background(), userID)
	require.Error(t, err, "GetByIDGlobalWithSettings should reject invalid stored settings")
	require.Contains(t, err.Error(), "parse user settings", "GetByIDGlobalWithSettings should wrap settings parse failures")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestUserStore_GetByIDGlobalWithSettings_QueryError(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create pgxmock pool without error")
	defer mock.Close()

	store := NewUserStore(mock)
	userID := uuid.New()

	mock.ExpectQuery(`SELECT .+ FROM users\s+WHERE id = @id`).
		WithArgs(userID).
		WillReturnError(errors.New("query failed"))

	_, err = store.GetByIDGlobalWithSettings(context.Background(), userID)
	require.Error(t, err, "GetByIDGlobalWithSettings should return query failures")
	require.Contains(t, err.Error(), "query user with settings", "GetByIDGlobalWithSettings should wrap query failures")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestUserStore_GetByIDGlobalWithSettings_ScanError(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create pgxmock pool without error")
	defer mock.Close()

	store := NewUserStore(mock)
	userID := uuid.New()
	orgID := uuid.New()

	mock.ExpectQuery(`SELECT .+ FROM users\s+WHERE id = @id`).
		WithArgs(userID).
		WillReturnRows(pgxmock.NewRows(userColumnsWithSettings).
			AddRow(userID, orgID, "u@example.com", "Name", "admin", nil, nil, nil, nil, "not-a-time", []byte(`{}`)))

	_, err = store.GetByIDGlobalWithSettings(context.Background(), userID)
	require.Error(t, err, "GetByIDGlobalWithSettings should surface scan failures")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestUserStore_GetLastOrgID(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		lastOrgID *uuid.UUID
		expectNil bool
		expectErr bool
	}{
		{
			name:      "returns stored last org id",
			lastOrgID: ptrUUID(uuid.New()),
		},
		{
			name:      "returns nil when preference is unset",
			expectNil: true,
		},
		{
			name:      "returns error when user is missing",
			expectErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "should create mock pool")
			defer mock.Close()

			store := NewUserStore(mock)
			userID := uuid.New()

			query := mock.ExpectQuery(`SELECT last_org_id FROM users WHERE id = @id`).
				WithArgs(userID)
			switch {
			case tt.expectErr:
				query.WillReturnRows(pgxmock.NewRows([]string{"last_org_id"}))
			case tt.expectNil:
				query.WillReturnRows(
					pgxmock.NewRows([]string{"last_org_id"}).
						AddRow(nil),
				)
			default:
				query.WillReturnRows(
					pgxmock.NewRows([]string{"last_org_id"}).
						AddRow(tt.lastOrgID.String()),
				)
			}

			lastOrgID, err := store.GetLastOrgID(context.Background(), userID)
			if tt.expectErr {
				require.Error(t, err, "GetLastOrgID should return an error when the user row is missing")
				require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
				return
			}

			require.NoError(t, err, "GetLastOrgID should not return an error")
			if tt.expectNil {
				require.Nil(t, lastOrgID, "GetLastOrgID should return nil when the preference is unset")
			} else {
				require.NotNil(t, lastOrgID, "GetLastOrgID should return the stored org id")
				require.Equal(t, *tt.lastOrgID, *lastOrgID, "GetLastOrgID should return the stored org id")
			}
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

func TestUserStore_UpdateLastOrgID(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		lastOrgID *uuid.UUID
	}{
		{
			name:      "stores a concrete org id",
			lastOrgID: ptrUUID(uuid.New()),
		},
		{
			name:      "clears the stored preference",
			lastOrgID: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "should create mock pool")
			defer mock.Close()

			store := NewUserStore(mock)
			userID := uuid.New()

			mock.ExpectExec(`UPDATE users SET last_org_id = @last_org_id WHERE id = @id`).
				WithArgs(tt.lastOrgID, userID).
				WillReturnResult(pgxmock.NewResult("UPDATE", 1))

			err = store.UpdateLastOrgID(context.Background(), userID, tt.lastOrgID)
			require.NoError(t, err, "UpdateLastOrgID should not return an error")
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

// GetByIDGlobal wraps Query errors with a "query user" prefix so callers
// can distinguish "database unreachable" from "no such row".
func TestUserStore_GetByIDGlobal_QueryError(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewUserStore(mock)
	mock.ExpectQuery(`SELECT .+ FROM users\s+WHERE id = @id`).
		WithArgs(pgxmock.AnyArg()).
		WillReturnError(errors.New("db down"))

	_, err = store.GetByIDGlobal(context.Background(), uuid.New())
	require.Error(t, err)
	require.Contains(t, err.Error(), "query user")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUserStore_GetByIDGlobal_NotFound(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewUserStore(mock)
	mock.ExpectQuery(`SELECT .+ FROM users\s+WHERE id = @id`).
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(userColumns))

	_, err = store.GetByIDGlobal(context.Background(), uuid.New())
	require.Error(t, err)
	require.True(t, errors.Is(err, pgx.ErrNoRows))
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUserStore_CreateWithPassword(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := NewUserStore(mock)
	now := time.Now()
	generatedID := uuid.New()
	hash := "$2a$10$fakehash"

	user := &models.User{
		OrgID:        uuid.New(),
		Email:        "new@example.com",
		Name:         "New User",
		Role:         "admin",
		PasswordHash: &hash,
	}

	mock.ExpectQuery("INSERT INTO users").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{"id", "created_at"}).
				AddRow(generatedID, now),
		)

	err = store.CreateWithPassword(context.Background(), user)
	require.NoError(t, err, "CreateWithPassword should not return an error")
	require.Equal(t, generatedID, user.ID, "should set the generated ID on the user")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestUserStore_UpsertFromGoogle(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := NewUserStore(mock)
	now := time.Now()
	generatedID := uuid.New()
	googleID := "google-sub-456"
	avatarURL := "https://lh3.googleusercontent.com/photo.jpg"

	user := &models.User{
		OrgID:     uuid.New(),
		Email:     "google@example.com",
		Name:      "Google User",
		Role:      "admin",
		GoogleID:  &googleID,
		AvatarURL: &avatarURL,
	}

	mock.ExpectQuery("INSERT INTO users").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{"id", "created_at"}).
				AddRow(generatedID, now),
		)

	err = store.UpsertFromGoogle(context.Background(), user)
	require.NoError(t, err, "UpsertFromGoogle should not return an error")
	require.Equal(t, generatedID, user.ID, "should set the generated ID on the user")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestUserStore_LinkGitHubAccount(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := NewUserStore(mock)
	userID := uuid.New()
	orgID := uuid.New()

	mock.ExpectExec("UPDATE users").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	err = store.LinkGitHubAccount(context.Background(), userID, orgID, int64(99999), "linked-user", "https://avatar.com/linked.png", "99999+linked-user@users.noreply.github.com")
	require.NoError(t, err, "LinkGitHubAccount should not return an error")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestUserStore_LinkGoogleAccount(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := NewUserStore(mock)
	userID := uuid.New()
	orgID := uuid.New()

	mock.ExpectExec("UPDATE users").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	err = store.LinkGoogleAccount(context.Background(), userID, orgID, "google-sub-linked", "https://avatar.com/google.png")
	require.NoError(t, err, "LinkGoogleAccount should not return an error")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestUserStore_UpdateSettings(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := NewUserStore(mock)
	userID := uuid.New()
	settings := models.UserSettings{
		CodingAgentReasoningDefaults: map[models.AgentType]models.ReasoningEffort{
			models.AgentTypeClaudeCode: models.ReasoningEffortMax,
		},
	}

	mock.ExpectExec("UPDATE users SET settings = @settings").
		WithArgs(pgxmock.AnyArg(), userID).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	err = store.UpdateSettings(context.Background(), userID, settings)
	require.NoError(t, err, "UpdateSettings should not return an error")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestUserStore_UpdateSettings_ErrorPaths(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		settings  models.UserSettings
		setupMock func(mock pgxmock.PgxPoolIface, userID uuid.UUID)
		wantErr   string
	}{
		{
			name: "returns marshal error for invalid settings",
			settings: models.UserSettings{
				CodingAgentReasoningDefaults: map[models.AgentType]models.ReasoningEffort{
					models.AgentTypeCodex: models.ReasoningEffortMax,
				},
			},
			wantErr: "marshal user settings",
		},
		{
			name: "returns exec error",
			settings: models.UserSettings{
				CodingAgentReasoningDefaults: map[models.AgentType]models.ReasoningEffort{
					models.AgentTypeClaudeCode: models.ReasoningEffortMax,
				},
			},
			setupMock: func(mock pgxmock.PgxPoolIface, userID uuid.UUID) {
				mock.ExpectExec("UPDATE users SET settings = @settings").
					WithArgs(pgxmock.AnyArg(), userID).
					WillReturnError(errors.New("write failed"))
			},
			wantErr: "update user settings",
		},
		{
			name: "returns no rows when user is missing",
			settings: models.UserSettings{
				CodingAgentReasoningDefaults: map[models.AgentType]models.ReasoningEffort{
					models.AgentTypeClaudeCode: models.ReasoningEffortMax,
				},
			},
			setupMock: func(mock pgxmock.PgxPoolIface, userID uuid.UUID) {
				mock.ExpectExec("UPDATE users SET settings = @settings").
					WithArgs(pgxmock.AnyArg(), userID).
					WillReturnResult(pgxmock.NewResult("UPDATE", 0))
			},
			wantErr: pgx.ErrNoRows.Error(),
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "should create mock pool")
			defer mock.Close()

			store := NewUserStore(mock)
			userID := uuid.New()
			if tt.setupMock != nil {
				tt.setupMock(mock, userID)
			}

			err = store.UpdateSettings(context.Background(), userID, tt.settings)
			require.Error(t, err, "UpdateSettings should return the expected error")
			require.Contains(t, err.Error(), tt.wantErr, "UpdateSettings should describe the failure")
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

func TestUserStore_GetByGitHubID(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		setupMock func(mock pgxmock.PgxPoolIface, userID, orgID uuid.UUID, now time.Time)
		expectErr bool
	}{
		{
			name: "returns user when found by GitHub ID",
			setupMock: func(mock pgxmock.PgxPoolIface, userID, orgID uuid.UUID, now time.Time) {
				ghID := int64(67890)
				ghLogin := "octocat"
				avatarURL := "https://github.com/octocat.png"

				mock.ExpectQuery("SELECT .+ FROM users WHERE github_id").
					WithArgs(pgxmock.AnyArg()).
					WillReturnRows(
						pgxmock.NewRows(userColumns).
							AddRow(userID, orgID, "octocat@example.com", "Octocat", "admin", &ghID, &ghLogin, nil, &avatarURL, nil, nil, now),
					)
			},
		},
		{
			name: "returns error when user not found by GitHub ID",
			setupMock: func(mock pgxmock.PgxPoolIface, userID, orgID uuid.UUID, now time.Time) {
				mock.ExpectQuery("SELECT .+ FROM users WHERE github_id").
					WithArgs(pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows(userColumns))
			},
			expectErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "should create mock pool")
			defer mock.Close()

			store := NewUserStore(mock)
			userID := uuid.New()
			orgID := uuid.New()
			now := time.Now()
			tt.setupMock(mock, userID, orgID, now)

			user, err := store.GetByGitHubID(context.Background(), int64(67890))
			if tt.expectErr {
				require.Error(t, err, "GetByGitHubID should return an error when user is not found")
				return
			}
			require.NoError(t, err, "GetByGitHubID should not return an error")
			require.Equal(t, userID, user.ID, "should return the correct user ID")
			require.Equal(t, "octocat@example.com", user.Email, "should return the correct user email")
			require.Equal(t, "Octocat", user.Name, "should return the correct user name")
			require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
		})
	}
}

func TestUserStore_ListByOrg(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "should create mock pool")
	defer mock.Close()

	store := NewUserStore(mock)
	orgID := uuid.New()
	userID1 := uuid.New()
	userID2 := uuid.New()
	now := time.Now()

	mock.ExpectQuery("SELECT .+ FROM users WHERE org_id").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(userColumns).
				AddRow(userID1, orgID, "alice@example.com", "Alice", "admin", nil, nil, nil, nil, nil, nil, now).
				AddRow(userID2, orgID, "bob@example.com", "Bob", "member", nil, nil, nil, nil, nil, nil, now),
		)

	users, err := store.ListByOrg(context.Background(), orgID)
	require.NoError(t, err, "ListByOrg should not return an error")
	require.Len(t, users, 2, "should return both users")
	require.Equal(t, "Alice", users[0].Name)
	require.Equal(t, "Bob", users[1].Name)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUserStore_ListByOrgViaMemberships(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewUserStore(mock)
	orgID := uuid.New()
	userID1 := uuid.New()
	userID2 := uuid.New()
	now := time.Now()
	membershipTime1 := now.Add(-2 * time.Hour)
	membershipTime2 := now.Add(-time.Hour)

	cols := append(append([]string{}, userColumns...), "membership_created_at")
	mock.ExpectQuery("(?s)FROM users u.+JOIN organization_memberships m").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(cols).
				AddRow(userID1, orgID, "alice@example.com", "Alice", "admin", nil, nil, nil, nil, nil, nil, now, membershipTime1).
				AddRow(userID2, orgID, "bob@example.com", "Bob", "member", nil, nil, nil, nil, nil, nil, now, membershipTime2),
		)

	users, lastMembershipTime, err := store.ListByOrgViaMemberships(context.Background(), orgID, MembershipPageFilters{Limit: 100})
	require.NoError(t, err)
	require.Len(t, users, 2)
	require.Equal(t, "Alice", users[0].Name)
	require.Equal(t, models.RoleAdmin, users[0].Role)
	require.Equal(t, "Bob", users[1].Name)
	require.Equal(t, models.RoleMember, users[1].Role)
	require.Equal(t, membershipTime2.UTC(), lastMembershipTime.UTC())
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUserStore_ListByOrgViaMemberships_QueryError(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	mock.ExpectQuery("(?s)FROM users u.+JOIN organization_memberships m").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnError(errors.New("boom"))

	_, _, err = NewUserStore(mock).ListByOrgViaMemberships(context.Background(), uuid.New(), MembershipPageFilters{Limit: 100})
	require.Error(t, err)
	require.Contains(t, err.Error(), "query users via memberships")
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUserStore_IsGitHubLoginMemberOfOrg_True(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	mock.ExpectQuery("(?s)SELECT EXISTS.+JOIN organization_memberships").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(true))

	got, err := NewUserStore(mock).IsGitHubLoginMemberOfOrg(context.Background(), "octocat", uuid.New())
	require.NoError(t, err)
	require.True(t, got)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUserStore_IsGitHubLoginMemberOfOrg_False(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	mock.ExpectQuery("(?s)SELECT EXISTS.+JOIN organization_memberships").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows([]string{"exists"}).AddRow(false))

	got, err := NewUserStore(mock).IsGitHubLoginMemberOfOrg(context.Background(), "octocat", uuid.New())
	require.NoError(t, err)
	require.False(t, got)
	require.NoError(t, mock.ExpectationsWereMet())
}

func TestUserStore_IsGitHubLoginMemberOfOrg_QueryError(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	mock.ExpectQuery("(?s)SELECT EXISTS.+JOIN organization_memberships").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnError(errors.New("boom"))

	_, err = NewUserStore(mock).IsGitHubLoginMemberOfOrg(context.Background(), "octocat", uuid.New())
	require.Error(t, err)
	require.Contains(t, err.Error(), "check github login membership")
	require.NoError(t, mock.ExpectationsWereMet())
}
