package handlers

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/models"
)

// --- test doubles ---

type stubGHCredentialStore struct {
	cred *models.DecryptedUserCredential
	err  error
}

func (s *stubGHCredentialStore) GetForUser(_ context.Context, _, _ uuid.UUID, _ models.ProviderName) (*models.DecryptedUserCredential, error) {
	return s.cred, s.err
}
func (s *stubGHCredentialStore) Upsert(_ context.Context, _, _ uuid.UUID, _ models.ProviderConfig, _ bool) error {
	return nil
}
func (s *stubGHCredentialStore) Disable(_ context.Context, _, _ uuid.UUID, _ models.ProviderName) error {
	return nil
}

type stubGitHubAppUserAuthService struct {
	hasValidCredentialFunc func(context.Context, uuid.UUID, uuid.UUID) (bool, error)
	exchangeCodeFunc       func(context.Context, string) (*models.GitHubAppUserConfig, error)
}

func (s *stubGitHubAppUserAuthService) HasValidCredential(ctx context.Context, orgID, userID uuid.UUID) (bool, error) {
	if s.hasValidCredentialFunc != nil {
		return s.hasValidCredentialFunc(ctx, orgID, userID)
	}
	return false, nil
}

func (s *stubGitHubAppUserAuthService) ExchangeCode(ctx context.Context, code string) (*models.GitHubAppUserConfig, error) {
	if s.exchangeCodeFunc != nil {
		return s.exchangeCodeFunc(ctx, code)
	}
	return nil, nil
}

type stubGHOrgReader struct {
	org models.Organization
	err error
}

func (s *stubGHOrgReader) GetByID(_ context.Context, _ uuid.UUID) (models.Organization, error) {
	return s.org, s.err
}

// --- tests ---

func TestGitHubStatusHandler_GetStatus_Connected(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	userID := uuid.New()
	login := "testuser"

	credStore := &stubGHCredentialStore{
		cred: &models.DecryptedUserCredential{
			Config: models.GitHubAppUserConfig{
				AccessToken:           "ghu_test",
				TokenType:             "bearer",
				ExpiresAt:             time.Now().Add(time.Hour),
				RefreshToken:          "ghr_test",
				RefreshTokenExpiresAt: time.Now().Add(30 * 24 * time.Hour),
			},
		},
	}
	orgReader := &stubGHOrgReader{
		org: models.Organization{
			Settings: json.RawMessage(`{"pr_authorship":"user_preferred"}`),
		},
	}

	handler := NewGitHubStatusHandler(credStore, orgReader, "", "", "", "")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/users/me/github-status", nil)
	ctx := middleware.WithUser(req.Context(), &models.User{ID: userID, OrgID: orgID, GitHubLogin: &login})
	ctx = middleware.WithOrgID(ctx, orgID)
	req = req.WithContext(ctx)

	rr := httptest.NewRecorder()
	handler.GetStatus(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)

	var resp GitHubStatusResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp), "response body should decode")
	require.True(t, resp.Connected, "connected app-user credential should mark GitHub as connected")
	require.True(t, resp.HasRepoScope, "app-user credential should be treated as PR-capable")
	require.Equal(t, "testuser", resp.GitHubLogin, "response should include the user's GitHub login")
	require.Equal(t, "user_preferred", resp.PRAuthorshipMode, "response should echo org PR authorship mode")
}

func TestGitHubStatusHandler_GetStatus_NotConnected(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	userID := uuid.New()

	credStore := &stubGHCredentialStore{
		err: context.DeadlineExceeded, // simulate no credential found
	}
	orgReader := &stubGHOrgReader{
		org: models.Organization{
			Settings: json.RawMessage(`{"pr_authorship":"app_only"}`),
		},
	}

	handler := NewGitHubStatusHandler(credStore, orgReader, "", "", "", "")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/users/me/github-status", nil)
	ctx := middleware.WithUser(req.Context(), &models.User{ID: userID, OrgID: orgID})
	ctx = middleware.WithOrgID(ctx, orgID)
	req = req.WithContext(ctx)

	rr := httptest.NewRecorder()
	handler.GetStatus(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)

	var resp GitHubStatusResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp), "response body should decode")
	require.False(t, resp.Connected, "missing app-user credential should report disconnected")
	require.False(t, resp.HasRepoScope, "missing app-user credential should not report PR capability")
	require.Equal(t, "app_only", resp.PRAuthorshipMode, "response should echo app_only mode")
}

