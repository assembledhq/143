package config

import (
	"os"
	"os/exec"
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

func TestSandboxImageRebuildTracksCLIInputs(t *testing.T) {
	t.Parallel()

	cmd := exec.Command("make", "-pn", "sandbox-image")
	cmd.Dir = repoPath("")
	output, err := cmd.Output()
	require.NoError(t, err, "make should expand the sandbox image dependency graph")

	database := string(output)
	ruleStart := strings.Index(database, "sandbox/.build-stamp:")
	require.NotEqual(t, -1, ruleStart, "make database should include the sandbox image stamp rule")
	ruleEnd := strings.IndexByte(database[ruleStart:], '\n')
	require.NotEqual(t, -1, ruleEnd, "sandbox image stamp rule should end with a newline")
	rule := database[ruleStart : ruleStart+ruleEnd]

	repoRoot, err := filepath.Abs(repoPath(""))
	require.NoError(t, err, "test should resolve the repository root")
	expectedInputs := []string{
		"go.mod",
		"go.sum",
		filepath.Join(repoRoot, "cmd", "tools"),
		filepath.Join(repoRoot, "internal", "cli", "preview_tools.go"),
		filepath.Join(repoRoot, "internal", "internalapi", "env.go"),
	}
	for _, expected := range expectedInputs {
		require.Contains(t, rule, expected, "sandbox image stamp should track every input that can change the baked 143-tools binary")
	}
}

func repoPath(path string) string {
	return filepath.Join("..", "..", path)
}
