// Command lint-lifecycle checks the reviewed Milestone 0 caller/writer inventory.
// It is a conservative syntax inventory, NOT a proof of lifecycle fencing or a
// whole-program call graph. See inventory.json for reviewed contracts and scope.
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"go/ast"
	"go/format"
	"go/parser"
	"go/token"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
)

type inventory struct {
	Version     int      `json:"version"`
	Scope       []string `json:"scope"`
	Limitations []string `json:"limitations"`
	Entries     []entry  `json:"entries"`
}

type entry struct {
	ID              string   `json:"id"`
	SourceSHA256    string   `json:"source_sha256"`
	Operation       string   `json:"operation"`
	Owner           string   `json:"owner"`
	Predicate       string   `json:"predicate"`
	Lock            string   `json:"lock"`
	StaleResult     string   `json:"stale_result"`
	Classification  string   `json:"classification"`
	Migration       string   `json:"migration"`
	Evidence        []string `json:"evidence"`
	CanonicalTarget string   `json:"canonical_target,omitempty"`
	Authority       string   `json:"authority,omitempty"`
	FenceOutcome    string   `json:"fence_outcome,omitempty"`
	Prerequisite0a  string   `json:"prerequisite_0a,omitempty"`
}

type candidate struct {
	ID           string   `json:"id"`
	Line         int      `json:"line"`
	SourceSHA256 string   `json:"source_sha256"`
	Kinds        []string `json:"kinds"`
	Calls        []string `json:"calls,omitempty"`
	SQL          []string `json:"sql,omitempty"`
	Guards       []string `json:"guards,omitempty"`
	Locks        []string `json:"locks,omitempty"`
	Results      []string `json:"results,omitempty"`
	Predicates   []string `json:"predicates,omitempty"`
}

type function struct {
	ast          *ast.FuncDecl
	file         string
	pkg          string
	symbol       string
	line         int
	calls        []call
	sql          []string
	kinds        []string
	links        []string
	guards       []string
	locks        []string
	results      []string
	dependencies []string
	predicates   []string
	trace        []string
}

type call struct {
	name     string
	expr     string
	local    bool
	receiver string
	boundary string
}

// Unambiguous lifecycle names cover non-status admission, start, seed, delivery,
// and cleanup seams. Generic names (Create, UpdateStatus, UpdateResult) are
// selected separately using receiver/argument evidence; matching them globally
// would classify every unrelated store as a lifecycle caller. The explicit InTx
// seams below are deliberately conservative global name candidates, including
// generic UpdateStatusInTx/UpdateResultInTx collisions: inventory review must
// classify unrelated uses rather than silently exclude generic receivers.
var boundaryNames = strings.Fields(`
lockLifecycleScopeInTx requireLifecycleScopeTransactionInTx
observeLifecycleCutoverInTx tryLockLifecycleCutoverSharedInTx tryLockLifecycleCutoverExclusiveInTx tryLockLifecycleCutoverInTx
SubmitSessionMessage SubmitThreadMessage AnswerQueuedHumanInputForContinue
answerQueuedHumanInputForContinue BeginRuntime beginRuntimeControl
RunSession RunAgent ContinueSession ContinueSessionWithOptions
SendMessage AnswerQuestion RetryInboxEntry
AppendForMessage PublishCheckpoint PublishCheckpointWithProvenance
PublishCheckpointWithProvenanceInTx publishCheckpointWithProvenanceRow
AppendApprovedSessionGrant AppendApprovedSessionGrantInTx AppendApprovedCapabilityGrant claimAutomationTurnAttempt
InsertGeneration RetireGeneration ReleaseStrandedOwnership
CreateRepairRun DeactivateRepairRun AnswerPending
AnswerLatestPendingForSession AnswerLatestPendingForThread
AnswerLatestPendingFreeTextBySession AnswerLatestPendingFreeTextByThread
AnswerRequest CancelRequest CancelPending
stopFullReviewFallbackLoop BumpWorkspaceRevision
SetPendingSnapshotKey PromotePendingSnapshot ClearPendingSnapshotKey ClearSnapshotKey
UpdateSnapshotInfo UpdateWorkspaceSnapshot writeDiffSnapshot AcceptSplit
PublishPrepared SetBaseSnapshot MarkRuntimeStopRequested UpdateRecoveryState
UpdatePreviewTargetSnapshotKey UpdatePreviewSourceWorkspaceRevision
UpdatePreviewRuntimeWorkspaceRevision CreateSnapshot DeleteOldestSnapshots
RetryWithLease RetryWithLeaseAndTarget RetryWithoutConsumingAttemptWithLease
RetryWithoutConsumingAttemptWithLeaseAndResetWindow RetryWithoutConsumingAttemptWithLeaseAndTarget
ClaimIdle ClaimForResume ClaimIdleForSession ClaimForResumeInSession
ClaimNextQueuedForSession ClaimForAutomationTurn ClaimPrimaryForAutomationTurn
ResetForRetry UndoResetForRetry RequestCancel MarkCancelRequested
MarkCancelRequestedBySessions ClaimDeliverableAfter AcknowledgeDelivered
ResetAfterLostReuse ResetForRuntime ReclaimLost ReclaimLostForSession
MarkAckedForSeedMessages MarkDeliveringForMessages MarkDeliveredForEntry
ResetUnsentDelivery CommitInboxDeliveryWithLease ReclaimExpiredLeases
RetryRecoverable ListDeliverableAfter ListAppliedMessageIDs
StartSeedPhase StartSeedPhaseInTx InspectContinuationRecovery RecordContinuationAttemptWithLease
acknowledgeInboxBatchInTx
AcknowledgeInboxBatch AcknowledgeInboxBatchWithTransition AcknowledgeInboxBatchAndStartPhase
ListAppliedRecoveryMessageIDs continuationRecoveryQuery
MarkCancelRequestedForTurn ReconcileTerminalReviews reconcileTerminalReviews
RetireOwnerForCompletedReplacement
UpdateTurnComplete UpdateTurnCompleteAndCompleteActivityPhase
UpdateResultAndCompleteActivityPhase UpdateStatusAndCompleteActivityPhase
UpdateResultAndCompleteActivityPhaseInTx UpdateStatusAndCompleteActivityPhaseInTx
UpdateTurnCompleteAndCompleteActivityPhaseInTx
UpdateStatusInTx CompleteTurnInTx UpdateResultInTx UpdateTurnCompleteInTx
MarkRunningWithSandboxState FailRunningBySession CompleteTurn
FailWorkspaceWaitForDeadLetter RecoverIncompleteCodeReviewWorkspace
ReconcileDrainedTerminalTurn ReconcileDrainedTerminalTurnInTx EndInterruptedAttempt CompleteExecutingPreflight
SettleAssessmentInTx withLockedPRInTx
settleAssessmentLockedPR
EndAttempt newTurnAttemptSessions
CompleteThreadTurnForAttempt FailThreadTurnForAttempt FailRetriesExhausted
CompleteFromMarker FailTerminalJob CreateStarting
CreateActive ReleaseWithLease AcquireLease BeginMaterialization
RecordMaterializedDiff CompleteMaterialization MaterializeChangeset
LaunchPreview ResumeStoppedWarmPreview UpdateRevisionContext
ConsumeCancelRequest MarkTerminalWithLease UpdateSandboxState ListExpiredSnapshots
CleanupSessionSnapshot
`)

