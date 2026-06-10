package middleware

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"time"

	chiMiddleware "github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
)

type contextKey string

type resolvedIdentityRecorder interface {
	SetResolvedIdentity(orgID, userID uuid.UUID)
}

const (
	userContextKey            contextKey = "user"
	orgIDContextKey           contextKey = "org_id"
	activeRoleContextKey      contextKey = "active_role"
	previewAPITokenContextKey contextKey = "preview_api_token"

	// SessionCookieName is the cookie holding the opaque session token.
	SessionCookieName = "session_token"
	// SessionTTL is how far ahead the session's expires_at is set.
	SessionTTL = 30 * 24 * time.Hour
	// sessionRefreshWindow: if a session has less than (TTL - refreshWindow)
	// remaining, we extend it back to TTL. With these values, an active user's
	// session gets pushed out by at most once every 5 days.
	sessionRefreshWindow = 5 * 24 * time.Hour
)

// ActiveOrgHeader is the HTTP header clients use to declare which org should
// scope the request. Takes precedence over the session's last_org_id hint.
// Missing, malformed, or unrelated values fall through to the session hint,
// then the user's oldest membership.
const ActiveOrgHeader = "X-Active-Org-ID"

// RevokedOrgHeader is emitted on responses when the request's X-Active-Org-ID
// header or the session's last_org_id hint pointed at an org the user is no
// longer a member of (typically because they were removed from that org) and
// the middleware fell through to a different membership. Clients should read
// this header and refresh their cached active-org state — otherwise every
// subsequent request keeps sending the stale header and getting silently
// re-resolved.
//
// The header value is an opaque "1" rather than the revoked org's UUID. The
// client already knows which org it asked for (it sent the X-Active-Org-ID
// header itself, or it has the session-minted hint in its cache) so echoing
// the UUID adds no information for the legitimate client; emitting a static
// flag avoids confirming org-id existence to a caller that forged the header.
const RevokedOrgHeader = "X-Org-Membership-Revoked"

// RevokedOrgHeaderValue is the single value the middleware sets on
// RevokedOrgHeader. Exported so tests and clients share the exact string.
const RevokedOrgHeaderValue = "1"

// RevokedMembershipCode is the error code body responses use to signal the
// same condition RevokedOrgHeader advertises for requests that also need a
// 4xx surface (e.g. a future explicit "am I still a member of this org?"
// endpoint). The header is the hot-path signal; this code is a reservation
// so handlers and tests agree on the vocabulary.
const RevokedMembershipCode = "ORG_MEMBERSHIP_REVOKED"

func UserFromContext(ctx context.Context) *models.User {
	u, _ := ctx.Value(userContextKey).(*models.User)
	return u
}

func OrgIDFromContext(ctx context.Context) uuid.UUID {
	id, _ := ctx.Value(orgIDContextKey).(uuid.UUID)
	return id
}

// ActiveRoleFromContext returns the role of the current user in the active
// org. Empty if no membership resolved (zero-membership user). Handlers
// gating behavior on role MUST prefer this over User.Role, because the
// legacy user.Role only reflects the user's primary-org role and is not
// authoritative in the multi-org world.
func ActiveRoleFromContext(ctx context.Context) string {
	role, _ := ctx.Value(activeRoleContextKey).(string)
	return role
}

func PreviewAPITokenFromContext(ctx context.Context) *models.PreviewAPIToken {
	token, _ := ctx.Value(previewAPITokenContextKey).(*models.PreviewAPIToken)
	return token
}

func WithUser(ctx context.Context, u *models.User) context.Context {
	return context.WithValue(ctx, userContextKey, u)
}

func WithOrgID(ctx context.Context, id uuid.UUID) context.Context {
	return context.WithValue(ctx, orgIDContextKey, id)
}

// WithActiveRole stores the resolved active-org role on the request context.
func WithActiveRole(ctx context.Context, role string) context.Context {
	return context.WithValue(ctx, activeRoleContextKey, role)
}

func WithPreviewAPIToken(ctx context.Context, token *models.PreviewAPIToken) context.Context {
	return context.WithValue(ctx, previewAPITokenContextKey, token)
}

