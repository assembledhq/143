package codereview

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"sync/atomic"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/jobctx"
	"github.com/assembledhq/143/internal/models"
	githubtelemetry "github.com/assembledhq/143/internal/services/github/telemetry"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

type insightTelemetryStore struct {
	rankingEnabled                                                                 bool
	projectionBatches                                                              []int64
	providerBatches                                                                [][]models.CodeReviewOutcomePullRequest
	rankBatches                                                                    [][]db.CodeReviewRankCandidate
	failureStage                                                                   string
	failure                                                                        error
	projectionCalls, selectionCalls, modeCalls, rankSelectionCalls, rankStoreCalls int
	projectionCutoffs, providerCutoffs                                             []time.Time
	projectionLimits, providerLimits, rankLimits                                   []int
	attemptCalls, outcomeCalls                                                     atomic.Int64
	projectContext                                                                 context.Context
	projectOrg, projectSession                                                     uuid.UUID
}

func (s *insightTelemetryStore) stageError(stage string) error {
	if s.failureStage == stage {
		return s.failure
	}
	return nil
}
func (s *insightTelemetryStore) ProjectDecision(ctx context.Context, orgID, sessionID uuid.UUID) error {
	s.projectContext, s.projectOrg, s.projectSession = ctx, orgID, sessionID
	return s.stageError("session_projection")
}
func (s *insightTelemetryStore) ProjectRecentDecisions(_ context.Context, _ uuid.UUID, cutoff time.Time, limit int) (int64, error) {
	index := s.projectionCalls
	s.projectionCalls++
	s.projectionCutoffs = append(s.projectionCutoffs, cutoff)
	s.projectionLimits = append(s.projectionLimits, limit)
	if err := s.stageError("projection"); err != nil {
		return 0, err
	}
	if index >= len(s.projectionBatches) {
		return 0, nil
	}
	return s.projectionBatches[index], nil
}
func (s *insightTelemetryStore) ListPullRequestsForOutcomeReconciliation(_ context.Context, _ uuid.UUID, cutoff time.Time, limit int) ([]models.CodeReviewOutcomePullRequest, error) {
	index := s.selectionCalls
	s.selectionCalls++
	s.providerCutoffs = append(s.providerCutoffs, cutoff)
	s.providerLimits = append(s.providerLimits, limit)
	if err := s.stageError("selection"); err != nil {
		return nil, err
	}
	if index >= len(s.providerBatches) {
		return nil, nil
	}
	return s.providerBatches[index], nil
}
func (s *insightTelemetryStore) RecordOutcomeReconciliationAttempt(context.Context, uuid.UUID, uuid.UUID, time.Time) error {
	s.attemptCalls.Add(1)
	return s.stageError("attempt")
}
func (s *insightTelemetryStore) ReconcilePullRequestOutcome(context.Context, uuid.UUID, uuid.UUID, models.CodeReviewOutcomeSnapshot) error {
	s.outcomeCalls.Add(1)
	return s.stageError("outcome")
}
func (s *insightTelemetryStore) RankingEnabled(context.Context, uuid.UUID) (bool, error) {
	s.modeCalls++
	return s.rankingEnabled, s.stageError("ranking_mode")
}
func (s *insightTelemetryStore) ListPendingRankCandidates(_ context.Context, _ uuid.UUID, limit int, _ bool) ([]db.CodeReviewRankCandidate, error) {
	index := s.rankSelectionCalls
	s.rankSelectionCalls++
	s.rankLimits = append(s.rankLimits, limit)
	if err := s.stageError("ranking_selection"); err != nil {
		return nil, err
	}
	if index >= len(s.rankBatches) {
		return nil, nil
	}
	return s.rankBatches[index], nil
}
func (s *insightTelemetryStore) UpdateDisputeRanks(context.Context, uuid.UUID, []models.CodeReviewRankUpdate) error {
	s.rankStoreCalls++
	return s.stageError("ranking_store")
}