var enqueueNames = strings.Fields(`Enqueue EnqueueInTx EnqueueWithTarget EnqueueWithOpts EnqueueInTxWithOpts`)

// These exact dispatch-key helpers are contract dependencies, unlike unrelated
// functions whose names merely contain Dedupe (for example finding dedupe).
var dedupeKeyNames = strings.Fields(`RunAgentDedupeKey ContinueSessionDedupeKey OpenPRDedupeKey AutomationTurnDedupeKey continueSessionDedupeKey continueSessionDrainDedupeKey`)

// Exact publication producers/uploader, not every CreatePR or Save method.
// They own ordinary object-key production as well as pending-key DB writes.
var snapshotPublicationRoots = []string{
	"internal/services/github/pr.go:PRService.CreatePR",
	"internal/services/github/pr.go:PRService.CreateBranch",
	"internal/services/github/pr.go:PRService.PushChangesToPR",
	"internal/services/github/pr.go:PRService.dispatchPostPRSnapshotUpload",
}

var snapshotPublicationCalls = strings.Fields(`SetPendingSnapshotKey dispatchPostPRSnapshotUpload Save PromotePendingSnapshot ClearPendingSnapshot`)

func snapshotPublicationRoot(id string) bool {
	return slices.Contains(snapshotPublicationRoots, id)
}

// Direct callers of these persistence/authority roots require a concrete
// operation contract, not only generic status callers. This is deliberately
// bounded to named roots: indirect wrapper propagation remains syntax-only.
var contractBoundaryNames = strings.Fields(`
BeginRuntime UpdateTurnComplete CompleteTurn
UpdateTurnCompleteAndCompleteActivityPhase UpdateResultAndCompleteActivityPhase
UpdateStatusAndCompleteActivityPhase
UpdateResultAndCompleteActivityPhaseInTx UpdateStatusAndCompleteActivityPhaseInTx
UpdateTurnCompleteAndCompleteActivityPhaseInTx
UpdateStatusInTx CompleteTurnInTx UpdateResultInTx UpdateTurnCompleteInTx StartSeedPhaseInTx
ReconcileDrainedTerminalTurnInTx
SettleAssessmentInTx withLockedPRInTx
settleAssessmentLockedPR
EndAttempt newTurnAttemptSessions
PublishCheckpointWithProvenanceInTx publishCheckpointWithProvenanceRow
acknowledgeInboxBatchInTx
AcknowledgeInboxBatch AcknowledgeInboxBatchWithTransition AcknowledgeInboxBatchAndStartPhase
ClaimIdle ClaimForResume ClaimIdleForSession ClaimForResumeInSession
ClaimNextQueuedForSession ClaimForAutomationTurn ClaimPrimaryForAutomationTurn
CreateStarting CreateActive ReleaseWithLease AcquireLease BeginMaterialization
RecordMaterializedDiff CompleteMaterialization MaterializeChangeset
LaunchPreview ResumeStoppedWarmPreview UpdateRevisionContext
ConsumeCancelRequest MarkTerminalWithLease UpdateSandboxState
MarkRunningWithSandboxState ResetForRetry UndoResetForRetry
ResetAfterLostReuse FailRunningBySession
CleanupSessionSnapshot AppendApprovedSessionGrantInTx AppendApprovedCapabilityGrant
`)

var lifecycleReceiver = regexp.MustCompile(`(?i)(session|thread|runtime|executor|runstore)`)
var lifecycleCreationReceiver = regexp.MustCompile(`(?i)(?:^|\.)[a-z_]*(?:sessions|threads|sessionstore|runstore|threadstore)\.`)
var mutationRE = regexp.MustCompile(`(?i)\b(UPDATE|INSERT\s+INTO|DELETE\s+FROM)\s+(?:(?:"?[a-z_][a-z0-9_]*"?)\s*\.\s*)?"?(sessions|session_threads)"?\b`)
var statusAssignmentRE = regexp.MustCompile(`(?i)\bstatus"?\s*=`)

