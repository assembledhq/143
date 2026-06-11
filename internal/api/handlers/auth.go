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
	"github.com/assembledhq/143/internal/services/domains"
	"github.com/assembledhq/143/internal/services/email"
)

type githubOrgAutoJoinVerifier interface {
	IsActiveOrgMember(ctx context.Context, installationID int64, orgLogin, username string) (bool, error)
}

type AuthHandler struct {
	cfg                 *config.Config
	pool                db.TxStarter
	userStore           *db.UserStore
	sessionStore        *db.AuthSessionStore
	invitationStore     *db.InvitationStore
	memberships         *db.OrganizationMembershipStore
	userCredentials     *db.UserCredentialStore
	orgDomains          *db.OrganizationDomainStore
	githubInstallations *db.GitHubInstallationStore
	githubOrgVerifier   githubOrgAutoJoinVerifier
	emailVerifications  *db.EmailVerificationStore
	emailSender         email.Sender
	audit               *db.AuditEmitter
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
	// CLI login flow stores (see auth_cli.go). Nil unless wired via
	// SetCLIAuthStores; the CLI endpoints fail with a configuration error
	// and the OAuth callback skips its CLI branch in that case.
	cliAuthCodes *db.CLIAuthCodeStore
	cliTokens    *db.UserCLITokenStore
	joinTokens   *db.OrgJoinTokenStore
	orgStore     *db.OrganizationStore
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

// SetOrgDomainStore injects the verified-domain store, enabling email-domain
// auto-join during OAuth signup. When nil, signups always create a fresh org.
func (h *AuthHandler) SetOrgDomainStore(store *db.OrganizationDomainStore) {
	h.orgDomains = store
}

func (h *AuthHandler) SetGitHubOrgAutoJoinDeps(store *db.GitHubInstallationStore, verifier githubOrgAutoJoinVerifier) {
	h.githubInstallations = store
	h.githubOrgVerifier = verifier
}

// SetEmailVerificationDeps wires the token store and email sender that power
// the password-signup verification flow. sender may be nil (SMTP not
// configured): tokens are still issued and confirmable, only delivery is
// skipped, so self-hosted setups degrade to logged links instead of a
// broken endpoint.
func (h *AuthHandler) SetEmailVerificationDeps(store *db.EmailVerificationStore, sender email.Sender) {
	h.emailVerifications = store
	h.emailSender = sender
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
		ID:            user.ID,
		OrgID:         user.OrgID,
		Email:         loadedUser.Email,
		Name:          loadedUser.Name,
		Role:          user.Role,
		GitHubID:      loadedUser.GitHubID,
		GitHubLogin:   loadedUser.GitHubLogin,
		AvatarURL:     loadedUser.AvatarURL,
		GoogleID:      loadedUser.GoogleID,
		EmailVerified: loadedUser.EmailVerified,
		Settings:      loadedUser.Settings,
		CreatedAt:     loadedUser.CreatedAt,
	}})
}

