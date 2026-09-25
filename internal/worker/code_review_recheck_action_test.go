package worker

import (
	"testing"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestCodeReviewEvidenceRecheckURL(t *testing.T) {
	t.Parallel()

	assessmentID := uuid.MustParse("90d8a47d-d87e-4780-90af-040f5144685a")
	const expectedURL = "https://143.test/code-reviews?recheck=90d8a47d-d87e-4780-90af-040f5144685a"
	tests := []struct {
		name      string
		configure func(**Services, *models.CodeReviewPolicyConfig, *uuid.UUID, *bool)
		expected  string
	}{
		{name: "enabled and fully covered", expected: expectedURL},
		{name: "services unavailable", configure: func(s **Services, _ *models.CodeReviewPolicyConfig, _ *uuid.UUID, _ *bool) { *s = nil }},
		{name: "assessments disabled", configure: func(s **Services, _ *models.CodeReviewPolicyConfig, _ *uuid.UUID, _ *bool) {
			(*s).CodeReviewAssessmentsEnabled = false
		}},
		{name: "rechecks disabled", configure: func(s **Services, _ *models.CodeReviewPolicyConfig, _ *uuid.UUID, _ *bool) {
			(*s).CodeReviewRechecksEnabled = false
		}},
		{name: "policy disabled", configure: func(_ **Services, p *models.CodeReviewPolicyConfig, _ *uuid.UUID, _ *bool) {
			p.ContinuationPolicy.Enabled = false
		}},
		{name: "legacy policy", configure: func(_ **Services, p *models.CodeReviewPolicyConfig, _ *uuid.UUID, _ *bool) {
			p.ContinuationPolicy = nil
		}},
		{name: "incomplete coverage", configure: func(_ **Services, _ *models.CodeReviewPolicyConfig, _ *uuid.UUID, complete *bool) { *complete = false }},
		{name: "missing assessment", configure: func(_ **Services, _ *models.CodeReviewPolicyConfig, id *uuid.UUID, _ *bool) { *id = uuid.Nil }},
		{name: "missing app URL", configure: func(s **Services, _ *models.CodeReviewPolicyConfig, _ *uuid.UUID, _ *bool) { (*s).FrontendURL = "" }},
		{name: "invalid app URL", configure: func(s **Services, _ *models.CodeReviewPolicyConfig, _ *uuid.UUID, _ *bool) {
			(*s).FrontendURL = "://invalid"
		}},
		{name: "hostless app URL", configure: func(s **Services, _ *models.CodeReviewPolicyConfig, _ *uuid.UUID, _ *bool) { (*s).FrontendURL = "/143" }},
		{name: "unsupported app URL", configure: func(s **Services, _ *models.CodeReviewPolicyConfig, _ *uuid.UUID, _ *bool) {
			(*s).FrontendURL = "javascript://143.test"
		}},
		{
			name: "existing query and fragment are replaced",
			configure: func(s **Services, _ *models.CodeReviewPolicyConfig, _ *uuid.UUID, _ *bool) {
				(*s).FrontendURL = " https://143.test/?old=value#fragment "
			},
			expected: expectedURL,
		},
		{
			name: "app path prefix is preserved",
			configure: func(s **Services, _ *models.CodeReviewPolicyConfig, _ *uuid.UUID, _ *bool) {
				(*s).FrontendURL = "https://143.test/app/"
			},
			expected: "https://143.test/app/code-reviews?recheck=" + assessmentID.String(),
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			services := &Services{CodeReviewAssessmentsEnabled: true, CodeReviewRechecksEnabled: true, FrontendURL: "https://143.test/"}
			policy := models.DefaultCodeReviewPolicyConfig()
			policy.ContinuationPolicy = &models.CodeReviewContinuationPolicy{Enabled: true}
			id, complete := assessmentID, true
			if tt.configure != nil {
				tt.configure(&services, &policy, &id, &complete)
			}
			require.Equal(t, tt.expected, codeReviewEvidenceRecheckURL(services, policy, id, complete), "only supported assessments should link to the authenticated confirmation page")
		})
	}
}

func TestCodeReviewAvailableReviewNowURL(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		services  *Services
		sessionID uuid.UUID
		expected  string
	}{
		{name: "enabled scheduler", services: &Services{FrontendURL: "https://143.test", CodeReviewLifecycle: &statusCommentSchedulingStub{enabled: true}}, sessionID: uuid.MustParse("90d8a47d-d87e-4780-90af-040f5144685a"), expected: "https://143.test/code-reviews?review_now=90d8a47d-d87e-4780-90af-040f5144685a"},
		{name: "services unavailable"},
		{name: "scheduler unavailable", services: &Services{FrontendURL: "https://143.test"}},
		{name: "scheduler disabled", services: &Services{FrontendURL: "https://143.test", CodeReviewLifecycle: &statusCommentSchedulingStub{}}},
		{name: "missing app URL", services: &Services{CodeReviewLifecycle: &statusCommentSchedulingStub{enabled: true}}, sessionID: uuid.New()},
		{name: "missing session", services: &Services{FrontendURL: "https://143.test", CodeReviewLifecycle: &statusCommentSchedulingStub{enabled: true}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.expected, codeReviewAvailableReviewNowURL(tt.services, tt.sessionID), "new full-review and rolling comments should share scheduler and destination eligibility")
		})
	}
}
