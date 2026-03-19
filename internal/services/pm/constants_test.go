package pm

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestContextLimitsForOrgSize(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		totalIssues    int
		expectedLimits contextLimits
	}{
		{
			name:           "zero issues returns small tier",
			totalIssues:    0,
			expectedLimits: limitsSmall,
		},
		{
			name:           "boundary at small max returns small tier",
			totalIssues:    tierSmallMax,
			expectedLimits: limitsSmall,
		},
		{
			name:           "one above small max returns medium tier",
			totalIssues:    tierSmallMax + 1,
			expectedLimits: limitsMedium,
		},
		{
			name:           "boundary at medium max returns medium tier",
			totalIssues:    tierMediumMax,
			expectedLimits: limitsMedium,
		},
		{
			name:           "one above medium max returns large tier",
			totalIssues:    tierMediumMax + 1,
			expectedLimits: limitsLarge,
		},
		{
			name:           "very large org returns large tier",
			totalIssues:    10000,
			expectedLimits: limitsLarge,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := contextLimitsForOrgSize(tt.totalIssues)
			require.Equal(t, tt.expectedLimits, got, "contextLimitsForOrgSize should return the correct tier")
		})
	}
}

func TestContextLimits_TierOrdering(t *testing.T) {
	t.Parallel()

	// Verify that limits scale up across tiers.
	require.Less(t, limitsSmall.IssuesPerStatus, limitsMedium.IssuesPerStatus, "medium should have more issues than small")
	require.Less(t, limitsMedium.IssuesPerStatus, limitsLarge.IssuesPerStatus, "large should have more issues than medium")

	require.Less(t, limitsSmall.PastDecisions, limitsMedium.PastDecisions, "medium should have more past decisions than small")
	require.Less(t, limitsMedium.PastDecisions, limitsLarge.PastDecisions, "large should have more past decisions than medium")

	require.Less(t, limitsSmall.RecentOutcomes, limitsMedium.RecentOutcomes, "medium should have more recent outcomes than small")
	require.LessOrEqual(t, limitsMedium.RecentOutcomes, limitsLarge.RecentOutcomes, "large should have at least as many recent outcomes as medium")

	// Large tier should have shorter description/stack limits to conserve tokens.
	require.GreaterOrEqual(t, limitsSmall.DescriptionLen, limitsLarge.DescriptionLen, "large tier should have equal or shorter description limit")
}
