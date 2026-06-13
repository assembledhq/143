package deploy_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/netip"
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

func TestFrontendContainerBindsAllInterfaces(t *testing.T) {
	t.Parallel()

	compose, err := os.ReadFile("../docker-compose.app.yml")
	require.NoError(t, err, "test should read the app compose file")
	require.Contains(t, string(compose), `HOSTNAME: "0.0.0.0"`, "frontend compose config should override Docker's container-id HOSTNAME so Next binds all interfaces")

	dockerfile, err := os.ReadFile("../Dockerfile.frontend")
	require.NoError(t, err, "test should read the frontend Dockerfile")
	require.Contains(t, string(dockerfile), "ENV HOSTNAME=0.0.0.0", "frontend image should default Next standalone to bind all interfaces")
}

func TestProductionComposeCapsDatabasePools(t *testing.T) {
	t.Parallel()

	appCompose, err := os.ReadFile("../docker-compose.app.yml")
	require.NoError(t, err, "test should read the app compose file")
	appText := string(appCompose)
	require.Contains(t, appText, "DATABASE_MAX_CONNS: ${API_DATABASE_MAX_CONNS:-20}", "app compose should cap the API database pool below Postgres max_connections")
	require.Contains(t, appText, "DATABASE_MAX_CONN_IDLE_TIME: ${API_DATABASE_MAX_CONN_IDLE_TIME:-5m}", "app compose should release idle API database connections after traffic bursts")

	workerCompose, err := os.ReadFile("../docker-compose.worker.yml")
	require.NoError(t, err, "test should read the worker compose file")
	workerText := string(workerCompose)
	require.Contains(t, workerText, "pool_max_conns=${WORKER_DATABASE_POOL_MAX_CONNS:-4}", "worker compose should cap worker and inherited session-executor database pools")
}

func TestFrontendDockerfileRunsRepoScopedStandaloneServer(t *testing.T) {
	t.Parallel()

	dockerfile, err := os.ReadFile("../Dockerfile.frontend")
	require.NoError(t, err, "test should read the frontend Dockerfile")
	dockerfileText := string(dockerfile)

	require.Contains(t, dockerfileText, "WORKDIR /app/frontend", "frontend image build and runtime should use Next's repo-scoped frontend directory")
	require.Contains(t, dockerfileText, "COPY --from=builder /app/frontend/.next/standalone ./", "frontend runtime image should copy the full repo-scoped standalone tree")
	require.Contains(t, dockerfileText, "COPY --from=builder /app/frontend/.next/static ./frontend/.next/static", "frontend runtime image should stage static assets next to the repo-scoped standalone server")
	require.Contains(t, dockerfileText, "COPY --from=builder /app/frontend/public ./frontend/public", "frontend runtime image should stage public assets next to the repo-scoped standalone server")
	require.Contains(t, dockerfileText, `CMD ["node", "server.js"]`, "frontend runtime image should start the server.js emitted into the frontend standalone directory")
}

func TestFrontendDockerfileCopiesFumadocsInputsBeforeInstall(t *testing.T) {
	t.Parallel()

	dockerfile, err := os.ReadFile("../Dockerfile.frontend")
	require.NoError(t, err, "test should read the frontend Dockerfile")
	dockerfileText := string(dockerfile)
	sourceConfigCopyIndex := strings.Index(dockerfileText, "COPY frontend/source.config.ts ./frontend/")
	publicDocsCopyIndex := strings.Index(dockerfileText, "COPY docs/public ./docs/public")
	npmCIIndex := strings.Index(dockerfileText, "RUN npm ci")

	require.NotEqual(t, -1, sourceConfigCopyIndex, "frontend image should copy the Fumadocs source config before install scripts run")
	require.NotEqual(t, -1, publicDocsCopyIndex, "frontend image should copy public docs before Fumadocs install scripts run")
	require.NotEqual(t, -1, npmCIIndex, "frontend image should install dependencies with npm ci")
	require.Less(t, sourceConfigCopyIndex, npmCIIndex, "source.config.ts should be available before npm ci runs postinstall")
	require.Less(t, publicDocsCopyIndex, npmCIIndex, "docs/public should be available before npm ci runs postinstall")

	dockerignore, err := os.ReadFile("../.dockerignore")
	require.NoError(t, err, "test should read the root Docker ignore file")
	dockerignoreText := string(dockerignore)
	require.NotContains(t, dockerignoreText, "\ndocs/\n", "docker build context should not exclude the entire docs tree because docs/public is needed by the frontend image")
	require.Contains(t, dockerignoreText, "docs/*", "docker build context should continue excluding non-public docs by default")
	require.Contains(t, dockerignoreText, "!docs/public", "docker build context should include the public docs directory")
	require.Contains(t, dockerignoreText, "!docs/public/**", "docker build context should include public docs files for Fumadocs generation")
}

func TestPreviewWildcardTLSUsesCloudflareDNSChallenge(t *testing.T) {
	t.Parallel()

	compose, err := os.ReadFile("../docker-compose.app.yml")
	require.NoError(t, err, "test should read the app compose file")
	composeText := string(compose)
	require.Contains(t, composeText, "build:", "app compose should build a custom Caddy image so the Cloudflare DNS provider module is available for wildcard preview certificates")
	require.Contains(t, composeText, "Dockerfile.caddy", "app compose should point the Caddy build at Dockerfile.caddy")
	require.Contains(t, composeText, "CLOUDFLARE_API_TOKEN", "app compose should pass the Cloudflare API token into the Caddy container for DNS-01 challenges")

	caddyfile, err := os.ReadFile("../deploy/Caddyfile")
	require.NoError(t, err, "test should read the Caddyfile")
	caddyText := string(caddyfile)
	require.Contains(t, caddyText, "*.preview.{$DOMAIN:143.dev}", "Caddyfile should keep a dedicated wildcard preview host block")
	require.Contains(t, caddyText, "dns cloudflare", "preview wildcard host should use the Cloudflare DNS challenge for certificate issuance")
	require.Contains(t, caddyText, "{env.CLOUDFLARE_API_TOKEN}", "preview wildcard host should read the Cloudflare API token from container env")

	caddyDockerfile, err := os.ReadFile("../Dockerfile.caddy")
	require.NoError(t, err, "test should read the custom Caddy Dockerfile")
	require.Contains(t, string(caddyDockerfile), "github.com/caddy-dns/cloudflare", "custom Caddy image should compile in the Cloudflare DNS provider module")

	deployScript, err := os.ReadFile("../deploy/scripts/deploy.sh")
	require.NoError(t, err, "test should read deploy.sh")
	deployText := string(deployScript)
	require.Contains(t, deployText, "Dockerfile.caddy", "app deploys should stage Dockerfile.caddy before docker compose up so remote builds can succeed")
	require.Contains(t, deployText, `docker compose -f "$COMPOSE_FILE" build caddy`, "app deploys should explicitly build the custom Caddy image so Dockerfile.caddy changes and base-image refreshes reach the host")
	require.Contains(t, deployText, `docker compose -f "$COMPOSE_FILE" up -d --no-deps caddy`, "app deploys should reconcile the running Caddy container against the freshly built image and current env")
	require.Contains(t, deployText, "CLOUDFLARE_API_TOKEN=%s", "app deploys should project the Cloudflare DNS-challenge token into /opt/143/.env for compose interpolation")
	require.Contains(t, deployText, "PREVIEW_ORIGIN_TEMPLATE=%s", "app deploys should project the preview origin template into /opt/143/.env so the app host can override the production preview domain")
	require.Contains(t, deployText, "NEXT_PUBLIC_PREVIEW_ORIGIN_TEMPLATE=%s", "app deploys should project the frontend preview-origin fallback into /opt/143/.env on the app host")
	buildIndex := strings.Index(deployText, `echo "Dockerfile.caddy changed — building custom Caddy image..."`)
	reconcileCallIndex := strings.LastIndex(deployText, `reconcile_caddy_service`)
	reloadIndex := strings.LastIndex(deployText, `caddy reload --config /etc/caddy/Caddyfile --adapter caddyfile`)
	require.NotEqual(t, -1, buildIndex, "deploy.sh should build caddy in the app-role execution path")
	require.NotEqual(t, -1, reconcileCallIndex, "deploy.sh should reconcile caddy after rolling the app/frontend services")
	require.NotEqual(t, -1, reloadIndex, "deploy.sh should still support in-place Caddyfile reloads")
	require.Less(t, buildIndex, reconcileCallIndex, "deploy.sh should build the custom caddy image before reconciling the running container")

	provisionScript, err := os.ReadFile("../deploy/scripts/provision.sh")
	require.NoError(t, err, "test should read provision.sh")
	require.Contains(t, string(provisionScript), "Dockerfile.caddy", "fresh app provisioning should stage Dockerfile.caddy before the first docker compose up")
	require.Contains(t, string(provisionScript), "CLOUDFLARE_API_TOKEN=%s", "fresh app provisioning should project the Cloudflare DNS-challenge token into /opt/143/.env for compose interpolation")
	require.Contains(t, string(provisionScript), "PREVIEW_ORIGIN_TEMPLATE=%s", "fresh app provisioning should project the preview origin template into /opt/143/.env so the app host can override the production preview domain")
	require.Contains(t, string(provisionScript), "NEXT_PUBLIC_PREVIEW_ORIGIN_TEMPLATE=%s", "fresh app provisioning should project the frontend preview-origin fallback into /opt/143/.env on the app host")
}

// TestCLIDistributionRoutesProxyToAPI pins the Caddyfile rules that send the
// 143-tools installer and download routes to the Go server instead of the
// Next.js frontend fallthrough. Without these, `curl https://143.com/install.sh`
// would return the frontend's 404 page — the install one-liner depends on it.
func TestCLIDistributionRoutesProxyToAPI(t *testing.T) {
	t.Parallel()

	caddyfile, err := os.ReadFile("../deploy/Caddyfile")
	require.NoError(t, err, "test should read the Caddyfile")
	caddyText := string(caddyfile)

	// Anchor to the line-start occurrence: the www. and *.preview. site
	// headers contain "{$DOMAIN:143.dev}" as a substring and appear first.
	mainStart := strings.Index(caddyText, "\n{$DOMAIN:143.dev} {")
	require.NotEqual(t, -1, mainStart, "Caddyfile should contain the main site block")
	mainBlock := extractCaddySiteBlock(t, caddyText[mainStart:], "{$DOMAIN:143.dev}")
	require.Contains(t, mainBlock, "@cli_dist path /install.sh /install/* /download/*",
		"main site block should match the CLI installer and download paths")
	require.Contains(t, mainBlock, "handle @cli_dist",
		"main site block should have an explicit handle for CLI distribution routes")

	cliDistIndex := strings.Index(mainBlock, "handle @cli_dist")
	apiIndex := strings.Index(mainBlock, "handle /api/*")
	frontendFallthroughIndex := strings.LastIndex(mainBlock, "\thandle {")
	require.NotEqual(t, -1, cliDistIndex, "handle @cli_dist must exist in the main site block")
	require.NotEqual(t, -1, apiIndex, "handle /api/* must exist in the main site block")
	require.NotEqual(t, -1, frontendFallthroughIndex, "frontend fallthrough handle must exist in the main site block")
	require.Less(t, cliDistIndex, frontendFallthroughIndex,
		"CLI distribution handle must appear before the frontend fallthrough, or Caddy routes installs to Next.js")

	cliDistBlock := mainBlock[cliDistIndex:frontendFallthroughIndex]
	require.Contains(t, cliDistBlock, "name api", "CLI distribution routes must proxy to the api upstream")
	require.Contains(t, cliDistBlock, "port 8080", "CLI distribution routes must target the main API port")
}

func TestPreviewWildcardProxyDoesNotUseMainAppPassiveHealth(t *testing.T) {
	t.Parallel()

	caddyfile, err := os.ReadFile("../deploy/Caddyfile")
	require.NoError(t, err, "test should read the Caddyfile")
	caddyText := string(caddyfile)

	previewBlock := extractCaddySiteBlock(t, caddyText, "*.preview.{$DOMAIN:143.dev}")
	previewDefaults := extractCaddySnippetBlock(t, caddyText, "preview_gateway_upstream_defaults")
	require.Contains(t, caddyText, "(preview_gateway_upstream_defaults)", "Caddyfile should define preview-gateway-specific upstream defaults")
	require.Contains(t, previewBlock, "import preview_gateway_upstream_defaults", "preview wildcard routes should use preview-gateway-specific proxy defaults")
	require.Contains(t, previewDefaults, "health_uri /healthz", "preview gateway upstream defaults should keep active health checks for API startup and drain windows")
	require.Contains(t, previewDefaults, "health_interval 2s", "preview gateway upstream defaults should actively refresh API health state")
	require.Contains(t, previewDefaults, "health_timeout 2s", "preview gateway upstream defaults should bound active health probes")
	require.NotContains(t, previewBlock, "import upstream_defaults", "preview wildcard routes must not inherit main app passive health checks")
	require.NotContains(t, previewDefaults, "unhealthy_status 502 503 504", "per-preview 5xx responses must not mark the single preview gateway upstream unhealthy")
	require.NotContains(t, previewDefaults, "fail_duration 10s", "preview gateway proxying should not fan out one preview failure into a 10s wildcard outage")
}

func extractCaddySnippetBlock(t *testing.T, caddyText, snippetName string) string {
	t.Helper()

	return extractCaddyBlock(t, caddyText, "("+snippetName+")")
}

func extractCaddySiteBlock(t *testing.T, caddyText, siteHeader string) string {
	t.Helper()

	return extractCaddyBlock(t, caddyText, siteHeader)
}

func extractCaddyBlock(t *testing.T, caddyText, blockHeader string) string {
	t.Helper()

	start := strings.Index(caddyText, blockHeader+" {")
	require.NotEqual(t, -1, start, "Caddyfile should contain the requested site block")

	blockStart := start + len(blockHeader) + 1
	depth := 0
	for i := blockStart; i < len(caddyText); i++ {
		switch caddyText[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return caddyText[start : i+1]
			}
		}
	}
	require.Fail(t, "Caddy site block should have a matching closing brace")
	return ""
}

func TestRoutineAppDeployLeavesUnchangedCaddyRunning(t *testing.T) {
	t.Parallel()

	deployScript, err := os.ReadFile("../deploy/scripts/deploy.sh")
	require.NoError(t, err, "test should read deploy.sh")
	deployText := string(deployScript)

	require.Contains(t, deployText, "stage_caddy_dockerfile_if_changed", "app deploy should compare staged Dockerfile.caddy before rebuilding the edge image")
	require.Contains(t, deployText, "caddy_env_fingerprint_changed", "app deploy should compare Caddy-specific env before recreating the edge container")
	require.Contains(t, deployText, "Caddy inputs unchanged — leaving caddy running.", "routine app deploys should skip Caddy rebuild/reconcile when only API/frontend code changed")
	require.Contains(t, deployText, `if stage_caddy_dockerfile_if_changed; then`, "deploy.sh should build the custom Caddy image only when Dockerfile.caddy changed")
	require.NotContains(t, deployText, `echo "Building custom Caddy image..."
    docker compose -f "$COMPOSE_FILE" build caddy`, "app deploys should not unconditionally rebuild Caddy because compose may recreate the Cloudflare-facing origin")
}

func TestWorkerProvisioningIncludesGitHubAppUserAuthSecrets(t *testing.T) {
	t.Parallel()

	cloudInit, err := os.ReadFile("../deploy/cloud-init/worker.yml")
	require.NoError(t, err, "test should read the worker cloud-init template")
	require.Contains(t, string(cloudInit), "SOPS_AGE_KEY=${SOPS_AGE_KEY}", "worker cloud-init should provide SOPS_AGE_KEY so the container can decrypt .env.production.enc at boot")
	require.Contains(t, string(cloudInit), "GITHUB_APP_CLIENT_ID=${GITHUB_APP_CLIENT_ID}", "worker cloud-init should provide the GitHub App user auth client ID")
	require.Contains(t, string(cloudInit), "GITHUB_APP_CLIENT_SECRET=${GITHUB_APP_CLIENT_SECRET}", "worker cloud-init should provide the GitHub App user auth client secret")
	require.Contains(t, string(cloudInit), "SANDBOX_HEALTH_CHECK_IMAGE=${SANDBOX_HEALTH_CHECK_IMAGE}", "worker cloud-init should allow first-boot workers to override the sandbox health-check image via envsubst")
	require.NotContains(t, string(cloudInit), "SANDBOX_HEALTH_CHECK_IMAGE=busybox:1.36.1", "worker cloud-init should not hard-code the sandbox health-check image because private-mirror overrides must work before first compose up")
	require.Contains(t, string(cloudInit), "SANDBOX_REQUIRE_DISK_QUOTA=true", "worker cloud-init should require Docker disk quota support by default")
	require.Contains(t, string(cloudInit), "SANDBOX_GC_INTERVAL=5m", "worker cloud-init should enable worker-local sandbox GC")
	require.Contains(t, string(cloudInit), "- path: /opt/143/.env.production.enc", "worker cloud-init should stage the encrypted production env file before docker compose starts")
	require.Contains(t, string(cloudInit), "ENV_PRODUCTION_ENC_B64", "worker cloud-init should carry the encrypted production env payload as base64 input")

	provisionScript, err := os.ReadFile("../deploy/scripts/provision.sh")
	require.NoError(t, err, "test should read the provisioning script")
	require.Contains(t, string(provisionScript), "SOPS_AGE_KEY=%s", "worker reprovision path should write SOPS_AGE_KEY into .env so docker-entrypoint.sh decrypts the encrypted env bundle")
	require.Contains(t, string(provisionScript), "GITHUB_APP_CLIENT_ID=%s", "worker reprovision path should write the GitHub App user auth client ID into .env")
	require.Contains(t, string(provisionScript), "GITHUB_APP_CLIENT_SECRET=%s", "worker reprovision path should write the GitHub App user auth client secret into .env")
	require.Contains(t, string(provisionScript), "WORKER_MAX_ACTIVE_SANDBOXES=%s", "worker reprovision path should write the per-machine live sandbox capacity cap into .env")
	require.Contains(t, string(provisionScript), "SANDBOX_HEALTH_CHECK_IMAGE=%s", "worker reprovision path should write the sandbox health-check image into .env")
	require.Contains(t, string(provisionScript), "SANDBOX_REQUIRE_DISK_QUOTA=%s", "worker reprovision path should write the disk-quota requirement into .env")
	require.Contains(t, string(provisionScript), "SANDBOX_GC_INTERVAL=%s", "worker reprovision path should write the sandbox GC interval into .env")
	require.Contains(t, string(provisionScript), `scp "${SCP_OPTS[@]}" "$ENC_FILE" root@"$HOST":/opt/143/`, "worker reprovision path should copy .env.production.enc to the host before starting docker compose so bind-mount source creation cannot turn it into a directory")
}

func TestDeployWritesWorkerSandboxCapacityEnv(t *testing.T) {
	t.Parallel()

	deployScript, err := os.ReadFile("../deploy/scripts/deploy.sh")
	require.NoError(t, err, "test should read deploy.sh")
	require.Contains(t, string(deployScript), "WORKER_MAX_ACTIVE_SANDBOXES=%s", "worker deploy should write the per-machine live sandbox capacity cap into .env")
	require.Contains(t, string(deployScript), "SANDBOX_HEALTH_CHECK_IMAGE=%s", "worker deploy should write the sandbox health-check image into .env for compose preflight and app config")
}

