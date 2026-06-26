package models

import (
	"bytes"
	"fmt"
	"strings"
	"text/template"
)

type CodeReviewFinalReviewInput struct {
	Decision      CodeReviewDecision
	Acceptable    bool
	RiskReasons   []string
	SessionURL    string
	PolicyVersion int
	HeadSHA       string
	Summary       string
	Template      string
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
	Decision      string
	Risk          string
	Acceptable    bool
	RiskReasons   []string
	SessionURL    string
	PolicyVersion int
	HeadSHA       string
	Summary       string
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
		Decision:      string(input.Decision),
		Risk:          risk,
		Acceptable:    input.Acceptable,
		RiskReasons:   append([]string(nil), input.RiskReasons...),
		SessionURL:    input.SessionURL,
		PolicyVersion: input.PolicyVersion,
		HeadSHA:       input.HeadSHA,
		Summary:       input.Summary,
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
	return strings.TrimSpace(b.String())
}

func SelectCodeReviewInlineFindings(findings []CodeReviewFinding, limit int) []CodeReviewFinding {
	if limit <= 0 {
		return nil
	}
	if limit > 10 {
		limit = 10
	}
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