// AuthStores bundles the store dependencies the Auth middleware needs. Using
// a struct rather than a long positional signature keeps router wiring (and
// the test harness) readable as we add the membership and org stores.
type AuthStores struct {
	Sessions         *db.AuthSessionStore
	Users            *db.UserStore
	Memberships      *db.OrganizationMembershipStore
	PreviewAPITokens *db.PreviewAPITokenStore
	APITokens        *db.APITokenStore
	UserCLITokens    *db.UserCLITokenStore
	Audit            *db.AuditEmitter
}

// Auth reads the session cookie (or Bearer token), loads the user identity,
// resolves the active membership, and injects user, org_id, and active_role
// into the request context. Session validation failures return 401 and clear
// the cookie; membership resolution failures return 403 so the client can
// recover by switching orgs. A user with zero memberships is still allowed
// through so the frontend can render an empty/no-memberships state — the
// org_id context value will be uuid.Nil in that case, and any downstream
// org-scoped handler will reject the request via OrgContext.
//
// csrfKey is used to extend the CSRF cookie in lockstep with sliding session
// refresh so its lifetime never trails the session. Pass nil to skip CSRF
// extension (e.g. in tests that don't exercise CSRF). logger is used to
// surface best-effort refresh failures at Warn level.
func Auth(stores AuthStores, csrfKey []byte, logger zerolog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			cookie, err := r.Cookie(SessionCookieName)
			if err != nil {
				auth := r.Header.Get("Authorization")
				if auth == "" || !strings.HasPrefix(auth, "Bearer ") {
					writeError(w, http.StatusUnauthorized, "UNAUTHORIZED", "missing session")
					return
				}
				token := strings.TrimPrefix(auth, "Bearer ")
				handleToken(w, r, next, stores, csrfKey, logger, token, false)
				return
			}
			handleToken(w, r, next, stores, csrfKey, logger, cookie.Value, true)
		})
	}
}

