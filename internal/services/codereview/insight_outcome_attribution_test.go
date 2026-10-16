package codereview

import (
	"context"
	"sort"
	"sync"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/jobctx"
	"github.com/assembledhq/143/internal/models"
	githubtelemetry "github.com/assembledhq/143/internal/services/github/telemetry"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

type outcomeAttributionListCall struct {
	OrgID          uuid.UUID
	ObservedBefore time.Time
	Limit          int
}

type outcomeAttributionStore struct {
	insightServiceStoreStub
	listCalls  []outcomeAttributionListCall
	mu         sync.Mutex
	reconciled map[uuid.UUID]models.CodeReviewOutcomeSnapshot
}

func (s *outcomeAttributionStore) ListPullRequestsForOutcomeReconciliation(_ context.Context, orgID uuid.UUID, before time.Time, limit int) ([]models.CodeReviewOutcomePullRequest, error) {
	s.listCalls = append(s.listCalls, outcomeAttributionListCall{orgID, before, limit})
	return s.pullRequests, nil
}

func (s *outcomeAttributionStore) ReconcilePullRequestOutcome(_ context.Context, _ uuid.UUID, prID uuid.UUID, snapshot models.CodeReviewOutcomeSnapshot) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reconciled[prID] = snapshot
	return nil
}

type outcomeAttributionProviderFunc func(context.Context, uuid.UUID, uuid.UUID, int) (models.CodeReviewOutcomeSnapshot, error)

func (f outcomeAttributionProviderFunc) GetCodeReviewOutcomeSnapshot(ctx context.Context, orgID, repoID uuid.UUID, number int) (models.CodeReviewOutcomeSnapshot, error) {
	return f(ctx, orgID, repoID, number)
}

type outcomeAttributionContextKey struct{}

type outcomeAttributionObservation struct {
	Number          int
	OrgID           uuid.UUID
	RepositoryID    uuid.UUID
	Metadata        githubtelemetry.RequestMetadata
	MetadataPresent bool
	JobID           uuid.UUID
	JobPresent      bool
	LockToken       uuid.UUID
	LockPresent     bool
	ParentValue     any
	Deadline        time.Time
	DeadlinePresent bool
}

func observeOutcomeAttribution(ctx context.Context, orgID, repoID uuid.UUID, number int) outcomeAttributionObservation {
	metadata, metadataPresent := githubtelemetry.RequestMetadataFromContext(ctx)
	jobID, jobPresent := jobctx.JobIDFromContext(ctx)
	lockToken, lockPresent := jobctx.LockTokenFromContext(ctx)
	deadline, deadlinePresent := ctx.Deadline()
	return outcomeAttributionObservation{
		Number: number, OrgID: orgID, RepositoryID: repoID,
		Metadata: metadata, MetadataPresent: metadataPresent,
		JobID: jobID, JobPresent: jobPresent, LockToken: lockToken, LockPresent: lockPresent,
		ParentValue: ctx.Value(outcomeAttributionContextKey{}), Deadline: deadline, DeadlinePresent: deadlinePresent,
	}
}

