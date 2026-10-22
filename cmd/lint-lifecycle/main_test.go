package main

import (
	"bytes"
	"encoding/json"
	"go/parser"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func fixture(t *testing.T, source string) string {
	t.Helper()
	root := t.TempDir()
	for _, dir := range []string{"cmd", "internal"} {
		require.NoError(t, os.Mkdir(filepath.Join(root, dir), 0o700), "fixture source directory should be created")
	}
	require.NoError(t, os.WriteFile(filepath.Join(root, "internal", "fixture.go"), []byte("package fixture\n"+source), 0o600), "fixture Go source should be written")
	return root
}

func publicationFixture(t *testing.T, source string) string {
	t.Helper()
	root := fixture(t, "func unrelated() {}")
	dir := filepath.Join(root, "internal", "services", "github")
	require.NoError(t, os.MkdirAll(dir, 0o700), "publication fixture should use the exact inventoried source path")
	require.NoError(t, os.WriteFile(filepath.Join(dir, "pr.go"), []byte("package github\n"+source), 0o600), "publication fixture source should be written")
	return root
}

func TestScanSQLShapes(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		source string
		kinds  []string
	}{
		{name: "unresolved identifiers", source: "func write() { consume(ctx, unknown, missing) }"},
		{name: "empty literal", source: `func write() { consume("") }`},
		{name: "unresolved parent retains nested SQL", source: "func write() { consume(unknown * len(`UPDATE sessions SET status='idle' WHERE org_id=$1`)) }", kinds: []string{"status-update"}},
		{name: "unresolved parent retains nested boundary", source: "func write() { consume(unknown * sessions.UpdateStatus(ctx, orgID, sessionID, state)) }", kinds: []string{"boundary-caller"}},
		{name: "raw update", source: "func write() { exec(`UPDATE sessions SET status='idle' WHERE org_id=$1`) }", kinds: []string{"status-update"}},
		{name: "aliased CTE", source: "func write() { exec(`WITH selected AS (SELECT id FROM sessions) UPDATE session_threads AS t SET status='failed' FROM selected s WHERE t.id=s.id`) }", kinds: []string{"status-update"}},
		{name: "schema and quoted identifiers", source: "func write() { exec(`UPDATE public.\"session_threads\" AS t SET \"status\"='failed' WHERE t.org_id=$1`) }", kinds: []string{"status-update"}},
		{name: "split literals and constant table", source: "const table = \"sessions\"\nfunc write() { exec(\"UP\" + \"DATE \" + table + \" SET status='failed'\") }", kinds: []string{"possible-status-update", "status-update"}},
		{name: "named SQL constant", source: "const query = `UPDATE session_threads SET status='idle' WHERE org_id=$1`\nfunc write() { exec(query) }", kinds: []string{"status-update"}},
		{name: "dynamic set is conservative", source: "func write() { exec(`UPDATE sessions SET ` + fields + ` WHERE org_id=$1`) }", kinds: []string{"possible-status-update"}},
		{name: "format set is conservative", source: "func write() { exec(format(`UPDATE sessions SET %s WHERE org_id=$1`, fields)) }", kinds: []string{"possible-status-update"}},
		{name: "alias target is conservative", source: "func write() { exec(`UPDATE t SET status='failed' FROM sessions t WHERE t.org_id=$1`) }", kinds: []string{"possible-status-update"}},
		{name: "statement built in stages is conservative", source: "func write() { query := `UPDATE sessions`; query += ` SET status='failed'`; exec(query) }", kinds: []string{"possible-status-update"}},
		{name: "unresolved target is conservative", source: "func write() { exec(`UPDATE ` + table + ` SET status='failed'`) }", kinds: []string{"possible-status-update"}},
		{name: "status insert", source: "func write() { exec(`INSERT INTO sessions (org_id,status) VALUES ($1,'pending')`) }", kinds: []string{"status-insert"}},
		{name: "delete", source: "func write() { exec(`DELETE FROM session_threads WHERE org_id=$1`) }", kinds: []string{"lifecycle-delete"}},
		{name: "status predicate is not a status mutation", source: "func write() { exec(`UPDATE sessions SET title=$2 WHERE status='idle' AND org_id=$1`) }"},
		{name: "unrelated table", source: "func write() { exec(`UPDATE jobs SET status='failed' WHERE org_id=$1`) }"},
		{name: "read only", source: "func write() { exec(`SELECT status FROM sessions WHERE org_id=$1`) }"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			candidates, err := scan(fixture(t, tt.source))
			require.NoError(t, err, "valid fixture should parse")
			var got []string
			for _, item := range candidates {
				got = append(got, item.Kinds...)
			}
			require.Equal(t, tt.kinds, got, "scanner should identify the exact mutation kind without treating WHERE status as SET status")
		})
	}
}

func TestSortedUniqueDoesNotMutateItsInput(t *testing.T) {
	t.Parallel()
	input := []string{"b", "a", "b", "a"}
	want := []string{"a", "b"}
	require.Equal(t, want, sortedUnique(input), "first normalization should sort and deduplicate facts")
	require.Equal(t, []string{"b", "a", "b", "a"}, input, "normalization must preserve the source facts for later fingerprint and output passes")
	require.Equal(t, want, sortedUnique(input), "repeated normalization must be idempotent without cleared duplicate tails")
}

func TestAssignmentRootIdentifier(t *testing.T) {
	t.Parallel()
	tests := []struct{ expression, root string }{
		{expression: "payload", root: "payload"},
		{expression: "payload.Body", root: "payload"},
		{expression: `payload["request"]`, root: "payload"},
		{expression: "payload[A, B]", root: "payload"},
		{expression: `(*payload).Inputs[0].Body["request"]`, root: "payload"},
		{expression: "((payload))", root: "payload"},
		{expression: "factory()", root: ""},
	}
	for _, tt := range tests {
		t.Run(tt.expression, func(t *testing.T) {
			t.Parallel()
			expr, err := parser.ParseExpr(tt.expression)
			require.NoError(t, err, "syntactic assignment-base fixture should parse without claiming type correctness")
			require.Equal(t, tt.root, rootIdentifier(expr), "assignment base should recursively retain the exact root identifier or conservatively remain unknown")
		})
	}
}

func TestScanIndirectWriters(t *testing.T) {
	t.Parallel()
	root := fixture(t, `
type Writer struct{}
type Unrelated struct{}
func (w *Writer) mutate() { exec("UPDATE sessions SET status='failed' WHERE org_id=$1") }
func (w *Writer) indirect() { w.mutate() }
func (w *Unrelated) unrelated() { w.mutate() }
func helper() { sessions.UpdateStatus(ctx, orgID, sessionID, state) }
func adapter() { helper() }
func initial() { jobs.Enqueue(ctx, orgID, "agent", "run_agent", payload) }
`)
	candidates, err := scan(root)
	require.NoError(t, err, "indirect writer fixture should parse")
	var ids []string
	for _, item := range candidates {
		ids = append(ids, item.ID)
	}
	require.Equal(t, []string{
		"internal/fixture.go:Writer.indirect",
		"internal/fixture.go:Writer.mutate",
		"internal/fixture.go:adapter",
		"internal/fixture.go:helper",
		"internal/fixture.go:initial",
	}, ids, "same-receiver and function wrappers should be inventoried without conflating unrelated receiver types")
}

func TestComposableApprovedGrantRemainsInventoried(t *testing.T) {
	t.Parallel()
	root := fixture(t, `
type GrantStore struct{}
func (s *GrantStore) AppendApprovedSessionGrantInTx() { exec("UPDATE session_execution_metadata SET capability_snapshot=$2 WHERE org_id=$1") }
func (s *GrantStore) legacy() { s.AppendApprovedSessionGrantInTx() }
func (s *GrantStore) adapter() { s.legacy() }
`)
	found, err := scan(root)
	require.NoError(t, err, "composable grant persistence and its legacy wrappers should scan")
	type observation struct {
		ID               string
		RequiresContract bool
	}
	var actual []observation
	for _, item := range found {
		actual = append(actual, observation{ID: item.ID, RequiresContract: requiresMutationContract(item)})
	}
	require.Equal(t, []observation{
		{ID: "internal/fixture.go:GrantStore.AppendApprovedSessionGrantInTx", RequiresContract: true},
		{ID: "internal/fixture.go:GrantStore.adapter"},
		{ID: "internal/fixture.go:GrantStore.legacy", RequiresContract: true},
	}, actual, "extraction must retain the exact persistence root and wrappers without upgrading indirect syntax to authority proof")
	reviewed := reviewedFixture(found)
	reviewed.Entries = reviewed.Entries[1:]
	require.ErrorContains(t, compare(found, reviewed), "uncovered lifecycle candidate: internal/fixture.go:GrantStore.AppendApprovedSessionGrantInTx", "a newly extracted persistence root must require its own reviewed inventory contract")
}