func TestFleetDeployDefaultsToUserFacingRuntimeRoles(t *testing.T) {
	t.Parallel()

	fleetScript, err := os.ReadFile("../deploy/scripts/deploy-fleet.sh")
	require.NoError(t, err, "test should read deploy-fleet.sh")
	fleetText := string(fleetScript)
	require.Contains(t, fleetText, `REQUESTED_ROLES="${3:-app,worker}"`, "fleet deploy should default to the user-facing runtime roles so routine app deploys do not restart db, redis, or logging")
	require.Contains(t, fleetText, `./deploy/scripts/deploy-fleet.sh <ssh-key> [tag] all`, "fleet deploy should document an explicit all-roles maintenance argument")
	require.Contains(t, fleetText, `FLEET_HOSTS format:  app:10.0.0.2,worker:10.0.0.4,db:10.0.0.3,logging:10.0.0.6,redis:10.0.0.5,egress:10.0.0.7`, "fleet deploy should document egress as an inventory role")
	require.Contains(t, fleetText, `validate_requested_roles`, "fleet deploy should reject misspelled roles instead of silently skipping every host")
	require.Contains(t, fleetText, `cannot be combined with other roles`, "fleet deploy should reject confusing mixed selections like all,redis")
	require.Contains(t, fleetText, `No fleet hosts matched requested roles`, "fleet deploy should fail loudly when a valid role selection matches no hosts")
	require.Contains(t, fleetText, `should_deploy_role()`, "fleet deploy should centralize role filtering so every FLEET_HOSTS entry is handled consistently")
	require.Contains(t, fleetText, `Skipping $ROLE@$IP`, "fleet deploy should log skipped maintenance roles so operators can tell they were intentionally left alone")
	require.Contains(t, fleetText, `if [ "$ROLE" = "egress" ]; then`, "fleet deploy should skip egress inventory entries even when ROLES=all is requested")
	require.Contains(t, fleetText, "make provision-egress", "fleet deploy should point operators at the dedicated egress gateway provisioning flow")
	require.Contains(t, fleetText, `DEPLOY_JOBS="${DEPLOY_JOBS:-4}"`, "fleet deploy should default to a bounded four-node deploy fan-out")
	require.Contains(t, fleetText, `xargs -n1 -P "$DEPLOY_JOBS"`, "fleet deploy should deploy matching nodes concurrently instead of serializing the whole fleet")
	require.Contains(t, fleetText, `LOG_DIR="$(mktemp -d /tmp/deploy-fleet.XXXXXX)"`, "parallel fleet deploy should keep per-host logs inspectable after failures")
	require.Contains(t, fleetText, `LOG_DIR="$DEPLOY_FLEET_LOG_DIR"`, "fleet deploy should honor a stable log dir override so CI can upload per-host logs as an artifact")
	require.Contains(t, fleetText, `dump_failed_logs`, "fleet deploy should print failed hosts' log tails so CI output is introspectable without the runner's /tmp")
	require.Contains(t, fleetText, `deploy_one()`, "fleet deploy should isolate single-host deploy behavior so parallel fan-out keeps role and host context")
	require.Contains(t, fleetText, `FAILED: one or more deploys failed`, "fleet deploy should finish pending parallel deploys and then fail loudly when any host fails")
	require.Contains(t, fleetText, `DEPLOY_JOBS=1`, "fleet deploy should document how to recover the old one-host-at-a-time rollout behavior")

	workflow, err := os.ReadFile("../.github/workflows/deploy.yml")
	require.NoError(t, err, "test should read the deploy workflow")
	require.Contains(t, string(workflow), `./deploy/scripts/deploy-fleet.sh ~/.ssh/deploy-key "${{ github.sha }}"`, "CI should use deploy-fleet's default app/worker role set for routine main-branch deploys")
	require.Contains(t, string(workflow), `DEPLOY_FLEET_LOG_DIR: /tmp/deploy-fleet-logs`, "CI should pin the fleet log dir so the artifact upload step can find per-host logs")
	require.Contains(t, string(workflow), `uses: actions/upload-artifact@v4`, "CI should upload per-host deploy logs on failure; the runner's /tmp vanishes when the job ends")

	makefile, err := os.ReadFile("../Makefile")
	require.NoError(t, err, "test should read Makefile")
	require.Contains(t, string(makefile), "ROLES ?= app,worker", "Makefile should make the default fleet role set visible to operators")
	require.Contains(t, string(makefile), "force ?=", "Makefile should expose active-session force deploys as a make argument")
	require.Contains(t, string(makefile), "TAG ?= latest", "Makefile should expose the image tag as the same kind of make argument as roles")
	require.Contains(t, string(makefile), "DEPLOY_JOBS ?= 4", "Makefile should make the default fleet deploy parallelism visible to operators")
	require.Contains(t, string(makefile), "make deploy-fleet DEPLOY_JOBS=1", "Makefile should document how to serialize fleet deploys when needed")
	require.Contains(t, string(makefile), "WORKER_BLUE_GREEN_PORT_START ?= 8080", "Makefile should default manual worker deploys to the CI blue/green port range start")
	require.Contains(t, string(makefile), "WORKER_BLUE_GREEN_PORT_END ?= 8087", "Makefile should default manual worker deploys to the CI blue/green port range end")
	require.Contains(t, string(makefile), "$(worker-blue-green-env) $(deploy-force-env) ./deploy/scripts/deploy.sh $(1) $(HOST) $(SSH_KEY) $(TAG)", "single-role deploy targets should honor force and the default blue/green range")
	require.Contains(t, string(makefile), "$(worker-blue-green-env) $(deploy-force-env) ./deploy/scripts/deploy.sh $(1) $$h $(SSH_KEY) $(TAG)", "multi-host single-role deploy targets should pass force and the default blue/green range for every host")
	require.Contains(t, string(makefile), "make deploy-fleet ROLES=all", "Makefile should document how to run an explicit all-role maintenance deploy with a make argument")
	require.Contains(t, string(makefile), "make deploy-fleet ROLES=app,worker", "Makefile should document the manual non-disruptive worker deploy command")
	require.Contains(t, string(makefile), "make deploy-fleet force=true", "Makefile should document how to override the active-session guardrail with a make argument")
	require.Contains(t, string(makefile), "$(worker-blue-green-env) $(deploy-force-env) DEPLOY_JOBS=$(DEPLOY_JOBS) ./deploy/scripts/deploy-fleet.sh $(SSH_KEY) $(TAG) $(ROLES)", "Makefile should pass role, tag, force, parallelism, and the default blue/green range through to deploy-fleet.sh")

	deployScript, err := os.ReadFile("../deploy/scripts/deploy.sh")
	require.NoError(t, err, "test should read deploy.sh")
	require.Contains(t, string(deployScript), `-e "FORCE_DEPLOY_WITH_ACTIVE_SESSIONS=${FORCE_DEPLOY_WITH_ACTIVE_SESSIONS:-}"`, "worker deploy guardrail container should receive the force override from the deploy environment")
}

func TestDeployFleetRunsMatchingHostsConcurrently(t *testing.T) {
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
while ! mkdir "$state/lock" 2>/dev/null; do sleep 0.01; done
touch "$state/$role-$ip.started"
rmdir "$state/lock"
deadline=$((SECONDS + 5))
while [ "$(find "$state" -name '*.started' | wc -l | tr -d ' ')" -lt 2 ]; do
  if [ "$SECONDS" -ge "$deadline" ]; then
    echo "timed out waiting for concurrent deploy peer" >&2
    exit 42
  fi
  sleep 0.05
done
echo "$role@$ip deployed by fake script"
`
	require.NoError(t, os.WriteFile(filepath.Join(scriptDir, "deploy.sh"), []byte(fakeDeploy), 0o755), "test should install a fake deploy.sh")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "bash", fleetScriptPath, "fake-key", "test-tag", "app,worker")
	cmd.Env = append(os.Environ(),
		"DEPLOY_JOBS=2",
		"FAKE_DEPLOY_STATE="+stateDir,
		"FLEET_HOSTS=app:10.0.0.1,worker:10.0.0.2,db:10.0.0.3,egress:10.0.0.4",
	)
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, "deploy-fleet should complete when two matching hosts can run concurrently: %s", string(output))
	require.Contains(t, string(output), "Deploying 2 node(s), 2 at a time", "deploy-fleet should report bounded parallel fan-out")
	require.FileExists(t, filepath.Join(stateDir, "app-10.0.0.1.started"), "fake app deploy should have started")
	require.FileExists(t, filepath.Join(stateDir, "worker-10.0.0.2.started"), "fake worker deploy should have started")
	require.NoFileExists(t, filepath.Join(stateDir, "db-10.0.0.3.started"), "unrequested db deploy should not have started")
}

func TestDeployFleetSerializesDeploysToTheSameHost(t *testing.T) {
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
host_safe="${ip//[^A-Za-z0-9_.-]/_}"
lock="$state/host-$host_safe.lock"
if [ "$role" = "app" ]; then
  sleep 0.2
fi
if ! mkdir "$lock" 2>/dev/null; then
  echo "same host deployed concurrently: $ip" >&2
  exit 43
fi
trap 'rmdir "$lock"' EXIT
touch "$state/$role-$ip.started"
printf '%s@%s\n' "$role" "$ip" >> "$state/order.log"
sleep 0.4
echo "$role@$ip deployed by fake script"
`
	require.NoError(t, os.WriteFile(filepath.Join(scriptDir, "deploy.sh"), []byte(fakeDeploy), 0o755), "test should install a fake deploy.sh")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	cmd := exec.CommandContext(ctx, "bash", fleetScriptPath, "fake-key", "test-tag", "all")
	cmd.Env = append(os.Environ(),
		"DEPLOY_JOBS=3",
		"FAKE_DEPLOY_STATE="+stateDir,
		"FLEET_HOSTS=app:10.0.0.1,worker:10.0.0.1,redis:10.0.0.2",
	)
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, "deploy-fleet should serialize role deploys that target the same host: %s", string(output))
	require.FileExists(t, filepath.Join(stateDir, "app-10.0.0.1.started"), "same-host app deploy should have started")
	require.FileExists(t, filepath.Join(stateDir, "worker-10.0.0.1.started"), "same-host worker deploy should have started after the app deploy released the host")
	require.FileExists(t, filepath.Join(stateDir, "redis-10.0.0.2.started"), "different-host deploy should still be eligible for parallel execution")

	orderBytes, err := os.ReadFile(filepath.Join(stateDir, "order.log"))
	require.NoError(t, err, "test should read the fake deploy order log")
	order := strings.Split(strings.TrimSpace(string(orderBytes)), "\n")
	appIndex := indexOfString(order, "app@10.0.0.1")
	workerIndex := indexOfString(order, "worker@10.0.0.1")
	require.NotEqual(t, -1, appIndex, "order log should include the same-host app deploy")
	require.NotEqual(t, -1, workerIndex, "order log should include the same-host worker deploy")
	require.Less(t, appIndex, workerIndex, "same-host deploys should preserve FLEET_HOSTS order")
}

func TestDeployFleetPrintsFailedHostLogs(t *testing.T) {
	t.Parallel()

	tempDir := t.TempDir()
	scriptDir := filepath.Join(tempDir, "deploy", "scripts")
	require.NoError(t, os.MkdirAll(scriptDir, 0o755), "test should create a temporary deploy script directory")

	fleetScript, err := os.ReadFile("../deploy/scripts/deploy-fleet.sh")
	require.NoError(t, err, "test should read deploy-fleet.sh")
	fleetScriptPath := filepath.Join(scriptDir, "deploy-fleet.sh")
	require.NoError(t, os.WriteFile(fleetScriptPath, fleetScript, 0o755), "test should copy deploy-fleet.sh into the temporary layout")

	fakeDeploy := `#!/usr/bin/env bash
set -euo pipefail
role="$1"
ip="$2"
if [ "$role" = "worker" ]; then
  echo "remote gate said: config changed during routine deploy on $ip"
  exit 1
fi
echo "healthy $role deploy detail that must stay out of failure output"
`
	require.NoError(t, os.WriteFile(filepath.Join(scriptDir, "deploy.sh"), []byte(fakeDeploy), 0o755), "test should install a fake deploy.sh")

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	// Pre-seed the pinned log dir with a previous run's failure; the script
	// must clear it instead of re-dumping it alongside this run's failures.
	logDir := filepath.Join(tempDir, "fleet-logs")
	require.NoError(t, os.MkdirAll(logDir, 0o755), "test should create the pinned log dir")
	staleLog := filepath.Join(logDir, "app-9.9.9.9.log")
	require.NoError(t, os.WriteFile(staleLog, []byte("stale failure from a previous run\n"), 0o644), "test should seed a stale per-host log")
	require.NoError(t, os.WriteFile(staleLog+".failed", nil, 0o644), "test should seed a stale failure marker")

	summaryPath := filepath.Join(tempDir, "step-summary.md")
	cmd := exec.CommandContext(ctx, "bash", fleetScriptPath, "fake-key", "test-tag", "app,worker")
	cmd.Env = append(os.Environ(),
		"DEPLOY_JOBS=2",
		"FLEET_HOSTS=app:10.0.0.1,worker:10.0.0.2",
		"DEPLOY_FLEET_LOG_DIR="+logDir,
		"GITHUB_ACTIONS=true",
		"GITHUB_STEP_SUMMARY="+summaryPath,
	)
	output, err := cmd.CombinedOutput()
	require.Error(t, err, "deploy-fleet should exit non-zero when a host fails: %s", string(output))
	text := string(output)
	require.Contains(t, text, "remote gate said: config changed during routine deploy on 10.0.0.2", "fleet output should include the failed host's log so CI failures are introspectable")
	require.Contains(t, text, "::group::FAILED worker-10.0.0.2", "fleet output should wrap failed logs in a collapsible GitHub Actions group")
	require.Contains(t, text, "::stop-commands::", "fleet output should fence dumped remote log content so the runner does not interpret ::-prefixed lines as workflow commands")
	require.NotContains(t, text, "healthy app deploy detail", "fleet output should not dump logs of hosts that deployed cleanly")
	require.NotContains(t, text, "stale failure from a previous run", "fleet deploy should clear prior-run state from a reused pinned log dir before deploying")
	require.FileExists(t, filepath.Join(logDir, "worker-10.0.0.2.log"), "fleet deploy should write per-host logs into the pinned artifact dir")

	summary, err := os.ReadFile(summaryPath)
	require.NoError(t, err, "fleet deploy should append failures to the GitHub step summary")
	require.Contains(t, string(summary), "remote gate said: config changed during routine deploy on 10.0.0.2", "step summary should carry the failed host's log tail")
}

func indexOfString(values []string, target string) int {
	for i, value := range values {
		if value == target {
			return i
		}
	}
	return -1
}

func TestWorkerDeployPreflightTargetValidatesBlueGreenReadiness(t *testing.T) {
	t.Parallel()

	makefile, err := os.ReadFile("../Makefile")
	require.NoError(t, err, "test should read Makefile")
	makefileText := string(makefile)

	require.Contains(t, makefileText, "deploy-worker-preflight", "Makefile should expose a read-only worker deploy preflight target")
	require.Contains(t, makefileText, "./deploy/scripts/deploy-worker-preflight.sh", "preflight target should delegate validation to a dedicated script")
	require.Contains(t, makefileText, "WORKER_BLUE_GREEN_PORT_START", "preflight target should pass the configured worker blue/green port start")
	require.Contains(t, makefileText, "WORKER_BLUE_GREEN_PORT_END", "preflight target should pass the configured worker blue/green port end")

	preflightScript, err := os.ReadFile("../deploy/scripts/deploy-worker-preflight.sh")
	require.NoError(t, err, "test should read worker preflight script")
	preflightText := string(preflightScript)

	require.Contains(t, preflightText, "NODE_ID WORKER_PRIVATE_IP DB_HOST DB_PASSWORD", "preflight should validate required worker env values")
	require.Contains(t, preflightText, "WORKER_BLUE_GREEN_PORT_START and WORKER_BLUE_GREEN_PORT_END must be numeric", "preflight should validate numeric blue/green port range")
	require.Contains(t, preflightText, "SELECT COUNT(*) FROM preview_runtimes", "preflight should validate preview runtime endpoint ownership queries")
	require.Contains(t, preflightText, "No safe worker blue/green port found", "preflight should give an actionable message when routine deploy cannot find a safe endpoint")
	require.Contains(t, preflightText, "Use DEPLOY_MODE=maintenance only for disruptive host/runtime/support-service changes", "preflight should preserve the routine-vs-maintenance operator contract")
}

// Worker preview routing and sandbox orchestration require per-host values:
// NODE_ID, WORKER_PRIVATE_IP, PREVIEW_INTERNAL_BASE_URL, and DOCKER_GID. They live in
// /opt/143/.env.local (not the shared .env that gets rewritten on every
// deploy). If we ever stop writing or appending that file, every preview
// start will fail with PREVIEW_NO_WORKERS — guard the wiring here so that
// regression is loud at PR time, not at production start-preview time.
func TestWorkerPerHostIdentityIsPreservedAcrossDeploys(t *testing.T) {
	t.Parallel()

	compose, err := os.ReadFile("../docker-compose.worker.yml")
	require.NoError(t, err, "test should read the worker compose file")
	require.Contains(t, string(compose), "${WORKER_PRIVATE_IP:?", "worker compose should bind port 8080 to the worker's private IP and fail loudly if WORKER_PRIVATE_IP is unset (binding to 0.0.0.0 would expose the internal preview API)")
	require.Contains(t, string(compose), "${NODE_ID:?", "worker compose should require NODE_ID rather than silently falling back to a random container hostname")
	require.Contains(t, string(compose), "${PREVIEW_INTERNAL_BASE_URL:?", "worker compose should require PREVIEW_INTERNAL_BASE_URL — without it, parseWorkerNode rejects the node and StartPreview returns PREVIEW_NO_WORKERS")
	require.Contains(t, string(compose), "${DOCKER_GID:?", "worker compose should require the host docker group GID instead of defaulting to a distro-specific value that can block docker.sock access")
	require.Contains(t, string(compose), "${WORKER_HOST_PORT:-8080}:8080", "worker compose should publish a configurable host port so blue/green generations can overlap on one worker host")
	require.Contains(t, string(compose), "name: 143_default", "worker compose should attach every generation project to the shared default network so chrome remains reachable")

	cloudInit, err := os.ReadFile("../deploy/cloud-init/worker.yml")
	require.NoError(t, err, "test should read the worker cloud-init template")
	require.Contains(t, string(cloudInit), "/opt/143/.env.local", "worker cloud-init should write per-host identity to .env.local so deploy.sh can preserve it across deploys")
	require.Contains(t, string(cloudInit), "NODE_ID=${NODE_ID}", "worker cloud-init should populate NODE_ID in .env.local")
	require.Contains(t, string(cloudInit), "WORKER_PRIVATE_IP=${WORKER_PRIVATE_IP}", "worker cloud-init should populate WORKER_PRIVATE_IP in .env.local")
	require.Contains(t, string(cloudInit), "PREVIEW_INTERNAL_BASE_URL=${PREVIEW_INTERNAL_BASE_URL}", "worker cloud-init should populate PREVIEW_INTERNAL_BASE_URL in .env.local")
	require.Contains(t, string(cloudInit), `DOCKER_GID="$(getent group docker | cut -d: -f3)"`, "worker cloud-init should detect the host docker group GID for docker.sock access")
	require.Contains(t, string(cloudInit), `DOCKER_GID=%s`, "worker cloud-init should persist DOCKER_GID in .env.local")
	require.Contains(t, string(cloudInit), "cat /opt/143/.env.local >> /opt/143/.env", "worker cloud-init should concatenate .env.local into .env so docker compose can interpolate ${WORKER_PRIVATE_IP} etc.")

	provisionScript, err := os.ReadFile("../deploy/scripts/provision.sh")
	require.NoError(t, err, "test should read the provisioning script")
	require.Contains(t, string(provisionScript), "getent group docker", "provision.sh should detect the host docker group GID instead of relying on a hardcoded docker group id")
	require.Contains(t, string(provisionScript), "NODE_ID=%s", "provision.sh should write NODE_ID into .env.local")
	require.Contains(t, string(provisionScript), "WORKER_PRIVATE_IP=%s", "provision.sh should write WORKER_PRIVATE_IP into .env.local")
	require.Contains(t, string(provisionScript), "PREVIEW_INTERNAL_BASE_URL=%s", "provision.sh should write PREVIEW_INTERNAL_BASE_URL into .env.local")
	require.Contains(t, string(provisionScript), "DOCKER_GID=%s", "provision.sh should write DOCKER_GID into .env.local")
	require.Contains(t, string(provisionScript), "/opt/143/.env.local", "provision.sh should target /opt/143/.env.local")
	require.Contains(t, string(provisionScript), "cat /opt/143/.env.local >> /opt/143/.env", "provision.sh should concatenate .env.local into .env after writing both")

	deployScript, err := os.ReadFile("../deploy/scripts/deploy.sh")
	require.NoError(t, err, "test should read the deploy script")
	require.Contains(t, string(deployScript), `DOCKER_GID="$(getent group docker | cut -d: -f3)"`, "deploy.sh should backfill DOCKER_GID for workers provisioned before the value was written to .env.local")
	require.Contains(t, string(deployScript), "cat /opt/143/.env.local >> /opt/143/.env", "deploy.sh worker branch should re-append .env.local into .env on every deploy — without this, every secret refresh wipes the per-host identity")
	require.Contains(t, string(deployScript), "/opt/143/.env.local is missing", "deploy.sh worker branch should abort loudly when .env.local is missing instead of coming up with empty NODE_ID, WORKER_PRIVATE_IP, or DOCKER_GID")
}

func TestWorkerDependencyCacheL1UsesHostBackedPath(t *testing.T) {
	t.Parallel()

	compose, err := os.ReadFile("../docker-compose.worker.yml")
	require.NoError(t, err, "test should read the worker compose file")
	composeText := string(compose)

	require.Contains(t, composeText, "PREVIEW_DEPENDENCY_CACHE_LOCAL_DIR: ${PREVIEW_DEPENDENCY_CACHE_LOCAL_DIR:-/var/cache/143/preview-dependency-cache}", "worker compose should default the dependency cache L1 to a host-backed path")
	require.Contains(t, composeText, "/var/cache/143/preview-dependency-cache:/var/cache/143/preview-dependency-cache", "worker compose should bind-mount the default L1 path so local cache blobs survive worker container recreation")

	provisionScript, err := os.ReadFile("../deploy/scripts/provision.sh")
	require.NoError(t, err, "test should read the provisioning script")
	provisionText := string(provisionScript)
	require.Contains(t, provisionText, "mkdir -p /var/cache/143/preview-dependency-cache", "worker provisioning should create the host dependency cache directory before compose starts")
	require.Contains(t, provisionText, "chown 1000:1000 /var/cache/143/preview-dependency-cache", "worker provisioning should make the host dependency cache directory writable by appuser in the worker container")
	require.Contains(t, provisionText, "chmod 0750 /var/cache/143/preview-dependency-cache", "worker provisioning should keep the host dependency cache directory private to the worker runtime user")

	cloudInit, err := os.ReadFile("../deploy/cloud-init/worker.yml")
	require.NoError(t, err, "test should read the worker cloud-init template")
	cloudInitText := string(cloudInit)
	require.Contains(t, cloudInitText, "mkdir -p /var/cache/143/preview-dependency-cache", "worker cloud-init should create the host dependency cache directory before first compose startup")
	require.Contains(t, cloudInitText, "chown 1000:1000 /var/cache/143/preview-dependency-cache", "worker cloud-init should make the host dependency cache directory writable by appuser")
	require.Contains(t, cloudInitText, "chmod 0750 /var/cache/143/preview-dependency-cache", "worker cloud-init should keep the host dependency cache directory private")

	reconcileScript, err := os.ReadFile("../deploy/scripts/reconcile-worker-host.sh")
	require.NoError(t, err, "test should read the worker reconcile script")
	reconcileText := string(reconcileScript)
	require.Contains(t, reconcileText, "mkdir -p /var/cache/143/preview-dependency-cache", "worker reconcile should create the host dependency cache directory so pre-#1342 hosts heal on deploy")
	require.Contains(t, reconcileText, "chown 1000:1000 /var/cache/143/preview-dependency-cache", "worker reconcile should repair dependency cache directory ownership drift")
	require.Contains(t, reconcileText, "chmod 0750 /var/cache/143/preview-dependency-cache", "worker reconcile should keep the dependency cache directory private")
}

