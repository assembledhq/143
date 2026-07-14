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
)

// Canary/stable release-channel deploy contracts.
// See docs/design/118-canary-stable-release-channels.md.

// readDeployScriptText mirrors deploy_config_test.go: content assertions read
// the script fresh so each test is self-contained.
func readDeployScriptText(t *testing.T) string {
	t.Helper()
	deployScript, err := os.ReadFile("../deploy/scripts/deploy.sh")
	require.NoError(t, err, "test should read deploy.sh")
	return string(deployScript)
}

func TestDeployScriptDefinesCanaryRoles(t *testing.T) {
	t.Parallel()

	deployText := readDeployScriptText(t)

	require.Contains(t, deployText, `app-canary)`, "deploy.sh should accept the app-canary role")
	require.Contains(t, deployText, `worker-canary)`, "deploy.sh should accept the worker-canary role")
	require.Contains(t, deployText, `ROLE_KIND="${ROLE%-canary}"`, "deploy.sh should normalize canary roles onto the shared host logic")
	require.Contains(t, deployText, `HEALTH_SERVICE="api-canary"`, "app-canary deploys should health-check the canary api")
}

func TestCanaryAppDeployOwnsMigrationsAndStaysOffStableContainers(t *testing.T) {
	t.Parallel()

	deployText := readDeployScriptText(t)

	require.Contains(t, deployText, `run --rm -T --no-deps \
    -e STABLE_MAX_MIGRATION api-canary /bin/migrate up`,
		"the canary plane owns migrations and must pass the destructive-migration gate input through")
	require.Contains(t, deployText, `docker compose -f "$COMPOSE_FILE" pull api-canary frontend-canary`,
		"app-canary deploys should pull only the canary images")
	require.Contains(t, deployText, `up -d --no-deps api-canary`,
		"app-canary deploys must recreate only canary services, never stable ones")
	require.NotContains(t, strings.Split(deployText, "CANARY_REMOTE")[1], "rolling_deploy_service",
		"the canary path deliberately recreates in place instead of duplicating the rolling machinery")
}

func TestStableAppDeploySupportsSchemaVerifyMode(t *testing.T) {
	t.Parallel()

	deployText := readDeployScriptText(t)

	require.Contains(t, deployText, `if [ "${APP_SCHEMA_MODE:-migrate}" = "verify" ]`,
		"stable app deploys should support the verify preflight in place of migrate up")
	require.Contains(t, deployText, `api /bin/migrate verify`,
		"the verify mode should run the stable-plane schema preflight")
	require.Contains(t, deployText, `api /bin/migrate up`,
		"the default mode must stay migrate so single-plane self-hosted deploys keep working")
	require.Contains(t, deployText, `$(remote_env_assignment APP_SCHEMA_MODE "${APP_SCHEMA_MODE:-migrate}")`,
		"APP_SCHEMA_MODE should be passed through SSH to the remote host")
}

func TestWorkerCanaryWritesChannelIntoHostEnv(t *testing.T) {
	t.Parallel()

	deployText := readDeployScriptText(t)

	require.Contains(t, deployText, `CHANNEL=%s`, "worker secrets refresh should persist the release channel into the host .env")
	require.Contains(t, deployText, `CHANNEL="canary"`, "canary roles should derive the canary channel")

	workerCompose, err := os.ReadFile("../docker-compose.worker.yml")
	require.NoError(t, err, "test should read docker-compose.worker.yml")
	require.Contains(t, string(workerCompose), "CHANNEL: ${CHANNEL:-stable}",
		"the worker service should hand the host channel to the server process")
}

