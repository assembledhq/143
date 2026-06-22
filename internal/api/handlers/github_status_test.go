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
	cred        *models.DecryptedUserCredential
	err         error
	upsertFunc  func(context.Context, uuid.UUID, uuid.UUID, models.ProviderConfig) error
	disableFunc func(context.Context, uuid.UUID, uuid.UUID, models.ProviderName) error
}

func (s *stubGHCredentialStore) GetForUser(_ context.Context, _, _ uuid.UUID, _ models.ProviderName) (*models.DecryptedUserCredential, error) {
	return s.cred, s.err
}
func (s *stubGHCredentialStore) Upsert(ctx context.Context, userID, orgID uuid.UUID, cfg models.ProviderConfig) error {
	if s.upsertFunc != nil {
		return s.upsertFunc(ctx, userID, orgID, cfg)
	}
	return nil
}
func (s *stubGHCredentialStore) Disable(ctx context.Context, orgID, userID uuid.UUID, provider models.ProviderName) error {
	if s.disableFunc != nil {
		return s.disableFunc(ctx, orgID, userID, provider)
	}
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
	require.Equal(t, "recommended", resp.AccountRequirement, "user_preferred should make the account connection recommended")
}

func TestGitHubStatusHandler_GetStatus_AccountRequirementByMode(t *testing.T) {
	t.Parallel()

	cases := []struct {
		mode string
		want string
	}{
		{"user_required", "required"},
		{"user_preferred", "recommended"},
		{"app_only", "optional"},
		{"", "recommended"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.mode, func(t *testing.T) {
			t.Parallel()
			orgID := uuid.New()
			userID := uuid.New()
			orgReader := &stubGHOrgReader{
				org: models.Organization{
					Settings: json.RawMessage(`{"pr_authorship":"` + tc.mode + `"}`),
				},
			}
			handler := NewGitHubStatusHandler(&stubGHCredentialStore{err: context.DeadlineExceeded}, orgReader, "", "", "", "")

			req := httptest.NewRequest(http.MethodGet, "/api/v1/users/me/github-status", nil)
			ctx := middleware.WithUser(req.Context(), &models.User{ID: userID, OrgID: orgID})
			ctx = middleware.WithOrgID(ctx, orgID)
			req = req.WithContext(ctx)
			rr := httptest.NewRecorder()
			handler.GetStatus(rr, req)

			require.Equal(t, http.StatusOK, rr.Code)
			var resp GitHubStatusResponse
			require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp))
			require.Equal(t, tc.want, resp.AccountRequirement, "account requirement should follow authorship mode")
		})
	}
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
	require.Empty(t, parsed.Query().Get("resume_action"), "redirect should omit resume_action for legacy tokens without an Action claim")
}