// UpdateSettings applies an RFC 7386 JSON merge patch to the authenticated
// user's personal settings document: omitted fields keep their stored value,
// null clears a field, and nested objects merge per key. Callers send only
// the fields they are changing — never a full document rebuilt from a client
// cache, which would let concurrent edits from another tab clobber each other.
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

	patch, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_BODY", "invalid request body")
		return
	}
	var patchObject map[string]json.RawMessage
	if err := json.Unmarshal(patch, &patchObject); err != nil || patchObject == nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_BODY", "invalid request body")
		return
	}
	// Applying the patch to an empty document exercises the same key and
	// value validation as the real merge, so bad patches fail with a 400
	// before we open the merge transaction.
	if _, err := models.ApplyUserSettingsMergePatch(nil, patch); err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_USER_SETTINGS", err.Error())
		return
	}
	if _, err := h.userStore.MergeSettings(r.Context(), user.ID, patch); err != nil {
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

	// Password accounts have no provider attestation — send the
	// verification link so they can prove the address and unlock
	// email-domain auto-join. Best-effort; never blocks the signup.
	h.sendEmailVerificationFor(r, user)

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

	// Fetch the verified-email list once: it feeds both the
	// commit-attribution noreply derivation and the provider-verification
	// check that gates email-domain auto-join.
	ghEmails := h.fetchGitHubEmails(r.Context(), tokenResp.AccessToken)

	// Compute the GitHub-attribution noreply email up-front so every account
	// path (existing, link, signup, invite) persists it consistently.
	noreplyEmail := selectGitHubNoreplyEmail(ghEmails, ghUser.ID, ghUser.Login)

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
	// CLI login handshake + join token, set by /auth/cli/start. Both nil/""
	// for ordinary web logins, in which case every branch below behaves
	// exactly as before.
	cliIntent := readAndClearCLILoginIntent(w, r)
	pendingJoin := readAndClearPendingJoinCookie(w, r)

	existingUser, err := h.userStore.GetByGitHubID(r.Context(), ghUser.ID)
	if err == nil {
		// Known GitHub user — update and sign in.
		existingUser.Name = displayName
		existingUser.Email = h.resolveExistingGitHubEmail(r.Context(), existingUser.Email, email, ghEmails)
		existingUser.GitHubLogin = &ghUser.Login
		existingUser.AvatarURL = &ghUser.AvatarURL
		existingUser.GitHubNoreplyEmail = &noreplyEmail
		if upsertErr := h.userStore.UpsertFromGitHub(r.Context(), &existingUser); upsertErr != nil {
			writeError(w, r, http.StatusInternalServerError, "USER_UPSERT_FAILED", "failed to upsert user", upsertErr)
			return
		}
		h.markGitHubEmailVerified(r, existingUser.ID, ghEmails, existingUser.Email)
		h.claimPendingInvitationForExistingUser(r, pendingInvite, existingUser.Email, ghUser.Login, existingUser.ID)
		// An existing user carrying a join token for an org they're not in
		// gets the membership granted; already-a-member or a bad token is a
		// no-op (matches ClaimInvitation's best-effort posture).
		h.applyJoinTokenForExistingUser(r, pendingJoin, &existingUser)
		joined := h.tryGitHubOrgAutoJoinExisting(r, &existingUser)
		h.storeGitHubToken(r, &existingUser, tokenResp)
		h.emitAuthEvent(r, &existingUser, models.AuditActionAuthLogin)
		if cliIntent != nil {
			// CLI flows have no browser redirect to carry the join toast; the
			// membership grant still happened — only the UI notification is skipped.
			h.createSessionAndFinishCLILogin(w, r, &existingUser, cliIntent)
			return
		}
		h.createSessionAndRedirectWithJoin(w, r, &existingUser, joined)
		return
	}

	// Try email match for account linking.
	if emailUser, emailErr := h.userStore.GetByEmail(r.Context(), email); emailErr == nil {
		// Linking grants full access to the existing account, so it demands
		// the strongest proof: GitHub must list this exact address as
		// verified. A nil ghEmails (failed /user/emails fetch) also blocks —
		// fail closed on a one-time linking event rather than admit the
		// unverified-email account-takeover class (nOAuth, Nhost CVE).
		if !gitHubEmailVerified(ghEmails, email) {
			writeError(w, r, http.StatusForbidden, "EMAIL_NOT_VERIFIED",
				"An account with this email already exists, and GitHub does not report the address as verified. Verify it in your GitHub email settings and try again.")
			return
		}
		if linkErr := h.userStore.LinkGitHubAccount(r.Context(), emailUser.ID, emailUser.OrgID, ghUser.ID, ghUser.Login, ghUser.AvatarURL, noreplyEmail); linkErr != nil {
			writeError(w, r, http.StatusInternalServerError, "LINK_FAILED", "failed to link GitHub account", linkErr)
			return
		}
		h.markEmailVerified(r, emailUser.ID, true, email)
		h.claimPendingInvitationForExistingUser(r, pendingInvite, email, ghUser.Login, emailUser.ID)
		h.applyJoinTokenForExistingUser(r, pendingJoin, &emailUser)
		joined := h.tryGitHubOrgAutoJoinExisting(r, &emailUser)
		h.storeGitHubToken(r, &emailUser, tokenResp)
		h.emitAuthEvent(r, &emailUser, models.AuditActionAuthLogin)
		if cliIntent != nil {
			// CLI flows have no browser redirect to carry the join toast; the
			// membership grant still happened — only the UI notification is skipped.
			h.createSessionAndFinishCLILogin(w, r, &emailUser, cliIntent)
			return
		}
		h.createSessionAndRedirectWithJoin(w, r, &emailUser, joined)
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
			h.markGitHubEmailVerified(r, createdUser.ID, ghEmails, email)
			h.storeGitHubToken(r, createdUser, tokenResp)
			h.emitAuthEvent(r, createdUser, models.AuditActionAuthRegister)
			if cliIntent != nil {
				h.finishCLILoginWithSession(w, r, createdUser, sessionToken, cliIntent)
				return
			}
			h.redirectWithSession(w, r, sessionToken)
			return
		}
		// Invalid invitation (wrong email, expired, etc.) — fall through to
		// a default signup so the user isn't stranded.
	}

	// New user with a join token: JIT-provision them into the token's org.
	// Unlike the invitation flow above, an invalid/exhausted token FAILS
	// CLOSED — no user is created. A forgiving fallback would silently log
	// the CLI into a fresh single-member org, which is strictly worse than
	// the error for someone trying to join a team.
	if pendingJoin != "" {
		user := &models.User{
			Email:              email,
			Name:               displayName,
			GitHubID:           &ghUser.ID,
			GitHubLogin:        &ghUser.Login,
			GitHubNoreplyEmail: &noreplyEmail,
			AvatarURL:          &ghUser.AvatarURL,
		}
		createdUser, sessionToken, usedToken, joinErr, createErr := h.createJoinedUser(
			r.Context(),
			pendingJoin,
			user,
			func(ctx context.Context, userStore *db.UserStore, joinedUser *models.User) error {
				return userStore.UpsertFromGitHub(ctx, joinedUser)
			},
		)
		if createErr != nil {
			writeError(w, r, http.StatusInternalServerError, "USER_UPSERT_FAILED", "failed to create account", createErr)
			return
		}
		if joinErr != nil {
			h.failCLILogin(w, r, cliIntent, joinErr.code, joinErr.message)
			return
		}
		h.emitJoinTokenUsed(r, createdUser.ID, usedToken, string(createdUser.Role))
		h.markGitHubEmailVerified(r, createdUser.ID, ghEmails, email)
		h.storeGitHubToken(r, createdUser, tokenResp)
		h.emitAuthEvent(r, createdUser, models.AuditActionAuthRegister)
		if cliIntent != nil {
			h.finishCLILoginWithSession(w, r, createdUser, sessionToken, cliIntent)
			return
		}
		h.redirectWithSession(w, r, sessionToken)
		return
	}

	user := &models.User{
		Email:              email,
		Name:               displayName,
		GitHubID:           &ghUser.ID,
		GitHubLogin:        &ghUser.Login,
		GitHubNoreplyEmail: &noreplyEmail,
		AvatarURL:          &ghUser.AvatarURL,
	}
	upsertGitHub := func(ctx context.Context, us *db.UserStore, u *models.User) error {
		return us.UpsertFromGitHub(ctx, u)
	}

	if sessionToken, joined := h.tryGitHubOrgAutoJoinSignup(r, user, upsertGitHub); joined != nil {
		h.storeGitHubToken(r, user, tokenResp)
		h.emitAuthEvent(r, user, models.AuditActionAuthRegister)
		if cliIntent != nil {
			h.finishCLILoginWithSession(w, r, user, sessionToken, cliIntent)
			return
		}
		h.redirectWithSessionAndJoin(w, r, sessionToken, joined)
		return
	}

	// Domain capture: a GitHub-verified email on a domain some org has
	// DNS-verified with auto-join enabled lands the user directly in that
	// org instead of a lonely fresh one. The capture email may differ from
	// the profile email: engineers commonly keep their profile email
	// private (so `email` is the noreply fallback) or personal, while the
	// verified work address sits in /user/emails — selectGitHubAutoJoinEmail
	// finds it and it becomes the account's identity.
	if joinEmail, ok := h.selectGitHubAutoJoinEmail(r.Context(), email, ghEmails); ok {
		user.Email = joinEmail
		if sessionToken, joined := h.tryDomainAutoJoin(r, user, true, upsertGitHub); joined {
			h.storeGitHubToken(r, user, tokenResp)
			h.emitAuthEvent(r, user, models.AuditActionAuthRegister)
			// CLI-initiated logins must finish the loopback handshake, not
			// redirect the browser to the web app — a teammate at a
			// captured-domain org running `143-tools login` as their very
			// first sign-in lands on this path.
			if cliIntent != nil {
				h.finishCLILoginWithSession(w, r, user, sessionToken, cliIntent)
				return
			}
			h.redirectWithSessionAndJoin(w, r, sessionToken, &autoJoinProvenance{OrgID: user.OrgID, Via: "domain"})
			return
		}
		// Capture failed mid-flight (e.g. domain toggled off in the race
		// window) — restore the default identity for the fresh-org path.
		user.Email = email
	}

	sessionToken, err := h.createSignupOrg(r.Context(), ghUser.Login+"'s Org", user, upsertGitHub)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "USER_CREATE_FAILED", "failed to create account", err)
		return
	}
	h.markGitHubEmailVerified(r, user.ID, ghEmails, email)

	h.storeGitHubToken(r, user, tokenResp)
	h.emitAuthEvent(r, user, models.AuditActionAuthRegister)
	if cliIntent != nil {
		h.finishCLILoginWithSession(w, r, user, sessionToken, cliIntent)
		return
	}
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
		h.markEmailVerified(r, existingUser.ID, gUser.EmailVerified, gUser.Email)
		h.claimPendingInvitationForExistingUser(r, pendingInvite, gUser.Email, "", existingUser.ID)
		h.emitAuthEvent(r, &existingUser, models.AuditActionAuthLogin)
		h.createSessionAndRedirect(w, r, &existingUser)
		return
	}

	if emailUser, emailErr := h.userStore.GetByEmail(r.Context(), gUser.Email); emailErr == nil {
		// Same linking gate as the GitHub flow: full account access
		// requires the provider to attest the address.
		if !gUser.EmailVerified {
			writeError(w, r, http.StatusForbidden, "EMAIL_NOT_VERIFIED",
				"An account with this email already exists, and Google does not report the address as verified.")
			return
		}
		if linkErr := h.userStore.LinkGoogleAccount(r.Context(), emailUser.ID, emailUser.OrgID, gUser.Sub, gUser.Picture); linkErr != nil {
			writeError(w, r, http.StatusInternalServerError, "LINK_FAILED", "failed to link Google account", linkErr)
			return
		}
		h.markEmailVerified(r, emailUser.ID, true, gUser.Email)
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
			h.markEmailVerified(r, createdUser.ID, gUser.EmailVerified, gUser.Email)
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
	upsertGoogle := func(ctx context.Context, us *db.UserStore, u *models.User) error {
		return us.UpsertFromGoogle(ctx, u)
	}

	// Domain capture: a Google-verified email on a domain some org has
	// DNS-verified with auto-join enabled lands the user directly in that
	// org instead of a lonely fresh one.
	if sessionToken, ok := h.tryDomainAutoJoin(r, user, gUser.EmailVerified, upsertGoogle); ok {
		h.emitAuthEvent(r, user, models.AuditActionAuthRegister)
		h.redirectWithSession(w, r, sessionToken)
		return
	}

	sessionToken, err := h.createSignupOrg(r.Context(), name+"'s Org", user, upsertGoogle)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "USER_CREATE_FAILED", "failed to create account", err)
		return
	}
	h.markEmailVerified(r, user.ID, gUser.EmailVerified, gUser.Email)

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

	// Possessing the emailed token proves receipt at the invited address
	// (tokens only leave the server inside the invitation email). When that
	// address is the account's own email, record the proof — it unlocks
	// email-domain features for password accounts without a separate
	// verification round-trip. The in-app accept-by-ID path deliberately
	// does NOT do this: clicking a button proves nothing about the inbox.
	if inv != nil && inv.Email != nil && strings.EqualFold(*inv.Email, user.Email) {
		h.markEmailVerified(r, user.ID, true, user.Email)
	}

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

