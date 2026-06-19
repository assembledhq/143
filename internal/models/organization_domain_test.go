package models

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestOrgDomainStatusValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		status    OrgDomainStatus
		expectErr bool
	}{
		{name: "pending is valid", status: OrgDomainStatusPending, expectErr: false},
		{name: "verified is valid", status: OrgDomainStatusVerified, expectErr: false},
		{name: "empty status is invalid", status: "", expectErr: true},
		{name: "unknown status is invalid", status: OrgDomainStatus("failed"), expectErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.status.Validate()
			if tt.expectErr {
				require.Error(t, err, "Validate should reject unknown org domain statuses")
				return
			}
			require.NoError(t, err, "Validate should accept known org domain statuses")
		})
	}
}