func TestComposablePersistenceBoundariesRemainInventoried(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name             string
		genericCollision bool
		lifecycleLink    bool
	}{
		{name: "UpdateResultAndCompleteActivityPhaseInTx"},
		{name: "UpdateStatusAndCompleteActivityPhaseInTx"},
		{name: "UpdateTurnCompleteAndCompleteActivityPhaseInTx"},
		{name: "UpdateStatusInTx", genericCollision: true},
		{name: "CompleteTurnInTx"},
		{name: "UpdateResultInTx", genericCollision: true},
		{name: "UpdateTurnCompleteInTx"},
		{name: "StartSeedPhaseInTx"},
		{name: "ReconcileDrainedTerminalTurnInTx"},
		{name: "AppendApprovedCapabilityGrant"},
		{name: "acknowledgeInboxBatchInTx"},
		{name: "AcknowledgeInboxBatch"},
		{name: "AcknowledgeInboxBatchWithTransition"},
		{name: "AcknowledgeInboxBatchAndStartPhase"},
		{name: "SettleAssessmentInTx"},
		{name: "withLockedPRInTx"},
		{name: "settleAssessmentLockedPR"},
		{name: "EndAttempt"},
		{name: "newTurnAttemptSessions", lifecycleLink: true},
		{name: "PublishCheckpointWithProvenanceInTx"},
		{name: "publishCheckpointWithProvenanceRow"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			// These deliberately lexical fixtures do not resolve imported types
			// or prove that the supplied tx is valid, owned or authorized.
			root := fixture(t, "type OpaqueStore struct{}\nfunc (s *OpaqueStore) "+tt.name+"(ctx context.Context, orgID uuid.UUID, tx pgx.Tx) {}")
			dir := filepath.Join(root, "internal", "consumer")
			require.NoError(t, os.Mkdir(dir, 0o700), "create a different package for the generic-receiver caller fixture")
			caller := "package consumer\ntype Pipeline struct{}\nfunc (p *Pipeline) write(ctx context.Context, orgID uuid.UUID, tx pgx.Tx) { p.store." + tt.name + "(ctx, orgID, tx) }"
			require.NoError(t, os.WriteFile(filepath.Join(dir, "adapter.go"), []byte(caller), 0o600), "write the cross-package caller without lifecycle receiver-name evidence")
			found, err := scan(root)
			require.NoError(t, err, "scan the named persistence root and generic cross-package caller without type-checking claims")
			type observation struct {
				ID               string
				Kinds, Calls     []string
				RequiresContract bool
			}
			var actual []observation
			for _, item := range found {
				actual = append(actual, observation{ID: item.ID, Kinds: item.Kinds, Calls: item.Calls, RequiresContract: requiresMutationContract(item)})
			}
			rootID := "internal/fixture.go:OpaqueStore." + tt.name
			expectedCaller := observation{ID: "internal/consumer/adapter.go:Pipeline.write", Kinds: []string{"boundary-caller"}, Calls: []string{"p.store." + tt.name, "p.store." + tt.name + "(ctx, orgID, tx)"}, RequiresContract: true}
			if tt.lifecycleLink {
				// This constructor name itself contains "Sessions", so the
				// conservative name-based cross-package edge is also recorded.
				expectedCaller.Kinds = []string{"boundary-caller", "indirect-caller"}
				expectedCaller.Calls = []string{"p.store." + tt.name, "p.store." + tt.name + " -> " + rootID, "p.store." + tt.name + "(ctx, orgID, tx)", "p.store." + tt.name + "(ctx, orgID, tx) -> " + rootID}
			}
			require.Equal(t, []observation{
				expectedCaller,
				{ID: rootID, Kinds: []string{"boundary-definition"}, RequiresContract: true},
			}, actual, "every explicit transaction seam and its direct caller must require review even without SQL or a lifecycle-named receiver")
			reviewed := reviewedFixture(found)
			require.NoError(t, compare(found, reviewed), "explicit reviewed contracts should cover exactly the discovered root and direct caller")
			rootMissing := reviewedFixture(found[:1])
			require.ErrorContains(t, compare(found, rootMissing), "uncovered lifecycle candidate: "+rootID, "extraction must not silently remove the named persistence root from inventory coverage")
			for index := range reviewed.Entries {
				reviewed.Entries[index].CanonicalTarget = ""
			}
			contractErr := compare(found, reviewed)
			for _, item := range found {
				require.ErrorContains(t, contractErr, "mutation requires concrete canonical authority/outcome/0a contract: "+item.ID, "both the root and generic direct caller need a concrete reviewed operation contract")
			}
			if tt.genericCollision {
				for index := range reviewed.Entries {
					reviewed.Entries[index].Classification = "non-lifecycle-candidate"
				}
				require.NoError(t, compare(found, reviewed), "a generic InTx name collision may be excluded only through an explicit reviewed non-lifecycle classification")
			}
		})
	}
}

func TestPrivateScopeLockPrerequisitesRemainInventoried(t *testing.T) {
	t.Parallel()
	tests := []struct{ name string }{
		{name: "lockLifecycleScopeInTx"},
		{name: "requireLifecycleScopeTransactionInTx"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			root := fixture(t, "func "+tt.name+"(ctx context.Context, tx pgx.Tx, orgID uuid.UUID) {}\nfunc adapter(ctx context.Context, tx pgx.Tx, orgID uuid.UUID) { "+tt.name+"(ctx, tx, orgID) }")
			found, err := scan(root)
			require.NoError(t, err, "scan the private prerequisite and its same-package caller without introducing a public authority API")
			type observation struct {
				ID               string
				RequiresContract bool
			}
			actual := make([]observation, 0, len(found))
			for _, item := range found {
				actual = append(actual, observation{ID: item.ID, RequiresContract: requiresMutationContract(item)})
			}
			want := []observation{{ID: "internal/fixture.go:adapter"}, {ID: "internal/fixture.go:" + tt.name}}
			require.Equal(t, want, actual, "private locking observations must be inventoried without being classified as mutation authority")
			reviewed := reviewedFixture(found)
			for index := range reviewed.Entries {
				reviewed.Entries[index].CanonicalTarget = ""
			}
			require.NoError(t, compare(found, reviewed), "an observational prerequisite does not become an authorization primitive merely by being explicitly inventoried")
			reviewed.Entries = reviewed.Entries[:1]
			require.ErrorContains(t, compare(found, reviewed), "uncovered lifecycle candidate: internal/fixture.go:"+tt.name, "the private locking root must still require an explicit reviewed inventory entry")
		})
	}
}

func TestPlainGenericStoreMethodsRemainUnselected(t *testing.T) {
	t.Parallel()
	tests := []struct{ name string }{
		{name: "UpdateStatus"},
		{name: "UpdateResult"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			root := fixture(t, "type OpaqueStore struct{}\ntype Pipeline struct{}\nfunc (s *OpaqueStore) "+tt.name+"() {}\nfunc (p *Pipeline) write() { p.store."+tt.name+"(ctx, orgID, id, value) }")
			found, err := scan(root)
			require.NoError(t, err, "scan unrelated generic store methods without assigning lifecycle meaning from their names alone")
			require.Equal(t, []candidate(nil), found, "registering explicit InTx seams must not broaden ordinary generic status/result discovery")
		})
	}
}

