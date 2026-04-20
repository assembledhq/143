package handlers

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/mail"
	"net/url"
	"strings"
	"time"

	chiMiddleware "github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog"
	"golang.org/x/crypto/bcrypt"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/config"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
)

type AuthHandler struct {
	cfg             *config.Config
	pool            db.TxStarter
	userStore       *db.UserStore
	sessionStore    *db.AuthSessionStore
	invitationStore *db.InvitationStore
	memberships     *db.OrganizationMembershipStore
	userCredentials *db.UserCredentialStore
	audit           *db.AuditEmitter
}

// SetAuditEmitter injects the audit emitter for logging auth events.
func (h *AuthHandler) SetAuditEmitter(audit *db.AuditEmitter) {
	h.audit = audit
}

// SetUserCredentialStore injects the user credential store for storing GitHub tokens.
func (h *AuthHandler) SetUserCredentialStore(store *db.UserCredentialStore) {
	h.userCredentials = store
}

func NewAuthHandler(
	cfg *config.Config,
	pool db.TxStarter,
	userStore *db.UserStore,
	sessionStore *db.AuthSessionStore,
	invitationStore *db.InvitationStore,
	memberships *db.OrganizationMembershipStore,
) *AuthHandler {
	return &AuthHandler{
		cfg:             cfg,
		pool:            pool,
		userStore:       userStore,
		sessionStore:    sessionStore,
		invitationStore: invitationStore,
		memberships:     memberships,
	}
}

