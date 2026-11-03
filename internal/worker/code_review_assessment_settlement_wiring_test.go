package worker

import (
	"bytes"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestFullAssessmentSettlementUsesSuppliedTransaction(t *testing.T) {
	t.Parallel()
	for _, name := range []string{"reconcileCompletedFullAssessment", "completeFullAssessment"} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			files := token.NewFileSet()
			source, err := parser.ParseFile(files, "code_review_assessment.go", nil, 0)
			require.NoError(t, err, "parse the actual full assessment worker consumers")
			var calls, forbidden []string
			for _, declaration := range source.Decls {
				function, ok := declaration.(*ast.FuncDecl)
				if !ok || function.Name.Name != name {
					continue
				}
				ast.Inspect(function.Body, func(node ast.Node) bool {
					call, ok := node.(*ast.CallExpr)
					if !ok {
						return true
					}
					selector, ok := call.Fun.(*ast.SelectorExpr)
					if !ok {
						return true
					}
					if selector.Sel.Name == "SettleAssessmentInTx" {
						var expression bytes.Buffer
						require.NoError(t, format.Node(&expression, files, call), "retain the exact outer transaction argument and nil-streams store binding")
						calls = append(calls, expression.String())
					}
					if selector.Sel.Name == "SettleAssessment" || selector.Sel.Name == "SetStreams" {
						forbidden = append(forbidden, selector.Sel.Name)
					}
					return true
				})
			}
			// This is deliberately a source-wiring assertion. Full-schema DB and
			// existing worker integration tests separately prove runtime behavior.
			require.Equal(t, []string{"db.NewCodeReviewScheduleStore(tx).SettleAssessmentInTx(ctx, assessment.OrgID, tx, assessment.ID)"}, calls, "each actual worker owner must compose settlement through exactly its supplied transaction")
			require.Empty(t, forbidden, "existing nil-streams temporary stores must not enter a nested wrapper or gain new notification wiring")
		})
	}
}
