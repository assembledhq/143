package middleware

import (
	"context"
	"net/http"
	"strconv"
	"sync/atomic"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/assembledhq/143/internal/metrics"
)

// httpMetricsPtr is the package-level metrics instance, set via SetHTTPMetrics.
// Uses atomic.Pointer for goroutine-safe reads without locks.
var httpMetricsPtr atomic.Pointer[metrics.HTTPMetrics]

// SetHTTPMetrics injects the OTel HTTP metrics instance. Call once at startup
// after telemetry.InitMeterProvider.
func SetHTTPMetrics(m *metrics.HTTPMetrics) {
	httpMetricsPtr.Store(m)
}

// Metrics returns middleware that records OTel metrics for HTTP requests.
func Metrics(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		m := httpMetricsPtr.Load()
		if m == nil {
			next.ServeHTTP(w, r)
			return
		}

		start := time.Now()
		// Use context.Background() for gauge adjustments so they succeed
		// even if the client disconnects and r.Context() is cancelled.
		m.RequestsInFlight.Add(context.Background(), 1)

		ww := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(ww, r)

		duration := time.Since(start).Seconds()
		m.RequestsInFlight.Add(context.Background(), -1)

		// Use the chi route pattern for consistent path labels.
		routePattern := chi.RouteContext(r.Context()).RoutePattern()
		if routePattern == "" {
			routePattern = r.URL.Path
		}

		m.RecordRequest(r.Context(), r.Method, routePattern, strconv.Itoa(ww.status), duration)
	})
}

type statusWriter struct {
	http.ResponseWriter
	status  int
	written bool
}

func (w *statusWriter) WriteHeader(status int) {
	if !w.written {
		w.status = status
		w.written = true
	}
	w.ResponseWriter.WriteHeader(status)
}

func (w *statusWriter) Write(b []byte) (int, error) {
	if !w.written {
		w.written = true
	}
	return w.ResponseWriter.Write(b)
}

func (w *statusWriter) Flush() {
	if f, ok := w.ResponseWriter.(http.Flusher); ok {
		f.Flush()
	}
}

// Unwrap exposes the underlying ResponseWriter to http.NewResponseController.
// See logging.responseWriter.Unwrap for the rationale: without it, handlers
// calling SetWriteDeadline (e.g. clearWriteDeadline for preview start) silently
// no-op when this middleware sits between the chi router and the handler.
func (w *statusWriter) Unwrap() http.ResponseWriter {
	return w.ResponseWriter
}