func TestInsightService_OutcomeProviderGetsIsolatedAuthoritativePRAttribution(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name           string
		parentMetadata bool
	}{
		{name: "preserves existing job caller and request dimensions", parentMetadata: true},
		{name: "does not invent assessment or session identity"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			now := time.Date(2026, 8, 4, 7, 0, 0, 0, time.UTC)
			orgID, jobID, lockToken := uuid.New(), uuid.New(), uuid.New()
			parent := context.WithValue(context.Background(), outcomeAttributionContextKey{}, "parent context")
			parent = jobctx.WithLockToken(jobctx.WithJobID(parent, jobID), lockToken)
			var original githubtelemetry.RequestMetadata
			if tt.parentMetadata {
				original = githubtelemetry.RequestMetadata{
					Kind: githubtelemetry.RequestKindAPI, AuthType: githubtelemetry.AuthTypeAppInstallation,
					InstallationID: 42, Caller: "existing_caller", SyncReason: "daily_repair", HealthOrigin: "scheduler",
					OrgID: uuid.NewString(), PullRequestID: uuid.New(), Repository: "example/repository",
					SessionID: uuid.NewString(), AssessmentID: uuid.New(), PrincipalUnresolved: true,
				}
				parent = githubtelemetry.WithRequestMetadata(parent, original)
			}
			parent, cancel := context.WithTimeout(parent, 10*time.Second)
			defer cancel()
			deadline, _ := parent.Deadline()
			pullRequests := make([]models.CodeReviewOutcomePullRequest, 5)
			snapshot := models.CodeReviewOutcomeSnapshot{AuthorLogin: "author", State: "open", ObservedAt: now}
			expectedReconciled := make(map[uuid.UUID]models.CodeReviewOutcomeSnapshot)
			expected := make([]outcomeAttributionObservation, len(pullRequests))
			for index := range pullRequests {
				candidate := models.CodeReviewOutcomePullRequest{PullRequestID: uuid.New(), RepositoryID: uuid.New(), GitHubPRNumber: index + 1}
				pullRequests[index] = candidate
				expectedReconciled[candidate.PullRequestID] = snapshot
				metadata := original
				metadata.OrgID = orgID.String()
				metadata.PullRequestID = candidate.PullRequestID
				expected[index] = outcomeAttributionObservation{
					Number: candidate.GitHubPRNumber, OrgID: orgID, RepositoryID: candidate.RepositoryID,
					Metadata: metadata, MetadataPresent: true, JobID: jobID, JobPresent: true,
					LockToken: lockToken, LockPresent: true, ParentValue: "parent context", Deadline: deadline, DeadlinePresent: true,
				}
			}
			store := &outcomeAttributionStore{
				insightServiceStoreStub: insightServiceStoreStub{pullRequests: pullRequests},
				reconciled:              make(map[uuid.UUID]models.CodeReviewOutcomeSnapshot),
			}
			started := make(chan outcomeAttributionObservation, len(pullRequests))
			finished := make(chan outcomeAttributionObservation, len(pullRequests))
			release := make(chan struct{})
			var releaseOnce sync.Once
			releaseProviders := func() { releaseOnce.Do(func() { close(release) }) }
			defer releaseProviders()
			provider := outcomeAttributionProviderFunc(func(ctx context.Context, orgID, repoID uuid.UUID, number int) (models.CodeReviewOutcomeSnapshot, error) {
				// Observe entry before any credential resolution or provider I/O.
				started <- observeOutcomeAttribution(ctx, orgID, repoID, number)
				select {
				case <-release:
				case <-ctx.Done():
					return models.CodeReviewOutcomeSnapshot{}, ctx.Err()
				}
				finished <- observeOutcomeAttribution(ctx, orgID, repoID, number)
				return snapshot, nil
			})
			service := NewInsightService(store, zerolog.Nop())
			service.now = func() time.Time { return now }
			service.SetOutcomeProvider(provider)
			done := make(chan error, 1)
			go func() { done <- service.ReconcileAndRank(parent, orgID) }()

			actual := make([]outcomeAttributionObservation, 0, len(pullRequests))
			for range pullRequests {
				select {
				case observation := <-started:
					actual = append(actual, observation)
				case <-parent.Done():
					t.Fatal("all five independent provider reads should start concurrently before any complete")
				}
			}
			releaseProviders()
			select {
			case err := <-done:
				require.NoError(t, err, "per-PR attribution should preserve outcome repair and ranking")
			case <-parent.Done():
				t.Fatal("outcome reconciliation should finish after providers are released")
			}
			close(finished)
			afterConcurrentReads := make([]outcomeAttributionObservation, 0, len(pullRequests))
			for observation := range finished {
				afterConcurrentReads = append(afterConcurrentReads, observation)
			}
			sort.Slice(actual, func(i, j int) bool { return actual[i].Number < actual[j].Number })
			sort.Slice(afterConcurrentReads, func(i, j int) bool { return afterConcurrentReads[i].Number < afterConcurrentReads[j].Number })
			require.Equal(t, expected, actual, "authoritative org and PR must be attached before provider entry while all other parent dimensions remain intact")
			require.Equal(t, expected, afterConcurrentReads, "concurrent PR reads must keep their own immutable metadata throughout the provider call")
			metadataAfter, presentAfter := githubtelemetry.RequestMetadataFromContext(parent)
			require.Equal(t, original, metadataAfter, "child attribution must not overwrite parent request metadata")
			require.Equal(t, tt.parentMetadata, presentAfter, "adding child attribution must not add metadata to a parent that had none")
			require.NoError(t, parent.Err(), "the per-batch child context must not cancel its parent")
			require.Equal(t, expectedReconciled, store.reconciled, "each original provider snapshot should still persist once for its own PR")
			require.Equal(t, []outcomeAttributionListCall{{orgID, now.Add(-24 * time.Hour), 25}}, store.listCalls, "attribution must not add selection queries or alter the 24-hour eligibility threshold and batch size")
			require.Equal(t, 1, store.projectionCalls, "attribution must not add projection database calls")
			require.Equal(t, 1, store.rankBatchCalls, "attribution must preserve the existing ranking pass")
		})
	}
}
