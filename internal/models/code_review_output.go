package models

import (
	"fmt"
	"strings"
	"time"
)

type CodeReviewFinalReviewInput struct {
	Decision                  CodeReviewDecision
	Acceptable                bool
	RiskReasons               []CodeReviewRiskReason
	GeneratedSummary          string
	ChangeSummary             string
	OperationalSummary        string
	SessionURL                string
	PolicySettingsURL         string
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
	HeadSHA                   string
	AssessedAt                time.Time
}

func BuildCodeReviewFinalReviewBody(input CodeReviewFinalReviewInput) string {
	return buildDefaultCodeReviewFinalReviewBody(input)
}

func buildDefaultCodeReviewFinalReviewBody(input CodeReviewFinalReviewInput) string {
	paragraphs := make([]string, 0, 9)
	if input.Decision == CodeReviewDecisionApproved {
		paragraphs = append(paragraphs, "✅ **143 Code Reviewer approved this PR**")
	} else if input.Acceptable {
		paragraphs = append(paragraphs, "❌ **143 Code Reviewer completed its review without approving this PR**")
	} else {
		paragraphs = append(paragraphs, "❌ **143 Code Reviewer needs human review**")
	}

	generatedSummary := codeReviewGeneratedSummary(input.GeneratedSummary)
	operationalSummary := codeReviewGeneratedSummary(input.OperationalSummary)
	explanation := operationalSummary
	if explanation == "" {
		explanation = generatedSummary
	}
	if explanation == "" {
		explanation = codeReviewDecisionExplanation(input)
	}
	paragraphs = append(paragraphs, "**Why:** "+explanation)

	if changeSummary := codeReviewGeneratedSummary(input.ChangeSummary); changeSummary != "" {
		paragraphs = append(paragraphs, "**Change:** "+changeSummary)
	}

	if !input.Acceptable {
		paragraphs = append(paragraphs, codeReviewBlockerSections(input)...)
		if issues := codeReviewReviewIssueSection(input.RiskReasons, input.DescriptionIssues); issues != "" {
			paragraphs = append(paragraphs, issues)
		}
	}

	if generatedSummary != "" && operationalSummary == "" {
		if facts := codeReviewFacts(input); len(facts) > 0 {
			paragraphs = append(paragraphs, "**Review facts:** "+strings.Join(facts, " · "))
		}
	}
	if agentSummaries := nonEmptyStrings(input.AgentSummaries); len(agentSummaries) > 0 {
		for i := range agentSummaries {
			agentSummaries[i] = strings.TrimRight(agentSummaries[i], ".")
		}
		paragraphs = append(paragraphs, "**Reviewer evidence:** "+strings.Join(agentSummaries, "; ")+".")
	}

	blockingFindings, advisoryFindings := partitionCodeReviewFindings(input.Findings)
	if len(blockingFindings) > 0 {
		var findings strings.Builder
		findings.WriteString("**Blocking findings:**\n")
		for _, finding := range groupedCodeReviewFindings(blockingFindings) {
			findings.WriteString("- " + finding + "\n")
		}
		paragraphs = append(paragraphs, strings.TrimSpace(findings.String()))
	}
	if len(advisoryFindings) > 0 {
		paragraphs = append(paragraphs, codeReviewAdvisoryFindingsSection(advisoryFindings))
	}
	if reviewers := nonEmptyStrings(input.RecommendedHumanReviewers); len(reviewers) > 0 {
		paragraphs = append(paragraphs, "**Suggested human reviewers:** "+strings.Join(reviewers, ", "))
	}
	if !input.Acceptable {
		if codeReviewBlockerCount(input.RiskReasons, blockingFindings) == 1 {
			if revision := codeReviewShortSHA(input.HeadSHA); revision != "" {
				paragraphs = append(paragraphs, "This is the only blocker as of `"+revision+"`.")
			}
		}
		if operationalSummary != "" {
			paragraphs = append(paragraphs, "**Next steps:** Retry the automated review to regenerate the final synthesis, or ask a human reviewer to review the available evidence directly.")
		} else {
			paragraphs = append(paragraphs, "**Next steps:** Review the explanation and evidence above, address any blockers, then request another automated review or ask a human reviewer to decide.")
		}
	}
	if assessment := codeReviewAssessmentSummary(input.HeadSHA, input.AssessedAt); assessment != "" {
		paragraphs = append(paragraphs, assessment)
	}
	if input.SessionURL != "" {
		paragraphs = append(paragraphs, "[View the full review]("+input.SessionURL+")")
	}
	return strings.Join(paragraphs, "\n\n")
}

