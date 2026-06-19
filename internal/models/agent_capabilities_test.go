package models

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAgentCapabilityEnumsValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		validate  func() error
		expectErr bool
	}{
		{name: "capability id", validate: AgentCapabilityRepoContext.Validate},
		{name: "capability id invalid", validate: AgentCapabilityID("unknown").Validate, expectErr: true},
		{name: "access read", validate: AgentCapabilityAccessRead.Validate},
		{name: "access write", validate: AgentCapabilityAccessWrite.Validate},
		{name: "access publish", validate: AgentCapabilityAccessPublish.Validate},
		{name: "access invalid", validate: AgentCapabilityAccessLevel("admin").Validate, expectErr: true},
		{name: "risk low", validate: AgentCapabilityRiskLow.Validate},
		{name: "risk invalid", validate: AgentCapabilityRisk("critical").Validate, expectErr: true},
		{name: "scope repository", validate: AgentCapabilityScopeRepository.Validate},
		{name: "scope invalid", validate: AgentCapabilityScope("global").Validate, expectErr: true},
		{name: "policy session default", validate: AgentCapabilityPolicyTypeSessionDefault.Validate},
		{name: "policy automation", validate: AgentCapabilityPolicyTypeAutomation.Validate},
		{name: "policy invalid", validate: AgentCapabilityPolicyType("repo").Validate, expectErr: true},
		{name: "grant source user approved", validate: AgentCapabilityGrantSourceUserApproved.Validate},
		{name: "grant source invalid", validate: AgentCapabilityGrantSource("implicit").Validate, expectErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.validate()
			if tt.expectErr {
				require.Error(t, err, "invalid enum value should be rejected")
				return
			}
			require.NoError(t, err, "valid enum value should be accepted")
		})
	}
}
