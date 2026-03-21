package sanitize

import (
	"net/url"
	"regexp"
	"strings"
	"unicode/utf8"
)

var (
	// codeFenceRe matches opening and closing markdown code fences.
	codeFenceRe = regexp.MustCompile("(?m)^```\\w*$")
	// xmlTagRe matches XML-like tags (but not their content).
	xmlTagRe = regexp.MustCompile(`<[^>]+>`)
	// urlRe matches HTTP/HTTPS URLs.
	urlRe = regexp.MustCompile(`https?://[^\s]+`)
)

// allowedURLDomains are domains whose URLs should be preserved.
var allowedURLDomains = []string{
	"github.com",
	"githubusercontent.com",
}

// isAllowedURL returns true if the URL belongs to an allowed domain.
func isAllowedURL(rawURL string) bool {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Host == "" {
		return false
	}
	host := strings.TrimPrefix(parsed.Host, "www.")
	for _, domain := range allowedURLDomains {
		if host == domain || strings.HasSuffix(host, "."+domain) {
			return true
		}
	}
	return false
}

// stripNonGitHubURLs removes URLs that don't point to allowed GitHub domains.
func stripNonGitHubURLs(s string) string {
	return urlRe.ReplaceAllStringFunc(s, func(match string) string {
		if isAllowedURL(match) {
			return match
		}
		return ""
	})
}

// SanitizeForPrompt strips markdown code fences and XML-like tags from s.
// If maxLen > 0 and the result exceeds maxLen, it is truncated.
func SanitizeForPrompt(s string, maxLen int) string {
	result := codeFenceRe.ReplaceAllString(s, "")
	result = xmlTagRe.ReplaceAllString(result, "")
	result = strings.TrimRight(result, " \t\n\r")
	if maxLen > 0 && len(result) > maxLen {
		// Truncate at a valid UTF-8 boundary to avoid splitting multi-byte characters.
		result = result[:maxLen]
		for len(result) > 0 && !utf8.Valid([]byte(result)) {
			result = result[:len(result)-1]
		}
	}
	return result
}

// SanitizeReviewComment applies all SanitizeForPrompt transformations and
// additionally strips non-GitHub URLs. Callers typically pass maxLen=2000.
func SanitizeReviewComment(s string, maxLen int) string {
	result := stripNonGitHubURLs(s)
	return SanitizeForPrompt(result, maxLen)
}
