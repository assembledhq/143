package deploy_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestCIUnitPartitionsAndCoverage(t *testing.T) {
	t.Parallel()

	raw, err := os.ReadFile("../.github/workflows/ci.yml")
	require.NoError(t, err, "test must exercise the actual CI workflow")
	var workflow struct {
		Jobs map[string]struct {
			Steps []struct {
				Name           string `yaml:"name"`
				Run            string `yaml:"run"`
				TimeoutMinutes int    `yaml:"timeout-minutes"`
				With           struct {
					Path string `yaml:"path"`
				} `yaml:"with"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	require.NoError(t, yaml.Unmarshal(raw, &workflow), "CI workflow must remain valid YAML")
	steps := make(map[string]string)
	stepIndexes := make(map[string]int)
	stepTimeouts := make(map[string]int)
	var coverageArtifacts string
	for i, step := range workflow.Jobs["backend-test"].Steps {
		if step.Run != "" {
			require.NotContains(t, steps, step.Name, "test steps must have unambiguous names")
			steps[step.Name] = step.Run
			stepIndexes[step.Name] = i
			stepTimeouts[step.Name] = step.TimeoutMinutes
		}
		if step.Name == "Upload coverage report" {
			coverageArtifacts = step.With.Path
		}
	}
	unitSteps := []string{
		"Run backend unit tests outside database, agent, and worker packages",
		"Run database tests",
		"Run worker unit tests",
		"Run agent unit tests",
		"Combine backend unit coverage",
	}
	for _, name := range append(append([]string{}, unitSteps...), "Run preview browser lifecycle tests", "Run integration tests", "Combine backend coverage") {
		require.NotEmpty(t, steps[name], "required backend test step must exist: %s", name)
	}
	for i := 1; i < len(unitSteps); i++ {
		require.Less(t, stepIndexes[unitSteps[i-1]], stepIndexes[unitSteps[i]], "unit partitions must finish before their shared coverage merge")
	}
	require.Equal(t, 10, stepTimeouts["Run database tests"], "sharding must retain the existing aggregate database step budget")
	require.Less(t, stepIndexes["Combine backend unit coverage"], stepIndexes["Combine backend coverage"], "final coverage must consume the completed unit merge")
	for _, profile := range []string{"coverage-unit-other.out", "coverage-unit-db.out", "coverage-unit-worker.out", "coverage-unit-agent.out"} {
		require.Contains(t, strings.Fields(coverageArtifacts), profile, "each unit partition must retain its coverage evidence")
	}
	merger, err := os.ReadFile("../.github/scripts/merge_go_coverage.py")
	require.NoError(t, err, "test must use the actual coverage merger")
	shardRunner, err := os.ReadFile("../.github/scripts/run_go_test_shards.py")
	require.NoError(t, err, "test must exercise the actual database root-discovery and shard runner")
	python, err := exec.LookPath("python3")
	require.NoError(t, err, "coverage merging requires Python like the CI runner")

	tests := []struct {
		name, event, mode, otherTimeout, boundedTimeout string
		race                                            []string
	}{
		{name: "pull request", event: "pull_request", mode: "count", otherTimeout: "120s", boundedTimeout: "300s"},
		{name: "push", event: "push", mode: "atomic", otherTimeout: "180s", boundedTimeout: "360s", race: []string{"-race"}},
		{name: "scheduled", event: "schedule", mode: "atomic", otherTimeout: "180s", boundedTimeout: "360s", race: []string{"-race"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			directory := t.TempDir()
			bin := filepath.Join(directory, "bin")
			scripts := filepath.Join(directory, ".github", "scripts")
			for _, path := range []string{bin, scripts} {
				require.NoError(t, os.MkdirAll(path, 0o755), "each parallel case must have isolated fixture commands")
			}
			require.NoError(t, os.WriteFile(filepath.Join(scripts, "merge_go_coverage.py"), merger, 0o600), "fixture must execute the real merger without accessing the repository")
			require.NoError(t, os.WriteFile(filepath.Join(scripts, "run_go_test_shards.py"), shardRunner, 0o600), "fixture must execute the real shard runner without accessing the repository")
			fakeGo := fmt.Sprintf(`#!%s
import json
import os
from pathlib import Path
import re
import sys

roots = ["ExampleSynthetic", "FuzzSynthetic", "TestAlpha", "TestAlphaLonger", "TestBravo", "TestCharlie", "TestDelta", "TestEcho"]
args = sys.argv[1:]
with open(os.environ["MOCK_GO_LOG"], "a") as log:
    log.write(json.dumps(args) + "\n")
if args == ["list", "./internal/..."]:
    print("\n".join([
        "github.com/assembledhq/143/internal/worker",
        "github.com/assembledhq/143/internal/config",
        "github.com/assembledhq/143/internal/services/agent",
        "github.com/assembledhq/143/internal/db",
        "github.com/assembledhq/143/internal/services/preview",
    ]))
elif args[:4] == ["test", "-list", ".", "./internal/db"]:
    print("\n".join(roots + ["BenchmarkIgnored", "ok\tgithub.com/assembledhq/143/internal/db\t0.01s"]))
elif args and args[0] == "test":
    profile = next(a.split("=", 1)[1] for a in args if a.startswith("-coverprofile="))
    mode = next(a.split("=", 1)[1] for a in args if a.startswith("-covermode="))
    if Path(profile).name.startswith("coverage-shard-"):
        suite = "db"
        pattern = args[args.index("-run") + 1]
        selected = [name for name in roots if re.fullmatch(pattern, name)]
        assert len(selected) == 2, "each database invocation must select a nonempty balanced root shard"
        for name in selected:
            print(json.dumps({"Action": "pass", "Package": "example/db", "Test": name}))
    else:
        suite = {"coverage-unit-other.out": "other", "coverage-unit-worker.out": "worker", "coverage-unit-agent.out": "agent"}[profile]
    Path(profile).write_text("mode: " + mode + "\nexample/" + suite + ".go:1.1,2.1 1 1\nexample/shared.go:1.1,2.1 1 1\n")
elif args == ["tool", "cover", "-func=coverage.out"]:
    assert Path("coverage.out").is_file()
else:
    raise SystemExit("unexpected Go invocation: " + repr(args))
`, python)
			require.NoError(t, os.WriteFile(filepath.Join(bin, "go"), []byte(fakeGo), 0o755), "fixture must record Go commands without compiling or using a database")
			// Supply the resolved interpreter to the actual merge commands even
			// when the developer's Python lives outside the system PATH.
			require.NoError(t, os.Symlink(python, filepath.Join(bin, "python3")), "fixture must expose the CI coverage interpreter")
			logPath := filepath.Join(directory, "go.jsonl")
			diagnosticsRoot := t.TempDir()
			env := []string{
				"PATH=" + bin + ":/usr/bin:/bin",
				"HOME=" + directory,
				"RUNNER_TEMP=" + diagnosticsRoot,
				"GITHUB_EVENT_NAME=" + tt.event,
				"MOCK_GO_LOG=" + logPath,
				"PYTHONDONTWRITEBYTECODE=1",
			}
			run := func(script string) {
				t.Helper()
				ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				defer cancel()
				command := exec.CommandContext(ctx, "bash", "-euo", "pipefail", "-c", script)
				command.Dir, command.Env = directory, env
				output, err := command.CombinedOutput()
				require.NoError(t, err, "actual CI shell must succeed against isolated commands: %s", output)
			}
			for _, name := range unitSteps {
				run(steps[name])
			}
			unitCoverage, err := os.ReadFile(filepath.Join(directory, "coverage-unit.out"))
			require.NoError(t, err, "unit partition merge must produce coverage")
			require.Equal(t, fmt.Sprintf("mode: %s\nexample/agent.go:1.1,2.1 1 1\nexample/db.go:1.1,2.1 1 4\nexample/other.go:1.1,2.1 1 1\nexample/shared.go:1.1,2.1 1 7\nexample/worker.go:1.1,2.1 1 1\n", tt.mode), string(unitCoverage), "unit coverage must include all four suites without duplicating the statement denominator")

			// Exercise the real browser/integration mode branches without
			// starting Chrome or Postgres, then feed their profiles through the
			// final workflow merger. Mixed modes must not surface only in CI.
			modeBranch := regexp.MustCompile(`(?ms)^mode=count\n.*?^fi\n`)
			for _, suite := range []struct{ step, name string }{
				{step: "Run preview browser lifecycle tests", name: "browser"},
				{step: "Run integration tests", name: "integration"},
			} {
				branch := modeBranch.FindString(steps[suite.step])
				require.NotEmpty(t, branch, "external coverage suite must expose its actual mode/race selection")
				run(branch + fmt.Sprintf("printf 'mode: %%s\\nexample/%s.go:1.1,2.1 1 1\\nexample/shared.go:1.1,2.1 1 1\\n' \"$mode\" > coverage-%s.out\n", suite.name, suite.name))
			}
			run(steps["Combine backend coverage"])
			finalCoverage, err := os.ReadFile(filepath.Join(directory, "coverage.out"))
			require.NoError(t, err, "final coverage merge must consume the unit partition output")
			require.Equal(t, fmt.Sprintf("mode: %s\nexample/agent.go:1.1,2.1 1 1\nexample/browser.go:1.1,2.1 1 1\nexample/db.go:1.1,2.1 1 4\nexample/integration.go:1.1,2.1 1 1\nexample/other.go:1.1,2.1 1 1\nexample/shared.go:1.1,2.1 1 9\nexample/worker.go:1.1,2.1 1 1\n", tt.mode), string(finalCoverage), "final coverage must retain database and worker evidence and agree with browser and integration modes")

			logged, err := os.ReadFile(logPath)
			require.NoError(t, err, "fixture must retain every actual Go invocation")
			var calls [][]string
			for _, line := range strings.Split(strings.TrimSpace(string(logged)), "\n") {
				var args []string
				require.NoError(t, json.Unmarshal([]byte(line), &args), "fake Go must record complete argument arrays")
				calls = append(calls, args)
			}
			otherArgs := []string{"test", "-json", "github.com/assembledhq/143/internal/config", "github.com/assembledhq/143/internal/services/preview", "-skip", "^TestBrowserChromeDP", "-coverpkg=./internal/...", "-coverprofile=coverage-unit-other.out", "-covermode=" + tt.mode}
			otherArgs = append(otherArgs, tt.race...)
			otherArgs = append(otherArgs, "-timeout="+tt.otherTimeout)
			boundedArgs := func(pkg, profile string) []string {
				args := []string{"test"}
				if pkg == "./internal/worker" {
					args = append(args, "-json")
				}
				args = append(args, pkg)
				args = append(args, "-coverpkg=./internal/...", "-coverprofile="+profile, "-covermode="+tt.mode)
				args = append(args, tt.race...)
				return append(args, "-timeout="+tt.boundedTimeout)
			}
			discoveryArgs := append([]string{"test", "-list", ".", "./internal/db"}, tt.race...)
			expectedCalls := [][]string{{"list", "./internal/..."}, otherArgs, discoveryArgs}
			expectedRoots := [][]string{
				{"ExampleSynthetic", "TestBravo"},
				{"FuzzSynthetic", "TestCharlie"},
				{"TestAlpha", "TestDelta"},
				{"TestAlphaLonger", "TestEcho"},
			}
			for index, roots := range expectedRoots {
				profile := filepath.Join(diagnosticsRoot, "ci-db-diagnostics", fmt.Sprintf("coverage-shard-%d.out", index+1))
				args := []string{"test", "-json", "./internal/db", "-parallel=2", "-count=1", "-coverpkg=./internal/...",
					"-coverprofile=" + profile, "-covermode=" + tt.mode, "-timeout=" + tt.boundedTimeout,
					"-run", "^(?:" + strings.Join(roots, "|") + ")$"}
				expectedCalls = append(expectedCalls, append(args, tt.race...))
			}
			expectedCalls = append(expectedCalls,
				boundedArgs("./internal/worker", "coverage-unit-worker.out"),
				boundedArgs("./internal/services/agent", "coverage-unit-agent.out"),
				[]string{"tool", "cover", "-func=coverage.out"})
			require.Equal(t, expectedCalls, calls, "database roots must run in four fresh bounded shards while ordinary packages, worker, agent coverage, and race settings remain intact")
			manifestRaw, err := os.ReadFile(filepath.Join(diagnosticsRoot, "ci-db-diagnostics", "shard-status.json"))
			require.NoError(t, err, "the actual shard runner must retain complete per-shard execution evidence")
			var manifest struct {
				CoverageStatus int `json:"coverage_status"`
				Shards         []struct {
					Roots      []string `json:"roots"`
					RootCount  int      `json:"root_count"`
					ExitStatus *int     `json:"exit_status"`
				} `json:"shards"`
			}
			require.NoError(t, json.Unmarshal(manifestRaw, &manifest), "shard evidence must be valid structured output")
			require.Equal(t, 0, manifest.CoverageStatus, "every shard profile must be validated before the database aggregate is accepted")
			actualRoots := make([][]string, 0, len(manifest.Shards))
			for _, shard := range manifest.Shards {
				require.NotNil(t, shard.ExitStatus, "every planned database shard must have actually finished")
				require.Equal(t, 0, *shard.ExitStatus, "each database shard must report its actual successful command status")
				require.Equal(t, 2, shard.RootCount, "all roots must be assigned to nonempty balanced shards")
				actualRoots = append(actualRoots, shard.Roots)
			}
			require.Equal(t, expectedRoots, actualRoots, "discovery must cover current tests, prefix-sharing names, fuzz seeds, and examples exactly once")
		})
	}
}
