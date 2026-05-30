package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgerrcode"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/agent"
)

const (
	prHealthStaleAfter       = 2 * time.Minute
	prHealthSyncQueue        = "default"
	prHealthSyncJobType      = "sync_pull_request_state"
	prHealthReconcileJobType = "reconcile_pull_request_state"
	prHealthEnrichJobType    = "enrich_pull_request_health"
	prMergeWhenReadyJobType  = "merge_pull_request_when_ready"
)

var (
	ErrPullRequestMergeabilityPending = errors.New("pull request mergeability is still being checked by GitHub")
	defaultMergeabilityRetryDelays    = []time.Duration{250 * time.Millisecond, 500 * time.Millisecond, time.Second, 2 * time.Second}
)

type gitHubPullRequestDetails struct {
	Number         int    `json:"number"`
	HTMLURL        string `json:"html_url"`
	State          string `json:"state"`
	Merged         bool   `json:"merged"`
	MergeCommitSHA string `json:"merge_commit_sha"`
	Mergeable      *bool  `json:"mergeable"`
	MergeableState string `json:"mergeable_state"`
	Head           struct {
		Ref string `json:"ref"`
		SHA string `json:"sha"`
	} `json:"head"`
	Base struct {
		Ref string `json:"ref"`
		SHA string `json:"sha"`
	} `json:"base"`
}

type gitHubCheckRunsResponse struct {
	CheckRuns []gitHubCheckRun `json:"check_runs"`
}

type gitHubBranchResponse struct {
	Protected  bool `json:"protected"`
	Protection struct {
		RequiredStatusChecks *struct {
			Contexts []string `json:"contexts"`
		} `json:"required_status_checks"`
	} `json:"protection"`
}

type gitHubCheckRun struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	HTMLURL    string `json:"html_url"`
	Conclusion string `json:"conclusion"`
	Status     string `json:"status"`
	DetailsURL string `json:"details_url"`
	App        struct {
		Slug string `json:"slug"`
	} `json:"app"`
	Output struct {
		Title            string `json:"title"`
		Summary          string `json:"summary"`
		Text             string `json:"text"`
		AnnotationsCount int    `json:"annotations_count"`
		AnnotationsURL   string `json:"annotations_url"`
	} `json:"output"`
}

type gitHubCheckRunAnnotation struct {
	Path            string `json:"path"`
	StartLine       int    `json:"start_line"`
	EndLine         int    `json:"end_line"`
	AnnotationLevel string `json:"annotation_level"`
	Message         string `json:"message"`
}

func (s *PRService) GetPullRequestHealth(ctx context.Context, orgID, pullRequestID uuid.UUID) (*models.PullRequestHealthResponse, error) {
	pr, err := s.pullRequests.GetByID(ctx, orgID, pullRequestID)
	if err != nil {
		return nil, err
	}

	if (pr.GitHubStateSyncedAt == nil || pr.HealthVersion == 0) && pr.Status == models.PullRequestStatusOpen {
		if err := s.SyncPullRequestState(ctx, orgID, pullRequestID); err != nil {
			if errors.Is(err, ErrPullRequestMergeabilityPending) {
				s.enqueuePullRequestStateSync(ctx, pr)
			} else {
				s.logger.Warn().Err(err).Str("pull_request_id", pullRequestID.String()).Msg("failed to sync pull request health inline")
			}
		}
		pr, err = s.pullRequests.GetByID(ctx, orgID, pullRequestID)
		if err != nil {
			return nil, err
		}
	} else if pr.Status == models.PullRequestStatusOpen && pr.GitHubStateSyncedAt != nil && time.Since(*pr.GitHubStateSyncedAt) > prHealthStaleAfter {
		s.enqueuePullRequestStateSync(ctx, pr)
	}

	resp, err := s.buildPullRequestHealthResponse(ctx, pr)
	if err != nil {
		return nil, err
	}

	if pr.Status == models.PullRequestStatusOpen && resp.FailingTestCount > 0 && !resp.EnrichmentReady && !resp.EnrichmentRequested {
		s.enqueuePullRequestHealthEnrichment(ctx, pr, resp.HealthVersion)
		resp.EnrichmentRequested = true
	}

	return resp, nil
}