func insightTelemetryEvents(t *testing.T, logs, message string) []map[string]any {
	t.Helper()
	decoder := json.NewDecoder(bytes.NewBufferString(logs))
	var events []map[string]any
	for {
		var event map[string]any
		err := decoder.Decode(&event)
		if errors.Is(err, io.EOF) {
			break
		}
		require.NoError(t, err, "each captured service event should be valid JSON")
		if event["message"] == message {
			events = append(events, event)
		}
	}
	return events
}

func newInsightTelemetryTargets(count int) []models.CodeReviewOutcomePullRequest {
	targets := make([]models.CodeReviewOutcomePullRequest, count)
	for index := range targets {
		targets[index] = models.CodeReviewOutcomePullRequest{PullRequestID: uuid.New(), RepositoryID: uuid.New(), GitHubPRNumber: index + 1}
	}
	return targets
}

func expectedInsightInvocation(orgID, jobID uuid.UUID, now time.Time) map[string]any {
	return map[string]any{
		"level": "info", "message": "completed code review insight invocation", "org_id": orgID.String(), "job_id": jobID.String(),
		"insight_dispatch_kind": "organization_reconcile", "measurement_scope": "method_calls_not_http_or_applied_rows",
		"invocation_result": "returned_nil", "context_state": "active", "phase": "completed",
		"stale_before": now.UTC().Format(time.RFC3339), "outcome_observed_before": now.UTC().Add(-24 * time.Hour).Format(time.RFC3339),
		"projection_calls": float64(1), "projection_errors": float64(0), "projection_rows_reported": float64(0), "projection_stop_reason": "empty_batch", "projection_limit_reached": false,
		"provider_selection_calls": float64(0), "provider_selection_errors": float64(0), "provider_selected_groups": float64(0), "provider_unique_targets": float64(0), "provider_duplicate_skips": float64(0), "provider_batches_completed": float64(0),
		"provider_snapshot_calls_started": float64(0), "provider_snapshot_calls_succeeded": float64(0), "provider_snapshot_calls_failed": float64(0),
		"attempt_store_nil_returns": float64(0), "attempt_store_errors": float64(0), "outcome_store_nil_returns": float64(0), "outcome_store_errors": float64(0), "provider_stop_reason": "provider_not_configured", "provider_limit_reached": false,
		"ranking_batches_started": float64(1), "ranking_mode_calls": float64(1), "ranking_mode_errors": float64(0), "ranking_selection_calls": float64(1), "ranking_selection_errors": float64(0), "ranking_candidates_selected": float64(0), "ranking_store_nil_returns": float64(1), "ranking_store_errors": float64(0), "ranking_rows_reported": float64(0), "ranking_enabled": false, "ranking_stop_reason": "empty_batch", "ranking_limit_reached": false,
	}
}

