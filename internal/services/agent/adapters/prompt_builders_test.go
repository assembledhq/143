package adapters

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/agent"
)

// ---------------------------------------------------------------------------
// buildSystemPrompt
// ---------------------------------------------------------------------------

func TestBuildSystemPrompt_IncludesPMContext(t *testing.T) {
	t.Parallel()

	issue := &models.Issue{
		Title: "Test issue",
	}
	input := &agent.AgentInput{
		Issue: issue,
		PMContext: &agent.PMTaskContext{
			Approach:      "Check handlers/billing.go:42",
			Risk:          "Be careful with retries",
			Reasoning:     "High impact",
			RelatedIssues: []string{"Payment timeout"},
			RootCause:     "Missing nil check",
		},
	}

	prompt := buildSystemPrompt(input)
	require.Contains(t, prompt, "Product Manager Analysis", "system prompt should include PM context header")
	require.Contains(t, prompt, "High impact", "system prompt should include PM reasoning")
	require.Contains(t, prompt, "Check handlers/billing.go:42", "system prompt should include PM approach")
	require.Contains(t, prompt, "Missing nil check", "system prompt should include PM root cause")
}

func TestBuildSystemPrompt_IncludesRevisionContext(t *testing.T) {
	t.Parallel()

	input := &agent.AgentInput{
		Issue: &models.Issue{Title: "Bug"},
		RevisionContext: &agent.RevisionContext{
			FormattedFeedback: "Please handle the edge case.",
			CommentSummary:    "Missing nil check in handler",
			PreviousDiff:      "--- a/main.go\n+++ b/main.go",
		},
	}

	prompt := buildSystemPrompt(input)
	require.Contains(t, prompt, "Revision Instructions")
	require.Contains(t, prompt, "REVISION run")
	require.Contains(t, prompt, "Please handle the edge case.")
	require.Contains(t, prompt, "Missing nil check in handler")
	require.Contains(t, prompt, "--- a/main.go")
}

func TestBuildSystemPrompt_IncludesRepairContext(t *testing.T) {
	t.Parallel()

	input := &agent.AgentInput{
		Issue: &models.Issue{Title: "Bug"},
		RevisionContext: &agent.RevisionContext{
			RepairAction: models.PullRequestRepairActionTypeFixTests,
			RepairContext: &agent.PullRequestRepairContext{
				PullRequestNumber: 184,
				Repository:        "org/repo",
				HeadSHA:           "abc123",
				BaseSHA:           "def456",
				MergeState:        models.PullRequestMergeStateClean,
				FailingChecks: []agent.PullRequestFailingCheck{
					{
						Name:        "unit tests / api",
						Category:    models.PullRequestCheckCategoryTest,
						Summary:     "2 failing tests in auth package",
						DetailsURL:  "https://github.com/org/repo/actions/runs/1/job/2",
						LogExcerpt:  "FAIL auth handler should reject expired token",
						Annotations: []string{"auth/handler_test.go:42 token expiry assertion failed"},
					},
				},
			},
		},
	}

	prompt := buildSystemPrompt(input)
	require.Contains(t, prompt, "Repair Context", "system prompt should include the repair context section")
	require.Contains(t, prompt, "fix_tests", "system prompt should include the repair action type")
	require.Contains(t, prompt, "PR #184", "system prompt should include the PR number")
	require.Contains(t, prompt, "unit tests / api", "system prompt should include failing check names")
	require.Contains(t, prompt, "token expiry assertion failed", "system prompt should include check annotations")
	require.NotContains(t, prompt, "Conflict resolution guidance", "system prompt should not include conflict guidance for fix_tests repair runs")
}

