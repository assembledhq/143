package codereview

import (
	"context"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

type insightServiceStoreStub struct {
	projectionBatches []int64
	projectionCalls   int
	pullRequests      []models.CodeReviewOutcomePullRequest
	reconciledID      uuid.UUID
	reconciled        models.CodeReviewOutcomeSnapshot
	rankBatches       [][]db.CodeReviewRankCandidate
	rankBatchCalls    int
	rankingEnabled    bool
	rankModes         []bool
	rankUpdates       []models.CodeReviewRankUpdate
}

func (s *insightServiceStoreStub) ProjectDecision(context.Context, uuid.UUID, uuid.UUID) error {
	return nil
}
func (s *insightServiceStoreStub) ProjectRecentDecisions(_ context.Context, _ uuid.UUID, _ time.Time, _ int) (int64, error) {
	index := s.projectionCalls
	s.projectionCalls++
	if index >= len(s.projectionBatches) {
		return 0, nil
	}
	return s.projectionBatches[index], nil
}
func (s *insightServiceStoreStub) ListPullRequestsForOutcomeReconciliation(context.Context, uuid.UUID, time.Time, int) ([]models.CodeReviewOutcomePullRequest, error) {
	return s.pullRequests, nil
}
func (s *insightServiceStoreStub) ReconcilePullRequestOutcome(_ context.Context, _ uuid.UUID, pullRequestID uuid.UUID, snapshot models.CodeReviewOutcomeSnapshot) error {
	s.reconciledID = pullRequestID
	s.reconciled = snapshot
	return nil
}
func (s *insightServiceStoreStub) RecordOutcomeReconciliationAttempt(context.Context, uuid.UUID, uuid.UUID, time.Time) error {
	return nil
}
func (s *insightServiceStoreStub) RankingEnabled(context.Context, uuid.UUID) (bool, error) {
	return s.rankingEnabled, nil
}
func (s *insightServiceStoreStub) ListPendingRankCandidates(_ context.Context, _ uuid.UUID, _ int, rankingEnabled bool) ([]db.CodeReviewRankCandidate, error) {
	s.rankModes = append(s.rankModes, rankingEnabled)
	index := s.rankBatchCalls
	s.rankBatchCalls++
	if index >= len(s.rankBatches) {
		return nil, nil
	}
	return s.rankBatches[index], nil
}
func (s *insightServiceStoreStub) UpdateDisputeRanks(_ context.Context, _ uuid.UUID, updates []models.CodeReviewRankUpdate) error {
	s.rankUpdates = append(s.rankUpdates, updates...)
	return nil
}

func TestInsightService_RankPendingBatchesDrainsFullBatches(t *testing.T) {
	t.Parallel()

	firstBatch := make([]db.CodeReviewRankCandidate, 500)
	for index := range firstBatch {
		firstBatch[index] = db.CodeReviewRankCandidate{
			Dispute:          models.CodeReviewDispute{ID: uuid.New()},
			BasePolicyActive: true,
		}
	}
	finalCandidateID := uuid.New()
	store := &insightServiceStoreStub{rankBatches: [][]db.CodeReviewRankCandidate{
		firstBatch,
		{{Dispute: models.CodeReviewDispute{ID: finalCandidateID}, BasePolicyActive: true}},
	}}
	service := NewInsightService(store, zerolog.Nop())

	ranked, enabled, err := service.RankPendingBatches(context.Background(), uuid.New(), 500, 20)

	require.NoError(t, err, "ranking should drain every available stale batch")
	require.False(t, enabled, "low-volume ranking should remain chronological")
	require.Equal(t, 501, ranked, "ranking should process candidates beyond the first 500 rows")
	require.Equal(t, 2, store.rankBatchCalls, "ranking should stop after the first partial batch")
	require.Equal(t, []bool{false, false}, store.rankModes, "candidate selection should receive the current organization-wide ranking mode")
	require.Len(t, store.rankUpdates, 501, "ranking should persist an update for every candidate")
	require.Equal(t, finalCandidateID, store.rankUpdates[500].ID, "ranking should include the candidate after the full first batch")
}

func TestInsightService_RankPendingPropagatesEnabledMode(t *testing.T) {
	t.Parallel()

	store := &insightServiceStoreStub{rankingEnabled: true}
	service := NewInsightService(store, zerolog.Nop())

	ranked, enabled, err := service.RankPending(context.Background(), uuid.New(), 25)

	require.NoError(t, err, "enabled ranking should query pending candidates")
	require.Zero(t, ranked, "an empty enabled queue should not report ranked disputes")
	require.True(t, enabled, "ranking should return the current organization-wide mode")
	require.Equal(t, []bool{true}, store.rankModes, "candidate selection should invalidate snapshots computed under the previous ranking mode")
}
func (s *insightServiceStoreStub) GetInsights(context.Context, uuid.UUID, models.CodeReviewInsightFilters) (models.CodeReviewInsights, error) {
	return models.CodeReviewInsights{}, nil
}

type insightOutcomeProviderStub struct {
	calledRepositoryID uuid.UUID
	calledPRNumber     int
	snapshot           models.CodeReviewOutcomeSnapshot
}

func (p *insightOutcomeProviderStub) GetCodeReviewOutcomeSnapshot(_ context.Context, _ uuid.UUID, repositoryID uuid.UUID, pullRequestNumber int) (models.CodeReviewOutcomeSnapshot, error) {
	p.calledRepositoryID = repositoryID
	p.calledPRNumber = pullRequestNumber
	return p.snapshot, nil
}

func TestInsightService_ReconcileAndRankDrainsProjectionBatchesAndRepairsProviderOutcome(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 8, 4, 7, 0, 0, 0, time.UTC)
	pullRequestID := uuid.New()
	repositoryID := uuid.New()
	store := &insightServiceStoreStub{
		projectionBatches: []int64{500, 500, 2},
		pullRequests:      []models.CodeReviewOutcomePullRequest{{PullRequestID: pullRequestID, RepositoryID: repositoryID, GitHubPRNumber: 42}},
	}
	provider := &insightOutcomeProviderStub{snapshot: models.CodeReviewOutcomeSnapshot{AuthorLogin: "author", State: "open", ObservedAt: now}}
	service := NewInsightService(store, zerolog.Nop())
	service.now = func() time.Time { return now }
	service.SetOutcomeProvider(provider)

	err := service.ReconcileAndRank(context.Background(), uuid.New())

	require.NoError(t, err, "reconciliation should repair projections, provider outcomes, and ranks")
	require.Equal(t, 3, store.projectionCalls, "reconciliation should drain full projection batches instead of starving older rows")
	require.Equal(t, repositoryID, provider.calledRepositoryID, "provider reconciliation should use the projected repository")
	require.Equal(t, 42, provider.calledPRNumber, "provider reconciliation should use the projected pull request number")
	require.Equal(t, pullRequestID, store.reconciledID, "provider snapshot should update the intended pull request")
	require.Equal(t, provider.snapshot, store.reconciled, "provider snapshot should be persisted exactly")
}