func TestInsightServiceInvocationTelemetry(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name                string
		setup               func(*insightTelemetryStore)
		provider            bool
		providerFailure     bool
		cancelled, deadline bool
		noJob               bool
		failureStage        string
		expectedError       bool
		expected            map[string]any
	}{
		{name: "no configured provider"},
		{name: "no job identity is invented", noJob: true},
		{name: "short projection and enabled ranking batches", setup: func(s *insightTelemetryStore) {
			s.projectionBatches = []int64{2}
			s.rankBatches = [][]db.CodeReviewRankCandidate{make([]db.CodeReviewRankCandidate, 2)}
			s.rankingEnabled = true
		}, expected: map[string]any{"projection_rows_reported": float64(2), "projection_stop_reason": "short_batch", "ranking_candidates_selected": float64(2), "ranking_rows_reported": float64(2), "ranking_enabled": true, "ranking_stop_reason": "short_batch"}},
		{name: "no selected provider rows", provider: true, expected: map[string]any{"provider_selection_calls": float64(1), "provider_batches_completed": float64(1), "provider_stop_reason": "empty_batch"}},
		{name: "all provider failures are swallowed", provider: true, providerFailure: true, setup: func(s *insightTelemetryStore) {
			s.providerBatches = [][]models.CodeReviewOutcomePullRequest{newInsightTelemetryTargets(3)}
		}, expected: map[string]any{"provider_selection_calls": float64(1), "provider_selected_groups": float64(3), "provider_unique_targets": float64(3), "provider_batches_completed": float64(1), "provider_snapshot_calls_started": float64(3), "provider_snapshot_calls_failed": float64(3), "attempt_store_nil_returns": float64(3), "provider_stop_reason": "short_batch"}},
		{name: "full four batches remain one hundred per invocation", provider: true, setup: func(s *insightTelemetryStore) {
			for range 4 {
				s.providerBatches = append(s.providerBatches, newInsightTelemetryTargets(25))
			}
		}, expected: map[string]any{"provider_selection_calls": float64(4), "provider_selected_groups": float64(100), "provider_unique_targets": float64(100), "provider_batches_completed": float64(4), "provider_snapshot_calls_started": float64(100), "provider_snapshot_calls_succeeded": float64(100), "outcome_store_nil_returns": float64(100), "provider_stop_reason": "batch_limit_reached", "provider_limit_reached": true}},
		{name: "duplicate only batch stops without new calls", provider: true, setup: func(s *insightTelemetryStore) {
			batch := newInsightTelemetryTargets(25)
			s.providerBatches = [][]models.CodeReviewOutcomePullRequest{batch, batch}
		}, expected: map[string]any{"provider_selection_calls": float64(2), "provider_selected_groups": float64(50), "provider_unique_targets": float64(25), "provider_duplicate_skips": float64(25), "provider_batches_completed": float64(2), "provider_snapshot_calls_started": float64(25), "provider_snapshot_calls_succeeded": float64(25), "outcome_store_nil_returns": float64(25), "provider_stop_reason": "no_new_targets"}},
		{name: "full projection and ranking final batches only report limits", setup: func(s *insightTelemetryStore) {
			for range 20 {
				s.projectionBatches = append(s.projectionBatches, 500)
				s.rankBatches = append(s.rankBatches, make([]db.CodeReviewRankCandidate, 500))
			}
		}, expected: map[string]any{"projection_calls": float64(20), "projection_rows_reported": float64(10000), "projection_stop_reason": "batch_limit_reached", "projection_limit_reached": true, "ranking_batches_started": float64(20), "ranking_mode_calls": float64(20), "ranking_selection_calls": float64(20), "ranking_candidates_selected": float64(10000), "ranking_store_nil_returns": float64(20), "ranking_rows_reported": float64(10000), "ranking_stop_reason": "batch_limit_reached", "ranking_limit_reached": true}},
		{name: "projection error", failureStage: "projection", expectedError: true, expected: map[string]any{"invocation_result": "returned_error", "phase": "projection", "projection_errors": float64(1), "projection_stop_reason": "error", "provider_stop_reason": "not_started", "ranking_batches_started": float64(0), "ranking_mode_calls": float64(0), "ranking_selection_calls": float64(0), "ranking_store_nil_returns": float64(0), "ranking_stop_reason": "not_started"}},
		{name: "selector error", provider: true, failureStage: "selection", expectedError: true, expected: map[string]any{"invocation_result": "returned_error", "phase": "provider_selection", "provider_selection_calls": float64(1), "provider_selection_errors": float64(1), "provider_stop_reason": "selection_error", "ranking_batches_started": float64(0), "ranking_mode_calls": float64(0), "ranking_selection_calls": float64(0), "ranking_store_nil_returns": float64(0), "ranking_stop_reason": "not_started"}},
		{name: "attempt marker error", provider: true, providerFailure: true, failureStage: "attempt", expectedError: true, setup: func(s *insightTelemetryStore) {
			s.providerBatches = [][]models.CodeReviewOutcomePullRequest{newInsightTelemetryTargets(1)}
		}, expected: map[string]any{"invocation_result": "returned_error", "phase": "provider_batch", "provider_selection_calls": float64(1), "provider_selected_groups": float64(1), "provider_unique_targets": float64(1), "provider_snapshot_calls_started": float64(1), "provider_snapshot_calls_failed": float64(1), "attempt_store_errors": float64(1), "provider_stop_reason": "batch_error", "ranking_batches_started": float64(0), "ranking_mode_calls": float64(0), "ranking_selection_calls": float64(0), "ranking_store_nil_returns": float64(0), "ranking_stop_reason": "not_started"}},
		{name: "outcome store error", provider: true, failureStage: "outcome", expectedError: true, setup: func(s *insightTelemetryStore) {
			s.providerBatches = [][]models.CodeReviewOutcomePullRequest{newInsightTelemetryTargets(1)}
		}, expected: map[string]any{"invocation_result": "returned_error", "phase": "provider_batch", "provider_selection_calls": float64(1), "provider_selected_groups": float64(1), "provider_unique_targets": float64(1), "provider_snapshot_calls_started": float64(1), "provider_snapshot_calls_succeeded": float64(1), "outcome_store_errors": float64(1), "provider_stop_reason": "batch_error", "ranking_batches_started": float64(0), "ranking_mode_calls": float64(0), "ranking_selection_calls": float64(0), "ranking_store_nil_returns": float64(0), "ranking_stop_reason": "not_started"}},
		{name: "ranking mode error", failureStage: "ranking_mode", expectedError: true, expected: map[string]any{"invocation_result": "returned_error", "phase": "ranking_mode", "ranking_mode_errors": float64(1), "ranking_selection_calls": float64(0), "ranking_store_nil_returns": float64(0), "ranking_stop_reason": "error"}},
		{name: "ranking selector error", failureStage: "ranking_selection", expectedError: true, expected: map[string]any{"invocation_result": "returned_error", "phase": "ranking_selection", "ranking_selection_errors": float64(1), "ranking_store_nil_returns": float64(0), "ranking_stop_reason": "error"}},
		{name: "ranking store error", failureStage: "ranking_store", expectedError: true, setup: func(s *insightTelemetryStore) {
			s.rankBatches = [][]db.CodeReviewRankCandidate{make([]db.CodeReviewRankCandidate, 2)}
		}, expected: map[string]any{"invocation_result": "returned_error", "phase": "ranking_store", "ranking_candidates_selected": float64(2), "ranking_store_nil_returns": float64(0), "ranking_store_errors": float64(1), "ranking_stop_reason": "error"}},
		{name: "cancellation keeps original return", cancelled: true, failureStage: "projection", expectedError: true, expected: map[string]any{"invocation_result": "cancelled", "context_state": "cancelled", "phase": "projection", "projection_errors": float64(1), "projection_stop_reason": "error", "provider_stop_reason": "not_started", "ranking_batches_started": float64(0), "ranking_mode_calls": float64(0), "ranking_selection_calls": float64(0), "ranking_store_nil_returns": float64(0), "ranking_stop_reason": "not_started"}},
		{name: "deadline keeps original return", deadline: true, failureStage: "projection", expectedError: true, expected: map[string]any{"invocation_result": "deadline_exceeded", "context_state": "deadline_exceeded", "phase": "projection", "projection_errors": float64(1), "projection_stop_reason": "error", "provider_stop_reason": "not_started", "ranking_batches_started": float64(0), "ranking_mode_calls": float64(0), "ranking_selection_calls": float64(0), "ranking_store_nil_returns": float64(0), "ranking_stop_reason": "not_started"}},
		{name: "swallowed cancelled provider does not change nil return", provider: true, providerFailure: true, cancelled: true, setup: func(s *insightTelemetryStore) {
			s.providerBatches = [][]models.CodeReviewOutcomePullRequest{newInsightTelemetryTargets(1)}
		}, expected: map[string]any{"context_state": "cancelled", "provider_selection_calls": float64(1), "provider_selected_groups": float64(1), "provider_unique_targets": float64(1), "provider_batches_completed": float64(1), "provider_snapshot_calls_started": float64(1), "provider_snapshot_calls_failed": float64(1), "attempt_store_nil_returns": float64(1), "provider_stop_reason": "short_batch"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			orgID, jobID, lockToken := uuid.New(), uuid.New(), uuid.New()
			now := time.Date(2026, 8, 4, 7, 0, 0, 0, time.FixedZone("fixture", 3600))
			failure := errors.New("private-error-sentinel https://private.invalid/path?signature=private-query Bearer private-token")
			var expectedErr error = failure
			ctx := jobctx.WithLockToken(jobctx.WithJobID(context.Background(), jobID), lockToken)
			if tt.noJob {
				ctx = jobctx.WithLockToken(context.Background(), lockToken)
			}
			ctx = githubtelemetry.WithRequestMetadata(ctx, githubtelemetry.RequestMetadata{OrgID: uuid.NewString(), Repository: "private-repository-sentinel", SessionID: uuid.NewString(), AssessmentID: uuid.New()})
			if tt.cancelled {
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
				expectedErr = context.Canceled
			}
			if tt.deadline {
				var cancel context.CancelFunc
				ctx, cancel = context.WithDeadline(ctx, time.Now().Add(-time.Second))
				defer cancel()
				expectedErr = context.DeadlineExceeded
			}
			store := &insightTelemetryStore{failureStage: tt.failureStage, failure: expectedErr}
			if tt.setup != nil {
				tt.setup(store)
			}
			var logs bytes.Buffer
			service := NewInsightService(store, zerolog.New(zerolog.SyncWriter(&logs)))
			var nowCalls atomic.Int64
			service.now = func() time.Time { nowCalls.Add(1); return now }
			var providerCalls atomic.Int64
			if tt.provider {
				service.SetOutcomeProvider(outcomeAttributionProviderFunc(func(context.Context, uuid.UUID, uuid.UUID, int) (models.CodeReviewOutcomeSnapshot, error) {
					providerCalls.Add(1)
					if tt.providerFailure {
						return models.CodeReviewOutcomeSnapshot{}, expectedErr
					}
					return models.CodeReviewOutcomeSnapshot{AuthorLogin: "private-author-sentinel"}, nil
				}))
			}
			err := service.ReconcileAndRank(ctx, orgID)
			if tt.expectedError {
				require.True(t, err == expectedErr, "telemetry should return the exact original dependency or cancellation error")
			} else {
				require.NoError(t, err, "telemetry should preserve the original nil result including swallowed provider failures")
			}
			events := insightTelemetryEvents(t, logs.String(), "completed code review insight invocation")
			require.Len(t, events, 1, "each valid organization invocation should produce exactly one distinct completion event")
			actual := events[0]
			duration, ok := actual["duration_ms"].(float64)
			require.True(t, ok, "completion should report a numeric wall-clock processing duration")
			require.GreaterOrEqual(t, duration, float64(0), "processing duration should be nonnegative")
			delete(actual, "duration_ms")
			expected := expectedInsightInvocation(orgID, jobID, now)
			if tt.noJob {
				delete(expected, "job_id")
			}
			for key, value := range tt.expected {
				expected[key] = value
			}
			require.Equal(t, expected, actual, "new completion event should report exact bounded counts, phases, stops, cutoff, and actual job identity")
			require.Equal(t, int64(expected["provider_snapshot_calls_started"].(float64)), providerCalls.Load(), "telemetry should count actual provider method calls without adding any")
			require.Equal(t, int64(expected["attempt_store_nil_returns"].(float64)+expected["attempt_store_errors"].(float64)), store.attemptCalls.Load(), "attempt counters should partition original marker method returns")
			require.Equal(t, int64(expected["outcome_store_nil_returns"].(float64)+expected["outcome_store_errors"].(float64)), store.outcomeCalls.Load(), "outcome counters should partition original reconciliation method returns without implying applied rows")
			require.Equal(t, int64(1+expected["ranking_candidates_selected"].(float64)), nowCalls.Load(), "telemetry should not call the service clock beyond the original cutoff and per-candidate rank calls")
			for index, cutoff := range store.projectionCutoffs {
				require.Equal(t, now.UTC(), cutoff, "projection batch %d should use the same invocation cutoff", index)
				require.Equal(t, 500, store.projectionLimits[index], "projection batch size should remain unchanged")
			}
			for index, cutoff := range store.providerCutoffs {
				require.Equal(t, now.UTC().Add(-24*time.Hour), cutoff, "provider selection %d should preserve the same 24-hour eligibility cutoff", index)
				require.Equal(t, 25, store.providerLimits[index], "provider selection group size should remain unchanged")
			}
			for _, limit := range store.rankLimits {
				require.Equal(t, 500, limit, "ranking batch size should remain unchanged")
			}
			legacy := insightTelemetryEvents(t, logs.String(), "reconciled code review decision insights")
			if tt.expectedError {
				require.Empty(t, legacy, "a failed invocation must not gain the legacy success event")
			} else {
				require.Equal(t, []map[string]any{{"level": "info", "message": "reconciled code review decision insights", "org_id": orgID.String(), "projected": expected["projection_rows_reported"], "provider_reconciled": expected["outcome_store_nil_returns"], "ranked": expected["ranking_rows_reported"], "ranking_enabled": expected["ranking_enabled"]}}, legacy, "legacy success event keys and nil-return semantics should remain exact")
			}
			encoded, encodeErr := json.Marshal(events[0])
			require.NoError(t, encodeErr, "new event should serialize for privacy verification")
			for _, private := range []string{"private-error-sentinel", "private.invalid", "private-query", "private-token", "private-repository-sentinel", "private-author-sentinel", lockToken.String()} {
				require.NotContains(t, string(encoded), private, "new telemetry must not include dependency prose, payloads, credential-like values, repository names, or lease tokens")
			}
		})
	}
}

