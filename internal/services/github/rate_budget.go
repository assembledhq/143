package github

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
)

type GitHubRateLimitStore interface {
	Observe(ctx context.Context, observation models.GitHubRateLimitObservation) error
	ReserveCodeReview(ctx context.Context, orgID uuid.UUID, installationID int64, metadataID uuid.UUID, now time.Time) (models.GitHubRateLimitDecision, error)
	CheckCodeReviewBlock(ctx context.Context, installationID int64, now time.Time) (models.GitHubRateLimitDecision, error)
}

type GitHubRateLimitSnapshotFetcher interface {
	FetchRateLimitSnapshot(ctx context.Context, installationID int64) ([]models.GitHubRateLimitObservation, error)
}

// RateBudget persists installation-wide GitHub quota observations and owns the
// durable admission decision for new code reviews.
type RateBudget struct {
	store   GitHubRateLimitStore
	logger  zerolog.Logger
	now     func() time.Time
	fetcher GitHubRateLimitSnapshotFetcher
}

func (b *RateBudget) SetSnapshotFetcher(fetcher GitHubRateLimitSnapshotFetcher) {
	if b != nil {
		b.fetcher = fetcher
	}
}

func NewRateBudget(store GitHubRateLimitStore, logger zerolog.Logger) *RateBudget {
	return &RateBudget{store: store, logger: logger, now: time.Now}
}

// ObserveResponse is deliberately best effort: losing a quota observation
// must never turn an otherwise successful GitHub operation into a failure.
func (b *RateBudget) ObserveResponse(ctx context.Context, installationID int64, defaultResource models.GitHubRateLimitResource, statusCode int, header http.Header) {
	b.observeResponse(ctx, installationID, defaultResource, statusCode, header, nil)
}

// ObserveResponseWithBody additionally classifies GitHub's headerless
// secondary-limit 403 response without conflating ordinary permission errors.
func (b *RateBudget) ObserveResponseWithBody(ctx context.Context, installationID int64, defaultResource models.GitHubRateLimitResource, statusCode int, header http.Header, body []byte) {
	b.observeResponse(ctx, installationID, defaultResource, statusCode, header, body)
}

func (b *RateBudget) observeResponse(ctx context.Context, installationID int64, defaultResource models.GitHubRateLimitResource, statusCode int, header http.Header, body []byte) {
	if b == nil || b.store == nil || installationID <= 0 {
		return
	}
	now := b.now().UTC()
	observation, ok := parseGitHubRateLimitObservationWithBody(installationID, defaultResource, statusCode, header, body, now)
	if !ok {
		return
	}
	observeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), time.Second)
	defer cancel()
	if err := b.store.Observe(observeCtx, observation); err != nil {
		b.logger.Warn().Err(err).
			Int64("installation_id", installationID).
			Str("rate_resource", string(observation.Resource)).
			Msg("failed to persist GitHub rate-limit observation")
	}
}

func (b *RateBudget) ReserveCodeReview(ctx context.Context, orgID uuid.UUID, installationID int64, metadataID uuid.UUID) (models.GitHubRateLimitDecision, error) {
	if b == nil || b.store == nil {
		return models.GitHubRateLimitDecision{Allowed: true}, nil
	}
	return b.store.ReserveCodeReview(ctx, orgID, installationID, metadataID, b.now().UTC())
}

// CheckCodeReviewBlock lets an admitted review resume without reapplying the
// primary-capacity floor while still honoring installation-wide secondary
// limits observed by another worker.
func (b *RateBudget) CheckCodeReviewBlock(ctx context.Context, installationID int64) (models.GitHubRateLimitDecision, error) {
	if b == nil || b.store == nil {
		return models.GitHubRateLimitDecision{Allowed: true}, nil
	}
	return b.store.CheckCodeReviewBlock(ctx, installationID, b.now().UTC())
}

