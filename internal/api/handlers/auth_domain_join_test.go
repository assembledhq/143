package handlers

import (
	"context"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type recordingDomainAutoJoiner struct {
	domain   models.VerifiedDomain
	findErr  error
	grants   int
	lastOrgs []uuid.UUID
}

func (j *recordingDomainAutoJoiner) FindVerifiedAutoJoinByEmailDomain(ctx context.Context, email string) (models.VerifiedDomain, error) {
	return j.domain, j.findErr
}

func (j *recordingDomainAutoJoiner) GrantDomainMembership(ctx context.Context, userID uuid.UUID, domain models.VerifiedDomain) error {
	j.grants++
	j.lastOrgs = append(j.lastOrgs, domain.OrgID)
	return nil
}

func TestAuthHandler_MaybeAutoJoinVerifiedDomain(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	userID := uuid.New()
	joiner := &recordingDomainAutoJoiner{
		domain: models.VerifiedDomain{
			ID:              uuid.New(),
			OrgID:           orgID,
			Domain:          "example.com",
			Status:          models.VerifiedDomainStatusVerified,
			AutoJoinEnabled: true,
			AutoJoinRole:    models.RoleMember,
			CreatedAt:       time.Now(),
			UpdatedAt:       time.Now(),
		},
	}

	err := maybeAutoJoinVerifiedDomain(context.Background(), joiner, userID, "person@example.com", true)
	require.NoError(t, err, "verified provider email should be eligible for auto-join")
	require.Equal(t, 1, joiner.grants, "verified provider email should grant one domain membership")
	require.Equal(t, []uuid.UUID{orgID}, joiner.lastOrgs, "auto-join should target the verified domain org")

	err = maybeAutoJoinVerifiedDomain(context.Background(), joiner, userID, "person@example.com", false)
	require.NoError(t, err, "unverified provider email should be ignored without error")
	require.Equal(t, 1, joiner.grants, "unverified provider email should not grant another membership")
}
