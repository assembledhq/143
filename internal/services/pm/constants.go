package pm

const pmMaxTokens = 50_000

// contextLimits holds the adaptive limits for PM context gathering.
// Limits scale based on org size (total issue count) so small repos don't
// get buried in noise and large repos get enough signal.
type contextLimits struct {
	IssuesPerStatus int // max open/triaged issues to fetch (each)
	InFlightRuns    int // max pending/running sessions to fetch (each)
	RecentOutcomes  int // max completed/failed sessions to learn from
	RecentPRs       int // max recent PRs to review
	PastDecisions   int // max decision log entries for institutional memory
	ProjectCycles   int // max recent cycles per project
	DescriptionLen  int // max chars for issue description truncation
	StackTraceLen   int // max chars for stack trace summary truncation
}

// Tier thresholds (total issue count across all statuses).
const (
	tierSmallMax  = 50  // <= 50 issues: small org/repo
	tierMediumMax = 500 // <= 500 issues: medium org/repo
	// > 500 issues: large org/repo
)

var (
	limitsSmall = contextLimits{
		IssuesPerStatus: 30,
		InFlightRuns:    10,
		RecentOutcomes:  10,
		RecentPRs:       10,
		PastDecisions:   20,
		ProjectCycles:   2,
		DescriptionLen:  500,
		StackTraceLen:   800,
	}

	limitsMedium = contextLimits{
		IssuesPerStatus: 75,
		InFlightRuns:    30,
		RecentOutcomes:  20,
		RecentPRs:       20,
		PastDecisions:   50,
		ProjectCycles:   3,
		DescriptionLen:  500,
		StackTraceLen:   800,
	}

	limitsLarge = contextLimits{
		IssuesPerStatus: 150,
		InFlightRuns:    50,
		RecentOutcomes:  30,
		RecentPRs:       30,
		PastDecisions:   75,
		ProjectCycles:   5,
		DescriptionLen:  400,
		StackTraceLen:   600,
	}
)

// contextLimitsForOrgSize returns appropriate limits based on total issue count.
func contextLimitsForOrgSize(totalIssues int) contextLimits {
	switch {
	case totalIssues <= tierSmallMax:
		return limitsSmall
	case totalIssues <= tierMediumMax:
		return limitsMedium
	default:
		return limitsLarge
	}
}
