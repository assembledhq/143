package models

import (
	"fmt"
	"strings"
)

type CodeReviewFinalReviewInput struct {
	Decision                  CodeReviewDecision
	Acceptable                bool
	RiskReasons               []CodeReviewRiskReason
	GeneratedSummary          string
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
	paragraphs := make([]string, 0, 9)
	if input.Decision == CodeReviewDecisionApproved {
		paragraphs = append(paragraphs, "143 Code Reviewer approved this PR")
	} else if input.Acceptable {
		paragraphs = append(paragraphs, "143 Code Reviewer completed its review without approving this PR")
	} else {
		paragraphs = append(paragraphs, "143 Code Reviewer did not approve this PR")
	}

	generatedSummary := codeReviewGeneratedSummary(input.GeneratedSummary)
	explanation := generatedSummary
	if explanation == "" {
		explanation = codeReviewDecisionExplanation(input)
	}
	paragraphs = append(paragraphs, "Why: "+explanation)

	if generatedSummary != "" && !input.Acceptable {
		if blockers := codeReviewRiskReasonExplanations(input.RiskReasons, input.DescriptionIssues); len(blockers) > 0 {
			var policyBlockers strings.Builder
			policyBlockers.WriteString("Policy blockers:\n")
			for _, blocker := range blockers {
				policyBlockers.WriteString("- " + blocker + "\n")
			}
			paragraphs = append(paragraphs, strings.TrimSpace(policyBlockers.String()))
		}
	}

	if generatedSummary != "" {
		if facts := codeReviewFacts(input); len(facts) > 0 {
			paragraphs = append(paragraphs, "Review facts: "+strings.Join(facts, " · "))
		}
	}
	if agentSummaries := nonEmptyStrings(input.AgentSummaries); len(agentSummaries) > 0 {
		for i := range agentSummaries {
			agentSummaries[i] = strings.TrimRight(agentSummaries[i], ".")
		}
		paragraphs = append(paragraphs, "Reviewer evidence: "+strings.Join(agentSummaries, "; ")+".")
	}

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
	if !input.Acceptable && generatedSummary == "" {
		paragraphs = append(paragraphs, "Address the items above and request another review, or ask a human reviewer to decide.")
	}
	if input.SessionURL != "" {
		paragraphs = append(paragraphs, "[View the full review]("+input.SessionURL+")")
	}
	return strings.Join(paragraphs, "\n\n")
}

func codeReviewGeneratedSummary(value string) string {
	return strings.Join(strings.Fields(value), " ")
}

func codeReviewFacts(input CodeReviewFinalReviewInput) []string {
	facts := make([]string, 0, 3)
	if input.ChangeStatsAvailable {
		facts = append(facts, fmt.Sprintf(
			"%d changed %s across %d %s",
			input.LinesChanged,
			pluralizeCodeReviewWord(input.LinesChanged, "line", "lines"),
			input.FilesChanged,
			pluralizeCodeReviewWord(input.FilesChanged, "file", "files"),
		))
	}
	if input.Acceptable && input.ChecksRequired {
		facts = append(facts, "required checks passed")
	}
	if input.Acceptable && input.ReviewerQuorumWaived {
		facts = append(facts, "reviewer quorum waived for this low-risk change")
	} else if input.Acceptable && input.RequiredReviewerQuorum > 0 {
		facts = append(facts, fmt.Sprintf("reviewer quorum %d/%d", input.ReviewerQuorum, input.RequiredReviewerQuorum))
	}
	return facts
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
			result += " Automated approval is disabled by organization policy."
		}
		return result
	}

	reasons := codeReviewRiskReasonExplanations(input.RiskReasons, input.DescriptionIssues)
	if len(reasons) == 0 {
		return "The available review evidence did not meet the configured approval policy."
	}
	return strings.Join(reasons, " ")
}

