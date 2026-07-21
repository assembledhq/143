package worker

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/jobctx"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/prompts"
	"github.com/assembledhq/143/internal/services/codereview"
	ghservice "github.com/assembledhq/143/internal/services/github"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func TestSyncCodeReviewPullRequestStateClassifiesTransientGitHubFailures(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name               string
		status             int
		body               string
		header             http.Header
		retryable          bool
		fatal              bool
		expectedRetryAfter time.Duration
	}{
		{name: "retries service unavailable", status: http.StatusServiceUnavailable, retryable: true},
		{name: "retries rate limiting", status: http.StatusTooManyRequests, retryable: true},
		{
			name:               "retries forbidden secondary rate limit using server delay",
			status:             http.StatusForbidden,
			body:               `{"message":"You have exceeded a secondary rate limit"}`,
			header:             http.Header{"Retry-After": []string{"17"}},
			retryable:          true,
			expectedRetryAfter: 17 * time.Second,
		},
		{name: "does not retry forbidden permission failure", status: http.StatusForbidden, body: `{"message":"Resource not accessible by integration"}`, fatal: true},
		{name: "does not retry validation failure", status: http.StatusUnprocessableEntity, fatal: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			body := tt.body
			if body == "" {
				body = "upstream response"
			}
			upstreamErr := &ghservice.GitHubAPIError{
				Method:     http.MethodGet,
				Path:       "/repos/acme/repo/pulls/42",
				StatusCode: tt.status,
				Body:       []byte(body),
				Header:     tt.header,
			}
			services := &Services{PR: &stubPRService{
				syncPullRequestStateFn: func(context.Context, uuid.UUID, uuid.UUID) error {
					return upstreamErr
				},
			}}

			err := syncCodeReviewPullRequestState(context.Background(), services, zerolog.Nop(), runCodeReviewPayload{
				OrgID:         uuid.New(),
				PullRequestID: uuid.New(),
			})

			var retryErr *RetryableError
			require.Equal(t, tt.retryable, errors.As(err, &retryErr), "GitHub status should receive the expected retry classification")
			var fatalErr *FatalError
			require.Equal(t, tt.fatal, errors.As(err, &fatalErr), "non-transient GitHub status should receive the expected fatal classification")
			if tt.retryable {
				require.True(t, retryErr.ConsumeAttempt, "transient GitHub retries should consume attempts so exponential backoff increases")
				if tt.expectedRetryAfter > 0 {
					require.NotNil(t, retryErr.RetryAfter, "rate-limited response should preserve the upstream retry delay")
					require.Equal(t, tt.expectedRetryAfter, *retryErr.RetryAfter, "rate-limited response should use the upstream retry delay")
				} else {
					require.Nil(t, retryErr.RetryAfter, "transient GitHub retries without a hint should use exponential backoff")
				}
			}
		})
	}
}

func TestCodeReviewDeadLetterReconciliationFailsMetadata(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should initialize")
	defer mock.Close()

	orgID := uuid.New()
	sessionID := uuid.New()
	repositoryID := uuid.New()
	pullRequestID := uuid.New()
	policyID := uuid.New()
	metadataID := uuid.New()
	now := time.Date(2026, 7, 16, 22, 56, 4, 0, time.UTC)
	deadLetterErr := &ghservice.GitHubAPIError{
		Method:     http.MethodGet,
		Path:       "/repos/acme/repo/pulls/42",
		StatusCode: http.StatusServiceUnavailable,
		Body:       []byte("unavailable"),
	}
	reason := codeReviewDeadLetterReason(deadLetterErr)
	decision := models.CodeReviewDecisionBlocked
	acceptable := false

	mock.ExpectQuery("UPDATE code_review_session_metadata").
		WithArgs(pgx.NamedArgs{"org_id": orgID, "session_id": sessionID, "failure_reason": reason}).
		WillReturnRows(newCodeReviewMetadataRows().
			AddRow(metadataID, orgID, sessionID, repositoryID, pullRequestID, policyID,
				"base", "head", false, models.CodeReviewTriggerSourceTeamReviewer,
				models.CodeReviewSessionStatusFailed, &decision, &acceptable, false, nil,
				"output-key", nil, nil, nil, nil, &reason, &now, now))
	idleRow := workerSessionRow(sessionID, uuid.Nil, orgID, models.SessionStatusIdle, 0, nil, nil)
	setWorkerSessionColumn(idleRow, "origin", models.SessionOriginCodeReview)
	mock.ExpectQuery("(?s)SELECT .*FROM sessions").
		WithArgs(pgx.NamedArgs{"id": sessionID, "org_id": orgID}).
		WillReturnRows(pgxmock.NewRows(workerSessionColumns).AddRow(idleRow...))
	failedRow := workerSessionRow(sessionID, uuid.Nil, orgID, models.SessionStatusFailed, 0, nil, nil)
	setWorkerSessionColumn(failedRow, "origin", models.SessionOriginCodeReview)
	mock.ExpectQuery("UPDATE sessions SET status = @status, completed_at = now").
		WithArgs(workerAnyArgs(3)...).
		WillReturnRows(pgxmock.NewRows(workerSessionColumns).AddRow(failedRow...))
	mock.ExpectExec("UPDATE sessions[\\s\\S]+failure_explanation").
		WithArgs(workerAnyArgs(6)...).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))
	mock.ExpectQuery("(?s)FROM pull_requests.*WHERE id = @id AND org_id = @org_id").
		WithArgs(pgx.NamedArgs{"id": pullRequestID, "org_id": orgID}).
		WillReturnRows(pgxmock.NewRows(workerPullRequestColumns).
			AddRow(workerPullRequestRow(pullRequestID, sessionID, orgID, "acme/repo", "feature", now)...))
	mock.ExpectQuery("(?s)FROM repositories.*WHERE id = @id AND org_id = @org_id").
		WithArgs(pgx.NamedArgs{"id": repositoryID, "org_id": orgID}).
		WillReturnRows(workerRepositoryRows(models.Repository{
			ID: repositoryID, OrgID: orgID, IntegrationID: uuid.New(), GitHubID: 42,
			FullName: "acme/repo", DefaultBranch: "main", CloneURL: "https://github.com/acme/repo.git",
			InstallationID: 143, Status: models.RepositoryStatusActive, Settings: json.RawMessage(`{}`),
			CreatedAt: now, UpdatedAt: now,
		}))

	remover := &capturingCodeReviewSubmitter{}
	ctx := jobctx.WithDeadLetterHooks(context.Background())
	registerCodeReviewDeadLetterReconciliation(ctx, &Stores{
		CodeReviews:  db.NewCodeReviewStore(mock),
		Sessions:     db.NewSessionStore(mock),
		PullRequests: db.NewPullRequestStore(mock),
		Repositories: db.NewRepositoryStore(mock),
	}, &Services{CodeReviews: remover}, zerolog.Nop(), runCodeReviewPayload{
		OrgID:                  orgID,
		SessionID:              sessionID,
		RepositoryID:           repositoryID,
		PullRequestID:          pullRequestID,
		RequestedReviewerLogin: "143-code-reviewer",
	})
	jobctx.RunDeadLetterHooks(ctx, deadLetterErr)

	require.NoError(t, mock.ExpectationsWereMet(), "dead-letter hook should fail the review metadata and parent session")
	require.Equal(t, []codereview.RequestedReviewersRequest{{
		InstallationID: 143,
		Repository:     "acme/repo",
		PullNumber:     42,
		Reviewers:      []string{"143-code-reviewer"},
	}}, remover.removeRequests, "dead-letter hook should remove the pending reviewer so GitHub can emit a new request")
}

type capturingCodeReviewSubmitter struct {
	removeRequests []codereview.RequestedReviewersRequest
}

func (s *capturingCodeReviewSubmitter) SubmitReview(context.Context, codereview.SubmitReviewRequest) (codereview.SubmitReviewResult, error) {
	return codereview.SubmitReviewResult{}, nil
}

func (s *capturingCodeReviewSubmitter) RemoveRequestedReviewers(_ context.Context, req codereview.RequestedReviewersRequest) error {
	s.removeRequests = append(s.removeRequests, req)
	return nil
}

func TestCodeReviewInlineComments(t *testing.T) {
	t.Parallel()

	path := "internal/api/router.go"
	emptyPath := ""
	line := 42
	zeroLine := 0

	tests := []struct {
		name     string
		findings []models.CodeReviewFinding
		expected []codereview.SubmitReviewComment
	}{
		{
			name: "returns selected file-backed findings",
			findings: []models.CodeReviewFinding{
				{
					Path:      &path,
					StartLine: &line,
					Summary:   "summary",
					Body:      "body",
				},
			},
			expected: []codereview.SubmitReviewComment{
				{Path: path, Line: line, Body: "body"},
			},
		},
		{
			name: "falls back to summary when body is empty",
			findings: []models.CodeReviewFinding{
				{
					Path:      &path,
					StartLine: &line,
					Summary:   "summary",
				},
			},
			expected: []codereview.SubmitReviewComment{
				{Path: path, Line: line, Body: "summary"},
			},
		},
		{
			name: "skips findings without GitHub comment coordinates",
			findings: []models.CodeReviewFinding{
				{Path: nil, StartLine: &line, Summary: "summary"},
				{Path: &emptyPath, StartLine: &line, Summary: "summary"},
				{Path: &path, StartLine: nil, Summary: "summary"},
				{Path: &path, StartLine: &zeroLine, Summary: "summary"},
				{Path: &path, StartLine: &line},
			},
			expected: []codereview.SubmitReviewComment{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			actual := codeReviewInlineComments(tt.findings)
			require.Equal(t, tt.expected, actual, "codeReviewInlineComments should return deterministic GitHub comments")
		})
	}
}

func TestParseCodeReviewFindings(t *testing.T) {
	t.Parallel()

	output := `Looks mostly good.
::code-comment{title="[P1] Missing org filter" body="This subquery can read rows from another org when IDs collide." file="/workspace/internal/db/users.go" start=42 end=43 priority=1}
::code-comment{title="[P3] Broad note" body="No line means this should be ignored." file="internal/db/users.go"}`

	findings := parseCodeReviewFindings(output, []string{"internal/db/users.go"})

	require.Equal(t, []models.CodeReviewFinding{{
		DedupeKey:  "internal/db/users.go:42:43:missing org filter",
		Severity:   models.CodeReviewFindingSeverityHigh,
		Confidence: models.CodeReviewFindingConfidenceHigh,
		Path:       stringPtr("internal/db/users.go"),
		StartLine:  intPtr(42),
		EndLine:    intPtr(43),
		Summary:    "Missing org filter",
		Body:       "This subquery can read rows from another org when IDs collide.",
	}}, findings, "parser should persist concrete directive-backed findings with repo-relative paths")
}

func TestCodeReviewFindingsOnChangedLines(t *testing.T) {
	t.Parallel()

	path := "internal/db/users.go"
	changedLine := 11
	contextLine := 10
	otherPath := "internal/db/projects.go"
	findings := []models.CodeReviewFinding{
		{ID: uuid.New(), Path: &path, StartLine: &changedLine, Summary: "changed line"},
		{ID: uuid.New(), Path: &path, StartLine: &contextLine, Summary: "context line"},
		{ID: uuid.New(), Path: &otherPath, StartLine: &changedLine, Summary: "other file"},
	}
	files := []codereview.PullRequestFile{
		{
			Filename: path,
			Patch: `@@ -8,5 +8,6 @@ func load() {
 context
 context
 context
+added guard
 context
}`,
		},
	}

	filtered := codeReviewFindingsOnChangedLines(findings, files)

	require.Equal(t, []models.CodeReviewFinding{findings[0]}, filtered, "inline selection should keep only findings attached to added diff lines")
}

func TestCodeReviewDescriptionPassed(t *testing.T) {
	t.Parallel()

	policy := models.DefaultCodeReviewPolicyConfig()
	body := "Fix invoice rounding.\n\nTesting: go test ./...\n\nPreview: https://preview.example.com"

	tests := []struct {
		name    string
		body    *string
		files   []codereview.PullRequestFile
		passed  bool
		message string
	}{
		{
			name:   "passes applicable built-ins",
			body:   &body,
			files:  []codereview.PullRequestFile{{Filename: "frontend/src/App.tsx", Additions: 40, Deletions: 2}},
			passed: true,
		},
		{
			name:   "skips nontrivial and UI requirements for tiny backend change",
			body:   stringPtr("Fix typo in log message."),
			files:  []codereview.PullRequestFile{{Filename: "internal/api/router.go", Additions: 1}},
			passed: true,
		},
		{
			name:   "requires testing evidence for nontrivial change",
			body:   stringPtr("Fix invoice rounding with backend changes."),
			files:  []codereview.PullRequestFile{{Filename: "internal/api/router.go", Additions: 40}},
			passed: false,
		},
		{
			name:   "rejects explicit missing testing evidence",
			body:   stringPtr("Fix invoice rounding with backend changes.\n\nTesting: not run"),
			files:  []codereview.PullRequestFile{{Filename: "internal/api/router.go", Additions: 40}},
			passed: false,
		},
		{
			name:   "requires UI evidence for frontend change",
			body:   stringPtr("Fix chart tooltip.\n\nTesting: npm test"),
			files:  []codereview.PullRequestFile{{Filename: "frontend/src/Chart.tsx", Additions: 8}},
			passed: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			pr := models.PullRequest{Body: tt.body}
			require.Equal(t, tt.passed, codeReviewDescriptionPassed(policy, pr, tt.files), "description policy should respect applicability and built-in evidence checks")
		})
	}
}

