package middleware

import (
	"net/http"

	"github.com/assembledhq/143/internal/requestctx"
	"github.com/google/uuid"
)

const ClientMutationIDHeader = "X-Client-Mutation-ID"

// MutationContext carries a valid client mutation ID through every database
// acquisition made for the request. Invalid IDs are ignored so an optional
// observability header can never make an otherwise valid mutation fail.
func MutationContext(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mutationID, err := uuid.Parse(r.Header.Get(ClientMutationIDHeader))
		if err == nil {
			r = r.WithContext(requestctx.WithMutationID(r.Context(), mutationID))
		}
		next.ServeHTTP(w, r)
	})
}
