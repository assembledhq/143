package integration

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
)

// SentryErrorTracker implements ErrorTracker for the Sentry error monitoring platform.
// It uses the Sentry REST API to query issues, stack traces, and trends.
type SentryErrorTracker struct {
	httpClient *http.Client
	baseURL    string // e.g. "https://sentry.io"
	authToken  string
	orgSlug    string
}

// SentryTrackerConfig holds the connection details for a Sentry ErrorTracker.
type SentryTrackerConfig struct {
	BaseURL   string // defaults to "https://sentry.io"
	AuthToken string
	OrgSlug   string
}

// NewSentryErrorTracker creates a Sentry ErrorTracker from the given config.
func NewSentryErrorTracker(cfg SentryTrackerConfig) *SentryErrorTracker {
	baseURL := cfg.BaseURL
	if baseURL == "" {
		baseURL = "https://sentry.io"
	}
	return &SentryErrorTracker{
		httpClient: &http.Client{Timeout: 30 * time.Second},
		baseURL:    strings.TrimRight(baseURL, "/"),
		authToken:  cfg.AuthToken,
		orgSlug:    cfg.OrgSlug,
	}
}

func (s *SentryErrorTracker) Name() string { return "sentry" }

// ListErrors fetches unresolved issues from Sentry matching the filter.
func (s *SentryErrorTracker) ListErrors(ctx context.Context, filter ErrorFilter) ([]ErrorSummary, error) {
	query := "is:unresolved"
	if filter.Severity != "" {
		query += fmt.Sprintf(" level:%s", mapSeverityToSentryLevel(filter.Severity))
	}

	endpoint := fmt.Sprintf("%s/api/0/organizations/%s/issues/", s.baseURL, s.orgSlug)
	if filter.ProjectSlug != "" {
		endpoint = fmt.Sprintf("%s/api/0/projects/%s/%s/issues/", s.baseURL, s.orgSlug, filter.ProjectSlug)
	}
	endpoint += fmt.Sprintf("?query=%s&sort=date", query)
	if filter.Limit > 0 {
		endpoint += fmt.Sprintf("&limit=%d", filter.Limit)
	}

	var issues []sentryAPIIssue
	if err := s.doGet(ctx, endpoint, &issues); err != nil {
		return nil, fmt.Errorf("list sentry issues: %w", err)
	}

	summaries := make([]ErrorSummary, 0, len(issues))
	for _, issue := range issues {
		es := sentryIssueToSummary(issue)
		if !filter.Since.IsZero() && es.LastSeen.Before(filter.Since) {
			continue
		}
		summaries = append(summaries, es)
	}
	return summaries, nil
}

// GetError fetches full details for a single Sentry issue, including the latest event's stack trace.
func (s *SentryErrorTracker) GetError(ctx context.Context, errorID string) (*ErrorDetail, error) {
	// Fetch the issue metadata.
	issueEndpoint := fmt.Sprintf("%s/api/0/issues/%s/", s.baseURL, errorID)
	var issue sentryAPIIssue
	if err := s.doGet(ctx, issueEndpoint, &issue); err != nil {
		return nil, fmt.Errorf("get sentry issue: %w", err)
	}

	detail := &ErrorDetail{
		ErrorSummary: sentryIssueToSummary(issue),
		ErrorType:    issue.Metadata.Type,
		ErrorValue:   issue.Metadata.Value,
		WebURL:       issue.Permalink,
	}

	// Fetch the latest event for stack trace.
	eventEndpoint := fmt.Sprintf("%s/api/0/issues/%s/events/latest/", s.baseURL, errorID)
	var event sentryAPIEvent
	if err := s.doGet(ctx, eventEndpoint, &event); err == nil {
		detail.StackTrace = parseEventStackTrace(event)
		detail.Tags = parseSentryTags(event.Tags)
	}

	return detail, nil
}

