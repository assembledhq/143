package preview

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/google/uuid"

	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/agent"
)

const PreviewDependencyCacheRuntimeVersion = "preview-dependency-cache-v1"
const PreviewPackageManagerCacheRuntimeVersion = "preview-package-manager-cache-v1"
const PreviewBuildCacheRuntimeVersion = "preview-build-cache-v1"

// PreviewBuildCacheHomeRuntimeVersion keys the HOME-rooted build cache (Go's
// GOCACHE/GOMODCACHE). It is distinct from the workdir build cache version so
// the two blobs occupy separate slots under the same build_artifact kind and
// never overwrite each other.
const PreviewBuildCacheHomeRuntimeVersion = "preview-build-cache-home-v1"

type PreviewInstallLockfileKey struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

type previewDependencyCacheKey struct {
	RuntimeVersion  string                      `json:"runtime_version"`
	SandboxProvider string                      `json:"sandbox_provider,omitempty"`
	SandboxCacheABI string                      `json:"sandbox_cache_abi,omitempty"`
	Kind            models.PreviewCacheKind     `json:"kind,omitempty"`
	InstallCommand  []string                    `json:"install_command"`
	InstallCwd      string                      `json:"install_cwd"`
	Lockfiles       []PreviewInstallLockfileKey `json:"lockfiles"`
	EffectivePaths  []string                    `json:"effective_paths"`
	PackageManagers []string                    `json:"package_managers,omitempty"`
}

type previewDependencyCachePlacementKey struct {
	RuntimeVersion string    `json:"runtime_version"`
	OrgID          uuid.UUID `json:"org_id"`
	RepoID         uuid.UUID `json:"repo_id"`
	ConfigName     string    `json:"config_name,omitempty"`
	ConfigDigest   string    `json:"config_digest,omitempty"`
	InstallCommand []string  `json:"install_command,omitempty"`
	InstallCwd     string    `json:"install_cwd,omitempty"`
	LockfilePaths  []string  `json:"lockfile_paths,omitempty"`
	EffectivePaths []string  `json:"effective_paths,omitempty"`
}

type dependencyCacheKeyReader interface {
	ReadFile(ctx context.Context, sb *agent.Sandbox, path string) ([]byte, error)
}

func ComputePreviewDependencyCacheKey(ctx context.Context, executor dependencyCacheKeyReader, sb *agent.Sandbox, install *models.PreviewInstallConfig, effectivePaths []string) (string, []PreviewInstallLockfileKey, error) {
	return computePreviewPathCacheKey(ctx, executor, sb, install, PreviewDependencyCacheRuntimeVersion, models.PreviewCacheKindInstallArtifact, effectivePaths, nil)
}

func ComputePreviewPackageManagerCacheKey(ctx context.Context, executor dependencyCacheKeyReader, sb *agent.Sandbox, install *models.PreviewInstallConfig, homeRelativePaths []string, packageManagers []string) (string, []PreviewInstallLockfileKey, error) {
	return computePreviewPathCacheKey(ctx, executor, sb, install, PreviewPackageManagerCacheRuntimeVersion, models.PreviewCacheKindPackageManager, homeRelativePaths, packageManagers)
}

