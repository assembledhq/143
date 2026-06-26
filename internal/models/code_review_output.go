package models

import (
	"bytes"
	"fmt"
	"strings"
	"text/template"
)

type CodeReviewFinalReviewInput struct {
	Decision                  CodeReviewDecision
	Acceptable                bool
	RiskReasons               []string
	SessionURL                string
	PolicyVersion             int
	HeadSHA                   string
	Summary                   string
	Template                  string
	DescriptionPassed         *bool
	AgentSummaries            []string
	Findings                  []CodeReviewFinding
	RecommendedHumanReviewers []string
	Checklist                 []string
}

func BuildCodeReviewFinalReviewBody(input CodeReviewFinalReviewInput) string {
	if strings.TrimSpace(input.Template) != "" {
		if rendered, ok := renderCodeReviewFinalReviewTemplate(input); ok {
			return rendered
		}
	}
	return buildDefaultCodeReviewFinalReviewBody(input)
}

type codeReviewFinalReviewTemplateData struct {
	Decision                  string
	Risk                      string
	Acceptable                bool
	RiskReasons               []string
	SessionURL                string
	PolicyVersion             int
	HeadSHA                   string
	Summary                   string
	DescriptionPassed         *bool
	AgentSummaries            []string
	Findings                  []CodeReviewFinding
	RecommendedHumanReviewers []string
	Checklist                 []string
}

func renderCodeReviewFinalReviewTemplate(input CodeReviewFinalReviewInput) (string, bool) {
	tmpl, err := template.New("code_review_final_review").Parse(input.Template)
	if err != nil {
		return "", false
	}
	risk := "needs human review"
	if input.Acceptable {
		risk = "acceptable"
	}
	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, codeReviewFinalReviewTemplateData{
		Decision:                  string(input.Decision),
		Risk:                      risk,
		Acceptable:                input.Acceptable,
		RiskReasons:               append([]string(nil), input.RiskReasons...),
		SessionURL:                input.SessionURL,
		PolicyVersion:             input.PolicyVersion,
		HeadSHA:                   input.HeadSHA,
		Summary:                   input.Summary,
		DescriptionPassed:         input.DescriptionPassed,
		AgentSummaries:            append([]string(nil), input.AgentSummaries...),
		Findings:                  append([]CodeReviewFinding(nil), input.Findings...),
		RecommendedHumanReviewers: append([]string(nil), input.RecommendedHumanReviewers...),
		Checklist:                 append([]string(nil), input.Checklist...),
	}); err != nil {
		return "", false
	}
	return strings.TrimSpace(buf.String()), true
}

func buildDefaultCodeReviewFinalReviewBody(input CodeReviewFinalReviewInput) string {
	var b strings.Builder
	if input.Decision == CodeReviewDecisionApproved {
		b.WriteString("143 Code Reviewer approved this PR\n\n")
	} else {
		b.WriteString("143 Code Reviewer did not approve this PR\n\n")
	}
	if input.Acceptable {
		b.WriteString("Risk: acceptable\n")
	} else {
		b.WriteString("Risk: needs human review\n")
	}
	if input.PolicyVersion > 0 {
		b.WriteString(fmt.Sprintf("Policy version: %d\n", input.PolicyVersion))
	}
	if input.HeadSHA != "" {
		b.WriteString("Reviewed head: " + input.HeadSHA + "\n")
	}
	if input.DescriptionPassed != nil {
		if *input.DescriptionPassed {
			b.WriteString("Description: passed\n")
		} else {
			b.WriteString("Description: failed\n")
		}
	}
	if agentSummaries := nonEmptyStrings(input.AgentSummaries); len(agentSummaries) > 0 {
		b.WriteString("Review agents: " + strings.Join(agentSummaries, ", ") + "\n")
	}
	if input.SessionURL != "" {
		b.WriteString("Review session: " + input.SessionURL + "\n")
	}
	if input.Summary != "" {
		b.WriteString("\nSummary:\n")
		b.WriteString("- " + input.Summary + "\n")
	}
	if len(input.RiskReasons) > 0 {
		b.WriteString("\nReasons:\n")
		for _, reason := range input.RiskReasons {
			if strings.TrimSpace(reason) == "" {
				continue
			}
			b.WriteString("- " + reason + "\n")
		}
	}
	if len(input.Findings) > 0 {
		b.WriteString("\nAgent findings:\n")
		for _, finding := range groupedCodeReviewFindings(input.Findings) {
			b.WriteString("- " + finding + "\n")
		}
	}
	if reviewers := nonEmptyStrings(input.RecommendedHumanReviewers); len(reviewers) > 0 {
		b.WriteString("\nRecommended human reviewers: " + strings.Join(reviewers, ", ") + "\n")
	}
	if checklist := nonEmptyStrings(input.Checklist); len(checklist) > 0 {
		b.WriteString("\nApproval checklist:\n")
		for _, item := range checklist {
			b.WriteString("- " + item + "\n")
		}
	}
	return strings.TrimSpace(b.String())
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
