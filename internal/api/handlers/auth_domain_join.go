package handlers

import (
	"context"

	"github.com/assembledhq/143/internal/db"
	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
)

type domainAutoJoiner interface {
	FindVerifiedAutoJoinByEmailDomain(ctx context.Context, email string) (models.VerifiedDomain, error)
	GrantDomainMembership(ctx context.Context, userID uuid.UUID, domain models.VerifiedDomain) error
}

func maybeAutoJoinVerifiedDomain(ctx context.Context, joiner domainAutoJoiner, userID uuid.UUID, email string, providerEmailVerified bool) error {
	if joiner == nil || !providerEmailVerified {
		return nil
	}
	domain, err := joiner.FindVerifiedAutoJoinByEmailDomain(ctx, email)
	if err != nil {
		if db.IsNoVerifiedDomainMatch(err) {
			return nil
		}
		return err
	}
	return joiner.GrantDomainMembership(ctx, userID, domain)
}