func handleToken(w http.ResponseWriter, r *http.Request, next http.Handler, stores AuthStores, csrfKey []byte, logger zerolog.Logger, token string, cookieBased bool) {
	session, err := stores.Sessions.GetByToken(r.Context(), token)
	if err != nil {
		// Only treat ErrNoRows as a genuinely invalid session. Other errors
		// (pool exhaustion, connection reset, query timeout during a rolling
		// deploy) must NOT clear the cookie or return 401, otherwise the
		// browser loses its session and the frontend — which treats 401 as
		// terminal — logs the user out. Surface transient errors as 503 so
		// the useAuth retry path handles them.
		if errors.Is(err, pgx.ErrNoRows) {
			if !cookieBased && stores.UserCLITokens != nil && strings.HasPrefix(token, db.UserCLITokenPrefix) {
				if handleUserCLIToken(w, r, next, stores, logger, token) {
					return
				}
			}
			if !cookieBased && stores.APITokens != nil && strings.HasPrefix(token, "143_sk_") {
				if handleGeneralAPIToken(w, r, next, stores, logger, token) {
					return
				}
			}
			if !cookieBased && stores.PreviewAPITokens != nil {
				if handlePreviewAPIToken(w, r, next, stores, logger, token) {
					return
				}
			}
			if cookieBased {
				clearSessionCookie(w, r)
			}
			writeError(w, http.StatusUnauthorized, "UNAUTHORIZED", "invalid session")
			return
		}
		logger.Warn().Err(err).Msg("auth: session lookup failed")
		writeError(w, http.StatusServiceUnavailable, "SERVICE_UNAVAILABLE", "session lookup failed")
		return
	}

	user, err := stores.Users.GetByIDGlobal(r.Context(), session.UserID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			if cookieBased {
				clearSessionCookie(w, r)
			}
			writeError(w, http.StatusUnauthorized, "UNAUTHORIZED", "user not found")
			return
		}
		logger.Warn().Err(err).Msg("auth: user lookup failed")
		writeError(w, http.StatusServiceUnavailable, "SERVICE_UNAVAILABLE", "user lookup failed")
		return
	}

	resolution, err := resolveActiveMembership(r, stores, user.ID, session)
	if err != nil {
		// resolveActiveMembership graceful-degrades through stale / malformed
		// headers and stale session hints, so an error at this point can only
		// come from a true infrastructure failure (DB unreachable, etc.). 500
		// rather than 403 so operators can distinguish real outages from the
		// "you're not a member of this org" case (which no longer raises).
		logger.Warn().Err(err).Str("user_id", user.ID.String()).Msg("auth: membership resolution failed")
		writeError(w, http.StatusInternalServerError, "MEMBERSHIP_RESOLUTION_FAILED", "failed to resolve active membership")
		return
	}
	activeOrgID := resolution.orgID
	activeRole := resolution.role

	// Surface a revocation signal to the client so it knows its cached active
	// org drifted (user was removed from that org, or the org was deleted).
	// This is a best-effort hint: the request still succeeds against whatever
	// membership we resolved to, but the client should refresh its cached
	// active-org state on seeing this header rather than keep sending the
	// stale X-Active-Org-ID every request. The value is an opaque flag — see
	// RevokedOrgHeader doc for why we don't echo the UUID.
	if resolution.membershipRevoked {
		w.Header().Set(RevokedOrgHeader, RevokedOrgHeaderValue)
	}

	// Persist the resolution as the session's last_org_id if it changed, so
	// the next cold load (new tab / no X-Active-Org-ID) picks up where this
	// one left off. Failures here are logged but do not fail the request —
	// the hint is a convenience, not load-bearing.
	//
	// We skip the write when the resolution came from the X-Active-Org-ID
	// header. Two simultaneous tabs pinned to different orgs would otherwise
	// fight to overwrite last_org_id on every request, causing the "neither
	// tab is sticky" symptom on cold load. The header is the client's
	// authoritative declaration; the session hint should only track the
	// session-level fallback (no header in flight).
	if !resolution.fromHeader && activeOrgID != uuid.Nil && (session.LastOrgID == nil || *session.LastOrgID != activeOrgID) {
		if updateErr := stores.Sessions.UpdateLastOrgID(r.Context(), token, &activeOrgID); updateErr != nil {
			zerolog.Ctx(r.Context()).Warn().Err(updateErr).Msg("failed to persist last_org_id")
		}
	}

	// TODO(2026-04-25): drop this legacy sync once user.OrgID / user.Role
	// are removed. Keep the legacy User.Role field in sync with the
	// active-role so handlers written against the single-org model continue
	// to behave correctly during the compatibility window. New code should
	// read ActiveRoleFromContext and OrgIDFromContext directly.
	if activeRole != "" {
		user.Role = models.Role(activeRole)
		user.OrgID = activeOrgID
	}

	// Sliding-window refresh: if the cookie is more than refreshWindow old
	// (i.e. expires_at is inside (now, now + TTL - refreshWindow)), push it
	// back out to TTL so active users don't get a hard logout at 30 days.
	// Only for cookie-based auth — bearer tokens manage their own lifetime.
	if cookieBased {
		maybeRefreshSession(w, r, stores.Sessions, csrfKey, logger, session)
	}

	ctx := WithUser(r.Context(), &user)
	ctx = WithOrgID(ctx, activeOrgID)
	ctx = WithActiveRole(ctx, activeRole)
	if recorder, ok := w.(resolvedIdentityRecorder); ok {
		recorder.SetResolvedIdentity(activeOrgID, user.ID)
	}
	next.ServeHTTP(w, r.WithContext(ctx))
}