// markEmailVerified best-effort synchronizes the stored email-verification
// watermark with the provider's attestation — stamping on attested=true and
// clearing on attested=false (the provider email was just upserted as the
// user's current address, so an unattested address must drop any stamp the
// previous address earned). Failures only degrade domain-based join
// discovery, never the login itself.
func (h *AuthHandler) markEmailVerified(r *http.Request, userID uuid.UUID, attested bool, email string) {
	if h.userStore == nil {
		return
	}
	if err := h.userStore.SetEmailVerification(r.Context(), userID, email, attested); err != nil {
		zerolog.Ctx(r.Context()).Warn().Err(err).Str("user_id", userID.String()).Msg("failed to record email verification")
	}
}

// resolveExistingGitHubEmail decides which email an existing GitHub-linked
// account keeps on login. The default is today's refresh behavior: adopt
// the incoming profile email. The exception is identity stickiness for
// captured work addresses — when the stored email is still GitHub-verified
// AND its domain is company-verified, it IS the account's canonical
// identity (invitations, the team directory, and every domain feature key
// on it), and the profile email — typically the noreply fallback or a
// personal address — must not overwrite it. Without this, an account
// created via alternate-email domain capture silently reverts to its
// noreply address on the second login.
//
// The protection drops the moment GitHub stops attesting the stored
// address (e.g. it was removed after leaving the company) or the domain's
// verification is deleted; a nil emails list (failed fetch) also falls
// back to the refresh path rather than trusting stale state.
func (h *AuthHandler) resolveExistingGitHubEmail(ctx context.Context, stored, incoming string, emails []gitHubEmail) string {
	if stored == "" || strings.EqualFold(stored, incoming) {
		return incoming
	}
	if !gitHubEmailVerified(emails, stored) {
		return incoming
	}
	if h.orgDomains == nil {
		return incoming
	}
	storedDomain := domains.EmailDomain(stored)
	if storedDomain == "" || domains.IsPublicEmailDomain(storedDomain) {
		return incoming
	}
	exists, err := h.orgDomains.VerifiedDomainExists(ctx, storedDomain)
	if err != nil {
		zerolog.Ctx(ctx).Warn().Err(err).Msg("verified-domain lookup failed during email resolution; refreshing from profile")
		return incoming
	}
	if exists {
		return stored
	}
	return incoming
}