func (s *PRService) buildPullRequestHealthResponse(ctx context.Context, pr models.PullRequest) (*models.PullRequestHealthResponse, error) {
	resp := &models.PullRequestHealthResponse{
		PullRequestID:       pr.ID,
		PullRequestNumber:   pr.GitHubPRNumber,
		Repository:          pr.GitHubRepo,
		URL:                 pr.GitHubPRURL,
		Status:              pr.Status,
		MergeState:          pr.MergeState,
		HasConflicts:        pr.HasConflicts,
		FailingTestCount:    pr.FailingTestCount,
		NeedsAgentAction:    pr.NeedsAgentAction,
		GitHubStateSyncedAt: pr.GitHubStateSyncedAt,
		HealthVersion:       pr.HealthVersion,
		MergeWhenReady: models.PullRequestMergeWhenReadyStatus{
			State:                  pr.MergeWhenReadyState,
			RequestedByUserID:      pr.MergeWhenReadyRequestedBy,
			RequestedAt:            pr.MergeWhenReadyRequestedAt,
			RequestedHeadSHA:       pr.MergeWhenReadyHeadSHA,
			RequestedHealthVersion: pr.MergeWhenReadyHealthVersion,
			LastError:              pr.MergeWhenReadyError,
		},
	}
	derivePullRequestRepairActions(resp)
	if pr.HeadSHA != nil {
		resp.HeadSHA = *pr.HeadSHA
	}
	if pr.BaseSHA != nil {
		resp.BaseSHA = *pr.BaseSHA
	}

	current, err := s.pullRequests.GetHealthCurrent(ctx, pr.OrgID, pr.ID)
	if err == nil {
		// currentMatchesHead suppresses the cached health summary when it
		// describes a SHA the PR has already moved past (e.g. after a "Push
		// changes" follow-up). The HealthVersion != 0 short-circuit relies
		// on PullRequestStore.UpdateHeadSHA resetting health_version to 0
		// on every push — if a future writer changes that invariant the
		// SHA comparison must become unconditional, otherwise stale
		// "Resolve conflicts"/"Fix tests" banners can survive a fresh push.
		// resp.HeadSHA == "" preserves legacy behavior for PRs that never
		// had a head SHA recorded; nothing to compare against.
		currentMatchesHead := pr.HealthVersion != 0 || resp.HeadSHA == "" || current.HeadSHA == resp.HeadSHA
		var summary models.PullRequestHealthSummary
		if currentMatchesHead {
			if unmarshalErr := json.Unmarshal(current.SummaryJSON, &summary); unmarshalErr == nil {
				normalizeStoredCheckSummaries(&summary)
				resp.MergeState = summary.MergeState
				resp.HasConflicts = summary.HasConflicts
				resp.FailingTestCount = summary.FailingTestCount
				resp.NeedsAgentAction = summary.NeedsAgentAction
				resp.Checks = summary.Checks
				resp.HealthVersion = current.Version
				resp.HeadSHA = current.HeadSHA
				resp.BaseSHA = current.BaseSHA
				resp.ChecksConfirmed = summary.ChecksConfirmed || (len(summary.Checks) > 0 && determineChecksConfirmed(summary.Checks, false))
				resp.EnrichmentStatus = current.EnrichmentStatus
				resp.EnrichmentRequested = current.EnrichmentStatus == models.PullRequestHealthEnrichmentStatusPending
				resp.EnrichmentReady = current.EnrichmentStatus == models.PullRequestHealthEnrichmentStatusReady
				derivePullRequestRepairActions(resp)
			}
		} else {
			s.logger.Info().
				Str("pull_request_id", pr.ID.String()).
				Str("pr_head_sha", resp.HeadSHA).
				Str("health_head_sha", current.HeadSHA).
				Msg("skipping stale pull request health summary for newer PR head")
		}

		if resp.EnrichmentReady {
			snapshot, snapshotErr := s.pullRequests.GetHealthSnapshot(ctx, pr.OrgID, pr.ID, current.Version)
			if snapshotErr == nil {
				resp.ConflictDetailAvailable = len(snapshot.ConflictPayload) > 0
				resp.FailingTestDetailAvailable = len(snapshot.FailingTestsPayload) > 0
			}
		}
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}

	if err := s.populateActiveRepairs(ctx, pr, resp); err != nil {
		return nil, err
	}
	resp.Summary = buildPRHealthSummaryText(*resp)
	return resp, nil
}

func (s *PRService) populateActiveRepairs(ctx context.Context, pr models.PullRequest, resp *models.PullRequestHealthResponse) error {
	if resp.HealthVersion == 0 || s.sessions == nil {
		return nil
	}

	runs, err := s.pullRequests.ListActiveRepairRuns(ctx, pr.OrgID, pr.ID, resp.HealthVersion)
	if err != nil {
		return err
	}
	if len(runs) == 0 {
		return nil
	}

	sessionIDs := make([]uuid.UUID, 0, len(runs))
	seenSessionIDs := make(map[uuid.UUID]struct{}, len(runs))
	for _, run := range runs {
		if _, ok := seenSessionIDs[run.SessionID]; ok {
			continue
		}
		seenSessionIDs[run.SessionID] = struct{}{}
		sessionIDs = append(sessionIDs, run.SessionID)
	}

	sessions, err := s.sessions.ListByIDs(ctx, pr.OrgID, sessionIDs)
	if err != nil {
		return fmt.Errorf("list repair sessions for pull request health: %w", err)
	}
	sessionsByID := make(map[uuid.UUID]models.Session, len(sessions))
	for _, session := range sessions {
		sessionsByID[session.ID] = session
	}

	activeRepairs := make([]models.PullRequestActiveRepair, 0, len(runs))
	for _, run := range runs {
		session, ok := sessionsByID[run.SessionID]
		if !ok || isSessionTerminalStatus(session.Status) {
			continue
		}
		activeRepairs = append(activeRepairs, models.PullRequestActiveRepair{
			ActionType:    run.ActionType,
			SessionID:     run.SessionID,
			SessionStatus: session.Status,
			HealthVersion: run.HealthVersion,
		})
	}

	if len(activeRepairs) > 0 {
		resp.ActiveRepairs = activeRepairs
		resp.CanMerge = false
	}
	return nil
}

func derivePullRequestRepairActions(resp *models.PullRequestHealthResponse) {
	resp.CanResolveConflicts = resp.HasConflicts || resp.MergeState == models.PullRequestMergeStateConflicted
	resp.CanFixTests = resp.FailingTestCount > 0
	// CanMerge is the green-light counterpart to the repair flags: GitHub has
	// confirmed the branch is mergeable and the check state is authoritative.
	// Once GitHub health has been loaded, zero checks means "no CI rules
	// configured" and is mergeable. Before that health snapshot exists, zero
	// checks remains ambiguous and we keep merge hidden.
	resp.CanMerge = resp.Status == models.PullRequestStatusOpen &&
		resp.MergeState == models.PullRequestMergeStateClean &&
		checksAllowMerge(resp.ChecksConfirmed, resp.Checks)
}