func TestEmbeddedSessionStoreCreationRemainsInventoried(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, receiver, method string
		selected               bool
	}{
		{name: "embedded create", receiver: "s.SessionStore", method: "Create", selected: true},
		{name: "embedded caller transaction", receiver: "s.SessionStore", method: "CreateInTx", selected: true},
		{name: "embedded provenance", receiver: "s.SessionStore", method: "CreateWithProvenance", selected: true},
		{name: "unrelated generic create", receiver: "s.store", method: "Create"},
		{name: "unrelated generic transaction", receiver: "s.store", method: "CreateInTx"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			root := fixture(t, "type Adapter struct{}\nfunc (s *Adapter) persist() { "+tt.receiver+"."+tt.method+"(ctx, tx, session) }")
			found, err := scan(root)
			require.NoError(t, err, "scan the embedded session-store creation boundary without Go type-resolution claims")
			var ids []string
			for _, item := range found {
				ids = append(ids, item.ID)
			}
			var expected []string
			if tt.selected {
				expected = []string{"internal/fixture.go:Adapter.persist"}
			}
			require.Equal(t, expected, ids, "select explicit session-store receivers without globally assigning lifecycle meaning to generic Create names")
		})
	}
}

func reviewedFixture(candidates []candidate) inventory {
	result := inventory{Version: 1, Scope: []string{"fixture"}, Limitations: []string{"syntax only"}}
	for _, item := range candidates {
		result.Entries = append(result.Entries, entry{ID: item.ID, SourceSHA256: item.SourceSHA256, Operation: "test status projection", Owner: "fixture writer", Predicate: "org_id=$1", Lock: "single UPDATE", StaleResult: "unfenced", Classification: "unfenced-needs-conversion", Migration: "0a conditional projection", Evidence: []string{"fixture source"}, CanonicalTarget: "ProjectTurnInTx", Authority: "RuntimeAuthority exact lease/epochs", FenceOutcome: "reject invalid authority; stale own immutable settlement only", Prerequisite0a: "replace unfenced UPDATE with canonical conditional projection"})
	}
	return result
}

func TestCompareRequiresReviewedCoverage(t *testing.T) {
	t.Parallel()
	candidates, err := scan(fixture(t, "func write() { exec(`UPDATE sessions SET status='idle' WHERE org_id=$1`) }"))
	require.NoError(t, err, "baseline writer fixture should parse")
	tests := []struct {
		name string
		edit func(*inventory)
		want string
	}{
		{name: "reviewed baseline passes", edit: func(*inventory) {}},
		{name: "new writer fails", edit: func(i *inventory) { i.Entries = nil }, want: "uncovered lifecycle candidate"},
		{name: "changed writer fails", edit: func(i *inventory) { i.Entries[0].SourceSHA256 = "old" }, want: "changed source requires contract review"},
		{name: "removed writer fails", edit: func(i *inventory) { i.Entries[0].ID = "internal/fixture.go:deleted" }, want: "stale inventory entry"},
		{name: "duplicate fails", edit: func(i *inventory) { i.Entries = append(i.Entries, i.Entries[0]) }, want: "duplicate inventory entry"},
		{name: "missing predicate fails", edit: func(i *inventory) { i.Entries[0].Predicate = "" }, want: "incomplete contract"},
		{name: "unknown class fails", edit: func(i *inventory) { i.Entries[0].Classification = "approved-package" }, want: "incomplete contract"},
		{name: "vague mutation contract fails", edit: func(i *inventory) { i.Entries[0].CanonicalTarget = "" }, want: "mutation requires concrete canonical authority/outcome/0a contract"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			reviewed := reviewedFixture(candidates)
			tt.edit(&reviewed)
			err := compare(candidates, reviewed)
			if tt.want == "" {
				require.NoError(t, err, "unchanged reviewed fixture should match")
				return
			}
			require.ErrorContains(t, err, tt.want, "coverage or contract drift should fail with an actionable diagnostic")
		})
	}
}

func TestAddedSQLAndIndirectWriterFailClosed(t *testing.T) {
	t.Parallel()
	root := fixture(t, "func existing() { exec(`UPDATE sessions SET status='idle' WHERE org_id=$1`) }")
	baseline, err := scan(root)
	require.NoError(t, err, "baseline inventory should scan")
	added := `package fixture
func injected() { exec("UPDATE sessions AS s SET status='running' WHERE s.org_id=$1") }
func wrapped() { injected() }
`
	require.NoError(t, os.WriteFile(filepath.Join(root, "internal", "added.go"), []byte(added), 0o600), "new raw SQL and wrapper fixture should be created")
	changed, err := scan(root)
	require.NoError(t, err, "new candidates should scan")
	err = compare(changed, reviewedFixture(baseline))
	require.ErrorContains(t, err, "uncovered lifecycle candidate: internal/added.go:injected", "added raw SQL writer must not be silently approved")
	require.ErrorContains(t, err, "uncovered lifecycle candidate: internal/added.go:wrapped", "added indirect writer must not bypass coverage")
}

func TestCanonicalRootCallersRequireConcreteContracts(t *testing.T) {
	t.Parallel()
	tests := []struct{ name, source string }{
		{name: "turn projection", source: `func write() { sessions.UpdateTurnComplete(ctx, org, id, turn, result, provider, snapshot) }`},
		{name: "runtime start", source: `func write() { sessions.BeginRuntime(ctx, org, id, capability, soft, hard, at) }`},
		{name: "thread admission", source: `func write() { threads.ClaimIdleForSession(ctx, org, session, thread, limit) }`},
		{name: "holder authority", source: `func write() { holders.CreateActive(ctx, org, params) }`},
		{name: "workspace materialization", source: `func write() { changesets.BeginMaterialization(ctx, org, session, changeset) }`},
		{name: "named root definition", source: `func BeginRuntime() {}`},
		{name: "running availability", source: `func write() { sessions.MarkRunningWithSandboxState(ctx, org, session, state) }`},
		{name: "start over reset", source: `func write() { sessions.ResetForRetry(ctx, org, session) }`},
		{name: "undo reset", source: `func write() { sessions.UndoResetForRetry(ctx, org, session, reason, category) }`},
		{name: "lost reuse reset", source: `func write() { sessions.ResetAfterLostReuse(ctx, org, session) }`},
		{name: "bulk running failure", source: `func write() { threads.FailRunningBySession(ctx, org, session, result) }`},
		{name: "context availability", source: `func write() { sessions.UpdateSandboxState(ctx, org, session, state) }`},
		{name: "snapshot deletion", source: `func write() { snapshotStore.Delete(ctx, key) }`},
		{name: "archive merge cleanup", source: `func write() { storage.CleanupSessionSnapshot(ctx, snapshots, sessions, org, session, key) }`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			found, err := scan(fixture(t, tt.source))
			require.NoError(t, err, "named authority-root fixture should parse")
			require.Len(t, found, 1, "the exact named root or direct caller should be inventoried")
			reviewed := reviewedFixture(found)
			reviewed.Entries[0].CanonicalTarget = ""
			require.ErrorContains(t, compare(found, reviewed), "mutation requires concrete canonical authority/outcome/0a contract", "named-root callers must not pass on generic syntax-observation metadata")
		})
	}
}

func TestChangedReferencedSQLRequiresReview(t *testing.T) {
	t.Parallel()
	root := fixture(t, "const query = `UPDATE sessions SET status='idle' WHERE org_id=$1 AND status='running'`\nfunc write() { exec(query) }")
	baseline, err := scan(root)
	require.NoError(t, err, "baseline constant-backed writer should scan")
	changedSource := "package fixture\nconst query = `UPDATE sessions SET status='idle' WHERE org_id=$1`\nfunc write() { exec(query) }"
	require.NoError(t, os.WriteFile(filepath.Join(root, "internal", "fixture.go"), []byte(changedSource), 0o600), "fixture should remove a predicate without changing the writer body")
	changed, err := scan(root)
	require.NoError(t, err, "changed constant-backed writer should scan")
	require.ErrorContains(t, compare(changed, reviewedFixture(baseline)), "changed source requires contract review: internal/fixture.go:write", "changing a referenced SQL predicate must invalidate the reviewed caller")
}

