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
func Auth(sessionStore *db.SessionStore, userStore *db.UserStore) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			cookie, err := r.Cookie("session_token")
			if err != nil {
				// Also check Authorization header for API access
				auth := r.Header.Get("Authorization")
				if auth == "" || !strings.HasPrefix(auth, "Bearer ") {
					http.Error(w, `{"error":{"code":"UNAUTHORIZED","message":"missing session"}}`, http.StatusUnauthorized)
					return
				}
				token := strings.TrimPrefix(auth, "Bearer ")
				handleToken(w, r, next, sessionStore, userStore, token)
				return
			}
			handleToken(w, r, next, sessionStore, userStore, cookie.Value)
		})
	}
}

func handleToken(w http.ResponseWriter, r *http.Request, next http.Handler, sessionStore *db.SessionStore, userStore *db.UserStore, token string) {
	session, err := sessionStore.GetByToken(r.Context(), token)
	if err != nil {
		http.Error(w, `{"error":{"code":"UNAUTHORIZED","message":"invalid session"}}`, http.StatusUnauthorized)
		return
	}

	user, err := userStore.GetByID(r.Context(), session.OrgID, session.UserID)
	if err != nil {
		http.Error(w, `{"error":{"code":"UNAUTHORIZED","message":"user not found"}}`, http.StatusUnauthorized)
		return
	}

	ctx := WithUser(r.Context(), &user)
	ctx = WithOrgID(ctx, user.OrgID)
	next.ServeHTTP(w, r.WithContext(ctx))
}