func TestWorkerDeployUsesBlueGreenGenerations(t *testing.T) {
	t.Parallel()

	deployScript, err := os.ReadFile("../deploy/scripts/deploy.sh")
	require.NoError(t, err, "test should read deploy.sh")
	deploy := string(deployScript)

	require.Contains(t, deploy, "deploy_worker_blue_green", "worker deploy should use a blue/green generation rollout")
	require.Contains(t, deploy, "start_worker_generation", "worker deploy should start the new generation before draining old containers")
	require.Contains(t, deploy, `preflight_node_id="$(first_running_worker_node_id "$old_containers" || true)"`, "worker deploy preflight should use the existing worker node id instead of the host's physical hostname")
	require.Contains(t, deploy, `--node-id "$preflight_node_id"`, "worker deploy preflight should load status by node id")
	require.Contains(t, deploy, `wait_worker_db_heartbeat "$node_id"`, "worker deploy should verify the green generation has registered a fresh DB heartbeat before draining blue")
	require.Contains(t, deploy, "Rolling back worker generation ${new_cid:0:12} after DB heartbeat readiness failure", "worker deploy should clean up green if DB heartbeat readiness fails")
	require.Contains(t, deploy, "Rolling back worker generation ${new_cid:0:12} after preview RPC auth compatibility failure", "worker deploy should clean up green if preview RPC auth compatibility fails")
	require.Contains(t, deploy, "drain_old_worker_containers", "worker deploy should drain old worker containers after the new generation is healthy")
	require.Contains(t, deploy, "run_ctl expire-budget", "worker deploy should mark over-budget blue executors for deploy-specific checkpoint/requeue")
	require.Contains(t, deploy, "--reason \"$reason\"", "worker deploy should pass the deploy reason into budget-expiry audit events")
	require.Contains(t, deploy, `run_worker_deployctl mark-draining`, "post-green drain control should run from a deploy-control helper instead of depending on the green worker container")
	require.Contains(t, deploy, `run_ctl retire-ready --node-id "$node_id" --json`, "retire polling should use a stable deploy-control helper so later worker generations cannot orphan older drains")
	require.NotContains(t, deploy, `docker exec -e "IMAGE_TAG=$build_sha" "$ctl_cid" /bin/worker-deployctl retire-ready`, "retire polling must not depend on a worker generation that future deploys may stop")
	require.Contains(t, deploy, "WORKER_BLUE_GREEN_PORT_START", "worker deploy should allocate worker generation ports from a configurable range")
	require.Contains(t, deploy, "WORKER_HOST_PORT", "worker deploy should pass the allocated host port into docker compose")
	require.Contains(t, deploy, `local end="${WORKER_BLUE_GREEN_PORT_END:-$start}"`, "worker deploy should default to the existing worker port only unless operators explicitly open a blue/green range")
	require.Contains(t, deploy, "app-to-worker network must allow every configured worker blue/green port", "worker deploy should warn operators that app nodes must be able to reach every advertised worker generation port")
	require.Contains(t, deploy, "worker_runtime_endpoint_in_use", "worker deploy should check preview runtime DB ownership before reusing a worker endpoint")
	require.Contains(t, deploy, "load_worker_endpoint_check_env", "synchronous worker deploy should load DB endpoint-check credentials before selecting a routine port")
	require.Contains(t, deploy, "FROM preview_runtimes WHERE endpoint_url", "worker deploy should query active preview runtime endpoints before selecting a generation port")
	require.Contains(t, deploy, "status IN ('starting', 'ready', 'draining')", "worker deploy should treat starting, ready, and draining preview runtimes as endpoint owners")
	require.Contains(t, deploy, `find_free_worker_port "$worker_private_ip"`, "worker deploy should pass the worker private IP into port selection so endpoint URLs match runtime routing")
	require.Contains(t, deploy, "refusing to reuse it", "worker deploy should fail closed when runtime endpoint ownership cannot be verified")
	require.NotContains(t, deploy, "falling back to blocking worker drain", "routine worker deploy should not interrupt old workers when no extra blue/green port is configured")
	require.Contains(t, deploy, "routine blue/green deploy refuses blocking drain fallback", "worker deploy should explain when it cannot do zero-interruption blue/green without an extra reachable port")
}

func TestDeployRunsPreviewRPCAuthCheckBeforeCutoverAndWorkerDrain(t *testing.T) {
	t.Parallel()

	deployScript, err := os.ReadFile("../deploy/scripts/deploy.sh")
	require.NoError(t, err, "test should read deploy.sh")
	deployText := string(deployScript)
	rollingBody := extractShellFunction(t, deployText, "rolling_deploy_service", "resolve_worker_drain_timeout_seconds")
	workerBody := extractShellFunction(t, deployText, "deploy_worker_blue_green", "dump_diagnostics")

	cliSource, err := os.ReadFile("../cmd/worker-deployctl/main.go")
	require.NoError(t, err, "test should read worker-deployctl source")
	require.Contains(t, string(cliSource), `case "preview-auth-check":`, "worker-deployctl should expose a preview RPC auth compatibility probe")
	require.Contains(t, string(cliSource), "runPreviewAuthCheck", "preview-auth-check should have a dedicated CLI implementation")

	appProbeIndex := strings.Index(rollingBody, `preview_rpc_auth_preflight "$new_container"`)
	require.NotEqual(t, -1, appProbeIndex, "app rolling deploy should run the preview RPC auth probe from the new api container")
	previewAuthPreflightBody := extractShellFunction(t, deployText, "preview_rpc_auth_preflight", "rolling_deploy_service")
	require.Contains(t, previewAuthPreflightBody, `docker exec "$cid" /docker-entrypoint.sh /bin/worker-deployctl preview-auth-check --json`, "app preview RPC auth probe should run through docker-entrypoint.sh so SOPS-decrypted production secrets are available to worker-deployctl")
	appDrainIndex := strings.Index(rollingBody, `echo "Draining $old_count old $service container(s)`)
	require.NotEqual(t, -1, appDrainIndex, "app rolling deploy should still drain old containers")
	require.Less(t, appProbeIndex, appDrainIndex, "app rolling deploy should run preview RPC auth check before draining old api containers")

	workerProbeIndex := strings.Index(workerBody, `run_worker_deployctl preview-auth-check --node-id "$node_id" --json`)
	require.NotEqual(t, -1, workerProbeIndex, "worker blue/green deploy should run preview RPC auth check against the candidate node")
	heartbeatIndex := strings.Index(workerBody, `wait_worker_db_heartbeat "$node_id"`)
	require.NotEqual(t, -1, heartbeatIndex, "worker blue/green deploy should wait for the green generation heartbeat")
	workerDrainIndex := strings.Index(workerBody, `drain_old_worker_containers "$new_cid" "$old_containers" "$deploy_id"`)
	require.NotEqual(t, -1, workerDrainIndex, "worker blue/green deploy should drain old worker containers")
	require.Less(t, heartbeatIndex, workerProbeIndex, "worker deploy should only probe after the green generation is registered")
	require.Less(t, workerProbeIndex, workerDrainIndex, "worker deploy should run preview RPC auth check before draining old worker containers")
}

func TestWorkerBlueGreenPreflightChecksCapacitySchemaAndSupportServices(t *testing.T) {
	t.Parallel()

	deployScript, err := os.ReadFile("../deploy/scripts/deploy.sh")
	require.NoError(t, err, "test should read deploy.sh")
	deploy := string(deployScript)

	require.Contains(t, deploy, "worker_host_capacity_preflight", "worker deploy should run a local CPU/memory capacity preflight before starting green")
	require.Contains(t, deploy, "WORKER_BLUE_GREEN_MIN_FREE_MEMORY_MB", "worker deploy should let operators set the minimum free memory needed for temporary worker overlap")
	require.Contains(t, deploy, "WORKER_BLUE_GREEN_MIN_IDLE_CPU_MILLIS", "worker deploy should let operators set the minimum idle CPU budget needed for temporary worker overlap")
	require.Contains(t, deploy, "WORKER_BLUE_GREEN_PREFLIGHT_ATTEMPTS", "worker deploy should retry transient capacity preflight failures before failing a routine rollout")
	require.Contains(t, deploy, "worker_support_service_fingerprint", "worker deploy should fingerprint support-service config inputs during preflight")
	require.Contains(t, deploy, "worker_process_config_fingerprint", "worker deploy should separately fingerprint worker-process config inputs during preflight")
	require.Contains(t, deploy, "worker_host_runtime_fingerprint", "worker deploy should separately fingerprint host runtime inputs during preflight")
	require.Contains(t, deploy, "worker_docker_daemon_fingerprint", "worker deploy should separately fingerprint docker daemon inputs during preflight")
	require.Contains(t, deploy, "--support-services-fingerprint", "worker deploy preflight should pass support-service fingerprints into worker-deployctl")
	require.Contains(t, deploy, "--worker-process-fingerprint", "worker deploy preflight should pass worker-process fingerprints into worker-deployctl")
	require.Contains(t, deploy, "--host-runtime-fingerprint", "worker deploy preflight should pass host-runtime fingerprints into worker-deployctl")
	require.Contains(t, deploy, "--docker-daemon-fingerprint", "worker deploy preflight should pass docker-daemon fingerprints into worker-deployctl")
	require.Contains(t, deploy, "--expected-schema-version", "worker deploy preflight should pass the expected migration/schema version into worker-deployctl")
	require.Contains(t, deploy, "impact --node-id", "worker deploy should emit a dry-run impact report for the old generation before routine drain")
}

func TestWorkerDeployFingerprintsAreSeparatedByBlastRadius(t *testing.T) {
	t.Parallel()

	deployScript, err := os.ReadFile("../deploy/scripts/deploy.sh")
	require.NoError(t, err, "test should read deploy.sh")
	deploy := string(deployScript)

	workerProcess := extractShellFunction(t, deploy, "worker_process_config_fingerprint", "worker_support_service_fingerprint")
	supportServices := extractShellFunction(t, deploy, "worker_support_service_fingerprint", "worker_host_runtime_fingerprint")
	hostRuntime := extractShellFunction(t, deploy, "worker_host_runtime_fingerprint", "worker_docker_daemon_fingerprint")
	dockerDaemon := extractShellFunction(t, deploy, "worker_docker_daemon_fingerprint", "ensure_routine_worker_fingerprints_compatible")

	require.Contains(t, workerProcess, "compose_service_fingerprint", "worker process fingerprint should be based on the worker service block")
	require.Contains(t, workerProcess, "docker-compose.worker.yml", "worker process fingerprint should include the worker compose source")
	require.Contains(t, workerProcess, "worker", "worker process fingerprint should target the worker service")

	require.NotContains(t, supportServices, "fingerprint_files \\\n        /opt/143/docker-compose.worker.yml", "support-service fingerprint should not hash the entire worker compose file")
	require.Contains(t, supportServices, "$WORKER_SUPPORT_SERVICE_COMPOSE_SERVICES", "support-service fingerprint should hash the shared support compose service list")
	require.Contains(t, supportServices, "$WORKER_SUPPORT_SERVICE_FINGERPRINT_FILES", "support-service fingerprint should hash the shared support file list")

	supportComposeServices := extractFingerprintListVar(t, deploy, "WORKER_SUPPORT_SERVICE_COMPOSE_SERVICES")
	require.Contains(t, supportComposeServices, "chrome", "support-service fingerprint should include the chrome service block")
	require.Contains(t, supportComposeServices, "gvisor-check", "support-service fingerprint should include the gVisor check service block")
	require.Contains(t, supportComposeServices, "sandbox-dns", "support-service fingerprint should include the sandbox DNS service block")
	supportFiles := extractFingerprintListVar(t, deploy, "WORKER_SUPPORT_SERVICE_FINGERPRINT_FILES")
	require.Contains(t, supportFiles, "Dockerfile.dnsmasq", "support-service fingerprint should include the dnsmasq Dockerfile")
	require.Contains(t, supportFiles, "docker-compose.dns-probe.yml", "support-service fingerprint should include the DNS probe compose file")

	require.Contains(t, hostRuntime, "$WORKER_HOST_RUNTIME_FINGERPRINT_FILES", "host-runtime fingerprint should hash the shared host-runtime file list")
	hostRuntimeFiles := extractFingerprintListVar(t, deploy, "WORKER_HOST_RUNTIME_FINGERPRINT_FILES")
	require.Contains(t, hostRuntimeFiles, "reconcile-worker-host.sh", "host-runtime fingerprint should include worker host reconciliation")
	require.Contains(t, hostRuntimeFiles, "sandbox-firewall.sh", "host-runtime fingerprint should include sandbox firewall rules")
	require.Contains(t, hostRuntimeFiles, "sandbox-resolv-conf.sh", "host-runtime fingerprint should include sandbox resolv.conf generation")
	require.Contains(t, hostRuntimeFiles, "install-static-egress-worker.sh", "host-runtime fingerprint should include static egress WireGuard installation")

	require.Contains(t, dockerDaemon, "$WORKER_DOCKER_DAEMON_FINGERPRINT_FILES", "docker-daemon fingerprint should hash the shared docker-daemon file list")
	dockerDaemonFiles := extractFingerprintListVar(t, deploy, "WORKER_DOCKER_DAEMON_FINGERPRINT_FILES")
	require.Contains(t, dockerDaemonFiles, "install-docker-dns.sh", "docker-daemon fingerprint should include Docker DNS installation")
	require.Contains(t, dockerDaemonFiles, "install-log-rotation.sh", "docker-daemon fingerprint should include Docker log rotation installation")
}

// extractFingerprintListVar returns the canonical (single, top-level)
// definition of one of the shared worker fingerprint input lists. The
// negative character class skips the detached-rollover heredoc line that
// re-binds the variable to itself ('$WORKER_...').
func extractFingerprintListVar(t *testing.T, deployText, name string) string {
	t.Helper()

	re := regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(name) + `='([^'$][^']*)'$`)
	matches := re.FindAllStringSubmatch(deployText, -1)
	require.Len(t, matches, 1, "deploy.sh should define %s exactly once as the canonical fingerprint input list", name)
	return matches[0][1]
}

// Regression test for the routine-deploy failure loop: the staged fingerprint
// gate and the blue/green rollover each used to carry their own hardcoded
// fingerprint input lists. When the lists diverged (install-static-egress-
// worker.sh was added to the gate's host-runtime list but not the rollover's),
// every routine deploy failed with "host-runtime config changed": the gate
// repaired the on-host baseline to a hash the rollover never computed, and a
// maintenance deploy wrote the rollover's hash back, re-arming the failure.
func TestWorkerFingerprintInputListsAreSharedBetweenGateAndRollover(t *testing.T) {
	t.Parallel()

	deployScript, err := os.ReadFile("../deploy/scripts/deploy.sh")
	require.NoError(t, err, "test should read deploy.sh")
	deploy := string(deployScript)

	gate := extractTopLevelShellBlock(t, deploy, "fingerprint_candidate_files", "\nREMOTE\n}")
	rollover := extractShellFunction(t, deploy, "worker_support_service_fingerprint", "ensure_routine_worker_fingerprints_compatible")

	listVars := []string{
		"WORKER_HOST_RUNTIME_FINGERPRINT_FILES",
		"WORKER_DOCKER_DAEMON_FINGERPRINT_FILES",
		"WORKER_SUPPORT_SERVICE_FINGERPRINT_FILES",
		"WORKER_SUPPORT_SERVICE_COMPOSE_SERVICES",
	}
	for _, name := range listVars {
		extractFingerprintListVar(t, deploy, name)
		require.Contains(t, gate, "$"+name, "staged fingerprint gate should consume the shared %s list", name)
		require.Contains(t, rollover, "$"+name, "blue/green rollover fingerprints should consume the shared %s list", name)
		require.Equal(t, 2, strings.Count(deploy, "remote_env_assignment "+name+" "), "both the gate and the main remote payload should receive %s over SSH", name)
		require.Contains(t, deploy, name+"='$"+name+"'", "detached rollover script should bake the shared %s list because it runs in a fresh process", name)
	}
}

// The staged gate repairs the persisted fingerprint baseline using
// fingerprint_candidate_files while the rollover recomputes it with
// fingerprint_files. The repaired baseline only satisfies the rollover if the
// two implementations hash identical files to identical digests — pin that
// here so a format change in either copy fails at PR time, not on the fleet.
func TestGateAndRolloverFingerprintImplementationsAgree(t *testing.T) {
	t.Parallel()

	deployScript, err := os.ReadFile("../deploy/scripts/deploy.sh")
	require.NoError(t, err, "test should read deploy.sh")
	deploy := string(deployScript)

	gateFn := extractTopLevelShellFunction(t, deploy, "fingerprint_candidate_files", "fingerprint_active_files")
	rolloverFn := extractShellFunction(t, deploy, "fingerprint_files", "compose_service_fingerprint")

	tmpDir := t.TempDir()
	first := filepath.Join(tmpDir, "reconcile-worker-host.sh")
	second := filepath.Join(tmpDir, "sandbox-firewall.sh")
	require.NoError(t, os.WriteFile(first, []byte("echo reconcile\n"), 0o755), "test should seed first fingerprinted file")
	require.NoError(t, os.WriteFile(second, []byte("echo firewall\n"), 0o755), "test should seed second fingerprinted file")

	script := gateFn + rolloverFn + `
set -euo pipefail
gate="$(fingerprint_candidate_files "$FILE_ONE" "$FILE_TWO")"
rollover="$(fingerprint_files "$FILE_ONE" "$FILE_TWO")"
printf '%s\n%s\n' "$gate" "$rollover"
`

	cmd := exec.Command("bash", "-c", script)
	cmd.Env = append(os.Environ(), "FILE_ONE="+first, "FILE_TWO="+second)
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, "fingerprint implementations should execute successfully: %s", output)

	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	require.Len(t, lines, 2, "comparison script should print both fingerprints")
	require.Len(t, lines[0], 64, "gate fingerprint should be a sha256 hex digest")
	require.Equal(t, lines[0], lines[1], "gate and rollover must compute identical fingerprints for identical files or routine deploys fail on a repaired baseline")
}

func TestStagedFingerprintIgnoresCandidateFilename(t *testing.T) {
	t.Parallel()

	deployScript, err := os.ReadFile("../deploy/scripts/deploy.sh")
	require.NoError(t, err, "test should read deploy.sh")
	functionBody := extractTopLevelShellFunction(t, string(deployScript), "fingerprint_candidate_files", "compose_service_fingerprint")

	tmpDir := t.TempDir()
	supportFile := filepath.Join(tmpDir, "support.conf")
	script := functionBody + `
set -euo pipefail
printf 'same support-service config\n' > "$SUPPORT_FILE"
active="$(fingerprint_candidate_files "$SUPPORT_FILE")"
cp "$SUPPORT_FILE" "$SUPPORT_FILE.new"
candidate="$(fingerprint_candidate_files "$SUPPORT_FILE")"
printf '%s\n%s\n' "$active" "$candidate"
`

	cmd := exec.Command("bash", "-c", script)
	cmd.Env = append(os.Environ(), "SUPPORT_FILE="+supportFile)
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, "staged fingerprint helper should execute successfully: %s", output)

	lines := strings.Split(strings.TrimSpace(string(output)), "\n")
	require.Equal(t, []string{lines[0], lines[0]}, lines, "staging an identical .new file should not change the candidate fingerprint")
}

func TestStagedFingerprintGateRepairsStaleBaselineWhenCandidateMatchesActive(t *testing.T) {
	t.Parallel()

	deployScript, err := os.ReadFile("../deploy/scripts/deploy.sh")
	require.NoError(t, err, "test should read deploy.sh")
	gateScript := extractTopLevelShellBlock(t, string(deployScript), "fingerprint_candidate_files", "\nREMOTE\n}")

	tmpDir := t.TempDir()
	gateScript = strings.ReplaceAll(gateScript, "/opt/143", tmpDir)
	require.NoError(t, os.MkdirAll(filepath.Join(tmpDir, "deploy", "scripts"), 0o755), "test should create deploy script directory")

	compose := `services:
  chrome:
    image: chromedp/headless-shell:latest
  gvisor-check:
    image: docker:27-cli
  sandbox-dns:
    image: 143-sandbox-dns:local
  worker:
    image: ghcr.io/assembledhq/143-server:${IMAGE_TAG:-latest}
`
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "docker-compose.worker.yml"), []byte(compose), 0o644), "test should seed active worker compose")
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "docker-compose.worker.yml.new"), []byte(compose), 0o644), "test should seed identical staged worker compose")
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "Dockerfile.dnsmasq"), []byte("FROM alpine:3.20\n"), 0o644), "test should seed active dnsmasq Dockerfile")
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "Dockerfile.dnsmasq.new"), []byte("FROM alpine:3.20\n"), 0o644), "test should seed identical staged dnsmasq Dockerfile")
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "docker-compose.dns-probe.yml"), []byte("services:\n  dns-probe:\n    image: alpine:3.20\n"), 0o644), "test should seed active DNS probe compose")
	require.NoError(t, os.WriteFile(filepath.Join(tmpDir, "docker-compose.dns-probe.yml.new"), []byte("services:\n  dns-probe:\n    image: alpine:3.20\n"), 0o644), "test should seed identical staged DNS probe compose")

	fingerprintFile := filepath.Join(tmpDir, ".worker-support-services.v2.fingerprint")
	require.NoError(t, os.WriteFile(fingerprintFile, []byte("stale-baseline\n"), 0o644), "test should seed a stale persisted support fingerprint")

	cmd := exec.Command("bash", "-c", gateScript)
	cmd.Env = append(os.Environ(), "DEPLOY_MODE=routine")
	// The gate reads the shared fingerprint input lists from its environment
	// (deploy.sh passes them over SSH); feed it the real lists, retargeted at
	// the temp directory.
	for _, name := range []string{
		"WORKER_HOST_RUNTIME_FINGERPRINT_FILES",
		"WORKER_DOCKER_DAEMON_FINGERPRINT_FILES",
		"WORKER_SUPPORT_SERVICE_FINGERPRINT_FILES",
		"WORKER_SUPPORT_SERVICE_COMPOSE_SERVICES",
	} {
		value := extractFingerprintListVar(t, string(deployScript), name)
		cmd.Env = append(cmd.Env, name+"="+strings.ReplaceAll(value, "/opt/143", tmpDir))
	}
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, "staged fingerprint gate should allow stale baseline repair when candidate matches active files: %s", output)
	require.Contains(t, string(output), "stored worker support-service fingerprint is stale", "gate should explain stale support fingerprint repair")

	repaired, err := os.ReadFile(fingerprintFile)
	require.NoError(t, err, "test should read repaired support fingerprint")
	require.NotEqual(t, "stale-baseline\n", string(repaired), "gate should update stale support fingerprint metadata")
	require.Len(t, strings.TrimSpace(string(repaired)), 64, "repaired support fingerprint should be a sha256 hex digest")
}

func TestRoutineWorkerDeployBlocksOnlyRuntimeFingerprints(t *testing.T) {
	t.Parallel()

	deployScript, err := os.ReadFile("../deploy/scripts/deploy.sh")
	require.NoError(t, err, "test should read deploy.sh")
	deploy := string(deployScript)

	compatibility := extractShellFunction(t, deploy, "ensure_routine_worker_fingerprints_compatible", "worker_expected_schema_version")

	require.Contains(t, compatibility, `.worker-support-services.v2.fingerprint`, "routine compatibility should persist semantic support-service fingerprints separately")
	require.Contains(t, compatibility, `.worker-host-runtime.fingerprint`, "routine compatibility should persist host-runtime fingerprints separately")
	require.Contains(t, compatibility, `.worker-docker-daemon.fingerprint`, "routine compatibility should persist docker-daemon fingerprints separately")
	require.Contains(t, compatibility, `.worker-process.fingerprint`, "routine compatibility should persist worker-process fingerprints for preflight reporting")
	require.Contains(t, compatibility, `support-service config changed during routine deploy`, "routine deploy should block support-service changes")
	require.Contains(t, compatibility, `worker host-runtime config changed during routine deploy`, "routine deploy should block host-runtime changes")
	require.Contains(t, compatibility, `worker docker-daemon config changed during routine deploy`, "routine deploy should block docker-daemon changes")
	require.NotContains(t, compatibility, `"worker process config changed during routine deploy"`, "routine deploy should allow worker-process config changes through blue-green")
}

