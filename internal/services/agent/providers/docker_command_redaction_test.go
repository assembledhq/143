package providers

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRedactSandboxCommandAuthenticatedGitURLs(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		command  string
		expected string
	}{
		{name: "clone", command: "git clone 'https://x-access-token:secret-token@github.com/test/repo.git'", expected: "git clone 'https://[redacted]@github.com/test/repo.git'"},
		{name: "nested preparation payload", command: `python3 -c 'program' '{"fetch":"git fetch https://x-access-token:secret-token@github.com/repo.git"}'`, expected: `python3 -c 'program' '{"fetch":"git fetch https://[redacted]@github.com/repo.git"}'`},
		{name: "plain URL", command: "git fetch https://github.com/test/repo.git", expected: "git fetch https://github.com/test/repo.git"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.expected, redactSandboxCommandForLog(tt.command), "command logs should retain repository context while excluding URL credentials")
		})
	}
}
