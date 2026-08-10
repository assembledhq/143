package agent

import (
	"context"
	"encoding/json"
	"testing"
	"time"

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
	active int
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

func (s *sandboxAdmissionJobStore) CountActiveSandboxTurnsByOrgAndClass(context.Context, uuid.UUID, models.SandboxWorkloadClass) (int, error) {
	return s.active, nil
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
