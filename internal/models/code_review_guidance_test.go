package models

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCodeReviewRepositoryGuidanceValidate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name     string
		guidance CodeReviewRepositoryGuidance
		wantErr  bool
	}{
		{"absent", CodeReviewRepositoryGuidance{Branch: "master"}, false},
		{"empty", CodeReviewRepositoryGuidance{Branch: "main", Present: true, BlobSHA: strings.Repeat("a", 40)}, false},
		{"maximum size", CodeReviewRepositoryGuidance{Branch: "main", Present: true, BlobSHA: strings.Repeat("a", 40), Content: strings.Repeat("a", CodeReviewGuidanceMaxBytes)}, false},
		{"missing branch", CodeReviewRepositoryGuidance{}, true},
		{"absent with content", CodeReviewRepositoryGuidance{Branch: "main", Content: "stale"}, true},
		{"absent with blob", CodeReviewRepositoryGuidance{Branch: "main", BlobSHA: strings.Repeat("a", 40)}, true},
		{"missing blob", CodeReviewRepositoryGuidance{Branch: "main", Present: true}, true},
		{"invalid blob", CodeReviewRepositoryGuidance{Branch: "main", Present: true, BlobSHA: strings.Repeat("z", 40)}, true},
		{"oversized", CodeReviewRepositoryGuidance{Branch: "main", Present: true, BlobSHA: strings.Repeat("a", 40), Content: strings.Repeat("a", CodeReviewGuidanceMaxBytes+1)}, true},
		{"non UTF8", CodeReviewRepositoryGuidance{Branch: "main", Present: true, BlobSHA: strings.Repeat("a", 40), Content: "\xff"}, true},
		{"NUL", CodeReviewRepositoryGuidance{Branch: "main", Present: true, BlobSHA: strings.Repeat("a", 40), Content: "\x00"}, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			err := tt.guidance.Validate()
			if tt.wantErr {
				require.Error(t, err, "invalid guidance must be rejected")
			} else {
				require.NoError(t, err, "complete guidance or explicit absence must be accepted")
			}
		})
	}
}