// These non-status columns also gate admission, snapshot lineage and retained
// ownership. Discover their writes even when the method has an innocuous name.
var gateAssignmentRE = regexp.MustCompile(`(?i)\b(?:automation_owner_generation_id|code_review_owner_pr_id|ownership_release_pending|cancel_requested_at|archived_at|current_turn|workspace_generation|workspace_revision|snapshot_key|pending_snapshot_key|base_snapshot_key|latest_diff_snapshot_id|recovery_state|sandbox_state|container_id|worker_node_id|turn_holding_container)"?\s*=`)
var gateTableRE = regexp.MustCompile(`(?i)\b(?:UPDATE|INSERT\s+INTO|DELETE\s+FROM)\s+(?:(?:"?[a-z_][a-z0-9_]*"?)\s*\.\s*)?"?(automation_target_sessions|pull_request_repair_runs|session_human_input_requests|session_lifecycle_cutovers)"?\b`)
var statusColumnRE = regexp.MustCompile(`(?i)\bstatus\b`)
var whitespaceRE = regexp.MustCompile(`\s+`)
var updateRE = regexp.MustCompile(`(?i)\bUPDATE\s+`)
var lifecycleFromRE = regexp.MustCompile(`(?i)\bFROM\s+(?:(?:"?[a-z_][a-z0-9_]*"?)\s*\.\s*)?"?(sessions|session_threads)"?\s+(?:AS\s+)?"?([a-z_][a-z0-9_]*)"?\b`)
var aliasUpdateRE = regexp.MustCompile(`(?i)\bUPDATE\s+"?([a-z_][a-z0-9_]*)"?\s+SET\b`)
var guardRE = regexp.MustCompile(`(?i)(status|owner|generation|locktoken|lease|currentturn|expectedturn|ErrNoRows|RowsAffected|purpose|checkpoint)`)
var authorityAssignmentRE = regexp.MustCompile(`(?i)(status|turn|generation|locktoken|ownerid|leasetoken|snapshotkey|checkpoint|purpose)$`)
var sqlLockRE = regexp.MustCompile(`(?i)(FOR\s+(?:NO\s+KEY\s+)?UPDATE(?:\s+OF\s+[a-z_]+)?(?:\s+(?:NOWAIT|SKIP\s+LOCKED))?|FOR\s+(?:KEY\s+)?SHARE|pg_advisory_xact_lock|lock_timeout[^;\n]*)`)