func computePreviewPathCacheKey(ctx context.Context, executor dependencyCacheKeyReader, sb *agent.Sandbox, install *models.PreviewInstallConfig, runtimeVersion string, kind models.PreviewCacheKind, effectivePaths []string, packageManagers []string) (string, []PreviewInstallLockfileKey, error) {
	if executor == nil {
		return "", nil, fmt.Errorf("dependency cache key: executor is required")
	}
	if install == nil {
		return "", nil, fmt.Errorf("dependency cache key: install config is required")
	}
	lockfiles := make([]PreviewInstallLockfileKey, 0, len(install.Lockfiles))
	for _, lockfile := range install.Lockfiles {
		cleanPath, err := cleanDependencyCacheRepoPath(lockfile, false)
		if err != nil {
			return "", nil, fmt.Errorf("preview.install.lockfiles path %q: %w", lockfile, err)
		}
		body, err := executor.ReadFile(ctx, sb, cleanPath)
		if err != nil {
			return "", nil, fmt.Errorf("read preview.install lockfile %q: %w", cleanPath, err)
		}
		sum := sha256.Sum256(body)
		lockfiles = append(lockfiles, PreviewInstallLockfileKey{Path: cleanPath, SHA256: fmt.Sprintf("%x", sum[:])})
	}
	sort.Slice(lockfiles, func(i, j int) bool { return lockfiles[i].Path < lockfiles[j].Path })
	cwd := install.Cwd
	if cwd == "" {
		cwd = "."
	}
	if cwd != "." {
		if _, err := cleanDependencyCacheRepoPath(cwd, false); err != nil {
			return "", nil, fmt.Errorf("preview.install.cwd %q: %w", cwd, err)
		}
	}
	payload := previewDependencyCacheKey{
		RuntimeVersion:  runtimeVersion,
		Kind:            kind,
		InstallCommand:  append([]string(nil), install.Command...),
		InstallCwd:      cwd,
		Lockfiles:       lockfiles,
		EffectivePaths:  sortedNormalizedDependencyPaths(effectivePaths),
		PackageManagers: sortedNormalizedDependencyPaths(packageManagers),
	}
	if sb != nil {
		payload.SandboxProvider = sb.Provider
		if sb.Metadata != nil {
			payload.SandboxCacheABI = SandboxCacheABIFromMetadata(sb.Metadata)
		}
	}
	key, err := stableJSONSHA256(payload)
	if err != nil {
		return "", nil, fmt.Errorf("marshal dependency cache key: %w", err)
	}
	return key, lockfiles, nil
}

func SandboxCacheABIFromMetadata(metadata map[string]string) string {
	if metadata == nil {
		return ""
	}
	if abi := strings.TrimSpace(metadata[agent.SandboxMetadataCacheABI]); abi != "" {
		return abi
	}
	if image := strings.TrimSpace(metadata["image"]); image != "" {
		return "legacy-image:" + image
	}
	return ""
}

func ComputePreviewDependencyCachePlacementKey(orgID, repoID uuid.UUID, configName, configDigest string, install *models.PreviewInstallConfig, effectivePaths []string) (string, error) {
	payload := previewDependencyCachePlacementKey{
		RuntimeVersion: PreviewDependencyCacheRuntimeVersion,
		OrgID:          orgID,
		RepoID:         repoID,
		ConfigName:     strings.TrimSpace(configName),
		ConfigDigest:   strings.TrimSpace(configDigest),
		EffectivePaths: sortedNormalizedDependencyPaths(effectivePaths),
	}
	if install != nil {
		payload.InstallCommand = append([]string(nil), install.Command...)
		payload.InstallCwd = install.Cwd
		if payload.InstallCwd == "" {
			payload.InstallCwd = "."
		}
		payload.LockfilePaths = sortedNormalizedDependencyPaths(install.Lockfiles)
	}
	key, err := stableJSONSHA256(payload)
	if err != nil {
		return "", fmt.Errorf("marshal dependency cache placement key: %w", err)
	}
	return key, nil
}

// ComputePreviewBuildCacheKey returns the latest-wins key for the
// build-artifact cache. Unlike install-artifact keys it deliberately excludes
// lockfile contents and sandbox identity: build tools (e.g. turbo) content-hash
// their own cache entries, so a single blob per (org, repo, config, install,
// paths) slot that is overwritten after every ready preview maximizes hit rate,
// and a stale blob degrades to partial build-tool hits rather than wrong
// output.
func ComputePreviewBuildCacheKey(orgID, repoID uuid.UUID, configName, configDigest string, install *models.PreviewInstallConfig, effectivePaths []string) (string, error) {
	key, err := stableJSONSHA256(newPreviewBuildCachePlacementPayload(PreviewBuildCacheRuntimeVersion, orgID, repoID, configName, configDigest, install, effectivePaths))
	if err != nil {
		return "", fmt.Errorf("marshal build cache key: %w", err)
	}
	return key, nil
}

