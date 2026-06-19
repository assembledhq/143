package handlers

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/repoconfig"
	"github.com/assembledhq/143/internal/services/agent"
)

func TestEvalCandidateValidatorPreparesRepositoryBeforeCodeChecks(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	repoID := uuid.New()
	diff := "diff --git a/package.json b/package.json\n"
	provider := newFakeEvalCandidateSandboxProvider()
	provider.files["/workspace/"+repoconfig.ConfigPath] = []byte(`{
		"bootstrap": {
			"commands": ["npm ci"]
		}
	}`)
	validator := NewEvalCandidateValidator(
		fakeEvalCandidateRepoStore{repo: activeEvalCandidateRepo(orgID, repoID)},
		fakeEvalCandidateGitHubClient{},
		provider,
	)

	err := validator.ValidateEvalCandidate(context.Background(), orgID, repoID, evalCandidateWithCodeCheck(diff, "npm test"))

	require.NoError(t, err, "candidate validation should succeed when repo bootstrap and code check pass")
	require.Equal(t, []string{
		"git -C '/workspace' fetch --depth=1 origin 'base123' 'solution456' && git -C '/workspace' diff --binary 'base123' 'solution456'",
		"git -C '/workspace' checkout --detach 'solution456'",
		"cd '/workspace' && npm ci",
		"npm test",
	}, provider.execCalls, "candidate validation should run repo bootstrap after checkout and before deterministic code checks")
}

func TestEvalCandidateValidatorStopsWhenBootstrapCommandFails(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	repoID := uuid.New()
	diff := "diff --git a/package.json b/package.json\n"
	provider := newFakeEvalCandidateSandboxProvider()
	provider.files["/workspace/"+repoconfig.ConfigPath] = []byte(`{
		"bootstrap": {
			"commands": ["npm ci"]
		}
	}`)
	provider.failCommands["cd '/workspace' && npm ci"] = fakeEvalCandidateExecFailure{
		exitCode: 1,
		stderr:   "npm ERR! missing lockfile",
	}
	validator := NewEvalCandidateValidator(
		fakeEvalCandidateRepoStore{repo: activeEvalCandidateRepo(orgID, repoID)},
		fakeEvalCandidateGitHubClient{},
		provider,
	)

	err := validator.ValidateEvalCandidate(context.Background(), orgID, repoID, evalCandidateWithCodeCheck(diff, "npm test"))

	require.Error(t, err, "candidate validation should fail when repo bootstrap fails")
	require.Contains(t, err.Error(), "npm ci", "bootstrap failure should identify the failed setup command")
	require.Contains(t, err.Error(), "missing lockfile", "bootstrap failure should include stderr details")
	require.NotContains(t, provider.execCalls, "npm test", "candidate validation should not run code checks after bootstrap fails")
}

func activeEvalCandidateRepo(orgID, repoID uuid.UUID) models.Repository {
	return models.Repository{
		ID:             repoID,
		OrgID:          orgID,
		FullName:       "assembledhq/example",
		DefaultBranch:  "main",
		CloneURL:       "https://github.com/assembledhq/example.git",
		InstallationID: 123,
		Status:         models.RepositoryStatusActive,
	}
}

func evalCandidateWithCodeCheck(diff string, command string) models.EvalBootstrapCandidate {
	return models.EvalBootstrapCandidate{
		PRNumber:          42,
		PRTitle:           "Fix package install",
		BaseCommitSHA:     "base123",
		SolutionCommitSHA: "solution456",
		SolutionDiff:      diff,
		IssueDescription:  "Install dependencies before tests.",
		ScoringCriteria: []models.ScoringCriterion{
			{
				Name:         "tests pass",
				GraderType:   models.GraderTypeCodeCheck,
				GraderConfig: []byte(`{"command": "` + command + `"}`),
				Weight:       1,
				Required:     true,
			},
		},
		Complexity:       models.EvalComplexityModerate,
		FitnessScore:     0.9,
		FitnessReasoning: "Deterministic test command.",
	}
}