func main() {
	root := flag.String("root", ".", "repository root")
	manifest := flag.String("inventory", "cmd/lint-lifecycle/inventory.json", "reviewed inventory relative to root")
	discover := flag.Bool("discover", false, "print discovered candidates as JSON; does not approve or write an inventory")
	jobManifest := flag.String("job-status-inventory", "cmd/lint-lifecycle/job-status-inventory.json", "reviewed jobs-status policies relative to root")
	discoverJobStatus := flag.Bool("discover-job-status", false, "print jobs-status syntax sites; does not approve policies")
	flag.Parse()
	if *discoverJobStatus {
		if err := runJobStatus(*root, *jobManifest, true, os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		return
	}
	if err := run(*root, *manifest, *discover, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if !*discover {
		if err := runJobStatus(*root, *jobManifest, false, os.Stdout); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
}

func run(root, manifest string, discover bool, out io.Writer) error {
	candidates, err := scan(root)
	if err != nil {
		return err
	}
	if discover {
		encoder := json.NewEncoder(out)
		encoder.SetIndent("", "  ")
		return encoder.Encode(candidates)
	}
	data, err := readRepositoryFile(root, filepath.Join(root, manifest))
	if err != nil {
		return fmt.Errorf("read lifecycle inventory: %w", err)
	}
	var reviewed inventory
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&reviewed); err != nil {
		return fmt.Errorf("decode lifecycle inventory: %w", err)
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return fmt.Errorf("lifecycle inventory must contain exactly one JSON object")
	}
	if err := compare(candidates, reviewed); err != nil {
		return err
	}
	_, err = fmt.Fprintf(out, "lifecycle inventory: %d inventoried symbols match; syntax coverage only (not runtime fence enforcement)\n", len(candidates))
	return err
}

func scan(root string) ([]candidate, error) {
	fset := token.NewFileSet()
	var functions []*function
	type sourceFile struct {
		path string
		ast  *ast.File
	}
	var sources []sourceFile
	packageFiles := map[string][]*ast.File{}
	for _, dir := range []string{"cmd", "internal"} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return walkErr
			}
			if d.IsDir() && path == filepath.Join(root, "cmd", "lint-lifecycle") {
				return filepath.SkipDir // The scanner's vocabulary is not runtime code.
			}
			if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
				return nil
			}
			src, err := readRepositoryFile(root, path)
			if err != nil {
				return err
			}
			file, err := parser.ParseFile(fset, path, src, 0)
			if err != nil {
				return err
			}
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			rel = filepath.ToSlash(rel)
			sources = append(sources, sourceFile{path: rel, ast: file})
			packageFiles[filepath.Dir(rel)] = append(packageFiles[filepath.Dir(rel)], file)
			return nil
		})
		if err != nil {
			return nil, fmt.Errorf("scan %s: %w", dir, err)
		}
	}
	// Constants and declarations share these parsed ASTs. No second read/parse
	// of each package is needed to resolve cross-file SQL string dependencies.
	packageValues := map[string]map[string]string{}
	for pkg, files := range packageFiles {
		packageValues[pkg] = packageStringConstants(files)
	}
	for _, source := range sources {
		pkg := filepath.Dir(source.path)
		for _, decl := range source.ast.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			function := &function{ast: fn, file: source.path, pkg: pkg, symbol: symbol(fn), line: fset.Position(fn.Pos()).Line}
			collectFacts(function, packageValues[pkg])
			functions = append(functions, function)
		}
	}
	// Historical migration writers are separate entries, not runtime-policy
	// exceptions. Include both directions; inventory does not execute SQL.
	migrations, err := filepath.Glob(filepath.Join(root, "migrations", "*.sql"))
	if err != nil {
		return nil, err
	}
	for _, path := range migrations {
		src, err := readRepositoryFile(root, path)
		if err != nil {
			return nil, err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return nil, err
		}
		fn := &function{ast: &ast.FuncDecl{Name: ast.NewIdent("SQL"), Body: &ast.BlockStmt{List: []ast.Stmt{&ast.ExprStmt{X: &ast.BasicLit{Kind: token.STRING, Value: strconv.Quote(string(src))}}}}}, file: filepath.ToSlash(rel), pkg: "migrations", symbol: "SQL", line: 1}
		collectFacts(fn, nil)
		if len(fn.sql) > 0 {
			fn.kinds = append(fn.kinds, "migration-sql")
			functions = append(functions, fn)
		}
	}

	// Propagate through same-package named functions and same-receiver methods.
	// Cross-package calls are conservative method-name candidates, not resolved
	// interface dispatch. Creation's generic Create name is deliberately excluded.
	selected := map[*function]bool{}
	byName := map[string][]*function{}
	for _, fn := range functions {
		byName[fn.ast.Name.Name] = append(byName[fn.ast.Name.Name], fn)
		if slices.Contains(boundaryNames, fn.ast.Name.Name) || snapshotPublicationRoot(fn.file+":"+fn.symbol) {
			fn.kinds = append(fn.kinds, "boundary-definition")
		}
		if slices.Contains(dedupeKeyNames, fn.ast.Name.Name) {
			fn.kinds = append(fn.kinds, "dispatch-key-definition")
		}
		if len(fn.kinds) > 0 {
			selected[fn] = true
		}
	}
	for changed := true; changed; {
		changed = false
		for _, fn := range functions {
			for _, c := range fn.calls {
				for _, target := range byName[c.name] {
					if !selected[target] || target == fn || target.ast.Name.Name != c.name || c.name == "Create" || c.name == "CreateInTx" || c.name == "CreateWithProvenance" {
						continue
					}
					local := target.pkg == fn.pkg && ((c.local && target.ast.Recv == nil) || (c.receiver == receiverName(fn.ast) && fn.ast.Recv != nil && target.ast.Recv != nil && receiverType(fn.ast) == receiverType(target.ast)))
					cross := !c.local && c.name != "UpdateStatus" && c.name != "UpdateResult" && c.name != "UpdateEditable" && c.name != "Archive" && c.name != "SendMessage" && c.name != "Run" && c.name != "Start" && c.name != "Cancel" && c.name != "Complete" && c.name != "Begin" && lifecycleReceiver.MatchString(c.expr)
					if !local && !cross {
						continue
					}
					if !slices.Contains(fn.kinds, "indirect-caller") {
						fn.kinds = append(fn.kinds, "indirect-caller")
					}
					fn.links = append(fn.links, c.expr+" -> "+target.file+":"+target.symbol, c.boundary+" -> "+target.file+":"+target.symbol)
					if !selected[fn] {
						selected[fn], changed = true, true
					}
				}
			}
		}
	}
	var result []candidate
	for _, fn := range functions {
		if !selected[fn] {
			continue
		}
		calls := append([]string(nil), fn.links...)
		for _, c := range fn.calls {
			if isBoundary(c) {
				calls = append(calls, c.expr)
			}
		}
		kinds, calls, queries := sortedUnique(fn.kinds), sortedUnique(calls), sortedUnique(fn.sql)
		ordered := append([]string(nil), fn.trace...)
		for _, call := range fn.calls {
			if slices.ContainsFunc(fn.links, func(edge string) bool { return strings.HasPrefix(edge, call.expr+" -> ") }) {
				ordered = append(ordered, "indirect-boundary:"+call.boundary)
			}
		}
		// Hash the discovered contract facts, not the enclosing function. An
		// unrelated log/comment/formatting change must not require hundreds of
		// inventory edits, while SQL/guard/lock/boundary drift must remain visible.
		fingerprint := []string{strings.Join(kinds, "\n"), strings.Join(calls, "\n"), strings.Join(queries, "\n"), strings.Join(sortedUnique(fn.guards), "\n"), strings.Join(sortedUnique(fn.locks), "\n"), strings.Join(sortedUnique(fn.predicates), "\n"), strings.Join(sortedUnique(fn.dependencies), "\n"), strings.Join(ordered, "\n")}
		digest := sha256.Sum256([]byte(strings.Join(fingerprint, "\n---\n")))
		result = append(result, candidate{ID: fn.file + ":" + fn.symbol, Line: fn.line, SourceSHA256: hex.EncodeToString(digest[:]), Kinds: kinds, Calls: calls, SQL: queries, Guards: sortedUnique(fn.guards), Locks: sortedUnique(fn.locks), Results: sortedUnique(fn.results), Predicates: sortedUnique(fn.predicates)})
	}
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result, nil
}

