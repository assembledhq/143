// Command lint-schema enforces multi-tenancy rules on SQL migrations.
//
// Every CREATE TABLE that is not in the allowlist must declare a non-null
// `org_id` column so every row can be scoped to a tenant. Tables without
// org_id are a P0 data isolation risk — see AGENTS.md ("Multi-tenancy").
//
// Scope: only `migrations/*.up.sql` is scanned. Down migrations restore prior
// state (and may legitimately re-create tables that predate the org_id rule),
// so enforcing on them would block valid rollbacks. The forward direction is
// where new schema is introduced and is therefore the only direction policed.
//
// To exempt a table:
//  1. Add it to allowedNoOrgID with a short justification; OR
//  2. Add an inline comment anywhere inside the `CREATE TABLE ... (...)`
//     statement that includes a `reason="<why>"` clause:
//     `-- lint:no-org-id reason="<why>"`
//     The comment may appear on the header line or inside the column list.
//
// Run via `make lint-schema` or directly: `go run ./cmd/lint-schema`.
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// allowedNoOrgID lists tables that legitimately don't need org_id.
// Prefer adding org_id over allowlisting — allowlist only when the table is
// truly global or is scoped transitively via a parent FK that already has
// org_id (defense-in-depth still favors adding org_id directly).
var allowedNoOrgID = map[string]string{
	"organizations":               "root tenant table",
	"schema_migrations":           "golang-migrate library internal",
	"nodes":                       "cluster/infrastructure registry, not tenant data",
	"audit_log":                   "legacy table superseded by audit_logs",
	"preview_services":            "child of preview_instances, scoped via preview_instance_id FK",
	"preview_infrastructure":      "child of preview_instances, scoped via preview_instance_id FK",
	"preview_snapshots":           "child of preview_instances, scoped via preview_instance_id FK",
	"issue_events":                "child of issues, scoped via issue_id FK",
	"agent_run_logs":              "child of agent_runs, scoped via agent_run_id FK",
	"pm_document_set_pin_members": "join table, scoped via pin_id -> pm_document_set_pins",
	"project_task_dependencies":   "self-referential join on project_tasks",
	"project_source_issues":       "join of projects and issues (both org-scoped)",
}

// identPattern matches a single SQL identifier: either an unquoted
// ASCII identifier or a double-quoted identifier (which may contain
// spaces, dots, etc. that would otherwise be syntactically significant).
const identPattern = `(?:[a-zA-Z_][a-zA-Z0-9_]*|"[^"]+")`

// qualifiedIdentPattern matches an optionally schema-qualified identifier
// like `foo`, `public.foo`, or `public."foo bar"`.
const qualifiedIdentPattern = identPattern + `(?:\.` + identPattern + `)?`

var (
	// Match `CREATE TABLE <name> (`, optionally with IF NOT EXISTS, supporting
	// schema-qualified (`public.foo`) and double-quoted (`"foo bar"`) names.
	// Skip PARTITION OF (inherits org_id from parent) and CREATE TABLE ... AS
	// SELECT (not a schema definition for tenant rows).
	createTableRE = regexp.MustCompile(`(?im)^\s*CREATE\s+TABLE\s+(?:IF\s+NOT\s+EXISTS\s+)?(` + qualifiedIdentPattern + `)\s*\(`)

	// Detect partition children like `CREATE TABLE foo_default PARTITION OF foo DEFAULT;`
	partitionOfRE = regexp.MustCompile(`(?im)^\s*CREATE\s+TABLE\s+(?:IF\s+NOT\s+EXISTS\s+)?` + qualifiedIdentPattern + `\s+PARTITION\s+OF\s+`)

	// Detect `CREATE TABLE ... AS SELECT` (backup/temp tables).
	createTableAsRE = regexp.MustCompile(`(?im)^\s*CREATE\s+TABLE\s+(?:IF\s+NOT\s+EXISTS\s+)?` + qualifiedIdentPattern + `\s+AS\s+`)

	// Inline escape hatch anywhere inside the CREATE TABLE statement. The
	// reason="..." clause is required so the justification stays alongside
	// the exception:
	//   CREATE TABLE foo ( -- lint:no-org-id reason="global registry"
	//     ...
	//   );
	inlineEscapeRE = regexp.MustCompile(`--[^\n]*lint:no-org-id\s+reason="[^"]+"`)

	// Hot-table FK opt-out: allows a table with org_id NOT NULL to omit the
	// REFERENCES organizations(id) FK. The reason="..." clause is required.
	// Use only for reviewed high-write tables where the write path validates
	// parent ownership in code. See docs/design/96-foreign-key-policy-and-hot-table-audit.md.
	hotTableFKMarkerRE = regexp.MustCompile(`--[^\n]*lint:allow-hot-table-no-fk\s+reason="[^"]+"`)
)