func TestDeployFleetRunsCanaryAppBarrierBeforeStableApp(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	scriptDir := filepath.Join(tempDir, "deploy", "scripts")
	require.NoError(t, os.MkdirAll(scriptDir, 0o755), "test should create a temporary deploy script directory")

	fleetScript, err := os.ReadFile("../deploy/scripts/deploy-fleet.sh")
	require.NoError(t, err, "test should read deploy-fleet.sh")
	fleetScriptPath := filepath.Join(scriptDir, "deploy-fleet.sh")
	require.NoError(t, os.WriteFile(fleetScriptPath, fleetScript, 0o755), "test should copy deploy-fleet.sh into the temporary layout")

	stateDir := filepath.Join(tempDir, "state")
	fakeDeploy := `#!/usr/bin/env bash
set -euo pipefail
role="$1"
ip="$2"
state="${FAKE_DEPLOY_STATE:?}"
mkdir -p "$state"
printf '%s@%s\n' "$role" "$ip" >> "$state/order.log"
echo "$role@$ip deployed by fake script"
`
	require.NoError(t, os.WriteFile(filepath.Join(scriptDir, "deploy.sh"), []byte(fakeDeploy), 0o755), "test should install a fake deploy.sh")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "bash", fleetScriptPath, "fake-key", "test-tag", "app-canary,app,worker,worker-canary")
	cmd.Env = append(os.Environ(),
		"DEPLOY_JOBS=1",
		"FAKE_DEPLOY_STATE="+stateDir,
		"FLEET_HOSTS=app:10.0.0.1,app-canary:10.0.0.1,worker:10.0.0.2,worker-canary:10.0.0.3",
	)
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, "deploy-fleet should deploy canary and stable planes together: %s", string(output))

	orderBytes, err := os.ReadFile(filepath.Join(stateDir, "order.log"))
	require.NoError(t, err, "test should read the fake deploy order log")
	order := strings.Split(strings.TrimSpace(string(orderBytes)), "\n")
	require.GreaterOrEqual(t, len(order), 4, "all four roles should deploy: %v", order)
	require.Equal(t, "app-canary@10.0.0.1", order[0], "the canary app (which owns migrations) must deploy first")
	require.Equal(t, "app@10.0.0.1", order[1], "the stable app must deploy after the canary migration barrier")
}

func TestDeployFleetToleratesFleetsWithoutCanaryHostsUnderAll(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	scriptDir := filepath.Join(tempDir, "deploy", "scripts")
	require.NoError(t, os.MkdirAll(scriptDir, 0o755), "test should create a temporary deploy script directory")

	fleetScript, err := os.ReadFile("../deploy/scripts/deploy-fleet.sh")
	require.NoError(t, err, "test should read deploy-fleet.sh")
	fleetScriptPath := filepath.Join(scriptDir, "deploy-fleet.sh")
	require.NoError(t, os.WriteFile(fleetScriptPath, fleetScript, 0o755), "test should copy deploy-fleet.sh into the temporary layout")

	stateDir := filepath.Join(tempDir, "state")
	fakeDeploy := `#!/usr/bin/env bash
set -euo pipefail
state="${FAKE_DEPLOY_STATE:?}"
mkdir -p "$state"
printf '%s@%s\n' "$1" "$2" >> "$state/order.log"
`
	require.NoError(t, os.WriteFile(filepath.Join(scriptDir, "deploy.sh"), []byte(fakeDeploy), 0o755), "test should install a fake deploy.sh")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// A single-plane fleet (no canary hosts) with roles=all must keep
	// working — self-hosters and the pre-canary production fleet.
	cmd := exec.CommandContext(ctx, "bash", fleetScriptPath, "fake-key", "test-tag", "all")
	cmd.Env = append(os.Environ(),
		"DEPLOY_JOBS=1",
		"FAKE_DEPLOY_STATE="+stateDir,
		"FLEET_HOSTS=app:10.0.0.1,worker:10.0.0.2",
	)
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, "roles=all must tolerate fleets without canary hosts: %s", string(output))

	// But an explicit app-canary request against a canary-less fleet fails
	// before any other role deploys (worker would otherwise proceed without
	// the canary migration barrier).
	cmd = exec.CommandContext(ctx, "bash", fleetScriptPath, "fake-key", "test-tag", "app-canary,worker")
	cmd.Env = append(os.Environ(),
		"DEPLOY_JOBS=1",
		"FAKE_DEPLOY_STATE="+stateDir,
		"FLEET_HOSTS=app:10.0.0.1,worker:10.0.0.2",
	)
	output, err = cmd.CombinedOutput()
	require.Error(t, err, "an explicit app-canary request must fail when the fleet has no canary host")
	require.Contains(t, string(output), "app-canary role was requested", "the failure should explain the missing canary barrier host")
}