func TestInsightServiceProjectionTelemetry(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name  string
		jobID bool
		err   error
	}{
		{name: "session projection with durable job", jobID: true},
		{name: "direct projection does not invent job identity"},
		{name: "projection error retains identity", jobID: true, err: errors.New("private projection error")},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			orgID, sessionID, jobID := uuid.New(), uuid.New(), uuid.New()
			ctx := context.Background()
			if tt.jobID {
				ctx = jobctx.WithJobID(ctx, jobID)
			}
			store := &insightTelemetryStore{failureStage: "session_projection", failure: tt.err}
			var logs bytes.Buffer
			service := NewInsightService(store, zerolog.New(&logs))
			err := service.ProjectDecision(ctx, orgID, sessionID)
			require.True(t, err == tt.err, "session projection should return the exact original store error or nil")
			require.True(t, ctx == store.projectContext, "projection telemetry should pass the original context unchanged")
			require.Equal(t, orgID, store.projectOrg, "projection telemetry should preserve the tenant argument")
			require.Equal(t, sessionID, store.projectSession, "projection telemetry should preserve the original session argument")
			events := insightTelemetryEvents(t, logs.String(), "completed code review insight projection")
			require.Len(t, events, 1, "each valid session projection should emit one distinct method completion event")
			require.IsType(t, float64(0), events[0]["duration_ms"], "projection completion should report wall-clock duration")
			delete(events[0], "duration_ms")
			expected := map[string]any{"level": "info", "message": "completed code review insight projection", "org_id": orgID.String(), "insight_dispatch_kind": "session_projection", "invocation_result": insightMethodResult(tt.err), "context_state": "active"}
			if tt.jobID {
				expected["job_id"] = jobID.String()
			}
			require.Equal(t, []map[string]any{expected}, events, "projection completion should expose only method status and actual correlation, without claiming changed rows")
			require.Empty(t, insightTelemetryEvents(t, logs.String(), "completed code review insight invocation"), "single-session projection should not emit an organization reconciliation completion")
			require.Empty(t, insightTelemetryEvents(t, logs.String(), "reconciled code review decision insights"), "single-session projection should not gain the periodic success summary")
		})
	}
}

