package models

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/google/uuid"
)

const (
	LiveEventSchemaVersion  = 1
	LiveEventMaxPayloadSize = 4 * 1024
)

type LiveEventType string
type LiveResourceType string
type LiveEventScope string
type LiveAudienceScope string

const (
	LiveEventSessionCreated       LiveEventType = "session.created"
	LiveEventSessionUpdated       LiveEventType = "session.updated"
	LiveEventPreviewUpdated       LiveEventType = "preview.updated"
	LiveEventAutomationUpdated    LiveEventType = "automation.updated"
	LiveEventAutomationRunUpdated LiveEventType = "automation.run.updated"
	LiveEventCodeReviewUpdated    LiveEventType = "code_review.updated"
	LiveEventPullRequestUpdated   LiveEventType = "pull_request.updated"
	LiveEventEvalBatchUpdated     LiveEventType = "eval_batch.updated"
	LiveEventEvalBootstrapUpdated LiveEventType = "eval_bootstrap.updated"
	LiveEventAuthorizationChanged LiveEventType = "authorization.changed"
)

const (
	LiveResourceSession       LiveResourceType = "session"
	LiveResourcePreview       LiveResourceType = "preview"
	LiveResourceAutomation    LiveResourceType = "automation"
	LiveResourceAutomationRun LiveResourceType = "automation_run"
	LiveResourceCodeReview    LiveResourceType = "code_review"
	LiveResourcePullRequest   LiveResourceType = "pull_request"
	LiveResourceEvalBatch     LiveResourceType = "eval_batch"
	LiveResourceEvalBootstrap LiveResourceType = "eval_bootstrap"
	LiveResourceAuthorization LiveResourceType = "authorization"
)

const (
	LiveEventScopeResource   LiveEventScope    = "resource"
	LiveEventScopeCollection LiveEventScope    = "collection"
	LiveAudienceOrg          LiveAudienceScope = "org"
	LiveAudienceRepository   LiveAudienceScope = "repository"
	LiveAudienceResource     LiveAudienceScope = "resource"
)

func (v LiveEventType) Validate() error {
	switch v {
	case LiveEventSessionCreated, LiveEventSessionUpdated, LiveEventPreviewUpdated,
		LiveEventAutomationUpdated, LiveEventAutomationRunUpdated, LiveEventCodeReviewUpdated,
		LiveEventPullRequestUpdated, LiveEventEvalBatchUpdated, LiveEventEvalBootstrapUpdated,
		LiveEventAuthorizationChanged:
		return nil
	default:
		return fmt.Errorf("unsupported live event type %q", v)
	}
}

func (v LiveResourceType) Validate() error {
	switch v {
	case LiveResourceSession, LiveResourcePreview, LiveResourceAutomation, LiveResourceAutomationRun,
		LiveResourceCodeReview, LiveResourcePullRequest, LiveResourceEvalBatch, LiveResourceEvalBootstrap,
		LiveResourceAuthorization:
		return nil
	default:
		return fmt.Errorf("unsupported live resource type %q", v)
	}
}

func (v LiveEventScope) Validate() error {
	if v != LiveEventScopeResource && v != LiveEventScopeCollection {
		return fmt.Errorf("unsupported live event scope %q", v)
	}
	return nil
}

func (v LiveAudienceScope) Validate() error {
	if v != LiveAudienceOrg && v != LiveAudienceRepository && v != LiveAudienceResource {
		return fmt.Errorf("unsupported live audience %q", v)
	}
	return nil
}

type LiveEvent struct {
	SchemaVersion int               `json:"schema_version"`
	EventID       uuid.UUID         `json:"event_id"`
	StreamID      string            `json:"-"`
	Type          LiveEventType     `json:"type"`
	Scope         LiveEventScope    `json:"scope"`
	OrgID         uuid.UUID         `json:"org_id"`
	ResourceType  LiveResourceType  `json:"resource_type"`
	ResourceID    *uuid.UUID        `json:"resource_id,omitempty"`
	ParentType    *LiveResourceType `json:"parent_type,omitempty"`
	ParentID      *uuid.UUID        `json:"parent_id,omitempty"`
	RepositoryID  *uuid.UUID        `json:"repository_id,omitempty"`
	Audience      LiveAudienceScope `json:"audience"`
	Version       *int64            `json:"version,omitempty"`
	CausationID   *uuid.UUID        `json:"causation_id,omitempty"`
	ChangedAt     time.Time         `json:"changed_at"`
	Payload       json.RawMessage   `json:"payload"`
}

type LiveInvalidationPayload struct {
	ListAffected   bool `json:"list_affected"`
	CountsAffected bool `json:"counts_affected"`
}

type SessionLiveProjection struct {
	Status              SessionStatus       `json:"status"`
	PRCreationState     PRCreationState     `json:"pr_creation_state,omitempty"`
	PRPushState         PRPushState         `json:"pr_push_state,omitempty"`
	BranchCreationState BranchCreationState `json:"branch_creation_state,omitempty"`
}
type SessionUpdatedPayload struct {
	StatusProjection *SessionLiveProjection `json:"status_projection,omitempty"`
	ListAffected     bool                   `json:"list_affected"`
	CountsAffected   bool                   `json:"counts_affected"`
}
type PreviewLiveProjection struct {
	Status    PreviewStatus         `json:"status"`
	Freshness PreviewFreshnessState `json:"freshness,omitempty"`
}
type PreviewUpdatedPayload struct {
	StatusProjection *PreviewLiveProjection `json:"status_projection,omitempty"`
	ListAffected     bool                   `json:"list_affected"`
	CountsAffected   bool                   `json:"counts_affected"`
}
type AutomationLiveProjection struct {
	Enabled bool `json:"enabled"`
}
type AutomationUpdatedPayload struct {
	StatusProjection *AutomationLiveProjection `json:"status_projection,omitempty"`
	ListAffected     bool                      `json:"list_affected"`
	CountsAffected   bool                      `json:"counts_affected"`
}
type AutomationRunLiveProjection struct {
	Status AutomationRunStatus `json:"status"`
}
type AutomationRunUpdatedPayload struct {
	StatusProjection *AutomationRunLiveProjection `json:"status_projection,omitempty"`
	ListAffected     bool                         `json:"list_affected"`
	CountsAffected   bool                         `json:"counts_affected"`
}
type AuthorizationChangedPayload struct {
	UserID uuid.UUID `json:"user_id"`
}

