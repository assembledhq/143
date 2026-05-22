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
	// gitHubAPIBaseURL / gitHubOAuthBaseURL are overridable so tests can
	// point the OAuth callback flow at a local httptest.Server instead of
	// mutating http.DefaultTransport (which would silently redirect any
	// HTTP traffic from concurrent tests in the same binary). Empty strings
	// mean "use the production GitHub URL".
	gitHubAPIBaseURL   string
	gitHubOAuthBaseURL string
	// httpClient is optional. When nil, callers fall back to
	// http.DefaultClient (production) or a locally-scoped client with a
	// short timeout (the noreply-email probe).
	httpClient *http.Client
}

// SetGitHubURLsForTest overrides the GitHub API and OAuth base URLs and
// the HTTP client. Test-only — production paths use the real github.com /
// api.github.com endpoints. Pass an empty string to leave a default.
func (h *AuthHandler) SetGitHubURLsForTest(apiBaseURL, oauthBaseURL string, client *http.Client) {
	h.gitHubAPIBaseURL = apiBaseURL
	h.gitHubOAuthBaseURL = oauthBaseURL
	h.httpClient = client
}

func (h *AuthHandler) gitHubAPIBase() string {
	if h.gitHubAPIBaseURL != "" {
		return strings.TrimRight(h.gitHubAPIBaseURL, "/")
	}
	return "https://api.github.com"
}

func (h *AuthHandler) gitHubOAuthBase() string {
	if h.gitHubOAuthBaseURL != "" {
		return strings.TrimRight(h.gitHubOAuthBaseURL, "/")
	}
	return "https://github.com"
}

func (h *AuthHandler) gitHubHTTPClient() *http.Client {
	if h.httpClient != nil {
		return h.httpClient
	}
	return http.DefaultClient
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
	if h.userStore == nil {
		writeJSON(w, http.StatusOK, map[string]any{"data": user})
		return
	}
	loadedUser, err := h.userStore.GetByIDGlobalWithSettings(r.Context(), user.ID)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "USER_LOOKUP_FAILED", "failed to load user settings", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": models.UserWithSettings{
		ID:          user.ID,
		OrgID:       user.OrgID,
		Email:       loadedUser.Email,
		Name:        loadedUser.Name,
		Role:        user.Role,
		GitHubID:    loadedUser.GitHubID,
		GitHubLogin: loadedUser.GitHubLogin,
		AvatarURL:   loadedUser.AvatarURL,
		GoogleID:    loadedUser.GoogleID,
		Settings:    loadedUser.Settings,
		CreatedAt:   loadedUser.CreatedAt,
	}})
}

// UpdateSettings updates the authenticated user's personal settings document.
func (h *AuthHandler) UpdateSettings(w http.ResponseWriter, r *http.Request) {
	user := middleware.UserFromContext(r.Context())
	if user == nil {
		writeError(w, r, http.StatusUnauthorized, "UNAUTHORIZED", "not authenticated")
		return
	}
	_ = middleware.OrgIDFromContext(r.Context())
	if h.userStore == nil {
		writeError(w, r, http.StatusInternalServerError, "USER_STORE_UNCONFIGURED", "user settings store not configured")
		return
	}

	var body models.UserSettings
	decoder := json.NewDecoder(r.Body)
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&body); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_BODY", "invalid request body")
		return
	}
	if err := body.Validate(); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_USER_SETTINGS", err.Error())
		return
	}
	if err := h.userStore.UpdateSettings(r.Context(), user.ID, body); err != nil {
		writeError(w, r, http.StatusInternalServerError, "USER_SETTINGS_UPDATE_FAILED", "failed to update user settings", err)
		return
	}

	updatedUser, err := h.userStore.GetByIDGlobalWithSettings(r.Context(), user.ID)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "USER_LOOKUP_FAILED", "failed to load updated user settings", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": models.UserWithSettings{
		ID:          user.ID,
		OrgID:       user.OrgID,
		Email:       updatedUser.Email,
		Name:        updatedUser.Name,
		Role:        user.Role,
		GitHubID:    updatedUser.GitHubID,
		GitHubLogin: updatedUser.GitHubLogin,
		AvatarURL:   updatedUser.AvatarURL,
		GoogleID:    updatedUser.GoogleID,
		Settings:    updatedUser.Settings,
		CreatedAt:   updatedUser.CreatedAt,
	}})
}