func TestPackageSQLConstantsAreResolved(t *testing.T) {
	t.Parallel()
	root := fixture(t, "func write() { exec(query) }")
	constants := "package fixture\nconst query = prefix + tail\nconst tail = ` SET status='idle' WHERE org_id=$1`\nconst prefix = `UPDATE sessions`"
	require.NoError(t, os.WriteFile(filepath.Join(root, "internal", "queries.go"), []byte(constants), 0o600), "cross-file forward-referenced SQL fixture should be created")
	got, err := scan(root)
	require.NoError(t, err, "package SQL constants should resolve")
	require.Equal(t, []string{"status-update"}, got[0].Kinds, "a writer using SQL declared in another file must be discovered")
	require.Equal(t, []string{"UPDATE sessions SET status='idle' WHERE org_id=$1"}, got[0].SQL, "resolved SQL should retain the reviewed predicate")
}

func TestFingerprintTracksContractFactsNotFunctionBody(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		before  string
		after   string
		changed bool
	}{
		{name: "logging and format", before: `func write() { logger.Info("old"); sessions.UpdateStatus(ctx, org, id, models.SessionStatusIdle) }`, after: "func write() {\n// unrelated comment\nlogger.Info(\"new\");\nsessions.UpdateStatus(ctx, org, id, models.SessionStatusIdle)\n}"},
		{name: "boundary status argument", before: `func write() { sessions.UpdateStatus(ctx, org, id, models.SessionStatusIdle) }`, after: `func write() { sessions.UpdateStatus(ctx, org, id, models.SessionStatusFailed) }`, changed: true},
		{name: "SQL predicate", before: "func write() { exec(`UPDATE sessions SET status='idle' WHERE org_id=$1 AND status='running'`) }", after: "func write() { exec(`UPDATE sessions SET status='idle' WHERE org_id=$1`) }", changed: true},
		{name: "guard", before: `func write() { if owner == expected { sessions.UpdateStatus(ctx, org, id, state) } }`, after: `func write() { if owner != expected { sessions.UpdateStatus(ctx, org, id, state) } }`, changed: true},
		{name: "lock", before: "func write() { query(`SELECT id FROM sessions WHERE org_id=$1 FOR UPDATE`); sessions.UpdateStatus(ctx, org, id, state) }", after: "func write() { query(`SELECT id FROM sessions WHERE org_id=$1 FOR SHARE`); sessions.UpdateStatus(ctx, org, id, state) }", changed: true},
		{name: "lock order", before: "func write() { query(`SELECT id FROM sessions WHERE org_id=$1 FOR UPDATE`); query(`SELECT id FROM session_threads WHERE org_id=$1 FOR UPDATE`); sessions.UpdateStatus(ctx, org, id, state) }", after: "func write() { query(`SELECT id FROM session_threads WHERE org_id=$1 FOR UPDATE`); query(`SELECT id FROM sessions WHERE org_id=$1 FOR UPDATE`); sessions.UpdateStatus(ctx, org, id, state) }", changed: true},
		{name: "callback logging", before: `func write() { store.WithTx(ctx, func() { logger.Info("old"); sessions.UpdateStatus(ctx, org, id, state) }) }`, after: `func write() { store.WithTx(ctx, func() { logger.Info("new"); sessions.UpdateStatus(ctx, org, id, state) }) }`},
		{name: "callback mutation", before: `func write() { store.WithTx(ctx, func() { sessions.UpdateStatus(ctx, org, id, models.SessionStatusIdle) }) }`, after: `func write() { store.WithTx(ctx, func() { sessions.UpdateStatus(ctx, org, id, models.SessionStatusFailed) }) }`, changed: true},
		{name: "enqueue receiver", before: `func write() { sessions.UpdateStatus(ctx, org, id, state); jobs.Enqueue(ctx, org, queue, kind, payload, priority, key) }`, after: `func write() { sessions.UpdateStatus(ctx, org, id, state); other.Enqueue(ctx, org, queue, kind, payload, priority, key) }`, changed: true},
		{name: "enqueue dedupe", before: `func write() { jobs.Enqueue(ctx, org, "agent", "continue_session", payload, 5, key) }`, after: `func write() { jobs.Enqueue(ctx, org, "agent", "continue_session", payload, 5, nil) }`, changed: true},
		{name: "enqueue transaction", before: `func write() { jobs.EnqueueInTx(ctx, tx, org, "agent", "continue_session", payload, 5, key) }`, after: `func write() { jobs.Enqueue(ctx, org, "agent", "continue_session", payload, 5, key) }`, changed: true},
		{name: "enqueue tx argument", before: `func write() { jobs.EnqueueInTx(ctx, tx, org, "agent", "continue_session", payload, 5, key) }`, after: `func write() { jobs.EnqueueInTx(ctx, otherTx, org, "agent", "continue_session", payload, 5, key) }`, changed: true},
		{name: "enqueue target payload", before: `func write() { jobs.EnqueueWithTarget(ctx, org, "agent", "continue_session", oldPayload, 5, key, target) }`, after: `func write() { jobs.EnqueueWithTarget(ctx, org, "agent", "continue_session", newPayload, 5, key, target) }`, changed: true},
		{name: "enqueue opts dedupe", before: `func write() { jobs.EnqueueInTxWithOpts(ctx, tx, org, Opts{JobType:"continue_session", Payload:payload, DedupeKey:key}) }`, after: `func write() { jobs.EnqueueInTxWithOpts(ctx, tx, org, Opts{JobType:"continue_session", Payload:payload}) }`, changed: true},
		{name: "local dedupe literal", before: `func write() { dedupeKey := "old:"+id; jobs.Enqueue(ctx, org, "agent", "continue_session", payload, 5, &dedupeKey) }`, after: `func write() { dedupeKey := "new:"+id; jobs.Enqueue(ctx, org, "agent", "continue_session", payload, 5, &dedupeKey) }`, changed: true},
		{name: "typed dedupe derivation", before: `func write() { key := db.ContinueSessionDedupeKey(sessionID); jobs.Enqueue(ctx, org, "agent", "continue_session", payload, 5, &key) }`, after: `func write() { key := db.ContinueSessionDedupeKey(threadID); jobs.Enqueue(ctx, org, "agent", "continue_session", payload, 5, &key) }`, changed: true},
		{name: "local opts dedupe", before: `func write() { opts := Opts{JobType:"continue_session", DedupeKey:key}; jobs.EnqueueInTxWithOpts(ctx, tx, org, opts) }`, after: `func write() { opts := Opts{JobType:"continue_session"}; jobs.EnqueueInTxWithOpts(ctx, tx, org, opts) }`, changed: true},
		{name: "enqueue unrelated local log", before: `func write() { info := "old"; logger.Info(info); jobs.Enqueue(ctx, org, "agent", "continue_session", payload, 5, key) }`, after: `func write() { info := "new"; logger.Info(info); jobs.Enqueue(ctx, org, "agent", "continue_session", payload, 5, key) }`},
		{name: "payload index binding added", before: `func write() { payload := map[string]string{}; jobs.Enqueue(ctx, org, "agent", "continue_session", payload, 5, key) }`, after: `func write() { payload := map[string]string{}; payload["human_input_request_id"] = rid; jobs.Enqueue(ctx, org, "agent", "continue_session", payload, 5, key) }`, changed: true},
		{name: "payload index binding removed", before: `func write() { payload["human_input_request_id"] = rid; jobs.Enqueue(ctx, org, "agent", "continue_session", payload, 5, key) }`, after: `func write() { jobs.Enqueue(ctx, org, "agent", "continue_session", payload, 5, key) }`, changed: true},
		{name: "payload index value", before: `func write() { payload["human_input_request_id"] = oldID; jobs.Enqueue(ctx, org, "agent", "continue_session", payload, 5, key) }`, after: `func write() { payload["human_input_request_id"] = newID; jobs.Enqueue(ctx, org, "agent", "continue_session", payload, 5, key) }`, changed: true},
		{name: "payload indexed selector", before: `func write() { envelope.Inputs[0].Payload["request"] = oldID; jobs.Enqueue(ctx, org, "agent", "continue_session", envelope.Inputs[0].Payload, 5, key) }`, after: `func write() { envelope.Inputs[0].Payload["request"] = newID; jobs.Enqueue(ctx, org, "agent", "continue_session", envelope.Inputs[0].Payload, 5, key) }`, changed: true},
		{name: "payload pointer index", before: `func write() { (*payload)["request"] = oldID; jobs.Enqueue(ctx, org, "agent", "continue_session", payload, 5, key) }`, after: `func write() { (*payload)["request"] = newID; jobs.Enqueue(ctx, org, "agent", "continue_session", payload, 5, key) }`, changed: true},
		{name: "payload index key derivation", before: `func write() { field := "old"; payload[field] = rid; jobs.Enqueue(ctx, org, "agent", "continue_session", payload, 5, key) }`, after: `func write() { field := "new"; payload[field] = rid; jobs.Enqueue(ctx, org, "agent", "continue_session", payload, 5, key) }`, changed: true},
		{name: "unrelated index and logging", before: `func write() { other["request"] = "old"; logger.Info("old"); jobs.Enqueue(ctx, org, "agent", "continue_session", payload, 5, key) }`, after: `func write() { other["request"] = "new"; logger.Info("new"); jobs.Enqueue(ctx, org, "agent", "continue_session", payload, 5, key) }`},
		{name: "dedupe helper return", before: `func ContinueSessionDedupeKey(id string) string { logger.Info("unchanged"); return "continue:"+id }`, after: `func ContinueSessionDedupeKey(id string) string { logger.Info("unchanged"); return "new:"+id }`, changed: true},
		{name: "dedupe helper logging", before: `func ContinueSessionDedupeKey(id string) string { logger.Info("old"); return "continue:"+id }`, after: `func ContinueSessionDedupeKey(id string) string { logger.Info("new"); return "continue:"+id }`},
		{name: "dedupe helper local prefix", before: `func ContinueSessionDedupeKey(id string) string { prefix := "old:"; return prefix+id }`, after: `func ContinueSessionDedupeKey(id string) string { prefix := "new:"; return prefix+id }`, changed: true},
		{name: "dedupe helper package prefix", before: `const prefix = "old:"; func ContinueSessionDedupeKey(id string) string { return prefix+id }`, after: `const prefix = "new:"; func ContinueSessionDedupeKey(id string) string { return prefix+id }`, changed: true},
		{name: "enqueue job type", before: `func write() { jobs.Enqueue(ctx, org, "agent", "continue_session", payload, 5, key) }`, after: `func write() { jobs.Enqueue(ctx, org, "agent", "run_agent", payload, 5, key) }`, changed: true},
		{name: "enqueue order", before: `func write() { jobs.Enqueue(ctx, org, "agent", "continue_session", first, 5, key); jobs.Enqueue(ctx, org, "agent", "continue_session", second, 5, key) }`, after: `func write() { jobs.Enqueue(ctx, org, "agent", "continue_session", second, 5, key); jobs.Enqueue(ctx, org, "agent", "continue_session", first, 5, key) }`, changed: true},
		{name: "switch eligibility", before: `func write() { switch thread.Status { case models.ThreadStatusIdle, models.ThreadStatusRunning: sessions.UpdateStatus(ctx, org, id, state) } }`, after: `func write() { switch thread.Status { case models.ThreadStatusIdle: sessions.UpdateStatus(ctx, org, id, state) } }`, changed: true},
		{name: "switch tag", before: `func write() { switch thread.Status { case models.ThreadStatusIdle: sessions.UpdateStatus(ctx, org, id, state) } }`, after: `func write() { switch parent.Status { case models.ThreadStatusIdle: sessions.UpdateStatus(ctx, org, id, state) } }`, changed: true},
		{name: "switch logging", before: `func write() { switch thread.Status { case models.ThreadStatusIdle: logger.Info("old"); sessions.UpdateStatus(ctx, org, id, state) } }`, after: `func write() { switch thread.Status { case models.ThreadStatusIdle: logger.Info("new"); sessions.UpdateStatus(ctx, org, id, state) } }`},
		{name: "expiry availability before delete", before: `func write() { snapshotStore.Delete(ctx, key); sessions.UpdateSandboxState(ctx, org, id, destroyed) }`, after: `func write() { sessions.UpdateSandboxState(ctx, org, id, destroyed); snapshotStore.Delete(ctx, key) }`, changed: true},
		{name: "archive cleanup key argument", before: `func write() { storage.CleanupSessionSnapshot(ctx, snapshots, sessions, org, id, oldKey) }`, after: `func write() { storage.CleanupSessionSnapshot(ctx, snapshots, sessions, org, id, newKey) }`, changed: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			before, err := scan(fixture(t, tt.before))
			require.NoError(t, err, "baseline contract fixture should parse")
			after, err := scan(fixture(t, tt.after))
			require.NoError(t, err, "edited contract fixture should parse")
			require.Equal(t, tt.changed, before[0].SourceSHA256 != after[0].SourceSHA256, "only discovered lifecycle contract changes should invalidate inventory")
		})
	}
}

