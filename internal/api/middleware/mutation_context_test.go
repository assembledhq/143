package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/assembledhq/143/internal/requestctx"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestMutationContext(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		header   string
		expected uuid.UUID
	}{
		{name: "valid mutation id is propagated", header: "11111111-1111-1111-1111-111111111111", expected: uuid.MustParse("11111111-1111-1111-1111-111111111111")},
		{name: "invalid mutation id is ignored", header: "invalid", expected: uuid.Nil},
		{name: "missing mutation id is ignored", expected: uuid.Nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var actual uuid.UUID
			handler := MutationContext(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
				actual = requestctx.MutationID(r.Context())
			}))
			req := httptest.NewRequest(http.MethodPost, "/mutation", nil)
			if tt.header != "" {
				req.Header.Set(ClientMutationIDHeader, tt.header)
			}
			handler.ServeHTTP(httptest.NewRecorder(), req)
			require.Equal(t, tt.expected, actual, "middleware should expose only a valid client mutation id")
		})
	}
}