func TestBuildSystemPrompt_IncludesResolveConflictsGuidance(t *testing.T) {
	t.Parallel()

	input := &agent.AgentInput{
		Issue: &models.Issue{Title: "Merge conflict"},
		RevisionContext: &agent.RevisionContext{
			RepairAction: models.PullRequestRepairActionTypeResolveConflicts,
			RepairContext: &agent.PullRequestRepairContext{
				PullRequestNumber: 99,
				Repository:        "org/repo",
				HeadSHA:           "headsha",
				BaseSHA:           "basesha",
				MergeState:        models.PullRequestMergeStateConflicted,
				HasConflicts:      true,
			},
		},
	}

	prompt := buildSystemPrompt(input)
	require.Contains(t, prompt, "Conflict resolution guidance", "system prompt should include the conflict guidance header for resolve_conflicts repair runs")
	require.Contains(t, prompt, "merge index", "system prompt should warn that mid-merge git diff/status reflects the merge index, not the PR's net delta")
	require.Contains(t, prompt, "git diff basesha...HEAD", "system prompt should reference the supplied base SHA when describing how to verify the net delta")
}

func TestBuildSystemPrompt_IncludesContextDocs(t *testing.T) {
	t.Parallel()

	input := &agent.AgentInput{
		Issue:       &models.Issue{Title: "Bug"},
		ContextDocs: []string{"Use Go 1.22", "Run tests with make test"},
	}

	prompt := buildSystemPrompt(input)
	require.Contains(t, prompt, "Repository Conventions")
	require.Contains(t, prompt, "Use Go 1.22")
	require.Contains(t, prompt, "Run tests with make test")
}

func TestBuildSystemPrompt_Minimal(t *testing.T) {
	t.Parallel()

	input := &agent.AgentInput{
		Issue: &models.Issue{Title: "Bug"},
	}

	prompt := buildSystemPrompt(input)
	require.NotEmpty(t, prompt)
	require.NotContains(t, prompt, "Revision Instructions")
	require.NotContains(t, prompt, "Product Manager Analysis")
	require.NotContains(t, prompt, "Repository Conventions")
}

func TestBuildSystemPrompt_ManualSessionSkipsBaseTemplate(t *testing.T) {
	t.Parallel()

	input := &agent.AgentInput{
		Issue:       &models.Issue{Title: "help me refactor", Source: models.IssueSourceManual},
		Manual:      true,
		ContextDocs: []string{"Use Go 1.22"},
	}

	prompt := buildSystemPrompt(input)
	require.NotContains(t, prompt, "coding agent tasked with fixing a bug", "manual sessions should not include bug-fixing template")
	require.NotContains(t, prompt, "testing_requirements", "manual sessions should not include testing requirements")
	require.Contains(t, prompt, "Repository Conventions", "manual sessions should still include repo conventions")
	require.Contains(t, prompt, "Use Go 1.22")
}

func TestBuildSystemPrompt_AnswerOnlyUsesAnswerPreamble(t *testing.T) {
	t.Parallel()

	input := &agent.AgentInput{
		PromptStyle: agent.PromptStyleAnswerOnly,
		UserMessage: "does our slack bot post notifications when a job finishes?",
		PMContext: &agent.PMTaskContext{
			Approach:  "This should not appear",
			Reasoning: "Neither should this",
		},
		ContextDocs: []string{"Use Go 1.24"},
	}

	systemPrompt := buildSystemPrompt(input)
	userPrompt := buildUserPrompt(input)

	require.Contains(t, systemPrompt, "answer-only", "answer-only prompts should use the dedicated answer preamble")
	require.Contains(t, systemPrompt, "Do not modify files", "answer-only prompts should forbid file modifications")
	require.NotContains(t, systemPrompt, "Write tests", "answer-only prompts should not include coding-task test instructions")
	require.NotContains(t, systemPrompt, "Product Manager Analysis", "answer-only prompts should not include PM implementation framing")
	require.Contains(t, systemPrompt, "Repository Conventions", "answer-only prompts should still include repo conventions")
	require.Contains(t, systemPrompt, "Use Go 1.24", "answer-only prompts should preserve repository context docs")
	require.Equal(t, "does our slack bot post notifications when a job finishes?", userPrompt, "answer-only prompts should pass through the raw Slack question")
}