func TestWorkerDeployGatesStagedRuntimeFilesBeforeApplying(t *testing.T) {
	t.Parallel()

	deployScript, err := os.ReadFile("../deploy/scripts/deploy.sh")
	require.NoError(t, err, "test should read deploy.sh")
	deploy := string(deployScript)

	gateIndex := strings.Index(deploy, "run_worker_staged_fingerprint_gate\n  ssh \"${SSH_OPTS[@]}\" deploy@\"$HOST\" \\\n    \"mv /opt/143/$COMPOSE_FILE.new")
	stageWorkerComposeIndex := strings.Index(deploy, `"$PROJECT_DIR/$COMPOSE_FILE" deploy@"$HOST":/opt/143/"$COMPOSE_FILE".new`)
	stageDNSProbeIndex := strings.Index(deploy, `"$PROJECT_DIR/docker-compose.dns-probe.yml" deploy@"$HOST":/opt/143/docker-compose.dns-probe.yml.new`)
	promoteWorkerComposeIndex := strings.Index(deploy, `mv /opt/143/$COMPOSE_FILE.new /opt/143/$COMPOSE_FILE`)
	promoteDNSProbeIndex := strings.Index(deploy, `mv /opt/143/docker-compose.dns-probe.yml.new /opt/143/docker-compose.dns-probe.yml`)
	promoteFirewallIndex := strings.Index(deploy, "mv /opt/143/deploy/scripts/sandbox-firewall.sh.new")
	promoteResolvIndex := strings.Index(deploy, "mv /opt/143/deploy/scripts/sandbox-resolv-conf.sh.new")
	promoteReconcileIndex := strings.Index(deploy, "mv /opt/143/deploy/scripts/reconcile-worker-host.sh.new")
	reconcileIndex := strings.Index(deploy, "Reconciling worker host invariants")

	require.NotEqual(t, -1, stageWorkerComposeIndex, "worker deploy should stage worker compose before the routine fingerprint gate")
	require.NotEqual(t, -1, stageDNSProbeIndex, "worker deploy should stage dns-probe compose before the routine fingerprint gate")
	require.NotEqual(t, -1, gateIndex, "worker deploy should gate staged host/runtime files before applying them")
	require.NotEqual(t, -1, promoteWorkerComposeIndex, "worker deploy should promote worker compose after the staged gate")
	require.NotEqual(t, -1, promoteDNSProbeIndex, "worker deploy should promote dns-probe compose after the staged gate")
	require.NotEqual(t, -1, promoteFirewallIndex, "worker deploy should promote sandbox-firewall after the staged gate")
	require.NotEqual(t, -1, promoteResolvIndex, "worker deploy should promote sandbox-resolv-conf after the staged gate")
	require.NotEqual(t, -1, promoteReconcileIndex, "worker deploy should promote reconcile-worker-host after the staged gate")
	require.NotEqual(t, -1, reconcileIndex, "worker deploy should reconcile host invariants after the staged gate")
	require.Less(t, stageWorkerComposeIndex, gateIndex, "worker compose should be staged before the staged fingerprint gate")
	require.Less(t, stageDNSProbeIndex, gateIndex, "dns-probe compose should be staged before the staged fingerprint gate")
	require.Less(t, gateIndex, promoteWorkerComposeIndex, "staged fingerprint gate should run before promoting worker compose")
	require.Less(t, gateIndex, promoteDNSProbeIndex, "staged fingerprint gate should run before promoting dns-probe compose")
	require.Less(t, gateIndex, promoteFirewallIndex, "staged fingerprint gate should run before promoting sandbox-firewall")
	require.Less(t, gateIndex, promoteResolvIndex, "staged fingerprint gate should run before promoting sandbox-resolv-conf")
	require.Less(t, gateIndex, promoteReconcileIndex, "staged fingerprint gate should run before promoting reconcile-worker-host")
	require.Less(t, gateIndex, reconcileIndex, "staged fingerprint gate should run before executing worker host reconciliation")
}

func TestWorkerCapacityPreflightMeasuresIdleCPUMillicores(t *testing.T) {
	t.Parallel()

	deployScript, err := os.ReadFile("../deploy/scripts/deploy.sh")
	require.NoError(t, err, "test should read deploy.sh")
	preflight := extractShellFunction(t, string(deployScript), "worker_host_capacity_preflight", "fingerprint_files")

	require.Contains(t, preflight, "cpu_count", "worker capacity preflight should detect the number of online CPUs")
	require.Contains(t, preflight, "* 1000 * cpu_count", "worker capacity preflight should convert idle CPU fraction into host-level millicores")
	require.NotContains(t, preflight, "* 1000) / delta", "worker capacity preflight should not treat idle CPU percent as millicores")
}

func TestWorkerComposeCapsDatabasePool(t *testing.T) {
	t.Parallel()

	compose, err := os.ReadFile("../docker-compose.worker.yml")
	require.NoError(t, err, "test should read worker compose")
	composeText := string(compose)

	require.Contains(t, composeText, "pool_max_conns=${WORKER_DATABASE_POOL_MAX_CONNS:-4}", "worker generations should cap pgx pool size so blue/green overlap cannot exhaust Postgres connections")
}

func TestWorkerDeployProtectsActiveExecutorImages(t *testing.T) {
	t.Parallel()

	deployScript, err := os.ReadFile("../deploy/scripts/deploy.sh")
	require.NoError(t, err, "test should read deploy.sh")
	deploy := string(deployScript)

	require.Contains(t, deploy, "protect_active_executor_images", "worker deploy should record active executor image retention before pruning")
	require.Contains(t, deploy, "worker-deployctl retain-images", "worker deploy should use DB-backed image retention before docker image prune")
	require.Contains(t, deploy, "worker-deployctl release-retained-images", "worker deploy should release expired image retention records after pruning")
}

func TestWorkerSpinDownScriptDrainsBeforeClearingHost(t *testing.T) {
	t.Parallel()

	script, err := os.ReadFile("../deploy/scripts/spin-down-worker.sh")
	require.NoError(t, err, "test should read the worker spin-down script")
	text := string(script)

	require.Contains(t, text, "docker kill --signal=TERM", "worker spin-down should request worker drain before stopping support services")
	require.Contains(t, text, "wait_for_stopped \"worker\" \"$WORKER_DRAIN_TIMEOUT_SECONDS\"", "worker spin-down should bound the worker drain with the drain timeout")
	require.Contains(t, text, "docker stop -t \"$EXECUTOR_DRAIN_TIMEOUT_SECONDS\"", "worker spin-down should bound the executor drain with Docker's stop timeout")
	require.Contains(t, text, "label=com.143.role=session-executor", "worker spin-down should include durable session executor containers in cleanup")
	require.Contains(t, text, "label=com.assembledhq.143.managed=true", "worker spin-down should include managed sandbox containers in cleanup")
	require.Contains(t, text, "docker compose -f docker-compose.worker.yml down", "worker spin-down should stop worker compose services after worker drain")
	require.Contains(t, text, "CLEAR_MACHINE=1", "destructive machine cleanup should require an explicit clear flag")
	require.Contains(t, text, "docker volume prune -f", "explicit machine cleanup should reclaim unused Docker volumes")
	require.Contains(t, text, "docker system prune -af", "explicit machine cleanup should reclaim unused Docker images and build cache")

	makefile, err := os.ReadFile("../Makefile")
	require.NoError(t, err, "test should read the Makefile")
	require.Contains(t, string(makefile), "spin-down-worker", "Makefile should expose the worker spin-down script as an operator target")
	require.Contains(t, string(makefile), "./deploy/scripts/spin-down-worker.sh", "Makefile target should invoke the worker spin-down script")
	require.Contains(t, string(makefile), "--timeout $(TIMEOUT)", "Makefile target should expose the worker drain timeout")
	require.Contains(t, string(makefile), "--executor-timeout $(EXECUTOR_TIMEOUT)", "Makefile target should expose the session executor drain timeout")
}

func TestWorkerRuntimeEndpointQueryUsesPsqlStdinVariables(t *testing.T) {
	t.Parallel()

	deployScript, err := os.ReadFile("../deploy/scripts/deploy.sh")
	require.NoError(t, err, "test should read deploy.sh")
	deploy := string(deployScript)

	require.Contains(t, deploy, `printf '%s\n' "$query" | docker run -i --rm`, "worker endpoint ownership query should be fed on stdin so psql variable interpolation is applied")
	require.Contains(t, deploy, `-v endpoint="$endpoint"`, "worker endpoint ownership query should bind the generated endpoint as a psql variable")
	require.Contains(t, deploy, `endpoint_url = :'endpoint'`, "worker endpoint ownership query should SQL-quote the bound endpoint variable")
	require.NotContains(t, deploy, `-tAc "SELECT COUNT(*) FROM preview_runtimes WHERE endpoint_url = :'endpoint'`, "worker endpoint ownership query must not use psql -c with psql variables because -c sends the colon syntax to Postgres")
}

func TestSynchronousWorkerDeployReadsDatabaseEnvFromRemoteEnvFile(t *testing.T) {
	t.Parallel()

	deployScript, err := os.ReadFile("../deploy/scripts/deploy.sh")
	require.NoError(t, err, "test should read deploy script")
	deployText := string(deployScript)
	functionBody := extractShellFunction(t, deployText, "deploy_worker_blue_green", "dump_diagnostics")

	require.Contains(t, deployText, `load_worker_endpoint_check_env()`, "deploy.sh should define one helper for loading endpoint-check DB credentials")
	require.Contains(t, deployText, `DB_HOST="${DB_HOST:-$(read_worker_env_value DB_HOST)}"`, "helper should load DB_HOST from the refreshed remote .env file")
	require.Contains(t, deployText, `DB_PASSWORD="${DB_PASSWORD:-$(read_worker_env_value DB_PASSWORD)}"`, "helper should load DB_PASSWORD from the refreshed remote .env file")
	require.Contains(t, functionBody, `load_worker_endpoint_check_env`, "synchronous worker deploy should load endpoint-check credentials before finding a strict routine port")
	require.Contains(t, functionBody, `find_free_worker_port "$worker_private_ip"`, "synchronous worker deploy should find a routine worker port after loading credentials")
}

func TestWorkerRuntimeEndpointQueryExecutesThroughPsqlStdin(t *testing.T) {
	t.Parallel()

	deployScript, err := os.ReadFile("../deploy/scripts/deploy.sh")
	require.NoError(t, err, "test should read deploy.sh")
	functionBody := extractShellFunction(t, string(deployScript), "worker_runtime_endpoint_in_use", "worker_blue_green_extra_ports_configured")

	tmpDir := t.TempDir()
	fakeDocker := filepath.Join(tmpDir, "docker")
	err = os.WriteFile(fakeDocker, []byte(`#!/usr/bin/env bash
set -euo pipefail
printf '%s\n' "$*" > "$DOCKER_ARGS_FILE"
cat > "$PSQL_STDIN_FILE"
printf '0\n'
`), 0o755)
	require.NoError(t, err, "test should write a fake docker executable")

	argsFile := filepath.Join(tmpDir, "docker.args")
	stdinFile := filepath.Join(tmpDir, "psql.stdin")
	script := functionBody + `
worker_runtime_endpoint_in_use "100.96.213.15" "8087"
case "$?" in
  1) exit 0 ;;
  *) exit 1 ;;
esac
`
	cmd := exec.Command("bash", "-c", script)
	cmd.Env = append(os.Environ(),
		"PATH="+tmpDir+string(os.PathListSeparator)+os.Getenv("PATH"),
		"DB_HOST=db.example.internal",
		"DB_PASSWORD=secret",
		"DOCKER_ARGS_FILE="+argsFile,
		"PSQL_STDIN_FILE="+stdinFile,
	)
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, "worker endpoint ownership check should execute through the fake docker/psql path: %s", output)

	args, err := os.ReadFile(argsFile)
	require.NoError(t, err, "fake docker should record invocation arguments")
	stdin, err := os.ReadFile(stdinFile)
	require.NoError(t, err, "fake docker should record query from stdin")

	require.Contains(t, string(args), "run -i --rm", "worker endpoint ownership query should keep stdin open for dockerized psql")
	require.Contains(t, string(args), "-v endpoint=http://100.96.213.15:8087", "worker endpoint ownership query should pass the checked endpoint as a psql variable")
	require.NotContains(t, string(args), "-c", "worker endpoint ownership query should not use psql -c with psql variables")
	require.Contains(t, string(stdin), "SELECT COUNT(*) FROM preview_runtimes", "worker endpoint ownership query should be sent to psql on stdin")
	require.Contains(t, string(stdin), "endpoint_url = :'endpoint'", "worker endpoint ownership query should use psql SQL-quoted variable interpolation")
}

func TestWorkerBlockingDrainAllowsDefaultEndpointReuse(t *testing.T) {
	t.Parallel()

	deployScript, err := os.ReadFile("../deploy/scripts/deploy.sh")
	require.NoError(t, err, "test should read deploy.sh")
	deploy := string(deployScript)
	functionBody := extractShellFunction(t, deploy, "deploy_worker_blue_green", "dump_diagnostics")

	require.Contains(t, deploy, `endpoint_reuse_mode="${2:-strict}"`, "worker port selection should default to strict preview runtime endpoint ownership checks")
	require.Contains(t, deploy, `[ "$endpoint_reuse_mode" = "after-blocking-drain" ]`, "worker port selection should retain explicit maintenance-only reuse after the old worker generation has fully drained")
	require.Contains(t, functionBody, `deploy_mode="${DEPLOY_MODE:-routine}"`, "worker deploy should resolve deploy mode once before selecting a rollout path")
	require.Contains(t, functionBody, `if [ "$deploy_mode" = "maintenance" ]; then`, "maintenance worker deploy should take an explicit blocking drain path")
	require.Contains(t, functionBody, `drain_worker_containers_blocking "$old_containers"`, "maintenance worker deploy should stop old containers before reusing their endpoint")
	require.Contains(t, functionBody, `host_port="$(find_free_worker_port "$worker_private_ip" "after-blocking-drain")"`, "maintenance worker deploy should allow default endpoint reuse after the blocking drain")
	require.Contains(t, functionBody, `host_port="$(find_free_worker_port "$worker_private_ip")"`, "routine worker deploy should keep strict endpoint selection")
	require.Contains(t, functionBody, `routine blue/green deploy refuses blocking drain fallback`, "routine worker deploy should still explain why it refuses endpoint reuse")
}

func extractShellFunction(t *testing.T, script, startFunc, nextFunc string) string {
	t.Helper()

	start := strings.Index(script, "  "+startFunc+"() {")
	require.NotEqual(t, -1, start, "deploy.sh should define %s", startFunc)
	end := strings.Index(script[start:], "  "+nextFunc+"() {")
	require.NotEqual(t, -1, end, "deploy.sh should define %s after %s", nextFunc, startFunc)
	return script[start : start+end]
}

func extractTopLevelShellFunction(t *testing.T, script, startFunc, nextFunc string) string {
	t.Helper()

	return extractTopLevelShellBlock(t, script, startFunc, nextFunc+"() {")
}

func extractTopLevelShellBlock(t *testing.T, script, startFunc, endMarker string) string {
	t.Helper()

	start := strings.Index(script, startFunc+"() {")
	require.NotEqual(t, -1, start, "deploy.sh should define %s", startFunc)
	end := strings.Index(script[start:], endMarker)
	require.NotEqual(t, -1, end, "deploy.sh should contain marker %q after %s", endMarker, startFunc)
	return script[start : start+end]
}

func TestCIDeployConfiguresWorkerBlueGreenPortRange(t *testing.T) {
	t.Parallel()

	workflow, err := os.ReadFile("../.github/workflows/deploy.yml")
	require.NoError(t, err, "test should read deploy workflow")
	workflowText := string(workflow)

	require.Contains(t, workflowText, `WORKER_BLUE_GREEN_PORT_START: "8080"`, "CI worker deploy should explicitly enable a worker blue/green port range")
	require.Contains(t, workflowText, `WORKER_BLUE_GREEN_PORT_END: "8087"`, "CI worker deploy should reserve enough ports for overlapping draining generations")
	require.Contains(t, workflowText, "generation binds a free port while old worker generations keep serving", "workflow comment should document why the port range is required")
}

func TestCIDeployCancelsStaleBuildsButNotActiveDeploys(t *testing.T) {
	t.Parallel()

	workflow, err := os.ReadFile("../.github/workflows/deploy.yml")
	require.NoError(t, err, "test should read deploy workflow")
	workflowText := string(workflow)

	jobsIndex := strings.Index(workflowText, "\njobs:")
	require.NotEqual(t, -1, jobsIndex, "deploy workflow should define jobs")
	workflowHeader := workflowText[:jobsIndex]
	require.NotContains(t, workflowHeader, "\nconcurrency:", "deploy workflow should not use workflow-level concurrency because stale build cancellation must not cancel active deploys")

	buildIndex := strings.Index(workflowText, "\n  build:")
	predeployIndex := strings.Index(workflowText, "\n  predeploy-latest:")
	deployIndex := strings.Index(workflowText, "\n  deploy:")
	require.NotEqual(t, -1, buildIndex, "deploy workflow should define the build job")
	require.NotEqual(t, -1, predeployIndex, "deploy workflow should define the predeploy freshness gate")
	require.NotEqual(t, -1, deployIndex, "deploy workflow should define the deploy job")
	require.Less(t, buildIndex, predeployIndex, "predeploy freshness gate should run after build")
	require.Less(t, predeployIndex, deployIndex, "deploy should run after the freshness gate")

	buildJob := workflowText[buildIndex:predeployIndex]
	require.Contains(t, buildJob, `group: deploy-build-${{ github.ref }}-${{ matrix.name }}`, "build job should cancel stale builds independently per image")
	require.Contains(t, buildJob, "cancel-in-progress: true", "build job should cancel in-progress stale image builds")

	predeployJob := workflowText[predeployIndex:deployIndex]
	require.Contains(t, predeployJob, "should_deploy", "predeploy freshness gate should expose a should_deploy output")
	require.Contains(t, predeployJob, `gh api "repos/$REPO/commits/main" --jq .sha`, "predeploy freshness gate should compare the run SHA to latest main")
	require.Contains(t, predeployJob, `echo "should_deploy=true" >> "$GITHUB_OUTPUT"`, "predeploy freshness gate should allow latest main to deploy")
	require.Contains(t, predeployJob, `echo "should_deploy=false" >> "$GITHUB_OUTPUT"`, "predeploy freshness gate should skip stale deploys")

	deployJob := workflowText[deployIndex:]
	require.Contains(t, deployJob, "needs: [build, predeploy-latest]", "deploy job should wait for both image builds and freshness gate")
	require.Contains(t, deployJob, "if: needs.predeploy-latest.outputs.should_deploy == 'true'", "deploy job should skip stale SHAs")
	require.Contains(t, deployJob, "group: deploy-fleet", "deploy job should serialize fleet deploys")
	require.Contains(t, deployJob, "cancel-in-progress: false", "deploy job should never cancel an active production deploy")
	require.Contains(t, deployJob, "id: deploy_latest", "deploy job should re-check freshness after acquiring the deploy lock")
	require.Contains(t, deployJob, `gh api "repos/$REPO/commits/main" --jq .sha`, "deploy job should compare the run SHA to latest main after acquiring the deploy lock")
	deployFreshnessIndex := strings.Index(deployJob, "id: deploy_latest")
	deployCheckoutIndex := strings.Index(deployJob, "uses: actions/checkout@v6")
	require.NotEqual(t, -1, deployFreshnessIndex, "deploy job should define an in-lock freshness check")
	require.NotEqual(t, -1, deployCheckoutIndex, "deploy job should checkout before deploying")
	require.Less(t, deployFreshnessIndex, deployCheckoutIndex, "deploy job should re-check freshness before any deploy setup or SSH work")
	require.GreaterOrEqual(t, strings.Count(deployJob, "if: steps.deploy_latest.outputs.should_deploy == 'true'"), 6, "all deploy setup, deploy, and verification steps should skip stale SHAs detected after acquiring the deploy lock")
}

func TestWorkerGVisorPreflightPullsHealthImageOnlyWhenMissing(t *testing.T) {
	t.Parallel()

	compose, err := os.ReadFile("../docker-compose.worker.yml")
	require.NoError(t, err, "test should read the worker compose file")
	composeText := string(compose)

	require.Contains(t, composeText, `SANDBOX_HEALTH_CHECK_IMAGE: ${SANDBOX_HEALTH_CHECK_IMAGE:-busybox:1.36.1}`, "worker compose should pass the configurable health-check image into the worker container")
	require.Contains(t, composeText, `docker image inspect "$$HEALTHCHECK_IMAGE" >/dev/null 2>&1 || docker pull "$$HEALTHCHECK_IMAGE"`, "gVisor preflight should use the cached health image when present and only pull when missing")
}

// The auto-default for NODE_ID must use the full IP (dotted-to-dash), not
// just the last octet — otherwise a fleet that spans multiple /24s would
// silently produce duplicate NODE_IDs (10.0.0.4 and 10.0.1.4 both mapping
// to "worker-4"), and the worker registry would clobber one node's row
// with the other's heartbeats.
//
// And auto-detection must refuse to guess on multi-homed hosts: picking
// the wrong NIC's IP would publish a preview_internal_base_url that app
// nodes can't reach, and start-preview would fail intermittently.
func TestWorkerProvisioningHandlesAddressingEdgeCases(t *testing.T) {
	t.Parallel()

	provisionScript, err := os.ReadFile("../deploy/scripts/provision.sh")
	require.NoError(t, err, "test should read the provisioning script")
	require.Contains(t, string(provisionScript), `worker-${WORKER_PRIVATE_IP//./-}`, "provision.sh's NODE_ID default should use the full dotted-to-dash IP so workers across multiple /24s don't collide on \"worker-<last-octet>\"")
	require.NotContains(t, string(provisionScript), `worker-${WORKER_PRIVATE_IP##*.}`, "provision.sh should not fall back to the last-octet-only default — it collides across /24s")
	require.Contains(t, string(provisionScript), "private IPv4 addresses on real interfaces", "provision.sh should detect multi-homed hosts and require the operator to set WORKER_PRIVATE_IP explicitly rather than silently picking a NIC")
	require.Contains(t, string(provisionScript), `docker|br-|veth|virbr|lo|wg`, "provision.sh should ignore static-egress WireGuard interfaces when auto-detecting the app-reachable worker IP")
}