func TestEvaluateCodeReviewDescriptionPolicyUsesCachedArtifact(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should initialize")
	defer mock.Close()

	orgID := uuid.New()
	sessionID := uuid.New()
	now := time.Date(2026, 6, 26, 12, 0, 0, 0, time.UTC)
	job := runCodeReviewPayload{
		OrgID:         orgID,
		SessionID:     sessionID,
		PolicyVersion: 3,
		HeadSHA:       "head-sha",
	}
	rootKey := "code-review-prompts/" + sessionID.String() + "/head-sha"
	artifactKey := rootKey + "/description-01-custom-requirement"
	passed := false
	artifactMetadata, err := json.Marshal(map[string]any{
		"requirement_key": "custom_requirement",
		"passed":          passed,
		"reason":          "cached failure",
		"policy_version":  3,
		"head_sha":        "head-sha",
	})
	require.NoError(t, err, "cached artifact metadata should marshal")

	mock.ExpectQuery("SELECT .+ FROM code_review_prompt_artifacts").
		WithArgs(pgx.NamedArgs{"org_id": orgID, "session_id": sessionID}).
		WillReturnRows(pgxmock.NewRows([]string{
			"id", "org_id", "session_id", "artifact_key", "role", "agent_provider",
			"content", "metadata", "created_at",
		}).AddRow(uuid.New(), orgID, sessionID, artifactKey, "description_policy", "platform_llm", "cached prompt", artifactMetadata, now))

	policyConfig := models.DefaultCodeReviewPolicyConfig()
	policyConfig.DescriptionPolicy.Requirements = []models.CodeReviewDescriptionRequirement{{
		Key:      "custom_requirement",
		Title:    "Custom requirement",
		Prompt:   "Require a custom statement.",
		Required: true,
		AppliesWhen: models.CodeReviewDescriptionApplicability{
			Kind: models.CodeReviewDescriptionApplicabilityAll,
		},
	}}
	policy := models.CodeReviewPolicyRecord{
		Version:            3,
		Enabled:            policyConfig.Enabled,
		ApprovalMode:       policyConfig.ApprovalMode,
		DescriptionPolicy:  policyConfig.DescriptionPolicy,
		RiskPolicy:         policyConfig.RiskPolicy,
		AgentRoster:        policyConfig.AgentRoster,
		InlineCommentLimit: policyConfig.InlineCommentLimit,
	}
	llm := &codeReviewDescriptionLLMStub{response: `{"passed":true,"reason":"fresh call"}`}

	evaluation, err := evaluateCodeReviewDescriptionPolicy(context.Background(), &Stores{
		CodeReviews: db.NewCodeReviewStore(mock),
	}, &Services{LLM: llm}, zerolog.Nop(), job, models.PullRequest{Title: "Fix invoices", Body: stringPtr("Body")}, policy, models.CodeReviewSessionMetadata{}, nil)

	require.NoError(t, err, "description evaluation should reuse cached prompt artifact")
	require.False(t, evaluation.Passed, "cached failed requirement should drive the evaluation result")
	require.Equal(t, 1, evaluation.FailedRequirementCount, "cached failed requirement should be counted once")
	require.Equal(t, []string{"Custom requirement: failed (cached failure)"}, evaluation.RequirementSummaries, "cached artifact should produce the same summary as a fresh evaluation")
	require.Equal(t, 0, llm.calls, "cached description artifact should avoid another LLM call")
	require.NoError(t, mock.ExpectationsWereMet(), "all database expectations should be met")
}

func TestCodeReviewDescriptionRequirementAppliesTypedRules(t *testing.T) {
	t.Parallel()

	files := []codereview.PullRequestFile{
		{Filename: "frontend/src/Chart.tsx", Additions: 12, Deletions: 1},
		{Filename: "internal/db/users_test.go", Additions: 4},
	}
	tests := []struct {
		name        string
		appliesWhen models.CodeReviewDescriptionApplicability
		expected    bool
	}{
		{
			name:        "matches frontend kind",
			appliesWhen: models.CodeReviewDescriptionApplicability{Kind: models.CodeReviewDescriptionApplicabilityFrontend},
			expected:    true,
		},
		{
			name:        "matches path patterns",
			appliesWhen: models.CodeReviewDescriptionApplicability{Kind: models.CodeReviewDescriptionApplicabilityPaths, PathPatterns: []string{"frontend/**"}},
			expected:    true,
		},
		{
			name:        "matches risk categories",
			appliesWhen: models.CodeReviewDescriptionApplicability{Kind: models.CodeReviewDescriptionApplicabilityCategories, Categories: []string{"frontend"}},
			expected:    true,
		},
		{
			name:        "matches changed test files",
			appliesWhen: models.CodeReviewDescriptionApplicability{Kind: models.CodeReviewDescriptionApplicabilityTests, RequireTestFilesChanged: true},
			expected:    true,
		},
		{
			name:        "does not match unrelated path patterns",
			appliesWhen: models.CodeReviewDescriptionApplicability{Kind: models.CodeReviewDescriptionApplicabilityPaths, PathPatterns: []string{"docs/**"}},
			expected:    false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			requirement := models.CodeReviewDescriptionRequirement{Key: "custom", Required: true, AppliesWhen: tt.appliesWhen}
			require.Equal(t, tt.expected, codeReviewDescriptionRequirementApplies(requirement, files), "typed applicability should be evaluated from changed files")
		})
	}
}

func TestCodeReviewReviewerMessageUsesNativeReviewCommand(t *testing.T) {
	t.Parallel()

	prompt := codeReviewReviewerPrompt(runCodeReviewPayload{}, models.PullRequest{}, models.DefaultCodeReviewPolicyConfig(), 0, "", nil)

	require.True(t, strings.HasPrefix(prompt, "/review"), "code review reviewer prompt should invoke the native review command")
	require.Contains(t, prompt, "Do NOT run test suites", "reviewer prompt should forbid running test suites")
	require.Contains(t, prompt, "Do NOT modify the workspace", "reviewer prompt should forbid workspace changes")
	require.Equal(t, prompt, codeReviewReviewerMessage(models.AgentTypeCodex, prompt), "Codex reviewer messages should invoke native /review with the review constraints")
	commands := codeReviewNativeReviewCommands(models.AgentTypeCodex, prompt)
	require.Len(t, commands, 1, "native reviewer command metadata should be persisted")
	require.Equal(t, strings.TrimSpace(strings.TrimPrefix(prompt, "/review")), commands[0].Arguments, "native reviewer command should carry the review constraints as arguments")
	require.Equal(t, prompt, codeReviewReviewerMessage(models.AgentTypeOpenCode, prompt), "agents without a native /review command should receive the plain prompt")
	require.Empty(t, codeReviewNativeReviewCommands(models.AgentTypeOpenCode, prompt), "agents without a native /review command should not persist command metadata")
}

func TestCodeReviewReviewerPromptIncludesPullRequestTarget(t *testing.T) {
	t.Parallel()

	baseSHA := "1111111111111111111111111111111111111111"
	pr := models.PullRequest{
		GitHubRepo:     "assembledhq/example",
		GitHubPRNumber: 53873,
		GitHubPRURL:    "https://github.com/assembledhq/example/pull/53873",
		BaseSHA:        &baseSHA,
	}
	job := runCodeReviewPayload{HeadSHA: "db848bf3c98e34c3c26d842b4e9b2ff1913dc34f"}
	files := []codereview.PullRequestFile{{Filename: "gocode/timeutils/interval.go"}, {Filename: "gocode/timeutils/interval_test.go"}}

	prompt := codeReviewReviewerPrompt(job, pr, models.DefaultCodeReviewPolicyConfig(), 3, "", files)

	require.True(t, strings.HasPrefix(prompt, "/review https://github.com/assembledhq/example/pull/53873"), "native /review invocation should carry the PR URL as its argument")
	require.Contains(t, prompt, "<review_target>", "reviewer prompt should include the review target block")
	require.Contains(t, prompt, "Repository: assembledhq/example", "reviewer prompt should identify the repository")
	require.Contains(t, prompt, "Pull request: #53873", "reviewer prompt should identify the PR number")
	require.Contains(t, prompt, "Base SHA: "+baseSHA, "reviewer prompt should pin the base SHA")
	require.Contains(t, prompt, "Head SHA: "+job.HeadSHA, "reviewer prompt should pin the head SHA")
	require.Contains(t, prompt, "git diff $(git merge-base "+baseSHA+" "+job.HeadSHA+") "+job.HeadSHA, "reviewer prompt should spell out the merge-base diff command")
	require.Contains(t, prompt, "git fetch origin "+job.HeadSHA, "reviewer prompt should tell the reviewer how to fetch a missing head SHA")
	require.Contains(t, prompt, "git fetch origin pull/53873/head", "reviewer prompt should offer the PR ref as a fetch fallback")
	require.Contains(t, prompt, "git checkout --detach "+job.HeadSHA, "reviewer prompt should permit a detached checkout of the head SHA")
	require.Contains(t, prompt, "substitute `origin/HEAD`", "reviewer prompt should offer a fallback when the base SHA is unreachable")
	require.Contains(t, prompt, "report the mismatch", "reviewer prompt should require reporting a workspace/head mismatch")
	require.Contains(t, prompt, "- gocode/timeutils/interval.go", "reviewer prompt should list changed files")

	explicitBase := "2222222222222222222222222222222222222222"
	withExplicitBase := codeReviewReviewerPrompt(job, pr, models.DefaultCodeReviewPolicyConfig(), 3, explicitBase, files)
	require.Contains(t, withExplicitBase, "Base SHA: "+explicitBase, "captured metadata base SHA should win over the PR record")

	commands := codeReviewNativeReviewCommands(models.AgentTypeClaudeCode, prompt)
	require.Len(t, commands, 1, "native reviewer command metadata should be persisted")
	require.True(t, strings.HasPrefix(commands[0].Arguments, pr.GitHubPRURL), "native /review arguments should start with the PR URL")

	withoutTarget := codeReviewReviewerPrompt(runCodeReviewPayload{}, models.PullRequest{}, models.DefaultCodeReviewPolicyConfig(), 0, "", nil)
	require.NotContains(t, withoutTarget, "<review_target>", "prompt without a head SHA should omit the target block")
	require.True(t, strings.HasPrefix(withoutTarget, "/review"), "prompt without a target should still begin with /review")
}

func TestCodeReviewEveryReviewerAgentPreservesNativeReviewPrefix(t *testing.T) {
	t.Parallel()
	agents := []models.AgentType{models.AgentTypeCodex, models.AgentTypeClaudeCode, models.AgentTypeAmp, models.AgentTypePi, models.AgentTypeOpenCode}
	for _, agentType := range agents {
		t.Run(string(agentType), func(t *testing.T) {
			t.Parallel()
			cfg := models.DefaultCodeReviewPolicyConfig()
			cfg.ReviewInstructions = "Review organization-specific invariants."
			prompt := codeReviewReviewerPrompt(runCodeReviewPayload{}, models.PullRequest{}, cfg, 2, "", nil)
			message := codeReviewReviewerMessage(agentType, prompt)
			require.Equal(t, "/review", strings.Fields(message)[0], "every configured or fallback reviewer invocation should begin with /review")
			require.Contains(t, message, cfg.ReviewInstructions, "every reviewer path should receive captured review instructions")
		})
	}
}

func TestCodeReviewPromptPolicyRouting(t *testing.T) {
	t.Parallel()
	cfg := models.DefaultCodeReviewPolicyConfig()
	cfg.ReviewInstructions = "Focus on tenant isolation; {{ .Title }} must remain literal."
	cfg.AutomatedApprovalPolicy = "Escalate every architectural change."
	reviewer := codeReviewReviewerPrompt(runCodeReviewPayload{}, models.PullRequest{}, cfg, 7, "", nil)
	require.True(t, strings.HasPrefix(reviewer, "/review"), "reviewer invocation should preserve /review as its first token")
	require.Contains(t, reviewer, cfg.ReviewInstructions, "reviewer should receive organization review instructions")
	require.NotContains(t, reviewer, cfg.AutomatedApprovalPolicy, "reviewer should not receive automated approval policy")
	require.Contains(t, reviewer, "{{ .Title }}", "organization prompt data should not be recursively rendered")

	empty := cfg
	empty.ReviewInstructions = ""
	require.NotContains(t, codeReviewReviewerPrompt(runCodeReviewPayload{}, models.PullRequest{}, empty, 7, "", nil), "<organization_review_instructions>", "empty instructions should omit the organization section")
}

func TestCodeReviewCapturedPolicyVersionsRenderDistinctPromptArtifacts(t *testing.T) {
	t.Parallel()
	first := models.DefaultCodeReviewPolicyConfig()
	first.ApprovalMode = models.CodeReviewApprovalModeApproveAcceptable
	first.ReviewInstructions = "captured review instructions version one"
	first.AutomatedApprovalPolicy = "captured approval policy version one"
	second := first
	second.ReviewInstructions = "new active review instructions version two"
	second.AutomatedApprovalPolicy = "new active approval policy version two"
	firstRecord := codeReviewPolicyRecordForTest(first)
	firstRecord.Version = 1
	secondRecord := codeReviewPolicyRecordForTest(second)
	secondRecord.Version = 2

	firstReviewerArtifact := codeReviewReviewerPrompt(runCodeReviewPayload{}, models.PullRequest{}, firstRecord.Config(), firstRecord.Version, "", nil)
	secondReviewerArtifact := codeReviewReviewerPrompt(runCodeReviewPayload{}, models.PullRequest{}, secondRecord.Config(), secondRecord.Version, "", nil)
	require.Contains(t, firstReviewerArtifact, first.ReviewInstructions, "captured reviewer artifact should use its historic policy record")
	require.NotContains(t, firstReviewerArtifact, second.ReviewInstructions, "captured reviewer artifact should not use the latest active policy")
	require.NotEqual(t, firstReviewerArtifact, secondReviewerArtifact, "different captured policy versions should render different reviewer artifacts")

	firstOrchestratorArtifact := prompts.CodeReviewOrchestratorPrompt(prompts.CodeReviewOrchestratorPromptData{
		PolicyVersion: firstRecord.Version, ReviewInstructions: first.ReviewInstructions, AutomatedApprovalPolicy: first.AutomatedApprovalPolicy, UseAutomatedApprovalPolicy: true,
	})
	secondOrchestratorArtifact := prompts.CodeReviewOrchestratorPrompt(prompts.CodeReviewOrchestratorPromptData{
		PolicyVersion: secondRecord.Version, ReviewInstructions: second.ReviewInstructions, AutomatedApprovalPolicy: second.AutomatedApprovalPolicy, UseAutomatedApprovalPolicy: true,
	})
	require.Contains(t, firstOrchestratorArtifact, first.AutomatedApprovalPolicy, "captured orchestrator artifact should use its historic approval policy")
	require.NotContains(t, firstOrchestratorArtifact, second.AutomatedApprovalPolicy, "captured orchestrator artifact should not use the latest active policy")
	require.NotEqual(t, firstOrchestratorArtifact, secondOrchestratorArtifact, "different captured policy versions should render different orchestrator artifacts")
}

