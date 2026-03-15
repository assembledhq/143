package memory

import (
	"context"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/models"
)

type mockMemoryStore struct {
	memories []models.Memory
	reinforced []uuid.UUID
}

func (m *mockMemoryStore) ListForContext(ctx context.Context, orgID uuid.UUID, repo string) ([]models.Memory, error) {
	return m.memories, nil
}

func (m *mockMemoryStore) ReinforceBatch(ctx context.Context, orgID uuid.UUID, memoryIDs []uuid.UUID) error {
	m.reinforced = append(m.reinforced, memoryIDs...)
	return nil
}

func TestComputeStrength_FrequencyScaling(t *testing.T) {
	t.Parallel()
	now := time.Now()

	// A memory used 0 times vs 10 times — frequency should scale sub-linearly.
	fresh := models.Memory{TimesReinforced: 0, LastUsedAt: &now, CreatedAt: now}
	veteran := models.Memory{TimesReinforced: 10, LastUsedAt: &now, CreatedAt: now}

	scoreFresh := computeStrength(fresh, now, nil)
	scoreVeteran := computeStrength(veteran, now, nil)

	require.Greater(t, scoreVeteran, scoreFresh, "veteran memory should score higher")
	// log2(11) ≈ 3.46, log2(1) = 0 → clamped to 0.1. Ratio should be ~34x, not 10x.
	require.Less(t, scoreVeteran/scoreFresh, 40.0, "frequency should have diminishing returns")
}

func TestComputeStrength_RecencyDecay(t *testing.T) {
	t.Parallel()
	now := time.Now()

	recent := now.Add(-1 * 24 * time.Hour) // 1 day ago
	old := now.Add(-60 * 24 * time.Hour)   // 60 days ago

	recentMem := models.Memory{TimesReinforced: 5, LastUsedAt: &recent, CreatedAt: now.Add(-90 * 24 * time.Hour)}
	oldMem := models.Memory{TimesReinforced: 5, LastUsedAt: &old, CreatedAt: now.Add(-90 * 24 * time.Hour)}

	scoreRecent := computeStrength(recentMem, now, nil)
	scoreOld := computeStrength(oldMem, now, nil)

	require.Greater(t, scoreRecent, scoreOld, "recent memory should score higher than old one")
	require.Greater(t, scoreRecent/scoreOld, 3.0, "60 days of decay should significantly reduce score")
}

func TestComputeRelevance_NoPatterns(t *testing.T) {
	t.Parallel()
	score := computeRelevance(nil, []string{"src/main.go"})
	require.Equal(t, 1.0, score, "no patterns = broadly applicable")
}

func TestComputeRelevance_MatchingPattern(t *testing.T) {
	t.Parallel()
	score := computeRelevance([]string{"*.go"}, []string{"src/main.go"})
	require.Equal(t, 1.0, score, "matching pattern should return 1.0")
}

func TestComputeRelevance_NonMatchingPattern(t *testing.T) {
	t.Parallel()
	score := computeRelevance([]string{"*.py"}, []string{"src/main.go"})
	require.Equal(t, 0.1, score, "non-matching pattern should return 0.1")
}

func TestComputeRelevance_NoFiles(t *testing.T) {
	t.Parallel()
	score := computeRelevance([]string{"*.go"}, nil)
	require.Equal(t, 0.5, score, "no file context should return neutral 0.5")
}

func TestGetContextMemories_EmptyStore(t *testing.T) {
	t.Parallel()

	store := &mockMemoryStore{memories: nil}
	svc := NewService(store)

	result, err := svc.GetContextMemories(context.Background(), ContextRequest{
		OrgID: uuid.New(),
		Repo:  "org/repo",
	})

	require.NoError(t, err)
	require.Empty(t, result.Memories)
	require.Empty(t, result.Formatted)
}