// handleUserCLIToken authenticates a "143u_" bearer token from the 143-tools
// CLI. CLI tokens are user-scoped session-equivalents: the request gets the
// same user context a session cookie would, and active-org resolution runs
// the identical header → last_org_id → oldest-membership chain. Returns true
// when the request was handled (success or terminal error); false to let the
// caller fall through to other token types.
func handleUserCLIToken(w http.ResponseWriter, r *http.Request, next http.Handler, stores AuthStores, logger zerolog.Logger, rawToken string) bool {
	cliToken, err := stores.UserCLITokens.GetActiveByToken(r.Context(), rawToken)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			logger.Warn().Err(err).Msg("auth: cli token lookup failed")
			writeError(w, http.StatusServiceUnavailable, "SERVICE_UNAVAILABLE", "cli token lookup failed")
			return true
		}
		return false
	}

	user, err := stores.Users.GetByIDGlobal(r.Context(), cliToken.UserID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			writeError(w, http.StatusUnauthorized, "UNAUTHORIZED", "user not found")
			return true
		}
		logger.Warn().Err(err).Msg("auth: cli token user lookup failed")
		writeError(w, http.StatusServiceUnavailable, "SERVICE_UNAVAILABLE", "user lookup failed")
		return true
	}

	// Reuse the session resolution chain by projecting the token's
	// last_org_id hint into a synthetic session value. The semantics are
	// deliberately identical — see UserCLIToken doc.
	resolution, err := resolveActiveMembership(r, stores, user.ID, models.AuthSession{LastOrgID: cliToken.LastOrgID})
	if err != nil {
		logger.Warn().Err(err).Str("user_id", user.ID.String()).Msg("auth: cli token membership resolution failed")
		writeError(w, http.StatusInternalServerError, "MEMBERSHIP_RESOLUTION_FAILED", "failed to resolve active membership")
		return true
	}
	if resolution.membershipRevoked {
		w.Header().Set(RevokedOrgHeader, RevokedOrgHeaderValue)
	}

	if !resolution.fromHeader && resolution.orgID != uuid.Nil &&
		(cliToken.LastOrgID == nil || *cliToken.LastOrgID != resolution.orgID) {
		if updateErr := stores.UserCLITokens.UpdateLastOrgID(r.Context(), cliToken.ID, &resolution.orgID); updateErr != nil {
			logger.Warn().Err(updateErr).Msg("auth: failed to persist cli token last_org_id")
		}
	}

	// Throttled usage stamp doubling as the sliding-expiry write: extend
	// expires_at to now()+TTL at most once per minute so active devices
	// never hit the 90-day wall while the hot path stays read-mostly.
	if cliToken.LastUsedAt == nil || time.Since(*cliToken.LastUsedAt) > time.Minute {
		if touchErr := stores.UserCLITokens.TouchUsage(r.Context(), cliToken.ID, remoteAddrIP(r), time.Now().Add(db.UserCLITokenTTL)); touchErr != nil {
			logger.Warn().Err(touchErr).Msg("auth: cli token usage stamp failed")
		}
	}

	// Legacy single-org compat sync, mirroring the session path.
	if resolution.role != "" {
		user.Role = models.Role(resolution.role)
		user.OrgID = resolution.orgID
	}

	ctx := WithUser(r.Context(), &user)
	ctx = WithOrgID(ctx, resolution.orgID)
	ctx = WithActiveRole(ctx, resolution.role)
	if recorder, ok := w.(resolvedIdentityRecorder); ok {
		recorder.SetResolvedIdentity(resolution.orgID, user.ID)
	}
	next.ServeHTTP(w, r.WithContext(ctx))
	return true
}

func handleGeneralAPIToken(w http.ResponseWriter, r *http.Request, next http.Handler, stores AuthStores, logger zerolog.Logger, rawToken string) bool {
	resolved, err := stores.APITokens.GetByToken(r.Context(), rawToken, remoteAddrIP(r), r.UserAgent())
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			logger.Warn().Err(err).Msg("auth: api token lookup failed")
			writeError(w, http.StatusServiceUnavailable, "SERVICE_UNAVAILABLE", "api token lookup failed")
			return true
		}
		return false
	}
	if resolved.Client.Status == models.APIClientStatusDisabled {
		writeError(w, http.StatusForbidden, "API_CLIENT_DISABLED", "API client is disabled")
		return true
	}
	ctx := WithAPIIdentity(r.Context(), &resolved.Client, &resolved.Token)
	ctx = WithActiveRole(ctx, "api_token")
	emitAPITokenUsed(r, stores.Audit, resolved)
	next.ServeHTTP(w, r.WithContext(ctx))
	return true
}