// GetTrend fetches occurrence stats for an issue over the given period.
func (s *SentryErrorTracker) GetTrend(ctx context.Context, errorID string, period time.Duration) (*ErrorTrend, error) {
	// Sentry's stats endpoint provides hourly/daily buckets.
	resolution := "1h"
	if period > 7*24*time.Hour {
		resolution = "1d"
	}

	since := time.Now().Add(-period)
	endpoint := fmt.Sprintf(
		"%s/api/0/issues/%s/events/?statsPeriod=%s&resolution=%s",
		s.baseURL, errorID,
		formatStatsPeriod(period), resolution,
	)

	// Fall back to a simpler approach: fetch the issue and build a basic trend.
	var issue sentryAPIIssue
	issueEndpoint := fmt.Sprintf("%s/api/0/issues/%s/", s.baseURL, errorID)
	if err := s.doGet(ctx, issueEndpoint, &issue); err != nil {
		return nil, fmt.Errorf("get sentry issue for trend: %w", err)
	}

	_ = endpoint // TODO: use stats endpoint when available
	_ = since

	trend := &ErrorTrend{
		ErrorID: errorID,
		Period:  period,
	}

	// Parse stats14d if available (Sentry returns this on issue detail).
	if len(issue.Stats14d) > 0 {
		for _, bucket := range issue.Stats14d {
			if len(bucket) == 2 {
				ts := time.Unix(int64(bucket[0]), 0)
				count := int(bucket[1])
				trend.DataPoints = append(trend.DataPoints, TrendDataPoint{
					Timestamp: ts,
					Count:     count,
				})
			}
		}
	}

	trend.Direction = computeTrendDirection(trend.DataPoints)
	return trend, nil
}

// FindRelated returns issues that share the same culprit (file/function) as the given error.
func (s *SentryErrorTracker) FindRelated(ctx context.Context, errorID string) ([]ErrorSummary, error) {
	// First, get the target issue to find its culprit.
	detail, err := s.GetError(ctx, errorID)
	if err != nil {
		return nil, err
	}
	if detail.Culprit == "" {
		return nil, nil
	}

	// Search for other issues with the same culprit.
	endpoint := fmt.Sprintf(
		"%s/api/0/organizations/%s/issues/?query=is:unresolved culprit:%s&sort=date&limit=10",
		s.baseURL, s.orgSlug, detail.Culprit,
	)

	var issues []sentryAPIIssue
	if err := s.doGet(ctx, endpoint, &issues); err != nil {
		return nil, fmt.Errorf("find related sentry issues: %w", err)
	}

	summaries := make([]ErrorSummary, 0, len(issues))
	for _, issue := range issues {
		if issue.ID == errorID {
			continue // exclude the original
		}
		summaries = append(summaries, sentryIssueToSummary(issue))
	}
	return summaries, nil
}

// --- internal types ---

type sentryAPIIssue struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Culprit   string `json:"culprit"`
	Level     string `json:"level"`
	Status    string `json:"status"`
	Count     string `json:"count"`
	UserCount int    `json:"userCount"`
	FirstSeen string `json:"firstSeen"`
	LastSeen  string `json:"lastSeen"`
	Permalink string `json:"permalink"`
	Metadata  struct {
		Type  string `json:"type"`
		Value string `json:"value"`
	} `json:"metadata"`
	Project struct {
		ID   string `json:"id"`
		Name string `json:"name"`
		Slug string `json:"slug"`
	} `json:"project"`
	Stats14d   [][]float64 `json:"stats"`
	StatusDetails struct {
		AutoResolved bool `json:"autoResolved"`
	} `json:"statusDetails"`
	IsRegression bool `json:"isRegression,omitempty"`
}

type sentryAPIEvent struct {
	EventID  string `json:"eventID"`
	Entries  []struct {
		Type string          `json:"type"`
		Data json.RawMessage `json:"data"`
	} `json:"entries"`
	Tags []struct {
		Key   string `json:"key"`
		Value string `json:"value"`
	} `json:"tags"`
}

type sentryExceptionData struct {
	Values []struct {
		Type       string `json:"type"`
		Value      string `json:"value"`
		Stacktrace struct {
			Frames []struct {
				Filename string `json:"filename"`
				AbsPath  string `json:"absPath"`
				Function string `json:"function"`
				LineNo   int    `json:"lineNo"`
				InApp    bool   `json:"inApp"`
				Context  [][]interface{} `json:"context"`
			} `json:"frames"`
		} `json:"stacktrace"`
	} `json:"values"`
}

// --- helpers ---

func (s *SentryErrorTracker) doGet(ctx context.Context, url string, target interface{}) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+s.authToken)

	resp, err := s.httpClient.Do(req) // #nosec G107 -- URL is from internal config
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("sentry API returned %d", resp.StatusCode)
	}

	return json.NewDecoder(resp.Body).Decode(target)
}

func sentryIssueToSummary(issue sentryAPIIssue) ErrorSummary {
	occurrences := 1
	if n, err := strconv.Atoi(issue.Count); err == nil && n > 0 {
		occurrences = n
	}

	return ErrorSummary{
		ID:            issue.ID,
		Title:         issue.Title,
		Culprit:       issue.Culprit,
		Severity:      mapSentryLevelToSeverity(issue.Level),
		Occurrences:   occurrences,
		AffectedUsers: issue.UserCount,
		FirstSeen:     parseTimeBestEffort(issue.FirstSeen),
		LastSeen:      parseTimeBestEffort(issue.LastSeen),
		Project:       issue.Project.Slug,
		IsRegression:  issue.IsRegression,
	}
}

