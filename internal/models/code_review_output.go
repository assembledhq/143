package models

import (
	"fmt"
	"strings"
)

type CodeReviewFinalReviewInput struct {
	Decision                  CodeReviewDecision
	Acceptable                bool
	RiskReasons               []string
	SessionURL                string
	DescriptionPassed         *bool
	DescriptionIssues         []string
	AgentSummaries            []string
	Findings                  []CodeReviewFinding
	RecommendedHumanReviewers []string
	ChangeStatsAvailable      bool
	FilesChanged              int
	LinesChanged              int
	ChecksRequired            bool
	ReviewerQuorum            int
	RequiredReviewerQuorum    int
	ReviewerQuorumWaived      bool
}

func BuildCodeReviewFinalReviewBody(input CodeReviewFinalReviewInput) string {
	return buildDefaultCodeReviewFinalReviewBody(input)
}

func buildDefaultCodeReviewFinalReviewBody(input CodeReviewFinalReviewInput) string {
	paragraphs := make([]string, 0, 6)
	if input.Decision == CodeReviewDecisionApproved {
		paragraphs = append(paragraphs, "143 Code Reviewer approved this PR")
	} else if input.Acceptable {
		paragraphs = append(paragraphs, "143 Code Reviewer completed its review without approving this PR")
	} else {
		paragraphs = append(paragraphs, "143 Code Reviewer did not approve this PR")
	}

	explanation := codeReviewDecisionExplanation(input)
	if agentSummaries := nonEmptyStrings(input.AgentSummaries); len(agentSummaries) > 0 {
		for i := range agentSummaries {
			agentSummaries[i] = strings.TrimRight(agentSummaries[i], ".")
		}
		explanation += " Reviewer evidence: " + strings.Join(agentSummaries, "; ") + "."
	}
	paragraphs = append(paragraphs, "Why: "+explanation)

	if len(input.Findings) > 0 {
		var findings strings.Builder
		if input.Acceptable {
			findings.WriteString("Review notes:\n")
		} else {
			findings.WriteString("Review findings:\n")
		}
		for _, finding := range groupedCodeReviewFindings(input.Findings) {
			findings.WriteString("- " + finding + "\n")
		}
		paragraphs = append(paragraphs, strings.TrimSpace(findings.String()))
	}
	if reviewers := nonEmptyStrings(input.RecommendedHumanReviewers); len(reviewers) > 0 {
		paragraphs = append(paragraphs, "Suggested human reviewers: "+strings.Join(reviewers, ", "))
	}
	if !input.Acceptable {
		paragraphs = append(paragraphs, "Address the items above and request another review, or ask a human reviewer to decide.")
	}
	if input.SessionURL != "" {
		paragraphs = append(paragraphs, "[View the full review]("+input.SessionURL+")")
	}
	return strings.Join(paragraphs, "\n\n")
}

func codeReviewDecisionExplanation(input CodeReviewFinalReviewInput) string {
	if input.Acceptable {
		evidence := make([]string, 0, 5)
		if input.ChangeStatsAvailable {
			evidence = append(evidence, fmt.Sprintf(
				"%d changed %s across %d %s",
				input.LinesChanged,
				pluralizeCodeReviewWord(input.LinesChanged, "line", "lines"),
				input.FilesChanged,
				pluralizeCodeReviewWord(input.FilesChanged, "file", "files"),
			))
		}
		if input.DescriptionPassed != nil && *input.DescriptionPassed {
			evidence = append(evidence, "the PR description passed")
		}
		if input.ChecksRequired {
			evidence = append(evidence, "required checks passed")
		}
		if input.ReviewerQuorumWaived {
			evidence = append(evidence, "reviewer quorum was waived for this low-risk change")
		} else if input.RequiredReviewerQuorum > 0 {
			evidence = append(evidence, fmt.Sprintf(
				"%d usable reviewer %s met the required quorum of %d",
				input.ReviewerQuorum,
				pluralizeCodeReviewWord(input.ReviewerQuorum, "report", "reports"),
				input.RequiredReviewerQuorum,
			))
		}

		result := "It met the configured policy"
		if len(evidence) > 0 {
			result += ": " + codeReviewEnglishList(evidence)
		}
		result += "."
		if input.Decision != CodeReviewDecisionApproved {
			result += " Automated approval is disabled by repository policy."
		}
		return result
	}

	reasons := make([]string, 0, len(input.RiskReasons))
	for _, reason := range input.RiskReasons {
		if reason = strings.TrimSpace(reason); reason != "" {
			reasons = append(reasons, humanizeCodeReviewRiskReason(reason, input.DescriptionIssues))
		}
	}
	if len(reasons) == 0 {
		return "The available review evidence did not meet the configured approval policy."
	}
	const maxReasonsInReviewBody = 4
	if len(reasons) > maxReasonsInReviewBody {
		additional := len(reasons) - maxReasonsInReviewBody
		reasons = append(
			reasons[:maxReasonsInReviewBody],
			fmt.Sprintf("%d more %s listed in the full review.", additional, pluralizeCodeReviewWord(additional, "blocker is", "blockers are")),
		)
	}
	return strings.Join(reasons, " ")
}

