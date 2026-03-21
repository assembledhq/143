package middleware

import (
	"encoding/json"
	"net/http"

	"github.com/rs/zerolog/log"
)

type errorBody struct {
	Error errorDetail `json:"error"`
}

type errorDetail struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// writeError writes a JSON error response. Uses the global logger for encode
// failures because this runs in auth/CSRF middleware before the request-scoped
// logger is available.
func writeError(w http.ResponseWriter, status int, code, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	if err := json.NewEncoder(w).Encode(errorBody{Error: errorDetail{Code: code, Message: message}}); err != nil {
		log.Warn().Err(err).Str("code", code).Msg("failed to encode error response")
	}
}
