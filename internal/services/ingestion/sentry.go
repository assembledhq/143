package ingestion

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"
)

// SentryWebhookPayload represents a Sentry webhook event.
type SentryWebhookPayload struct {
	Action string `json:"action"`
	Data   struct {
		Issue SentryIssue `json:"issue"`
		Event SentryEvent `json:"event"`
	} `json:"data"`
}

// SentryIssue represents a Sentry issue from a webhook or API.
type SentryIssue struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Culprit   string `json:"culprit"`
	Level     string `json:"level"`
	Status    string `json:"status"`
	Count     string `json:"count"`
	UserCount int    `json:"userCount"`
	FirstSeen string `json:"firstSeen"`
	LastSeen  string `json:"lastSeen"`
	Metadata  struct {
		Type  string `json:"type"`
		Value string `json:"value"`
	} `json:"metadata"`
	ShortID string `json:"shortId"`
	Project struct {
		ID   string `json:"id"`
		Name string `json:"name"`
		Slug string `json:"slug"`
	} `json:"project"`
}

// SentryEvent represents a single Sentry error event.
type SentryEvent struct {
	EventID  string `json:"event_id"`
	Title    string `json:"title"`
	Message  string `json:"message"`
	Platform string `json:"platform"`
	Tags     []struct {
		Key   string `json:"key"`
		Value string `json:"value"`
	} `json:"tags"`
}

// SentryAdapter normalizes Sentry webhook payloads into NormalizedIssue.
type SentryAdapter struct{}

func NewSentryAdapter() *SentryAdapter {
	return &SentryAdapter{}
}

// ParseWebhook parses a Sentry webhook payload and returns a normalized issue.
func (a *SentryAdapter) ParseWebhook(integrationID uuid.UUID, payload json.RawMessage) (*NormalizedIssue, error) {
	var event SentryWebhookPayload
	if err := json.Unmarshal(payload, &event); err != nil {
		return nil, fmt.Errorf("unmarshal sentry webhook: %w", err)
	}

	issue := event.Data.Issue
	if issue.ID == "" {
		return nil, fmt.Errorf("sentry webhook missing issue ID")
	}

	// Only process created/regression events for new issues
	if event.Action != "created" && event.Action != "regression" {
		return nil, nil // skip non-actionable events
	}

	occurrenceCount := 1
	if n := parseIntSafe(issue.Count); n > 0 {
		occurrenceCount = n
	}

	firstSeen := parseTimeSafe(issue.FirstSeen)
	lastSeen := parseTimeSafe(issue.LastSeen)
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
	if issue.Culprit != "" {
		description += "\n\nCulprit: " + issue.Culprit
	}

	severity := mapSentryLevel(issue.Level)

	tags := []string{
		fmt.Sprintf("project:%s", issue.Project.Slug),
	}

	return &NormalizedIssue{
		ExternalID:            issue.ID,
		Source:                "sentry",
		SourceIntegrationID:   integrationID,
		Title:                 issue.Title,
		Description:           description,
		Severity:              severity,
		OccurrenceCount:       occurrenceCount,
		AffectedCustomerCount: issue.UserCount,
		Tags:                  tags,
		FirstSeenAt:           firstSeen,
		LastSeenAt:            lastSeen,
		RawData:               payload,
	}, nil
}

func mapSentryLevel(level string) string {
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
