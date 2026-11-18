package main

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestLifecycleMigrationBuildWiring(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, path, selector, expected string
	}{
		{
			name:     "packaged image binary",
			path:     "../../Dockerfile",
			selector: "-o /bin/migrate ",
			expected: `CGO_ENABLED=0 go build -ldflags "-X github.com/assembledhq/143/internal/version.BuildSHA=${BUILD_SHA}" -o /bin/migrate ./cmd/migrate`,
		},
		{
			name:     "local packaged binary",
			path:     "../../Makefile",
			selector: "-o bin/migrate ",
			expected: `go build -ldflags "$(LDFLAGS)" -o bin/migrate ./cmd/migrate`,
		},
		{
			name:     "single file up entrypoint",
			path:     "../../Makefile",
			selector: "go run cmd/migrate/main.go up",
			expected: "go run cmd/migrate/main.go up",
		},
		{
			name:     "single file down entrypoint",
			path:     "../../Makefile",
			selector: "go run cmd/migrate/main.go down",
			expected: "go run cmd/migrate/main.go down",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			contents, err := os.ReadFile(tt.path)
			require.NoError(t, err, "inspect the actual migration build or run entrypoint")
			var actual []string
			for line := range strings.SplitSeq(string(contents), "\n") {
				if strings.Contains(line, tt.selector) {
					actual = append(actual, strings.TrimSpace(strings.TrimSuffix(strings.TrimSpace(line), `&& \`)))
				}
			}
			require.Equal(t, []string{tt.expected}, actual, "the migration recipe must embed the existing build identity while preserving its command target")
		})
	}
}
