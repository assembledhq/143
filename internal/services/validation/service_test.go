package validation

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/agent"
)

// --- Mock SandboxProvider ---

type mockSandboxProvider struct {
	files     map[string][]byte
	execFn    func(ctx context.Context, sb *agent.Sandbox, cmd string, stdout, stderr io.Writer) (int, error)
	execCalls []string
}

func newMockProvider() *mockSandboxProvider {
	return &mockSandboxProvider{
		files: make(map[string][]byte),
	}
}

func (m *mockSandboxProvider) Name() string { return "mock" }

func (m *mockSandboxProvider) Create(ctx context.Context, cfg agent.SandboxConfig) (*agent.Sandbox, error) {
	return &agent.Sandbox{ID: "mock-sandbox", Provider: "mock", WorkDir: "/workspace"}, nil
}

func (m *mockSandboxProvider) CloneRepo(ctx context.Context, sb *agent.Sandbox, repoURL, branch, token string) error {
	return nil
}

func (m *mockSandboxProvider) Exec(ctx context.Context, sb *agent.Sandbox, cmd string, stdout, stderr io.Writer) (int, error) {
	m.execCalls = append(m.execCalls, cmd)
	if m.execFn != nil {
		return m.execFn(ctx, sb, cmd, stdout, stderr)
	}
	return 0, nil
}

func (m *mockSandboxProvider) ReadFile(ctx context.Context, sb *agent.Sandbox, path string) ([]byte, error) {
	data, ok := m.files[path]
	if !ok {
		return nil, fmt.Errorf("file not found: %s", path)
	}
	return data, nil
}

func (m *mockSandboxProvider) WriteFile(ctx context.Context, sb *agent.Sandbox, path string, data []byte) error {
	m.files[path] = data
	return nil
}

func (m *mockSandboxProvider) Destroy(ctx context.Context, sb *agent.Sandbox) error {
	return nil
}

func (m *mockSandboxProvider) ConnectionInfo(ctx context.Context, sb *agent.Sandbox) (*agent.SandboxConnectionInfo, error) {
	return &agent.SandboxConnectionInfo{}, nil
}

// --- Mock Stores ---

type mockValidationStore struct {
	lastStatus   string
	checkResults map[string]string
	lastID       uuid.UUID
}

func (m *mockValidationStore) Create(ctx context.Context, v *models.Validation) error {
	v.ID = uuid.New()
	m.lastID = v.ID
	return nil
}

func (m *mockValidationStore) UpdateCheck(ctx context.Context, orgID, id uuid.UUID, checkName, result string, details []byte) error {
	m.checkResults[checkName] = result
	return nil
}

func (m *mockValidationStore) UpdateStatus(ctx context.Context, orgID, id uuid.UUID, status string) error {
	m.lastStatus = status
	return nil
}

type mockIssueStore struct {
	lastStatus string
}

func (m *mockIssueStore) UpdateStatus(ctx context.Context, orgID, issueID uuid.UUID, status string) error {
	m.lastStatus = status
	return nil
}

type mockJobStore struct {
	lastJobType string
}

func (m *mockJobStore) Enqueue(ctx context.Context, orgID uuid.UUID, queue, jobType string, payload any, priority int, dedupeKey *string) (uuid.UUID, error) {
	m.lastJobType = jobType
	return uuid.New(), nil
}

type mockStores struct {
	validations *mockValidationStore
	issues      *mockIssueStore
	jobs        *mockJobStore
}

func newMockStores() *mockStores {
	return &mockStores{
		validations: &mockValidationStore{checkResults: make(map[string]string)},
		issues:      &mockIssueStore{},
		jobs:        &mockJobStore{},
	}
}

// --- Security Scan Tests ---

func TestCheckSecurity_CleanDiff(t *testing.T) {
	t.Parallel()
	s := &Service{logger: zerolog.Nop()}

	diff := `--- a/main.go
+++ b/main.go
@@ -1,3 +1,4 @@
 package main
+import "fmt"
+func hello() { fmt.Println("hello") }
`
	result, details, err := s.checkSecurity(diff)
	require.NoError(t, err)
	assert.Equal(t, "pass", result)
	assert.Contains(t, details, "no security issues")
}

