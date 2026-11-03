package providers

import (
	"errors"
	"testing"

	"github.com/assembledhq/143/internal/services/agent"
	"github.com/stretchr/testify/require"
)

func TestParseNPMSetupFailure(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, raw string
		terminal  bool
	}{
		{"npm eleven lock rejection", `{"code":"EUSAGE","category":"permanent","node_version":"v22.14.0","npm_version":"11.1.0","phase_durations_ms":{"install":123}}`, true},
		{"engine mismatch", `{"code":"EBADENGINE","category":"permanent"}`, true},
		{"transient", `{"code":"ECONNRESET","category":"permanent"}`, false},
		{"unknown secret", `npm error secret-private-token https://private.example/package`, false},
		{"unsafe version", `{"code":"EUSAGE","category":"permanent","npm_version":"secret-private-token"}`, false},
		{"unsafe phase", `{"code":"EUSAGE","category":"permanent","phase_durations_ms":{"secret-private-token":1}}`, false},
		{"unbounded time", `{"code":"EUSAGE","category":"permanent","phase_durations_ms":{"install":600001}}`, false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := parseNPMSetupFailure([]byte(tt.raw))
			require.Equal(t, tt.terminal, agent.IsTerminalNPMSetupError(err), "only known permanent sanitized diagnostics should terminate retries")
			require.NotContains(t, err.Error(), "secret-private-token", "error text must exclude private diagnostics")
			joined := errors.Join(err, errors.New("isolated npm setup cleanup failed"))
			require.Equal(t, tt.terminal, agent.IsTerminalNPMSetupError(joined), "classification should survive joined cleanup failure")
		})
	}
}
