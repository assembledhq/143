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

func TestDefaultPreviewRestartClassifier_SelectUpdateMode(t *testing.T) {
	t.Parallel()

	revision := int64(1)
	tests := []struct {
		name          string
		status        models.PreviewStatus
		freshness     *models.PreviewFreshness
		reloadBrowser bool
		hmrCapable    bool
		expected      models.PreviewUpdateMode
	}{
		{
			name:          "current preview reloads browser when requested",
			status:        models.PreviewStatusReady,
			freshness:     &models.PreviewFreshness{State: models.PreviewFreshnessCurrent, PreviewWorkspaceRevision: &revision},
			reloadBrowser: true,
			expected:      models.PreviewUpdateModeBrowserReload,
		},
		{
			name:      "current preview noops without browser reload",
			status:    models.PreviewStatusReady,
			freshness: &models.PreviewFreshness{State: models.PreviewFreshnessCurrent, PreviewWorkspaceRevision: &revision},
			expected:  models.PreviewUpdateModeNoopCurrent,
		},
		{
			name:      "restart reasons force full recycle",
			status:    models.PreviewStatusReady,
			freshness: &models.PreviewFreshness{State: models.PreviewFreshnessRestartRequired, RestartRequired: true},
			expected:  models.PreviewUpdateModeFullRecycle,
		},
		{
			name:      "ordinary out of date preview uses soft restart contract",
			status:    models.PreviewStatusReady,
			freshness: &models.PreviewFreshness{State: models.PreviewFreshnessOutOfDate},
			expected:  models.PreviewUpdateModeSoftServiceRestart,
		},
		{
			name:       "hmr-capable out of date preview reloads browser",
			status:     models.PreviewStatusReady,
			freshness:  &models.PreviewFreshness{State: models.PreviewFreshnessOutOfDate},
			hmrCapable: true,
			expected:   models.PreviewUpdateModeBrowserReload,
		},
		{
			name:       "hmr capability does not override restart reasons",
			status:     models.PreviewStatusReady,
			freshness:  &models.PreviewFreshness{State: models.PreviewFreshnessRestartRequired, RestartRequired: true},
			hmrCapable: true,
			expected:   models.PreviewUpdateModeFullRecycle,
		},
		{
			name:       "hmr capability does not rescue a terminal preview",
			status:     models.PreviewStatusFailed,
			freshness:  &models.PreviewFreshness{State: models.PreviewFreshnessOutOfDate},
			hmrCapable: true,
			expected:   models.PreviewUpdateModeColdRelaunch,
		},
		{
			name:      "terminal preview cold relaunches",
			status:    models.PreviewStatusFailed,
			freshness: &models.PreviewFreshness{State: models.PreviewFreshnessOutOfDate},
			expected:  models.PreviewUpdateModeColdRelaunch,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			classifier := DefaultPreviewRestartClassifier{}
			actual := classifier.SelectUpdateMode(tt.status, tt.freshness, tt.reloadBrowser, tt.hmrCapable)
			require.Equal(t, tt.expected, actual, "classifier should select the expected update mode")
		})
	}
}
