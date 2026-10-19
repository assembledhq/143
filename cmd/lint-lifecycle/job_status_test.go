package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestJobSQLStatusFacts(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, query string
		expected    []string
	}{
		{name: "dedupe set", query: `SELECT 1 FROM jobs WHERE status IN ('pending','running')`, expected: []string{"status in ( 'pending' , 'running' )"}},
		{name: "aliased negated set", query: `SELECT 1 FROM public."jobs" AS j WHERE j."status" NOT IN ('failed','cancelled')`, expected: []string{"j . status not in ( 'failed' , 'cancelled' )"}},
		{name: "single running owner", query: `UPDATE jobs AS owner SET lock_token=NULL WHERE owner.status='running'`, expected: []string{"owner . status = 'running'"}},
		{name: "single not running", query: `SELECT 1 FROM jobs owner WHERE owner.status <> 'running'`, expected: []string{"owner . status <> 'running'"}},
		{name: "bound status argument", query: `SELECT 1 FROM jobs WHERE status=$3`, expected: []string{"status = $3"}},
		{name: "nested no active work", query: `SELECT id FROM sessions s WHERE NOT EXISTS(SELECT 1 FROM jobs peer WHERE peer.status='running')`, expected: []string{"peer . status = 'running'"}},
		{name: "outer unrelated status", query: `SELECT 1 FROM sessions s WHERE s.status='running' AND NOT EXISTS(SELECT 1 FROM jobs j WHERE j.status IN ('pending','running'))`, expected: []string{"j . status in ( 'pending' , 'running' )"}},
		{name: "CTE scopes exclude node status", query: `WITH active AS (SELECT id FROM nodes WHERE status='active'), picked AS (SELECT id FROM jobs j WHERE j.status='pending') UPDATE jobs SET status='running' FROM picked WHERE jobs.id=picked.id`, expected: []string{"j . status = 'pending'", "status = 'running'"}},
		{name: "CTE projected job status", query: `WITH stale AS (SELECT j.id,j.status AS job_status FROM jobs j) SELECT id FROM stale WHERE stale.job_status IN ('failed','cancelled')`, expected: []string{"j . status", "job_status", "stale . job_status in ( 'failed' , 'cancelled' )"}},
		{name: "inner alias shadows outer job", query: `SELECT j.status FROM jobs j WHERE EXISTS(SELECT 1 FROM nodes j WHERE j.status='active')`, expected: []string{"j . status"}},
		{name: "quoted words not SQL", query: `SELECT 'jobs status SELECT' FROM nodes WHERE status='active'`},
		{name: "wave status is not job status", query: `UPDATE worker_deploy_waves SET status='paused' WHERE status IN ('pending','running')`},
		{name: "quoted differently cased table is unrelated", query: `SELECT status FROM "Jobs" WHERE status='pending'`},
		{name: "joined unrelated status ignored", query: `SELECT 1 FROM jobs j JOIN worker_deploy_waves wave ON wave.id=j.id WHERE wave.status='running' AND j.status='pending'`, expected: []string{"j . status = 'pending'"}},
		{name: "comments preserve following query", query: "-- jobs WHERE status='fake'\nSELECT status FROM jobs -- status='fake'\nWHERE status='running'", expected: []string{"status", "status = 'running'"}},
		{name: "block comments ignored", query: `SELECT /* jobs status='fake' */ status FROM jobs WHERE status='running'`, expected: []string{"status", "status = 'running'"}},
		{name: "separate statements do not leak jobs", query: `SELECT status FROM jobs; SELECT status FROM nodes WHERE status='active'`, expected: []string{"status"}},
		{name: "schema constraint", query: `ALTER TABLE jobs ADD CONSTRAINT jobs_status CHECK(status IN ('pending','running'))`, expected: []string{"status in ( 'pending' , 'running' )"}},
		{name: "partial index", query: `CREATE UNIQUE INDEX active_jobs ON jobs(queue,dedupe_key) WHERE status IN ('pending','running')`, expected: []string{"status in ( 'pending' , 'running' )"}},
		{name: "insert status projection", query: `INSERT INTO jobs(org_id,status) VALUES ($1,'pending')`, expected: []string{"status"}},
		{name: "literal quote escape", query: `SELECT 1 FROM jobs WHERE status != 'isn''t SELECT jobs'`, expected: []string{"status != 'isn''t SELECT jobs'"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.expected, jobSQLStatusFacts(tt.query), "only syntactically jobs-bound status references should be inventoried")
		})
	}
}

