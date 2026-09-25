package models

import (
	"fmt"
	"net/url"
	"regexp"
	"strings"
	"time"
	_ "time/tzdata" // Keep Eastern Time available in minimal runtime images.
)

const (
	codeReviewFooterStart = "<!-- 143-code-review-footer:start -->"
	codeReviewFooterEnd   = "<!-- 143-code-review-footer:end -->"
)

var codeReviewCommentEasternTime = func() *time.Location {
	location, err := time.LoadLocation("America/New_York")
	if err != nil {
		panic(fmt.Sprintf("load embedded Eastern Time zone: %v", err))
	}
	return location
}()

// CodeReviewCommentFooter contains navigation for the displayed assessment.
// Historical navigation belongs in the history or previous-result disclosure.
type CodeReviewCommentFooter struct {
	DetailURL          string
	DetailLabel        string
	EvidenceRecheckURL string
	ReviewNowURL       string
}

// CodeReviewCommentTime uses GitHub's localized timestamp element with a
// readable Eastern Time fallback for clients that display the comment as plain text.
func CodeReviewCommentTime(value time.Time) string {
	utc := value.UTC()
	return fmt.Sprintf(
		`<relative-time datetime="%s">%s</relative-time>`,
		utc.Format(time.RFC3339),
		value.In(codeReviewCommentEasternTime).Format("Jan 2, 2006 at 3:04 PM MST"),
	)
}

// CodeReviewCommentDetailLabel distinguishes assessments from full sessions.
func CodeReviewCommentDetailLabel(detailURL string, active bool) string {
	target, err := url.Parse(detailURL)
	assessment := err == nil && target.Query().Get("assessment") != ""
	if active {
		if assessment {
			return "Follow assessment"
		}
		return "Follow review"
	}
	if assessment {
		return "View assessment"
	}
	return "View full review"
}

// WithCodeReviewCommentFooter replaces generated navigation without modifying
// links in findings, evidence, or historical disclosures. Callers choose the
// action from current structured state; this function only renders that choice.
func WithCodeReviewCommentFooter(body string, footer CodeReviewCommentFooter) string {
	body, _ = SplitCodeReviewCommentFooter(body, footer.DetailURL)
	links := make([]string, 0, 2)
	if target := codeReviewCommentURL(footer.EvidenceRecheckURL); target != "" {
		links = append(links, "[Re-check evidence]("+target+")")
	} else if target := codeReviewCommentURL(footer.ReviewNowURL); target != "" {
		links = append(links, "[Request review]("+target+")")
	}
	if target := codeReviewCommentURL(footer.DetailURL); target != "" {
		label := footer.DetailLabel
		if !codeReviewDetailLabel(label) {
			label = CodeReviewCommentDetailLabel(footer.DetailURL, false)
		}
		links = append(links, "["+label+"]("+target+")")
	}
	if len(links) == 0 {
		return body
	}
	block := codeReviewFooterStart + "\n" + strings.Join(links, " · ") + "\n" + codeReviewFooterEnd
	if body == "" {
		return block
	}
	return body + "\n\n" + block
}

// SplitCodeReviewCommentFooter extracts the top-level generated footer. Legacy
// navigation is recognized only in known review bodies and on the trusted app
// origin/path. This is a presentation transform, never a stored-result rewrite.
func SplitCodeReviewCommentFooter(body, trustedDetailURL string) (string, CodeReviewCommentFooter) {
	body = strings.TrimSpace(body)
	lines := strings.Split(body, "\n")
	depth, fenced := 0, false
	for i, line := range lines {
		if depth == 0 && !fenced && line == codeReviewFooterStart && i+2 < len(lines) && lines[i+2] == codeReviewFooterEnd {
			footer, ok := parseCodeReviewFooter(lines[i+1], trustedDetailURL)
			if !ok {
				// A URL change must not leave stale navigation beside the new footer.
				footer = CodeReviewCommentFooter{}
			}
			before, after := strings.TrimSpace(strings.Join(lines[:i], "\n")), strings.TrimSpace(strings.Join(lines[i+3:], "\n"))
			return joinCodeReviewCommentParts(before, after), footer
		}
		depth, fenced = codeReviewCommentMarkup(line, depth, fenced)
	}
	return splitLegacyCodeReviewFooter(body, trustedDetailURL)
}

