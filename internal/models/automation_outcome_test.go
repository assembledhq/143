package models

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAutomationOutcomeEnumsValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		validate func() error
		valid    bool
	}{
		{name: "passed decision", validate: AutomationOutcomeDecisionPassed.Validate, valid: true},
		{name: "changes requested decision", validate: AutomationOutcomeDecisionChangesRequested.Validate, valid: true},
		{name: "advisory decision", validate: AutomationOutcomeDecisionAdvisory.Validate, valid: true},
		{name: "not applicable decision", validate: AutomationOutcomeDecisionNotApplicable.Validate, valid: true},
		{name: "invalid decision", validate: AutomationOutcomeDecision("done").Validate, valid: false},
		{name: "agent reported source", validate: AutomationOutcomeSourceAgentReported.Validate, valid: true},
		{name: "legacy inferred source", validate: AutomationOutcomeSourceLegacyInferred.Validate, valid: true},
		{name: "invalid source", validate: AutomationOutcomeSource("parsed").Validate, valid: false},
		{name: "changes requested action", validate: AutomationExternalActionGitHubReviewChangesRequested.Validate, valid: true},
		{name: "approved action", validate: AutomationExternalActionGitHubReviewApproved.Validate, valid: true},
		{name: "comment action", validate: AutomationExternalActionGitHubComment.Validate, valid: true},
		{name: "invalid action", validate: AutomationExternalActionType("review").Validate, valid: false},
		{name: "reported verification", validate: AutomationExternalActionVerificationReported.Validate, valid: true},
		{name: "verified verification", validate: AutomationExternalActionVerificationVerified.Validate, valid: true},
		{name: "unavailable verification", validate: AutomationExternalActionVerificationUnavailable.Validate, valid: true},
		{name: "invalid verification", validate: AutomationExternalActionVerificationStatus("unknown").Validate, valid: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.validate()
			if tt.valid {
				require.NoError(t, err, "valid automation outcome enum should pass validation")
				return
			}
			require.Error(t, err, "invalid automation outcome enum should fail validation")
		})
	}
}
