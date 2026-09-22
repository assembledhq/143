package models

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"time"

	"github.com/google/uuid"
)

// AutomationActionKind identifies one of the supported external effects.
type AutomationActionKind string

const (
	AutomationActionLabel   AutomationActionKind = "github_label"
	AutomationActionTeam    AutomationActionKind = "github_team_review"
	AutomationActionComment AutomationActionKind = "github_issue_comment"
	AutomationActionNotion  AutomationActionKind = "notion_tracking_row"
	AutomationActionSlack   AutomationActionKind = "slack_notification"
)

func AutomationActionKinds() []AutomationActionKind {
	return []AutomationActionKind{AutomationActionLabel, AutomationActionTeam, AutomationActionComment, AutomationActionNotion, AutomationActionSlack}
}
func (k AutomationActionKind) Validate() error {
	for _, v := range AutomationActionKinds() {
		if k == v {
			return nil
		}
	}
	return fmt.Errorf("invalid automation action kind %q", k)
}

type AutomationActionStatus string

const (
	AutomationActionPending   AutomationActionStatus = "pending"
	AutomationActionSending   AutomationActionStatus = "sending"
	AutomationActionSucceeded AutomationActionStatus = "succeeded"
	AutomationActionFailed    AutomationActionStatus = "failed"
	AutomationActionUnknown   AutomationActionStatus = "unknown"
)

func (s AutomationActionStatus) Validate() error {
	switch s {
	case AutomationActionPending, AutomationActionSending, AutomationActionSucceeded, AutomationActionFailed, AutomationActionUnknown:
		return nil
	}
	return fmt.Errorf("invalid automation action status %q", s)
}

type AutomationActionDeliveryStatus string

const (
	AutomationActionDelivered      AutomationActionDeliveryStatus = "delivered"
	AutomationActionPartial        AutomationActionDeliveryStatus = "partial"
	AutomationActionNeedsAttention AutomationActionDeliveryStatus = "needs_attention"
	AutomationActionInProgress     AutomationActionDeliveryStatus = "in_progress"
	AutomationActionNotStarted     AutomationActionDeliveryStatus = "not_started"
)

func (s AutomationActionDeliveryStatus) Validate() error {
	switch s {
	case AutomationActionDelivered, AutomationActionPartial, AutomationActionNeedsAttention, AutomationActionInProgress, AutomationActionNotStarted:
		return nil
	}
	return fmt.Errorf("invalid automation delivery status %q", s)
}

// AutomationActionConfig grants selected primitives and fixed destinations.
type AutomationActionConfig struct {
	Actions            []AutomationActionKind                  `json:"actions"`
	Repository         string                                  `json:"repository,omitempty"`
	Label              string                                  `json:"label,omitempty"`
	Team               string                                  `json:"team,omitempty"`
	NotionDataSourceID string                                  `json:"notion_data_source_id,omitempty"`
	NotionProperties   map[string]AutomationActionPropertyType `json:"notion_properties,omitempty"`
	SlackChannelID     string                                  `json:"slack_channel_id,omitempty"`
}

type AutomationActionPropertyType string

const (
	AutomationActionPropertyTitle  AutomationActionPropertyType = "title"
	AutomationActionPropertyText   AutomationActionPropertyType = "rich_text"
	AutomationActionPropertyURL    AutomationActionPropertyType = "url"
	AutomationActionPropertyDate   AutomationActionPropertyType = "date"
	AutomationActionPropertySelect AutomationActionPropertyType = "select"
)

func (p AutomationActionPropertyType) Validate() error {
	switch p {
	case AutomationActionPropertyTitle, AutomationActionPropertyText, AutomationActionPropertyURL, AutomationActionPropertyDate, AutomationActionPropertySelect:
		return nil
	}
	return fmt.Errorf("unsupported property type %q", p)
}

