package handlers

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/mail"
	"net/url"
	"strings"
	"time"

	"golang.org/x/crypto/bcrypt"

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

// Providers returns which auth methods are configured.
func (h *AuthHandler) Providers(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"data": map[string]bool{
			"github": h.cfg.GitHubOAuthClientID != "",
			"google": h.cfg.GoogleOAuthClientID != "",
			"email":  true,
		},
	})
}

// Me returns the currently authenticated user.
func (h *AuthHandler) Me(w http.ResponseWriter, r *http.Request) {
	user := middleware.UserFromContext(r.Context())
	if user == nil {
		writeError(w, http.StatusUnauthorized, "UNAUTHORIZED", "not authenticated")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": user})
}

// Register handles email/password sign-up.
func (h *AuthHandler) Register(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Email    string `json:"email"`
		Password string `json:"password"`
		Name     string `json:"name"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", "invalid request body")
		return
	}

	body.Email = strings.TrimSpace(body.Email)
	body.Name = strings.TrimSpace(body.Name)

	if body.Name == "" {
		writeError(w, http.StatusBadRequest, "MISSING_NAME", "Name is required.")
		return
	}
	if _, err := mail.ParseAddress(body.Email); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_EMAIL", "Invalid email address.")
		return
	}
	if len(body.Password) < 8 {
		writeError(w, http.StatusBadRequest, "WEAK_PASSWORD", "Password must be at least 8 characters.")
		return
	}

	// Check for existing user
	if _, err := h.userStore.GetByEmail(r.Context(), body.Email); err == nil {
		writeError(w, http.StatusConflict, "EMAIL_EXISTS", "An account with this email already exists.")
		return
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(body.Password), bcrypt.DefaultCost)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to hash password")
		return
	}

	// Create org
	org := &models.Organization{
		Name:     body.Name + "'s Org",
		Settings: json.RawMessage(`{}`),
	}
	if err := h.orgStore.Create(r.Context(), org); err != nil {
		writeError(w, http.StatusInternalServerError, "ORG_CREATE_FAILED", "Failed to create organization.")
		return
	}

	hashStr := string(hash)
	user := &models.User{
		OrgID:        org.ID,
		Email:        body.Email,
		Name:         body.Name,
		Role:         "admin",
		PasswordHash: &hashStr,
	}
	if err := h.userStore.CreateWithPassword(r.Context(), user); err != nil {
		writeError(w, http.StatusInternalServerError, "USER_CREATE_FAILED", "failed to create user")
		return
	}

	h.createSessionAndRespond(w, r, user)
}

// EmailLogin handles email/password sign-in.
func (h *AuthHandler) EmailLogin(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Email    string `json:"email"`
		Password string `json:"password"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "INVALID_BODY", "invalid request body")
		return
	}

	body.Email = strings.TrimSpace(body.Email)

	user, err := h.userStore.GetByEmail(r.Context(), body.Email)
	if err != nil {
		writeError(w, http.StatusUnauthorized, "INVALID_CREDENTIALS", "Invalid email or password.")
		return
	}

	if user.PasswordHash == nil {
		writeError(w, http.StatusUnauthorized, "OAUTH_ONLY", "This account uses social login. Please sign in with GitHub or Google.")
		return
	}

	if err := bcrypt.CompareHashAndPassword([]byte(*user.PasswordHash), []byte(body.Password)); err != nil {
		writeError(w, http.StatusUnauthorized, "INVALID_CREDENTIALS", "Invalid email or password.")
		return
	}

	h.createSessionAndRespond(w, r, &user)
}

// Login redirects to GitHub OAuth.
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

// Callback handles GitHub OAuth callback.
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
	tokenResp, err := h.exchangeGitHubCode(code)
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

	email := ghUser.Email
	if email == "" {
		email = ghUser.Login + "@users.noreply.github.com"
	}

	// Account linking: try GitHub ID → email → create new
	existingUser, err := h.userStore.GetByGitHubID(r.Context(), ghUser.ID)
	if err == nil {
		// Known GitHub user — update and sign in
		existingUser.Name = ghUser.Name
		existingUser.Email = email
		existingUser.GitHubLogin = &ghUser.Login
		existingUser.AvatarURL = &ghUser.AvatarURL
		if upsertErr := h.userStore.UpsertFromGitHub(r.Context(), &existingUser); upsertErr != nil {
			writeError(w, http.StatusInternalServerError, "USER_UPSERT_FAILED", "failed to upsert user")
			return
		}
		h.createSessionAndRedirect(w, r, &existingUser)
		return
	}

	// Try email match for account linking
	if emailUser, emailErr := h.userStore.GetByEmail(r.Context(), email); emailErr == nil {
		if linkErr := h.userStore.LinkGitHubAccount(r.Context(), emailUser.ID, emailUser.OrgID, ghUser.ID, ghUser.Login, ghUser.AvatarURL); linkErr != nil {
			writeError(w, http.StatusInternalServerError, "LINK_FAILED", "failed to link GitHub account")
			return
		}
		h.createSessionAndRedirect(w, r, &emailUser)
		return
	}

	// New user — create a default org
	org := &models.Organization{
		Name:     ghUser.Login + "'s Org",
		Settings: json.RawMessage(`{}`),
	}
	if createErr := h.orgStore.Create(r.Context(), org); createErr != nil {
		writeError(w, http.StatusInternalServerError, "ORG_CREATE_FAILED", "Failed to create organization.")
		return
	}

	user := &models.User{
		OrgID:       org.ID,
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

	h.createSessionAndRedirect(w, r, user)
}