func TestTailscaleReadyPrivateServiceBinding(t *testing.T) {
	t.Parallel()

	dbCompose, err := os.ReadFile("../docker-compose.db.yml")
	require.NoError(t, err, "test should read db compose file")
	dbComposeText := string(dbCompose)
	require.Contains(t, dbComposeText, "${DB_BIND_IP:?", "db compose should require an explicit primary private bind IP instead of defaulting Postgres to the public interface")
	require.NotContains(t, dbComposeText, "DB_TAILSCALE_BIND_IP", "db compose should not make Postgres startup depend on a Tailscale interface address")
	require.NotContains(t, dbComposeText, "0.0.0.0:5432:5432", "db compose must not expose Postgres on every interface when cross-region workers use an overlay network")

	pgHBA, err := os.ReadFile("../deploy/postgres/pg_hba.conf")
	require.NoError(t, err, "test should read pg_hba.conf")
	require.Contains(t, string(pgHBA), "100.64.0.0/10", "Postgres should allow Tailscale tailnet clients after Tailscale ACLs have admitted the nodes")

	provisionScript, err := os.ReadFile("../deploy/scripts/provision.sh")
	require.NoError(t, err, "test should read provision.sh")
	provisionText := string(provisionScript)
	require.Contains(t, provisionText, `: "${DB_BIND_IP:?DB_BIND_IP is required for db role`, "db provisioning should fail loudly until the operator chooses the primary private bind address")
	require.Contains(t, provisionText, "DB_BIND_IP=%s", "db provisioning should write DB_BIND_IP into /opt/143/.env for compose interpolation")
	require.Contains(t, provisionText, `TS_ADVERTISE_ROUTES:=${DB_BIND_IP}/32`, "db Tailscale enrollment should derive the advertised DB route from DB_BIND_IP instead of requiring a second production secret")
	require.NotContains(t, provisionText, "TS_DB_ADVERTISE_ROUTES", "db provisioning should not require a separate DB route secret that can drift from DB_BIND_IP")

	deployScript, err := os.ReadFile("../deploy/scripts/deploy.sh")
	require.NoError(t, err, "test should read deploy.sh")
	deployText := string(deployScript)
	require.Contains(t, deployText, `[ -z "${!key:-}" ]`, "deploy.sh should fill empty exported env vars from .env.production.enc like provision.sh")
	require.Contains(t, deployText, `: "${DB_BIND_IP:?DB_BIND_IP is required for db role`, "db deploy should fail loudly until the operator chooses the primary private bind address")
	require.Contains(t, deployText, "DB_BIND_IP=%s", "db deploy should preserve DB_BIND_IP in /opt/143/.env for compose interpolation")
}

func TestProvisioningCanInstallAndUseTailscaleAddresses(t *testing.T) {
	t.Parallel()

	installScript, err := os.ReadFile("../deploy/scripts/install-tailscale.sh")
	require.NoError(t, err, "install-tailscale.sh should exist as the shared Tailscale host setup helper")
	installText := string(installScript)
	require.Contains(t, installText, "TS_AUTH_KEY", "Tailscale install helper should use an auth key for non-interactive server enrollment")
	require.Contains(t, installText, "--advertise-tags", "Tailscale install helper should support tagged production nodes for ACLs")
	require.Contains(t, installText, "--advertise-routes", "Tailscale install helper should support private subnet route advertisement for cross-region DB access")
	require.Contains(t, installText, "net.ipv4.ip_forward", "Tailscale install helper should enable forwarding when a node advertises subnet routes")
	require.Contains(t, installText, "--accept-routes=true", "Tailscale install helper should let remote workers accept advertised DB routes")
	require.Contains(t, installText, "--accept-dns=false", "Tailscale install helper should not let tailnet DNS rewrite host resolver state")
	require.Contains(t, installText, "tailscale ip -4", "Tailscale install helper should print the assigned IPv4 address for provisioning")

	provisionScript, err := os.ReadFile("../deploy/scripts/provision.sh")
	require.NoError(t, err, "test should read provision.sh")
	provisionText := string(provisionScript)
	require.Contains(t, provisionText, "install-tailscale.sh", "provisioning should run the shared Tailscale setup helper when TS_AUTH_KEY is provided")
	require.Contains(t, provisionText, "TS_AUTH_KEY_APP", "provisioning should support role-specific app Tailscale auth keys from production secrets")
	require.Contains(t, provisionText, "TS_AUTH_KEY_DB", "provisioning should support role-specific db Tailscale auth keys from production secrets")
	require.Contains(t, provisionText, "TS_AUTH_KEY_WORKER", "provisioning should support role-specific worker Tailscale auth keys from production secrets")
	require.Contains(t, provisionText, "TS_AUTH_KEY_REDIS", "provisioning should support role-specific redis Tailscale auth keys from production secrets")
	require.Contains(t, provisionText, `TS_ADVERTISE_ROUTES:=${REDIS_PRIVATE_IP}/32`, "redis Tailscale enrollment should advertise the Redis private IP route from REDIS_PRIVATE_IP")
	require.Contains(t, provisionText, "TS_WORKER_HOSTS", "provisioning should use a host list to choose which workers join Tailscale")
	require.Contains(t, provisionText, "TS_ACCEPT_ROUTES", "provisioning should pass route acceptance through to Tailscale enrollment")
	require.NotContains(t, provisionText, "TS_WORKER_ACCEPT_ROUTES", "mapped Tailscale workers should always accept advertised private routes without a separate production knob")
	require.Contains(t, provisionText, "WORKER_PRIVATE_IP_SOURCE:=tailscale", "worker provisioning should derive Tailscale address discovery from the worker host list")
	require.Contains(t, provisionText, "tailscale ip -4", "worker provisioning should be able to discover the worker's Tailscale IPv4 address")
	require.Contains(t, provisionText, "100.64.0.0/10", "worker provisioning comments/errors should make the Tailscale address range explicit")
	require.Contains(t, provisionText, "--tailscale-only", "provisioning should support enrolling already-provisioned hosts in Tailscale without reprovisioning containers or volumes")
	require.Contains(t, provisionText, "Tailscale enrollment applied", "Tailscale-only enrollment should finish before the normal running-container reprovision guard")
	tailscaleOnlyIndex := strings.Index(provisionText, `if [ "$MODE" = "--tailscale-only" ]`)
	runningGuardIndex := strings.Index(provisionText, "# Check if already provisioned")
	require.NotEqual(t, -1, tailscaleOnlyIndex, "provisioning should have a Tailscale-only mode branch")
	require.NotEqual(t, -1, runningGuardIndex, "provisioning should still have the normal running-container guard")
	require.Less(t, tailscaleOnlyIndex, runningGuardIndex, "Tailscale-only enrollment should bypass the running-container guard that blocks normal provisioning")

	makefile, err := os.ReadFile("../Makefile")
	require.NoError(t, err, "test should read Makefile")
	makefileText := string(makefile)
	require.Contains(t, makefileText, "tailscale-enroll:", "Makefile should expose a non-destructive Tailscale enrollment target for existing app/db/redis nodes")
	require.Contains(t, makefileText, `ROLE=<app|db|redis>`, "Makefile should allow non-destructive Redis Tailscale enrollment")
	require.NotContains(t, makefileText, "TS_DB_ADVERTISE_ROUTES", "Makefile should document DB_BIND_IP as the single source for the advertised DB route")

	cloudInit, err := os.ReadFile("../deploy/cloud-init/worker.yml")
	require.NoError(t, err, "test should read worker cloud-init template")
	cloudInitText := string(cloudInit)
	require.NotContains(t, cloudInitText, "TS_AUTH_KEY", "worker Tailscale enrollment should stay in provision.sh so it can use the production host map")
	require.NotContains(t, cloudInitText, "tailscale up", "worker cloud-init should not duplicate the Tailscale enrollment path")
}

func TestStaticEgressDeployWiring(t *testing.T) {
	t.Parallel()

	firewallScript, err := os.ReadFile("../deploy/scripts/sandbox-firewall.sh")
	require.NoError(t, err, "test should read sandbox-firewall.sh")
	firewallText := string(firewallScript)
	require.Contains(t, firewallText, `COMMENT_TAG="143-sandbox-egress-${NETWORK_TAG}"`, "firewall rules should use network-specific comment tags so reconciling one bridge does not delete the other bridge's rules")
	require.Contains(t, firewallText, "169.254.0.0/16", "firewall should block metadata destinations")
	require.Contains(t, firewallText, "10.0.0.0/8", "firewall should block private ranges")
	require.Contains(t, firewallText, "100.64.0.0/10", "firewall should block Tailscale CGNAT destinations from sandbox traffic")

	reconcileScript, err := os.ReadFile("../deploy/scripts/reconcile-worker-host.sh")
	require.NoError(t, err, "test should read reconcile-worker-host.sh")
	reconcileText := string(reconcileScript)
	require.Contains(t, reconcileText, "STATIC_EGRESS_NETWORK", "worker reconciliation should know about the static egress bridge")
	require.Contains(t, reconcileText, "143-sandbox-static-egress", "worker reconciliation should create the static egress sandbox network")
	require.Contains(t, reconcileText, "172.31.0.0/24", "static egress bridge should use a pinned subnet distinct from the default sandbox bridge")
	require.Contains(t, reconcileText, "sandbox-static-egress-resolv.conf", "static egress sandboxes should get a dedicated resolver file")
	require.Contains(t, reconcileText, "install-static-egress-worker.sh", "worker reconciliation should install policy routing and WireGuard for the static egress bridge")
	require.NotContains(t, reconcileText, "STATIC_EGRESS_ENABLED", "worker reconciliation should not require a separate static egress enabled flag")
	require.Contains(t, reconcileText, "/opt/143/.env", "worker reconciliation should load static egress config from the host env file during fresh provisioning")
	require.Contains(t, reconcileText, "/opt/143/static-egress-worker.env", "worker reconciliation should load host-only static egress secrets outside the compose env file")
	require.Contains(t, reconcileText, "load_static_egress_env_key", "worker reconciliation should parse env values without eval/source")
	require.Contains(t, reconcileText, "static egress is configured but /opt/143/deploy/scripts/install-static-egress-worker.sh is missing", "configured static egress must not silently skip a missing install helper")
	require.Contains(t, reconcileText, "ensure_static_egress_dns", "worker reconciliation should ensure sandbox DNS exists before static egress verification")
	require.Contains(t, reconcileText, "docker compose -f \"$compose_file\" up -d --build --no-deps sandbox-dns", "fresh worker provisioning should start sandbox-dns before probing the static egress bridge")

	deployScript, err := os.ReadFile("../deploy/scripts/deploy.sh")
	require.NoError(t, err, "test should read deploy.sh")
	deployText := string(deployScript)
	require.Contains(t, deployText, "install-static-egress-worker.sh.new", "worker deploys should sync the static egress install helper to existing workers")
	require.Contains(t, deployText, "STATIC_EGRESS_PROBE_IMAGE:=ghcr.io/assembledhq/143-sandbox:$TAG", "worker deploys should default the static egress verifier to the release sandbox image")
	require.Contains(t, deployText, "STATIC_EGRESS_PROBE_IMAGE=%s", "worker deploys should write the verifier image into the remote worker env")
	require.NotContains(t, deployText, "STATIC_EGRESS_ENABLED", "deploy should not require a separate static egress enabled flag")
	require.Contains(t, deployText, "NEXT_PUBLIC_PREVIEW_ORIGIN_TEMPLATE=%s\\nSTATIC_EGRESS_PUBLIC_IP=%s\\n", "app deploys should write static egress public IP for network status and preview routing")
	require.Contains(t, deployText, `if [ -n "${STATIC_EGRESS_PUBLIC_IP:-}" ]; then`, "worker deploys should pre-pull the static egress verifier when a static egress public IP is configured")
	require.Contains(t, deployText, "apply_static_egress_worker_host_map", "deploy should resolve per-worker static egress tunnel config from the centralized production host map")
	require.Contains(t, deployText, "STATIC_EGRESS_WORKER_HOSTS", "deploy should support centralized per-worker static egress config from production secrets")
	require.NotContains(t, deployText, "<node-id>:<host>@<wg-address>@<private-key>", "static egress worker host maps should use the simpler host@wg-address@private-key format")
	probePullIndex := strings.Index(deployText, `docker pull \"$static_egress_probe_image\"`)
	reconcileIndex := strings.Index(deployText, "if ! run_worker_host_reconcile")
	require.NotEqual(t, -1, probePullIndex, "worker deploys should pre-pull the configured static egress verifier image")
	require.NotEqual(t, -1, reconcileIndex, "worker deploys should reconcile worker host invariants")
	require.Less(t, probePullIndex, reconcileIndex, "worker deploys should pull the configured static egress verifier image before root-side static egress verification")
	require.NotContains(t, deployText, "STATIC_EGRESS_WORKER_PRIVATE_KEY=%q", "deploy should not place the WireGuard private key in ssh/sudo argv")
	require.NotContains(t, deployText, "sudo -n env $reconcile_env", "deploy should let root-side reconciliation read static egress config from /opt/143/.env")
	require.Contains(t, deployText, "/opt/143/static-egress-worker.env", "deploy should write WireGuard secrets into a host-only static egress env file")

	provisionScript, err := os.ReadFile("../deploy/scripts/provision.sh")
	require.NoError(t, err, "test should read provision.sh")
	provisionText := string(provisionScript)
	require.Contains(t, provisionText, "/opt/143/static-egress-worker.env", "provision should initialize the host-only static egress env file for new workers")
	require.NotContains(t, provisionText, "STATIC_EGRESS_ENABLED", "provisioning should not require a separate static egress enabled flag")
	require.Contains(t, provisionText, "NEXT_PUBLIC_PREVIEW_ORIGIN_TEMPLATE=%s\\nSTATIC_EGRESS_PUBLIC_IP=%s\\n", "app provisioning should write static egress public IP for network status and preview routing")
	require.Contains(t, provisionText, "apply_static_egress_worker_host_map", "provisioning should resolve per-worker static egress tunnel config from the centralized production host map")
	require.Contains(t, provisionText, "STATIC_EGRESS_WORKER_HOSTS", "provisioning should support centralized per-worker static egress config from production secrets")
	require.NotContains(t, provisionText, "<node-id>:<host>@<wg-address>@<private-key>", "static egress worker host maps should use the simpler host@wg-address@private-key format")

	workerInstallScript, err := os.ReadFile("../deploy/scripts/install-static-egress-worker.sh")
	require.NoError(t, err, "test should read static egress worker installer")
	workerInstallText := string(workerInstallScript)
	workerInterfaceDefault := regexp.MustCompile(`STATIC_EGRESS_WG_INTERFACE:-([^}]+)`).FindStringSubmatch(workerInstallText)
	require.Len(t, workerInterfaceDefault, 2, "static egress worker installer should define a default WireGuard interface")
	require.Equal(t, "wg-egress", workerInterfaceDefault[1], "worker WireGuard interface default should stay short and readable")
	require.LessOrEqual(t, len(workerInterfaceDefault[1]), 15, "worker WireGuard interface name must fit Linux IFNAMSIZ so wg-quick can create it")
	require.Contains(t, workerInstallText, "docker run", "static egress verification should probe from a sandbox-network container")
	require.Contains(t, workerInstallText, "--network \"$STATIC_EGRESS_NETWORK\"", "static egress verification should exercise the static egress bridge")
	require.Contains(t, workerInstallText, "--dns \"$STATIC_EGRESS_DNS_IP\"", "static egress verification should use the sandbox DNS resolver")
	require.Contains(t, workerInstallText, "getent hosts \"$host\"", "static egress verification should prove DNS works before advertising capability")
	require.NotContains(t, workerInstallText, "curl --interface", "static egress verification should not use a host-originated WireGuard interface probe")
	require.NotContains(t, workerInstallText, "ip rule replace", "worker WireGuard policy routing should avoid unsupported ip rule replace syntax")
	require.Contains(t, workerInstallText, "ip rule add fwmark", "worker WireGuard service should restore static egress policy routing after reboot")
	require.Contains(t, workerInstallText, "PostDown = ip rule del", "worker WireGuard service should clean up static egress policy routing on stop")
	require.Contains(t, workerInstallText, "systemctl stop \"wg-quick@${WG_INTERFACE}\"", "worker install should stop the existing WireGuard unit before recreating the interface")
	require.Contains(t, workerInstallText, "ip link delete dev \"$WG_INTERFACE\"", "worker install should remove stale WireGuard links before wg-quick up")
	require.Contains(t, workerInstallText, "systemctl start \"wg-quick@${WG_INTERFACE}\"", "worker install should start from a known-clean WireGuard interface state")
	require.NotContains(t, workerInstallText, "systemctl restart \"wg-quick@${WG_INTERFACE}\"", "worker install should avoid restart because stale links can survive a failed wg-quick down")
	require.Contains(t, workerInstallText, "rm -f \"$CAPABILITY_FILE\"", "worker install should clear stale capability before re-verifying the gateway path")
	require.Contains(t, workerInstallText, "iptables-persistent", "worker install should install persistent iptables support before advertising capability")
	require.Contains(t, workerInstallText, "command -v netfilter-persistent", "worker install should verify iptables persistence is available")
	require.Contains(t, workerInstallText, "netfilter-persistent save", "worker install should persist static egress mark and NAT rules")
	require.Contains(t, workerInstallText, "docker pull \"$PROBE_IMAGE\"", "static egress verification should ensure its probe image exists before running with --pull never")

	workerCompose, err := os.ReadFile("../docker-compose.worker.yml")
	require.NoError(t, err, "test should read worker compose")
	workerComposeText := string(workerCompose)
	require.NotContains(t, workerComposeText, "SANDBOX_STATIC_EGRESS_RESOLV_CONF", "static egress resolver path should be fixed internally instead of exposed as compose config")
	require.NotContains(t, workerComposeText, "SANDBOX_STATIC_EGRESS_NETWORK", "static egress bridge name should be fixed internally instead of exposed as compose config")
	require.Contains(t, workerComposeText, "STATIC_EGRESS_PUBLIC_IP", "worker should advertise the configured public static egress IP")
	require.NotContains(t, workerComposeText, "STATIC_EGRESS_WORKER_PRIVATE_KEY", "worker app containers should not receive WireGuard private keys")
	require.NotContains(t, workerComposeText, "STATIC_EGRESS_CAPABILITY_FILE", "static egress capability marker path should be fixed internally instead of exposed as compose config")
	require.Contains(t, workerComposeText, "/etc/143:/etc/143:ro", "worker container should see the host verifier marker and resolver files read-only")
	require.Contains(t, workerComposeText, "static-egress-sandbox", "worker service should join the static egress sandbox bridge")
	require.NotContains(t, workerComposeText, "sandbox-static-egress-dns", "one sandbox-dns service should attach to both sandbox bridges")
	require.Contains(t, workerComposeText, "ipv4_address: 172.31.0.2", "the shared sandbox-dns service should have a fixed IP on the static egress bridge")
	require.Contains(t, workerComposeText, "name: 143-sandbox-static-egress", "worker compose should declare the static egress bridge as an external network")

	makefile, err := os.ReadFile("../Makefile")
	require.NoError(t, err, "test should read Makefile")
	makefileText := string(makefile)
	require.Contains(t, makefileText, "provision-egress", "Makefile should expose an egress gateway provisioning entrypoint")
	require.Contains(t, makefileText, "deploy/scripts/sync-static-egress-secrets.sh --apply", "provision-worker should sync generated static egress secrets from FLEET_HOSTS before provisioning")
	require.Contains(t, makefileText, "PROVISION_WORKER_HOST=$(HOST)", "provision-worker should verify the requested worker is present in FLEET_HOSTS during static egress sync")
	require.Contains(t, makefileText, "deploy/scripts/provision-egress.sh", "provision-worker should reload the egress gateway after provisioning a static-egress worker")
	require.Contains(t, makefileText, "EGRESS_SSH_KEY ?= $(or $(wildcard ~/.ssh/143-egress),$(wildcard ~/.ssh/143-egress.pem),$(SSH_KEY))", "static egress provisioning should auto-detect a role-specific SSH key before falling back to the fleet deploy key")
	require.Contains(t, makefileText, "provision-egress:\n\t@test -n \"$(EGRESS_SSH_KEY)\"", "provision-egress should validate the gateway SSH key before provisioning")
	require.Contains(t, makefileText, "@deploy/scripts/sync-static-egress-secrets.sh --apply", "provision-egress should sync generated gateway and worker peer secrets before provisioning the gateway")
	require.Contains(t, makefileText, "EGRESS_SSH_KEY", "provision-worker should support a separate SSH key for reloading an AWS-hosted egress gateway")
	require.Contains(t, makefileText, "EGRESS_SSH_USER", "provision-egress should support a role-specific SSH user override without changing worker provisioning")
	require.Contains(t, makefileText, "TS_AUTH_KEY_EGRESS", "provision-egress should support enrolling the egress gateway in Tailscale")
	require.NotContains(t, makefileText, "static-egress-worker-keys", "static egress should not expose a second worker inventory via a standalone key-generation target")
	require.NotContains(t, makefileText, "generate-static-egress-keys.sh", "static egress key generation should happen from FLEET_HOSTS during provisioning")
	syncIndex := strings.Index(makefileText, "deploy/scripts/sync-static-egress-secrets.sh --apply")
	gatewayIndex := strings.Index(makefileText, `deploy/scripts/provision-egress.sh "" "$(EGRESS_SSH_KEY)"`)
	workerProvisionIndex := strings.Index(makefileText, "./deploy/scripts/provision.sh worker")
	require.NotEqual(t, -1, syncIndex, "provision-worker should sync static egress secrets")
	require.NotEqual(t, -1, gatewayIndex, "provision-worker should reload the egress gateway")
	require.NotEqual(t, -1, workerProvisionIndex, "provision-worker should provision the worker")
	require.Less(t, syncIndex, gatewayIndex, "provision-worker should sync gateway peer config before reloading the gateway")
	require.Less(t, gatewayIndex, workerProvisionIndex, "provision-worker should reload the gateway before the worker runs static egress probes")

	syncScript, err := os.ReadFile("../deploy/scripts/sync-static-egress-secrets.sh")
	require.NoError(t, err, "test should read static egress sync helper")
	syncText := string(syncScript)
	require.Contains(t, syncText, "FLEET_HOSTS", "static egress sync should derive workers from the fleet host inventory")
	require.Contains(t, syncText, "role=\"${entry%%:*}\"", "static egress sync should identify worker entries by role")
	require.Contains(t, syncText, "egress_host_count", "static egress sync should count egress inventory entries before mutating production secrets")
	require.Contains(t, syncText, "exactly one egress:<host>", "static egress sync should require exactly one gateway host in FLEET_HOSTS")
	require.Contains(t, syncText, "duplicate worker:<host>", "static egress sync should reject duplicate worker inventory entries before generating WireGuard peers")
	require.Contains(t, syncText, "brew install wireguard-tools", "static egress sync should explain how to install the local WireGuard CLI prerequisite")
	require.Contains(t, syncText, "STATIC_EGRESS_GATEWAY_PRIVATE_KEY", "static egress sync should generate or preserve the gateway private key")
	require.Contains(t, syncText, "STATIC_EGRESS_GATEWAY_PUBLIC_KEY", "static egress sync should derive the gateway public key")
	require.Contains(t, syncText, "STATIC_EGRESS_WORKER_HOSTS", "static egress sync should update the generated worker private-key map")
	require.Contains(t, syncText, "STATIC_EGRESS_WORKER_PEERS", "static egress sync should update the derived gateway peer list")
	require.Contains(t, syncText, "PROVISION_WORKER_HOST", "static egress sync should validate provision-worker is backed by FLEET_HOSTS")
	require.Contains(t, syncText, "sops set", "static egress sync should edit generated keys in place in apply mode")
	require.Contains(t, syncText, "--idempotent", "static egress sync should skip keys whose value is unchanged so no-op re-runs produce no diff")
	require.NotContains(t, syncText, "sops --encrypt", "static egress sync should not full re-encrypt the file, which rotates the data key and rewrites every value into an unreviewable whole-file diff")
	require.Contains(t, syncText, "cp \"$ENC_FILE\" \"$staged_enc\"", "static egress sync should stage edits on a copy so a partial failure cannot leave the live secrets file half-updated")
	require.Contains(t, syncText, "mv \"$staged_enc\" \"$ENC_FILE\"", "static egress sync should swap the fully-edited staged copy in with an atomic rename")
	require.Contains(t, syncText, "Commit $ENC_FILE after provisioning succeeds", "static egress sync should remind operators to commit generated encrypted secrets after provisioning succeeds")

	provisionEgressScript, err := os.ReadFile("../deploy/scripts/provision-egress.sh")
	require.NoError(t, err, "test should read egress provisioning wrapper")
	provisionEgressText := string(provisionEgressScript)
	require.Contains(t, provisionEgressText, ".env.production.enc", "egress provisioning wrapper should load gateway config from encrypted production secrets")
	require.Contains(t, provisionEgressText, "FLEET_HOSTS", "egress provisioning wrapper should resolve the gateway host from the fleet host inventory")
	require.Contains(t, provisionEgressText, `role="${entry%%:*}"`, "egress provisioning wrapper should identify egress entries by role")
	require.Contains(t, provisionEgressText, `role" = "egress"`, "egress provisioning wrapper should support an egress:<host> fleet role")
	require.Contains(t, provisionEgressText, "trap 'rm -f \"$tmp_env\"' RETURN", "egress provisioning wrapper should clean up decrypted production env temp files on every function exit path")
	require.NotContains(t, provisionEgressText, "STATIC_EGRESS_GATEWAY_HOST", "egress provisioning wrapper should avoid a second gateway host inventory field")
	require.Contains(t, provisionEgressText, "STATIC_EGRESS_GATEWAY_PRIVATE_KEY", "egress provisioning wrapper should require and forward the gateway private key")
	require.Contains(t, provisionEgressText, "STATIC_EGRESS_WORKER_PEERS", "egress provisioning wrapper should require and forward worker peer config")
	require.Contains(t, provisionEgressText, "/opt/143/static-egress-gateway.env", "egress provisioning wrapper should stage remote gateway config instead of assuming remote shell env")
	require.Contains(t, provisionEgressText, "provision-egress-gateway.sh", "egress provisioning wrapper should run the gateway provisioning helper")
	require.Contains(t, provisionEgressText, `EGRESS_SSH_USER="${EGRESS_SSH_USER:-${SSH_USER:-}}"`, "egress provisioning should allow a role-specific user while preserving the legacy SSH_USER override")
	require.Contains(t, provisionEgressText, "resolve_remote_user", "egress provisioning should auto-detect root versus ubuntu before running scp")
	require.Contains(t, provisionEgressText, "root ubuntu", "egress provisioning should probe both common cloud bootstrap users")
	require.Contains(t, provisionEgressText, "remote_sudo_prefix", "egress provisioning should use sudo for privileged remote commands when SSH_USER is not root")
	require.NotContains(t, provisionEgressText, `root@"$HOST"`, "egress provisioning should not hard-code root SSH because AWS Ubuntu AMIs require ubuntu@")
	require.Contains(t, provisionEgressText, "install-tailscale.sh", "egress provisioning should optionally enroll the gateway in Tailscale")
	require.Contains(t, provisionEgressText, "TS_AUTH_KEY_EGRESS", "egress provisioning should support a role-specific Tailscale auth key")

	gatewayScript, err := os.ReadFile("../deploy/scripts/provision-egress-gateway.sh")
	require.NoError(t, err, "test should read egress gateway provisioning helper")
	gatewayText := string(gatewayScript)
	require.Contains(t, gatewayText, "wg0", "egress gateway provisioning should configure WireGuard")
	require.Contains(t, gatewayText, "publicKey@allowedIP", "egress gateway peer format should use a delimiter that cannot appear in base64 WireGuard keys")
	require.NotContains(t, gatewayText, "%%=*", "egress gateway should not split WireGuard peer keys on '=' because base64 public keys may be padded")
	require.NotContains(t, gatewayText, "#*=", "egress gateway should not split WireGuard peer keys on '=' because base64 public keys may be padded")
	require.Contains(t, gatewayText, "MASQUERADE", "egress gateway should SNAT tunnel traffic to its public IPv4")
	require.Contains(t, gatewayText, "iptables-persistent", "egress gateway should install persistent iptables support")
	require.Contains(t, gatewayText, "netfilter-persistent save", "egress gateway should persist NAT and guard rules")
	require.Contains(t, gatewayText, "systemctl restart \"wg-quick@${WG_INTERFACE}\"", "egress gateway provisioning should reload rewritten WireGuard peer config")
	require.Contains(t, gatewayText, "169.254.0.0/16", "egress gateway should independently block metadata ranges")
	require.Contains(t, gatewayText, "10.0.0.0/8", "egress gateway should independently block private ranges")
	require.Contains(t, gatewayText, "100.64.0.0/10", "egress gateway should block Tailscale CGNAT ranges")
}