var automationActionTeamPattern = regexp.MustCompile(`^[a-zA-Z0-9][a-zA-Z0-9-]{0,99}$`)
var automationActionChannelPattern = regexp.MustCompile(`^[CG][A-Z0-9]{8,31}$`)
var automationActionRepositoryPattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+$`)
var automationActionSHAPattern = regexp.MustCompile(`^(?:[0-9a-f]{40}|[0-9a-f]{64})$`)
var automationActionKeyPattern = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]{0,159}$`)

func ParseAutomationActionConfig(raw json.RawMessage) (AutomationActionConfig, error) {
	var c AutomationActionConfig
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&c); err != nil {
		return c, fmt.Errorf("decode action config: %w", err)
	}
	if err := dec.Decode(new(any)); !errors.Is(err, io.EOF) {
		return c, errors.New("action config must contain one JSON object")
	}
	return c.Canonical(), c.Validate()
}

// Canonical returns a copy whose selected action order does not affect policy identity.
func (c AutomationActionConfig) Canonical() AutomationActionConfig {
	c.Actions = slices.Clone(c.Actions)
	slices.Sort(c.Actions)
	return c
}

func (c AutomationActionConfig) Allows(k AutomationActionKind) bool {
	return slices.Contains(c.Actions, k)
}
func (c AutomationActionConfig) Validate() error {
	if len(c.Actions) == 0 || len(c.Actions) > 5 {
		return errors.New("select at least one supported action")
	}
	seen := map[AutomationActionKind]bool{}
	for _, k := range c.Actions {
		if err := k.Validate(); err != nil {
			return err
		}
		if seen[k] {
			return errors.New("action kinds must be distinct")
		}
		seen[k] = true
		if k == AutomationActionLabel || k == AutomationActionTeam || k == AutomationActionComment {
			if !automationActionRepositoryPattern.MatchString(c.Repository) || len(c.Repository) > 256 {
				return errors.New("repository must be owner/name")
			}
			for _, part := range strings.Split(c.Repository, "/") {
				if part == "." || part == ".." {
					return errors.New("invalid repository")
				}
			}
		}
	}
	if c.Allows(AutomationActionLabel) && (strings.TrimSpace(c.Label) == "" || len(c.Label) > 50 || strings.ContainsAny(c.Label, "\r\n")) {
		return errors.New("label must be nonempty and at most 50 bytes")
	}
	if c.Allows(AutomationActionTeam) && !automationActionTeamPattern.MatchString(c.Team) {
		return errors.New("team must be a GitHub team slug")
	}
	if c.Allows(AutomationActionSlack) && !automationActionChannelPattern.MatchString(c.SlackChannelID) {
		return errors.New("slack_channel_id must identify a Slack channel")
	}
	if c.Allows(AutomationActionNotion) {
		if id, err := uuid.Parse(c.NotionDataSourceID); err != nil || id == uuid.Nil {
			return errors.New("notion_data_source_id must be a UUID")
		}
		if len(c.NotionProperties) == 0 || len(c.NotionProperties) > 20 {
			return errors.New("configure between one and twenty Notion properties")
		}
		titles := 0
		for name, kind := range c.NotionProperties {
			if strings.TrimSpace(name) == "" || len(name) > 100 {
				return errors.New("invalid Notion property name")
			}
			if err := kind.Validate(); err != nil {
				return err
			}
			if kind == AutomationActionPropertyTitle {
				titles++
			}
		}
		if titles != 1 {
			return errors.New("configure exactly one Notion title property")
		}
	}
	return nil
}

// AutomationActionKey identifies a workflow step across runs, never a provider destination.
type AutomationActionKey struct {
	OperationKey string `json:"operation_key"`
	ActionKey    string `json:"action_key"`
}

func (k AutomationActionKey) Validate() error {
	if !automationActionKeyPattern.MatchString(k.OperationKey) || !automationActionKeyPattern.MatchString(k.ActionKey) {
		return errors.New("operation_key and action_key must be 1-160 safe identifier characters")
	}
	return nil
}
func ValidateAutomationOperationKey(key string) error {
	if !automationActionKeyPattern.MatchString(key) {
		return errors.New("invalid operation_key")
	}
	return nil
}

