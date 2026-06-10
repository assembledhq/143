package handlers

import (
	"context"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog"

	"github.com/assembledhq/143/internal/api/middleware"
	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
)

// CLI login handshake cookies, set by CLIStart and consumed by the GitHub
// OAuth callback. Same short-lived HttpOnly pattern as pending_invitation.
const (
	cliPortCookie      = "cli_port"
	cliChallengeCookie = "cli_challenge"
	cliDeviceCookie    = "cli_device"
	pendingJoinCookie  = "pending_join"

	cliCookieMaxAge = 600 // seconds; one OAuth round-trip with margin
)

// cliChallengePattern: the challenge is SHA-256(verifier) hex from the CLI.
var cliChallengePattern = regexp.MustCompile(`^[0-9a-fA-F]{64}$`)

// maxCLIDeviceNameLength caps the hostname the CLI sends at login.
const maxCLIDeviceNameLength = 64

// SetCLIAuthStores wires the stores backing the CLI login flow. The org
// store is used to name the org in the exchange response ("Logged in as
// @octocat (Acme Org)"); the join-token store powers JIT provisioning in
// the OAuth callback.
func (h *AuthHandler) SetCLIAuthStores(
	authCodes *db.CLIAuthCodeStore,
	cliTokens *db.UserCLITokenStore,
	joinTokens *db.OrgJoinTokenStore,
	orgs *db.OrganizationStore,
) {
	h.cliAuthCodes = authCodes
	h.cliTokens = cliTokens
	h.joinTokens = joinTokens
	h.orgStore = orgs
}

// CLIStart begins the browser-based CLI login. It validates the loopback
// port + PKCE-style challenge from the CLI, stashes them (plus the optional
// join token and device name) in short-lived cookies, and redirects into
// the existing GitHub OAuth Login handler. The browser carries the cookies
// through GitHub and back to the callback, which closes the loop against
// the CLI's loopback listener.
func (h *AuthHandler) CLIStart(w http.ResponseWriter, r *http.Request) {
	if h.cfg.DemoMode || h.cfg.GitHubOAuthClientID == "" {
		writeError(w, r, http.StatusConflict, "GITHUB_OAUTH_DISABLED",
			"CLI login requires GitHub OAuth, which is not enabled on this server")
		return
	}

	port, err := strconv.Atoi(r.URL.Query().Get("port"))
	if err != nil || port < 1024 || port > 65535 {
		writeError(w, r, http.StatusBadRequest, "INVALID_CLI_PORT", "port must be an integer in [1024, 65535]")
		return
	}

	challenge := strings.ToLower(strings.TrimSpace(r.URL.Query().Get("challenge")))
	if !cliChallengePattern.MatchString(challenge) {
		writeError(w, r, http.StatusBadRequest, "INVALID_CLI_CHALLENGE", "challenge must be 64 hex characters (SHA-256 of the verifier)")
		return
	}

	device := sanitizeCLIDeviceName(r.URL.Query().Get("device"))

	join := strings.TrimSpace(r.URL.Query().Get("join"))
	if join != "" && !joinTokenPathPattern.MatchString(join) {
		writeError(w, r, http.StatusBadRequest, "INVALID_JOIN_TOKEN", "join token is malformed")
		return
	}

	setCLICookie(w, cliPortCookie, strconv.Itoa(port))
	setCLICookie(w, cliChallengeCookie, challenge)
	if device != "" {
		setCLICookie(w, cliDeviceCookie, device)
	}
	if join != "" {
		setCLICookie(w, pendingJoinCookie, join)
	}

	h.Login(w, r)
}

// cliLoginIntent is the CLI handshake state recovered from the cookies set
// by CLIStart, present on the OAuth callback only for CLI-initiated logins.
type cliLoginIntent struct {
	port      int
	challenge string
	device    string
}