func TestNonStatusLifecycleGatesAreInventoried(t *testing.T) {
	t.Parallel()
	tests := []struct{ name, query, kind string }{
		{name: "owner marker", query: "UPDATE sessions SET automation_owner_generation_id=$2 WHERE org_id=$1", kind: "lifecycle-gate-update"},
		{name: "archive", query: "UPDATE session_threads SET archived_at=now() WHERE org_id=$1", kind: "lifecycle-gate-update"},
		{name: "workspace revision", query: "UPDATE sessions SET workspace_revision=workspace_revision+1 WHERE org_id=$1", kind: "lifecycle-gate-update"},
		{name: "snapshot", query: "UPDATE sessions SET pending_snapshot_key=$2 WHERE org_id=$1", kind: "lifecycle-gate-update"},
		{name: "context availability", query: "UPDATE sessions SET sandbox_state=$2 WHERE org_id=$1", kind: "lifecycle-gate-update"},
		{name: "repair reservation", query: "UPDATE pull_request_repair_runs SET active=false WHERE org_id=$1", kind: "authority-table-write"},
		{name: "answer mutation", query: "UPDATE session_human_input_requests SET status='answered' WHERE org_id=$1", kind: "authority-table-write"},
		{name: "generation release", query: "UPDATE automation_target_sessions SET ownership_release_pending=false WHERE org_id=$1", kind: "authority-table-write"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			found, err := scan(fixture(t, "func gate() { exec(`"+tt.query+"`) }"))
			require.NoError(t, err, "lifecycle gate fixture should parse")
			require.Equal(t, []string{tt.kind}, found[0].Kinds, "a non-status authority mutation must not escape inventory")
		})
	}
}

func TestCleanupSessionSnapshotFingerprintsDeleteAndClearOrder(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, before, after string
		changed             bool
	}{
		{name: "deletion before versus after clear", before: `store.Delete(ctx, *key); clearer.ClearSnapshotKey(ctx, org, id)`, after: `clearer.ClearSnapshotKey(ctx, org, id); store.Delete(ctx, *key)`, changed: true},
		{name: "exact delete key", before: `store.Delete(ctx, *key); clearer.ClearSnapshotKey(ctx, org, id)`, after: `store.Delete(ctx, *replacement); clearer.ClearSnapshotKey(ctx, org, id)`, changed: true},
		{name: "delete receiver", before: `store.Delete(ctx, *key); clearer.ClearSnapshotKey(ctx, org, id)`, after: `other.Delete(ctx, *key); clearer.ClearSnapshotKey(ctx, org, id)`, changed: true},
		{name: "clear association", before: `store.Delete(ctx, *key); clearer.ClearSnapshotKey(ctx, org, id)`, after: `store.Delete(ctx, *key); clearer.ClearSnapshotKey(ctx, org, successor)`, changed: true},
		{name: "unrelated logging", before: `logger.Info("old"); store.Delete(ctx, *key); clearer.ClearSnapshotKey(ctx, org, id)`, after: `logger.Info("new"); store.Delete(ctx, *key); clearer.ClearSnapshotKey(ctx, org, id)`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			scanBody := func(body string) []candidate {
				root := fixture(t, "func unrelated() {}")
				dir := filepath.Join(root, "internal", "services", "storage")
				require.NoError(t, os.MkdirAll(dir, 0o700), "cleanup fixture should use the exact narrowly inventoried helper path")
				require.NoError(t, os.WriteFile(filepath.Join(dir, "snapshot.go"), []byte("package storage\nfunc CleanupSessionSnapshot() { "+body+" }"), 0o600), "exact cleanup-helper fixture should be written")
				found, err := scan(root)
				require.NoError(t, err, "cleanup-helper fixture should parse")
				var ids []string
				for _, item := range found {
					ids = append(ids, item.ID)
				}
				require.Equal(t, []string{"internal/services/storage/snapshot.go:CleanupSessionSnapshot"}, ids, "only the exact cleanup helper should be selected")
				return found
			}
			before, after := scanBody(tt.before), scanBody(tt.after)
			require.Equal(t, tt.changed, before[0].SourceSHA256 != after[0].SourceSHA256, "cleanup argument/effect order changes must drift while unrelated logs remain stable")
		})
	}
}

