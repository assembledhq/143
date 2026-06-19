package models

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPRReadinessEnumsValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		validate  func() error
		expectErr bool
	}{
		{name: "run status queued", validate: PRReadinessRunStatusQueued.Validate},
		{name: "run status invalid", validate: PRReadinessRunStatus("bogus").Validate, expectErr: true},
		{name: "check status passed", validate: PRReadinessCheckStatusPassed.Validate},
		{name: "check status invalid", validate: PRReadinessCheckStatus("bogus").Validate, expectErr: true},
		{name: "check type risk flags", validate: PRReadinessCheckTypeRiskFlags.Validate},
		{name: "check type invalid", validate: PRReadinessCheckType("bogus").Validate, expectErr: true},
		{name: "enforcement blocking", validate: PRReadinessEnforcementBlocking.Validate},
		{name: "enforcement invalid", validate: PRReadinessEnforcement("bogus").Validate, expectErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.validate()
			if tt.expectErr {
				require.Error(t, err, "invalid readiness enum values should be rejected")
				return
			}
			require.NoError(t, err, "valid readiness enum values should be accepted")
		})
	}
}

func TestDefaultPRReadinessPolicy(t *testing.T) {
	t.Parallel()

	policy := DefaultPRReadinessPolicy()

	require.Equal(t, PRReadinessEnforcementBlocking, policy.EnforcementFor(RoleBuilder, PRReadinessCheckTypeAgentReviewClean), "builder policy should block on agent review by default")
	require.Equal(t, PRReadinessEnforcementBlocking, policy.EnforcementFor(RoleBuilder, PRReadinessCheckTypeFreshness), "builder policy should block stale readiness by default")
	require.Equal(t, PRReadinessEnforcementAdvisory, policy.EnforcementFor(RoleBuilder, PRReadinessCheckTypeTestEvidencePresent), "builder policy should start test evidence as advisory")
	require.Equal(t, PRReadinessEnforcementAdvisory, policy.EnforcementFor(RoleMember, PRReadinessCheckTypeAgentReviewClean), "engineer policy should be advisory by default")
	require.Equal(t, PRReadinessEnforcementOff, policy.EnforcementFor(RoleViewer, PRReadinessCheckTypeAgentReviewClean), "viewer policy should not evaluate PR readiness")
}
