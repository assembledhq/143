package handlers

import (
	"context"
	"testing"

	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/agentcapabilities"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestSessionCapabilityApproverBuildsWithoutAppender(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		capability models.AgentCapabilityID
		invalid    bool
	}{
		{name: "valid construction needs no independent appender", capability: models.AgentCapabilitySessionHistory},
		{name: "invalid construction retains validation error", capability: "unknown", invalid: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			adapter := sessionCapabilityApprover{svc: agentcapabilities.NewService(nil)}
			requestID := uuid.New()
			item, err := adapter.BuildApprovedGrant(context.Background(), uuid.New(), uuid.New(), requestID, tt.capability, models.AgentCapabilityAccessRead)
			if tt.invalid {
				require.ErrorIs(t, err, agentcapabilities.ErrInvalidGrant, "handler wiring must retain catalog validation without independently persisting")
				require.Equal(t, models.AgentCapabilitySnapshotItem{}, item, "invalid adapter construction cannot return an accepted item")
				return
			}
			require.NoError(t, err, "the adapter must only construct, not invoke the unconfigured legacy appender")
			expected := models.AgentCapabilitySnapshotItem{ID: models.AgentCapabilitySessionHistory, DisplayName: "Session history", AccessLevel: models.AgentCapabilityAccessRead, Risk: models.AgentCapabilityRiskMedium, Scope: models.AgentCapabilityScopeRepository, Config: []byte(`{}`), Source: models.AgentCapabilityGrantSourceUserApproved, GrantedAt: item.GrantedAt, HumanInputRequestID: &requestID}
			require.Equal(t, expected, item, "adapter construction must preserve every validated grant field and exact request identity")
		})
	}
}
