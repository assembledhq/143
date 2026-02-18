package db

import (
	"context"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var userColumns = []string{
	"id", "org_id", "email", "name", "role", "github_id", "github_login", "avatar_url", "created_at",
}

func TestUserStore_UpsertFromGitHub_Success(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
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

	// 7 named args: org_id, email, name, role, github_id, github_login, avatar_url
	mock.ExpectQuery("INSERT INTO users").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows([]string{"id", "created_at"}).
				AddRow(generatedID, now),
		)

	err = store.UpsertFromGitHub(context.Background(), user)
	require.NoError(t, err)
	assert.Equal(t, generatedID, user.ID)
	assert.Equal(t, now, user.CreatedAt)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestUserStore_GetByID_Success(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewUserStore(mock)
	orgID := uuid.New()
	userID := uuid.New()
	now := time.Now()

	ghID := int64(12345)
	ghLogin := "testuser"
	avatarURL := "https://github.com/avatar.png"

	// 2 named args: id, org_id
	mock.ExpectQuery("SELECT .+ FROM users WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(userColumns).
				AddRow(userID, orgID, "test@example.com", "Test User", "member", &ghID, &ghLogin, &avatarURL, now),
		)

	user, err := store.GetByID(context.Background(), orgID, userID)
	require.NoError(t, err)
	assert.Equal(t, userID, user.ID)
	assert.Equal(t, orgID, user.OrgID)
	assert.Equal(t, "test@example.com", user.Email)
	assert.Equal(t, "Test User", user.Name)
	assert.Equal(t, "member", user.Role)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestUserStore_GetByID_NotFound(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewUserStore(mock)
	orgID := uuid.New()
	userID := uuid.New()

	// 2 named args: id, org_id
	mock.ExpectQuery("SELECT .+ FROM users WHERE id").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(userColumns))

	_, err = store.GetByID(context.Background(), orgID, userID)
	assert.Error(t, err)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestUserStore_GetByGitHubID_Success(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewUserStore(mock)
	userID := uuid.New()
	orgID := uuid.New()
	now := time.Now()

	ghID := int64(67890)
	ghLogin := "octocat"
	avatarURL := "https://github.com/octocat.png"

	// 1 named arg: github_id
	mock.ExpectQuery("SELECT .+ FROM users WHERE github_id").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(
			pgxmock.NewRows(userColumns).
				AddRow(userID, orgID, "octocat@example.com", "Octocat", "admin", &ghID, &ghLogin, &avatarURL, now),
		)

	user, err := store.GetByGitHubID(context.Background(), int64(67890))
	require.NoError(t, err)
	assert.Equal(t, userID, user.ID)
	assert.Equal(t, "octocat@example.com", user.Email)
	assert.Equal(t, "Octocat", user.Name)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestUserStore_GetByGitHubID_NotFound(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	store := NewUserStore(mock)

	// 1 named arg: github_id
	mock.ExpectQuery("SELECT .+ FROM users WHERE github_id").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(pgxmock.NewRows(userColumns))

	_, err = store.GetByGitHubID(context.Background(), int64(99999))
	assert.Error(t, err)
	assert.NoError(t, mock.ExpectationsWereMet())
}
