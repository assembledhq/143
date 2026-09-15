package github

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/services/github/ratelimit"
)

func TestClassifyRetry(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.July, 21, 18, 0, 0, 0, time.UTC)
	resetAt := now.Add(24 * time.Minute)
	joinedThrottle := &GitHubAPIError{StatusCode: http.StatusTooManyRequests}
	joinedThrottleWithDeadline := &GitHubAPIError{StatusCode: http.StatusTooManyRequests, Header: http.Header{"Retry-After": []string{"120"}}}
	joinedTransient := &GitHubAPIError{StatusCode: http.StatusServiceUnavailable, Header: http.Header{"Retry-After": []string{"15"}}}
	joinedGraphQLThrottle := &GitHubGraphQLError{
		Errors: []ratelimit.GraphQLError{{Message: "API rate limit exceeded", Type: "RATE_LIMITED"}},
		Header: http.Header{"Retry-After": []string{"150"}},
	}
	joinedDeferral := &ratelimit.Deferral{Kind: ratelimit.KindSecondary, InstallationID: 42, RetryAt: now.Add(3 * time.Minute), Generation: 2}
	databaseRecoveryErr := errors.New("database recovery unavailable")
	activeCaller := context.Background()
	cancelledCaller, cancelCaller := context.WithCancel(context.Background())
	cancelCaller()
	expiredCaller, cancelDeadline := context.WithDeadline(context.Background(), time.Unix(1, 0))
	t.Cleanup(cancelDeadline)
	interruptedOK := NewGitHubResponseReadError(activeCaller, http.MethodGet, "/repos/acme/repo/commits/head/status", &http.Response{StatusCode: http.StatusOK, Header: make(http.Header)}, []byte(`{"statuses":[`), io.ErrUnexpectedEOF)
	interruptedThrottle := NewGitHubResponseReadError(activeCaller, http.MethodGet, "/repos/acme/repo/commits/head/status", &http.Response{
		StatusCode: http.StatusTooManyRequests, Header: http.Header{"Retry-After": []string{"120"}},
	}, []byte(`{"message":"API rate`), io.ErrUnexpectedEOF)
	managedInterruptedHeader := make(http.Header)
	ratelimit.AttachInternalRetryMetadata(managedInterruptedHeader, joinedDeferral)
	managedInterruptedThrottle := NewGitHubResponseReadError(activeCaller, http.MethodGet, "/repos/acme/repo/commits/head/status", &http.Response{
		StatusCode: http.StatusTooManyRequests, Header: managedInterruptedHeader,
	}, []byte(`{"message":"API rate`), io.ErrUnexpectedEOF)
	tests := []struct {
		name               string
		err                error
		expected           RetryClassification
		expectedRetryAfter *time.Duration
		expectedManaged    bool
	}{
		{
			name: "primary rate limit uses reset timestamp",
			err: &GitHubAPIError{
				StatusCode: http.StatusForbidden,
				Header: githubTestHeaders(map[string]string{
					"X-RateLimit-Remaining": "0",
					"X-RateLimit-Reset":     strconv.FormatInt(resetAt.Unix(), 10),
				}),
			},
			expected:           RetryClassification{Retryable: true, RateLimited: true},
			expectedRetryAfter: durationPointer(24 * time.Minute),
		},
		{
			name: "secondary rate limit uses retry after seconds",
			err: &GitHubAPIError{
				StatusCode: http.StatusForbidden,
				Body:       []byte(`{"message":"You have exceeded a secondary rate limit"}`),
				Header:     http.Header{"Retry-After": []string{"17"}},
			},
			expected:           RetryClassification{Retryable: true, RateLimited: true},
			expectedRetryAfter: durationPointer(17 * time.Second),
		},
		{
			name: "secondary rate limit uses retry after date",
			err: &GitHubAPIError{
				StatusCode: http.StatusForbidden,
				Body:       []byte(`{"message":"secondary rate limit"}`),
				Header:     githubTestHeaders(map[string]string{"Retry-After": resetAt.Format(http.TimeFormat)}),
			},
			expected:           RetryClassification{Retryable: true, RateLimited: true},
			expectedRetryAfter: durationPointer(24 * time.Minute),
		},
		{
			name: "too many requests without hint remains rate limited",
			err:  &GitHubAPIError{StatusCode: http.StatusTooManyRequests},
			expected: RetryClassification{
				Retryable:   true,
				RateLimited: true,
			},
		},
		{
			name: "service unavailable honors server retry delay",
			err: &GitHubAPIError{
				StatusCode: http.StatusServiceUnavailable,
				Header:     githubTestHeaders(map[string]string{"Retry-After": "9"}),
			},
			expected:           RetryClassification{Retryable: true},
			expectedRetryAfter: durationPointer(9 * time.Second),
		},
		{
			name:     "network error is transient",
			err:      &net.DNSError{Err: "temporary failure", IsTemporary: true},
			expected: RetryClassification{Retryable: true},
		},
		{
			name:     "interrupted successful response read is transient",
			err:      interruptedOK,
			expected: RetryClassification{Retryable: true},
		},
		{
			name:               "interrupted throttle response preserves retry deadline",
			err:                interruptedThrottle,
			expected:           RetryClassification{Retryable: true, RateLimited: true},
			expectedRetryAfter: durationPointer(2 * time.Minute),
		},
		{
			name:               "interrupted enforced throttle preserves controller metadata",
			err:                managedInterruptedThrottle,
			expected:           RetryClassification{Retryable: true, RateLimited: true},
			expectedRetryAfter: durationPointer(3 * time.Minute),
			expectedManaged:    true,
		},
		{
			name:     "HTTP client response timeout is transient while caller remains active",
			err:      NewGitHubResponseReadError(activeCaller, http.MethodGet, "/repos/acme/repo", &http.Response{StatusCode: http.StatusOK}, nil, context.DeadlineExceeded),
			expected: RetryClassification{Retryable: true},
		},
		{
			name:     "interrupted response cancellation remains terminal",
			err:      NewGitHubResponseReadError(cancelledCaller, http.MethodGet, "/repos/acme/repo", &http.Response{StatusCode: http.StatusOK}, nil, context.Canceled),
			expected: RetryClassification{},
		},
		{
			name:     "caller deadline during response read remains terminal",
			err:      NewGitHubResponseReadError(expiredCaller, http.MethodGet, "/repos/acme/repo", &http.Response{StatusCode: http.StatusOK}, nil, context.DeadlineExceeded),
			expected: RetryClassification{},
		},
		{
			name:     "unattributed HTTP deadline remains terminal",
			err:      &url.Error{Op: "Get", URL: "https://api.github.com/repos/acme/repo", Err: context.DeadlineExceeded},
			expected: RetryClassification{},
		},
		{
			name: "raw deadline remains terminal", err: context.DeadlineExceeded,
		},
		{
			name: "formatted raw deadline remains terminal", err: fmt.Errorf("identity probe: %w", context.DeadlineExceeded),
		},
		{
			name: "live request wrapper cannot lend provenance to raw sibling deadline",
			err:  errors.Join(NewGitHubRequestError(activeCaller, databaseRecoveryErr), &url.Error{Err: context.DeadlineExceeded}),
		},
		{
			name: "raw sibling deadline cannot borrow later response provenance",
			err:  errors.Join(&url.Error{Err: context.DeadlineExceeded}, interruptedOK),
		},
		{
			name: "owned timeout does not make independent raw deadline retryable",
			err:  errors.Join(NewGitHubRequestError(activeCaller, &url.Error{Err: context.DeadlineExceeded}), fmt.Errorf("operation deadline: %w", context.DeadlineExceeded)),
		},
		{
			name:     "owned client timeout remains transient through wrapping",
			err:      fmt.Errorf("refresh health: %w", NewGitHubRequestError(activeCaller, &url.Error{Op: "Get", URL: "https://api.github.com/repos/acme/repo", Err: context.DeadlineExceeded})),
			expected: RetryClassification{Retryable: true},
		},
		{
			name:     "owned caller timeout remains terminal through wrapping",
			err:      fmt.Errorf("refresh health: %w", NewGitHubRequestError(expiredCaller, &url.Error{Op: "Get", URL: "https://api.github.com/repos/acme/repo", Err: context.DeadlineExceeded})),
			expected: RetryClassification{},
		},
		{
			name:     "owned caller cancellation remains terminal through wrapping",
			err:      fmt.Errorf("refresh health: %w", NewGitHubRequestError(cancelledCaller, &url.Error{Op: "Get", URL: "https://api.github.com/repos/acme/repo", Err: context.Canceled})),
			expected: RetryClassification{},
		},
		{
			name:     "owned live caller timeout remains transient in joined errors",
			err:      errors.Join(databaseRecoveryErr, NewGitHubRequestError(activeCaller, &url.Error{Op: "Get", URL: "https://api.github.com/repos/acme/repo", Err: context.DeadlineExceeded})),
			expected: RetryClassification{Retryable: true},
		},
		{
			name:     "later caller timeout dominates an earlier interrupted response",
			err:      errors.Join(interruptedOK, NewGitHubRequestError(expiredCaller, &url.Error{Op: "Get", URL: "https://api.github.com/repos/acme/repo", Err: context.DeadlineExceeded})),
			expected: RetryClassification{},
		},
		{
			name:     "earlier caller timeout dominates a later interrupted response",
			err:      errors.Join(NewGitHubRequestError(expiredCaller, &url.Error{Op: "Get", URL: "https://api.github.com/repos/acme/repo", Err: context.DeadlineExceeded}), interruptedOK),
			expected: RetryClassification{},
		},
		{
			name: "forbidden permission error is permanent",
			err: &GitHubAPIError{
				StatusCode: http.StatusForbidden,
				Body:       []byte(`{"message":"Resource not accessible by integration"}`),
			},
			expected: RetryClassification{},
		},
		{
			name:     "unstructured forbidden prose is not throttle evidence",
			err:      &GitHubAPIError{StatusCode: http.StatusForbidden, Body: []byte("rate limit")},
			expected: RetryClassification{},
		},
		{
			name:     "validation error is permanent",
			err:      &GitHubAPIError{StatusCode: http.StatusUnprocessableEntity},
			expected: RetryClassification{},
		},
		{
			name:     "cancelled request is not retried",
			err:      context.Canceled,
			expected: RetryClassification{},
		},
		{
			name:     "wrapped deadline is not retried",
			err:      errors.Join(errors.New("request stopped"), context.DeadlineExceeded),
			expected: RetryClassification{},
		},
		{
			name:     "joined throttle remains classified before database failure",
			err:      errors.Join(joinedThrottle, databaseRecoveryErr),
			expected: RetryClassification{Retryable: true, RateLimited: true},
		},
		{
			name:     "joined throttle remains classified after database failure",
			err:      errors.Join(databaseRecoveryErr, joinedThrottle),
			expected: RetryClassification{Retryable: true, RateLimited: true},
		},
		{
			name:               "joined transient before throttle prioritizes throttle deadline",
			err:                errors.Join(joinedTransient, joinedThrottleWithDeadline),
			expected:           RetryClassification{Retryable: true, RateLimited: true},
			expectedRetryAfter: durationPointer(2 * time.Minute),
		},
		{
			name:               "joined throttle before transient retains throttle deadline",
			err:                errors.Join(joinedThrottleWithDeadline, joinedTransient),
			expected:           RetryClassification{Retryable: true, RateLimited: true},
			expectedRetryAfter: durationPointer(2 * time.Minute),
		},
		{
			name:               "joined GraphQL throttle after REST transient controls retry",
			err:                errors.Join(joinedTransient, joinedGraphQLThrottle),
			expected:           RetryClassification{Retryable: true, RateLimited: true},
			expectedRetryAfter: durationPointer(150 * time.Second),
		},
		{
			name:               "joined controller deferral after REST transient controls exact retry",
			err:                errors.Join(joinedTransient, joinedDeferral),
			expected:           RetryClassification{Retryable: true, RateLimited: true},
			expectedRetryAfter: durationPointer(3 * time.Minute),
			expectedManaged:    true,
		},
		{
			name:     "joined cancellation dominates throttle",
			err:      errors.Join(joinedThrottleWithDeadline, context.Canceled),
			expected: RetryClassification{},
		},
		{
			name:     "joined job deadline dominates throttle",
			err:      errors.Join(joinedThrottleWithDeadline, context.DeadlineExceeded),
			expected: RetryClassification{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			actual := ClassifyRetry(tt.err, now)
			require.Equal(t, tt.expected.Retryable, actual.Retryable, "classification should identify retryable failures")
			require.Equal(t, tt.expected.RateLimited, actual.RateLimited, "classification should distinguish rate limits from other transient failures")
			require.Equal(t, tt.expectedRetryAfter, actual.RetryAfter, "classification should preserve GitHub's reset delay")
			require.Equal(t, tt.expectedManaged, actual.ControllerManaged, "classification should preserve controller-managed response metadata")
		})
	}
}