func TestWorkerReprovisionDrainsBlueGreenGenerations(t *testing.T) {
	t.Parallel()

	provisionScript, err := os.ReadFile("../deploy/scripts/provision.sh")
	require.NoError(t, err, "test should read provision.sh")
	provisionText := string(provisionScript)

	require.Contains(t, provisionText, "list_worker_reprovision_containers", "worker reprovision should inspect all compose worker generations, not only the base compose project")
	require.Contains(t, provisionText, `label=com.docker.compose.service=worker`, "worker reprovision should detect blue/green worker containers by compose service label")
	require.Contains(t, provisionText, "spin-down-worker.sh", "worker reprovision should drain blue/green generations through the canonical spin-down path")
	require.Contains(t, provisionText, "WORKER_REPROVISION_DRAIN_TIMEOUT_SECONDS", "worker reprovision should expose an operator-controlled drain timeout")
}

func TestGrafanaProvisionedDashboardsUseValidDatasourcesAndRangeQueries(t *testing.T) {
	t.Parallel()

	dashboardFiles, err := os.ReadDir("../deploy/grafana/provisioning/dashboards")
	require.NoError(t, err, "test should read provisioned dashboard directory")
	dashboardNames := make(map[string]bool, len(dashboardFiles))
	for _, dashboardFile := range dashboardFiles {
		dashboardNames[dashboardFile.Name()] = true
	}
	require.True(t, dashboardNames["platform-health.json"], "platform health dashboard should be provisioned from the repo")
	require.True(t, dashboardNames["primary-operations.json"], "primary operations dashboard should be provisioned from the repo")

	for _, dashboardFile := range dashboardFiles {
		if dashboardFile.IsDir() || !strings.HasSuffix(dashboardFile.Name(), ".json") {
			continue
		}
		rawDashboard, err := os.ReadFile("../deploy/grafana/provisioning/dashboards/" + dashboardFile.Name())
		require.NoError(t, err, "test should read each provisioned dashboard")

		var dashboard struct {
			UID    string `json:"uid"`
			Title  string `json:"title"`
			Panels []struct {
				Type       string `json:"type"`
				Title      string `json:"title"`
				Datasource *struct {
					UID string `json:"uid"`
				} `json:"datasource"`
				Targets []struct {
					QueryType  string `json:"queryType"`
					Expr       string `json:"expr"`
					Datasource *struct {
						UID string `json:"uid"`
					} `json:"datasource"`
				} `json:"targets"`
			} `json:"panels"`
		}
		require.NoError(t, json.Unmarshal(rawDashboard, &dashboard), "provisioned dashboard %s should be valid JSON", dashboardFile.Name())
		require.NotEmpty(t, dashboard.UID, "provisioned dashboard %s should declare a stable UID", dashboardFile.Name())
		require.NotEmpty(t, dashboard.Title, "provisioned dashboard %s should declare a title", dashboardFile.Name())

		for _, panel := range dashboard.Panels {
			if panel.Datasource != nil && panel.Datasource.UID != "" && panel.Datasource.UID != "-- Grafana --" {
				require.Equal(t, "victorialogs", panel.Datasource.UID, "dashboard %s panel %q should use the provisioned VictoriaLogs datasource UID", dashboardFile.Name(), panel.Title)
			}
			for _, target := range panel.Targets {
				if target.Datasource != nil && target.Datasource.UID != "" {
					require.Equal(t, "victorialogs", target.Datasource.UID, "dashboard %s panel %q target should use the provisioned VictoriaLogs datasource UID", dashboardFile.Name(), panel.Title)
				}
				if panel.Type != "timeseries" || !strings.Contains(target.Expr, "| stats") {
					if dashboardFile.Name() == "platform-health.json" && panel.Type == "stat" && strings.Contains(target.Expr, "pending_runnable") {
						require.Equal(t, "statsRange", target.QueryType, "platform health stat panel %q should use a range query so Grafana can reduce the latest bucket", panel.Title)
					}
					continue
				}
				require.Equal(t, "statsRange", target.QueryType, "time-series stats panel %q in dashboard %s should use the VictoriaLogs range query type", panel.Title, dashboardFile.Name())
			}
		}
	}
}

func TestPlatformHealthDashboardPrioritizesActionableQueueAndWorkerCapacity(t *testing.T) {
	t.Parallel()

	rawDashboard, err := os.ReadFile("../deploy/grafana/provisioning/dashboards/platform-health.json")
	require.NoError(t, err, "test should read platform health dashboard")

	var dashboard struct {
		Panels []struct {
			Title   string `json:"title"`
			Targets []struct {
				Expr string `json:"expr"`
			} `json:"targets"`
		} `json:"panels"`
	}
	require.NoError(t, json.Unmarshal(rawDashboard, &dashboard), "platform health dashboard should be valid JSON")

	panelTitles := make(map[string]bool, len(dashboard.Panels))
	var expressions []string
	for _, panel := range dashboard.Panels {
		panelTitles[panel.Title] = true
		require.NotContains(t, panel.Title, "Runnable", "platform health dashboard should use operator-facing ready/waiting terminology instead of internal runnable wording")
		require.NotContains(t, panel.Title, "runnable", "platform health dashboard should use operator-facing ready/waiting terminology instead of internal runnable wording")
		for _, target := range panel.Targets {
			expressions = append(expressions, target.Expr)
		}
	}

	require.True(t, panelTitles["Ready jobs waiting"], "dashboard should expose jobs that are ready to claim in plain language")
	require.True(t, panelTitles["Oldest waiting job"], "dashboard should expose queue staleness in plain language")
	require.True(t, panelTitles["Running containers"], "dashboard should show a current container snapshot")
	require.True(t, panelTitles["Lowest RAM headroom"], "dashboard should show remaining worker memory headroom")
	require.True(t, panelTitles["Lowest CPU headroom"], "dashboard should show remaining worker CPU headroom")
	require.True(t, panelTitles["Worker capacity snapshot"], "dashboard should include a per-worker capacity table")
	require.True(t, panelTitles["Queue action list"], "dashboard should include an operator-focused queue table")

	allExpressions := strings.Join(expressions, "\n")
	require.Contains(t, allExpressions, "min_memory_headroom", "dashboard should query explicit memory headroom from runtime health logs")
	require.Contains(t, allExpressions, "min_cpu_headroom", "dashboard should query explicit CPU headroom from runtime health logs")
	require.Contains(t, allExpressions, "stats by (worker_node_id)", "dashboard should group worker capacity by worker node")
	require.Contains(t, allExpressions, "pending_runnable) as ready", "dashboard can still query the stored runnable field but should alias it to ready")
}

func TestPrimaryOperationsDashboardTracksWorkerLoad(t *testing.T) {
	t.Parallel()

	rawDashboard, err := os.ReadFile("../deploy/grafana/provisioning/dashboards/primary-operations.json")
	require.NoError(t, err, "test should read the primary operations dashboard")

	var dashboard struct {
		Title  string `json:"title"`
		Panels []struct {
			Title   string `json:"title"`
			Type    string `json:"type"`
			Targets []struct {
				QueryType string `json:"queryType"`
				Expr      string `json:"expr"`
			} `json:"targets"`
		} `json:"panels"`
	}
	require.NoError(t, json.Unmarshal(rawDashboard, &dashboard), "primary operations dashboard should be valid JSON")
	require.Equal(t, "143 - Primary Operations", dashboard.Title, "primary operations dashboard should have the expected title")

	required := map[string]string{
		"Running sessions":                `platform health: worker load total sample`,
		"Active previews":                 `platform health: worker load total sample`,
		"Worker CPU utilization":          `host_cpu_util`,
		"Worker RAM utilization":          `host_memory_util`,
		"Sessions and previews by worker": `platform health: worker load sample`,
	}
	for title, exprFragment := range required {
		found := false
		for _, panel := range dashboard.Panels {
			if panel.Title != title {
				continue
			}
			found = true
			require.NotEmpty(t, panel.Targets, "panel %q should have a LogsQL target", title)
			require.Contains(t, panel.Targets[0].Expr, exprFragment, "panel %q should query the expected field or log message", title)
		}
		require.True(t, found, "primary operations dashboard should include panel %q", title)
	}
}

func TestPlatformHealthDashboardTracksSessionSnapshotSize(t *testing.T) {
	t.Parallel()

	rawDashboard, err := os.ReadFile("../deploy/grafana/provisioning/dashboards/platform-health.json")
	require.NoError(t, err, "test should read the platform health dashboard")

	var dashboard struct {
		Panels []struct {
			Title   string `json:"title"`
			Type    string `json:"type"`
			Targets []struct {
				QueryType string `json:"queryType"`
				Expr      string `json:"expr"`
			} `json:"targets"`
		} `json:"panels"`
	}
	require.NoError(t, json.Unmarshal(rawDashboard, &dashboard), "platform health dashboard should be valid JSON")

	foundSnapshotSizePanel := false
	for _, panel := range dashboard.Panels {
		if panel.Title != "Session snapshot size" {
			continue
		}
		foundSnapshotSizePanel = true
		require.Equal(t, "timeseries", panel.Type, "snapshot size should be graphed over time")
		require.NotEmpty(t, panel.Targets, "snapshot size panel should have a LogsQL target")
		require.Equal(t, "statsRange", panel.Targets[0].QueryType, "snapshot size panel should use a range query")
		require.Contains(t, panel.Targets[0].Expr, `_msg:"session snapshot saved"`, "snapshot size panel should query the session snapshot success log")
		require.Contains(t, panel.Targets[0].Expr, "snapshot_size_bytes", "snapshot size panel should aggregate the snapshot byte field")
	}
	require.True(t, foundSnapshotSizePanel, "platform health dashboard should include session snapshot size telemetry")
}

func TestProductionAlertsUseValidLogsQLRangeFilters(t *testing.T) {
	t.Parallel()

	alerts, err := os.ReadFile("../deploy/vmalert/rules/production-alerts.yml")
	require.NoError(t, err, "test should read production vmalert rules")

	alertConfig := string(alerts)
	require.NotContains(t, alertConfig, "status:[", "VictoriaLogs numeric ranges should use field:range[...] syntax so vmalert can parse the rules")
	require.GreaterOrEqual(t, strings.Count(alertConfig, "status:range[500,599]"), 3, "API 5xx alert rules should filter inclusive 500-599 statuses with valid LogsQL range syntax")
}

func TestLoggingDesignDocsTrackProvisionedDashboardsAndAlerts(t *testing.T) {
	t.Parallel()

	design, err := os.ReadFile("../docs/design/implemented/47-logging-victorialogs.md")
	require.NoError(t, err, "test should read the VictoriaLogs design doc")

	designText := string(design)
	require.NotContains(t, designText, "Dashboard and alert curation remain follow-up operational work.", "logging design doc should not describe provisioned dashboards and alerts as future work")
	require.Contains(t, designText, "deploy/grafana/provisioning/dashboards/errors.json", "logging design doc should track the provisioned error dashboard")
	require.Contains(t, designText, "deploy/vmalert/rules/production-alerts.yml", "logging design doc should track repo-owned alert rules")
}

// Docker's json-file driver grows unbounded by default, so a chatty
// container will fill the disk on its own. The fix: install-log-rotation.sh
// merges log-driver/log-opts into /etc/docker/daemon.json (preserving any
// gVisor runtimes block on workers) and restarts docker only when the
// content actually changes. Pin the wiring here so a future refactor
// doesn't silently strip it off and leave us in the unbounded-growth
// state. db keeps its own larger cap because the db host is the only
// role without Vector log shipping (postgres logs are local-only) AND
// postgresql.conf logs every connection, every query >500ms, and every
// lock wait — a smaller cap would lose the forensic trail during an
// incident.
func TestDeployConfiguresDockerLogRotation(t *testing.T) {
	t.Parallel()

	helper, err := os.ReadFile("../deploy/scripts/install-log-rotation.sh")
	require.NoError(t, err, "install-log-rotation.sh should exist as the single source of truth for daemon.json log-rotation logic")
	helperText := string(helper)
	require.Contains(t, helperText, "/etc/docker/daemon.json", "install-log-rotation.sh should target daemon.json (not a per-container override) so dynamically-spawned sandbox containers also inherit the cap")
	require.Contains(t, helperText, `"log-driver"`, "install-log-rotation.sh should write the json-file log-driver into daemon.json")
	require.Contains(t, helperText, "systemctl restart docker", "install-log-rotation.sh must restart docker on change — log-driver/log-opts only take effect for newly created containers, so existing services need to inherit the cap on the next recreate")
	require.Contains(t, helperText, "mv ", "install-log-rotation.sh should write atomically (tempfile + rename) — a SIGKILL between truncate and write under a plain `>` would leave a zero-byte daemon.json that docker rejects")

	deployScript, err := os.ReadFile("../deploy/scripts/deploy.sh")
	require.NoError(t, err, "test should read deploy script")
	deployText := string(deployScript)
	require.Contains(t, deployText, "install-log-rotation.sh", "deploy.sh should sync and invoke the install-log-rotation.sh helper for maintenance-capable deploys")
	require.Contains(t, deployText, "ALLOW_DEPLOY_DOCKER_DAEMON_RESTART", "deploy.sh should require an explicit maintenance flag before app deploys can restart Docker")
	require.Contains(t, deployText, "Skipping docker log rotation check on app deploy", "routine app deploys should skip daemon-mutating log rotation checks to keep Caddy bound on 80/443")
	require.Contains(t, deployText, `db) LOG_MAX_SIZE="500m"`, "deploy.sh should give the db role a larger log cap — postgres logging is verbose and the db host has no Vector log shipping, so the local docker log is the only copy")
	require.Contains(t, deployText, `*)  LOG_MAX_SIZE="100m"`, "deploy.sh should default non-db roles to a 100m cap")
	require.Contains(t, deployText, "sudo -n /opt/143/deploy/scripts/install-log-rotation.sh", "deploy.sh should invoke install-log-rotation.sh via deploy+sudo (not root SSH) — matches the sandbox-firewall.sh pattern and avoids depending on root SSH at routine deploy time")
	require.Contains(t, deployText, "repair-deploy-sudoers.sh", "deploy.sh should try the narrow root repair path when a legacy host is missing the deploy sudoers entry")
	require.Contains(t, deployText, "Retrying docker log rotation after sudoers repair", "deploy.sh should retry install-log-rotation.sh after repairing sudoers so a single deploy can recover legacy hosts")
	require.Contains(t, deployText, "WARNING: docker log rotation was not updated on this deploy; continuing.", "deploy.sh should keep routine deploys moving when a legacy host cannot be sudoers-repaired from CI")
	require.NotContains(t, deployText, "ERROR: install-log-rotation.sh failed and sudoers repair via root SSH did not complete.", "deploy.sh should not fail the whole deploy solely because optional log-rotation repair is unavailable")

	bootstrap, err := os.ReadFile("../deploy/scripts/bootstrap.sh")
	require.NoError(t, err, "test should read bootstrap.sh")
	require.Contains(t, string(bootstrap), "/opt/143/deploy/scripts/install-log-rotation.sh *", "bootstrap.sh sudoers Cmnd_Alias must allow install-log-rotation.sh — without it the deploy+sudo path in deploy.sh fails on app/worker hosts")

	provision, err := os.ReadFile("../deploy/scripts/provision.sh")
	require.NoError(t, err, "test should read provision.sh")
	provisionText := string(provision)
	require.Contains(t, provisionText, "configure-docker-daemon.sh", "provision.sh should configure Docker daemon hardening through one helper so fresh hosts do not hit systemd start limits from sequential restarts")
	require.NotContains(t, provisionText, `"/opt/143/deploy/scripts/install-log-rotation.sh $LOG_MAX_SIZE 5"`, "provision.sh must not invoke the log-rotation helper directly because the DNS helper would perform a second Docker restart on fresh hosts")
	require.GreaterOrEqual(t, strings.Count(provisionText, "/usr/bin/chown -R deploy\\:deploy /opt/143/deploy/scripts"), 3, "provision.sh inline bootstraps for db, logging, and redis must allow deploy to fix root-owned deploy/scripts before syncing helpers")
	// db/logging/redis bootstraps don't run bootstrap.sh, so each must
	// install its own /etc/sudoers.d/99-deploy or the deploy+sudo path
	// breaks on those roles.
	require.GreaterOrEqual(t, strings.Count(provisionText, "/opt/143/deploy/scripts/install-log-rotation.sh *"), 3, "provision.sh inline bootstraps for db, logging, and redis must each grant deploy NOPASSWD sudo for install-log-rotation.sh")

	repair, err := os.ReadFile("../deploy/scripts/repair-deploy-sudoers.sh")
	require.NoError(t, err, "repair-deploy-sudoers.sh should exist for legacy hosts that are missing the deploy sudoers entry")
	repairText := string(repair)
	require.Contains(t, repairText, "/etc/sudoers.d/99-deploy", "repair-deploy-sudoers.sh should update the deploy sudoers file")
	require.Contains(t, repairText, "mktemp /etc/sudoers.d/99-deploy", "repair-deploy-sudoers.sh should stage sudoers in the target directory before replacing the live file")
	require.Contains(t, repairText, "visudo -cf \"$TMP\"", "repair-deploy-sudoers.sh should validate the staged sudoers file before installing it")
	require.Contains(t, repairText, "mv \"$TMP\" /etc/sudoers.d/99-deploy", "repair-deploy-sudoers.sh should atomically replace sudoers only after validation succeeds")
	require.NotContains(t, repairText, "cat > /etc/sudoers.d/99-deploy", "repair-deploy-sudoers.sh must not write directly to the live sudoers file")
	require.Contains(t, repairText, "deploy ALL=(root) NOPASSWD: DEPLOY_CMDS", "repair-deploy-sudoers.sh should install the same narrow command alias used by provisioning")
	require.NotContains(t, repairText, "NOPASSWD:ALL", "repair-deploy-sudoers.sh must not repair legacy hosts by granting blanket passwordless sudo")

	makefile, err := os.ReadFile("../Makefile")
	require.NoError(t, err, "test should read Makefile")
	require.Contains(t, string(makefile), "repair-deploy-sudoers:", "Makefile should expose the no-teardown sudoers repair as an operator target")
}