func TestGetContextMemories_SelectsWithinBudget(t *testing.T) {
	t.Parallel()

	now := time.Now()
	orgID := uuid.New()

	// Create 3 memories with different scores.
	memories := []models.Memory{
		{ID: uuid.New(), Rule: "Always handle errors", Category: "error-handling", TimesReinforced: 10, LastUsedAt: &now, CreatedAt: now},
		{ID: uuid.New(), Rule: "Use gofmt for formatting", Category: "style", TimesReinforced: 5, LastUsedAt: &now, CreatedAt: now},
		{ID: uuid.New(), Rule: "Never use global state", Category: "architecture", TimesReinforced: 1, LastUsedAt: &now, CreatedAt: now},
	}

	store := &mockMemoryStore{memories: memories}
	svc := NewService(store)

	result, err := svc.GetContextMemories(context.Background(), ContextRequest{
		OrgID: orgID,
		Repo:  "org/repo",
	})

	require.NoError(t, err)
	require.NotEmpty(t, result.Memories, "should select at least one memory")
	require.NotEmpty(t, result.Formatted, "should produce formatted output")
	require.Contains(t, result.Formatted, "## Learned Conventions")
	require.Equal(t, len(result.Memories), len(result.MemoryIDs), "MemoryIDs should match selected memories")

	// Highest-scored memory should be first.
	require.Equal(t, "Always handle errors", result.Memories[0].Memory.Rule)
}

func TestGetContextMemories_TightBudget(t *testing.T) {
	t.Parallel()

	now := time.Now()
	orgID := uuid.New()

	memories := []models.Memory{
		{ID: uuid.New(), Rule: "Always handle errors with context wrapping using fmt.Errorf", Category: "error-handling", TimesReinforced: 10, LastUsedAt: &now, CreatedAt: now},
		{ID: uuid.New(), Rule: "Use gofmt", Category: "style", TimesReinforced: 5, LastUsedAt: &now, CreatedAt: now},
	}

	store := &mockMemoryStore{memories: memories}
	svc := NewService(store)

	// Very tight budget — should still fit at least the short rule.
	result, err := svc.GetContextMemoriesWithBudget(context.Background(), ContextRequest{
		OrgID: orgID,
		Repo:  "org/repo",
	}, 50)

	require.NoError(t, err)
	require.LessOrEqual(t, result.TokensUsed, 50, "should respect token budget")
}

func TestGetContextMemories_ScoreOrdering(t *testing.T) {
	t.Parallel()

	now := time.Now()
	oldDate := now.Add(-90 * 24 * time.Hour) // 90 days ago
	orgID := uuid.New()

	memories := []models.Memory{
		{ID: uuid.New(), Rule: "Old and rarely used rule", Category: "general", TimesReinforced: 1, LastUsedAt: &oldDate, CreatedAt: oldDate},
		{ID: uuid.New(), Rule: "Fresh and frequently used rule", Category: "general", TimesReinforced: 20, LastUsedAt: &now, CreatedAt: now},
	}

	store := &mockMemoryStore{memories: memories}
	svc := NewService(store)

	result, err := svc.GetContextMemories(context.Background(), ContextRequest{
		OrgID: orgID,
		Repo:  "org/repo",
	})

	require.NoError(t, err)
	require.Len(t, result.Memories, 2)
	require.Equal(t, "Fresh and frequently used rule", result.Memories[0].Memory.Rule, "highest-scored should be first")
}

func TestReinforceMemories(t *testing.T) {
	t.Parallel()

	store := &mockMemoryStore{}
	svc := NewService(store)

	ids := []uuid.UUID{uuid.New(), uuid.New()}
	err := svc.ReinforceMemories(context.Background(), uuid.New(), ids)
	require.NoError(t, err)
	require.Equal(t, ids, store.reinforced)
}

func TestReinforceMemories_Empty(t *testing.T) {
	t.Parallel()

	store := &mockMemoryStore{}
	svc := NewService(store)

	err := svc.ReinforceMemories(context.Background(), uuid.New(), nil)
	require.NoError(t, err)
	require.Empty(t, store.reinforced, "should not call store for empty list")
}

func TestFormatMemories_GroupsByCategory(t *testing.T) {
	t.Parallel()

	memories := []ScoredMemory{
		{Memory: models.Memory{Rule: "Handle errors", Category: "error-handling"}, Score: 5.0},
		{Memory: models.Memory{Rule: "Use gofmt", Category: "style"}, Score: 4.0},
		{Memory: models.Memory{Rule: "Wrap errors with context", Category: "error-handling"}, Score: 3.0},
	}

	formatted := formatMemories(memories)
	require.Contains(t, formatted, "### error-handling")
	require.Contains(t, formatted, "### style")
	require.Contains(t, formatted, "- Handle errors")
	require.Contains(t, formatted, "- Use gofmt")
	require.Contains(t, formatted, "- Wrap errors with context")
}
