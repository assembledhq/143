package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/prompts"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type PRFeedbackTriageSummary struct {
	Eligible int
	Ignored  int
	Pending  int
}

func (s *PRService) TriagePendingPullRequestFeedback(ctx context.Context, orgID, pullRequestID uuid.UUID) (PRFeedbackTriageSummary, error) {
	var summary PRFeedbackTriageSummary
	if s.feedback == nil || s.pullRequests == nil || s.orgs == nil || s.repos == nil || s.sessions == nil {
		return summary, ErrPRFeedbackNotConfigured
	}
	pr, err := s.pullRequests.GetByID(ctx, orgID, pullRequestID)
	if err != nil {
		return summary, err
	}
	org, err := s.orgs.GetByID(ctx, orgID)
	if err != nil {
		return summary, err
	}
	settings, err := models.ParseOrgSettings(org.Settings)
	if err != nil {
		return summary, err
	}
	repo, err := s.repos.GetByFullNameAnyStatus(ctx, orgID, pr.GitHubRepo)
	if err != nil {
		return summary, err
	}
	personal := models.AutomaticFollowThroughPreferenceInherit
	archived := false
	if pr.SessionID != nil {
		session, sessionErr := s.sessions.GetByID(ctx, orgID, *pr.SessionID)
		if sessionErr != nil {
			return summary, sessionErr
		}
		archived = session.ArchivedAt != nil
		if session.TriggeredByUserID != nil && s.users != nil {
			user, userErr := s.users.GetByIDGlobalWithSettings(ctx, *session.TriggeredByUserID)
			if userErr != nil && !errors.Is(userErr, pgx.ErrNoRows) {
				return summary, userErr
			}
			if userErr == nil && user.Settings.AutomaticPRFollowThrough != nil {
				personal = user.Settings.AutomaticPRFollowThrough.RespondToPRFeedback
			}
		}
	}
	orgPolicy := settings.SessionAutomation.AutomaticFollowThrough
	effective := resolvePRFeedbackPolicy(prFeedbackPolicyInput{Organization: orgPolicy, Personal: personal, Monitoring: pr.FeedbackMonitoring, PrivateRepo: repo.Private, Linked: pr.SessionID != nil, Archived: archived})
	items, err := s.feedback.ListPendingItems(ctx, orgID, pullRequestID, 100)
	if err != nil {
		return summary, err
	}
	for _, item := range items {
		if effective.PausedReason != "" {
			if err := s.ignoreFeedbackItem(ctx, orgID, item, "policy_"+effective.PausedReason, models.PRFeedbackIntentUnsafe, models.PRFeedbackBotEligibilityNone, nil); err != nil {
				return summary, err
			}
			summary.Ignored++
			continue
		}
		ownApp := false
		if item.GitHubAppID != nil && s.tokenProvider != nil {
			ownApp = *item.GitHubAppID == s.tokenProvider.appID
		}
		eligibility := evaluatePRFeedbackEligibility(prFeedbackEligibilityInput{
			HumanMode: effective.HumanMode, BotMode: effective.BotMode, BotAllowlist: orgPolicy.PRFeedbackBotAllowlist,
			PrivateRepo: repo.Private, AuthorLogin: item.AuthorLogin, AuthorType: item.AuthorType,
			Association: item.AuthorAssociation, InstalledApp: item.GitHubAppID != nil && !ownApp,
			Mentioned: strings.Contains(strings.ToLower(item.Body), "@143"), OwnAppLogin: conditionalOwnAppLogin(ownApp, item.AuthorLogin), Body: item.Body,
		})
		if !eligibility.Eligible {
			if err := s.ignoreFeedbackItem(ctx, orgID, item, eligibility.IgnoreReason, models.PRFeedbackIntentUnsafe, eligibility.BotEligibility, nil); err != nil {
				return summary, err
			}
			summary.Ignored++
			continue
		}
		var fingerprint *string
		if item.AuthorType == models.PRFeedbackAuthorTypeBot {
			value := feedbackFindingFingerprint(item)
			fingerprint = &value
			duplicate, duplicateErr := s.feedback.HasBotFingerprintOnHead(ctx, orgID, pullRequestID, item.ID, value, item.ObservedHeadSHA)
			if duplicateErr != nil {
				return summary, duplicateErr
			}
			if duplicate {
				if err := s.ignoreFeedbackItem(ctx, orgID, item, "duplicate_bot_finding_same_head", models.PRFeedbackIntentAcknowledgement, eligibility.BotEligibility, fingerprint); err != nil {
					return summary, err
				}
				summary.Ignored++
				continue
			}
		}
		result, classified := deterministicPRFeedbackTriage(item)
		if !classified {
			if s.llmClient == nil {
				summary.Pending++
				continue
			}
			result, err = s.triageFeedbackWithLLM(ctx, item)
			if err != nil {
				return summary, err
			}
		}
		status := models.PRFeedbackItemStatusPending
		var ignoreReason *string
		if !result.RequiresAgent {
			status = models.PRFeedbackItemStatusIgnored
			reason := "triage_no_agent_required"
			ignoreReason = &reason
			summary.Ignored++
		} else {
			summary.Eligible++
		}
		if err := s.feedback.SetItemDecision(ctx, orgID, item.ID, result.Intent, status, ignoreReason, fingerprint, eligibility.BotEligibility); err != nil {
			return summary, err
		}
	}
	return summary, nil
}

func (s *PRService) ignoreFeedbackItem(ctx context.Context, orgID uuid.UUID, item models.PullRequestFeedbackItem, reason string, intent models.PRFeedbackIntent, eligibility models.PRFeedbackBotEligibilitySource, fingerprint *string) error {
	return s.feedback.SetItemDecision(ctx, orgID, item.ID, intent, models.PRFeedbackItemStatusIgnored, &reason, fingerprint, eligibility)
}

func (s *PRService) triageFeedbackWithLLM(ctx context.Context, item models.PullRequestFeedbackItem) (models.PRFeedbackTriageResult, error) {
	payload, err := json.Marshal(map[string]any{"item_id": item.ID, "author_login": item.AuthorLogin, "author_type": item.AuthorType, "surface": item.Surface, "body": item.Body, "path": item.Path, "line": item.Line})
	if err != nil {
		return models.PRFeedbackTriageResult{}, fmt.Errorf("marshal PR feedback triage input: %w", err)
	}
	raw, err := s.llmClient.Complete(ctx, prompts.PRFeedbackTriagePrompt(), string(payload))
	if err != nil {
		return models.PRFeedbackTriageResult{}, fmt.Errorf("triage PR feedback: %w", err)
	}
	var result models.PRFeedbackTriageResult
	if err := json.Unmarshal([]byte(extractPRFeedbackJSONObject(raw)), &result); err != nil {
		return result, fmt.Errorf("decode PR feedback triage: %w", err)
	}
	if err := result.Validate(); err != nil {
		return result, fmt.Errorf("validate PR feedback triage: %w", err)
	}
	return result, nil
}

func extractPRFeedbackJSONObject(value string) string {
	start := strings.IndexByte(value, '{')
	end := strings.LastIndexByte(value, '}')
	if start >= 0 && end >= start {
		return value[start : end+1]
	}
	return strings.TrimSpace(value)
}

func conditionalOwnAppLogin(own bool, login string) string {
	if own {
		return login
	}
	return ""
}