func TestBuildSystemPrompt_IncludesLinkedIssuesContext(t *testing.T) {
	t.Parallel()

	input := &agent.AgentInput{
		Issue: &models.Issue{Title: "Bug"},
		LinkedIssues: []models.SessionIssueSnapshotEntry{
			{
				Role:         models.SessionIssueLinkRolePrimary,
				Source:       models.IssueSourceLinear,
				Title:        "Fix checkout timeout",
				ExternalID:   "ENG-123",
				Description:  "Customers hit a timeout after payment authorization.",
				Priority:     "high",
				AssigneeName: "Ada Lovelace",
				TeamKey:      "ENG",
				URL:          "https://linear.app/acme/issue/ENG-123",
				Attachments: []models.SessionIssueSnapshotAttachment{
					{Title: "Trace", URL: "https://example.com/trace", Source: "sentry"},
				},
				Comments: []models.SessionIssueSnapshotComment{
					{Author: "Grace", Body: "Please include the edge case."},
				},
			},
			{
				Role:        models.SessionIssueLinkRoleRelated,
				Source:      models.IssueSourceSentry,
				Title:       "Cart worker panic",
				ExternalID:  "SENTRY-1",
				Description: "This description should not be copied for related issues.",
			},
		},
	}

	prompt := buildSystemPrompt(input)
	require.Contains(t, prompt, "Linked Issues Context", "buildSystemPrompt should include the linked issue context header")
	require.Contains(t, prompt, "<external_id>ENG-123</external_id>", "buildSystemPrompt should include external ids for linked issues")
	require.Contains(t, prompt, "<description>Customers hit a timeout after payment authorization.</description>", "buildSystemPrompt should include descriptions for primary linked issues")
	require.Contains(t, prompt, "<priority>high</priority>", "buildSystemPrompt should include Linear priority metadata")
	require.Contains(t, prompt, "<assignee>Ada Lovelace</assignee>", "buildSystemPrompt should include Linear assignee metadata")
	require.Contains(t, prompt, "<attachment", "buildSystemPrompt should include Linear attachment metadata")
	require.Contains(t, prompt, "Please include the edge case.", "buildSystemPrompt should include bounded Linear comments")
	require.NotContains(t, prompt, "This description should not be copied for related issues.", "buildSystemPrompt should omit descriptions for related linked issues")
}

// Manual sessions skip the coding-task preamble (which carries the
// "untrusted external content" warning) but can still be linked to Linear
// issues whose titles/descriptions/comments are attacker-controllable. The
// fence has to live inside the linked-issues block so it travels with the
// data regardless of caller — see linked_issues_context.template.
func TestBuildSystemPrompt_ManualSessionLinkedIssuesCarryTrustFence(t *testing.T) {
	t.Parallel()

	input := &agent.AgentInput{
		Issue:  &models.Issue{Title: "help me refactor", Source: models.IssueSourceManual},
		Manual: true,
		LinkedIssues: []models.SessionIssueSnapshotEntry{
			{
				Role:        models.SessionIssueLinkRolePrimary,
				Source:      models.IssueSourceLinear,
				Title:       "Fix checkout timeout",
				ExternalID:  "ENG-123",
				Description: "Customers hit a timeout after payment authorization.",
			},
		},
	}

	prompt := buildSystemPrompt(input)
	require.NotContains(t, prompt, "untrusted external content (e.g. from issue trackers)", "manual sessions correctly skip the coding-task preamble fence")
	require.Contains(t, prompt, "<trust_warning>", "linked-issues block must carry its own untrusted-content fence even on manual sessions")
	require.Contains(t, prompt, "untrusted external content", "trust_warning must call out untrusted external content")
}

