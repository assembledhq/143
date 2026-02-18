package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
)

func TestLogging_LogsRequest(t *testing.T) {
	logger := zerolog.Nop()

	nextCalled := false
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		nextCalled = true
		w.WriteHeader(http.StatusOK)
	})

	handler := Logging(logger)(next)
	req := httptest.NewRequest(http.MethodGet, "/test-path", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	assert.True(t, nextCalled)
	assert.Equal(t, http.StatusOK, w.Code)
}

func TestLogging_CapturesStatusCode(t *testing.T) {
	logger := zerolog.Nop()

	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})

	handler := Logging(logger)(next)
	req := httptest.NewRequest(http.MethodGet, "/missing", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	// The responseWriter wrapper should have captured the 404 status code.
	// The httptest.ResponseRecorder also records the status.
	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestLogging_DefaultStatus200(t *testing.T) {
	logger := zerolog.Nop()

	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Do not call WriteHeader; the default should be 200.
		w.Write([]byte("hello"))
	})

	handler := Logging(logger)(next)
	req := httptest.NewRequest(http.MethodGet, "/default", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Equal(t, "hello", w.Body.String())
}
