// Command lint-stores enforces that every exported method on a *XxxStore
// type under internal/db takes org_id into account — either as an explicit
// `orgID uuid.UUID` parameter, or via a carrier struct (e.g. `*models.User`)
// whose `OrgID` field provides the scope.
//
// Carrier verification is real: the lint pre-scans `internal/models/*.go`
// and only treats a `*models.X` parameter as scoped if the underlying
// struct literally declares an `OrgID` field. A future model that omits
// `OrgID` will trigger a violation when used as a carrier.
//
// Methods that truly cannot scope by org (pre-auth lookups, cross-org
// system cleanup) must opt out with a comment directly above the func:
//
//	// lint:allow-no-orgid reason="pre-auth lookup by email"
//	func (s *UserStore) GetByEmail(ctx context.Context, email string) (...)
//
// Run via `make lint-stores` or directly: `go run ./cmd/lint-stores`.
package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

const (
	allowMarker      = "lint:allow-no-orgid"
	defaultStoresDir = "internal/db"
	defaultModelsDir = "internal/models"
	orgIDFieldName   = "OrgID"
)

// orgIDParamNameRE matches parameter names that end in "orgid" (case-
// insensitive), with an optional underscore: `OrgID`, `orgID`, `org_id`,
// `srcOrgID`, `targetOrgID`. Names like `organizerID` deliberately do NOT
// match — substring matching on "org" was too permissive.
var orgIDParamNameRE = regexp.MustCompile(`(?i)org_?id$`)

// allowCommentRE matches a properly-formed opt-out: the marker followed by
// a `reason="<non-empty>"` clause. A bare `// lint:allow-no-orgid` is NOT
// accepted — the reason must travel with the exception so drive-by opt-outs
// stay documented (mirrors the schema lint's inline escape requirement).
var allowCommentRE = regexp.MustCompile(`lint:allow-no-orgid\s+reason="[^"]+"`)

func main() {
	dir := defaultStoresDir
	if len(os.Args) > 1 {
		dir = os.Args[1]
	}
	modelsDir := defaultModelsDir
	if len(os.Args) > 2 {
		modelsDir = os.Args[2]
	}

	carriers, err := loadOrgIDCarriers(modelsDir)
	if err != nil {
		fatal("load carriers from %s: %v", modelsDir, err)
	}

	files, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil {
		fatal("glob %s: %v", dir, err)
	}
	if len(files) == 0 {
		fatal("no Go files found under %q", dir)
	}
	sort.Strings(files)

	fset := token.NewFileSet()
	var violations []string
	scanned := 0

	// First pass: parse every file and collect all `*XxxStore` type
	// declarations so checkFunc can restrict its attention to methods whose
	// receiver is an actual declared store (defense against a future helper
	// type whose name happens to end in "Store").
	parsed := make([]*ast.File, 0, len(files))
	stores := map[string]bool{}
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		scanned++
		// #nosec G304 -- f comes from filepath.Glob over internal/db; not user input.
		src, err := os.ReadFile(f)
		if err != nil {
			fatal("read %s: %v", f, err)
		}
		file, err := parser.ParseFile(fset, f, src, parser.ParseComments)
		if err != nil {
			fatal("parse %s: %v", f, err)
		}
		parsed = append(parsed, file)
		collectStoreTypes(file, stores)
	}

	for _, file := range parsed {
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok {
				continue
			}
			v := checkFunc(fset, fn, carriers, stores)
			if v != "" {
				violations = append(violations, v)
			}
		}
	}

	if len(violations) == 0 {
		fmt.Printf("lint-stores: OK — scanned %d file(s) under %s (%d carrier types loaded)\n", scanned, dir, len(carriers))
		return
	}

	fmt.Fprintln(os.Stderr, "lint-stores: org_id enforcement violations")
	for _, v := range violations {
		fmt.Fprintln(os.Stderr, "  "+v)
	}
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "Every exported *Store method must accept org scope — either as")
	fmt.Fprintln(os.Stderr, "  orgID uuid.UUID (preferred), or via a *models.X carrier whose")
	fmt.Fprintln(os.Stderr, "  struct declares an OrgID field.")
	fmt.Fprintln(os.Stderr, "If the method is legitimately cross-org (pre-auth, system cleanup),")
	fmt.Fprintln(os.Stderr, "add a comment above it (the reason clause is required):")
	fmt.Fprintln(os.Stderr, `  // lint:allow-no-orgid reason="..."`)
	os.Exit(1)
}