func TestScanJobStatusConstruction(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, source string
		expected     []string
	}{
		{name: "literal", source: "func check() { db.Query(ctx, `SELECT 1 FROM jobs j WHERE j.status='running'`) }", expected: []string{"j . status = 'running'"}},
		{name: "constant derivation", source: "const active = `('pending','running')`\nconst query = `SELECT 1 FROM jobs j WHERE j.status IN `+active\nfunc check() { db.Query(ctx, query) }", expected: []string{"j . status in ( 'pending' , 'running' )"}},
		{name: "local concatenation", source: "func check() { query := `SELECT 1 FROM jobs j`; query += ` WHERE j.status='running'`; db.Query(ctx,query) }", expected: []string{"j . status = 'running'"}},
		{name: "format dependency", source: "const active = `('pending','running')`\nfunc check() { query := fmt.Sprintf(`SELECT 1 FROM jobs j WHERE j.status IN %s`,active); db.Query(ctx,query) }", expected: []string{"j . status in ( 'pending' , 'running' )"}},
		{name: "lease helper sink", source: "func check() { return updateWithLease(ctx, `UPDATE jobs SET status='pending' WHERE status='running'`,jobID,token) }", expected: []string{"status = 'pending'", "status = 'running'"}},
		{name: "unknown set retained", source: "func check() { db.Query(ctx, `SELECT 1 FROM jobs WHERE status IN `+dynamicStates) }", expected: []string{"status in < dynamic >"}},
		{name: "typed Go comparison", source: "func check() { if job.Status == models.JobStatusPending { run() } }", expected: []string{"job.Status == models.JobStatusPending"}},
		{name: "typed Go switch", source: "func check() { switch job.Status { case models.JobStatusPending: run(); default: wait() } }", expected: []string{"switch job.Status;case(models.JobStatusPending);case()"}},
		{name: "known untyped status variable", source: "func check() { if jobStatus == \"running\" { run() } }", expected: []string{"jobStatus == \"running\""}},
		{name: "unrelated domain", source: "func check() { db.Query(ctx, `SELECT 1 FROM worker_deploy_waves WHERE status IN ('pending','running')`) }"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			found, err := scanJobStatus(fixture(t, tt.source))
			require.NoError(t, err, "syntactic jobs-status fixture should scan without requiring compilable DB dependencies")
			var facts []string
			for _, item := range found {
				facts = append(facts, item.Facts...)
			}
			require.Equal(t, tt.expected, facts, "constructed SQL and status conditions should retain their exact policy facts without duplicate format templates")
		})
	}
}

func reviewedJobStatusFixture(found []jobStatusCandidate) jobStatusInventory {
	reviewed := jobStatusInventory{Version: 1, Scope: []string{"fixture"}, Limitations: []string{"syntax only"}}
	for _, item := range found {
		reviewed.Entries = append(reviewed.Entries, jobStatusEntry{ID: item.ID, SourceSHA256: item.SourceSHA256, Classification: "nonterminal-consumer", Action0a: "use exact lifecycle consumer predicate including parked control", Reason: "parked intent still owns its lane", Evidence: []string{"fixture source"}})
	}
	return reviewed
}

func TestCompareJobStatusRequiresPolicies(t *testing.T) {
	t.Parallel()
	found, err := scanJobStatus(fixture(t, "func check() { db.Query(ctx, `SELECT 1 FROM jobs WHERE status IN ('pending','running')`) }"))
	require.NoError(t, err, "baseline jobs-status fixture should scan")
	tests := []struct {
		name string
		edit func(*jobStatusInventory)
		want string
	}{
		{name: "reviewed policy passes", edit: func(*jobStatusInventory) {}},
		{name: "new site fails", edit: func(i *jobStatusInventory) { i.Entries = nil }, want: "uncovered jobs-status site"},
		{name: "changed predicate fails", edit: func(i *jobStatusInventory) { i.Entries[0].SourceSHA256 = "old" }, want: "changed jobs-status source requires policy review"},
		{name: "deleted site fails", edit: func(i *jobStatusInventory) { i.Entries[0].ID += ":deleted" }, want: "stale jobs-status site"},
		{name: "duplicate policy fails", edit: func(i *jobStatusInventory) { i.Entries = append(i.Entries, i.Entries[0]) }, want: "duplicate jobs-status site"},
		{name: "missing migration fails", edit: func(i *jobStatusInventory) { i.Entries[0].Action0a = "" }, want: "unclassified jobs-status site"},
		{name: "unknown class fails", edit: func(i *jobStatusInventory) { i.Entries[0].Classification = "all-active" }, want: "unclassified jobs-status site"},
		{name: "missing scope fails", edit: func(i *jobStatusInventory) { i.Scope = nil }, want: "requires version 1"},
		{name: "mixed policies must be complete", edit: func(i *jobStatusInventory) {
			i.Entries[0].PredicatePolicies = []jobStatusPredicatePolicy{{Fact: "wrong", Classification: "runnable", Action0a: "pending only"}}
		}, want: "invalid jobs-status predicate policy"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			reviewed := reviewedJobStatusFixture(found)
			tt.edit(&reviewed)
			err := compareJobStatus(found, reviewed)
			if tt.want == "" {
				require.NoError(t, err, "reviewed unchanged jobs-status policy should pass")
			} else {
				require.ErrorContains(t, err, tt.want, "unreviewed or incomplete policy should fail closed with an actionable source identity")
			}
		})
	}
}