func codeReviewAssessmentSummary(headSHA string, assessedAt time.Time) string {
	shortSHA := codeReviewShortSHA(headSHA)
	if shortSHA == "" {
		return ""
	}
	summary := "**Latest assessment:** `" + shortSHA + "`"
	if !assessedAt.IsZero() {
		summary += " at " + assessedAt.UTC().Format(time.RFC3339)
	}
	return summary
}

func codeReviewShortSHA(headSHA string) string {
	headSHA = strings.TrimSpace(headSHA)
	if len(headSHA) > 7 {
		return headSHA[:7]
	}
	return headSHA
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
	if input.Acceptable && input.RequiredReviewerQuorum > 0 {
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
		if input.RequiredReviewerQuorum > 0 {
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

	reasons := codeReviewRiskReasonExplanations(codeReviewActionableRiskReasons(input.RiskReasons), input.DescriptionIssues)
	if len(reasons) == 0 {
		if codeReviewHasReviewIssue(input.RiskReasons) {
			return "143 could not complete the automated review."
		}
		return "The available review evidence did not meet the configured approval policy."
	}
	return strings.Join(reasons, " ")
}

type codeReviewBlockerGroup string

const (
	codeReviewBlockerGroupPolicy   codeReviewBlockerGroup = "Policy thresholds"
	codeReviewBlockerGroupFinding  codeReviewBlockerGroup = "Review findings"
	codeReviewBlockerGroupJudgment codeReviewBlockerGroup = "Human judgment needed"
)

var codeReviewBlockerGroupOrder = []codeReviewBlockerGroup{
	codeReviewBlockerGroupPolicy,
	codeReviewBlockerGroupFinding,
	codeReviewBlockerGroupJudgment,
}

const maxCodeReviewReasonsPerGroup = 4

func codeReviewBlockerSections(input CodeReviewFinalReviewInput) []string {
	grouped := make(map[codeReviewBlockerGroup][]string, len(codeReviewBlockerGroupOrder))
	for _, reason := range input.RiskReasons {
		if codeReviewRiskReasonIsReviewIssue(reason.Code) {
			continue
		}
		explanation := humanizeCodeReviewRiskReason(reason, input.DescriptionIssues)
		if explanation == "" {
			continue
		}
		group := codeReviewRiskReasonBlockerGroup(reason.Code)
		if group == codeReviewBlockerGroupPolicy {
			explanation = codeReviewExplanationWithSettingsLink(explanation, input.PolicySettingsURL, reason.Code)
		}
		grouped[group] = append(grouped[group], explanation)
	}

	sections := make([]string, 0, len(grouped))
	for _, group := range codeReviewBlockerGroupOrder {
		explanations := grouped[group]
		if len(explanations) == 0 {
			continue
		}
		var section strings.Builder
		section.WriteString("**" + string(group) + ":**\n")
		displayed := explanations
		if len(displayed) > maxCodeReviewReasonsPerGroup {
			displayed = displayed[:maxCodeReviewReasonsPerGroup]
		}
		for _, explanation := range displayed {
			section.WriteString("- " + explanation + "\n")
		}
		if hidden := len(explanations) - len(displayed); hidden > 0 {
			section.WriteString(fmt.Sprintf("- %d more %s listed in the full review.\n", hidden, pluralizeCodeReviewWord(hidden, "blocker is", "blockers are")))
		}
		sections = append(sections, strings.TrimSpace(section.String()))
	}
	return sections
}

func codeReviewRiskReasonBlockerGroup(code CodeReviewRiskReasonCode) codeReviewBlockerGroup {
	switch code {
	case CodeReviewRiskReasonReviewerDisabled,
		CodeReviewRiskReasonFilesLimitExceeded,
		CodeReviewRiskReasonLinesLimitExceeded,
		CodeReviewRiskReasonChecksFailing,
		CodeReviewRiskReasonRequiredCheckFailing,
		CodeReviewRiskReasonDescriptionFailed,
		CodeReviewRiskReasonBranchOutOfDate,
		CodeReviewRiskReasonForkIneligible,
		CodeReviewRiskReasonAuthorIneligible,
		CodeReviewRiskReasonSensitivePath,
		CodeReviewRiskReasonPathOutsideScope,
		CodeReviewRiskReasonBlockedPath,
		CodeReviewRiskReasonPolicyPathChanged,
		CodeReviewRiskReasonExcludedCategory:
		return codeReviewBlockerGroupPolicy
	case CodeReviewRiskReasonBlockingFindings,
		CodeReviewRiskReasonReviewerDisagreement,
		CodeReviewRiskReasonScopeMismatch,
		CodeReviewRiskReasonUnresolvedUncertainty,
		CodeReviewRiskReasonPromptInjection,
		CodeReviewRiskReasonHeadChanged,
		CodeReviewRiskReasonOrchestratorContextStale:
		return codeReviewBlockerGroupFinding
	case CodeReviewRiskReasonUnresolvedHumanReview,
		CodeReviewRiskReasonReviewerQuorum,
		CodeReviewRiskReasonOrchestratorEscalation,
		CodeReviewRiskReasonArchitecture,
		CodeReviewRiskReasonOwnership,
		CodeReviewRiskReasonOperationalRisk,
		CodeReviewRiskReasonSensitiveChange,
		CodeReviewRiskReasonPolicyRequirement:
		return codeReviewBlockerGroupJudgment
	default:
		// Unknown future reasons fail closed into human judgment instead of
		// disappearing from a non-approval explanation.
		return codeReviewBlockerGroupJudgment
	}
}

func codeReviewRiskReasonIsReviewIssue(code CodeReviewRiskReasonCode) bool {
	return code == CodeReviewRiskReasonContextUnavailable || code == CodeReviewRiskReasonOrchestratorSynthesisInvalid
}

func codeReviewActionableRiskReasons(reasons []CodeReviewRiskReason) []CodeReviewRiskReason {
	actionable := make([]CodeReviewRiskReason, 0, len(reasons))
	for _, reason := range reasons {
		if !codeReviewRiskReasonIsReviewIssue(reason.Code) {
			actionable = append(actionable, reason)
		}
	}
	return actionable
}

func codeReviewHasReviewIssue(reasons []CodeReviewRiskReason) bool {
	for _, reason := range reasons {
		if codeReviewRiskReasonIsReviewIssue(reason.Code) {
			return true
		}
	}
	return false
}

func codeReviewReviewIssueSection(reasons []CodeReviewRiskReason, descriptionIssues []string) string {
	var section strings.Builder
	for _, reason := range reasons {
		if !codeReviewRiskReasonIsReviewIssue(reason.Code) {
			continue
		}
		explanation := humanizeCodeReviewRiskReason(reason, descriptionIssues)
		if explanation == "" {
			continue
		}
		if section.Len() == 0 {
			section.WriteString("**143 review issues:**\n")
		}
		section.WriteString("- " + explanation + "\n")
	}
	return strings.TrimSpace(section.String())
}

func codeReviewExplanationWithSettingsLink(explanation, settingsURL string, code CodeReviewRiskReasonCode) string {
	settingsURL = strings.TrimSpace(settingsURL)
	if settingsURL == "" {
		return explanation
	}
	if fragment := codeReviewPolicySettingFragment(code); fragment != "" {
		settingsURL = strings.SplitN(settingsURL, "#", 2)[0] + "#" + fragment
	}
	return explanation + " [View policy setting](" + settingsURL + ")"
}

func codeReviewPolicySettingFragment(code CodeReviewRiskReasonCode) string {
	switch code {
	case CodeReviewRiskReasonFilesLimitExceeded:
		return "policy-max-files-changed"
	case CodeReviewRiskReasonLinesLimitExceeded:
		return "policy-max-lines-changed"
	default:
		return ""
	}
}

func codeReviewBlockerCount(reasons []CodeReviewRiskReason, blockingFindings []CodeReviewFinding) int {
	count := 0
	for _, reason := range reasons {
		if codeReviewRiskReasonIsReviewIssue(reason.Code) {
			continue
		}
		if reason.Code == CodeReviewRiskReasonBlockingFindings && len(blockingFindings) > 0 {
			count += len(blockingFindings)
			continue
		}
		count++
	}
	return count
}

func codeReviewAdvisoryFindingsSection(findings []CodeReviewFinding) string {
	var section strings.Builder
	section.WriteString("<details>\n")
	section.WriteString(fmt.Sprintf("<summary><strong>Advisory findings</strong> (%d non-blocking)</summary>\n\n", len(findings)))
	for _, finding := range groupedCodeReviewFindings(findings) {
		section.WriteString("- " + finding + "\n")
	}
	section.WriteString("\nP2 and P3 observations do not affect the approval decision.\n")
	section.WriteString("</details>")
	return section.String()
}

func codeReviewRiskReasonExplanations(reasons []CodeReviewRiskReason, descriptionIssues []string) []string {
	explanations := make([]string, 0, len(reasons))
	for _, reason := range reasons {
		if explanation := humanizeCodeReviewRiskReason(reason, descriptionIssues); explanation != "" {
			explanations = append(explanations, explanation)
		}
	}
	if len(explanations) > maxCodeReviewReasonsPerGroup {
		additional := len(explanations) - maxCodeReviewReasonsPerGroup
		explanations = append(
			explanations[:maxCodeReviewReasonsPerGroup],
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
	case CodeReviewRiskReasonOrchestratorEscalation:
		return "The coding-agent orchestrator recommends human review."
	case CodeReviewRiskReasonOrchestratorContextStale:
		return "The PR title or description changed after the coding-agent assessment, so that recommendation is stale."
	case CodeReviewRiskReasonArchitecture:
		return codeReviewExplicitHumanReviewExplanation("architectural judgment", reason.Subject)
	case CodeReviewRiskReasonOwnership:
		return codeReviewExplicitHumanReviewExplanation("ownership judgment", reason.Subject)
	case CodeReviewRiskReasonOperationalRisk:
		return codeReviewExplicitHumanReviewExplanation("operational-risk judgment", reason.Subject)
	case CodeReviewRiskReasonSensitiveChange:
		return codeReviewExplicitHumanReviewExplanation("sensitive-change judgment", reason.Subject)
	case CodeReviewRiskReasonPolicyRequirement:
		return codeReviewExplicitHumanReviewExplanation("an automated approval policy requirement", reason.Subject)
	}

	return codeReviewSentence(reason.Message())
}

func codeReviewExplicitHumanReviewExplanation(kind, detail string) string {
	detail = strings.TrimSpace(detail)
	if detail == "" {
		return "Human review is required for " + kind + "."
	}
	return "Human review is required for " + kind + ": " + strings.TrimRight(detail, ".") + "."
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

func partitionCodeReviewFindings(findings []CodeReviewFinding) (blocking, advisory []CodeReviewFinding) {
	blocking = make([]CodeReviewFinding, 0, len(findings))
	advisory = make([]CodeReviewFinding, 0, len(findings))
	for _, finding := range findings {
		if finding.Severity.IsBlocking() {
			blocking = append(blocking, finding)
		} else {
			advisory = append(advisory, finding)
		}
	}
	return blocking, advisory
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
		if !finding.Severity.IsBlocking() {
			continue
		}
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
