package middleware

import (
	"encoding/json"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestWriteError(t *testing.T) {
	t.Parallel()

	rr := httptest.NewRecorder()
	writeError(rr, 422, "VALIDATION_ERROR", "field is required")

	require.Equal(t, 422, rr.Code)
	require.Equal(t, "application/json", rr.Header().Get("Content-Type"))

	var body errorBody
	err := json.NewDecoder(rr.Body).Decode(&body)
	require.NoError(t, err)
	require.Equal(t, "VALIDATION_ERROR", body.Error.Code)
	require.Equal(t, "field is required", body.Error.Message)
}
