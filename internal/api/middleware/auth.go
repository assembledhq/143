package middleware

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
)

type contextKey string

const (
	userContextKey       contextKey = "user"
	orgIDContextKey      contextKey = "org_id"
	activeRoleContextKey contextKey = "active_role"

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

// AuthStores bundles the store dependencies the Auth middleware needs. Using
// a struct rather than a long positional signature keeps router wiring (and
// the test harness) readable as we add the membership and org stores.
type AuthStores struct {
	Sessions    *db.AuthSessionStore
	Users       *db.UserStore
	Memberships *db.OrganizationMembershipStore
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

	activeOrgID, activeRole, fromHeader, err := resolveActiveMembership(r, stores, user.ID, session)
	if err != nil {
		writeError(w, http.StatusForbidden, "NO_MEMBERSHIP", err.Error())
		return
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
	if !fromHeader && activeOrgID != uuid.Nil && (session.LastOrgID == nil || *session.LastOrgID != activeOrgID) {
		if updateErr := stores.Sessions.UpdateLastOrgID(r.Context(), token, &activeOrgID); updateErr != nil {
			zerolog.Ctx(r.Context()).Warn().Err(updateErr).Msg("failed to persist last_org_id")
		}
	}

	// Keep the legacy User.Role field in sync with the active-role so
	// handlers written against the single-org model continue to behave
	// correctly during the compatibility window. New code should read
	// ActiveRoleFromContext directly.
	if activeRole != "" {
		user.Role = activeRole
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
	next.ServeHTTP(w, r.WithContext(ctx))
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
// Returns uuid.Nil with a nil error when the user has zero memberships —
// allowing the user through to see the empty state. A non-nil error means
// the client explicitly requested an org the user does not belong to, which
// is a 403 (the client should re-resolve via /auth/me).
//
// The fromHeader return is true when the X-Active-Org-ID header drove the
// choice; the caller uses that to skip the last_org_id write so two tabs
// pinned to different orgs don't trample each other's session hint.
func resolveActiveMembership(r *http.Request, stores AuthStores, userID uuid.UUID, session models.AuthSession) (uuid.UUID, string, bool, error) {
	// 1. Explicit header.
	if raw := strings.TrimSpace(r.Header.Get(ActiveOrgHeader)); raw != "" {
		requested, parseErr := uuid.Parse(raw)
		if parseErr != nil {
			return uuid.Nil, "", false, errors.New("invalid active org header")
		}
		m, err := stores.Memberships.Get(r.Context(), userID, requested)
		if err == nil {
			return m.OrgID, m.Role, true, nil
		}
		if !errors.Is(err, pgx.ErrNoRows) {
			return uuid.Nil, "", false, err
		}
		// Explicit request for an org the user does not belong to — refuse.
		return uuid.Nil, "", false, errors.New("not a member of requested org")
	}

	// 2. Session hint.
	if session.LastOrgID != nil {
		m, err := stores.Memberships.Get(r.Context(), userID, *session.LastOrgID)
		if err == nil {
			return m.OrgID, m.Role, false, nil
		}
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return uuid.Nil, "", false, err
		}
		// Session hint is stale (membership revoked or org deleted) — fall
		// through to oldest membership.
	}

	// 3. Oldest membership (or zero-membership empty-state).
	m, err := stores.Memberships.OldestForUser(r.Context(), userID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return uuid.Nil, "", false, nil
		}
		return uuid.Nil, "", false, err
	}
	return m.OrgID, m.Role, false, nil
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
