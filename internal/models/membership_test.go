package models

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestIsValidRole(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		role  MembershipRole
		valid bool
	}{
		{name: "admin", role: RoleAdmin, valid: true},
		{name: "member", role: RoleMember, valid: true},
		{name: "builder", role: RoleBuilder, valid: true},
		{name: "viewer", role: RoleViewer, valid: true},
		{name: "empty", role: "", valid: false},
		{name: "unknown", role: MembershipRole("superadmin"), valid: false},
		{name: "case-sensitive", role: MembershipRole("Admin"), valid: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.valid, IsValidRole(tt.role), "IsValidRole should match the known membership role set")
		})
	}
}

func TestValidRoles_OrderedByPrivilege(t *testing.T) {
	t.Parallel()
	require.Equal(t, []MembershipRole{RoleAdmin, RoleMember, RoleBuilder, RoleViewer}, ValidRoles, "ValidRoles should remain ordered by privilege")
}

func TestMembershipRoleValidate(t *testing.T) {
	t.Parallel()

	require.NoError(t, RoleBuilder.Validate(), "Validate should accept known roles")
	require.Error(t, MembershipRole("owner").Validate(), "Validate should reject unknown roles")
}
