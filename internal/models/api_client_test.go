package models

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAPIClientStatusValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		status    APIClientStatus
		expectErr bool
	}{
		{name: "enabled", status: APIClientStatusEnabled},
		{name: "disabled", status: APIClientStatusDisabled},
		{name: "empty", status: "", expectErr: true},
		{name: "unknown", status: "paused", expectErr: true},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.status.Validate()
			if tt.expectErr {
				require.Error(t, err, "Validate should reject unknown API client statuses")
				return
			}
			require.NoError(t, err, "Validate should accept known API client statuses")
		})
	}
}

func TestAPITokenScopeValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		scope     APITokenScope
		expectErr bool
	}{
		{name: "sessions read", scope: APITokenScopeSessionsRead},
		{name: "sessions create", scope: APITokenScopeSessionsCreate},
		{name: "sessions write", scope: APITokenScopeSessionsWrite},
		{name: "sessions cancel", scope: APITokenScopeSessionsCancel},
		{name: "sessions publish", scope: APITokenScopeSessionsPublish},
		{name: "sessions all", scope: APITokenScopeSessionsAll},
		{name: "automations read", scope: APITokenScopeAutomationsRead},
		{name: "automations create", scope: APITokenScopeAutomationsCreate},
		{name: "automations write", scope: APITokenScopeAutomationsWrite},
		{name: "automations run", scope: APITokenScopeAutomationsRun},
		{name: "automations all", scope: APITokenScopeAutomationsAll},
		{name: "previews read", scope: APITokenScopePreviewsRead},
		{name: "previews create", scope: APITokenScopePreviewsCreate},
		{name: "previews stop", scope: APITokenScopePreviewsStop},
		{name: "previews all", scope: APITokenScopePreviewsAll},
		{name: "wildcard rejected", scope: "sessions:*", expectErr: true},
		{name: "global all rejected", scope: "api:all", expectErr: true},
		{name: "empty rejected", scope: "", expectErr: true},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.scope.Validate()
			if tt.expectErr {
				require.Error(t, err, "Validate should reject unsupported API token scopes")
				return
			}
			require.NoError(t, err, "Validate should accept supported API token scopes")
		})
	}
}

func TestValidateAPITokenScopes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		scopes    []string
		expectErr bool
	}{
		{name: "valid scopes", scopes: []string{"sessions:create", "sessions:read"}},
		{name: "valid family scope", scopes: []string{"sessions:all"}},
		{name: "empty list", scopes: []string{}, expectErr: true},
		{name: "invalid scope", scopes: []string{"sessions:create", "sessions:*"}, expectErr: true},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := ValidateAPITokenScopes(tt.scopes)
			if tt.expectErr {
				require.Error(t, err, "ValidateAPITokenScopes should reject invalid scope sets")
				return
			}
			require.NoError(t, err, "ValidateAPITokenScopes should accept valid scope sets")
		})
	}
}
