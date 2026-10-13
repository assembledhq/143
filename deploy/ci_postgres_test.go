package deploy_test

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestCIPostgresStartsAfterRegistryFallback(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile("../.github/workflows/ci.yml")
	require.NoError(t, err, "read CI database consumers")
	var workflow struct {
		Jobs map[string]struct {
			Services map[string]any
			Steps    []struct {
				ID, Name, Run, If, Uses string
				Env                     map[string]string
			}
		}
	}
	require.NoError(t, yaml.Unmarshal(raw, &workflow), "CI workflow must be valid YAML")
	for _, jobName := range []string{"backend-test", "migration-check"} {
		t.Run(jobName, func(t *testing.T) {
			t.Parallel()
			job := workflow.Jobs[jobName]
			require.Empty(t, job.Services, "database pulls must run after checkout so registry fallback can execute")
			checkout, start, wait, cleanup := -1, -1, -1, -1
			for i, step := range job.Steps {
				if step.Uses == "actions/checkout@v6" {
					checkout = i
				}
				if step.ID == "postgres" {
					start = i
					require.Equal(t, "bash .github/scripts/start-ci-postgres.sh", step.Run, "both jobs must use the tested registry recovery startup")
				}
				if step.Name == "Wait for Postgres" {
					wait = i
					require.Equal(t, "go run ./cmd/wait-postgres", step.Run, "database consumers must wait for a successful connection")
				}
				if step.Name == "Stop CI Postgres" {
					cleanup = i
					require.Equal(t, "always() && steps.postgres.outputs.container_id != ''", step.If, "cleanup must run even after failure and only with a retained container")
					require.Equal(t, "${{ steps.postgres.outputs.container_id }}", step.Env["POSTGRES_CONTAINER"], "cleanup must remove the exact started container")
					require.Equal(t, `docker rm --force --volumes "$POSTGRES_CONTAINER"`, step.Run, "cleanup must remove disposable image-declared storage too")
				}
			}
			require.GreaterOrEqual(t, checkout, 0, "jobs must check out the startup helper before running it")
			require.Greater(t, start, checkout, "checkout must precede database startup")
			require.Greater(t, wait, start, "readiness must follow database startup")
			require.Greater(t, cleanup, wait, "cleanup must follow database use")
		})
	}
}
