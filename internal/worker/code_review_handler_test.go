package worker

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/codereview"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/pashagolub/pgxmock/v4"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

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

	prompt := "/review"

	require.Equal(t, "/review", codeReviewReviewerPrompt(runCodeReviewPayload{}, models.PullRequest{}, models.DefaultCodeReviewPolicyConfig(), 0, "", nil), "code review reviewer prompt should stay as the bare native review command")
	require.Equal(t, "/review", codeReviewReviewerMessage(models.AgentTypeCodex, prompt), "Codex reviewer messages should invoke only native /review")
	require.Len(t, codeReviewNativeReviewCommands(models.AgentTypeCodex, prompt), 1, "native reviewer command metadata should be persisted")
	require.Equal(t, "", codeReviewNativeReviewCommands(models.AgentTypeCodex, prompt)[0].Arguments, "native reviewer command should not carry extra review arguments")
	require.Equal(t, "/review", codeReviewReviewerMessage(models.AgentTypeOpenCode, prompt), "agents without a native /review command should receive the plain prompt")
	require.Empty(t, codeReviewNativeReviewCommands(models.AgentTypeOpenCode, prompt), "agents without a native /review command should not persist command metadata")
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

func TestFailCodeReviewIfParentSessionTerminalFailsMetadata(t *testing.T) {
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
	failure := "This session was unable to start within the expected time."
	reason := "parent code review session is terminal: failed: " + failure
	decision := models.CodeReviewDecisionBlocked
	acceptable := false
	sessionRow := workerSessionRow(sessionID, uuid.Nil, orgID, models.SessionStatusFailed, 0, nil, nil)
	for i, col := range workerSessionColumns {
		if col == "failure_explanation" {
			sessionRow[i] = &failure
			break
		}
	}

	mock.ExpectQuery("(?s)SELECT .*FROM sessions").
		WithArgs(pgx.NamedArgs{"id": sessionID, "org_id": orgID}).
		WillReturnRows(pgxmock.NewRows(workerSessionColumns).AddRow(sessionRow...))
	mock.ExpectQuery("UPDATE code_review_session_metadata").
		WithArgs(pgx.NamedArgs{"org_id": orgID, "session_id": sessionID, "failure_reason": reason}).
		WillReturnRows(newCodeReviewMetadataRows().
			AddRow(metadataID, orgID, sessionID, repositoryID, pullRequestID, policyID,
				"base", "head", false, models.CodeReviewTriggerSourceTeamReviewer,
				models.CodeReviewSessionStatusFailed, &decision, &acceptable, false, nil,
				"output-key", nil, nil, nil, nil, &reason, &now, now))

	terminal, err := failCodeReviewIfParentSessionTerminal(context.Background(), &Stores{
		Sessions:    db.NewSessionStore(mock),
		CodeReviews: db.NewCodeReviewStore(mock),
	}, nil, zerolog.Nop(), runCodeReviewPayload{
		OrgID:     orgID,
		SessionID: sessionID,
	}, models.PullRequest{})

	require.NoError(t, err, "terminal parent session should not return an error")
	require.True(t, terminal, "terminal parent session should stop the code review job")
	require.NoError(t, mock.ExpectationsWereMet(), "terminal parent guard should fail the code review metadata")
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
{"scope_mismatch":false,"unresolved_uncertainty":false,"reviewer_disagreement":false,"prompt_injection_detected":false,"summary":"Adds review handling.","risk_notes":["tests needed"]}
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
		WithArgs(pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg(), pgxmock.AnyArg()).
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

type codeReviewDescriptionLLMStub struct {
	calls    int
	response string
}

func (s *codeReviewDescriptionLLMStub) Complete(context.Context, string, string) (string, error) {
	s.calls++
	return s.response, nil
}

func TestBuildUnavailableCodeReviewOutcome(t *testing.T) {
	t.Parallel()

	policy := models.DefaultCodeReviewPolicyConfig()
	policy.ApprovalMode = models.CodeReviewApprovalModeApproveAcceptable
	job := runCodeReviewPayload{PolicyVersion: 7, HeadSHA: "abc123"}

	decision, body := buildUnavailableCodeReviewOutcome(policy, job)

	require.Equal(t, models.CodeReviewDecisionNeedsHumanReview, decision.Decision, "unavailable live reviewer evidence should require human review")
	require.False(t, decision.Acceptable, "unavailable live reviewer evidence should not be acceptable risk")
	require.Contains(t, decision.RiskReasons, "Automated reviewer agents are not configured for this worker.", "decision should explain missing live reviewers")
	require.Contains(t, body, "Policy version: 7", "final body should include captured policy version")
	require.Contains(t, body, "Reviewed head: abc123", "final body should include reviewed head")
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

func TestCodeReviewStatusTargetURL(t *testing.T) {
	t.Parallel()

	sessionID := uuid.New()

	tests := []struct {
		name        string
		frontendURL string
		expected    string
	}{
		{name: "empty frontend URL omits target", expected: ""},
		{name: "trims trailing slash", frontendURL: "https://143.dev/", expected: "https://143.dev/sessions/" + sessionID.String()},
		{name: "uses base URL", frontendURL: "https://app.143.dev", expected: "https://app.143.dev/sessions/" + sessionID.String()},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			actual := codeReviewStatusTargetURL(tt.frontendURL, sessionID)

			require.Equal(t, tt.expected, actual, "codeReviewStatusTargetURL should build stable session links")
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
					{Role: models.CodeReviewAgentRoleOrchestrator, Status: models.CodeReviewAgentResultStatusCompleted},
				},
				ChangedFiles: []codereview.PullRequestFile{
					{Filename: "internal/api/router.go", Additions: 10, Deletions: 2},
				},
				ChangedFilesAvailable: true,
			},
			expected:     models.CodeReviewDecisionApproved,
			bodyContains: "Review session: https://143.dev/sessions/" + sessionID.String(),
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
			bodyContains: "codex produced no usable review output",
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
			expected: models.CodeReviewDecisionApproved,
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
				require.Contains(t, body, tt.reason, "final review body should explain the non-approval reason")
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
		ID:                 uuid.New(),
		Version:            1,
		Enabled:            config.Enabled,
		ApprovalMode:       config.ApprovalMode,
		DescriptionPolicy:  config.DescriptionPolicy,
		RiskPolicy:         config.RiskPolicy,
		AgentRoster:        config.AgentRoster,
		InlineCommentLimit: config.InlineCommentLimit,
		Inheritance:        config.Inheritance,
		CreatedAt:          time.Now().UTC(),
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
