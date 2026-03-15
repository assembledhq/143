// Package memory implements strength-scored memory retrieval for agent context
// injection. Memories are ranked by a composite score of frequency, recency,
// and relevance, then selected within a token budget to keep agent prompts lean.
package memory

import (
	"context"
	"fmt"
	"math"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"

	"github.com/assembledhq/143/internal/models"
)

var (
	memoriesInjectedTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "memory_context_injections_total",
		Help: "Total number of times memories were injected into agent context",
	})
	memoriesInjectedCount = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "memory_context_injected_count",
		Help:    "Number of memories selected per context injection",
		Buckets: []float64{0, 1, 2, 5, 10, 20, 50},
	})
	memoriesReinforcedTotal = promauto.NewCounter(prometheus.CounterOpts{
		Name: "memory_reinforcements_total",
		Help: "Total number of memory reinforcement operations (on PR approval)",
	})
	memoriesReinforcedCount = promauto.NewHistogram(prometheus.HistogramOpts{
		Name:    "memory_reinforced_count",
		Help:    "Number of memories reinforced per PR approval",
		Buckets: []float64{0, 1, 2, 5, 10, 20, 50},
	})
)

// MemoryStore defines the DB operations needed by the memory service.
type MemoryStore interface {
	ListForContext(ctx context.Context, orgID uuid.UUID, repo string) ([]models.Memory, error)
	ReinforceBatch(ctx context.Context, orgID uuid.UUID, memoryIDs []uuid.UUID) error
}

// Service provides scored memory retrieval and reinforcement.
type Service struct {
	store MemoryStore
}

// NewService creates a new memory service.
func NewService(store MemoryStore) *Service {
	return &Service{store: store}
}

// ContextRequest describes the current agent context for memory selection.
type ContextRequest struct {
	OrgID     uuid.UUID
	Repo      string
	FilePaths []string // files being modified (for relevance scoring)
}

// ScoredMemory pairs a memory with its computed strength score.
type ScoredMemory struct {
	Memory        models.Memory
	Score         float64
	TokenEstimate int
}

// ContextResult contains the selected memories and formatted context string.
type ContextResult struct {
	Memories   []ScoredMemory
	Formatted  string
	TokensUsed int
	MemoryIDs  []uuid.UUID // IDs of injected memories (for later reinforcement)
}

const (
	// DefaultTokenBudget is the max tokens allocated for memory context.
	// ~2K tokens keeps the memory section lean while providing useful guidance.
	DefaultTokenBudget = 2048

	// recencyHalfLifeDays controls how fast recency decays. At 30 days since
	// last use, recency_factor ≈ 0.5. At 60 days, ≈ 0.25.
	recencyHalfLifeDays = 30.0

	// charsPerToken is a rough approximation for token estimation.
	charsPerToken = 4
)

// GetContextMemories retrieves and scores memories for injection into an agent
// prompt. Returns the top memories that fit within the token budget, formatted
// as markdown grouped by category.
func (s *Service) GetContextMemories(ctx context.Context, req ContextRequest) (*ContextResult, error) {
	return s.GetContextMemoriesWithBudget(ctx, req, DefaultTokenBudget)
}

