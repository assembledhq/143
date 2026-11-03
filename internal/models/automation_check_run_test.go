package models

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestAutomationGitHubEventFiltersCheckRunValidation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		filter    AutomationGitHubEventFilters
		wantError bool
	}{
		{name: "legacy", filter: AutomationGitHubEventFilters{}},
		{name: "bound", filter: AutomationGitHubEventFilters{Labels: []string{"ready"}, CheckRun: &AutomationGitHubCheckRunFilter{Name: "policy", AppID: 123}}},
		{name: "empty name", wantError: true, filter: AutomationGitHubEventFilters{Labels: []string{"ready"}, CheckRun: &AutomationGitHubCheckRunFilter{AppID: 123}}},
		{name: "space name", wantError: true, filter: AutomationGitHubEventFilters{Labels: []string{"ready"}, CheckRun: &AutomationGitHubCheckRunFilter{Name: " policy", AppID: 123}}},
		{name: "missing app", wantError: true, filter: AutomationGitHubEventFilters{Labels: []string{"ready"}, CheckRun: &AutomationGitHubCheckRunFilter{Name: "policy"}}},
		{name: "missing labels", wantError: true, filter: AutomationGitHubEventFilters{CheckRun: &AutomationGitHubCheckRunFilter{Name: "policy", AppID: 123}}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			if tt.wantError {
				require.Error(t, tt.filter.Validate(), "invalid check binding must be rejected")
			} else {
				require.NoError(t, tt.filter.Validate(), "valid or legacy filters should be accepted")
			}
		})
	}
}
