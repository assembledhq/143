package handlers

import (
	"testing"

	"github.com/assembledhq/143/internal/models"
	"github.com/stretchr/testify/require"
)

func TestValidCodeReviewDisputeAdjudicationUpdate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		status   models.CodeReviewDisputeAdjudicationStatus
		expected bool
	}{
		{name: "upheld", status: models.CodeReviewDisputeAdjudicationUpheld, expected: true},
		{name: "rejected", status: models.CodeReviewDisputeAdjudicationRejected, expected: true},
		{name: "needs context", status: models.CodeReviewDisputeAdjudicationNeedsContext, expected: true},
		{name: "pending is list-only", status: models.CodeReviewDisputeAdjudicationPending, expected: false},
		{name: "expired is lifecycle-only", status: models.CodeReviewDisputeAdjudicationExpired, expected: false},
		{name: "unknown", status: models.CodeReviewDisputeAdjudicationStatus("unknown"), expected: false},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.expected, validCodeReviewDisputeAdjudicationUpdate(tt.status), "only terminal admin decisions should be accepted by the PATCH contract")
		})
	}
}