func validateLivePayload(eventType LiveEventType, payload json.RawMessage) error {
	var target any
	switch eventType {
	case LiveEventSessionCreated, LiveEventSessionUpdated:
		target = &SessionUpdatedPayload{}
	case LiveEventPreviewUpdated:
		target = &PreviewUpdatedPayload{}
	case LiveEventAutomationUpdated:
		target = &AutomationUpdatedPayload{}
	case LiveEventAutomationRunUpdated:
		target = &AutomationRunUpdatedPayload{}
	case LiveEventAuthorizationChanged:
		target = &AuthorizationChangedPayload{}
	default:
		target = &LiveInvalidationPayload{}
	}
	if err := json.Unmarshal(payload, target); err != nil {
		return fmt.Errorf("payload does not match %s: %w", eventType, err)
	}
	if auth, ok := target.(*AuthorizationChangedPayload); ok && auth.UserID == uuid.Nil {
		return errors.New("authorization payload requires user_id")
	}
	switch typed := target.(type) {
	case *SessionUpdatedPayload:
		if typed.StatusProjection != nil {
			if err := typed.StatusProjection.Status.Validate(); err != nil {
				return fmt.Errorf("invalid session status projection: %w", err)
			}
			if typed.StatusProjection.PRCreationState != "" {
				if err := typed.StatusProjection.PRCreationState.Validate(); err != nil {
					return err
				}
			}
			if typed.StatusProjection.PRPushState != "" {
				if err := typed.StatusProjection.PRPushState.Validate(); err != nil {
					return err
				}
			}
			if typed.StatusProjection.BranchCreationState != "" {
				if err := typed.StatusProjection.BranchCreationState.Validate(); err != nil {
					return err
				}
			}
		}
	case *PreviewUpdatedPayload:
		if typed.StatusProjection != nil {
			if err := typed.StatusProjection.Status.Validate(); err != nil {
				return err
			}
			if typed.StatusProjection.Freshness != "" {
				if err := typed.StatusProjection.Freshness.Validate(); err != nil {
					return err
				}
			}
		}
	case *AutomationRunUpdatedPayload:
		if typed.StatusProjection != nil {
			if err := typed.StatusProjection.Status.Validate(); err != nil {
				return err
			}
		}
	}
	return nil
}

func (e LiveEvent) Validate() error {
	if e.SchemaVersion != LiveEventSchemaVersion {
		return fmt.Errorf("unsupported schema version %d", e.SchemaVersion)
	}
	if e.EventID == uuid.Nil || e.OrgID == uuid.Nil || e.ChangedAt.IsZero() {
		return errors.New("event_id, org_id, and changed_at are required")
	}
	if err := e.Type.Validate(); err != nil {
		return err
	}
	if err := e.ResourceType.Validate(); err != nil {
		return err
	}
	if err := e.Scope.Validate(); err != nil {
		return err
	}
	if err := e.Audience.Validate(); err != nil {
		return err
	}
	if e.Scope == LiveEventScopeResource && e.ResourceID == nil {
		return errors.New("resource events require resource_id")
	}
	if e.Scope == LiveEventScopeCollection && e.ResourceID != nil {
		return errors.New("collection events cannot carry resource_id")
	}
	if e.Audience == LiveAudienceRepository && e.RepositoryID == nil {
		return errors.New("repository audience requires repository_id")
	}
	if e.Audience == LiveAudienceResource && e.ResourceID == nil {
		return errors.New("resource audience requires resource_id")
	}
	if e.Version != nil && *e.Version <= 0 {
		return errors.New("version must be positive")
	}
	if len(e.Payload) == 0 || len(e.Payload) > LiveEventMaxPayloadSize || !json.Valid(e.Payload) {
		return errors.New("payload must be valid JSON within the transport limit")
	}
	if err := validateLivePayload(e.Type, e.Payload); err != nil {
		return err
	}
	if e.Scope == LiveEventScopeResource {
		switch e.Type {
		case LiveEventSessionUpdated, LiveEventPreviewUpdated, LiveEventAutomationUpdated, LiveEventAutomationRunUpdated:
			if e.Version == nil {
				return errors.New("patchable projection events require version")
			}
		}
	}
	return nil
}

func NewLiveEvent(eventType LiveEventType, resourceType LiveResourceType, scope LiveEventScope, orgID uuid.UUID, resourceID *uuid.UUID, audience LiveAudienceScope, version *int64, causationID *uuid.UUID, payload any) (LiveEvent, error) {
	raw, err := json.Marshal(payload)
	if err != nil {
		return LiveEvent{}, fmt.Errorf("marshal live event payload: %w", err)
	}
	event := LiveEvent{SchemaVersion: LiveEventSchemaVersion, EventID: uuid.New(), Type: eventType, Scope: scope, OrgID: orgID, ResourceType: resourceType, ResourceID: resourceID, Audience: audience, Version: version, CausationID: causationID, ChangedAt: time.Now().UTC(), Payload: raw}
	if err := event.Validate(); err != nil {
		return LiveEvent{}, err
	}
	return event, nil
}
