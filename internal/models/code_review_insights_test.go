package models

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCodeReviewInsightInputChangeValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		value     CodeReviewInsightInputChange
		expectErr bool
	}{
		{name: "changed", value: CodeReviewInsightInputChangeChanged},
		{name: "unchanged", value: CodeReviewInsightInputChangeUnchanged},
		{name: "unknown", value: CodeReviewInsightInputChangeUnknown},
		{name: "invalid", value: CodeReviewInsightInputChange("different"), expectErr: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.value.Validate()

			if tt.expectErr {
				require.Error(t, err, "an unknown input-change bucket should be rejected")
				return
			}
			require.NoError(t, err, "a declared input-change bucket should validate")
		})
	}
}