// newPreviewBuildCachePlacementPayload builds the canonical placement-key
// payload shared by the workdir and home build-artifact slots. Centralizing it
// guarantees that ComputePreviewBuildCacheKey, ComputePreviewBuildCacheHomeKey,
// and PreviewBuildCacheKeyDebug all hash byte-identical bytes.
func newPreviewBuildCachePlacementPayload(runtimeVersion string, orgID, repoID uuid.UUID, configName, configDigest string, install *models.PreviewInstallConfig, effectivePaths []string) previewDependencyCachePlacementKey {
	payload := previewDependencyCachePlacementKey{
		RuntimeVersion: runtimeVersion,
		OrgID:          orgID,
		RepoID:         repoID,
		ConfigName:     strings.TrimSpace(configName),
		ConfigDigest:   strings.TrimSpace(configDigest),
		EffectivePaths: sortedNormalizedDependencyPaths(effectivePaths),
	}
	if install != nil {
		payload.InstallCommand = append([]string(nil), install.Command...)
		payload.InstallCwd = install.Cwd
		if payload.InstallCwd == "" {
			payload.InstallCwd = "."
		}
		payload.LockfilePaths = sortedNormalizedDependencyPaths(install.Lockfiles)
	}
	return payload
}

// PreviewBuildCacheKeyDebug recomputes a build-artifact placement key and also
// returns the exact canonical JSON payload that is SHA-256'd to produce it.
//
// TEMPORARY INSTRUMENTATION: this exists only to diagnose build-cache key
// instability across launches. Because it shares newPreviewBuildCachePlacementPayload
// and the same json.Marshal as the production key functions, the returned key is
// byte-identical to ComputePreviewBuildCacheKey/ComputePreviewBuildCacheHomeKey,
// and payloadJSON is exactly the bytes that were hashed — diff payloadJSON across
// two launches to pinpoint, in a single pass, which field changed. Pass the
// runtime-version constant for the slot under diagnosis
// (PreviewBuildCacheRuntimeVersion for workdir, PreviewBuildCacheHomeRuntimeVersion
// for home). Safe to delete once the unstable input is identified.
func PreviewBuildCacheKeyDebug(runtimeVersion string, orgID, repoID uuid.UUID, configName, configDigest string, install *models.PreviewInstallConfig, effectivePaths []string) (key string, payloadJSON string, err error) {
	payload := newPreviewBuildCachePlacementPayload(runtimeVersion, orgID, repoID, configName, configDigest, install, effectivePaths)
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", "", fmt.Errorf("marshal build cache debug payload: %w", err)
	}
	sum := sha256.Sum256(raw)
	return fmt.Sprintf("%x", sum[:]), string(raw), nil
}

// ComputePreviewBuildCacheHomeKey returns the latest-wins key for the
// HOME-rooted build-artifact cache (Go's GOCACHE/GOMODCACHE). It mirrors
// ComputePreviewBuildCacheKey but uses a distinct runtime version so the
// home-rooted blob occupies its own slot, separate from the workdir build
// blob, within the build_artifact cache kind.
func ComputePreviewBuildCacheHomeKey(orgID, repoID uuid.UUID, configName, configDigest string, install *models.PreviewInstallConfig, effectivePaths []string) (string, error) {
	key, err := stableJSONSHA256(newPreviewBuildCachePlacementPayload(PreviewBuildCacheHomeRuntimeVersion, orgID, repoID, configName, configDigest, install, effectivePaths))
	if err != nil {
		return "", fmt.Errorf("marshal build cache home key: %w", err)
	}
	return key, nil
}

func ComputePreviewDependencyCacheRepoPlacementKey(orgID, repoID uuid.UUID) (string, error) {
	return ComputePreviewDependencyCachePlacementKey(orgID, repoID, "", "", nil, nil)
}

func ComputePreviewConfigDigest(config *models.PreviewConfig) (string, error) {
	if config == nil {
		return "", nil
	}
	return stableJSONSHA256(config)
}

func stableJSONSHA256(v any) (string, error) {
	payload, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(payload)
	return fmt.Sprintf("%x", sum[:]), nil
}

