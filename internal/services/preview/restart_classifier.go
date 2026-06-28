package preview

import (
	"path"
	"strings"

	"github.com/assembledhq/143/internal/models"
)

type PreviewRestartClassifier interface {
	Classify(paths []string) []models.PreviewRestartReason
	SelectUpdateMode(status models.PreviewStatus, freshness *models.PreviewFreshness, reloadBrowser, hmrCapable bool) models.PreviewUpdateMode
}

type DefaultPreviewRestartClassifier struct{}

func (DefaultPreviewRestartClassifier) Classify(paths []string) []models.PreviewRestartReason {
	reasons := make([]models.PreviewRestartReason, 0)
	seen := make(map[string]struct{})
	for _, raw := range paths {
		clean := strings.Trim(path.Clean(strings.TrimSpace(raw)), "/")
		if clean == "" || clean == "." {
			continue
		}
		kind, detail, ok := classifyPreviewRestartPath(clean)
		if !ok {
			continue
		}
		key := string(kind) + "\x00" + clean
		if _, exists := seen[key]; exists {
			continue
		}
		seen[key] = struct{}{}
		reasons = append(reasons, models.PreviewRestartReason{
			Kind:   kind,
			Path:   clean,
			Detail: detail,
		})
	}
	if len(reasons) == 0 {
		return nil
	}
	return reasons
}

// SelectUpdateMode chooses the cheapest safe lifecycle path to bring a preview
// up to date. hmrCapable reports whether the primary service hot-reloads source
// edits in place; it only influences the out-of-date branch, where an
// HMR-capable service can serve the latest source after a browser reload rather
// than paying a service restart. The caller is responsible for ensuring that
// only non-restart-requiring (source-only) changes reach the out-of-date state;
// config/lockfile/env changes surface as restart reasons and route to a full
// recycle before this branch is considered.
func (DefaultPreviewRestartClassifier) SelectUpdateMode(status models.PreviewStatus, freshness *models.PreviewFreshness, reloadBrowser, hmrCapable bool) models.PreviewUpdateMode {
	if freshness == nil || freshness.State == models.PreviewFreshnessUnknown {
		return models.PreviewUpdateModeColdRelaunch
	}
	switch status {
	case models.PreviewStatusStarting:
		return models.PreviewUpdateModeFullRecycle
	case models.PreviewStatusFailed, models.PreviewStatusStopped, models.PreviewStatusExpired, models.PreviewStatusUnavailable:
		return models.PreviewUpdateModeColdRelaunch
	}
	if freshness.RestartRequired || len(freshness.RestartReasons) > 0 {
		return models.PreviewUpdateModeFullRecycle
	}
	switch freshness.State {
	case models.PreviewFreshnessCurrent, models.PreviewFreshnessLiveUpdated:
		if reloadBrowser {
			return models.PreviewUpdateModeBrowserReload
		}
		return models.PreviewUpdateModeNoopCurrent
	case models.PreviewFreshnessOutOfDate:
		if hmrCapable {
			return models.PreviewUpdateModeBrowserReload
		}
		return models.PreviewUpdateModeSoftServiceRestart
	case models.PreviewFreshnessUpdating:
		return models.PreviewUpdateModeFullRecycle
	default:
		return models.PreviewUpdateModeColdRelaunch
	}
}

func classifyPreviewRestartPath(p string) (models.PreviewRestartReasonKind, string, bool) {
	base := path.Base(p)
	switch {
	case isDependencyPath(base):
		return models.PreviewRestartReasonDependencyChanged, "Dependencies changed. Restart to install and apply them.", true
	case p == ".143/config.json" || p == ".143/preview-start.sh":
		return models.PreviewRestartReasonPreviewConfigChanged, "Preview configuration changed. Restart to relaunch with the new config.", true
	case isBuildConfigPath(base):
		return models.PreviewRestartReasonBuildConfigChanged, "Build configuration changed. Restart to rebuild the preview server.", true
	case isEnvironmentConfigPath(p, base):
		return models.PreviewRestartReasonEnvironmentConfigChanged, "Environment configuration changed. Restart to reload runtime settings.", true
	case isDatabaseSchemaPath(p, base):
		return models.PreviewRestartReasonDatabaseSchemaChanged, "Database schema changed. Restart to rerun startup and migrations.", true
	default:
		return "", "", false
	}
}

func isDependencyPath(base string) bool {
	switch base {
	case "package.json", "package-lock.json", "npm-shrinkwrap.json",
		"pnpm-lock.yaml", "yarn.lock", "bun.lock", "bun.lockb",
		"go.mod", "go.sum", "pyproject.toml", "poetry.lock", "uv.lock",
		"Pipfile.lock", "pdm.lock":
		return true
	}
	return strings.HasPrefix(base, "requirements") && strings.HasSuffix(base, ".txt")
}

func isBuildConfigPath(base string) bool {
	if base == "turbo.json" || base == "tsconfig.json" {
		return true
	}
	for _, prefix := range []string{"next.config.", "vite.config.", "webpack.config."} {
		if strings.HasPrefix(base, prefix) {
			return true
		}
	}
	return false
}

func isEnvironmentConfigPath(p, base string) bool {
	return base == ".env.example" ||
		base == ".env.local.example" ||
		strings.HasPrefix(p, "config/")
}

func isDatabaseSchemaPath(p, base string) bool {
	return strings.HasPrefix(p, "migrations/") ||
		strings.HasPrefix(p, "db/migrations/") ||
		base == "schema.sql" ||
		p == "prisma/schema.prisma"
}

func ChangedPathsFromUnifiedDiff(diff string) []string {
	paths := make([]string, 0)
	seen := make(map[string]struct{})
	for _, line := range strings.Split(diff, "\n") {
		if !strings.HasPrefix(line, "diff --git ") {
			continue
		}
		parts := strings.Fields(line)
		if len(parts) < 4 {
			continue
		}
		for _, candidate := range []string{parts[2], parts[3]} {
			clean := strings.TrimPrefix(candidate, "a/")
			clean = strings.TrimPrefix(clean, "b/")
			clean = strings.Trim(path.Clean(clean), "/")
			if clean == "" || clean == "." || clean == "/dev/null" {
				continue
			}
			if _, exists := seen[clean]; exists {
				continue
			}
			seen[clean] = struct{}{}
			paths = append(paths, clean)
		}
	}
	return paths
}
