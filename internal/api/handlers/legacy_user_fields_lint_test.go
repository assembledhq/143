package handlers

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestHandlersMustNotReadLegacyUserFields enforces that handler code reads
// the active-org role from middleware.ActiveRoleFromContext and the active
// org from middleware.OrgIDFromContext instead of the legacy User.Role and
// User.OrgID fields.
//
// Why this exists: the auth middleware currently rewrites User.Role and
// User.OrgID to the *active-org* values during the compatibility window
// (middleware/auth.go, TODO 2026-04-25). That means any future handler that
// reads user.Role or user.OrgID inherits active-org semantics silently — it
// looks like primary-org logic but is actually driven by the X-Active-Org-ID
// header. Once the compat sync is removed, those readers will regress to
// "whatever was in the users row," which may be stale or Nil for zero-
// membership users.
//
// A failing entry here is a prompt to switch to ActiveRoleFromContext /
// OrgIDFromContext. Legitimate exceptions (e.g. webhook paths that don't
// run through Auth and therefore never had the sync applied, or response-
// construction code that just passes the role back to the client) go in the
// allowlist with a justification. Each entry is keyed "file.go:<field>" so
// new readers have to explicitly be added rather than silently inheriting
// the old reader's exemption.
func TestHandlersMustNotReadLegacyUserFields(t *testing.T) {
	t.Parallel()

	// Legitimate exemptions. Key: "<file>.go:<field>", value: justification.
	// A file may hold multiple exemptions for the same field on different
	// call sites if they all share one reason — keep the justification
	// precise so drift is visible on the next reviewer pass.
	allowlist := map[string]string{
		// Webhook entry points do not run through the Auth middleware, so
		// user.OrgID is the literal primary-org foreign key from the users
		// row — no active-org sync was ever applied. Using OrgIDFromContext
		// here would be wrong because there is no org_id on the context.
		"webhooks.go:OrgID": "webhook path; no Auth middleware, user.OrgID is the literal users-row value",

		// Signup flows *populate* user.OrgID / user.Role on a freshly
		// created models.User before inserting it; they are writes, not
		// reads of the legacy field. The AST scan treats the selector the
		// same so it needs an entry.
		"auth_signup.go:OrgID": "signup assigns user.OrgID on the new user row before insert; not a read",
		"auth_signup.go:Role":  "signup assigns user.Role on the new user row before insert; not a read",

		// Join-token JIT provisioning mirrors auth_signup: it assigns
		// user.OrgID / user.Role on the freshly-created user row before
		// insert (then syncs Role to the GrantAtLeast effective role). All
		// writes, not reads.
		"auth_cli.go:OrgID": "JIT join assigns user.OrgID on the new user row before insert; not a read",
		"auth_cli.go:Role":  "JIT join assigns user.Role before insert and syncs the GrantAtLeast effective role; not a read",

		// Auth handlers return the user back to the client (e.g. /auth/me,
		// signup response body, invitation claim response). The legacy
		// fields exist on the wire contract for the compat window and are
		// populated from the active membership via the middleware sync or
		// the explicit assignment just above. Dropping these reads means
		// breaking the /auth/me response — which is the rollout step that
		// happens at the 2026-04-25 sunset, not here.
		"auth.go:OrgID": "wire-contract response; populated from active membership during compat window (see TODO 2026-04-25)",
		"auth.go:Role":  "wire-contract response; populated from active membership during compat window (see TODO 2026-04-25)",

		// Team handlers echo the updated role back in the response body.
		// That's a response field, not an authz decision — the authz check
		// uses ActiveRoleFromContext via RequireRole.
		"team.go:Role": "response body echoes the new role back to the client after ChangeRole succeeds",
	}

	fset := token.NewFileSet()

	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatalf("failed to read handler directory: %v", err)
	}

	for _, entry := range entries {
		name := entry.Name()
		if !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}

		path := filepath.Join(".", name)
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			t.Fatalf("failed to parse %s: %v", name, err)
		}

		ast.Inspect(file, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			field := sel.Sel.Name
			if field != "Role" && field != "OrgID" {
				return true
			}
			ident, ok := sel.X.(*ast.Ident)
			if !ok {
				return true
			}
			// Only flag `user.<field>` / `u.<field>`. Other receivers
			// (e.g. body.Role, inv.Role, member.Role) are domain objects,
			// not the middleware-synced models.User pointer.
			if ident.Name != "user" && ident.Name != "u" {
				return true
			}

			key := name + ":" + field
			if _, exempt := allowlist[key]; exempt {
				return true
			}

			t.Errorf("%s:%d: %s.%s is a legacy user field read. Use middleware.ActiveRoleFromContext / OrgIDFromContext, "+
				"or add %q to the allowlist in legacy_user_fields_lint_test.go with a justification.",
				name, fset.Position(sel.Pos()).Line, ident.Name, field, key)
			return true
		})
	}
}