func TestHarvestCodeReviewReviewerResultsIgnoresReadOnlyWorkspaceChanges(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should initialize")
	defer mock.Close()

	orgID := uuid.New()
	sessionID := uuid.New()
	threadID := uuid.New()
	resultID := uuid.New()
	findingID := uuid.New()
	now := time.Now().UTC()
	rawDiff := "diff --git a/internal/db/users.go b/internal/db/users.go"
	rawReview := `The review found one issue.
::code-comment{title="[P1] Missing org filter" body="This query can read another org's rows." file="/workspace/internal/db/users.go" start=42 priority=1}`
	state := marshalCodeReviewReviewerStructuredResult(codeReviewReviewerStructuredResult{
		ReviewerKey:   codeReviewReviewerKey(0, models.AgentTypeCodex),
		ReviewerIndex: 0,
		ThreadID:      threadID.String(),
		ReadOnly:      true,
	})
	updatedState := marshalCodeReviewReviewerStructuredResult(codeReviewReviewerStructuredResult{
		ReviewerKey:       codeReviewReviewerKey(0, models.AgentTypeCodex),
		ReviewerIndex:     0,
		ThreadID:          threadID.String(),
		FindingCount:      1,
		CostCents:         0.25,
		ReadOnly:          true,
		ReadOnlyViolation: true,
		CompletedAt:       now.Format(time.RFC3339),
	})

	mock.ExpectQuery("(?s)SELECT .*FROM code_review_agent_results").
		WithArgs(pgx.NamedArgs{"org_id": orgID, "session_id": sessionID}).
		WillReturnRows(newCodeReviewAgentResultRows().
			AddRow(resultID, orgID, sessionID, "codex", nil, models.CodeReviewAgentRoleReviewer, models.CodeReviewAgentResultStatusRunning, nil, state, now))
	mock.ExpectQuery("(?s)SELECT .*FROM session_threads").
		WithArgs(pgx.NamedArgs{"id": threadID, "org_id": orgID}).
		WillReturnRows(newSessionThreadRows().
			AddRow(threadID, sessionID, orgID, models.AgentTypeCodex, nil,
				"Code review: codex", nil, []string{"internal/db/users.go"}, models.ThreadStatusCompleted,
				nil, 1, &now, nil, &rawDiff, nil, nil,
				&now, &now, now, models.ThreadCreatedBySourceSystem, nil, nil,
				nil, 0.25, 0, nil, "", nil, "", "", json.RawMessage(`[]`),
				models.ThreadExecutionModeReview, models.ThreadFilesystemModeReadOnly))
	mock.ExpectQuery("(?s)SELECT .*FROM session_messages").
		WithArgs(pgx.NamedArgs{"org_id": orgID, "thread_id": threadID}).
		WillReturnRows(newSessionMessageRows().
			AddRow(int64(1), sessionID, orgID, &threadID, nil, 1, models.MessageRoleAssistant, rawReview, nil, nil, nil, nil, "", now))
	mock.ExpectQuery("INSERT INTO code_review_findings").
		WithArgs(
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
		).
		WillReturnRows(newCodeReviewFindingRows().
			AddRow(findingID, orgID, sessionID, &resultID, "internal/db/users.go:42:42:missing org filter",
				models.CodeReviewFindingSeverityHigh, models.CodeReviewFindingConfidenceHigh,
				stringPtr("internal/db/users.go"), intPtr(42), intPtr(42), "Missing org filter",
				"This query can read another org's rows.", false, nil, now))
	mock.ExpectQuery("UPDATE code_review_agent_results").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(newCodeReviewAgentResultRows().
			AddRow(resultID, orgID, sessionID, "codex", nil, models.CodeReviewAgentRoleReviewer, models.CodeReviewAgentResultStatusCompleted, &rawReview, updatedState, now))

	cfg := models.DefaultCodeReviewPolicyConfig()
	policy := codeReviewPolicyRecordForTest(cfg)
	stores := &Stores{
		CodeReviews:     db.NewCodeReviewStore(mock),
		SessionThreads:  db.NewSessionThreadStore(mock),
		SessionMessages: db.NewSessionMessageStore(mock),
	}
	err = harvestCodeReviewReviewerResults(context.Background(), stores, nil, zerolog.Nop(), runCodeReviewPayload{
		OrgID:     orgID,
		SessionID: sessionID,
	}, policy, models.CodeReviewSessionMetadata{CreatedAt: now}, []codereview.PullRequestFile{{Filename: "internal/db/users.go"}})

	require.NoError(t, err, "read-only workspace changes should not invalidate a completed reviewer output")
	require.NoError(t, mock.ExpectationsWereMet(), "reviewer harvest should parse output and mark the result completed")
}

func TestHarvestCodeReviewReviewerResultsCompletesIdleReadOnlyViolationWithoutAssistantMessage(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should initialize")
	defer mock.Close()

	orgID := uuid.New()
	sessionID := uuid.New()
	threadID := uuid.New()
	resultID := uuid.New()
	now := time.Now().UTC()
	rawDiff := "diff --git a/internal/db/users.go b/internal/db/users.go"
	failure := "read-only review thread produced workspace changes; automatic revert failed"
	state := marshalCodeReviewReviewerStructuredResult(codeReviewReviewerStructuredResult{
		ReviewerKey:   codeReviewReviewerKey(0, models.AgentTypeCodex),
		ReviewerIndex: 0,
		ThreadID:      threadID.String(),
		ReadOnly:      true,
	})

	mock.ExpectQuery("(?s)SELECT .*FROM code_review_agent_results").
		WithArgs(pgx.NamedArgs{"org_id": orgID, "session_id": sessionID}).
		WillReturnRows(newCodeReviewAgentResultRows().
			AddRow(resultID, orgID, sessionID, "codex", nil, models.CodeReviewAgentRoleReviewer, models.CodeReviewAgentResultStatusRunning, nil, state, now))
	mock.ExpectQuery("(?s)SELECT .*FROM session_threads").
		WithArgs(pgx.NamedArgs{"id": threadID, "org_id": orgID}).
		WillReturnRows(newSessionThreadRows().
			AddRow(threadID, sessionID, orgID, models.AgentTypeCodex, nil,
				"Code review: codex", nil, []string{"internal/db/users.go"}, models.ThreadStatusIdle,
				nil, 1, &now, nil, &rawDiff, &failure, stringPtr("read_only_violation"),
				&now, &now, now, models.ThreadCreatedBySourceSystem, nil, nil,
				nil, 0.25, 0, nil, "", nil, "", "", json.RawMessage(`[]`),
				models.ThreadExecutionModeReview, models.ThreadFilesystemModeReadOnly))
	mock.ExpectQuery("(?s)SELECT .*FROM session_messages").
		WithArgs(pgx.NamedArgs{"org_id": orgID, "thread_id": threadID}).
		WillReturnRows(newSessionMessageRows().
			AddRow(int64(1), sessionID, orgID, &threadID, nil, 1, models.MessageRoleUser, "review this PR", nil, nil, nil, nil, "", now))
	mock.ExpectQuery("UPDATE code_review_agent_results").
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
		WillReturnRows(newCodeReviewAgentResultRows().
			AddRow(resultID, orgID, sessionID, "codex", nil, models.CodeReviewAgentRoleReviewer, models.CodeReviewAgentResultStatusCompleted, &failure, state, now))

	cfg := models.DefaultCodeReviewPolicyConfig()
	policy := codeReviewPolicyRecordForTest(cfg)
	stores := &Stores{
		CodeReviews:     db.NewCodeReviewStore(mock),
		SessionThreads:  db.NewSessionThreadStore(mock),
		SessionMessages: db.NewSessionMessageStore(mock),
	}
	err = harvestCodeReviewReviewerResults(context.Background(), stores, nil, zerolog.Nop(), runCodeReviewPayload{
		OrgID:     orgID,
		SessionID: sessionID,
	}, policy, models.CodeReviewSessionMetadata{CreatedAt: now}, []codereview.PullRequestFile{{Filename: "internal/db/users.go"}})

	require.NoError(t, err, "idle read-only violations without assistant output should not fail reviewer results")
	require.NoError(t, mock.ExpectationsWereMet(), "reviewer harvest should keep code review moving after a read-only violation")
}

func TestHarvestCodeReviewReviewerResultsClassifiesFailedThreadOutput(t *testing.T) {
	t.Parallel()

	authError := "no credentials configured for Claude Code: connect a Claude subscription or add an Anthropic API key"
	tests := []struct {
		name            string
		failure         string
		failureCategory string
		assistantOutput string
		expectedStatus  models.CodeReviewAgentResultStatus
	}{
		{
			name:            "keeps auth error failed",
			failure:         authError,
			failureCategory: "claude_code_auth_expired",
			assistantOutput: authError,
			expectedStatus:  models.CodeReviewAgentResultStatusFailed,
		},
		{
			name:            "keeps a completed review after bookkeeping failure",
			failure:         "update interactive turn result: connection reset",
			failureCategory: "turn_persistence_failed",
			assistantOutput: "No actionable issues found.",
			expectedStatus:  models.CodeReviewAgentResultStatusCompleted,
		},
		{
			name:            "rejects assistant text from an operational failure",
			failure:         "sandbox capacity stayed full until the retry window expired",
			failureCategory: "sandbox_capacity",
			assistantOutput: "The sandbox is unavailable; retry this review later.",
			expectedStatus:  models.CodeReviewAgentResultStatusFailed,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "pgxmock pool should initialize")
			defer mock.Close()

			orgID := uuid.New()
			sessionID := uuid.New()
			threadID := uuid.New()
			resultID := uuid.New()
			now := time.Now().UTC()
			state := marshalCodeReviewReviewerStructuredResult(codeReviewReviewerStructuredResult{
				ReviewerKey:   codeReviewReviewerKey(1, models.AgentTypeClaudeCode),
				ReviewerIndex: 1,
				ThreadID:      threadID.String(),
				ReadOnly:      true,
			})

			mock.ExpectQuery("(?s)SELECT .*FROM code_review_agent_results").
				WithArgs(pgx.NamedArgs{"org_id": orgID, "session_id": sessionID}).
				WillReturnRows(newCodeReviewAgentResultRows().
					AddRow(resultID, orgID, sessionID, "claude_code", nil, models.CodeReviewAgentRoleReviewer, models.CodeReviewAgentResultStatusRunning, nil, state, now))
			mock.ExpectQuery("(?s)SELECT .*FROM session_threads").
				WithArgs(pgx.NamedArgs{"id": threadID, "org_id": orgID}).
				WillReturnRows(newSessionThreadRows().
					AddRow(threadID, sessionID, orgID, models.AgentTypeClaudeCode, nil,
						"Code review: claude_code", nil, []string{"internal/db/users.go"}, models.ThreadStatusFailed,
						nil, 1, &now, nil, nil, &tt.failure, &tt.failureCategory,
						&now, &now, now, models.ThreadCreatedBySourceSystem, nil, nil,
						nil, 0.0, 0, nil, "", nil, "", "", json.RawMessage(`[]`),
						models.ThreadExecutionModeReview, models.ThreadFilesystemModeReadOnly))
			mock.ExpectQuery("(?s)SELECT .*FROM session_messages").
				WithArgs(pgx.NamedArgs{"org_id": orgID, "thread_id": threadID}).
				WillReturnRows(newSessionMessageRows().
					AddRow(int64(1), sessionID, orgID, &threadID, nil, 1, models.MessageRoleAssistant, tt.assistantOutput, nil, nil, nil, nil, "", now))
			mock.ExpectQuery("UPDATE code_review_agent_results").
				WithArgs(tt.expectedStatus, &tt.assistantOutput, pgxmock.AnyArg(), orgID, resultID).
				WillReturnRows(newCodeReviewAgentResultRows().
					AddRow(resultID, orgID, sessionID, "claude_code", nil, models.CodeReviewAgentRoleReviewer, tt.expectedStatus, &tt.assistantOutput, state, now))

			cfg := models.DefaultCodeReviewPolicyConfig()
			policy := codeReviewPolicyRecordForTest(cfg)
			stores := &Stores{
				CodeReviews:     db.NewCodeReviewStore(mock),
				SessionThreads:  db.NewSessionThreadStore(mock),
				SessionMessages: db.NewSessionMessageStore(mock),
			}
			err = harvestCodeReviewReviewerResults(context.Background(), stores, nil, zerolog.Nop(), runCodeReviewPayload{
				OrgID:     orgID,
				SessionID: sessionID,
			}, policy, models.CodeReviewSessionMetadata{CreatedAt: now}, []codereview.PullRequestFile{{Filename: "internal/db/users.go"}})

			require.NoError(t, err, "failed reviewer output should be classified without stopping the harvest")
			require.NoError(t, mock.ExpectationsWereMet(), "failed reviewer output should use valid completed reviews but reject operational error text")
		})
	}
}

