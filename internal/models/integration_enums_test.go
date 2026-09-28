package models

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestIntegrationProviderValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		value       IntegrationProvider
		expectedErr string
	}{
		{name: "valid github", value: IntegrationProviderGitHub},
		{name: "valid sentry", value: IntegrationProviderSentry},
		{name: "valid linear", value: IntegrationProviderLinear},
		{name: "valid slack", value: IntegrationProviderSlack},
		{name: "valid notion", value: IntegrationProviderNotion},
		{name: "valid circleci", value: IntegrationProviderCircleCI},
		{name: "valid victorialogs", value: IntegrationProviderVictoriaLogs},
		{name: "valid mezmo", value: IntegrationProviderMezmo},
		{name: "invalid empty", value: IntegrationProvider(""), expectedErr: `invalid IntegrationProvider: ""`},
		{name: "invalid unknown", value: IntegrationProvider("jira"), expectedErr: `invalid IntegrationProvider: "jira"`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.value.Validate()
			if tt.expectedErr != "" {
				require.EqualError(t, err, tt.expectedErr, "Validate should return the expected error for invalid integration provider")
				return
			}
			require.NoError(t, err, "Validate should succeed for valid integration provider")
		})
	}
}

func TestIntegrationStatusValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		value       IntegrationStatus
		expectedErr string
	}{
		{name: "valid active", value: IntegrationStatusActive},
		{name: "valid inactive", value: IntegrationStatusInactive},
		{name: "valid error", value: IntegrationStatusError},
		{name: "invalid empty", value: IntegrationStatus(""), expectedErr: `invalid IntegrationStatus: ""`},
		{name: "invalid unknown", value: IntegrationStatus("paused"), expectedErr: `invalid IntegrationStatus: "paused"`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.value.Validate()
			if tt.expectedErr != "" {
				require.EqualError(t, err, tt.expectedErr, "Validate should return the expected error for invalid integration status")
				return
			}
			require.NoError(t, err, "Validate should succeed for valid integration status")
		})
	}
}

func TestGitHubRepositoryClaimStatusValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		value       GitHubRepositoryClaimStatus
		expectedErr string
	}{
		{name: "valid unclaimed", value: GitHubRepositoryClaimStatusUnclaimed},
		{name: "valid owned by current org", value: GitHubRepositoryClaimStatusOwnedByCurrentOrg},
		{name: "valid owned by other org", value: GitHubRepositoryClaimStatusOwnedByOtherOrg},
		{name: "valid disconnected in current org", value: GitHubRepositoryClaimStatusDisconnectedInCurrentOrg},
		{name: "invalid empty", value: GitHubRepositoryClaimStatus(""), expectedErr: `invalid GitHubRepositoryClaimStatus: ""`},
		{name: "invalid unknown", value: GitHubRepositoryClaimStatus("claimed"), expectedErr: `invalid GitHubRepositoryClaimStatus: "claimed"`},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.value.Validate()
			if tt.expectedErr != "" {
				require.EqualError(t, err, tt.expectedErr, "Validate should return the expected error for invalid GitHub repository claim status")
				return
			}
			require.NoError(t, err, "Validate should succeed for valid GitHub repository claim status")
		})
	}
}
