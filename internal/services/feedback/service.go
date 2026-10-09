// Package feedback implements the review feedback loop: capturing PR review
// comments, classifying them via LLM, deduplicating against existing memories,
// and managing the learned memories knowledge base.
package feedback

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/prompts"
)

// LLMClient abstracts the LLM call for classification.
type LLMClient interface {
	Complete(ctx context.Context, systemPrompt, userPrompt string) (string, error)
}

// ReviewCommentStore defines the DB operations for review comments.
type ReviewCommentStore interface {
	Create(ctx context.Context, c *models.ReviewComment) error
	GetByID(ctx context.Context, orgID, id uuid.UUID) (models.ReviewComment, error)
	UpdateClassification(ctx context.Context, orgID, id uuid.UUID, filterStatus string, category *string, actionable, generalizable bool, generalizedRule, summary *string) error
	MarkApplied(ctx context.Context, orgID, id uuid.UUID) error
	ListActionableByPullRequest(ctx context.Context, orgID, prID uuid.UUID) ([]models.ReviewComment, error)
}

// MemoryStore defines the DB operations for memories (learned conventions).
type MemoryStore interface {
	ApplyReviewComment(ctx context.Context, orgID, commentID uuid.UUID, repo, rule, category string) error
	ListActiveByRepo(ctx context.Context, orgID uuid.UUID, repo string) ([]models.Memory, error)
	UpdateMemory(ctx context.Context, orgID, id uuid.UUID, rule *string, status *string) error
}

// JobStore defines the job enqueue operations.
type JobStore interface {
	Enqueue(ctx context.Context, orgID uuid.UUID, queue, jobType string, payload any, priority int, dedupeKey *string) (uuid.UUID, error)
}

// Service implements the review feedback processing pipeline.
type Service struct {
	comments ReviewCommentStore
	memories MemoryStore
	jobs     JobStore
	llm      LLMClient
	logger   zerolog.Logger
}

// NewService creates a new feedback service.
func NewService(
	comments ReviewCommentStore,
	memories MemoryStore,
	jobs JobStore,
	llm LLMClient,
	logger zerolog.Logger,
) *Service {
	return &Service{
		comments: comments,
		memories: memories,
		jobs:     jobs,
		llm:      llm,
		logger:   logger,
	}
}

// ProcessComment runs the full processing pipeline on a single review comment:
// structural pre-filter → LLM classification → pattern dedup/creation.
func (s *Service) ProcessComment(ctx context.Context, commentID, orgID uuid.UUID) error {
	comment, err := s.comments.GetByID(ctx, orgID, commentID)
	if err != nil {
		return fmt.Errorf("get review comment: %w", err)
	}

	if comment.FilterStatus != "pending" {
		return nil // already processed
	}

	// 1. Structural pre-filter.
	if !passesStructuralFilter(comment.Reviewer, comment.Body) {
		return s.comments.UpdateClassification(ctx, orgID, comment.ID,
			"filtered_structural", nil, false, false, nil, nil)
	}

	// 2. LLM classification.
	classification, err := s.classifyComment(ctx, &comment)
	if err != nil {
		return fmt.Errorf("classify review comment: %w", err)
	}

	if !classification.Actionable {
		return s.comments.UpdateClassification(ctx, orgID, comment.ID,
			"filtered_not_actionable", classificationCategory(classification.Category), false, false, nil, &classification.Summary)
	}

	// 3. Store classification.
	if err := s.comments.UpdateClassification(ctx, orgID, comment.ID,
		"accepted", &classification.Category, true, classification.Generalizable,
		classification.GeneralizedRule, &classification.Summary); err != nil {
		return fmt.Errorf("update comment classification: %w", err)
	}

	// Memory dedup/creation is handled by UpdateMemories, called by the worker
	// handler which has the repo name from the job payload.

	return nil
}

// GetProcessedComment retrieves a comment by ID for checking its classification result.
func (s *Service) GetProcessedComment(ctx context.Context, commentID, orgID uuid.UUID) (*models.ReviewComment, error) {
	comment, err := s.comments.GetByID(ctx, orgID, commentID)
	if err != nil {
		return nil, err
	}
	return &comment, nil
}

