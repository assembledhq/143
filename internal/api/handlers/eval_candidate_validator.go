package handlers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/agent"
)

type evalCandidateRepositoryStore interface {
	GetByID(ctx context.Context, orgID, repoID uuid.UUID) (models.Repository, error)
}

type evalCandidateGitHubClient interface {
	GetInstallationToken(ctx context.Context, installationID int64) (string, error)
	CommitExists(ctx context.Context, token, owner, repo, sha string) error
}

type EvalCandidateValidator struct {
	repos    evalCandidateRepositoryStore
	github   evalCandidateGitHubClient
	provider agent.SandboxProvider
}

func NewEvalCandidateValidator(repos evalCandidateRepositoryStore, github evalCandidateGitHubClient, provider agent.SandboxProvider) *EvalCandidateValidator {
	return &EvalCandidateValidator{repos: repos, github: github, provider: provider}
}

func (v *EvalCandidateValidator) ValidateEvalCandidate(ctx context.Context, orgID uuid.UUID, repoID uuid.UUID, candidate models.EvalBootstrapCandidate) error {
	if v == nil || v.repos == nil || v.github == nil || v.provider == nil {
		return errors.New("repository validation is not configured")
	}
	codeChecks, err := evalCandidateCodeChecks(candidate)
	if err != nil {
		return err
	}
	if len(codeChecks) == 0 {
		return errors.New("at least one deterministic code_check criterion is required")
	}
	repo, err := v.repos.GetByID(ctx, orgID, repoID)
	if err != nil {
		return fmt.Errorf("load repository: %w", err)
	}
	if !repo.IsActive() {
		return errors.New("repository is disconnected")
	}
	owner, name, ok := strings.Cut(repo.FullName, "/")
	if !ok || owner == "" || name == "" {
		return fmt.Errorf("repository full name %q is invalid", repo.FullName)
	}
	token, err := v.github.GetInstallationToken(ctx, repo.InstallationID)
	if err != nil {
		return fmt.Errorf("get GitHub installation token: %w", err)
	}
	baseSHA := strings.TrimSpace(candidate.BaseCommitSHA)
	solutionSHA := strings.TrimSpace(candidate.SolutionCommitSHA)
	if err := v.github.CommitExists(ctx, token, owner, name, baseSHA); err != nil {
		return fmt.Errorf("base_commit_sha is not reachable: %w", err)
	}
	if err := v.github.CommitExists(ctx, token, owner, name, solutionSHA); err != nil {
		return fmt.Errorf("solution_commit_sha is not reachable: %w", err)
	}

	cfg := agent.DefaultSandboxConfig()
	cfg.Purpose = "eval_candidate_validation"
	cfg.OrgID = orgID.String()
	cfg.Timeout = 10 * time.Minute
	sandbox, err := v.provider.Create(ctx, cfg)
	if err != nil {
		return fmt.Errorf("create validation sandbox: %w", err)
	}
	defer func() {
		destroyCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		_ = v.provider.Destroy(destroyCtx, sandbox)
	}()
	if err := v.provider.CloneRepo(ctx, sandbox, repo.CloneURL, repo.DefaultBranch, token); err != nil {
		return fmt.Errorf("clone repository: %w", err)
	}
	diff, err := v.gitDiff(ctx, sandbox, baseSHA, solutionSHA)
	if err != nil {
		return err
	}
	if normalizeEvalDiff(diff) != normalizeEvalDiff(candidate.SolutionDiff) {
		return errors.New("solution_diff does not match git diff between base_commit_sha and solution_commit_sha")
	}
	if err := v.checkoutCommit(ctx, sandbox, solutionSHA); err != nil {
		return err
	}
	if err := agent.PrepareSandboxRepository(ctx, v.provider, sandbox, sandbox.WorkDir, zerolog.Nop()); err != nil {
		return fmt.Errorf("prepare repository: %w", err)
	}
	for _, check := range codeChecks {
		if err := v.runCodeCheck(ctx, sandbox, check); err != nil {
			return err
		}
	}
	return nil
}

func evalCandidateCodeChecks(candidate models.EvalBootstrapCandidate) ([]models.CodeCheckConfig, error) {
	var checks []models.CodeCheckConfig
	for i, criterion := range candidate.ScoringCriteria {
		if criterion.GraderType != models.GraderTypeCodeCheck {
			continue
		}
		var cfg models.CodeCheckConfig
		if len(criterion.GraderConfig) > 0 {
			if err := json.Unmarshal(criterion.GraderConfig, &cfg); err != nil {
				return nil, fmt.Errorf("scoring_criteria[%d].grader_config is invalid: %w", i, err)
			}
		}
		cfg.Command = strings.TrimSpace(cfg.Command)
		if cfg.Command == "" {
			return nil, fmt.Errorf("scoring_criteria[%d].grader_config.command is required", i)
		}
		checks = append(checks, cfg)
	}
	return checks, nil
}

func (v *EvalCandidateValidator) gitDiff(ctx context.Context, sandbox *agent.Sandbox, baseSHA, solutionSHA string) (string, error) {
	cmd := fmt.Sprintf("git -C %s fetch --depth=1 origin %s %s && git -C %s diff --binary %s %s",
		shellQuoteLocal(sandbox.WorkDir),
		shellQuoteLocal(baseSHA),
		shellQuoteLocal(solutionSHA),
		shellQuoteLocal(sandbox.WorkDir),
		shellQuoteLocal(baseSHA),
		shellQuoteLocal(solutionSHA),
	)
	var stdout, stderr bytes.Buffer
	exitCode, err := v.provider.Exec(ctx, sandbox, cmd, &stdout, &stderr)
	if err != nil {
		return "", fmt.Errorf("compute solution diff: %w", err)
	}
	if exitCode != 0 {
		return "", fmt.Errorf("compute solution diff failed: %s", strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

func (v *EvalCandidateValidator) checkoutCommit(ctx context.Context, sandbox *agent.Sandbox, sha string) error {
	cmd := fmt.Sprintf("git -C %s checkout --detach %s", shellQuoteLocal(sandbox.WorkDir), shellQuoteLocal(sha))
	var stdout, stderr bytes.Buffer
	exitCode, err := v.provider.Exec(ctx, sandbox, cmd, &stdout, &stderr)
	if err != nil {
		return fmt.Errorf("checkout solution commit: %w", err)
	}
	if exitCode != 0 {
		return fmt.Errorf("checkout solution commit failed: %s", strings.TrimSpace(stderr.String()))
	}
	return nil
}

func (v *EvalCandidateValidator) runCodeCheck(ctx context.Context, sandbox *agent.Sandbox, check models.CodeCheckConfig) error {
	execCtx := ctx
	cancel := func() {}
	if check.TimeoutSeconds > 0 {
		execCtx, cancel = context.WithTimeout(ctx, time.Duration(check.TimeoutSeconds)*time.Second)
	}
	defer cancel()
	var stdout, stderr bytes.Buffer
	exitCode, err := v.provider.Exec(execCtx, sandbox, check.Command, &stdout, &stderr)
	if err != nil {
		return fmt.Errorf("dry-run code_check %q: %w", check.Command, err)
	}
	if exitCode != 0 {
		return fmt.Errorf("dry-run code_check %q failed with exit %d: %s", check.Command, exitCode, strings.TrimSpace(stderr.String()))
	}
	return nil
}

func normalizeEvalDiff(diff string) string {
	return strings.TrimSpace(strings.ReplaceAll(diff, "\r\n", "\n"))
}

func shellQuoteLocal(s string) string {
	if s == "" {
		return "''"
	}
	return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'"
}
