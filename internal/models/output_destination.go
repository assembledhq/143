package models

import (
	"encoding/json"
	"fmt"
	"regexp"
	"time"

	"github.com/google/uuid"
)

// NotionIDPattern matches Notion UUIDs (with or without dashes).
var NotionIDPattern = regexp.MustCompile(`^[0-9a-fA-F]{8}-?[0-9a-fA-F]{4}-?[0-9a-fA-F]{4}-?[0-9a-fA-F]{4}-?[0-9a-fA-F]{12}$`)

// OutputDestinationType identifies where a project's outputs are delivered.
type OutputDestinationType string

const (
	OutputDestSlack   OutputDestinationType = "slack"
	OutputDestEmail   OutputDestinationType = "email"
	OutputDestNotion  OutputDestinationType = "notion"
	OutputDestWebhook OutputDestinationType = "webhook"
)

func (t OutputDestinationType) Validate() error {
	switch t {
	case OutputDestSlack, OutputDestEmail, OutputDestNotion, OutputDestWebhook:
		return nil
	default:
		return fmt.Errorf("invalid output destination type: %q", t)
	}
}

// OutputDestination configures a single delivery target for project results.
type OutputDestination struct {
	ID              uuid.UUID             `db:"id" json:"id"`
	ProjectID       uuid.UUID             `db:"project_id" json:"project_id"`
	OrgID           uuid.UUID             `db:"org_id" json:"org_id"`
	DestinationType OutputDestinationType `db:"destination_type" json:"destination_type"`
	Label           string                `db:"label" json:"label"`
	Config          json.RawMessage       `db:"config" json:"config"`
	Enabled         bool                  `db:"enabled" json:"enabled"`
	CreatedAt       time.Time             `db:"created_at" json:"created_at"`
	UpdatedAt       time.Time             `db:"updated_at" json:"updated_at"`
}

// RedactSecrets removes sensitive fields (like webhook HMAC secrets) from the
// config before returning to API clients.
func (d *OutputDestination) RedactSecrets() {
	if d.DestinationType == OutputDestWebhook {
		var cfg WebhookOutputConfig
		if err := json.Unmarshal(d.Config, &cfg); err == nil && cfg.Secret != "" {
			cfg.Secret = "**redacted**"
			if redacted, err := json.Marshal(cfg); err == nil {
				d.Config = redacted
			}
		}
	}
}

// Per-destination config structs — stored as JSON in the config column.

// SlackOutputConfig targets a Slack channel. Uses the org's existing Slack integration.
type SlackOutputConfig struct {
	ChannelID   string `json:"channel_id"`
	ChannelName string `json:"channel_name,omitempty"`
	ThreadTS    string `json:"thread_ts,omitempty"` // optional: post in thread
}

// EmailOutputConfig targets one or more email recipients.
type EmailOutputConfig struct {
	Recipients []string `json:"recipients"`
	Subject    string   `json:"subject,omitempty"` // template; blank = auto
}

// NotionOutputConfig targets a Notion page or database.
type NotionOutputConfig struct {
	PageID     string `json:"page_id"`
	PageTitle  string `json:"page_title,omitempty"`
	DatabaseID string `json:"database_id,omitempty"` // if set, creates a DB row per run
}

// WebhookOutputConfig posts the result as JSON to an arbitrary URL.
type WebhookOutputConfig struct {
	URL     string            `json:"url"`
	Method  string            `json:"method,omitempty"` // default POST
	Headers map[string]string `json:"headers,omitempty"`
	Secret  string            `json:"secret,omitempty"` // HMAC signing secret
}

// ReviewerStrategy controls how PR reviewers are automatically assigned.
type ReviewerStrategy string

const (
	ReviewerStrategyCodeowners ReviewerStrategy = "codeowners"
	ReviewerStrategyNone       ReviewerStrategy = "none"
)

func (s ReviewerStrategy) Validate() error {
	switch s {
	case ReviewerStrategyCodeowners, ReviewerStrategyNone, "":
		return nil
	default:
		return fmt.Errorf("invalid reviewer strategy: %q", s)
	}
}