func parseEventStackTrace(event sentryAPIEvent) *StackTrace {
	for _, entry := range event.Entries {
		if entry.Type != "exception" {
			continue
		}

		var data sentryExceptionData
		if err := json.Unmarshal(entry.Data, &data); err != nil {
			continue
		}

		st := &StackTrace{}
		var summaryParts []string

		for _, value := range data.Values {
			// Iterate frames in reverse (most recent first).
			for i := len(value.Stacktrace.Frames) - 1; i >= 0; i-- {
				frame := value.Stacktrace.Frames[i]

				// Skip vendor frames.
				if !frame.InApp && (strings.Contains(frame.Filename, "node_modules") ||
					strings.Contains(frame.Filename, "site-packages") ||
					strings.HasPrefix(frame.Filename, "<")) {
					continue
				}

				file := frame.Filename
				if frame.AbsPath != "" {
					file = frame.AbsPath
				}

				sf := StackFrame{
					File:     file,
					Function: frame.Function,
					Line:     frame.LineNo,
				}

				// Extract source context if available.
				if len(frame.Context) > 0 {
					var contextLines []string
					for _, line := range frame.Context {
						if len(line) >= 2 {
							if s, ok := line[1].(string); ok {
								contextLines = append(contextLines, s)
							}
						}
					}
					if len(contextLines) > 0 {
						sf.Context = strings.Join(contextLines, "\n")
					}
				}

				st.AppFrames = append(st.AppFrames, sf)

				if len(summaryParts) < 3 {
					summaryParts = append(summaryParts, fmt.Sprintf("%s:%d", file, frame.LineNo))
				}
			}

			if len(summaryParts) > 0 {
				st.Summary = fmt.Sprintf("%s: %s → %s",
					value.Type, value.Value,
					strings.Join(summaryParts, " → "),
				)
			}
		}

		if len(st.AppFrames) > 0 {
			return st
		}
	}
	return nil
}

func parseSentryTags(tags []struct {
	Key   string `json:"key"`
	Value string `json:"value"`
}) map[string]string {
	if len(tags) == 0 {
		return nil
	}
	m := make(map[string]string, len(tags))
	for _, t := range tags {
		m[t.Key] = t.Value
	}
	return m
}

func mapSentryLevelToSeverity(level string) string {
	switch level {
	case "fatal":
		return "critical"
	case "error":
		return "high"
	case "warning":
		return "medium"
	case "info":
		return "low"
	default:
		return "medium"
	}
}

func mapSeverityToSentryLevel(severity string) string {
	switch severity {
	case "critical":
		return "fatal"
	case "high":
		return "error"
	case "medium":
		return "warning"
	case "low":
		return "info"
	default:
		return severity
	}
}

func parseTimeBestEffort(s string) time.Time {
	for _, layout := range []string{
		time.RFC3339,
		time.RFC3339Nano,
		"2006-01-02T15:04:05Z",
		"2006-01-02T15:04:05.000Z",
	} {
		if t, err := time.Parse(layout, s); err == nil {
			return t
		}
	}
	return time.Time{}
}

func formatStatsPeriod(d time.Duration) string {
	hours := int(d.Hours())
	if hours <= 24 {
		return fmt.Sprintf("%dh", hours)
	}
	return fmt.Sprintf("%dd", hours/24)
}

func computeTrendDirection(points []TrendDataPoint) string {
	if len(points) < 2 {
		return "stable"
	}

	// Compare the last quarter to the first quarter.
	quarter := len(points) / 4
	if quarter < 1 {
		quarter = 1
	}

	var earlySum, lateSum int
	for i := 0; i < quarter; i++ {
		earlySum += points[i].Count
	}
	for i := len(points) - quarter; i < len(points); i++ {
		lateSum += points[i].Count
	}

	if earlySum == 0 && lateSum == 0 {
		return "stable"
	}
	if earlySum == 0 && lateSum > 0 {
		return "spike"
	}

	ratio := float64(lateSum) / float64(earlySum)
	switch {
	case ratio > 2.0:
		return "spike"
	case ratio > 1.2:
		return "increasing"
	case ratio < 0.5:
		return "decreasing"
	default:
		return "stable"
	}
}