type violation struct {
	file   string
	line   int
	table  string
	detail string
}

func main() {
	dir := "migrations"
	if len(os.Args) > 1 {
		dir = os.Args[1]
	}

	files, err := filepath.Glob(filepath.Join(dir, "*.up.sql"))
	if err != nil {
		fatal("glob migrations: %v", err)
	}
	if len(files) == 0 {
		fatal("no *.up.sql files found under %q", dir)
	}
	sort.Strings(files)

	var violations []violation
	for _, f := range files {
		// #nosec G304 -- f comes from filepath.Glob over the migrations dir; not user input.
		src, err := os.ReadFile(f)
		if err != nil {
			fatal("read %s: %v", f, err)
		}
		violations = append(violations, scan(f, string(src))...)
	}

	if len(violations) == 0 {
		fmt.Printf("lint-schema: OK — scanned %d migration file(s)\n", len(files))
		return
	}

	fmt.Fprintln(os.Stderr, "lint-schema: multi-tenancy violations")
	for _, v := range violations {
		fmt.Fprintf(os.Stderr, "  %s:%d: CREATE TABLE %q: %s\n", v.file, v.line, v.table, v.detail)
	}
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "Every new tenant-scoped table MUST have `org_id uuid NOT NULL REFERENCES organizations(id)`.")
	fmt.Fprintln(os.Stderr, "For reviewed hot/append-only tables that intentionally omit the FK, add")
	fmt.Fprintln(os.Stderr, "`-- lint:allow-hot-table-no-fk reason=\"...\"` inside the CREATE TABLE statement.")
	fmt.Fprintln(os.Stderr, "To exempt a table from org_id entirely, add it to allowedNoOrgID in")
	fmt.Fprintln(os.Stderr, "cmd/lint-schema/main.go or use `-- lint:no-org-id reason=\"...\"` on the CREATE TABLE line.")
	os.Exit(1)
}

// scan finds violations in a single migration file.
func scan(file, src string) []violation {
	var out []violation

	// Walk all CREATE TABLE matches, filtering out partitions and AS SELECT.
	for _, m := range createTableRE.FindAllStringSubmatchIndex(src, -1) {
		start := m[0]
		headerLine := src[start:findLineEnd(src, start)]

		if partitionOfRE.MatchString(headerLine) || createTableAsRE.MatchString(headerLine) {
			continue
		}

		openParen := m[1] - 1
		closeParen := findMatchingClose(src, openParen)
		// The full statement text — header + body — is the escape-hatch
		// search window, so the comment may live on a dedicated line
		// inside the body rather than sharing the header line.
		statement := src[start : closeParen+1]
		if inlineEscapeRE.MatchString(statement) {
			continue
		}

		rawName := src[m[2]:m[3]]
		table := normalizeTableName(rawName)
		if _, ok := allowedNoOrgID[table]; ok {
			continue
		}
		// Skip internal backup/temp tables that start with underscore.
		if strings.HasPrefix(table, "_") {
			continue
		}

		body := src[openParen+1 : closeParen]
		if !hasRequiredOrgIDColumn(body) {
			out = append(out, violation{
				file:   file,
				line:   lineOf(src, start),
				table:  table,
				detail: "missing org_id uuid NOT NULL column",
			})
			continue
		}

		if !hasOrgIDForeignKey(body) && !hasHotTableFKMarker(statement) && !hasOrgIDAlterFK(src, table) {
			out = append(out, violation{
				file:   file,
				line:   lineOf(src, start),
				table:  table,
				detail: "org_id is present but missing REFERENCES organizations(id) — add the FK for ordinary tables, or add `-- lint:allow-hot-table-no-fk reason=\"...\"` for reviewed hot tables",
			})
			continue
		}
	}
	return out
}