// RefreshCodeReview synchronously fetches and durably persists GitHub's
// non-primary-costing /rate_limit snapshot before a bootstrap review starts.
func (b *RateBudget) RefreshCodeReview(ctx context.Context, installationID int64) error {
	if b == nil || b.store == nil || b.fetcher == nil {
		return errors.New("GitHub rate-limit snapshot refresh is unavailable")
	}
	observations, err := b.fetcher.FetchRateLimitSnapshot(ctx, installationID)
	if err != nil {
		return err
	}
	coreObserved := false
	for _, observation := range observations {
		if observation.InstallationID != installationID {
			return fmt.Errorf("GitHub rate-limit snapshot installation mismatch: got %d, want %d", observation.InstallationID, installationID)
		}
		if err := b.store.Observe(ctx, observation); err != nil {
			return fmt.Errorf("persist refreshed GitHub rate limit for %s: %w", observation.Resource, err)
		}
		coreObserved = coreObserved || observation.Resource == models.GitHubRateLimitResourceCore
	}
	if !coreObserved {
		return errors.New("GitHub rate-limit snapshot did not include core quota")
	}
	return nil
}

func parseGitHubRateLimitObservation(installationID int64, defaultResource models.GitHubRateLimitResource, statusCode int, header http.Header, now time.Time) (models.GitHubRateLimitObservation, bool) {
	return parseGitHubRateLimitObservationWithBody(installationID, defaultResource, statusCode, header, nil, now)
}

func parseGitHubRateLimitObservationWithBody(installationID int64, defaultResource models.GitHubRateLimitResource, statusCode int, header http.Header, body []byte, now time.Time) (models.GitHubRateLimitObservation, bool) {
	resource := parseGitHubRateLimitResource(header.Get("X-RateLimit-Resource"), defaultResource)
	observation := models.GitHubRateLimitObservation{
		InstallationID: installationID,
		Resource:       resource,
		ObservedAt:     now,
	}

	limit, limitErr := strconv.Atoi(strings.TrimSpace(header.Get("X-RateLimit-Limit")))
	remaining, remainingErr := strconv.Atoi(strings.TrimSpace(header.Get("X-RateLimit-Remaining")))
	resetUnix, resetErr := strconv.ParseInt(strings.TrimSpace(header.Get("X-RateLimit-Reset")), 10, 64)
	if limitErr == nil && remainingErr == nil && resetErr == nil && limit > 0 && remaining >= 0 && remaining <= limit {
		resetAt := time.Unix(resetUnix, 0).UTC()
		observation.Limit = &limit
		observation.Remaining = &remaining
		observation.ResetAt = &resetAt
	}

	if statusCode == http.StatusTooManyRequests || statusCode == http.StatusForbidden {
		if retryAfter := parseGitHubRetryAfterHeader(header.Get("Retry-After"), now); retryAfter != nil {
			blockedUntil := now.Add(*retryAfter)
			observation.BlockedUntil = &blockedUntil
		} else if statusCode == http.StatusTooManyRequests || (statusCode == http.StatusForbidden && githubSecondaryRateLimitBody(body)) {
			blockedUntil := now.Add(time.Minute)
			observation.BlockedUntil = &blockedUntil
		}
	}
	return observation, observation.Limit != nil || observation.BlockedUntil != nil
}

func githubSecondaryRateLimitBody(body []byte) bool {
	normalized := strings.ToLower(string(body))
	return strings.Contains(normalized, "secondary rate limit") || strings.Contains(normalized, "abuse detection")
}

func parseGitHubRateLimitResource(raw string, fallback models.GitHubRateLimitResource) models.GitHubRateLimitResource {
	resource := models.GitHubRateLimitResource(strings.ToLower(strings.TrimSpace(raw)))
	if resource == "" {
		resource = fallback
	}
	if err := resource.Validate(); err != nil {
		return models.GitHubRateLimitResourceUnknown
	}
	return resource
}

func parseGitHubRetryAfterHeader(raw string, now time.Time) *time.Duration {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil
	}
	if seconds, err := strconv.Atoi(raw); err == nil && seconds >= 0 {
		delay := time.Duration(seconds) * time.Second
		return &delay
	}
	if retryAt, err := http.ParseTime(raw); err == nil {
		return nonNegativeRetryDelay(retryAt, now)
	}
	return nil
}
