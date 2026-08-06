package codereview

import (
	"context"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"golang.org/x/sync/errgroup"
)

type codeReviewInsightStore interface {
	ProjectDecision(ctx context.Context, orgID, sessionID uuid.UUID) error
	ProjectRecentDecisions(ctx context.Context, orgID uuid.UUID, staleBefore time.Time, limit int) (int64, error)
	ListPullRequestsForOutcomeReconciliation(ctx context.Context, orgID uuid.UUID, observedBefore time.Time, limit int) ([]models.CodeReviewOutcomePullRequest, error)
	ReconcilePullRequestOutcome(ctx context.Context, orgID, pullRequestID uuid.UUID, snapshot models.CodeReviewOutcomeSnapshot) error
	RecordOutcomeReconciliationAttempt(ctx context.Context, orgID, pullRequestID uuid.UUID, attemptedAt time.Time) error
	RankingEnabled(ctx context.Context, orgID uuid.UUID) (bool, error)
	ListPendingRankCandidates(ctx context.Context, orgID uuid.UUID, limit int, rankingEnabled bool) ([]db.CodeReviewRankCandidate, error)
	UpdateDisputeRanks(ctx context.Context, orgID uuid.UUID, updates []models.CodeReviewRankUpdate) error
}

type codeReviewOutcomeProvider interface {
	GetCodeReviewOutcomeSnapshot(ctx context.Context, orgID, repositoryID uuid.UUID, pullRequestNumber int) (models.CodeReviewOutcomeSnapshot, error)
}

type InsightService struct {
	store    codeReviewInsightStore
	provider codeReviewOutcomeProvider
	logger   zerolog.Logger
	now      func() time.Time
}

func (s *InsightService) SetOutcomeProvider(provider codeReviewOutcomeProvider) {
	s.provider = provider
}

func NewInsightService(store codeReviewInsightStore, logger zerolog.Logger) *InsightService {
	return &InsightService{store: store, logger: logger, now: time.Now}
}

func (s *InsightService) ProjectDecision(ctx context.Context, orgID, sessionID uuid.UUID) error {
	if s == nil || s.store == nil {
		return fmt.Errorf("code review insight store is unavailable")
	}
	return s.store.ProjectDecision(ctx, orgID, sessionID)
}

func (s *InsightService) ReconcileAndRank(ctx context.Context, orgID uuid.UUID) error {
	if s == nil || s.store == nil {
		return fmt.Errorf("code review insight store is unavailable")
	}
	staleBefore := s.now().UTC()
	var projected int64
	for range 20 {
		batch, err := s.store.ProjectRecentDecisions(ctx, orgID, staleBefore, 500)
		if err != nil {
			return err
		}
		projected += batch
		if batch < 500 {
			break
		}
	}
	var providerReconciled atomic.Int64
	if s.provider != nil {
		observedBefore := staleBefore.Add(-24 * time.Hour)
		attempted := make(map[uuid.UUID]struct{})
		for range 4 {
			pullRequests, err := s.store.ListPullRequestsForOutcomeReconciliation(ctx, orgID, observedBefore, 25)
			if err != nil {
				return err
			}
			progressed := false
			group, groupContext := errgroup.WithContext(ctx)
			group.SetLimit(5)
			for _, pullRequest := range pullRequests {
				if _, alreadyAttempted := attempted[pullRequest.PullRequestID]; alreadyAttempted {
					continue
				}
				attempted[pullRequest.PullRequestID] = struct{}{}
				progressed = true
				pullRequest := pullRequest
				group.Go(func() error {
					snapshot, err := s.provider.GetCodeReviewOutcomeSnapshot(groupContext, orgID, pullRequest.RepositoryID, pullRequest.GitHubPRNumber)
					if err != nil {
						if attemptErr := s.store.RecordOutcomeReconciliationAttempt(groupContext, orgID, pullRequest.PullRequestID, staleBefore); attemptErr != nil {
							return attemptErr
						}
						s.logger.Warn().Err(err).Str("org_id", orgID.String()).Str("pull_request_id", pullRequest.PullRequestID.String()).Msg("failed to load provider code review outcome")
						return nil
					}
					if err := s.store.ReconcilePullRequestOutcome(groupContext, orgID, pullRequest.PullRequestID, snapshot); err != nil {
						return err
					}
					providerReconciled.Add(1)
					return nil
				})
			}
			if err := group.Wait(); err != nil {
				return err
			}
			if len(pullRequests) < 25 || !progressed {
				break
			}
		}
	}
	ranked, enabled, err := s.RankPendingBatches(ctx, orgID, 500, 20)
	if err != nil {
		return err
	}
	s.logger.Info().Str("org_id", orgID.String()).Int64("projected", projected).
		Int64("provider_reconciled", providerReconciled.Load()).
		Int("ranked", ranked).Bool("ranking_enabled", enabled).
		Msg("reconciled code review decision insights")
	return nil
}

