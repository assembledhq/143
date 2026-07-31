package publicationintent

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/models"
)

func TestResolvePolicy(t *testing.T) {
	t.Parallel()

	enabled, disabled := true, false
	tests := []struct {
		name     string
		org      models.AutomaticFollowThroughOrgSettings
		personal *models.AutomaticPRFollowThroughSettings
		want     EffectivePolicy
	}{
		{
			name: "product defaults on",
			want: EffectivePolicy{
				CreatePRWhenAgentReady: true,
				CreatePRSource:         models.PublicationPolicySourceProductDefault,
				ReviewBeforePR:         true,
				ReviewSource:           models.PublicationPolicySourceProductDefault,
				ReviewMaxPasses:        UserInitiatedReviewMaxPasses,
			},
		},
		{
			name: "organization values are authoritative when inherited",
			org: models.AutomaticFollowThroughOrgSettings{
				CreatePRWhenAgentReady: &disabled,
				ReviewBeforePR:         &enabled,
			},
			personal: &models.AutomaticPRFollowThroughSettings{
				CreatePRWhenAgentReady: models.AutomaticFollowThroughPreferenceInherit,
				ReviewBeforePR:         models.AutomaticFollowThroughPreferenceInherit,
			},
			want: EffectivePolicy{
				CreatePRWhenAgentReady: false,
				CreatePRSource:         models.PublicationPolicySourceOrganization,
				ReviewBeforePR:         true,
				ReviewSource:           models.PublicationPolicySourceOrganization,
				ReviewMaxPasses:        UserInitiatedReviewMaxPasses,
			},
		},
		{
			name: "personal values override organization values",
			org: models.AutomaticFollowThroughOrgSettings{
				CreatePRWhenAgentReady: &disabled,
				ReviewBeforePR:         &enabled,
			},
			personal: &models.AutomaticPRFollowThroughSettings{
				CreatePRWhenAgentReady: models.AutomaticFollowThroughPreferenceOn,
				ReviewBeforePR:         models.AutomaticFollowThroughPreferenceOff,
			},
			want: EffectivePolicy{
				CreatePRWhenAgentReady: true,
				CreatePRSource:         models.PublicationPolicySourcePersonal,
				ReviewBeforePR:         false,
				ReviewSource:           models.PublicationPolicySourcePersonal,
				ReviewMaxPasses:        UserInitiatedReviewMaxPasses,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, ResolvePolicy(tt.org, tt.personal), "policy resolution should apply personal, organization, and product precedence")
		})
	}
}
