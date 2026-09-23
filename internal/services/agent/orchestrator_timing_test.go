package agent

import (
	"bytes"
	"context"
	"errors"
	"path"
	"testing"

	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/repoconfig"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func TestPrepareSandboxRepositoryTimingOnlyForCodeReview(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		origin   models.SessionOrigin
		wantLogs bool
	}{
		{name: "code review", origin: models.SessionOriginCodeReview, wantLogs: true},
		{name: "ordinary session", origin: models.SessionOriginManual, wantLogs: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var output bytes.Buffer
			logger := zerolog.New(&output)
			provider := &testInternalSandboxProvider{readFiles: map[string][]byte{
				path.Join("/workspace", repoconfig.ConfigPath): []byte(`{"bootstrap":{"commands":["true"]}}`),
			}}
			err := prepareSandboxRepositoryForSession(context.Background(), provider, &models.Session{Origin: tt.origin}, &Sandbox{ID: "sandbox-1", WorkDir: "/workspace"}, "/workspace", repositoryPreparationFull, logger)
			require.NoError(t, err, "repository preparation should run the declared bootstrap command")
			require.Equal(t, []string{"cd '/workspace' && true"}, provider.execCalls, "both session types should run their declared bootstrap command")
			if tt.wantLogs {
				require.Contains(t, output.String(), `"stage":"repository_preparation"`, "review preparation should emit timing boundaries")
				require.Contains(t, output.String(), `"stage":"repository_bootstrap"`, "review preparation should time bootstrap work")
				require.Contains(t, output.String(), `"outcome":"succeeded"`, "review preparation should record success")
			} else {
				require.NotContains(t, output.String(), `"stage":"repository_preparation"`, "ordinary sessions should not emit the review-specific outer boundary")
				require.NotContains(t, output.String(), `"stage":"repository_bootstrap"`, "ordinary sessions should not emit review-specific inner timing")
			}
		})
	}
}

func TestSandboxCapacityStageOutcome(t *testing.T) {
	t.Parallel()

	cancelledCtx, cancel := context.WithCancel(context.Background())
	cancel()
	tests := []struct {
		name    string
		ctx     context.Context
		err     error
		outcome string
	}{
		{name: "admitted", ctx: context.Background(), outcome: "succeeded"},
		{name: "host full", ctx: context.Background(), err: ErrSandboxCapacityReached, outcome: "waiting"},
		{name: "configuration failure", ctx: context.Background(), err: ErrSandboxCapacity, outcome: "failed"},
		{name: "counter failure", ctx: context.Background(), err: errors.Join(ErrSandboxCapacity, errors.New("database unavailable")), outcome: "failed"},
		{name: "cancelled while full", ctx: cancelledCtx, err: ErrSandboxCapacityReached, outcome: "cancelled"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.outcome, sandboxCapacityStageOutcome(tt.ctx, tt.err), "capacity timing should distinguish pressure from failures")
		})
	}
}

type testCodeReviewRoleResolver struct {
	role  models.CodeReviewAgentRole
	found bool
	err   error
}

func (r testCodeReviewRoleResolver) ResolveAgentRoleForThread(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) (models.CodeReviewAgentRole, bool, error) {
	return r.role, r.found, r.err
}

