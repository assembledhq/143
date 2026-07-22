package github

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func TestParseGitHubRateLimitObservation(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 21, 18, 0, 0, 0, time.UTC)
	reset := now.Add(time.Hour)
	tests := []struct {
		name              string
		defaultResource   models.GitHubRateLimitResource
		statusCode        int
		header            http.Header
		body              []byte
		expectedResource  models.GitHubRateLimitResource
		expectedLimit     *int
		expectedRemaining *int
		expectedReset     *time.Time
		expectedBlocked   *time.Time
		expectOK          bool
	}{
		{
			name:             "parses REST core quota",
			defaultResource:  models.GitHubRateLimitResourceCore,
			statusCode:       http.StatusOK,
			header:           githubRateHeaders("core", 5000, 4321, reset),
			expectedResource: models.GitHubRateLimitResourceCore,
			expectedLimit:    rateIntPointer(5000), expectedRemaining: rateIntPointer(4321), expectedReset: rateTimePointer(reset), expectOK: true,
		},
		{
			name:             "uses GraphQL fallback when resource header is absent",
			defaultResource:  models.GitHubRateLimitResourceGraphQL,
			statusCode:       http.StatusOK,
			header:           githubRateHeaders("", 5000, 4000, reset),
			expectedResource: models.GitHubRateLimitResourceGraphQL,
			expectedLimit:    rateIntPointer(5000), expectedRemaining: rateIntPointer(4000), expectedReset: rateTimePointer(reset), expectOK: true,
		},
		{
			name:             "records secondary limit without primary headers",
			defaultResource:  models.GitHubRateLimitResourceCore,
			statusCode:       http.StatusForbidden,
			header:           http.Header{"Retry-After": []string{"90"}},
			expectedResource: models.GitHubRateLimitResourceCore,
			expectedBlocked:  rateTimePointer(now.Add(90 * time.Second)), expectOK: true,
		},
		{
			name:             "records resource-local primary exhaustion without a global block",
			defaultResource:  models.GitHubRateLimitResourceCore,
			statusCode:       http.StatusForbidden,
			header:           githubRateHeaders("core", 5000, 0, reset),
			expectedResource: models.GitHubRateLimitResourceCore,
			expectedLimit:    rateIntPointer(5000), expectedRemaining: rateIntPointer(0), expectedReset: rateTimePointer(reset), expectOK: true,
		},
		{
			name:             "applies fallback block to headerless 429",
			defaultResource:  models.GitHubRateLimitResourceCore,
			statusCode:       http.StatusTooManyRequests,
			header:           make(http.Header),
			expectedResource: models.GitHubRateLimitResourceCore,
			expectedBlocked:  rateTimePointer(now.Add(time.Minute)), expectOK: true,
		},
		{
			name:             "applies fallback block to classified secondary 403",
			defaultResource:  models.GitHubRateLimitResourceGraphQL,
			statusCode:       http.StatusForbidden,
			header:           make(http.Header),
			body:             []byte(`{"message":"You have exceeded a secondary rate limit"}`),
			expectedResource: models.GitHubRateLimitResourceGraphQL,
			expectedBlocked:  rateTimePointer(now.Add(time.Minute)), expectOK: true,
		},
		{
			name:             "does not block ordinary permission 403",
			defaultResource:  models.GitHubRateLimitResourceCore,
			statusCode:       http.StatusForbidden,
			header:           make(http.Header),
			body:             []byte(`{"message":"Resource not accessible by integration"}`),
			expectedResource: models.GitHubRateLimitResourceCore,
		},
		{
			name:             "ignores malformed success headers",
			defaultResource:  models.GitHubRateLimitResourceCore,
			statusCode:       http.StatusOK,
			header:           http.Header{"X-RateLimit-Limit": []string{"many"}},
			expectedResource: models.GitHubRateLimitResourceCore,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			actual, ok := parseGitHubRateLimitObservationWithBody(143, tt.defaultResource, tt.statusCode, tt.header, tt.body, now)
			require.Equal(t, tt.expectOK, ok, "parser should report whether the response carries usable rate-limit state")
			require.Equal(t, int64(143), actual.InstallationID, "observation should retain the installation identity")
			require.Equal(t, tt.expectedResource, actual.Resource, "observation should keep primary resources isolated")
			require.Equal(t, tt.expectedLimit, actual.Limit, "observation should parse the quota limit exactly")
			require.Equal(t, tt.expectedRemaining, actual.Remaining, "observation should parse remaining quota exactly")
			require.Equal(t, tt.expectedReset, actual.ResetAt, "observation should parse the reset timestamp exactly")
			require.Equal(t, tt.expectedBlocked, actual.BlockedUntil, "observation should preserve secondary or primary block windows")
		})
	}
}

func TestRateBudgetObserveResponseIsBestEffort(t *testing.T) {
	t.Parallel()

	store := &rateLimitStoreStub{observeErr: errors.New("database unavailable")}
	var logs bytes.Buffer
	now := time.Date(2026, 7, 21, 18, 0, 0, 0, time.UTC)
	budget := NewRateBudget(store, zerolog.New(&logs))
	budget.now = func() time.Time { return now }

	budget.ObserveResponse(context.Background(), 143, models.GitHubRateLimitResourceCore, http.StatusOK,
		githubRateHeaders("core", 5000, 4321, now.Add(time.Hour)))

	require.Equal(t, int64(143), store.observation.InstallationID, "observer should attempt to persist the installation quota")
	require.Contains(t, logs.String(), "failed to persist GitHub rate-limit observation", "observer failures should be logged without failing the GitHub request")
}