func TestCodeReviewDisputeRank(t *testing.T) {
	t.Parallel()
	now := time.Date(2026, time.August, 4, 7, 0, 0, 0, time.UTC)
	unchanged := false
	flipped := true
	login := "maintainer"
	base := db.CodeReviewRankCandidate{
		Dispute: models.CodeReviewDispute{
			ID: uuid.New(), Decision: models.CodeReviewDecisionBlocked, CreatedAt: now.Add(-time.Hour),
			ReassessmentStatus:  models.CodeReviewDisputeReassessmentCompleted,
			ReassessmentFlipped: &unchanged,
		},
		Outcome:           &models.CodeReviewDecisionOutcome{ObservedUntil: now, IndependentApproverLogin: &login},
		RepeatReasonCount: 3, BasePolicyActive: true,
	}
	tests := []struct {
		name             string
		candidate        db.CodeReviewRankCandidate
		enabled          bool
		expectedPriority float64
		expected         models.CodeReviewQueueSignals
	}{
		{
			name: "persists signals but keeps low volume queue flat", candidate: base, enabled: false,
			expectedPriority: 0,
			expected: models.CodeReviewQueueSignals{IndependentHumanContradiction: true, IndependentHumanLogin: login,
				ReassessmentUnchanged: true, FilerIsNotPRAuthor: true, RepeatReasonDisputes14Days: 3,
				OutcomeFresh: true, ComputedAt: now},
		},
		{
			name: "ranks explainable signals after volume trigger", candidate: base, enabled: true,
			expectedPriority: 100,
			expected: models.CodeReviewQueueSignals{IndependentHumanContradiction: true, IndependentHumanLogin: login,
				ReassessmentUnchanged: true, FilerIsNotPRAuthor: true, RepeatReasonDisputes14Days: 3,
				OutcomeFresh: true, RankingEnabled: true, ComputedAt: now},
		},
		{
			name: "does not treat a compatible approval as contradicting comment only", candidate: func() db.CodeReviewRankCandidate {
				candidate := base
				candidate.Dispute.Decision = models.CodeReviewDecisionCommentOnly
				return candidate
			}(), enabled: true, expectedPriority: 60,
			expected: models.CodeReviewQueueSignals{
				ReassessmentUnchanged: true, FilerIsNotPRAuthor: true, RepeatReasonDisputes14Days: 3,
				OutcomeFresh: true, RankingEnabled: true, ComputedAt: now,
			},
		},
		{
			name: "de-ranks disputes against superseded policy", candidate: func() db.CodeReviewRankCandidate {
				candidate := base
				candidate.BasePolicyActive = false
				return candidate
			}(), enabled: true, expectedPriority: -500,
			expected: models.CodeReviewQueueSignals{IndependentHumanContradiction: true, IndependentHumanLogin: login,
				ReassessmentUnchanged: true, FilerIsNotPRAuthor: true, RepeatReasonDisputes14Days: 3,
				BasePolicySuperseded: true, OutcomeFresh: true, RankingEnabled: true, ComputedAt: now},
		},
		{
			name: "excludes a reassessment that already flipped the decision", candidate: func() db.CodeReviewRankCandidate {
				candidate := base
				candidate.Dispute.ReassessmentFlipped = &flipped
				return candidate
			}(), enabled: true, expectedPriority: -1000,
			expected: models.CodeReviewQueueSignals{IndependentHumanContradiction: true, IndependentHumanLogin: login,
				ReassessmentFlipped: true, FilerIsNotPRAuthor: true, RepeatReasonDisputes14Days: 3,
				OutcomeFresh: true, RankingEnabled: true, ComputedAt: now},
		},
		{
			name: "uses durable contradiction evidence while marking a stale snapshot", candidate: func() db.CodeReviewRankCandidate {
				candidate := base
				outcome := *candidate.Outcome
				candidate.Outcome = &outcome
				candidate.Outcome.ObservedUntil = candidate.Dispute.CreatedAt.Add(-time.Second)
				return candidate
			}(), enabled: true, expectedPriority: 100,
			expected: models.CodeReviewQueueSignals{IndependentHumanContradiction: true, IndependentHumanLogin: login,
				ReassessmentUnchanged: true, FilerIsNotPRAuthor: true, RepeatReasonDisputes14Days: 3,
				RankingEnabled: true, ComputedAt: now},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			actual, priority := CodeReviewDisputeRank(tt.candidate, tt.enabled, now)
			require.Equal(t, tt.expected, actual, "ranking should persist the exact explainable signal snapshot")
			require.Equal(t, tt.expectedPriority, priority, "ranking should apply the expected attention priority")
		})
	}
}
