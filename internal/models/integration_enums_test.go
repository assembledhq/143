package models

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestIntegrationProviderValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		value   IntegrationProvider
		wantErr bool
	}{
		{name: "valid github", value: IntegrationProviderGitHub},
		{name: "valid sentry", value: IntegrationProviderSentry},
		{name: "valid linear", value: IntegrationProviderLinear},
		{name: "valid slack", value: IntegrationProviderSlack},
		{name: "valid notion", value: IntegrationProviderNotion},
		{name: "valid circleci", value: IntegrationProviderCircleCI},
		{name: "valid victorialogs", value: IntegrationProviderVictoriaLogs},
		{name: "valid mezmo", value: IntegrationProviderMezmo},
		{name: "invalid empty", value: IntegrationProvider(""), wantErr: true},
		{name: "invalid unknown", value: IntegrationProvider("jira"), wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.value.Validate()
			if tt.wantErr {
				require.Error(t, err, "Validate should return error for invalid integration provider")
				return
			}
			require.NoError(t, err, "Validate should succeed for valid integration provider")
		})
	}
}

func TestIntegrationStatusValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		value   IntegrationStatus
		wantErr bool
	}{
		{name: "valid active", value: IntegrationStatusActive},
		{name: "valid inactive", value: IntegrationStatusInactive},
		{name: "valid error", value: IntegrationStatusError},
		{name: "invalid empty", value: IntegrationStatus(""), wantErr: true},
		{name: "invalid unknown", value: IntegrationStatus("paused"), wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.value.Validate()
			if tt.wantErr {
				require.Error(t, err, "Validate should return error for invalid integration status")
				return
			}
			require.NoError(t, err, "Validate should succeed for valid integration status")
		})
	}
}

func TestGitHubRepositoryClaimStatusValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		value   GitHubRepositoryClaimStatus
		wantErr bool
	}{
		{name: "valid unclaimed", value: GitHubRepositoryClaimStatusUnclaimed},
		{name: "valid owned by current org", value: GitHubRepositoryClaimStatusOwnedByCurrentOrg},
		{name: "valid owned by other org", value: GitHubRepositoryClaimStatusOwnedByOtherOrg},
		{name: "valid disconnected in current org", value: GitHubRepositoryClaimStatusDisconnectedInCurrentOrg},
		{name: "invalid empty", value: GitHubRepositoryClaimStatus(""), wantErr: true},
		{name: "invalid unknown", value: GitHubRepositoryClaimStatus("claimed"), wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.value.Validate()
			if tt.wantErr {
				require.Error(t, err, "Validate should return error for invalid GitHub repository claim status")
				return
			}
			require.NoError(t, err, "Validate should succeed for valid GitHub repository claim status")
		})
	}
}