func TestFailCodeReviewWithoutReviewerOutputFailsMetadata(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should initialize")
	defer mock.Close()

	orgID := uuid.New()
	sessionID := uuid.New()
	repositoryID := uuid.New()
	pullRequestID := uuid.New()
	policyID := uuid.New()
	metadataID := uuid.New()
	now := time.Now().UTC()
	reason := "no code review reviewer produced usable output: Codex failed, Claude Code failed"
	decision := models.CodeReviewDecisionBlocked
	acceptable := false
	mock.ExpectQuery("UPDATE code_review_session_metadata").
		WithArgs(pgx.NamedArgs{"org_id": orgID, "session_id": sessionID, "failure_reason": reason}).
		WillReturnRows(newCodeReviewMetadataRows().
			AddRow(metadataID, orgID, sessionID, repositoryID, pullRequestID, policyID,
				"base", "head", false, models.CodeReviewTriggerSourceTeamReviewer,
				models.CodeReviewSessionStatusFailed, &decision, &acceptable, false, nil,
				"output-key", nil, nil, nil, nil, &reason, &now, now))

	err = failCodeReviewWithoutReviewerOutput(context.Background(), &Stores{
		CodeReviews: db.NewCodeReviewStore(mock),
	}, nil, zerolog.Nop(), runCodeReviewPayload{
		OrgID:     orgID,
		SessionID: sessionID,
	}, models.PullRequest{}, []models.CodeReviewAgentResult{
		{Role: models.CodeReviewAgentRoleReviewer, AgentProvider: "codex", Status: models.CodeReviewAgentResultStatusFailed},
		{Role: models.CodeReviewAgentRoleReviewer, AgentProvider: "claude_code", Status: models.CodeReviewAgentResultStatusFailed},
	})

	require.NoError(t, err, "missing reviewer output should terminate the code review cleanly")
	require.NoError(t, mock.ExpectationsWereMet(), "missing reviewer output should fail the code review metadata")
}

func TestRunCodeReviewHandlerReconcilesTerminalFailedMetadata(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should initialize")
	defer mock.Close()

	orgID := uuid.New()
	sessionID := uuid.New()
	repositoryID := uuid.New()
	pullRequestID := uuid.New()
	policyID := uuid.New()
	metadataID := uuid.New()
	now := time.Now().UTC()
	reason := "no code review reviewer produced usable output: claude_code failed"

	mock.ExpectQuery("UPDATE code_review_session_metadata").
		WithArgs(pgx.NamedArgs{"org_id": orgID, "session_id": sessionID}).
		WillReturnRows(newCodeReviewMetadataRows())
	mock.ExpectQuery("(?s)SELECT .*FROM code_review_session_metadata").
		WithArgs(pgx.NamedArgs{"org_id": orgID, "session_id": sessionID}).
		WillReturnRows(newCodeReviewMetadataRows().
			AddRow(metadataID, orgID, sessionID, repositoryID, pullRequestID, policyID,
				"base", "head", false, models.CodeReviewTriggerSourceTeamReviewer,
				models.CodeReviewSessionStatusFailed, nil, nil, false, nil,
				"output-key", nil, nil, nil, nil, &reason, &now, now))

	pendingRow := workerSessionRow(sessionID, uuid.Nil, orgID, models.SessionStatusPending, 0, nil, nil)
	setWorkerSessionColumn(pendingRow, "origin", models.SessionOriginCodeReview)
	mock.ExpectQuery("(?s)SELECT .*FROM sessions").
		WithArgs(pgx.NamedArgs{"id": sessionID, "org_id": orgID}).
		WillReturnRows(pgxmock.NewRows(workerSessionColumns).AddRow(pendingRow...))
	failedRow := workerSessionRow(sessionID, uuid.Nil, orgID, models.SessionStatusFailed, 0, nil, nil)
	setWorkerSessionColumn(failedRow, "origin", models.SessionOriginCodeReview)
	mock.ExpectQuery("UPDATE sessions SET status = @status, completed_at = now").
		WithArgs(workerAnyArgs(3)...).
		WillReturnRows(pgxmock.NewRows(workerSessionColumns).AddRow(failedRow...))
	mock.ExpectExec("UPDATE sessions[\\s\\S]+failure_explanation").
		WithArgs(workerAnyArgs(6)...).
		WillReturnResult(pgxmock.NewResult("UPDATE", 1))

	job := runCodeReviewPayload{
		OrgID:         orgID,
		SessionID:     sessionID,
		RepositoryID:  repositoryID,
		PullRequestID: pullRequestID,
		PolicyID:      policyID,
	}
	payload, err := json.Marshal(job)
	require.NoError(t, err, "code review job payload should marshal")
	err = newRunCodeReviewHandler(&Stores{
		CodeReviews: db.NewCodeReviewStore(mock),
		Sessions:    db.NewSessionStore(mock),
	}, nil, zerolog.Nop())(context.Background(), "run_code_review", payload)

	require.NoError(t, err, "terminal failed metadata should reconcile a parent left non-terminal by a prior transient failure")
	require.NoError(t, mock.ExpectationsWereMet(), "terminal failed metadata should retry parent failure reconciliation")
}

func TestStopCodeReviewIfParentSessionCancelledCancelsMetadata(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should initialize")
	defer mock.Close()

	orgID := uuid.New()
	sessionID := uuid.New()
	repositoryID := uuid.New()
	pullRequestID := uuid.New()
	policyID := uuid.New()
	metadataID := uuid.New()
	now := time.Now().UTC()
	reason := "parent code review session was cancelled"
	sessionRow := workerSessionRow(sessionID, uuid.Nil, orgID, models.SessionStatusCancelled, 0, nil, nil)
	setWorkerSessionColumn(sessionRow, "origin", models.SessionOriginCodeReview)

	mock.ExpectQuery("(?s)SELECT .*FROM sessions").
		WithArgs(pgx.NamedArgs{"id": sessionID, "org_id": orgID}).
		WillReturnRows(pgxmock.NewRows(workerSessionColumns).AddRow(sessionRow...))
	mock.ExpectQuery("UPDATE code_review_session_metadata").
		WithArgs(pgx.NamedArgs{"org_id": orgID, "session_id": sessionID, "failure_reason": reason}).
		WillReturnRows(newCodeReviewMetadataRows().
			AddRow(metadataID, orgID, sessionID, repositoryID, pullRequestID, policyID,
				"base", "head", false, models.CodeReviewTriggerSourceTeamReviewer,
				models.CodeReviewSessionStatusCancelled, nil, nil, false, nil,
				"output-key", nil, nil, nil, nil, &reason, &now, now))

	stopped, err := stopCodeReviewIfParentSessionCancelled(context.Background(), &Stores{
		Sessions:    db.NewSessionStore(mock),
		CodeReviews: db.NewCodeReviewStore(mock),
	}, nil, zerolog.Nop(), runCodeReviewPayload{
		OrgID:     orgID,
		SessionID: sessionID,
	}, models.PullRequest{})

	require.NoError(t, err, "parent cancellation should stop the code review cleanly")
	require.True(t, stopped, "parent cancellation should prevent any later orchestrator or GitHub submission")
	require.NoError(t, mock.ExpectationsWereMet(), "parent cancellation should persist cancelled code review metadata")
}

func TestReconcileCodeReviewSessionSuccess(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		status       models.SessionStatus
		origin       models.SessionOrigin
		stale        bool
		expectUpdate bool
	}{
		{name: "completes a failed code review parent after degraded success", status: models.SessionStatusFailed, origin: models.SessionOriginCodeReview, expectUpdate: true},
		{name: "preserves a failed code review parent when the review becomes stale", status: models.SessionStatusFailed, origin: models.SessionOriginCodeReview, stale: true, expectUpdate: false},
		{name: "preserves a cancelled code review parent", status: models.SessionStatusCancelled, origin: models.SessionOriginCodeReview, expectUpdate: false},
		{name: "preserves a failed non-code-review parent", status: models.SessionStatusFailed, origin: models.SessionOriginManual, expectUpdate: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			mock, err := pgxmock.NewPool()
			require.NoError(t, err, "pgxmock pool should initialize")
			defer mock.Close()

			orgID := uuid.New()
			sessionID := uuid.New()
			sessionRow := workerSessionRow(sessionID, uuid.Nil, orgID, tt.status, 0, nil, nil)
			setWorkerSessionColumn(sessionRow, "origin", tt.origin)
			mock.ExpectQuery("(?s)SELECT .*FROM sessions").
				WithArgs(pgx.NamedArgs{"id": sessionID, "org_id": orgID}).
				WillReturnRows(pgxmock.NewRows(workerSessionColumns).AddRow(sessionRow...))
			if tt.expectUpdate {
				completedRow := workerSessionRow(sessionID, uuid.Nil, orgID, models.SessionStatusCompleted, 0, nil, nil)
				setWorkerSessionColumn(completedRow, "origin", tt.origin)
				mock.ExpectQuery("UPDATE sessions SET status = @status, completed_at = now").
					WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
					WillReturnRows(pgxmock.NewRows(workerSessionColumns).AddRow(completedRow...))
			}

			stores := &Stores{Sessions: db.NewSessionStore(mock)}
			job := runCodeReviewPayload{OrgID: orgID, SessionID: sessionID}
			if tt.stale {
				reconcileCodeReviewSessionStale(context.Background(), stores, zerolog.Nop(), job)
			} else {
				reconcileCodeReviewSessionSuccess(context.Background(), stores, zerolog.Nop(), job)
			}

			require.NoError(t, mock.ExpectationsWereMet(), "review reconciliation should recover failed parents only after successful completion")
		})
	}
}

func TestHarvestCodeReviewOrchestratorResultPersistsFindings(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should initialize")
	defer mock.Close()

	orgID := uuid.New()
	sessionID := uuid.New()
	threadID := uuid.New()
	resultID := uuid.New()
	findingID := uuid.New()
	now := time.Now().UTC()
	rawReview := `Synthesis found one issue.
::code-comment{title="[P2] Missing regression coverage" body="The parser behavior changed without a direct regression test." file="internal/worker/code_review_handler.go" start=42 priority=2}

` + "```json" + `
{"scope_mismatch":false,"unresolved_uncertainty":false,"reviewer_disagreement":false,"prompt_injection_detected":false,"summary":"Adds review handling.","review_summary":"The parser change is focused, but it needs direct regression coverage before approval.","risk_notes":["tests needed"]}
` + "```"
	state := marshalCodeReviewOrchestratorStructuredResult(codeReviewOrchestratorStructuredResult{
		ThreadID: threadID.String(),
	})

	mock.ExpectQuery("(?s)SELECT .*FROM code_review_agent_results").
		WithArgs(pgx.NamedArgs{"org_id": orgID, "session_id": sessionID}).
		WillReturnRows(newCodeReviewAgentResultRows().
			AddRow(resultID, orgID, sessionID, "codex", nil, models.CodeReviewAgentRoleOrchestrator, models.CodeReviewAgentResultStatusRunning, nil, state, now))
	mock.ExpectQuery("(?s)SELECT .*FROM session_threads").
		WithArgs(pgx.NamedArgs{"id": threadID, "org_id": orgID}).
		WillReturnRows(newSessionThreadRows().
			AddRow(threadID, sessionID, orgID, models.AgentTypeCodex, nil,
				"Main", nil, []string{"internal/worker/code_review_handler.go"}, models.ThreadStatusCompleted,
				nil, 1, &now, nil, nil, nil, nil,
				&now, &now, now, models.ThreadCreatedBySourceSystem, nil, nil,
				nil, 0.25, 0, nil, "", nil, "", "", json.RawMessage(`[]`),
				models.ThreadExecutionModeWork, models.ThreadFilesystemModeReadWrite))
	mock.ExpectQuery("(?s)SELECT .*FROM session_messages").
		WithArgs(pgx.NamedArgs{"org_id": orgID, "thread_id": threadID}).
		WillReturnRows(newSessionMessageRows().
			AddRow(int64(1), sessionID, orgID, &threadID, nil, 1, models.MessageRoleAssistant, rawReview, nil, nil, nil, nil, "", now))
	mock.ExpectQuery("INSERT INTO code_review_findings").
		WithArgs(
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
			pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(),
		).
		WillReturnRows(newCodeReviewFindingRows().
			AddRow(findingID, orgID, sessionID, &resultID, "internal/worker/code_review_handler.go:42:42:missing regression coverage",
				models.CodeReviewFindingSeverityMedium, models.CodeReviewFindingConfidenceHigh,
				stringPtr("internal/worker/code_review_handler.go"), intPtr(42), intPtr(42), "Missing regression coverage",
				"The parser behavior changed without a direct regression test.", false, nil, now))
	mock.ExpectQuery("UPDATE code_review_agent_results").
		WithArgs(models.CodeReviewAgentResultStatusCompleted, &rawReview, validatedOrchestratorResultArg{}, orgID, resultID).
		WillReturnRows(newCodeReviewAgentResultRows().
			AddRow(resultID, orgID, sessionID, "codex", nil, models.CodeReviewAgentRoleOrchestrator, models.CodeReviewAgentResultStatusCompleted, &rawReview, state, now))

	policy := codeReviewPolicyRecordForTest(models.DefaultCodeReviewPolicyConfig())
	stores := &Stores{
		CodeReviews:     db.NewCodeReviewStore(mock),
		SessionThreads:  db.NewSessionThreadStore(mock),
		SessionMessages: db.NewSessionMessageStore(mock),
	}
	err = harvestCodeReviewOrchestratorResult(context.Background(), stores, nil, zerolog.Nop(), runCodeReviewPayload{
		OrgID:     orgID,
		SessionID: sessionID,
	}, policy, models.CodeReviewSessionMetadata{CreatedAt: now}, []codereview.PullRequestFile{{Filename: "internal/worker/code_review_handler.go"}})

	require.NoError(t, err, "orchestrator harvest should persist directive-backed findings")
	require.NoError(t, mock.ExpectationsWereMet(), "orchestrator harvest should parse findings and mark the result completed")
}