// loopbackURL builds the redirect target on the CLI's loopback listener.
// Hardcodes 127.0.0.1 — never "localhost", which can resolve unexpectedly.
func (i *cliLoginIntent) loopbackURL(query string) string {
	return fmt.Sprintf("http://127.0.0.1:%d/callback?%s", i.port, query)
}

// readAndClearCLILoginIntent pops the CLI handshake cookies. Clearing is
// unconditional so a stale aborted flow can't leak into the next web login.
// Returns nil when this is not a CLI-initiated login or the cookie values
// fail re-validation (cookies are client-controlled — never trust them more
// than the query params they came from).
func readAndClearCLILoginIntent(w http.ResponseWriter, r *http.Request) *cliLoginIntent {
	portRaw := popCookie(w, r, cliPortCookie)
	challenge := popCookie(w, r, cliChallengeCookie)
	device := sanitizeCLIDeviceName(popCookie(w, r, cliDeviceCookie))
	if portRaw == "" || challenge == "" {
		return nil
	}
	port, err := strconv.Atoi(portRaw)
	if err != nil || port < 1024 || port > 65535 || !cliChallengePattern.MatchString(challenge) {
		return nil
	}
	return &cliLoginIntent{port: port, challenge: strings.ToLower(challenge), device: device}
}

// readAndClearPendingJoinCookie pops the pending_join cookie, re-applying
// the syntactic gate (cookies are client-controlled).
func readAndClearPendingJoinCookie(w http.ResponseWriter, r *http.Request) string {
	join := popCookie(w, r, pendingJoinCookie)
	if join == "" || !joinTokenPathPattern.MatchString(join) {
		return ""
	}
	return join
}

// createSessionAndFinishCLILogin is the CLI-branch analogue of
// createSessionAndRedirect: the user already exists, so the session is
// created via the pool, then the loopback handoff completes the CLI login.
func (h *AuthHandler) createSessionAndFinishCLILogin(w http.ResponseWriter, r *http.Request, user *models.User, intent *cliLoginIntent) {
	sessionToken, err := h.persistSessionTx(r.Context(), h.pool, user)
	if err != nil {
		h.renderCLIErrorPage(w, r, "failed to create session — return to your terminal and retry `143-tools login`", err)
		return
	}
	h.finishCLILoginWithSession(w, r, user, sessionToken, intent)
}

// finishCLILoginWithSession installs the normal web session + CSRF cookies
// (the user just completed a full OAuth login — leaving them signed into
// the web app too is free and expected), mints the one-time code, and
// redirects the browser to the CLI's loopback listener. The browser never
// sees the real token — only this short-lived code, which the CLI exchanges
// together with its verifier via a direct POST.
func (h *AuthHandler) finishCLILoginWithSession(w http.ResponseWriter, r *http.Request, user *models.User, sessionToken string, intent *cliLoginIntent) {
	if !h.writeSessionAndCSRFCookies(w, r, sessionToken) {
		return
	}
	if h.cliAuthCodes == nil {
		h.renderCLIErrorPage(w, r, "CLI login is not configured on this server", errors.New("cli auth code store not wired"))
		return
	}

	code, err := db.GenerateCLIAuthCode()
	if err != nil {
		h.renderCLIErrorPage(w, r, "failed to mint login code — return to your terminal and retry `143-tools login`", err)
		return
	}

	authCode := &models.CLIAuthCode{
		CodeHash:   db.HashCLIAuthCode(code),
		Challenge:  intent.challenge,
		UserID:     user.ID,
		OrgID:      h.resolveCLILoginOrg(r.Context(), user.ID),
		DeviceName: intent.device,
		ExpiresAt:  time.Now().Add(db.CLIAuthCodeTTL),
	}
	if err := h.cliAuthCodes.Create(r.Context(), authCode); err != nil {
		h.renderCLIErrorPage(w, r, "failed to persist login code — return to your terminal and retry `143-tools login`", err)
		return
	}

	http.Redirect(w, r, intent.loopbackURL("code="+code), http.StatusTemporaryRedirect)
}