func TestProductionPostgresConnectionHeadroom(t *testing.T) {
	t.Parallel()

	conf, err := os.ReadFile("../deploy/postgres/postgresql.conf")
	require.NoError(t, err, "test should read production PostgreSQL config")

	require.Contains(t, string(conf), "max_connections = 200", "production Postgres should leave headroom for blue/green worker overlap and deploy-control clients")
}

func TestDBDeploySyncsMountedPostgresConfig(t *testing.T) {
	t.Parallel()

	deployScript, err := os.ReadFile("../deploy/scripts/deploy.sh")
	require.NoError(t, err, "test should read deploy script")
	deployText := string(deployScript)

	require.Contains(t, deployText, `if [ "$ROLE" = "db" ]; then`, "db deploy should have role-specific sync for mounted Postgres config")
	require.Contains(t, deployText, "$PROJECT_DIR/deploy/postgres/postgresql.conf", "db deploy should sync the mounted postgresql.conf")
	require.Contains(t, deployText, "$PROJECT_DIR/deploy/postgres/pg_hba.conf", "db deploy should sync the mounted pg_hba.conf")
	require.Contains(t, deployText, "mkdir -p /opt/143/deploy/postgres", "db deploy should ensure the mounted config directory exists")
}

func TestDeployPrunesDockerArtifactsAfterSuccessfulRollout(t *testing.T) {
	t.Parallel()

	deployScript, err := os.ReadFile("../deploy/scripts/deploy.sh")
	require.NoError(t, err, "test should read deploy script")
	deployText := string(deployScript)

	require.Contains(t, deployText, "prune_docker_deploy_artifacts()", "deploy.sh should define one prune helper so app, worker, and detached worker paths stay aligned")
	require.Contains(t, deployText, `docker container prune -f --filter "until=$prune_until"`, "deploy prune should remove stopped containers after a successful rollout")
	require.NotContains(t, deployText, `docker rm -f`, "deploy prune must not force-remove running executor containers")
	require.Contains(t, deployText, `docker image prune -af --filter "until=$prune_until"`, "deploy prune should remove old unused SHA-tagged images after a successful rollout")
	require.Contains(t, deployText, `docker builder prune -af --filter "until=$prune_until"`, "deploy prune should remove unused build cache after a successful rollout")
	require.Contains(t, deployText, `$(remote_env_assignment DEPLOY_DOCKER_PRUNE "${DEPLOY_DOCKER_PRUNE:-1}")`, "deploy should pass the prune enable/disable knob through SSH to the remote host")
	require.Contains(t, deployText, `$(remote_env_assignment DOCKER_PRUNE_UNTIL "${DOCKER_PRUNE_UNTIL:-24h}")`, "deploy should pass the prune age window through SSH to the remote host")
	require.Contains(t, deployText, `$(remote_env_assignment SESSION_EXECUTOR_DOCKER_NETWORK "${SESSION_EXECUTOR_DOCKER_NETWORK:-}")`, "deploy should pass the executor network override through SSH to the remote host")
	require.Contains(t, deployText, `docker image inspect "$sandbox_image"`, "worker prune should verify the sandbox image survived image pruning")
	require.Contains(t, deployText, `docker pull "$sandbox_image"`, "worker prune should re-pull the sandbox image when image pruning removes it")
	require.Contains(t, deployText, `deploy_worker_blue_green wait_container_healthy dump_diagnostics prune_docker_deploy_artifacts)`, "detached worker rollovers should embed the blue/green and prune helpers in the host-side script")
	require.NotContains(t, deployText, "run_worker_deployctl_in_container", "detached worker rollovers should not embed worker-container-bound deploy control helpers")
	require.Contains(t, deployText, `IMAGE_TAG='$IMAGE_TAG'`, "detached worker rollovers should bake IMAGE_TAG so the prune helper can protect the sandbox image")
	require.Contains(t, deployText, `prune_docker_deploy_artifacts worker`, "detached worker rollovers should prune only after the new worker is healthy")
	require.Contains(t, deployText, `flock -xo /tmp/143-deploy-worker.lock`, "detached worker rollovers should not let background drain watchers inherit the deploy lock")
	require.Contains(t, deployText, `prune_docker_deploy_artifacts "$ROLE"`, "synchronous deploy paths should prune after the rollout and health checks succeed")
	require.Contains(t, deployText, `DEPLOY_DOCKER_PRUNE=0`, "operators should have an explicit escape hatch for incident response or rollback-cache preservation")
}

func TestDetachedWorkerDeployReadsDatabaseEnvFromRemoteEnvFile(t *testing.T) {
	t.Parallel()

	deployScript, err := os.ReadFile("../deploy/scripts/deploy.sh")
	require.NoError(t, err, "test should read deploy script")
	deployText := string(deployScript)

	require.Contains(t, deployText, `DB_HOST=$(printf '%q' "$(read_worker_env_value DB_HOST)")`, "detached worker deploy should bake DB_HOST from the refreshed remote .env file")
	require.Contains(t, deployText, `DB_PASSWORD=$(printf '%q' "$(read_worker_env_value DB_PASSWORD)")`, "detached worker deploy should bake DB_PASSWORD from the refreshed remote .env file")
	require.NotContains(t, deployText, `DB_HOST='$DB_HOST'`, "detached worker deploy must not expand an unset parent-shell DB_HOST under set -u")
	require.NotContains(t, deployText, `DB_PASSWORD='$DB_PASSWORD'`, "detached worker deploy must not expand an unset parent-shell DB_PASSWORD under set -u")
}

func TestWorkerComposeConfiguresSessionExecutorNetwork(t *testing.T) {
	t.Parallel()

	compose, err := os.ReadFile("../docker-compose.worker.yml")
	require.NoError(t, err, "test should read worker compose file")
	composeText := string(compose)

	require.Contains(t, composeText, "SESSION_EXECUTOR_DOCKER_NETWORK", "worker compose should configure executor containers onto the worker docker network")
	require.Contains(t, composeText, "${SESSION_EXECUTOR_DOCKER_NETWORK:-143_default}", "worker compose should default executor containers to the production compose network")
	require.Contains(t, composeText, "SESSION_EXECUTOR_IMAGE: ghcr.io/assembledhq/143-server:${IMAGE_TAG:-latest}", "worker compose should provide the executor image required by production startup")
	require.NotContains(t, composeText, "SESSION_EXECUTORS_ENABLED", "worker compose should launch session executors without a dark-launch boolean")
}

// Pin the multi-resolver Docker DNS wiring so a future refactor doesn't
// silently drop it and reintroduce the single-upstream SPOF that produced
// the 2026-05-07T04:15Z incident (workers couldn't resolve github.com,
// sandboxes couldn't resolve chatgpt.com — same DNS path, same blast
// radius). The helper, the deploy invocation, the provisioning
// invocation, and the sudoers grant on every role must stay aligned;
// missing any one of them silently leaves a host on its inherited
// resolv.conf.
func TestDeployPinsDockerDaemonDNSResolvers(t *testing.T) {
	t.Parallel()

	helper, err := os.ReadFile("../deploy/scripts/install-docker-dns.sh")
	require.NoError(t, err, "install-docker-dns.sh should exist as the single source of truth for daemon.json `dns` configuration")
	helperText := string(helper)
	require.Contains(t, helperText, "/etc/docker/daemon.json", "install-docker-dns.sh should target daemon.json so dynamically-spawned sandbox containers also inherit the resolver list")
	require.Contains(t, helperText, "{dns: $ARGS.positional}", "install-docker-dns.sh should merge `dns` into daemon.json via jq (not overwrite the file) so log-driver / runtimes keys are preserved")
	require.Contains(t, helperText, "systemctl restart docker", "install-docker-dns.sh must restart docker on change — `dns` only takes effect for newly created containers")
	require.Contains(t, helperText, "mv ", "install-docker-dns.sh should write atomically (tempfile + rename) — a SIGKILL between truncate and write under a plain `>` would leave a zero-byte daemon.json that docker rejects")
	require.Contains(t, helperText, "is_ip_literal", "install-docker-dns.sh should reject hostname resolvers — using one would create a bootstrap dependency where the embedded resolver needs a working upstream just to discover its own upstream")
	require.Contains(t, helperText, "command -v jq", "install-docker-dns.sh should fail loudly with an actionable message if jq is missing rather than aborting mid-pipeline under set -e")
	require.Contains(t, helperText, "refusing to overwrite operator state", "install-docker-dns.sh should reject malformed daemon.json explicitly rather than silently overwriting an operator-edited file")
	require.Contains(t, helperText, `chmod --reference="$DAEMON_JSON"`, "install-docker-dns.sh should preserve daemon.json's existing mode so an operator who tightened permissions (e.g. 0640) doesn't get them silently widened")
	require.Contains(t, helperText, "all containers on this host will recycle", "install-docker-dns.sh should announce the docker restart so deploy-log readers know the next 30s of dropped connections is expected, not a regression")

	deployScript, err := os.ReadFile("../deploy/scripts/deploy.sh")
	require.NoError(t, err, "test should read deploy script")
	deployText := string(deployScript)
	require.Contains(t, deployText, "install-docker-dns.sh", "deploy.sh should sync and invoke install-docker-dns.sh for maintenance-capable deploys")
	require.Contains(t, deployText, "DOCKER_DNS_RESOLVERS=(1.1.1.1 8.8.8.8 9.9.9.9)", "deploy.sh should pin three independent resolver operators (Cloudflare, Google, Quad9) so a single-provider outage doesn't take fleet DNS down")
	require.Contains(t, deployText, "ALLOW_DEPLOY_DOCKER_DAEMON_RESTART", "deploy.sh should require an explicit maintenance flag before app deploys can restart Docker")
	require.Contains(t, deployText, "Skipping docker daemon DNS check on $ROLE deploy", "routine app and worker deploys should skip daemon-mutating DNS checks")
	require.Contains(t, deployText, "sudo -n /opt/143/deploy/scripts/install-docker-dns.sh", "deploy.sh should invoke install-docker-dns.sh via deploy+sudo so missing sudoers fails fast instead of hanging")
	require.Contains(t, deployText, "Retrying docker daemon DNS pinning after sudoers repair", "deploy.sh should retry install-docker-dns.sh after repairing sudoers so the first deploy that introduces the helper succeeds on legacy hosts")
	require.Contains(t, deployText, "warn_docker_dns_skipped", "deploy.sh should warn (not fail the deploy) when the DNS helper can't be installed — DNS hardening is operational, not a hard prerequisite for the rolling deploy")

	bootstrap, err := os.ReadFile("../deploy/scripts/bootstrap.sh")
	require.NoError(t, err, "test should read bootstrap.sh")
	require.Contains(t, string(bootstrap), "apt-get install -y jq", "bootstrap.sh should install jq because install-docker-dns.sh requires it during SSH provisioning")
	require.Contains(t, string(bootstrap), "/opt/143/deploy/scripts/install-docker-dns.sh *", "bootstrap.sh sudoers Cmnd_Alias must allow install-docker-dns.sh — without it the deploy+sudo path fails on app/worker hosts")

	provision, err := os.ReadFile("../deploy/scripts/provision.sh")
	require.NoError(t, err, "test should read provision.sh")
	provisionText := string(provision)
	require.Contains(t, provisionText, "configure-docker-daemon.sh", "provision.sh should pin DNS resolvers as part of the single Docker daemon hardening pass before services start")
	require.Contains(t, provisionText, "--dns 1.1.1.1 8.8.8.8 9.9.9.9", "provision.sh should pass three independent resolver operators into the daemon hardening helper")
	require.NotContains(t, provisionText, `"/opt/143/deploy/scripts/install-docker-dns.sh 1.1.1.1 8.8.8.8 9.9.9.9"`, "provision.sh must not invoke the DNS helper directly because log rotation and DNS should share one Docker restart on fresh hosts")
	require.GreaterOrEqual(t, strings.Count(provisionText, "/opt/143/deploy/scripts/install-docker-dns.sh *"), 3, "provision.sh inline bootstraps for db, logging, and redis must each grant deploy NOPASSWD sudo for install-docker-dns.sh")

	repair, err := os.ReadFile("../deploy/scripts/repair-deploy-sudoers.sh")
	require.NoError(t, err, "test should read repair-deploy-sudoers.sh")
	repairText := string(repair)
	require.Contains(t, repairText, "/opt/143/deploy/scripts/install-docker-dns.sh *", "repair-deploy-sudoers.sh should grant the install-docker-dns.sh sudoers entry — otherwise legacy-host repair via the no-teardown path leaves DNS pinning broken")
}

func TestConfigureDockerDaemonMergesHardeningInOnePass(t *testing.T) {
	t.Parallel()

	helper, err := os.ReadFile("../deploy/scripts/configure-docker-daemon.sh")
	require.NoError(t, err, "test should read configure-docker-daemon.sh")
	helperText := string(helper)
	require.Contains(t, helperText, "systemctl reset-failed docker.service docker.socket", "configure-docker-daemon.sh should clear systemd start-rate limits before retrying Docker startup")
	require.Contains(t, helperText, "systemctl restart docker", "configure-docker-daemon.sh should apply changed daemon config with a Docker restart")
	require.Contains(t, helperText, "docker info", "configure-docker-daemon.sh should verify Docker is usable after restart")

	tmp := t.TempDir()
	daemonPath := filepath.Join(tmp, "daemon.json")
	initial := `{
  "runtimes": {
    "runsc": {
      "path": "/usr/bin/runsc",
      "runtimeArgs": ["--ignore-cgroups", "--host-uds=open"]
    }
  },
  "features": {
    "containerd-snapshotter": true
  }
}`
	require.NoError(t, os.WriteFile(daemonPath, []byte(initial), 0o640), "test should seed daemon.json with worker runtime and operator-owned keys")

	cmd := exec.Command(
		"bash",
		"../deploy/scripts/configure-docker-daemon.sh",
		"--log-max-size", "100m",
		"--log-max-file", "5",
		"--dns", "1.1.1.1", "8.8.8.8", "9.9.9.9",
	)
	cmd.Env = append(os.Environ(),
		"DAEMON_JSON="+daemonPath,
		"SKIP_DOCKER_RESTART=1",
	)
	output, err := cmd.CombinedOutput()
	require.NoError(t, err, "configure-docker-daemon.sh should merge Docker hardening settings without requiring a live daemon: %s", string(output))

	raw, err := os.ReadFile(daemonPath)
	require.NoError(t, err, "test should read the merged daemon.json")

	var merged map[string]any
	require.NoError(t, json.Unmarshal(raw, &merged), "merged daemon.json should remain valid JSON")
	require.Equal(t, "json-file", merged["log-driver"], "merged daemon.json should set the json-file log driver")
	require.Equal(t, []any{"1.1.1.1", "8.8.8.8", "9.9.9.9"}, merged["dns"], "merged daemon.json should set the complete resolver list")

	logOpts, ok := merged["log-opts"].(map[string]any)
	require.True(t, ok, "merged daemon.json should include log-opts")
	require.Equal(t, "100m", logOpts["max-size"], "merged daemon.json should set the requested max log size")
	require.Equal(t, "5", logOpts["max-file"], "merged daemon.json should set the requested max log file count")

	runtimes, ok := merged["runtimes"].(map[string]any)
	require.True(t, ok, "merged daemon.json should preserve existing runtimes")
	runsc, ok := runtimes["runsc"].(map[string]any)
	require.True(t, ok, "merged daemon.json should preserve the runsc runtime block")
	require.Equal(t, "/usr/bin/runsc", runsc["path"], "merged daemon.json should preserve runsc path")

	features, ok := merged["features"].(map[string]any)
	require.True(t, ok, "merged daemon.json should preserve unrelated operator-owned keys")
	require.Equal(t, true, features["containerd-snapshotter"], "merged daemon.json should preserve unrelated nested values")

	info, err := os.Stat(daemonPath)
	require.NoError(t, err, "test should stat daemon.json after merge")
	require.Equal(t, os.FileMode(0o640), info.Mode().Perm(), "configure-docker-daemon.sh should preserve daemon.json file permissions")
}

func TestProvisionWaitsForDockerDaemonBeforePullingImages(t *testing.T) {
	t.Parallel()

	provision, err := os.ReadFile("../deploy/scripts/provision.sh")
	require.NoError(t, err, "test should read provision.sh")
	provisionText := string(provision)

	require.Contains(t, provisionText, "wait_for_docker_daemon()", "provision.sh should define a reusable Docker daemon readiness gate for fresh hosts")
	require.Contains(t, provisionText, "systemctl enable --now docker", "provision.sh should start Docker before the first daemon-backed docker command")
	require.Contains(t, provisionText, "su - deploy -c 'docker info >/dev/null 2>&1'", "provision.sh should verify the deploy user can reach the Docker daemon, not just that root can")
	require.Contains(t, provisionText, "journalctl -u docker", "provision.sh should print Docker daemon logs when readiness fails so setup failures are actionable")

	waitCallIndex := strings.Index(provisionText, "\nwait_for_docker_daemon\n")
	pullStepIndex := strings.Index(provisionText, "echo \"--- Step 4/5: Pulling images ---\"")
	require.NotEqual(t, -1, waitCallIndex, "provision.sh should call the Docker readiness gate")
	require.NotEqual(t, -1, pullStepIndex, "provision.sh should still have the image-pulling step")
	require.Less(t, waitCallIndex, pullStepIndex, "provision.sh should wait for Docker before docker login or docker pull runs")
}

// The synthetic DNS probe is what surfaces upstream DNS issues directly,
// before they cascade into user-visible failures. Pin its wiring: the
// service definition must exist in the shared compose include, both
// app and worker stacks must include it, the alert rule must match the
// log line the probe emits, and deploy/provision must stage the file so
// `docker compose up` can resolve the include directive.
func TestDNSProbeAlertingWiredEndToEnd(t *testing.T) {
	t.Parallel()

	probeCompose, err := os.ReadFile("../docker-compose.dns-probe.yml")
	require.NoError(t, err, "docker-compose.dns-probe.yml should define the shared dns-probe service")
	probeText := string(probeCompose)
	require.Contains(t, probeText, "dns-probe:", "compose file should declare the dns-probe service")
	require.Contains(t, probeText, `"dns probe failed"`, "probe must emit `dns probe failed` so vmalert / Grafana can match a stable string")
	require.Contains(t, probeText, "nslookup", "probe should use busybox nslookup (built into alpine) — apk install at runtime would itself depend on working DNS")
	require.NotContains(t, probeText, "apk add", "probe must not apk install at runtime — it would re-fetch on every restart and depend on the very DNS path it's meant to validate")

	app, err := os.ReadFile("../docker-compose.app.yml")
	require.NoError(t, err, "test should read app compose file")
	require.Contains(t, string(app), "docker-compose.dns-probe.yml", "app compose should include the shared dns-probe stack so every app host runs the probe")

	worker, err := os.ReadFile("../docker-compose.worker.yml")
	require.NoError(t, err, "test should read worker compose file")
	require.Contains(t, string(worker), "docker-compose.dns-probe.yml", "worker compose should include the shared dns-probe stack so every worker host runs the probe")

	alerts, err := os.ReadFile("../deploy/vmalert/rules/production-alerts.yml")
	require.NoError(t, err, "test should read production vmalert rules")
	alertText := string(alerts)
	require.Contains(t, alertText, "DNSProbeFailures", "vmalert rules should declare the DNSProbeFailures alert")
	require.Contains(t, alertText, `_msg:"dns probe failed"`, "DNSProbeFailures must match on the exact message the probe emits — drift between the probe and the rule silently breaks alerting")
	require.Contains(t, alertText, "stats by (target, hostname)", "DNSProbeFailures should group by (target, hostname) so a host-local issue (one worker's daemon.json missing the dns pin) is distinguishable from a fleet-wide upstream outage")

	deployScript, err := os.ReadFile("../deploy/scripts/deploy.sh")
	require.NoError(t, err, "test should read deploy script")
	require.Contains(t, string(deployScript), `"$PROJECT_DIR/docker-compose.dns-probe.yml"`, "deploy.sh should scp docker-compose.dns-probe.yml to app and worker hosts so the include directive resolves")

	provision, err := os.ReadFile("../deploy/scripts/provision.sh")
	require.NoError(t, err, "test should read provision.sh")
	require.Contains(t, string(provision), `"$PROJECT_DIR/docker-compose.dns-probe.yml"`, "provision.sh should stage docker-compose.dns-probe.yml on fresh hosts before services start")
}