func TestInsightServiceProviderLimitRemainsInvocationLocal(t *testing.T) {
	t.Parallel()

	store := &insightTelemetryStore{}
	batches := make([][]models.CodeReviewOutcomePullRequest, 4)
	for index := range batches {
		batches[index] = newInsightTelemetryTargets(25)
	}
	store.providerBatches = append(store.providerBatches, batches...)
	store.providerBatches = append(store.providerBatches, batches...)
	var providerCalls atomic.Int64
	service := NewInsightService(store, zerolog.Nop())
	service.SetOutcomeProvider(outcomeAttributionProviderFunc(func(context.Context, uuid.UUID, uuid.UUID, int) (models.CodeReviewOutcomeSnapshot, error) {
		providerCalls.Add(1)
		return models.CodeReviewOutcomeSnapshot{}, nil
	}))
	orgID := uuid.New()

	require.NoError(t, service.ReconcileAndRank(context.Background(), orgID), "first invocation should preserve its one hundred target allowance")
	require.Equal(t, int64(100), providerCalls.Load(), "first invocation should call the provider exactly once for each selected target")
	require.NoError(t, service.ReconcileAndRank(context.Background(), orgID), "another same-org invocation should preserve the original fresh allowance")
	require.Equal(t, int64(200), providerCalls.Load(), "telemetry must not introduce a shared or daily provider cap or carry deduplication across invocations")
	require.Equal(t, 8, store.selectionCalls, "two full invocations should preserve four original selector calls each")
	require.Equal(t, int64(200), store.outcomeCalls.Load(), "both invocations should retain their original outcome-store method calls")
}

