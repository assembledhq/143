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

func TestRecheckPublicationSettlementUsesOwnerTransaction(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		check func(*testing.T, *token.FileSet, *ast.FuncLit, *ast.FuncLit)
	}{
		{
			name: "concrete transaction and unchanged rejection",
			check: func(t *testing.T, files *token.FileSet, _ *ast.FuncLit, complete *ast.FuncLit) {
				var assertions, rejectionErrors, stores, settlements []string
				ast.Inspect(complete.Body, func(node ast.Node) bool {
					if assignment, ok := node.(*ast.AssignStmt); ok && len(assignment.Rhs) == 1 {
						if assertion, ok := assignment.Rhs[0].(*ast.TypeAssertExpr); ok && recheckSettlementWiringText(t, files, assertion.Type) == "pgx.Tx" {
							assertions = append(assertions, recheckSettlementWiringText(t, files, assignment))
						}
					}
					call, ok := node.(*ast.CallExpr)
					if !ok {
						return true
					}
					switch recheckSettlementWiringCallName(call) {
					case "New":
						if recheckSettlementWiringText(t, files, call.Fun) == "errors.New" {
							rejectionErrors = append(rejectionErrors, recheckSettlementWiringText(t, files, call))
						}
					case "NewCodeReviewScheduleStore":
						stores = append(stores, recheckSettlementWiringText(t, files, call))
					case "SettleAssessmentInTx":
						settlements = append(settlements, recheckSettlementWiringText(t, files, call))
					}
					return true
				})
				require.Equal(t, []string{"publicationTx, ok := tx.(pgx.Tx)"}, assertions, "completion must retain the publication owner's concrete transaction")
				require.Equal(t, []string{`errors.New("assessment publication transaction cannot settle scheduler")`}, rejectionErrors, "an unavailable concrete transaction must retain the existing error")
				require.Equal(t, []string{"db.NewCodeReviewScheduleStore(publicationTx)"}, stores, "the temporary scheduler store must use only the supplied publication transaction")
				require.Equal(t, []string{"db.NewCodeReviewScheduleStore(publicationTx).SettleAssessmentInTx(lockCtx, a.OrgID, publicationTx, a.ID)"}, settlements, "scheduler settlement must receive the same publication transaction")
				var rejected bool
				for _, statement := range complete.Body.List {
					guard, ok := statement.(*ast.IfStmt)
					if !ok || guard.Init != nil || recheckSettlementWiringText(t, files, guard.Cond) != "!ok" {
						continue
					}
					rejected = recheckSettlementWiringText(t, files, guard.Body) == "{\n\treturn errors.New(\"assessment publication transaction cannot settle scheduler\")\n}"
				}
				require.True(t, rejected, "a failed pgx.Tx assertion must return the unchanged error before settlement")
			},
		},
		{
			name: "completion and optional enqueue keep their order",
			check: func(t *testing.T, files *token.FileSet, _ *ast.FuncLit, complete *ast.FuncLit) {
				var sequence, completions, updaterGuards []string
				var enqueues []*ast.CallExpr
				for _, statement := range complete.Body.List {
					if guard, ok := statement.(*ast.IfStmt); ok && guard.Init != nil {
						if assignment, ok := guard.Init.(*ast.AssignStmt); ok && len(assignment.Rhs) == 1 {
							if assertion, ok := assignment.Rhs[0].(*ast.TypeAssertExpr); ok && recheckSettlementWiringText(t, files, assertion.Type) == "codeReviewStatusCommentUpdater" {
								updaterGuards = append(updaterGuards, recheckSettlementWiringText(t, files, guard))
							}
						}
					}
					ast.Inspect(statement, func(node ast.Node) bool {
						if assertion, ok := node.(*ast.TypeAssertExpr); ok {
							switch recheckSettlementWiringText(t, files, assertion.Type) {
							case "pgx.Tx":
								sequence = append(sequence, "concrete publication transaction")
							case "codeReviewStatusCommentUpdater":
								sequence = append(sequence, "optional status-comment publisher")
							}
						}
						call, ok := node.(*ast.CallExpr)
						if !ok {
							return true
						}
						switch recheckSettlementWiringCallName(call) {
						case "Complete":
							sequence = append(sequence, "assessment completion")
							completions = append(completions, recheckSettlementWiringText(t, files, call))
						case "SettleAssessmentInTx":
							sequence = append(sequence, "scheduler settlement")
						case "EnqueueWithOpts":
							sequence = append(sequence, "status-comment enqueue")
							enqueues = append(enqueues, call)
						}
						return true
					})
				}
				require.Equal(t, []string{"assessment completion", "concrete publication transaction", "scheduler settlement", "optional status-comment publisher", "status-comment enqueue"}, sequence, "completion must settle before the optional final job enqueue without moving assessment completion")
				require.Equal(t, []string{"assessments.Complete(lockCtx, a.OrgID, a.ID, a.Generation, a.InputDigest, completion)"}, completions, "completion must retain the same assessment identity and staged result")
				require.Equal(t, []string{"if _, ok := services.CodeReviews.(codeReviewStatusCommentUpdater); !ok {\n\treturn nil\n}"}, updaterGuards, "publishers without status-comment support must still return success after settlement")
				require.Len(t, enqueues, 1, "completion must retain one final status-comment enqueue")
				require.Equal(t, "db.NewJobStore(tx).EnqueueWithOpts", recheckSettlementWiringText(t, files, enqueues[0].Fun), "the final status-comment job must remain in the owner transaction")
				require.Len(t, enqueues[0].Args, 3, "the final enqueue must retain context, tenant, and options arguments")
				require.Equal(t, "lockCtx", recheckSettlementWiringText(t, files, enqueues[0].Args[0]), "the final enqueue must use the publication callback context")
				require.Equal(t, "a.OrgID", recheckSettlementWiringText(t, files, enqueues[0].Args[1]), "the final enqueue must retain assessment tenant scoping")
				require.Equal(t, "return err", recheckSettlementWiringText(t, files, complete.Body.List[len(complete.Body.List)-1]), "the publication owner must still receive the final enqueue error")
			},
		},
		{
			name: "publication callback has no nested owner or fallback",
			check: func(t *testing.T, files *token.FileSet, callback *ast.FuncLit, _ *ast.FuncLit) {
				var forbidden []string
				ast.Inspect(callback.Body, func(node ast.Node) bool {
					if assertion, ok := node.(*ast.TypeAssertExpr); ok && recheckSettlementWiringText(t, files, assertion.Type) == "db.TxStarter" {
						forbidden = append(forbidden, "db.TxStarter assertion")
					}
					call, ok := node.(*ast.CallExpr)
					if !ok {
						return true
					}
					switch name := recheckSettlementWiringCallName(call); name {
					case "SettleAssessment", "WithLockedPR", "WithLockedPRInTx", "Begin", "NewTxStarter", "SetStreams", "settleFullAssessmentScheduler", "settleCodeReviewAssessment":
						forbidden = append(forbidden, name)
					}
					return true
				})
				require.Empty(t, forbidden, "the bounded publication callback must not open nested ownership, use a fallback transaction, or add stream notifications")
			},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			files, callback, complete := recheckPublicationCompletionWiring(t)
			// These are source-wiring pins, not type-checking or runtime proof.
			// Full-schema publication-owner tests establish database atomicity.
			tt.check(t, files, callback, complete)
		})
	}
}

