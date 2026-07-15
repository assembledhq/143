package mcp

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestToolContentJSON(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		content  ToolContent
		expected string
	}{
		{
			name:     "text omits image fields",
			content:  ToolContent{Type: "text", Text: "ready"},
			expected: `{"type":"text","text":"ready"}`,
		},
		{
			name:     "image uses MCP field names",
			content:  ImageContent("iVBORw0KGgo=", "image/png"),
			expected: `{"type":"image","data":"iVBORw0KGgo=","mimeType":"image/png"}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			actual, err := json.Marshal(tt.content)
			require.NoError(t, err, "tool content should serialize")
			require.JSONEq(t, tt.expected, string(actual), "tool content should match the MCP wire contract")
		})
	}
}
