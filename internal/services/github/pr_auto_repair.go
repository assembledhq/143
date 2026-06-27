package github

import (
	"context"
	"errors"
	"fmt"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"

	"github.com/assembledhq/143/internal/models"
)

const maxAutomaticRepairAttemptsPerHeadAction = 1

type AutoRepairDecisionStatus string

const (
	AutoRepairDecisionStarted         AutoRepairDecisionStatus = "started"
	AutoRepairDecisionDisabled        AutoRepairDecisionStatus = "disabled"
	AutoRepairDecisionNoPullRequest   AutoRepairDecisionStatus = "no_pull_request"
	AutoRepairDecisionNotOpen         AutoRepairDecisionStatus = "pull_request_not_open"
	AutoRepairDecisionNotResumable    AutoRepairDecisionStatus = "session_not_resumable"
	AutoRepairDecisionBlockedHealth   AutoRepairDecisionStatus = "health_blocked"
	AutoRepairDecisionNoBlocker       AutoRepairDecisionStatus = "no_blocker"
	AutoRepairDecisionActiveRepair    AutoRepairDecisionStatus = "active_repair"
	AutoRepairDecisionBudgetExhausted AutoRepairDecisionStatus = "budget_exhausted"
	AutoRepairDecisionHeadChanged     AutoRepairDecisionStatus = "head_changed"
	AutoRepairDecisionBusy            AutoRepairDecisionStatus = "session_busy"
)

type AutoRepairDecision struct {
	Status        AutoRepairDecisionStatus
	PullRequestID *uuid.UUID
	Action        models.PullRequestRepairActionType
	HeadSHA       string
	Reason        string
	Response      *models.PullRequestRepairResponse
}

func (s *PRService) MaybeStartAutoRepair(ctx context.Context, orgID uuid.UUID, sessionID uuid.UUID, reason string) (*AutoRepairDecision, error) {
	if s == nil || s.sessions == nil || s.pullRequests == nil || s.orgs == nil {
		return autoRepairDecision(AutoRepairDecisionDisabled, nil, "", "", "auto-repair dependencies are not configured"), nil
	}
	session, err := s.sessions.GetByID(ctx, orgID, sessionID)
	if err != nil {
		return nil, fmt.Errorf("load session for auto-repair: %w", err)
	}
	if !autoRepairSessionCanStart(session.Status) {
		return autoRepairDecision(AutoRepairDecisionNotResumable, nil, "", "", "session is not idle or resumable"), nil
	}

	pr, err := s.pullRequests.GetBySessionID(ctx, orgID, sessionID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return autoRepairDecision(AutoRepairDecisionNoPullRequest, nil, "", "", "session has no linked pull request"), nil
		}
		return nil, fmt.Errorf("load linked pull request for auto-repair: %w", err)
	}
	if pr.Status != models.PullRequestStatusOpen {
		return autoRepairDecision(AutoRepairDecisionNotOpen, &pr.ID, "", headSHAValue(pr.HeadSHA), "pull request is not open"), nil
	}

	policy, policySource, err := s.resolveAutoRepairPolicy(ctx, orgID, session)
	if err != nil {
		return nil, err
	}
	if !policy.ResolveConflicts && !policy.FixTests {
		return autoRepairDecision(AutoRepairDecisionDisabled, &pr.ID, "", headSHAValue(pr.HeadSHA), policySource), nil
	}

	if pr.HeadSHA != nil && *pr.HeadSHA != "" {
		exhausted, err := s.budgetExhaustedBeforeHealth(ctx, orgID, pr.ID, *pr.HeadSHA, policy)
		if err != nil {
			return nil, err
		}
		if exhausted {
			return autoRepairDecision(AutoRepairDecisionBudgetExhausted, &pr.ID, "", *pr.HeadSHA, "automatic repair already attempted for current head"), nil
		}
	}

	health, err := s.GetPullRequestHealth(ctx, orgID, pr.ID)
	if err != nil {
		return nil, fmt.Errorf("load pull request health for auto-repair: %w", err)
	}
	if health.SyncStatus == models.PullRequestHealthSyncStatusBlocked {
		return autoRepairDecision(AutoRepairDecisionBlockedHealth, &pr.ID, "", health.HeadSHA, string(health.SyncBlocker)), nil
	}
	if len(health.ActiveRepairs) > 0 {
		return autoRepairDecision(AutoRepairDecisionActiveRepair, &pr.ID, health.ActiveRepairs[0].ActionType, health.HeadSHA, "repair already in progress"), nil
	}

	action := chooseAutoRepairAction(policy, health)
	if action == "" {
		return autoRepairDecision(AutoRepairDecisionNoBlocker, &pr.ID, "", health.HeadSHA, "no enabled repair blocker found"), nil
	}
	exhausted, err := s.autoRepairBudgetExhausted(ctx, orgID, pr.ID, action, health.HeadSHA)
	if err != nil {
		return nil, err
	}
	if exhausted {
		return autoRepairDecision(AutoRepairDecisionBudgetExhausted, &pr.ID, action, health.HeadSHA, "automatic repair already attempted for current head and action"), nil
	}

	resp, err := s.StartPullRequestRepair(ctx, orgID, pr.ID, uuid.Nil, StartPullRequestRepairOptions{
		Action:            action,
		ExpectedHeadSHA:   health.HeadSHA,
		SystemAuthored:    true,
		AutoAttempt:       true,
		TriggerReason:     reason,
		TriggeredBySource: models.PullRequestRepairTriggeredBySourceSystemAutoRepair,
	})
	if err != nil {
		switch {
		case errors.Is(err, ErrRepairAlreadyInProgress):
			return autoRepairDecision(AutoRepairDecisionActiveRepair, &pr.ID, action, health.HeadSHA, "repair already in progress"), nil
		case errors.Is(err, ErrRepairSessionBusy):
			return autoRepairDecision(AutoRepairDecisionBusy, &pr.ID, action, health.HeadSHA, "session became busy"), nil
		case errors.Is(err, ErrRepairHeadChanged):
			return autoRepairDecision(AutoRepairDecisionHeadChanged, &pr.ID, action, health.HeadSHA, "pull request head changed"), nil
		default:
			return nil, err
		}
	}
	decision := autoRepairDecision(AutoRepairDecisionStarted, &pr.ID, action, health.HeadSHA, reason)
	decision.Response = resp
	return decision, nil
}