func TestReviewRepositoryPreparationSkipsOnlyVerifiedReviewTurns(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		origin    models.SessionOrigin
		revision  string
		threadID  *uuid.UUID
		resolver  CodeReviewRoleResolver
		wantMode  repositoryPreparationMode
		wantExecs []string
	}{
		{name: "reviewer cold", origin: models.SessionOriginCodeReview, revision: `{"kind":"code_review","github_pr_number":12,"head_sha":"abc"}`, threadID: testUUIDPtr(), resolver: testCodeReviewRoleResolver{role: models.CodeReviewAgentRoleReviewer, found: true}, wantMode: repositoryPreparationMinimalReview},
		{name: "Main synthesis", origin: models.SessionOriginCodeReview, revision: `{"kind":"code_review","github_pr_number":12,"head_sha":"abc"}`, threadID: testUUIDPtr(), resolver: testCodeReviewRoleResolver{role: models.CodeReviewAgentRoleOrchestrator, found: true}, wantMode: repositoryPreparationMinimalReview},
		{name: "review role missing", origin: models.SessionOriginCodeReview, revision: `{"kind":"code_review","github_pr_number":12,"head_sha":"abc"}`, threadID: testUUIDPtr(), resolver: testCodeReviewRoleResolver{}, wantMode: repositoryPreparationFull, wantExecs: []string{"cd '/workspace' && true"}},
		{name: "role resolver absent", origin: models.SessionOriginCodeReview, revision: `{"kind":"code_review","github_pr_number":12,"head_sha":"abc"}`, threadID: testUUIDPtr(), wantMode: repositoryPreparationFull, wantExecs: []string{"cd '/workspace' && true"}},
		{name: "invalid role", origin: models.SessionOriginCodeReview, revision: `{"kind":"code_review","github_pr_number":12,"head_sha":"abc"}`, threadID: testUUIDPtr(), resolver: testCodeReviewRoleResolver{role: "other", found: true}, wantMode: repositoryPreparationFull, wantExecs: []string{"cd '/workspace' && true"}},
		{name: "review role lookup failed", origin: models.SessionOriginCodeReview, revision: `{"kind":"code_review","github_pr_number":12,"head_sha":"abc"}`, threadID: testUUIDPtr(), resolver: testCodeReviewRoleResolver{err: errors.New("database unavailable")}, wantMode: repositoryPreparationFull, wantExecs: []string{"cd '/workspace' && true"}},
		{name: "invalid revision context", origin: models.SessionOriginCodeReview, revision: `{"kind":"code_review","github_pr_number":12}`, threadID: testUUIDPtr(), resolver: testCodeReviewRoleResolver{role: models.CodeReviewAgentRoleReviewer, found: true}, wantMode: repositoryPreparationFull, wantExecs: []string{"cd '/workspace' && true"}},
		{name: "thread missing", origin: models.SessionOriginCodeReview, revision: `{"kind":"code_review","github_pr_number":12,"head_sha":"abc"}`, resolver: testCodeReviewRoleResolver{role: models.CodeReviewAgentRoleReviewer, found: true}, wantMode: repositoryPreparationFull, wantExecs: []string{"cd '/workspace' && true"}},
		{name: "ordinary session", origin: models.SessionOriginManual, threadID: testUUIDPtr(), resolver: testCodeReviewRoleResolver{role: models.CodeReviewAgentRoleReviewer, found: true}, wantMode: repositoryPreparationFull, wantExecs: []string{"cd '/workspace' && true"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var output bytes.Buffer
			logger := zerolog.New(&output)
			provider := &testInternalSandboxProvider{readFiles: map[string][]byte{
				path.Join("/workspace", repoconfig.ConfigPath): []byte(`{"bootstrap":{"commands":["true"]}}`),
			}}
			session := &models.Session{ID: uuid.New(), OrgID: uuid.New(), Origin: tt.origin, RevisionContext: []byte(tt.revision)}
			orchestrator := &Orchestrator{codeReviewRoles: tt.resolver}
			mode, role := orchestrator.reviewRepositoryPreparationMode(context.Background(), session, tt.threadID, logger)
			require.Equal(t, tt.wantMode, mode, "only trusted built-in review roles should receive minimal preparation")
			if mode == repositoryPreparationMinimalReview {
				require.NotEmpty(t, role, "minimal review preparation should retain the verified role")
			} else {
				require.Empty(t, role, "unverified roles should not be attributed to full preparation")
			}
			err := prepareSandboxRepositoryForSession(context.Background(), provider, session, &Sandbox{ID: "sandbox-1", WorkDir: "/workspace"}, "/workspace", mode, logger)
			require.NoError(t, err, "eligible review setup should skip bootstrap without affecting full setup")
			require.Equal(t, tt.wantExecs, provider.execCalls, "only full preparation should run repo bootstrap commands")
			if mode == repositoryPreparationMinimalReview {
				require.Contains(t, output.String(), `"outcome":"skipped"`, "minimal review setup should record that dependency and bootstrap work was skipped")
			}
		})
	}
}

func TestMinimalReviewPreparationSkipsDependenciesAndBootstrap(t *testing.T) {
	t.Parallel()

	provider := &testInternalSandboxProvider{readFiles: map[string][]byte{
		path.Join("/workspace", repoconfig.ConfigPath): []byte(`{
			"dependencies": {"golangci-lint": "1.64.8"},
			"bootstrap": {"commands": ["npm ci"]}
		}`),
	}}
	session := &models.Session{Origin: models.SessionOriginCodeReview}
	err := prepareSandboxRepositoryForSession(context.Background(), provider, session, &Sandbox{ID: "sandbox-1", WorkDir: "/workspace"}, "/workspace", repositoryPreparationMinimalReview, zerolog.Nop())
	require.NoError(t, err, "minimal review preparation should succeed without repository-declared setup")
	require.Empty(t, provider.execCalls, "minimal review preparation should run neither dependency installers nor npm bootstrap")
}

func testUUIDPtr() *uuid.UUID {
	id := uuid.New()
	return &id
}

