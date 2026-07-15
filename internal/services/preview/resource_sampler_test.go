package preview

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParsePreviewProcessSnapshot(t *testing.T) {
	t.Parallel()

	raw := parsePreviewProcessSnapshot("123 1 4096 go go build ./cmd/web\n456 1 2048 node node server.js\n")

	var processes []previewProcessSample
	require.NoError(t, json.Unmarshal(raw, &processes), "process snapshot should produce JSON")
	require.Equal(t, []previewProcessSample{
		{PID: 123, PPID: 1, RSSKiB: 4096, Command: "go", Args: "go build ./cmd/web"},
		{PID: 456, PPID: 1, RSSKiB: 2048, Command: "node", Args: "node server.js"},
	}, processes, "process snapshot should parse pid, memory, command, and args")
}