func TestRateBudgetReserveCodeReviewDelegatesDurableIdentity(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	metadataID := uuid.New()
	now := time.Date(2026, 7, 21, 18, 0, 0, 0, time.UTC)
	expected := models.GitHubRateLimitDecision{Allowed: true, MetadataID: metadataID}
	store := &rateLimitStoreStub{decision: expected}
	budget := NewRateBudget(store, zerolog.Nop())
	budget.now = func() time.Time { return now }

	actual, err := budget.ReserveCodeReview(context.Background(), orgID, 143, metadataID)

	require.NoError(t, err, "durable admission should return the store decision")
	require.Equal(t, expected, actual, "rate budget should preserve the durable admission result")
	require.Equal(t, orgID, store.orgID, "admission should retain tenant ownership for the reservation")
	require.Equal(t, int64(143), store.installationID, "admission should coordinate on the GitHub installation")
	require.Equal(t, metadataID, store.metadataID, "admission should be idempotent for the code review metadata row")
	require.Equal(t, now, store.now, "admission should use one deterministic decision timestamp")
}

func TestRateBudgetCheckCodeReviewBlockDelegatesInstallationIdentity(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 21, 18, 0, 0, 0, time.UTC)
	blockedUntil := now.Add(90 * time.Second)
	expected := models.GitHubRateLimitDecision{BlockedUntil: blockedUntil, RetryAfter: 90 * time.Second}
	store := &rateLimitStoreStub{blockDecision: expected}
	budget := NewRateBudget(store, zerolog.Nop())
	budget.now = func() time.Time { return now }

	actual, err := budget.CheckCodeReviewBlock(context.Background(), 143)

	require.NoError(t, err, "running-review block check should return the store decision")
	require.Equal(t, expected, actual, "rate budget should preserve the installation-wide block")
	require.Equal(t, int64(143), store.installationID, "block check should retain the GitHub installation identity")
	require.Equal(t, now, store.now, "block check should use one deterministic decision timestamp")
}

func TestRateBudgetRefreshCodeReviewPersistsFetchedCoreSnapshot(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 7, 21, 18, 0, 0, 0, time.UTC)
	reset := now.Add(time.Hour)
	limit := 5000
	remaining := 700
	store := &rateLimitStoreStub{}
	fetcher := &rateLimitSnapshotFetcherStub{observations: []models.GitHubRateLimitObservation{{
		InstallationID: 143, Resource: models.GitHubRateLimitResourceCore,
		Limit: &limit, Remaining: &remaining, ResetAt: &reset, ObservedAt: now,
	}}}
	budget := NewRateBudget(store, zerolog.Nop())
	budget.SetSnapshotFetcher(fetcher)

	err := budget.RefreshCodeReview(context.Background(), 143)

	require.NoError(t, err, "pre-start quota refresh should persist a core snapshot")
	require.Equal(t, int64(143), fetcher.installationID, "refresh should fetch the review installation")
	require.Equal(t, fetcher.observations, store.observations, "refresh should durably persist the fetched snapshot before re-admission")
}

type rateLimitStoreStub struct {
	observation    models.GitHubRateLimitObservation
	observations   []models.GitHubRateLimitObservation
	observeErr     error
	orgID          uuid.UUID
	installationID int64
	metadataID     uuid.UUID
	now            time.Time
	decision       models.GitHubRateLimitDecision
	blockDecision  models.GitHubRateLimitDecision
	reserveErr     error
	blockErr       error
}

func (s *rateLimitStoreStub) CheckCodeReviewBlock(_ context.Context, installationID int64, now time.Time) (models.GitHubRateLimitDecision, error) {
	s.installationID = installationID
	s.now = now
	return s.blockDecision, s.blockErr
}

func (s *rateLimitStoreStub) Observe(_ context.Context, observation models.GitHubRateLimitObservation) error {
	s.observation = observation
	s.observations = append(s.observations, observation)
	return s.observeErr
}

func (s *rateLimitStoreStub) ReserveCodeReview(_ context.Context, orgID uuid.UUID, installationID int64, metadataID uuid.UUID, now time.Time) (models.GitHubRateLimitDecision, error) {
	s.orgID = orgID
	s.installationID = installationID
	s.metadataID = metadataID
	s.now = now
	return s.decision, s.reserveErr
}

type rateLimitSnapshotFetcherStub struct {
	installationID int64
	observations   []models.GitHubRateLimitObservation
	err            error
}

func (s *rateLimitSnapshotFetcherStub) FetchRateLimitSnapshot(_ context.Context, installationID int64) ([]models.GitHubRateLimitObservation, error) {
	s.installationID = installationID
	return s.observations, s.err
}

func githubRateHeaders(resource string, limit, remaining int, reset time.Time) http.Header {
	header := make(http.Header)
	header.Set("X-RateLimit-Limit", strconv.Itoa(limit))
	header.Set("X-RateLimit-Remaining", strconv.Itoa(remaining))
	header.Set("X-RateLimit-Reset", strconv.FormatInt(reset.Unix(), 10))
	if resource != "" {
		header.Set("X-RateLimit-Resource", resource)
	}
	return header
}

func rateIntPointer(value int) *int              { return &value }
func rateTimePointer(value time.Time) *time.Time { return &value }