func TestJobStatusFingerprintChanges(t *testing.T) {
	t.Parallel()
	baseline := "const states = `('pending','running')`\nfunc check() { db.Query(ctx, `SELECT 1 FROM jobs j WHERE j.status IN `+states) }"
	before, err := scanJobStatus(fixture(t, baseline))
	require.NoError(t, err, "baseline policy should scan")
	tests := []struct{ name, source string }{
		{name: "changed statuses", source: strings.ReplaceAll(baseline, "'pending','running'", "'pending','running','awaiting_start_control'")},
		{name: "negated set", source: strings.ReplaceAll(baseline, "status IN", "status NOT IN")},
		{name: "changed jobs alias", source: strings.ReplaceAll(strings.ReplaceAll(baseline, "jobs j", "jobs peer"), "j.status", "peer.status")},
		{name: "single status", source: "func check() { db.Query(ctx, `SELECT 1 FROM jobs j WHERE j.status='running'`) }"},
		{name: "new query site", source: baseline + "\nfunc newOwner() { db.Query(ctx, `SELECT 1 FROM jobs WHERE status='pending'`) }"},
		{name: "eligibility changed", source: strings.ReplaceAll(baseline, "WHERE j.status", "WHERE j.lease_expires_at>now() AND j.status")},
		{name: "literal whitespace matters", source: strings.ReplaceAll(baseline, "'pending','running'", "'pending  ','running'")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			after, err := scanJobStatus(fixture(t, tt.source))
			require.NoError(t, err, "changed status syntax should still scan")
			require.Error(t, compareJobStatus(after, reviewedJobStatusFixture(before)), "any changed status set, alias, negation, eligibility or new site must require explicit policy review")
		})
	}
}

func TestRunJobStatusInventory(t *testing.T) {
	t.Parallel()
	root := fixture(t, "func check() { db.Query(ctx, `SELECT 1 FROM jobs WHERE status='running'`) }")
	found, err := scanJobStatus(root)
	require.NoError(t, err, "fixture should discover one status site")
	reviewed := reviewedJobStatusFixture(found)
	data, err := json.Marshal(reviewed)
	require.NoError(t, err, "fixture inventory should encode")
	require.NoError(t, os.WriteFile(filepath.Join(root, "reviewed.json"), data, 0o600), "fixture policy ledger should be saved")
	var out bytes.Buffer
	require.NoError(t, runJobStatus(root, "reviewed.json", false, &out), "reviewed ledger should pass its CLI path")
	require.Contains(t, out.String(), "1 classified syntax sites match", "CLI should distinguish policy inventory from runtime proof")
	out.Reset()
	require.NoError(t, runJobStatus(root, "missing.json", true, &out), "discovery must not require or write a reviewed ledger")
	var discovered []jobStatusCandidate
	require.NoError(t, json.Unmarshal(out.Bytes(), &discovered), "discovery should emit valid JSON")
	require.Equal(t, found, discovered, "discovery should retain exact deterministic site identities and fingerprints")
	require.ErrorContains(t, runJobStatus(root, "missing.json", false, &out), "read jobs-status inventory", "missing policy ledger should fail closed")
	require.NoError(t, os.WriteFile(filepath.Join(root, "bad.json"), []byte(`{"unknown":1}`), 0o600), "invalid fixture should be saved")
	require.ErrorContains(t, runJobStatus(root, "bad.json", false, &out), "decode jobs-status inventory", "unknown policy fields should not be ignored")
	require.NoError(t, os.WriteFile(filepath.Join(root, "extra.json"), append(data, []byte("{}")...), 0o600), "multiple JSON objects fixture should be saved")
	require.ErrorContains(t, runJobStatus(root, "extra.json", false, &out), "exactly one JSON object", "trailing unreviewed policy data should not be ignored")
}

func TestJobStatusInventoryMatchesRepository(t *testing.T) {
	t.Parallel()
	_, source, _, ok := runtime.Caller(0)
	require.True(t, ok, "test source path should locate the repository independently of the working directory")
	root := filepath.Clean(filepath.Join(filepath.Dir(source), "..", ".."))
	var out bytes.Buffer
	require.NoError(t, runJobStatus(root, "cmd/lint-lifecycle/job-status-inventory.json", false, &out), "checked-in jobs-status policies should cover every currently discovered repository site")
	require.Contains(t, out.String(), "classified syntax sites match; not SQL/type or runtime-policy proof", "the repository test must check the real policy ledger without claiming runtime enforcement")
}