func TestClassifyRetryUsesExactControllerDeadline(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.September, 14, 12, 0, 0, 0, time.UTC)
	retryAt := now.Add(37 * time.Second)
	header := make(http.Header)
	ratelimit.AttachInternalRetryMetadata(header, &ratelimit.Deferral{
		Kind: ratelimit.KindSecondary, InstallationID: 42, RetryAt: retryAt, Generation: 3,
	})

	tests := []struct {
		name string
		err  error
	}{
		{name: "local deferral", err: fmt.Errorf("covered request: %w", &ratelimit.Deferral{Kind: ratelimit.KindSecondary, InstallationID: 42, RetryAt: retryAt, Generation: 3})},
		{name: "actual provider response", err: &GitHubAPIError{StatusCode: http.StatusTooManyRequests, Header: header, Body: []byte(`{"message":"secondary rate limit"}`)}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			actual := ClassifyRetry(tt.err, now)
			require.True(t, actual.Retryable, "controller-managed throttle should remain retryable")
			require.True(t, actual.RateLimited, "controller-managed throttle should retain rate-limit status")
			require.True(t, actual.ControllerManaged, "controller-managed throttle should bypass worker slot scheduling")
			require.Equal(t, &retryAt, actual.RetryAt, "controller deadline should remain exact")
			require.Equal(t, durationPointer(37*time.Second), actual.RetryAfter, "controller delay should not gain worker jitter")
		})
	}
}