type validatedOrchestratorResultArg struct{}

func (validatedOrchestratorResultArg) Match(value any) bool {
	var raw json.RawMessage
	switch typed := value.(type) {
	case json.RawMessage:
		raw = typed
	case []byte:
		raw = typed
	case string:
		raw = json.RawMessage(typed)
	default:
		return false
	}
	state, ok := parseCodeReviewOrchestratorStructuredResult(raw)
	return ok && state.SynthesisValidated && codeReviewOrchestratorSynthesisUsable(state.Synthesis)
}

func TestParseCodeReviewOrchestratorSynthesis(t *testing.T) {
	t.Parallel()

	valid := codeReviewOrchestratorSynthesis{
		Summary:                 "The change is safe to approve.",
		ReviewSummary:           "The change is focused, and the review evidence supports approval.",
		RiskNotes:               []string{},
		ScopeMismatch:           false,
		UnresolvedUncertainty:   false,
		ReviewerDisagreement:    false,
		PromptInjectionDetected: false,
	}
	tests := []struct {
		name      string
		raw       string
		expected  codeReviewOrchestratorSynthesis
		expectErr bool
	}{
		{
			name:     "accepts complete fenced synthesis",
			raw:      "Review complete.\n```json\n{\"scope_mismatch\":false,\"unresolved_uncertainty\":false,\"reviewer_disagreement\":false,\"prompt_injection_detected\":false,\"summary\":\"The change is safe to approve.\",\"review_summary\":\"The change is focused, and the review evidence supports approval.\",\"risk_notes\":[]}\n```",
			expected: valid,
		},
		{
			name:      "rejects prose without JSON",
			raw:       "I reviewed the code and found no issues.",
			expectErr: true,
		},
		{
			name:      "rejects missing required fields",
			raw:       `{"summary":"The change is safe to approve."}`,
			expectErr: true,
		},
		{
			name:      "rejects empty summary",
			raw:       `{"scope_mismatch":false,"unresolved_uncertainty":false,"reviewer_disagreement":false,"prompt_injection_detected":false,"summary":" ","review_summary":"The review evidence is otherwise complete.","risk_notes":[]}`,
			expectErr: true,
		},
		{
			name:      "rejects missing reviewer-facing summary",
			raw:       `{"scope_mismatch":false,"unresolved_uncertainty":false,"reviewer_disagreement":false,"prompt_injection_detected":false,"summary":"The change is safe to approve.","risk_notes":[]}`,
			expectErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			actual, err := parseCodeReviewOrchestratorSynthesis(tt.raw)
			if tt.expectErr {
				require.Error(t, err, "malformed orchestrator synthesis should be rejected")
				return
			}
			require.NoError(t, err, "complete orchestrator synthesis should parse")
			require.Equal(t, tt.expected, actual, "parser should preserve every synthesis field")
		})
	}
}

func TestCodeReviewOrchestratorReviewSummary(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		synthesis codeReviewOrchestratorSynthesis
		expected  string
	}{
		{
			name: "prefers reviewer-facing generated summary",
			synthesis: codeReviewOrchestratorSynthesis{
				Summary:       "Changes the parser.",
				ReviewSummary: "The parser change is focused, but it needs direct regression coverage before approval.",
			},
			expected: "The parser change is focused, but it needs direct regression coverage before approval.",
		},
		{
			name:      "falls back to legacy generated summary",
			synthesis: codeReviewOrchestratorSynthesis{Summary: "Changes the parser."},
			expected:  "Changes the parser.",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.expected, codeReviewOrchestratorReviewSummary(tt.synthesis), "final review should use the best available LLM-generated summary")
		})
	}
}

func TestHarvestCodeReviewOrchestratorResultRejectsMalformedSynthesis(t *testing.T) {
	t.Parallel()

	mock, err := pgxmock.NewPool()
	require.NoError(t, err, "pgxmock pool should initialize")
	defer mock.Close()

	orgID := uuid.New()
	sessionID := uuid.New()
	threadID := uuid.New()
	resultID := uuid.New()
	now := time.Now().UTC()
	rawReview := "I reviewed the code and found no issues."
	state := marshalCodeReviewOrchestratorStructuredResult(codeReviewOrchestratorStructuredResult{
		ThreadID: threadID.String(),
	})

	mock.ExpectQuery("(?s)SELECT .*FROM code_review_agent_results").
		WithArgs(pgx.NamedArgs{"org_id": orgID, "session_id": sessionID}).
		WillReturnRows(newCodeReviewAgentResultRows().
			AddRow(resultID, orgID, sessionID, "codex", nil, models.CodeReviewAgentRoleOrchestrator, models.CodeReviewAgentResultStatusRunning, nil, state, now))
	mock.ExpectQuery("(?s)SELECT .*FROM session_threads").
		WithArgs(pgx.NamedArgs{"id": threadID, "org_id": orgID}).
		WillReturnRows(newSessionThreadRows().
			AddRow(threadID, sessionID, orgID, models.AgentTypeCodex, nil,
				"Main", nil, []string{"internal/worker/code_review_handler.go"}, models.ThreadStatusCompleted,
				nil, 1, &now, nil, nil, nil, nil,
				&now, &now, now, models.ThreadCreatedBySourceSystem, nil, nil,
				nil, 0.25, 0, nil, "", nil, "", "", json.RawMessage(`[]`),
				models.ThreadExecutionModeWork, models.ThreadFilesystemModeReadWrite))
	mock.ExpectQuery("(?s)SELECT .*FROM session_messages").
		WithArgs(pgx.NamedArgs{"org_id": orgID, "thread_id": threadID}).
		WillReturnRows(newSessionMessageRows().
			AddRow(int64(1), sessionID, orgID, &threadID, nil, 1, models.MessageRoleAssistant, rawReview, nil, nil, nil, nil, "", now))
	mock.ExpectQuery("UPDATE code_review_agent_results").
		WithArgs(models.CodeReviewAgentResultStatusFailed, &rawReview, pgxmock.AnyArg(), orgID, resultID).
		WillReturnRows(newCodeReviewAgentResultRows().
			AddRow(resultID, orgID, sessionID, "codex", nil, models.CodeReviewAgentRoleOrchestrator, models.CodeReviewAgentResultStatusFailed, &rawReview, state, now))

	policy := codeReviewPolicyRecordForTest(models.DefaultCodeReviewPolicyConfig())
	stores := &Stores{
		CodeReviews:     db.NewCodeReviewStore(mock),
		SessionThreads:  db.NewSessionThreadStore(mock),
		SessionMessages: db.NewSessionMessageStore(mock),
	}
	err = harvestCodeReviewOrchestratorResult(context.Background(), stores, nil, zerolog.Nop(), runCodeReviewPayload{
		OrgID:     orgID,
		SessionID: sessionID,
	}, policy, models.CodeReviewSessionMetadata{CreatedAt: now}, []codereview.PullRequestFile{{Filename: "internal/worker/code_review_handler.go"}})

	require.NoError(t, err, "orchestrator harvest should record malformed synthesis as an agent failure")
	require.NoError(t, mock.ExpectationsWereMet(), "malformed synthesis should be retained as raw output and never marked completed")
}

type codeReviewDescriptionLLMStub struct {
	calls    int
	response string
}

func (s *codeReviewDescriptionLLMStub) Complete(context.Context, string, string) (string, error) {
	s.calls++
	return s.response, nil
}

func TestCodeReviewReviewerExecutionFailed(t *testing.T) {
	t.Parallel()

	policy := models.DefaultCodeReviewPolicyConfig()
	codexState := marshalCodeReviewReviewerStructuredResult(codeReviewReviewerStructuredResult{ReviewerKey: codeReviewReviewerKey(0, models.AgentTypeCodex)})
	claudeState := marshalCodeReviewReviewerStructuredResult(codeReviewReviewerStructuredResult{ReviewerKey: codeReviewReviewerKey(1, models.AgentTypeClaudeCode)})
	noOutputState := marshalCodeReviewReviewerStructuredResult(codeReviewReviewerStructuredResult{
		ReviewerKey:       codeReviewReviewerKey(0, models.AgentTypeCodex),
		ReadOnlyViolation: true,
		Error:             "reviewer produced no assistant output",
	})
	tests := []struct {
		name     string
		results  []models.CodeReviewAgentResult
		expected bool
	}{
		{
			name: "continues with one successful reviewer",
			results: []models.CodeReviewAgentResult{
				{Role: models.CodeReviewAgentRoleReviewer, AgentProvider: "codex", Status: models.CodeReviewAgentResultStatusCompleted, StructuredResult: codexState},
				{Role: models.CodeReviewAgentRoleReviewer, AgentProvider: "claude_code", Status: models.CodeReviewAgentResultStatusFailed, StructuredResult: claudeState},
			},
			expected: false,
		},
		{
			name: "fails when all reviewers fail",
			results: []models.CodeReviewAgentResult{
				{Role: models.CodeReviewAgentRoleReviewer, AgentProvider: "codex", Status: models.CodeReviewAgentResultStatusFailed, StructuredResult: codexState},
				{Role: models.CodeReviewAgentRoleReviewer, AgentProvider: "claude_code", Status: models.CodeReviewAgentResultStatusTimedOut, StructuredResult: claudeState},
			},
			expected: true,
		},
		{
			name: "waits while a reviewer is still running",
			results: []models.CodeReviewAgentResult{
				{Role: models.CodeReviewAgentRoleReviewer, AgentProvider: "codex", Status: models.CodeReviewAgentResultStatusRunning, StructuredResult: codexState},
				{Role: models.CodeReviewAgentRoleReviewer, AgentProvider: "claude_code", Status: models.CodeReviewAgentResultStatusFailed, StructuredResult: claudeState},
			},
			expected: false,
		},
		{
			name: "fails when completed output is unusable",
			results: []models.CodeReviewAgentResult{
				{Role: models.CodeReviewAgentRoleReviewer, AgentProvider: "codex", Status: models.CodeReviewAgentResultStatusCompleted, StructuredResult: noOutputState},
				{Role: models.CodeReviewAgentRoleReviewer, AgentProvider: "claude_code", Status: models.CodeReviewAgentResultStatusFailed, StructuredResult: claudeState},
			},
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.expected, codeReviewReviewerExecutionFailed(policy, tt.results), "review execution should fail only when every configured reviewer is terminal without usable output")
		})
	}
}

func TestCodeReviewRequiredReviewerQuorum(t *testing.T) {
	t.Parallel()

	policy := models.DefaultCodeReviewPolicyConfig()
	unavailableState := marshalCodeReviewReviewerStructuredResult(codeReviewReviewerStructuredResult{
		ReviewerKey: codeReviewReviewerKey(1, models.AgentTypeClaudeCode),
		Unavailable: true,
		Error:       "reviewer skipped because claude_code authentication is not configured",
	})
	failedState := marshalCodeReviewReviewerStructuredResult(codeReviewReviewerStructuredResult{
		ReviewerKey: codeReviewReviewerKey(1, models.AgentTypeClaudeCode),
		Error:       "reviewer thread did not complete successfully",
	})

	tests := []struct {
		name     string
		results  []models.CodeReviewAgentResult
		expected int
	}{
		{
			name:     "keeps the configured quorum before results exist",
			results:  nil,
			expected: 2,
		},
		{
			name: "keeps the configured quorum when every reviewer could run",
			results: []models.CodeReviewAgentResult{
				{Role: models.CodeReviewAgentRoleReviewer, AgentProvider: "codex", Status: models.CodeReviewAgentResultStatusCompleted},
				{Role: models.CodeReviewAgentRoleReviewer, AgentProvider: "claude_code", Status: models.CodeReviewAgentResultStatusCompleted},
			},
			expected: 2,
		},
		{
			name: "keeps the configured quorum when a reviewer ran and failed",
			results: []models.CodeReviewAgentResult{
				{Role: models.CodeReviewAgentRoleReviewer, AgentProvider: "codex", Status: models.CodeReviewAgentResultStatusCompleted},
				{Role: models.CodeReviewAgentRoleReviewer, AgentProvider: "claude_code", Status: models.CodeReviewAgentResultStatusFailed, StructuredResult: failedState},
			},
			expected: 2,
		},
		{
			name: "clamps the quorum when a reviewer credential is unavailable",
			results: []models.CodeReviewAgentResult{
				{Role: models.CodeReviewAgentRoleReviewer, AgentProvider: "codex", Status: models.CodeReviewAgentResultStatusCompleted},
				{Role: models.CodeReviewAgentRoleReviewer, AgentProvider: "claude_code", Status: models.CodeReviewAgentResultStatusFailed, StructuredResult: unavailableState},
			},
			expected: 1,
		},
		{
			name: "never drops the quorum below one reviewer",
			results: []models.CodeReviewAgentResult{
				{Role: models.CodeReviewAgentRoleReviewer, AgentProvider: "codex", Status: models.CodeReviewAgentResultStatusFailed, StructuredResult: marshalCodeReviewReviewerStructuredResult(codeReviewReviewerStructuredResult{
					ReviewerKey: codeReviewReviewerKey(0, models.AgentTypeCodex),
					Unavailable: true,
				})},
				{Role: models.CodeReviewAgentRoleReviewer, AgentProvider: "claude_code", Status: models.CodeReviewAgentResultStatusFailed, StructuredResult: unavailableState},
			},
			expected: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.expected, codeReviewRequiredReviewerQuorum(policy, tt.results), "required quorum should clamp to reviewers that could actually run")
		})
	}
}

