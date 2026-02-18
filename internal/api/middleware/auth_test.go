package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestUserFromContext_NoUser(t *testing.T) {
	ctx := context.Background()
	user := UserFromContext(ctx)
	assert.Nil(t, user)
}

func TestUserFromContext_WithUser(t *testing.T) {
	ctx := context.Background()
	u := &models.User{
		ID:    uuid.New(),
		OrgID: uuid.New(),
		Email: "test@example.com",
		Name:  "Test User",
		Role:  "admin",
	}
	ctx = WithUser(ctx, u)
	result := UserFromContext(ctx)
	assert.NotNil(t, result)
	assert.Equal(t, u.ID, result.ID)
	assert.Equal(t, u.Email, result.Email)
}

func TestOrgIDFromContext_NoOrgID(t *testing.T) {
	ctx := context.Background()
	orgID := OrgIDFromContext(ctx)
	assert.Equal(t, uuid.Nil, orgID)
}

func TestOrgIDFromContext_WithOrgID(t *testing.T) {
	ctx := context.Background()
	expected := uuid.New()
	ctx = WithOrgID(ctx, expected)
	result := OrgIDFromContext(ctx)
	assert.Equal(t, expected, result)
}

func TestOrgContext_RejectsMissingOrg(t *testing.T) {
	handler := OrgContext(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)
	assert.Equal(t, http.StatusForbidden, w.Code)
}

func TestOrgContext_AllowsValidOrg(t *testing.T) {
	handler := OrgContext(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	ctx := WithOrgID(req.Context(), uuid.New())
	req = req.WithContext(ctx)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
}

func TestAuth_ValidCookie(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	sessionStore := db.NewSessionStore(mock)
	userStore := db.NewUserStore(mock)

	sessionID := uuid.New()
	userID := uuid.New()
	orgID := uuid.New()
	token := "valid-session-token"
	now := time.Now()

	// GetByToken uses Query with 1 named arg
	sessionRows := pgxmock.NewRows([]string{"id", "user_id", "org_id", "token", "expires_at", "created_at"}).
		AddRow(sessionID, userID, orgID, token, now.Add(24*time.Hour), now)
	mock.ExpectQuery("SELECT .+ FROM sessions").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(sessionRows)

	// GetByID uses Query with 2 named args
	ghID := int64(12345)
	ghLogin := "testuser"
	avatarURL := "https://example.com/avatar.png"
	userRows := pgxmock.NewRows([]string{"id", "org_id", "email", "name", "role", "github_id", "github_login", "avatar_url", "created_at"}).
		AddRow(userID, orgID, "test@example.com", "Test User", "member", &ghID, &ghLogin, &avatarURL, now)
	mock.ExpectQuery("SELECT .+ FROM users").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(userRows)

	nextCalled := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nextCalled = true
		user := UserFromContext(r.Context())
		assert.NotNil(t, user)
		assert.Equal(t, userID, user.ID)
		assert.Equal(t, orgID, user.OrgID)
		w.WriteHeader(http.StatusOK)
	})

	handler := Auth(sessionStore, userStore)(next)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: "session_token", Value: token})
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	assert.True(t, nextCalled)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestAuth_ValidBearerToken(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	sessionStore := db.NewSessionStore(mock)
	userStore := db.NewUserStore(mock)

	sessionID := uuid.New()
	userID := uuid.New()
	orgID := uuid.New()
	token := "bearer-token-value"
	now := time.Now()

	sessionRows := pgxmock.NewRows([]string{"id", "user_id", "org_id", "token", "expires_at", "created_at"}).
		AddRow(sessionID, userID, orgID, token, now.Add(24*time.Hour), now)
	mock.ExpectQuery("SELECT .+ FROM sessions").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(sessionRows)

	ghID := int64(12345)
	ghLogin := "testuser"
	avatarURL := "https://example.com/avatar.png"
	userRows := pgxmock.NewRows([]string{"id", "org_id", "email", "name", "role", "github_id", "github_login", "avatar_url", "created_at"}).
		AddRow(userID, orgID, "test@example.com", "Test User", "admin", &ghID, &ghLogin, &avatarURL, now)
	mock.ExpectQuery("SELECT .+ FROM users").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(userRows)

	nextCalled := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nextCalled = true
		user := UserFromContext(r.Context())
		assert.NotNil(t, user)
		assert.Equal(t, userID, user.ID)
		w.WriteHeader(http.StatusOK)
	})

	handler := Auth(sessionStore, userStore)(next)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	assert.True(t, nextCalled)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestAuth_NoCookieNoHeader(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	sessionStore := db.NewSessionStore(mock)
	userStore := db.NewUserStore(mock)

	nextCalled := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nextCalled = true
	})

	handler := Auth(sessionStore, userStore)(next)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	assert.False(t, nextCalled)
	assert.Equal(t, http.StatusUnauthorized, w.Code)
	assert.Contains(t, w.Body.String(), "missing session")
}

func TestAuth_InvalidSession(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	sessionStore := db.NewSessionStore(mock)
	userStore := db.NewUserStore(mock)

	// GetByToken returns empty rows (no matching session)
	sessionRows := pgxmock.NewRows([]string{"id", "user_id", "org_id", "token", "expires_at", "created_at"})
	mock.ExpectQuery("SELECT .+ FROM sessions").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(sessionRows)

	nextCalled := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nextCalled = true
	})

	handler := Auth(sessionStore, userStore)(next)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: "session_token", Value: "bad-token"})
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	assert.False(t, nextCalled)
	assert.Equal(t, http.StatusUnauthorized, w.Code)
	assert.Contains(t, w.Body.String(), "invalid session")
	assert.NoError(t, mock.ExpectationsWereMet())
}

func TestAuth_UserNotFound(t *testing.T) {
	mock, err := pgxmock.NewPool()
	require.NoError(t, err)
	defer mock.Close()

	sessionStore := db.NewSessionStore(mock)
	userStore := db.NewUserStore(mock)

	sessionID := uuid.New()
	userID := uuid.New()
	orgID := uuid.New()
	token := "valid-token"
	now := time.Now()

	// GetByToken returns valid session
	sessionRows := pgxmock.NewRows([]string{"id", "user_id", "org_id", "token", "expires_at", "created_at"}).
		AddRow(sessionID, userID, orgID, token, now.Add(24*time.Hour), now)
	mock.ExpectQuery("SELECT .+ FROM sessions").
		WithArgs(pgxmock.AnyArg()).
		WillReturnRows(sessionRows)

	// GetByID returns empty rows (user not found)
	userRows := pgxmock.NewRows([]string{"id", "org_id", "email", "name", "role", "github_id", "github_login", "avatar_url", "created_at"})
	mock.ExpectQuery("SELECT .+ FROM users").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(userRows)

	nextCalled := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nextCalled = true
	})

	handler := Auth(sessionStore, userStore)(next)
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.AddCookie(&http.Cookie{Name: "session_token", Value: token})
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	assert.False(t, nextCalled)
	assert.Equal(t, http.StatusUnauthorized, w.Code)
	assert.Contains(t, w.Body.String(), "user not found")
	assert.NoError(t, mock.ExpectationsWereMet())
}