// loadOrgIDCarriers parses every Go file in modelsDir and returns the set of
// struct type names that declare (or inherit via embedding) a field literally
// named "OrgID". These are the only types accepted as `*models.X` carriers
// by the lint.
//
// Inheritance resolution is transitive: if `Session` embeds `BaseEntity` and
// `BaseEntity` declares `OrgID`, then `Session` is a carrier. Resolution
// runs to a fixed point so long embedding chains resolve correctly.
func loadOrgIDCarriers(modelsDir string) (map[string]bool, error) {
	files, err := filepath.Glob(filepath.Join(modelsDir, "*.go"))
	if err != nil {
		return nil, fmt.Errorf("glob: %w", err)
	}
	structs := map[string]*ast.StructType{}
	fset := token.NewFileSet()
	for _, f := range files {
		if strings.HasSuffix(f, "_test.go") {
			continue
		}
		// #nosec G304 -- f comes from filepath.Glob over internal/models; not user input.
		src, err := os.ReadFile(f)
		if err != nil {
			return nil, fmt.Errorf("read %s: %w", f, err)
		}
		file, err := parser.ParseFile(fset, f, src, parser.SkipObjectResolution)
		if err != nil {
			return nil, fmt.Errorf("parse %s: %w", f, err)
		}
		for _, decl := range file.Decls {
			gen, ok := decl.(*ast.GenDecl)
			if !ok || gen.Tok != token.TYPE {
				continue
			}
			for _, spec := range gen.Specs {
				ts, ok := spec.(*ast.TypeSpec)
				if !ok {
					continue
				}
				st, ok := ts.Type.(*ast.StructType)
				if !ok || st.Fields == nil {
					continue
				}
				structs[ts.Name.Name] = st
			}
		}
	}

	carriers := map[string]bool{}
	// Seed with structs that directly declare OrgID.
	for name, st := range structs {
		if structHasDirectOrgIDField(st) {
			carriers[name] = true
		}
	}
	// Propagate via embedded fields until fixed point. A struct that embeds
	// any known carrier becomes a carrier itself.
	for changed := true; changed; {
		changed = false
		for name, st := range structs {
			if carriers[name] {
				continue
			}
			if structEmbedsCarrier(st, carriers) {
				carriers[name] = true
				changed = true
			}
		}
	}
	return carriers, nil
}

// collectStoreTypes records every top-level type declaration whose name ends
// in "Store" into `out`. Used to bound the store-method check to types that
// actually exist in the scanned package.
func collectStoreTypes(file *ast.File, out map[string]bool) {
	for _, decl := range file.Decls {
		gen, ok := decl.(*ast.GenDecl)
		if !ok || gen.Tok != token.TYPE {
			continue
		}
		for _, spec := range gen.Specs {
			ts, ok := spec.(*ast.TypeSpec)
			if !ok {
				continue
			}
			if strings.HasSuffix(ts.Name.Name, "Store") {
				out[ts.Name.Name] = true
			}
		}
	}
}

// structHasDirectOrgIDField reports whether st declares a field literally
// named "OrgID" on the struct itself (i.e. not reached via embedding).
func structHasDirectOrgIDField(st *ast.StructType) bool {
	for _, field := range st.Fields.List {
		for _, name := range field.Names {
			if name.Name == orgIDFieldName {
				return true
			}
		}
	}
	return false
}

// structEmbedsCarrier reports whether st has an embedded field (one with no
// explicit name) whose type name is already known to be a carrier. Handles
// both `Embedded` and `*Embedded` forms; only considers embeddings that
// resolve within the same `models` package (no cross-package resolution).
func structEmbedsCarrier(st *ast.StructType, carriers map[string]bool) bool {
	for _, field := range st.Fields.List {
		// Embedded fields have no explicit name.
		if len(field.Names) != 0 {
			continue
		}
		name := embeddedTypeName(field.Type)
		if name != "" && carriers[name] {
			return true
		}
	}
	return false
}

// embeddedTypeName returns the local type name of an embedded field, or "" if
// the field embeds a type from another package (which we don't attempt to
// resolve). `Foo` and `*Foo` both return "Foo"; `other.Foo` returns "".
func embeddedTypeName(expr ast.Expr) string {
	if star, ok := expr.(*ast.StarExpr); ok {
		return embeddedTypeName(star.X)
	}
	if ident, ok := expr.(*ast.Ident); ok {
		return ident.Name
	}
	return ""
}