func collectFacts(fn *function, constants map[string]string) {
	publicationRoot := snapshotPublicationRoot(fn.file + ":" + fn.symbol)
	if fn.ast.Type != nil && fn.ast.Type.Params != nil {
		for _, parameter := range fn.ast.Type.Params.List {
			if expression(parameter.Type) == "pgx.Tx" {
				fn.locks = append(fn.locks, "caller-supplied pgx.Tx")
			}
		}
	}
	ast.Inspect(fn.ast.Body, func(node ast.Node) bool {
		switch n := node.(type) {
		case *ast.CallExpr:
			c := call{expr: expression(n.Fun)}
			switch callee := n.Fun.(type) {
			case *ast.Ident:
				c.name, c.local = callee.Name, true
			case *ast.SelectorExpr:
				c.name = callee.Sel.Name
				if id, ok := callee.X.(*ast.Ident); ok {
					c.receiver = id.Name
				}
			}
			if c.name != "" {
				c.boundary = boundaryCall(c.expr, n.Args)
				fn.calls = append(fn.calls, c)
				if slices.Contains(enqueueNames, c.name) {
					// Enqueue syntax is relevant in an otherwise selected lifecycle
					// function, but does not classify every unrelated queue producer
					// as lifecycle code. Preserve receiver/tx/job/payload/dedupe args
					// and source order instead of only noticing a job-type literal.
					fn.links = append(fn.links, c.expr, c.boundary)
					fn.trace = append(fn.trace, "enqueue:"+c.boundary)
				}
				if c.name == "Begin" || c.name == "Commit" || c.name == "WithTx" || c.name == "InTransaction" || c.name == "WithLockedPR" || strings.Contains(c.name, "Advisory") {
					fn.locks = append(fn.locks, c.expr)
					fn.trace = append(fn.trace, "transaction:"+c.boundary)
				}
				if c.name == "RowsAffected" || c.name == "CollectOneRow" || c.name == "Scan" {
					fn.results = append(fn.results, c.expr)
				}
				// This exact helper uses a generically named store parameter;
				// track its delete/clear order without classifying every Delete.
				cleanupDelete := fn.file == "internal/services/storage/snapshot.go" && fn.symbol == "CleanupSessionSnapshot" && c.name == "Delete"
				publicationCall := publicationRoot && slices.Contains(snapshotPublicationCalls, c.name)
				if isBoundary(c) || genericLifecycleCall(c, n.Args) || cleanupDelete || publicationCall {
					fn.kinds = append(fn.kinds, "boundary-caller")
					fn.links = append(fn.links, c.expr, c.boundary)
					fn.trace = append(fn.trace, "boundary:"+c.boundary)
				}
			}
		case *ast.BasicLit, *ast.BinaryExpr, *ast.Ident:
			expr := node.(ast.Expr)
			value := constantString(expr, constants)
			// Ordinary identifiers and non-string expressions resolve to this
			// marker and cannot contain SQL or a dispatch type. Keep walking
			// their children; partially resolved SQL still needs inspection.
			if value == "" || value == "<dynamic>" {
				break
			}
			normalized := whitespaceRE.ReplaceAllString(strings.TrimSpace(value), " ")
			if relevantSQL(value) {
				fn.trace = append(fn.trace, "SQL:"+normalized)
			}
			if index := strings.Index(strings.ToUpper(normalized), " WHERE "); index >= 0 && relevantSQL(value) && (strings.Contains(strings.ToUpper(normalized[:index]), "SELECT ") || strings.Contains(strings.ToUpper(normalized[:index]), "UPDATE ") || strings.Contains(strings.ToUpper(normalized[:index]), "INSERT ")) {
				fn.predicates = append(fn.predicates, normalized[index+1:])
			}
			if id, ok := expr.(*ast.Ident); ok {
				if resolved, exists := constants[id.Name]; exists && relevantSQL(value) {
					fn.dependencies = append(fn.dependencies, whitespaceRE.ReplaceAllString(strings.TrimSpace(resolved), " "))
				}
			}
			for _, lock := range sqlLockRE.FindAllString(value, -1) {
				fn.locks = append(fn.locks, whitespaceRE.ReplaceAllString(lock, " "))
				fn.trace = append(fn.trace, "SQL-lock:"+whitespaceRE.ReplaceAllString(lock, " "))
			}
			if value == "run_agent" || value == "continue_session" || value == "deliver_thread_inbox" || value == "continue_pr_feedback" {
				fn.kinds = append(fn.kinds, "dispatch-reference")
				fn.links = append(fn.links, "job type "+value)
			}
			if gateTableRE.MatchString(value) {
				fn.kinds = append(fn.kinds, "authority-table-write")
				fn.sql = append(fn.sql, normalized)
			}
			// Alias-target dialects and split/dynamic target strings are not
			// resolved as concrete PostgreSQL writes. Keep them as reviewable
			// possible candidates instead of claiming the target is harmless.
			if updateRE.MatchString(value) && (aliasStatusUpdate(value) || strings.Contains(strings.ToUpper(value), "UPDATE <DYNAMIC>") || strings.TrimSpace(value) == "UPDATE") {
				fn.kinds = append(fn.kinds, "possible-status-update")
				fn.sql = append(fn.sql, whitespaceRE.ReplaceAllString(strings.TrimSpace(value), " "))
			}
			matches := mutationRE.FindAllStringSubmatchIndex(value, -1)
			for index, match := range matches {
				end := len(value)
				if index+1 < len(matches) {
					end = matches[index+1][0]
				}
				query := value[match[0]:end]
				verb := strings.ToUpper(value[match[2]:match[3]])
				// A variable-built SET clause cannot be proved harmless. Capture
				// it as possible status SQL instead of silently dropping it.
				if strings.HasPrefix(verb, "INSERT") {
					end := strings.Index(query, ")")
					if end >= 0 && statusColumnRE.MatchString(query[:end]) {
						fn.kinds = append(fn.kinds, "status-insert")
					} else {
						continue
					}
				} else if strings.HasPrefix(verb, "DELETE") {
					fn.kinds = append(fn.kinds, "lifecycle-delete")
				} else if statusAssignmentRE.MatchString(setClause(query)) {
					fn.kinds = append(fn.kinds, "status-update")
				} else if gateAssignmentRE.MatchString(setClause(query)) {
					fn.kinds = append(fn.kinds, "lifecycle-gate-update")
				} else if strings.Contains(query, "<dynamic>") || strings.Contains(setClause(query), "%s") || strings.HasSuffix(strings.TrimSpace(strings.ToUpper(query)), "SET") || setClause(query) == "" {
					fn.kinds = append(fn.kinds, "possible-status-update")
				} else {
					continue
				}
				fn.sql = append(fn.sql, whitespaceRE.ReplaceAllString(strings.TrimSpace(query), " "))
			}
		case *ast.IfStmt:
			condition := expression(n.Cond)
			if guardRE.MatchString(condition) || (publicationRoot && controlsSnapshotPublication(n)) {
				fn.guards = append(fn.guards, condition)
				fn.trace = append(fn.trace, "guard:"+condition)
			}
		case *ast.SwitchStmt:
			tag := "<conditionless>"
			if n.Tag != nil {
				tag = expression(n.Tag)
			}
			parts := []string{"switch " + tag}
			for _, statement := range n.Body.List {
				clause := statement.(*ast.CaseClause)
				if len(clause.List) == 0 {
					parts = append(parts, "default")
				} else {
					parts = append(parts, "case"+boundaryCall("", clause.List))
				}
			}
			fact := strings.Join(parts, "; ")
			if guardRE.MatchString(fact) {
				fn.guards = append(fn.guards, fact)
				fn.trace = append(fn.trace, "guard:"+fact)
			}
		case *ast.AssignStmt:
			for index, left := range n.Lhs {
				if authorityAssignmentRE.MatchString(expression(left)) && index < len(n.Rhs) {
					fn.guards = append(fn.guards, expression(left)+" "+n.Tok.String()+" "+boundaryCall("", []ast.Expr{n.Rhs[index]}))
					fn.trace = append(fn.trace, "authority-assignment:"+expression(left)+" "+n.Tok.String()+" "+boundaryCall("", []ast.Expr{n.Rhs[index]}))
				}
			}
		case *ast.ReturnStmt:
			if slices.Contains(dedupeKeyNames, fn.ast.Name.Name) {
				fn.trace = append(fn.trace, "dispatch-key-return:"+boundaryCall("", n.Results))
			}
		}
		return true
	})
	fn.trace = append(fn.trace, dispatchArgumentDependencies(fn.ast.Body, slices.Contains(dedupeKeyNames, fn.ast.Name.Name), publicationRoot, constants)...)
}

