package feedback

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/models"
)

// ---------------------------------------------------------------------------
// Mock implementations
// ---------------------------------------------------------------------------

type mockLLMClient struct {
	completeFn func(ctx context.Context, systemPrompt, userPrompt string) (string, error)
}

func (m *mockLLMClient) Complete(ctx context.Context, systemPrompt, userPrompt string) (string, error) {
	return m.completeFn(ctx, systemPrompt, userPrompt)
}

type mockReviewCommentStore struct {
	createFn                      func(ctx context.Context, c *models.ReviewComment) error
	getByIDFn                     func(ctx context.Context, orgID, id uuid.UUID) (models.ReviewComment, error)
	updateClassificationFn        func(ctx context.Context, orgID, id uuid.UUID, filterStatus string, category *string, actionable, generalizable bool, generalizedRule, summary *string) error
	markAppliedFn                 func(ctx context.Context, orgID, id uuid.UUID) error
	listActionableByPullRequestFn func(ctx context.Context, orgID, prID uuid.UUID) ([]models.ReviewComment, error)
}

func (m *mockReviewCommentStore) Create(ctx context.Context, c *models.ReviewComment) error {
	if m.createFn != nil {
		return m.createFn(ctx, c)
	}
	return nil
}

func (m *mockReviewCommentStore) GetByID(ctx context.Context, orgID, id uuid.UUID) (models.ReviewComment, error) {
	if m.getByIDFn != nil {
		return m.getByIDFn(ctx, orgID, id)
	}
	return models.ReviewComment{}, nil
}

func (m *mockReviewCommentStore) UpdateClassification(ctx context.Context, orgID, id uuid.UUID, filterStatus string, category *string, actionable, generalizable bool, generalizedRule, summary *string) error {
	if m.updateClassificationFn != nil {
		return m.updateClassificationFn(ctx, orgID, id, filterStatus, category, actionable, generalizable, generalizedRule, summary)
	}
	return nil
}

func (m *mockReviewCommentStore) MarkApplied(ctx context.Context, orgID, id uuid.UUID) error {
	if m.markAppliedFn != nil {
		return m.markAppliedFn(ctx, orgID, id)
	}
	return nil
}

func (m *mockReviewCommentStore) ListActionableByPullRequest(ctx context.Context, orgID, prID uuid.UUID) ([]models.ReviewComment, error) {
	if m.listActionableByPullRequestFn != nil {
		return m.listActionableByPullRequestFn(ctx, orgID, prID)
	}
	return nil, nil
}

type mockReviewPatternStore struct {
	createFn              func(ctx context.Context, p *models.ReviewPattern) error
	getByIDFn             func(ctx context.Context, orgID, id uuid.UUID) (models.ReviewPattern, error)
	findMatchingRuleFn    func(ctx context.Context, orgID uuid.UUID, repo, normalizedRule string) (models.ReviewPattern, error)
	incrementOccurrenceFn func(ctx context.Context, orgID, patternID, commentID uuid.UUID) error
	listActiveByRepoFn    func(ctx context.Context, orgID uuid.UUID, repo string) ([]models.ReviewPattern, error)
	updatePatternFn       func(ctx context.Context, orgID, id uuid.UUID, rule *string, status *string) error
}

func (m *mockReviewPatternStore) Create(ctx context.Context, p *models.ReviewPattern) error {
	if m.createFn != nil {
		return m.createFn(ctx, p)
	}
	return nil
}

func (m *mockReviewPatternStore) GetByID(ctx context.Context, orgID, id uuid.UUID) (models.ReviewPattern, error) {
	if m.getByIDFn != nil {
		return m.getByIDFn(ctx, orgID, id)
	}
	return models.ReviewPattern{}, nil
}

func (m *mockReviewPatternStore) FindMatchingRule(ctx context.Context, orgID uuid.UUID, repo, normalizedRule string) (models.ReviewPattern, error) {
	if m.findMatchingRuleFn != nil {
		return m.findMatchingRuleFn(ctx, orgID, repo, normalizedRule)
	}
	return models.ReviewPattern{}, errors.New("no matching rule")
}