func recheckPublicationCompletionWiring(t *testing.T) (*token.FileSet, *ast.FuncLit, *ast.FuncLit) {
	t.Helper()
	files := token.NewFileSet()
	source, err := parser.ParseFile(files, "code_review_recheck_handler.go", nil, 0)
	require.NoError(t, err, "parse the actual recheck publication worker consumer")
	var callbacks []*ast.FuncLit
	for _, declaration := range source.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if !ok || function.Name.Name != "publishCodeReviewRecheck" {
			continue
		}
		ast.Inspect(function.Body, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok || recheckSettlementWiringCallName(call) != "RunWithGitHubPublicationLock" {
				return true
			}
			require.Len(t, call.Args, 4, "publication locking must retain its existing owner callback signature")
			callback, ok := call.Args[3].(*ast.FuncLit)
			require.True(t, ok, "publication completion must stay inside the concrete owner callback")
			callbacks = append(callbacks, callback)
			return false
		})
	}
	require.Len(t, callbacks, 1, "the actual recheck publisher must have exactly one publication owner callback")
	var completions []*ast.FuncLit
	for _, statement := range callbacks[0].Body.List {
		assignment, ok := statement.(*ast.AssignStmt)
		if !ok || len(assignment.Lhs) != 1 || len(assignment.Rhs) != 1 {
			continue
		}
		name, ok := assignment.Lhs[0].(*ast.Ident)
		if !ok || name.Name != "complete" {
			continue
		}
		complete, ok := assignment.Rhs[0].(*ast.FuncLit)
		require.True(t, ok, "the shared completion path must remain a closure in the publication owner")
		completions = append(completions, complete)
	}
	require.Len(t, completions, 1, "confirmed receipts and fresh publication must share one completion closure")
	return files, callbacks[0], completions[0]
}

func recheckSettlementWiringCallName(call *ast.CallExpr) string {
	switch function := call.Fun.(type) {
	case *ast.SelectorExpr:
		return function.Sel.Name
	case *ast.Ident:
		return function.Name
	default:
		return ""
	}
}

func recheckSettlementWiringText(t *testing.T, files *token.FileSet, node ast.Node) string {
	t.Helper()
	var text bytes.Buffer
	require.NoError(t, format.Node(&text, files, node), "format only the selected publication wiring expression")
	return text.String()
}