// The DNS probe → vector → vmalert pipeline depends on three field names
// surviving end-to-end intact: `message` (the probe writes it, vector
// promotes it to `_msg` via _msg_field, the alert matches on
// `_msg:"dns probe failed"`); `probe` and `target` (the probe writes them,
// vector merges them to root via parse_json+merge, the alert filters on
// `probe:dns` and groups by `target`); and `hostname` (vector enriches
// from get_hostname(), the alert groups by `hostname`).
//
// Vector's enrichment in deploy/vector.yaml protects a denylist of
// metadata keys from being overwritten by parsed JSON. If a future change
// adds `probe` / `target` / `message` / `hostname` to that denylist (or
// removes them from the merge path), the alert silently stops firing on
// real DNS outages — exactly the failure mode this whole stack exists to
// prevent. Pin the assumptions explicitly.
func TestDNSProbeVectorAlertFieldNamesAlign(t *testing.T) {
	t.Parallel()

	probe, err := os.ReadFile("../docker-compose.dns-probe.yml")
	require.NoError(t, err, "test should read dns-probe compose file")
	probeText := string(probe)
	require.Contains(t, probeText, `"message":"%s"`, "probe must emit a `message` field — vector's _msg_field is `message`, so the alert's `_msg:` match depends on it")
	require.Contains(t, probeText, `"probe":"dns"`, "probe must emit `probe:dns` — the alert filters on it to avoid catching unrelated `dns probe failed` strings from other services")
	require.Contains(t, probeText, `"target":"%s"`, "probe must emit a `target` field — the alert groups by target to keep per-vendor outages from page-bombing on-call")

	vector, err := os.ReadFile("../deploy/vector.yaml")
	require.NoError(t, err, "test should read vector.yaml")
	vectorText := string(vector)
	require.Contains(t, vectorText, "_msg_field: \"message\"", "vector.yaml must promote .message to _msg or the alert's `_msg:` match will never fire")
	require.Contains(t, vectorText, ". = merge(., parsed)", "vector.yaml must merge parsed zerolog JSON to root — without this, .probe and .target stay nested under .message and the alert query can't see them")
	require.Contains(t, vectorText, `.hostname = get_hostname() ?? "unknown"`, "vector.yaml must enrich each log line with .hostname without emitting unused-variable VRL warnings")

	// Explicitly assert the keys the alert depends on are NOT in vector's
	// protected denylist. Adding any of them would cause vector to
	// overwrite the probe's value with the docker_logs metadata of the
	// same name (or null), silently breaking the alert.
	for _, field := range []string{"probe", "target", "message"} {
		require.NotContains(t, vectorText, "protected_"+field+" = .", fmt.Sprintf("vector.yaml must not protect `%s` — the probe writes it and the alert depends on the probe's value flowing through unmodified", field))
	}
}

func TestRollingDeployAllowsCaddyToDiscoverNewUpstreamBeforeDrainingOld(t *testing.T) {
	t.Parallel()

	deployScript, err := os.ReadFile("../deploy/scripts/deploy.sh")
	require.NoError(t, err, "test should read deploy script")
	deployText := string(deployScript)

	require.Contains(t, deployText, `CADDY_UPSTREAM_DISCOVERY_ATTEMPTS:-10`, "rolling deploy should use a bounded readiness loop instead of a single blind sleep")
	require.Contains(t, deployText, `for i in $(seq 1 "$attempts")`, "rolling deploy should retry Caddy reachability checks before old containers drain")
	require.Contains(t, deployText, `docker exec "$caddy_id" sh -c`, "rolling deploy should probe from inside the Caddy container network namespace")
	require.Contains(t, deployText, `resolve_caddy_service_ips "$caddy_id" "$service"`, "rolling deploy should verify Caddy's service-name DNS path includes the new upstream")
	require.Contains(t, deployText, `grep -Fxq "$new_ip"`, "rolling deploy should wait until Docker DNS resolves the service name to the new container IP")
	require.Contains(t, deployText, `CADDY_DYNAMIC_REFRESH_SECONDS:-2`, "rolling deploy should give Caddy one dynamic upstream refresh after DNS includes the new container")
	require.Contains(t, deployText, `read -r first second third _`, "rolling deploy should parse BusyBox nslookup Address N rows by field position")
	require.Contains(t, deployText, `Address)`, "rolling deploy should handle BusyBox nslookup rows like `Address 1: 172.20.0.5 api`")
	require.NotContains(t, deployText, `ip="${line##* }"`, "rolling deploy must not parse nslookup answers from the trailing token because BusyBox may append the hostname after the IP")
	require.Contains(t, deployText, `http://$new_ip:$port/healthz`, "rolling deploy should probe the new container's health endpoint directly")
	require.Contains(t, deployText, `wait_caddy_upstream_discovery "$service" "$new_container"`, "rolling deploy should wait for Caddy to discover the new upstream before old containers drain")

	waitIndex := strings.Index(deployText, `wait_caddy_upstream_discovery "$service" "$new_container"`)
	drainIndex := strings.Index(deployText, `echo "Draining $old_count old $service container(s)`)
	require.NotEqual(t, -1, waitIndex, "wait call should be present in deploy script")
	require.NotEqual(t, -1, drainIndex, "old-container drain should be present in deploy script")
	require.Less(t, waitIndex, drainIndex, "Caddy upstream discovery wait should happen before draining old containers")
}

func TestRollingDeployDiagnosticsUseFailedServiceLogs(t *testing.T) {
	t.Parallel()

	deployScript, err := os.ReadFile("../deploy/scripts/deploy.sh")
	require.NoError(t, err, "test should read deploy script")
	deployText := string(deployScript)

	require.Contains(t, deployText, `wait_container_healthy "$new_container" 180 "$service"`, "rolling app deploys should tell health diagnostics which service failed")
	require.Contains(t, deployText, `service="${3:-$HEALTH_SERVICE}"`, "health wait should default to the role health service when no service is passed")
	require.Contains(t, deployText, `echo "--- Last 50 lines of $service logs ---"`, "diagnostics should label logs with the failed service name")
	require.Contains(t, deployText, `docker compose -f "$COMPOSE_FILE" logs --tail=50 "$service"`, "diagnostics should print logs from the failed service, not always the role health service")
}

func TestLoggingDeploySyncsProvisionedObservabilityConfig(t *testing.T) {
	t.Parallel()

	deployScript, err := os.ReadFile("../deploy/scripts/deploy.sh")
	require.NoError(t, err, "test should read deploy script")
	deployText := string(deployScript)

	require.Contains(t, deployText, `"$ROLE" = "logging"`, "deploy script should have logging-role handling")
	require.Contains(t, deployText, "docker-compose.vector.yml", "logging deploy should sync the shared Vector compose include")
	require.Contains(t, deployText, "deploy/vector.yaml", "logging deploy should sync Vector config for the logging node")
	require.Contains(t, deployText, "deploy/grafana/provisioning", "logging deploy should sync Grafana provisioning files")
	require.Contains(t, deployText, "deploy/vmalert/rules", "logging deploy should sync vmalert rules")
	require.Contains(t, deployText, "deploy/scripts/alertmanager_slack_relay.py", "logging deploy should sync the Alertmanager Slack relay script mounted by docker-compose.logging.yml")
	require.Contains(t, deployText, "rm -rf /opt/143/deploy/grafana/provisioning /opt/143/deploy/vmalert/rules", "logging deploy should remove stale provisioned dashboards and rules before syncing repo-owned config")

	compose, err := os.ReadFile("../docker-compose.logging.yml")
	require.NoError(t, err, "test should read logging compose file")
	composeText := string(compose)
	require.Contains(t, composeText, "docker-compose.vector.yml", "logging compose should include the shared Vector collector")
	require.Contains(t, deployText, "SERVER_ROLE=%s", "logging deploy should write SERVER_ROLE=logging for Vector")
	vectorCheck := deployText[strings.Index(deployText, "# Verify Vector is running"):]
	require.Contains(t, vectorCheck, `"$ROLE" = "logging"`, "logging deploy should verify the logging-node Vector collector after stack recreation")

	vectorCompose, err := os.ReadFile("../docker-compose.vector.yml")
	require.NoError(t, err, "test should read shared Vector compose file")
	require.NotContains(t, string(vectorCompose), "--api.enabled", "Vector compose must not pass API settings as CLI flags because the pinned Vector image rejects them")
	require.Contains(t, string(vectorCompose), "http://127.0.0.1:8686/health", "Vector healthcheck should probe IPv4 loopback because the API is configured on 0.0.0.0 and localhost may resolve to IPv6 first")
	vectorConfig, err := os.ReadFile("../deploy/vector.yaml")
	require.NoError(t, err, "test should read Vector config")
	require.Contains(t, string(vectorConfig), "api:", "Vector config should enable the API in vector.yaml")
	require.Contains(t, string(vectorConfig), "enabled: true", "Vector API should stay enabled for the healthcheck")
	require.Contains(t, string(vectorConfig), `address: "0.0.0.0:8686"`, "Vector API should bind the healthcheck address from config")

	dashboardProvider, err := os.ReadFile("../deploy/grafana/provisioning/dashboards/dashboards.yml")
	require.NoError(t, err, "test should read Grafana dashboard provider config")
	require.Contains(t, string(dashboardProvider), "disableDeletion: false", "Grafana dashboard provisioning should remove dashboards deleted from the repo")
}

func TestDeployWaitsForVectorHealthcheck(t *testing.T) {
	t.Parallel()

	deployScript, err := os.ReadFile("../deploy/scripts/deploy.sh")
	require.NoError(t, err, "test should read deploy script")
	deployText := string(deployScript)

	require.Contains(t, deployText, "wait_vector_healthy()", "deploy script should have a dedicated Vector health wait helper")
	require.Contains(t, deployText, `VECTOR_HEALTH_TIMEOUT:-90`, "Vector health wait should give Docker healthchecks time to leave the initial starting state")
	require.Contains(t, deployText, `health="$(docker inspect --format '{{if .State.Health}}{{.State.Health.Status}}{{else}}none{{end}}' "$cid"`, "Vector health wait should read Docker's health status on each poll")
	require.Contains(t, deployText, `if [ "$state" = "exited" ] || [ "$state" = "dead" ]; then`, "Vector health wait should still fail immediately when the collector has exited")
	require.Contains(t, deployText, `wait_vector_healthy "$VECTOR_ID"`, "deploy script should call the Vector health wait before declaring deploy success")

	require.Contains(t, deployText, `"healthy"`, "deploy should require Vector's healthcheck to report healthy")
	require.Contains(t, deployText, `"none"`, "deploy may accept running only when no healthcheck exists")
	require.Contains(t, deployText, `Vector is not healthy`, "deploy should fail closed for Restarting, unhealthy, missing, and other non-healthy states")
	require.Contains(t, deployText, `logs --tail=50 vector`, "deploy failure should print enough Vector logs to diagnose crash loops")
}

// Sandbox DNS resolution depends on three values agreeing across three
// files:
//
//   - the bridge subnet pinned by provision.sh (so sandbox-dns can claim a
//     stable IP),
//   - the sandbox-dns container's static ipv4_address in the worker compose
//     file,
//   - the nameserver line written into /etc/143/sandbox-resolv.conf, which
//     every sandbox bind-mounts as /etc/resolv.conf.
//
// If any of these drifts independently, sandboxes silently lose DNS for
// preview-infrastructure container names and every preview start fails at
// the first migration. Lock the alignment in CI so a future renumbering
// has to touch all three files in the same PR.
func TestSandboxDNSConfigAlignment(t *testing.T) {
	t.Parallel()

	const (
		sandboxSubnet = "172.30.0.0/24"
		sandboxDNSIP  = "172.30.0.2"
	)

	prefix, err := netip.ParsePrefix(sandboxSubnet)
	require.NoError(t, err, "test constant for sandbox subnet should be a valid CIDR")
	dnsAddr, err := netip.ParseAddr(sandboxDNSIP)
	require.NoError(t, err, "test constant for sandbox-dns IP should be a valid address")
	require.True(t, prefix.Contains(dnsAddr), "sandbox-dns IP %s must lie inside the pinned subnet %s", sandboxDNSIP, sandboxSubnet)

	provisionScript, err := os.ReadFile("../deploy/scripts/provision.sh")
	require.NoError(t, err, "test should read the provisioning script")
	provisionText := string(provisionScript)
	require.NotContains(t, provisionText, "enable_icc=false", "provision.sh must not disable bridge ICC because sandboxes must reach sandbox-dns on the shared bridge")

	reconcileScript, err := os.ReadFile("../deploy/scripts/reconcile-worker-host.sh")
	require.NoError(t, err, "test should read the worker host reconciliation script")
	reconcileText := string(reconcileScript)
	require.Contains(t, reconcileText, `SANDBOX_SUBNET="`+sandboxSubnet+`"`, "reconcile-worker-host.sh should define the pinned sandbox subnet so sandbox-dns gets a predictable static IP")
	require.Contains(t, reconcileText, `ensure_bridge "$SANDBOX_NETWORK" "$SANDBOX_SUBNET"`, "reconcile-worker-host.sh should create 143-sandbox with the pinned subnet variable")
	require.Contains(t, reconcileText, `"$existing_subnet" != "$subnet"`, "reconcile-worker-host.sh should fail loudly when an existing 143-sandbox network has a different subnet — silent reuse breaks the static-IP mapping")

	// The sandbox resolv.conf writer is the single source of truth for the
	// nameserver line. provision.sh and deploy.sh both call it so a content
	// change rolls out via routine deploys instead of requiring a fleet-wide
	// reprovision maintenance window.
	resolvScript, err := os.ReadFile("../deploy/scripts/sandbox-resolv-conf.sh")
	require.NoError(t, err, "test should read the sandbox resolv.conf writer")
	require.Contains(t, string(resolvScript), `NAMESERVER="${2:-`+sandboxDNSIP+`}"`, "sandbox-resolv-conf.sh should default to sandbox-dns's IP for /etc/143/sandbox-resolv.conf")
	require.Contains(t, reconcileText, "/opt/143/deploy/scripts/sandbox-resolv-conf.sh", "reconcile-worker-host.sh should delegate to the shared writer instead of inlining the file content")
	deployScript, err := os.ReadFile("../deploy/scripts/deploy.sh")
	require.NoError(t, err, "test should read the deploy script")
	deployText := string(deployScript)
	require.Contains(t, deployText, "/opt/143/deploy/scripts/reconcile-worker-host.sh 143-sandbox", "deploy.sh should refresh worker host invariants through the canonical reconciliation script")
	require.NotContains(t, deployText, "enable_icc=false", "deploy.sh must not create 143-sandbox with bridge ICC disabled because Docker blocks sandbox DNS before DOCKER-USER can carve it out")

	compose, err := os.ReadFile("../docker-compose.worker.yml")
	require.NoError(t, err, "test should read the worker compose file")
	composeText := string(compose)
	require.Contains(t, composeText, "ipv4_address: "+sandboxDNSIP, "worker compose should pin sandbox-dns to the same static IP that sandbox-resolv.conf points at")
	require.Contains(t, composeText, "name: 143-sandbox", "worker compose should attach sandbox-dns to the externally-managed 143-sandbox bridge, not a compose-private network")
	require.Contains(t, composeText, "external: true", "worker compose should declare the sandbox network as external — the bridge is created by provision.sh, not compose")

	cloudInit, err := os.ReadFile("../deploy/cloud-init/worker.yml")
	require.NoError(t, err, "test should read the worker cloud-init template")
	cloudInitText := string(cloudInit)
	require.Contains(t, cloudInitText, "/opt/143/deploy/scripts/reconcile-worker-host.sh 143-sandbox", "worker cloud-init should reconcile worker host invariants before starting the worker")
	require.Contains(t, cloudInitText, "cp /tmp/143-repo/Dockerfile.dnsmasq /opt/143/", "worker cloud-init should stage Dockerfile.dnsmasq before docker compose starts so sandbox-dns can build on first boot")
	require.NotContains(t, cloudInitText, "enable_icc=false", "worker cloud-init must leave bridge ICC enabled so first-boot sandboxes can reach sandbox-dns")
	require.Contains(t, provisionText, `"$PROJECT_DIR/Dockerfile.dnsmasq" root@"$HOST":/opt/143/`, "provision.sh should stage Dockerfile.dnsmasq before docker compose starts so sandbox-dns can build on fresh worker provisioning")

	dockerfile, err := os.ReadFile("../Dockerfile.dnsmasq")
	require.NoError(t, err, "test should read the dnsmasq Dockerfile")
	dockerfileText := string(dockerfile)
	require.Contains(t, dockerfileText, "--server=127.0.0.11", "dnsmasq must forward to Docker's embedded resolver — that's the only place preview-infra container names are registered")
	require.Contains(t, dockerfileText, "--no-resolv", "dnsmasq must ignore its own /etc/resolv.conf (which itself points at 127.0.0.11) to avoid a forwarding loop")
}

func TestWorkerReconciliationOwnsSandboxAuthSocketDirBeforeCompose(t *testing.T) {
	t.Parallel()

	reconcileScript, err := os.ReadFile("../deploy/scripts/reconcile-worker-host.sh")
	require.NoError(t, err, "test should read the worker host reconciliation script")
	reconcileText := string(reconcileScript)
	require.Contains(t, reconcileText, "/etc/tmpfiles.d/143-sandbox-auth.conf", "worker reconciliation should install tmpfiles config for the sandbox auth socket dir")
	require.Contains(t, reconcileText, "chown 1000:1000 /var/run/143/sandbox-auth", "worker reconciliation should force appuser ownership on the sandbox auth socket dir")
	require.Contains(t, reconcileText, "chmod 0750 /var/run/143/sandbox-auth", "worker reconciliation should force 0750 permissions on the sandbox auth socket dir")

	cloudInit, err := os.ReadFile("../deploy/cloud-init/worker.yml")
	require.NoError(t, err, "test should read the worker cloud-init template")
	cloudInitText := string(cloudInit)

	reconcileIndex := strings.Index(cloudInitText, "/opt/143/deploy/scripts/reconcile-worker-host.sh 143-sandbox")
	composeIndex := strings.Index(cloudInitText, "docker compose -f docker-compose.worker.yml up -d --remove-orphans")

	require.NotEqual(t, -1, reconcileIndex, "worker cloud-init should call worker host reconciliation")
	require.NotEqual(t, -1, composeIndex, "worker cloud-init should still start the worker compose stack")
	require.Less(t, reconcileIndex, composeIndex, "worker cloud-init must reconcile host invariants before Docker compose can auto-create bind-mount sources")
}

func TestWorkerDeployRunsReconciliationBeforeCompose(t *testing.T) {
	t.Parallel()

	deployScript, err := os.ReadFile("../deploy/scripts/deploy.sh")
	require.NoError(t, err, "test should read the deploy script")
	deployText := string(deployScript)

	syncIndex := strings.Index(deployText, "$PROJECT_DIR/deploy/scripts/reconcile-worker-host.sh")
	reconcileIndex := strings.LastIndex(deployText, "run_worker_host_reconcile")
	composeIndex := strings.LastIndex(deployText, "deploy_worker_blue_green")

	require.NotEqual(t, -1, syncIndex, "worker deploy should sync reconcile-worker-host.sh before running it")
	require.NotEqual(t, -1, reconcileIndex, "worker deploy should repair worker host invariants through the canonical reconciliation script")
	require.NotEqual(t, -1, composeIndex, "worker deploy should still start a new worker generation")
	require.Less(t, syncIndex, reconcileIndex, "worker deploy must sync the latest reconciliation script before executing it")
	require.Less(t, reconcileIndex, composeIndex, "worker deploy must repair host invariants before the new worker starts")
}

func TestWorkerDeployRequiresExactRunscHostUDSOpen(t *testing.T) {
	t.Parallel()

	deployScript, err := os.ReadFile("../deploy/scripts/deploy.sh")
	require.NoError(t, err, "test should read the deploy script")
	deployText := string(deployScript)

	require.Contains(t, deployText, `grep -Eq -- '--host-uds(=|[[:space:]]+)open' "$DAEMON_JSON"`, "worker deploy should verify runsc host UDS is open, not merely that a host-uds flag exists")
	require.Contains(t, deployText, "sudo runsc install -- --ignore-cgroups --host-uds=open", "worker deploy should repair runsc with host UDS opened for sandbox credential sockets")
	require.NotContains(t, deployText, `grep -q "host-uds" "$DAEMON_JSON"`, "worker deploy must not accept an arbitrary host-uds value because host-uds=none still breaks sandbox credential sockets")
}

func TestProvisionAndMakeExposeWorkerReconciliation(t *testing.T) {
	t.Parallel()

	provisionScript, err := os.ReadFile("../deploy/scripts/provision.sh")
	require.NoError(t, err, "test should read the provisioning script")
	require.Contains(t, string(provisionScript), "/opt/143/deploy/scripts/reconcile-worker-host.sh 143-sandbox", "worker provisioning should use the same host reconciliation path as cloud-init and deploy")

	makefile, err := os.ReadFile("../Makefile")
	require.NoError(t, err, "test should read the Makefile")
	makefileText := string(makefile)
	require.Contains(t, makefileText, "repair-worker-host", "Makefile should expose an obvious worker host repair command")
	require.Contains(t, makefileText, "sudo -n /opt/143/deploy/scripts/reconcile-worker-host.sh 143-sandbox", "worker repair target should run the canonical reconciliation script on the host")
}

func TestWorkerCloudInitInstallsDeploySudoersGrant(t *testing.T) {
	t.Parallel()

	cloudInit, err := os.ReadFile("../deploy/cloud-init/worker.yml")
	require.NoError(t, err, "test should read the worker cloud-init template")
	cloudInitText := string(cloudInit)

	require.Contains(t, cloudInitText, "/etc/sudoers.d/99-deploy", "worker cloud-init should install the deploy sudoers grant so cloud-init-only workers survive routine fleet deploys")
	require.Contains(t, cloudInitText, "deploy ALL=(root) NOPASSWD: DEPLOY_CMDS", "worker cloud-init should grant only the narrow deploy command alias")
	require.Contains(t, cloudInitText, "/opt/143/deploy/scripts/reconcile-worker-host.sh 143-sandbox", "worker cloud-init sudoers should allow deploy-time worker host reconciliation")
	require.Contains(t, cloudInitText, "/opt/143/deploy/scripts/install-log-rotation.sh *", "worker cloud-init sudoers should allow deploy-time Docker log rotation")
	require.Contains(t, cloudInitText, "/opt/143/deploy/scripts/install-docker-dns.sh *", "worker cloud-init sudoers should allow deploy-time Docker DNS pinning")
	require.Contains(t, cloudInitText, "visudo -cf /etc/sudoers.d/99-deploy", "worker cloud-init should validate sudoers before first deploy depends on it")
	require.NotContains(t, cloudInitText, "No sudo here", "worker cloud-init comments should not imply sudoers is external to cloud-init")
}

func TestWorkerCanReachSandboxBridge(t *testing.T) {
	t.Parallel()

	compose, err := os.ReadFile("../docker-compose.worker.yml")
	require.NoError(t, err, "test should read the worker compose file")

	var parsed struct {
		Services map[string]struct {
			Networks map[string]struct {
				GatewayPriority int `yaml:"gw_priority"`
			} `yaml:"networks"`
		} `yaml:"services"`
	}
	require.NoError(t, yaml.Unmarshal(compose, &parsed), "worker compose should be valid YAML")

	worker, ok := parsed.Services["worker"]
	require.True(t, ok, "worker compose should define the worker service")
	require.Contains(t, worker.Networks, "default", "worker must stay on the default compose network so it can reach chrome and other local services")
	require.Contains(t, worker.Networks, "sandbox", "worker must join 143-sandbox so preview proxy dials can reach sandbox container IPs")
	require.Greater(t, worker.Networks["default"].GatewayPriority, worker.Networks["sandbox"].GatewayPriority, "worker default gateway must stay on the compose default network so DB/private-fleet traffic does not route through the sandbox bridge")
}
