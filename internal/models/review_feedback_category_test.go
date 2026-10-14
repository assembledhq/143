package models

import (
	"github.com/stretchr/testify/require"
	"testing"
)

func TestReviewFeedbackCategoryValidate(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		category ReviewFeedbackCategory
		valid    bool
	}{
		{ReviewFeedbackStyle, true}, {ReviewFeedbackLogicBug, true}, {ReviewFeedbackEdgeCase, true}, {ReviewFeedbackWrongApproach, true},
		{ReviewFeedbackMissingTest, true}, {ReviewFeedbackSecurity, true}, {ReviewFeedbackPerformance, true}, {ReviewFeedbackNit, true}, {"", false}, {"praise", false},
	} {
		t.Run(string(tt.category), func(t *testing.T) {
			t.Parallel()
			err := tt.category.Validate()
			if tt.valid {
				require.NoError(t, err, "database categories must validate")
			} else {
				require.Error(t, err, "unknown categories must be rejected")
			}
		})
	}
}
