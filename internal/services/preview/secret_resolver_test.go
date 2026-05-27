package preview

import (
	"encoding/json"
	"testing"

	"github.com/assembledhq/143/internal/models"
	"github.com/stretchr/testify/require"
)

func TestRenderPreviewSecretOutputs(t *testing.T) {
	t.Parallel()

	source := models.PreviewSecretBundleSource{
		Type: "managed",
		Values: map[string]string{
			"database_url": "postgres://user:pass@db/app",
			"queue_type":   "local",
		},
	}
	jsonContent := json.RawMessage(`{"database":{"url":"secret:database_url"},"msgbroker":{"queue_type":"secret:queue_type"},"literal":"ok"}`)

	env, files, err := renderPreviewSecretOutputs(
		models.PreviewSecretBundleRef{
			Bundle:   "assembled-dev",
			Services: []string{"webserver"},
			Env:      []string{"DATABASE_URL"},
			Files:    []string{"development.conf.json"},
		},
		source,
		[]models.PreviewSecretBundleOutput{
			{Type: "env", Values: map[string]string{"DATABASE_URL": "secret:database_url"}},
			{Type: "file", Path: "development.conf.json", Format: "json", Content: jsonContent},
		},
		[]string{"webserver"},
	)

	require.NoError(t, err, "renderPreviewSecretOutputs should resolve managed env and file outputs")
	require.Equal(t, map[string]string{"DATABASE_URL": "postgres://user:pass@db/app"}, env, "renderPreviewSecretOutputs should return resolved env values")
	require.Len(t, files, 1, "renderPreviewSecretOutputs should return one generated file")
	require.Equal(t, "development.conf.json", files[0].Path, "renderPreviewSecretOutputs should preserve the requested file path")
	require.JSONEq(t, `{"database":{"url":"postgres://user:pass@db/app"},"msgbroker":{"queue_type":"local"},"literal":"ok"}`, string(files[0].Content), "renderPreviewSecretOutputs should render JSON with secret references resolved")
}

func TestRenderPreviewSecretOutputsRejectsMissingHints(t *testing.T) {
	t.Parallel()

	_, _, err := renderPreviewSecretOutputs(
		models.PreviewSecretBundleRef{Bundle: "repo-dev", Services: []string{"web"}, Env: []string{"DATABASE_URL"}},
		models.PreviewSecretBundleSource{Type: "managed", Values: map[string]string{"database_url": "postgres://"}},
		[]models.PreviewSecretBundleOutput{{Type: "env", Values: map[string]string{"OTHER_URL": "secret:database_url"}}},
		[]string{"web"},
	)

	require.Error(t, err, "renderPreviewSecretOutputs should reject bundles that do not satisfy repo hints")
	require.Contains(t, err.Error(), `required env "DATABASE_URL"`, "renderPreviewSecretOutputs should identify the missing env hint")
}

func TestRenderPreviewSecretOutputsRejectsServiceScopedFileOutputs(t *testing.T) {
	t.Parallel()

	_, _, err := renderPreviewSecretOutputs(
		models.PreviewSecretBundleRef{Bundle: "repo-dev", Services: []string{"web"}, Files: []string{"development.conf.json"}},
		models.PreviewSecretBundleSource{Type: "managed", Values: map[string]string{"database_url": "postgres://"}},
		[]models.PreviewSecretBundleOutput{{Type: "file", Path: "development.conf.json", Format: "json", Content: json.RawMessage(`{"database_url":"secret:database_url"}`)}},
		[]string{"web", "frontend"},
	)

	require.Error(t, err, "renderPreviewSecretOutputs should reject file outputs that are scoped to only some services")
	require.Contains(t, err.Error(), "file outputs are workspace-wide", "error should explain why all services must be included")
}
