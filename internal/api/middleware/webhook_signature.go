package middleware

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"strings"

	"github.com/rs/zerolog"
)

// VerifyWebhookSignature verifies HMAC-SHA256 signatures on inbound webhooks.
// headerName is the HTTP header containing the signature (e.g., "X-Sentry-Hook-Signature", "X-Linear-Signature").
// secret is the shared HMAC key.
// prefixToStrip is an optional prefix to strip from the header value (e.g., "sha256=").
func VerifyWebhookSignature(headerName, secret, prefixToStrip string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if secret == "" {
				// No secret configured — skip verification (dev mode)
				next.ServeHTTP(w, r)
				return
			}

			signature := r.Header.Get(headerName)
			if signature == "" {
				zerolog.Ctx(r.Context()).Warn().Str("header", headerName).Msg("missing webhook signature")
				http.Error(w, `{"error":{"code":"UNAUTHORIZED","message":"missing webhook signature"}}`, http.StatusUnauthorized)
				return
			}

			if prefixToStrip != "" {
				signature = strings.TrimPrefix(signature, prefixToStrip)
			}

			// Read the body for verification, then restore it
			body, err := io.ReadAll(r.Body)
			if err != nil {
				zerolog.Ctx(r.Context()).Warn().Err(err).Msg("failed to read webhook request body")
				http.Error(w, `{"error":{"code":"BAD_REQUEST","message":"failed to read request body"}}`, http.StatusBadRequest)
				return
			}
			r.Body = io.NopCloser(bytes.NewReader(body))

			// Compute HMAC-SHA256
			mac := hmac.New(sha256.New, []byte(secret))
			mac.Write(body)
			expectedMAC := hex.EncodeToString(mac.Sum(nil))

			if !hmac.Equal([]byte(signature), []byte(expectedMAC)) {
				zerolog.Ctx(r.Context()).Warn().Str("header", headerName).Msg("invalid webhook signature")
				http.Error(w, `{"error":{"code":"UNAUTHORIZED","message":"invalid webhook signature"}}`, http.StatusUnauthorized)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}