type AutomationActionRequest struct {
	AutomationActionKey
	Kind       AutomationActionKind `json:"kind"`
	PRNumber   int                  `json:"pr_number,omitempty"`
	HeadSHA    string               `json:"head_sha,omitempty"`
	Text       string               `json:"text,omitempty"`
	Properties map[string]string    `json:"properties,omitempty"`
}

func (r AutomationActionRequest) Validate() error {
	if err := r.AutomationActionKey.Validate(); err != nil {
		return err
	}
	if err := r.Kind.Validate(); err != nil {
		return err
	}
	if r.PRNumber < 0 || (r.PRNumber > 0 && !automationActionSHAPattern.MatchString(r.HeadSHA)) || (r.PRNumber == 0 && r.HeadSHA != "") {
		return errors.New("a PR precondition requires a positive pr_number and full lowercase head_sha")
	}
	switch r.Kind {
	case AutomationActionLabel, AutomationActionTeam, AutomationActionComment:
		if r.PRNumber == 0 {
			return errors.New("GitHub actions require a PR precondition")
		}
	}
	switch r.Kind {
	case AutomationActionComment, AutomationActionSlack:
		limit := 32 << 10
		if r.Kind == AutomationActionSlack {
			limit = 4 << 10
		}
		if strings.TrimSpace(r.Text) == "" || len(r.Text) > limit || len(r.Properties) > 0 {
			return errors.New("provide bounded text, without properties")
		}
	case AutomationActionNotion:
		if r.Text != "" || len(r.Properties) == 0 || len(r.Properties) > 20 {
			return errors.New("provide Notion properties, without text")
		}
		size := 0
		for name, value := range r.Properties {
			size += len(name) + len(value)
			if strings.TrimSpace(name) == "" || len(name) > 100 || len(value) > 8<<10 {
				return errors.New("invalid Notion property value")
			}
		}
		if size > 32<<10 {
			return errors.New("notion content exceeds 32 KiB")
		}
	default:
		if r.Text != "" || len(r.Properties) > 0 {
			return errors.New("this action accepts no text or properties")
		}
	}
	return nil
}
func (r AutomationActionRequest) ValidateFor(c AutomationActionConfig) error {
	if err := r.Validate(); err != nil {
		return err
	}
	if !c.Allows(r.Kind) {
		return errors.New("action kind is not configured")
	}
	if r.Kind != AutomationActionNotion {
		return nil
	}
	for name, value := range r.Properties {
		kind, ok := c.NotionProperties[name]
		if !ok {
			return fmt.Errorf("notion property %q is not allowed", name)
		}
		switch kind {
		case AutomationActionPropertyURL:
			u, err := url.Parse(value)
			if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil || len(value) > 2048 {
				return errors.New("invalid property URL")
			}
		case AutomationActionPropertyDate:
			if _, err := time.Parse("2006-01-02", value); err != nil {
				return errors.New("date properties use YYYY-MM-DD")
			}
		case AutomationActionPropertyTitle, AutomationActionPropertySelect:
			if strings.TrimSpace(value) == "" || (kind == AutomationActionPropertySelect && len(value) > 100) {
				return errors.New("title and select values must be nonempty and bounded")
			}
		}
	}
	for name, kind := range c.NotionProperties {
		if kind == AutomationActionPropertyTitle && strings.TrimSpace(r.Properties[name]) == "" {
			return errors.New("notion title is required")
		}
	}
	return nil
}