func parseCodeReviewFooter(line, trustedDetailURL string) (CodeReviewCommentFooter, bool) {
	var footer CodeReviewCommentFooter
	links := strings.Split(line, " · ")
	if len(links) > 2 {
		return footer, false
	}
	for _, link := range links {
		label, target, ok := codeReviewMarkdownLink(link)
		if !ok || !codeReviewSameApp(target, trustedDetailURL) {
			return CodeReviewCommentFooter{}, false
		}
		switch {
		case label == "Re-check evidence" && footer.EvidenceRecheckURL == "" && footer.ReviewNowURL == "":
			footer.EvidenceRecheckURL = target
		case label == "Request review" && footer.EvidenceRecheckURL == "" && footer.ReviewNowURL == "":
			footer.ReviewNowURL = target
		case codeReviewDetailLabel(label) && footer.DetailURL == "":
			footer.DetailURL, footer.DetailLabel = target, label
		default:
			return CodeReviewCommentFooter{}, false
		}
	}
	return footer, true
}

func codeReviewDetailLabel(label string) bool {
	switch label {
	case "View full review", "View review", "Follow review", "View assessment", "Follow assessment":
		return true
	default:
		return false
	}
}

func codeReviewCommentURL(raw string) string {
	if strings.TrimSpace(raw) != raw || strings.ContainsAny(raw, "\r\n\t <>\"`") {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.User != nil || (u.Scheme != "https" && u.Scheme != "http") {
		return ""
	}
	return strings.NewReplacer("(", "%28", ")", "%29").Replace(raw)
}

func codeReviewMarkdownLink(link string) (string, string, bool) {
	if !strings.HasPrefix(link, "[") || !strings.HasSuffix(link, ")") {
		return "", "", false
	}
	label, target, ok := strings.Cut(link[1:len(link)-1], "](")
	if !ok || strings.ContainsAny(label, "[]\n") || codeReviewCommentURL(target) == "" {
		return "", "", false
	}
	return label, target, true
}

func codeReviewSameApp(target, trusted string) bool {
	if trusted == "" {
		return true // New marker-delimited footers can be moved without a URL override.
	}
	actual, err := url.Parse(target)
	if err != nil {
		return false
	}
	expected, err := url.Parse(trusted)
	return err == nil && codeReviewCommentURL(trusted) != "" && actual.Scheme == expected.Scheme && strings.EqualFold(actual.Host, expected.Host)
}

func codeReviewLegacyTarget(target, trusted, kind string) bool {
	if trusted == "" || !codeReviewSameApp(target, trusted) {
		return false
	}
	actual, err := url.Parse(target)
	if err != nil {
		return false
	}
	expected, err := url.Parse(trusted)
	if err != nil {
		return false
	}
	prefix, _, session := strings.Cut(expected.Path, "/sessions/")
	if !session {
		if !strings.HasSuffix(expected.Path, "/code-reviews") {
			return false
		}
		prefix = strings.TrimSuffix(expected.Path, "/code-reviews")
	}
	if kind == "detail" && strings.HasPrefix(actual.Path, prefix+"/sessions/") && strings.TrimPrefix(actual.Path, prefix+"/sessions/") != "" {
		return actual.RawQuery == "" && actual.Fragment == ""
	}
	if actual.Path != prefix+"/code-reviews" {
		return false
	}
	switch kind {
	case "detail":
		return actual.Query().Get("assessment") != "" && len(actual.Query()) == 1
	case "policy":
		return actual.Query().Get("tab") == "policy" && len(actual.Query()) == 1
	default:
		return actual.Query().Get(kind) != "" && len(actual.Query()) == 1 && actual.Fragment == ""
	}
}

var legacyCodeReviewAssessment = regexp.MustCompile("^\\*\\*Latest assessment:\\*\\* `([[:xdigit:]]{1,40})`(?: at (\\S+))?$")

func splitLegacyCodeReviewFooter(body, trusted string) (string, CodeReviewCommentFooter) {
	var footer CodeReviewCommentFooter
	if trusted == "" || !strings.HasPrefix(body, "❌ **143 Code Reviewer") && !strings.HasPrefix(body, "✅ **143 Code Reviewer") && !strings.HasPrefix(body, CodeReviewProvisionalReviewHeading) && !strings.HasPrefix(body, "143 Code Reviewer") {
		return body, footer
	}
	paragraphs := strings.Split(body, "\n\n")
	kept := make([]string, 0, len(paragraphs))
	depth, fenced, history := 0, false, false
	for _, paragraph := range paragraphs {
		protected := depth > 0 || fenced || history
		for _, line := range strings.Split(paragraph, "\n") {
			if strings.Contains(line, "<!-- 143-code-review-history:start -->") {
				history, protected = true, true
			}
			if strings.Contains(line, "<!-- 143-code-review-history:end -->") {
				history = false
			}
			depth, fenced = codeReviewCommentMarkup(line, depth, fenced)
		}
		if protected || depth > 0 || fenced {
			kept = append(kept, paragraph)
			continue
		}
		if label, target, ok := codeReviewMarkdownLink(paragraph); ok && codeReviewLegacyTarget(target, trusted, "detail") {
			switch label {
			case "View the full review", "View the review session", "Follow the review session":
				footer.DetailURL = target
				footer.DetailLabel = CodeReviewCommentDetailLabel(target, label == "Follow the review session")
				continue
			}
		}
		if target := legacyCodeReviewRequestLink(paragraph, trusted); target != "" {
			footer.ReviewNowURL = target
			continue
		}
		if content, target := legacyCodeReviewEvidenceStep(paragraph, trusted); target != "" {
			paragraph, footer.EvidenceRecheckURL = content, target
		}
		if paragraph == "**Why:** This PR did not meet the configured approval policy; the blockers are grouped below." {
			continue
		}
		if strings.HasPrefix(paragraph, "**Policy thresholds:**\n") {
			lines := strings.Split(paragraph, "\n")
			for i, line := range lines {
				if before, link, ok := strings.Cut(line, " [View policy setting]("); ok && strings.HasPrefix(line, "- ") {
					_, target, valid := codeReviewMarkdownLink("[View policy setting](" + link)
					if valid && codeReviewLegacyTarget(target, trusted, "policy") {
						lines[i] = before
					}
				}
			}
			paragraph = strings.Join(lines, "\n")
		}
		if fields := legacyCodeReviewAssessment.FindStringSubmatch(paragraph); fields != nil {
			assessedAt, err := time.Parse(time.RFC3339, fields[2])
			if fields[2] == "" || err == nil {
				paragraph = codeReviewAssessmentSummary(fields[1], assessedAt)
			}
		}
		if evidence, ok := strings.CutPrefix(paragraph, "**Reviewer evidence:** "); ok {
			paragraph = "**Reviewers:** " + evidence
		}
		kept = append(kept, paragraph)
	}
	return strings.TrimSpace(strings.Join(kept, "\n\n")), footer
}

func legacyCodeReviewRequestLink(paragraph, trusted string) string {
	for _, suffix := range []string{
		" · Open 143 to request a review of your latest pushed changes. If a running or completed review already covers those changes, 143 may use it instead of starting another.",
		" · Request review of the latest revision in 143. Existing applicable work may be reused.",
	} {
		if link, ok := strings.CutSuffix(paragraph, suffix); ok {
			label, target, valid := codeReviewMarkdownLink(link)
			if valid && (label == "Request Full Re-Review Now" || label == "Review now") && codeReviewLegacyTarget(target, trusted, "review_now") {
				return target
			}
		}
	}
	return ""
}

func legacyCodeReviewEvidenceStep(paragraph, trusted string) (string, string) {
	const suffix = ". Open 143 to confirm the request. Any remaining approval requirements still apply."
	for _, prefix := range []string{
		"**Next steps:** Add the missing evidence under **Testing** or **Evidence** in the PR description or a comment, then ",
		"**Next steps:** If you have evidence that addresses these findings, add it under **Testing** or **Evidence** in the PR description or a comment, then ",
		"**Next steps:** Once updated CI results are available, ",
	} {
		if rest, ok := strings.CutPrefix(paragraph, prefix); ok {
			if link, ok := strings.CutSuffix(rest, suffix); ok {
				label, target, valid := codeReviewMarkdownLink(link)
				if valid && label == "Re-check PR evidence" && codeReviewLegacyTarget(target, trusted, "recheck") {
					return prefix + "request an evidence recheck. Other approval requirements still apply.", target
				}
			}
		}
	}
	return paragraph, ""
}

func codeReviewCommentMarkup(line string, depth int, fenced bool) (int, bool) {
	trimmed := strings.TrimSpace(line)
	if strings.HasPrefix(trimmed, "```") || strings.HasPrefix(trimmed, "~~~") {
		return depth, !fenced
	}
	if !fenced {
		// Generated disclosures use block HTML. Prose and inline code can name
		// <details> without opening a disclosure around the following footer.
		markup := strings.ToLower(trimmed)
		if strings.HasPrefix(markup, "<details>") || strings.HasPrefix(markup, "<details ") || strings.HasPrefix(markup, "<details\t") {
			depth++
			if strings.Contains(markup, "</details>") {
				depth--
			}
		} else if strings.HasPrefix(markup, "</details>") {
			depth--
		}
		if depth < 0 {
			depth = 0
		}
	}
	return depth, fenced
}

func joinCodeReviewCommentParts(before, after string) string {
	if before == "" {
		return after
	}
	if after == "" {
		return before
	}
	return before + "\n\n" + after
}