// markGitHubEmailVerified is markEmailVerified with GitHub's tri-state
// folded in: a nil emails slice means the /user/emails fetch failed — no
// information — so the stored watermark is left untouched rather than
// cleared. Only an affirmative listing (verified or not) updates it.
func (h *AuthHandler) markGitHubEmailVerified(r *http.Request, userID uuid.UUID, emails []gitHubEmail, email string) {
	if emails == nil {
		return
	}
	h.markEmailVerified(r, userID, gitHubEmailVerified(emails, email), email)
}

// selectGitHubAutoJoinEmail picks the email address to attempt domain
// capture with for a brand-new GitHub signup. Preference order:
//
//  1. The profile email, when GitHub attests it and its domain is captured.
//  2. Any other GitHub-verified address (primary first) whose domain is
//     captured — this is the common "profile email private or personal,
//     work email verified on the account" case.
//
// An alternate address is only chosen when no existing account owns it —
// silently merging into another account's identity would be a takeover
// shape, so that case falls back to the classic signup instead.
// Returns ("", false) when no capture-eligible address exists.
func (h *AuthHandler) selectGitHubAutoJoinEmail(ctx context.Context, profileEmail string, emails []gitHubEmail) (string, bool) {
	if h.orgDomains == nil {
		return "", false
	}

	captured := func(addr string) bool {
		d := domains.EmailDomain(addr)
		if d == "" || domains.IsPublicEmailDomain(d) {
			return false
		}
		if _, err := h.orgDomains.FindAutoJoinOrgByDomain(ctx, d); err != nil {
			if !errors.Is(err, pgx.ErrNoRows) {
				zerolog.Ctx(ctx).Warn().Err(err).Msg("auto-join domain lookup failed during email selection")
			}
			return false
		}
		return true
	}

	if gitHubEmailVerified(emails, profileEmail) && captured(profileEmail) {
		return profileEmail, true
	}

	// Primary first, then the rest, verified non-noreply only.
	ordered := make([]gitHubEmail, 0, len(emails))
	for _, e := range emails {
		if e.Primary {
			ordered = append(ordered, e)
		}
	}
	for _, e := range emails {
		if !e.Primary {
			ordered = append(ordered, e)
		}
	}
	for _, e := range ordered {
		if !e.Verified || strings.EqualFold(e.Email, profileEmail) ||
			strings.HasSuffix(strings.ToLower(e.Email), "@users.noreply.github.com") {
			continue
		}
		if !captured(e.Email) {
			continue
		}
		if _, err := h.userStore.GetByEmail(ctx, e.Email); err == nil {
			// Address already owned by an account — don't merge identities.
			continue
		}
		return e.Email, true
	}
	return "", false
}

