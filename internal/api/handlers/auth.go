package handlers

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"time"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/config"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
)

type AuthHandler struct {
	cfg          *config.Config
	orgStore     *db.OrganizationStore
	userStore    *db.UserStore
	sessionStore *db.SessionStore
}

func NewAuthHandler(cfg *config.Config, orgStore *db.OrganizationStore, userStore *db.UserStore, sessionStore *db.SessionStore) *AuthHandler {
	return &AuthHandler{
		cfg:          cfg,
		orgStore:     orgStore,
		userStore:    userStore,
		sessionStore: sessionStore,
	}
}

func (h *AuthHandler) Login(w http.ResponseWriter, r *http.Request) {
	state, err := generateRandomString(32)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to generate state")
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:     "oauth_state",
		Value:    state,
		Path:     "/",
		MaxAge:   600,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})

	params := url.Values{
		"client_id":    {h.cfg.GitHubOAuthClientID},
		"redirect_uri": {h.cfg.BaseURL + "/api/v1/auth/github/callback"},
		"scope":        {"read:user,user:email"},
		"state":        {state},
	}

	http.Redirect(w, r, "https://github.com/login/oauth/authorize?"+params.Encode(), http.StatusTemporaryRedirect)
}

func (h *AuthHandler) Callback(w http.ResponseWriter, r *http.Request) {
	// Validate state
	stateCookie, err := r.Cookie("oauth_state")
	if err != nil || stateCookie.Value != r.URL.Query().Get("state") {
		writeError(w, http.StatusBadRequest, "INVALID_STATE", "OAuth state mismatch")
		return
	}

	// Clear state cookie
	http.SetCookie(w, &http.Cookie{
		Name:     "oauth_state",
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
	})

	code := r.URL.Query().Get("code")
	if code == "" {
		writeError(w, http.StatusBadRequest, "MISSING_CODE", "missing authorization code")
		return
	}

	// Exchange code for access token
	tokenResp, err := h.exchangeCode(code)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "TOKEN_EXCHANGE_FAILED", "failed to exchange code")
		return
	}

	// Fetch GitHub user
	ghUser, err := h.fetchGitHubUser(tokenResp.AccessToken)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "GITHUB_API_FAILED", "failed to fetch GitHub user")
		return
	}

	// Check if user already exists
	existingUser, err := h.userStore.GetByGitHubID(r.Context(), ghUser.ID)
	var orgID = existingUser.OrgID

	if err != nil {
		// New user — create a default org
		org := &models.Organization{
			Name:     ghUser.Login + "'s Org",
			Slug:     ghUser.Login,
			Settings: json.RawMessage(`{}`),
		}
		if createErr := h.orgStore.Create(r.Context(), org); createErr != nil {
			writeError(w, http.StatusInternalServerError, "ORG_CREATE_FAILED", "failed to create organization")
			return
		}
		orgID = org.ID
	}

	// Upsert user
	email := ghUser.Email
	if email == "" {
		email = ghUser.Login + "@users.noreply.github.com"
	}
	user := &models.User{
		OrgID:       orgID,
		Email:       email,
		Name:        ghUser.Name,
		Role:        "admin",
		GitHubID:    &ghUser.ID,
		GitHubLogin: &ghUser.Login,
		AvatarURL:   &ghUser.AvatarURL,
	}
	if err := h.userStore.UpsertFromGitHub(r.Context(), user); err != nil {
		writeError(w, http.StatusInternalServerError, "USER_UPSERT_FAILED", "failed to upsert user")
		return
	}

	// Create session
	sessionToken, err := generateRandomString(32)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to generate session token")
		return
	}
	session := &models.Session{
		UserID:    user.ID,
		OrgID:     user.OrgID,
		Token:     sessionToken,
		ExpiresAt: time.Now().Add(30 * 24 * time.Hour),
	}
	if err := h.sessionStore.Create(r.Context(), session); err != nil {
		writeError(w, http.StatusInternalServerError, "SESSION_CREATE_FAILED", "failed to create session")
		return
	}

	// Set session cookie
	http.SetCookie(w, &http.Cookie{
		Name:     "session_token",
		Value:    sessionToken,
		Path:     "/",
		MaxAge:   30 * 24 * 60 * 60,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})

	http.Redirect(w, r, h.cfg.FrontendURL, http.StatusTemporaryRedirect)
}

func (h *AuthHandler) Logout(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie("session_token")
	if err == nil {
		if deleteErr := h.sessionStore.DeleteByToken(r.Context(), cookie.Value); deleteErr != nil {
			writeError(w, http.StatusInternalServerError, "SESSION_DELETE_FAILED", "failed to delete session")
			return
		}
	}

	http.SetCookie(w, &http.Cookie{
		Name:     "session_token",
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
	})

	user := middleware.UserFromContext(r.Context())
	if user != nil {
		writeJSON(w, http.StatusOK, map[string]string{"status": "logged out"})
	} else {
		writeJSON(w, http.StatusOK, map[string]string{"status": "logged out"})
	}
}

type githubTokenResponse struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	Scope       string `json:"scope"`
}

type githubUser struct {
	ID        int64  `json:"id"`
	Login     string `json:"login"`
	Name      string `json:"name"`
	Email     string `json:"email"`
	AvatarURL string `json:"avatar_url"`
}

func (h *AuthHandler) exchangeCode(code string) (*githubTokenResponse, error) {
	data := url.Values{
		"client_id":     {h.cfg.GitHubOAuthClientID},
		"client_secret": {h.cfg.GitHubOAuthClientSecret},
		"code":          {code},
	}

	req, err := http.NewRequest("POST", "https://github.com/login/oauth/access_token", nil)
	if err != nil {
		return nil, err
	}
	req.URL.RawQuery = data.Encode()
	req.Header.Set("Accept", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("token exchange request: %w", err)
	}
	defer resp.Body.Close()

	var tokenResp githubTokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
		return nil, fmt.Errorf("decode token response: %w", err)
	}
	if tokenResp.AccessToken == "" {
		return nil, fmt.Errorf("empty access token")
	}
	return &tokenResp, nil
}

func (h *AuthHandler) fetchGitHubUser(accessToken string) (*githubUser, error) {
	req, err := http.NewRequest("GET", "https://api.github.com/user", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("github user request: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read github user response: %w", err)
	}

	var user githubUser
	if err := json.Unmarshal(body, &user); err != nil {
		return nil, fmt.Errorf("decode github user: %w", err)
	}
	return &user, nil
}

func generateRandomString(n int) (string, error) {
	bytes := make([]byte, n)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes), nil
}