func TestCheckSecurity_AWSKeyDetected(t *testing.T) {
	t.Parallel()
	s := &Service{logger: zerolog.Nop()}

	diff := `--- a/config.go
+++ b/config.go
@@ -1,3 +1,4 @@
 package config
+const awsKey = "AKIAIOSFODNN7EXAMPLE"
`
	result, details, err := s.checkSecurity(diff)
	require.NoError(t, err)
	assert.Equal(t, "fail", result)
	assert.Contains(t, details, "potential secret detected")
}

func TestCheckSecurity_GitHubTokenDetected(t *testing.T) {
	t.Parallel()
	s := &Service{logger: zerolog.Nop()}

	diff := `--- a/auth.go
+++ b/auth.go
@@ -1,3 +1,4 @@
 package auth
+var token = "ghp_ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghij"
`
	result, details, err := s.checkSecurity(diff)
	require.NoError(t, err)
	assert.Equal(t, "fail", result)
	assert.Contains(t, details, "potential secret detected")
}

func TestCheckSecurity_HardcodedPasswordDetected(t *testing.T) {
	t.Parallel()
	s := &Service{logger: zerolog.Nop()}

	diff := `--- a/db.go
+++ b/db.go
@@ -1,3 +1,4 @@
 package db
+var password = "supersecretpassword123"
`
	result, details, err := s.checkSecurity(diff)
	require.NoError(t, err)
	assert.Equal(t, "fail", result)
	assert.Contains(t, details, "potential secret detected")
}

func TestCheckSecurity_PrivateKeyDetected(t *testing.T) {
	t.Parallel()
	s := &Service{logger: zerolog.Nop()}

	diff := `--- a/keys.go
+++ b/keys.go
@@ -1,3 +1,5 @@
 package keys
+const key = ` + "`" + `-----BEGIN RSA PRIVATE KEY-----
+MIIEpAIBAAKCAQEA...` + "`" + `
`
	result, details, err := s.checkSecurity(diff)
	require.NoError(t, err)
	assert.Equal(t, "fail", result)
	assert.Contains(t, details, "potential secret detected")
}

func TestCheckSecurity_SQLInjectionDetected(t *testing.T) {
	t.Parallel()
	s := &Service{logger: zerolog.Nop()}

	diff := `--- a/query.go
+++ b/query.go
@@ -1,3 +1,4 @@
 package query
+query := "SELECT * FROM users WHERE id = " + userID
`
	result, details, err := s.checkSecurity(diff)
	require.NoError(t, err)
	assert.Equal(t, "fail", result)
	assert.Contains(t, details, "SQL injection")
}

func TestCheckSecurity_FmtSprintfSQLDetected(t *testing.T) {
	t.Parallel()
	s := &Service{logger: zerolog.Nop()}

	diff := `--- a/query.go
+++ b/query.go
@@ -1,3 +1,4 @@
 package query
+q := fmt.Sprintf("SELECT * FROM users WHERE name = '%s'", name)
`
	result, details, err := s.checkSecurity(diff)
	require.NoError(t, err)
	assert.Equal(t, "fail", result)
	assert.Contains(t, details, "SQL injection")
}

func TestCheckSecurity_RemovedLinesIgnored(t *testing.T) {
	t.Parallel()
	s := &Service{logger: zerolog.Nop()}

	diff := `--- a/config.go
+++ b/config.go
@@ -1,4 +1,3 @@
 package config
-const awsKey = "AKIAIOSFODNN7EXAMPLE"
+// key removed for security
`
	result, _, err := s.checkSecurity(diff)
	require.NoError(t, err)
	assert.Equal(t, "pass", result)
}

// --- Diff Size Tests ---

func TestCheckDiffSize_Small(t *testing.T) {
	t.Parallel()
	s := &Service{logger: zerolog.Nop()}

	diff := generateDiff(10, 5)
	result, details, err := s.checkDiffSize(diff)
	require.NoError(t, err)
	assert.Equal(t, "pass", result)
	assert.Contains(t, details, "15 lines changed")
}