// classificationResult holds the LLM classification output.
type classificationResult struct {
	Actionable      bool    `json:"actionable"`
	Category        string  `json:"category"`
	Summary         string  `json:"summary"`
	Generalizable   bool    `json:"generalizable"`
	GeneralizedRule *string `json:"generalized_rule"`
}

func (s *Service) classifyComment(ctx context.Context, comment *models.ReviewComment) (*classificationResult, error) {
	if s.llm == nil {
		return &classificationResult{
			Actionable: true,
			Category:   "nit",
			Summary:    comment.Body,
		}, nil
	}

	systemPrompt := prompts.ReviewCommentPrompt()

	diffContext := ""
	if comment.DiffPath != nil {
		diffContext = fmt.Sprintf("File: %s", *comment.DiffPath)
		if comment.DiffPosition != nil {
			diffContext += fmt.Sprintf(", position: %d", *comment.DiffPosition)
		}
	}

	userPrompt := prompts.ReviewCommentUserPrompt(prompts.ReviewCommentUserPromptData{
		DiffContext: diffContext,
		CommentBody: comment.Body,
	})

	response, err := s.llm.Complete(ctx, systemPrompt, userPrompt)
	if err != nil {
		return nil, fmt.Errorf("LLM classification: %w", err)
	}

	// Extract JSON from the response (handle markdown fences if present).
	jsonStr := extractJSON(response)

	var result classificationResult
	if err := json.Unmarshal([]byte(jsonStr), &result); err != nil {
		return nil, fmt.Errorf("parse LLM classification response: %w", err)
	}

	if err := models.ReviewFeedbackCategory(result.Category).Validate(); err != nil {
		if result.Actionable {
			return nil, fmt.Errorf("validate actionable classification: %w", err)
		}
		// The nullable category contract permits acknowledgments and other
		// non-actionable responses with no meaningful category.
		result.Category = ""
	}
	if result.Actionable && result.Generalizable && (result.GeneralizedRule == nil || strings.TrimSpace(*result.GeneralizedRule) == "") {
		return nil, fmt.Errorf("generalizable classification requires a nonempty rule")
	}
	return &result, nil
}

func classificationCategory(category string) *string {
	if category == "" {
		return nil
	}
	return &category
}

// extractJSON extracts the first JSON object from a string, handling markdown fences.
func extractJSON(s string) string {
	// Try to find JSON within markdown code fences first.
	if idx := strings.Index(s, "```json"); idx != -1 {
		start := idx + 7
		end := strings.Index(s[start:], "```")
		if end != -1 {
			return strings.TrimSpace(s[start : start+end])
		}
	}
	if idx := strings.Index(s, "```"); idx != -1 {
		start := idx + 3
		end := strings.Index(s[start:], "```")
		if end != -1 {
			return strings.TrimSpace(s[start : start+end])
		}
	}
	// Try raw JSON.
	start := strings.Index(s, "{")
	end := strings.LastIndex(s, "}")
	if start != -1 && end > start {
		return s[start : end+1]
	}
	return s
}

// UpdateMemories performs dedup and creates/updates memories for a classified comment.
// This is called by the worker handler which has the repo name from the PR.
func (s *Service) UpdateMemories(ctx context.Context, orgID, commentID uuid.UUID, repo, rule, category string) error {
	if strings.TrimSpace(repo) == "" || strings.TrimSpace(rule) == "" {
		return fmt.Errorf("review memory requires a repository and nonempty rule")
	}
	if err := models.ReviewFeedbackCategory(category).Validate(); err != nil {
		return fmt.Errorf("validate review memory category: %w", err)
	}
	return s.memories.ApplyReviewComment(ctx, orgID, commentID, repo, rule, category)
}

