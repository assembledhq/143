package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func TestRecoverer_CapturesPanicsAndReturns500(t *testing.T) {
	t.Parallel()

	reporter := &capturingReporter{}
	handler := Logging(zerolog.Nop(), reporter)(
		Recoverer(zerolog.Nop(), reporter)(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			panic("boom")
		})),
	)

	req := httptest.NewRequest(http.MethodGet, "/api/v1/panic", nil)
	w := httptest.NewRecorder()

	handler.ServeHTTP(w, req)

	require.Equal(t, http.StatusInternalServerError, w.Code, "recoverer should translate panics into 500 responses")
	require.Len(t, reporter.panicEvents, 1, "recoverer should capture the panic exactly once")
	require.Equal(t, "boom", reporter.panicEvents[0].recovered, "recoverer should report the recovered panic value")
	require.NotEmpty(t, reporter.panicEvents[0].stack, "recoverer should include a stack trace")
	require.Empty(t, reporter.requestErrors, "panic recovery should not also emit a duplicate request-error capture")
}
