package middleware

import (
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"

	"github.com/assembledhq/143/internal/metrics"
)

// httpMetrics is the package-level metrics instance, set via SetHTTPMetrics.
// When nil (e.g. in tests), the middleware is a no-op pass-through.
var httpMetrics *metrics.HTTPMetrics

// SetHTTPMetrics injects the OTel HTTP metrics instance. Call once at startup
// after telemetry.InitMeterProvider.
func SetHTTPMetrics(m *metrics.HTTPMetrics) {
	httpMetrics = m
}

// Metrics returns middleware that records OTel metrics for HTTP requests.
func Metrics(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if httpMetrics == nil {
			next.ServeHTTP(w, r)
			return
		}

		start := time.Now()
		httpMetrics.RequestsInFlight.Add(r.Context(), 1)

		ww := &statusWriter{ResponseWriter: w, status: http.StatusOK}
		next.ServeHTTP(ww, r)

		duration := time.Since(start).Seconds()
		httpMetrics.RequestsInFlight.Add(r.Context(), -1)

		// Use the chi route pattern for consistent path labels.
		routePattern := chi.RouteContext(r.Context()).RoutePattern()
		if routePattern == "" {
			routePattern = r.URL.Path
		}

		httpMetrics.RecordRequest(r.Context(), r.Method, routePattern, strconv.Itoa(ww.status), duration)
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
