package main

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"go/types"
	"regexp"
	"strconv"
	"strings"
	"unicode"
)

const allowMarker = "lint:allow-log-pii"

var allowCommentRE = regexp.MustCompile(`lint:allow-log-pii\s+reason="[^"]+"`)

// keyedFieldMethods are zerolog Event/Context methods whose first argument is
// the field key and whose remaining arguments are written into the log line.
// Numeric, boolean, and time fields cannot carry the data this lint looks for.
var keyedFieldMethods = set(
	"Str", "Strs", "Stringer", "Stringers", "Interface", "Any", "RawJSON", "RawCBOR", "Bytes", "Hex",
	"IPAddr", "IPPrefix", "MACAddr", "Object", "Array", "Dict", "AnErr",
)

// recordFieldMethods serialize whatever they are given, so passing a whole
// user record or request header map writes every field it holds.
var recordFieldMethods = set("Interface", "Any", "RawJSON", "RawCBOR", "Stringer", "Stringers")

// serializingCalls render their arguments in full, so a whole record passed
// to them is as revealing as passing it to Interface.
var serializingCalls = set("Sprint", "Sprintf", "Sprintln", "Marshal", "MarshalIndent")

// printMethods are the printf-style methods shared by the standard library
// log package and zerolog.Logger. They count as logging only when called on
// something named like a logger, so fmt.Printf CLI output is not flagged.
var printMethods = set("Print", "Printf", "Println", "Fatal", "Fatalf", "Fatalln", "Panic", "Panicf", "Panicln")

// opaqueCallPrefixes name calls that return a value derived from their
// argument without revealing it (a length, a predicate, a hash, or a redacted
// form), so their arguments are not inspected.
var opaqueCallPrefixes = []string{"redact", "mask", "hash", "anonymi", "pseudonymi", "has", "is", "count"}

var opaqueBuiltins = set("len", "cap")

