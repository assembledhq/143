package codereview

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/jobctx"
	"github.com/assembledhq/143/internal/models"
	githubtelemetry "github.com/assembledhq/143/internal/services/github/telemetry"
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

// Counters are invocation-local. Only provider workers update the atomic fields;
// nil store returns describe method completion, not rows changed or applied work.
type insightInvocationTelemetry struct {
	phase, projectionStop, providerStop, rankingStop       string
	projectionCalls, projectionErrors                      int
	projected                                              int64
	selectionCalls, selectionErrors                        int
	selectedGroups, uniqueTargets, duplicateSkips          int
	providerBatches                                        int
	providerStarted, providerSucceeded, providerFailed     atomic.Int64
	attemptNil, attemptErrors, outcomeNil, outcomeErrors   atomic.Int64
	rankingBatches, rankingModeCalls, rankingModeErrors    int
	rankingSelectionCalls, rankingSelectionErrors          int
	rankingCandidates, rankingStoreNil, rankingStoreErrors int
	ranked                                                 int
	rankingEnabled                                         bool
}

func insightMethodResult(err error) string {
	switch {
	case err == nil:
		return "returned_nil"
	case errors.Is(err, context.Canceled):
		return "cancelled"
	case errors.Is(err, context.DeadlineExceeded):
		return "deadline_exceeded"
	default:
		return "returned_error"
	}
}

func insightContextState(ctx context.Context) string {
	if ctx.Err() == nil {
		return "active"
	}
	return insightMethodResult(ctx.Err())
}

func insightServiceEvent(logger zerolog.Logger, ctx context.Context, orgID uuid.UUID) *zerolog.Event {
	event := logger.Info().Str("org_id", orgID.String())
	if jobID, ok := jobctx.JobIDFromContext(ctx); ok && jobID != uuid.Nil {
		event.Str("job_id", jobID.String())
	}
	return event
}

func (s *InsightService) ProjectDecision(ctx context.Context, orgID, sessionID uuid.UUID) (resultErr error) {
	if s == nil || s.store == nil {
		return fmt.Errorf("code review insight store is unavailable")
	}
	startedAt := time.Now()
	defer func() {
		insightServiceEvent(s.logger, ctx, orgID).
			Str("insight_dispatch_kind", "session_projection").
			Str("invocation_result", insightMethodResult(resultErr)).
			Str("context_state", insightContextState(ctx)).
			Int64("duration_ms", time.Since(startedAt).Milliseconds()).
			Msg("completed code review insight projection")
	}()
	return s.store.ProjectDecision(ctx, orgID, sessionID)
}

