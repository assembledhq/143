package agent

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/jobctx"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/models"
)

type sandboxAdmissionOrgStore struct {
	settings json.RawMessage
}

func (s *sandboxAdmissionOrgStore) GetByID(context.Context, uuid.UUID) (models.Organization, error) {
	return models.Organization{Settings: s.settings}, nil
}

type sandboxAdmissionJobStore struct {
	active            int
	releaseCalls      int
	releasedJobID     uuid.UUID
	releasedLockToken uuid.UUID
}

func (s *sandboxAdmissionJobStore) Enqueue(context.Context, uuid.UUID, string, string, any, int, *string) (uuid.UUID, error) {
	return uuid.New(), nil
}

func (s *sandboxAdmissionJobStore) EnqueueWithTarget(context.Context, uuid.UUID, string, string, any, int, *string, *string) (uuid.UUID, error) {
	return uuid.New(), nil
}

func (s *sandboxAdmissionJobStore) OldestPendingSessionJobAge(context.Context) (time.Duration, bool, error) {
	return 0, false, nil
}

func (s *sandboxAdmissionJobStore) QueueChangesetPRCreation(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, string, any, int) (uuid.UUID, bool, error) {
	return uuid.New(), true, nil
}

func (s *sandboxAdmissionJobStore) CountActiveSandboxTurnsByOrg(context.Context, uuid.UUID) (int, error) {
	return s.active, nil
}

func (s *sandboxAdmissionJobStore) ReleaseSandboxRoutingPlacementWithLease(_ context.Context, jobID, lockToken uuid.UUID) (bool, error) {
	s.releaseCalls++
	s.releasedJobID = jobID
	s.releasedLockToken = lockToken
	return true, nil
}

func TestAdmitSandboxTurnAppliesSharedOrgLimit(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		origin    models.SessionOrigin
		active    int
		expectErr bool
	}{
		{name: "allows claimed interactive turn at the shared limit", origin: models.SessionOriginManual, active: 2},
		{name: "rejects interactive turn beyond the shared limit", origin: models.SessionOriginManual, active: 3, expectErr: true},
		{name: "allows claimed code-review turn at the shared limit", origin: models.SessionOriginCodeReview, active: 2},
		{name: "rejects code-review turn beyond the shared limit", origin: models.SessionOriginCodeReview, active: 3, expectErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			orgID := uuid.New()
			orchestrator := &Orchestrator{
				orgs:   &sandboxAdmissionOrgStore{settings: json.RawMessage(`{"max_concurrent_runs":2}`)},
				jobs:   &sandboxAdmissionJobStore{active: tt.active},
				logger: zerolog.Nop(),
			}
			ctx := jobctx.WithJobID(context.Background(), uuid.New())
			reservation, err := orchestrator.admitSandboxTurn(ctx, &models.Session{
				ID:     uuid.New(),
				OrgID:  orgID,
				Origin: tt.origin,
			}, "continue_session", false, false)

			if tt.expectErr {
				require.ErrorIs(t, err, ErrSandboxTurnConcurrency, "turn above the shared org limit should wait without consuming an attempt")
				require.Nil(t, reservation, "rejected org admission should not reserve local capacity")
				return
			}
			require.NoError(t, err, "the current claimed turn should be allowed at the shared limit")
			require.Nil(t, reservation, "existing-sandbox turns should not reserve another local sandbox slot")
		})
	}
}

func TestAdmitSandboxTurnReleasesRejectedDurablePlacement(t *testing.T) {
	t.Parallel()

	jobID, lockToken := uuid.New(), uuid.New()
	jobs := &sandboxAdmissionJobStore{active: 3}
	orchestrator := &Orchestrator{
		orgs:   &sandboxAdmissionOrgStore{settings: json.RawMessage(`{"max_concurrent_runs":2}`)},
		jobs:   jobs,
		logger: zerolog.Nop(),
	}
	ctx := jobctx.WithLockToken(jobctx.WithJobID(context.Background(), jobID), lockToken)

	reservation, err := orchestrator.admitSandboxTurn(ctx, &models.Session{
		ID:     uuid.New(),
		OrgID:  uuid.New(),
		Origin: models.SessionOriginCodeReview,
	}, "continue_session", false, false)

	require.ErrorIs(t, err, ErrSandboxTurnConcurrency, "org admission should reject a turn beyond the shared limit")
	require.Nil(t, reservation, "rejected admission should not hold local capacity")
	require.Equal(t, 1, jobs.releaseCalls, "rejected admission should release its durable routing placement exactly once")
	require.Equal(t, jobID, jobs.releasedJobID, "placement release should be fenced to the claimed job")
	require.Equal(t, lockToken, jobs.releasedLockToken, "placement release should use the current attempt lock token")
}
