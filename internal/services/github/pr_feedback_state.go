package github

import (
	"context"
	"errors"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

var (
	ErrPRFeedbackNotConfigured = errors.New("pull request feedback follow-through is not configured")
	ErrPRFeedbackNotRetryable  = errors.New("pull request feedback batch is not retryable")
)

type prFeedbackPolicyInput struct {
	Organization models.AutomaticFollowThroughOrgSettings
	Personal     models.AutomaticFollowThroughPreference
	Monitoring   models.PRFeedbackMonitoring
	PrivateRepo  bool
	Linked       bool
	Archived     bool
}

type prFeedbackPolicy struct {
	HumanMode    models.PRFeedbackHumanMode
	BotMode      models.PRFeedbackBotMode
	CycleLimit   *int
	BotScope     models.PRFeedbackBotScope
	PausedReason string
}

func resolvePRFeedbackPolicy(input prFeedbackPolicyInput) prFeedbackPolicy {
	mode := input.Organization.PRFeedbackMode.Effective()
	botMode := input.Organization.PRFeedbackBotMode.Effective()
	policy := prFeedbackPolicy{
		HumanMode:  mode,
		BotMode:    botMode,
		CycleLimit: input.Organization.PRFeedbackBotCycleLimit.Effective(),
		BotScope:   models.PRFeedbackBotScopeTrustedPublic,
	}
	switch {
	case botMode == models.PRFeedbackBotModeNone:
		policy.BotScope = models.PRFeedbackBotScopeNone
	case botMode == models.PRFeedbackBotModeAllowlist:
		policy.BotScope = models.PRFeedbackBotScopeSelected
	case input.PrivateRepo:
		policy.BotScope = models.PRFeedbackBotScopeAllPrivate
	}
	switch {
	case mode == models.PRFeedbackHumanModeOff && botMode == models.PRFeedbackBotModeNone:
		policy.PausedReason = "organization_disabled"
	case input.Personal == models.AutomaticFollowThroughPreferenceOff:
		policy.PausedReason = "personal_disabled"
	case input.Monitoring == models.PRFeedbackMonitoringDisabled:
		policy.PausedReason = "pull_request_disabled"
	case !input.Linked:
		policy.PausedReason = "pull_request_not_linked_to_session"
	case input.Archived:
		policy.PausedReason = "session_archived"
	}
	return policy
}

func (s *PRService) GetPullRequestFeedbackState(ctx context.Context, orgID, pullRequestID, userID uuid.UUID) (*models.PullRequestFeedbackState, error) {
	if s.pullRequests == nil || s.feedback == nil || s.orgs == nil || s.users == nil || s.repos == nil || s.sessions == nil {
		return nil, ErrPRFeedbackNotConfigured
	}
	pr, err := s.pullRequests.GetByID(ctx, orgID, pullRequestID)
	if err != nil {
		return nil, err
	}
	org, err := s.orgs.GetByID(ctx, orgID)
	if err != nil {
		return nil, err
	}
	orgSettings, err := models.ParseOrgSettings(org.Settings)
	if err != nil {
		return nil, err
	}
	user, err := s.users.GetByIDGlobalWithSettings(ctx, userID)
	if err != nil {
		return nil, err
	}
	repo, err := s.repos.GetByFullNameAnyStatus(ctx, orgID, pr.GitHubRepo)
	if err != nil {
		return nil, err
	}
	items, err := s.feedback.ListRecentItems(ctx, orgID, pullRequestID, 20)
	if err != nil {
		return nil, err
	}
	pending, needsAttention, err := s.feedback.CountItemsByState(ctx, orgID, pullRequestID)
	if err != nil {
		return nil, err
	}
	active, err := s.feedback.GetActiveBatch(ctx, orgID, pullRequestID)
	if errors.Is(err, pgx.ErrNoRows) {
		active = nil
	} else if err != nil {
		return nil, err
	}
	personal := models.AutomaticFollowThroughPreferenceInherit
	if user.Settings.AutomaticPRFollowThrough != nil {
		personal = user.Settings.AutomaticPRFollowThrough.RespondToPRFeedback
	}
	archived := false
	if pr.SessionID != nil {
		session, sessionErr := s.sessions.GetByID(ctx, orgID, *pr.SessionID)
		if sessionErr != nil {
			return nil, sessionErr
		}
		archived = session.ArchivedAt != nil
	}
	effective := resolvePRFeedbackPolicy(prFeedbackPolicyInput{
		Organization: orgSettings.SessionAutomation.AutomaticFollowThrough,
		Personal:     personal,
		Monitoring:   pr.FeedbackMonitoring,
		PrivateRepo:  repo.Private,
		Linked:       pr.SessionID != nil,
		Archived:     archived,
	})
	state := &models.PullRequestFeedbackState{
		PullRequestID:          pullRequestID,
		EffectiveMode:          effective.HumanMode,
		EffectiveBotMode:       effective.BotMode,
		EffectiveBotCycleLimit: effective.CycleLimit,
		BotScope:               effective.BotScope,
		Monitoring:             pr.FeedbackMonitoring,
		PausedReason:           effective.PausedReason,
		PendingCount:           pending,
		NeedsAttentionCount:    needsAttention,
		ActiveBatch:            active,
		RecentItems:            items,
	}
	return state, nil
}

func (s *PRService) UpdatePullRequestFeedbackMonitoring(ctx context.Context, orgID, pullRequestID, userID uuid.UUID, monitoring models.PRFeedbackMonitoring) (*models.PullRequestFeedbackState, error) {
	if s.feedback == nil {
		return nil, ErrPRFeedbackNotConfigured
	}
	if err := s.feedback.UpdateMonitoring(ctx, orgID, pullRequestID, monitoring); err != nil {
		return nil, err
	}
	return s.GetPullRequestFeedbackState(ctx, orgID, pullRequestID, userID)
}

func (s *PRService) RetryPullRequestFeedbackBatch(ctx context.Context, orgID, pullRequestID, batchID, userID uuid.UUID) (*models.PullRequestFeedbackState, error) {
	if s.pullRequests == nil || s.feedback == nil {
		return nil, ErrPRFeedbackNotConfigured
	}
	pr, err := s.pullRequests.GetByID(ctx, orgID, pullRequestID)
	if err != nil {
		return nil, err
	}
	headSHA := ""
	if pr.HeadSHA != nil {
		headSHA = *pr.HeadSHA
	}
	retried, err := s.feedback.RetryBatch(ctx, orgID, pullRequestID, batchID, headSHA)
	if err != nil {
		return nil, err
	}
	if !retried {
		return nil, ErrPRFeedbackNotRetryable
	}
	return s.GetPullRequestFeedbackState(ctx, orgID, pullRequestID, userID)
}
