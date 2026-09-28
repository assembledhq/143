package models

import "fmt"

// IntegrationProvider identifies an external integration provider.
type IntegrationProvider string

const (
	IntegrationProviderGitHub       IntegrationProvider = "github"
	IntegrationProviderSentry       IntegrationProvider = "sentry"
	IntegrationProviderLinear       IntegrationProvider = "linear"
	IntegrationProviderPagerDuty    IntegrationProvider = "pagerduty"
	IntegrationProviderSlack        IntegrationProvider = "slack"
	IntegrationProviderNotion       IntegrationProvider = "notion"
	IntegrationProviderCircleCI     IntegrationProvider = "circleci"
	IntegrationProviderVictoriaLogs IntegrationProvider = "victorialogs"
	IntegrationProviderMezmo        IntegrationProvider = "mezmo"
)

func (p IntegrationProvider) Validate() error {
	return validateIntegrationEnum("IntegrationProvider", p,
		IntegrationProviderGitHub, IntegrationProviderSentry, IntegrationProviderLinear, IntegrationProviderPagerDuty,
		IntegrationProviderSlack, IntegrationProviderNotion, IntegrationProviderCircleCI,
		IntegrationProviderVictoriaLogs, IntegrationProviderMezmo)
}

// IntegrationStatus captures lifecycle state for an integration.
type IntegrationStatus string

const (
	IntegrationStatusActive   IntegrationStatus = "active"
	IntegrationStatusInactive IntegrationStatus = "inactive"
	IntegrationStatusError    IntegrationStatus = "error"
)

func (s IntegrationStatus) Validate() error {
	return validateIntegrationEnum("IntegrationStatus", s,
		IntegrationStatusActive, IntegrationStatusInactive, IntegrationStatusError)
}

// GitHubRepositoryClaimStatus describes a repository's active ownership from
// the perspective of the current 143 organization.
type GitHubRepositoryClaimStatus string

const (
	GitHubRepositoryClaimStatusUnclaimed                GitHubRepositoryClaimStatus = "unclaimed"
	GitHubRepositoryClaimStatusOwnedByCurrentOrg        GitHubRepositoryClaimStatus = "owned_by_current_org"
	GitHubRepositoryClaimStatusOwnedByOtherOrg          GitHubRepositoryClaimStatus = "owned_by_other_org"
	GitHubRepositoryClaimStatusDisconnectedInCurrentOrg GitHubRepositoryClaimStatus = "disconnected_in_current_org"
)

func (s GitHubRepositoryClaimStatus) Validate() error {
	return validateIntegrationEnum("GitHubRepositoryClaimStatus", s,
		GitHubRepositoryClaimStatusUnclaimed,
		GitHubRepositoryClaimStatusOwnedByCurrentOrg,
		GitHubRepositoryClaimStatusOwnedByOtherOrg,
		GitHubRepositoryClaimStatusDisconnectedInCurrentOrg)
}

func validateIntegrationEnum[T ~string](typeName string, value T, valid ...T) error {
	for _, candidate := range valid {
		if value == candidate {
			return nil
		}
	}
	return fmt.Errorf("invalid %s: %q", typeName, value)
}
