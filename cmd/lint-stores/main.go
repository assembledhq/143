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
	allowComment     = "lint:allow-no-orgid"
	defaultStoresDir = "internal/db"
	defaultModelsDir = "internal/models"
	orgIDFieldName   = "OrgID"
)

// orgIDParamNameRE matches parameter names that end in "orgid" (case-
// insensitive), with an optional underscore: `OrgID`, `orgID`, `org_id`,
// `srcOrgID`, `targetOrgID`. Names like `organizerID` deliberately do NOT
// match — substring matching on "org" was too permissive.
var orgIDParamNameRE = regexp.MustCompile(`(?i)org_?id$`)

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

		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok {
				continue
			}
			v := checkFunc(fset, fn, carriers)
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
	fmt.Fprintln(os.Stderr, "add a comment above it:")
	fmt.Fprintln(os.Stderr, `  // lint:allow-no-orgid reason="..."`)
	os.Exit(1)
}

// loadOrgIDCarriers parses every Go file in modelsDir and returns the set of
// struct type names that declare a field literally named "OrgID". These are
// the only types accepted as `*models.X` carriers by the lint.
func loadOrgIDCarriers(modelsDir string) (map[string]bool, error) {
	files, err := filepath.Glob(filepath.Join(modelsDir, "*.go"))
	if err != nil {
		return nil, fmt.Errorf("glob: %w", err)
	}
	carriers := map[string]bool{}
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
				if structHasOrgIDField(st) {
					carriers[ts.Name.Name] = true
				}
			}
		}
	}
	return carriers, nil
}

func structHasOrgIDField(st *ast.StructType) bool {
	for _, field := range st.Fields.List {
		for _, name := range field.Names {
			if name.Name == orgIDFieldName {
				return true
			}
		}
	}
	return false
}

// checkFunc returns a violation string if fn is a non-compliant exported
// *Store method, or "" otherwise.
func checkFunc(fset *token.FileSet, fn *ast.FuncDecl, carriers map[string]bool) string {
	// Must be a method on a *XxxStore receiver.
	if fn.Recv == nil || len(fn.Recv.List) == 0 {
		return ""
	}
	recv := receiverTypeName(fn.Recv.List[0].Type)
	if !strings.HasSuffix(recv, "Store") {
		return ""
	}
	// Only check exported methods.
	if !fn.Name.IsExported() {
		return ""
	}
	// Respect the opt-out comment.
	if hasAllowComment(fn.Doc) {
		return ""
	}

	if hasOrgScope(fn.Type.Params, carriers) {
		return ""
	}

	pos := fset.Position(fn.Pos())
	return fmt.Sprintf("%s:%d: (%s).%s is missing org scope (add orgID uuid.UUID, pass a *models.X carrier with an OrgID field, or add %q)",
		pos.Filename, pos.Line, recv, fn.Name.Name, allowComment)
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

func hasAllowComment(doc *ast.CommentGroup) bool {
	if doc == nil {
		return false
	}
	for _, c := range doc.List {
		if strings.Contains(c.Text, allowComment) {
			return true
		}
	}
	return false
}

func fatal(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "lint-stores: "+format+"\n", args...)
	os.Exit(2)
}