func TestCodeReviewReviewerAgentModel(t *testing.T) {
	t.Parallel()

	cfg := models.DefaultCodeReviewPolicyConfig()
	cfg.AgentRoster.Reviewers = []models.AgentType{models.AgentTypeCodex, models.AgentTypeClaudeCode}
	cfg.AgentRoster.ReviewerModels = []string{models.DefaultCodexModel, "  "}

	require.Equal(t, models.DefaultCodexModel, *codeReviewReviewerAgentModel(cfg, 0, models.AgentTypeCodex),
		"non-empty configured model should win")
	require.Equal(t, models.DefaultClaudeCodeModel, *codeReviewReviewerAgentModel(cfg, 1, models.AgentTypeClaudeCode),
		"whitespace-only configured model should fall back to the per-agent default")
	require.Equal(t, models.DefaultClaudeCodeModel, *codeReviewReviewerAgentModel(cfg, 5, models.AgentTypeClaudeCode),
		"out-of-range index should fall back to the per-agent default")

	empty := models.DefaultCodeReviewPolicyConfig()
	empty.AgentRoster.ReviewerModels = nil
	require.Equal(t, models.DefaultCodexModel, *codeReviewReviewerAgentModel(empty, 0, models.AgentTypeCodex),
		"missing reviewer_models should fall back to the per-agent default")
}

type codeReviewAgentAvailabilityStub struct {
	available      map[models.AgentType]bool
	availableModel map[models.AgentType]map[string]bool
	err            error
}

func (s codeReviewAgentAvailabilityStub) IsAgentAvailable(_ context.Context, _ uuid.UUID, _ *uuid.UUID, agentType models.AgentType, model string) (bool, error) {
	if s.err != nil {
		return false, s.err
	}
	if modelsForAgent, ok := s.availableModel[agentType]; ok {
		return modelsForAgent[model], nil
	}
	return s.available[agentType], nil
}

func TestResolveCodeReviewReviewerAvailability(t *testing.T) {
	t.Parallel()

	cfg := models.DefaultCodeReviewPolicyConfig()
	tests := []struct {
		name      string
		services  *Services
		expected  []codeReviewReviewerSelection
		expectErr bool
	}{
		{
			name: "uses only Codex when Claude Code is not authenticated",
			services: &Services{CodingAgents: codeReviewAgentAvailabilityStub{available: map[models.AgentType]bool{
				models.AgentTypeCodex: true,
			}}},
			expected: []codeReviewReviewerSelection{
				{Index: 0, AgentType: models.AgentTypeCodex, Available: true},
				{Index: 1, AgentType: models.AgentTypeClaudeCode, Available: false},
			},
		},
		{
			name: "uses only Claude Code when Codex is not authenticated",
			services: &Services{CodingAgents: codeReviewAgentAvailabilityStub{available: map[models.AgentType]bool{
				models.AgentTypeClaudeCode: true,
			}}},
			expected: []codeReviewReviewerSelection{
				{Index: 0, AgentType: models.AgentTypeCodex, Available: false},
				{Index: 1, AgentType: models.AgentTypeClaudeCode, Available: true},
			},
		},
		{
			name: "keeps both authenticated reviewers",
			services: &Services{CodingAgents: codeReviewAgentAvailabilityStub{available: map[models.AgentType]bool{
				models.AgentTypeCodex:      true,
				models.AgentTypeClaudeCode: true,
			}}},
			expected: []codeReviewReviewerSelection{
				{Index: 0, AgentType: models.AgentTypeCodex, Available: true},
				{Index: 1, AgentType: models.AgentTypeClaudeCode, Available: true},
			},
		},
		{
			name: "preserves the configured roster without an availability service",
			expected: []codeReviewReviewerSelection{
				{Index: 0, AgentType: models.AgentTypeCodex, Available: true},
				{Index: 1, AgentType: models.AgentTypeClaudeCode, Available: true},
			},
		},
		{
			name:      "propagates availability lookup errors",
			services:  &Services{CodingAgents: codeReviewAgentAvailabilityStub{err: errors.New("resolver failed")}},
			expectErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			actual, err := resolveCodeReviewReviewerAvailability(context.Background(), tt.services, uuid.New(), cfg)
			if tt.expectErr {
				require.Error(t, err, "availability resolution should propagate lookup failures")
				return
			}
			require.NoError(t, err, "availability resolution should succeed")
			require.Equal(t, tt.expected, actual, "availability resolution should mark only authenticated reviewers runnable")
		})
	}
}

func TestResolveCodeReviewOrchestratorAvailability(t *testing.T) {
	t.Parallel()

	cfg := models.DefaultCodeReviewPolicyConfig()
	tests := []struct {
		name      string
		services  *Services
		expected  codeReviewOrchestratorSelection
		expectErr bool
	}{
		{
			name: "uses configured orchestrator when authenticated",
			services: &Services{CodingAgents: codeReviewAgentAvailabilityStub{available: map[models.AgentType]bool{
				models.AgentTypeOpenCode: true,
				models.AgentTypeCodex:    true,
			}}},
			expected: codeReviewOrchestratorSelection{
				AgentType:  models.AgentTypeOpenCode,
				AgentModel: stringPtr(models.OpenCodeModelGPT55),
				Available:  true,
			},
		},
		{
			name: "falls back to Codex when it is the only authenticated agent",
			services: &Services{CodingAgents: codeReviewAgentAvailabilityStub{available: map[models.AgentType]bool{
				models.AgentTypeCodex: true,
			}}},
			expected: codeReviewOrchestratorSelection{
				AgentType:  models.AgentTypeCodex,
				AgentModel: stringPtr(models.DefaultCodexModel),
				Available:  true,
			},
		},
		{
			name: "falls back to Codex when the configured OpenCode model has no runnable credential route",
			services: &Services{CodingAgents: codeReviewAgentAvailabilityStub{
				available: map[models.AgentType]bool{
					models.AgentTypeCodex: true,
				},
				availableModel: map[models.AgentType]map[string]bool{
					models.AgentTypeOpenCode: {
						models.OpenCodeModelGPT55: false,
					},
				},
			}},
			expected: codeReviewOrchestratorSelection{
				AgentType:  models.AgentTypeCodex,
				AgentModel: stringPtr(models.DefaultCodexModel),
				Available:  true,
			},
		},
		{
			name: "falls back to Claude Code when it is the only authenticated agent",
			services: &Services{CodingAgents: codeReviewAgentAvailabilityStub{available: map[models.AgentType]bool{
				models.AgentTypeClaudeCode: true,
			}}},
			expected: codeReviewOrchestratorSelection{
				AgentType:  models.AgentTypeClaudeCode,
				AgentModel: stringPtr(models.DefaultClaudeCodeModel),
				Available:  true,
			},
		},
		{
			name:     "does not select an unauthenticated agent",
			services: &Services{CodingAgents: codeReviewAgentAvailabilityStub{available: map[models.AgentType]bool{}}},
			expected: codeReviewOrchestratorSelection{
				AgentType:  models.AgentTypeOpenCode,
				AgentModel: stringPtr(models.OpenCodeModelGPT55),
				Available:  false,
			},
		},
		{
			name: "preserves the configured orchestrator without an availability service",
			expected: codeReviewOrchestratorSelection{
				AgentType:  models.AgentTypeOpenCode,
				AgentModel: stringPtr(models.OpenCodeModelGPT55),
				Available:  true,
			},
		},
		{
			name:      "propagates availability lookup errors",
			services:  &Services{CodingAgents: codeReviewAgentAvailabilityStub{err: errors.New("resolver failed")}},
			expectErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			actual, err := resolveCodeReviewOrchestratorAvailability(context.Background(), tt.services, uuid.New(), cfg)
			if tt.expectErr {
				require.Error(t, err, "orchestrator availability resolution should propagate lookup failures")
				return
			}
			require.NoError(t, err, "orchestrator availability resolution should succeed")
			require.Equal(t, tt.expected, actual, "orchestrator availability resolution should select only an authenticated coding agent")
		})
	}
}

func TestUnavailableCodeReviewReviewerResult(t *testing.T) {
	t.Parallel()

	job := runCodeReviewPayload{OrgID: uuid.New(), SessionID: uuid.New()}
	result := unavailableCodeReviewReviewerResult(job, 1, models.AgentTypeClaudeCode, stringPtr(models.DefaultClaudeCodeModel))
	state, ok := parseCodeReviewReviewerStructuredResult(result.StructuredResult)

	require.Equal(t, models.CodeReviewAgentResultStatusFailed, result.Status, "unavailable reviewers should be terminal without starting a thread")
	require.True(t, ok, "unavailable reviewer state should be valid structured JSON")
	require.True(t, state.Unavailable, "unavailable reviewer state should explain why no thread was started")
	require.Empty(t, state.ThreadID, "unavailable reviewers should not have a thread id")
	require.Equal(t, []string{"Claude Code unavailable"}, codeReviewAgentSummaries([]models.CodeReviewAgentResult{*result}, nil), "review summary should distinguish unavailable auth from a runtime failure")
}

func TestCodeReviewOrchestratorAgentModel(t *testing.T) {
	t.Parallel()

	cfg := models.DefaultCodeReviewPolicyConfig()
	cfg.AgentRoster.Orchestrator = models.AgentTypeOpenCode

	pinned := cfg
	model := models.OpenCodeModelGPT54Mini
	pinned.AgentRoster.OrchestratorModel = &model
	require.Equal(t, models.OpenCodeModelGPT54Mini, *codeReviewOrchestratorAgentModel(pinned),
		"non-empty configured orchestrator model should win")

	whitespace := cfg
	blank := "   "
	whitespace.AgentRoster.OrchestratorModel = &blank
	require.Equal(t, models.OpenCodeModelGPT55, *codeReviewOrchestratorAgentModel(whitespace),
		"whitespace-only orchestrator model should fall back to the per-agent default")

	unset := cfg
	unset.AgentRoster.OrchestratorModel = nil
	require.Equal(t, models.OpenCodeModelGPT55, *codeReviewOrchestratorAgentModel(unset),
		"nil orchestrator model should fall back to the per-agent default")
}

func TestCodeReviewSessionURL(t *testing.T) {
	t.Parallel()

	sessionID := uuid.New()

	tests := []struct {
		name        string
		frontendURL string
		expected    string
	}{
		{name: "empty frontend URL omits link", expected: ""},
		{name: "trims trailing slash", frontendURL: "https://143.dev/", expected: "https://143.dev/sessions/" + sessionID.String()},
		{name: "uses base URL", frontendURL: "https://app.143.dev", expected: "https://app.143.dev/sessions/" + sessionID.String()},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			actual := codeReviewSessionURL(tt.frontendURL, sessionID)

			require.Equal(t, tt.expected, actual, "codeReviewSessionURL should build stable session links")
		})
	}
}

