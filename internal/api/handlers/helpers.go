package handlers

import (
	"encoding/json"
	"net/http"

	"github.com/assembledhq/143/internal/models"
)

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, code, message string) {
	writeJSON(w, status, models.ErrorResponse{
		Error: models.ErrorDetail{Code: code, Message: message},
	})
}