// Memberships returns the authenticated user's full membership set together
// with the active-org resolution the middleware picked for this request.
//
// This is a separate endpoint rather than a field on /auth/me because the
// /auth/me response shape is pinned by the frontend wire contract during the
// compat window (see legacy_user_fields_lint_test.go — the 2026-04-25 sunset
// is where that shape change lands). Returning memberships here lets the org
// switcher render in one round-trip without waiting for the sunset step.
//
// Zero-membership users (user was removed from their last org) still get 200
// with an empty list and uuid.Nil active_org_id so the frontend can render
// the empty state rather than bouncing on a 403.
//
// lint:allow-no-orgid reason="user-scoped; returns the full membership set, not an org-scoped query"
func (h *AuthHandler) Memberships(w http.ResponseWriter, r *http.Request) {
	user := middleware.UserFromContext(r.Context())
	if user == nil {
		writeError(w, r, http.StatusUnauthorized, "UNAUTHORIZED", "not authenticated")
		return
	}

	memberships, err := h.memberships.ListByUser(r.Context(), user.ID)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "MEMBERSHIPS_LOOKUP_FAILED", "failed to list memberships", err)
		return
	}
	if memberships == nil {
		memberships = []models.MembershipSummary{}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"data": models.MembershipsResponse{
			ActiveOrgID: middleware.OrgIDFromContext(r.Context()),
			ActiveRole:  models.Role(middleware.ActiveRoleFromContext(r.Context())),
			Memberships: memberships,
		},
	})
}

