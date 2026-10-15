package deploy_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestDeployWorkflowKeepsFleetInventoryAndDiagnosticsPrivate(t *testing.T) {
	t.Parallel()

	workflow, err := os.ReadFile("../.github/workflows/deploy.yml")
	require.NoError(t, err, "test should exercise the published deploy workflow")
	var parsed struct {
		Env  map[string]string `yaml:"env"`
		Jobs map[string]struct {
			Env   map[string]string `yaml:"env"`
			Steps []struct {
				Name string            `yaml:"name"`
				Run  string            `yaml:"run"`
				Env  map[string]string `yaml:"env"`
			} `yaml:"steps"`
		} `yaml:"jobs"`
	}
	require.NoError(t, yaml.Unmarshal(workflow, &parsed), "workflow should remain valid YAML")
	require.NotContains(t, parsed.Env, "FLEET_HOSTS", "inventory must not become workflow-level environment metadata")
	var deployRun string
	var deployEnv map[string]string
	for _, job := range parsed.Jobs {
		require.NotContains(t, job.Env, "FLEET_HOSTS", "inventory must not become job-level environment metadata")
		for _, step := range job.Steps {
			require.NotContains(t, step.Env, "FLEET_HOSTS", "inventory must not become static step environment metadata")
			if strings.Contains(step.Run, "FLEET_HOSTS") {
				require.NotContains(t, step.Run, "GITHUB_ENV", "inventory must not be propagated into later public Actions step headers")
			}
			if step.Name == "Deploy fleet" {
				require.Empty(t, deployRun, "workflow should have exactly one authoritative fleet invocation")
				deployRun, deployEnv = step.Run, step.Env
			}
		}
	}
	require.NotEmpty(t, deployRun, "workflow should provide the fleet step to execute")

	const inventory = "app:synthetic-app.invalid,worker:synthetic-worker.invalid"
	const rawSOPS = "synthetic-decrypt-diagnostic"
	const rawFleetOut = "synthetic-fleet-stdout"
	const rawFleetErr = "synthetic-fleet-stderr"
	const sha = "0123456789abcdef0123456789abcdef01234567"
	tests := []struct {
		name          string
		decrypted     string
		sopsExit      string
		fleetExit     string
		wantFleet     bool
		wantError     bool
		publicMessage string
	}{
		{name: "successful rollout", decrypted: "FLEET_HOSTS=" + inventory + "\n", sopsExit: "0", fleetExit: "0", wantFleet: true, publicMessage: "Fleet rollout and retirement checks completed."},
		{name: "failed rollout", decrypted: "FLEET_HOSTS=" + inventory + "\n", sopsExit: "0", fleetExit: "31", wantFleet: true, wantError: true, publicMessage: "Fleet rollout incomplete"},
		{name: "failed decryption with partial output", decrypted: "FLEET_HOSTS=" + inventory + "\n", sopsExit: "32", fleetExit: "0", wantError: true, publicMessage: "Fleet inventory unavailable"},
		{name: "empty inventory", decrypted: "FLEET_HOSTS=\n", sopsExit: "0", fleetExit: "0", wantError: true, publicMessage: "Fleet inventory unavailable"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			directory := t.TempDir()
			bin := filepath.Join(directory, "bin")
			scripts := filepath.Join(directory, "deploy", "scripts")
			runner := filepath.Join(directory, "runner")
			home := filepath.Join(directory, "home")
			for _, path := range []string{bin, scripts, runner, home} {
				require.NoError(t, os.MkdirAll(path, 0o755), "fixture paths must be isolated for parallel execution")
			}
			githubEnv := filepath.Join(directory, "github-env")
			githubSummary := filepath.Join(directory, "github-summary")
			const originalMetadata = "existing unrelated metadata\n"
			for _, path := range []string{githubEnv, githubSummary} {
				require.NoError(t, os.WriteFile(path, []byte(originalMetadata), 0o600), "fixture should track public Actions metadata writes")
			}
			sops := `#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$@" > "$MOCK_SOPS_ARGS"
printf '%s\n' 'synthetic-decrypt-diagnostic' >&2
printf '%s' "$MOCK_DECRYPTED"
exit "$MOCK_SOPS_EXIT"
`
			fleet := `#!/usr/bin/env bash
set -euo pipefail
printf '%s' "${FLEET_HOSTS:?}" > "$MOCK_FLEET_INVENTORY"
printf '%s\n' "$@" > "$MOCK_FLEET_ARGS"
printf 'synthetic-fleet-stdout: %s\n' "$FLEET_HOSTS"
printf 'synthetic-fleet-stderr: %s\n' "$FLEET_HOSTS" >&2
if [ -n "${GITHUB_STEP_SUMMARY:-}" ]; then
  printf 'synthetic-fleet-summary: %s\n' "$FLEET_HOSTS" >> "$GITHUB_STEP_SUMMARY"
fi
exit "$MOCK_FLEET_EXIT"
`
			require.NoError(t, os.WriteFile(filepath.Join(bin, "sops"), []byte(sops), 0o755), "fixture must replace secret decryption without accessing credentials")
			require.NoError(t, os.WriteFile(filepath.Join(scripts, "deploy-fleet.sh"), []byte(fleet), 0o755), "fixture must replace fleet access without reaching hosts")
			require.NoError(t, os.WriteFile(filepath.Join(scripts, "deploy.sh"), []byte("#!/bin/sh\nexit 99\n"), 0o755), "fixture must satisfy the real workflow chmod without deploying")
			sopsArgs := filepath.Join(directory, "sops-args")
			fleetInventory := filepath.Join(directory, "fleet-inventory")
			fleetArgs := filepath.Join(directory, "fleet-args")
			run := strings.ReplaceAll(deployRun, "${{ github.event.workflow_run.head_sha }}", sha)
			require.NotContains(t, run, "${{", "fixture must resolve all Actions expressions before executing the real shell")
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			command := exec.CommandContext(ctx, "bash", "-c", run)
			command.Dir = directory
			// Do not inherit credentials, BASH_ENV, exported shell options, or
			// FLEET_HOSTS from the developer's environment.
			command.Env = []string{
				"PATH=" + bin + ":/usr/bin:/bin",
				"HOME=" + home,
				"RUNNER_TEMP=" + runner,
				"GITHUB_ENV=" + githubEnv,
				"GITHUB_STEP_SUMMARY=" + githubSummary,
				"MOCK_SOPS_ARGS=" + sopsArgs,
				"MOCK_DECRYPTED=" + tt.decrypted,
				"MOCK_SOPS_EXIT=" + tt.sopsExit,
				"MOCK_FLEET_INVENTORY=" + fleetInventory,
				"MOCK_FLEET_ARGS=" + fleetArgs,
				"MOCK_FLEET_EXIT=" + tt.fleetExit,
			}
			for name, value := range deployEnv {
				value = strings.ReplaceAll(value, "${{ github.workspace }}", directory)
				require.NotContains(t, value, "${{", "fixture must resolve all static step environment expressions")
				command.Env = append(command.Env, name+"="+value)
			}
			output, err := command.CombinedOutput()
			if tt.wantError {
				require.Error(t, err, "an unavailable inventory or failed fleet must fail the actual workflow step")
			} else {
				require.NoError(t, err, "valid inventory and a completed fleet should pass the actual workflow step: %s", output)
			}
			require.Contains(t, string(output), tt.publicMessage, "public output should communicate only the bounded rollout result")
			for _, sensitive := range []string{inventory, "synthetic-app.invalid", "synthetic-worker.invalid", rawSOPS, rawFleetOut, rawFleetErr, "synthetic-fleet-summary"} {
				require.NotContains(t, string(output), sensitive, "workflow stdout/stderr must not disclose inventory or raw diagnostics")
			}
			for _, path := range []string{githubEnv, githubSummary} {
				actual, readErr := os.ReadFile(path)
				require.NoError(t, readErr, "Actions metadata must remain readable")
				require.Equal(t, originalMetadata, string(actual), "inventory and raw fleet diagnostics must not enter public step metadata")
			}
			decryptDiagnostic, readErr := os.ReadFile(filepath.Join(runner, "fleet-hosts-error.log"))
			require.NoError(t, readErr, "private runner diagnostics should retain decryption failures for reconciliation")
			require.Equal(t, rawSOPS+"\n", string(decryptDiagnostic), "decryption diagnostics must be captured privately even on success")
			actualSOPSArgs, readErr := os.ReadFile(sopsArgs)
			require.NoError(t, readErr, "the workflow must actually invoke the mocked decrypter")
			require.Equal(t, "--decrypt\n--input-type\ndotenv\n--output-type\ndotenv\n.infra/.env.production.enc\n", string(actualSOPSArgs), "workflow should decrypt only the expected encrypted inventory bundle")
			if !tt.wantFleet {
				_, statErr := os.Stat(fleetInventory)
				require.ErrorIs(t, statErr, os.ErrNotExist, "decryption failure or empty inventory must never start fleet deployment")
				_, statErr = os.Stat(fleetArgs)
				require.ErrorIs(t, statErr, os.ErrNotExist, "unavailable inventory must not invoke the fleet script")
				return
			}
			actualInventory, readErr := os.ReadFile(fleetInventory)
			require.NoError(t, readErr, "the real workflow step must pass inventory to its fleet subprocess")
			require.Equal(t, inventory, string(actualInventory), "fleet must receive exact decrypted inventory despite metadata suppression")
			actualFleetArgs, readErr := os.ReadFile(fleetArgs)
			require.NoError(t, readErr, "fleet invocation should retain its synthetic argument receipt")
			require.Equal(t, filepath.Join(home, ".ssh", "deploy-key")+"\n"+sha+"\n", string(actualFleetArgs), "workflow must deploy the exact verified release SHA")
			fleetDiagnostic, readErr := os.ReadFile(filepath.Join(runner, "deploy-fleet.log"))
			require.NoError(t, readErr, "raw fleet diagnostics should remain available only in the runner-local capture")
			require.Equal(t, rawFleetOut+": "+inventory+"\n"+rawFleetErr+": "+inventory+"\n", string(fleetDiagnostic), "both fleet output streams must be privately captured on success and failure")
		})
	}
}