func TestBuildSystemPrompt_AutomationRunMatchesSessionStyle(t *testing.T) {
	t.Parallel()

	input := &agent.AgentInput{
		Issue:             &models.Issue{Title: "Nightly dependency refresh"},
		PromptStyle:       agent.PromptStyleRawTask,
		UserMessage:       "Update stale dependencies and run the focused test suite.",
		ContextDocs:       []string{"Use Go 1.24"},
		IntegrationSkills: "# Integration Tools\n\nUse 143-tools ...",
		PMContext: &agent.PMTaskContext{
			Approach:  "This should not appear",
			Reasoning: "Neither should this",
		},
		LinkedIssues: []models.SessionIssueSnapshotEntry{
			{
				Role:        models.SessionIssueLinkRolePrimary,
				Source:      models.IssueSourceLinear,
				Title:       "Should stay out of automation prompts",
				ExternalID:  "ENG-999",
				Description: "Too much extra context.",
			},
		},
	}

	prompt := buildSystemPrompt(input)
	require.NotContains(t, prompt, "Write tests that cover any new or changed behavior.", "automation runs should skip the issue-triage base template just like manual sessions")
	require.Contains(t, prompt, "Repository Conventions", "automation runs should keep repository convention docs like normal sessions")
	require.Contains(t, prompt, "Use Go 1.24", "automation runs should include repository convention content")
	require.Contains(t, prompt, "# Integration Tools", "automation runs should keep integration skills like normal sessions")
	require.NotContains(t, prompt, "Product Manager Analysis", "automation runs should not inject a PM analysis wrapper around the goal")
}

// ---------------------------------------------------------------------------
// buildUserPrompt
// ---------------------------------------------------------------------------

func TestBuildUserPrompt(t *testing.T) {
	t.Parallel()

	desc := "Users see 500 errors on /api/billing"

	tests := []struct {
		name        string
		input       *agent.AgentInput
		wantStrings []string
	}{
		{
			name: "basic issue with description",
			input: &agent.AgentInput{
				Issue: &models.Issue{
					Title:       "Billing crash",
					Source:      models.IssueSourceSentry,
					Description: &desc,
				},
			},
			wantStrings: []string{"Billing crash", "500 errors"},
		},
		{
			name: "sentry issue with stack trace",
			input: &agent.AgentInput{
				Issue: &models.Issue{
					Title:  "NullPointer",
					Source: models.IssueSourceSentry,
					RawData: json.RawMessage(`{
						"entries": [{
							"type": "exception",
							"data": {
								"values": [{
									"type": "TypeError",
									"value": "null is not an object",
									"stacktrace": {
										"frames": [{
											"filename": "app.js",
											"function": "handleRequest",
											"lineNo": 42
										}]
									}
								}]
							}
						}]
					}`),
				},
			},
			wantStrings: []string{"Stack Trace", "TypeError", "handleRequest"},
		},
		{
			name: "customer impact",
			input: &agent.AgentInput{
				Issue: &models.Issue{
					Title:                 "Error",
					Source:                models.IssueSourceSentry,
					OccurrenceCount:       150,
					AffectedCustomerCount: 25,
				},
			},
			wantStrings: []string{"Customer Impact", "150", "25"},
		},
		{
			name: "severity",
			input: &agent.AgentInput{
				Issue: &models.Issue{
					Title:    "Error",
					Source:   models.IssueSourceSentry,
					Severity: "critical",
				},
			},
			wantStrings: []string{"critical"},
		},
		{
			name: "complexity estimate",
			input: &agent.AgentInput{
				Issue: &models.Issue{Title: "Error", Source: models.IssueSourceLinear},
				ComplexityEstimate: &agent.ComplexityEstimate{
					Tier:      2,
					Reasoning: "Multiple files affected",
				},
			},
			wantStrings: []string{"Complexity Assessment", "Tier: 2", "Multiple files affected"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			prompt := buildUserPrompt(tt.input)
			for _, s := range tt.wantStrings {
				require.Contains(t, prompt, s)
			}
		})
	}
}

func TestBuildUserPrompt_ManualSessionReturnsRawMessage(t *testing.T) {
	t.Parallel()

	msg := "help me improve the margins in the session"
	input := &agent.AgentInput{
		Issue: &models.Issue{
			Title:       "help me improve the margins",
			Source:      models.IssueSourceManual,
			Description: &msg,
		},
		Manual:      true,
		UserMessage: msg,
	}

	prompt := buildUserPrompt(input)
	require.Equal(t, msg, prompt, "manual session should return raw user message")
	require.NotContains(t, prompt, "## Issue:")
	require.NotContains(t, prompt, "### Description")
	require.NotContains(t, prompt, "Customer Impact")
	require.NotContains(t, prompt, "Severity")
}

