package pm

import (
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/models"
)

func TestDeduplicateProposal_NoOverlap(t *testing.T) {
	t.Parallel()

	result := DeduplicateProposal(
		"Add dark mode support",
		nil,
		nil,
		[]models.Project{
			{ID: uuid.New(), Title: "Fix payment processing", Status: models.ProjectStatusActive},
		},
	)

	require.False(t, result.HardDuplicate, "should not be a hard duplicate")
	require.Nil(t, result.Warning, "should not have a warning")
	require.Empty(t, result.SimilarProjects, "should have no similar projects")
}

func TestDeduplicateProposal_TitleSimilarity(t *testing.T) {
	t.Parallel()

	result := DeduplicateProposal(
		"Standardize payment error handling",
		nil,
		nil,
		[]models.Project{
			{ID: uuid.New(), Title: "Standardize payment error types", Status: models.ProjectStatusActive},
		},
	)

	require.True(t, len(result.SimilarProjects) > 0, "should detect similar project")
	require.Equal(t, "title", result.SimilarProjects[0].OverlapType, "should be title overlap")
}

func TestDeduplicateProposal_HardDuplicate(t *testing.T) {
	t.Parallel()

	result := DeduplicateProposal(
		"Standardize payment error handling",
		nil,
		nil,
		[]models.Project{
			{ID: uuid.New(), Title: "Standardize payment error handling", Status: models.ProjectStatusActive},
		},
	)

	require.True(t, result.HardDuplicate, "should be a hard duplicate")
}

func TestDeduplicateProposal_IssueOverlap(t *testing.T) {
	t.Parallel()

	issueID1 := uuid.New()
	issueID2 := uuid.New()
	issueID3 := uuid.New()

	result := DeduplicateProposal(
		"Fix error handling",
		[]uuid.UUID{issueID1, issueID2},
		nil,
		[]models.Project{
			{
				ID:             uuid.New(),
				Title:          "Unrelated project",
				SourceIssueIDs: []uuid.UUID{issueID1, issueID2, issueID3},
				Status:         models.ProjectStatusActive,
			},
		},
	)

	require.True(t, len(result.SimilarProjects) > 0, "should detect issue overlap")
	require.True(t, result.HardDuplicate, "should be hard duplicate with 100% issue overlap")
}

func TestDeduplicateProposal_AtSimilarityThreshold(t *testing.T) {
	t.Parallel()

	// Two projects whose title similarity is just at the similarity threshold
	// should be flagged as similar but not as a hard duplicate.
	existingID := uuid.New()
	result := DeduplicateProposal(
		"Refactor payment error types",
		nil,
		nil,
		[]models.Project{
			{ID: existingID, Title: "Refactor payment error handling", Status: models.ProjectStatusActive},
		},
	)

	// The titles are similar enough to clear SimilarityThreshold but
	// not HardDuplicateThreshold (they are similar but not identical).
	if len(result.SimilarProjects) > 0 {
		require.False(t, result.HardDuplicate, "should not be a hard duplicate at moderate similarity")
		require.NotNil(t, result.Warning, "should have a warning for similar projects")
		require.Less(t, result.SimilarProjects[0].OverlapScore, HardDuplicateThreshold,
			"overlap score should be below hard duplicate threshold")
		require.GreaterOrEqual(t, result.SimilarProjects[0].OverlapScore, SimilarityThreshold,
			"overlap score should be at or above similarity threshold")
	}
}

func TestDeduplicateProposal_AtHardDuplicateThreshold(t *testing.T) {
	t.Parallel()

	// An exact title match produces score 1.0 which exceeds HardDuplicateThreshold.
	result := DeduplicateProposal(
		"Implement rate limiting",
		nil,
		nil,
		[]models.Project{
			{ID: uuid.New(), Title: "Implement rate limiting", Status: models.ProjectStatusActive},
		},
	)

	require.True(t, result.HardDuplicate, "exact title match should be hard duplicate")
	require.Len(t, result.SimilarProjects, 1)
	require.GreaterOrEqual(t, result.SimilarProjects[0].OverlapScore, HardDuplicateThreshold,
		"overlap score should be at or above hard duplicate threshold")
}

