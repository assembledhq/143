package models

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestIssueSourceValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		value   IssueSource
		wantErr bool
	}{
		{name: "valid sentry", value: IssueSourceSentry},
		{name: "valid linear", value: IssueSourceLinear},
		{name: "valid manual", value: IssueSourceManual},
		{name: "valid pm_agent", value: IssueSourcePMAgent},
		{name: "invalid empty", value: IssueSource(""), wantErr: true},
		{name: "invalid unknown", value: IssueSource("jira"), wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.value.Validate()
			if tt.wantErr {
				require.Error(t, err, "Validate should return error for invalid issue source")
				return
			}
			require.NoError(t, err, "Validate should succeed for valid issue source")
		})
	}
}
