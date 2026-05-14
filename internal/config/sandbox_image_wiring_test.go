package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSandboxImageWiring(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		path     string
		patterns []string
	}{
		{
			name: "docker compose defines a sandbox image build",
			path: "docker-compose.yml",
			patterns: []string{
				"sandbox:",
				"image: 143-sandbox:latest",
				"dockerfile: sandbox/Dockerfile",
			},
		},
		{
			name: "make dev builds the sandbox image",
			path: "Makefile",
			patterns: []string{
				"sandbox-image:",
				"docker compose build sandbox",
				"$(MAKE) sandbox-image",
			},
		},
		{
			name: "ci builds the sandbox image",
			path: ".github/workflows/ci.yml",
			patterns: []string{
				"name: sandbox",
				"name: Build ${{ matrix.name }} Docker image",
				"file: sandbox/Dockerfile",
				"tags: 143-sandbox:latest",
			},
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			content, err := os.ReadFile(repoPath(tt.path))
			require.NoError(t, err, "test should be able to read %s", tt.path)

			for _, pattern := range tt.patterns {
				require.True(
					t,
					strings.Contains(string(content), pattern),
					"%s should contain %q so 143-sandbox is built in standard workflows",
					tt.path,
					pattern,
				)
			}
		})
	}
}

func repoPath(path string) string {
	return filepath.Join("..", "..", path)
}