// resolveCLILoginOrg picks the org recorded on the auth-code row: the
// user's persisted last org, falling back to their oldest membership —
// the same chain sessions use. Nil for zero-membership users, who can
// still complete login.
func (h *AuthHandler) resolveCLILoginOrg(ctx context.Context, userID uuid.UUID) *uuid.UUID {
	if h.userStore != nil {
		if lastOrgID, err := h.userStore.GetLastOrgID(ctx, userID); err == nil && lastOrgID != nil {
			if h.memberships != nil {
				if _, err := h.memberships.Get(ctx, userID, *lastOrgID); err == nil {
					return lastOrgID
				}
			}
		}
	}
	if h.memberships != nil {
		if m, err := h.memberships.OldestForUser(ctx, userID); err == nil {
			orgID := m.OrgID
			return &orgID
		}
	}
	return nil
}

// failCLILogin reports a terminal CLI-login failure. With a live loopback
// intent the browser is redirected to the CLI (which prints the message and
// exits non-zero, and renders its own "return to terminal" page); without
// one, a minimal server-rendered page is the fallback.
func (h *AuthHandler) failCLILogin(w http.ResponseWriter, r *http.Request, intent *cliLoginIntent, code, message string) {
	if intent != nil {
		q := url.Values{"error": {code}, "message": {message}}
		http.Redirect(w, r, intent.loopbackURL(q.Encode()), http.StatusTemporaryRedirect)
		return
	}
	renderCLIMessagePage(w, http.StatusForbidden, message)
}

// renderCLIErrorPage logs the underlying failure and shows the browser a
// terminal-shaped error page. Used when the loopback handoff itself cannot
// proceed (we may not have a usable port to redirect to).
func (h *AuthHandler) renderCLIErrorPage(w http.ResponseWriter, r *http.Request, message string, err error) {
	zerolog.Ctx(r.Context()).Error().Err(err).Msg("cli login failed")
	renderCLIMessagePage(w, http.StatusInternalServerError, message)
}

func renderCLIMessagePage(w http.ResponseWriter, status int, message string) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.WriteHeader(status)
	// Template-free: message is composed of fixed server-side strings only.
	fmt.Fprintf(w, `<!doctype html><meta charset="utf-8"><title>143 CLI login</title>
<body style="font-family: system-ui, sans-serif; max-width: 40rem; margin: 4rem auto;">
<h1 style="font-size:1.2rem">143 CLI login</h1><p>%s</p></body>`, message)
}