type fakeEvalCandidateRepoStore struct {
	repo models.Repository
	err  error
}

func (s fakeEvalCandidateRepoStore) GetByID(context.Context, uuid.UUID, uuid.UUID) (models.Repository, error) {
	if s.err != nil {
		return models.Repository{}, s.err
	}
	return s.repo, nil
}

type fakeEvalCandidateGitHubClient struct {
	token string
	err   error
}

func (c fakeEvalCandidateGitHubClient) GetInstallationToken(context.Context, int64) (string, error) {
	if c.err != nil {
		return "", c.err
	}
	if c.token == "" {
		return "ghs_test", nil
	}
	return c.token, nil
}

func (c fakeEvalCandidateGitHubClient) CommitExists(context.Context, string, string, string, string) error {
	return c.err
}

type fakeEvalCandidateExecFailure struct {
	exitCode int
	stderr   string
	err      error
}

type fakeEvalCandidateSandboxProvider struct {
	files        map[string][]byte
	execCalls    []string
	failCommands map[string]fakeEvalCandidateExecFailure
}

func newFakeEvalCandidateSandboxProvider() *fakeEvalCandidateSandboxProvider {
	return &fakeEvalCandidateSandboxProvider{
		files:        make(map[string][]byte),
		failCommands: make(map[string]fakeEvalCandidateExecFailure),
	}
}

func (p *fakeEvalCandidateSandboxProvider) Name() string {
	return "fake"
}

func (p *fakeEvalCandidateSandboxProvider) Create(context.Context, agent.SandboxConfig) (*agent.Sandbox, error) {
	return &agent.Sandbox{ID: "sandbox-1", Provider: "fake", WorkDir: "/workspace"}, nil
}

func (p *fakeEvalCandidateSandboxProvider) CloneRepo(context.Context, *agent.Sandbox, string, string, string) error {
	return nil
}

func (p *fakeEvalCandidateSandboxProvider) Exec(_ context.Context, _ *agent.Sandbox, cmd string, stdout, stderr io.Writer) (int, error) {
	p.execCalls = append(p.execCalls, cmd)
	if failure, ok := p.failCommands[cmd]; ok {
		if failure.stderr != "" {
			_, _ = io.WriteString(stderr, failure.stderr)
		}
		return failure.exitCode, failure.err
	}
	if strings.Contains(cmd, " diff --binary ") {
		_, _ = io.WriteString(stdout, "diff --git a/package.json b/package.json\n")
	}
	return 0, nil
}

func (p *fakeEvalCandidateSandboxProvider) ReadFile(_ context.Context, _ *agent.Sandbox, path string) ([]byte, error) {
	if data, ok := p.files[path]; ok {
		return append([]byte(nil), data...), nil
	}
	return nil, errors.New("file does not exist")
}

func (p *fakeEvalCandidateSandboxProvider) WriteFile(context.Context, *agent.Sandbox, string, []byte) error {
	return nil
}

func (p *fakeEvalCandidateSandboxProvider) Destroy(context.Context, *agent.Sandbox) error {
	return nil
}

func (p *fakeEvalCandidateSandboxProvider) IsAlive(context.Context, *agent.Sandbox) (bool, error) {
	return true, nil
}

func (p *fakeEvalCandidateSandboxProvider) ConnectionInfo(context.Context, *agent.Sandbox) (*agent.SandboxConnectionInfo, error) {
	return nil, nil
}

func (p *fakeEvalCandidateSandboxProvider) Snapshot(context.Context, *agent.Sandbox) (io.ReadCloser, error) {
	return io.NopCloser(bytes.NewReader(nil)), nil
}

func (p *fakeEvalCandidateSandboxProvider) Restore(context.Context, *agent.Sandbox, io.Reader) error {
	return nil
}

func (p *fakeEvalCandidateSandboxProvider) ExecStream(context.Context, *agent.Sandbox, string, func([]byte), io.Writer) (int, error) {
	return 0, nil
}