func TestPublicationContractsAreRequiredOnlyAtExactRoots(t *testing.T) {
	t.Parallel()
	tests := []struct {
		method, receiver string
		selected         bool
	}{
		{method: "CreatePR", receiver: "PRService", selected: true},
		{method: "CreateBranch", receiver: "PRService", selected: true},
		{method: "PushChangesToPR", receiver: "PRService", selected: true},
		{method: "dispatchPostPRSnapshotUpload", receiver: "PRService", selected: true},
		{method: "CreatePR", receiver: "UnrelatedStore"},
		{method: "OtherUpload", receiver: "PRService"},
	}
	for _, tt := range tests {
		t.Run(tt.receiver+"."+tt.method, func(t *testing.T) {
			t.Parallel()
			found, err := scan(publicationFixture(t, "func (s *"+tt.receiver+") "+tt.method+"() { s.blobs.Save(ctx, key, reader) }"))
			require.NoError(t, err, "publication root fixture should parse")
			var ids []string
			for _, item := range found {
				ids = append(ids, item.ID)
			}
			var expected []string
			if tt.selected {
				expected = []string{"internal/services/github/pr.go:" + tt.receiver + "." + tt.method}
			}
			require.Equal(t, expected, ids, "only exact source/receiver/root combinations should classify otherwise generic Save calls")
			if !tt.selected {
				return
			}
			reviewed := reviewedFixture(found)
			reviewed.Entries[0].CanonicalTarget = ""
			require.ErrorContains(t, compare(found, reviewed), "mutation requires concrete canonical authority/outcome/0a contract", "publication producers and uploader must not pass as syntax-only wrappers")
		})
	}
}

func TestPublicationFingerprintsTrackKeyIdentityAndEffectOrder(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, method, before, after string
		beforeDecl, afterDecl       string
		changed                     bool
	}{
		{name: "PR fixed-key literal", method: "CreatePR", before: `key := fmt.Sprintf("post-pr/%s", id); s.sessions.SetPendingSnapshotKey(ctx, org, id, key); s.dispatchPostPRSnapshotUpload(org, id, key, path, size)`, after: `key := fmt.Sprintf("post-pr-new/%s", id); s.sessions.SetPendingSnapshotKey(ctx, org, id, key); s.dispatchPostPRSnapshotUpload(org, id, key, path, size)`, changed: true},
		{name: "branch key argument", method: "CreateBranch", before: `key := fmt.Sprintf("post-branch/%s", id); s.sessions.SetPendingSnapshotKey(ctx, org, id, key)`, after: `key := fmt.Sprintf("post-branch/%s", successor); s.sessions.SetPendingSnapshotKey(ctx, org, id, key)`, changed: true},
		{name: "push UUID derivation", method: "PushChangesToPR", before: `uploadID := uuid.New(); key := uploadID.String(); s.dispatchPostPRSnapshotUpload(org, id, key, path, size)`, after: `uploadID := previousID; key := uploadID.String(); s.dispatchPostPRSnapshotUpload(org, id, key, path, size)`, changed: true},
		{name: "key helper argument", method: "CreatePR", before: `key := uploadKey(attempt); s.dispatchPostPRSnapshotUpload(org, id, key, path, size)`, after: `key := uploadKey(oldAttempt); s.dispatchPostPRSnapshotUpload(org, id, key, path, size)`, changed: true},
		{name: "package key prefix", method: "CreatePR", beforeDecl: `const prefix = "old/"`, afterDecl: `const prefix = "new/"`, before: `key := prefix + id; s.sessions.SetPendingSnapshotKey(ctx, org, id, key)`, after: `key := prefix + id; s.sessions.SetPendingSnapshotKey(ctx, org, id, key)`, changed: true},
		{name: "set pending association", method: "CreatePR", before: `s.sessions.SetPendingSnapshotKey(ctx, org, id, key); s.dispatchPostPRSnapshotUpload(org, id, key, path, size)`, after: `s.sessions.SetPendingSnapshotKey(ctx, org, successor, key); s.dispatchPostPRSnapshotUpload(org, id, key, path, size)`, changed: true},
		{name: "dispatch key", method: "CreateBranch", before: `s.dispatchPostPRSnapshotUpload(org, id, key, path, size)`, after: `s.dispatchPostPRSnapshotUpload(org, id, otherKey, path, size)`, changed: true},
		{name: "pending versus dispatch order", method: "PushChangesToPR", before: `s.sessions.SetPendingSnapshotKey(ctx, org, id, key); s.dispatchPostPRSnapshotUpload(org, id, key, path, size)`, after: `s.dispatchPostPRSnapshotUpload(org, id, key, path, size); s.sessions.SetPendingSnapshotKey(ctx, org, id, key)`, changed: true},
		{name: "save key", method: "dispatchPostPRSnapshotUpload", before: `s.snapshots.Save(ctx, key, f); s.sessions.PromotePendingSnapshot(ctx, org, id, key)`, after: `s.snapshots.Save(ctx, otherKey, f); s.sessions.PromotePendingSnapshot(ctx, org, id, key)`, changed: true},
		{name: "save receiver", method: "dispatchPostPRSnapshotUpload", before: `s.snapshots.Save(ctx, key, f)`, after: `other.Save(ctx, key, f)`, changed: true},
		{name: "save reader binding", method: "dispatchPostPRSnapshotUpload", before: `f := open(oldPath); s.snapshots.Save(ctx, key, f)`, after: `f := open(newPath); s.snapshots.Save(ctx, key, f)`, changed: true},
		{name: "promote identity", method: "dispatchPostPRSnapshotUpload", before: `s.sessions.PromotePendingSnapshot(ctx, org, id, key)`, after: `s.sessions.PromotePendingSnapshot(ctx, org, id, replacement)`, changed: true},
		{name: "clear association", method: "dispatchPostPRSnapshotUpload", before: `s.sessions.ClearPendingSnapshot(ctx, org, id)`, after: `s.sessions.ClearPendingSnapshot(ctx, org, successor)`, changed: true},
		{name: "clear expected-key argument", method: "dispatchPostPRSnapshotUpload", before: `s.sessions.ClearPendingSnapshot(ctx, org, id, key)`, after: `s.sessions.ClearPendingSnapshot(ctx, org, id)`, changed: true},
		{name: "save before promote", method: "dispatchPostPRSnapshotUpload", before: `s.snapshots.Save(ctx, key, f); s.sessions.PromotePendingSnapshot(ctx, org, id, key)`, after: `s.sessions.PromotePendingSnapshot(ctx, org, id, key); s.snapshots.Save(ctx, key, f)`, changed: true},
		{name: "save result branch", method: "dispatchPostPRSnapshotUpload", before: `if saveErr := s.snapshots.Save(ctx, key, f); saveErr != nil { s.sessions.ClearPendingSnapshot(ctx, org, id) }`, after: `if saveErr := s.snapshots.Save(ctx, key, f); saveErr == nil { s.sessions.ClearPendingSnapshot(ctx, org, id) }`, changed: true},
		{name: "capture eligibility", method: "CreatePR", before: `if pushed.CapturedSnapshotPath != "" { s.sessions.SetPendingSnapshotKey(ctx, org, id, key) }`, after: `if pushed.CapturedSnapshotPath == "" { s.sessions.SetPendingSnapshotKey(ctx, org, id, key) }`, changed: true},
		{name: "clear effect order", method: "dispatchPostPRSnapshotUpload", before: `s.sessions.ClearPendingSnapshot(ctx, org, id); s.snapshots.Save(ctx, key, f)`, after: `s.snapshots.Save(ctx, key, f); s.sessions.ClearPendingSnapshot(ctx, org, id)`, changed: true},
		{name: "unrelated error branch logging", method: "CreateBranch", before: `if infoErr != nil { logger.Info("old") }; s.sessions.SetPendingSnapshotKey(ctx, org, id, key)`, after: `if infoErr == nil { logger.Info("new") }; s.sessions.SetPendingSnapshotKey(ctx, org, id, key)`},
		{name: "callback logging and unrelated local", method: "dispatchPostPRSnapshotUpload", before: `go func() { info := "old"; logger.Info(info); s.snapshots.Save(ctx, key, f); s.sessions.PromotePendingSnapshot(ctx, org, id, key) }()`, after: `go func() { info := "new"; logger.Info(info); s.snapshots.Save(ctx, key, f); s.sessions.PromotePendingSnapshot(ctx, org, id, key) }()`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			scanSource := func(declarations, body string) []candidate {
				found, err := scan(publicationFixture(t, declarations+"\nfunc (s *PRService) "+tt.method+"() { "+body+" }"))
				require.NoError(t, err, "publication effect fixture should parse")
				var ids []string
				for _, item := range found {
					ids = append(ids, item.ID)
				}
				require.Equal(t, []string{"internal/services/github/pr.go:PRService." + tt.method}, ids, "fixture should select exactly the intended publication root")
				return found
			}
			before, after := scanSource(tt.beforeDecl, tt.before), scanSource(tt.afterDecl, tt.after)
			require.Equal(t, tt.changed, before[0].SourceSHA256 != after[0].SourceSHA256, "publication key derivation and pipeline identity/order must drift without hashing unrelated callback/logging bodies")
		})
	}
}

