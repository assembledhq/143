package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestRequireRole(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		allowedRoles  []string
		user          *models.User
		requestMethod string
		expectedCode  int
	}{
		{
			name:          "returns 401 when no user is in context",
			allowedRoles:  []string{"admin"},
			user:          nil,
			requestMethod: http.MethodGet,
			expectedCode:  http.StatusUnauthorized,
		},
		{
			name:         "returns 403 when user role is not in allowed roles",
			allowedRoles: []string{"admin"},
			user: &models.User{
				ID:    uuid.MustParse("aaaaaaaa-0000-0000-0000-000000000001"),
				OrgID: uuid.MustParse("aaaaaaaa-0000-0000-0000-000000000002"),
				Role:  "viewer",
			},
			requestMethod: http.MethodGet,
			expectedCode:  http.StatusForbidden,
		},
		{
			name:         "allows request when user role matches one of the allowed roles",
			allowedRoles: []string{"admin", "builder", "member"},
			user: &models.User{
				ID:    uuid.MustParse("bbbbbbbb-0000-0000-0000-000000000001"),
				OrgID: uuid.MustParse("bbbbbbbb-0000-0000-0000-000000000002"),
				Role:  "member",
			},
			requestMethod: http.MethodGet,
			expectedCode:  http.StatusOK,
		},
		{
			name:         "allows builder to access member-grade route",
			allowedRoles: []string{"admin", "builder", "member"},
			user: &models.User{
				ID:    uuid.MustParse("bcbcbcbc-0000-0000-0000-000000000001"),
				OrgID: uuid.MustParse("bcbcbcbc-0000-0000-0000-000000000002"),
				Role:  "builder",
			},
			requestMethod: http.MethodPost,
			expectedCode:  http.StatusOK,
		},
		{
			name:         "allows admin to access admin-only route",
			allowedRoles: []string{"admin"},
			user: &models.User{
				ID:    uuid.MustParse("cccccccc-0000-0000-0000-000000000001"),
				OrgID: uuid.MustParse("cccccccc-0000-0000-0000-000000000002"),
				Role:  "admin",
			},
			requestMethod: http.MethodDelete,
			expectedCode:  http.StatusOK,
		},
		{
			name:         "returns 403 when viewer tries to access write-only route",
			allowedRoles: []string{"admin", "builder", "member"},
			user: &models.User{
				ID:    uuid.MustParse("dddddddd-0000-0000-0000-000000000001"),
				OrgID: uuid.MustParse("dddddddd-0000-0000-0000-000000000002"),
				Role:  "viewer",
			},
			requestMethod: http.MethodPatch,
			expectedCode:  http.StatusForbidden,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			handler := RequireRole(tt.allowedRoles...)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
			}))

			req := httptest.NewRequest(tt.requestMethod, "/", nil)
			if tt.user != nil {
				ctx := WithUser(req.Context(), tt.user)
				ctx = WithActiveRole(ctx, tt.user.Role)
				req = req.WithContext(ctx)
			}
			w := httptest.NewRecorder()

			handler.ServeHTTP(w, req)
			require.Equal(t, tt.expectedCode, w.Code, "should return expected HTTP status code")
		})
	}
}