type delayedCodeReviewRoleResolver struct{ calls int }

func (r *delayedCodeReviewRoleResolver) ResolveAgentRoleForThread(context.Context, uuid.UUID, uuid.UUID, uuid.UUID) (models.CodeReviewAgentRole, bool, error) {
	r.calls++
	if r.calls == 1 {
		return "", false, nil
	}
	return models.CodeReviewAgentRoleOrchestrator, true, nil
}

func TestReviewRepositoryPreparationRefreshesRoleAfterSynthesisDispatch(t *testing.T) {
	t.Parallel()

	resolver := &delayedCodeReviewRoleResolver{}
	threadID := uuid.New()
	session := &models.Session{
		ID: uuid.New(), OrgID: uuid.New(), Origin: models.SessionOriginCodeReview,
		RevisionContext: []byte(`{"kind":"code_review","github_pr_number":12,"head_sha":"abc"}`),
	}
	orchestrator := &Orchestrator{codeReviewRoles: resolver}
	mode, _ := orchestrator.reviewRepositoryPreparationMode(context.Background(), session, &threadID, zerolog.Nop())
	require.Equal(t, repositoryPreparationFull, mode, "a synthesis role not yet persisted should initially retain full preparation")
	mode, role := orchestrator.refreshReviewPreparationMode(context.Background(), session, &threadID, []models.SessionMessage{{Source: models.SessionMessageSourceCodeReview}}, mode, zerolog.Nop())
	require.Equal(t, repositoryPreparationMinimalReview, mode, "platform review input should retry role lookup after synthesis result persistence")
	require.Equal(t, models.CodeReviewAgentRoleOrchestrator, role, "late role resolution should remain available for preparation timing logs")
	require.Equal(t, 2, resolver.calls, "refresh should read the persisted role once after the initial miss")
}

func TestReviewTurnPreparationModeRequiresPlatformReviewInput(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		mode     repositoryPreparationMode
		sources  []models.SessionMessageSource
		wantMode repositoryPreparationMode
	}{
		{name: "review input", mode: repositoryPreparationMinimalReview, sources: []models.SessionMessageSource{models.SessionMessageSourceCodeReview}, wantMode: repositoryPreparationMinimalReview},
		{name: "review repair input", mode: repositoryPreparationMinimalReview, sources: []models.SessionMessageSource{models.SessionMessageSourceCodeReview, models.SessionMessageSourceCodeReview}, wantMode: repositoryPreparationMinimalReview},
		{name: "human follow-up", mode: repositoryPreparationMinimalReview, sources: []models.SessionMessageSource{""}, wantMode: repositoryPreparationFull},
		{name: "mixed queued input", mode: repositoryPreparationMinimalReview, sources: []models.SessionMessageSource{models.SessionMessageSourceCodeReview, ""}, wantMode: repositoryPreparationFull},
		{name: "old untagged review input", mode: repositoryPreparationMinimalReview, sources: []models.SessionMessageSource{models.SessionMessageSourceAgentTool}, wantMode: repositoryPreparationFull},
		{name: "no input", mode: repositoryPreparationMinimalReview, wantMode: repositoryPreparationFull},
		{name: "unverified role", mode: repositoryPreparationFull, sources: []models.SessionMessageSource{models.SessionMessageSourceCodeReview}, wantMode: repositoryPreparationFull},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pending := make([]models.SessionMessage, 0, len(tt.sources))
			for _, source := range tt.sources {
				pending = append(pending, models.SessionMessage{Source: source})
			}
			require.Equal(t, tt.wantMode, reviewTurnPreparationMode(tt.mode, pending), "minimal setup should require only platform-owned review messages")
		})
	}
}

func TestNewOrchestratorWiresCodeReviewRoleResolver(t *testing.T) {
	t.Parallel()

	threadID := uuid.New()
	session := &models.Session{
		ID: uuid.New(), OrgID: uuid.New(), Origin: models.SessionOriginCodeReview,
		RevisionContext: []byte(`{"kind":"code_review","github_pr_number":12,"head_sha":"abc"}`),
	}
	orchestrator := NewOrchestrator(OrchestratorConfig{
		CodeReviewRoles: testCodeReviewRoleResolver{role: models.CodeReviewAgentRoleOrchestrator, found: true},
	})
	mode, role := orchestrator.reviewRepositoryPreparationMode(context.Background(), session, &threadID, zerolog.Nop())
	require.Equal(t, repositoryPreparationMinimalReview, mode, "constructor should pass persisted review role lookup to turn preparation")
	require.Equal(t, models.CodeReviewAgentRoleOrchestrator, role, "constructor should preserve the Main synthesis role")
}
