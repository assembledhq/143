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