// CLIExchange swaps a one-time code + verifier for a personal CLI token.
// Registered OUTSIDE the CSRF-wrapped auth group: the CLI has no CSRF
// cookie/header, and the one-time code + verifier binding is a strictly
// stronger anti-forgery guarantee than the double-submit cookie.
func (h *AuthHandler) CLIExchange(w http.ResponseWriter, r *http.Request) {
	if h.cliAuthCodes == nil || h.cliTokens == nil {
		writeError(w, r, http.StatusInternalServerError, "CLI_AUTH_NOT_CONFIGURED", "CLI login is not configured on this server")
		return
	}

	var body struct {
		Code     string `json:"code"`
		Verifier string `json:"verifier"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.Code == "" || body.Verifier == "" {
		writeError(w, r, http.StatusBadRequest, "INVALID_BODY", "code and verifier are required")
		return
	}

	authCode, err := h.cliAuthCodes.Consume(r.Context(), db.HashCLIAuthCode(body.Code))
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, r, http.StatusGone, "CLI_CODE_EXPIRED", "login code is invalid, expired, or already used — retry `143-tools login`")
			return
		}
		writeError(w, r, http.StatusInternalServerError, "CLI_EXCHANGE_FAILED", "failed to look up login code", err)
		return
	}

	// Verifier binding: SHA-256(verifier) must equal the challenge the CLI
	// sent at /cli/start. This prevents another local process that raced to
	// read the loopback redirect from redeeming the code. The row is already
	// consumed at this point — a failed binding burns the code, by design.
	verifierSum := sha256.Sum256([]byte(body.Verifier))
	if subtle.ConstantTimeCompare([]byte(hex.EncodeToString(verifierSum[:])), []byte(strings.ToLower(authCode.Challenge))) != 1 {
		writeError(w, r, http.StatusBadRequest, "CLI_VERIFIER_MISMATCH", "verifier does not match the login challenge")
		return
	}

	user, err := h.userStore.GetByIDGlobal(r.Context(), authCode.UserID)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "CLI_EXCHANGE_FAILED", "failed to load user", err)
		return
	}

	rawToken, err := db.GenerateUserCLIToken()
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "CLI_EXCHANGE_FAILED", "failed to mint token", err)
		return
	}
	cliToken := &models.UserCLIToken{
		UserID:      user.ID,
		TokenHash:   db.HashAPIToken(rawToken),
		TokenPrefix: db.UserCLITokenDisplayPrefix(rawToken),
		DeviceName:  authCode.DeviceName,
		LastOrgID:   authCode.OrgID,
		ExpiresAt:   time.Now().Add(db.UserCLITokenTTL),
	}
	if err := h.cliTokens.Create(r.Context(), cliToken); err != nil {
		writeError(w, r, http.StatusInternalServerError, "CLI_EXCHANGE_FAILED", "failed to persist token", err)
		return
	}

	var orgPayload map[string]any
	if authCode.OrgID != nil {
		h.emitCLITokenEvent(r, &user, *authCode.OrgID, cliToken, models.AuditActionAuthCLILogin)
		if h.orgStore != nil {
			if org, orgErr := h.orgStore.GetByID(r.Context(), *authCode.OrgID); orgErr == nil {
				orgPayload = map[string]any{"id": org.ID, "name": org.Name}
			}
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"data": map[string]any{
			"token":        rawToken,
			"token_id":     cliToken.ID,
			"token_prefix": cliToken.TokenPrefix,
			"expires_at":   cliToken.ExpiresAt,
			"user": map[string]any{
				"id":           user.ID,
				"email":        user.Email,
				"name":         user.Name,
				"github_login": user.GitHubLogin,
			},
			"org": orgPayload,
		},
	})
}

// ListCLITokens returns the caller's own CLI tokens (device, prefix,
// last-used) for the self-service "CLI sessions" surface. Zero-membership
// safe: token management must survive losing your last org.
func (h *AuthHandler) ListCLITokens(w http.ResponseWriter, r *http.Request) {
	user := middleware.UserFromContext(r.Context())
	if user == nil {
		writeError(w, r, http.StatusUnauthorized, "UNAUTHORIZED", "not authenticated")
		return
	}
	if h.cliTokens == nil {
		writeError(w, r, http.StatusInternalServerError, "CLI_AUTH_NOT_CONFIGURED", "CLI tokens are not configured on this server")
		return
	}
	tokens, err := h.cliTokens.ListByUser(r.Context(), user.ID)
	if err != nil {
		writeError(w, r, http.StatusInternalServerError, "CLI_TOKEN_LIST_FAILED", "failed to list CLI tokens", err)
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": tokens})
}

// RevokeCLIToken revokes one of the caller's own CLI tokens. `143-tools
// logout` calls this with its current token before clearing local config.
func (h *AuthHandler) RevokeCLIToken(w http.ResponseWriter, r *http.Request) {
	user := middleware.UserFromContext(r.Context())
	if user == nil {
		writeError(w, r, http.StatusUnauthorized, "UNAUTHORIZED", "not authenticated")
		return
	}
	if h.cliTokens == nil {
		writeError(w, r, http.StatusInternalServerError, "CLI_AUTH_NOT_CONFIGURED", "CLI tokens are not configured on this server")
		return
	}
	id, err := uuid.Parse(strings.TrimSpace(chi.URLParam(r, "id")))
	if err != nil {
		writeError(w, r, http.StatusBadRequest, "INVALID_TOKEN_ID", "token id must be a UUID")
		return
	}
	revoked, err := h.cliTokens.Revoke(r.Context(), user.ID, id)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, r, http.StatusNotFound, "CLI_TOKEN_NOT_FOUND", "CLI token not found")
			return
		}
		writeError(w, r, http.StatusInternalServerError, "CLI_TOKEN_REVOKE_FAILED", "failed to revoke CLI token", err)
		return
	}
	if revoked.LastOrgID != nil {
		h.emitCLITokenEvent(r, user, *revoked.LastOrgID, &revoked, models.AuditActionAuthCLILogout)
	}
	writeJSON(w, http.StatusOK, map[string]any{"data": map[string]string{"status": "revoked"}})
}