func TestEvaluateLiveCodeReviewOutcome(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	sessionID := uuid.New()
	policy := models.DefaultCodeReviewPolicyConfig()
	policy.ApprovalMode = models.CodeReviewApprovalModeApproveAcceptable
	prBody := "Fixes invoice rounding.\n\nTesting: go test ./..."
	validOrchestratorResult := models.CodeReviewAgentResult{
		Role:   models.CodeReviewAgentRoleOrchestrator,
		Status: models.CodeReviewAgentResultStatusCompleted,
		StructuredResult: marshalCodeReviewOrchestratorStructuredResult(codeReviewOrchestratorStructuredResult{
			SynthesisValidated: true,
			Synthesis: codeReviewOrchestratorSynthesis{
				Summary:       "The reviewed change is safe to approve.",
				ReviewSummary: "The router update is focused, and both review agents found no blocking issues.",
				RiskNotes:     []string{},
			},
		}),
	}

	tests := []struct {
		name         string
		input        liveCodeReviewOutcomeInput
		expected     models.CodeReviewDecision
		reason       string
		bodyContains string
	}{
		{
			name: "approves when live reviewer quorum and PR health satisfy policy",
			input: liveCodeReviewOutcomeInput{
				Policy:     policy,
				Job:        runCodeReviewPayload{OrgID: orgID, SessionID: sessionID, PolicyVersion: 3, HeadSHA: "head"},
				SessionURL: "https://143.dev/sessions/" + sessionID.String(),
				PullRequest: models.PullRequest{
					OrgID:   orgID,
					Body:    &prBody,
					HeadSHA: stringPtr("head"),
					Status:  models.PullRequestStatusOpen,
				},
				Health: &models.PullRequestHealthResponse{
					HeadSHA:         "head",
					Status:          models.PullRequestStatusOpen,
					CanMerge:        true,
					ChecksConfirmed: true,
					Checks: []models.PullRequestCheckSummary{
						{Name: "tests", Status: models.PullRequestCheckStatusPassed},
					},
					MergeState: models.PullRequestMergeStateClean,
				},
				AgentResults: []models.CodeReviewAgentResult{
					{Role: models.CodeReviewAgentRoleReviewer, Status: models.CodeReviewAgentResultStatusCompleted},
					{Role: models.CodeReviewAgentRoleReviewer, Status: models.CodeReviewAgentResultStatusCompleted},
					validOrchestratorResult,
				},
				ChangedFiles: []codereview.PullRequestFile{
					{Filename: "internal/api/router.go", Additions: 10, Deletions: 2},
				},
				ChangedFilesAvailable: true,
				OrchestratorSynthesis: codeReviewOrchestratorSynthesis{
					ReviewSummary: "The router update is focused, and both review agents found no blocking issues.",
				},
			},
			expected:     models.CodeReviewDecisionApproved,
			bodyContains: "Why: The router update is focused, and both review agents found no blocking issues.",
		},
		{
			name: "withholds approval when orchestrator synthesis is malformed",
			input: liveCodeReviewOutcomeInput{
				Policy: policy,
				Job:    runCodeReviewPayload{OrgID: orgID, SessionID: sessionID, PolicyVersion: 3, HeadSHA: "head"},
				PullRequest: models.PullRequest{
					OrgID:   orgID,
					Body:    &prBody,
					HeadSHA: stringPtr("head"),
					Status:  models.PullRequestStatusOpen,
				},
				Health: &models.PullRequestHealthResponse{
					HeadSHA:         "head",
					Status:          models.PullRequestStatusOpen,
					CanMerge:        true,
					ChecksConfirmed: true,
					Checks: []models.PullRequestCheckSummary{
						{Name: "tests", Status: models.PullRequestCheckStatusPassed},
					},
					MergeState: models.PullRequestMergeStateClean,
				},
				AgentResults: []models.CodeReviewAgentResult{
					{Role: models.CodeReviewAgentRoleReviewer, Status: models.CodeReviewAgentResultStatusCompleted},
					{Role: models.CodeReviewAgentRoleReviewer, Status: models.CodeReviewAgentResultStatusCompleted},
					{
						Role:   models.CodeReviewAgentRoleOrchestrator,
						Status: models.CodeReviewAgentResultStatusCompleted,
						StructuredResult: marshalCodeReviewOrchestratorStructuredResult(codeReviewOrchestratorStructuredResult{
							Synthesis: codeReviewOrchestratorSynthesis{Summary: "This summary was never strictly validated."},
						}),
					},
				},
				ChangedFiles: []codereview.PullRequestFile{
					{Filename: "internal/api/router.go", Additions: 10, Deletions: 2},
				},
				ChangedFilesAvailable: true,
			},
			expected: models.CodeReviewDecisionNeedsHumanReview,
			reason:   "orchestrator did not produce a valid structured synthesis",
		},
		{
			name: "uses queued GitHub author login for eligible author policy",
			input: liveCodeReviewOutcomeInput{
				Policy: func() models.CodeReviewPolicyConfig {
					config := policy
					config.RiskPolicy.EligibleAuthors = []string{"anya"}
					return config
				}(),
				Job: runCodeReviewPayload{OrgID: orgID, SessionID: sessionID, PolicyVersion: 3, HeadSHA: "head", PullRequestAuthor: "anya"},
				PullRequest: models.PullRequest{
					OrgID:      orgID,
					Body:       &prBody,
					HeadSHA:    stringPtr("head"),
					Status:     models.PullRequestStatusOpen,
					AuthoredBy: models.GitIdentitySourceUser,
				},
				Health: &models.PullRequestHealthResponse{
					HeadSHA:         "head",
					Status:          models.PullRequestStatusOpen,
					CanMerge:        true,
					ChecksConfirmed: true,
					Checks: []models.PullRequestCheckSummary{
						{Name: "tests", Status: models.PullRequestCheckStatusPassed},
					},
					MergeState: models.PullRequestMergeStateClean,
				},
				AgentResults: []models.CodeReviewAgentResult{
					{Role: models.CodeReviewAgentRoleReviewer, Status: models.CodeReviewAgentResultStatusCompleted},
					{Role: models.CodeReviewAgentRoleReviewer, Status: models.CodeReviewAgentResultStatusCompleted},
				},
				ChangedFiles: []codereview.PullRequestFile{
					{Filename: "internal/api/router.go", Additions: 10, Deletions: 2},
				},
				ChangedFilesAvailable: true,
			},
			expected: models.CodeReviewDecisionApproved,
		},
		{
			name: "withholds approval without reviewer quorum",
			input: liveCodeReviewOutcomeInput{
				Policy: policy,
				Job:    runCodeReviewPayload{OrgID: orgID, SessionID: sessionID, PolicyVersion: 3, HeadSHA: "head"},
				PullRequest: models.PullRequest{
					OrgID:   orgID,
					Body:    &prBody,
					HeadSHA: stringPtr("head"),
					Status:  models.PullRequestStatusOpen,
				},
				Health: &models.PullRequestHealthResponse{
					HeadSHA:         "head",
					Status:          models.PullRequestStatusOpen,
					CanMerge:        true,
					ChecksConfirmed: true,
					MergeState:      models.PullRequestMergeStateClean,
				},
				AgentResults: []models.CodeReviewAgentResult{
					{Role: models.CodeReviewAgentRoleReviewer, Status: models.CodeReviewAgentResultStatusCompleted},
				},
				ChangedFiles: []codereview.PullRequestFile{
					{Filename: "internal/api/router.go", Additions: 10, Deletions: 2},
				},
				ChangedFilesAvailable: true,
			},
			expected: models.CodeReviewDecisionNeedsHumanReview,
			reason:   "reviewer quorum 1 is below policy requirement 2",
		},
		{
			name: "uses successful reviewer output when a sibling reviewer fails",
			input: liveCodeReviewOutcomeInput{
				Policy: policy,
				Job:    runCodeReviewPayload{OrgID: orgID, SessionID: sessionID, PolicyVersion: 3, HeadSHA: "head"},
				PullRequest: models.PullRequest{
					OrgID:   orgID,
					Body:    &prBody,
					HeadSHA: stringPtr("head"),
					Status:  models.PullRequestStatusOpen,
				},
				Health: &models.PullRequestHealthResponse{
					HeadSHA:         "head",
					Status:          models.PullRequestStatusOpen,
					CanMerge:        true,
					ChecksConfirmed: true,
					MergeState:      models.PullRequestMergeStateClean,
				},
				AgentResults: []models.CodeReviewAgentResult{
					{Role: models.CodeReviewAgentRoleReviewer, AgentProvider: "codex", Status: models.CodeReviewAgentResultStatusCompleted},
					{Role: models.CodeReviewAgentRoleReviewer, AgentProvider: "claude_code", Status: models.CodeReviewAgentResultStatusFailed},
				},
				ChangedFiles: []codereview.PullRequestFile{
					{Filename: "internal/api/router.go", Additions: 10, Deletions: 2},
				},
				ChangedFilesAvailable: true,
			},
			expected:     models.CodeReviewDecisionNeedsHumanReview,
			reason:       "reviewer quorum 1 is below policy requirement 2",
			bodyContains: "Reviewer evidence: Codex found no blocking issues; Claude Code failed",
		},
		{
			name: "explains the failed PR description requirement",
			input: liveCodeReviewOutcomeInput{
				Policy: policy,
				Job:    runCodeReviewPayload{OrgID: orgID, SessionID: sessionID, PolicyVersion: 3, HeadSHA: "head"},
				PullRequest: models.PullRequest{
					OrgID:   orgID,
					Body:    &prBody,
					HeadSHA: stringPtr("head"),
					Status:  models.PullRequestStatusOpen,
				},
				Health: &models.PullRequestHealthResponse{
					HeadSHA:         "head",
					Status:          models.PullRequestStatusOpen,
					CanMerge:        true,
					ChecksConfirmed: true,
					MergeState:      models.PullRequestMergeStateClean,
				},
				AgentResults: []models.CodeReviewAgentResult{
					{Role: models.CodeReviewAgentRoleReviewer, Status: models.CodeReviewAgentResultStatusCompleted},
					{Role: models.CodeReviewAgentRoleReviewer, Status: models.CodeReviewAgentResultStatusCompleted},
				},
				ChangedFiles: []codereview.PullRequestFile{
					{Filename: "internal/api/router.go", Additions: 10, Deletions: 2},
				},
				ChangedFilesAvailable: true,
				DescriptionEvaluation: codeReviewDescriptionEvaluation{
					Passed:               false,
					RequirementSummaries: []string{"Understandable description: failed (explain why this change is needed)"},
				},
			},
			expected:     models.CodeReviewDecisionNeedsHumanReview,
			reason:       "PR description policy did not pass",
			bodyContains: "Understandable description (explain why this change is needed)",
		},
		{
			name: "withholds approval when completed read-only reviewer has no usable output",
			input: liveCodeReviewOutcomeInput{
				Policy: policy,
				Job:    runCodeReviewPayload{OrgID: orgID, SessionID: sessionID, PolicyVersion: 3, HeadSHA: "head"},
				PullRequest: models.PullRequest{
					OrgID:   orgID,
					Body:    &prBody,
					HeadSHA: stringPtr("head"),
					Status:  models.PullRequestStatusOpen,
				},
				Health: &models.PullRequestHealthResponse{
					HeadSHA:         "head",
					Status:          models.PullRequestStatusOpen,
					CanMerge:        true,
					ChecksConfirmed: true,
					MergeState:      models.PullRequestMergeStateClean,
				},
				AgentResults: []models.CodeReviewAgentResult{
					{Role: models.CodeReviewAgentRoleReviewer, AgentProvider: "claude", Status: models.CodeReviewAgentResultStatusCompleted},
					{
						Role:          models.CodeReviewAgentRoleReviewer,
						AgentProvider: "codex",
						Status:        models.CodeReviewAgentResultStatusCompleted,
						StructuredResult: marshalCodeReviewReviewerStructuredResult(codeReviewReviewerStructuredResult{
							ReadOnlyViolation: true,
							Error:             "reviewer thread produced workspace changes without persisted assistant output",
						}),
					},
				},
				ChangedFiles: []codereview.PullRequestFile{
					{Filename: "internal/api/router.go", Additions: 10, Deletions: 2},
				},
				ChangedFilesAvailable: true,
			},
			expected:     models.CodeReviewDecisionNeedsHumanReview,
			reason:       "reviewer quorum 1 is below policy requirement 2",
			bodyContains: "Codex produced no usable review output",
		},
		{
			name: "withholds approval for fork pull requests when policy disallows forks",
			input: liveCodeReviewOutcomeInput{
				Policy: policy,
				Job:    runCodeReviewPayload{OrgID: orgID, SessionID: sessionID, PolicyVersion: 3, HeadSHA: "head", FromFork: true},
				PullRequest: models.PullRequest{
					OrgID:   orgID,
					Body:    &prBody,
					HeadSHA: stringPtr("head"),
					Status:  models.PullRequestStatusOpen,
				},
				Health: &models.PullRequestHealthResponse{
					HeadSHA:         "head",
					Status:          models.PullRequestStatusOpen,
					CanMerge:        true,
					ChecksConfirmed: true,
					Checks: []models.PullRequestCheckSummary{
						{Name: "tests", Status: models.PullRequestCheckStatusPassed},
					},
					MergeState: models.PullRequestMergeStateClean,
				},
				AgentResults: []models.CodeReviewAgentResult{
					{Role: models.CodeReviewAgentRoleReviewer, Status: models.CodeReviewAgentResultStatusCompleted},
					{Role: models.CodeReviewAgentRoleReviewer, Status: models.CodeReviewAgentResultStatusCompleted},
				},
				ChangedFiles: []codereview.PullRequestFile{
					{Filename: "internal/api/router.go", Additions: 10, Deletions: 2},
				},
				ChangedFilesAvailable: true,
			},
			expected: models.CodeReviewDecisionNeedsHumanReview,
			reason:   "fork PRs are not eligible for approval",
		},
		{
			name: "withholds approval when prior human review requested changes",
			input: liveCodeReviewOutcomeInput{
				Policy: policy,
				Job:    runCodeReviewPayload{OrgID: orgID, SessionID: sessionID, PolicyVersion: 3, HeadSHA: "head"},
				PullRequest: models.PullRequest{
					OrgID:        orgID,
					Body:         &prBody,
					HeadSHA:      stringPtr("head"),
					Status:       models.PullRequestStatusOpen,
					ReviewStatus: models.PullRequestReviewStatusChangesRequested,
				},
				Health: &models.PullRequestHealthResponse{
					HeadSHA:         "head",
					Status:          models.PullRequestStatusOpen,
					CanMerge:        true,
					ChecksConfirmed: true,
					Checks: []models.PullRequestCheckSummary{
						{Name: "tests", Status: models.PullRequestCheckStatusPassed},
					},
					MergeState: models.PullRequestMergeStateClean,
				},
				AgentResults: []models.CodeReviewAgentResult{
					{Role: models.CodeReviewAgentRoleReviewer, Status: models.CodeReviewAgentResultStatusCompleted},
					{Role: models.CodeReviewAgentRoleReviewer, Status: models.CodeReviewAgentResultStatusCompleted},
				},
				ChangedFiles: []codereview.PullRequestFile{
					{Filename: "internal/api/router.go", Additions: 10, Deletions: 2},
				},
				ChangedFilesAvailable: true,
			},
			expected: models.CodeReviewDecisionNeedsHumanReview,
			reason:   "unresolved human review threads are present",
		},
		{
			name: "withholds approval when PR head moved",
			input: liveCodeReviewOutcomeInput{
				Policy: policy,
				Job:    runCodeReviewPayload{OrgID: orgID, SessionID: sessionID, PolicyVersion: 3, HeadSHA: "old"},
				PullRequest: models.PullRequest{
					OrgID:   orgID,
					Body:    &prBody,
					HeadSHA: stringPtr("new"),
					Status:  models.PullRequestStatusOpen,
				},
				Health: &models.PullRequestHealthResponse{
					HeadSHA:         "new",
					Status:          models.PullRequestStatusOpen,
					CanMerge:        true,
					ChecksConfirmed: true,
					MergeState:      models.PullRequestMergeStateClean,
				},
				AgentResults: []models.CodeReviewAgentResult{
					{Role: models.CodeReviewAgentRoleReviewer, Status: models.CodeReviewAgentResultStatusCompleted},
					{Role: models.CodeReviewAgentRoleReviewer, Status: models.CodeReviewAgentResultStatusCompleted},
				},
				ChangedFiles: []codereview.PullRequestFile{
					{Filename: "internal/api/router.go", Additions: 10, Deletions: 2},
				},
				ChangedFilesAvailable: true,
			},
			expected: models.CodeReviewDecisionNeedsHumanReview,
			reason:   "PR head changed after review started",
		},
		{
			name: "withholds approval for blocking findings",
			input: liveCodeReviewOutcomeInput{
				Policy: policy,
				Job:    runCodeReviewPayload{OrgID: orgID, SessionID: sessionID, PolicyVersion: 3, HeadSHA: "head"},
				PullRequest: models.PullRequest{
					OrgID:   orgID,
					Body:    &prBody,
					HeadSHA: stringPtr("head"),
					Status:  models.PullRequestStatusOpen,
				},
				Health: &models.PullRequestHealthResponse{
					HeadSHA:         "head",
					Status:          models.PullRequestStatusOpen,
					CanMerge:        true,
					ChecksConfirmed: true,
					MergeState:      models.PullRequestMergeStateClean,
				},
				AgentResults: []models.CodeReviewAgentResult{
					{Role: models.CodeReviewAgentRoleReviewer, Status: models.CodeReviewAgentResultStatusCompleted},
					{Role: models.CodeReviewAgentRoleReviewer, Status: models.CodeReviewAgentResultStatusCompleted},
				},
				Findings: []models.CodeReviewFinding{
					{Severity: models.CodeReviewFindingSeverityHigh},
				},
				ChangedFiles: []codereview.PullRequestFile{
					{Filename: "internal/api/router.go", Additions: 10, Deletions: 2},
				},
				ChangedFilesAvailable: true,
			},
			expected: models.CodeReviewDecisionNeedsHumanReview,
			reason:   "review agents reported blocking findings",
		},
		{
			name: "withholds approval for dependency file category",
			input: liveCodeReviewOutcomeInput{
				Policy: policy,
				Job:    runCodeReviewPayload{OrgID: orgID, SessionID: sessionID, PolicyVersion: 3, HeadSHA: "head"},
				PullRequest: models.PullRequest{
					OrgID:   orgID,
					Body:    &prBody,
					HeadSHA: stringPtr("head"),
					Status:  models.PullRequestStatusOpen,
				},
				Health: &models.PullRequestHealthResponse{
					HeadSHA:         "head",
					Status:          models.PullRequestStatusOpen,
					CanMerge:        true,
					ChecksConfirmed: true,
					MergeState:      models.PullRequestMergeStateClean,
				},
				AgentResults: []models.CodeReviewAgentResult{
					{Role: models.CodeReviewAgentRoleReviewer, Status: models.CodeReviewAgentResultStatusCompleted},
					{Role: models.CodeReviewAgentRoleReviewer, Status: models.CodeReviewAgentResultStatusCompleted},
				},
				ChangedFiles: []codereview.PullRequestFile{
					{Filename: "go.mod", Additions: 1, Deletions: 0},
				},
				ChangedFilesAvailable: true,
			},
			expected: models.CodeReviewDecisionNeedsHumanReview,
			reason:   "excluded risk category changed: dependencies",
		},
		{
			name: "approves large docs-only change through the low-risk lane despite timed-out reviewers",
			input: liveCodeReviewOutcomeInput{
				Policy: policy,
				Job:    runCodeReviewPayload{OrgID: orgID, SessionID: sessionID, PolicyVersion: 3, HeadSHA: "head"},
				PullRequest: models.PullRequest{
					OrgID:   orgID,
					Body:    &prBody,
					HeadSHA: stringPtr("head"),
					Status:  models.PullRequestStatusOpen,
				},
				Health: &models.PullRequestHealthResponse{
					HeadSHA:         "head",
					Status:          models.PullRequestStatusOpen,
					CanMerge:        true,
					ChecksConfirmed: true,
					Checks: []models.PullRequestCheckSummary{
						{Name: "All Checks Pass", Status: models.PullRequestCheckStatusPassed},
						// The reviewer's own status is pending while it runs; it must
						// not be counted as a failing check against its own approval.
						{Name: "143 Code Reviewer", Status: models.PullRequestCheckStatusPending},
					},
					MergeState: models.PullRequestMergeStateClean,
				},
				AgentResults: []models.CodeReviewAgentResult{
					{Role: models.CodeReviewAgentRoleReviewer, Status: models.CodeReviewAgentResultStatusTimedOut},
					{Role: models.CodeReviewAgentRoleReviewer, Status: models.CodeReviewAgentResultStatusTimedOut},
				},
				ChangedFiles: []codereview.PullRequestFile{
					// Filename contains "session" — must not be classified as auth.
					// 607 lines exceeds the base 300 cap but is under the docs lane cap.
					{Filename: "docs/design/future/111-session-changesets-and-stacks.md", Additions: 607, Deletions: 0},
				},
				ChangedFilesAvailable: true,
			},
			expected:     models.CodeReviewDecisionApproved,
			bodyContains: "reviewer quorum was waived for this low-risk change",
		},
		{
			name: "reports satisfied reviewer quorum for a low-risk change with complete reviews",
			input: liveCodeReviewOutcomeInput{
				Policy: policy,
				Job:    runCodeReviewPayload{OrgID: orgID, SessionID: sessionID, PolicyVersion: 3, HeadSHA: "head"},
				PullRequest: models.PullRequest{
					OrgID:   orgID,
					Body:    &prBody,
					HeadSHA: stringPtr("head"),
					Status:  models.PullRequestStatusOpen,
				},
				Health: &models.PullRequestHealthResponse{
					HeadSHA:         "head",
					Status:          models.PullRequestStatusOpen,
					CanMerge:        true,
					ChecksConfirmed: true,
					Checks: []models.PullRequestCheckSummary{
						{Name: "All Checks Pass", Status: models.PullRequestCheckStatusPassed},
					},
					MergeState: models.PullRequestMergeStateClean,
				},
				AgentResults: []models.CodeReviewAgentResult{
					{Role: models.CodeReviewAgentRoleReviewer, Status: models.CodeReviewAgentResultStatusCompleted},
					{Role: models.CodeReviewAgentRoleReviewer, Status: models.CodeReviewAgentResultStatusCompleted},
				},
				ChangedFiles: []codereview.PullRequestFile{
					{Filename: "docs/review-guide.md", Additions: 10, Deletions: 2},
				},
				ChangedFilesAvailable: true,
			},
			expected:     models.CodeReviewDecisionApproved,
			bodyContains: "2 usable reviewer reports met the required quorum of 2",
		},
		{
			name: "clamps reviewer quorum to reviewers whose credentials are available",
			input: liveCodeReviewOutcomeInput{
				Policy: policy,
				Job:    runCodeReviewPayload{OrgID: orgID, SessionID: sessionID, PolicyVersion: 3, HeadSHA: "head"},
				PullRequest: models.PullRequest{
					OrgID:   orgID,
					Body:    &prBody,
					HeadSHA: stringPtr("head"),
					Status:  models.PullRequestStatusOpen,
				},
				Health: &models.PullRequestHealthResponse{
					HeadSHA:         "head",
					Status:          models.PullRequestStatusOpen,
					CanMerge:        true,
					ChecksConfirmed: true,
					Checks: []models.PullRequestCheckSummary{
						{Name: "tests", Status: models.PullRequestCheckStatusPassed},
					},
					MergeState: models.PullRequestMergeStateClean,
				},
				AgentResults: []models.CodeReviewAgentResult{
					{Role: models.CodeReviewAgentRoleReviewer, AgentProvider: "codex", Status: models.CodeReviewAgentResultStatusCompleted},
					{
						Role:          models.CodeReviewAgentRoleReviewer,
						AgentProvider: "claude_code",
						Status:        models.CodeReviewAgentResultStatusFailed,
						StructuredResult: marshalCodeReviewReviewerStructuredResult(codeReviewReviewerStructuredResult{
							Unavailable: true,
							Error:       "reviewer skipped because claude_code authentication is not configured",
						}),
					},
					validOrchestratorResult,
				},
				ChangedFiles: []codereview.PullRequestFile{
					{Filename: "internal/api/router.go", Additions: 10, Deletions: 2},
				},
				ChangedFilesAvailable: true,
				OrchestratorSynthesis: codeReviewOrchestratorSynthesis{
					ReviewSummary: "The only available reviewer found no blocking issues.",
				},
			},
			expected:     models.CodeReviewDecisionApproved,
			bodyContains: "reviewer quorum 1/1",
		},
		{
			name: "still requires human review for a docs change above the low-risk lane ceiling",
			input: liveCodeReviewOutcomeInput{
				Policy: policy,
				Job:    runCodeReviewPayload{OrgID: orgID, SessionID: sessionID, PolicyVersion: 3, HeadSHA: "head"},
				PullRequest: models.PullRequest{
					OrgID:   orgID,
					Body:    &prBody,
					HeadSHA: stringPtr("head"),
					Status:  models.PullRequestStatusOpen,
				},
				Health: &models.PullRequestHealthResponse{
					HeadSHA:         "head",
					Status:          models.PullRequestStatusOpen,
					CanMerge:        true,
					ChecksConfirmed: true,
					Checks: []models.PullRequestCheckSummary{
						{Name: "All Checks Pass", Status: models.PullRequestCheckStatusPassed},
					},
					MergeState: models.PullRequestMergeStateClean,
				},
				AgentResults: []models.CodeReviewAgentResult{
					{Role: models.CodeReviewAgentRoleReviewer, Status: models.CodeReviewAgentResultStatusCompleted},
					{Role: models.CodeReviewAgentRoleReviewer, Status: models.CodeReviewAgentResultStatusCompleted},
				},
				ChangedFiles: []codereview.PullRequestFile{
					{Filename: "docs/design/future/huge.md", Additions: 1200, Deletions: 0},
				},
				ChangedFilesAvailable: true,
			},
			expected: models.CodeReviewDecisionNeedsHumanReview,
			reason:   "changed lines 1200 exceeds policy limit 1000",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			decision, body := evaluateLiveCodeReviewOutcome(tt.input)

			require.Equal(t, tt.expected, decision.Decision, "live code review outcome should choose the expected decision")
			if tt.reason != "" {
				require.Contains(t, decision.RiskReasons, tt.reason, "non-approval should preserve the expected risk reason")
				require.Contains(t, body, "Why:", "final review body should explain the non-approval reason")
			}
			if tt.bodyContains != "" {
				require.Contains(t, body, tt.bodyContains, "final review body should include expected evidence")
			}
		})
	}
}