// normalizeTableName strips any schema qualifier and surrounding double
// quotes so `public."foo"` and `"foo"` both normalize to `foo` for
// allowlist lookup and violation display.
func normalizeTableName(raw string) string {
	if i := strings.LastIndex(raw, "."); i >= 0 {
		raw = raw[i+1:]
	}
	return strings.Trim(raw, `"`)
}

// findMatchingClose returns the index of the `)` that balances the `(` at
// openParen. Falls back to len(src)-1 if the statement is unterminated so
// callers get a best-effort body string instead of a panic.
func findMatchingClose(src string, openParen int) int {
	depth := 0
	for i := openParen; i < len(src); i++ {
		switch src[i] {
		case '(':
			depth++
		case ')':
			depth--
			if depth == 0 {
				return i
			}
		}
	}
	return len(src) - 1
}

// hasRequiredOrgIDColumn returns true if the column list declares a non-null
// org_id column. Matches lines like:
//
//	org_id uuid NOT NULL REFERENCES ...
//	org_id UUID NOT NULL,
var orgIDColumnRE = regexp.MustCompile(`(?is)(?:^|,)\s*org_id\s+uuid\b[^,]*\bnot\s+null\b`)

func hasRequiredOrgIDColumn(body string) bool {
	return orgIDColumnRE.MatchString(body)
}

// hasOrgIDForeignKey returns true if the org_id column declaration includes
// REFERENCES organizations(...), i.e. a DB-backed FK to the parent table.
var orgIDFKRE = regexp.MustCompile(`(?is)(?:^|,)\s*org_id\s+uuid\b[^,]*\bREFERENCES\s+organizations\b`)

func hasOrgIDForeignKey(body string) bool {
	return orgIDFKRE.MatchString(body)
}

func hasHotTableFKMarker(statement string) bool {
	return hotTableFKMarkerRE.MatchString(statement)
}

// hasOrgIDAlterFK returns true if the migration source adds a FK on org_id to
// organizations for the named table via ALTER TABLE ADD CONSTRAINT in the same
// file. This handles the common pattern of partitioned-table migrations that
// separate CREATE TABLE from ALTER TABLE ADD CONSTRAINT.
func hasOrgIDAlterFK(src, table string) bool {
	q := regexp.QuoteMeta(table)
	re := regexp.MustCompile(`(?is)ALTER\s+TABLE\s+(?:IF\s+EXISTS\s+)?(?:ONLY\s+)?` +
		`(?:[a-zA-Z_][a-zA-Z0-9_]*\s*\.\s*)?` +
		`"?` + q + `"?` +
		`\b[^;]+\bFOREIGN\s+KEY\s*\(\s*org_id\s*\)\s*REFERENCES\s+organizations\b`)
	return re.MatchString(src)
}

func findLineEnd(s string, i int) int {
	for j := i; j < len(s); j++ {
		if s[j] == '\n' {
			return j
		}
	}
	return len(s)
}

func lineOf(s string, i int) int {
	return strings.Count(s[:i], "\n") + 1
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "lint-schema: "+format+"\n", args...)
	os.Exit(2)
}
