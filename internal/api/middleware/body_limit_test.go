package middleware

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMaxBodySize(t *testing.T) {
	t.Parallel()

	const (
		tinyLimitBytes      int64 = 100
		streamingLimitBytes int64 = 50
	)

	tests := []struct {
		name         string
		maxSize      int64
		method       string
		body         func() *http.Request
		expectedCode int
		expectedBody string
	}{
		{
			name:    "allows request with body smaller than limit",
			maxSize: 1024,
			method:  http.MethodPost,
			body: func() *http.Request {
				body := bytes.NewReader([]byte("small body"))
				req := httptest.NewRequest(http.MethodPost, "/", body)
				req.Header.Set("Content-Length", "10")
				return req
			},
			expectedCode: http.StatusOK,
		},
		{
			name:    "rejects request with Content-Length exceeding limit",
			maxSize: tinyLimitBytes,
			method:  http.MethodPost,
			body: func() *http.Request {
				body := bytes.NewReader(make([]byte, 200))
				req := httptest.NewRequest(http.MethodPost, "/", body)
				req.ContentLength = 200
				return req
			},
			expectedCode: http.StatusRequestEntityTooLarge,
			expectedBody: `{"error":{"code":"PAYLOAD_TOO_LARGE","message":"Request body too large (max 100 bytes)"}}` + "\n",
		},
		{
			name:    "wraps body with MaxBytesReader to reject oversized streaming body",
			maxSize: streamingLimitBytes,
			method:  http.MethodPost,
			body: func() *http.Request {
				body := strings.NewReader(strings.Repeat("x", 100))
				req := httptest.NewRequest(http.MethodPost, "/", body)
				req.ContentLength = -1 // unknown content length
				return req
			},
			expectedCode: http.StatusRequestEntityTooLarge,
		},
		{
			name:    "defaults to allowing small body when limit is zero",
			maxSize: 0,
			method:  http.MethodPost,
			body: func() *http.Request {
				body := bytes.NewReader([]byte("hello"))
				req := httptest.NewRequest(http.MethodPost, "/", body)
				req.ContentLength = 5
				return req
			},
			expectedCode: http.StatusOK,
		},
		{
			name:    "allows GET requests without body enforcement",
			maxSize: 100,
			method:  http.MethodGet,
			body: func() *http.Request {
				return httptest.NewRequest(http.MethodGet, "/", nil)
			},
			expectedCode: http.StatusOK,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			handler := MaxBodySize(tt.maxSize)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				// For the streaming body test, we need to actually read the body
				// to trigger the MaxBytesReader limit
				if r.ContentLength == -1 && r.Body != nil {
					buf := make([]byte, 100)
					_, err := r.Body.Read(buf)
					if err != nil {
						http.Error(w, "body too large", http.StatusRequestEntityTooLarge)
						return
					}
				}
				w.WriteHeader(http.StatusOK)
			}))

			req := tt.body()
			w := httptest.NewRecorder()

			handler.ServeHTTP(w, req)
			require.Equal(t, tt.expectedCode, w.Code, "should return expected HTTP status code")
			if tt.expectedBody != "" {
				require.Equal(t, tt.expectedBody, w.Body.String(), "should return a capitalized message with the max body size")
			}
		})
	}
}

func TestMaxBodySizeForPaths(t *testing.T) {
	t.Parallel()

	const (
		normalAPIPath      = "/api/v1/sessions/manual"
		uploadAPIPath      = "/api/v1/uploads"
		uploadMaxBodyBytes = 11 * bytesPerMiB
	)

	tests := []struct {
		name         string
		path         string
		contentSize  int64
		expectedCode int
		expectedBody string
	}{
		{
			name:         "uses default limit for normal API paths",
			path:         normalAPIPath,
			contentSize:  DefaultMaxBodyBytes + 1,
			expectedCode: http.StatusRequestEntityTooLarge,
			expectedBody: `{"error":{"code":"PAYLOAD_TOO_LARGE","message":"Request body too large (max 1 MB)"}}` + "\n",
		},
		{
			name:         "allows upload path above the default limit",
			path:         uploadAPIPath,
			contentSize:  DefaultMaxBodyBytes + 1,
			expectedCode: http.StatusOK,
		},
		{
			name:         "rejects upload path above the upload limit",
			path:         uploadAPIPath,
			contentSize:  uploadMaxBodyBytes + 1,
			expectedCode: http.StatusRequestEntityTooLarge,
			expectedBody: `{"error":{"code":"PAYLOAD_TOO_LARGE","message":"Request body too large (max 11 MB)"}}` + "\n",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			handler := MaxBodySizeForPaths(DefaultMaxBodyBytes, map[string]int64{
				uploadAPIPath: uploadMaxBodyBytes,
			})(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(http.StatusOK)
			}))

			req := httptest.NewRequest(http.MethodPost, tt.path, bytes.NewReader(make([]byte, int(tt.contentSize))))
			req.ContentLength = tt.contentSize
			w := httptest.NewRecorder()

			handler.ServeHTTP(w, req)

			require.Equal(t, tt.expectedCode, w.Code, "should return expected HTTP status code")
			if tt.expectedBody != "" {
				require.Equal(t, tt.expectedBody, w.Body.String(), "should include the effective max body size in the error response")
			}
		})
	}
}
