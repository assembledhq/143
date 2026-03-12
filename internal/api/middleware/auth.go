package middleware

import (
	"context"
	"net/http"
	"strings"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
)

type contextKey string

const (
	userContextKey  contextKey = "user"
	orgIDContextKey contextKey = "org_id"
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
func Auth(sessionStore *db.AuthSessionStore, userStore *db.UserStore) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			cookie, err := r.Cookie("session_token")
			if err != nil {
				// Also check Authorization header for API access
				auth := r.Header.Get("Authorization")
				if auth == "" || !strings.HasPrefix(auth, "Bearer ") {
					writeError(w, http.StatusUnauthorized, "UNAUTHORIZED", "missing session")
					return
				}
				token := strings.TrimPrefix(auth, "Bearer ")
				handleToken(w, r, next, sessionStore, userStore, token, false)
				return
			}
			handleToken(w, r, next, sessionStore, userStore, cookie.Value, true)
		})
	}
}

func handleToken(w http.ResponseWriter, r *http.Request, next http.Handler, sessionStore *db.AuthSessionStore, userStore *db.UserStore, token string, clearCookieOnFailure bool) {
	session, err := sessionStore.GetByToken(r.Context(), token)
	if err != nil {
		if clearCookieOnFailure {
			clearSessionCookie(w)
		}
		writeError(w, http.StatusUnauthorized, "UNAUTHORIZED", "invalid session")
		return
	}

	user, err := userStore.GetByID(r.Context(), session.OrgID, session.UserID)
	if err != nil {
		if clearCookieOnFailure {
			clearSessionCookie(w)
		}
		writeError(w, http.StatusUnauthorized, "UNAUTHORIZED", "user not found")
		return
	}

	ctx := WithUser(r.Context(), &user)
	ctx = WithOrgID(ctx, user.OrgID)
	next.ServeHTTP(w, r.WithContext(ctx))
}

func clearSessionCookie(w http.ResponseWriter) {
	http.SetCookie(w, &http.Cookie{
		Name:     "session_token",
		Value:    "",
		Path:     "/",
		MaxAge:   -1,
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
	})
}
