package models

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestIsValidRole(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		role  Role
		valid bool
	}{
		{name: "admin", role: RoleAdmin, valid: true},
		{name: "builder", role: RoleBuilder, valid: true},
		{name: "member", role: RoleMember, valid: true},
		{name: "viewer", role: RoleViewer, valid: true},
		{name: "empty", role: "", valid: false},
		{name: "unknown", role: "superadmin", valid: false},
		{name: "case-sensitive", role: "Admin", valid: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.valid, IsValidRole(string(tt.role)))
		})
	}
}

func TestValidRoles_OrderedByPrivilege(t *testing.T) {
	t.Parallel()
	require.Equal(t, []Role{RoleAdmin, RoleMember, RoleBuilder, RoleViewer}, ValidRoles)
}
