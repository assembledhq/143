package db

import (
	"context"
	"encoding/json"
	"net/netip"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
)

// AuditEmitter provides convenience methods for emitting audit log entries.
// All Emit* methods log errors internally and never return them, so callers
// can treat emission as fire-and-forget without discarding errors silently.
type AuditEmitter struct {
	store  *AuditLogStore
	logger zerolog.Logger
}

func NewAuditEmitter(store *AuditLogStore, logger zerolog.Logger) *AuditEmitter {
	return &AuditEmitter{store: store, logger: logger}
}

// EmitUserAction logs an action performed by an authenticated user.
func (e *AuditEmitter) EmitUserAction(ctx context.Context, params UserActionParams) {
	entry := &models.AuditLog{
		OrgID:        params.OrgID,
		ActorType:    models.AuditActorUser,
		ActorID:      params.UserID.String(),
		UserID:       &params.UserID,
		Action:       params.Action,
		ResourceType: params.ResourceType,
		ResourceID:   params.ResourceID,
		Details:      params.Details,
		RequestID:    params.RequestID,
		IPAddress:    params.IPAddress,
		UserAgent:    params.UserAgent,
		SessionID:    params.SessionID,
		ProjectID:    params.ProjectID,
	}
	if err := e.store.Create(ctx, entry); err != nil {
		e.logger.Warn().Err(err).
			Str("action", string(params.Action)).
			Str("actor_id", params.UserID.String()).
			Msg("failed to emit audit log")
	}
}

// EmitUserActions logs multiple user-actor audits in a single DB round-trip.
// Prefer this over a loop of EmitUserAction calls when emitting more than
// one entry at a time: an N-row INSERT scales O(1) in network latency where
// N separate INSERTs scale O(N). On error the whole batch is dropped — same
// fire-and-forget contract as EmitUserAction, so callers don't need to
// handle errors.
//
// All entries must share the same OrgID; mixed-org batches are rejected by
// the underlying store. In practice this is naturally satisfied because a
// batch emit comes from a single request, which has a single active org.
func (e *AuditEmitter) EmitUserActions(ctx context.Context, paramsList []UserActionParams) {
	if len(paramsList) == 0 {
		return
	}
	orgID := paramsList[0].OrgID
	entries := make([]*models.AuditLog, 0, len(paramsList))
	for _, params := range paramsList {
		userID := params.UserID
		entries = append(entries, &models.AuditLog{
			OrgID:        params.OrgID,
			ActorType:    models.AuditActorUser,
			ActorID:      userID.String(),
			UserID:       &userID,
			Action:       params.Action,
			ResourceType: params.ResourceType,
			ResourceID:   params.ResourceID,
			Details:      params.Details,
			RequestID:    params.RequestID,
			IPAddress:    params.IPAddress,
			UserAgent:    params.UserAgent,
			SessionID:    params.SessionID,
			ProjectID:    params.ProjectID,
		})
	}
	if err := e.store.CreateBatch(ctx, orgID, entries); err != nil {
		e.logger.Warn().Err(err).
			Int("batch_size", len(entries)).
			Msg("failed to emit audit log batch")
	}
}

type UserActionParams struct {
	OrgID        uuid.UUID
	UserID       uuid.UUID
	Action       models.AuditAction
	ResourceType models.AuditResourceType
	ResourceID   *string
	Details      json.RawMessage
	RequestID    *string
	IPAddress    *netip.Prefix
	UserAgent    *string
	SessionID    *uuid.UUID
	ProjectID    *uuid.UUID
}

// EmitAPIAction logs an action performed by an external API client.
func (e *AuditEmitter) EmitAPIAction(ctx context.Context, params APIActionParams) {
	entry := &models.AuditLog{
		OrgID:        params.OrgID,
		ActorType:    models.AuditActorAPI,
		ActorID:      params.APIClientID.String(),
		Action:       params.Action,
		ResourceType: params.ResourceType,
		ResourceID:   params.ResourceID,
		Details:      params.Details,
		RequestID:    params.RequestID,
		IPAddress:    params.IPAddress,
		UserAgent:    params.UserAgent,
		SessionID:    params.SessionID,
		ProjectID:    params.ProjectID,
	}
	if err := e.store.Create(ctx, entry); err != nil {
		e.logger.Warn().Err(err).
			Str("action", string(params.Action)).
			Str("actor_id", params.APIClientID.String()).
			Msg("failed to emit audit log")
	}
}

