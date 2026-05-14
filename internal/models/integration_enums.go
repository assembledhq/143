package models

import "fmt"

// IntegrationProvider identifies an external integration provider.
type IntegrationProvider string

const (
	IntegrationProviderGitHub   IntegrationProvider = "github"
	IntegrationProviderSentry   IntegrationProvider = "sentry"
	IntegrationProviderLinear   IntegrationProvider = "linear"
	IntegrationProviderSlack    IntegrationProvider = "slack"
	IntegrationProviderNotion   IntegrationProvider = "notion"
	IntegrationProviderCircleCI IntegrationProvider = "circleci"
)

func (p IntegrationProvider) Validate() error {
	switch p {
	case IntegrationProviderGitHub, IntegrationProviderSentry, IntegrationProviderLinear,
		IntegrationProviderSlack, IntegrationProviderNotion, IntegrationProviderCircleCI:
		return nil
	default:
		return fmt.Errorf("invalid IntegrationProvider: %q", p)
	}
}

// IntegrationStatus captures lifecycle state for an integration.
type IntegrationStatus string

const (
	IntegrationStatusActive   IntegrationStatus = "active"
	IntegrationStatusInactive IntegrationStatus = "inactive"
	IntegrationStatusError    IntegrationStatus = "error"
)

func (s IntegrationStatus) Validate() error {
	switch s {
	case IntegrationStatusActive, IntegrationStatusInactive, IntegrationStatusError:
		return nil
	default:
		return fmt.Errorf("invalid IntegrationStatus: %q", s)
	}
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
	switch s {
	case GitHubRepositoryClaimStatusUnclaimed,
		GitHubRepositoryClaimStatusOwnedByCurrentOrg,
		GitHubRepositoryClaimStatusOwnedByOtherOrg,
		GitHubRepositoryClaimStatusDisconnectedInCurrentOrg:
		return nil
	default:
		return fmt.Errorf("invalid GitHubRepositoryClaimStatus: %q", s)
	}
}