// Only the exact producer/uploader roots use this narrow control dependency.
// Record conditions enclosing pending/Save/promote/clear effects, not arbitrary
// callback contents or unrelated logging/error branches in the same function.
func controlsSnapshotPublication(statement *ast.IfStmt) bool {
	found := false
	ast.Inspect(statement, func(node ast.Node) bool {
		if found {
			return false
		}
		if call, ok := node.(*ast.CallExpr); ok {
			if selector, ok := call.Fun.(*ast.SelectorExpr); ok {
				found = slices.Contains(snapshotPublicationCalls, selector.Sel.Name)
			}
		}
		return true
	})
	return found
}

// Resolve only local syntax feeding enqueue arguments, exact publication
// pipeline arguments, or exact key-helper returns. This is conservative
// identifier dependency tracking, not flow/scope/type analysis. It catches a
// changed dedupeKey derivation or opts.DedupeKey even when the call still passes
// the same variable. Unrelated local logging/assignments are not fingerprinted.
func dispatchArgumentDependencies(body *ast.BlockStmt, keyHelper, publicationRoot bool, constants map[string]string) []string {
	type binding struct {
		name, left, operator string
		target               ast.Expr
		value                ast.Expr
	}
	var bindings []binding
	referenced := map[string]bool{}
	identifiers := func(node ast.Node) {
		ast.Inspect(node, func(node ast.Node) bool {
			if id, ok := node.(*ast.Ident); ok {
				referenced[id.Name] = true
			}
			return true
		})
	}
	add := func(left ast.Expr, operator string, value ast.Expr) {
		if root := rootIdentifier(left); root != "" {
			bindings = append(bindings, binding{name: root, left: expression(left), operator: operator, target: left, value: value})
		}
	}
	ast.Inspect(body, func(node ast.Node) bool {
		switch n := node.(type) {
		case *ast.CallExpr:
			if callee, ok := n.Fun.(*ast.SelectorExpr); ok && (slices.Contains(enqueueNames, callee.Sel.Name) || (publicationRoot && slices.Contains(snapshotPublicationCalls, callee.Sel.Name))) {
				for _, arg := range n.Args {
					identifiers(arg)
				}
			}
		case *ast.AssignStmt:
			for i, left := range n.Lhs {
				if i < len(n.Rhs) {
					add(left, n.Tok.String(), n.Rhs[i])
				}
			}
		case *ast.ValueSpec:
			for i, name := range n.Names {
				if i < len(n.Values) {
					add(name, "=", n.Values[i])
				}
			}
		case *ast.ReturnStmt:
			if keyHelper {
				for _, result := range n.Results {
					identifiers(result)
				}
			}
		}
		return true
	})
	for changed := true; changed; {
		count := len(referenced)
		for _, item := range bindings {
			if referenced[item.name] {
				identifiers(item.target)
				identifiers(item.value)
			}
		}
		changed = count != len(referenced)
	}
	var facts []string
	for _, item := range bindings {
		if referenced[item.name] {
			facts = append(facts, "enqueue-dependency:"+item.left+" "+item.operator+" "+boundaryCall("", []ast.Expr{item.value}))
		}
	}
	var names []string
	for name := range referenced {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if value, ok := constants[name]; ok && value != "<dynamic>" {
			facts = append(facts, "dispatch-constant:"+name+"="+value)
		}
	}
	return facts
}