func TestGitHubStatusHandler_GetStatus_IgnoresExpiredCredential(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	userID := uuid.New()
	login := "testuser"

	credStore := &stubGHCredentialStore{
		cred: &models.DecryptedUserCredential{
			Config: models.GitHubAppUserConfig{
				AccessToken:           "ghu_test",
				TokenType:             "bearer",
				ExpiresAt:             time.Now().Add(-time.Minute),
				RefreshToken:          "ghr_test",
				RefreshTokenExpiresAt: time.Now().Add(30 * 24 * time.Hour),
			},
		},
	}
	orgReader := &stubGHOrgReader{
		org: models.Organization{
			Settings: json.RawMessage(`{}`),
		},
	}

	handler := NewGitHubStatusHandler(credStore, orgReader, "", "", "", "")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/users/me/github-status", nil)
	ctx := middleware.WithUser(req.Context(), &models.User{ID: userID, OrgID: orgID, GitHubLogin: &login})
	ctx = middleware.WithOrgID(ctx, orgID)
	req = req.WithContext(ctx)

	rr := httptest.NewRecorder()
	handler.GetStatus(rr, req)

	require.Equal(t, http.StatusOK, rr.Code)

	var resp GitHubStatusResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp), "response body should decode")
	require.False(t, resp.Connected, "expired app-user credential should not be treated as connected")
	require.False(t, resp.HasRepoScope, "expired app-user credential should not be treated as PR-capable")
	require.Empty(t, resp.GitHubLogin, "disconnected response should not promise authorship as the user")
}

func TestGitHubStatusHandler_StartConnect_NotConfigured(t *testing.T) {
	t.Parallel()

	handler := NewGitHubStatusHandler(&stubGHCredentialStore{}, &stubGHOrgReader{}, "", "", "", "")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/users/me/github/connect", nil)
	rr := httptest.NewRecorder()
	handler.StartConnect(rr, req)

	require.Equal(t, http.StatusServiceUnavailable, rr.Code)
}

func TestGitHubStatusHandler_StartConnect_Redirects(t *testing.T) {
	t.Parallel()

	handler := NewGitHubStatusHandler(
		&stubGHCredentialStore{}, &stubGHOrgReader{},
		"test-client-id", "test-secret", "https://app.143.dev", "https://app.143.dev",
	)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/users/me/github/connect", nil)
	rr := httptest.NewRecorder()
	handler.StartConnect(rr, req)

	require.Equal(t, http.StatusTemporaryRedirect, rr.Code)
	loc := rr.Header().Get("Location")
	require.Contains(t, loc, "github.com/login/oauth/authorize")
	require.Contains(t, loc, "client_id=test-client-id")
	require.Contains(t, loc, "redirect_uri=")
}

func TestGitHubStatusHandler_StartConnect_StoresResumeCookie(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	userID := uuid.New()
	sessionID := uuid.New()
	handler := NewGitHubStatusHandler(
		&stubGHCredentialStore{}, &stubGHOrgReader{},
		"test-client-id", "test-secret", "https://app.143.dev", "https://app.143.dev",
	)
	handler.SetPRAuthFlow("test-signing-key-32bytes-minimum-length")

	resumeToken, err := signPRAuthResumeToken([]byte("test-signing-key-32bytes-minimum-length"), prAuthResumeClaims{
		SessionID:  sessionID,
		UserID:     userID,
		OrgID:      orgID,
		AuthorMode: "user",
		ExpiresAt:  time.Now().Add(5 * time.Minute).Unix(),
	})
	require.NoError(t, err, "resume token should sign")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/users/me/github/connect?resume_token="+url.QueryEscape(resumeToken), nil)
	req = req.WithContext(middleware.WithOrgID(req.Context(), orgID))
	req = req.WithContext(middleware.WithUser(req.Context(), &models.User{ID: userID, OrgID: orgID}))
	rr := httptest.NewRecorder()

	handler.StartConnect(rr, req)

	require.Equal(t, http.StatusTemporaryRedirect, rr.Code, "connect should redirect to GitHub")
	loc := rr.Header().Get("Location")
	parsed, parseErr := url.Parse(loc)
	require.NoError(t, parseErr, "redirect location should parse")
	state := parsed.Query().Get("state")
	require.NotEmpty(t, state, "redirect should include oauth state")
	cookies := rr.Result().Cookies()
	found := false
	for _, cookie := range cookies {
		if cookie.Name == githubPRResumeCookiePrefix+state {
			found = true
			require.Equal(t, resumeToken, cookie.Value, "resume cookie should preserve resume token")
		}
	}
	require.True(t, found, "connect should set a resume cookie when resume_token is provided")
}

