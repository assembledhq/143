package deploy_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestCICommandTestsRunEveryPackage(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile("../.github/workflows/ci.yml")
	require.NoError(t, err, "test must exercise the actual command-test CI gate")
	var workflow struct {
		Jobs map[string]struct {
			Env   map[string]string `yaml:"env"`
			Steps []struct {
				Name           string            `yaml:"name"`
				If             string            `yaml:"if"`
				Run            string            `yaml:"run"`
				Env            map[string]string `yaml:"env"`
				TimeoutMinutes int               `yaml:"timeout-minutes"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	require.NoError(t, yaml.Unmarshal(raw, &workflow), "CI workflow must remain valid YAML")
	job, ok := workflow.Jobs["backend-test"]
	require.True(t, ok, "command tests must run in the backend test job")
	require.NotEmpty(t, job.Env["TEST_DATABASE_URL"], "database-backed command tests must receive a test database")
	require.Equal(t, job.Env["DATABASE_URL"], job.Env["TEST_DATABASE_URL"], "command tests must use the database provisioned for the backend job")
	commandIndex, migrationIndex := -1, -1
	for i, step := range job.Steps {
		if strings.Contains(step.Run, "go run cmd/migrate/main.go up") {
			migrationIndex = i
		}
		if step.Name == "Run command tests" {
			require.Equal(t, -1, commandIndex, "the backend job must have one comprehensive command-test gate")
			commandIndex = i
		}
	}
	require.GreaterOrEqual(t, migrationIndex, 0, "backend tests must migrate the configured database")
	require.Greater(t, commandIndex, migrationIndex, "command tests must run after database migrations")
	step := job.Steps[commandIndex]
	require.Empty(t, step.If, "all backend test invocations must exercise the comprehensive command gate")
	require.Positive(t, step.TimeoutMinutes, "command-test execution must have a step timeout")
	require.LessOrEqual(t, step.TimeoutMinutes, 5, "command tests must retain their bounded CI budget")
	if databaseURL, overridden := step.Env["TEST_DATABASE_URL"]; overridden {
		require.Equal(t, job.Env["TEST_DATABASE_URL"], databaseURL, "the command-test step must preserve its provisioned test database")
	}

	tests := []struct {
		name     string
		event    string
		race     bool
		exitCode int
	}{
		{name: "pull request", event: "pull_request"},
		{name: "push", event: "push", race: true},
		{name: "schedule", event: "schedule", race: true},
		{name: "manual", event: "workflow_dispatch", race: true},
		{name: "pull request failure", event: "pull_request", exitCode: 23},
		{name: "push failure", event: "push", race: true, exitCode: 23},
		{name: "schedule failure", event: "schedule", race: true, exitCode: 23},
		{name: "manual failure", event: "workflow_dispatch", race: true, exitCode: 23},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			directory := t.TempDir()
			bin := filepath.Join(directory, "bin")
			require.NoError(t, os.Mkdir(bin, 0o755), "each case must have isolated fixture commands")
			fakeGo := `#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$@" >> "${MOCK_GO_LOG:?}"
exit "${MOCK_GO_EXIT_CODE:?}"
`
			require.NoError(t, os.WriteFile(filepath.Join(bin, "go"), []byte(fakeGo), 0o755), "fixture must capture Go arguments without compiling or accessing a database")
			logPath := filepath.Join(directory, "go-args")
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, "bash", "-euo", "pipefail", "-c", step.Run)
			command.Dir = directory
			command.Env = []string{
				"PATH=" + bin + ":/usr/bin:/bin",
				"GITHUB_EVENT_NAME=" + tt.event,
				"TEST_DATABASE_URL=" + job.Env["TEST_DATABASE_URL"],
				"MOCK_GO_LOG=" + logPath,
				"MOCK_GO_EXIT_CODE=" + strconv.Itoa(tt.exitCode),
			}
			output, err := command.CombinedOutput()
			if tt.exitCode == 0 {
				require.NoError(t, err, "actual CI command-test shell must succeed when Go tests pass: %s", output)
			} else {
				var exitError *exec.ExitError
				require.ErrorAs(t, err, &exitError, "actual CI command-test shell must fail when any command package fails: %s", output)
				require.Equal(t, tt.exitCode, exitError.ExitCode(), "command test failures must propagate to the CI step")
			}
			logged, err := os.ReadFile(logPath)
			require.NoError(t, err, "fixture must record the actual Go test arguments")
			expected := []string{"test", "./cmd/...", "-count=1"}
			if tt.race {
				expected = append(expected, "-race")
			}
			expected = append(expected, "-timeout=120s")
			require.Equal(t, expected, strings.Split(strings.TrimSuffix(string(logged), "\n"), "\n"), "CI must test every command package without a test-name filter, with the event's race mode and a bounded package timeout")
		})
	}
}
