package middleware

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func computeHMAC(body, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(body))
	return hex.EncodeToString(mac.Sum(nil))
}

func TestVerifyWebhookSignature(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		headerName   string
		secret       string
		prefix       string
		body         string
		signature    string
		expectedCode int
		checkBody    bool
	}{
		{
			name:         "allows request with valid HMAC signature and restores body",
			headerName:   "X-Webhook-Signature",
			secret:       "test-secret-key",
			prefix:       "",
			body:         `{"action":"created","data":{"id":"123"}}`,
			signature:    computeHMAC(`{"action":"created","data":{"id":"123"}}`, "test-secret-key"),
			expectedCode: http.StatusOK,
			checkBody:    true,
		},
		{
			name:         "rejects request with invalid signature",
			headerName:   "X-Webhook-Signature",
			secret:       "test-secret-key",
			prefix:       "",
			body:         `{"action":"created"}`,
			signature:    "invalid-signature",
			expectedCode: http.StatusUnauthorized,
			checkBody:    false,
		},
		{
			name:         "rejects request with missing signature header",
			headerName:   "X-Webhook-Signature",
			secret:       "secret",
			prefix:       "",
			body:         `{}`,
			signature:    "",
			expectedCode: http.StatusUnauthorized,
			checkBody:    false,
		},
		{
			name:         "skips verification when secret is empty",
			headerName:   "X-Webhook-Signature",
			secret:       "",
			prefix:       "",
			body:         `{}`,
			signature:    "",
			expectedCode: http.StatusOK,
			checkBody:    false,
		},
		{
			name:         "strips prefix from signature header before verification",
			headerName:   "X-Hub-Signature-256",
			secret:       "test-secret",
			prefix:       "sha256=",
			body:         `{"event":"issue.created"}`,
			signature:    "sha256=" + computeHMAC(`{"event":"issue.created"}`, "test-secret"),
			expectedCode: http.StatusOK,
			checkBody:    false,
		},
		{
			name:         "restores request body after successful verification",
			headerName:   "X-Sig",
			secret:       "restore-test",
			prefix:       "",
			body:         `{"key":"value"}`,
			signature:    computeHMAC(`{"key":"value"}`, "restore-test"),
			expectedCode: http.StatusOK,
			checkBody:    true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			var bodyAfterMiddleware []byte
			handler := VerifyWebhookSignature(tt.headerName, tt.secret, tt.prefix)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if tt.checkBody {
					b, err := io.ReadAll(r.Body)
					require.NoError(t, err, "should read body without error after middleware")
					bodyAfterMiddleware = b
				}
				w.WriteHeader(http.StatusOK)
			}))

			req := httptest.NewRequest(http.MethodPost, "/", bytes.NewBufferString(tt.body))
			if tt.signature != "" {
				req.Header.Set(tt.headerName, tt.signature)
			}
			w := httptest.NewRecorder()

			handler.ServeHTTP(w, req)

			require.Equal(t, tt.expectedCode, w.Code, "should return expected HTTP status code")
			if tt.checkBody && tt.expectedCode == http.StatusOK {
				require.Equal(t, tt.body, string(bodyAfterMiddleware), "should restore request body after signature verification")
			}
		})
	}
}
