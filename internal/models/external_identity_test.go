package models

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestExternalIdentityProvider_Validate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		provider  ExternalIdentityProvider
		expectErr bool
	}{
		{name: "slack is valid", provider: ExternalIdentityProviderSlack},
		{name: "linear is valid", provider: ExternalIdentityProviderLinear},
		{name: "unknown is invalid", provider: ExternalIdentityProvider("github"), expectErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.provider.Validate()
			if tt.expectErr {
				require.Error(t, err, "Validate should reject unknown external identity providers")
				return
			}
			require.NoError(t, err, "Validate should accept known external identity providers")
		})
	}
}

func TestExternalUserLinkSource_Validate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		source    ExternalUserLinkSource
		expectErr bool
	}{
		{name: "self linked is valid", source: ExternalUserLinkSourceSelfLinked},
		{name: "admin linked is valid", source: ExternalUserLinkSourceAdminLinked},
		{name: "email match is valid", source: ExternalUserLinkSourceEmailMatch},
		{name: "directory is valid", source: ExternalUserLinkSourceDirectory},
		{name: "observed is invalid", source: ExternalUserLinkSource("observed"), expectErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.source.Validate()
			if tt.expectErr {
				require.Error(t, err, "Validate should reject non-authoritative external user link sources")
				return
			}
			require.NoError(t, err, "Validate should accept authoritative external user link sources")
		})
	}
}

func TestExternalUserLinkStatus_Validate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		status    ExternalUserLinkStatus
		expectErr bool
	}{
		{name: "active is valid", status: ExternalUserLinkStatusActive},
		{name: "revoked is valid", status: ExternalUserLinkStatusRevoked},
		{name: "pending is invalid", status: ExternalUserLinkStatus("pending"), expectErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.status.Validate()
			if tt.expectErr {
				require.Error(t, err, "Validate should reject unknown external user link statuses")
				return
			}
			require.NoError(t, err, "Validate should accept known external user link statuses")
		})
	}
}