// Follow an assignment's syntactic base, without asserting alias identity,
// scope, flow or type correctness. Indexed/indirect payload writes must remain
// dependencies when enqueue still passes the same root variable.
func rootIdentifier(expr ast.Expr) string {
	switch n := expr.(type) {
	case *ast.Ident:
		return n.Name
	case *ast.SelectorExpr:
		return rootIdentifier(n.X)
	case *ast.IndexExpr:
		return rootIdentifier(n.X)
	case *ast.IndexListExpr:
		return rootIdentifier(n.X)
	case *ast.StarExpr:
		return rootIdentifier(n.X)
	case *ast.ParenExpr:
		return rootIdentifier(n.X)
	default:
		return ""
	}
}

func aliasStatusUpdate(query string) bool {
	if !statusAssignmentRE.MatchString(setClause(query)) || mutationRE.MatchString(query) {
		return false
	}
	for _, update := range aliasUpdateRE.FindAllStringSubmatch(query, -1) {
		for _, from := range lifecycleFromRE.FindAllStringSubmatch(query, -1) {
			if strings.EqualFold(update[1], from[2]) {
				return true
			}
		}
	}
	return false
}

func isBoundary(c call) bool {
	return slices.Contains(boundaryNames, c.name) || slices.Contains(dedupeKeyNames, c.name) || snapshotDelete(c.expr)
}

func snapshotDelete(call string) bool {
	return strings.HasSuffix(call, ".Delete") && strings.Contains(strings.ToLower(call), "snapshot")
}

func relevantSQL(value string) bool {
	return mutationRE.MatchString(value) || gateTableRE.MatchString(value) || lifecycleReceiver.MatchString(value) && (strings.Contains(strings.ToUpper(value), "SELECT ") || strings.Contains(strings.ToUpper(value), "UPDATE "))
}

// Callback bodies are inspected independently. Keep their signature here, not
// unrelated lambda implementation/logging boilerplate in a boundary fingerprint.
func boundaryCall(callee string, args []ast.Expr) string {
	values := make([]string, 0, len(args))
	for _, argument := range args {
		value := expression(argument)
		if strings.Contains(value, "func(") || strings.Contains(value, "func (") {
			// Parse a private copy before stripping nested callback bodies. The
			// original AST remains intact for lifecycle-fact inspection.
			if copy, err := parser.ParseExpr(value); err == nil {
				ast.Inspect(copy, func(node ast.Node) bool {
					if fn, ok := node.(*ast.FuncLit); ok {
						fn.Body = &ast.BlockStmt{}
					}
					return true
				})
				value = expression(copy)
			}
		}
		values = append(values, value)
	}
	return callee + "(" + strings.Join(values, ", ") + ")"
}

func genericLifecycleCall(c call, args []ast.Expr) bool {
	if (c.name == "Answer" || c.name == "Cancel") && strings.Contains(strings.ToLower(c.expr), "humaninput") {
		return true
	}
	if (c.name == "Create" || c.name == "CreateInTx" || c.name == "CreateWithProvenance") && lifecycleCreationReceiver.MatchString(c.expr) {
		return true
	}
	if c.name != "UpdateStatus" && c.name != "UpdateResult" && c.name != "UpdateEditable" && c.name != "Archive" {
		return false
	}
	if lifecycleReceiver.MatchString(c.expr) {
		return true
	}
	for _, arg := range args {
		value := expression(arg)
		if strings.Contains(value, "models.SessionStatus") || strings.Contains(value, "models.ThreadStatus") || strings.Contains(value, "models.Session{") || strings.Contains(value, "models.SessionThread{") {
			return true
		}
	}
	return false
}

func setClause(query string) string {
	query = whitespaceRE.ReplaceAllString(query, " ")
	upper := strings.ToUpper(query)
	start := strings.Index(upper, " SET ")
	if start < 0 {
		return ""
	}
	end := len(query)
	for _, delimiter := range []string{" WHERE ", " RETURNING "} {
		if index := strings.Index(upper[start:], delimiter); index >= 0 && start+index < end {
			end = start + index
		}
	}
	return query[start:end]
}