// AutomationAction is a durable reservation. It outlives a session generation.
type AutomationAction struct {
	ScopeKey         string                 `db:"scope_key" json:"-"`
	OperationKey     string                 `db:"operation_key" json:"operation_key"`
	ActionKey        string                 `db:"action_key" json:"action_key"`
	ID               uuid.UUID              `db:"id" json:"id"`
	OrgID            uuid.UUID              `db:"org_id" json:"-"`
	AutomationID     uuid.UUID              `db:"automation_id" json:"-"`
	RepositoryID     uuid.UUID              `db:"repository_id" json:"-"`
	PRNumber         int                    `db:"pr_number" json:"-"`
	Kind             AutomationActionKind   `db:"kind" json:"kind"`
	CreatedRunID     uuid.UUID              `db:"created_run_id" json:"created_run_id,omitempty"`
	LastRunID        uuid.UUID              `db:"last_run_id" json:"last_run_id,omitempty"`
	HeadSHA          string                 `db:"head_sha" json:"head_sha"`
	RequestDigest    string                 `db:"request_digest" json:"-"`
	Destination      json.RawMessage        `db:"destination" json:"destination"`
	Payload          json.RawMessage        `db:"payload" json:"-"`
	Status           AutomationActionStatus `db:"status" json:"status"`
	AttemptCount     int                    `db:"attempt_count" json:"-"`
	SendToken        *uuid.UUID             `db:"send_token" json:"-"`
	SendDeadlineAt   *time.Time             `db:"send_deadline_at" json:"-"`
	ProviderObjectID *string                `db:"provider_object_id" json:"provider_object_id,omitempty"`
	ProviderURL      *string                `db:"provider_url" json:"provider_url,omitempty"`
	LastErrorCode    *string                `db:"last_error_code" json:"error_code,omitempty"`
	CreatedAt        time.Time              `db:"created_at" json:"created_at,omitempty"`
	UpdatedAt        time.Time              `db:"updated_at" json:"-"`
	CompletedAt      *time.Time             `db:"completed_at" json:"completed_at,omitempty"`
}

// EffectiveStatus never turns an expired send back into retryable work.
func (a AutomationAction) EffectiveStatus(now time.Time) AutomationActionStatus {
	if a.Status == AutomationActionSending && (a.SendDeadlineAt == nil || !now.Before(*a.SendDeadlineAt)) {
		return AutomationActionUnknown
	}
	return a.Status
}

type AutomationActionResult struct {
	Reused    bool                           `json:"reused,omitempty"`
	Status    AutomationActionDeliveryStatus `json:"status"`
	ErrorCode string                         `json:"error_code,omitempty"`
	Actions   []AutomationAction             `json:"actions"`
}

func AutomationActionResultFor(actions []AutomationAction, now time.Time) AutomationActionResult {
	out := AutomationActionResult{Status: AutomationActionNotStarted, Actions: make([]AutomationAction, 0, len(actions))}
	unknown, sending, succeeded := false, false, 0
	for _, a := range actions {
		a.Status = a.EffectiveStatus(now)
		out.Actions = append(out.Actions, a)
		unknown = unknown || a.Status == AutomationActionUnknown
		sending = sending || a.Status == AutomationActionSending
		if a.Status == AutomationActionSucceeded {
			succeeded++
		}
	}
	switch {
	case unknown:
		out.Status = AutomationActionNeedsAttention
	case sending:
		out.Status = AutomationActionInProgress
	case len(actions) > 0 && succeeded == len(actions):
		out.Status = AutomationActionDelivered
	case len(actions) > 0:
		out.Status = AutomationActionPartial
	}
	return out
}

// AutomationActionActor comes from signed internal claims, never request JSON.
type AutomationActionActor struct {
	OrgID        uuid.UUID
	RepositoryID uuid.UUID
	SessionID    uuid.UUID
	ThreadID     uuid.UUID
	RunID        uuid.UUID
	AttemptToken uuid.UUID
	JobID        uuid.UUID
}
type AutomationActionScope struct {
	Actor          AutomationActionActor
	TargetID       uuid.UUID
	AutomationID   uuid.UUID
	PRNumber       int
	RepositoryName string
	InstallationID int64
	HeadSHA        string
	ScopeKey       string
	Config         AutomationActionConfig
}