func checksAllowMerge(checksConfirmed bool, checks []models.PullRequestCheckSummary) bool {
	if len(checks) == 0 {
		return checksConfirmed
	}
	for _, check := range checks {
		if classifyStoredCheckStatus(check) != models.PullRequestCheckStatusPassed {
			return false
		}
	}
	return true
}

func (s *PRService) SyncPullRequestState(ctx context.Context, orgID, pullRequestID uuid.UUID) error {
	pr, err := s.pullRequests.GetByID(ctx, orgID, pullRequestID)
	if err != nil {
		return err
	}

	repo, err := s.repos.GetByFullName(ctx, orgID, pr.GitHubRepo)
	if err != nil {
		return fmt.Errorf("load repository for pull request health sync: %w", err)
	}
	token, err := s.getInstallationTokenForRepo(ctx, orgID, &repo)
	if err != nil {
		return fmt.Errorf("load installation token for pull request health sync: %w", err)
	}

	owner, repoName := splitRepo(pr.GitHubRepo)
	details, err := s.fetchPullRequestDetails(ctx, token, owner, repoName, pr.GitHubPRNumber)
	if err != nil {
		return err
	}

	// Self-heal: when GitHub reports the PR closed but our DB still has it
	// open, the pull_request:closed webhook never landed (delivery failure,
	// signature mismatch, app restart, etc.). Apply the same transition the
	// webhook handler would have, then stop — a closed PR has no live health
	// to track and re-running follow-ups would just churn idempotent work.
	if strings.EqualFold(strings.TrimSpace(details.State), "closed") && pr.Status == models.PullRequestStatusOpen {
		s.logger.Warn().
			Str("pull_request_id", pullRequestID.String()).
			Str("repo", pr.GitHubRepo).
			Int("number", pr.GitHubPRNumber).
			Bool("merged", details.Merged).
			Msg("self-healing PR status drift during sync; closed-event webhook was likely dropped")
		return s.applyClosedPRTransition(ctx, pr, details.Merged, details.MergeCommitSHA, details.Head.SHA)
	}

	checkRuns, err := s.listCheckRunsForRef(ctx, token, owner, repoName, details.Head.SHA)
	if err != nil {
		return err
	}

	requiredChecksConfigured := false
	if len(checkRuns) == 0 {
		requiredChecksConfigured, err = s.branchRequiresStatusChecks(ctx, token, owner, repoName, details.Base.Ref)
		if err != nil {
			return err
		}
	}

	var prior *models.PullRequestHealthCurrent
	priorCurrent, priorErr := s.pullRequests.GetHealthCurrent(ctx, orgID, pullRequestID)
	switch {
	case priorErr == nil:
		prior = &priorCurrent
	case errors.Is(priorErr, pgx.ErrNoRows):
		prior = nil
	default:
		return priorErr
	}

	summary := models.PullRequestHealthSummary{
		Checks: make([]models.PullRequestCheckSummary, 0),
	}
	summary.MergeState, summary.HasConflicts = normalizeMergeState(details.Mergeable, details.MergeableState)
	for _, check := range checkRuns {
		category := classifyCheckRunCategory(check.Name)
		status := normalizeCheckRunStatus(check)
		if category == models.PullRequestCheckCategoryTest && status == models.PullRequestCheckStatusFailed {
			summary.FailingTestCount++
		}
		summary.Checks = append(summary.Checks, models.PullRequestCheckSummary{
			Name:       check.Name,
			Category:   category,
			Status:     status,
			Provider:   check.App.Slug,
			DetailsURL: firstNonEmpty(check.DetailsURL, check.HTMLURL),
			Summary:    firstNonEmpty(check.Output.Title, truncateText(stripWhitespace(check.Output.Summary), 240)),
		})
	}
	summary.ChecksConfirmed = determineChecksConfirmed(summary.Checks, requiredChecksConfigured)
	summary.NeedsAgentAction = summary.HasConflicts || summary.FailingTestCount > 0

	mergeStateIndeterminate, testsIndeterminate := detectIndeterminateSignals(details.Mergeable, details.MergeableState, checkRuns)
	if mergeStateIndeterminate || testsIndeterminate {
		if prior != nil && shouldSkipIndeterminateSnapshotWrite(mergeStateIndeterminate, testsIndeterminate, details.Head.SHA, summary.FailingTestCount, *prior) {
			s.logger.Debug().
				Str("pull_request_id", pullRequestID.String()).
				Str("head_sha", details.Head.SHA).
				Bool("merge_state_indeterminate", mergeStateIndeterminate).
				Bool("tests_indeterminate", testsIndeterminate).
				Msg("skipping pull request health snapshot write; GitHub data still indeterminate on same head SHA")
			if mergeStateIndeterminate {
				return ErrPullRequestMergeabilityPending
			}
			return nil
		}
	}

	current, err := s.pullRequests.UpsertHealthSummary(ctx, orgID, pullRequestID, details.Head.SHA, details.Base.SHA, summary, nil)
	if err != nil {
		return err
	}

	ciStatus := deriveAggregateCIStatus(summary.Checks)
	if err := s.pullRequests.UpdateCIStatus(ctx, orgID, pullRequestID, models.PullRequestCIStatus(ciStatus)); err != nil {
		s.logger.Warn().Err(err).Str("pull_request_id", pullRequestID.String()).Msg("failed to update CI status during pull request health sync")
	}

	s.publishPullRequestUpdated(ctx, pr, current)
	s.enqueueMergeWhenReadyProcessing(ctx, pr)
	if mergeStateIndeterminate {
		return ErrPullRequestMergeabilityPending
	}
	return nil
}