func TestBuildUserPrompt_ManualSessionAppendsCanonicalReferences(t *testing.T) {
	t.Parallel()

	msg := "Investigate the session composer"
	input := &agent.AgentInput{
		Issue: &models.Issue{
			Title:       "Manual session",
			Source:      models.IssueSourceManual,
			Description: &msg,
		},
		Manual:      true,
		UserMessage: msg,
		References: []models.SessionInputReference{
			{
				Kind:    models.SessionInputReferenceKindFile,
				Token:   "@internal/api/handlers/sessions.go",
				Path:    "internal/api/handlers/sessions.go",
				Display: "sessions.go",
			},
			{
				Kind:    models.SessionInputReferenceKindApp,
				ID:      "github",
				Display: "GitHub",
			},
		},
	}

	prompt := buildUserPrompt(input)
	require.Contains(t, prompt, "## Referenced context", "manual prompts with references should append a canonical reference section")
	require.Contains(t, prompt, "- @internal/api/handlers/sessions.go (internal/api/handlers/sessions.go)", "manual prompts should render token and canonical path when display differs")
	require.Contains(t, prompt, "- GitHub [github]", "manual prompts should render id-backed references without requiring a token")
}

func TestBuildUserPrompt_ManualSessionPreservesSlashCommandTokens(t *testing.T) {
	t.Parallel()

	msg := "/review focus on the auth handler"
	input := &agent.AgentInput{
		Manual:      true,
		UserMessage: msg,
		Commands: []models.SessionInputCommand{
			{Kind: "command", AgentType: models.AgentTypeClaudeCode, Name: "review", Token: "/review", Display: "/review", Arguments: "focus on the auth handler"},
		},
	}

	prompt := buildUserPrompt(input)
	require.Equal(t, msg, prompt, "manual prompt should preserve the user message verbatim when commands are already inlined")
}

func TestBuildUserPrompt_ManualSessionPrependsMissingCommand(t *testing.T) {
	t.Parallel()

	msg := "fix the bug"
	input := &agent.AgentInput{
		Manual:      true,
		UserMessage: msg,
		Commands: []models.SessionInputCommand{
			{Kind: "command", AgentType: models.AgentTypeClaudeCode, Name: "review", Token: "/review", Display: "/review", Arguments: "focus on auth"},
		},
	}

	prompt := buildUserPrompt(input)
	require.Contains(t, prompt, "/review focus on auth", "missing command tokens should be prepended so the agent still sees them")
	require.Contains(t, prompt, msg, "the original user message should still appear after the prepended commands")
}

func TestBuildUserPrompt_AutomationRunReturnsRawGoal(t *testing.T) {
	t.Parallel()

	goal := "Please review recently merged PRs, identify likely regressions, and open a follow-up fix if one is obvious."
	input := &agent.AgentInput{
		Issue:       &models.Issue{Title: "Nightly automation"},
		PromptStyle: agent.PromptStyleRawTask,
		UserMessage: goal,
		PMContext: &agent.PMTaskContext{
			Approach:  goal,
			Reasoning: "This should not be wrapped into a PM section.",
		},
	}

	prompt := buildUserPrompt(input)
	require.Equal(t, goal, prompt, "automation runs should send only the raw goal as the user prompt")
	require.NotContains(t, prompt, "## Issue:", "automation runs should not wrap the goal in issue headings")
	require.NotContains(t, prompt, "### Description", "automation runs should not wrap the goal in a description section")
}

// ---------------------------------------------------------------------------
// extractFileHints
// ---------------------------------------------------------------------------

