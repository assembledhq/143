package prompts

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAgentPRHandoff(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		data   AgentPRHandoffData
		wanted []string
	}{
		{
			name: "automatic handoff and review enabled",
			data: AgentPRHandoffData{AutomaticHandoff: true, ReviewBeforePR: true, ReviewMaxPasses: 2},
			wanted: []string{
				"143-tools pr create",
				"Automatic PR handoff: on",
				"Pre-PR review/fix: on, up to 2 passes",
			},
		},
		{
			name: "automatic handoff and review disabled",
			data: AgentPRHandoffData{},
			wanted: []string{
				"leave verified changes for manual publication",
				"trigger_kind=explicit_action",
				"Pre-PR review/fix: off",
				"A queued PR or started review is not a created",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := AgentPRHandoff(tt.data)
			for _, wanted := range tt.wanted {
				require.Contains(t, got, wanted, "handoff prompt should include the required policy and safety language")
			}
		})
	}
}