// SetActiveOrg persists the user's explicitly selected org so future sessions
// can default to it across logins. The target org must be one the user
// currently belongs to.
//
// lint:allow-no-orgid reason="user-scoped preference write validated against memberships"
func (h *AuthHandler) SetActiveOrg(w http.ResponseWriter, r *http.Request) {
	user := middleware.UserFromContext(r.Context())
	if user == nil {
		writeError(w, r, http.StatusUnauthorized, "UNAUTHORIZED", "not authenticated")
		return
	}

	var body struct {
		OrgID string `json:"org_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_BODY", "invalid request body")
		return
	}

	orgID, err := uuid.Parse(strings.TrimSpace(body.OrgID))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_ORG_ID", "invalid organization id")
		return
	}

	if _, err := h.memberships.Get(r.Context(), user.ID, orgID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, r, http.StatusForbidden, "FORBIDDEN", "organization not available to user")
			return
		}
		writeError(w, r, http.StatusInternalServerError, "ACTIVE_ORG_LOOKUP_FAILED", "failed to validate organization membership", err)
		return
	}

	if err := h.userStore.UpdateLastOrgID(r.Context(), user.ID, &orgID); err != nil {
		writeError(w, r, http.StatusInternalServerError, "ACTIVE_ORG_UPDATE_FAILED", "failed to persist active organization", err)
		return
	}

	if h.sessionStore != nil {
		if cookie, err := r.Cookie(middleware.SessionCookieName); err == nil && cookie.Value != "" {
			if updateErr := h.sessionStore.UpdateLastOrgID(r.Context(), cookie.Value, &orgID); updateErr != nil {
				writeError(w, r, http.StatusInternalServerError, "ACTIVE_ORG_UPDATE_FAILED", "failed to persist active organization", updateErr)
				return
			}
		}
	}

	w.WriteHeader(http.StatusNoContent)
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

	// Check for existing user. A 409 here would strand an invited existing
	// user — they can't create a new account, but Register has no way to
	// authenticate them either. Surface a distinct code so the frontend can
	// route them to the sign-in + claim flow instead of showing the generic
	// "account already exists" error.
	if _, err := h.userStore.GetByEmail(r.Context(), body.Email); err == nil {
		if body.Invitation != "" {
			writeError(w, r, http.StatusConflict, "EMAIL_EXISTS_USE_LOGIN",
				"An account with this email already exists. Sign in and reopen the invitation link to join this organization.")
			return
		}
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

		user, sessionToken, invErr, registerErr := h.createInvitedUserWithPassword(r.Context(), body.Invitation, body.Email, body.Name, hashStr)
		if invErr != nil {
			writeError(w, r, invErr.status, invErr.code, invErr.message)
			return
		}
		if registerErr != nil {
			writeError(w, r, http.StatusInternalServerError, "USER_CREATE_FAILED", "failed to create user", registerErr)
			return
		}
		h.emitAuthEvent(r, user, models.AuditActionAuthRegister)
		h.respondWithSession(w, r, user, sessionToken)
		return
	}

	// No invitation — atomically create a new org, user, admin membership, and session.
	user := &models.User{
		Email:        body.Email,
		Name:         body.Name,
		PasswordHash: &hashStr,
	}
	sessionToken, err := h.createSignupOrg(r.Context(), body.Name+"'s Org", user, func(ctx context.Context, us *db.UserStore, u *models.User) error {
		return us.CreateWithPassword(ctx, u)
	})
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "USER_CREATE_FAILED", "failed to create account", err)
		return
	}

	h.emitAuthEvent(r, user, models.AuditActionAuthRegister)
	h.respondWithSession(w, r, user, sessionToken)
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

	// Compute the GitHub-attribution noreply email up-front so every account
	// path (existing, link, signup, invite) persists it consistently.
	noreplyEmail := h.fetchGitHubNoreplyEmail(r.Context(), tokenResp.AccessToken, ghUser.ID, ghUser.Login)

	email := ghUser.Email
	if email == "" {
		// Use the canonical user-id-prefixed form rather than the deprecated
		// `{login}@users.noreply.github.com` shape, so that commits authored
		// with this address stay linked to the user even if they later
		// rename their GitHub account.
		email = noreplyEmail
	}

	// GitHub returns "" for users who haven't set a public display name on
	// their profile. Fall back to the login so every UI surface that renders
	// users.name has something readable instead of an empty string.
	displayName := ghUser.Name
	if displayName == "" {
		displayName = ghUser.Login
	}

	// Account linking: try GitHub ID → email → create new.
	pendingInvite := readAndClearPendingInvitationCookie(w, r)

	existingUser, err := h.userStore.GetByGitHubID(r.Context(), ghUser.ID)
	if err == nil {
		// Known GitHub user — update and sign in.
		existingUser.Name = displayName
		existingUser.Email = email
		existingUser.GitHubLogin = &ghUser.Login
		existingUser.AvatarURL = &ghUser.AvatarURL
		existingUser.GitHubNoreplyEmail = &noreplyEmail
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
		if linkErr := h.userStore.LinkGitHubAccount(r.Context(), emailUser.ID, emailUser.OrgID, ghUser.ID, ghUser.Login, ghUser.AvatarURL, noreplyEmail); linkErr != nil {
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
				OrgID:              inv.OrgID,
				Email:              email,
				Name:               displayName,
				Role:               role,
				GitHubID:           &ghUser.ID,
				GitHubLogin:        &ghUser.Login,
				GitHubNoreplyEmail: &noreplyEmail,
				AvatarURL:          &ghUser.AvatarURL,
			}
			createdUser, sessionToken, claimErr, createErr := h.acceptInvitationAndUpsertUser(
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
			h.redirectWithSession(w, r, sessionToken)
			return
		}
		// Invalid invitation (wrong email, expired, etc.) — fall through to
		// a default signup so the user isn't stranded.
	}

	user := &models.User{
		Email:              email,
		Name:               displayName,
		GitHubID:           &ghUser.ID,
		GitHubLogin:        &ghUser.Login,
		GitHubNoreplyEmail: &noreplyEmail,
		AvatarURL:          &ghUser.AvatarURL,
	}
	sessionToken, err := h.createSignupOrg(r.Context(), ghUser.Login+"'s Org", user, func(ctx context.Context, us *db.UserStore, u *models.User) error {
		return us.UpsertFromGitHub(ctx, u)
	})
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "USER_CREATE_FAILED", "failed to create account", err)
		return
	}

	h.storeGitHubToken(r, user, tokenResp)
	h.emitAuthEvent(r, user, models.AuditActionAuthRegister)
	h.redirectWithSession(w, r, sessionToken)
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
			createdUser, sessionToken, claimErr, createErr := h.acceptInvitationAndUpsertUser(
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
			h.redirectWithSession(w, r, sessionToken)
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
	sessionToken, err := h.createSignupOrg(r.Context(), name+"'s Org", user, func(ctx context.Context, us *db.UserStore, u *models.User) error {
		return us.UpsertFromGoogle(ctx, u)
	})
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "USER_CREATE_FAILED", "failed to create account", err)
		return
	}

	h.emitAuthEvent(r, user, models.AuditActionAuthRegister)
	h.redirectWithSession(w, r, sessionToken)
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

	inv, effectiveRole, invErr, err := h.claimInvitationForExistingUser(r.Context(), body.Token, user.Email, githubLogin, user.ID)
	if err != nil {
		// Never surface the wrapped error text through audit: it can contain
		// raw pgx / SQL details and the audit row is retained and served back
		// to admin UIs. Log the detail; persist a fixed generic message.
		zerolog.Ctx(r.Context()).Warn().Err(err).Str("user_id", user.ID.String()).Msg("failed to claim invitation")
		h.emitInvitationClaimFailed(r, user.ID, inv, "INTERNAL_ERROR", "internal error during invitation claim")
		writeError(w, r, http.StatusInternalServerError, "CLAIM_FAILED", "failed to claim invitation", err)
		return
	}
	if invErr != nil {
		// inv is nil when GetByToken failed to find any invitation — we have
		// no org context to attach an audit row to. Log at Warn so the
		// "unknown token submitted by authenticated user" signal is still
		// visible to security monitoring, with a token prefix (not the full
		// token) so ops can correlate bursts without the log becoming a
		// secret store.
		if inv == nil {
			logInvitationClaimUnknownToken(r, user.ID, body.Token, invErr.code)
		} else {
			h.emitInvitationClaimFailed(r, user.ID, inv, invErr.code, invErr.message)
		}
		writeError(w, r, invErr.status, invErr.code, invErr.message)
		return
	}

	h.emitInvitationAccepted(r, user.ID, inv)

	// Echo the *effective* role, not the invite's role: GrantAtLeast never
	// downgrades, so a user who already held admin stays admin even if the
	// invite named a lower role. Returning inv.Role would mislead the
	// frontend switcher into rendering a role the user doesn't actually hold.
	writeJSON(w, http.StatusOK, map[string]any{
		"data": map[string]any{
			"org_id": inv.OrgID,
			"role":   effectiveRole,
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

// persistSessionTx inserts a session row for the user via the given DBTX and
// returns the opaque token. Callers pass a pgx.Tx for signup flows (so the
// session lives or dies with the transaction that created the user row) or
// h.pool for login flows (where the user already exists and there is no
// surrounding transaction).
func (h *AuthHandler) persistSessionTx(ctx context.Context, dbtx db.DBTX, user *models.User) (string, error) {
	sessionToken, err := generateRandomString(32)
	if err != nil {
		return "", fmt.Errorf("generate session token: %w", err)
	}
	userStore := db.NewUserStore(dbtx)
	lastOrgID, err := userStore.GetLastOrgID(ctx, user.ID)
	if err != nil {
		return "", fmt.Errorf("get user last_org_id: %w", err)
	}
	// TODO(2026-04-25): drop OrgID from AuthSession once the legacy column
	// is removed. Session->org resolution now flows through the middleware
	// (X-Active-Org-ID header → session.last_org_id → oldest membership);
	// auth_sessions.org_id is only kept in sync so pre-migration readers
	// don't regress during the sunset window.
	//
	// The AuthSession.OrgID readers that must be audited before the column
	// drop (enforced by auth_session_orgid_lint_test.go):
	//   - internal/db/auth_sessions.go:Create — writes the NOT NULL column on
	//     INSERT. This read goes away the same day the migration drops the
	//     column; the lint test's allowlist entry disappears alongside it.
	//   - internal/db/auth_sessions_test.go — test fixtures that seed the
	//     column, no production readers.
	// Anything else that grows a .OrgID read against an AuthSession during
	// the sunset window is a bug: those call sites want the active-org
	// resolution (OrgIDFromContext), not the session's frozen legacy value.
	session := &models.AuthSession{
		UserID:    user.ID,
		OrgID:     user.OrgID,
		LastOrgID: lastOrgID,
		Token:     sessionToken,
		ExpiresAt: time.Now().Add(middleware.SessionTTL),
	}
	if err := db.NewAuthSessionStore(dbtx).Create(ctx, session); err != nil {
		return "", fmt.Errorf("create session: %w", err)
	}
	return sessionToken, nil
}

// writeSessionAndCSRFCookies installs the session and CSRF cookies for the
// given token. Returns false and writes an error response if the CSRF cookie
// cannot be signed; callers should stop processing in that case.
func (h *AuthHandler) writeSessionAndCSRFCookies(w http.ResponseWriter, r *http.Request, sessionToken string) bool {
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
		return false
	}
	return true
}

// respondWithSession writes the session+CSRF cookies and returns the user as
// JSON. Used by the email register/login paths that expect a JSON response.
func (h *AuthHandler) respondWithSession(w http.ResponseWriter, r *http.Request, user *models.User, sessionToken string) {
	if !h.writeSessionAndCSRFCookies(w, r, sessionToken) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": user})
}

// redirectWithSession writes the session+CSRF cookies and issues the OAuth
// post-login redirect. Honors the oauth_return_to cookie for same-origin
// relative paths only.
func (h *AuthHandler) redirectWithSession(w http.ResponseWriter, r *http.Request, sessionToken string) {
	if !h.writeSessionAndCSRFCookies(w, r, sessionToken) {
		return
	}
	redirectURL := h.cfg.FrontendURL
	if returnCookie, err := r.Cookie("oauth_return_to"); err == nil && returnCookie.Value != "" {
		http.SetCookie(w, &http.Cookie{Name: "oauth_return_to", Value: "", Path: "/", MaxAge: -1, HttpOnly: true})
		if len(returnCookie.Value) > 0 && returnCookie.Value[0] == '/' {
			redirectURL = h.cfg.FrontendURL + returnCookie.Value
		}
	}
	http.Redirect(w, r, redirectURL, http.StatusTemporaryRedirect)
}

// createSessionAndRedirect is the non-signup redirect path: the user already
// exists (login) so the session is created via the pool, not inside a tx.
// Signup flows use the Tx-variant that co-commits with user/org/membership.
func (h *AuthHandler) createSessionAndRedirect(w http.ResponseWriter, r *http.Request, user *models.User) {
	token, err := h.persistSessionTx(r.Context(), h.pool, user)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "SESSION_CREATE_FAILED", "failed to create session", err)
		return
	}
	h.redirectWithSession(w, r, token)
}

// createSessionAndRespond is the non-signup JSON path (email login): the user
// already exists so the session is created via the pool.
func (h *AuthHandler) createSessionAndRespond(w http.ResponseWriter, r *http.Request, user *models.User) {
	token, err := h.persistSessionTx(r.Context(), h.pool, user)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "SESSION_CREATE_FAILED", "failed to create session", err)
		return
	}
	h.respondWithSession(w, r, user, token)
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
	data := url.Values{
		"client_id":     {h.cfg.GitHubOAuthClientID},
		"client_secret": {h.cfg.GitHubOAuthClientSecret},
		"code":          {code},
	}

	req, err := http.NewRequest("POST", h.gitHubOAuthBase()+"/login/oauth/access_token", nil)
	if err != nil {
		return nil, err
	}
	req.URL.RawQuery = data.Encode()
	req.Header.Set("Accept", "application/json")

	resp, err := h.gitHubHTTPClient().Do(req) // #nosec G704 -- URL is GitHub OAuth endpoint or test override
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
	req, err := http.NewRequest("GET", h.gitHubAPIBase()+"/user", nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/vnd.github+json")

	resp, err := h.gitHubHTTPClient().Do(req) // #nosec G704 -- URL is GitHub API endpoint or test override
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

// gitHubEmail is one row from GET /user/emails.
type gitHubEmail struct {
	Email      string `json:"email"`
	Primary    bool   `json:"primary"`
	Verified   bool   `json:"verified"`
	Visibility string `json:"visibility"`
}

// noreplyEmailHTTPTimeout bounds the /user/emails probe so a hung GitHub
// API doesn't block the OAuth login critical path. The fallback path
// produces a usable address either way.
const noreplyEmailHTTPTimeout = 10 * time.Second

// fetchGitHubNoreplyEmail returns the address GitHub uses to attribute commits
// to the user's profile.
//
// Strategy (in order):
//  1. Probe `GET /user/emails` and look for the user's `noreply` entry —
//     this is what GitHub itself recommends for committing as the user.
//  2. Fall back to the deterministic `{user_id}+{login}@users.noreply.github.com`
//     form. This format has been the canonical scheme since August 2017 and
//     is always linkable to the user's GitHub account.
//
// Errors from /user/emails are non-fatal: the function still returns the
// computed fallback so the caller can persist it. We never return an empty
// string for a real GitHub user.
func (h *AuthHandler) fetchGitHubNoreplyEmail(ctx context.Context, accessToken string, userID int64, login string) string {
	// Pass h.httpClient (may be nil) directly rather than going through
	// gitHubHTTPClient(): when nil, the inner function applies the explicit
	// noreplyEmailHTTPTimeout. http.DefaultClient has no timeout, so going
	// through gitHubHTTPClient() would silently drop that guarantee.
	return fetchGitHubNoreplyEmailFrom(ctx, h.httpClient, h.gitHubAPIBase()+"/user/emails", accessToken, userID, login)
}

// fetchGitHubNoreplyEmailFrom is the testable seam for the noreply lookup.
// The API URL is parameterized so unit tests can point at a httptest.Server,
// and the client is injectable so tests can pass server.Client() directly
// rather than mutating http.DefaultTransport (which would leak across
// concurrent tests in the same binary). When client is nil, a dedicated
// client with noreplyEmailHTTPTimeout is used — this matches production
// behavior, where AuthHandler.httpClient is nil and the function still
// needs an explicit timeout because the OAuth login critical path can't
// rely on http.DefaultClient (which has no timeout).
func fetchGitHubNoreplyEmailFrom(ctx context.Context, client *http.Client, emailsURL, accessToken string, userID int64, login string) string {
	fallback := computeNoreplyEmail(userID, login)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, emailsURL, nil)
	if err != nil {
		return fallback
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/vnd.github+json")

	if client == nil {
		client = &http.Client{Timeout: noreplyEmailHTTPTimeout}
	}
	resp, err := client.Do(req) // #nosec G704 -- URL is GitHub API endpoint or test override
	if err != nil {
		return fallback
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// Common when the OAuth scope/App permission lacks `user:email` —
		// the fallback is correct and safe to persist.
		return fallback
	}

	var emails []gitHubEmail
	if err := json.NewDecoder(resp.Body).Decode(&emails); err != nil {
		return fallback
	}

	// Preference order:
	//  1. The canonical user-id-prefixed noreply (`{id}+{login}@…`), verified.
	//     This is the address GitHub itself recommends and the only one that
	//     stays linked to the user across login renames.
	//  2. Any verified noreply address.
	//  3. Any noreply address (last-resort: GitHub may surface unverified
	//     rows on accounts that haven't toggled the flag).
	//  4. The deterministic fallback.
	canonical := computeNoreplyEmail(userID, login)
	for _, e := range emails {
		if e.Verified && canonical != "" && strings.EqualFold(e.Email, canonical) {
			return e.Email
		}
	}
	for _, e := range emails {
		if e.Verified && strings.HasSuffix(e.Email, "@users.noreply.github.com") {
			return e.Email
		}
	}
	for _, e := range emails {
		if strings.HasSuffix(e.Email, "@users.noreply.github.com") {
			return e.Email
		}
	}
	return fallback
}

// computeNoreplyEmail returns the canonical user-id-prefixed noreply address.
// userID==0 or empty login returns "" — caller decides what to do with that
// (today: never invoked when github_id is unset).
func computeNoreplyEmail(userID int64, login string) string {
	if userID <= 0 || login == "" {
		return ""
	}
	return fmt.Sprintf("%d+%s@users.noreply.github.com", userID, login)
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
// invited OAuth user, grants them a membership, and issues their initial
// session token — all in one transaction so membership is never granted
// without a successful claim (and a successfully-claimed user is never left
// without a way to log in). The returned sessionToken should be installed
// into the response cookies.
func (h *AuthHandler) acceptInvitationAndUpsertUser(
	ctx context.Context,
	invitationID uuid.UUID,
	user *models.User,
	upsert oauthUserUpsertFunc,
) (*models.User, string, *invitationError, error) {
	if h.pool == nil {
		return nil, "", nil, fmt.Errorf("auth handler pool is not configured")
	}
	if user == nil {
		return nil, "", nil, fmt.Errorf("user is required")
	}
	if upsert == nil {
		return nil, "", nil, fmt.Errorf("oauth user upsert function is required")
	}

	tx, err := h.pool.Begin(ctx)
	if err != nil {
		return nil, "", nil, fmt.Errorf("begin invitation transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	txInvitationStore := db.NewInvitationStore(tx)
	if err := txInvitationStore.Accept(ctx, invitationID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, "", &invitationError{
				status:  http.StatusGone,
				code:    "INVITE_INVALID",
				message: "this invitation is no longer valid",
			}, nil
		}
		return nil, "", nil, fmt.Errorf("accept invitation: %w", err)
	}

	txUserStore := db.NewUserStore(tx)
	// TODO(2026-04-25): the oauth upsert still writes user.OrgID / user.Role
	// onto the legacy users row; after sunset, drop those columns and derive
	// the caller's role from the GrantAtLeast below instead.
	if err := upsert(ctx, txUserStore, user); err != nil {
		return nil, "", nil, fmt.Errorf("upsert invited oauth user: %w", err)
	}

	effectiveRole, err := db.NewOrganizationMembershipStore(tx).GrantAtLeast(ctx, user.ID, user.OrgID, user.Role)
	if err != nil {
		return nil, "", nil, fmt.Errorf("grant invited membership: %w", err)
	}
	// Sync the legacy users.role column with the effective membership role so
	// the compat-window dual-read lands on the same value the new path sees.
	user.Role = models.Role(effectiveRole)

	sessionToken, err := h.persistSessionTx(ctx, tx, user)
	if err != nil {
		return nil, "", nil, fmt.Errorf("create invitation session: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, "", nil, fmt.Errorf("commit invitation transaction: %w", err)
	}

	return user, sessionToken, nil, nil
}

// createInvitedUserWithPassword validates and claims an invitation, creates
// the user, grants a membership, and issues the initial session token — all
// in one transaction so membership is never granted without a successful
// claim and a created user is never left without a way to log in. The
// returned sessionToken should be installed into the response cookies.
func (h *AuthHandler) createInvitedUserWithPassword(ctx context.Context, token, email, name, hash string) (*models.User, string, *invitationError, error) {
	if h.pool == nil {
		return nil, "", nil, fmt.Errorf("auth handler pool is not configured")
	}
	if h.userStore == nil {
		return nil, "", nil, fmt.Errorf("user store is not configured")
	}

	tx, err := h.pool.Begin(ctx)
	if err != nil {
		return nil, "", nil, fmt.Errorf("begin invitation transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	txInvitationStore := db.NewInvitationStore(tx)
	txUserStore := db.NewUserStore(tx)

	inv, orgID, role, invErr := h.validateInvitationWithStore(ctx, txInvitationStore, token, email, "")
	if invErr != nil {
		return nil, "", invErr, nil
	}

	if err := txInvitationStore.Accept(ctx, inv.ID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, "", &invitationError{
				status:  http.StatusGone,
				code:    "INVITE_INVALID",
				message: "this invitation is no longer valid",
			}, nil
		}
		return nil, "", nil, fmt.Errorf("accept invitation: %w", err)
	}

	// TODO(2026-04-25): drop OrgID / Role from the User literal once
	// users.org_id and users.role are removed. The GrantAtLeast call below
	// is the authoritative membership write; the legacy fields are populated
	// here only so read paths that still touch users.org_id keep working
	// through the sunset window.
	user := &models.User{
		OrgID:        orgID,
		Email:        email,
		Name:         name,
		Role:         role,
		PasswordHash: &hash,
	}
	if err := txUserStore.CreateWithPassword(ctx, user); err != nil {
		return nil, "", nil, fmt.Errorf("create invited user: %w", err)
	}

	effectiveRole, err := db.NewOrganizationMembershipStore(tx).GrantAtLeast(ctx, user.ID, orgID, role)
	if err != nil {
		return nil, "", nil, fmt.Errorf("grant invited membership: %w", err)
	}
	// Freshly-created password user: the grant is the first membership, so
	// effectiveRole equals the invited role. Assignment is harmless and
	// keeps the legacy users.role column in sync with the membership row.
	user.Role = models.Role(effectiveRole)

	sessionToken, err := h.persistSessionTx(ctx, tx, user)
	if err != nil {
		return nil, "", nil, fmt.Errorf("create invitation session: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, "", nil, fmt.Errorf("commit invitation transaction: %w", err)
	}

	return user, sessionToken, nil, nil
}

// validateInvitation looks up and validates an invitation token.
// It checks that the invitation is pending, not expired, and that the
// signing-in user satisfies the invitation's acceptance method. An invitation
// can keep an email for notifications while still requiring GitHub OAuth.
func (h *AuthHandler) validateInvitation(ctx context.Context, token, email, githubLogin string) (models.Invitation, uuid.UUID, models.Role, *invitationError) {
	return h.validateInvitationWithStore(ctx, h.invitationStore, token, email, githubLogin)
}

func (h *AuthHandler) validateInvitationWithStore(ctx context.Context, invitationStore invitationLookupStore, token, email, githubLogin string) (models.Invitation, uuid.UUID, models.Role, *invitationError) {
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

	if invErr := validateInvitationForRecipient(inv, email, githubLogin); invErr != nil {
		return inv, uuid.Nil, "", invErr
	}
	return inv, inv.OrgID, inv.Role, nil
}

// validateInvitationForRecipient checks the three invariants that gate every
// invitation claim regardless of how the row was looked up: the invitation
// is still pending, has not expired, and its recipient identifier matches
// the authenticated user. Shared by the token-based claim path and the
// id-based in-app accept/decline paths so the matching rules can never
// drift between the dropdown's "is this invite mine?" filter and the
// server's "is this user allowed to accept?" check.
//
// Match rules (mirror the ListPendingForUser store query):
//   - acceptance_method=email: the user's email must match
//   - acceptance_method=github: the user's github_login must match
//   - acceptance_method=either or legacy empty: either match suffices
//   - empty user-side identifiers can never satisfy either branch
//
// All three failures return invitationError values with status codes that
// suit the token-claim path. Id-based callers may remap the mismatch code
// to 403 since the id is part of the URL (the request is naming a specific
// invitation the user has no claim to, not submitting bad input).
func validateInvitationForRecipient(inv models.Invitation, email, githubLogin string) *invitationError {
	if inv.Status != "pending" {
		return &invitationError{http.StatusGone, "INVITE_INVALID", "this invitation is no longer valid"}
	}
	if time.Now().After(inv.ExpiresAt) {
		return &invitationError{http.StatusGone, "INVITE_EXPIRED", "this invitation has expired"}
	}
	emailMatches := inv.Email != nil && email != "" && strings.EqualFold(*inv.Email, email)
	githubMatches := inv.GitHubUsername != nil && githubLogin != "" && strings.EqualFold(*inv.GitHubUsername, githubLogin)
	acceptanceMethod := inv.AcceptanceMethod
	if acceptanceMethod == "" {
		acceptanceMethod = models.InvitationAcceptanceMethodEither
	}
	if acceptanceMethod.Validate() != nil {
		return &invitationError{http.StatusGone, "INVITE_INVALID", "this invitation is no longer valid"}
	}

	matched := false
	switch acceptanceMethod {
	case models.InvitationAcceptanceMethodEmail:
		matched = emailMatches
	case models.InvitationAcceptanceMethodGitHub:
		matched = githubMatches
	case models.InvitationAcceptanceMethodEither:
		matched = emailMatches || githubMatches
	}
	if !matched {
		return &invitationError{http.StatusBadRequest, "INVITE_MISMATCH", "this account does not match the invitation"}
	}
	return nil
}

// emitAuthEvent emits an audit log entry for an authentication event. It
// works even before org context middleware runs (e.g., during registration/
// login).
//
// Attribution note: audit events are written against user.OrgID — the user's
// legacy primary org. For login events that's the best we can do since the
// active membership isn't resolved yet (the X-Active-Org-ID middleware sits
// downstream of the auth handlers). For register/invite-accept we set
// user.OrgID to the joined org *before* calling emitAuthEvent, so the audit
// row lands in the org the user just entered. Both flows produce a stable
// "this event belongs to exactly one org" invariant for downstream auditing
// without needing to look at memberships.
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
		Details: marshalAuditDetails(*zerolog.Ctx(r.Context()), map[string]any{
			"user_id": user.ID.String(),
			"email":   user.Email,
			"name":    user.Name,
			"role":    user.Role,
			"action":  string(action),
		}),
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