// GoogleLogin redirects to Google OAuth.
func (h *AuthHandler) GoogleLogin(w http.ResponseWriter, r *http.Request) {
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
		"client_id":     {h.cfg.GoogleOAuthClientID},
		"redirect_uri":  {h.cfg.BaseURL + "/api/v1/auth/google/callback"},
		"response_type": {"code"},
		"scope":         {"openid email profile"},
		"state":         {state},
	}

	http.Redirect(w, r, "https://accounts.google.com/o/oauth2/v2/auth?"+params.Encode(), http.StatusTemporaryRedirect)
}

// GoogleCallback handles Google OAuth callback.
func (h *AuthHandler) GoogleCallback(w http.ResponseWriter, r *http.Request) {
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
	tokenResp, err := h.exchangeGoogleCode(code)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "TOKEN_EXCHANGE_FAILED", "failed to exchange code")
		return
	}

	// Fetch Google user info
	gUser, err := h.fetchGoogleUser(tokenResp.AccessToken)
	if err != nil {
		writeError(w, http.StatusInternalServerError, "GOOGLE_API_FAILED", "failed to fetch Google user")
		return
	}

	// Account linking: try Google ID → email → create new
	if existingUser, googleErr := h.userStore.GetByGoogleID(r.Context(), gUser.Sub); googleErr == nil {
		h.createSessionAndRedirect(w, r, &existingUser)
		return
	}

	if emailUser, emailErr := h.userStore.GetByEmail(r.Context(), gUser.Email); emailErr == nil {
		if linkErr := h.userStore.LinkGoogleAccount(r.Context(), emailUser.ID, emailUser.OrgID, gUser.Sub, gUser.Picture); linkErr != nil {
			writeError(w, http.StatusInternalServerError, "LINK_FAILED", "failed to link Google account")
			return
		}
		h.createSessionAndRedirect(w, r, &emailUser)
		return
	}

	// New user — create a default org
	name := gUser.Name
	if name == "" {
		name = gUser.Email
	}

	org := &models.Organization{
		Name:     name + "'s Org",
		Settings: json.RawMessage(`{}`),
	}
	if createErr := h.orgStore.Create(r.Context(), org); createErr != nil {
		writeError(w, http.StatusInternalServerError, "ORG_CREATE_FAILED", "Failed to create organization.")
		return
	}

	user := &models.User{
		OrgID:     org.ID,
		Email:     gUser.Email,
		Name:      name,
		Role:      "admin",
		GoogleID:  &gUser.Sub,
		AvatarURL: &gUser.Picture,
	}
	if err := h.userStore.UpsertFromGoogle(r.Context(), user); err != nil {
		writeError(w, http.StatusInternalServerError, "USER_UPSERT_FAILED", "failed to upsert user")
		return
	}

	h.createSessionAndRedirect(w, r, user)
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

// --- shared helpers ---

func (h *AuthHandler) createSessionAndRedirect(w http.ResponseWriter, r *http.Request, user *models.User) {
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

func (h *AuthHandler) createSessionAndRespond(w http.ResponseWriter, r *http.Request, user *models.User) {
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

	http.SetCookie(w, &http.Cookie{
		Name:     "session_token",
		Value:    sessionToken,
		Path:     "/",
		MaxAge:   30 * 24 * 60 * 60,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})

	writeJSON(w, http.StatusOK, map[string]any{"data": user})
}

// --- GitHub OAuth helpers ---

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

func (h *AuthHandler) exchangeGitHubCode(code string) (*githubTokenResponse, error) {
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

// --- Google OAuth helpers ---

type googleTokenResponse struct {
	AccessToken string `json:"access_token"`
	TokenType   string `json:"token_type"`
	ExpiresIn   int    `json:"expires_in"`
	IDToken     string `json:"id_token"`
}

type googleUser struct {
	Sub     string `json:"sub"`
	Email   string `json:"email"`
	Name    string `json:"name"`
	Picture string `json:"picture"`
}

func (h *AuthHandler) exchangeGoogleCode(code string) (*googleTokenResponse, error) {
	data := url.Values{
		"client_id":     {h.cfg.GoogleOAuthClientID},
		"client_secret": {h.cfg.GoogleOAuthClientSecret},
		"code":          {code},
		"grant_type":    {"authorization_code"},
		"redirect_uri":  {h.cfg.BaseURL + "/api/v1/auth/google/callback"},
	}

	resp, err := http.PostForm("https://oauth2.googleapis.com/token", data)
	if err != nil {
		return nil, fmt.Errorf("google token exchange: %w", err)
	}
	defer resp.Body.Close()

	var tokenResp googleTokenResponse
	if err := json.NewDecoder(resp.Body).Decode(&tokenResp); err != nil {
		return nil, fmt.Errorf("decode google token response: %w", err)
	}
	if tokenResp.AccessToken == "" {
		return nil, fmt.Errorf("empty access token from Google")
	}
	return &tokenResp, nil
}

func (h *AuthHandler) fetchGoogleUser(accessToken string) (*googleUser, error) {
	req, err := http.NewRequest("GET", "https://www.googleapis.com/oauth2/v3/userinfo", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("google userinfo request: %w", err)
	}
	defer resp.Body.Close()

	var user googleUser
	if err := json.NewDecoder(resp.Body).Decode(&user); err != nil {
		return nil, fmt.Errorf("decode google userinfo: %w", err)
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