func TestCheckDiffSize_Warning(t *testing.T) {
	t.Parallel()
	s := &Service{logger: zerolog.Nop()}

	diff := generateDiff(150, 60)
	result, details, err := s.checkDiffSize(diff)
	require.NoError(t, err)
	assert.Equal(t, "warn", result)
	assert.Contains(t, details, "large diff")
}

func TestCheckDiffSize_Fail(t *testing.T) {
	t.Parallel()
	s := &Service{logger: zerolog.Nop()}

	diff := generateDiff(400, 150)
	result, details, err := s.checkDiffSize(diff)
	require.NoError(t, err)
	assert.Equal(t, "fail", result)
	assert.Contains(t, details, "diff too large")
}

func TestCheckDiffSize_EmptyDiff(t *testing.T) {
	t.Parallel()
	s := &Service{logger: zerolog.Nop()}

	result, details, err := s.checkDiffSize("")
	require.NoError(t, err)
	assert.Equal(t, "pass", result)
	assert.Contains(t, details, "0 lines changed")
}

// --- CI Check Tests ---

func TestCheckCI_GoTestsPass(t *testing.T) {
	t.Parallel()
	provider := newMockProvider()
	provider.files["go.mod"] = []byte("module test")
	provider.execFn = func(ctx context.Context, sb *agent.Sandbox, cmd string, stdout, stderr io.Writer) (int, error) {
		fmt.Fprint(stdout, "ok  \ttest\t0.001s\n")
		return 0, nil
	}

	s := &Service{provider: provider, logger: zerolog.Nop()}
	sandbox := &agent.Sandbox{ID: "test", WorkDir: "/workspace"}

	result, details, err := s.checkCI(context.Background(), sandbox)
	require.NoError(t, err)
	assert.Equal(t, "pass", result)
	assert.Contains(t, details, "go test")
}

func TestCheckCI_GoTestsFail(t *testing.T) {
	t.Parallel()
	provider := newMockProvider()
	provider.files["go.mod"] = []byte("module test")
	provider.execFn = func(ctx context.Context, sb *agent.Sandbox, cmd string, stdout, stderr io.Writer) (int, error) {
		fmt.Fprint(stderr, "FAIL\ttest\t0.001s\n--- FAIL: TestFoo (0.00s)\n    foo_test.go:10: expected 1, got 2\n")
		return 1, nil
	}

	s := &Service{provider: provider, logger: zerolog.Nop()}
	sandbox := &agent.Sandbox{ID: "test", WorkDir: "/workspace"}

	result, details, err := s.checkCI(context.Background(), sandbox)
	require.NoError(t, err)
	assert.Equal(t, "fail", result)
	assert.Contains(t, details, "tests failed")
	assert.Contains(t, details, "exit code 1")
}

func TestCheckCI_NpmTestsPass(t *testing.T) {
	t.Parallel()
	provider := newMockProvider()
	provider.files["package.json"] = []byte("{}")
	provider.execFn = func(ctx context.Context, sb *agent.Sandbox, cmd string, stdout, stderr io.Writer) (int, error) {
		fmt.Fprint(stdout, "Tests: 5 passed\n")
		return 0, nil
	}

	s := &Service{provider: provider, logger: zerolog.Nop()}
	sandbox := &agent.Sandbox{ID: "test", WorkDir: "/workspace"}

	result, details, err := s.checkCI(context.Background(), sandbox)
	require.NoError(t, err)
	assert.Equal(t, "pass", result)
	assert.Contains(t, details, "npm test")
}

func TestCheckCI_NoProjectType(t *testing.T) {
	t.Parallel()
	provider := newMockProvider()

	s := &Service{provider: provider, logger: zerolog.Nop()}
	sandbox := &agent.Sandbox{ID: "test", WorkDir: "/workspace"}

	result, details, err := s.checkCI(context.Background(), sandbox)
	require.NoError(t, err)
	assert.Equal(t, "pass", result)
	assert.Contains(t, details, "no recognized project type")
}

func TestCheckCI_NilSandbox(t *testing.T) {
	t.Parallel()
	s := &Service{logger: zerolog.Nop()}

	result, details, err := s.checkCI(context.Background(), nil)
	require.NoError(t, err)
	assert.Equal(t, "fail", result)
	assert.Contains(t, details, "no sandbox available")
}