func (s *InsightService) RankPendingBatches(ctx context.Context, orgID uuid.UUID, limit, maxBatches int) (int, bool, error) {
	if limit <= 0 || limit > 1000 {
		limit = 500
	}
	if maxBatches <= 0 || maxBatches > 20 {
		maxBatches = 20
	}
	var ranked int
	var enabled bool
	for range maxBatches {
		batch, batchEnabled, err := s.RankPending(ctx, orgID, limit)
		if err != nil {
			return ranked, batchEnabled, err
		}
		enabled = batchEnabled
		ranked += batch
		if batch < limit {
			break
		}
	}
	return ranked, enabled, nil
}

func (s *InsightService) RankPending(ctx context.Context, orgID uuid.UUID, limit int) (int, bool, error) {
	enabled, err := s.store.RankingEnabled(ctx, orgID)
	if err != nil {
		return 0, false, err
	}
	candidates, err := s.store.ListPendingRankCandidates(ctx, orgID, limit, enabled)
	if err != nil {
		return 0, enabled, err
	}
	updates := make([]models.CodeReviewRankUpdate, 0, len(candidates))
	for _, candidate := range candidates {
		signals, priority := CodeReviewDisputeRank(candidate, enabled, s.now().UTC())
		updates = append(updates, models.CodeReviewRankUpdate{ID: candidate.Dispute.ID, Signals: signals, Priority: priority})
	}
	if err := s.store.UpdateDisputeRanks(ctx, orgID, updates); err != nil {
		return 0, enabled, err
	}
	return len(candidates), enabled, nil
}

// CodeReviewDisputeRank is deliberately simple and explainable. Signals only
// order human attention; they never adjudicate a dispute or change policy.
func CodeReviewDisputeRank(candidate db.CodeReviewRankCandidate, enabled bool, now time.Time) (models.CodeReviewQueueSignals, float64) {
	dispute := candidate.Dispute
	signals := models.CodeReviewQueueSignals{
		ReassessmentUnchanged: dispute.ReassessmentStatus == models.CodeReviewDisputeReassessmentCompleted &&
			dispute.ReassessmentFlipped != nil && !*dispute.ReassessmentFlipped,
		ReassessmentFlipped: dispute.ReassessmentStatus == models.CodeReviewDisputeReassessmentCompleted &&
			dispute.ReassessmentFlipped != nil && *dispute.ReassessmentFlipped,
		FilerIsNotPRAuthor:         !dispute.AuthorIsPRAuthor,
		RepeatReasonDisputes14Days: candidate.RepeatReasonCount,
		Escalated:                  dispute.EscalatedAt != nil,
		BasePolicySuperseded:       !candidate.BasePolicyActive,
		RankingEnabled:             enabled,
		ComputedAt:                 now,
	}
	if candidate.Outcome != nil {
		signals.OutcomeFresh = !candidate.Outcome.ObservedUntil.Before(dispute.CreatedAt)
		if dispute.Decision == models.CodeReviewDecisionApproved && candidate.Outcome.IndependentBlockingReviewLogin != nil {
			signals.IndependentHumanContradiction = true
			signals.IndependentHumanLogin = *candidate.Outcome.IndependentBlockingReviewLogin
		} else if dispute.Decision == models.CodeReviewDecisionBlocked && candidate.Outcome.IndependentApproverLogin != nil {
			signals.IndependentHumanContradiction = true
			signals.IndependentHumanLogin = *candidate.Outcome.IndependentApproverLogin
		}
	}
	if !enabled {
		return signals, 0
	}
	if signals.ReassessmentFlipped {
		return signals, -1000
	}
	if signals.BasePolicySuperseded {
		return signals, -500
	}
	priority := 0.0
	if signals.IndependentHumanContradiction {
		priority += 40
	}
	if signals.ReassessmentUnchanged {
		priority += 30
	}
	if signals.FilerIsNotPRAuthor {
		priority += 15
	}
	if signals.RepeatReasonDisputes14Days > 0 {
		repeats := signals.RepeatReasonDisputes14Days
		if repeats > 5 {
			repeats = 5
		}
		priority += float64(repeats * 5)
	}
	if signals.Escalated {
		priority += 20
	}
	return signals, priority
}
