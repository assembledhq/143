package pm

import (
	"strings"
	"unicode"

	"github.com/google/uuid"

	"github.com/assembledhq/143/internal/models"
)

const (
	// SimilarityThreshold is the minimum overlap score for a project to be
	// flagged as similar to a proposal.
	SimilarityThreshold = 0.5

	// HardDuplicateThreshold is the overlap score at or above which a
	// proposal is rejected as a hard duplicate.
	HardDuplicateThreshold = 0.85
)

// DedupResult describes the outcome of deduplication analysis.
type DedupResult struct {
	HardDuplicate   bool
	Warning         *string
	SimilarProjects []models.ProposalOverlap
}

// DeduplicateProposal checks a proposed project against existing same-repo projects.
// It returns a DedupResult indicating whether the proposal should be rejected,
// accepted with warnings, or accepted cleanly.
func DeduplicateProposal(
	title string,
	sourceIssueIDs []uuid.UUID,
	scope *string,
	existingProjects []models.Project,
) DedupResult {
	var result DedupResult

	for _, existing := range existingProjects {
		var maxScore float64
		var overlapType, explanation string

		// Heuristic 1: Issue overlap
		if len(sourceIssueIDs) > 0 && len(existing.SourceIssueIDs) > 0 {
			overlap := issueOverlap(sourceIssueIDs, existing.SourceIssueIDs)
			if overlap > maxScore {
				maxScore = overlap
				overlapType = "issue"
				explanation = "Shares motivating issues with existing project"
			}
		}

		// Heuristic 2: Title similarity
		titleSim := titleSimilarity(title, existing.Title)
		if titleSim > maxScore {
			maxScore = titleSim
			overlapType = "title"
			explanation = "Similar project title"
		}

		// Heuristic 3: Scope overlap (keyword-based)
		if scope != nil && existing.Scope != nil {
			scopeSim := scopeSimilarity(*scope, *existing.Scope)
			if scopeSim > maxScore {
				maxScore = scopeSim
				overlapType = "scope"
				explanation = "Overlapping project scope"
			}
		}

		if maxScore >= SimilarityThreshold {
			result.SimilarProjects = append(result.SimilarProjects, models.ProposalOverlap{
				ProjectID:    existing.ID,
				Title:        existing.Title,
				OverlapScore: maxScore,
				OverlapType:  overlapType,
				Explanation:  explanation,
			})
		}

		if maxScore >= HardDuplicateThreshold {
			result.HardDuplicate = true
		}
	}

	if !result.HardDuplicate && len(result.SimilarProjects) > 0 {
		w := "Proposed project has similarities with existing projects"
		result.Warning = &w
	}

	return result
}

// issueOverlap returns the fraction of proposed source issues that overlap
// with an existing project's source issues. Returns 0 if either set is empty.
// The metric is intentionally asymmetric: |intersection| / |proposed|.
// This answers "are the proposal's motivating issues already covered?"
// rather than "do these two projects share all issues?".
func issueOverlap(proposed, existing []uuid.UUID) float64 {
	if len(proposed) == 0 {
		return 0
	}
	existingSet := make(map[uuid.UUID]struct{}, len(existing))
	for _, id := range existing {
		existingSet[id] = struct{}{}
	}
	var overlap int
	for _, id := range proposed {
		if _, ok := existingSet[id]; ok {
			overlap++
		}
	}
	return float64(overlap) / float64(len(proposed))
}

// titleSimilarity computes a simple normalized bigram similarity between two titles.
// Returns a value between 0 and 1.
func titleSimilarity(a, b string) float64 {
	a = normalizeText(a)
	b = normalizeText(b)
	if a == b {
		return 1.0
	}
	if len(a) < 2 || len(b) < 2 {
		return 0
	}

	bigramsA := bigramMultiset(a)
	bigramsB := bigramMultiset(b)

	// Sum of min counts for each bigram = multiset intersection size.
	var intersect int
	for bg, countA := range bigramsA {
		if countB, ok := bigramsB[bg]; ok {
			if countA < countB {
				intersect += countA
			} else {
				intersect += countB
			}
		}
	}

	totalA := bigramCount(bigramsA)
	totalB := bigramCount(bigramsB)
	if totalA+totalB == 0 {
		return 0
	}
	// Dice coefficient over multisets
	return 2.0 * float64(intersect) / float64(totalA+totalB)
}

// scopeSimilarity computes keyword overlap between two scope descriptions.
func scopeSimilarity(a, b string) float64 {
	wordsA := extractKeywords(a)
	wordsB := extractKeywords(b)
	if len(wordsA) == 0 || len(wordsB) == 0 {
		return 0
	}

	setB := make(map[string]struct{}, len(wordsB))
	for _, w := range wordsB {
		setB[w] = struct{}{}
	}

	var overlap int
	for _, w := range wordsA {
		if _, ok := setB[w]; ok {
			overlap++
		}
	}

	// Jaccard similarity
	union := len(wordsA) + len(wordsB) - overlap
	if union == 0 {
		return 0
	}
	return float64(overlap) / float64(union)
}

// normalizeText lowercases and removes non-alphanumeric characters.
func normalizeText(s string) string {
	s = strings.ToLower(s)
	var b strings.Builder
	for _, r := range s {
		if unicode.IsLetter(r) || unicode.IsDigit(r) || r == ' ' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// bigramMultiset returns a frequency map of character bigrams from a string.
// Using a multiset (rather than a set) ensures repeated bigrams like "aa" in
// "aaa" are counted correctly for the Dice coefficient.
func bigramMultiset(s string) map[string]int {
	result := make(map[string]int)
	runes := []rune(s)
	for i := 0; i < len(runes)-1; i++ {
		bg := string(runes[i : i+2])
		result[bg]++
	}
	return result
}

// bigramCount returns the total number of bigrams in a multiset.
func bigramCount(m map[string]int) int {
	var n int
	for _, c := range m {
		n += c
	}
	return n
}

// stopWords is the set of common English words excluded from keyword extraction.
var stopWords = map[string]struct{}{
	"a": {}, "an": {}, "the": {}, "and": {}, "or": {}, "but": {}, "in": {},
	"on": {}, "at": {}, "to": {}, "for": {}, "of": {}, "with": {}, "by": {},
	"is": {}, "are": {}, "was": {}, "were": {}, "be": {}, "been": {},
	"not": {}, "no": {}, "this": {}, "that": {}, "it": {}, "its": {},
	"from": {}, "as": {}, "will": {}, "should": {}, "would": {}, "can": {},
}

// extractKeywords splits text into lowercase words, filtering out stop words and short words.
func extractKeywords(s string) []string {
	words := strings.Fields(strings.ToLower(s))
	var keywords []string
	for _, w := range words {
		// Remove non-alphanumeric
		var clean strings.Builder
		for _, r := range w {
			if unicode.IsLetter(r) || unicode.IsDigit(r) {
				clean.WriteRune(r)
			}
		}
		word := clean.String()
		if len(word) < 3 {
			continue
		}
		if _, stop := stopWords[word]; stop {
			continue
		}
		keywords = append(keywords, word)
	}
	return keywords
}
