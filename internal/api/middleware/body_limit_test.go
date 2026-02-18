package middleware

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestMaxBodySize_AllowsSmallBody(t *testing.T) {
	handler := MaxBodySize(1024)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	body := bytes.NewReader([]byte("small body"))
	req := httptest.NewRequest(http.MethodPost, "/", body)
	req.Header.Set("Content-Length", "10")
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
}

func TestMaxBodySize_RejectsLargeContentLength(t *testing.T) {
	handler := MaxBodySize(100)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	body := bytes.NewReader(make([]byte, 200))
	req := httptest.NewRequest(http.MethodPost, "/", body)
	req.ContentLength = 200
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)
	assert.Equal(t, http.StatusRequestEntityTooLarge, w.Code)
	assert.Contains(t, w.Body.String(), "PAYLOAD_TOO_LARGE")
}

func TestMaxBodySize_WrapsBodyWithMaxBytesReader(t *testing.T) {
	handler := MaxBodySize(50)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		buf := make([]byte, 100)
		_, err := r.Body.Read(buf)
		if err != nil {
			http.Error(w, "body too large", http.StatusRequestEntityTooLarge)
			return
		}
		w.WriteHeader(http.StatusOK)
	}))

	// Body is larger than limit but Content-Length header is not set
	body := strings.NewReader(strings.Repeat("x", 100))
	req := httptest.NewRequest(http.MethodPost, "/", body)
	req.ContentLength = -1 // unknown content length
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)
	assert.Equal(t, http.StatusRequestEntityTooLarge, w.Code)
}

func TestMaxBodySize_DefaultsToOneMB(t *testing.T) {
	handler := MaxBodySize(0)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	body := bytes.NewReader([]byte("hello"))
	req := httptest.NewRequest(http.MethodPost, "/", body)
	req.ContentLength = 5
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
}

func TestMaxBodySize_AllowsGetRequests(t *testing.T) {
	handler := MaxBodySize(100)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
}
