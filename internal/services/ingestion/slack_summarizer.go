package ingestion

import (
	"context"
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/rs/zerolog"
	"golang.org/x/sync/errgroup"

	"github.com/assembledhq/143/internal/llm"
)

const slackSummarizerSystemPrompt = `Analyze this Slack conversation and return a JSON object with these fields:
- "actionable": boolean — true if this contains a bug report, feature request, customer issue, or work item
- "category": one of "bug_report", "feature_request", "customer_issue", "discussion", "not_actionable"
- "summary": string — max 200 characters summarizing what was discussed and any action needed
- "urgency": one of "high", "medium", "low", "none"

Return ONLY the JSON object, no other text.`

var trivialPattern = regexp.MustCompile(`(?i)^(\s*|ok|okay|thanks|thank you|ty|thx|lgtm|👍|🎉|✅|💯|sure|yes|no|yep|nope|np|got it|sounds good|will do)\s*$`)

// SlackSummarizer uses a cheap LLM to analyze Slack threads.
type SlackSummarizer struct {
	llm    llm.Client
	model  string
	logger zerolog.Logger
}

// NewSlackSummarizer creates a new summarizer.
func NewSlackSummarizer(client llm.Client, model string, logger zerolog.Logger) *SlackSummarizer {
	return &SlackSummarizer{
		llm:    client,
		model:  model,
		logger: logger,
	}
}

// shouldSummarize applies pre-filter heuristics.
func shouldSummarize(messages []SlackMessage) bool {
	if len(messages) == 0 {
		return false
	}

	// Single message must be at least 50 chars
	if len(messages) == 1 {
		return utf8.RuneCountInString(messages[0].Text) >= 50
	}

	// Thread must have at least 100 chars total
	totalChars := 0
	nonTrivialCount := 0
	for _, msg := range messages {
		text := strings.TrimSpace(msg.Text)
		if !trivialPattern.MatchString(text) {
			nonTrivialCount++
		}
		totalChars += utf8.RuneCountInString(text)
	}

	// Need at least one non-trivial message and 100 chars total
	return nonTrivialCount > 0 && totalChars >= 100
}

// SummarizeThreads analyzes threads and returns them with analysis attached.
// LLM calls are parallelized with bounded concurrency.
func (s *SlackSummarizer) SummarizeThreads(ctx context.Context, threads []SlackThreadSummary) ([]SlackThreadSummary, error) {
	// Pre-filter: mark trivial threads without LLM calls.
	type llmWork struct {
		index int
		text  string
	}
	var pending []llmWork

	for i := range threads {
		var messages []SlackMessage
		if err := json.Unmarshal(threads[i].Messages, &messages); err != nil {
			s.logger.Warn().Err(err).Str("thread", threads[i].ThreadTS).Msg("failed to unmarshal thread messages for summarization")
			continue
		}

		if !shouldSummarize(messages) {
			threads[i].Analysis = &ThreadAnalysis{
				Actionable: false,
				Category:   "not_actionable",
				Summary:    "Thread too short or trivial",
				Urgency:    "none",
			}
			continue
		}

		var sb strings.Builder
		for _, msg := range messages {
			fmt.Fprintf(&sb, "[%s] %s: %s\n", msg.Timestamp, msg.User, msg.Text)
		}
		pending = append(pending, llmWork{index: i, text: sb.String()})
	}

	// Run LLM calls concurrently with bounded parallelism.
	const maxConcurrent = 5
	g, gctx := errgroup.WithContext(ctx)
	g.SetLimit(maxConcurrent)

	var mu sync.Mutex
	for _, work := range pending {
		work := work
		g.Go(func() error {
			analysis, err := s.analyzeThread(gctx, work.text)

			mu.Lock()
			defer mu.Unlock()

			if err != nil {
				s.logger.Warn().Err(err).Str("thread", threads[work.index].ThreadTS).Msg("failed to analyze thread, marking as discussion")
				summary := work.text
				if utf8.RuneCountInString(summary) > 200 {
					summary = string([]rune(summary)[:200])
				}
				threads[work.index].Analysis = &ThreadAnalysis{
					Actionable: true,
					Category:   "discussion",
					Summary:    summary,
					Urgency:    "low",
				}
				return nil // graceful fallback, don't fail the group
			}
			threads[work.index].Analysis = analysis
			return nil
		})
	}

	if err := g.Wait(); err != nil {
		return threads, fmt.Errorf("summarize threads: %w", err)
	}

	return threads, nil
}

func (s *SlackSummarizer) analyzeThread(ctx context.Context, conversationText string) (*ThreadAnalysis, error) {
	resp, err := s.llm.Complete(ctx, slackSummarizerSystemPrompt, conversationText)
	if err != nil {
		return nil, fmt.Errorf("llm complete: %w", err)
	}

	var analysis ThreadAnalysis
	if err := json.Unmarshal([]byte(resp), &analysis); err != nil {
		// Try to extract JSON from the response
		start := strings.Index(resp, "{")
		end := strings.LastIndex(resp, "}")
		if start >= 0 && end > start {
			if err2 := json.Unmarshal([]byte(resp[start:end+1]), &analysis); err2 != nil {
				return nil, fmt.Errorf("parse llm response: %w", err2)
			}
		} else {
			return nil, fmt.Errorf("no JSON in llm response: %s", resp)
		}
	}

	// Enforce 200 char limit on summary
	if utf8.RuneCountInString(analysis.Summary) > 200 {
		analysis.Summary = string([]rune(analysis.Summary)[:200])
	}

	return &analysis, nil
}