// GetContextMemoriesWithBudget is like GetContextMemories but accepts a custom
// token budget.
func (s *Service) GetContextMemoriesWithBudget(ctx context.Context, req ContextRequest, tokenBudget int) (*ContextResult, error) {
	memories, err := s.store.ListForContext(ctx, req.OrgID, req.Repo)
	if err != nil {
		return nil, fmt.Errorf("list memories for context: %w", err)
	}

	if len(memories) == 0 {
		return &ContextResult{}, nil
	}

	now := time.Now()

	// Score all memories.
	scored := make([]ScoredMemory, 0, len(memories))
	for _, m := range memories {
		score := computeStrength(m, now, req.FilePaths)
		tokenEst := estimateTokens(m.Rule, m.Category)
		scored = append(scored, ScoredMemory{
			Memory:        m,
			Score:         score,
			TokenEstimate: tokenEst,
		})
	}

	// Sort descending by score.
	sort.Slice(scored, func(i, j int) bool {
		return scored[i].Score > scored[j].Score
	})

	// Select top memories within token budget, reserving space for formatting.
	headerTokens := estimateTokens("## Learned Conventions\n\nFollow these project conventions when making changes:\n\n", "")
	remaining := tokenBudget - headerTokens
	var selected []ScoredMemory
	var memoryIDs []uuid.UUID

	seenCategories := make(map[string]bool)
	for _, sm := range scored {
		cost := sm.TokenEstimate + 5 // 5 tokens for "- " prefix and newline overhead
		// Account for category header ("### category\n") on first memory per category.
		cat := sm.Memory.Category
		if cat == "" {
			cat = "general"
		}
		if !seenCategories[cat] {
			cost += estimateTokens("### "+cat+"\n", "")
		}
		if remaining < cost {
			continue // skip this one, try smaller memories
		}
		remaining -= cost
		seenCategories[cat] = true
		selected = append(selected, sm)
		memoryIDs = append(memoryIDs, sm.Memory.ID)
	}

	if len(selected) == 0 {
		return &ContextResult{}, nil
	}

	memoriesInjectedTotal.Inc()
	memoriesInjectedCount.Observe(float64(len(selected)))

	formatted := formatMemories(selected)
	tokensUsed := tokenBudget - remaining

	return &ContextResult{
		Memories:   selected,
		Formatted:  formatted,
		TokensUsed: tokensUsed,
		MemoryIDs:  memoryIDs,
	}, nil
}

// ReinforceMemories increments times_reinforced and updates last_used_at for
// the given memory IDs. Called when a PR that used these memories is approved.
func (s *Service) ReinforceMemories(ctx context.Context, orgID uuid.UUID, memoryIDs []uuid.UUID) error {
	if len(memoryIDs) == 0 {
		return nil
	}
	memoriesReinforcedTotal.Inc()
	memoriesReinforcedCount.Observe(float64(len(memoryIDs)))
	return s.store.ReinforceBatch(ctx, orgID, memoryIDs)
}

// computeStrength calculates the composite strength score for a memory:
//
//	strength = frequency_factor × recency_factor × relevance_factor
//
// frequency_factor: log2(times_reinforced + 1) — diminishing returns, avoids
// runaway scores for heavily-reinforced memories.
//
// recency_factor: exp(-days_since_last_used / 30) — 30-day half-life. Memories
// used yesterday score ~1.0, memories unused for 60 days score ~0.25.
//
// relevance_factor: 1.0 if memory has no file_patterns (applies broadly) or if
// any pattern matches the current file paths. 0.1 if patterns exist but none
// match — still included at low priority rather than excluded entirely.
func computeStrength(m models.Memory, now time.Time, filePaths []string) float64 {
	// Frequency: log2(times_reinforced + 1), min 0.1 for new memories.
	freq := math.Log2(float64(m.TimesReinforced + 1))
	if freq < 0.1 {
		freq = 0.1
	}

	// Recency: exponential decay with 30-day half-life.
	var daysSinceUsed float64
	if m.LastUsedAt != nil {
		daysSinceUsed = now.Sub(*m.LastUsedAt).Hours() / 24.0
	} else {
		daysSinceUsed = now.Sub(m.CreatedAt).Hours() / 24.0
	}
	if daysSinceUsed < 0 {
		daysSinceUsed = 0
	}
	recency := math.Exp(-daysSinceUsed / recencyHalfLifeDays)

	// Relevance: file pattern matching.
	relevance := computeRelevance(m.FilePatterns, filePaths)

	return freq * recency * relevance
}

// computeRelevance scores how relevant a memory is to the current file paths.
// Returns 1.0 if the memory has no file patterns (broadly applicable) or if
// any pattern matches. Returns 0.1 if patterns exist but don't match.
func computeRelevance(memoryPatterns, currentFiles []string) float64 {
	if len(memoryPatterns) == 0 {
		return 1.0 // no patterns = broadly applicable
	}
	if len(currentFiles) == 0 {
		return 0.5 // no file context provided, neutral score
	}

	for _, pattern := range memoryPatterns {
		for _, file := range currentFiles {
			if matchPattern(pattern, file) {
				return 1.0
			}
		}
	}

	return 0.1 // patterns exist but don't match
}