func TestGitHubStatusHandler_HandleConnectCallback_RedirectsBackToSessionResume(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	userID := uuid.New()
	sessionID := uuid.New()
	handler := NewGitHubStatusHandler(
		&stubGHCredentialStore{}, &stubGHOrgReader{},
		"test-client-id", "test-secret", "https://app.143.dev", "https://app.143.dev",
	)
	handler.SetPRAuthFlow("test-signing-key-32bytes-minimum-length")
	handler.appUserAuth = &stubGitHubAppUserAuthService{
		exchangeCodeFunc: func(context.Context, string) (*models.GitHubAppUserConfig, error) {
			return &models.GitHubAppUserConfig{
				AccessToken:           "ghu_test",
				TokenType:             "bearer",
				ExpiresAt:             time.Now().Add(time.Hour),
				RefreshToken:          "ghr_test",
				RefreshTokenExpiresAt: time.Now().Add(30 * 24 * time.Hour),
			}, nil
		},
	}

	resumeToken, err := signPRAuthResumeToken([]byte("test-signing-key-32bytes-minimum-length"), prAuthResumeClaims{
		SessionID:  sessionID,
		UserID:     userID,
		OrgID:      orgID,
		AuthorMode: "user",
		ExpiresAt:  time.Now().Add(5 * time.Minute).Unix(),
	})
	require.NoError(t, err, "resume token should sign")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/users/me/github/callback?state=ok&code=abc", nil)
	req.AddCookie(&http.Cookie{Name: githubPRConnectStateCookie, Value: "ok"})
	req.AddCookie(&http.Cookie{Name: githubPRResumeCookiePrefix + "ok", Value: resumeToken})
	ctx := middleware.WithUser(req.Context(), &models.User{ID: userID, OrgID: orgID})
	ctx = middleware.WithOrgID(ctx, orgID)
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()

	handler.HandleConnectCallback(rr, req)

	require.Equal(t, http.StatusTemporaryRedirect, rr.Code, "callback should redirect back to the session page")
	loc := rr.Header().Get("Location")
	parsed, parseErr := url.Parse(loc)
	require.NoError(t, parseErr, "redirect location should parse")
	require.Equal(t, "/sessions/"+sessionID.String(), parsed.Path, "callback should return to the originating session page")
	require.Equal(t, "connected", parsed.Query().Get("github_pr"), "redirect should note successful GitHub PR auth")
	require.NotEmpty(t, parsed.Query().Get("resume_pr"), "redirect should carry resume token back to the frontend")
}

func TestGitHubStatusHandler_HandleConnectCallback_UsesStateScopedResumeCookie(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	userID := uuid.New()
	firstSessionID := uuid.New()
	secondSessionID := uuid.New()
	handler := NewGitHubStatusHandler(
		&stubGHCredentialStore{}, &stubGHOrgReader{},
		"test-client-id", "test-secret", "https://app.143.dev", "https://app.143.dev",
	)
	handler.SetPRAuthFlow("test-signing-key-32bytes-minimum-length")
	handler.appUserAuth = &stubGitHubAppUserAuthService{
		exchangeCodeFunc: func(context.Context, string) (*models.GitHubAppUserConfig, error) {
			return &models.GitHubAppUserConfig{
				AccessToken:           "ghu_test",
				TokenType:             "bearer",
				ExpiresAt:             time.Now().Add(time.Hour),
				RefreshToken:          "ghr_test",
				RefreshTokenExpiresAt: time.Now().Add(30 * 24 * time.Hour),
			}, nil
		},
	}

	firstResumeToken, err := signPRAuthResumeToken([]byte("test-signing-key-32bytes-minimum-length"), prAuthResumeClaims{
		SessionID:  firstSessionID,
		UserID:     userID,
		OrgID:      orgID,
		AuthorMode: "user",
		ExpiresAt:  time.Now().Add(5 * time.Minute).Unix(),
	})
	require.NoError(t, err, "first resume token should sign")
	secondResumeToken, err := signPRAuthResumeToken([]byte("test-signing-key-32bytes-minimum-length"), prAuthResumeClaims{
		SessionID:  secondSessionID,
		UserID:     userID,
		OrgID:      orgID,
		AuthorMode: "user",
		ExpiresAt:  time.Now().Add(5 * time.Minute).Unix(),
	})
	require.NoError(t, err, "second resume token should sign")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/users/me/github/callback?state=second-state&code=abc", nil)
	req.AddCookie(&http.Cookie{Name: githubPRConnectStateCookie, Value: "second-state"})
	req.AddCookie(&http.Cookie{Name: githubPRResumeCookiePrefix + "first-state", Value: firstResumeToken})
	req.AddCookie(&http.Cookie{Name: githubPRResumeCookiePrefix + "second-state", Value: secondResumeToken})
	ctx := middleware.WithUser(req.Context(), &models.User{ID: userID, OrgID: orgID})
	ctx = middleware.WithOrgID(ctx, orgID)
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()

	handler.HandleConnectCallback(rr, req)

	require.Equal(t, http.StatusTemporaryRedirect, rr.Code, "callback should redirect after successful auth")
	loc := rr.Header().Get("Location")
	parsed, parseErr := url.Parse(loc)
	require.NoError(t, parseErr, "redirect location should parse")
	require.Equal(t, "/sessions/"+secondSessionID.String(), parsed.Path, "callback should resume the flow bound to the callback state")
	require.Equal(t, secondResumeToken, parsed.Query().Get("resume_pr"), "callback should return the state-scoped resume token")
}
