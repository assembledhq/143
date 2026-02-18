package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func TestLogging(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		handler      http.HandlerFunc
		expectedCode int
		expectedBody string
	}{
		{
			name: "logs request and calls next handler",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
			},
			expectedCode: http.StatusOK,
			expectedBody: "",
		},
		{
			name: "captures non-200 status code",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusNotFound)
			},
			expectedCode: http.StatusNotFound,
			expectedBody: "",
		},
		{
			name: "defaults to 200 when WriteHeader is not called",
			handler: func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte("hello"))
			},
			expectedCode: http.StatusOK,
			expectedBody: "hello",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			logger := zerolog.Nop()
			handler := Logging(logger)(tt.handler)

			req := httptest.NewRequest(http.MethodGet, "/test-path", nil)
			w := httptest.NewRecorder()

			handler.ServeHTTP(w, req)

			require.Equal(t, tt.expectedCode, w.Code, "should return expected HTTP status code")
			if tt.expectedBody != "" {
				require.Equal(t, tt.expectedBody, w.Body.String(), "should return expected response body")
			}
		})
	}
}
