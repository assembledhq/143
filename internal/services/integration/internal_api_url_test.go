package integration

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestInternalIntegrationConstructorsNormalizeBaseURL(t *testing.T) {
	t.Parallel()
	const origin = "https://143.dev"
	const expected = "https://143.dev/api/v1/internal"
	tests := []struct {
		name    string
		baseURL func(string) string
	}{
		{name: "issue creator", baseURL: func(raw string) string { return NewInternalIssueCreator("token", raw).baseURL }},
		{name: "pull request creator", baseURL: func(raw string) string { return NewInternalPullRequestCreator("token", raw).baseURL }},
		{name: "slack sender", baseURL: func(raw string) string { return NewInternalSlackMessageSender("token", raw).baseURL }},
		{name: "automation manager", baseURL: func(raw string) string { return NewInternalAutomationManager("token", raw).baseURL }},
		{name: "session tabs", baseURL: func(raw string) string { return NewInternalSessionTabManager("token", raw).baseURL }},
		{name: "project proposer", baseURL: func(raw string) string { return NewInternalProjectProposer("token", raw).baseURL }},
		{name: "eval reporter", baseURL: func(raw string) string { return NewInternalEvalCandidateReporter("token", raw).baseURL }},
		{name: "goal improvement completer", baseURL: func(raw string) string { return NewInternalAutomationGoalImprovementCompleter("token", raw).baseURL }},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, expected, tt.baseURL(origin), "constructor should append the internal API prefix to an origin")
			require.Equal(t, expected, tt.baseURL(expected), "constructor should accept the legacy path-qualified base without duplication")
		})
	}
}
