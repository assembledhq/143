package publicationintent

import "github.com/assembledhq/143/internal/models"

const UserInitiatedReviewMaxPasses = 2

type EffectivePolicy struct {
	CreatePRWhenAgentReady bool
	CreatePRSource         models.PublicationPolicySource
	ReviewBeforePR         bool
	ReviewSource           models.PublicationPolicySource
	ReviewMaxPasses        int
}

// ResolvePolicy snapshots organization policy and the stable session
// initiator's personal overrides. Callers are responsible for loading the user
// captured on the session, rather than the user making a later request.
func ResolvePolicy(
	org models.AutomaticFollowThroughOrgSettings,
	personal *models.AutomaticPRFollowThroughSettings,
) EffectivePolicy {
	policy := EffectivePolicy{
		CreatePRWhenAgentReady: org.EffectiveCreatePRWhenAgentReady(),
		CreatePRSource:         organizationOrDefault(org.CreatePRWhenAgentReady),
		ReviewBeforePR:         org.EffectiveReviewBeforePR(),
		ReviewSource:           organizationOrDefault(org.ReviewBeforePR),
		ReviewMaxPasses:        UserInitiatedReviewMaxPasses,
	}
	if personal == nil {
		return policy
	}
	policy.CreatePRWhenAgentReady, policy.CreatePRSource = applyPersonal(
		personal.CreatePRWhenAgentReady,
		policy.CreatePRWhenAgentReady,
		policy.CreatePRSource,
	)
	policy.ReviewBeforePR, policy.ReviewSource = applyPersonal(
		personal.ReviewBeforePR,
		policy.ReviewBeforePR,
		policy.ReviewSource,
	)
	return policy
}

func organizationOrDefault(value *bool) models.PublicationPolicySource {
	if value == nil {
		return models.PublicationPolicySourceProductDefault
	}
	return models.PublicationPolicySourceOrganization
}

func applyPersonal(
	preference models.AutomaticFollowThroughPreference,
	inherited bool,
	inheritedSource models.PublicationPolicySource,
) (bool, models.PublicationPolicySource) {
	switch preference {
	case models.AutomaticFollowThroughPreferenceOn:
		return true, models.PublicationPolicySourcePersonal
	case models.AutomaticFollowThroughPreferenceOff:
		return false, models.PublicationPolicySourcePersonal
	default:
		return inherited, inheritedSource
	}
}
