package middleware

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
)

type contextKey string

const (
	userContextKey  contextKey = "user"
	orgIDContextKey contextKey = "org_id"

	// SessionCookieName is the cookie holding the opaque session token.
	SessionCookieName = "session_token"
	// SessionTTL is how far ahead the session's expires_at is set.
	SessionTTL = 30 * 24 * time.Hour
	// sessionRefreshWindow: if a session has less than (TTL - refreshWindow)
	// remaining, we extend it back to TTL. With these values, an active user's
	// session gets pushed out by at most once every 5 days.
	sessionRefreshWindow = 5 * 24 * time.Hour
)

func UserFromContext(ctx context.Context) *models.User {
	u, _ := ctx.Value(userContextKey).(*models.User)
	return u
}

func OrgIDFromContext(ctx context.Context) uuid.UUID {
	id, _ := ctx.Value(orgIDContextKey).(uuid.UUID)
	return id
}

func WithUser(ctx context.Context, u *models.User) context.Context {
	return context.WithValue(ctx, userContextKey, u)
}

func WithOrgID(ctx context.Context, id uuid.UUID) context.Context {
	return context.WithValue(ctx, orgIDContextKey, id)
}

// Auth middleware reads the session cookie, validates the session, and sets user on context.
// csrfKey is used to extend the CSRF cookie in lockstep with sliding session
// refresh so its lifetime never trails the session. Pass nil to skip CSRF
// extension (e.g. in tests that don't exercise CSRF). logger is used to
// surface best-effort refresh failures at Warn level.
func Auth(sessionStore *db.AuthSessionStore, userStore *db.UserStore, csrfKey []byte, logger zerolog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			cookie, err := r.Cookie(SessionCookieName)
			if err != nil {
				// Also check Authorization header for API access
				auth := r.Header.Get("Authorization")
				if auth == "" || !strings.HasPrefix(auth, "Bearer ") {
					writeError(w, http.StatusUnauthorized, "UNAUTHORIZED", "missing session")
					return
				}
				token := strings.TrimPrefix(auth, "Bearer ")
				handleToken(w, r, next, sessionStore, userStore, csrfKey, logger, token, false)
				return
			}
			handleToken(w, r, next, sessionStore, userStore, csrfKey, logger, cookie.Value, true)
		})
	}
}

func handleToken(w http.ResponseWriter, r *http.Request, next http.Handler, sessionStore *db.AuthSessionStore, userStore *db.UserStore, csrfKey []byte, logger zerolog.Logger, token string, cookieBased bool) {
	session, err := sessionStore.GetByToken(r.Context(), token)
	if err != nil {
		if cookieBased {
			clearSessionCookie(w, r)
		}
		writeError(w, http.StatusUnauthorized, "UNAUTHORIZED", "invalid session")
		return
	}

	user, err := userStore.GetByID(r.Context(), session.OrgID, session.UserID)
	if err != nil {
		if cookieBased {
			clearSessionCookie(w, r)
		}
		writeError(w, http.StatusUnauthorized, "UNAUTHORIZED", "user not found")
		return
	}

	// Sliding-window refresh: if the cookie is more than refreshWindow old
	// (i.e. expires_at is inside (now, now + TTL - refreshWindow)), push it
	// back out to TTL so active users don't get a hard logout at 30 days.
	// Only for cookie-based auth — bearer tokens manage their own lifetime.
	if cookieBased {
		maybeRefreshSession(w, r, sessionStore, csrfKey, logger, session)
	}

	ctx := WithUser(r.Context(), &user)
	ctx = WithOrgID(ctx, user.OrgID)
	next.ServeHTTP(w, r.WithContext(ctx))
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
