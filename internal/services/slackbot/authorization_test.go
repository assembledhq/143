package slackbot

import (
	"context"
	"errors"
	"testing"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

type stubUserLinkStore struct {
	link models.SlackUserLink
	err  error
}

func (s stubUserLinkStore) GetBySlackUser(_ context.Context, _ uuid.UUID, _, _ string) (models.SlackUserLink, error) {
	return s.link, s.err
}

type stubMembershipStore struct {
	membership models.OrganizationMembership
	err        error
}

func (s stubMembershipStore) Get(_ context.Context, _ uuid.UUID, _ uuid.UUID) (models.OrganizationMembership, error) {
	return s.membership, s.err
}

type stubChannelStore struct {
	settings models.SlackChannelSettings
	err      error
}

func (s stubChannelStore) GetByChannel(_ context.Context, _ uuid.UUID, _, _ string) (models.SlackChannelSettings, error) {
	return s.settings, s.err
}

func TestAuthorizerAuthorize(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	userID := uuid.New()

	tests := []struct {
		name      string
		input     ActionRequest
		links     SlackUserLinkStore
		members   MembershipStore
		channels  SlackChannelStore
		expected  Decision
		expectErr bool
	}{
		{
			name: "mapped member may run allowed channel action",
			input: ActionRequest{
				OrgID:         orgID,
				TeamID:        "T1",
				ChannelID:     "C1",
				SlackUserID:   "U1",
				Capability:    CapabilityPreview,
				RequireMapped: true,
				AllowedRoles:  []models.Role{models.RoleAdmin, models.RoleMember, models.RoleBuilder},
			},
			links: stubUserLinkStore{link: models.SlackUserLink{UserID: &userID}},
			members: stubMembershipStore{membership: models.OrganizationMembership{
				UserID: userID,
				OrgID:  orgID,
				Role:   models.RoleMember,
			}},
			channels: stubChannelStore{settings: models.SlackChannelSettings{AllowedActions: []string{string(CapabilityPreview)}}},
			expected: Decision{
				MappedUserID: &userID,
				Role:         models.RoleMember,
				TeamSession:  false,
			},
		},
		{
			name: "viewer is rejected for member action",
			input: ActionRequest{
				OrgID:         orgID,
				TeamID:        "T1",
				ChannelID:     "C1",
				SlackUserID:   "U1",
				Capability:    CapabilityPreview,
				RequireMapped: true,
				AllowedRoles:  []models.Role{models.RoleAdmin, models.RoleMember, models.RoleBuilder},
			},
			links: stubUserLinkStore{link: models.SlackUserLink{UserID: &userID}},
			members: stubMembershipStore{membership: models.OrganizationMembership{
				UserID: userID,
				OrgID:  orgID,
				Role:   models.RoleViewer,
			}},
			channels:  stubChannelStore{settings: models.SlackChannelSettings{AllowedActions: []string{string(CapabilityPreview)}}},
			expectErr: true,
		},
		{
			name: "unmapped user may operate originating team session when channel allows capability",
			input: ActionRequest{
				OrgID:                    orgID,
				TeamID:                   "T1",
				ChannelID:                "C1",
				SlackUserID:              "U1",
				Capability:               CapabilityPreview,
				AllowUnmappedTeamSession: true,
				IsOriginatingTeamSession: true,
			},
			links:    stubUserLinkStore{err: pgx.ErrNoRows},
			channels: stubChannelStore{settings: models.SlackChannelSettings{AllowedActions: []string{string(CapabilityPreview)}}},
			expected: Decision{
				MappedUserID: nil,
				Role:         "",
				TeamSession:  true,
			},
		},
		{
			name: "unmapped user cannot operate non-originating team session",
			input: ActionRequest{
				OrgID:                    orgID,
				TeamID:                   "T1",
				ChannelID:                "C1",
				SlackUserID:              "U1",
				Capability:               CapabilityPreview,
				AllowUnmappedTeamSession: true,
				IsOriginatingTeamSession: false,
			},
			links:     stubUserLinkStore{err: pgx.ErrNoRows},
			channels:  stubChannelStore{settings: models.SlackChannelSettings{AllowedActions: []string{string(CapabilityPreview)}}},
			expectErr: true,
		},
		{
			name: "channel capability denial rejects action before role grants",
			input: ActionRequest{
				OrgID:         orgID,
				TeamID:        "T1",
				ChannelID:     "C1",
				SlackUserID:   "U1",
				Capability:    CapabilityPreview,
				RequireMapped: true,
				AllowedRoles:  []models.Role{models.RoleAdmin, models.RoleMember, models.RoleBuilder},
			},
			links: stubUserLinkStore{link: models.SlackUserLink{UserID: &userID}},
			members: stubMembershipStore{membership: models.OrganizationMembership{
				UserID: userID,
				OrgID:  orgID,
				Role:   models.RoleAdmin,
			}},
			channels:  stubChannelStore{settings: models.SlackChannelSettings{AllowedActions: []string{string(CapabilitySession)}}},
			expectErr: true,
		},
		{
			name: "unexpected link store error is propagated",
			input: ActionRequest{
				OrgID:       orgID,
				TeamID:      "T1",
				ChannelID:   "C1",
				SlackUserID: "U1",
				Capability:  CapabilityPreview,
			},
			links:     stubUserLinkStore{err: errors.New("database unavailable")},
			channels:  stubChannelStore{settings: models.SlackChannelSettings{AllowedActions: []string{string(CapabilityPreview)}}},
			expectErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			authorizer := NewAuthorizer(tt.links, tt.members, tt.channels)
			actual, err := authorizer.Authorize(context.Background(), tt.input)
			if tt.expectErr {
				require.Error(t, err, "Authorize should reject invalid Slack action")
				return
			}
			require.NoError(t, err, "Authorize should allow valid Slack action")
			require.Equal(t, tt.expected, actual, "Authorize should return the expected identity decision")
		})
	}
}