func (s *InsightService) ReconcileAndRank(ctx context.Context, orgID uuid.UUID) (resultErr error) {
	if s == nil || s.store == nil {
		return fmt.Errorf("code review insight store is unavailable")
	}
	startedAt := time.Now()
	staleBefore := s.now().UTC()
	stats := &insightInvocationTelemetry{phase: "projection", projectionStop: "not_started", providerStop: "not_started", rankingStop: "not_started"}
	defer func() { s.logInsightInvocation(ctx, orgID, staleBefore, startedAt, stats, resultErr) }()
	var projected int64
	for range 20 {
		stats.projectionCalls++
		batch, err := s.store.ProjectRecentDecisions(ctx, orgID, staleBefore, 500)
		if err != nil {
			stats.projectionErrors++
			stats.projectionStop = "error"
			return err
		}
		projected += batch
		stats.projected = projected
		stats.projectionStop = "batch_limit_reached"
		if batch < 500 {
			stats.projectionStop = "short_batch"
			if batch == 0 {
				stats.projectionStop = "empty_batch"
			}
			break
		}
	}
	var providerReconciled atomic.Int64
	stats.providerStop = "provider_not_configured"
	if s.provider != nil {
		observedBefore := staleBefore.Add(-24 * time.Hour)
		attempted := make(map[uuid.UUID]struct{})
		for range 4 {
			stats.phase = "provider_selection"
			stats.selectionCalls++
			pullRequests, err := s.store.ListPullRequestsForOutcomeReconciliation(ctx, orgID, observedBefore, 25)
			if err != nil {
				stats.selectionErrors++
				stats.providerStop = "selection_error"
				return err
			}
			stats.selectedGroups += len(pullRequests)
			stats.phase = "provider_batch"
			progressed := false
			group, groupContext := errgroup.WithContext(ctx)
			group.SetLimit(5)
			for _, pullRequest := range pullRequests {
				if _, alreadyAttempted := attempted[pullRequest.PullRequestID]; alreadyAttempted {
					stats.duplicateSkips++
					continue
				}
				attempted[pullRequest.PullRequestID] = struct{}{}
				stats.uniqueTargets++
				progressed = true
				pullRequest := pullRequest
				group.Go(func() error {
					metadata, _ := githubtelemetry.RequestMetadataFromContext(groupContext)
					metadata.OrgID = orgID.String()
					metadata.PullRequestID = pullRequest.PullRequestID
					requestContext := githubtelemetry.WithRequestMetadata(groupContext, metadata)
					stats.providerStarted.Add(1)
					snapshot, err := s.provider.GetCodeReviewOutcomeSnapshot(requestContext, orgID, pullRequest.RepositoryID, pullRequest.GitHubPRNumber)
					if err != nil {
						stats.providerFailed.Add(1)
						if attemptErr := s.store.RecordOutcomeReconciliationAttempt(groupContext, orgID, pullRequest.PullRequestID, staleBefore); attemptErr != nil {
							stats.attemptErrors.Add(1)
							return attemptErr
						}
						stats.attemptNil.Add(1)
						s.logger.Warn().Err(err).Str("org_id", orgID.String()).Str("pull_request_id", pullRequest.PullRequestID.String()).Msg("failed to load provider code review outcome")
						return nil
					}
					stats.providerSucceeded.Add(1)
					if err := s.store.ReconcilePullRequestOutcome(groupContext, orgID, pullRequest.PullRequestID, snapshot); err != nil {
						stats.outcomeErrors.Add(1)
						return err
					}
					stats.outcomeNil.Add(1)
					providerReconciled.Add(1)
					return nil
				})
			}
			if err := group.Wait(); err != nil {
				stats.providerStop = "batch_error"
				return err
			}
			stats.providerBatches++
			stats.providerStop = "batch_limit_reached"
			if len(pullRequests) < 25 || !progressed {
				switch {
				case len(pullRequests) == 0:
					stats.providerStop = "empty_batch"
				case !progressed:
					stats.providerStop = "no_new_targets"
				default:
					stats.providerStop = "short_batch"
				}
				break
			}
		}
	}
	ranked, enabled, err := s.rankPendingBatches(ctx, orgID, 500, 20, stats)
	if err != nil {
		return err
	}
	s.logger.Info().Str("org_id", orgID.String()).Int64("projected", projected).
		Int64("provider_reconciled", providerReconciled.Load()).
		Int("ranked", ranked).Bool("ranking_enabled", enabled).
		Msg("reconciled code review decision insights")
	stats.phase = "completed"
	return nil
}

func (s *InsightService) logInsightInvocation(ctx context.Context, orgID uuid.UUID, staleBefore, startedAt time.Time, stats *insightInvocationTelemetry, resultErr error) {
	insightServiceEvent(s.logger, ctx, orgID).
		Str("insight_dispatch_kind", "organization_reconcile").
		Str("measurement_scope", "method_calls_not_http_or_applied_rows").
		Str("invocation_result", insightMethodResult(resultErr)).
		Str("context_state", insightContextState(ctx)).Str("phase", stats.phase).
		Time("stale_before", staleBefore).Time("outcome_observed_before", staleBefore.Add(-24*time.Hour)).
		Int64("duration_ms", time.Since(startedAt).Milliseconds()).
		Int("projection_calls", stats.projectionCalls).Int("projection_errors", stats.projectionErrors).
		Int64("projection_rows_reported", stats.projected).Str("projection_stop_reason", stats.projectionStop).
		Bool("projection_limit_reached", stats.projectionStop == "batch_limit_reached").
		Int("provider_selection_calls", stats.selectionCalls).Int("provider_selection_errors", stats.selectionErrors).
		Int("provider_selected_groups", stats.selectedGroups).Int("provider_unique_targets", stats.uniqueTargets).
		Int("provider_duplicate_skips", stats.duplicateSkips).Int("provider_batches_completed", stats.providerBatches).
		Int64("provider_snapshot_calls_started", stats.providerStarted.Load()).
		Int64("provider_snapshot_calls_succeeded", stats.providerSucceeded.Load()).
		Int64("provider_snapshot_calls_failed", stats.providerFailed.Load()).
		Int64("attempt_store_nil_returns", stats.attemptNil.Load()).Int64("attempt_store_errors", stats.attemptErrors.Load()).
		Int64("outcome_store_nil_returns", stats.outcomeNil.Load()).Int64("outcome_store_errors", stats.outcomeErrors.Load()).
		Str("provider_stop_reason", stats.providerStop).Bool("provider_limit_reached", stats.providerStop == "batch_limit_reached").
		Int("ranking_batches_started", stats.rankingBatches).Int("ranking_mode_calls", stats.rankingModeCalls).
		Int("ranking_mode_errors", stats.rankingModeErrors).Int("ranking_selection_calls", stats.rankingSelectionCalls).
		Int("ranking_selection_errors", stats.rankingSelectionErrors).Int("ranking_candidates_selected", stats.rankingCandidates).
		Int("ranking_store_nil_returns", stats.rankingStoreNil).Int("ranking_store_errors", stats.rankingStoreErrors).
		Int("ranking_rows_reported", stats.ranked).Bool("ranking_enabled", stats.rankingEnabled).
		Str("ranking_stop_reason", stats.rankingStop).Bool("ranking_limit_reached", stats.rankingStop == "batch_limit_reached").
		Msg("completed code review insight invocation")
}

