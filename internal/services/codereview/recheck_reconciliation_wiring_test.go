package codereview

import (
	"bytes"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"testing"

	"github.com/stretchr/testify/require"
)

// This source-level regression checks the real caller's supplied-tx wiring; the
// DB full-schema tests separately establish runtime settlement and visibility.
func TestForcedEvidenceReconciliationUsesSuppliedTransaction(t *testing.T) {
	t.Parallel()
	files := token.NewFileSet()
	source, err := parser.ParseFile(files, "recheck_admission.go", nil, 0)
	require.NoError(t, err, "parse the actual force-fresh cancellation implementation")
	var callbacks []*ast.FuncLit
	for _, declaration := range source.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if !ok || function.Name.Name != "cancelForcedEvidenceAssessment" {
			continue
		}
		ast.Inspect(function.Body, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok {
				return true
			}
			selector, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || selector.Sel.Name != "WithLockedPR" {
				return true
			}
			require.Len(t, call.Args, 5, "the actual domain owner must retain its explicit PR callback argument")
			callback, ok := call.Args[4].(*ast.FuncLit)
			require.True(t, ok, "force-fresh reconciliation must be supplied the existing owner's concrete transaction")
			callbacks = append(callbacks, callback)
			return true
		})
	}
	require.Len(t, callbacks, 2, "force-fresh cancellation retains separate durable cancellation and later drain-reconciliation owners")
	callback := callbacks[1]
	var transaction bytes.Buffer
	require.NoError(t, format.Node(&transaction, files, callback.Type.Params.List[0].Names[0]), "read the transaction parameter owned by the actual drain callback")
	var actual []string
	var forbidden []string
	ast.Inspect(callback.Body, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		selector, ok := call.Fun.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		switch selector.Sel.Name {
		case "ReconcileDrainedTerminalTurnInTx":
			var expression bytes.Buffer
			require.NoError(t, format.Node(&expression, files, call), "retain the concrete same-tx reconciliation call including its store binding")
			actual = append(actual, expression.String())
		case "ReconcileDrainedTerminalTurn", "Begin", "Commit", "Rollback", "Notify", "PublishUpdated":
			forbidden = append(forbidden, selector.Sel.Name)
		}
		return true
	})
	require.Equal(t, "tx", transaction.String(), "the existing PR owner must keep passing its original explicit transaction")
	require.Equal(t, []string{"db.NewCodeReviewRecheckStore(tx).ReconcileDrainedTerminalTurnInTx(ctx, orgID, tx, assessmentID)"}, actual, "the real force-fresh path must use exactly its supplied transaction, not a pool or nested wrapper")
	require.Empty(t, forbidden, "the callback must not settle, notify, publish or enter the legacy nested transaction wrapper")
}