func (s *PRService) ReconcilePullRequestState(ctx context.Context, orgID uuid.UUID, limit int) error {
	stale, err := s.pullRequests.ListOpenStaleForHealthSync(ctx, orgID, time.Now().Add(-prHealthStaleAfter), limit)
	if err != nil {
		return err
	}
	for _, pr := range stale {
		if err := s.SyncPullRequestState(ctx, orgID, pr.ID); err != nil {
			if errors.Is(err, ErrPullRequestMergeabilityPending) {
				s.logger.Debug().Str("pull_request_id", pr.ID.String()).Msg("pull request mergeability is still pending during reconciliation")
				continue
			}
			s.logger.Warn().Err(err).Str("pull_request_id", pr.ID.String()).Msg("failed to reconcile pull request health")
		}
	}
	queued, err := s.pullRequests.ListMergeWhenReadyForProcessing(ctx, orgID, time.Now().Add(-mergeWhenReadyMergingStaleAfter), limit)
	if err != nil {
		return err
	}
	for _, pr := range queued {
		s.enqueueMergeWhenReadyProcessing(ctx, pr)
	}
	return nil
}

func (s *PRService) EnrichPullRequestHealth(ctx context.Context, orgID, pullRequestID uuid.UUID, version int64) error {
	pr, err := s.pullRequests.GetByID(ctx, orgID, pullRequestID)
	if err != nil {
		return err
	}
	repo, err := s.repos.GetByFullName(ctx, orgID, pr.GitHubRepo)
	if err != nil {
		return err
	}
	token, err := s.getInstallationTokenForRepo(ctx, orgID, &repo)
	if err != nil {
		return err
	}
	owner, repoName := splitRepo(pr.GitHubRepo)
	details, err := s.fetchPullRequestDetails(ctx, token, owner, repoName, pr.GitHubPRNumber)
	if err != nil {
		return err
	}
	checkRuns, err := s.listCheckRunsForRef(ctx, token, owner, repoName, details.Head.SHA)
	if err != nil {
		return err
	}

	conflictPayload, err := json.Marshal(map[string]any{
		"pull_request_id":  pr.ID,
		"repository":       pr.GitHubRepo,
		"pull_request_num": pr.GitHubPRNumber,
		"url":              pr.GitHubPRURL,
		"base_branch":      details.Base.Ref,
		"head_branch":      details.Head.Ref,
		"base_sha":         details.Base.SHA,
		"head_sha":         details.Head.SHA,
		"merge_state":      normalizeRepairMergeState(pr.MergeState, details.Mergeable, details.MergeableState),
		"behind_base":      normalizeRepairMergeState(pr.MergeState, details.Mergeable, details.MergeableState) == models.PullRequestMergeStateBehind,
	})
	if err != nil {
		return fmt.Errorf("marshal conflict payload: %w", err)
	}

	type failingCheckPayload struct {
		Name        string                          `json:"name"`
		Category    models.PullRequestCheckCategory `json:"category"`
		Provider    string                          `json:"provider,omitempty"`
		Summary     string                          `json:"summary,omitempty"`
		DetailsURL  string                          `json:"details_url,omitempty"`
		LogExcerpt  string                          `json:"log_excerpt,omitempty"`
		Annotations []string                        `json:"annotations,omitempty"`
	}
	payloadChecks := make([]failingCheckPayload, 0)
	for _, check := range checkRuns {
		category := classifyCheckRunCategory(check.Name)
		if category != models.PullRequestCheckCategoryTest {
			continue
		}
		if normalizeCheckRunStatus(check) != models.PullRequestCheckStatusFailed {
			continue
		}
		annotations, annErr := s.fetchCheckRunAnnotations(ctx, token, owner, repoName, check.ID)
		if annErr != nil {
			s.logger.Warn().Err(annErr).Int64("check_run_id", check.ID).Msg("failed to fetch check run annotations")
		}
		payloadChecks = append(payloadChecks, failingCheckPayload{
			Name:        check.Name,
			Category:    category,
			Provider:    check.App.Slug,
			Summary:     firstNonEmpty(check.Output.Title, truncateText(stripWhitespace(check.Output.Summary), 240)),
			DetailsURL:  firstNonEmpty(check.DetailsURL, check.HTMLURL),
			LogExcerpt:  truncateText(stripWhitespace(firstNonEmpty(check.Output.Text, check.Output.Summary)), 1200),
			Annotations: annotations,
		})
	}

	failingTestsPayload, err := json.Marshal(map[string]any{
		"pull_request_id":  pr.ID,
		"repository":       pr.GitHubRepo,
		"pull_request_num": pr.GitHubPRNumber,
		"url":              pr.GitHubPRURL,
		"head_sha":         details.Head.SHA,
		"checks":           payloadChecks,
	})
	if err != nil {
		return fmt.Errorf("marshal failing tests payload: %w", err)
	}

	return s.pullRequests.UpdateHealthEnrichment(ctx, orgID, pullRequestID, version, conflictPayload, failingTestsPayload, models.PullRequestHealthEnrichmentStatusReady)
}