func sortedNormalizedDependencyPaths(values []string) []string {
	out := make([]string, 0, len(values))
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		clean := filepath.ToSlash(filepath.Clean(strings.TrimSpace(value)))
		if clean == "" || clean == "." {
			continue
		}
		if _, ok := seen[clean]; ok {
			continue
		}
		seen[clean] = struct{}{}
		out = append(out, clean)
	}
	sort.Strings(out)
	return out
}

func cleanDependencyCacheRepoPath(raw string, allowGlob bool) (string, error) {
	if strings.TrimSpace(raw) == "" {
		return "", fmt.Errorf("empty path")
	}
	if strings.HasPrefix(raw, "/") {
		return "", fmt.Errorf("absolute paths are not allowed")
	}
	if strings.ContainsAny(raw, " \t\r\n;&|`$(){}[]<>!?\\\"'") {
		return "", fmt.Errorf("unsupported shell metacharacter")
	}
	if !allowGlob && strings.Contains(raw, "*") {
		return "", fmt.Errorf("glob paths are not allowed here")
	}
	clean := filepath.ToSlash(filepath.Clean(raw))
	if clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("path escapes the repo root")
	}
	if allowGlob {
		for _, part := range strings.Split(clean, "/") {
			if part == ".." {
				return "", fmt.Errorf("path escapes the repo root")
			}
		}
	}
	if clean == "." {
		return "", fmt.Errorf("path is too broad")
	}
	if clean == ".git" || strings.HasPrefix(clean, ".git/") {
		return "", fmt.Errorf("path must not target .git")
	}
	if dependencyCachePathTargetsPreviewInstallMarkers(clean) {
		return "", fmt.Errorf("path must not target preview install markers")
	}
	if dependencyCachePathTargetsPlatformCache(clean) {
		return "", fmt.Errorf("path must not target platform preview cache")
	}
	return clean, nil
}

func dependencyCachePathTargetsPreviewInstallMarkers(clean string) bool {
	const marker = ".143/cache/preview-install"
	clean = filepath.ToSlash(filepath.Clean(strings.TrimSpace(clean)))
	if clean == "" || clean == "." {
		return false
	}
	if clean == marker || strings.HasPrefix(clean, marker+"/") {
		return true
	}
	if !strings.Contains(clean, "*") {
		return strings.HasPrefix(marker, clean+"/")
	}
	return dependencyCacheGlobTargetsPathOrDescendant(clean, marker)
}

func dependencyCachePathTargetsPlatformCache(clean string) bool {
	const platformCache = ".143/cache"
	clean = filepath.ToSlash(filepath.Clean(strings.TrimSpace(clean)))
	if clean == "" || clean == "." {
		return false
	}
	if clean == platformCache || strings.HasPrefix(clean, platformCache+"/") {
		return true
	}
	if !strings.Contains(clean, "*") {
		return strings.HasPrefix(platformCache, clean+"/")
	}
	return dependencyCacheGlobTargetsPathOrDescendant(clean, platformCache)
}

func dependencyCacheGlobTargetsPathOrDescendant(pattern, target string) bool {
	patternParts := strings.Split(pattern, "/")
	targetParts := strings.Split(target, "/")
	if len(patternParts) <= len(targetParts) {
		for i, patternPart := range patternParts {
			ok, err := path.Match(patternPart, targetParts[i])
			if err != nil || !ok {
				return false
			}
		}
		return true
	}
	for i, targetPart := range targetParts {
		ok, err := path.Match(patternParts[i], targetPart)
		if err != nil || !ok {
			return false
		}
	}
	for _, patternPart := range patternParts[len(targetParts):] {
		if !dependencyCacheGlobSegmentCanMatchNonEmpty(patternPart) {
			return false
		}
	}
	return true
}

func dependencyCacheGlobSegmentCanMatchNonEmpty(pattern string) bool {
	candidate := strings.ReplaceAll(pattern, "*", "x")
	if candidate == "" {
		candidate = "x"
	}
	ok, err := path.Match(pattern, candidate)
	return err == nil && ok
}