func packageStringConstants(parsed []*ast.File) map[string]string {
	valueCount := 0
	for _, file := range parsed {
		for _, declaration := range file.Decls {
			if group, ok := declaration.(*ast.GenDecl); ok {
				for _, item := range group.Specs {
					if value, ok := item.(*ast.ValueSpec); ok {
						valueCount += len(value.Values)
					}
				}
			}
		}
	}
	values := map[string]string{}
	// Resolve forward/file-to-file constant references to a fixed point. Unknown
	// or cyclic expressions remain dynamic, never evidence of a safe query.
	for iteration := 0; iteration <= valueCount; iteration++ {
		changed := false
		for _, file := range parsed {
			for _, decl := range file.Decls {
				declaration, ok := decl.(*ast.GenDecl)
				if !ok {
					continue
				}
				for _, item := range declaration.Specs {
					if spec, ok := item.(*ast.ValueSpec); ok {
						for index, expr := range spec.Values {
							if index < len(spec.Names) {
								name, value := spec.Names[index].Name, constantString(expr, values)
								if old, exists := values[name]; !exists || old != value {
									values[name], changed = value, true
								}
							}
						}
					}
				}
			}
		}
		if !changed {
			break
		}
	}
	return values
}

func constantString(expr ast.Expr, constants map[string]string) string {
	switch n := expr.(type) {
	case *ast.BasicLit:
		if n.Kind == token.STRING {
			value, err := strconv.Unquote(n.Value)
			if err == nil {
				return value
			}
		}
	case *ast.Ident:
		if value, ok := constants[n.Name]; ok {
			return value
		}
	case *ast.BinaryExpr:
		if n.Op == token.ADD {
			return constantString(n.X, constants) + constantString(n.Y, constants)
		}
	case *ast.ParenExpr:
		return constantString(n.X, constants)
	}
	return "<dynamic>"
}

func expression(expr ast.Expr) string {
	var out bytes.Buffer
	if err := format.Node(&out, token.NewFileSet(), expr); err != nil {
		return "<unformatted>"
	}
	return out.String()
}

func symbol(fn *ast.FuncDecl) string {
	if fn.Recv == nil {
		return fn.Name.Name
	}
	return strings.TrimPrefix(expression(fn.Recv.List[0].Type), "*") + "." + fn.Name.Name
}

func receiverName(fn *ast.FuncDecl) string {
	if fn.Recv != nil && len(fn.Recv.List[0].Names) > 0 {
		return fn.Recv.List[0].Names[0].Name
	}
	return ""
}

func receiverType(fn *ast.FuncDecl) string {
	return strings.TrimPrefix(expression(fn.Recv.List[0].Type), "*")
}

func sortedUnique(values []string) []string {
	values = slices.Clone(values)
	sort.Strings(values)
	return slices.Compact(values)
}

func compare(candidates []candidate, reviewed inventory) error {
	var failures []string
	if reviewed.Version != 1 || len(reviewed.Scope) == 0 || len(reviewed.Limitations) == 0 {
		failures = append(failures, "inventory must declare version 1, scope, and limitations")
	}
	entries := map[string]entry{}
	classes := []string{"lifecycle-owned", "fenced-specialist", "unfenced-needs-conversion", "non-lifecycle-candidate"}
	for _, item := range reviewed.Entries {
		if _, exists := entries[item.ID]; exists {
			failures = append(failures, "duplicate inventory entry: "+item.ID)
		}
		entries[item.ID] = item
		if item.ID == "" || item.SourceSHA256 == "" || item.Operation == "" || item.Owner == "" || item.Predicate == "" || item.Lock == "" || item.StaleResult == "" || item.Migration == "" || len(item.Evidence) == 0 || !slices.Contains(classes, item.Classification) {
			failures = append(failures, "incomplete contract: "+item.ID)
		}
	}
	for _, found := range candidates {
		item, ok := entries[found.ID]
		if !ok {
			failures = append(failures, "uncovered lifecycle candidate: "+found.ID)
			continue
		}
		delete(entries, found.ID)
		if item.SourceSHA256 != found.SourceSHA256 {
			failures = append(failures, "changed source requires contract review: "+found.ID)
		}
		if requiresMutationContract(found) && item.Classification != "non-lifecycle-candidate" {
			if item.CanonicalTarget == "" || item.Authority == "" || item.FenceOutcome == "" || item.Prerequisite0a == "" || strings.Contains(strings.ToLower(item.Migration), "0a: audit") {
				failures = append(failures, "mutation requires concrete canonical authority/outcome/0a contract: "+found.ID)
			}
		}
	}
	for id := range entries {
		failures = append(failures, "stale inventory entry: "+id)
	}
	if len(failures) != 0 {
		sort.Strings(failures)
		return fmt.Errorf("lifecycle inventory mismatch:\n%s\nUse -discover to inspect candidates; review contracts before updating inventory.json", strings.Join(failures, "\n"))
	}
	return nil
}

func requiresMutationContract(found candidate) bool {
	if slices.Contains(found.Kinds, "migration-sql") {
		return false
	}
	if snapshotPublicationRoot(found.ID) {
		return true
	}
	for _, kind := range found.Kinds {
		if kind == "status-update" || kind == "status-insert" || kind == "lifecycle-gate-update" || kind == "authority-table-write" {
			return true
		}
	}
	for _, root := range contractBoundaryNames {
		if strings.HasSuffix(found.ID, "."+root) || strings.HasSuffix(found.ID, ":"+root) {
			return true
		}
	}
	return slices.ContainsFunc(found.Calls, func(call string) bool {
		if snapshotDelete(call) {
			return true
		}
		if strings.HasSuffix(call, ".UpdateStatus") || strings.HasSuffix(call, ".UpdateResult") {
			return true
		}
		return slices.ContainsFunc(contractBoundaryNames, func(root string) bool {
			return call == root || strings.HasSuffix(call, "."+root)
		})
	})
}
