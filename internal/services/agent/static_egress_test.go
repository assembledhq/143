package agent

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/models"
)

type staticEgressTestOrgStore struct {
	org models.Organization
}

func (s staticEgressTestOrgStore) GetByID(context.Context, uuid.UUID) (models.Organization, error) {
	return s.org, nil
}

func TestResolveStaticEgressRuntimeConfigRequiresCapabilityFile(t *testing.T) {
	t.Parallel()

	cfg := ResolveStaticEgressRuntimeConfig(false, "203.0.113.10")
	require.False(t, cfg.Enabled, "disabled static egress should not enable the runtime")
	require.False(t, cfg.Capable, "disabled static egress should not advertise capability")
	require.Equal(t, DefaultStaticEgressNetwork, cfg.NetworkName, "static egress bridge should use the fixed internal network")
	require.Equal(t, DefaultStaticEgressResolvConf, cfg.ResolvConfPath, "static egress resolver should use the fixed internal path")
}

func TestParseStaticEgressCapabilityPublicIP(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		content     string
		expected    string
		expectedErr string
	}{
		{
			name:     "returns public ip",
			content:  "public_ip=203.0.113.10\nnetwork=143-sandbox-static-egress\n",
			expected: "203.0.113.10",
		},
		{
			name:        "rejects empty public ip",
			content:     "public_ip=\n",
			expectedErr: "empty public_ip",
		},
		{
			name:        "requires public ip",
			content:     "network=143-sandbox-static-egress\n",
			expectedErr: "missing public_ip",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := parseStaticEgressCapabilityPublicIP(tt.content, "test-marker")
			if tt.expectedErr != "" {
				require.Error(t, err, "invalid marker content should return an error")
				require.Contains(t, err.Error(), tt.expectedErr, "error should explain the invalid marker content")
				return
			}
			require.NoError(t, err, "valid marker content should parse")
			require.Equal(t, tt.expected, got, "parser should return the marker public IP")
		})
	}
}

func TestApplyOrgSandboxNetworkSettingsRequiresVerifiedStaticEgress(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	orgSettings := json.RawMessage(`{"sandbox_network":{"static_egress_enabled":true}}`)
	orgs := staticEgressTestOrgStore{org: models.Organization{ID: orgID, Settings: orgSettings}}
	cfg := DefaultSandboxConfig()

	err := ApplyOrgSandboxNetworkSettings(context.Background(), orgs, orgID, StaticEgressRuntimeConfig{
		Enabled:  true,
		Capable:  false,
		PublicIP: "203.0.113.10",
	}, &cfg)

	require.Error(t, err, "static-egress orgs should fail closed when the worker has not passed host verification")
	require.Contains(t, err.Error(), "not static-egress capable", "error should explain that this worker cannot serve static egress")

	verifiedCfg := DefaultSandboxConfig()
	err = ApplyOrgSandboxNetworkSettings(context.Background(), orgs, orgID, StaticEgressRuntimeConfig{
		Enabled:     true,
		Capable:     true,
		NetworkName: "static-net",
		PublicIP:    "203.0.113.10",
	}, &verifiedCfg)

	require.NoError(t, err, "verified static-egress workers should accept opted-in org sandboxes")
	require.Equal(t, "static-net", verifiedCfg.NetworkName, "verified static egress should select the configured bridge")
	require.Equal(t, SandboxEgressModeStatic, verifiedCfg.EgressMode, "verified static egress should persist static egress mode")
}