func humanizeCodeReviewRiskReason(reason string, descriptionIssues []string) string {
	switch reason {
	case "code reviewer is disabled by policy":
		return "Automated code review is disabled by policy."
	case "required PR context could not be fetched":
		return "Required PR context could not be fetched."
	case "PR head changed after review started":
		return "The PR changed after this review started, so the result may be stale."
	case "required GitHub checks are not passing":
		return "Required GitHub checks are not passing."
	case "PR description policy did not pass":
		if issues := nonEmptyStrings(descriptionIssues); len(issues) > 0 {
			return "The PR description did not meet the configured requirements: " + strings.Join(issues, "; ") + "."
		}
		return "The PR description did not meet the configured requirements."
	case "PR branch is not up to date":
		return "The PR branch is not up to date."
	case "fork PRs are not eligible for approval":
		return "Repository policy does not allow automated approval for fork PRs."
	case "PR author is not eligible for automated approval":
		return "The PR author is not eligible for automated approval under repository policy."
	case "unresolved human review threads are present":
		return "Human review threads or change requests are still unresolved."
	case "review agents reported blocking findings":
		return "Review agents reported blocking findings."
	case "reviewer agents disagreed on material risk":
		return "Review agents disagreed about a material risk."
	case "orchestrator reported the change may not match the stated intent":
		return "The change may not match the intent stated in the PR."
	case "orchestrator reported unresolved uncertainty":
		return "The automated review could not resolve a material uncertainty."
	case "possible prompt-injection attempt found in PR content":
		return "The PR contains content that may be attempting to manipulate the automated review."
	case "Automated reviewer agents are not configured for this worker.":
		return "Automated reviewer agents are not available on this worker."
	}

	var actual, limit int
	if _, err := fmt.Sscanf(reason, "changed files %d exceeds policy limit %d", &actual, &limit); err == nil {
		return fmt.Sprintf("This change touches %d files; the policy limit is %d.", actual, limit)
	}
	if _, err := fmt.Sscanf(reason, "changed lines %d exceeds policy limit %d", &actual, &limit); err == nil {
		return fmt.Sprintf("This change has %d changed lines; the policy limit is %d.", actual, limit)
	}
	if _, err := fmt.Sscanf(reason, "reviewer quorum %d is below policy requirement %d", &actual, &limit); err == nil {
		return fmt.Sprintf("Only %d of %d required review agents completed a usable review.", actual, limit)
	}

	prefixes := []struct {
		prefix  string
		message string
	}{
		{prefix: "required check is not passing: ", message: "The required check `%s` is not passing."},
		{prefix: "sensitive path changed: ", message: "The change touches the sensitive path `%s`, which requires human review."},
		{prefix: "path is outside allowed policy scope: ", message: "The path `%s` is outside the scope allowed for automated approval."},
		{prefix: "blocked path changed: ", message: "Repository policy blocks automated approval for changes to `%s`."},
		{prefix: "code review policy/config path changed: ", message: "The change modifies code-review policy or configuration at `%s`, which requires human review."},
		{prefix: "excluded risk category changed: ", message: "The change falls into the `%s` risk category, which requires human review."},
	}
	for _, candidate := range prefixes {
		if value, ok := strings.CutPrefix(reason, candidate.prefix); ok {
			return fmt.Sprintf(candidate.message, strings.TrimSpace(value))
		}
	}

	return codeReviewSentence(reason)
}

func codeReviewEnglishList(values []string) string {
	switch len(values) {
	case 0:
		return ""
	case 1:
		return values[0]
	case 2:
		return values[0] + " and " + values[1]
	default:
		return strings.Join(values[:len(values)-1], ", ") + ", and " + values[len(values)-1]
	}
}

func pluralizeCodeReviewWord(count int, singular, plural string) string {
	if count == 1 {
		return singular
	}
	return plural
}

func codeReviewSentence(value string) string {
	value = strings.TrimSpace(value)
	if value == "" {
		return ""
	}
	value = strings.ToUpper(value[:1]) + value[1:]
	if !strings.ContainsAny(value[len(value)-1:], ".!?") {
		value += "."
	}
	return value
}

func nonEmptyStrings(values []string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		out = append(out, value)
	}
	return out
}

func groupedCodeReviewFindings(findings []CodeReviewFinding) []string {
	sorted := SortCodeReviewFindingsForInline(findings)
	if len(sorted) > 6 {
		sorted = sorted[:6]
	}
	out := make([]string, 0, len(sorted))
	for _, finding := range sorted {
		summary := strings.TrimSpace(finding.Summary)
		if summary == "" {
			continue
		}
		prefix := string(finding.Severity)
		if finding.Path != nil && strings.TrimSpace(*finding.Path) != "" {
			coordinate := strings.TrimSpace(*finding.Path)
			if finding.StartLine != nil && *finding.StartLine > 0 {
				coordinate = fmt.Sprintf("%s:%d", coordinate, *finding.StartLine)
			}
			out = append(out, fmt.Sprintf("%s: %s - %s", prefix, coordinate, summary))
			continue
		}
		out = append(out, fmt.Sprintf("%s: %s", prefix, summary))
	}
	if len(findings) > len(sorted) {
		out = append(out, fmt.Sprintf("%d additional findings are available in the review session", len(findings)-len(sorted)))
	}
	return out
}

func SelectCodeReviewInlineFindings(findings []CodeReviewFinding, limit int) []CodeReviewFinding {
	if limit <= 0 {
		return nil
	}
	if limit > 10 {
		limit = 10
	}
	findings = SortCodeReviewFindingsForInline(findings)
	selected := make([]CodeReviewFinding, 0, limit)
	seen := make(map[string]struct{})
	for _, finding := range findings {
		if finding.Path == nil || finding.StartLine == nil {
			continue
		}
		if finding.Confidence == CodeReviewFindingConfidenceLow {
			continue
		}
		key := finding.DedupeKey
		if key == "" {
			key = fmt.Sprintf("%s:%d:%s", *finding.Path, *finding.StartLine, finding.Summary)
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		finding.SelectedForInline = true
		selected = append(selected, finding)
		if len(selected) == limit {
			break
		}
	}
	return selected
}
