package preview

import (
	"testing"

	"github.com/assembledhq/143/internal/models"
	"github.com/stretchr/testify/require"
)

func TestDefaultPreviewRestartClassifier_Classify(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		paths    []string
		expected []models.PreviewRestartReason
	}{
		{
			name:  "dependency files require restart",
			paths: []string{"frontend/package.json", "frontend/package-lock.json"},
			expected: []models.PreviewRestartReason{
				{Kind: models.PreviewRestartReasonDependencyChanged, Path: "frontend/package.json", Detail: "Dependencies changed. Restart to install and apply them."},
				{Kind: models.PreviewRestartReasonDependencyChanged, Path: "frontend/package-lock.json", Detail: "Dependencies changed. Restart to install and apply them."},
			},
		},
		{
			name:  "preview config requires restart",
			paths: []string{".143/config.json"},
			expected: []models.PreviewRestartReason{
				{Kind: models.PreviewRestartReasonPreviewConfigChanged, Path: ".143/config.json", Detail: "Preview configuration changed. Restart to relaunch with the new config."},
			},
		},
		{
			name:  "build config requires restart",
			paths: []string{"frontend/next.config.mjs", "turbo.json"},
			expected: []models.PreviewRestartReason{
				{Kind: models.PreviewRestartReasonBuildConfigChanged, Path: "frontend/next.config.mjs", Detail: "Build configuration changed. Restart to rebuild the preview server."},
				{Kind: models.PreviewRestartReasonBuildConfigChanged, Path: "turbo.json", Detail: "Build configuration changed. Restart to rebuild the preview server."},
			},
		},
		{
			name:  "environment and schema changes require restart",
			paths: []string{"config/development.json", "migrations/000181_preview_runtime_revision.up.sql"},
			expected: []models.PreviewRestartReason{
				{Kind: models.PreviewRestartReasonEnvironmentConfigChanged, Path: "config/development.json", Detail: "Environment configuration changed. Restart to reload runtime settings."},
				{Kind: models.PreviewRestartReasonDatabaseSchemaChanged, Path: "migrations/000181_preview_runtime_revision.up.sql", Detail: "Database schema changed. Restart to rerun startup and migrations."},
			},
		},
		{
			name:     "ordinary source files do not require restart",
			paths:    []string{"frontend/src/components/preview/preview-panel.tsx", "internal/api/handlers/preview.go"},
			expected: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			classifier := DefaultPreviewRestartClassifier{}
			actual := classifier.Classify(tt.paths)
			require.Equal(t, tt.expected, actual, "classifier should return expected restart reasons")
		})
	}
}
