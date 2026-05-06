package middleware

import (
	"encoding/json"
	"net/http"
	"strconv"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/observability"
	"github.com/go-chi/chi/v5"
	chiMiddleware "github.com/go-chi/chi/v5/middleware"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
)

type responseWriter struct {
	http.ResponseWriter
	status           int
	errorResponse    *models.ErrorResponse
	skipErrorCapture bool
	orgID            uuid.UUID
	userID           uuid.UUID
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

// Unwrap exposes the underlying ResponseWriter so http.NewResponseController
// can reach the raw net.Conn for SetWriteDeadline / SetReadDeadline. Without
// this, clearWriteDeadline silently fails (Go's controller cannot punch
// through a wrapper that doesn't implement Unwrap), and the server's 15s
// WriteTimeout still drops long-running responses — preview start in
// particular, where snapshot restore + readiness probes routinely run for
// 60-100s. The downstream symptom is a 502 EOF at the API edge with the real
// error code (PREVIEW_SERVICE_NOT_READY etc.) lost.
func (rw *responseWriter) Unwrap() http.ResponseWriter {
	return rw.ResponseWriter
}

func (rw *responseWriter) SetResolvedIdentity(orgID, userID uuid.UUID) {
	rw.orgID = orgID
	rw.userID = userID
}

func (rw *responseWriter) captureErrorDetails(responseBody []byte) {
	if rw.status < http.StatusBadRequest {
		return
	}

	var response models.ErrorResponse
	if err := json.Unmarshal(responseBody, &response); err != nil {
		return
	}

	rw.errorResponse = &response
}

func Logging(logger zerolog.Logger, reporter observability.Reporter) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			start := time.Now()
			rw := &responseWriter{ResponseWriter: w, status: http.StatusOK}
			next.ServeHTTP(rw, r)
			duration := time.Since(start)

			logEvent := logger.Info()
			if rw.status >= http.StatusInternalServerError {
				logEvent = logger.Error()
			} else if rw.status >= http.StatusBadRequest {
				logEvent = logger.Warn()
			}

			logEvent.
				Str("method", r.Method).
				Str("path", r.URL.Path).
				Str("request_id", chiMiddleware.GetReqID(r.Context())).
				Int("status", rw.status).
				Str("status_class", strconv.Itoa(rw.status/100)+"xx").
				Dur("duration", duration).
				Float64("duration_ms", observability.DurationMillis(duration))

			if rw.status >= http.StatusBadRequest {
				if rw.errorResponse != nil && rw.errorResponse.Error.Code != "" {
					logEvent = logEvent.Str("error_code", rw.errorResponse.Error.Code)
				}
				if rw.errorResponse != nil && rw.errorResponse.Error.Message != "" {
					logEvent = logEvent.Str("error_message", rw.errorResponse.Error.Message)
				}
				logEvent.Msg("request failed")
				if reporter != nil && rw.status >= http.StatusInternalServerError && !rw.skipErrorCapture {
					reporter.CaptureRequestError(r, buildRequestErrorEvent(r, rw))
				}
				return
			}

			logEvent.Msg("request")
		})
	}
}

func buildRequestErrorEvent(r *http.Request, rw *responseWriter) observability.RequestErrorEvent {
	event := observability.RequestErrorEvent{
		Method:    r.Method,
		Path:      r.URL.Path,
		Route:     routePattern(r),
		RequestID: chiMiddleware.GetReqID(r.Context()),
		Status:    rw.status,
	}
	if rw.errorResponse != nil {
		event.ErrorCode = rw.errorResponse.Error.Code
		event.ErrorMessage = rw.errorResponse.Error.Message
	}
	if orgID := OrgIDFromContext(r.Context()); orgID != uuid.Nil {
		event.OrgID = orgID.String()
	} else if rw.orgID != uuid.Nil {
		event.OrgID = rw.orgID.String()
	}
	if user := UserFromContext(r.Context()); user != nil {
		event.UserID = user.ID.String()
	} else if rw.userID != uuid.Nil {
		event.UserID = rw.userID.String()
	}
	return event
}

func routePattern(r *http.Request) string {
	routeCtx := chi.RouteContext(r.Context())
	if routeCtx == nil {
		return r.URL.Path
	}
	if pattern := routeCtx.RoutePattern(); pattern != "" {
		return pattern
	}
	return r.URL.Path
}
