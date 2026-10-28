package cluster

import (
	"context"
	"errors"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
	"testing"
)

type orderedWakeRepair struct {
	order *[]string
	name  string
	err   error
}

func (s *orderedWakeRepair) RepairMissingWakes(context.Context) error {
	*s.order = append(*s.order, s.name)
	return s.err
}

type orderedNoIntegrations struct {
	mockIntegrations
	order *[]string
}

func (s *orderedNoIntegrations) ListOrgsWithActiveIntegrations(context.Context) ([]uuid.UUID, error) {
	*s.order = append(*s.order, "integrations")
	return nil, nil
}
func TestCallerSendRepairPrecedesIntegrationCensus(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name string
		err  error
	}{{name: "no integrations"}, {name: "prior repair failure", err: errors.New("synthetic unavailable schedule store")}}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			order := []string{}
			scheduler := NewScheduler(&mockSchedulerLock{}, &mockJobs{}, &mockOrgs{}, &orderedNoIntegrations{order: &order}, &mockRepos{}, zerolog.Nop())
			scheduler.SetCodeReviewScheduleReconciler(&orderedWakeRepair{order: &order, name: "schedule", err: tt.err})
			scheduler.SetCallerSendWakeReconciler(&orderedWakeRepair{order: &order, name: "retained"})
			scheduler.runOnce(context.Background())
			require.Equal(t, []string{"schedule", "retained", "integrations"}, order, "retained owner repair must be independent of integration membership and prior schedule repair success")
		})
	}
}
