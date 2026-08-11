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
	active                 int
	requestedWorkloadClass models.SandboxWorkloadClass
	releaseCalls           int
	releasedJobID          uuid.UUID
	releasedLockToken      uuid.UUID
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

func (s *sandboxAdmissionJobStore) CountActiveSandboxTurnsByOrgAndClass(_ context.Context, _ uuid.UUID, workloadClass models.SandboxWorkloadClass) (int, error) {
	s.requestedWorkloadClass = workloadClass
	return s.active, nil
}

func (s *sandboxAdmissionJobStore) ReleaseSandboxRoutingPlacementWithLease(_ context.Context, jobID, lockToken uuid.UUID) (bool, error) {
	s.releaseCalls++
	s.releasedJobID = jobID
	s.releasedLockToken = lockToken
	return true, nil
}

func TestAdmitSandboxTurnAppliesCodeReviewOrgLimit(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		active    int
		expectErr bool
	}{
		{name: "allows the claimed turn at the configured limit", active: 2},
		{name: "rejects a claimed turn beyond the configured limit", active: 3, expectErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			orgID := uuid.New()
			orchestrator := &Orchestrator{
				orgs: &sandboxAdmissionOrgStore{settings: json.RawMessage(`{
					"max_concurrent_runs": 20,
					"code_review_max_concurrent_turns": 2
				}`)},
				jobs:   &sandboxAdmissionJobStore{active: tt.active},
				logger: zerolog.Nop(),
			}
			reservation, err := orchestrator.admitSandboxTurn(context.Background(), &models.Session{
				ID:     uuid.New(),
				OrgID:  orgID,
				Origin: models.SessionOriginCodeReview,
			}, "continue_session", false, false)

			if tt.expectErr {
				require.ErrorIs(t, err, ErrSandboxTurnConcurrency, "code-review turn above the org limit should wait without consuming an attempt")
				require.Nil(t, reservation, "rejected org admission should not reserve local capacity")
				return
			}
			require.NoError(t, err, "the current claimed code-review turn should be allowed at the configured limit")
			require.Nil(t, reservation, "existing-sandbox turns should not reserve another local sandbox slot")
		})
	}
}

func TestAdmitSandboxTurnCountsOnlyInteractiveWorkAgainstInteractiveLimit(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		active    int
		expectErr bool
	}{
		{name: "allows the claimed interactive turn at the limit", active: 2},
		{name: "rejects a claimed interactive turn beyond the limit", active: 3, expectErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			jobs := &sandboxAdmissionJobStore{active: tt.active}
			orchestrator := &Orchestrator{
				jobs:          jobs,
				maxConcurrent: 2,
				logger:        zerolog.Nop(),
			}
			ctx := jobctx.WithJobID(context.Background(), uuid.New())
			reservation, err := orchestrator.admitSandboxTurn(ctx, &models.Session{
				ID:     uuid.New(),
				OrgID:  uuid.New(),
				Origin: models.SessionOriginManual,
			}, "continue_session", false, false)

			require.Equal(t, models.SandboxWorkloadClassInteractive, jobs.requestedWorkloadClass, "interactive admission should count only interactive running jobs")
			if tt.expectErr {
				require.ErrorIs(t, err, ErrConcurrencyLimit, "interactive work above its own limit should wait")
				require.Nil(t, reservation, "rejected interactive admission should not reserve local capacity")
				return
			}
			require.NoError(t, err, "code-review sessions should not consume the interactive org limit")
			require.Nil(t, reservation, "existing-sandbox turns should not reserve another local sandbox slot")
		})
	}
}

func TestAdmitSandboxTurnReleasesRejectedDurablePlacement(t *testing.T) {
	t.Parallel()

	jobID, lockToken := uuid.New(), uuid.New()
	jobs := &sandboxAdmissionJobStore{active: 3}
	orchestrator := &Orchestrator{
		orgs: &sandboxAdmissionOrgStore{settings: json.RawMessage(`{
			"max_concurrent_runs": 20,
			"code_review_max_concurrent_turns": 2
		}`)},
		jobs:   jobs,
		logger: zerolog.Nop(),
	}
	ctx := jobctx.WithLockToken(jobctx.WithJobID(context.Background(), jobID), lockToken)

	reservation, err := orchestrator.admitSandboxTurn(ctx, &models.Session{
		ID:     uuid.New(),
		OrgID:  uuid.New(),
		Origin: models.SessionOriginCodeReview,
	}, "continue_session", false, false)

	require.ErrorIs(t, err, ErrSandboxTurnConcurrency, "org admission should reject a review turn beyond its configured limit")
	require.Nil(t, reservation, "rejected admission should not hold local capacity")
	require.Equal(t, 1, jobs.releaseCalls, "rejected admission should release its durable routing placement exactly once")
	require.Equal(t, jobID, jobs.releasedJobID, "placement release should be fenced to the claimed job")
	require.Equal(t, lockToken, jobs.releasedLockToken, "placement release should use the current attempt lock token")
}