func TestInventoryMatchesRepository(t *testing.T) {
	t.Parallel()
	_, source, _, ok := runtime.Caller(0)
	require.True(t, ok, "test source path should locate repository independently of cwd")
	root := filepath.Clean(filepath.Join(filepath.Dir(source), "..", ".."))
	var out bytes.Buffer
	require.NoError(t, run(root, "cmd/lint-lifecycle/inventory.json", false, &out), "reviewed lifecycle inventory should match all current repository candidates")
	require.Contains(t, out.String(), "syntax coverage only", "successful inventory must not claim runtime fencing")

	// run already proves exact equality between discovery and this manifest.
	// Read those IDs for required-root assertions instead of scanning every
	// production Go file a second time (especially expensive under -race).
	manifest, err := os.ReadFile(filepath.Join(root, "cmd", "lint-lifecycle", "inventory.json"))
	require.NoError(t, err, "the verified manifest should be available for known-path assertions")
	var reviewed inventory
	require.NoError(t, json.Unmarshal(manifest, &reviewed), "the verified manifest should retain its typed inventory contract")
	var ids []string
	for _, item := range reviewed.Entries {
		ids = append(ids, item.ID)
	}
	for _, id := range []string{
		"internal/db/session_store.go:SessionStore.BeginRuntime",
		"internal/db/session_store.go:SessionStore.ResetAfterLostReuse",
		"internal/db/session_executor_store.go:SessionExecutorStore.ReclaimLost",
		"internal/db/session_executor_store.go:SessionExecutorStore.ReclaimLostForSession",
		"internal/db/session_thread_workspace_failure.go:SessionThreadStore.FailWorkspaceWaitForDeadLetter",
		"internal/db/session_review_workspace_recovery.go:SessionStore.RecoverIncompleteCodeReviewWorkspace",
		"internal/db/code_review_terminal_recovery.go:reconcileTerminalReviews",
		"internal/db/automation_dispatch.go:SessionStore.ClaimForAutomationTurn",
		"internal/db/automation_dispatch.go:SessionThreadStore.ClaimPrimaryForAutomationTurn",
		"internal/db/continuation_recovery.go:JobStore.RecordContinuationAttemptWithLease",
		"internal/db/session_activity_seed.go:SessionActivityPhaseStore.StartSeedPhase",
		"internal/db/session_activity_seed.go:SessionActivityPhaseStore.StartSeedPhaseInTx",
		"internal/db/session_activity_phases.go:SessionActivityPhaseStore.acknowledgeInboxBatchInTx",
		"internal/db/session_activity_phases.go:SessionActivityPhaseStore.AcknowledgeInboxBatch",
		"internal/db/session_activity_phases.go:SessionActivityPhaseStore.AcknowledgeInboxBatchWithTransition",
		"internal/db/session_activity_phases.go:SessionActivityPhaseStore.AcknowledgeInboxBatchAndStartPhase",
		"internal/services/agent/activity_phases.go:ActivityPhaseService.AcknowledgeInboxBatch",
		"internal/services/agent/activity_phases.go:ActivityPhaseService.AcknowledgeInboxBatchAndStartPhase",
		"internal/services/agent/activity_phase_execution.go:activityPhaseExecution.acknowledgeAndResume",
		"internal/services/codereview/scheduling.go:scheduledSessionStore.Create",
		"internal/db/session_result_persistence.go:SessionStore.UpdateResultInTx",
		"internal/db/session_result_persistence.go:SessionStore.UpdateStatusInTx",
		"internal/db/session_result_persistence.go:SessionStore.UpdateTurnCompleteInTx",
		"internal/db/session_result_persistence.go:SessionStore.UpdateResultAndCompleteActivityPhaseInTx",
		"internal/db/session_result_persistence.go:SessionStore.UpdateStatusAndCompleteActivityPhaseInTx",
		"internal/db/session_result_persistence.go:SessionStore.UpdateTurnCompleteAndCompleteActivityPhaseInTx",
		"internal/db/session_thread_persistence.go:SessionThreadStore.UpdateStatusInTx",
		"internal/db/session_thread_persistence.go:SessionThreadStore.UpdateResultInTx",
		"internal/db/session_thread_persistence.go:SessionThreadStore.CompleteTurnInTx",
		"internal/db/session_thread_persistence.go:SessionThreadStore.UpdateTurnCompleteInTx",
		"internal/db/code_review_recheck_dispatch.go:CodeReviewRecheckStore.ReconcileDrainedTerminalTurnInTx",
		"internal/db/code_review_schedule_assessment.go:CodeReviewScheduleStore.SettleAssessmentInTx",
		"internal/db/code_review_scheduling.go:CodeReviewScheduleStore.withLockedPRInTx",
		"internal/db/code_review_schedule_assessment.go:settleAssessmentLockedPR",
		"internal/services/automations/turn_store.go:TurnStore.EndAttempt",
		"internal/worker/code_review_recheck_handler.go:publishCodeReviewRecheck",
		"internal/services/automations/turn_attempt_persistence.go:newTurnAttemptSessions",
		"internal/services/automations/turn_attempt_persistence.go:turnAttemptSessions.UpdateStatus",
		"internal/services/automations/turn_attempt_persistence.go:turnAttemptSessions.UpdateResult",
		"internal/services/automations/turn_attempt_persistence.go:turnAttemptSessions.UpdateTurnComplete",
		"internal/services/codereview/recheck_admission.go:Service.cancelForcedEvidenceAssessment",
		"internal/services/agent/reaper.go:SessionReaper.reap",
		"internal/worker/handlers.go:answerQueuedHumanInputForContinue",
		"internal/db/automation_targets.go:AutomationTargetStore.InsertGeneration",
		"internal/db/automation_targets.go:AutomationTargetStore.RetireGeneration",
		"internal/db/automation_completion.go:AutomationTargetStore.ReleaseStrandedOwnership",
		"internal/db/pull_request_health.go:PullRequestStore.CreateRepairRun",
		"internal/db/pull_request_health.go:PullRequestStore.DeactivateRepairRun",
		"internal/db/session_human_input_requests.go:SessionHumanInputRequestStore.AnswerPending",
		"internal/db/session_human_input_requests.go:SessionHumanInputRequestStore.CancelPending",
		"internal/db/session_human_input_requests.go:SessionHumanInputRequestStore.AnswerLatestPendingFreeTextBySession",
		"internal/db/session_human_input_requests.go:SessionHumanInputRequestStore.AnswerLatestPendingFreeTextByThread",
		"internal/services/humaninput/db_repository.go:dbTx.AnswerRequest",
		"internal/services/humaninput/db_repository.go:dbTx.AppendApprovedCapabilityGrant",
		"internal/services/humaninput/service.go:Service.applyApprovedCapabilityGrant",
		"internal/db/session_thread_store.go:SessionThreadStore.Archive",
		"internal/db/pull_request_feedback.go:PullRequestFeedbackStore.QueueContinuation",
		"internal/db/code_review_loop_guard.go:stopFullReviewFallbackLoop",
		"internal/db/session_store.go:SessionStore.BumpWorkspaceRevision",
		"internal/db/session_store.go:SessionStore.writeDiffSnapshot",
		"internal/db/session_store.go:SessionStore.SetPendingSnapshotKey",
		"internal/db/session_store.go:SessionStore.PromotePendingSnapshot",
		"internal/db/session_store.go:SessionStore.ClearPendingSnapshot",
		"internal/db/session_store.go:SessionStore.ClearSnapshotKey",
		"internal/db/session_store.go:SessionStore.PublishCheckpointWithProvenance",
		"internal/db/session_checkpoint_persistence.go:SessionStore.PublishCheckpointWithProvenanceInTx",
		"internal/db/session_checkpoint_persistence.go:SessionStore.publishCheckpointWithProvenanceRow",
		"internal/services/automations/turn_store.go:TurnStore.PublishCheckpointWithProvenance",
		"internal/db/session_changesets.go:SessionChangesetStore.AcceptSplit",
		"internal/db/code_review_workspace.go:CodeReviewWorkspaceStore.PublishPrepared",
		"internal/db/session_thread_store.go:SessionThreadStore.SetBaseSnapshot",
		"internal/db/preview_store.go:PreviewStore.UpdatePreviewTargetSnapshotKey",
		"internal/db/jobs.go:JobStore.RetryWithoutConsumingAttemptWithLease",
		"internal/db/jobs.go:JobStore.RetryWithoutConsumingAttemptWithLeaseAndResetWindow",
		"internal/db/jobs.go:JobStore.RetryWithoutConsumingAttemptWithLeaseAndTarget",
		"internal/services/agent/activity_phase_execution.go:activityPhaseExecution.persistTurnCompleteAndComplete",
		"internal/services/agent/runtime.go:runtimeController.Begin",
		"internal/services/agent/automation_turn.go:Orchestrator.endCancelledAutomationTurn",
		"internal/services/agent/review_setup_hold.go:Orchestrator.acquireReviewSetupHold",
		"internal/api/handlers/sessions.go:SessionHandler.retrySessionFromCheckpoint",
		"internal/api/handlers/sessions.go:SessionHandler.claimRetryThread",
		"internal/services/humaninput/db_repository.go:dbTx.ClaimSessionForResume",
		"internal/services/humaninput/db_repository.go:dbTx.ClaimThreadForResume",
		"internal/services/thread/service.go:Service.claimSessionForSend",
		"internal/services/thread/service.go:Service.claimThreadForSend",
		"internal/db/session_sandbox_holder_store.go:SessionSandboxHolderStore.CreateActive",
		"internal/db/session_sandbox_holder_store.go:SessionSandboxHolderStore.ReleaseWithLease",
		"internal/db/session_changesets.go:SessionChangesetStore.AcquireLease",
		"internal/db/session_changesets.go:SessionChangesetStore.BeginMaterialization",
		"internal/db/session_changesets.go:SessionChangesetStore.RecordMaterializedDiff",
		"internal/db/session_changesets.go:SessionChangesetStore.CompleteMaterialization",
		"internal/services/agent/orchestrator.go:Orchestrator.MaterializeChangeset",
		"internal/services/preview/manager.go:Manager.LaunchPreview",
		"internal/services/preview/manager.go:Manager.ResumeStoppedWarmPreview",
		"internal/db/session_store.go:SessionStore.UpdateRevisionContext",
		"internal/db/session_store.go:SessionStore.ConsumeCancelRequest",
		"internal/db/thread_runtime_store.go:ThreadRuntimeStore.MarkTerminalWithLease",
		"internal/db/session_executor_store.go:SessionExecutorStore.MarkTerminalWithLease",
		"internal/db/session_store.go:SessionStore.UpdateSandboxState",
		"internal/db/session_store.go:SessionStore.ListExpiredSnapshots",
		"internal/db/session_store.go:SessionStore.MarkRunningWithSandboxState",
		"internal/db/session_store.go:SessionStore.ResetForRetry",
		"internal/db/session_store.go:SessionStore.UndoResetForRetry",
		"internal/db/session_thread_store.go:SessionThreadStore.FailRunningBySession",
		"internal/api/handlers/sessions.go:SessionHandler.retrySessionStartOver",
		"internal/services/agent/thread_runtime_delivery.go:keepSessionRunningIfSiblingRuntimesActive",
		"internal/services/agent/orchestrator.go:Orchestrator.cleanupReviewBundle",
		"internal/services/storage/snapshot.go:CleanupSessionSnapshot",
		"internal/api/handlers/sessions.go:SessionHandler.ArchiveSession",
		"internal/services/github/pr.go:PRService.runMergedPullRequestFollowUps",
		"internal/services/github/pr.go:PRService.maybeAutoArchiveSessionOnPRClose",
		"internal/services/github/pr.go:PRService.CreatePR",
		"internal/services/github/pr.go:PRService.CreateBranch",
		"internal/services/github/pr.go:PRService.PushChangesToPR",
		"internal/services/github/pr.go:PRService.dispatchPostPRSnapshotUpload",
		"internal/db/jobs.go:RunAgentDedupeKey",
		"internal/db/jobs.go:ContinueSessionDedupeKey",
		"internal/db/jobs.go:OpenPRDedupeKey",
		"internal/services/automations/target_dispatch.go:AutomationTurnDedupeKey",
		"internal/services/agent/orchestrator.go:continueSessionDedupeKey",
		"internal/services/agent/orchestrator.go:continueSessionDrainDedupeKey",
	} {
		require.True(t, slices.Contains(ids, id), "required M0 caller should be inventoried: %s", id)
	}
}