func TestCodeReviewPathCategoriesDocsOnly(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		path     string
		expected []string
	}{
		{
			name:     "docs filename containing session is not classified as auth",
			path:     "docs/design/future/111-session-changesets-and-stacks.md",
			expected: []string{"docs"},
		},
		{
			name:     "docs filename containing token is not classified as crypto",
			path:     "docs/auth-token-rotation.md",
			expected: []string{"docs"},
		},
		{
			name:     "non-docs auth path still classified as auth",
			path:     "internal/auth/session.go",
			expected: []string{"backend", "auth"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.expected, codeReviewPathCategories(tt.path), "docs prose must not inherit code-risk categories")
		})
	}
}

func TestCodeReviewChecksPassingIgnoresSelfReportedStatuses(t *testing.T) {
	t.Parallel()

	policy := models.DefaultCodeReviewPolicyConfig()
	health := &models.PullRequestHealthResponse{
		Checks: []models.PullRequestCheckSummary{
			{Name: "All Checks Pass", Status: models.PullRequestCheckStatusPassed},
			{Name: "143 Code Reviewer", Status: models.PullRequestCheckStatusPending},
			{Name: "preview/143", Status: models.PullRequestCheckStatusPending},
		},
	}

	require.True(t, codeReviewChecksPassing(policy, health),
		"the reviewer's own pending status must not block its own approval")
}

func codeReviewPolicyRecordForTest(config models.CodeReviewPolicyConfig) models.CodeReviewPolicyRecord {
	return models.CodeReviewPolicyRecord{
		ID:                      uuid.New(),
		Version:                 1,
		Enabled:                 config.Enabled,
		ApprovalMode:            config.ApprovalMode,
		ReviewInstructions:      config.ReviewInstructions,
		AutomatedApprovalPolicy: config.AutomatedApprovalPolicy,
		DescriptionPolicy:       config.DescriptionPolicy,
		RiskPolicy:              config.RiskPolicy,
		AgentRoster:             config.AgentRoster,
		InlineCommentLimit:      config.InlineCommentLimit,
		CreatedAt:               time.Now().UTC(),
	}
}

func newCodeReviewAgentResultRows() *pgxmock.Rows {
	return pgxmock.NewRows([]string{
		"id", "org_id", "session_id", "agent_provider", "agent_model", "role", "status",
		"raw_output", "structured_result", "created_at",
	})
}

func newCodeReviewFindingRows() *pgxmock.Rows {
	return pgxmock.NewRows([]string{
		"id", "org_id", "session_id", "agent_result_id", "dedupe_key", "severity",
		"confidence", "path", "start_line", "end_line", "summary", "body",
		"selected_for_inline", "github_comment_id", "created_at",
	})
}

func newCodeReviewMetadataRows() *pgxmock.Rows {
	return pgxmock.NewRows([]string{
		"id", "org_id", "session_id", "repository_id", "pull_request_id", "policy_id",
		"base_sha", "head_sha", "from_fork", "trigger_source", "status", "decision", "acceptable",
		"stale", "superseded_by_session_id", "review_output_key", "prompt_artifact_key",
		"github_review_id", "github_review_url", "final_review_body", "failure_reason", "completed_at", "created_at",
	})
}

func newSessionThreadRows() *pgxmock.Rows {
	return pgxmock.NewRows([]string{
		"id", "session_id", "org_id", "agent_type", "model_override",
		"label", "instructions", "file_scope", "status", "agent_session_id", "current_turn", "last_activity_at",
		"result_summary", "diff", "failure_explanation", "failure_category",
		"started_at", "completed_at", "created_at", "created_by_source", "created_by_thread_id", "archived_at",
		"base_snapshot_key", "cost_cents", "pending_message_count", "cancel_requested_at",
		"runtime_stop_reason", "runtime_graceful_stop_at", "recovery_state", "recovery_reason", "recovery_event_history",
		"execution_mode", "filesystem_mode",
	})
}

func newSessionMessageRows() *pgxmock.Rows {
	return pgxmock.NewRows([]string{
		"id", "session_id", "org_id", "thread_id", "user_id", "turn_number", "role", "content",
		"attachments", "references", "commands", "token_usage", "source", "created_at",
	})
}

func stringPtr(value string) *string {
	return &value
}

func intPtr(value int) *int {
	return &value
}
