package worker

import (
	"testing"

	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/codereview"
	"github.com/stretchr/testify/require"
)

func TestCodeReviewInlineComments(t *testing.T) {
	t.Parallel()

	path := "internal/api/router.go"
	emptyPath := ""
	line := 42
	zeroLine := 0

	tests := []struct {
		name     string
		findings []models.CodeReviewFinding
		expected []codereview.SubmitReviewComment
	}{
		{
			name: "returns selected file-backed findings",
			findings: []models.CodeReviewFinding{
				{
					Path:      &path,
					StartLine: &line,
					Summary:   "summary",
					Body:      "body",
				},
			},
			expected: []codereview.SubmitReviewComment{
				{Path: path, Line: line, Body: "body"},
			},
		},
		{
			name: "falls back to summary when body is empty",
			findings: []models.CodeReviewFinding{
				{
					Path:      &path,
					StartLine: &line,
					Summary:   "summary",
				},
			},
			expected: []codereview.SubmitReviewComment{
				{Path: path, Line: line, Body: "summary"},
			},
		},
		{
			name: "skips findings without GitHub comment coordinates",
			findings: []models.CodeReviewFinding{
				{Path: nil, StartLine: &line, Summary: "summary"},
				{Path: &emptyPath, StartLine: &line, Summary: "summary"},
				{Path: &path, StartLine: nil, Summary: "summary"},
				{Path: &path, StartLine: &zeroLine, Summary: "summary"},
				{Path: &path, StartLine: &line},
			},
			expected: []codereview.SubmitReviewComment{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			actual := codeReviewInlineComments(tt.findings)
			require.Equal(t, tt.expected, actual, "codeReviewInlineComments should return deterministic GitHub comments")
		})
	}
}
