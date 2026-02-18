package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMetrics(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		handler      http.HandlerFunc
		path         string
		expectedCode int
	}{
		{
			name: "records request and passes through to next handler",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
			},
			path:         "/api/v1/issues",
			expectedCode: http.StatusOK,
		},
		{
			name: "records non-200 status code from next handler",
			handler: func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusNotFound)
			},
			path:         "/api/v1/issues/123",
			expectedCode: http.StatusNotFound,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			handler := Metrics(tt.handler)
			req := httptest.NewRequest(http.MethodGet, tt.path, nil)
			w := httptest.NewRecorder()

			handler.ServeHTTP(w, req)
			require.Equal(t, tt.expectedCode, w.Code, "should return expected HTTP status code")
		})
	}
}

func TestStatusWriter(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		action          func(sw *statusWriter)
		expectedStatus  int
		expectedWritten bool
	}{
		{
			name: "defaults to 200 and marks written on Write call",
			action: func(sw *statusWriter) {
				_, _ = sw.Write([]byte("hello"))
			},
			expectedStatus:  http.StatusOK,
			expectedWritten: true,
		},
		{
			name: "captures explicit WriteHeader status code",
			action: func(sw *statusWriter) {
				sw.WriteHeader(http.StatusCreated)
			},
			expectedStatus:  http.StatusCreated,
			expectedWritten: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			w := httptest.NewRecorder()
			sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}

			tt.action(sw)

			require.Equal(t, tt.expectedStatus, sw.status, "should capture expected status code")
			require.Equal(t, tt.expectedWritten, sw.written, "should mark written flag correctly")
		})
	}
}
