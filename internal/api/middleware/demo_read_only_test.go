package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDemoReadOnly(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		enabled      bool
		method       string
		path         string
		expectedCode int
		expectedBody string
	}{
		{
			name:         "disabled allows writes",
			enabled:      false,
			method:       http.MethodPost,
			path:         "/api/v1/sessions",
			expectedCode: http.StatusNoContent,
		},
		{
			name:         "allows safe reads",
			enabled:      true,
			method:       http.MethodGet,
			path:         "/api/v1/sessions",
			expectedCode: http.StatusNoContent,
		},
		{
			name:         "allows direct demo entry",
			enabled:      true,
			method:       http.MethodPost,
			path:         "/api/v1/auth/demo",
			expectedCode: http.StatusNoContent,
		},
		{
			name:         "allows logout",
			enabled:      true,
			method:       http.MethodPost,
			path:         "/api/v1/auth/logout",
			expectedCode: http.StatusNoContent,
		},
		{
			name:         "blocks session creation",
			enabled:      true,
			method:       http.MethodPost,
			path:         "/api/v1/sessions",
			expectedCode: http.StatusForbidden,
			expectedBody: "DEMO_READ_ONLY",
		},
		{
			name:         "blocks settings writes",
			enabled:      true,
			method:       http.MethodPatch,
			path:         "/api/v1/auth/me/settings",
			expectedCode: http.StatusForbidden,
			expectedBody: "DEMO_READ_ONLY",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusNoContent)
			})
			handler := DemoReadOnly(tt.enabled)(next)
			req := httptest.NewRequest(tt.method, tt.path, nil)
			w := httptest.NewRecorder()

			handler.ServeHTTP(w, req)
			require.Equal(t, tt.expectedCode, w.Code, "DemoReadOnly should return expected status")
			if tt.expectedBody != "" {
				require.Contains(t, w.Body.String(), tt.expectedBody, "DemoReadOnly should return expected error code")
			}
		})
	}
}