func emitAPITokenUsed(r *http.Request, emitter *db.AuditEmitter, resolved models.AuthenticatedAPIToken) {
	if emitter == nil {
		return
	}
	tokenID := resolved.Token.ID
	resourceID := tokenID.String()
	var requestID *string
	if reqID := chiMiddleware.GetReqID(r.Context()); reqID != "" {
		requestID = &reqID
	}
	var userAgent *string
	if ua := r.UserAgent(); ua != "" {
		userAgent = &ua
	}
	details, err := json.Marshal(map[string]any{
		"method":       r.Method,
		"path":         r.URL.Path,
		"token_prefix": resolved.Token.TokenPrefix,
	})
	if err != nil {
		details = []byte(`{}`)
	}
	emitter.EmitAPIAction(r.Context(), db.APIActionParams{
		OrgID:        resolved.Client.OrgID,
		APIClientID:  resolved.Client.ID,
		APITokenID:   &tokenID,
		Action:       models.AuditActionAPITokenUsed,
		ResourceType: models.AuditResourceAPIToken,
		ResourceID:   &resourceID,
		Details:      details,
		RequestID:    requestID,
		UserAgent:    userAgent,
	})
}

func handlePreviewAPIToken(w http.ResponseWriter, r *http.Request, next http.Handler, stores AuthStores, logger zerolog.Logger, rawToken string) bool {
	apiToken, err := stores.PreviewAPITokens.GetByToken(r.Context(), rawToken)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			logger.Warn().Err(err).Msg("auth: preview api token lookup failed")
			writeError(w, http.StatusServiceUnavailable, "SERVICE_UNAVAILABLE", "preview api token lookup failed")
			return true
		}
		return false
	}
	// Single JOIN query: verify user exists and is still a member of the token's
	// org. This replaces the prior two sequential queries (user lookup + membership
	// check) and correctly handles the "creator left the org" revocation case.
	user, err := stores.Users.GetByIDGlobalWithMembershipCheck(r.Context(), apiToken.CreatedByUserID, apiToken.OrgID)
	if err != nil {
		logger.Warn().Err(err).
			Str("user_id", apiToken.CreatedByUserID.String()).
			Str("org_id", apiToken.OrgID.String()).
			Msg("auth: preview api token creator not found or no longer a member of the org")
		writeError(w, http.StatusUnauthorized, "UNAUTHORIZED", "preview api token creator is not an active member of the organization")
		return true
	}
	ctx := WithUser(r.Context(), &user)
	ctx = WithOrgID(ctx, apiToken.OrgID)
	ctx = WithActiveRole(ctx, "preview_api_token")
	ctx = WithPreviewAPIToken(ctx, &apiToken)
	next.ServeHTTP(w, r.WithContext(ctx))
	return true
}

// membershipResolution is the outcome of resolveActiveMembership. orgID/role
// are the resolved active membership (orgID may be uuid.Nil when the user has
// zero memberships — handlers render an empty state in that case). fromHeader
// is true when X-Active-Org-ID drove the choice; the caller uses that to skip
// the last_org_id write so two tabs pinned to different orgs don't trample
// each other's session hint. membershipRevoked is true when the request's
// header or session hint named an org the user is no longer in — the caller
// emits RevokedOrgHeader so the client can refresh its cached active org. We
// track this as a bool, not the revoked UUID, so the response header stays
// opaque (RevokedOrgHeader doc explains why).
type membershipResolution struct {
	orgID             uuid.UUID
	role              string
	fromHeader        bool
	membershipRevoked bool
}