func (s *InsightService) RankPendingBatches(ctx context.Context, orgID uuid.UUID, limit, maxBatches int) (int, bool, error) {
	return s.rankPendingBatches(ctx, orgID, limit, maxBatches, nil)
}

func (s *InsightService) rankPendingBatches(ctx context.Context, orgID uuid.UUID, limit, maxBatches int, stats *insightInvocationTelemetry) (int, bool, error) {
	if limit <= 0 || limit > 1000 {
		limit = 500
	}
	if maxBatches <= 0 || maxBatches > 20 {
		maxBatches = 20
	}
	var ranked int
	var enabled bool
	for range maxBatches {
		if stats != nil {
			stats.rankingBatches++
		}
		batch, batchEnabled, err := s.rankPending(ctx, orgID, limit, stats)
		if stats != nil {
			stats.rankingEnabled = batchEnabled
		}
		if err != nil {
			if stats != nil {
				stats.rankingStop = "error"
			}
			return ranked, batchEnabled, err
		}
		enabled = batchEnabled
		ranked += batch
		if stats != nil {
			stats.ranked = ranked
			stats.rankingStop = "batch_limit_reached"
		}
		if batch < limit {
			if stats != nil {
				stats.rankingStop = "short_batch"
				if batch == 0 {
					stats.rankingStop = "empty_batch"
				}
			}
			break
		}
	}
	return ranked, enabled, nil
}

func (s *InsightService) RankPending(ctx context.Context, orgID uuid.UUID, limit int) (int, bool, error) {
	return s.rankPending(ctx, orgID, limit, nil)
}

func (s *InsightService) rankPending(ctx context.Context, orgID uuid.UUID, limit int, stats *insightInvocationTelemetry) (int, bool, error) {
	if stats != nil {
		stats.phase = "ranking_mode"
		stats.rankingModeCalls++
	}
	enabled, err := s.store.RankingEnabled(ctx, orgID)
	if err != nil {
		if stats != nil {
			stats.rankingModeErrors++
		}
		return 0, false, err
	}
	if stats != nil {
		stats.phase = "ranking_selection"
		stats.rankingSelectionCalls++
	}
	candidates, err := s.store.ListPendingRankCandidates(ctx, orgID, limit, enabled)
	if err != nil {
		if stats != nil {
			stats.rankingSelectionErrors++
		}
		return 0, enabled, err
	}
	if stats != nil {
		stats.rankingCandidates += len(candidates)
	}
	updates := make([]models.CodeReviewRankUpdate, 0, len(candidates))
	for _, candidate := range candidates {
		signals, priority := CodeReviewDisputeRank(candidate, enabled, s.now().UTC())
		updates = append(updates, models.CodeReviewRankUpdate{ID: candidate.Dispute.ID, Signals: signals, Priority: priority})
	}
	if stats != nil {
		stats.phase = "ranking_store"
	}
	if err := s.store.UpdateDisputeRanks(ctx, orgID, updates); err != nil {
		if stats != nil {
			stats.rankingStoreErrors++
		}
		return 0, enabled, err
	}
	if stats != nil {
		stats.rankingStoreNil++
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
