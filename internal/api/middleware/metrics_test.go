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

// TestStatusWriter_Unwrap is the regression guard for the production EOF that
// surfaced as 502 PREVIEW_WORKER_REQUEST_FAILED on long preview starts: when
// this wrapper does not implement Unwrap(), http.NewResponseController cannot
// see the underlying conn, so handlers calling SetWriteDeadline silently
// no-op and the server's 15s WriteTimeout drops the connection mid-handler.
// The same wrapper sits in the middleware chain in front of every preview
// route, so this test pins the contract.
func TestStatusWriter_Unwrap(t *testing.T) {
	t.Parallel()
	inner := httptest.NewRecorder()
	sw := &statusWriter{ResponseWriter: inner, status: http.StatusOK}
	unwrapper, ok := any(sw).(interface{ Unwrap() http.ResponseWriter })
	require.True(t, ok, "statusWriter must implement Unwrap so http.NewResponseController can reach the underlying conn")
	require.Equal(t, http.ResponseWriter(inner), unwrapper.Unwrap(), "Unwrap must return the wrapped ResponseWriter")
}