func (m *mockReviewPatternStore) IncrementOccurrence(ctx context.Context, orgID, patternID, commentID uuid.UUID) error {
	if m.incrementOccurrenceFn != nil {
		return m.incrementOccurrenceFn(ctx, orgID, patternID, commentID)
	}
	return nil
}

func (m *mockReviewPatternStore) ListActiveByRepo(ctx context.Context, orgID uuid.UUID, repo string) ([]models.ReviewPattern, error) {
	if m.listActiveByRepoFn != nil {
		return m.listActiveByRepoFn(ctx, orgID, repo)
	}
	return nil, nil
}

func (m *mockReviewPatternStore) UpdatePattern(ctx context.Context, orgID, id uuid.UUID, rule *string, status *string) error {
	if m.updatePatternFn != nil {
		return m.updatePatternFn(ctx, orgID, id, rule, status)
	}
	return nil
}

type mockJobStore struct {
	enqueueFn func(ctx context.Context, orgID uuid.UUID, queue, jobType string, payload any, priority int, dedupeKey *string) (uuid.UUID, error)
}

func (m *mockJobStore) Enqueue(ctx context.Context, orgID uuid.UUID, queue, jobType string, payload any, priority int, dedupeKey *string) (uuid.UUID, error) {
	if m.enqueueFn != nil {
		return m.enqueueFn(ctx, orgID, queue, jobType, payload, priority, dedupeKey)
	}
	return uuid.New(), nil
}

// ---------------------------------------------------------------------------
// Helpers
// ---------------------------------------------------------------------------

func strPtr(s string) *string { return &s }
func intPtr(i int) *int       { return &i }

func newTestService(
	comments ReviewCommentStore,
	patterns ReviewPatternStore,
	jobs JobStore,
	llm LLMClient,
) *Service {
	return NewService(comments, patterns, jobs, llm, zerolog.Nop())
}

// ---------------------------------------------------------------------------
// Tests: passesStructuralFilter
// ---------------------------------------------------------------------------

func TestPassesStructuralFilter(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		reviewer string
		body     string
		expected bool
	}{
		{
			name:     "bot accounts are filtered",
			reviewer: "dependabot[bot]",
			body:     "Bumps lodash from 4.17.20 to 4.17.21.",
			expected: false,
		},
		{
			name:     "short comments are filtered",
			reviewer: "human-reviewer",
			body:     "LGTM",
			expected: false,
		},
		{
			name:     "pure emoji comments are filtered",
			reviewer: "human-reviewer",
			body:     "  \U0001F44D \U0001F44D \U0001F44D \U0001F44D \U0001F44D  ",
			expected: false,
		},
		{
			name:     "CI auto-generated comments are filtered",
			reviewer: "human-reviewer",
			body:     "<!-- Coverage Report -->\nThis comment was automatically generated by some CI tool with details.",
			expected: false,
		},
		{
			name:     "valid human comments pass the filter",
			reviewer: "human-reviewer",
			body:     "This function should handle the nil case before dereferencing the pointer to avoid panics.",
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result := passesStructuralFilter(tt.reviewer, tt.body)
			require.Equal(t, tt.expected, result, "passesStructuralFilter should return expected result for case: %s", tt.name)
		})
	}
}

// ---------------------------------------------------------------------------
// Tests: ProcessComment
// ---------------------------------------------------------------------------