func durationPointer(value time.Duration) *time.Duration {
	return &value
}

func githubTestHeaders(values map[string]string) http.Header {
	header := make(http.Header, len(values))
	for name, value := range values {
		header.Set(name, value)
	}
	return header
}

func TestOwnedGitHubRequestErrorsPreserveCallerContext(t *testing.T) {
	t.Parallel()

	requests := []struct {
		name string
		call func(context.Context, *PRService, *Service) error
	}{
		{
			name: "PR REST",
			call: func(ctx context.Context, prs *PRService, _ *Service) error {
				_, err := prs.doGitHubRequest(ctx, "token", http.MethodGet, "/repos/acme/repo", nil)
				return err
			},
		},
		{
			name: "PR GraphQL",
			call: func(ctx context.Context, prs *PRService, _ *Service) error {
				_, err := prs.doGitHubGraphQL(ctx, "token", "query { viewer { login } }", nil)
				return err
			},
		},
		{
			name: "installation token exchange",
			call: func(ctx context.Context, _ *PRService, service *Service) error {
				_, _, err := service.exchangeForInstallationToken(ctx, "jwt", 42)
				return err
			},
		},
		{
			name: "installation details",
			call: func(ctx context.Context, _ *PRService, service *Service) error {
				_, err := service.GetInstallationDetails(ctx, 42)
				return err
			},
		},
		{
			name: "org members",
			call: func(ctx context.Context, _ *PRService, service *Service) error {
				_, err := service.ListOrgMembers(ctx, 42, "acme")
				return err
			},
		},
		{
			name: "org membership",
			call: func(ctx context.Context, _ *PRService, service *Service) error {
				_, err := service.IsActiveOrgMember(ctx, 42, "acme", "octocat")
				return err
			},
		},
		{
			name: "team membership",
			call: func(ctx context.Context, _ *PRService, service *Service) error {
				_, err := service.IsActiveTeamMember(ctx, 42, "acme", "engineering", "octocat")
				return err
			},
		},
	}
	failures := []struct {
		name              string
		clientTimeout     time.Duration
		callerDeadline    bool
		cancelCaller      bool
		expectedCause     error
		expectedCallerErr error
		expected          RetryClassification
	}{
		{
			name: "caller deadline before headers", callerDeadline: true,
			expectedCause: context.DeadlineExceeded, expectedCallerErr: context.DeadlineExceeded,
		},
		{
			name: "caller cancellation before headers", cancelCaller: true,
			expectedCause: context.Canceled, expectedCallerErr: context.Canceled,
		},
		{
			name: "client timeout with live caller", clientTimeout: 10 * time.Millisecond,
			expectedCause: context.DeadlineExceeded, expected: RetryClassification{Retryable: true},
		},
	}
	for _, request := range requests {
		t.Run(request.name, func(t *testing.T) {
			t.Parallel()
			for _, failure := range failures {
				t.Run(failure.name, func(t *testing.T) {
					t.Parallel()

					service, err := NewService(143, testPrivateKeyPEM(t))
					require.NoError(t, err, "GitHub service fixture should initialize")
					service.cache[42] = &cachedToken{Token: "token", ExpiresAt: time.Now().Add(time.Hour)}
					ctx, cancel := context.WithCancel(context.Background())
					if failure.callerDeadline {
						cancel()
						ctx, cancel = context.WithTimeout(context.Background(), 10*time.Millisecond)
					}
					t.Cleanup(cancel)
					client := &http.Client{
						Timeout: failure.clientTimeout,
						Transport: roundTripFunc(func(req *http.Request) (*http.Response, error) {
							if failure.cancelCaller {
								cancel()
							}
							<-req.Context().Done()
							return nil, req.Context().Err()
						}),
					}
					service.httpClient = client
					prs := &PRService{baseURL: "https://api.github.com", httpClient: client}

					err = request.call(ctx, prs, service)
					require.ErrorIs(t, err, failure.expectedCause, "request errors should preserve the transport cancellation or deadline cause")
					var requestErr *GitHubRequestError
					require.ErrorAs(t, err, &requestErr, "owned HTTP requests should retain caller context provenance before headers")
					require.Equal(t, failure.expectedCallerErr, requestErr.CallerContextErr, "only caller termination should stop durable recovery")
					require.Equal(t, failure.expectedCallerErr, ctx.Err(), "client timeout should leave the caller context live")
					require.Equal(t, failure.expected, ClassifyRetry(err, time.Now()), "retry classification should distinguish caller and client timeouts")

					cancel()
					require.Equal(t, failure.expected, ClassifyRetry(fmt.Errorf("recover GitHub request: %w", err), time.Now()), "later context cleanup and wrapping should not change the captured timeout provenance")
				})
			}
		})
	}
}
