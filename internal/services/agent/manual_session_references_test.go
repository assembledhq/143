package agent

import (
	"testing"

	"github.com/assembledhq/143/internal/models"
	"github.com/stretchr/testify/require"
)

func TestManualSessionReferences(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		issue *models.Issue
		want  []models.SessionInputReference
	}{
		{
			name:  "nil issue",
			issue: nil,
			want:  nil,
		},
		{
			name: "non manual issue",
			issue: &models.Issue{
				Source: models.IssueSourceLinear,
				RawData: []byte(`{
					"references":[{"kind":"file","path":"internal/api/handlers/sessions.go","display":"internal/api/handlers/sessions.go"}]
				}`),
			},
			want: nil,
		},
		{
			name: "invalid json",
			issue: &models.Issue{
				Source:  models.IssueSourceManual,
				RawData: []byte("{"),
			},
			want: nil,
		},
		{
			name: "filters invalid references",
			issue: &models.Issue{
				Source: models.IssueSourceManual,
				RawData: []byte(`{
					"references":[
						{"kind":"file","path":"internal/api/handlers/sessions.go","display":"internal/api/handlers/sessions.go"},
						{"kind":"directory","display":"frontend/src"},
						{"kind":"plugin","id":"github","display":"GitHub"}
					]
				}`),
			},
			want: []models.SessionInputReference{
				{
					Kind:    models.SessionInputReferenceKindFile,
					Path:    "internal/api/handlers/sessions.go",
					Display: "internal/api/handlers/sessions.go",
				},
				{
					Kind:    models.SessionInputReferenceKindPlugin,
					ID:      "github",
					Display: "GitHub",
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, manualSessionReferences(tt.issue), "manualSessionReferences should return the expected canonical references")
		})
	}
}