// resolveActiveMembership chooses which org's membership should scope this
// request. Resolution order:
//
//  1. The X-Active-Org-ID header, if present and the user holds a membership
//     in that org. Header precedence gives the client authoritative control
//     for tab-independent multi-org use.
//  2. The session's last_org_id, if it still points at a membership. This is
//     the server-side hint used on cold loads before the client has echoed
//     back a header.
//  3. The oldest membership. Deterministic fallback that keeps behavior
//     stable for users with a single membership (they always get it) and
//     avoids surprising "random org" behavior for users with many.
//
// Returns orgID=uuid.Nil with a nil error when the user has zero memberships —
// allowing the user through to see the empty state. A non-nil error means an
// unexpected database failure; requests for an org the user does not belong
// to graceful-degrade through to the next resolution step and set
// membershipRevoked so the caller can emit RevokedOrgHeader.
func resolveActiveMembership(r *http.Request, stores AuthStores, userID uuid.UUID, session models.AuthSession) (membershipResolution, error) {
	var revoked bool

	// 1. Explicit header. We graceful-degrade a malformed or stale header
	// (falling through to the session hint and then oldest membership) rather
	// than 400/403-ing the request: a client with a corrupted or stale
	// X-Active-Org-ID (for example, after being removed from an org) would
	// otherwise be hard-locked out of every request until it hears about its
	// own removal from some other channel. Revocation is surfaced as an
	// opaque bool so the caller can set RevokedOrgHeader; DB errors still
	// propagate because those indicate real infrastructure problems.
	if raw := strings.TrimSpace(r.Header.Get(ActiveOrgHeader)); raw != "" {
		log := zerolog.Ctx(r.Context())
		requested, parseErr := uuid.Parse(raw)
		if parseErr != nil {
			log.Info().Str("user_id", userID.String()).Str("header", raw).Msg("active org header malformed; falling through to session hint")
		} else {
			m, err := stores.Memberships.Get(r.Context(), userID, requested)
			if err == nil {
				return membershipResolution{orgID: m.OrgID, role: string(m.Role), fromHeader: true}, nil
			}
			if !errors.Is(err, pgx.ErrNoRows) {
				return membershipResolution{}, err
			}
			log.Info().Str("user_id", userID.String()).Str("requested_org_id", requested.String()).Msg("active org header names an org the user is not a member of; falling through")
			revoked = true
		}
	}

	// 2. Session hint.
	if session.LastOrgID != nil {
		m, err := stores.Memberships.Get(r.Context(), userID, *session.LastOrgID)
		if err == nil {
			return membershipResolution{orgID: m.OrgID, role: string(m.Role), membershipRevoked: revoked}, nil
		}
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return membershipResolution{}, err
		}
		// Session hint is stale (membership revoked or org deleted). Signal
		// revocation so the client knows its cached state drifted, even if
		// the header path already set it — the bool only needs to be true.
		revoked = true
	}

	// 3. Oldest membership (or zero-membership empty-state).
	m, err := stores.Memberships.OldestForUser(r.Context(), userID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return membershipResolution{membershipRevoked: revoked}, nil
		}
		return membershipResolution{}, err
	}
	return membershipResolution{orgID: m.OrgID, role: string(m.Role), membershipRevoked: revoked}, nil
}

func maybeRefreshSession(w http.ResponseWriter, r *http.Request, store *db.AuthSessionStore, csrfKey []byte, logger zerolog.Logger, session models.AuthSession) {
	if time.Until(session.ExpiresAt) > SessionTTL-sessionRefreshWindow {
		return
	}
	newExpiresAt := time.Now().Add(SessionTTL)
	if err := store.Touch(r.Context(), session.Token, newExpiresAt); err != nil {
		// Best-effort: session is still valid; the user will just need to
		// re-login at the original expiry if this keeps failing.
		logger.Warn().Err(err).Msg("auth: sliding session refresh failed")
		return
	}
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookieName,
		Value:    session.Token,
		Path:     "/",
		MaxAge:   int(SessionTTL.Seconds()),
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   IsRequestSecure(r),
	})
	// Keep CSRF cookie lifetime pinned to the session so an active user
	// can't end up with a live session but an expired CSRF cookie. Best-
	// effort: failure just means the CSRF cookie expires on its own clock
	// and ensureCSRFCookie reissues on the next safe-method request.
	if len(csrfKey) > 0 {
		if err := ExtendCSRFCookie(w, r, csrfKey); err != nil {
			logger.Warn().Err(err).Msg("auth: CSRF cookie extension failed")
		}
	}
}

func clearSessionCookie(w http.ResponseWriter, r *http.Request) {
	http.SetCookie(w, &http.Cookie{
		Name:     SessionCookieName,
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Secure:   IsRequestSecure(r),
	})
}
