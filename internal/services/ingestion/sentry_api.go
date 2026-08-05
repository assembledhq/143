package ingestion

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/assembledhq/143/internal/models"
)

// SentryAPIClient fetches issues from the Sentry REST API.
type SentryAPIClient struct {
	httpClient *http.Client
	logger     zerolog.Logger
}

func NewSentryAPIClient(httpClient *http.Client, logger zerolog.Logger) *SentryAPIClient {
	return &SentryAPIClient{
		httpClient: httpClient,
		logger:     logger,
	}
}

const (
	// sentryMaxPages is the maximum number of pages to fetch from the Sentry API
	// during a single sync to prevent unbounded pagination.
	sentryMaxPages = 50

	// sentryMaxRetries is the maximum number of retry attempts for rate-limited
	// Sentry API requests before giving up.
	sentryMaxRetries = 3

	// sentryMaxDrainBytes bounds how much of an unread response body is drained
	// before closing it. The cap keeps a large or slow body from stalling
	// cleanup; a body that exceeds it simply loses connection reuse.
	sentryMaxDrainBytes = 64 << 10
)

// FetchIssues retrieves unresolved issues from a Sentry project since the given time.
// It handles pagination and rate limiting automatically.
func (c *SentryAPIClient) FetchIssues(ctx context.Context, integrationID uuid.UUID, baseURL, authToken, projectSlug string, since time.Time) ([]NormalizedIssue, error) {
	var allIssues []NormalizedIssue

	url := fmt.Sprintf("%s/api/0/projects/%s/issues/?query=is:unresolved&sort=date", baseURL, projectSlug)

	for page := 0; url != "" && page < sentryMaxPages; page++ {
		issues, nextURL, err := c.fetchPage(ctx, url, authToken)
		if err != nil {
			return nil, err
		}

		for _, issue := range issues {
			lastSeen := ParseTimeSafe(issue.LastSeen)
			if !since.IsZero() && !lastSeen.IsZero() && lastSeen.Before(since) {
				// We've gone past the since boundary, stop paginating
				return allIssues, nil
			}

			normalized := c.normalizeIssue(integrationID, issue)
			allIssues = append(allIssues, normalized)
		}

		url = nextURL
	}

	if allIssues == nil {
		allIssues = []NormalizedIssue{}
	}

	return allIssues, nil
}

func (c *SentryAPIClient) fetchPage(ctx context.Context, url, authToken string) ([]SentryIssue, string, error) {
	for attempt := 0; attempt <= sentryMaxRetries; attempt++ {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		if err != nil {
			return nil, "", fmt.Errorf("create request: %w", err)
		}
		req.Header.Set("Authorization", "Bearer "+authToken)

		resp, err := c.httpClient.Do(req) // #nosec G704 -- URL is from internal Sentry config
		if err != nil {
			return nil, "", fmt.Errorf("fetch sentry issues: %w", err)
		}

		if resp.StatusCode == http.StatusTooManyRequests {
			retryAfter := parseRetryAfter(resp.Header.Get("Retry-After"))
			if err := closeSentryResponseBody(resp.Body, nil); err != nil {
				return nil, "", err
			}
			c.logger.Warn().
				Str("url", url).
				Int("attempt", attempt).
				Dur("retry_after", retryAfter).
				Msg("sentry rate limited, retrying")

			select {
			case <-ctx.Done():
				return nil, "", ctx.Err()
			case <-time.After(retryAfter):
				continue
			}
		}

		if resp.StatusCode != http.StatusOK {
			responseErr := fmt.Errorf("unexpected status %d from sentry API", resp.StatusCode)
			return nil, "", closeSentryResponseBody(resp.Body, responseErr)
		}

		var issues []SentryIssue
		decodeErr := json.NewDecoder(resp.Body).Decode(&issues)
		if decodeErr != nil {
			decodeErr = fmt.Errorf("decode sentry issues: %w", decodeErr)
		}
		if err := closeSentryResponseBody(resp.Body, decodeErr); err != nil {
			return nil, "", err
		}

		nextURL := parseLinkHeader(resp.Header.Get("Link"))
		return issues, nextURL, nil
	}

	return nil, "", fmt.Errorf("sentry API rate limited after %d retries", sentryMaxRetries)
}

