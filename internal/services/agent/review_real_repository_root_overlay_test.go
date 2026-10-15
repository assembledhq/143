package agent_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/docker/docker/api/types/system"
)

// Only the disposable acceptance harness may select this runtime registration.
// The empty value retains the existing registration and ordinary acceptance path.
type realReviewRootOverlay string

const (
	realReviewRootOverlayDefault realReviewRootOverlay = ""
	realReviewRootOverlayNone    realReviewRootOverlay = "none"
)

func (overlay *realReviewRootOverlay) UnmarshalJSON(raw []byte) error {
	var value string
	if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) || json.Unmarshal(raw, &value) != nil || !realReviewRootOverlay(value).valid() {
		return errors.New("invalid acceptance root overlay selector")
	}
	*overlay = realReviewRootOverlay(value)
	return nil
}

func (overlay realReviewRootOverlay) valid() bool {
	return overlay == realReviewRootOverlayDefault || overlay == realReviewRootOverlayNone
}

func (overlay realReviewRootOverlay) runscArgs() []string {
	if !overlay.valid() {
		return nil
	}
	if overlay == realReviewRootOverlayNone {
		return []string{"--host-uds=open", "--overlay2=none"}
	}
	return []string{"--host-uds=open"}
}

func (overlay realReviewRootOverlay) label() string {
	if overlay == realReviewRootOverlayDefault {
		return "default"
	}
	return string(overlay)
}

func parseRealReviewRootOverlay(value string, optedIn bool, runtimeState, runtimeName string) (realReviewRootOverlay, error) {
	overlay := realReviewRootOverlay(value)
	if !overlay.valid() {
		return realReviewRootOverlayDefault, errors.New("unsupported acceptance root overlay selector")
	}
	if overlay == realReviewRootOverlayNone && (!optedIn || runtimeName != "runsc" || len(runtimeState) > 1024 || !filepath.IsAbs(runtimeState) || filepath.Clean(runtimeState) != runtimeState || runtimeState == "/" || strings.ContainsAny(runtimeState, "\x00\r\n")) {
		return realReviewRootOverlayDefault, errors.New("explicit root overlay requires opted-in owned runsc acceptance")
	}
	return overlay, nil
}

// Docker adds status metadata to this registration. Only the exact legacy path
// and arguments are permitted; shim type/options would select a different mode.
func realReviewLoadedRuntimeMatches(overlay realReviewRootOverlay, registration system.RuntimeWithStatus) bool {
	return overlay.valid() && registration.Path == "/usr/bin/runsc" && slices.Equal(registration.Args, overlay.runscArgs()) && registration.Type == "" && len(registration.Options) == 0
}

func realReviewEffectiveRootMatches(overlay realReviewRootOverlay, proof realReviewRuntimeFilesystemProof) error {
	if !overlay.valid() || proof.RootOverlay != overlay || !proof.Verified || proof.CleanupFailed || proof.UnavailableReason != "" || proof.Root == nil || proof.Schema != "runsc_effective_root_v1" || proof.RuntimeVersion != realReviewFilesystemRelease || proof.GoferMountCount < 1 || proof.GoferMountCount > realReviewFilesystemMountLimit {
		return errors.New("effective runtime filesystem proof is unavailable or belongs to another selector")
	}
	root := proof.Root
	if root.Lower < 0 || root.Lower > 2 || root.Upper < 0 || root.Upper > 3 || root.Lower == 0 && root.Upper == 0 || root.LowerLabel != []string{"none", "lisafs", "erofs"}[root.Lower] || root.UpperLabel != []string{"none", "memory", "self", "anon"}[root.Upper] || root.SizeClassification != "default" && root.SizeClassification != "custom_size_present" {
		return errors.New("effective runtime filesystem proof contains an invalid sanitized root")
	}
	if overlay == realReviewRootOverlayNone && (proof.Root.Lower != 1 || proof.Root.Upper != 0 || proof.Root.LowerLabel != "lisafs" || proof.Root.UpperLabel != "none" || proof.Root.SizeClassification != "default") {
		return errors.New("effective runtime root differs from selected lisafs none default mode")
	}
	return nil
}

// This is called by the common Create path, so cold and snapshot-restored
// containers must pass the same proof even when helper profiling is disabled.
func verifyRealReviewCreatedFilesystem(overlay realReviewRootOverlay, profile bool, observe func() (realReviewRuntimeFilesystemProof, error), confirm func(*realReviewRuntimeFilesystemProof) error) (*realReviewRuntimeFilesystemProof, error) {
	if !overlay.valid() {
		return nil, errors.New("unsupported acceptance root overlay selector")
	}
	if !profile && overlay == realReviewRootOverlayDefault {
		return nil, nil
	}
	proof, err := observe()
	if err != nil {
		proof.Verified = false
		return &proof, errors.New("effective runtime filesystem observation failed")
	}
	if err := realReviewEffectiveRootMatches(overlay, proof); err != nil {
		proof.Verified, proof.UnavailableReason = false, "root_overlay_mismatch"
		return &proof, err
	}
	if err := confirm(&proof); err != nil {
		return &proof, err
	}
	return &proof, nil
}

type realReviewRuntimeContract struct {
	RootOverlaySelector        string   `json:"root_overlay_selector"`
	RunscPath                  string   `json:"runsc_path"`
	RunscArgs                  []string `json:"runsc_args"`
	EffectiveRootProofRequired bool     `json:"effective_root_proof_required"`
	AllCreationsVerified       bool     `json:"all_creations_verified"`
}

func realReviewAcceptedRuntimeContract(overlay realReviewRootOverlay, profile bool, created int64, resources []map[string]any) realReviewRuntimeContract {
	contract := realReviewRuntimeContract{RootOverlaySelector: overlay.label(), RunscPath: "/usr/bin/runsc", RunscArgs: overlay.runscArgs(), EffectiveRootProofRequired: profile || overlay == realReviewRootOverlayNone}
	if !overlay.valid() || created <= 0 || int64(len(resources)) != created {
		return contract
	}
	seen := map[string]bool{}
	for _, resource := range resources {
		id, ok := resource["container_id"].(string)
		if !ok || !regexp.MustCompile(`^[0-9a-f]{64}$`).MatchString(id) || seen[id] || resource["runtime"] != "runsc" {
			return contract
		}
		seen[id] = true
		kernel, ok := resource["kernel_cgroup_proof"].(realReviewKernelResourceProof)
		if !ok || !kernel.Verified || kernel.ContainerID != id || kernel.SandboxPID <= 0 {
			return contract
		}
		if contract.EffectiveRootProofRequired {
			proof, ok := resource["effective_runtime_filesystem"].(*realReviewRuntimeFilesystemProof)
			if !ok || proof == nil || proof.Schema != "runsc_effective_root_v1" || proof.RuntimeVersion != realReviewFilesystemRelease || proof.ContainerID != id || proof.SandboxPID != kernel.SandboxPID || realReviewEffectiveRootMatches(overlay, *proof) != nil {
				return contract
			}
		}
	}
	contract.AllCreationsVerified = true
	return contract
}