type autoRepairPolicy struct {
	ResolveConflicts bool
	FixTests         bool
}

func (s *PRService) resolveAutoRepairPolicy(ctx context.Context, orgID uuid.UUID, session models.Session) (autoRepairPolicy, string, error) {
	org, err := s.orgs.GetByID(ctx, orgID)
	if err != nil {
		return autoRepairPolicy{}, "", fmt.Errorf("load organization settings for auto-repair: %w", err)
	}
	settings, err := models.ParseOrgSettings(org.Settings)
	if err != nil {
		return autoRepairPolicy{}, "", fmt.Errorf("parse organization settings for auto-repair: %w", err)
	}
	followThrough := settings.SessionAutomation.AutomaticFollowThrough
	policy := autoRepairPolicy{
		ResolveConflicts: followThrough.ResolveConflictsWhenIdle,
		FixTests:         followThrough.FixTestsWhenIdle,
	}
	source := "organization default"

	if session.TriggeredByUserID == nil || s.users == nil {
		return policy, source, nil
	}
	user, err := s.users.GetByIDGlobalWithSettings(ctx, *session.TriggeredByUserID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return policy, source, nil
		}
		return autoRepairPolicy{}, "", fmt.Errorf("load user settings for auto-repair: %w", err)
	}
	if user.Settings.AutomaticPRFollowThrough == nil {
		return policy, source, nil
	}
	userSettings := user.Settings.AutomaticPRFollowThrough
	policy.ResolveConflicts = applyAutoRepairPreference(policy.ResolveConflicts, userSettings.ResolveConflictsWhenIdle)
	policy.FixTests = applyAutoRepairPreference(policy.FixTests, userSettings.FixTestsWhenIdle)
	return policy, "user preference over organization default", nil
}

func applyAutoRepairPreference(orgDefault bool, pref models.AutomaticFollowThroughPreference) bool {
	switch pref {
	case models.AutomaticFollowThroughPreferenceOn:
		return true
	case models.AutomaticFollowThroughPreferenceOff:
		return false
	default:
		return orgDefault
	}
}

func chooseAutoRepairAction(policy autoRepairPolicy, health *models.PullRequestHealthResponse) models.PullRequestRepairActionType {
	if policy.ResolveConflicts && health.CanResolveConflicts {
		return models.PullRequestRepairActionTypeResolveConflicts
	}
	if policy.FixTests && health.CanFixTests {
		return models.PullRequestRepairActionTypeFixTests
	}
	return ""
}

func (s *PRService) autoRepairBudgetExhausted(ctx context.Context, orgID, pullRequestID uuid.UUID, action models.PullRequestRepairActionType, headSHA string) (bool, error) {
	if headSHA == "" || action == "" {
		return true, nil
	}
	count, err := s.pullRequests.CountAutoRepairAttemptsByHead(ctx, orgID, pullRequestID, headSHA, action)
	if err != nil {
		return false, err
	}
	return count >= maxAutomaticRepairAttemptsPerHeadAction, nil
}

func (s *PRService) budgetExhaustedBeforeHealth(ctx context.Context, orgID, pullRequestID uuid.UUID, headSHA string, policy autoRepairPolicy) (bool, error) {
	anyEnabled := false
	allExhausted := true
	for _, action := range []models.PullRequestRepairActionType{models.PullRequestRepairActionTypeResolveConflicts, models.PullRequestRepairActionTypeFixTests} {
		if action == models.PullRequestRepairActionTypeResolveConflicts && !policy.ResolveConflicts {
			continue
		}
		if action == models.PullRequestRepairActionTypeFixTests && !policy.FixTests {
			continue
		}
		anyEnabled = true
		exhausted, err := s.autoRepairBudgetExhausted(ctx, orgID, pullRequestID, action, headSHA)
		if err != nil {
			return false, err
		}
		if !exhausted {
			allExhausted = false
		}
	}
	return anyEnabled && allExhausted, nil
}

func autoRepairSessionCanStart(status models.SessionStatus) bool {
	return status == models.SessionStatusIdle || status.IsResumable()
}

func autoRepairDecision(status AutoRepairDecisionStatus, pullRequestID *uuid.UUID, action models.PullRequestRepairActionType, headSHA, reason string) *AutoRepairDecision {
	return &AutoRepairDecision{
		Status:        status,
		PullRequestID: pullRequestID,
		Action:        action,
		HeadSHA:       headSHA,
		Reason:        reason,
	}
}

func headSHAValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
