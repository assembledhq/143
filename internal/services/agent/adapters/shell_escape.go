package adapters

import "strings"

// Shell-escape helpers shared by every adapter that builds command lines via
// string interpolation. Kept in their own file (rather than inside any single
// adapter) so the dependency is obvious at the call site — grepping for
// `shellEscape` turns up both the callers and the one implementation.

// shellEscapeDouble escapes characters for safe use inside double-quoted shell
// strings. Handles backslash, double quote, dollar sign, backtick, and
// exclamation mark (history expansion in bash, harmless in POSIX sh but
// escaped for safety).
func shellEscapeDouble(s string) string {
	r := strings.NewReplacer(
		`\`, `\\`,
		`"`, `\"`,
		`$`, `\$`,
		"`", "\\`",
		`!`, `\!`,
	)
	return r.Replace(s)
}

// shellEscapeSingle escapes single quotes for safe use inside single-quoted
// shell strings. Replaces each single quote with the standard close-quote,
// escaped-quote, reopen-quote pattern.
func shellEscapeSingle(s string) string {
	return strings.ReplaceAll(s, "'", "'\\''")
}