func TestCheckCI_ExecError(t *testing.T) {
	t.Parallel()
	provider := newMockProvider()
	provider.files["go.mod"] = []byte("module test")
	provider.execFn = func(ctx context.Context, sb *agent.Sandbox, cmd string, stdout, stderr io.Writer) (int, error) {
		return 0, fmt.Errorf("sandbox connection lost")
	}

	s := &Service{provider: provider, logger: zerolog.Nop()}
	sandbox := &agent.Sandbox{ID: "test", WorkDir: "/workspace"}

	result, details, err := s.checkCI(context.Background(), sandbox)
	require.NoError(t, err)
	assert.Equal(t, "fail", result)
	assert.Contains(t, details, "sandbox connection lost")
}

func TestCheckCI_LongOutputTruncated(t *testing.T) {
	t.Parallel()
	provider := newMockProvider()
	provider.files["go.mod"] = []byte("module test")
	provider.execFn = func(ctx context.Context, sb *agent.Sandbox, cmd string, stdout, stderr io.Writer) (int, error) {
		longOutput := strings.Repeat("error line\n", 500)
		fmt.Fprint(stderr, longOutput)
		return 1, nil
	}

	s := &Service{provider: provider, logger: zerolog.Nop()}
	sandbox := &agent.Sandbox{ID: "test", WorkDir: "/workspace"}

	result, details, err := s.checkCI(context.Background(), sandbox)
	require.NoError(t, err)
	assert.Equal(t, "fail", result)
	assert.Contains(t, details, "truncated")
	assert.LessOrEqual(t, len(details), 2200)
}

// --- Helper function tests ---

func TestExtractAddedLines(t *testing.T) {
	t.Parallel()

	diff := `--- a/file.go
+++ b/file.go
@@ -1,3 +1,5 @@
 unchanged
-removed line
+added line 1
+added line 2
 context
`
	result := extractAddedLines(diff)
	assert.Contains(t, result, "added line 1")
	assert.Contains(t, result, "added line 2")
	assert.NotContains(t, result, "removed line")
	assert.NotContains(t, result, "b/file.go")
}

func TestCountDiffLines(t *testing.T) {
	t.Parallel()

	diff := `--- a/file.go
+++ b/file.go
@@ -1,5 +1,6 @@
 unchanged
-removed 1
-removed 2
+added 1
+added 2
+added 3
 context
`
	added, removed := countDiffLines(diff)
	assert.Equal(t, 3, added)
	assert.Equal(t, 2, removed)
}

// --- Full Pipeline Tests ---

func TestValidate_AllPass_EnqueuesPR(t *testing.T) {
	t.Parallel()

	provider := newMockProvider()
	provider.files["go.mod"] = []byte("module test")
	provider.execFn = func(ctx context.Context, sb *agent.Sandbox, cmd string, stdout, stderr io.Writer) (int, error) {
		fmt.Fprint(stdout, "ok\n")
		return 0, nil
	}

	stores := newMockStores()
	svc := NewService(stores.validations, stores.issues, stores.jobs, provider, zerolog.Nop())

	diff := generateDiff(5, 3)
	agentRun := &models.AgentRun{
		ID:      uuid.New(),
		IssueID: uuid.New(),
		OrgID:   uuid.New(),
		Diff:    &diff,
	}
	sandbox := &agent.Sandbox{ID: "test", WorkDir: "/workspace"}

	err := svc.Validate(context.Background(), agentRun, sandbox)
	require.NoError(t, err)

	assert.Equal(t, "passed", stores.validations.lastStatus)
	assert.Equal(t, "open_pr", stores.jobs.lastJobType)
	assert.Empty(t, stores.issues.lastStatus)
}