// matchPattern matches a file path against a pattern, supporting both standard
// filepath.Match globs (e.g. "*.go") and "**" for recursive directory matching
// (e.g. "src/**/*.go"). filepath.Match alone doesn't handle "**" or cross-
// directory matching, so we:
//  1. If the pattern contains "**", split on "**" and match segments.
//  2. Try the full path match — handles patterns like "src/*.go".
//  3. Try matching against just the basename — handles extension-only patterns
//     like "*.go" against paths like "internal/handler.go".
func matchPattern(pattern, file string) bool {
	if strings.Contains(pattern, "**") {
		return matchDoublestar(pattern, file)
	}

	// Full path match.
	if matched, err := filepath.Match(pattern, file); err == nil && matched {
		return true
	}
	// Basename match: allows "*.go" to match "src/internal/handler.go".
	if matched, err := filepath.Match(pattern, filepath.Base(file)); err == nil && matched {
		return true
	}
	return false
}

// matchDoublestar handles "**" glob patterns by splitting the pattern on "**"
// and checking that all segments appear in the file path in order. This is a
// lightweight alternative to importing a doublestar library.
//
// Examples:
//
//	"src/**/*.go"   matches "src/pkg/handler.go"
//	"**/*.test.ts"  matches "frontend/src/utils/api.test.ts"
//	"**/models/**"  matches "internal/models/user.go"
func matchDoublestar(pattern, file string) bool {
	parts := strings.Split(pattern, "**")

	// Check prefix (before first **).
	remaining := file
	if prefix := parts[0]; prefix != "" {
		prefix = strings.TrimSuffix(prefix, "/")
		if !strings.HasPrefix(remaining, prefix) {
			return false
		}
		remaining = remaining[len(prefix):]
		remaining = strings.TrimPrefix(remaining, "/")
	}

	// Check suffix (after last **) — this is the most common case: "**/*.go".
	if suffix := parts[len(parts)-1]; suffix != "" {
		suffix = strings.TrimPrefix(suffix, "/")
		// The suffix may contain globs, so match the basename against it.
		base := filepath.Base(file)
		if matched, err := filepath.Match(suffix, base); err == nil && matched {
			return true
		}
		// Also try matching the suffix against the remaining path tail.
		if matched, err := filepath.Match(suffix, filepath.Base(remaining)); err == nil && matched {
			return true
		}
		return false
	}

	// Pattern ends with "**" — matches everything under the prefix.
	return true
}

// estimateTokens returns a rough token count for a memory's contribution to
// the context string.
func estimateTokens(rule, category string) int {
	chars := len(rule) + len(category) + 4 // "- " prefix + newline
	tokens := chars / charsPerToken
	if tokens < 1 {
		tokens = 1
	}
	return tokens
}

// formatMemories renders scored memories as a markdown context section grouped
// by category. The output is designed to be injected directly into the agent
// system prompt.
func formatMemories(memories []ScoredMemory) string {
	var b strings.Builder
	b.WriteString("## Learned Conventions\n\n")
	b.WriteString("Follow these project conventions when making changes:\n\n")

	// Group by category, preserving score order within each group.
	type categoryGroup struct {
		name     string
		memories []ScoredMemory
	}
	groupMap := make(map[string]*categoryGroup)
	var groupOrder []string

	for _, sm := range memories {
		cat := sm.Memory.Category
		if cat == "" {
			cat = "general"
		}
		g, exists := groupMap[cat]
		if !exists {
			g = &categoryGroup{name: cat}
			groupMap[cat] = g
			groupOrder = append(groupOrder, cat)
		}
		g.memories = append(g.memories, sm)
	}

	for _, catName := range groupOrder {
		g := groupMap[catName]
		b.WriteString("### ")
		b.WriteString(g.name)
		b.WriteString("\n")
		for _, sm := range g.memories {
			b.WriteString("- ")
			b.WriteString(sm.Memory.Rule)
			b.WriteString("\n")
		}
		b.WriteString("\n")
	}

	return b.String()
}
