package codereview

import (
	"path/filepath"
	"regexp"
	"strings"

	"github.com/assembledhq/143/internal/models"
)

// ApplicableDescriptionRequirements matches the full review's deterministic
// requirement selection. Admission must inspect exactly the set for which the
// validated orchestrator produced assessments.
func ApplicableDescriptionRequirements(policy models.CodeReviewPolicyConfig, files []PullRequestFile) []models.CodeReviewDescriptionRequirement {
	policy = models.ResolveCodeReviewPolicyConfig(&policy)
	result := make([]models.CodeReviewDescriptionRequirement, 0, len(policy.DescriptionPolicy.Requirements))
	for _, r := range policy.DescriptionPolicy.Requirements {
		if !r.Required || strings.TrimSpace(r.Key) == "" {
			continue
		}
		if descriptionRequirementApplies(r, files) {
			result = append(result, r)
		}
	}
	return result
}

func descriptionRequirementApplies(r models.CodeReviewDescriptionRequirement, files []PullRequestFile) bool {
	if r.AppliesWhen.Empty() {
		switch strings.ToLower(strings.TrimSpace(r.Applicability)) {
		case "nontrivial":
			return len(files) > 1 || changedLines(files) > 30
		default:
			return true
		}
	}
	a := r.AppliesWhen
	if a.MinFilesChanged > 0 && len(files) >= a.MinFilesChanged {
		return true
	}
	if a.MinLinesChanged > 0 && changedLines(files) >= a.MinLinesChanged {
		return true
	}
	if len(a.PathPatterns) > 0 {
		for _, f := range files {
			if descriptionPathMatchesAny(f.Filename, a.PathPatterns) {
				return true
			}
		}
	}
	switch a.Kind {
	case "", models.CodeReviewDescriptionApplicabilityAll:
		return true
	case models.CodeReviewDescriptionApplicabilityNontrivial:
		return len(files) > 1 || changedLines(files) > 30
	case models.CodeReviewDescriptionApplicabilityPaths:
		return len(a.PathPatterns) == 0
	default:
		return true
	}
}

func changedLines(files []PullRequestFile) int {
	n := 0
	for _, f := range files {
		n += f.Additions + f.Deletions
	}
	return n
}

func descriptionPathMatchesAny(path string, patterns []string) bool {
	path = filepath.ToSlash(strings.ToLower(strings.TrimSpace(path)))
	for _, pattern := range patterns {
		pattern = filepath.ToSlash(strings.ToLower(strings.TrimSpace(pattern)))
		if pattern == "" {
			continue
		}
		if ok, err := filepath.Match(pattern, path); err == nil && ok {
			return true
		}
		if strings.Contains(pattern, "**") {
			re := regexp.QuoteMeta(pattern)
			re = strings.ReplaceAll(re, `\*\*`, `.*`)
			re = strings.ReplaceAll(re, `\*`, `[^/]*`)
			if ok, err := regexp.MatchString("^"+re+"$", path); err == nil && ok {
				return true
			}
		}
		trimmed := strings.TrimSuffix(pattern, "/**")
		if trimmed != pattern && (path == trimmed || strings.HasPrefix(path, trimmed+"/")) {
			return true
		}
		if path == pattern || strings.HasPrefix(path, pattern+"/") || strings.HasPrefix(path, pattern) {
			return true
		}
	}
	return false
}
