package llm

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestClassifyHTTPError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		statusCode int
		body       string
		wantErr    error
	}{
		{"rate limit 429", 429, "too many requests", ErrRateLimit},
		{"unauthorized 401", 401, "invalid key", ErrAuthError},
		{"forbidden 403", 403, "forbidden", ErrAuthError},
		{"server error 500", 500, "internal error", ErrServerError},
		{"server error 502", 502, "bad gateway", ErrServerError},
		{"server error 503", 503, "unavailable", ErrServerError},
		{"overloaded 529", 529, "overloaded", ErrServerError},
		{"bad request 400", 400, "malformed", ErrBadRequest},
		{"unknown 418", 418, "teapot", nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := classifyHTTPError(tt.statusCode, tt.body)
			require.Error(t, err, "classifyHTTPError should always return an error")
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr, "error should wrap the expected sentinel")
			}
			require.Contains(t, err.Error(), tt.body, "error message should contain the body")
		})
	}
}

func TestTruncate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		input  string
		maxLen int
		want   string
	}{
		{"short string unchanged", "hello", 10, "hello"},
		{"exact length unchanged", "hello", 5, "hello"},
		{"long string truncated", "hello world", 5, "hello..."},
		{"empty string", "", 5, ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got := truncate(tt.input, tt.maxLen)
			require.Equal(t, tt.want, got, "truncate should return expected result")
		})
	}
}
