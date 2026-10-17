package handlers

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestValidateAutomationCheckRunFilter(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		raw       string
		expected  string
		wantError bool
	}{
		{name: "bound source", raw: `{"check_run":{"name":"Review Policy","app_id":123},"labels":[" ready ","READY"]}`, expected: `{"check_run":{"name":"Review Policy","app_id":123},"labels":["ready"]}`},
		{name: "blank normalized labels", raw: `{"check_run":{"name":"policy","app_id":123},"labels":[" "]}`, wantError: true},
		{name: "unbound source", raw: `{"check_run":{"name":"policy"},"labels":["ready"]}`, wantError: true},
		{name: "inexact configured name", raw: `{"check_run":{"name":" policy","app_id":123},"labels":["ready"]}`, wantError: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			got, err := validateAutomationGitHubEventFilters(json.RawMessage(tt.raw))
			if tt.wantError {
				require.Error(t, err, "unsafe check filters must not be saved")
			} else {
				require.NoError(t, err, "bound check filters should be accepted")
				require.JSONEq(t, tt.expected, string(got), "normalization must preserve exact check identity and the label gate")
			}
		})
	}
}
