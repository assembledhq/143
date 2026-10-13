package models

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestCodeReviewEvidenceCategoryValidate(t *testing.T) {
	t.Parallel()
	tests := []struct {
		category CodeReviewEvidenceCategory
		expected string
	}{
		{category: CodeReviewEvidenceGit},
		{category: CodeReviewEvidenceJavaScriptDependencies},
		{category: CodeReviewEvidenceGoDependencies},
		{category: CodeReviewEvidencePythonDependencies},
		{category: CodeReviewEvidenceRubyDependencies},
		{category: CodeReviewEvidenceGeneratedArtifacts},
		{category: CodeReviewEvidenceSubmodules},
		{category: CodeReviewEvidenceLFS},
		{category: CodeReviewEvidenceExternalCaches},
		{category: "", expected: `invalid review evidence category ""`},
		{category: "runtime", expected: `invalid review evidence category "runtime"`},
	}
	for _, tt := range tests {
		t.Run(string(tt.category), func(t *testing.T) {
			t.Parallel()
			err := tt.category.Validate()
			if tt.expected != "" {
				require.EqualError(t, err, tt.expected, "unsupported audit evidence categories should fail validation")
				return
			}
			require.NoError(t, err, "named audit evidence categories should validate")
		})
	}
}