// checkFunc returns a violation string if fn is a non-compliant exported
// method on a known store type, or "" otherwise. `stores` is the set of
// `*XxxStore` type names declared in the scanned files — methods whose
// receiver isn't in that set are skipped, so we only police real stores.
func checkFunc(fset *token.FileSet, fn *ast.FuncDecl, carriers, stores map[string]bool) string {
	// Must be a method on a *XxxStore receiver declared in the scanned files.
	if fn.Recv == nil || len(fn.Recv.List) == 0 {
		return ""
	}
	recv := receiverTypeName(fn.Recv.List[0].Type)
	if !stores[recv] {
		return ""
	}
	// Only check exported methods.
	if !fn.Name.IsExported() {
		return ""
	}

	pos := fset.Position(fn.Pos())
	// A malformed opt-out (marker without reason="...") is itself a violation:
	// bare `// lint:allow-no-orgid` would silently disable the check otherwise.
	bare, proper := classifyAllowComment(fn.Doc)
	if proper {
		return ""
	}
	if bare {
		return fmt.Sprintf(`%s:%d: (%s).%s has a bare %q comment; the reason="..." clause is required`,
			pos.Filename, pos.Line, recv, fn.Name.Name, allowMarker)
	}

	if hasOrgScope(fn.Type.Params, carriers) {
		return ""
	}

	return fmt.Sprintf("%s:%d: (%s).%s is missing org scope (add orgID uuid.UUID, pass a *models.X carrier with an OrgID field, or add %q)",
		pos.Filename, pos.Line, recv, fn.Name.Name, allowMarker+` reason="..."`)
}

func receiverTypeName(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.StarExpr:
		return receiverTypeName(t.X)
	case *ast.Ident:
		return t.Name
	case *ast.IndexExpr:
		return receiverTypeName(t.X)
	case *ast.IndexListExpr:
		return receiverTypeName(t.X)
	}
	return ""
}

// hasOrgScope returns true if params contain either:
//  1. a uuid.UUID parameter whose name matches orgIDParamNameRE
//     (e.g. `orgID`, `OrgID`, `org_id`, `srcOrgID`); OR
//  2. a pointer-to or value `models.X` parameter where X is a known carrier
//     (loaded from internal/models/*.go and verified to declare an OrgID
//     field) — used by Create/Upsert methods that receive the whole entity.
func hasOrgScope(params *ast.FieldList, carriers map[string]bool) bool {
	if params == nil {
		return false
	}
	for _, field := range params.List {
		// A field can declare multiple names for one type: `a, b uuid.UUID`.
		if isUUIDType(field.Type) {
			for _, name := range field.Names {
				if orgIDParamNameRE.MatchString(name.Name) {
					return true
				}
			}
			continue
		}
		if isCarrierType(field.Type, carriers) {
			return true
		}
	}
	return false
}

// isUUIDType reports whether the type expression is uuid.UUID.
func isUUIDType(expr ast.Expr) bool {
	sel, ok := expr.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	if !ok {
		return false
	}
	return pkg.Name == "uuid" && sel.Sel.Name == "UUID"
}

// isCarrierType reports whether the type expression is a `*models.X` or
// `models.X` parameter where X is a known carrier — i.e. its struct
// declaration in internal/models contains an OrgID field. The carriers
// set is built up-front by loadOrgIDCarriers; types not present return
// false even if syntactically they look like a carrier.
func isCarrierType(expr ast.Expr, carriers map[string]bool) bool {
	// Unwrap pointer.
	if star, ok := expr.(*ast.StarExpr); ok {
		return isCarrierType(star.X, carriers)
	}
	sel, ok := expr.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	if !ok {
		return false
	}
	if pkg.Name != "models" {
		return false
	}
	return carriers[sel.Sel.Name]
}

// classifyAllowComment inspects the doc comment for the opt-out marker.
// Returns (bare, proper): bare=true when the marker appears without a
// reason="..." clause (a malformed opt-out), proper=true when a properly-
// formed marker is present. Both false means no opt-out was present.
func classifyAllowComment(doc *ast.CommentGroup) (bare, proper bool) {
	if doc == nil {
		return false, false
	}
	for _, c := range doc.List {
		if allowCommentRE.MatchString(c.Text) {
			return false, true
		}
		if strings.Contains(c.Text, allowMarker) {
			bare = true
		}
	}
	return bare, false
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "lint-stores: "+format+"\n", args...)
	os.Exit(2)
}
