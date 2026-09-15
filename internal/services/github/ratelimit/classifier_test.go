package ratelimit

import (
	"bytes"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestClassifyResponse(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, time.September, 14, 12, 0, 0, 0, time.UTC)
	reset := now.Add(10 * time.Minute)
	httpDateRetry := now.Add(2 * time.Minute)
	tests := []struct {
		name     string
		input    ResponseInput
		expected Classification
	}{
		{
			name: "primary forbidden with exhausted remaining",
			input: ResponseInput{StatusCode: http.StatusForbidden, Header: http.Header{
				"X-Ratelimit-Remaining": []string{"0"}, "X-Ratelimit-Reset": []string{strconv.FormatInt(reset.Unix(), 10)}, "X-Ratelimit-Resource": []string{"core"},
			}, Message: "API rate limit exceeded", Now: now},
			expected: Classification{RateLimited: true, Kind: KindPrimary, Resource: "core", Reason: "API rate limit exceeded", RetryAt: &reset},
		},
		{
			name: "primary uses retry after when reset is missing",
			input: ResponseInput{StatusCode: http.StatusForbidden, Header: http.Header{
				"X-Ratelimit-Remaining": []string{"0"}, "Retry-After": []string{"180"},
			}, Message: "API rate limit exceeded", Now: now},
			expected: Classification{RateLimited: true, Kind: KindPrimary, Reason: "API rate limit exceeded", RetryAt: timePointer(now.Add(3 * time.Minute))},
		},
		{
			name: "primary uses later retry after when reset is expired",
			input: ResponseInput{StatusCode: http.StatusForbidden, Header: http.Header{
				"X-Ratelimit-Remaining": []string{"0"}, "X-Ratelimit-Reset": []string{strconv.FormatInt(now.Add(-time.Minute).Unix(), 10)}, "Retry-After": []string{"180"},
			}, Message: "API rate limit exceeded", Now: now},
			expected: Classification{RateLimited: true, Kind: KindPrimary, Reason: "API rate limit exceeded", RetryAt: timePointer(now.Add(3 * time.Minute))},
		},
		{
			name: "primary preserves strongest later retry after",
			input: ResponseInput{StatusCode: http.StatusForbidden, Header: http.Header{
				"X-Ratelimit-Remaining": []string{"0"}, "X-Ratelimit-Reset": []string{strconv.FormatInt(now.Add(time.Minute).Unix(), 10)}, "Retry-After": []string{"180"},
			}, Message: "API rate limit exceeded", Now: now},
			expected: Classification{RateLimited: true, Kind: KindPrimary, Reason: "API rate limit exceeded", RetryAt: timePointer(now.Add(3 * time.Minute))},
		},
		{
			name:     "secondary retry after",
			input:    ResponseInput{StatusCode: http.StatusForbidden, Header: http.Header{"Retry-After": []string{"60"}}, Message: "secondary rate limit", Now: now},
			expected: Classification{RateLimited: true, Kind: KindSecondary, Reason: "secondary rate limit", RetryAt: timePointer(now.Add(time.Minute))},
		},
		{
			name:     "secondary HTTP-date retry after",
			input:    ResponseInput{StatusCode: http.StatusTooManyRequests, Header: http.Header{"Retry-After": []string{httpDateRetry.Format(http.TimeFormat)}}, Now: now},
			expected: Classification{RateLimited: true, Kind: KindSecondary, Reason: "secondary throttle", RetryAt: &httpDateRetry},
		},
		{
			name:  "ordinary permission denied",
			input: ResponseInput{StatusCode: http.StatusForbidden, Message: "Resource not accessible by integration", Now: now},
		},
		{
			name:  "successful graphql data text is ignored",
			input: ResponseInput{StatusCode: http.StatusOK, GraphQLErrors: nil, Message: "", Now: now},
		},
		{
			name:     "structured graphql throttle",
			input:    ResponseInput{StatusCode: http.StatusOK, GraphQLErrors: []GraphQLError{{Message: "API rate limit exceeded", Type: "RATE_LIMITED"}}, Now: now},
			expected: Classification{RateLimited: true, Kind: KindUnknown, Reason: "API rate limit exceeded"},
		},
		{
			name:  "graphql complexity error is not throttle",
			input: ResponseInput{StatusCode: http.StatusOK, GraphQLErrors: []GraphQLError{{Message: "Query has too many nodes", Type: "MAX_NODE_LIMIT_EXCEEDED"}}, Now: now},
		},
		{
			name: "not modified still observes exhausted quota",
			input: ResponseInput{StatusCode: http.StatusNotModified, Header: http.Header{
				"X-Ratelimit-Remaining": []string{"0"}, "X-Ratelimit-Reset": []string{strconv.FormatInt(reset.Unix(), 10)},
			}, Now: now},
			expected: Classification{RateLimited: true, Kind: KindPrimary, Reason: "primary quota exhausted", RetryAt: &reset},
		},
		{
			name: "successful last request opens primary cooldown",
			input: ResponseInput{StatusCode: http.StatusOK, Header: http.Header{
				"X-Ratelimit-Remaining": []string{"0"}, "X-Ratelimit-Reset": []string{strconv.FormatInt(reset.Unix(), 10)},
			}, Now: now},
			expected: Classification{RateLimited: true, Kind: KindPrimary, Reason: "primary quota exhausted", RetryAt: &reset},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.expected, ClassifyResponse(tt.input), "classifier should use only structured throttle evidence")
		})
	}
}

