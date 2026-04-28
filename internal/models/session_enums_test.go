package models

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSessionOrigin_Validate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		value     SessionOrigin
		expectErr bool
	}{
		{name: "issue trigger", value: SessionOriginIssueTrigger},
		{name: "manual", value: SessionOriginManual},
		{name: "project", value: SessionOriginProject},
		{name: "automation", value: SessionOriginAutomation},
		{name: "revision", value: SessionOriginRevision},
		{name: "invalid", value: SessionOrigin("bogus"), expectErr: true},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.value.Validate()
			if tt.expectErr {
				require.Error(t, err, "Validate should reject unknown session origins")
				return
			}
			require.NoError(t, err, "Validate should accept known session origins")
		})
	}
}

func TestSessionInteractionMode_Validate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		value     SessionInteractionMode
		expectErr bool
	}{
		{name: "interactive", value: SessionInteractionModeInteractive},
		{name: "single run", value: SessionInteractionModeSingleRun},
		{name: "invalid", value: SessionInteractionMode("bogus"), expectErr: true},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.value.Validate()
			if tt.expectErr {
				require.Error(t, err, "Validate should reject unknown interaction modes")
				return
			}
			require.NoError(t, err, "Validate should accept known interaction modes")
		})
	}
}

func TestSessionValidationPolicy_Validate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		value     SessionValidationPolicy
		expectErr bool
	}{
		{name: "turn complete", value: SessionValidationPolicyOnTurnComplete},
		{name: "session end", value: SessionValidationPolicyOnSessionEnd},
		{name: "skip", value: SessionValidationPolicySkip},
		{name: "invalid", value: SessionValidationPolicy("bogus"), expectErr: true},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.value.Validate()
			if tt.expectErr {
				require.Error(t, err, "Validate should reject unknown validation policies")
				return
			}
			require.NoError(t, err, "Validate should accept known validation policies")
		})
	}
}

func TestLinearPrepareState_Validate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		value     LinearPrepareState
		expectErr bool
	}{
		{name: "none", value: LinearPrepareStateNone},
		{name: "pending", value: LinearPrepareStatePending},
		{name: "ready", value: LinearPrepareStateReady},
		{name: "failed", value: LinearPrepareStateFailed},
		{name: "invalid", value: LinearPrepareState("bogus"), expectErr: true},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.value.Validate()
			if tt.expectErr {
				require.Error(t, err, "Validate should reject unknown Linear prepare states")
				return
			}
			require.NoError(t, err, "Validate should accept known Linear prepare states")
		})
	}
}

func TestSessionIssueLinkRole_Validate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		value     SessionIssueLinkRole
		expectErr bool
	}{
		{name: "primary", value: SessionIssueLinkRolePrimary},
		{name: "related", value: SessionIssueLinkRoleRelated},
		{name: "invalid", value: SessionIssueLinkRole("bogus"), expectErr: true},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.value.Validate()
			if tt.expectErr {
				require.Error(t, err, "Validate should reject unknown session issue link roles")
				return
			}
			require.NoError(t, err, "Validate should accept known session issue link roles")
		})
	}
}
