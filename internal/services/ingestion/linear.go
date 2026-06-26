package ingestion

import (
	"encoding/json"
	"fmt"
	"time"

	"github.com/google/uuid"

	"github.com/assembledhq/143/internal/models"
)

// LinearWebhookPayload represents a Linear webhook event.
type LinearWebhookPayload struct {
	Action string      `json:"action"`
	Type   string      `json:"type"`
	Data   LinearIssue `json:"data"`
}

// LinearIssue represents a Linear issue from a webhook or API.
type LinearIssue struct {
	ID          string `json:"id"`
	Identifier  string `json:"identifier"` // e.g. "ENG-123"
	Title       string `json:"title"`
	Description string `json:"description"`
	URL         string `json:"url"`
	Priority    int    `json:"priority"` // 0=No, 1=Urgent, 2=High, 3=Medium, 4=Low
	State       struct {
		Name string `json:"name"`
		Type string `json:"type"` // triage, backlog, unstarted, started, completed, cancelled
	} `json:"state"`
	Labels []struct {
		Name string `json:"name"`
	} `json:"labels"`
	IssueType struct {
		Name string `json:"name"`
	} `json:"issueType"`
	Type struct {
		Name string `json:"name"`
	} `json:"type"`
	CreatedAt string `json:"createdAt"`
	UpdatedAt string `json:"updatedAt"`
	Team      struct {
		ID   string `json:"id"`
		Key  string `json:"key"`
		Name string `json:"name"`
	} `json:"team"`
}

// LinearAdapter normalizes Linear webhook payloads into NormalizedIssue.
type LinearAdapter struct{}

func NewLinearAdapter() *LinearAdapter {
	return &LinearAdapter{}
}

// ParseWebhook parses a Linear webhook payload and returns a normalized issue.
func (a *LinearAdapter) ParseWebhook(integrationID uuid.UUID, payload json.RawMessage) (*NormalizedIssue, error) {
	var event LinearWebhookPayload
	if err := json.Unmarshal(payload, &event); err != nil {
		return nil, fmt.Errorf("unmarshal linear webhook: %w", err)
	}

	if event.Type != "Issue" {
		return nil, nil // skip non-issue events
	}

	if event.Action != "create" && event.Action != "update" {
		return nil, nil // skip non-actionable events
	}

	issue := event.Data
	if issue.ID == "" {
		return nil, fmt.Errorf("linear webhook missing issue ID")
	}

	createdAt := ParseTimeSafe(issue.CreatedAt)
	if createdAt.IsZero() {
		createdAt = time.Now()
	}

	severity := MapLinearPriority(issue.Priority)

	tags := make([]string, 0, len(issue.Labels)+1)
	for _, l := range issue.Labels {
		tags = append(tags, l.Name)
	}
	if issue.Team.Key != "" {
		tags = append(tags, fmt.Sprintf("team:%s", issue.Team.Key))
	}

	title := issue.Title
	if issue.Identifier != "" {
		title = fmt.Sprintf("%s: %s", issue.Identifier, issue.Title)
	}

	return &NormalizedIssue{
		ExternalID:          issue.ID,
		Source:              models.IssueSourceLinear,
		SourceIntegrationID: integrationID,
		Title:               title,
		Description:         issue.Description,
		Severity:            severity,
		OccurrenceCount:     1,
		Tags:                tags,
		FirstSeenAt:         createdAt,
		LastSeenAt:          createdAt,
		RawData:             payload,
	}, nil
}
