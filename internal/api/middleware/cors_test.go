package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCORS(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                string
		allowedOrigins      []string
		requestOrigin       string
		requestMethod       string
		expectedCode        int
		expectedAllowOrigin string
		expectedCredentials string
	}{
		{
			name:                "allows configured origin and sets CORS headers",
			allowedOrigins:      []string{"http://localhost:3000"},
			requestOrigin:       "http://localhost:3000",
			requestMethod:       http.MethodGet,
			expectedCode:        http.StatusOK,
			expectedAllowOrigin: "http://localhost:3000",
			expectedCredentials: "true",
		},
		{
			name:                "rejects unknown origin by omitting CORS headers",
			allowedOrigins:      []string{"http://localhost:3000"},
			requestOrigin:       "http://evil.com",
			requestMethod:       http.MethodGet,
			expectedCode:        http.StatusOK,
			expectedAllowOrigin: "",
			expectedCredentials: "",
		},
		{
			name:                "handles preflight OPTIONS request and returns 200",
			allowedOrigins:      []string{"http://localhost:3000"},
			requestOrigin:       "http://localhost:3000",
			requestMethod:       http.MethodOptions,
			expectedCode:        http.StatusOK,
			expectedAllowOrigin: "http://localhost:3000",
			expectedCredentials: "true",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			handler := CORS(tt.allowedOrigins)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
			}))

			req := httptest.NewRequest(tt.requestMethod, "/", nil)
			req.Header.Set("Origin", tt.requestOrigin)
			w := httptest.NewRecorder()

			handler.ServeHTTP(w, req)

			require.Equal(t, tt.expectedCode, w.Code, "should return expected HTTP status code")
			require.Equal(t, tt.expectedAllowOrigin, w.Header().Get("Access-Control-Allow-Origin"), "should set expected Access-Control-Allow-Origin header")
			if tt.expectedCredentials != "" {
				require.Equal(t, tt.expectedCredentials, w.Header().Get("Access-Control-Allow-Credentials"), "should set Access-Control-Allow-Credentials header")
			}
		})
	}
}