type APIActionParams struct {
	OrgID        uuid.UUID
	APIClientID  uuid.UUID
	APITokenID   *uuid.UUID
	Action       models.AuditAction
	ResourceType models.AuditResourceType
	ResourceID   *string
	Details      json.RawMessage
	RequestID    *string
	IPAddress    *netip.Prefix
	UserAgent    *string
	SessionID    *uuid.UUID
	ProjectID    *uuid.UUID
}

// EmitSystemAction logs an action performed by a system process.
func (e *AuditEmitter) EmitSystemAction(ctx context.Context, params SystemActionParams) {
	entry := &models.AuditLog{
		OrgID:        params.OrgID,
		ActorType:    models.AuditActorSystem,
		ActorID:      params.ActorID,
		Action:       params.Action,
		ResourceType: params.ResourceType,
		ResourceID:   params.ResourceID,
		Details:      params.Details,
		SessionID:    params.SessionID,
		ProjectID:    params.ProjectID,
	}
	if err := e.store.Create(ctx, entry); err != nil {
		e.logger.Warn().Err(err).
			Str("action", string(params.Action)).
			Str("actor_id", params.ActorID).
			Msg("failed to emit audit log")
	}
}

type SystemActionParams struct {
	OrgID        uuid.UUID
	ActorID      string
	Action       models.AuditAction
	ResourceType models.AuditResourceType
	ResourceID   *string
	Details      json.RawMessage
	SessionID    *uuid.UUID
	ProjectID    *uuid.UUID
}

// EmitAgentAction logs an action performed by an agent.
func (e *AuditEmitter) EmitAgentAction(ctx context.Context, params AgentActionParams) {
	entry := &models.AuditLog{
		OrgID:        params.OrgID,
		ActorType:    models.AuditActorAgent,
		ActorID:      params.AgentRunID,
		Action:       params.Action,
		ResourceType: params.ResourceType,
		ResourceID:   params.ResourceID,
		Details:      params.Details,
		SessionID:    params.SessionID,
		ProjectID:    params.ProjectID,
	}
	if err := e.store.Create(ctx, entry); err != nil {
		e.logger.Warn().Err(err).
			Str("action", string(params.Action)).
			Str("actor_id", params.AgentRunID).
			Msg("failed to emit audit log")
	}
}

type AgentActionParams struct {
	OrgID        uuid.UUID
	AgentRunID   string
	Action       models.AuditAction
	ResourceType models.AuditResourceType
	ResourceID   *string
	Details      json.RawMessage
	SessionID    *uuid.UUID
	ProjectID    *uuid.UUID
}

// EmitWebhookAction logs an action triggered by an external webhook.
func (e *AuditEmitter) EmitWebhookAction(ctx context.Context, params WebhookActionParams) {
	entry := &models.AuditLog{
		OrgID:        params.OrgID,
		ActorType:    models.AuditActorWebhook,
		ActorID:      params.ProviderName,
		Action:       params.Action,
		ResourceType: params.ResourceType,
		ResourceID:   params.ResourceID,
		Details:      params.Details,
		RequestID:    params.RequestID,
		IPAddress:    params.IPAddress,
		SessionID:    params.SessionID,
		ProjectID:    params.ProjectID,
	}
	if err := e.store.Create(ctx, entry); err != nil {
		e.logger.Warn().Err(err).
			Str("action", string(params.Action)).
			Str("actor_id", params.ProviderName).
			Msg("failed to emit audit log")
	}
}

type WebhookActionParams struct {
	OrgID        uuid.UUID
	ProviderName string
	Action       models.AuditAction
	ResourceType models.AuditResourceType
	ResourceID   *string
	Details      json.RawMessage
	RequestID    *string
	IPAddress    *netip.Prefix
	SessionID    *uuid.UUID
	ProjectID    *uuid.UUID
}
