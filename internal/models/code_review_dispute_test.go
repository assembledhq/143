package models

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCodeReviewDisputeJSONHidesAuthorizationEvidence(t *testing.T) {
	t.Parallel()

	dispute := CodeReviewDispute{
		MembershipEvidence:        json.RawMessage(`{"internal_membership_id":"secret"}`),
		SourceBodyHash:            "internal-source-hash",
		SemanticInputHashAtFiling: "internal-filing-hash",
		SemanticInputHashAtRerun:  stringPointerForDisputeModelTest("internal-rerun-hash"),
		ReplyCycleReserved:        true,
	}

	encoded, err := json.Marshal(dispute)
	require.NoError(t, err, "dispute API serialization should succeed")
	var fields map[string]any
	require.NoError(t, json.Unmarshal(encoded, &fields), "serialized dispute should be valid JSON")
	require.NotContains(t, fields, "membership_evidence", "authorization evidence must not be exposed through dispute APIs")
	require.NotContains(t, fields, "source_body_hash", "internal source fingerprints must not be exposed through dispute APIs")
	require.NotContains(t, fields, "semantic_input_hash_at_filing", "semantic filing fingerprints must not be exposed through dispute APIs")
	require.NotContains(t, fields, "semantic_input_hash_at_rerun", "semantic rerun fingerprints must not be exposed through dispute APIs")
	require.NotContains(t, fields, "reply_cycle_reserved", "machine loop-guard state must remain internal")
}

func stringPointerForDisputeModelTest(value string) *string {
	return &value
}

func TestCodeReviewDisputeEnumsValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		validate  func() error
		expectErr bool
	}{
		{name: "valid visibility", validate: CodeReviewRepositoryVisibilityPrivate.Validate},
		{name: "invalid visibility", validate: CodeReviewRepositoryVisibility("internal").Validate, expectErr: true},
		{name: "valid direction", validate: CodeReviewDisputeDirectionShouldHaveApproved.Validate},
		{name: "invalid direction", validate: CodeReviewDisputeDirection("reverse").Validate, expectErr: true},
		{name: "valid source", validate: CodeReviewDisputeSourceGitHubComment.Validate},
		{name: "invalid source", validate: CodeReviewDisputeSource("email").Validate, expectErr: true},
		{name: "valid routing", validate: CodeReviewDisputeRoutingPolicySignalOnly.Validate},
		{name: "invalid routing", validate: CodeReviewDisputeRouting("ignore").Validate, expectErr: true},
		{name: "valid intake status", validate: CodeReviewDisputeIntakeTriaged.Validate},
		{name: "invalid intake status", validate: CodeReviewDisputeIntakeStatus("done").Validate, expectErr: true},
		{name: "valid adjudication status", validate: CodeReviewDisputeAdjudicationUpheld.Validate},
		{name: "invalid adjudication status", validate: CodeReviewDisputeAdjudicationStatus("approved").Validate, expectErr: true},
		{name: "valid reassessment status", validate: CodeReviewDisputeReassessmentRunning.Validate},
		{name: "invalid reassessment status", validate: CodeReviewDisputeReassessmentStatus("started").Validate, expectErr: true},
		{name: "valid reply status", validate: CodeReviewDisputeReplyPublished.Validate},
		{name: "invalid reply status", validate: CodeReviewDisputeReplyStatus("sent").Validate, expectErr: true},
		{name: "valid authorization action", validate: CodeReviewDisputeAuthorizationRerun.Validate},
		{name: "invalid authorization action", validate: CodeReviewDisputeAuthorizationAction("approve").Validate, expectErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.validate()
			if tt.expectErr {
				require.Error(t, err, "invalid enum values should be rejected")
				return
			}
			require.NoError(t, err, "known enum values should validate")
		})
	}
}

func TestCodeReviewDispute_CurrentTrust(t *testing.T) {
	t.Parallel()

	trusted := true
	untrusted := false
	tests := []struct {
		name           string
		dispute        CodeReviewDispute
		expectedTrust  bool
		expectedReason string
	}{
		{name: "private repository", dispute: CodeReviewDispute{RepositoryVisibility: CodeReviewRepositoryVisibilityPrivate}, expectedTrust: true, expectedReason: "private repository contributor"},
		{name: "trusted association", dispute: CodeReviewDispute{RepositoryVisibility: CodeReviewRepositoryVisibilityPublic, AuthorAssociation: "member"}, expectedTrust: true, expectedReason: "trusted GitHub association"},
		{name: "outside contributor", dispute: CodeReviewDispute{RepositoryVisibility: CodeReviewRepositoryVisibilityPublic, AuthorAssociation: "none"}, expectedReason: "external contributor"},
		// The two override directions must read differently: the reason is shown
		// next to the trust badge in the admin queue.
		{name: "positive override", dispute: CodeReviewDispute{TrustOverride: &trusted}, expectedTrust: true, expectedReason: "admin promoted this dispute"},
		{name: "negative override", dispute: CodeReviewDispute{RepositoryVisibility: CodeReviewRepositoryVisibilityPrivate, TrustOverride: &untrusted}, expectedReason: "admin demoted this dispute"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			actualTrust, actualReason := tt.dispute.CurrentTrust()
			require.Equal(t, tt.expectedTrust, actualTrust, "trust should derive from current facts and explicit override")
			require.Equal(t, tt.expectedReason, actualReason, "trust should expose the reason used for authorization")
		})
	}
}
