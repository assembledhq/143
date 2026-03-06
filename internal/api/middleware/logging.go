package middleware

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/assembledhq/143/internal/models"
	chiMiddleware "github.com/go-chi/chi/v5/middleware"
	"github.com/rs/zerolog"
)

type responseWriter struct {
	http.ResponseWriter
	status        int
	errorResponse *models.ErrorResponse
}

func (rw *responseWriter) WriteHeader(code int) {
	rw.status = code
	rw.ResponseWriter.WriteHeader(code)
}

func (rw *responseWriter) Write(b []byte) (int, error) {
	rw.captureErrorDetails(b)
	return rw.ResponseWriter.Write(b)
}

func (rw *responseWriter) Flush() {
	if f, ok := rw.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

func (rw *responseWriter) captureErrorDetails(responseBody []byte) {
	if rw.status < http.StatusInternalServerError {
		return
	}

	var response models.ErrorResponse
	if err := json.Unmarshal(responseBody, &response); err != nil {
		return
	}

	rw.errorResponse = &response
}

func Logging(logger zerolog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			rw := &responseWriter{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(rw, r)

			logEvent := logger.Info()
			if rw.status >= http.StatusInternalServerError {
				logEvent = logger.Error()
			}

			logEvent.
				Str("method", r.Method).
				Str("path", r.URL.Path).
				Str("request_id", chiMiddleware.GetReqID(r.Context())).
				Int("status", rw.status).
				Dur("duration", time.Since(start))

			if rw.status >= http.StatusInternalServerError {
				if rw.errorResponse != nil && rw.errorResponse.Error.Code != "" {
					logEvent = logEvent.Str("error_code", rw.errorResponse.Error.Code)
				}
				if rw.errorResponse != nil && rw.errorResponse.Error.Message != "" {
					logEvent = logEvent.Str("error_message", rw.errorResponse.Error.Message)
				}
				logEvent.Msg("request failed")
				return
			}

			logEvent.Msg("request")
		})
	}
}