func TestRunRejectsMalformedInventory(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		json string
		want string
	}{
		{name: "unknown field", json: `{"version":1,"extra":"not accepted"}`, want: "unknown field"},
		{name: "invalid JSON", json: `{`, want: "decode lifecycle inventory"},
		{name: "trailing JSON value", json: `{"version":1} {}`, want: "exactly one JSON object"},
		{name: "trailing garbage", json: `{"version":1} not-json`, want: "exactly one JSON object"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			root := fixture(t, "func unused() {}")
			require.NoError(t, os.WriteFile(filepath.Join(root, "inventory.json"), []byte(tt.json), 0o600), "malformed inventory fixture should be written")
			require.ErrorContains(t, run(root, "inventory.json", false, &bytes.Buffer{}), tt.want, "malformed inventory must not be accepted")
		})
	}
}

func TestDiscoverIsStableAndDoesNotApprove(t *testing.T) {
	t.Parallel()
	root := fixture(t, "func initial() { enqueue(\"run_agent\") }")
	var first, second bytes.Buffer
	require.NoError(t, run(root, "missing.json", true, &first), "discovery should not require an approved manifest")
	require.NoError(t, run(root, "missing.json", true, &second), "repeat discovery should succeed")
	require.Equal(t, first.String(), second.String(), "discovery should be deterministic")
	var got []candidate
	require.NoError(t, json.Unmarshal(first.Bytes(), &got), "discovery should emit valid candidate JSON")
	require.Equal(t, []string{"dispatch-reference"}, got[0].Kinds, "initial-job producers should be discoverable independently of status writes")
	_, err := os.Stat(filepath.Join(root, "missing.json"))
	require.True(t, os.IsNotExist(err), "discovery must never write or approve an inventory")
	require.False(t, strings.Contains(first.String(), "classification"), "discovery must not synthesize reviewed ownership classifications")
}
