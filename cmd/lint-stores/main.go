// Command lint-stores enforces that every exported method on a *XxxStore
// type under internal/db takes org_id into account — either as an explicit
// `orgID uuid.UUID` parameter, or via a carrier struct (e.g. `*models.User`)
// whose OrgID field is used internally.
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
	"sort"
	"strings"
)

const allowComment = "lint:allow-no-orgid"

func main() {
	dir := "internal/db"
	if len(os.Args) > 1 {
		dir = os.Args[1]
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
			v := checkFunc(fset, fn)
			if v != "" {
				violations = append(violations, v)
			}
		}
	}

	if len(violations) == 0 {
		fmt.Printf("lint-stores: OK — scanned %d file(s) under %s\n", scanned, dir)
		return
	}

	fmt.Fprintln(os.Stderr, "lint-stores: org_id enforcement violations")
	for _, v := range violations {
		fmt.Fprintln(os.Stderr, "  "+v)
	}
	fmt.Fprintln(os.Stderr, "")
	fmt.Fprintln(os.Stderr, "Every exported *Store method must accept org scope — either as")
	fmt.Fprintln(os.Stderr, "  orgID uuid.UUID (preferred), or via a carrier struct (*models.X).")
	fmt.Fprintln(os.Stderr, "If the method is legitimately cross-org (pre-auth, system cleanup),")
	fmt.Fprintln(os.Stderr, "add a comment above it:")
	fmt.Fprintln(os.Stderr, `  // lint:allow-no-orgid reason="..."`)
	os.Exit(1)
}

// checkFunc returns a violation string if fn is a non-compliant exported
// *Store method, or "" otherwise.
func checkFunc(fset *token.FileSet, fn *ast.FuncDecl) string {
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

	if hasOrgScope(fn.Type.Params) {
		return ""
	}

	pos := fset.Position(fn.Pos())
	return fmt.Sprintf("%s:%d: (%s).%s is missing org scope (add orgID uuid.UUID, pass a *models.X carrier, or add %q)",
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
//  1. a parameter whose name contains "org" (case-insensitive) with type uuid.UUID, OR
//  2. a pointer-to-struct or struct-value parameter from a package (a "carrier"),
//     used by Create/Upsert-style methods that receive the whole entity.
func hasOrgScope(params *ast.FieldList) bool {
	if params == nil {
		return false
	}
	for _, field := range params.List {
		// A field can declare multiple names for one type: `a, b uuid.UUID`.
		if isUUIDType(field.Type) {
			for _, name := range field.Names {
				if strings.Contains(strings.ToLower(name.Name), "org") {
					return true
				}
			}
			continue
		}
		if isCarrierType(field.Type) {
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

// isCarrierType reports whether the type looks like a struct carrier whose
// OrgID field is used internally. Heuristic: *pkg.Type or pkg.Type where pkg
// is "models" or the type name starts with an uppercase letter (user struct).
func isCarrierType(expr ast.Expr) bool {
	// Unwrap pointer.
	if star, ok := expr.(*ast.StarExpr); ok {
		return isCarrierType(star.X)
	}
	sel, ok := expr.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	pkg, ok := sel.X.(*ast.Ident)
	if !ok {
		return false
	}
	// Accept carriers from the models package; that's where entity structs
	// live in this repo and they conventionally have OrgID fields.
	return pkg.Name == "models"
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
