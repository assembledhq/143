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

	"github.com/stretchr/testify/assert"
)

func computeHMAC(body, secret string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(body))
	return hex.EncodeToString(mac.Sum(nil))
}

func TestVerifyWebhookSignature_ValidSignature(t *testing.T) {
	secret := "test-secret-key"
	body := `{"action":"created","data":{"id":"123"}}`
	sig := computeHMAC(body, secret)

	handler := VerifyWebhookSignature("X-Webhook-Signature", secret, "")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Verify body was restored
		b, err := io.ReadAll(r.Body)
		assert.NoError(t, err)
		assert.Equal(t, body, string(b))
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewBufferString(body))
	req.Header.Set("X-Webhook-Signature", sig)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
}

func TestVerifyWebhookSignature_InvalidSignature(t *testing.T) {
	secret := "test-secret-key"
	body := `{"action":"created"}`

	handler := VerifyWebhookSignature("X-Webhook-Signature", secret, "")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewBufferString(body))
	req.Header.Set("X-Webhook-Signature", "invalid-signature")
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)
	assert.Equal(t, http.StatusUnauthorized, w.Code)
	assert.Contains(t, w.Body.String(), "invalid webhook signature")
}

func TestVerifyWebhookSignature_MissingSignature(t *testing.T) {
	handler := VerifyWebhookSignature("X-Webhook-Signature", "secret", "")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewBufferString(`{}`))
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)
	assert.Equal(t, http.StatusUnauthorized, w.Code)
	assert.Contains(t, w.Body.String(), "missing webhook signature")
}

func TestVerifyWebhookSignature_EmptySecret_SkipsVerification(t *testing.T) {
	handler := VerifyWebhookSignature("X-Webhook-Signature", "", "")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewBufferString(`{}`))
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
}

func TestVerifyWebhookSignature_StripPrefix(t *testing.T) {
	secret := "test-secret"
	body := `{"event":"issue.created"}`
	sig := computeHMAC(body, secret)

	handler := VerifyWebhookSignature("X-Hub-Signature-256", secret, "sha256=")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewBufferString(body))
	req.Header.Set("X-Hub-Signature-256", "sha256="+sig)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
}

func TestVerifyWebhookSignature_BodyRestoredAfterVerification(t *testing.T) {
	secret := "restore-test"
	body := `{"key":"value"}`
	sig := computeHMAC(body, secret)

	var bodyAfterMiddleware []byte
	handler := VerifyWebhookSignature("X-Sig", secret, "")(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		b, _ := io.ReadAll(r.Body)
		bodyAfterMiddleware = b
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodPost, "/", bytes.NewBufferString(body))
	req.Header.Set("X-Sig", sig)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, body, string(bodyAfterMiddleware))
}