// GenerateConventionsDoc generates the .143/learned-conventions.md content
// from all active memories for a repo.
func (s *Service) GenerateConventionsDoc(ctx context.Context, orgID uuid.UUID, repo string) (string, error) {
	memories, err := s.memories.ListActiveByRepo(ctx, orgID, repo)
	if err != nil {
		return "", fmt.Errorf("list active memories: %w", err)
	}

	if len(memories) == 0 {
		return "", nil
	}

	var b strings.Builder
	b.WriteString("# 143 Learned Conventions\n")
	b.WriteString("#\n")
	b.WriteString("# This file is auto-generated from learned memories observed by 143.dev.\n")
	b.WriteString("# Manual edits are preserved — the system will not overwrite lines you change.\n")
	b.WriteString("#\n\n")

	// Group by category.
	grouped := make(map[string][]models.Memory)
	var categories []string
	for _, m := range memories {
		if _, exists := grouped[m.Category]; !exists {
			categories = append(categories, m.Category)
		}
		grouped[m.Category] = append(grouped[m.Category], m)
	}

	categoryTitles := map[string]string{
		"style":          "Style",
		"logic_bug":      "Logic",
		"edge_case":      "Edge Cases",
		"wrong_approach": "Architecture",
		"missing_test":   "Testing",
		"security":       "Security",
		"performance":    "Performance",
		"nit":            "Nits",
	}

	for _, cat := range categories {
		title := categoryTitles[cat]
		if title == "" {
			// Capitalize the first letter manually to avoid deprecated strings.Title.
			if len(cat) > 0 {
				title = strings.ToUpper(cat[:1]) + cat[1:]
			}
		}
		fmt.Fprintf(&b, "## %s\n", title)
		for _, m := range grouped[cat] {
			fmt.Fprintf(&b, "- %s\n", m.Rule)
			fmt.Fprintf(&b, "  (%d occurrences)\n", m.OccurrenceCount)
		}
		b.WriteString("\n")
	}

	return b.String(), nil
}

// FormatRevisionFeedback formats actionable review comments into a string
// suitable for injection into the agent's revision prompt.
func (s *Service) FormatRevisionFeedback(ctx context.Context, orgID, prID uuid.UUID) (string, error) {
	comments, err := s.comments.ListActionableByPullRequest(ctx, orgID, prID)
	if err != nil {
		return "", fmt.Errorf("list actionable comments: %w", err)
	}

	if len(comments) == 0 {
		return "", nil
	}

	var b strings.Builder
	for i, c := range comments {
		fmt.Fprintf(&b, "%d. [%s] @%s: %s\n", i+1, strOr(c.Category, "general"), c.Reviewer, c.Body)
		if c.DiffPath != nil {
			fmt.Fprintf(&b, "   File: %s", *c.DiffPath)
			if c.DiffPosition != nil {
				fmt.Fprintf(&b, ", line: %d", *c.DiffPosition)
			}
			b.WriteString("\n")
		}
	}

	return b.String(), nil
}

func strOr(s *string, fallback string) string {
	if s != nil {
		return *s
	}
	return fallback
}

// --- Structural pre-filter ---

var (
	botPatterns = []string{
		"[bot]", "dependabot", "codecov", "github-actions",
		"renovate", "greenkeeper", "snyk", "sonarcloud",
		"coveralls", "codeclimate",
	}

	emojiOnlyRegexp = regexp.MustCompile(`^[\s\p{So}\p{Sk}\p{Sm}]+$`)
	ciPatterns      = []string{
		"<!-- ", "Coverage Report", "Build Status", "CI Results",
		"This comment was automatically generated",
	}
)

// passesStructuralFilter returns true if the comment should proceed to LLM classification.
func passesStructuralFilter(reviewer, body string) bool {
	// Skip bot accounts.
	reviewerLower := strings.ToLower(reviewer)
	for _, bot := range botPatterns {
		if strings.Contains(reviewerLower, bot) {
			return false
		}
	}

	// Skip short comments.
	trimmed := strings.TrimSpace(body)
	if len(trimmed) < 20 {
		return false
	}

	// Skip pure emoji.
	if emojiOnlyRegexp.MatchString(trimmed) {
		return false
	}

	// Skip auto-generated CI comments.
	for _, pattern := range ciPatterns {
		if strings.Contains(body, pattern) {
			return false
		}
	}

	return true
}
