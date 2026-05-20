package agent

import (
	"context"
	"encoding/json"
	"path/filepath"
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

	capabilityFile := filepath.Join(t.TempDir(), "static-egress-capable")
	cfg := resolveStaticEgressRuntimeConfig(true, "203.0.113.10", capabilityFile)
	require.True(t, cfg.Enabled, "static egress should remain enabled when platform config is present")
	require.False(t, cfg.Capable, "static egress should not be capable before the host verifier writes its marker")
	require.Contains(t, cfg.UnavailableReason, "capability file", "missing marker should explain why the worker is not capable")

	require.NoError(t, WriteStaticEgressCapabilityFile(capabilityFile, "203.0.113.10", "static-net"), "test should write a verifier marker")
	cfg = resolveStaticEgressRuntimeConfig(true, "203.0.113.10", capabilityFile)
	require.True(t, cfg.Capable, "matching verifier marker should make the runtime static-egress capable")
	require.Equal(t, DefaultStaticEgressNetwork, cfg.NetworkName, "static egress bridge should use the fixed internal network")
	require.Equal(t, DefaultStaticEgressResolvConf, cfg.ResolvConfPath, "static egress resolver should use the fixed internal path")
	require.Empty(t, cfg.UnavailableReason, "capable runtime should not carry an unavailable reason")

	require.NoError(t, WriteStaticEgressCapabilityFile(capabilityFile, "198.51.100.20", "static-net"), "test should write a mismatched verifier marker")
	cfg = resolveStaticEgressRuntimeConfig(true, "203.0.113.10", capabilityFile)
	require.False(t, cfg.Capable, "mismatched public IP marker should not make the worker capable")
	require.Contains(t, cfg.UnavailableReason, "does not match", "mismatched marker should explain the public IP mismatch")
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
