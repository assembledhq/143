package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"

	"github.com/google/uuid"

	"github.com/assembledhq/143/internal/models"
)

const (
	DefaultStaticEgressNetwork    = "143-sandbox-static-egress"
	DefaultStaticEgressResolvConf = "/etc/143/sandbox-static-egress-resolv.conf"
	DefaultStaticEgressCapability = "/etc/143/static-egress-capable"
)

// OrgSettingsReader is the subset of the org store needed to resolve sandbox
// network settings without coupling the agent package to the concrete DB store.
type OrgSettingsReader interface {
	GetByID(ctx context.Context, orgID uuid.UUID) (models.Organization, error)
}

// StaticEgressRuntimeConfig describes the worker-local static egress network.
type StaticEgressRuntimeConfig struct {
	Enabled           bool
	Capable           bool
	NetworkName       string
	ResolvConfPath    string
	PublicIP          string
	UnavailableReason string
}

// ResolveStaticEgressRuntimeConfig converts platform config into the
// worker-local runtime contract. Static egress uses fixed internal bridge and
// resolver paths; only the customer-facing public IP is configurable.
func ResolveStaticEgressRuntimeConfig(enabled bool, publicIP string) StaticEgressRuntimeConfig {
	runtime := StaticEgressRuntimeConfig{
		Enabled:        enabled && publicIP != "",
		NetworkName:    DefaultStaticEgressNetwork,
		ResolvConfPath: DefaultStaticEgressResolvConf,
		PublicIP:       publicIP,
	}
	if !enabled {
		runtime.UnavailableReason = "STATIC_EGRESS_ENABLED is false"
		return runtime
	}
	if publicIP == "" {
		runtime.UnavailableReason = "STATIC_EGRESS_PUBLIC_IP is not configured"
		return runtime
	}
	verifiedIP, err := readStaticEgressCapabilityPublicIP()
	if err != nil {
		runtime.UnavailableReason = err.Error()
		return runtime
	}
	if verifiedIP != publicIP {
		runtime.UnavailableReason = fmt.Sprintf("static egress capability public_ip %q does not match configured public IP %q", verifiedIP, publicIP)
		return runtime
	}
	runtime.Capable = true
	return runtime
}

func readStaticEgressCapabilityPublicIP() (string, error) {
	content, err := os.ReadFile(DefaultStaticEgressCapability)
	if err != nil {
		return "", fmt.Errorf("static egress capability file %q is not readable: %w", DefaultStaticEgressCapability, err)
	}
	return parseStaticEgressCapabilityPublicIP(string(content), DefaultStaticEgressCapability)
}

func parseStaticEgressCapabilityPublicIP(content, source string) (string, error) {
	for _, line := range strings.Split(content, "\n") {
		key, value, ok := strings.Cut(strings.TrimSpace(line), "=")
		if !ok || strings.TrimSpace(key) != "public_ip" {
			continue
		}
		value = strings.TrimSpace(value)
		if value == "" {
			return "", fmt.Errorf("static egress capability file %q has an empty public_ip", source)
		}
		return value, nil
	}
	return "", fmt.Errorf("static egress capability file %q is missing public_ip", source)
}

// ApplyOrgSandboxNetworkSettings chooses the sandbox network for a new
// container. Static egress is opt-in per org and fail-closed when the worker
// is not configured for the static egress bridge.
func ApplyOrgSandboxNetworkSettings(ctx context.Context, orgs OrgSettingsReader, orgID uuid.UUID, runtime StaticEgressRuntimeConfig, cfg *SandboxConfig) error {
	if cfg == nil || orgs == nil {
		return nil
	}
	org, err := orgs.GetByID(ctx, orgID)
	if err != nil {
		return fmt.Errorf("load org settings for sandbox network: %w", err)
	}
	settings, err := models.ParseOrgSettings(org.Settings)
	if err != nil {
		return fmt.Errorf("parse org settings for sandbox network: %w", err)
	}
	if !settings.SandboxNetwork.StaticEgressEnabled {
		cfg.EgressMode = SandboxEgressModeDirect
		return nil
	}
	if !runtime.Enabled || !runtime.Capable {
		reason := runtime.UnavailableReason
		if reason == "" {
			reason = "host verification has not completed"
		}
		return fmt.Errorf("static egress is enabled for this org, but this worker is not static-egress capable (%s); restart environment to apply network setting", reason)
	}
	ApplyStaticEgressRuntimeConfig(runtime, cfg)
	return nil
}

// ApplyStaticEgressRuntimeConfig mutates cfg to create the sandbox on the
// worker's static egress bridge.
func ApplyStaticEgressRuntimeConfig(runtime StaticEgressRuntimeConfig, cfg *SandboxConfig) {
	if cfg == nil {
		return
	}
	network := runtime.NetworkName
	if network == "" {
		network = DefaultStaticEgressNetwork
	}
	cfg.NetworkName = network
	cfg.ResolvConfPath = runtime.ResolvConfPath
	cfg.EgressMode = SandboxEgressModeStatic
}

// SandboxNetworkMatches verifies that a live sandbox is attached to the
// network that new sandboxes for the same org setting would use.
func SandboxNetworkMatches(ctx context.Context, provider SandboxProvider, sb *Sandbox, expectedNetwork, staticNetwork string) (bool, error) {
	if provider == nil || expectedNetwork == "" && staticNetwork == "" {
		return true, nil
	}
	info, err := provider.ConnectionInfo(ctx, sb)
	if err != nil {
		return false, err
	}
	current := ""
	if info != nil && info.Environment != nil {
		current = info.Environment["DOCKER_HOST"]
	}
	if expectedNetwork != "" {
		return current == expectedNetwork, nil
	}
	if staticNetwork != "" && current == staticNetwork {
		return false, nil
	}
	return true, nil
}

// StaticEgressEnabledFromRawSettings is a small helper for API paths that need
// the org toggle without constructing a sandbox config.
func StaticEgressEnabledFromRawSettings(raw json.RawMessage) (bool, error) {
	settings, err := models.ParseOrgSettings(raw)
	if err != nil {
		return false, err
	}
	return settings.SandboxNetwork.StaticEgressEnabled, nil
}