func (s *PRService) StartPullRequestRepair(ctx context.Context, orgID, pullRequestID, userID uuid.UUID, action models.PullRequestRepairActionType) (*models.PullRequestRepairResponse, error) {
	if err := action.Validate(); err != nil {
		return nil, err
	}

	pr, err := s.pullRequests.GetByID(ctx, orgID, pullRequestID)
	if err != nil {
		return nil, err
	}
	current, err := s.pullRequests.GetHealthCurrent(ctx, orgID, pullRequestID)
	if err != nil {
		return nil, err
	}
	snapshot, err := s.pullRequests.GetHealthSnapshot(ctx, orgID, pullRequestID, current.Version)
	if err != nil {
		return nil, err
	}

	var summary models.PullRequestHealthSummary
	if err := json.Unmarshal(current.SummaryJSON, &summary); err != nil {
		return nil, fmt.Errorf("decode pull request health summary for repair: %w", err)
	}
	switch action {
	case models.PullRequestRepairActionTypeResolveConflicts:
		if !summary.HasConflicts && summary.MergeState != models.PullRequestMergeStateConflicted {
			return nil, fmt.Errorf("pull request does not currently require conflict resolution")
		}
	case models.PullRequestRepairActionTypeFixTests:
		if summary.FailingTestCount == 0 {
			return nil, fmt.Errorf("pull request does not currently have failing tests")
		}
		if snapshot.EnrichmentStatus != models.PullRequestHealthEnrichmentStatusReady {
			if err := s.EnrichPullRequestHealth(ctx, orgID, pullRequestID, current.Version); err != nil {
				return nil, err
			}
			snapshot, err = s.pullRequests.GetHealthSnapshot(ctx, orgID, pullRequestID, current.Version)
			if err != nil {
				return nil, err
			}
		}
	}

	existing, err := s.pullRequests.GetActiveRepairRun(ctx, orgID, pullRequestID, action, current.Version)
	if err == nil {
		session, sessionErr := s.sessions.GetByID(ctx, orgID, existing.SessionID)
		if sessionErr == nil && !isSessionTerminalStatus(session.Status) {
			return &models.PullRequestRepairResponse{
				SessionID:        existing.SessionID,
				Mode:             "existing",
				ReusedInFlight:   true,
				HeadSHA:          current.HeadSHA,
				BaseSHA:          current.BaseSHA,
				HealthVersion:    current.Version,
				RepairActionType: action,
			}, nil
		}
		_ = s.pullRequests.DeactivateRepairRun(ctx, orgID, existing.ID)
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return nil, err
	}

	revisionContext, err := s.buildRepairRevisionContext(pr, current, summary, snapshot, action)
	if err != nil {
		return nil, err
	}

	if pr.SessionID == nil {
		return nil, fmt.Errorf("pull request is not linked to a canonical session")
	}
	session, err := s.sessions.GetByID(ctx, orgID, *pr.SessionID)
	if err != nil {
		return nil, err
	}
	workspaceMode, reason := s.repairWorkspaceMode(session)
	if reason != "" {
		s.logger.Info().
			Str("session_id", session.ID.String()).
			Str("workspace_mode", string(workspaceMode)).
			Str("reason", reason).
			Msg("selected pull request repair workspace mode")
	}
	if workspaceMode == models.PullRequestRepairWorkspaceModeSnapshotContinuation && reason != "" {
		return nil, fmt.Errorf("canonical pull request session is not ready for repair: %s", reason)
	}
	return s.resumeRepairSession(ctx, pr, session, revisionContext, repairPromptForAction(action), userID, action, current.Version, current.HeadSHA, current.BaseSHA, workspaceMode)
}

func (s *PRService) buildRepairRevisionContext(pr models.PullRequest, current models.PullRequestHealthCurrent, summary models.PullRequestHealthSummary, snapshot models.PullRequestHealthSnapshot, action models.PullRequestRepairActionType) ([]byte, error) {
	ctxPayload := &agent.RevisionContext{
		RepairAction: action,
		RepairContext: &agent.PullRequestRepairContext{
			PullRequestNumber: pr.GitHubPRNumber,
			Repository:        pr.GitHubRepo,
			HeadSHA:           current.HeadSHA,
			BaseSHA:           current.BaseSHA,
			MergeState:        summary.MergeState,
			HasConflicts:      summary.HasConflicts,
		},
	}

	if len(snapshot.FailingTestsPayload) > 0 {
		var payload struct {
			Checks []struct {
				Name        string                          `json:"name"`
				Category    models.PullRequestCheckCategory `json:"category"`
				Summary     string                          `json:"summary"`
				DetailsURL  string                          `json:"details_url"`
				LogExcerpt  string                          `json:"log_excerpt"`
				Annotations []string                        `json:"annotations"`
			} `json:"checks"`
		}
		if err := json.Unmarshal(snapshot.FailingTestsPayload, &payload); err == nil {
			ctxPayload.RepairContext.FailingChecks = make([]agent.PullRequestFailingCheck, 0, len(payload.Checks))
			for _, check := range payload.Checks {
				ctxPayload.RepairContext.FailingChecks = append(ctxPayload.RepairContext.FailingChecks, agent.PullRequestFailingCheck{
					Name:        check.Name,
					Category:    check.Category,
					Summary:     check.Summary,
					DetailsURL:  check.DetailsURL,
					LogExcerpt:  check.LogExcerpt,
					Annotations: check.Annotations,
				})
			}
		}
	}

	return json.Marshal(ctxPayload)
}

func (s *PRService) canResumeRepairSession(session models.Session) bool {
	mode, reason := s.repairWorkspaceMode(session)
	return mode == models.PullRequestRepairWorkspaceModeSnapshotContinuation && reason == ""
}