func TestProcessComment(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name                     string
		commentID                uuid.UUID
		orgID                    uuid.UUID
		setupCommentStore        func(commentID, orgID uuid.UUID) *mockReviewCommentStore
		setupPatternStore        func() *mockReviewPatternStore
		setupLLM                 func() *mockLLMClient
		expectErr                bool
		expectedUpdateFilterStat string // the filter_status passed to UpdateClassification, if any
	}{
		{
			name:      "already processed comment is skipped",
			commentID: uuid.New(),
			orgID:     uuid.New(),
			setupCommentStore: func(commentID, orgID uuid.UUID) *mockReviewCommentStore {
				return &mockReviewCommentStore{
					getByIDFn: func(ctx context.Context, oID, cID uuid.UUID) (models.ReviewComment, error) {
						return models.ReviewComment{
							ID:           commentID,
							OrgID:        orgID,
							FilterStatus: "accepted",
							Reviewer:     "human",
							Body:         "Some comment that is long enough to pass the filter easily.",
						}, nil
					},
				}
			},
			setupPatternStore: func() *mockReviewPatternStore {
				return &mockReviewPatternStore{}
			},
			setupLLM: func() *mockLLMClient {
				return &mockLLMClient{
					completeFn: func(ctx context.Context, sys, usr string) (string, error) {
						t.Error("LLM should not be called for already processed comments")
						return "", nil
					},
				}
			},
			expectErr: false,
		},
		{
			name:      "structural filter rejects bot comment",
			commentID: uuid.New(),
			orgID:     uuid.New(),
			setupCommentStore: func(commentID, orgID uuid.UUID) *mockReviewCommentStore {
				var capturedFilterStatus string
				return &mockReviewCommentStore{
					getByIDFn: func(ctx context.Context, oID, cID uuid.UUID) (models.ReviewComment, error) {
						return models.ReviewComment{
							ID:           commentID,
							OrgID:        orgID,
							FilterStatus: "pending",
							Reviewer:     "codecov[bot]",
							Body:         "Coverage report for this pull request is available.",
						}, nil
					},
					updateClassificationFn: func(ctx context.Context, oID, cID uuid.UUID, filterStatus string, category *string, actionable, generalizable bool, generalizedRule, summary *string) error {
						capturedFilterStatus = filterStatus
						require.Equal(t, "filtered_structural", capturedFilterStatus, "filter status should be filtered_structural for bot comments")
						require.False(t, actionable, "bot comment should not be marked actionable")
						return nil
					},
				}
			},
			setupPatternStore: func() *mockReviewPatternStore {
				return &mockReviewPatternStore{}
			},
			setupLLM: func() *mockLLMClient {
				return &mockLLMClient{
					completeFn: func(ctx context.Context, sys, usr string) (string, error) {
						t.Error("LLM should not be called when structural filter rejects")
						return "", nil
					},
				}
			},
			expectErr:                false,
			expectedUpdateFilterStat: "filtered_structural",
		},
		{
			name:      "LLM classifies as not actionable",
			commentID: uuid.New(),
			orgID:     uuid.New(),
			setupCommentStore: func(commentID, orgID uuid.UUID) *mockReviewCommentStore {
				return &mockReviewCommentStore{
					getByIDFn: func(ctx context.Context, oID, cID uuid.UUID) (models.ReviewComment, error) {
						return models.ReviewComment{
							ID:           commentID,
							OrgID:        orgID,
							FilterStatus: "pending",
							Reviewer:     "human-reviewer",
							Body:         "This is a non-actionable observation about the code that does not need changes.",
						}, nil
					},
					updateClassificationFn: func(ctx context.Context, oID, cID uuid.UUID, filterStatus string, category *string, actionable, generalizable bool, generalizedRule, summary *string) error {
						require.Equal(t, "filtered_not_actionable", filterStatus, "filter status should be filtered_not_actionable")
						require.False(t, actionable, "comment should not be marked actionable")
						require.NotNil(t, category, "category should be set even for non-actionable comments")
						require.Equal(t, "nit", *category, "category should match LLM response")
						return nil
					},
				}
			},
			setupPatternStore: func() *mockReviewPatternStore {
				return &mockReviewPatternStore{}
			},
			setupLLM: func() *mockLLMClient {
				resp := classificationResult{
					Actionable:    false,
					Category:      "nit",
					Summary:       "Observation about code style",
					Generalizable: false,
				}
				respJSON, _ := json.Marshal(resp)
				return &mockLLMClient{
					completeFn: func(ctx context.Context, sys, usr string) (string, error) {
						return string(respJSON), nil
					},
				}
			},
			expectErr: false,
		},
		{
			name:      "LLM classifies as actionable and generalizable",
			commentID: uuid.New(),
			orgID:     uuid.New(),
			setupCommentStore: func(commentID, orgID uuid.UUID) *mockReviewCommentStore {
				updateCallCount := 0
				return &mockReviewCommentStore{
					getByIDFn: func(ctx context.Context, oID, cID uuid.UUID) (models.ReviewComment, error) {
						return models.ReviewComment{
							ID:            commentID,
							OrgID:         orgID,
							PullRequestID: uuid.New(),
							FilterStatus:  "pending",
							Reviewer:      "senior-dev",
							Body:          "Always check for nil pointers before dereferencing to prevent panics in production.",
						}, nil
					},
					updateClassificationFn: func(ctx context.Context, oID, cID uuid.UUID, filterStatus string, category *string, actionable, generalizable bool, generalizedRule, summary *string) error {
						updateCallCount++
						require.Equal(t, "accepted", filterStatus, "filter status should be accepted for actionable comment")
						require.True(t, actionable, "comment should be marked actionable")
						require.True(t, generalizable, "comment should be marked generalizable")
						require.NotNil(t, generalizedRule, "generalized rule should be set")
						require.Equal(t, "Always check pointers for nil before dereferencing", *generalizedRule, "generalized rule should match LLM response")
						return nil
					},
				}
			},
			setupPatternStore: func() *mockReviewPatternStore {
				return &mockReviewPatternStore{}
			},
			setupLLM: func() *mockLLMClient {
				rule := "Always check pointers for nil before dereferencing"
				resp := classificationResult{
					Actionable:      true,
					Category:        "logic_bug",
					Summary:         "Check nil pointer before deref",
					Generalizable:   true,
					GeneralizedRule: &rule,
				}
				respJSON, _ := json.Marshal(resp)
				return &mockLLMClient{
					completeFn: func(ctx context.Context, sys, usr string) (string, error) {
						return string(respJSON), nil
					},
				}
			},
			expectErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			commentStore := tt.setupCommentStore(tt.commentID, tt.orgID)
			patternStore := tt.setupPatternStore()
			llm := tt.setupLLM()
			svc := newTestService(commentStore, patternStore, &mockJobStore{}, llm)

			err := svc.ProcessComment(context.Background(), tt.commentID, tt.orgID)
			if tt.expectErr {
				require.Error(t, err, "ProcessComment should return an error")
			} else {
				require.NoError(t, err, "ProcessComment should not return an error")
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Tests: UpdatePatterns
// ---------------------------------------------------------------------------

func TestUpdatePatterns(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name              string
		orgID             uuid.UUID
		commentID         uuid.UUID
		repo              string
		rule              string
		category          string
		setupPatternStore func(orgID, commentID uuid.UUID) *mockReviewPatternStore
		expectErr         bool
	}{
		{
			name:      "creates new pattern when no match exists",
			orgID:     uuid.New(),
			commentID: uuid.New(),
			repo:      "my-org/my-repo",
			rule:      "Always validate user input before processing.",
			category:  "security",
			setupPatternStore: func(orgID, commentID uuid.UUID) *mockReviewPatternStore {
				return &mockReviewPatternStore{
					findMatchingRuleFn: func(ctx context.Context, oID uuid.UUID, repo, normalizedRule string) (models.ReviewPattern, error) {
						return models.ReviewPattern{}, errors.New("no matching rule found")
					},
					createFn: func(ctx context.Context, p *models.ReviewPattern) error {
						require.Equal(t, orgID, p.OrgID, "pattern org_id should match")
						require.Equal(t, "my-org/my-repo", p.Repo, "pattern repo should match")
						require.Equal(t, "Always validate user input before processing.", p.Rule, "pattern rule should match the original rule text")
						require.Equal(t, "security", p.Category, "pattern category should match")
						require.Equal(t, 1, p.OccurrenceCount, "new pattern should have occurrence count of 1")
						require.Equal(t, "candidate", p.Status, "new pattern should have candidate status")
						require.Len(t, p.SourceCommentIDs, 1, "new pattern should have one source comment ID")
						require.Equal(t, commentID, p.SourceCommentIDs[0], "source comment ID should match")
						return nil
					},
				}
			},
			expectErr: false,
		},
		{
			name:      "increments existing pattern on match",
			orgID:     uuid.New(),
			commentID: uuid.New(),
			repo:      "my-org/my-repo",
			rule:      "Always validate user input before processing.",
			category:  "security",
			setupPatternStore: func(orgID, commentID uuid.UUID) *mockReviewPatternStore {
				existingPatternID := uuid.New()
				return &mockReviewPatternStore{
					findMatchingRuleFn: func(ctx context.Context, oID uuid.UUID, repo, normalizedRule string) (models.ReviewPattern, error) {
						return models.ReviewPattern{
							ID:              existingPatternID,
							OrgID:           orgID,
							Repo:            "my-org/my-repo",
							Rule:            "Always validate user input before processing.",
							Category:        "security",
							OccurrenceCount: 3,
							Status:          "candidate",
						}, nil
					},
					incrementOccurrenceFn: func(ctx context.Context, oID, patternID, cID uuid.UUID) error {
						require.Equal(t, orgID, oID, "org_id should match when incrementing occurrence")
						require.Equal(t, existingPatternID, patternID, "pattern ID should match the existing pattern")
						require.Equal(t, commentID, cID, "comment ID should match when incrementing occurrence")
						return nil
					},
					createFn: func(ctx context.Context, p *models.ReviewPattern) error {
						t.Error("Create should not be called when a matching pattern already exists")
						return nil
					},
				}
			},
			expectErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			patternStore := tt.setupPatternStore(tt.orgID, tt.commentID)
			svc := newTestService(&mockReviewCommentStore{}, patternStore, &mockJobStore{}, nil)

			err := svc.UpdatePatterns(context.Background(), tt.orgID, tt.commentID, tt.repo, tt.rule, tt.category)
			if tt.expectErr {
				require.Error(t, err, "UpdatePatterns should return an error")
			} else {
				require.NoError(t, err, "UpdatePatterns should not return an error")
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Tests: FormatRevisionFeedback
// ---------------------------------------------------------------------------

func TestFormatRevisionFeedback(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name              string
		orgID             uuid.UUID
		prID              uuid.UUID
		setupCommentStore func(orgID, prID uuid.UUID) *mockReviewCommentStore
		expected          string
		expectErr         bool
	}{
		{
			name:  "formats actionable comments with file and line info",
			orgID: uuid.New(),
			prID:  uuid.New(),
			setupCommentStore: func(orgID, prID uuid.UUID) *mockReviewCommentStore {
				return &mockReviewCommentStore{
					listActionableByPullRequestFn: func(ctx context.Context, oID, pID uuid.UUID) ([]models.ReviewComment, error) {
						return []models.ReviewComment{
							{
								Reviewer:     "alice",
								Body:         "Handle the error from db.Query",
								Category:     strPtr("logic_bug"),
								DiffPath:     strPtr("internal/db/store.go"),
								DiffPosition: intPtr(42),
							},
							{
								Reviewer: "bob",
								Body:     "Add a test for the empty case",
								Category: strPtr("missing_test"),
							},
						}, nil
					},
				}
			},
			expected: "1. [logic_bug] @alice: Handle the error from db.Query\n" +
				"   File: internal/db/store.go, line: 42\n" +
				"2. [missing_test] @bob: Add a test for the empty case\n",
			expectErr: false,
		},
		{
			name:  "returns empty string when no actionable comments",
			orgID: uuid.New(),
			prID:  uuid.New(),
			setupCommentStore: func(orgID, prID uuid.UUID) *mockReviewCommentStore {
				return &mockReviewCommentStore{
					listActionableByPullRequestFn: func(ctx context.Context, oID, pID uuid.UUID) ([]models.ReviewComment, error) {
						return []models.ReviewComment{}, nil
					},
				}
			},
			expected:  "",
			expectErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			commentStore := tt.setupCommentStore(tt.orgID, tt.prID)
			svc := newTestService(commentStore, &mockReviewPatternStore{}, &mockJobStore{}, nil)

			result, err := svc.FormatRevisionFeedback(context.Background(), tt.orgID, tt.prID)
			if tt.expectErr {
				require.Error(t, err, "FormatRevisionFeedback should return an error")
			} else {
				require.NoError(t, err, "FormatRevisionFeedback should not return an error")
				require.Equal(t, tt.expected, result, "FormatRevisionFeedback should return the expected formatted string")
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Tests: GenerateConventionsDoc
// ---------------------------------------------------------------------------

func TestGenerateConventionsDoc(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name              string
		orgID             uuid.UUID
		repo              string
		setupPatternStore func(orgID uuid.UUID) *mockReviewPatternStore
		expectContains    []string
		expectEmpty       bool
		expectErr         bool
	}{
		{
			name:  "generates markdown from active patterns",
			orgID: uuid.New(),
			repo:  "my-org/my-repo",
			setupPatternStore: func(orgID uuid.UUID) *mockReviewPatternStore {
				return &mockReviewPatternStore{
					listActiveByRepoFn: func(ctx context.Context, oID uuid.UUID, repo string) ([]models.ReviewPattern, error) {
						return []models.ReviewPattern{
							{
								Rule:            "Always check nil pointers before dereferencing",
								Category:        "logic_bug",
								OccurrenceCount: 5,
							},
							{
								Rule:            "Use require instead of assert in tests",
								Category:        "style",
								OccurrenceCount: 3,
							},
						}, nil
					},
				}
			},
			expectContains: []string{
				"# 143 Learned Conventions",
				"## Logic",
				"- Always check nil pointers before dereferencing",
				"(5 occurrences)",
				"## Style",
				"- Use require instead of assert in tests",
				"(3 occurrences)",
			},
			expectEmpty: false,
			expectErr:   false,
		},
		{
			name:  "returns empty string when no active patterns exist",
			orgID: uuid.New(),
			repo:  "my-org/empty-repo",
			setupPatternStore: func(orgID uuid.UUID) *mockReviewPatternStore {
				return &mockReviewPatternStore{
					listActiveByRepoFn: func(ctx context.Context, oID uuid.UUID, repo string) ([]models.ReviewPattern, error) {
						return []models.ReviewPattern{}, nil
					},
				}
			},
			expectEmpty: true,
			expectErr:   false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			patternStore := tt.setupPatternStore(tt.orgID)
			svc := newTestService(&mockReviewCommentStore{}, patternStore, &mockJobStore{}, nil)

			result, err := svc.GenerateConventionsDoc(context.Background(), tt.orgID, tt.repo)
			if tt.expectErr {
				require.Error(t, err, "GenerateConventionsDoc should return an error")
				return
			}
			require.NoError(t, err, "GenerateConventionsDoc should not return an error")

			if tt.expectEmpty {
				require.Empty(t, result, "GenerateConventionsDoc should return empty string when no patterns exist")
				return
			}

			for _, substr := range tt.expectContains {
				require.Contains(t, result, substr, "GenerateConventionsDoc output should contain: %s", substr)
			}
		})
	}
}

// ---------------------------------------------------------------------------
// Tests: normalizeRule
// ---------------------------------------------------------------------------

func TestNormalizeRule(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "lowercases the rule",
			input:    "Always Check Pointers",
			expected: "always check pointers",
		},
		{
			name:     "strips punctuation",
			input:    "Always check pointers! Don't forget.",
			expected: "always check pointers dont forget",
		},
		{
			name:     "collapses whitespace",
			input:    "  always   check   pointers  ",
			expected: "always check pointers",
		},
		{
			name:     "handles combined normalization",
			input:    "  Use `require` — not `assert`!  ",
			expected: "use `require` not `assert`",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result := normalizeRule(tt.input)
			require.Equal(t, tt.expected, result, "normalizeRule should produce the expected normalized output")
		})
	}
}

// ---------------------------------------------------------------------------
// Tests: extractJSON
// ---------------------------------------------------------------------------

func TestExtractJSON(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "extracts JSON from markdown json fence",
			input:    "Here is the result:\n```json\n{\"actionable\": true}\n```\nDone.",
			expected: `{"actionable": true}`,
		},
		{
			name:     "extracts JSON from plain markdown fence",
			input:    "```\n{\"actionable\": false}\n```",
			expected: `{"actionable": false}`,
		},
		{
			name:     "extracts raw JSON object",
			input:    `Some preamble {"actionable": true, "category": "nit"} trailing text`,
			expected: `{"actionable": true, "category": "nit"}`,
		},
		{
			name:     "returns input unchanged when no JSON found",
			input:    "no json here",
			expected: "no json here",
		},
		{
			name: "handles multiline JSON in code fence",
			input: fmt.Sprintf("```json\n%s\n```", `{
  "actionable": true,
  "category": "style"
}`),
			expected: `{
  "actionable": true,
  "category": "style"
}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			result := extractJSON(tt.input)
			require.Equal(t, tt.expected, result, "extractJSON should extract the expected JSON string")
		})
	}
}
