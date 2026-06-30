// Package gitref provides a single, shared validator for git ref/branch names
// that originate from external input (REST bodies, Slack messages, PagerDuty
// payloads, etc.). Centralizing it keeps the rules consistent across the API,
// worker, and slackbot packages — the value eventually reaches shell commands
// like `git fetch origin <branch>`, where a value that git can mistake for an
// option (leading '-') or a malformed refspec must never slip through.
package gitref

import (
	"regexp"
	"strings"
)

// pattern requires an alphanumeric first character (so a value can never be
// parsed as a `-flag`) and restricts the rest to characters that are safe in a
// branch/ref name. It deliberately excludes ':' (refspec separator), '~', '^',
// '?', '*', '[', whitespace, and backslashes.
var pattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9._/-]*$`)

// IsValidRef reports whether s is a safe git ref/branch name to forward into a
// git command. It is intentionally stricter than git's own check-ref-format:
// false negatives just surface a clear "invalid branch" error, whereas a false
// positive could enable git argument injection.
func IsValidRef(s string) bool {
	if s == "" || len(s) > 255 {
		return false
	}
	if strings.ContainsAny(s, " \t\n\r\\") {
		return false
	}
	if strings.Contains(s, "..") ||
		strings.Contains(s, "~") ||
		strings.Contains(s, "^") ||
		strings.Contains(s, ":") {
		return false
	}
	return pattern.MatchString(s)
}