func (h *AuthHandler) emitCLITokenEvent(r *http.Request, user *models.User, orgID uuid.UUID, token *models.UserCLIToken, action models.AuditAction) {
	if h.audit == nil {
		return
	}
	tokenID := token.ID.String()
	details, _ := json.Marshal(map[string]any{
		"token_prefix": token.TokenPrefix,
		"device_name":  token.DeviceName,
	})
	params := db.UserActionParams{
		OrgID:        orgID,
		UserID:       user.ID,
		Action:       action,
		ResourceType: models.AuditResourceCLIToken,
		ResourceID:   &tokenID,
		Details:      details,
	}
	if ua := r.UserAgent(); ua != "" {
		params.UserAgent = &ua
	}
	if ip := parseClientIP(r); ip != nil {
		params.IPAddress = ip
	}
	h.audit.EmitUserAction(r.Context(), params)
}

// --- join-token JIT provisioning (Phase 3) ---

// applyJoinTokenForExistingUser grants an existing user membership in the
// join token's org. Already-a-member is a no-op that does NOT burn a use;
// invalid tokens are logged and ignored (the token was a no-op for an
// existing user anyway — they still log in normally). Mirrors
// claimPendingInvitationForExistingUser's best-effort posture.
func (h *AuthHandler) applyJoinTokenForExistingUser(r *http.Request, joinToken string, user *models.User) {
	if joinToken == "" || h.joinTokens == nil {
		return
	}
	log := zerolog.Ctx(r.Context())

	preview, err := h.joinTokens.GetActiveByToken(r.Context(), joinToken)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			log.Warn().Err(err).Msg("join token lookup failed during oauth login")
		} else {
			log.Info().Str("user_id", user.ID.String()).Msg("invalid join token presented by existing user; ignoring")
		}
		return
	}
	if h.memberships != nil {
		if _, err := h.memberships.Get(r.Context(), user.ID, preview.OrgID); err == nil {
			return // already a member — no-op, no use consumed
		}
	}

	tx, err := h.pool.Begin(r.Context())
	if err != nil {
		log.Warn().Err(err).Msg("join token grant: begin failed")
		return
	}
	defer func() { _ = tx.Rollback(r.Context()) }()

	consumed, err := db.NewOrgJoinTokenStore(tx).ConsumeByToken(r.Context(), joinToken)
	if err != nil {
		// Raced out of uses or revoked between preview and consume.
		log.Info().Err(err).Str("user_id", user.ID.String()).Msg("join token consume failed for existing user; ignoring")
		return
	}
	effectiveRole, err := db.NewOrganizationMembershipStore(tx).GrantAtLeast(r.Context(), user.ID, consumed.OrgID, consumed.Role)
	if err != nil {
		log.Warn().Err(err).Msg("join token grant failed for existing user")
		return
	}
	if err := tx.Commit(r.Context()); err != nil {
		log.Warn().Err(err).Msg("join token grant: commit failed")
		return
	}
	h.emitJoinTokenUsed(r, user.ID, &consumed, effectiveRole)
}

