package agent

import "testing"

func TestIsTokenExpiredError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		errMsg   string
		expected bool
	}{
		{"empty string", "", false},
		{"unrelated error", "codex CLI exited with code 1: syntax error", false},
		{"token_expired keyword", "codex CLI exited with code 1: auth error code: token_expired", true},
		{"token is expired", "Provided authentication token is expired. Please try signing in again.", true},
		{"full error message", "failed to refresh available models: unexpected status 401 Unauthorized: Provided authentication token is expired", true},
		{"partial match", "something token_expired something", true},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := isTokenExpiredError(tc.errMsg)
			if got != tc.expected {
				t.Errorf("isTokenExpiredError(%q) = %v, want %v", tc.errMsg, got, tc.expected)
			}
		})
	}
}
