package externalidentity

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"
)

func TestResolver_ResolveExternalActor(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	mappedUserID := uuid.New()
	emailUserID := uuid.New()
	linkID := uuid.New()
	now := time.Now().UTC()
	email := "alice@example.com"
	handle := "alice"
	displayName := "Alice"

	tests := []struct {
		name          string
		links         *fakeLinkStore
		users         *fakeUserLookup
		input         ExternalActorInput
		wantMapped    *uuid.UUID
		wantSource    *models.ExternalUserLinkSource
		wantTeam      bool
		wantSuggest   bool
		wantLinkWrite bool
	}{
		{
			name: "active trusted link wins over email",
			links: &fakeLinkStore{active: &models.ExternalUserLink{
				ID: linkID, OrgID: orgID, Provider: models.ExternalIdentityProviderSlack, ProviderWorkspaceID: "T123",
				ProviderUserID: "U123", UserID: mappedUserID, Source: models.ExternalUserLinkSourceSelfLinked,
				Status: models.ExternalUserLinkStatusActive, Confidence: 100, CreatedAt: now,
			}},
			users:      &fakeUserLookup{user: models.User{ID: emailUserID}},
			input:      actorInput(email, true, handle, displayName),
			wantMapped: &mappedUserID,
			wantSource: ptr(models.ExternalUserLinkSourceSelfLinked),
		},
		{
			name:          "verified email creates email match",
			links:         &fakeLinkStore{getErr: pgx.ErrNoRows},
			users:         &fakeUserLookup{user: models.User{ID: emailUserID}},
			input:         actorInput(email, true, handle, displayName),
			wantMapped:    &emailUserID,
			wantSource:    ptr(models.ExternalUserLinkSourceEmailMatch),
			wantLinkWrite: true,
		},
		{
			name:        "verified email falls back when auto link disabled",
			links:       &fakeLinkStore{getErr: pgx.ErrNoRows},
			users:       &fakeUserLookup{user: models.User{ID: emailUserID}},
			input:       actorInput(email, true, handle, displayName),
			wantTeam:    true,
			wantSuggest: true,
		},
		{
			name:     "unverified email does not map",
			links:    &fakeLinkStore{getErr: pgx.ErrNoRows},
			users:    &fakeUserLookup{user: models.User{ID: emailUserID}},
			input:    actorInput(email, false, handle, displayName),
			wantTeam: true,
		},
		{
			name:        "handle match creates suggestion only",
			links:       &fakeLinkStore{getErr: pgx.ErrNoRows},
			users:       &fakeUserLookup{getErr: pgx.ErrNoRows, hintUserID: &emailUserID},
			input:       actorInput("", false, handle, displayName),
			wantTeam:    true,
			wantSuggest: true,
		},
		{
			name:     "no match falls back to team session",
			links:    &fakeLinkStore{getErr: pgx.ErrNoRows},
			users:    &fakeUserLookup{getErr: pgx.ErrNoRows},
			input:    actorInput("", false, "", ""),
			wantTeam: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			suggestions := &fakeSuggestionStore{}
			options := Options{AllowVerifiedEmailAutoLink: true}
			if tt.name == "verified email falls back when auto link disabled" {
				options.AllowVerifiedEmailAutoLink = false
			}
			resolver := NewResolver(tt.links, suggestions, nil, tt.users, options)
			got, err := resolver.ResolveExternalActor(context.Background(), orgID, tt.input)
			require.NoError(t, err, "ResolveExternalActor should not fail for expected resolution paths")
			require.Equal(t, tt.wantMapped, got.MappedUserID, "resolver should return the expected mapped user")
			require.Equal(t, tt.wantSource, got.Source, "resolver should report the expected authoritative source")
			require.Equal(t, tt.wantTeam, got.TeamFallback, "resolver should report whether the session must use team fallback")
			require.Equal(t, tt.wantSuggest, suggestions.upserted != nil, "resolver should create suggestions only for non-authoritative hints")
			require.Equal(t, tt.wantLinkWrite, tt.links.upserted != nil, "resolver should persist only authoritative automatic links")
		})
	}
}

