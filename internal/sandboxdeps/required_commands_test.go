package sandboxdeps

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestRequiredCommandsFailClosedAndVerifyPins(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		deps    map[string]string
		wantErr bool
	}{
		{name: "supported exact pin", deps: map[string]string{"golangci-lint": "2.10.1"}},
		{name: "unknown tool", deps: map[string]string{"unsupported": "1.0.0"}, wantErr: true},
		{name: "invalid pin", deps: map[string]string{"golangci-lint": "latest"}, wantErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			// Existing Apply tests replace the shared registry under this mutex.
			dependencyRegistryMu.Lock()
			defer dependencyRegistryMu.Unlock()
			install, verify, err := RequiredCommands(tt.deps)
			if tt.wantErr {
				require.Error(t, err, "review setup must reject an unavailable or invalid required dependency")
				require.Nil(t, install, "failed review recipes must not publish partial installation commands")
				require.Nil(t, verify, "failed review recipes must not publish successful verification commands")
				return
			}
			require.NoError(t, err, "supported pinned review tooling should provide strict setup and verification")
			require.Equal(t, 1, len(install), "one declared tool should generate one required installation recipe")
			require.Contains(t, install[0], "golangci-lint-2.10.1", "required install should preserve the exact declared version")
			require.Equal(t, []string{`test -x "$HOME/.local/bin/golangci-lint" && "$HOME/.local/bin/golangci-lint" --version | grep -F 'version 2.10.1 ' >/dev/null`}, verify, "reuse should verify the actual executable version before trusting its receipt")
		})
	}
}
