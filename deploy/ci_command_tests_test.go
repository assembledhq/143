package deploy_test

import (
	"context"
	"encoding/json"
	"fmt"
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
	require.Equal(t, 10, step.TimeoutMinutes, "command tests must retain a bounded step budget with compilation headroom")
	require.Greater(t, time.Duration(step.TimeoutMinutes)*time.Minute, 300*time.Second, "step budget must leave headroom beyond the command package deadline")
	if databaseURL, overridden := step.Env["TEST_DATABASE_URL"]; overridden {
		require.Equal(t, job.Env["TEST_DATABASE_URL"], databaseURL, "the command-test step must preserve its provisioned test database")
	}
	shardRunner, err := os.ReadFile("../.github/scripts/run_go_test_shards.py")
	require.NoError(t, err, "command fixtures must exercise the actual migration root discovery and shard runner")
	merger, err := os.ReadFile("../.github/scripts/merge_go_coverage.py")
	require.NoError(t, err, "the isolated runner must have its real coverage dependency even when commands do not request coverage")
	python, err := exec.LookPath("python3")
	require.NoError(t, err, "command sharding requires Python like the CI runner")

	tests := []struct {
		name, event                           string
		race                                  bool
		ordinaryExit, migrationExit, exitCode int
	}{
		{name: "pull request", event: "pull_request"},
		{name: "push", event: "push", race: true},
		{name: "schedule", event: "schedule", race: true},
		{name: "manual", event: "workflow_dispatch", race: true},
		{name: "pull request ordinary failure", event: "pull_request", ordinaryExit: 23, exitCode: 23},
		{name: "push ordinary failure", event: "push", race: true, ordinaryExit: 23, exitCode: 23},
		{name: "schedule ordinary failure", event: "schedule", race: true, ordinaryExit: 23, exitCode: 23},
		{name: "manual ordinary failure", event: "workflow_dispatch", race: true, ordinaryExit: 23, exitCode: 23},
		{name: "pull request migration failure", event: "pull_request", migrationExit: 31, exitCode: 31},
		{name: "push migration failure", event: "push", race: true, migrationExit: 31, exitCode: 31},
		{name: "first partition failure wins", event: "pull_request", ordinaryExit: 23, migrationExit: 31, exitCode: 23},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			directory := t.TempDir()
			bin := filepath.Join(directory, "bin")
			require.NoError(t, os.Mkdir(bin, 0o755), "each case must have isolated fixture commands")
			scripts := filepath.Join(directory, ".github", "scripts")
			require.NoError(t, os.MkdirAll(scripts, 0o755), "each case must own its actual runner dependencies")
			require.NoError(t, os.WriteFile(filepath.Join(scripts, "run_go_test_shards.py"), shardRunner, 0o600), "fixture must execute the real migration shard planner")
			require.NoError(t, os.WriteFile(filepath.Join(scripts, "merge_go_coverage.py"), merger, 0o600), "fixture must load the runner's real module dependency")
			require.NoError(t, os.Symlink(python, filepath.Join(bin, "python3")), "fixture must expose the resolved CI interpreter")
			fakeGo := fmt.Sprintf(`#!%s
import json
import os
import re
import sys

roots = ["ExampleSynthetic", "FuzzSynthetic", "TestAlpha", "TestAlphaLonger", "TestBravo", "TestCharlie", "TestDelta", "TestEcho"]
args = sys.argv[1:]
with open(os.environ["MOCK_GO_LOG"], "a") as log:
    log.write(json.dumps(args) + "\n")
if args == ["list", "./cmd/..."]:
    print("\n".join(["github.com/assembledhq/143/cmd/migrate", "github.com/assembledhq/143/cmd/demo-seed", "github.com/assembledhq/143/cmd/wait-postgres"]))
elif args[:4] == ["test", "-list", ".", "./cmd/migrate"]:
    print("\n".join(roots + ["BenchmarkIgnored", "ok\tgithub.com/assembledhq/143/cmd/migrate\t0.01s"]))
elif args and args[0] == "test":
    if "./cmd/migrate" in args:
        pattern = args[args.index("-run") + 1]
        selected = [name for name in roots if re.fullmatch(pattern, name)]
        assert len(selected) == 2, "each migration shard must select a nonempty balanced root set"
        for name in selected:
            print(json.dumps({"Action": "pass", "Package": "example/migrate", "Test": name}))
        sys.exit(int(os.environ["MOCK_MIGRATION_EXIT_CODE"]))
    sys.exit(int(os.environ["MOCK_ORDINARY_EXIT_CODE"]))
else:
    raise SystemExit("unexpected Go invocation: " + repr(args))
`, python)
			require.NoError(t, os.WriteFile(filepath.Join(bin, "go"), []byte(fakeGo), 0o755), "fixture must capture Go arguments without compiling or accessing a database")
			logPath := filepath.Join(directory, "go-args")
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, "bash", "-euo", "pipefail", "-c", step.Run)
			command.Dir = directory
			command.Env = []string{
				"PATH=" + bin + ":/usr/bin:/bin",
				"RUNNER_TEMP=" + directory,
				"GITHUB_EVENT_NAME=" + tt.event,
				"TEST_DATABASE_URL=" + job.Env["TEST_DATABASE_URL"],
				"MOCK_GO_LOG=" + logPath,
				"MOCK_ORDINARY_EXIT_CODE=" + strconv.Itoa(tt.ordinaryExit),
				"MOCK_MIGRATION_EXIT_CODE=" + strconv.Itoa(tt.migrationExit),
				"PYTHONDONTWRITEBYTECODE=1",
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
			ordinary := []string{"test", "-json", "github.com/assembledhq/143/cmd/demo-seed", "github.com/assembledhq/143/cmd/wait-postgres", "-p=1", "-parallel=2", "-count=1"}
			discovery := []string{"test", "-list", ".", "./cmd/migrate", "-p=1"}
			if tt.race {
				ordinary = append(ordinary, "-race")
				discovery = append(discovery, "-race")
			}
			ordinary = append(ordinary, "-timeout=300s")
			expected := [][]string{{"list", "./cmd/..."}, ordinary, discovery}
			expectedRoots := [][]string{{"ExampleSynthetic", "TestBravo"}, {"FuzzSynthetic", "TestCharlie"}, {"TestAlpha", "TestDelta"}, {"TestAlphaLonger", "TestEcho"}}
			for _, roots := range expectedRoots {
				args := []string{"test", "-json", "./cmd/migrate", "-p=1", "-parallel=2", "-count=1", "-timeout=300s", "-run", "^(?:" + strings.Join(roots, "|") + ")$"}
				if tt.race {
					args = append(args, "-race")
				}
				expected = append(expected, args)
			}
			var calls [][]string
			for _, line := range strings.Split(strings.TrimSpace(string(logged)), "\n") {
				var args []string
				require.NoError(t, json.Unmarshal([]byte(line), &args), "fixture must retain complete actual command arguments")
				calls = append(calls, args)
			}
			require.Equal(t, expected, calls, "every ordinary package must run once and every migration root must run in its exact shard, preserving concurrency, timeout and race mode even after earlier failures")
			status, err := os.ReadFile(filepath.Join(directory, "ci-command-diagnostics", "partition-status.txt"))
			require.NoError(t, err, "both command partitions must retain their actual exit statuses")
			require.Equal(t, fmt.Sprintf("ordinary_commands=%d\nmigration_shards=%d\n", tt.ordinaryExit, tt.migrationExit), string(status), "diagnostics must distinguish ordinary command failures from migration shard failures")
			manifestRaw, err := os.ReadFile(filepath.Join(directory, "ci-command-diagnostics", "migrate-shards", "shard-status.json"))
			require.NoError(t, err, "the actual runner must retain the complete migration shard manifest")
			var manifest struct {
				Package        string `json:"package"`
				CoverageStatus string `json:"coverage_status"`
				Shards         []struct {
					Roots      []string `json:"roots"`
					ExitStatus int      `json:"exit_status"`
				} `json:"shards"`
			}
			require.NoError(t, json.Unmarshal(manifestRaw, &manifest), "migration shard evidence must remain structured")
			require.Equal(t, "./cmd/migrate", manifest.Package, "the manifest must identify the isolated command package")
			require.Equal(t, "not_requested", manifest.CoverageStatus, "command partitioning must not add coverage instrumentation")
			var actualRoots [][]string
			for _, shard := range manifest.Shards {
				require.Equal(t, tt.migrationExit, shard.ExitStatus, "all migration shards must finish even after an earlier partition failure")
				actualRoots = append(actualRoots, shard.Roots)
			}
			require.Equal(t, expectedRoots, actualRoots, "tests, prefix-sharing names, fuzz seeds and examples must each appear in exactly one executed shard")
		})
	}
}