// Providers returns which auth methods are configured.
//
// When DemoMode is on, "github" is forced to false regardless of OAuth
// configuration so the login page does not offer a button that would 500
// against the stubbed GitHub client. "demo" tells the frontend to render
// the seeded-credentials banner, and "demo_email" / "demo_password" carry
// the banner text so a reviewer sees whatever the server was actually
// seeded with (server is the single source of truth, not the TSX).
func (h *AuthHandler) Providers(w http.ResponseWriter, r *http.Request) {
	githubEnabled := h.cfg.GitHubOAuthClientID != "" && !h.cfg.DemoMode
	data := map[string]any{
		"github": githubEnabled,
		"google": h.cfg.GoogleOAuthClientID != "",
		"email":  true,
		"demo":   h.cfg.DemoMode,
	}
	if h.cfg.DemoMode {
		data["demo_email"] = h.cfg.DemoEmail
		data["demo_password"] = h.cfg.DemoPassword
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": data})
}

// Me returns the currently authenticated user.
func (h *AuthHandler) Me(w http.ResponseWriter, r *http.Request) {
	user := middleware.UserFromContext(r.Context())
	if user == nil {
		writeError(w, r, http.StatusUnauthorized, "UNAUTHORIZED", "not authenticated")
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": user})
}

// Register handles email/password sign-up.
// If an invitation token is provided, the user joins the inviting org instead of creating a new one.
func (h *AuthHandler) Register(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Email      string `json:"email"`
		Password   string `json:"password"` // #nosec G117 -- request body field
		Name       string `json:"name"`
		Invitation string `json:"invitation"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_BODY", "invalid request body")
		return
	}

	body.Email = strings.TrimSpace(body.Email)
	body.Name = strings.TrimSpace(body.Name)

	if body.Name == "" {
		writeError(w, r, http.StatusBadRequest, "MISSING_NAME", "Name is required.")
		return
	}
	if _, err := mail.ParseAddress(body.Email); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_EMAIL", "Invalid email address.")
		return
	}
	if len(body.Password) < 8 {
		writeError(w, r, http.StatusBadRequest, "WEAK_PASSWORD", "Password must be at least 8 characters.")
		return
	}

	// Check for existing user
	if _, err := h.userStore.GetByEmail(r.Context(), body.Email); err == nil {
		writeError(w, r, http.StatusConflict, "EMAIL_EXISTS", "An account with this email already exists.")
		return
	}

	hash, err := bcrypt.GenerateFromPassword([]byte(body.Password), bcrypt.DefaultCost)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to hash password", err)
		return
	}

	hashStr := string(hash)

	// If invitation token is provided, join the inviting org.
	if body.Invitation != "" {
		// Clear any stale pending_invitation cookie from a prior OAuth attempt.
		http.SetCookie(w, &http.Cookie{Name: "pending_invitation", Value: "", Path: "/", MaxAge: -1, HttpOnly: true})

		user, invErr, registerErr := h.createInvitedUserWithPassword(r.Context(), body.Invitation, body.Email, body.Name, hashStr)
		if invErr != nil {
			writeError(w, r, invErr.status, invErr.code, invErr.message)
			return
		}
		if registerErr != nil {
			writeError(w, r, http.StatusInternalServerError, "USER_CREATE_FAILED", "failed to create user", registerErr)
			return
		}
		h.emitAuthEvent(r, user, models.AuditActionAuthRegister)
		h.createSessionAndRespond(w, r, user)
		return
	}

	// No invitation — atomically create a new org, user, and admin membership.
	user := &models.User{
		Email:        body.Email,
		Name:         body.Name,
		PasswordHash: &hashStr,
	}
	if err := h.createSignupOrg(r.Context(), body.Name+"'s Org", user, func(ctx context.Context, us *db.UserStore, u *models.User) error {
		return us.CreateWithPassword(ctx, u)
	}); err != nil {
		writeError(w, r, http.StatusInternalServerError, "USER_CREATE_FAILED", "failed to create account", err)
		return
	}

	h.emitAuthEvent(r, user, models.AuditActionAuthRegister)
	h.createSessionAndRespond(w, r, user)
}

// EmailLogin handles email/password sign-in.
func (h *AuthHandler) EmailLogin(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Email    string `json:"email"`
		Password string `json:"password"` // #nosec G117 -- request body field
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_BODY", "invalid request body")
		return
	}

	body.Email = strings.TrimSpace(body.Email)

	user, err := h.userStore.GetByEmail(r.Context(), body.Email)
	if err != nil {
		writeError(w, r, http.StatusUnauthorized, "INVALID_CREDENTIALS", "Invalid email or password.")
		return
	}

	if user.PasswordHash == nil {
		writeError(w, r, http.StatusUnauthorized, "OAUTH_ONLY", "This account uses social login. Please sign in with GitHub or Google.")
		return
	}

	if err := bcrypt.CompareHashAndPassword([]byte(*user.PasswordHash), []byte(body.Password)); err != nil {
		writeError(w, r, http.StatusUnauthorized, "INVALID_CREDENTIALS", "Invalid email or password.")
		return
	}

	h.emitAuthEvent(r, &user, models.AuditActionAuthLogin)
	h.createSessionAndRespond(w, r, &user)
}

// Login redirects to GitHub OAuth.
// If ?invitation=TOKEN is provided, it's stored in a cookie for use after the callback.
func (h *AuthHandler) Login(w http.ResponseWriter, r *http.Request) {
	state, err := setOAuthState(w, githubOAuthStateCookie)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to generate state", err)
		return
	}

	if invToken := r.URL.Query().Get("invitation"); invToken != "" {
		http.SetCookie(w, &http.Cookie{
			Name:     "pending_invitation",
			Value:    invToken,
			Path:     "/",
			MaxAge:   600,
			HttpOnly: true,
			SameSite: http.SameSiteLaxMode,
		})
	}

	if returnTo := r.URL.Query().Get("return_to"); returnTo != "" {
		http.SetCookie(w, &http.Cookie{
			Name:     "oauth_return_to",
			Value:    returnTo,
			Path:     "/",
			MaxAge:   600,
			HttpOnly: true,
			SameSite: http.SameSiteLaxMode,
		})
	}

	params := url.Values{
		"client_id":    {h.cfg.GitHubOAuthClientID},
		"redirect_uri": {h.cfg.GitHubOAuthRedirectURI},
		"scope":        {"read:user,user:email"},
		"state":        {state},
	}

	http.Redirect(w, r, "https://github.com/login/oauth/authorize?"+params.Encode(), http.StatusTemporaryRedirect)
}

// Callback handles GitHub OAuth callback.
func (h *AuthHandler) Callback(w http.ResponseWriter, r *http.Request) {
	// GitHub App installation redirects here with setup_action=install (new)
	// or setup_action=update (reconfigured existing installation).
	// This is not an OAuth flow — redirect to the authenticated integration
	// endpoint that creates the integration record for the user's org.
	if sa := r.URL.Query().Get("setup_action"); sa == "install" || sa == "update" {
		redirectURL := h.cfg.BaseURL + "/api/v1/integrations/github/installed"
		if qs := r.URL.RawQuery; qs != "" {
			redirectURL += "?" + qs
		}
		http.Redirect(w, r, redirectURL, http.StatusTemporaryRedirect)
		return
	}

	code, ok := validateOAuthCallback(w, r, githubOAuthStateCookie)
	if !ok {
		return
	}

	// Exchange code for access token
	tokenResp, err := h.exchangeGitHubCode(code)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "TOKEN_EXCHANGE_FAILED", "failed to exchange code", err)
		return
	}

	// Fetch GitHub user
	ghUser, err := h.fetchGitHubUser(tokenResp.AccessToken)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "GITHUB_API_FAILED", "failed to fetch GitHub user", err)
		return
	}

	email := ghUser.Email
	if email == "" {
		email = ghUser.Login + "@users.noreply.github.com"
	}

	// Account linking: try GitHub ID → email → create new.
	pendingInvite := readAndClearPendingInvitationCookie(w, r)

	existingUser, err := h.userStore.GetByGitHubID(r.Context(), ghUser.ID)
	if err == nil {
		// Known GitHub user — update and sign in.
		existingUser.Name = ghUser.Name
		existingUser.Email = email
		existingUser.GitHubLogin = &ghUser.Login
		existingUser.AvatarURL = &ghUser.AvatarURL
		if upsertErr := h.userStore.UpsertFromGitHub(r.Context(), &existingUser); upsertErr != nil {
			writeError(w, r, http.StatusInternalServerError, "USER_UPSERT_FAILED", "failed to upsert user", upsertErr)
			return
		}
		h.claimPendingInvitationForExistingUser(r, pendingInvite, email, ghUser.Login, existingUser.ID)
		h.storeGitHubToken(r, &existingUser, tokenResp)
		h.emitAuthEvent(r, &existingUser, models.AuditActionAuthLogin)
		h.createSessionAndRedirect(w, r, &existingUser)
		return
	}

	// Try email match for account linking.
	if emailUser, emailErr := h.userStore.GetByEmail(r.Context(), email); emailErr == nil {
		if linkErr := h.userStore.LinkGitHubAccount(r.Context(), emailUser.ID, emailUser.OrgID, ghUser.ID, ghUser.Login, ghUser.AvatarURL); linkErr != nil {
			writeError(w, r, http.StatusInternalServerError, "LINK_FAILED", "failed to link GitHub account", linkErr)
			return
		}
		h.claimPendingInvitationForExistingUser(r, pendingInvite, email, ghUser.Login, emailUser.ID)
		h.storeGitHubToken(r, &emailUser, tokenResp)
		h.emitAuthEvent(r, &emailUser, models.AuditActionAuthLogin)
		h.createSessionAndRedirect(w, r, &emailUser)
		return
	}

	// New user — either claim a pending invitation or create a fresh org.
	if pendingInvite != "" {
		inv, _, role, invErr := h.validateInvitation(r.Context(), pendingInvite, email, ghUser.Login)
		if invErr == nil {
			user := &models.User{
				OrgID:       inv.OrgID,
				Email:       email,
				Name:        ghUser.Name,
				Role:        role,
				GitHubID:    &ghUser.ID,
				GitHubLogin: &ghUser.Login,
				AvatarURL:   &ghUser.AvatarURL,
			}
			createdUser, claimErr, createErr := h.acceptInvitationAndUpsertUser(
				r.Context(),
				inv.ID,
				user,
				func(ctx context.Context, userStore *db.UserStore, invitedUser *models.User) error {
					return userStore.UpsertFromGitHub(ctx, invitedUser)
				},
			)
			if claimErr != nil {
				writeError(w, r, claimErr.status, claimErr.code, claimErr.message)
				return
			}
			if createErr != nil {
				writeError(w, r, http.StatusInternalServerError, "USER_UPSERT_FAILED", "failed to upsert user", createErr)
				return
			}
			h.storeGitHubToken(r, createdUser, tokenResp)
			h.emitAuthEvent(r, createdUser, models.AuditActionAuthRegister)
			h.createSessionAndRedirect(w, r, createdUser)
			return
		}
		// Invalid invitation (wrong email, expired, etc.) — fall through to
		// a default signup so the user isn't stranded.
	}

	user := &models.User{
		Email:       email,
		Name:        ghUser.Name,
		GitHubID:    &ghUser.ID,
		GitHubLogin: &ghUser.Login,
		AvatarURL:   &ghUser.AvatarURL,
	}
	if err := h.createSignupOrg(r.Context(), ghUser.Login+"'s Org", user, func(ctx context.Context, us *db.UserStore, u *models.User) error {
		return us.UpsertFromGitHub(ctx, u)
	}); err != nil {
		writeError(w, r, http.StatusInternalServerError, "USER_CREATE_FAILED", "failed to create account", err)
		return
	}

	h.storeGitHubToken(r, user, tokenResp)
	h.emitAuthEvent(r, user, models.AuditActionAuthRegister)
	h.createSessionAndRedirect(w, r, user)
}

// GoogleLogin redirects to Google OAuth.
// If ?invitation=TOKEN is provided, it's stored in a cookie for use after the callback.
func (h *AuthHandler) GoogleLogin(w http.ResponseWriter, r *http.Request) {
	state, err := setOAuthState(w, googleOAuthStateCookie)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to generate state", err)
		return
	}

	if invToken := r.URL.Query().Get("invitation"); invToken != "" {
		http.SetCookie(w, &http.Cookie{
			Name:     "pending_invitation",
			Value:    invToken,
			Path:     "/",
			MaxAge:   600,
			HttpOnly: true,
			SameSite: http.SameSiteLaxMode,
		})
	}

	if returnTo := r.URL.Query().Get("return_to"); returnTo != "" {
		http.SetCookie(w, &http.Cookie{
			Name:     "oauth_return_to",
			Value:    returnTo,
			Path:     "/",
			MaxAge:   600,
			HttpOnly: true,
			SameSite: http.SameSiteLaxMode,
		})
	}

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
	code, ok := validateOAuthCallback(w, r, googleOAuthStateCookie)
	if !ok {
		return
	}

	// Exchange code for access token
	tokenResp, err := h.exchangeGoogleCode(code)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "TOKEN_EXCHANGE_FAILED", "failed to exchange code", err)
		return
	}

	// Fetch Google user info
	gUser, err := h.fetchGoogleUser(tokenResp.AccessToken)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "GOOGLE_API_FAILED", "failed to fetch Google user", err)
		return
	}

	pendingInvite := readAndClearPendingInvitationCookie(w, r)

	// Account linking: try Google ID → email → create new.
	if existingUser, googleErr := h.userStore.GetByGoogleID(r.Context(), gUser.Sub); googleErr == nil {
		h.claimPendingInvitationForExistingUser(r, pendingInvite, gUser.Email, "", existingUser.ID)
		h.emitAuthEvent(r, &existingUser, models.AuditActionAuthLogin)
		h.createSessionAndRedirect(w, r, &existingUser)
		return
	}

	if emailUser, emailErr := h.userStore.GetByEmail(r.Context(), gUser.Email); emailErr == nil {
		if linkErr := h.userStore.LinkGoogleAccount(r.Context(), emailUser.ID, emailUser.OrgID, gUser.Sub, gUser.Picture); linkErr != nil {
			writeError(w, r, http.StatusInternalServerError, "LINK_FAILED", "failed to link Google account", linkErr)
			return
		}
		h.claimPendingInvitationForExistingUser(r, pendingInvite, gUser.Email, "", emailUser.ID)
		h.emitAuthEvent(r, &emailUser, models.AuditActionAuthLogin)
		h.createSessionAndRedirect(w, r, &emailUser)
		return
	}

	// New user — either claim a pending invitation or create a fresh org.
	name := gUser.Name
	if name == "" {
		name = gUser.Email
	}

	if pendingInvite != "" {
		inv, _, role, invErr := h.validateInvitation(r.Context(), pendingInvite, gUser.Email, "")
		if invErr == nil {
			user := &models.User{
				OrgID:     inv.OrgID,
				Email:     gUser.Email,
				Name:      name,
				Role:      role,
				GoogleID:  &gUser.Sub,
				AvatarURL: &gUser.Picture,
			}
			createdUser, claimErr, createErr := h.acceptInvitationAndUpsertUser(
				r.Context(),
				inv.ID,
				user,
				func(ctx context.Context, userStore *db.UserStore, invitedUser *models.User) error {
					return userStore.UpsertFromGoogle(ctx, invitedUser)
				},
			)
			if claimErr != nil {
				writeError(w, r, claimErr.status, claimErr.code, claimErr.message)
				return
			}
			if createErr != nil {
				writeError(w, r, http.StatusInternalServerError, "USER_UPSERT_FAILED", "failed to upsert user", createErr)
				return
			}
			h.emitAuthEvent(r, createdUser, models.AuditActionAuthRegister)
			h.createSessionAndRedirect(w, r, createdUser)
			return
		}
		// Invalid invitation — fall through to default signup.
	}

	user := &models.User{
		Email:     gUser.Email,
		Name:      name,
		GoogleID:  &gUser.Sub,
		AvatarURL: &gUser.Picture,
	}
	if err := h.createSignupOrg(r.Context(), name+"'s Org", user, func(ctx context.Context, us *db.UserStore, u *models.User) error {
		return us.UpsertFromGoogle(ctx, u)
	}); err != nil {
		writeError(w, r, http.StatusInternalServerError, "USER_CREATE_FAILED", "failed to create account", err)
		return
	}

	h.emitAuthEvent(r, user, models.AuditActionAuthRegister)
	h.createSessionAndRedirect(w, r, user)
}

// ClaimInvitation accepts an invitation on behalf of the currently signed-in
// user, granting them a membership in the inviting org. This is how an
// already-authenticated user joins a second organization without going
// through another OAuth round-trip.
//
// The caller's email (and GitHub login, when present) must match the
// invitation per the same rules as signup — an invite for x@foo.com cannot
// be claimed by a session belonging to y@bar.com. On success the handler
// returns the new membership summary so the frontend can update the
// org switcher without a second round-trip.
func (h *AuthHandler) ClaimInvitation(w http.ResponseWriter, r *http.Request) {
	user := middleware.UserFromContext(r.Context())
	if user == nil {
		writeError(w, r, http.StatusUnauthorized, "UNAUTHORIZED", "not authenticated")
		return
	}

	var body struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || strings.TrimSpace(body.Token) == "" {
		writeError(w, r, http.StatusBadRequest, "INVALID_BODY", "token is required")
		return
	}

	githubLogin := ""
	if user.GitHubLogin != nil {
		githubLogin = *user.GitHubLogin
	}

	inv, invErr, err := h.claimInvitationForExistingUser(r.Context(), body.Token, user.Email, githubLogin, user.ID)
	if err != nil {
		h.emitInvitationClaimFailed(r, user.ID, inv, "INTERNAL_ERROR", err.Error())
		writeError(w, r, http.StatusInternalServerError, "CLAIM_FAILED", "failed to claim invitation", err)
		return
	}
	if invErr != nil {
		h.emitInvitationClaimFailed(r, user.ID, inv, invErr.code, invErr.message)
		writeError(w, r, invErr.status, invErr.code, invErr.message)
		return
	}

	h.emitInvitationAccepted(r, user.ID, inv)

	writeJSON(w, http.StatusOK, map[string]any{
		"data": map[string]any{
			"org_id": inv.OrgID,
			"role":   inv.Role,
		},
	})
}

func (h *AuthHandler) Logout(w http.ResponseWriter, r *http.Request) {
	cookie, err := r.Cookie("session_token")
	if err == nil {
		if deleteErr := h.sessionStore.DeleteByToken(r.Context(), cookie.Value); deleteErr != nil {
			writeError(w, r, http.StatusInternalServerError, "SESSION_DELETE_FAILED", "failed to delete session", deleteErr)
			return
		}
	}

	http.SetCookie(w, &http.Cookie{
		Name:     middleware.SessionCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   middleware.IsRequestSecure(r),
	})

	// Clear CSRF cookie on logout.
	http.SetCookie(w, &http.Cookie{
		Name:     middleware.CSRFCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: false,
		SameSite: http.SameSiteLaxMode,
		Secure:   middleware.IsRequestSecure(r),
	})

	user := middleware.UserFromContext(r.Context())
	if user != nil {
		h.emitAuthEvent(r, user, models.AuditActionAuthLogout)
	}
	writeJSON(w, http.StatusOK, map[string]string{"status": "logged out"})
}

// --- shared helpers ---

func (h *AuthHandler) createSessionAndRedirect(w http.ResponseWriter, r *http.Request, user *models.User) {
	sessionToken, err := generateRandomString(32)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to generate session token", err)
		return
	}
	session := &models.AuthSession{
		UserID:    user.ID,
		OrgID:     user.OrgID,
		Token:     sessionToken,
		ExpiresAt: time.Now().Add(middleware.SessionTTL),
	}
	if err := h.sessionStore.Create(r.Context(), session); err != nil {
		writeError(w, r, http.StatusInternalServerError, "SESSION_CREATE_FAILED", "failed to create session", err)
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:     middleware.SessionCookieName,
		Value:    sessionToken,
		Path:     "/",
		MaxAge:   int(middleware.SessionTTL.Seconds()),
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   middleware.IsRequestSecure(r),
	})

	if err := middleware.SetCSRFCookie(w, r, []byte(h.cfg.CSRFSigningKey)); err != nil {
		writeError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to generate CSRF token", err)
		return
	}

	redirectURL := h.cfg.FrontendURL
	if returnCookie, err := r.Cookie("oauth_return_to"); err == nil && returnCookie.Value != "" {
		// Clear the cookie.
		http.SetCookie(w, &http.Cookie{Name: "oauth_return_to", Value: "", Path: "/", MaxAge: -1, HttpOnly: true})
		// Only allow relative paths to prevent open redirect.
		if len(returnCookie.Value) > 0 && returnCookie.Value[0] == '/' {
			redirectURL = h.cfg.FrontendURL + returnCookie.Value
		}
	}

	http.Redirect(w, r, redirectURL, http.StatusTemporaryRedirect)
}

func (h *AuthHandler) createSessionAndRespond(w http.ResponseWriter, r *http.Request, user *models.User) {
	sessionToken, err := generateRandomString(32)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to generate session token", err)
		return
	}
	session := &models.AuthSession{
		UserID:    user.ID,
		OrgID:     user.OrgID,
		Token:     sessionToken,
		ExpiresAt: time.Now().Add(middleware.SessionTTL),
	}
	if err := h.sessionStore.Create(r.Context(), session); err != nil {
		writeError(w, r, http.StatusInternalServerError, "SESSION_CREATE_FAILED", "failed to create session", err)
		return
	}

	http.SetCookie(w, &http.Cookie{
		Name:     middleware.SessionCookieName,
		Value:    sessionToken,
		Path:     "/",
		MaxAge:   int(middleware.SessionTTL.Seconds()),
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   middleware.IsRequestSecure(r),
	})

	if err := middleware.SetCSRFCookie(w, r, []byte(h.cfg.CSRFSigningKey)); err != nil {
		writeError(w, r, http.StatusInternalServerError, "INTERNAL_ERROR", "failed to generate CSRF token", err)
		return
	}

	writeJSON(w, http.StatusOK, map[string]any{"data": user})
}

// storeGitHubToken persists the user's GitHub OAuth token for PR creation.
// Non-fatal: user can still sign in even if token storage fails.
func (h *AuthHandler) storeGitHubToken(r *http.Request, user *models.User, tokenResp *githubTokenResponse) {
	if h.userCredentials == nil || tokenResp == nil || tokenResp.AccessToken == "" {
		return
	}
	cfg := models.GitHubOAuthConfig{
		AccessToken: tokenResp.AccessToken,
		TokenType:   tokenResp.TokenType,
		Scope:       tokenResp.Scope,
	}
	if err := h.userCredentials.Upsert(r.Context(), user.ID, user.OrgID, cfg, false); err != nil {
		// Non-fatal — user can still sign in, just can't create PRs as themselves.
		zerolog.Ctx(r.Context()).Warn().Err(err).Str("user_id", user.ID.String()).Msg("failed to store GitHub OAuth token for PR authorship")
	}
}

// --- GitHub OAuth helpers ---

type githubTokenResponse struct {
	AccessToken string `json:"access_token"` // #nosec G117 -- OAuth response field
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
	return exchangeGitHubOAuthCode(h.cfg.GitHubOAuthClientID, h.cfg.GitHubOAuthClientSecret, code)
}

// exchangeGitHubOAuthCode is the shared implementation for exchanging a GitHub
// OAuth authorization code for an access token.
func exchangeGitHubOAuthCode(clientID, clientSecret, code string) (*githubTokenResponse, error) {
	data := url.Values{
		"client_id":     {clientID},
		"client_secret": {clientSecret},
		"code":          {code},
	}

	req, err := http.NewRequest("POST", "https://github.com/login/oauth/access_token", nil)
	if err != nil {
		return nil, err
	}
	req.URL.RawQuery = data.Encode()
	req.Header.Set("Accept", "application/json")

	resp, err := http.DefaultClient.Do(req) // #nosec G704 -- URL is GitHub OAuth endpoint
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

	resp, err := http.DefaultClient.Do(req) // #nosec G704 -- URL is GitHub API endpoint
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
	AccessToken string `json:"access_token"` // #nosec G117 -- OAuth response field
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

	resp, err := http.DefaultClient.Do(req) // #nosec G704 -- URL is Google OAuth endpoint
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

type invitationLookupStore interface {
	GetByToken(ctx context.Context, token string) (models.Invitation, error)
}

// invitationError is a structured error for invitation validation failures.
type invitationError struct {
	status  int
	code    string
	message string
}

type oauthUserUpsertFunc func(ctx context.Context, userStore *db.UserStore, user *models.User) error

// acceptInvitationAndUpsertUser atomically accepts an invitation, upserts the
// invited OAuth user, and grants the user a membership in the inviting org.
// The three writes share a transaction so membership is never granted without
// a successful claim (and the claim is never left stranded if the user row or
// membership row fails to insert).
func (h *AuthHandler) acceptInvitationAndUpsertUser(
	ctx context.Context,
	invitationID uuid.UUID,
	user *models.User,
	upsert oauthUserUpsertFunc,
) (*models.User, *invitationError, error) {
	if h.pool == nil {
		return nil, nil, fmt.Errorf("auth handler pool is not configured")
	}
	if user == nil {
		return nil, nil, fmt.Errorf("user is required")
	}
	if upsert == nil {
		return nil, nil, fmt.Errorf("oauth user upsert function is required")
	}

	tx, err := h.pool.Begin(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("begin invitation transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	txInvitationStore := db.NewInvitationStore(tx)
	if err := txInvitationStore.Accept(ctx, invitationID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, &invitationError{
				status:  http.StatusGone,
				code:    "INVITE_INVALID",
				message: "this invitation is no longer valid",
			}, nil
		}
		return nil, nil, fmt.Errorf("accept invitation: %w", err)
	}

	txUserStore := db.NewUserStore(tx)
	if err := upsert(ctx, txUserStore, user); err != nil {
		return nil, nil, fmt.Errorf("upsert invited oauth user: %w", err)
	}

	if err := db.NewOrganizationMembershipStore(tx).Upsert(ctx, user.ID, user.OrgID, user.Role); err != nil {
		return nil, nil, fmt.Errorf("grant invited membership: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, nil, fmt.Errorf("commit invitation transaction: %w", err)
	}

	return user, nil, nil
}

// createInvitedUserWithPassword validates and claims an invitation, creates
// the user, and grants a membership — all in one transaction so membership is
// never granted without a successful claim.
func (h *AuthHandler) createInvitedUserWithPassword(ctx context.Context, token, email, name, hash string) (*models.User, *invitationError, error) {
	if h.pool == nil {
		return nil, nil, fmt.Errorf("auth handler pool is not configured")
	}
	if h.userStore == nil {
		return nil, nil, fmt.Errorf("user store is not configured")
	}

	tx, err := h.pool.Begin(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("begin invitation transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	txInvitationStore := db.NewInvitationStore(tx)
	txUserStore := db.NewUserStore(tx)

	inv, orgID, role, invErr := h.validateInvitationWithStore(ctx, txInvitationStore, token, email, "")
	if invErr != nil {
		return nil, invErr, nil
	}

	if err := txInvitationStore.Accept(ctx, inv.ID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, &invitationError{
				status:  http.StatusGone,
				code:    "INVITE_INVALID",
				message: "this invitation is no longer valid",
			}, nil
		}
		return nil, nil, fmt.Errorf("accept invitation: %w", err)
	}

	user := &models.User{
		OrgID:        orgID,
		Email:        email,
		Name:         name,
		Role:         role,
		PasswordHash: &hash,
	}
	if err := txUserStore.CreateWithPassword(ctx, user); err != nil {
		return nil, nil, fmt.Errorf("create invited user: %w", err)
	}

	if err := db.NewOrganizationMembershipStore(tx).Upsert(ctx, user.ID, orgID, role); err != nil {
		return nil, nil, fmt.Errorf("grant invited membership: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, nil, fmt.Errorf("commit invitation transaction: %w", err)
	}

	return user, nil, nil
}

// validateInvitation looks up and validates an invitation token.
// It checks that the invitation is pending, not expired, and that either the
// email or the GitHub login of the signing-in user matches the invitation.
// For an email-only invitation, the email must match. For a GitHub-only
// invitation, the GitHub login must match. If both identifiers are set on the
// invitation, either a matching email or a matching GitHub login is accepted.
func (h *AuthHandler) validateInvitation(ctx context.Context, token, email, githubLogin string) (models.Invitation, uuid.UUID, string, *invitationError) {
	return h.validateInvitationWithStore(ctx, h.invitationStore, token, email, githubLogin)
}

func (h *AuthHandler) validateInvitationWithStore(ctx context.Context, invitationStore invitationLookupStore, token, email, githubLogin string) (models.Invitation, uuid.UUID, string, *invitationError) {
	if invitationStore == nil {
		return models.Invitation{}, uuid.Nil, "", &invitationError{http.StatusInternalServerError, "INVITE_LOOKUP_FAILED", "failed to look up invitation"}
	}

	inv, err := invitationStore.GetByToken(ctx, token)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return inv, uuid.Nil, "", &invitationError{http.StatusBadRequest, "INVITE_NOT_FOUND", "invitation not found"}
		}
		return inv, uuid.Nil, "", &invitationError{http.StatusInternalServerError, "INVITE_LOOKUP_FAILED", "failed to look up invitation"}
	}

	if inv.Status != "pending" {
		return inv, uuid.Nil, "", &invitationError{http.StatusGone, "INVITE_INVALID", "this invitation is no longer valid"}
	}

	if time.Now().After(inv.ExpiresAt) {
		return inv, uuid.Nil, "", &invitationError{http.StatusGone, "INVITE_EXPIRED", "this invitation has expired"}
	}

	emailMatches := inv.Email != nil && email != "" && strings.EqualFold(*inv.Email, email)
	githubMatches := inv.GitHubUsername != nil && githubLogin != "" && strings.EqualFold(*inv.GitHubUsername, githubLogin)
	if !emailMatches && !githubMatches {
		return inv, uuid.Nil, "", &invitationError{http.StatusBadRequest, "INVITE_MISMATCH", "this account does not match the invitation"}
	}

	return inv, inv.OrgID, inv.Role, nil
}

// emitAuthEvent emits an audit log entry for an authentication event.
// It works even before org context middleware runs (e.g., during registration/login).
func (h *AuthHandler) emitAuthEvent(r *http.Request, user *models.User, action models.AuditAction) {
	if h.audit == nil || user == nil {
		return
	}
	userIDStr := user.ID.String()
	params := db.UserActionParams{
		OrgID:        user.OrgID,
		UserID:       user.ID,
		Action:       action,
		ResourceType: models.AuditResourceUser,
		ResourceID:   &userIDStr,
	}
	if reqID := chiMiddleware.GetReqID(r.Context()); reqID != "" {
		params.RequestID = &reqID
	}
	if ua := r.UserAgent(); ua != "" {
		params.UserAgent = &ua
	}
	if ip := parseClientIP(r); ip != nil {
		params.IPAddress = ip
	}
	h.audit.EmitUserAction(r.Context(), params)
}

func generateRandomString(n int) (string, error) {
	bytes := make([]byte, n)
	if _, err := rand.Read(bytes); err != nil {
		return "", err
	}
	return hex.EncodeToString(bytes), nil
}
