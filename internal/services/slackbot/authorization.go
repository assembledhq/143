package slackbot

import (
	"context"
	"errors"
	"fmt"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type Capability string

const (
	CapabilitySession    Capability = "session"
	CapabilityPreview    Capability = "preview"
	CapabilityPRRequest  Capability = "pr_request"
	CapabilityHumanInput Capability = "human_input"
)

type SlackUserLinkStore interface {
	GetBySlackUser(ctx context.Context, orgID uuid.UUID, teamID, slackUserID string) (models.SlackUserLink, error)
}

type ExternalUserLinkStore interface {
	GetActiveByExternal(ctx context.Context, orgID uuid.UUID, provider models.ExternalIdentityProvider, workspaceID, providerUserID string) (models.ExternalUserLink, error)
}

type MembershipStore interface {
	Get(ctx context.Context, userID, orgID uuid.UUID) (models.OrganizationMembership, error)
}

type SlackChannelStore interface {
	GetEffectiveByChannel(ctx context.Context, orgID uuid.UUID, teamID, channelID string) (models.EffectiveSlackChannelSettings, error)
}

type Authorizer struct {
	links       SlackUserLinkStore
	external    ExternalUserLinkStore
	memberships MembershipStore
	channels    SlackChannelStore
}

type ActionRequest struct {
	OrgID       uuid.UUID
	TeamID      string
	ChannelID   string
	SlackUserID string

	Capability   Capability
	AllowedRoles []models.Role

	RequireMapped            bool
	AllowUnmappedTeamSession bool
	IsOriginatingTeamSession bool
}

type Decision struct {
	MappedUserID *uuid.UUID
	Role         models.Role
	TeamSession  bool
}

func NewAuthorizer(links SlackUserLinkStore, memberships MembershipStore, channels SlackChannelStore) *Authorizer {
	return &Authorizer{links: links, memberships: memberships, channels: channels}
}

func NewAuthorizerWithExternal(external ExternalUserLinkStore, memberships MembershipStore, channels SlackChannelStore) *Authorizer {
	return &Authorizer{external: external, memberships: memberships, channels: channels}
}

func (a *Authorizer) Authorize(ctx context.Context, req ActionRequest) (Decision, error) {
	if req.OrgID == uuid.Nil {
		return Decision{}, fmt.Errorf("slack authorization requires org_id")
	}
	if req.SlackUserID == "" {
		return Decision{}, fmt.Errorf("slack authorization requires slack user")
	}
	if err := a.authorizeChannelCapability(ctx, req); err != nil {
		return Decision{}, err
	}

	link, err := a.resolveUserLink(ctx, req)
	if err != nil {
		if !errors.Is(err, pgx.ErrNoRows) {
			return Decision{}, err
		}
		return a.authorizeUnmapped(req)
	}
	if link.UserID == nil {
		return a.authorizeUnmapped(req)
	}
	if len(req.AllowedRoles) == 0 {
		return Decision{MappedUserID: link.UserID, TeamSession: false}, nil
	}
	if a.memberships == nil {
		return Decision{}, fmt.Errorf("slack authorization requires membership store for role-gated action")
	}
	membership, err := a.memberships.Get(ctx, *link.UserID, req.OrgID)
	if err != nil {
		return Decision{}, fmt.Errorf("load slack mapped user membership: %w", err)
	}
	if !roleAllowed(membership.Role, req.AllowedRoles) {
		return Decision{}, fmt.Errorf("slack action requires one of roles %v", req.AllowedRoles)
	}
	return Decision{MappedUserID: link.UserID, Role: membership.Role, TeamSession: false}, nil
}

func (a *Authorizer) authorizeChannelCapability(ctx context.Context, req ActionRequest) error {
	if a.channels == nil || req.ChannelID == "" || req.Capability == "" {
		return nil
	}
	settings, err := a.channels.GetEffectiveByChannel(ctx, req.OrgID, req.TeamID, req.ChannelID)
	if err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return nil
		}
		return fmt.Errorf("load slack channel settings: %w", err)
	}
	if !stringSliceContains(settings.AllowedActions, string(req.Capability)) {
		return fmt.Errorf("slack channel does not allow %s actions", req.Capability)
	}
	return nil
}

func (a *Authorizer) resolveUserLink(ctx context.Context, req ActionRequest) (models.SlackUserLink, error) {
	if a.external != nil {
		link, err := a.external.GetActiveByExternal(ctx, req.OrgID, models.ExternalIdentityProviderSlack, req.TeamID, req.SlackUserID)
		if err != nil {
			return models.SlackUserLink{}, fmt.Errorf("resolve external slack user link: %w", err)
		}
		userID := link.UserID
		return models.SlackUserLink{OrgID: req.OrgID, SlackTeamID: req.TeamID, SlackUserID: req.SlackUserID, UserID: &userID}, nil
	}
	if a.links == nil {
		return models.SlackUserLink{}, pgx.ErrNoRows
	}
	link, err := a.links.GetBySlackUser(ctx, req.OrgID, req.TeamID, req.SlackUserID)
	if err != nil {
		return models.SlackUserLink{}, fmt.Errorf("resolve slack user link: %w", err)
	}
	return link, nil
}

func (a *Authorizer) authorizeUnmapped(req ActionRequest) (Decision, error) {
	if req.RequireMapped {
		return Decision{}, fmt.Errorf("slack action requires a linked 143 user")
	}
	if req.AllowUnmappedTeamSession && req.IsOriginatingTeamSession {
		return Decision{TeamSession: true}, nil
	}
	return Decision{}, fmt.Errorf("slack action requires originating team session or linked 143 user")
}

func roleAllowed(role models.Role, allowed []models.Role) bool {
	for _, candidate := range allowed {
		if role == candidate {
			return true
		}
	}
	return false
}

func stringSliceContains(values []string, target string) bool {
	for _, value := range values {
		if value == target {
			return true
		}
	}
	return false
}
