package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestMetrics_RecordsRequest(t *testing.T) {
	handler := Metrics(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/issues", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)
	assert.Equal(t, http.StatusOK, w.Code)
}

func TestMetrics_RecordsStatusCode(t *testing.T) {
	handler := Metrics(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))

	req := httptest.NewRequest(http.MethodGet, "/api/v1/issues/123", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)
	assert.Equal(t, http.StatusNotFound, w.Code)
}

func TestStatusWriter_DefaultsTo200(t *testing.T) {
	w := httptest.NewRecorder()
	sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}

	_, err := sw.Write([]byte("hello"))
	assert.NoError(t, err)
	assert.Equal(t, http.StatusOK, sw.status)
	assert.True(t, sw.written)
}

func TestStatusWriter_CapturesWriteHeader(t *testing.T) {
	w := httptest.NewRecorder()
	sw := &statusWriter{ResponseWriter: w, status: http.StatusOK}

	sw.WriteHeader(http.StatusCreated)
	assert.Equal(t, http.StatusCreated, sw.status)
	assert.True(t, sw.written)
}