func TestInsightServiceInvocationCancellationDuringProvider(t *testing.T) {
	t.Parallel()

	store := &insightTelemetryStore{
		providerBatches: [][]models.CodeReviewOutcomePullRequest{newInsightTelemetryTargets(1)},
		failureStage:    "attempt", failure: context.Canceled,
	}
	var logs bytes.Buffer
	service := NewInsightService(store, zerolog.New(zerolog.SyncWriter(&logs)))
	started := make(chan struct{})
	service.SetOutcomeProvider(outcomeAttributionProviderFunc(func(ctx context.Context, _ uuid.UUID, _ uuid.UUID, _ int) (models.CodeReviewOutcomeSnapshot, error) {
		close(started)
		<-ctx.Done()
		return models.CodeReviewOutcomeSnapshot{}, ctx.Err()
	}))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- service.ReconcileAndRank(ctx, uuid.New()) }()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("provider should start before the parent invocation is cancelled")
	}
	cancel()
	select {
	case err := <-done:
		require.True(t, err == context.Canceled, "cancellation during provider work should preserve the exact marker-store cancellation error")
	case <-time.After(2 * time.Second):
		t.Fatal("cancelled provider and original marker-store failure should finish the invocation")
	}
	events := insightTelemetryEvents(t, logs.String(), "completed code review insight invocation")
	require.Len(t, events, 1, "provider cancellation should still emit exactly one invocation completion")
	require.Equal(t, "cancelled", events[0]["invocation_result"], "completion should classify the unchanged cancellation return")
	require.Equal(t, "provider_batch", events[0]["phase"], "concurrent store failure should retain a conservative provider batch phase")
	require.Equal(t, float64(1), events[0]["provider_snapshot_calls_failed"], "cancelled provider method should be counted once as failed")
	require.Equal(t, float64(1), events[0]["attempt_store_errors"], "cancelled attempt method should be counted once as an error")
	require.Empty(t, insightTelemetryEvents(t, logs.String(), "reconciled code review decision insights"), "cancellation must not gain the original success summary")
}
