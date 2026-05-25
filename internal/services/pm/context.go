package pm

import (
	"context"
	"encoding/json"
	"fmt"
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
	pmDocuments    []models.PMDocument
	slackThreads   []slackThreadData // raw thread data for sandbox files
}

// slackThreadData holds raw thread data for writing to sandbox files.
type slackThreadData struct {
	ChannelName string          `json:"channel_name"`
	ThreadTS    string          `json:"thread_ts"`
	Messages    json.RawMessage `json:"messages"`
}

// gatherContext collects the context needed for PM analysis. When repo is
// non-nil, repo-level PM settings are merged on top of the org defaults.
func (s *Service) gatherContext(ctx context.Context, orgID uuid.UUID, repo *models.Repository) (*gatheredContext, error) {
	org, err := s.orgs.GetByID(ctx, orgID)
	if err != nil {
		return nil, err
	}
	settings, parseErr := models.ParseOrgSettings(org.Settings)
	if parseErr != nil {
		return nil, fmt.Errorf("parse org settings: %w", parseErr)
	}

	// Apply repo-level PM overrides when running for a specific repository.
	if repo != nil {
		repoSettings, repoParseErr := models.ParseRepoSettings(repo.Settings)
		if repoParseErr != nil {
			return nil, fmt.Errorf("parse repo settings: %w", repoParseErr)
		}
		settings = models.MergeRepoPMSettings(settings, repoSettings)
	}

	limits := settings.ContextLimits

	openIssues, err := s.issues.ListByOrg(ctx, orgID, db.IssueFilters{Status: "open", Limit: limits.MaxOpenIssues})
	if err != nil {
		return nil, err
	}
	triagedIssues, err := s.issues.ListByOrg(ctx, orgID, db.IssueFilters{Status: "triaged", Limit: limits.MaxTriagedIssues})
	if err != nil {
		return nil, err
	}
	allIssues := make([]models.Issue, 0, len(openIssues)+len(triagedIssues))
	allIssues = append(allIssues, openIssues...)
	allIssues = append(allIssues, triagedIssues...)

	issueSummaries := make([]IssueSummary, 0, len(allIssues))
	for _, issue := range allIssues {
		issueSummaries = append(issueSummaries, summarizeIssue(issue, limits.IssueDescriptionMax))
	}

	// Fetch pending + running sessions in a single query. Results are ordered by
	// created_at DESC (interleaved), which is fine since we only summarize them.
	inFlight, err := s.sessions.ListByOrg(ctx, orgID, db.SessionFilters{
		Statuses: []models.SessionStatus{models.SessionStatusPending, models.SessionStatusRunning},
		Limit:    limits.MaxInFlightRuns,
	})
	if err != nil {
		return nil, err
	}

	inFlightSummaries := make([]RunSummary, 0, len(inFlight))
	for _, run := range inFlight {
		inFlightSummaries = append(inFlightSummaries, RunSummary{
			ID:        run.ID,
			IssueID:   run.PrimaryIssueID,
			Status:    string(run.Status),
			StartedAt: run.StartedAt,
		})
	}

	recentRuns, err := s.sessions.ListRecentByOrg(ctx, orgID, []string{"completed", "failed", "needs_human_guidance"}, limits.MaxRecentOutcomes)
	if err != nil {
		return nil, err
	}
	outcomes := make([]OutcomeSummary, 0, len(recentRuns))
	for _, run := range recentRuns {
		outcomes = append(outcomes, OutcomeSummary{
			RunID:              run.ID,
			IssueID:            run.PrimaryIssueID,
			Status:             string(run.Status),
			FailureCategory:    run.FailureCategory,
			FailureExplanation: run.FailureExplanation,
			CompletedAt:        run.CompletedAt,
		})
	}

	prSummaries := make([]PRSummary, 0)
	if s.pullRequests != nil {
		prs, err := s.pullRequests.ListByOrg(ctx, orgID, db.PullRequestFilters{Limit: limits.MaxRecentPRs})
		if err != nil {
			return nil, err
		}
		for _, pr := range prs {
			prSummaries = append(prSummaries, PRSummary{
				ID:           pr.ID,
				SessionID:    pr.SessionID,
				Title:        pr.Title,
				Status:       string(pr.Status),
				ReviewStatus: string(pr.ReviewStatus),
				MergedAt:     pr.MergedAt,
			})
		}
	}

	decisionSummaries := make([]DecisionLogEntrySummary, 0)
	if s.decisionLog != nil {
		decisions, err := s.decisionLog.ListRecentByOrg(ctx, orgID, limits.MaxDecisionHistory)
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

	currentRunning, err := s.sessions.CountRunningByOrg(ctx, orgID)
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

	// Gather project context if projects feature is enabled.
	if s.projects != nil && s.projectTasks != nil && s.projectCycles != nil {
		projectSummaries, err := s.buildProjectSummaries(ctx, orgID)
		if err != nil {
			// Non-fatal: log and continue without project context.
			s.logger.Warn().Err(err).Msg("failed to build project summaries for PM context")
		} else {
			pmCtx.ActiveProjects = projectSummaries
		}
	}

	// Gather PM documents if store is configured, excluding pending refresh docs.
	var pmDocs []models.PMDocument
	if s.pmDocuments != nil {
		docs, err := s.pmDocuments.ListByOrgExcludeSourceType(ctx, orgID, models.PMDocSourceRefresh, 100)
		if err != nil {
			s.logger.Warn().Err(err).Msg("failed to load PM documents for context")
		} else {
			pmDocs = docs
		}
	}

	// Gather Slack thread summaries if integration is connected.
	var slackThreads []slackThreadData
	if s.integrations != nil && s.credentials != nil {
		slackCtx, threadData, err := s.gatherSlackContext(ctx, orgID)
		if err != nil {
			s.logger.Warn().Err(err).Msg("failed to gather slack context")
		} else if len(slackCtx) > 0 {
			pmCtx.SlackThreads = slackCtx
			slackThreads = threadData
		}
	}

	return &gatheredContext{
		pmContext:      pmCtx,
		productContext: settings.ProductContext,
		settings:       settings,
		pmDocuments:    pmDocs,
		slackThreads:   slackThreads,
	}, nil
}

// gatherSlackContext reads recent thread summaries from the Slack integration config.
func (s *Service) gatherSlackContext(ctx context.Context, orgID uuid.UUID) ([]SlackThreadContext, []slackThreadData, error) {
	integrations, err := s.integrations.ListByOrgAndProvider(ctx, orgID, models.IntegrationProviderSlack)
	if err != nil || len(integrations) == 0 {
		return nil, nil, err
	}

	integ := integrations[0]
	if integ.Config == nil {
		return nil, nil, nil
	}

	var config slackIntegrationConfig
	if err := json.Unmarshal(integ.Config, &config); err != nil {
		return nil, nil, fmt.Errorf("parse slack integration config: %w", err)
	}

	var summaries []SlackThreadContext
	var threadData []slackThreadData

	for _, t := range config.RecentThreads {
		if t.Analysis == nil || !t.Analysis.Actionable {
			continue
		}

		threadFile := fmt.Sprintf("/workspace/.slack-threads/%s-%s.json", t.ChannelName, t.ThreadTS)
		summaries = append(summaries, SlackThreadContext{
			ChannelName:  t.ChannelName,
			Category:     t.Analysis.Category,
			Summary:      t.Analysis.Summary,
			Urgency:      t.Analysis.Urgency,
			MessageCount: t.MessageCount,
			Participants: t.Participants,
			LastActivity: t.LastActivity,
			ThreadFile:   threadFile,
		})

		threadData = append(threadData, slackThreadData{
			ChannelName: t.ChannelName,
			ThreadTS:    t.ThreadTS,
			Messages:    t.Messages,
		})
	}

	return summaries, threadData, nil
}

func summarizeIssue(issue models.Issue, descriptionMax int) IssueSummary {
	description := ""
	if issue.Description != nil {
		description = *issue.Description
	}
	description = truncate(description, descriptionMax)

	summary := IssueSummary{
		ID:                    issue.ID.String(),
		Source:                string(issue.Source),
		Title:                 issue.Title,
		Description:           description,
		Severity:              string(issue.Severity),
		OccurrenceCount:       issue.OccurrenceCount,
		AffectedCustomerCount: issue.AffectedCustomerCount,
		FirstSeenAt:           issue.FirstSeenAt.Format(time.RFC3339),
		LastSeenAt:            issue.LastSeenAt.Format(time.RFC3339),
		Tags:                  issue.Tags,
		HasStackTrace:         hasStackTrace(issue.RawData),
	}

	// Enrich with source-specific metadata.
	switch issue.Source {
	case models.IssueSourceSentry:
		summary.StackTraceSummary = extractStackTraceSummary(issue.RawData)
	case models.IssueSourceLinear:
		enrichLinearMetadata(&summary, issue.RawData)
	}

	return summary
}

// extractStackTraceSummary pulls a condensed stack trace from Sentry raw data
// so the PM agent can reason about root causes without exploring the codebase.
func extractStackTraceSummary(rawData json.RawMessage) string {
	if len(rawData) == 0 {
		return ""
	}

	var data struct {
		Entries []struct {
			Type string `json:"type"`
			Data struct {
				Values []struct {
					Type       string `json:"type"`
					Value      string `json:"value"`
					Stacktrace struct {
						Frames []struct {
							Filename string `json:"filename"`
							Function string `json:"function"`
							LineNo   int    `json:"lineNo"`
						} `json:"frames"`
					} `json:"stacktrace"`
				} `json:"values"`
			} `json:"data"`
		} `json:"entries"`
	}

	if err := json.Unmarshal(rawData, &data); err != nil {
		return ""
	}

	var b strings.Builder
	for _, entry := range data.Entries {
		if entry.Type != "exception" {
			continue
		}
		for _, value := range entry.Data.Values {
			b.WriteString(value.Type + ": " + value.Value + "\n")
			// Include the top 5 app frames (most relevant for root cause).
			frameCount := 0
			for i := len(value.Stacktrace.Frames) - 1; i >= 0 && frameCount < 5; i-- {
				frame := value.Stacktrace.Frames[i]
				if frame.Filename == "" || strings.HasPrefix(frame.Filename, "<") ||
					strings.Contains(frame.Filename, "node_modules") ||
					strings.Contains(frame.Filename, "site-packages") {
					continue
				}
				fmt.Fprintf(&b, "  at %s (%s:%d)\n", frame.Function, frame.Filename, frame.LineNo)
				frameCount++
			}
		}
	}

	return truncate(b.String(), 800)
}

// enrichLinearMetadata extracts Linear-specific fields from raw webhook data.
func enrichLinearMetadata(summary *IssueSummary, rawData json.RawMessage) {
	if len(rawData) == 0 {
		return
	}

	var payload struct {
		Data struct {
			Identifier string `json:"identifier"`
			State      struct {
				Name string `json:"name"`
				Type string `json:"type"`
			} `json:"state"`
			Team struct {
				Key  string `json:"key"`
				Name string `json:"name"`
			} `json:"team"`
		} `json:"data"`
	}

	if err := json.Unmarshal(rawData, &payload); err != nil {
		return
	}

	summary.LinearIdentifier = payload.Data.Identifier
	summary.LinearState = payload.Data.State.Name
	if payload.Data.Team.Name != "" {
		summary.LinearTeam = payload.Data.Team.Name
	} else {
		summary.LinearTeam = payload.Data.Team.Key
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

func (s *Service) buildProjectSummaries(ctx context.Context, orgID uuid.UUID) ([]ProjectSummary, error) {
	projects, err := s.projects.ListByOrg(ctx, orgID, db.ProjectFilters{Status: "active"})
	if err != nil {
		return nil, err
	}

	summaries := make([]ProjectSummary, 0, len(projects))
	for _, project := range projects {
		summary, err := s.buildProjectSummary(ctx, orgID, &project)
		if err != nil {
			s.logger.Warn().Err(err).Str("project_id", project.ID.String()).Msg("failed to build project summary")
			continue
		}
		summaries = append(summaries, summary)
	}
	return summaries, nil
}

func (s *Service) buildProjectSummary(ctx context.Context, orgID uuid.UUID, project *models.Project) (ProjectSummary, error) {
	tasks, err := s.projectTasks.ListByProject(ctx, orgID, project.ID, db.ProjectTaskFilters{})
	if err != nil {
		return ProjectSummary{}, err
	}

	cycles, err := s.projectCycles.ListByProject(ctx, orgID, project.ID, 3)
	if err != nil {
		return ProjectSummary{}, err
	}

	summary := ProjectSummary{
		ID:              project.ID.String(),
		Title:           project.Title,
		Goal:            project.Goal,
		Priority:        project.Priority,
		Status:          string(project.Status),
		ExecutionMode:   string(project.ExecutionMode),
		MaxConcurrent:   project.MaxConcurrent,
		TotalTasks:      project.TotalTasks,
		CompletedTasks:  project.CompletedTasks,
		FailedTasks:     project.FailedTasks,
		LessonsLearned:  project.LessonsLearned,
		ApproachHistory: project.ApproachHistory,
	}

	if project.Scope != nil {
		summary.Scope = *project.Scope
	}
	if project.CompletionCriteria != nil {
		summary.CompletionCriteria = *project.CompletionCriteria
	}
	if project.CurrentPhase != nil {
		summary.CurrentPhase = *project.CurrentPhase
	}

	if project.TotalTasks > 0 {
		summary.ProgressPct = (project.CompletedTasks * 100) / project.TotalTasks
	}

	for _, cycle := range cycles {
		summary.RecentCycles = append(summary.RecentCycles, CycleSummary{
			CycleNumber:    cycle.CycleNumber,
			Analysis:       cycle.Analysis,
			TasksCreated:   cycle.TasksCreatedThisCycle,
			TasksCompleted: cycle.TasksCompletedThisCycle,
			TasksFailed:    cycle.TasksFailedThisCycle,
			CreatedAt:      cycle.CreatedAt.Format(time.RFC3339),
		})
	}

	for _, task := range tasks {
		ts := TaskSummary{
			ID:          task.ID.String(),
			Title:       task.Title,
			Status:      string(task.Status),
			BatchNumber: task.BatchNumber,
		}
		if task.Approach != nil {
			ts.Approach = *task.Approach
		}
		if task.OutcomeNotes != nil {
			ts.OutcomeNotes = *task.OutcomeNotes
		}
		if task.Complexity != nil {
			ts.Complexity = *task.Complexity
		}
		if task.Confidence != nil {
			ts.Confidence = *task.Confidence
		}

		switch task.Status {
		case models.ProjectTaskStatusPending:
			summary.PendingTasks = append(summary.PendingTasks, ts)
		case models.ProjectTaskStatusRunning, models.ProjectTaskStatusDelegated:
			summary.RunningTasks = append(summary.RunningTasks, ts)
		case models.ProjectTaskStatusCompleted:
			summary.RecentlyCompleted = append(summary.RecentlyCompleted, ts)
		case models.ProjectTaskStatusFailed:
			summary.RecentlyFailed = append(summary.RecentlyFailed, ts)
		}
	}

	return summary, nil
}