func TestRunJobStatusRejectsNewUncoveredSite(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, source string
	}{
		{name: "single status", source: "func added() { db.Query(ctx, `SELECT 1 FROM jobs WHERE status='pending'`) }"},
		{name: "aliased status set", source: "func added() { db.Query(ctx, `SELECT 1 FROM jobs peer WHERE peer.status IN ('pending','running')`) }"},
		{name: "negated status set", source: "func added() { db.Query(ctx, `SELECT 1 FROM jobs peer WHERE peer.status NOT IN ('failed','cancelled')`) }"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			root := fixture(t, "func baseline() { db.Query(ctx, `SELECT 1 FROM jobs WHERE status='running'`) }")
			found, err := scanJobStatus(root)
			require.NoError(t, err, "the fixture baseline should discover its reviewed status site")
			data, err := json.Marshal(reviewedJobStatusFixture(found))
			require.NoError(t, err, "the baseline policy ledger should encode")
			require.NoError(t, os.WriteFile(filepath.Join(root, "reviewed.json"), data, 0o600), "the baseline policy ledger should be saved before new production syntax is added")
			var out bytes.Buffer
			require.NoError(t, runJobStatus(root, "reviewed.json", false, &out), "the fixture baseline should pass the same ledger-checking path used by the repository test")
			require.NoError(t, os.WriteFile(filepath.Join(root, "internal", "added.go"), []byte("package fixture\n"+tt.source), 0o600), "a new unreviewed production source site should be added outside test files")
			require.EqualError(t, runJobStatus(root, "reviewed.json", false, &out), "uncovered jobs-status site: internal/added.go:added:sql:1", "a new status predicate must fail the repository-test path until its exact source site has a reviewed policy")
		})
	}
}

func TestJobStatusCrossFileConstantsAndHarmlessChanges(t *testing.T) {
	t.Parallel()
	root := fixture(t, "func check() { db.Query(ctx, query) }")
	require.NoError(t, os.WriteFile(filepath.Join(root, "internal", "policy.go"), []byte("package fixture\nconst query = `SELECT 1 FROM jobs j WHERE j.status IN ` + statuses\nconst statuses = `('pending','running')`"), 0o600), "cross-file SQL and status constants should be saved")
	before, err := scanJobStatus(root)
	require.NoError(t, err, "cross-file package constants should resolve to a jobs status site")
	require.Equal(t, []string{"j . status in ( 'pending' , 'running' )"}, before[0].Facts, "forward-referenced status constant should remain a reviewed dependency")
	require.NoError(t, os.WriteFile(filepath.Join(root, "internal", "fixture.go"), []byte("package fixture\nfunc check() { logger.Info(\"irrelevant\"); db.Query(ctx, query) }\nfunc wave() { db.Exec(ctx,`UPDATE worker_deploy_waves SET status='paused' WHERE status IN ('pending','running')`) }"), 0o600), "harmless logging and an unrelated wave status predicate should be saved")
	after, err := scanJobStatus(root)
	require.NoError(t, err, "unrelated source edits should still scan")
	require.NoError(t, compareJobStatus(after, reviewedJobStatusFixture(before)), "logging and unrelated table status predicates must not create jobs policy churn")
	require.NoError(t, os.WriteFile(filepath.Join(root, "internal", "policy.go"), []byte("package fixture\nconst query = `SELECT  1 /* harmless */ FROM jobs j WHERE j.status IN ` + statuses\nconst statuses = `('pending','running')`"), 0o600), "SQL spacing/comment-only fixture should be saved")
	after, err = scanJobStatus(root)
	require.NoError(t, err, "comment-preserving tokenization should scan")
	require.NoError(t, compareJobStatus(after, reviewedJobStatusFixture(before)), "SQL token-equivalent whitespace/comments should not change policy fingerprints")
	require.NoError(t, os.WriteFile(filepath.Join(root, "internal", "policy.go"), []byte("package fixture\nconst query = `SELECT 1 FROM jobs j WHERE j.status IN ` + statuses\nconst statuses = `('pending','running','awaiting_start_control')`"), 0o600), "changed cross-file status dependency should be saved")
	after, err = scanJobStatus(root)
	require.NoError(t, err, "changed cross-file query should still scan")
	require.ErrorContains(t, compareJobStatus(after, reviewedJobStatusFixture(before)), "changed jobs-status source requires policy review", "cross-file constant changes must not bypass source policy review")
}