func TestExtractFileHints(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		input     *agent.AgentInput
		wantFiles []string
		wantNil   bool
	}{
		{
			name: "non-sentry source returns nil",
			input: &agent.AgentInput{
				Issue: &models.Issue{Title: "Bug", Source: models.IssueSourceLinear},
			},
			wantNil: true,
		},
		{
			name: "empty raw data returns nil",
			input: &agent.AgentInput{
				Issue: &models.Issue{Title: "Bug", Source: models.IssueSourceSentry, RawData: nil},
			},
			wantNil: true,
		},
		{
			name: "extracts filenames from sentry frames",
			input: &agent.AgentInput{
				Issue: &models.Issue{
					Title:  "Bug",
					Source: models.IssueSourceSentry,
					RawData: json.RawMessage(`{
						"entries": [{
							"type": "exception",
							"data": {
								"values": [{
									"stacktrace": {
										"frames": [
											{"filename": "src/handler.go", "absPath": ""},
											{"filename": "src/service.go", "absPath": "/app/src/service.go"}
										]
									}
								}]
							}
						}]
					}`),
				},
			},
			wantFiles: []string{"src/handler.go", "/app/src/service.go"},
		},
		{
			name: "skips standard lib and vendor frames",
			input: &agent.AgentInput{
				Issue: &models.Issue{
					Title:  "Bug",
					Source: models.IssueSourceSentry,
					RawData: json.RawMessage(`{
						"entries": [{
							"type": "exception",
							"data": {
								"values": [{
									"stacktrace": {
										"frames": [
											{"filename": "<frozen importlib>"},
											{"filename": "node_modules/express/lib/router.js"},
											{"filename": "site-packages/django/core/handlers.py"},
											{"filename": "src/app.go"}
										]
									}
								}]
							}
						}]
					}`),
				},
			},
			wantFiles: []string{"src/app.go"},
		},
		{
			name: "deduplicates paths",
			input: &agent.AgentInput{
				Issue: &models.Issue{
					Title:  "Bug",
					Source: models.IssueSourceSentry,
					RawData: json.RawMessage(`{
						"entries": [{
							"type": "exception",
							"data": {
								"values": [{
									"stacktrace": {
										"frames": [
											{"filename": "src/app.go"},
											{"filename": "src/app.go"}
										]
									}
								}]
							}
						}]
					}`),
				},
			},
			wantFiles: []string{"src/app.go"},
		},
		{
			name: "skips non-exception entries",
			input: &agent.AgentInput{
				Issue: &models.Issue{
					Title:  "Bug",
					Source: models.IssueSourceSentry,
					RawData: json.RawMessage(`{
						"entries": [{
							"type": "breadcrumbs",
							"data": {
								"values": [{
									"stacktrace": {
										"frames": [{"filename": "should_not_appear.go"}]
									}
								}]
							}
						}]
					}`),
				},
			},
			wantNil: true,
		},
		{
			name: "invalid JSON returns nil",
			input: &agent.AgentInput{
				Issue: &models.Issue{
					Title:   "Bug",
					Source:  models.IssueSourceSentry,
					RawData: json.RawMessage(`{not valid json`),
				},
			},
			wantNil: true,
		},
		{
			name: "absPath preferred over filename",
			input: &agent.AgentInput{
				Issue: &models.Issue{
					Title:  "Bug",
					Source: models.IssueSourceSentry,
					RawData: json.RawMessage(`{
						"entries": [{
							"type": "exception",
							"data": {
								"values": [{
									"stacktrace": {
										"frames": [
											{"filename": "handler.go", "absPath": "/app/src/handler.go"}
										]
									}
								}]
							}
						}]
					}`),
				},
			},
			wantFiles: []string{"/app/src/handler.go"},
		},
		{
			name: "skips frames with empty path",
			input: &agent.AgentInput{
				Issue: &models.Issue{
					Title:  "Bug",
					Source: models.IssueSourceSentry,
					RawData: json.RawMessage(`{
						"entries": [{
							"type": "exception",
							"data": {
								"values": [{
									"stacktrace": {
										"frames": [
											{"filename": "", "absPath": ""},
											{"filename": "src/real.go"}
										]
									}
								}]
							}
						}]
					}`),
				},
			},
			wantFiles: []string{"src/real.go"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			files := extractFileHints(tt.input)
			if tt.wantNil {
				require.Nil(t, files)
			} else {
				require.Equal(t, tt.wantFiles, files)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// extractStackTrace
// ---------------------------------------------------------------------------

func TestExtractStackTrace(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		rawData json.RawMessage
		want    []string // substrings expected in result
		wantNil bool
	}{
		{
			name:    "empty raw data",
			rawData: nil,
			wantNil: true,
		},
		{
			name:    "invalid JSON",
			rawData: json.RawMessage(`{broken`),
			wantNil: true,
		},
		{
			name: "valid sentry data",
			rawData: json.RawMessage(`{
				"entries": [{
					"type": "exception",
					"data": {
						"values": [{
							"type": "TypeError",
							"value": "null is not an object",
							"stacktrace": {
								"frames": [{
									"filename": "app.js",
									"function": "handleRequest",
									"lineNo": 42
								},{
									"filename": "router.js",
									"function": "dispatch",
									"lineNo": 100
								}]
							}
						}]
					}
				}]
			}`),
			want: []string{"TypeError: null is not an object", "handleRequest", "app.js:42", "dispatch", "router.js:100"},
		},
		{
			name: "skips non-exception entries",
			rawData: json.RawMessage(`{
				"entries": [{
					"type": "breadcrumbs",
					"data": {"values": []}
				}]
			}`),
			wantNil: true,
		},
		{
			name: "multiple exception values",
			rawData: json.RawMessage(`{
				"entries": [{
					"type": "exception",
					"data": {
						"values": [
							{
								"type": "RootError",
								"value": "connection refused",
								"stacktrace": {"frames": [{"filename": "db.go", "function": "connect", "lineNo": 10}]}
							},
							{
								"type": "WrapperError",
								"value": "init failed",
								"stacktrace": {"frames": [{"filename": "main.go", "function": "init", "lineNo": 5}]}
							}
						]
					}
				}]
			}`),
			want: []string{"RootError: connection refused", "WrapperError: init failed", "db.go:10", "main.go:5"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := extractStackTrace(tt.rawData)
			if tt.wantNil {
				require.Empty(t, result)
				return
			}
			for _, s := range tt.want {
				require.Contains(t, result, s)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// composeFreshExecPrompt
// ---------------------------------------------------------------------------

func TestComposeFreshExecPrompt_PlainPromptKeepsSystemFirst(t *testing.T) {
	t.Parallel()

	got := composeFreshExecPrompt("system context", "fix the failing test")
	require.Equal(t, "system context\n\n---\n\nfix the failing test", got)
}

func TestComposeFreshExecPrompt_SlashCommandMovesUserFirst(t *testing.T) {
	t.Parallel()

	user := "/review the current workspace diff\n\nFix nits when they are local."
	got := composeFreshExecPrompt("system context", user)
	require.True(t, strings.HasPrefix(got, "/review "), "slash command must be at position 0 so the CLI dispatches its native handler")
	require.Contains(t, got, "system context", "system prompt should still reach the model")
	require.Equal(t, user+"\n\n---\n\nsystem context", got)
}

func TestComposeFreshExecPrompt_SlashCommandWithEmptySystemPromptReturnsUserOnly(t *testing.T) {
	t.Parallel()

	got := composeFreshExecPrompt("   \n\t", "/review go")
	require.Equal(t, "/review go", got, "empty system prompts should not leave a trailing separator")
}

func TestComposeFreshExecPrompt_LeadingWhitespaceBeforeSlashStillFlips(t *testing.T) {
	t.Parallel()

	got := composeFreshExecPrompt("system context", "   /review go")
	require.True(t, strings.HasPrefix(got, "   /review go"), "leading whitespace should still count as a slash-command opener")
}

func TestUserPromptStartsWithSlashCommand(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		in   string
		want bool
	}{
		{"plain text", "fix the bug", false},
		{"slash with args", "/review the diff", true},
		{"slash with newline", "/review\nfix nits", true},
		{"slash only", "/review", true},
		{"slash then non-identifier", "/foo!bar", false},
		{"slash with hyphen", "/code-review now", true},
		{"slash with colon namespace", "/plugin:review go", true},
		{"lone slash", "/", false},
		{"middle of line", "see /review later", false},
		{"empty", "", false},
		{"only whitespace", "   \n\t", false},
		{"leading whitespace then slash", "  \t/review go", true},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tc.want, userPromptStartsWithSlashCommand(tc.in))
		})
	}
}
