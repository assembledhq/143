package integration

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPagerDutyIncidentProviderRejectsWritesWhenWritebackDisabled(t *testing.T) {
	t.Parallel()

	provider := NewPagerDutyIncidentProvider(PagerDutyProviderConfig{
		AccessToken:      "token",
		WritebackEnabled: false,
	})

	_, err := provider.AddIncidentNote(context.Background(), "PINCIDENT", "investigating")
	require.ErrorContains(t, err, "PagerDuty writeback is disabled", "AddIncidentNote should reject writes unless writeback is enabled")

	err = provider.CreateIncidentStatusUpdate(context.Background(), "PINCIDENT", "investigating")
	require.ErrorContains(t, err, "PagerDuty writeback is disabled", "CreateIncidentStatusUpdate should reject writes unless writeback is enabled")
}