func TestResolver_CreateSelfLinkClaim(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	claims := &fakeClaimStore{}
	resolver := NewResolver(&fakeLinkStore{}, nil, claims, &fakeUserLookup{}, Options{})
	claim, rawToken, err := resolver.CreateSelfLinkClaim(context.Background(), orgID, actorInput("", false, "", ""), []byte(`{"surface":"slack"}`))

	require.NoError(t, err, "CreateSelfLinkClaim should create a valid one-time claim")
	require.NotEmpty(t, rawToken, "CreateSelfLinkClaim should return the bearer token only to the caller")
	require.Equal(t, orgID, claim.OrgID, "claim should remain scoped to the requested organization")
	require.Equal(t, HashClaimToken(rawToken), claims.tokenHash, "claim store should receive only the token hash")
	require.LessOrEqual(t, claim.ExpiresAt, time.Now().UTC().Add(30*time.Minute), "claim should expire within the maximum lifetime")
}

func actorInput(email string, emailVerified bool, handle string, displayName string) ExternalActorInput {
	input := ExternalActorInput{
		Provider:            models.ExternalIdentityProviderSlack,
		ProviderWorkspaceID: "T123",
		ProviderUserID:      "U123",
		EmailVerified:       emailVerified,
	}
	if email != "" {
		input.Email = &email
	}
	if handle != "" {
		input.Handle = &handle
	}
	if displayName != "" {
		input.DisplayName = &displayName
	}
	return input
}

func ptr[T any](v T) *T { return &v }

type fakeLinkStore struct {
	active   *models.ExternalUserLink
	getErr   error
	upserted *models.ExternalUserLink
}

func (f *fakeLinkStore) GetActiveByExternal(context.Context, uuid.UUID, models.ExternalIdentityProvider, string, string) (models.ExternalUserLink, error) {
	if f.active != nil {
		return *f.active, nil
	}
	if f.getErr != nil {
		return models.ExternalUserLink{}, f.getErr
	}
	return models.ExternalUserLink{}, pgx.ErrNoRows
}

func (f *fakeLinkStore) UpsertActive(_ context.Context, link models.ExternalUserLink) (models.ExternalUserLink, error) {
	f.upserted = &link
	if link.ID == uuid.Nil {
		link.ID = uuid.New()
	}
	link.Status = models.ExternalUserLinkStatusActive
	return link, nil
}

type fakeSuggestionStore struct {
	upserted *models.ExternalUserLinkSuggestion
}

type fakeClaimStore struct {
	tokenHash []byte
}

func (f *fakeClaimStore) CreateClaim(_ context.Context, claim models.ExternalUserLinkClaim, tokenHash []byte) (models.ExternalUserLinkClaim, error) {
	f.tokenHash = append([]byte(nil), tokenHash...)
	claim.ID = uuid.New()
	claim.CreatedAt = time.Now().UTC()
	return claim, nil
}

func (f *fakeSuggestionStore) UpsertOpen(_ context.Context, suggestion models.ExternalUserLinkSuggestion) (models.ExternalUserLinkSuggestion, error) {
	f.upserted = &suggestion
	if suggestion.ID == uuid.Nil {
		suggestion.ID = uuid.New()
	}
	return suggestion, nil
}

type fakeUserLookup struct {
	user       models.User
	getErr     error
	hintUserID *uuid.UUID
}

func (f *fakeUserLookup) GetByOrgAndEmail(context.Context, uuid.UUID, string) (models.User, error) {
	if f.getErr != nil {
		return models.User{}, f.getErr
	}
	if f.user.ID == uuid.Nil {
		return models.User{}, pgx.ErrNoRows
	}
	return f.user, nil
}

func (f *fakeUserLookup) SuggestByOrgHint(context.Context, uuid.UUID, string, string) (*uuid.UUID, error) {
	if f.hintUserID != nil {
		return f.hintUserID, nil
	}
	if f.getErr != nil && !errors.Is(f.getErr, pgx.ErrNoRows) {
		return nil, f.getErr
	}
	return nil, nil
}