// createJoinedUser atomically consumes a join token, creates the OAuth user,
// grants the membership, and issues the initial session — mirroring
// acceptInvitationAndUpsertUser. The returned *invitationError (reusing the
// struct for its status/code/message shape) is non-nil when the token is
// invalid/exhausted: the caller fails closed for new users, because a
// forgiving fallback would silently log the CLI into a fresh single-member
// org — strictly worse than the error for someone trying to join a team.
func (h *AuthHandler) createJoinedUser(
	ctx context.Context,
	joinToken string,
	user *models.User,
	upsert oauthUserUpsertFunc,
) (*models.User, string, *models.OrgJoinToken, *invitationError, error) {
	if h.pool == nil {
		return nil, "", nil, nil, fmt.Errorf("auth handler pool is not configured")
	}

	tx, err := h.pool.Begin(ctx)
	if err != nil {
		return nil, "", nil, nil, fmt.Errorf("begin join transaction: %w", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()

	consumed, err := db.NewOrgJoinTokenStore(tx).ConsumeByToken(ctx, joinToken)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil, "", nil, &invitationError{
				status:  http.StatusForbidden,
				code:    "JOIN_TOKEN_INVALID",
				message: "this join link is expired or revoked — ask your admin for a new one",
			}, nil
		}
		return nil, "", nil, nil, fmt.Errorf("consume join token: %w", err)
	}

	user.OrgID = consumed.OrgID
	user.Role = consumed.Role
	if err := upsert(ctx, db.NewUserStore(tx), user); err != nil {
		return nil, "", nil, nil, fmt.Errorf("upsert joined oauth user: %w", err)
	}

	effectiveRole, err := db.NewOrganizationMembershipStore(tx).GrantAtLeast(ctx, user.ID, consumed.OrgID, consumed.Role)
	if err != nil {
		return nil, "", nil, nil, fmt.Errorf("grant joined membership: %w", err)
	}
	user.Role = models.Role(effectiveRole)

	sessionToken, err := h.persistSessionTx(ctx, tx, user)
	if err != nil {
		return nil, "", nil, nil, fmt.Errorf("create join session: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return nil, "", nil, nil, fmt.Errorf("commit join transaction: %w", err)
	}
	return user, sessionToken, &consumed, nil, nil
}

func (h *AuthHandler) emitJoinTokenUsed(r *http.Request, userID uuid.UUID, token *models.OrgJoinToken, effectiveRole string) {
	if h.audit == nil || token == nil {
		return
	}
	tokenID := token.ID.String()
	details, _ := json.Marshal(map[string]any{
		"token_prefix":   token.TokenPrefix,
		"token_name":     token.Name,
		"granted_role":   token.Role,
		"effective_role": effectiveRole,
		"use_count":      token.UseCount,
	})
	h.audit.EmitUserAction(r.Context(), db.UserActionParams{
		OrgID:        token.OrgID,
		UserID:       userID,
		Action:       models.AuditActionOrgJoinTokenUsed,
		ResourceType: models.AuditResourceOrgJoinToken,
		ResourceID:   &tokenID,
		Details:      details,
	})
}

// --- small shared helpers ---

func sanitizeCLIDeviceName(raw string) string {
	raw = strings.TrimSpace(raw)
	var b strings.Builder
	for _, r := range raw {
		if r < 0x20 || r == 0x7f {
			continue
		}
		b.WriteRune(r)
	}
	out := b.String()
	if len(out) > maxCLIDeviceNameLength {
		out = out[:maxCLIDeviceNameLength]
	}
	return out
}

func setCLICookie(w http.ResponseWriter, name, value string) {
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Value:    value,
		Path:     "/",
		MaxAge:   cliCookieMaxAge,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
}

// popCookie reads and unconditionally clears a cookie. Returns "" when
// absent.
func popCookie(w http.ResponseWriter, r *http.Request, name string) string {
	cookie, err := r.Cookie(name)
	if err != nil || cookie.Value == "" {
		return ""
	}
	http.SetCookie(w, &http.Cookie{
		Name:     name,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
	})
	return cookie.Value
}
