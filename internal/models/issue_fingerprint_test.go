package models

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestIssueFingerprint(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		source     IssueSource
		externalID string
		expected   string
	}{
		{
			name:       "linear issue id uses source-prefixed hash",
			source:     IssueSourceLinear,
			externalID: "2563b72a-e241-44db-85a3-4267084bb274",
			expected:   "linear:2072004d71b40dd3c2eac1cdfa1c7290",
		},
		{
			name:       "sentry issue id remains source scoped",
			source:     IssueSourceSentry,
			externalID: "12345",
			expected:   "sentry:97d01c7db052953ab2eed34a407e8545",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := IssueFingerprint(tt.source, tt.externalID)
			require.Equal(t, tt.expected, got, "IssueFingerprint should match the canonical ingestion fingerprint")
		})
	}
}