func codeReviewRiskReasonExplanations(reasons []CodeReviewRiskReason, descriptionIssues []string) []string {
	explanations := make([]string, 0, len(reasons))
	for _, reason := range reasons {
		if explanation := humanizeCodeReviewRiskReason(reason, descriptionIssues); explanation != "" {
			explanations = append(explanations, explanation)
		}
	}
	const maxReasonsInReviewBody = 4
	if len(explanations) > maxReasonsInReviewBody {
		additional := len(explanations) - maxReasonsInReviewBody
		explanations = append(
			explanations[:maxReasonsInReviewBody],
			fmt.Sprintf("%d more %s listed in the full review.", additional, pluralizeCodeReviewWord(additional, "blocker is", "blockers are")),
		)
	}
	return explanations
}

func humanizeCodeReviewRiskReason(reason CodeReviewRiskReason, descriptionIssues []string) string {
	switch reason.Code {
	case CodeReviewRiskReasonReviewerDisabled:
		return "Automated code review is disabled by policy."
	case CodeReviewRiskReasonContextUnavailable:
		return "Required PR context could not be fetched."
	case CodeReviewRiskReasonHeadChanged:
		return "The PR changed after this review started, so the result may be stale."
	case CodeReviewRiskReasonFilesLimitExceeded:
		return fmt.Sprintf("This change touches %d files; the policy limit is %d.", reason.Actual, reason.Limit)
	case CodeReviewRiskReasonLinesLimitExceeded:
		return fmt.Sprintf("This change has %d changed lines; the policy limit is %d.", reason.Actual, reason.Limit)
	case CodeReviewRiskReasonChecksFailing:
		return "Required GitHub checks are not passing."
	case CodeReviewRiskReasonRequiredCheckFailing:
		return fmt.Sprintf("The required check `%s` is not passing.", reason.Subject)
	case CodeReviewRiskReasonDescriptionFailed:
		if issues := nonEmptyStrings(descriptionIssues); len(issues) > 0 {
			return "The PR description did not meet the configured requirements: " + strings.Join(issues, "; ") + "."
		}
		return "The PR description did not meet the configured requirements."
	case CodeReviewRiskReasonBranchOutOfDate:
		return "The PR branch is not up to date."
	case CodeReviewRiskReasonForkIneligible:
		return "Repository policy does not allow automated approval for fork PRs."
	case CodeReviewRiskReasonAuthorIneligible:
		return "The PR author is not eligible for automated approval under organization policy."
	case CodeReviewRiskReasonUnresolvedHumanReview:
		return "Human review threads or change requests are still unresolved."
	case CodeReviewRiskReasonBlockingFindings:
		return "Review agents reported blocking findings."
	case CodeReviewRiskReasonReviewerDisagreement:
		return "Review agents disagreed about a material risk."
	case CodeReviewRiskReasonScopeMismatch:
		return "The change may not match the intent stated in the PR."
	case CodeReviewRiskReasonUnresolvedUncertainty:
		return "The automated review could not resolve a material uncertainty."
	case CodeReviewRiskReasonPromptInjection:
		return "The PR contains content that may be attempting to manipulate the automated review."
	case CodeReviewRiskReasonSensitivePath:
		return fmt.Sprintf("The change touches the sensitive path `%s`, which requires human review.", reason.Subject)
	case CodeReviewRiskReasonPathOutsideScope:
		return fmt.Sprintf("The path `%s` is outside the scope allowed for automated approval.", reason.Subject)
	case CodeReviewRiskReasonBlockedPath:
		return fmt.Sprintf("Repository policy blocks automated approval for changes to `%s`.", reason.Subject)
	case CodeReviewRiskReasonPolicyPathChanged:
		return fmt.Sprintf("The change modifies code-review policy or configuration at `%s`, which requires human review.", reason.Subject)
	case CodeReviewRiskReasonExcludedCategory:
		return fmt.Sprintf("The change falls into the `%s` risk category, which requires human review.", reason.Subject)
	case CodeReviewRiskReasonReviewerQuorum:
		return fmt.Sprintf("Only %d of %d required review agents completed a usable review.", reason.Actual, reason.Limit)
	case CodeReviewRiskReasonOrchestratorSynthesisInvalid:
		return "The orchestrator did not produce a valid structured synthesis."
	}

	return codeReviewSentence(reason.Message())
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
