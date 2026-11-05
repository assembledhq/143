package automationactions

import (
	"regexp"
	"strings"
)

var slackPRReviewLink = regexp.MustCompile(
	`\[PR #([1-9][0-9]*)\]\(` +
		`(https://github\.com/([A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+)/pull/([1-9][0-9]*)#pullrequestreview-[1-9][0-9]*)\)`,
)

func renderSlackActionText(text, repository string) string {
	escaped := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;").Replace(text)
	return slackPRReviewLink.ReplaceAllStringFunc(escaped, func(link string) string {
		parts := slackPRReviewLink.FindStringSubmatch(link)
		labelPR, reviewURL, linkRepository, urlPR := parts[1], parts[2], parts[3], parts[4]
		if labelPR != urlPR || !strings.EqualFold(linkRepository, repository) {
			return link
		}
		return "<" + reviewURL + "|PR #" + labelPR + ">"
	})
}