var identifierLiteralRE = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_.-]*$`)

type finding struct {
	File     string
	Line     int
	Func     string
	Category category
	Subject  string
}

func (f finding) String() string {
	return fmt.Sprintf("%s:%d: %s logs %s (%s)", f.File, f.Line, f.Func, f.Category, f.Subject)
}

func (f finding) baselineKey() string {
	return strings.Join([]string{f.File, f.Func, f.Subject}, "\t")
}

type checkResult struct {
	findings     []finding
	markerErrors []string
}

type checker struct {
	fset        *token.FileSet
	path        string
	allowedLine map[int]bool
	funcName    string
	stmtLine    int
	findings    []finding
}

func checkSource(path string, src []byte) (checkResult, error) {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, src, parser.ParseComments)
	if err != nil {
		return checkResult{}, fmt.Errorf("parse %s: %w", path, err)
	}

	allowed, markerErrors := collectMarkers(fset, path, file)
	c := &checker{fset: fset, path: path, allowedLine: allowed}
	for _, decl := range file.Decls {
		c.funcName = declName(decl)
		c.walk(decl)
	}
	return checkResult{findings: c.findings, markerErrors: markerErrors}, nil
}

func collectMarkers(fset *token.FileSet, path string, file *ast.File) (map[int]bool, []string) {
	allowed := map[int]bool{}
	var errs []string
	for _, group := range file.Comments {
		for _, comment := range group.List {
			if !strings.Contains(comment.Text, allowMarker) {
				continue
			}
			line := fset.Position(comment.Slash).Line
			if !allowCommentRE.MatchString(comment.Text) {
				errs = append(errs, fmt.Sprintf("%s:%d: %s needs a reason=\"...\" clause", path, line, allowMarker))
				continue
			}
			allowed[line] = true
			allowed[fset.Position(comment.End()).Line] = true
		}
	}
	return allowed, errs
}

func declName(decl ast.Decl) string {
	fn, ok := decl.(*ast.FuncDecl)
	if !ok {
		return "package scope"
	}
	if fn.Recv == nil || len(fn.Recv.List) == 0 {
		return fn.Name.Name
	}
	return "(" + types.ExprString(fn.Recv.List[0].Type) + ")." + fn.Name.Name
}

func (c *checker) walk(root ast.Node) {
	var stack []ast.Node
	var stmtLines []int
	ast.Inspect(root, func(n ast.Node) bool {
		if n == nil {
			if isStatement(stack[len(stack)-1]) {
				stmtLines = stmtLines[:len(stmtLines)-1]
			}
			stack = stack[:len(stack)-1]
			return true
		}
		stack = append(stack, n)
		if isStatement(n) {
			stmtLines = append(stmtLines, c.line(n.Pos()))
		}
		if call, ok := n.(*ast.CallExpr); ok {
			c.stmtLine = 0
			if len(stmtLines) > 0 {
				c.stmtLine = stmtLines[len(stmtLines)-1]
			}
			c.checkCall(call)
		}
		return true
	})
}

func isStatement(n ast.Node) bool {
	if _, ok := n.(ast.Stmt); !ok {
		return false
	}
	_, isBlock := n.(*ast.BlockStmt)
	return !isBlock
}

func (c *checker) line(pos token.Pos) int {
	return c.fset.Position(pos).Line
}

func (c *checker) checkCall(call *ast.CallExpr) {
	sel, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return
	}
	switch method := sel.Sel.Name; {
	case keyedFieldMethods[method]:
		if len(call.Args) == 0 {
			return
		}
		key, ok := stringLiteral(call.Args[0])
		if !ok {
			return
		}
		c.checkField(call.Args[0], key, call.Args[1:], recordFieldMethods[method])
	case method == "Fields":
		if len(call.Args) == 1 {
			c.checkFieldsLiteral(call.Args[0])
		}
	case method == "Msg" || method == "Err":
		c.checkValues(call.Args)
	case method == "Msgf":
		if len(call.Args) > 1 {
			c.checkValues(call.Args[1:])
		}
	case printMethods[method] && strings.Contains(strings.ToLower(lastName(sel.X)), "log"):
		c.checkValues(call.Args)
	}
}

func (c *checker) checkField(keyExpr ast.Expr, key string, values []ast.Expr, recordField bool) {
	if cat, ok := classifyName(key); ok {
		c.report(keyExpr.Pos(), cat, fmt.Sprintf("field %q", key))
		return
	}
	for _, v := range values {
		c.checkValue(v, recordField)
	}
}

func (c *checker) checkValues(values []ast.Expr) {
	for _, v := range values {
		c.checkValue(v, false)
	}
}

func (c *checker) checkFieldsLiteral(arg ast.Expr) {
	lit, ok := arg.(*ast.CompositeLit)
	if !ok {
		return
	}
	if _, isMap := lit.Type.(*ast.MapType); isMap {
		for _, elt := range lit.Elts {
			kv, ok := elt.(*ast.KeyValueExpr)
			if !ok {
				continue
			}
			if key, ok := stringLiteral(kv.Key); ok {
				c.checkField(kv.Key, key, []ast.Expr{kv.Value}, true)
				continue
			}
			c.checkValue(kv.Value, true)
		}
		return
	}
	for i := 0; i < len(lit.Elts); i += 2 {
		key, ok := stringLiteral(lit.Elts[i])
		if !ok || i+1 >= len(lit.Elts) {
			c.checkValues(lit.Elts[i:min(i+2, len(lit.Elts))])
			continue
		}
		c.checkField(lit.Elts[i], key, lit.Elts[i+1:i+2], true)
	}
}

// checkValue reports identifiers, struct fields, getters, map keys, and
// header lookups inside a logged value that name personal data. It follows
// string building (concatenation, conversions, formatting calls) but not
// comparisons or opaque calls such as len(email) or redactEmail(email),
// whose results do not reveal the value.
func (c *checker) checkValue(expr ast.Expr, recordField bool) {
	switch e := expr.(type) {
	case *ast.Ident:
		c.checkName(e, e.Name, recordField)
	case *ast.SelectorExpr:
		if cat, ok := classifyName(e.Sel.Name); ok {
			c.report(e.Pos(), cat, "value "+types.ExprString(e))
			return
		}
		if recv := lastName(e.X); recv != "" {
			if cat, ok := classifyName(recv + "_" + e.Sel.Name); ok && cat == categoryPersonName {
				c.report(e.Pos(), cat, "value "+types.ExprString(e))
				return
			}
		}
		if recordField {
			c.checkRecord(e, e.Sel.Name)
		}
	case *ast.CallExpr:
		c.checkCallValue(e, recordField)
	case *ast.IndexExpr:
		if key, ok := stringLiteral(e.Index); ok {
			if cat, ok := classifyName(key); ok {
				c.report(e.Pos(), cat, "value "+types.ExprString(e))
				return
			}
		}
		c.checkValue(e.X, false)
	case *ast.BinaryExpr:
		if e.Op == token.ADD {
			c.checkValue(e.X, false)
			c.checkValue(e.Y, false)
		}
	case *ast.ParenExpr:
		c.checkValue(e.X, recordField)
	case *ast.StarExpr:
		c.checkValue(e.X, recordField)
	case *ast.UnaryExpr:
		if e.Op == token.AND {
			c.checkValue(e.X, recordField)
		}
	case *ast.SliceExpr:
		c.checkValue(e.X, false)
	case *ast.TypeAssertExpr:
		c.checkValue(e.X, recordField)
	case *ast.CompositeLit:
		for _, elt := range e.Elts {
			kv, ok := elt.(*ast.KeyValueExpr)
			if !ok {
				c.checkValue(elt, recordField)
				continue
			}
			name := ""
			if key, ok := stringLiteral(kv.Key); ok {
				name = key
			} else if ident, ok := kv.Key.(*ast.Ident); ok {
				name = ident.Name
			}
			if cat, ok := classifyName(name); ok {
				c.report(kv.Pos(), cat, "value "+types.ExprString(kv))
				continue
			}
			c.checkValue(kv.Value, recordField)
		}
	}
}

func (c *checker) checkName(ident *ast.Ident, name string, recordField bool) {
	if cat, ok := classifyName(name); ok {
		c.report(ident.Pos(), cat, "value "+name)
		return
	}
	if recordField {
		c.checkRecord(ident, name)
	}
}

func (c *checker) checkRecord(expr ast.Expr, name string) {
	if cat, ok := classifyRecord(name); ok {
		c.report(expr.Pos(), cat, "value "+types.ExprString(expr))
	}
}

func (c *checker) checkCallValue(call *ast.CallExpr, recordField bool) {
	name := calleeName(call.Fun)
	if opaqueBuiltins[name] || hasOpaquePrefix(name) {
		return
	}
	for _, arg := range call.Args {
		if lit, ok := stringLiteral(arg); ok && identifierLiteralRE.MatchString(lit) {
			if cat, ok := classifyName(lit); ok {
				c.report(call.Pos(), cat, "value "+types.ExprString(call))
				return
			}
		}
	}
	sel, isMethod := call.Fun.(*ast.SelectorExpr)
	if isMethod && len(call.Args) == 0 {
		getter := strings.TrimPrefix(sel.Sel.Name, "Get")
		if cat, ok := classifyName(getter); ok {
			c.report(call.Pos(), cat, "value "+types.ExprString(call))
			return
		}
		if recv := lastName(sel.X); recv != "" {
			if cat, ok := classifyName(recv + "_" + getter); ok && cat == categoryPersonName {
				c.report(call.Pos(), cat, "value "+types.ExprString(call))
				return
			}
		}
		c.checkValue(sel.X, recordField)
		return
	}
	serializes := serializingCalls[name]
	for _, arg := range call.Args {
		c.checkValue(arg, serializes)
	}
}

func (c *checker) report(pos token.Pos, cat category, subject string) {
	line := c.line(pos)
	if c.allowedLine[line] || c.allowedLine[line-1] || c.allowedLine[c.stmtLine] || c.allowedLine[c.stmtLine-1] {
		return
	}
	c.findings = append(c.findings, finding{
		File:     c.path,
		Line:     line,
		Func:     c.funcName,
		Category: cat,
		Subject:  subject,
	})
}

func calleeName(fun ast.Expr) string {
	switch f := fun.(type) {
	case *ast.Ident:
		return f.Name
	case *ast.SelectorExpr:
		return f.Sel.Name
	case *ast.IndexExpr:
		return calleeName(f.X)
	}
	return ""
}

func hasOpaquePrefix(name string) bool {
	lower := strings.ToLower(name)
	for _, prefix := range opaqueCallPrefixes {
		if !strings.HasPrefix(lower, prefix) {
			continue
		}
		rest := name[len(prefix):]
		if rest == "" || unicode.IsUpper([]rune(rest)[0]) || prefix == "anonymi" || prefix == "pseudonymi" {
			return true
		}
	}
	return false
}

func lastName(expr ast.Expr) string {
	switch e := expr.(type) {
	case *ast.Ident:
		return e.Name
	case *ast.SelectorExpr:
		return e.Sel.Name
	case *ast.StarExpr:
		return lastName(e.X)
	case *ast.ParenExpr:
		return lastName(e.X)
	}
	return ""
}

func stringLiteral(expr ast.Expr) (string, bool) {
	lit, ok := expr.(*ast.BasicLit)
	if !ok || lit.Kind != token.STRING {
		return "", false
	}
	value, err := strconv.Unquote(lit.Value)
	if err != nil {
		return "", false
	}
	return value, true
}
