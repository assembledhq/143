package github

import (
	"testing"

	"github.com/assembledhq/143/internal/models"
	"github.com/stretchr/testify/require"
)

func TestResolvePRFeedbackPolicy(t *testing.T) {
	t.Parallel()

	unlimited := models.NullableCycleLimit{Set: true}
	zero := 0
	tests := []struct {
		name     string
		input    prFeedbackPolicyInput
		expected prFeedbackPolicy
	}{
		{
			name:     "absent settings use GA defaults for public repository",
			input:    prFeedbackPolicyInput{Monitoring: models.PRFeedbackMonitoringInherit, Linked: true},
			expected: prFeedbackPolicy{HumanMode: models.PRFeedbackHumanModeAllTrusted, BotMode: models.PRFeedbackBotModeAll, CycleLimit: intPointer(3), BotScope: models.PRFeedbackBotScopeTrustedPublic},
		},
		{
			name:     "private repository accepts all bots",
			input:    prFeedbackPolicyInput{PrivateRepo: true, Monitoring: models.PRFeedbackMonitoringEnabled, Linked: true},
			expected: prFeedbackPolicy{HumanMode: models.PRFeedbackHumanModeAllTrusted, BotMode: models.PRFeedbackBotModeAll, CycleLimit: intPointer(3), BotScope: models.PRFeedbackBotScopeAllPrivate},
		},
		{
			name:     "allowlist narrows bot scope and unlimited remains nil",
			input:    prFeedbackPolicyInput{Organization: models.AutomaticFollowThroughOrgSettings{PRFeedbackBotMode: models.PRFeedbackBotModeAllowlist, PRFeedbackBotCycleLimit: unlimited}, Linked: true},
			expected: prFeedbackPolicy{HumanMode: models.PRFeedbackHumanModeAllTrusted, BotMode: models.PRFeedbackBotModeAllowlist, CycleLimit: nil, BotScope: models.PRFeedbackBotScopeSelected},
		},
		{
			name:     "organization off is authoritative",
			input:    prFeedbackPolicyInput{Organization: models.AutomaticFollowThroughOrgSettings{PRFeedbackMode: models.PRFeedbackHumanModeOff, PRFeedbackBotMode: models.PRFeedbackBotModeNone}, Personal: models.AutomaticFollowThroughPreferenceOn, Monitoring: models.PRFeedbackMonitoringEnabled, Linked: true},
			expected: prFeedbackPolicy{HumanMode: models.PRFeedbackHumanModeOff, BotMode: models.PRFeedbackBotModeNone, CycleLimit: intPointer(3), BotScope: models.PRFeedbackBotScopeNone, PausedReason: "organization_disabled"},
		},
		{
			name:     "personal off pauses inherited policy",
			input:    prFeedbackPolicyInput{Personal: models.AutomaticFollowThroughPreferenceOff, Monitoring: models.PRFeedbackMonitoringEnabled, Linked: true},
			expected: prFeedbackPolicy{HumanMode: models.PRFeedbackHumanModeAllTrusted, BotMode: models.PRFeedbackBotModeAll, CycleLimit: intPointer(3), BotScope: models.PRFeedbackBotScopeTrustedPublic, PausedReason: "personal_disabled"},
		},
		{
			name:     "per PR disabled pauses work",
			input:    prFeedbackPolicyInput{Monitoring: models.PRFeedbackMonitoringDisabled, Linked: true},
			expected: prFeedbackPolicy{HumanMode: models.PRFeedbackHumanModeAllTrusted, BotMode: models.PRFeedbackBotModeAll, CycleLimit: intPointer(3), BotScope: models.PRFeedbackBotScopeTrustedPublic, PausedReason: "pull_request_disabled"},
		},
		{
			name:     "unlinked PR cannot run",
			input:    prFeedbackPolicyInput{Monitoring: models.PRFeedbackMonitoringEnabled},
			expected: prFeedbackPolicy{HumanMode: models.PRFeedbackHumanModeAllTrusted, BotMode: models.PRFeedbackBotModeAll, CycleLimit: intPointer(3), BotScope: models.PRFeedbackBotScopeTrustedPublic, PausedReason: "pull_request_not_linked_to_session"},
		},
		{
			name:     "archived session pauses work and zero limit remains zero",
			input:    prFeedbackPolicyInput{Organization: models.AutomaticFollowThroughOrgSettings{PRFeedbackBotCycleLimit: models.NullableCycleLimit{Set: true, Value: &zero}}, Monitoring: models.PRFeedbackMonitoringEnabled, Linked: true, Archived: true},
			expected: prFeedbackPolicy{HumanMode: models.PRFeedbackHumanModeAllTrusted, BotMode: models.PRFeedbackBotModeAll, CycleLimit: &zero, BotScope: models.PRFeedbackBotScopeTrustedPublic, PausedReason: "session_archived"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.expected, resolvePRFeedbackPolicy(tt.input), "policy should resolve all precedence and scope fields")
		})
	}
}

func intPointer(value int) *int { return &value }
