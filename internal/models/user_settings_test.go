package models

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestParseUserSettings(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		raw     json.RawMessage
		want    UserSettings
		wantErr bool
	}{
		{
			name: "parses supported agent defaults",
			raw:  json.RawMessage(`{"coding_agent_reasoning_defaults":{"codex":"xhigh","claude_code":"max"}}`),
			want: UserSettings{
				CodingAgentReasoningDefaults: map[AgentType]ReasoningEffort{
					AgentTypeCodex:      ReasoningEffortXHigh,
					AgentTypeClaudeCode: ReasoningEffortMax,
				},
			},
		},
		{
			name:    "rejects unsupported effort for agent",
			raw:     json.RawMessage(`{"coding_agent_reasoning_defaults":{"codex":"max"}}`),
			wantErr: true,
		},
		{
			name:    "rejects unsupported agent type",
			raw:     json.RawMessage(`{"coding_agent_reasoning_defaults":{"gemini_cli":"high"}}`),
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := ParseUserSettings(tt.raw)
			if tt.wantErr {
				require.Error(t, err, "ParseUserSettings should reject invalid settings")
				return
			}
			require.NoError(t, err, "ParseUserSettings should accept valid settings")
			require.Equal(t, tt.want, got, "ParseUserSettings should return the expected settings")
		})
	}
}
