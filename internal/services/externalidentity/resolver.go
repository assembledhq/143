package externalidentity

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

const (
	ConfidenceSelfLinked  = 100
	ConfidenceAdminLinked = 90
	ConfidenceEmailMatch  = 80
	ConfidenceFuzzyHint   = 40
)

type LinkStore interface {
	GetActiveByExternal(ctx context.Context, orgID uuid.UUID, provider models.ExternalIdentityProvider, workspaceID, providerUserID string) (models.ExternalUserLink, error)
	UpsertActive(ctx context.Context, link models.ExternalUserLink) (models.ExternalUserLink, error)
}

type SuggestionStore interface {
	UpsertOpen(ctx context.Context, suggestion models.ExternalUserLinkSuggestion) (models.ExternalUserLinkSuggestion, error)
}

type ClaimStore interface {
}

type UserLookup interface {
	GetByOrgAndEmail(ctx context.Context, orgID uuid.UUID, email string) (models.User, error)
	SuggestByOrgHint(ctx context.Context, orgID uuid.UUID, handle, displayName string) (*uuid.UUID, error)
}

type Options struct {
	AllowVerifiedEmailAutoLink bool
}

type Resolver struct {
	links       LinkStore
	suggestions SuggestionStore
	claims      ClaimStore
	users       UserLookup
	options     Options
}

func NewResolver(links LinkStore, suggestions SuggestionStore, claims ClaimStore, users UserLookup, options Options) *Resolver {
	return &Resolver{links: links, suggestions: suggestions, claims: claims, users: users, options: options}
}

type ExternalActorInput struct {
	Provider            models.ExternalIdentityProvider
	ProviderWorkspaceID string
	ProviderUserID      string
	Email               *string
	EmailVerified       bool
	Handle              *string
	DisplayName         *string
}

type ExternalActorResolution struct {
	MappedUserID    *uuid.UUID
	LinkID          *uuid.UUID
	Source          *models.ExternalUserLinkSource
	Confidence      int
	TeamFallback    bool
	LinkRequiredFor []string
	SuggestedUserID *uuid.UUID
	ClaimURL        *string
}

func (r *Resolver) ResolveExternalActor(ctx context.Context, orgID uuid.UUID, input ExternalActorInput) (ExternalActorResolution, error) {
	if err := validateExternalActorInput(input); err != nil {
		return ExternalActorResolution{}, err
	}

	link, err := r.links.GetActiveByExternal(ctx, orgID, input.Provider, input.ProviderWorkspaceID, input.ProviderUserID)
	if err == nil {
		return resolutionFromLink(link), nil
	}
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return ExternalActorResolution{}, fmt.Errorf("resolve external user link: %w", err)
	}

	if r.options.AllowVerifiedEmailAutoLink && input.EmailVerified && input.Email != nil && strings.TrimSpace(*input.Email) != "" {
		user, err := r.users.GetByOrgAndEmail(ctx, orgID, strings.TrimSpace(*input.Email))
		if err == nil {
			link, err := r.links.UpsertActive(ctx, models.ExternalUserLink{
				OrgID:               orgID,
				Provider:            input.Provider,
				ProviderWorkspaceID: input.ProviderWorkspaceID,
				ProviderUserID:      input.ProviderUserID,
				UserID:              user.ID,
				Source:              models.ExternalUserLinkSourceEmailMatch,
				Confidence:          ConfidenceEmailMatch,
				ExternalEmail:       normalizedPtr(input.Email),
				ExternalHandle:      normalizedPtr(input.Handle),
				ExternalDisplayName: normalizedPtr(input.DisplayName),
			})
			if err != nil {
				return ExternalActorResolution{}, fmt.Errorf("persist email-matched external user link: %w", err)
			}
			return resolutionFromLink(link), nil
		}
		if err != nil && !errors.Is(err, pgx.ErrNoRows) {
			return ExternalActorResolution{}, fmt.Errorf("lookup external actor by verified email: %w", err)
		}
	}

	if r.suggestions != nil && (input.Handle != nil || input.DisplayName != nil) {
		suggestedUserID, err := r.users.SuggestByOrgHint(ctx, orgID, deref(input.Handle), deref(input.DisplayName))
		if err != nil {
			return ExternalActorResolution{}, fmt.Errorf("lookup external actor suggestion: %w", err)
		}
		if suggestedUserID != nil {
			suggestion, err := r.suggestions.UpsertOpen(ctx, models.ExternalUserLinkSuggestion{
				OrgID:               orgID,
				Provider:            input.Provider,
				ProviderWorkspaceID: input.ProviderWorkspaceID,
				ProviderUserID:      input.ProviderUserID,
				SuggestedUserID:     *suggestedUserID,
				Reason:              "profile_hint",
				Confidence:          ConfidenceFuzzyHint,
				ExternalEmail:       normalizedPtr(input.Email),
				ExternalHandle:      normalizedPtr(input.Handle),
				ExternalDisplayName: normalizedPtr(input.DisplayName),
			})
			if err != nil {
				return ExternalActorResolution{}, fmt.Errorf("persist external actor suggestion: %w", err)
			}
			return ExternalActorResolution{
				TeamFallback:    true,
				LinkRequiredFor: defaultLinkRequiredCapabilities(),
				SuggestedUserID: &suggestion.SuggestedUserID,
			}, nil
		}
	}

	return ExternalActorResolution{
		TeamFallback:    true,
		LinkRequiredFor: defaultLinkRequiredCapabilities(),
	}, nil
}

func resolutionFromLink(link models.ExternalUserLink) ExternalActorResolution {
	userID := link.UserID
	linkID := link.ID
	source := link.Source
	return ExternalActorResolution{
		MappedUserID: &userID,
		LinkID:       &linkID,
		Source:       &source,
		Confidence:   link.Confidence,
		TeamFallback: false,
	}
}

func validateExternalActorInput(input ExternalActorInput) error {
	if err := input.Provider.Validate(); err != nil {
		return err
	}
	if strings.TrimSpace(input.ProviderWorkspaceID) == "" {
		return fmt.Errorf("provider workspace id is required")
	}
	if strings.TrimSpace(input.ProviderUserID) == "" {
		return fmt.Errorf("provider user id is required")
	}
	return nil
}

func defaultLinkRequiredCapabilities() []string {
	return []string{"personal_credentials", "personal_pr_authorship", "sensitive_dm_delivery"}
}

func normalizedPtr(value *string) *string {
	if value == nil {
		return nil
	}
	trimmed := strings.TrimSpace(*value)
	if trimmed == "" {
		return nil
	}
	return &trimmed
}

func deref(value *string) string {
	if value == nil {
		return ""
	}
	return strings.TrimSpace(*value)
}

func GenerateClaimToken() (raw string, hash []byte, err error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", nil, fmt.Errorf("generate external identity claim token: %w", err)
	}
	raw = base64.RawURLEncoding.EncodeToString(buf)
	return raw, HashClaimToken(raw), nil
}

func HashClaimToken(token string) []byte {
	sum := sha256.Sum256([]byte(token))
	return sum[:]
}

func ClaimExpiresAt(now time.Time) time.Time {
	return now.Add(30 * time.Minute)
}