// tryDomainAutoJoin attempts the domain-capture path for a brand-new OAuth
// user: when the provider attests the email and its domain is verified by
// an org with auto-join enabled, the user is created directly as a member
// of that org. Returns (sessionToken, true) on success; ("", false) means
// "no auto-join applies" and the caller proceeds with the fresh-org signup.
// Internal errors also return false (logged) — a broken auto-join must
// degrade to the classic signup rather than strand the user at login.
func (h *AuthHandler) tryDomainAutoJoin(r *http.Request, user *models.User, emailAttested bool, createUser signupUserCreateFunc) (string, bool) {
	if h.orgDomains == nil || !emailAttested {
		return "", false
	}
	emailDomain := domains.EmailDomain(user.Email)
	if emailDomain == "" || domains.IsPublicEmailDomain(emailDomain) {
		return "", false
	}

	target, err := h.orgDomains.FindAutoJoinOrgByDomain(r.Context(), emailDomain)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			zerolog.Ctx(r.Context()).Warn().Err(err).Msg("auto-join domain lookup failed; falling back to fresh-org signup")
		}
		return "", false
	}

	// Legacy users.org_id / users.role columns — same sunset note as
	// createSignupOrg; the membership grant inside createAutoJoinUser is
	// the authoritative write.
	user.OrgID = target.OrgID
	user.Role = models.RoleMember
	sessionToken, err := h.createAutoJoinUser(r.Context(), user, createUser)
	if err != nil {
		zerolog.Ctx(r.Context()).Warn().Err(err).Str("org_id", target.OrgID.String()).
			Msg("domain auto-join failed; falling back to fresh-org signup")
		user.OrgID = uuid.Nil
		user.Role = ""
		return "", false
	}

	h.emitAutoJoinEvent(r, user, target)
	return sessionToken, true
}