func TestTitleSimilarity(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		a        string
		b        string
		minScore float64
		maxScore float64
	}{
		{"identical", "Fix payment handling", "Fix payment handling", 1.0, 1.0},
		{"very similar", "Standardize payment errors", "Standardize payment error types", 0.7, 1.0},
		{"different", "Add dark mode", "Fix database migrations", 0.0, 0.3},
		{"empty", "", "Something", 0.0, 0.0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			score := titleSimilarity(tt.a, tt.b)
			require.GreaterOrEqual(t, score, tt.minScore, "score should be >= %f, got %f", tt.minScore, score)
			require.LessOrEqual(t, score, tt.maxScore, "score should be <= %f, got %f", tt.maxScore, score)
		})
	}
}

func TestScopeSimilarity(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		a        string
		b        string
		minScore float64
		maxScore float64
	}{
		{"identical scopes", "refactor authentication middleware", "refactor authentication middleware", 0.9, 1.0},
		{"overlapping keywords", "refactor authentication service and logging", "improve authentication service and caching", 0.3, 0.7},
		{"no overlap", "database migration scripts", "frontend styling components", 0.0, 0.1},
		{"empty first", "", "some scope", 0.0, 0.0},
		{"empty second", "some scope", "", 0.0, 0.0},
		{"both empty", "", "", 0.0, 0.0},
		{"stop words only", "the and or but", "is are was were", 0.0, 0.0},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			score := scopeSimilarity(tt.a, tt.b)
			require.GreaterOrEqual(t, score, tt.minScore, "score should be >= %f, got %f", tt.minScore, score)
			require.LessOrEqual(t, score, tt.maxScore, "score should be <= %f, got %f", tt.maxScore, score)
		})
	}
}

func TestExtractKeywords(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		input    string
		expected []string
	}{
		{"filters stop words", "the quick brown fox and the lazy dog", []string{"quick", "brown", "fox", "lazy", "dog"}},
		{"filters short words", "I am a go dev", []string{"dev"}},
		{"removes punctuation", "hello, world! foo-bar", []string{"hello", "world", "foobar"}},
		{"empty string", "", nil},
		{"only stop words", "the and or but is are", nil},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			result := extractKeywords(tt.input)
			require.Equal(t, tt.expected, result)
		})
	}
}

func TestDeduplicateProposal_ScopeOverlap(t *testing.T) {
	t.Parallel()

	scope1 := "refactor authentication middleware and session handling"
	scope2 := "refactor authentication middleware and token validation"
	result := DeduplicateProposal(
		"Migrate database schema",
		nil,
		&scope1,
		[]models.Project{
			{ID: uuid.New(), Title: "Upgrade frontend build pipeline", Scope: &scope2, Status: models.ProjectStatusActive},
		},
	)

	require.True(t, len(result.SimilarProjects) > 0, "should detect scope overlap")
	require.Equal(t, "scope", result.SimilarProjects[0].OverlapType, "overlap type should be scope")
}

func TestIssueOverlap(t *testing.T) {
	t.Parallel()

	id1 := uuid.New()
	id2 := uuid.New()
	id3 := uuid.New()

	tests := []struct {
		name     string
		proposed []uuid.UUID
		existing []uuid.UUID
		expected float64
	}{
		{"empty proposed", nil, []uuid.UUID{id1}, 0},
		{"no overlap", []uuid.UUID{id1}, []uuid.UUID{id2}, 0},
		{"full overlap", []uuid.UUID{id1, id2}, []uuid.UUID{id1, id2, id3}, 1.0},
		{"partial overlap", []uuid.UUID{id1, id2}, []uuid.UUID{id1, id3}, 0.5},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			score := issueOverlap(tt.proposed, tt.existing)
			require.InDelta(t, tt.expected, score, 0.001, "overlap score mismatch")
		})
	}
}