func (s *PRService) repairWorkspaceMode(session models.Session) (models.PullRequestRepairWorkspaceMode, string) {
	if session.PendingSnapshotKey != nil && *session.PendingSnapshotKey != "" {
		return models.PullRequestRepairWorkspaceModePRHeadReconstruction, "pending snapshot upload"
	}
	if session.SnapshotKey == nil || *session.SnapshotKey == "" {
		return models.PullRequestRepairWorkspaceModePRHeadReconstruction, "missing snapshot"
	}
	if session.SandboxState == models.SandboxStateDestroyed {
		return models.PullRequestRepairWorkspaceModePRHeadReconstruction, "destroyed sandbox"
	}
	switch session.Status {
	case models.SessionStatusIdle,
		models.SessionStatusCompleted,
		models.SessionStatusPRCreated,
		models.SessionStatusFailed,
		models.SessionStatusCancelled,
		models.SessionStatusAwaitingInput,
		models.SessionStatusNeedsHumanGuidance:
		return models.PullRequestRepairWorkspaceModeSnapshotContinuation, ""
	default:
		return models.PullRequestRepairWorkspaceModeSnapshotContinuation, "session is not resumable"
	}
}

func (s *PRService) resumeRepairSession(ctx context.Context, pr models.PullRequest, session models.Session, revisionContext []byte, shortPrompt string, userID uuid.UUID, action models.PullRequestRepairActionType, healthVersion int64, headSHA, baseSHA string, workspaceMode models.PullRequestRepairWorkspaceMode) (*models.PullRequestRepairResponse, error) {
	if s.sessionMessages == nil {
		return nil, fmt.Errorf("session message store not configured")
	}
	if workspaceMode == "" {
		workspaceMode = models.PullRequestRepairWorkspaceModeSnapshotContinuation
	}
	if err := workspaceMode.Validate(); err != nil {
		return nil, err
	}

	tx, err := s.sessions.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer func() { _ = tx.Rollback(ctx) }()

	txSessions := db.NewSessionStore(tx)
	txMessages := db.NewSessionMessageStore(tx)
	txThreads := db.NewSessionThreadStore(tx)
	txPRs := db.NewPullRequestStore(tx)

	claimed, claimErr := txSessions.ClaimIdle(ctx, pr.OrgID, session.ID)
	if claimErr != nil {
		claimed, claimErr = txSessions.ClaimForResume(ctx, pr.OrgID, session.ID)
		if claimErr != nil {
			return nil, claimErr
		}
	}
	if err := txSessions.UpdateRevisionContext(ctx, pr.OrgID, claimed.ID, revisionContext); err != nil {
		return nil, err
	}
	// The session's primary thread is the "Main" tab seeded at session
	// creation. Without attributing the repair message to that thread, the
	// session-detail UI's per-thread timeline query (ListByThread) skips it
	// and the user sees the click do nothing in the conversation view —
	// SessionStore.ClaimIdle/ClaimForResume don't hydrate PrimaryThreadID
	// from the row, so we look it up here. ListBySession orders by
	// created_at ASC, matching the convention that the first-created thread
	// is "Main".
	threads, err := txThreads.ListBySession(ctx, pr.OrgID, claimed.ID)
	if err != nil {
		return nil, fmt.Errorf("list session threads for repair message: %w", err)
	}
	var threadID *uuid.UUID
	if len(threads) > 0 {
		id := threads[0].ID
		threadID = &id
	}
	msg := &models.SessionMessage{
		SessionID:  claimed.ID,
		OrgID:      pr.OrgID,
		ThreadID:   threadID,
		UserID:     &userID,
		TurnNumber: claimed.CurrentTurn + 1,
		Role:       models.MessageRoleUser,
		Content:    shortPrompt,
	}
	if err := txMessages.Create(ctx, msg); err != nil {
		return nil, err
	}
	repairRun := &models.PullRequestRepairRun{
		OrgID:         pr.OrgID,
		PullRequestID: pr.ID,
		SessionID:     claimed.ID,
		ActionType:    action,
		HealthVersion: healthVersion,
		WorkspaceMode: workspaceMode,
		Active:        true,
	}
	if err := txPRs.CreateRepairRun(ctx, repairRun); err != nil {
		if isUniqueActiveRepairRunViolation(err) {
			existing, lookupErr := s.pullRequests.GetActiveRepairRun(ctx, pr.OrgID, pr.ID, action, healthVersion)
			if lookupErr == nil {
				return &models.PullRequestRepairResponse{
					SessionID:        existing.SessionID,
					Mode:             "existing",
					ReusedInFlight:   true,
					HeadSHA:          headSHA,
					BaseSHA:          baseSHA,
					HealthVersion:    healthVersion,
					RepairActionType: action,
				}, nil
			}
		}
		return nil, err
	}
	continueDedupeKey := db.ContinueSessionDedupeKey(claimed.ID)
	payload := map[string]any{
		"session_id":          claimed.ID.String(),
		"org_id":              pr.OrgID.String(),
		"pull_request_id":     pr.ID.String(),
		"repair_run_id":       repairRun.ID.String(),
		"command_type":        string(action),
		"health_version":      healthVersion,
		"head_sha":            headSHA,
		"workspace_mode":      string(workspaceMode),
		"pull_request_number": pr.GitHubPRNumber,
	}
	if _, err := s.jobs.EnqueueInTxWithOpts(ctx, tx, pr.OrgID, db.EnqueueOpts{
		Queue:        "agent",
		JobType:      "continue_session",
		Payload:      payload,
		Priority:     5,
		DedupeKey:    &continueDedupeKey,
		TargetNodeID: models.SessionWorkerTarget(&claimed),
	}); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return &models.PullRequestRepairResponse{
		SessionID:        claimed.ID,
		Mode:             repairResponseMode(workspaceMode),
		ReusedInFlight:   false,
		HeadSHA:          headSHA,
		BaseSHA:          baseSHA,
		HealthVersion:    healthVersion,
		RepairActionType: action,
	}, nil
}

