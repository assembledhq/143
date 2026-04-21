package db

import (
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"strings"
	"testing"
)

// TestAuthSessionOrgIDReadsAllowlisted enforces the sunset of the legacy
// auth_sessions.org_id column (TODO 2026-04-25 at handlers/auth.go
// persistSessionTx). The column is kept in sync during the compat window but
// is not authoritative for request-time org resolution — that flows through
// the middleware waterfall (X-Active-Org-ID header → session.last_org_id →
// oldest membership). Any *new* reader of AuthSession.OrgID during the sunset
// window is a bug: it would silently inherit a frozen value that may point at
// an org the user has since been removed from.
//
// The AST check walks three files that are known to touch AuthSession (the
// store, the auth handler that persists sessions, and the middleware that
// reads them back). Restricting the scan to these files avoids conflating
// this check with reads of the unrelated models.Session.OrgID field used by
// the coding-agent orchestrator, which shares the selector text but a
// different type.
//
// To legitimately add a new read, annotate the line (or the statement above
// it) with `// lint:allow-auth-session-orgid reason="..."`. The comment must
// attach to the same line as the SelectorExpr or the line immediately before
// it, matching the codebase's existing `// lint:allow-no-orgid` convention.
// Line-number allowlists were tried first and turned out to be brittle — a
// drive-by edit above the pinned line silently invalidated the exemption or
// moved the violation to a new line that inherited it.
func TestAuthSessionOrgIDReadsAllowlisted(t *testing.T) {
	t.Parallel()

	const marker = "lint:allow-auth-session-orgid"

	files := []string{
		filepath.Join(".", "auth_sessions.go"),
		filepath.Join("..", "api", "handlers", "auth.go"),
		filepath.Join("..", "api", "middleware", "auth.go"),
	}

	fset := token.NewFileSet()
	for _, path := range files {
		file, err := parser.ParseFile(fset, path, nil, parser.ParseComments)
		if err != nil {
			t.Fatalf("parse %s: %v", path, err)
		}

		// Index lint:allow-auth-session-orgid comments by the line they sit on.
		// A violation at line N is exempt if the marker appears on N itself
		// (trailing comment) or on N-1 (preceding doc-style comment).
		exemptLines := map[int]bool{}
		for _, group := range file.Comments {
			for _, c := range group.List {
				if !strings.Contains(c.Text, marker) {
					continue
				}
				exemptLines[fset.Position(c.Slash).Line] = true
			}
		}

		ast.Inspect(file, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if sel.Sel.Name != "OrgID" {
				return true
			}
			ident, ok := sel.X.(*ast.Ident)
			if !ok {
				return true
			}
			// Match only the receiver names actually used for AuthSession in
			// the scanned files. "s" is excluded because it is also the
			// store receiver name and would false-flag every line.
			if ident.Name != "session" && ident.Name != "sess" && ident.Name != "authSession" {
				return true
			}

			line := fset.Position(sel.Pos()).Line
			if exemptLines[line] || exemptLines[line-1] {
				return true
			}

			t.Errorf("%s:%d: %s.OrgID is a legacy AuthSession field read. "+
				"Use middleware.OrgIDFromContext, or add a "+
				"`// lint:allow-auth-session-orgid reason=\"...\"` comment on "+
				"this line or the line above it with a justification.",
				path, line, ident.Name)
			return true
		})
	}
}
