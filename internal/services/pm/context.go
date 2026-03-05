package pm

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
)

type gatheredContext struct {
	pmContext      *PMContext
	productContext *models.ProductContext
	settings       models.OrgSettings
}

func (s *Service) gatherContext(ctx context.Context, orgID uuid.UUID) (*gatheredContext, error) {
	org, err := s.orgs.GetByID(ctx, orgID)
	if err != nil {
		return nil, err
	}
	settings := models.ParseOrgSettings(org.Settings)

	openIssues, err := s.issues.ListByOrg(ctx, orgID, db.IssueFilters{Status: "open", Limit: 100})
	if err != nil {
		return nil, err
	}
	triagedIssues, err := s.issues.ListByOrg(ctx, orgID, db.IssueFilters{Status: "triaged", Limit: 100})
	if err != nil {
		return nil, err
	}
	allIssues := make([]models.Issue, 0, len(openIssues)+len(triagedIssues))
	allIssues = append(allIssues, openIssues...)
	allIssues = append(allIssues, triagedIssues...)

	issueSummaries := make([]IssueSummary, 0, len(allIssues))
	for _, issue := range allIssues {
		issueSummaries = append(issueSummaries, summarizeIssue(issue))
	}

	pendingRuns, err := s.agentRuns.ListByOrg(ctx, orgID, db.AgentRunFilters{Status: models.AgentRunStatusPending, Limit: 50})
	if err != nil {
		return nil, err
	}
	runningRuns, err := s.agentRuns.ListByOrg(ctx, orgID, db.AgentRunFilters{Status: models.AgentRunStatusRunning, Limit: 50})
	if err != nil {
		return nil, err
	}
	inFlight := make([]models.AgentRun, 0, len(pendingRuns)+len(runningRuns))
	inFlight = append(inFlight, pendingRuns...)
	inFlight = append(inFlight, runningRuns...)

	inFlightSummaries := make([]RunSummary, 0, len(inFlight))
	for _, run := range inFlight {
		inFlightSummaries = append(inFlightSummaries, RunSummary{
			ID:        run.ID,
			IssueID:   run.IssueID,
			Status:    run.Status,
			StartedAt: run.StartedAt,
		})
	}

	recentRuns, err := s.agentRuns.ListRecentByOrg(ctx, orgID, []string{"completed", "failed", "needs_human_guidance"}, 20)
	if err != nil {
		return nil, err
	}
	outcomes := make([]OutcomeSummary, 0, len(recentRuns))
	for _, run := range recentRuns {
		outcomes = append(outcomes, OutcomeSummary{
			RunID:              run.ID,
			IssueID:            run.IssueID,
			Status:             run.Status,
			ConfidenceScore:    run.ConfidenceScore,
			FailureCategory:    run.FailureCategory,
			FailureExplanation: run.FailureExplanation,
			CompletedAt:        run.CompletedAt,
		})
	}

	prSummaries := make([]PRSummary, 0)
	if s.pullRequests != nil {
		prs, err := s.pullRequests.ListByOrg(ctx, orgID, db.PullRequestFilters{Limit: 20})
		if err != nil {
			return nil, err
		}
		for _, pr := range prs {
			prSummaries = append(prSummaries, PRSummary{
				ID:           pr.ID,
				AgentRunID:   pr.AgentRunID,
				Title:        pr.Title,
				Status:       pr.Status,
				ReviewStatus: pr.ReviewStatus,
				MergedAt:     pr.MergedAt,
			})
		}
	}

	decisionSummaries := make([]DecisionLogEntrySummary, 0)
	if s.decisionLog != nil {
		decisions, err := s.decisionLog.ListRecentByOrg(ctx, orgID, 50)
		if err != nil {
			return nil, err
		}
		for _, entry := range decisions {
			decisionSummaries = append(decisionSummaries, DecisionLogEntrySummary{
				ID:        entry.ID,
				PlanID:    entry.PlanID,
				IssueID:   entry.IssueID,
				Decision:  entry.Decision,
				Reasoning: entry.Reasoning,
				Outcome:   entry.Outcome,
				CreatedAt: entry.CreatedAt,
			})
		}
	}

	currentRunning, err := s.agentRuns.CountRunningByOrg(ctx, orgID)
	if err != nil {
		return nil, err
	}

	pmCtx := &PMContext{
		OpenIssues:        issueSummaries,
		InFlightRuns:      inFlightSummaries,
		RecentOutcomes:    outcomes,
		RecentPRs:         prSummaries,
		PreviousDecisions: decisionSummaries,
		MaxConcurrentRuns: settings.MaxConcurrentRuns,
		CurrentRunCount:   currentRunning,
	}

	return &gatheredContext{
		pmContext:      pmCtx,
		productContext: settings.ProductContext,
		settings:       settings,
	}, nil
}

func summarizeIssue(issue models.Issue) IssueSummary {
	description := ""
	if issue.Description != nil {
		description = *issue.Description
	}
	description = truncate(description, 200)

	return IssueSummary{
		ID:                    issue.ID.String(),
		Source:                issue.Source,
		Title:                 issue.Title,
		Description:           description,
		Severity:              issue.Severity,
		OccurrenceCount:       issue.OccurrenceCount,
		AffectedCustomerCount: issue.AffectedCustomerCount,
		FirstSeenAt:           issue.FirstSeenAt.Format(time.RFC3339),
		LastSeenAt:            issue.LastSeenAt.Format(time.RFC3339),
		Tags:                  issue.Tags,
		HasStackTrace:         hasStackTrace(issue.RawData),
	}
}

func hasStackTrace(rawData json.RawMessage) bool {
	if len(rawData) == 0 {
		return false
	}
	return strings.Contains(string(rawData), "\"stacktrace\"")
}

func truncate(input string, max int) string {
	if max <= 0 {
		return input
	}
	runes := []rune(input)
	if len(runes) <= max {
		return input
	}
	return string(runes[:max])
}