func repairResponseMode(workspaceMode models.PullRequestRepairWorkspaceMode) string {
	if workspaceMode == models.PullRequestRepairWorkspaceModePRHeadReconstruction {
		return "reconstructed"
	}
	return "resumed"
}

func (s *PRService) fetchPullRequestDetails(ctx context.Context, token, owner, repo string, number int) (*gitHubPullRequestDetails, error) {
	path := fmt.Sprintf("/repos/%s/%s/pulls/%d", owner, repo, number)
	delays := s.mergeabilityBackoffDelays()
	for attempt := 0; ; attempt++ {
		body, err := s.doGitHubRequest(ctx, token, http.MethodGet, path, nil)
		if err != nil {
			return nil, err
		}
		var details gitHubPullRequestDetails
		if err := json.Unmarshal(body, &details); err != nil {
			return nil, fmt.Errorf("decode GitHub pull request details: %w", err)
		}
		if details.Mergeable != nil || isDefinitiveNullMergeabilityState(details.MergeableState) || attempt >= len(delays) {
			return &details, nil
		}
		delay := delays[attempt]
		s.logger.Debug().
			Str("repo", owner+"/"+repo).
			Int("pull_request_number", number).
			Int("attempt", attempt+1).
			Dur("delay", delay).
			Msg("GitHub mergeability still pending; retrying pull request details")
		if err := s.waitForMergeabilityBackoff(ctx, delay); err != nil {
			return nil, err
		}
	}
}

func (s *PRService) mergeabilityBackoffDelays() []time.Duration {
	if s.mergeabilityRetryDelays != nil {
		return s.mergeabilityRetryDelays
	}
	return defaultMergeabilityRetryDelays
}

func (s *PRService) waitForMergeabilityBackoff(ctx context.Context, delay time.Duration) error {
	if s.mergeabilityRetryWait != nil {
		return s.mergeabilityRetryWait(ctx, delay)
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (s *PRService) listCheckRunsForRef(ctx context.Context, token, owner, repo, ref string) ([]gitHubCheckRun, error) {
	path := fmt.Sprintf("/repos/%s/%s/commits/%s/check-runs?per_page=100", owner, repo, ref)
	body, err := s.doGitHubRequest(ctx, token, http.MethodGet, path, nil)
	if err != nil {
		return nil, err
	}
	var resp gitHubCheckRunsResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return nil, fmt.Errorf("decode GitHub check runs: %w", err)
	}
	return dedupeCheckRunsByName(resp.CheckRuns), nil
}

func (s *PRService) branchRequiresStatusChecks(ctx context.Context, token, owner, repo, branch string) (bool, error) {
	path := fmt.Sprintf("/repos/%s/%s/branches/%s", owner, repo, url.PathEscape(branch))
	body, err := s.doGitHubRequest(ctx, token, http.MethodGet, path, nil)
	if err != nil {
		return false, err
	}

	var resp gitHubBranchResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		return false, fmt.Errorf("decode GitHub branch protection: %w", err)
	}
	if !resp.Protected || resp.Protection.RequiredStatusChecks == nil {
		return false, nil
	}
	return len(resp.Protection.RequiredStatusChecks.Contexts) > 0, nil
}

// dedupeCheckRunsByName collapses check runs that share a display name down to
// the most recent one. GitHub's filter=latest only dedupes within a single
// workflow run, so a repo whose workflow triggers on both `pull_request` and
// `push` ends up with two parallel runs on the same SHA — each emits its own
// "Backend Test", "Frontend Test", etc. and the unfiltered list shows every
// job twice. Highest ID wins because GitHub allocates check_run IDs
// monotonically, so the newer workflow run's state replaces the older one.
func dedupeCheckRunsByName(checkRuns []gitHubCheckRun) []gitHubCheckRun {
	if len(checkRuns) <= 1 {
		return checkRuns
	}
	bestIdx := make(map[string]int, len(checkRuns))
	for i, check := range checkRuns {
		key := strings.ToLower(strings.TrimSpace(check.Name))
		if existing, ok := bestIdx[key]; !ok || checkRuns[i].ID > checkRuns[existing].ID {
			bestIdx[key] = i
		}
	}
	deduped := make([]gitHubCheckRun, 0, len(bestIdx))
	for i, check := range checkRuns {
		key := strings.ToLower(strings.TrimSpace(check.Name))
		if bestIdx[key] == i {
			deduped = append(deduped, check)
		}
	}
	return deduped
}

func (s *PRService) fetchCheckRunAnnotations(ctx context.Context, token, owner, repo string, checkRunID int64) ([]string, error) {
	path := fmt.Sprintf("/repos/%s/%s/check-runs/%d/annotations?per_page=50", owner, repo, checkRunID)
	body, err := s.doGitHubRequest(ctx, token, http.MethodGet, path, nil)
	if err != nil {
		var apiErr *GitHubAPIError
		if errors.As(err, &apiErr) && apiErr.StatusCode == http.StatusNotFound {
			return nil, nil
		}
		return nil, err
	}
	var annotations []gitHubCheckRunAnnotation
	if err := json.Unmarshal(body, &annotations); err != nil {
		return nil, fmt.Errorf("decode GitHub check run annotations: %w", err)
	}
	lines := make([]string, 0, len(annotations))
	for _, annotation := range annotations {
		lines = append(lines, fmt.Sprintf("%s:%d %s", annotation.Path, annotation.StartLine, stripWhitespace(annotation.Message)))
	}
	return lines, nil
}

func (s *PRService) enqueuePullRequestStateSync(ctx context.Context, pr models.PullRequest) {
	s.enqueuePullRequestStateSyncWithScope(ctx, pr, "")
}

