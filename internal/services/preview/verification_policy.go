package preview

import (
	"path/filepath"
	"sort"
	"strings"

	"github.com/assembledhq/143/internal/models"
)

type VerificationDecision struct {
	Due        bool
	SkipReason string
	Plan       []models.PreviewVerificationPlanStep
}

var userFacingExtensions = map[string]struct{}{
	".css": {}, ".gif": {}, ".html": {}, ".jpeg": {}, ".jpg": {}, ".js": {},
	".jsx": {}, ".mdx": {}, ".png": {}, ".scss": {}, ".svg": {}, ".ts": {}, ".tsx": {}, ".vue": {}, ".webp": {},
}

// PlanVerification makes the automatic trigger deterministic and conservative:
// false positives cost a bounded smoke run, while false negatives leave UI work unverified.
func PlanVerification(diff string, cfg models.PreviewVerificationConfig) VerificationDecision {
	if !cfg.Auto {
		return VerificationDecision{SkipReason: "automatic preview verification is disabled"}
	}
	if !hasUserFacingDiff(diff) {
		return VerificationDecision{SkipReason: "the workspace diff has no likely user-facing changes"}
	}

	paths := append([]string(nil), cfg.SmokePaths...)
	if len(paths) == 0 {
		paths = []string{"/"}
	}
	viewports := append([]models.ViewportSpec(nil), cfg.Viewports...)
	if len(viewports) == 0 {
		viewports = []models.ViewportSpec{{Width: 1440, Height: 900}}
	}
	sort.Strings(paths)
	plan := make([]models.PreviewVerificationPlanStep, 0, len(paths)*len(viewports))
	for _, path := range paths {
		for _, viewport := range viewports {
			plan = append(plan, models.PreviewVerificationPlanStep{Path: path, Viewport: viewport})
		}
	}
	return VerificationDecision{Due: true, Plan: plan}
}

func hasUserFacingDiff(diff string) bool {
	for _, line := range strings.Split(diff, "\n") {
		if !strings.HasPrefix(line, "+++ b/") {
			continue
		}
		name := strings.TrimPrefix(line, "+++ b/")
		if strings.Contains(name, "/migrations/") || strings.HasPrefix(name, "migrations/") || strings.HasSuffix(name, "_test.go") || strings.Contains(name, ".test.") {
			continue
		}
		if _, ok := userFacingExtensions[strings.ToLower(filepath.Ext(name))]; ok {
			return true
		}
	}
	return false
}