// closeSentryResponseBody drains and closes a response body, preserving any
// error that was already being returned for the response. Keeping this out of a
// defer is important in the retry loop: a rate-limited response must be
// released before the client waits and starts the next attempt.
//
// The bounded drain is what lets the transport pool the connection for that
// retry. Closing a body that was never read forces the transport to tear the
// connection down, so every rate-limit retry would otherwise pay for a fresh
// handshake against an API that is already shedding load.
func closeSentryResponseBody(body io.ReadCloser, responseErr error) error {
	// A drain failure is not worth reporting: it says nothing the response error
	// does not already say, and the close below still runs either way.
	_, _ = io.Copy(io.Discard, io.LimitReader(body, sentryMaxDrainBytes))

	closeErr := body.Close()
	if closeErr == nil {
		return responseErr
	}

	closeErr = fmt.Errorf("close sentry response body: %w", closeErr)
	if responseErr == nil {
		return closeErr
	}

	return errors.Join(responseErr, closeErr)
}

func (c *SentryAPIClient) normalizeIssue(integrationID uuid.UUID, issue SentryIssue) NormalizedIssue {
	occurrenceCount := 1
	if n := ParseIntSafe(issue.Count); n > 0 {
		occurrenceCount = n
	}

	firstSeen := ParseTimeSafe(issue.FirstSeen)
	lastSeen := ParseTimeSafe(issue.LastSeen)
	if firstSeen.IsZero() {
		firstSeen = time.Now()
	}
	if lastSeen.IsZero() {
		lastSeen = time.Now()
	}

	description := issue.Title
	if issue.Metadata.Value != "" {
		description = fmt.Sprintf("%s: %s", issue.Metadata.Type, issue.Metadata.Value)
	}

	severity := MapSentryLevel(issue.Level)

	tags := []string{
		fmt.Sprintf("project:%s", issue.Project.Slug),
	}

	rawData, err := json.Marshal(issue)
	if err != nil {
		c.logger.Warn().Err(err).Str("issue_id", issue.ID).Msg("failed to marshal sentry issue raw data")
		rawData = nil
	}

	return NormalizedIssue{
		ExternalID:            issue.ID,
		Source:                models.IssueSourceSentry,
		SourceIntegrationID:   integrationID,
		Title:                 issue.Title,
		Description:           description,
		Severity:              severity,
		OccurrenceCount:       occurrenceCount,
		AffectedCustomerCount: issue.UserCount,
		Tags:                  tags,
		FirstSeenAt:           firstSeen,
		LastSeenAt:            lastSeen,
		RawData:               rawData,
	}
}

// parseLinkHeader extracts the next page URL from a Sentry Link header.
// Format: <URL>; rel="next"; results="true"; cursor="..."
func parseLinkHeader(header string) string {
	if header == "" {
		return ""
	}

	// Split multiple links separated by comma
	links := strings.Split(header, ",")
	for _, link := range links {
		parts := strings.Split(strings.TrimSpace(link), ";")
		if len(parts) < 2 {
			continue
		}

		isNext := false
		hasResults := false

		for _, part := range parts[1:] {
			trimmed := strings.TrimSpace(part)
			if trimmed == `rel="next"` {
				isNext = true
			}
			if trimmed == `results="true"` {
				hasResults = true
			}
		}

		if isNext && hasResults {
			url := strings.TrimSpace(parts[0])
			url = strings.TrimPrefix(url, "<")
			url = strings.TrimSuffix(url, ">")
			return url
		}
	}

	return ""
}

func parseRetryAfter(s string) time.Duration {
	if s == "" {
		return 1 * time.Second
	}
	secs, err := strconv.Atoi(s)
	if err != nil {
		return 1 * time.Second
	}
	return time.Duration(secs) * time.Second
}