func (s *PRService) enqueuePullRequestStateSyncWithScope(ctx context.Context, pr models.PullRequest, scope string) {
	if s.jobs == nil {
		return
	}
	dedupeKey := pullRequestStateSyncDedupeKey(pr.ID, scope)
	_, err := s.jobs.Enqueue(ctx, pr.OrgID, prHealthSyncQueue, prHealthSyncJobType, map[string]string{
		"org_id":          pr.OrgID.String(),
		"pull_request_id": pr.ID.String(),
	}, 6, &dedupeKey)
	if err != nil {
		s.logger.Warn().Err(err).Str("pull_request_id", pr.ID.String()).Msg("failed to enqueue pull request health sync")
	}
}

func pullRequestStateSyncDedupeKey(pullRequestID uuid.UUID, scope string) string {
	dedupeKey := fmt.Sprintf("%s:%s", prHealthSyncJobType, pullRequestID.String())
	if scope != "" {
		dedupeKey = fmt.Sprintf("%s:%s", dedupeKey, scope)
	}
	return dedupeKey
}

func (s *PRService) enqueuePullRequestHealthEnrichment(ctx context.Context, pr models.PullRequest, version int64) {
	if s.jobs == nil {
		return
	}
	dedupeKey := fmt.Sprintf("%s:%s:%d", prHealthEnrichJobType, pr.ID.String(), version)
	_, err := s.jobs.Enqueue(ctx, pr.OrgID, prHealthSyncQueue, prHealthEnrichJobType, map[string]string{
		"org_id":          pr.OrgID.String(),
		"pull_request_id": pr.ID.String(),
		"version":         fmt.Sprintf("%d", version),
	}, 4, &dedupeKey)
	if err != nil {
		s.logger.Warn().Err(err).Str("pull_request_id", pr.ID.String()).Msg("failed to enqueue pull request health enrichment")
	}
}

func (s *PRService) enqueueMergeWhenReadyProcessing(ctx context.Context, pr models.PullRequest) {
	if s.jobs == nil || !isMergeWhenReadyProcessable(pr, time.Now()) {
		return
	}
	dedupeKey := fmt.Sprintf("%s:%s", prMergeWhenReadyJobType, pr.ID.String())
	_, err := s.jobs.Enqueue(ctx, pr.OrgID, prHealthSyncQueue, prMergeWhenReadyJobType, map[string]string{
		"org_id":          pr.OrgID.String(),
		"pull_request_id": pr.ID.String(),
	}, 7, &dedupeKey)
	if err != nil {
		s.logger.Warn().Err(err).Str("pull_request_id", pr.ID.String()).Msg("failed to enqueue merge-when-ready processing")
	}
}

func isMergeWhenReadyProcessable(pr models.PullRequest, now time.Time) bool {
	return pr.MergeWhenReadyState == models.PullRequestMergeWhenReadyStateQueued || isStaleMergeWhenReadyMerging(pr, now)
}

func (s *PRService) publishPullRequestUpdated(ctx context.Context, pr models.PullRequest, current models.PullRequestHealthCurrent) {
	if s.prHealthStreams != nil {
		if err := s.prHealthStreams.PublishUpdated(ctx, pr.OrgID, models.PullRequestUpdatedEvent{
			PullRequestID: pr.ID,
			Version:       current.Version,
			HeadSHA:       current.HeadSHA,
			BaseSHA:       current.BaseSHA,
			SyncedAt:      current.UpdatedAt,
		}); err != nil {
			s.logger.Warn().Err(err).Str("pull_request_id", pr.ID.String()).Msg("failed to publish pull request health update")
		}
	}
	s.logger.Debug().
		Str("pull_request_id", pr.ID.String()).
		Int64("version", current.Version).
		Str("head_sha", current.HeadSHA).
		Str("base_sha", current.BaseSHA).
		Msg("pull request health updated")
}

func repairPromptForAction(action models.PullRequestRepairActionType) string {
	switch action {
	case models.PullRequestRepairActionTypeResolveConflicts:
		return "Please resolve the conflicts."
	case models.PullRequestRepairActionTypeFixTests:
		return "Please fix these tests."
	default:
		return "Please repair this pull request."
	}
}

func normalizeRepairMergeState(existing models.PullRequestMergeState, mergeable *bool, githubState string) models.PullRequestMergeState {
	normalized, _ := normalizeMergeState(mergeable, githubState)
	if normalized != models.PullRequestMergeStateUnknown && normalized != models.PullRequestMergeStateMergeabilityPending {
		return normalized
	}
	return existing
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return value
		}
	}
	return ""
}

func firstNonNilString(values ...*string) *string {
	for _, value := range values {
		if value != nil && strings.TrimSpace(*value) != "" {
			return value
		}
	}
	return nil
}

func truncateText(value string, limit int) string {
	value = strings.TrimSpace(value)
	if limit <= 0 || len(value) <= limit {
		return value
	}
	return strings.TrimSpace(value[:limit]) + "…"
}

func stripWhitespace(value string) string {
	return strings.Join(strings.Fields(strings.TrimSpace(value)), " ")
}

func isSessionTerminalStatus(status models.SessionStatus) bool {
	switch status {
	case models.SessionStatusCompleted,
		models.SessionStatusPRCreated,
		models.SessionStatusFailed,
		models.SessionStatusCancelled,
		models.SessionStatusSkipped:
		return true
	default:
		return false
	}
}

func isUniqueActiveRepairRunViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == pgerrcode.UniqueViolation && pgErr.ConstraintName == "idx_pull_request_repair_runs_active"
}
