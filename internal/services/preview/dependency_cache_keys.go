package preview

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"path/filepath"
	"sort"
	"strings"

	"github.com/google/uuid"

	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/agent"
)

const PreviewDependencyCacheRuntimeVersion = "preview-dependency-cache-v1"

type PreviewInstallLockfileKey struct {
	Path   string `json:"path"`
	SHA256 string `json:"sha256"`
}

type previewDependencyCacheKey struct {
	RuntimeVersion  string                      `json:"runtime_version"`
	SandboxProvider string                      `json:"sandbox_provider,omitempty"`
	SandboxImage    string                      `json:"sandbox_image,omitempty"`
	InstallCommand  []string                    `json:"install_command"`
	InstallCwd      string                      `json:"install_cwd"`
	Lockfiles       []PreviewInstallLockfileKey `json:"lockfiles"`
	EffectivePaths  []string                    `json:"effective_paths"`
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
		RuntimeVersion: PreviewDependencyCacheRuntimeVersion,
		InstallCommand: append([]string(nil), install.Command...),
		InstallCwd:     cwd,
		Lockfiles:      lockfiles,
		EffectivePaths: sortedNormalizedDependencyPaths(effectivePaths),
	}
	if sb != nil {
		payload.SandboxProvider = sb.Provider
		if sb.Metadata != nil {
			payload.SandboxImage = sb.Metadata["image"]
		}
	}
	key, err := stableJSONSHA256(payload)
	if err != nil {
		return "", nil, fmt.Errorf("marshal dependency cache key: %w", err)
	}
	return key, lockfiles, nil
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
	if clean == ".143/cache/preview-install" || strings.HasPrefix(clean, ".143/cache/preview-install/") {
		return "", fmt.Errorf("path must not target preview install markers")
	}
	return clean, nil
}