// emitAutoJoinEvent records the org-side audit trail for a domain-capture
// join, so admins can answer "who entered via the verified domain and when".
// The caller still emits the standard auth.register event for the user side.
func (h *AuthHandler) emitAutoJoinEvent(r *http.Request, user *models.User, target models.JoinableOrganization) {
	if h.audit == nil {
		return
	}
	userIDStr := user.ID.String()
	params := db.UserActionParams{
		OrgID:        target.OrgID,
		UserID:       user.ID,
		Action:       models.AuditActionTeamMemberAutoJoined,
		ResourceType: models.AuditResourceTeamMember,
		ResourceID:   &userIDStr,
		Details: marshalAuditDetails(*zerolog.Ctx(r.Context()), map[string]any{
			"email":  user.Email,
			"domain": target.Domain,
			"role":   string(user.Role),
			"source": "domain",
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

type autoJoinProvenance struct {
	OrgID          uuid.UUID
	OrgName        string
	Via            string
	GitHubOrgLogin string
}

func (h *AuthHandler) tryGitHubOrgAutoJoinSignup(r *http.Request, user *models.User, createUser signupUserCreateFunc) (string, *autoJoinProvenance) {
	if h.githubInstallations == nil || h.githubOrgVerifier == nil || user == nil || user.GitHubID == nil || user.GitHubLogin == nil {
		return "", nil
	}
	candidates, err := h.githubInstallations.FindAutoJoinCandidatesByGitHubUserID(r.Context(), *user.GitHubID)
	if err != nil {
		zerolog.Ctx(r.Context()).Warn().Err(err).Msg("github org auto-join lookup failed; falling back")
		return "", nil
	}
	confirmed := h.confirmGitHubOrgAutoJoinCandidates(r, *user.GitHubLogin, candidates)
	if len(confirmed) == 0 {
		return "", nil
	}

	sessionToken, err := h.createGitHubOrgAutoJoinUser(r.Context(), user, createUser, confirmed)
	if err != nil {
		zerolog.Ctx(r.Context()).Warn().Err(err).Msg("github org auto-join signup failed; falling back")
		user.OrgID = uuid.Nil
		user.Role = ""
		return "", nil
	}
	for _, target := range confirmed {
		h.emitGitHubOrgAutoJoinEvent(r, user, target)
	}
	first := confirmed[0]
	return sessionToken, &autoJoinProvenance{
		OrgID:          first.OrgID,
		OrgName:        first.OrgName,
		Via:            "github_org",
		GitHubOrgLogin: first.AccountLogin,
	}
}

func (h *AuthHandler) tryGitHubOrgAutoJoinExisting(r *http.Request, user *models.User) *autoJoinProvenance {
	if h.githubInstallations == nil || h.githubOrgVerifier == nil || h.memberships == nil || h.userStore == nil || user == nil || user.GitHubID == nil || user.GitHubLogin == nil {
		return nil
	}
	candidates, err := h.githubInstallations.FindAutoJoinCandidatesByGitHubUserID(r.Context(), *user.GitHubID)
	if err != nil {
		zerolog.Ctx(r.Context()).Warn().Err(err).Msg("github org auto-join lookup failed during login")
		return nil
	}
	confirmed := h.confirmGitHubOrgAutoJoinCandidates(r, *user.GitHubLogin, candidates)
	if len(confirmed) == 0 {
		return nil
	}
	var firstJoined *models.GitHubOrgAutoJoinCandidate
	for i := range confirmed {
		target := confirmed[i]
		if _, err := h.memberships.Get(r.Context(), user.ID, target.OrgID); err == nil {
			continue // already a member — nothing to grant
		} else if !errors.Is(err, pgx.ErrNoRows) {
			zerolog.Ctx(r.Context()).Warn().Err(err).Str("org_id", target.OrgID.String()).Msg("github org auto-join membership check failed; attempting grant anyway")
		}
		effectiveRole, err := h.memberships.GrantAtLeast(r.Context(), user.ID, target.OrgID, models.RoleMember)
		if err != nil {
			zerolog.Ctx(r.Context()).Warn().Err(err).Str("org_id", target.OrgID.String()).Msg("github org auto-join grant failed during login")
			continue
		}
		user.Role = models.Role(effectiveRole)
		if firstJoined == nil {
			firstJoined = &target
		}
		h.emitGitHubOrgAutoJoinEvent(r, user, target)
	}
	if firstJoined == nil {
		return nil
	}
	if err := h.userStore.UpdateLastOrgID(r.Context(), user.ID, &firstJoined.OrgID); err != nil {
		zerolog.Ctx(r.Context()).Warn().Err(err).Str("org_id", firstJoined.OrgID.String()).Msg("failed to pin github auto-joined org")
	}
	user.OrgID = firstJoined.OrgID
	return &autoJoinProvenance{
		OrgID:          firstJoined.OrgID,
		OrgName:        firstJoined.OrgName,
		Via:            "github_org",
		GitHubOrgLogin: firstJoined.AccountLogin,
	}
}

func (h *AuthHandler) confirmGitHubOrgAutoJoinCandidates(r *http.Request, githubLogin string, candidates []models.GitHubOrgAutoJoinCandidate) []models.GitHubOrgAutoJoinCandidate {
	confirmed := make([]models.GitHubOrgAutoJoinCandidate, 0, len(candidates))
	for _, target := range candidates {
		ok, err := h.githubOrgVerifier.IsActiveOrgMember(r.Context(), target.InstallationID, target.AccountLogin, githubLogin)
		if err != nil {
			zerolog.Ctx(r.Context()).Warn().Err(err).Int64("installation_id", target.InstallationID).Msg("github org membership confirmation failed")
			continue
		}
		if ok {
			confirmed = append(confirmed, target)
		}
	}
	return confirmed
}

func (h *AuthHandler) createGitHubOrgAutoJoinUser(ctx context.Context, user *models.User, createUser signupUserCreateFunc, targets []models.GitHubOrgAutoJoinCandidate) (string, error) {
	if h.pool == nil {
		return "", fmt.Errorf("auth handler pool is not configured")
	}
	if len(targets) == 0 {
		return "", fmt.Errorf("github org auto-join target is required")
	}
	tx, err := h.pool.Begin(ctx)
	if err != nil {
		return "", fmt.Errorf("begin github org auto-join signup transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	first := targets[0]
	user.OrgID = first.OrgID
	user.Role = models.RoleMember
	txUserStore := db.NewUserStore(tx)
	if err := createUser(ctx, txUserStore, user); err != nil {
		return "", fmt.Errorf("create github org auto-join user: %w", err)
	}
	txMemberships := db.NewOrganizationMembershipStore(tx)
	for _, target := range targets {
		effectiveRole, err := txMemberships.GrantAtLeast(ctx, user.ID, target.OrgID, models.RoleMember)
		if err != nil {
			return "", fmt.Errorf("grant github org auto-join membership: %w", err)
		}
		if target.OrgID == first.OrgID {
			user.Role = models.Role(effectiveRole)
		}
	}
	if err := txUserStore.UpdateLastOrgID(ctx, user.ID, &first.OrgID); err != nil {
		return "", fmt.Errorf("set github org auto-join last org: %w", err)
	}
	sessionToken, err := h.persistSessionTx(ctx, tx, user)
	if err != nil {
		return "", fmt.Errorf("create github org auto-join session: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return "", fmt.Errorf("commit github org auto-join signup transaction: %w", err)
	}
	return sessionToken, nil
}

func (h *AuthHandler) emitGitHubOrgAutoJoinEvent(r *http.Request, user *models.User, target models.GitHubOrgAutoJoinCandidate) {
	if h.audit == nil {
		return
	}
	userIDStr := user.ID.String()
	params := db.UserActionParams{
		OrgID:        target.OrgID,
		UserID:       user.ID,
		Action:       models.AuditActionTeamMemberAutoJoined,
		ResourceType: models.AuditResourceTeamMember,
		ResourceID:   &userIDStr,
		Details: marshalAuditDetails(*zerolog.Ctx(r.Context()), map[string]any{
			"email":            user.Email,
			"role":             string(models.RoleMember),
			"source":           "github_org",
			"github_org_login": target.AccountLogin,
			"installation_id":  target.InstallationID,
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
	h.redirectWithSessionAndJoin(w, r, sessionToken, nil)
}

func (h *AuthHandler) redirectWithSessionAndJoin(w http.ResponseWriter, r *http.Request, sessionToken string, joined *autoJoinProvenance) {
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
	if joined != nil && joined.OrgID != uuid.Nil && joined.Via != "" {
		parsed, err := url.Parse(redirectURL)
		if err == nil {
			q := parsed.Query()
			q.Set("joined_org", joined.OrgID.String())
			q.Set("joined_via", joined.Via)
			if joined.OrgName != "" {
				q.Set("joined_org_name", joined.OrgName)
			}
			if joined.GitHubOrgLogin != "" {
				q.Set("github_org", joined.GitHubOrgLogin)
			}
			parsed.RawQuery = q.Encode()
			redirectURL = parsed.String()
		}
	}
	http.Redirect(w, r, redirectURL, http.StatusTemporaryRedirect)
}

// createSessionAndRedirect is the non-signup redirect path: the user already
// exists (login) so the session is created via the pool, not inside a tx.
// Signup flows use the Tx-variant that co-commits with user/org/membership.
func (h *AuthHandler) createSessionAndRedirect(w http.ResponseWriter, r *http.Request, user *models.User) {
	h.createSessionAndRedirectWithJoin(w, r, user, nil)
}

func (h *AuthHandler) createSessionAndRedirectWithJoin(w http.ResponseWriter, r *http.Request, user *models.User, joined *autoJoinProvenance) {
	token, err := h.persistSessionTx(r.Context(), h.pool, user)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "SESSION_CREATE_FAILED", "failed to create session", err)
		return
	}
	h.redirectWithSessionAndJoin(w, r, token, joined)
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

// fetchGitHubEmails retrieves the /user/emails list (nil on any failure).
// The single fetch feeds both the commit-attribution noreply selection
// (selectGitHubNoreplyEmail) and the provider-verification check
// (gitHubEmailVerified).
//
// Pass h.httpClient (may be nil) directly rather than going through
// gitHubHTTPClient(): when nil, the inner function applies the explicit
// noreplyEmailHTTPTimeout. http.DefaultClient has no timeout, so going
// through gitHubHTTPClient() would silently drop that guarantee.
func (h *AuthHandler) fetchGitHubEmails(ctx context.Context, accessToken string) []gitHubEmail {
	return fetchGitHubEmailsFrom(ctx, h.httpClient, h.gitHubAPIBase()+"/user/emails", accessToken)
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
	emails := fetchGitHubEmailsFrom(ctx, client, emailsURL, accessToken)
	return selectGitHubNoreplyEmail(emails, userID, login)
}

// fetchGitHubEmailsFrom retrieves the user's email list from GET
// /user/emails. Errors are non-fatal and return nil — callers treat a
// missing list as "no information" (fallback noreply address, email not
// provider-verified).
func fetchGitHubEmailsFrom(ctx context.Context, client *http.Client, emailsURL, accessToken string) []gitHubEmail {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, emailsURL, nil)
	if err != nil {
		return nil
	}
	req.Header.Set("Authorization", "Bearer "+accessToken)
	req.Header.Set("Accept", "application/vnd.github+json")

	if client == nil {
		client = &http.Client{Timeout: noreplyEmailHTTPTimeout}
	}
	resp, err := client.Do(req) // #nosec G704 -- URL is GitHub API endpoint or test override
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// Common when the OAuth scope/App permission lacks `user:email`.
		return nil
	}

	var emails []gitHubEmail
	if err := json.NewDecoder(resp.Body).Decode(&emails); err != nil {
		return nil
	}
	return emails
}

// gitHubEmailVerified reports whether GitHub attests the given address as
// verified for this account. The /user/emails list is authoritative: an
// address absent from it (including the public-profile email of someone
// else's account — impossible — or an empty fetch) is not verified.
func gitHubEmailVerified(emails []gitHubEmail, email string) bool {
	for _, e := range emails {
		if e.Verified && strings.EqualFold(e.Email, email) {
			return true
		}
	}
	return false
}

// selectGitHubNoreplyEmail picks the commit-attribution noreply address from
// the email list, falling back to the deterministic computed form.
func selectGitHubNoreplyEmail(emails []gitHubEmail, userID int64, login string) string {
	fallback := computeNoreplyEmail(userID, login)
	if len(emails) == 0 {
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
	// EmailVerified is Google's attestation that the account owns this
	// address. Practically always true for Google accounts, but the spec
	// allows false (e.g. some federated workspace setups) — and auto-join
	// must never fire on an unattested address.
	EmailVerified bool `json:"email_verified"`
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

	// Claiming an emailed invitation token IS proof of address ownership:
	// the token only ever leaves the server inside the email sent to this
	// address (InvitationResponse omits it), and validateInvitationWithStore
	// just matched the registering email against the invite. Stamp it so
	// the user immediately qualifies for email-domain features.
	if inv.Email != nil && strings.EqualFold(*inv.Email, email) {
		if verr := txUserStore.SetEmailVerification(ctx, user.ID, email, true); verr != nil {
			return nil, "", nil, fmt.Errorf("mark invited email verified: %w", verr)
		}
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
