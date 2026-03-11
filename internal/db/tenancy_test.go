package db

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestMultiTenancyAudit scans all store files for SQL queries and verifies
// that every query on a multi-tenant table includes an org_id filter.
// This is a critical safety test — a missing org_id filter is a P0 data isolation bug.
func TestMultiTenancyAudit(t *testing.T) {
	t.Parallel()
	// Tables that require org_id filtering
	multiTenantTables := []string{
		"issues",
		"sessions",
		"pm_plans",
		"pm_decision_log",
		"webhook_deliveries",
		"jobs",
		"repositories",
		"integrations",
		"sessions",
		"users",
		"pull_requests",
		"validations",
		"session_logs",
		"session_questions",
		"issue_events",
		"priority_scores",
		"complexity_estimates",
		"deploys",
	}

	// Tables exempt from org_id requirement (global or no org_id column)
	exemptTables := map[string]bool{
		"organizations":     true,
		"schema_migrations": true,
	}

	// Queries legitimately exempt from org_id filtering, keyed by table+pattern.
	// Session lookups by token are done pre-auth (to identify the user/org).
	// DisconnectByInstallationID is a cross-org webhook handler operation.
	// Integration GetByID is used pre-auth in webhook handlers to discover org_id.
	type exemption struct {
		table   string
		pattern string
	}
	exemptions := []exemption{
		{"sessions", "where token"},
		{"sessions", "where user_id"},
		{"repositories", "installation_id"},
		{"integrations", "from integrations"},
		{"session_logs", "from session_logs"}, // no org_id column; scoped via session_id FK
		{"session_logs", "into session_logs"}, // no org_id column; scoped via session_id FK
		{"users", "where github_id"},              // pre-auth lookup by GitHub ID
		{"users", "where email"},                  // pre-auth lookup by email
		{"users", "where google_id"},              // pre-auth lookup by Google ID
	}

	// Scan all .go files in the db package (not test files)
	files, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("failed to glob db files: %v", err)
	}

	fset := token.NewFileSet()
	var violations []string

	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		if file == "db.go" {
			continue // skip connection pool setup
		}

		src, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("failed to read %s: %v", file, err)
		}

		f, err := parser.ParseFile(fset, file, src, parser.ParseComments)
		if err != nil {
			t.Fatalf("failed to parse %s: %v", file, err)
		}

		// Walk the AST looking for string literals containing SQL
		ast.Inspect(f, func(n ast.Node) bool {
			lit, ok := n.(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}

			val := lit.Value
			valLower := strings.ToLower(val)

			// Check if this looks like a SQL query
			if !strings.Contains(valLower, "select") &&
				!strings.Contains(valLower, "insert") &&
				!strings.Contains(valLower, "update") &&
				!strings.Contains(valLower, "delete") {
				return true
			}

			// Check each multi-tenant table
			for _, table := range multiTenantTables {
				if exemptTables[table] {
					continue
				}

				// Check if the query references this table
				if !strings.Contains(valLower, table) {
					continue
				}

				// Skip if it's just a column reference like "issue_id"
				// but not the table name itself
				tablePatterns := []string{
					"from " + table,
					"into " + table,
					"update " + table,
					"join " + table,
				}
				referencesTable := false
				for _, p := range tablePatterns {
					if strings.Contains(valLower, p) {
						referencesTable = true
						break
					}
				}
				if !referencesTable {
					continue
				}

				// Verify org_id is present in the query
				if !strings.Contains(valLower, "org_id") {
					// Check if this query matches a table-specific exemption
					isExempt := false
					for _, ex := range exemptions {
						if ex.table == table && strings.Contains(valLower, ex.pattern) {
							isExempt = true
							break
						}
					}
					if !isExempt {
						pos := fset.Position(lit.Pos())
						violations = append(violations,
							file+":"+pos.String()+": query on '"+table+"' missing org_id filter")
					}
				}
			}

			return true
		})
	}

	if len(violations) > 0 {
		t.Error("Multi-tenancy violations found — every query on a multi-tenant table MUST include org_id:\n" +
			strings.Join(violations, "\n"))
	}

	// Also verify we scanned at least some files
	require.Greater(t, len(files), 0, "should have scanned at least one store file")
}