func TestValidate_SecurityFails_FailFast(t *testing.T) {
	t.Parallel()

	provider := newMockProvider()
	provider.files["go.mod"] = []byte("module test")

	stores := newMockStores()
	svc := NewService(stores.validations, stores.issues, stores.jobs, provider, zerolog.Nop())

	diff := `--- a/config.go
+++ b/config.go
@@ -1,3 +1,4 @@
 package config
+const awsKey = "AKIAIOSFODNN7EXAMPLE"
`
	agentRun := &models.AgentRun{
		ID:      uuid.New(),
		IssueID: uuid.New(),
		OrgID:   uuid.New(),
		Diff:    &diff,
	}
	sandbox := &agent.Sandbox{ID: "test", WorkDir: "/workspace"}

	err := svc.Validate(context.Background(), agentRun, sandbox)
	require.NoError(t, err)

	assert.Equal(t, "failed", stores.validations.lastStatus)
	// CI should NOT have been called (fail-fast)
	assert.Empty(t, provider.execCalls)
	assert.Equal(t, "triaged", stores.issues.lastStatus)
	assert.Empty(t, stores.jobs.lastJobType)
}

func TestValidate_SkippedChecksRecorded(t *testing.T) {
	t.Parallel()

	provider := newMockProvider()
	provider.files["go.mod"] = []byte("module test")
	provider.execFn = func(ctx context.Context, sb *agent.Sandbox, cmd string, stdout, stderr io.Writer) (int, error) {
		return 0, nil
	}

	stores := newMockStores()
	svc := NewService(stores.validations, stores.issues, stores.jobs, provider, zerolog.Nop())

	diff := generateDiff(5, 3)
	agentRun := &models.AgentRun{
		ID:      uuid.New(),
		IssueID: uuid.New(),
		OrgID:   uuid.New(),
		Diff:    &diff,
	}
	sandbox := &agent.Sandbox{ID: "test", WorkDir: "/workspace"}

	err := svc.Validate(context.Background(), agentRun, sandbox)
	require.NoError(t, err)

	skipped := stores.validations.checkResults
	assert.Equal(t, "skipped", skipped["direction_check"])
	assert.Equal(t, "skipped", skipped["correctness_check"])
	assert.Equal(t, "skipped", skipped["regression_test_check"])
}

func TestValidate_DiffTooLarge_Fails(t *testing.T) {
	t.Parallel()

	provider := newMockProvider()
	stores := newMockStores()
	svc := NewService(stores.validations, stores.issues, stores.jobs, provider, zerolog.Nop())

	diff := generateDiff(400, 150)
	agentRun := &models.AgentRun{
		ID:      uuid.New(),
		IssueID: uuid.New(),
		OrgID:   uuid.New(),
		Diff:    &diff,
	}
	sandbox := &agent.Sandbox{ID: "test", WorkDir: "/workspace"}

	err := svc.Validate(context.Background(), agentRun, sandbox)
	require.NoError(t, err)

	assert.Equal(t, "failed", stores.validations.lastStatus)
	assert.Equal(t, "triaged", stores.issues.lastStatus)
}

func TestValidate_NilDiff_PassesSecurity(t *testing.T) {
	t.Parallel()

	provider := newMockProvider()
	provider.files["go.mod"] = []byte("module test")
	provider.execFn = func(ctx context.Context, sb *agent.Sandbox, cmd string, stdout, stderr io.Writer) (int, error) {
		return 0, nil
	}

	stores := newMockStores()
	svc := NewService(stores.validations, stores.issues, stores.jobs, provider, zerolog.Nop())

	agentRun := &models.AgentRun{
		ID:      uuid.New(),
		IssueID: uuid.New(),
		OrgID:   uuid.New(),
		Diff:    nil,
	}
	sandbox := &agent.Sandbox{ID: "test", WorkDir: "/workspace"}

	err := svc.Validate(context.Background(), agentRun, sandbox)
	require.NoError(t, err)
	assert.Equal(t, "passed", stores.validations.lastStatus)
}

// --- Helpers ---

func generateDiff(added, removed int) string {
	var buf bytes.Buffer
	buf.WriteString("--- a/file.go\n+++ b/file.go\n@@ -1,1 +1,1 @@\n")
	for i := 0; i < removed; i++ {
		fmt.Fprintf(&buf, "-removed line %d\n", i)
	}
	for i := 0; i < added; i++ {
		fmt.Fprintf(&buf, "+added line %d\n", i)
	}
	return buf.String()
}
