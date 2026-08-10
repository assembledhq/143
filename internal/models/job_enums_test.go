package models

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSandboxWorkloadClassValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		class     SandboxWorkloadClass
		expectErr bool
	}{
		{name: "interactive", class: SandboxWorkloadClassInteractive},
		{name: "code review", class: SandboxWorkloadClassCodeReview},
		{name: "empty", class: "", expectErr: true},
		{name: "unknown", class: "batch", expectErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.class.Validate()
			if tt.expectErr {
				require.Error(t, err, "invalid workload class should be rejected")
				return
			}
			require.NoError(t, err, "known workload class should validate")
		})
	}
}

func TestSandboxWorkloadClassForSession(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		session  *Session
		expected SandboxWorkloadClass
	}{
		{name: "nil session", expected: SandboxWorkloadClassInteractive},
		{name: "manual session", session: &Session{Origin: SessionOriginManual}, expected: SandboxWorkloadClassInteractive},
		{name: "code review session", session: &Session{Origin: SessionOriginCodeReview}, expected: SandboxWorkloadClassCodeReview},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.expected, SandboxWorkloadClassForSession(tt.session), "session origin should map to the expected sandbox workload class")
		})
	}
}
