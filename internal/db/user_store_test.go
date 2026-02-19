package db

import (
	"context"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/require"
)

var userColumns = []string{
	"id", "org_id", "email", "name", "role", "github_id", "github_login", "avatar_url", "password_hash", "google_id", "created_at",
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
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
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
							AddRow(userID, orgID, "test@example.com", "Test User", "member", &ghID, &ghLogin, &avatarURL, nil, nil, now),
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
			require.Equal(t, "member", user.Role, "should return the correct user role")
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
				mock.ExpectQuery("SELECT .+ FROM users WHERE email").
					WithArgs(pgxmock.AnyArg()).
					WillReturnRows(
						pgxmock.NewRows(userColumns).
							AddRow(userID, orgID, "found@example.com", "Found User", "admin", nil, nil, nil, nil, nil, now),
					)
			},
		},
		{
			name: "returns error when user not found by email",
			setupMock: func(mock pgxmock.PgxPoolIface, userID, orgID uuid.UUID, now time.Time) {
				mock.ExpectQuery("SELECT .+ FROM users WHERE email").
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
							AddRow(userID, orgID, "google@example.com", "Google User", "admin", nil, nil, nil, nil, &googleID, now),
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
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	err = store.LinkGitHubAccount(context.Background(), userID, orgID, int64(99999), "linked-user", "https://avatar.com/linked.png")
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
							AddRow(userID, orgID, "octocat@example.com", "Octocat", "admin", &ghID, &ghLogin, &avatarURL, nil, nil, now),
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