func TestDecodeGraphQLErrors(t *testing.T) {
	t.Parallel()

	errors, err := DecodeGraphQLErrors([]byte(`{"data":{"message":"rate limit"},"errors":[{"message":"denied","extensions":{"code":"FORBIDDEN"}}]}`))
	require.NoError(t, err, "structured GraphQL envelope should decode")
	require.Equal(t, []GraphQLError{{Message: "denied", Type: "FORBIDDEN"}}, errors, "decoder should ignore rate-limit prose in successful data")
}

func TestDecodeGraphQLErrorsRejectsNonEnvelopeJSON(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		body string
	}{
		{name: "null document", body: `null`},
		{name: "empty object", body: `{}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			_, err := DecodeGraphQLErrors([]byte(tt.body))
			require.Error(t, err, "valid JSON without GraphQL data or errors must not count as authoritative recovery")
		})
	}
}

//nolint:paralleltest // benchmark allocation accounting includes process-wide allocations, so package tests must run separately
func TestDecodeGraphQLErrorsSuccessfulDataAllocationIsBounded(t *testing.T) {
	// Benchmark-based allocation accounting is process-sensitive, so this test
	// intentionally does not run in parallel with package tests.
	small := append([]byte(`{"data":{"value":"`), bytes.Repeat([]byte("a"), 1024)...)
	small = append(small, []byte(`"}}`)...)
	large := append([]byte(`{"data":{"value":"`), bytes.Repeat([]byte("a"), 4<<20)...)
	large = append(large, []byte(`"}}`)...)

	allocated := func(input []byte) int64 {
		result := testing.Benchmark(func(b *testing.B) {
			for range b.N {
				errors, err := DecodeGraphQLErrors(input)
				if err != nil || errors != nil {
					b.Fatalf("successful GraphQL data should decode without errors: %v", err)
				}
			}
		})
		return result.AllocedBytesPerOp()
	}

	smallBytes := allocated(small)
	largeBytes := allocated(large)
	t.Logf("successful data decoder bytes/op: 1KiB=%d 4MiB=%d", smallBytes, largeBytes)
	require.LessOrEqual(t, largeBytes, smallBytes+4096, "decoder allocations should not grow with successful GraphQL data size")
}

func timePointer(value time.Time) *time.Time { return &value }
