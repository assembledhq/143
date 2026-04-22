package middleware

import (
	"encoding/json"
	"net/http"
	"runtime/debug"

	chiMiddleware "github.com/go-chi/chi/v5/middleware"
	"github.com/rs/zerolog"

	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/observability"
)

func Recoverer(logger zerolog.Logger, reporter observability.Reporter) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			defer func() {
				if recovered := recover(); recovered != nil {
					stack := debug.Stack()
					if rw, ok := w.(*responseWriter); ok {
						rw.skipErrorCapture = true
					}
					if reporter != nil {
						reporter.CaptureRecoveredPanic(r, recovered, stack)
					}
					zerolog.Ctx(r.Context()).Error().
						Interface("panic", recovered).
						Bytes("stack", stack).
						Str("request_id", chiMiddleware.GetReqID(r.Context())).
						Msg("request panic recovered")
					writeRecoveryError(w)
				}
			}()

			next.ServeHTTP(w, r)
		})
	}
}

func writeRecoveryError(w http.ResponseWriter) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusInternalServerError)
	_ = json.NewEncoder(w).Encode(models.ErrorResponse{
		Error: models.ErrorDetail{
			Code:    "INTERNAL_SERVER_ERROR",
			Message: "internal server error",
		},
	})
}