func TestGitHubStatusHandler_HandleConnectCallback_ForwardsResumeAction(t *testing.T) {
	t.Parallel()

	// When the resume token's claims record an originating action, the
	// callback must forward it as a resume_action URL param so the frontend
	// dispatches deterministically (push vs create) regardless of any PR
	// state change during the OAuth round-trip.
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
		Action:     string(prAuthActionPushChanges),
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
	parsed, parseErr := url.Parse(rr.Header().Get("Location"))
	require.NoError(t, parseErr, "redirect location should parse")
	require.Equal(t, "push_changes", parsed.Query().Get("resume_action"), "callback should forward the action recorded in the signed claims")
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

func TestGitHubStatusHandler_HandleConnectCallback_RedirectsToIntegrations(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	userID := uuid.New()
	handler := NewGitHubStatusHandler(
		&stubGHCredentialStore{}, &stubGHOrgReader{},
		"test-client-id", "test-secret", "https://app.143.dev", "https://app.143.dev",
	)
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

	req := httptest.NewRequest(http.MethodGet, "/api/v1/users/me/github/callback?state=ok&code=abc", nil)
	req.AddCookie(&http.Cookie{Name: githubPRConnectStateCookie, Value: "ok"})
	ctx := middleware.WithUser(req.Context(), &models.User{ID: userID, OrgID: orgID})
	ctx = middleware.WithOrgID(ctx, orgID)
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()

	handler.HandleConnectCallback(rr, req)

	require.Equal(t, http.StatusTemporaryRedirect, rr.Code, "callback should redirect after successful auth")
	loc := rr.Header().Get("Location")
	parsed, parseErr := url.Parse(loc)
	require.NoError(t, parseErr, "redirect location should parse")
	require.Equal(t, "/settings/integrations", parsed.Path, "non-resume callback should return to the integrations page where the connect button lives")
	require.Equal(t, "connected", parsed.Query().Get("github_pr"), "redirect should note successful GitHub PR auth")
}

func TestGitHubStatusHandler_GetStatus_Unauthorized(t *testing.T) {
	t.Parallel()

	handler := NewGitHubStatusHandler(&stubGHCredentialStore{}, &stubGHOrgReader{}, "", "", "", "")
	req := httptest.NewRequest(http.MethodGet, "/api/v1/users/me/github-status", nil)
	rr := httptest.NewRecorder()

	handler.GetStatus(rr, req)

	require.Equal(t, http.StatusUnauthorized, rr.Code, "GetStatus should reject unauthenticated requests")
}

func TestGitHubStatusHandler_GetStatus_AppUserAuthError(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	userID := uuid.New()
	handler := NewGitHubStatusHandler(&stubGHCredentialStore{}, &stubGHOrgReader{}, "", "", "", "")
	handler.SetAppUserAuth(&stubGitHubAppUserAuthService{
		hasValidCredentialFunc: func(context.Context, uuid.UUID, uuid.UUID) (bool, error) {
			return false, context.DeadlineExceeded
		},
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/users/me/github-status", nil)
	ctx := middleware.WithUser(req.Context(), &models.User{ID: userID, OrgID: orgID})
	ctx = middleware.WithOrgID(ctx, orgID)
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()

	handler.GetStatus(rr, req)

	require.Equal(t, http.StatusInternalServerError, rr.Code, "GetStatus should surface app-user auth check failures")
}

func TestGitHubStatusHandler_GetStatus_UsesAppUserAuthService(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	userID := uuid.New()
	login := "octocat"
	handler := NewGitHubStatusHandler(&stubGHCredentialStore{}, &stubGHOrgReader{}, "", "", "", "")
	handler.SetAppUserAuth(&stubGitHubAppUserAuthService{
		hasValidCredentialFunc: func(context.Context, uuid.UUID, uuid.UUID) (bool, error) {
			return true, nil
		},
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/users/me/github-status", nil)
	ctx := middleware.WithUser(req.Context(), &models.User{ID: userID, OrgID: orgID, GitHubLogin: &login})
	ctx = middleware.WithOrgID(ctx, orgID)
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()

	handler.GetStatus(rr, req)

	require.Equal(t, http.StatusOK, rr.Code, "GetStatus should succeed")
	var resp GitHubStatusResponse
	require.NoError(t, json.Unmarshal(rr.Body.Bytes(), &resp), "response body should decode")
	require.True(t, resp.Connected, "valid app user auth should mark the user connected")
	require.True(t, resp.HasRepoScope, "valid app user auth should mark repo scope")
	require.Equal(t, login, resp.GitHubLogin, "GetStatus should include the GitHub login when connected")
}

func TestGitHubStatusHandler_StartConnect_ResumeTokenRequiresAuthContext(t *testing.T) {
	t.Parallel()

	handler := NewGitHubStatusHandler(&stubGHCredentialStore{}, &stubGHOrgReader{}, "test-client-id", "test-secret", "https://app.143.dev", "https://app.143.dev")
	handler.SetPRAuthFlow("test-signing-key-32bytes-minimum-length")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/users/me/github/connect?resume_token=token", nil)
	rr := httptest.NewRecorder()

	handler.StartConnect(rr, req)

	require.Equal(t, http.StatusUnauthorized, rr.Code, "StartConnect should reject resume flows without an authenticated user")
}

func TestGitHubStatusHandler_StartConnect_ResumeTokenRequiresSigningKey(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	userID := uuid.New()
	handler := NewGitHubStatusHandler(&stubGHCredentialStore{}, &stubGHOrgReader{}, "test-client-id", "test-secret", "https://app.143.dev", "https://app.143.dev")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/users/me/github/connect?resume_token=token", nil)
	ctx := middleware.WithUser(req.Context(), &models.User{ID: userID, OrgID: orgID})
	ctx = middleware.WithOrgID(ctx, orgID)
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()

	handler.StartConnect(rr, req)

	require.Equal(t, http.StatusInternalServerError, rr.Code, "StartConnect should reject resume flows when PR auth signing is not configured")
}

func TestGitHubStatusHandler_StartConnect_InvalidResumeToken(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	userID := uuid.New()
	handler := NewGitHubStatusHandler(&stubGHCredentialStore{}, &stubGHOrgReader{}, "test-client-id", "test-secret", "https://app.143.dev", "https://app.143.dev")
	handler.SetPRAuthFlow("test-signing-key-32bytes-minimum-length")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/users/me/github/connect?resume_token=bad-token", nil)
	ctx := middleware.WithUser(req.Context(), &models.User{ID: userID, OrgID: orgID})
	ctx = middleware.WithOrgID(ctx, orgID)
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()

	handler.StartConnect(rr, req)

	require.Equal(t, http.StatusConflict, rr.Code, "StartConnect should reject expired or invalid resume tokens")
}

func TestGitHubStatusHandler_HandleConnectCallback_NotConfigured(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	userID := uuid.New()
	handler := NewGitHubStatusHandler(&stubGHCredentialStore{}, &stubGHOrgReader{}, "test-client-id", "test-secret", "https://app.143.dev", "https://app.143.dev")

	req := httptest.NewRequest(http.MethodGet, "/api/v1/users/me/github/callback?state=ok&code=abc", nil)
	req.AddCookie(&http.Cookie{Name: githubPRConnectStateCookie, Value: "ok"})
	ctx := middleware.WithUser(req.Context(), &models.User{ID: userID, OrgID: orgID})
	ctx = middleware.WithOrgID(ctx, orgID)
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()

	handler.HandleConnectCallback(rr, req)

	require.Equal(t, http.StatusServiceUnavailable, rr.Code, "callback should fail fast when app user auth is unavailable")
}

func TestGitHubStatusHandler_HandleConnectCallback_ExchangeFailure(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	userID := uuid.New()
	handler := NewGitHubStatusHandler(&stubGHCredentialStore{}, &stubGHOrgReader{}, "test-client-id", "test-secret", "https://app.143.dev", "https://app.143.dev")
	handler.SetAppUserAuth(&stubGitHubAppUserAuthService{
		exchangeCodeFunc: func(context.Context, string) (*models.GitHubAppUserConfig, error) {
			return nil, context.DeadlineExceeded
		},
	})

	req := httptest.NewRequest(http.MethodGet, "/api/v1/users/me/github/callback?state=ok&code=abc", nil)
	req.AddCookie(&http.Cookie{Name: githubPRConnectStateCookie, Value: "ok"})
	ctx := middleware.WithUser(req.Context(), &models.User{ID: userID, OrgID: orgID})
	ctx = middleware.WithOrgID(ctx, orgID)
	req = req.WithContext(ctx)
	rr := httptest.NewRecorder()

	handler.HandleConnectCallback(rr, req)

	require.Equal(t, http.StatusInternalServerError, rr.Code, "callback should surface exchange failures")
}

func TestGitHubStatusHandler_Disconnect(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		user       *models.User
		disableErr error
		wantCode   int
	}{
		{
			name:     "unauthorized",
			wantCode: http.StatusUnauthorized,
		},
		{
			name:       "disable failure",
			user:       &models.User{ID: uuid.New(), OrgID: uuid.New()},
			disableErr: context.DeadlineExceeded,
			wantCode:   http.StatusInternalServerError,
		},
		{
			name:     "success",
			user:     &models.User{ID: uuid.New(), OrgID: uuid.New()},
			wantCode: http.StatusOK,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			handler := NewGitHubStatusHandler(&stubGHCredentialStore{
				disableFunc: func(context.Context, uuid.UUID, uuid.UUID, models.ProviderName) error {
					return tt.disableErr
				},
			}, &stubGHOrgReader{}, "", "", "", "")
			req := httptest.NewRequest(http.MethodPost, "/api/v1/users/me/github/disconnect", nil)
			if tt.user != nil {
				ctx := middleware.WithUser(req.Context(), tt.user)
				ctx = middleware.WithOrgID(ctx, tt.user.OrgID)
				req = req.WithContext(ctx)
			}
			rr := httptest.NewRecorder()

			handler.Disconnect(rr, req)

			require.Equal(t, tt.wantCode, rr.Code, "Disconnect should return the expected status")
		})
	}
}
