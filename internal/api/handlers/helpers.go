package handlers

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/assembledhq/143/internal/models"
	"github.com/rs/zerolog"
)

func queryInt(r *http.Request, key string, defaultVal int) int {
	v := r.URL.Query().Get(key)
	if v == "" {
		return defaultVal
	}
	n, err := strconv.Atoi(v)
	if err != nil || n < 0 {
		return defaultVal
	}
	return n
}

func strPtr(s string) *string { return &s }

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	// Encode directly to the response writer. If encoding fails, the status
	// header is already sent so we can only log — http.Error would attempt a
	// second WriteHeader which is a no-op and prints a warning.
	_ = json.NewEncoder(w).Encode(v)
}

// writeError logs the error and writes a JSON error response. It logs at Error
// level for 5xx status codes and Info level for 4xx. If an error is provided
// via errs, it is attached to the log entry with .Err().
func writeError(w http.ResponseWriter, r *http.Request, status int, code, message string, errs ...error) {
	logger := zerolog.Ctx(r.Context()).With().Str("code", code).Int("status", status).Logger()
	var evt *zerolog.Event
	if status >= 500 {
		evt = logger.Error()
	} else {
		evt = logger.Info()
	}
	if len(errs) > 0 && errs[0] != nil {
		evt = evt.Err(errs[0])
	}
	evt.Msg(message)

	writeJSON(w, status, models.ErrorResponse{
		Error: models.ErrorDetail{Code: code, Message: message},
	})
}
