package agentcapabilities

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestServiceBuildApprovedGrant(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		capability models.AgentCapabilityID
		access     models.AgentCapabilityAccessLevel
		invalid    bool
	}{
		{name: "valid session history", capability: models.AgentCapabilitySessionHistory, access: models.AgentCapabilityAccessRead},
		{name: "unknown capability", capability: "unknown", access: models.AgentCapabilityAccessRead, invalid: true},
		{name: "invalid access", capability: models.AgentCapabilitySessionHistory, access: "invalid", invalid: true},
		{name: "above catalog maximum", capability: models.AgentCapabilitySessionHistory, access: models.AgentCapabilityAccessWrite, invalid: true},
		{name: "automation actions retain required config validation", capability: models.AgentCapabilityAutomationActions, access: models.AgentCapabilityAccessWrite, invalid: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			service := NewService(nil)
			appender, requests := &recordingApprovedGrantAppender{}, &recordingRequestWriter{}
			service.SetApprovedGrantAppender(appender)
			service.SetHumanInputRequestWriter(requests)
			input := ApprovedGrantInput{OrgID: uuid.New(), SessionID: uuid.New(), HumanInputRequestID: uuid.New(), Capability: tt.capability, AccessLevel: tt.access}
			before := time.Now().UTC()
			item, err := service.BuildApprovedGrant(input)
			if tt.invalid {
				require.ErrorIs(t, err, ErrInvalidGrant, "construction must share all existing catalog and capability-config validation")
				require.Equal(t, models.AgentCapabilitySnapshotItem{}, item, "invalid grants must not return a partially accepted item")
			} else {
				require.NoError(t, err, "construction must not require a persistence dependency")
				require.False(t, item.GrantedAt.Before(before), "construction must timestamp the grant at this approval, not reuse stale policy time")
				require.False(t, item.GrantedAt.After(time.Now().UTC()), "construction cannot invent a future grant timestamp")
				require.Equal(t, time.UTC, item.GrantedAt.Location(), "the original grant timestamp must remain UTC")
				expected := models.AgentCapabilitySnapshotItem{ID: models.AgentCapabilitySessionHistory, DisplayName: "Session history", AccessLevel: models.AgentCapabilityAccessRead, Risk: models.AgentCapabilityRiskMedium, Scope: models.AgentCapabilityScopeRepository, Config: json.RawMessage(`{}`), Source: models.AgentCapabilityGrantSourceUserApproved, GrantedAt: item.GrantedAt, HumanInputRequestID: &input.HumanInputRequestID}
				require.Equal(t, expected, item, "construction must preserve every original display, risk, scope, config, source and request-binding field")
			}
			require.Equal(t, &recordingApprovedGrantAppender{}, appender, "pure construction must never append even when the legacy appender is configured")
			require.Equal(t, &recordingRequestWriter{}, requests, "pure construction must never create an approval request")
		})
	}
}

func TestServiceApplyApprovedGrantValidationBoundary(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		configured bool
	}{
		{name: "missing appender retains configuration error first"},
		{name: "configured appender is not called for invalid grant", configured: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			service, appender := NewService(nil), &recordingApprovedGrantAppender{}
			if tt.configured {
				service.SetApprovedGrantAppender(appender)
			}
			snapshot, err := service.ApplyApprovedGrant(context.Background(), ApprovedGrantInput{Capability: "unknown", AccessLevel: models.AgentCapabilityAccessRead})
			if tt.configured {
				require.ErrorIs(t, err, ErrInvalidGrant, "legacy standalone approval must share the same construction validation")
			} else {
				require.ErrorContains(t, err, "store is not configured", "legacy standalone approval must preserve its pre-validation dependency error")
			}
			require.Nil(t, snapshot, "rejected standalone approval cannot return an accepted snapshot")
			require.Equal(t, &recordingApprovedGrantAppender{}, appender, "invalid standalone approval must have no persistence side effect")
		})
	}
}
