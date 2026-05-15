package models

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestInvitationAcceptanceMethodValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		method    InvitationAcceptanceMethod
		expectErr bool
	}{
		{name: "empty method is accepted for legacy zero values", method: "", expectErr: false},
		{name: "either method is valid", method: InvitationAcceptanceMethodEither, expectErr: false},
		{name: "email method is valid", method: InvitationAcceptanceMethodEmail, expectErr: false},
		{name: "github method is valid", method: InvitationAcceptanceMethodGitHub, expectErr: false},
		{name: "unknown method is invalid", method: InvitationAcceptanceMethod("sms"), expectErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.method.Validate()
			if tt.expectErr {
				require.Error(t, err, "Validate should reject unknown invitation acceptance methods")
				return
			}
			require.NoError(t, err, "Validate should accept known invitation acceptance methods")
		})
	}
}
