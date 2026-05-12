package deploy_test

import (
	"encoding/json"
	"fmt"
	"net/netip"
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
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

// Worker preview routing requires three per-host values: NODE_ID,
// WORKER_PRIVATE_IP, and PREVIEW_INTERNAL_BASE_URL. They live in
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
	require.Contains(t, string(compose), ":8080:8080", "worker compose should publish port 8080 so the app node can reach the worker's internal preview API")

	cloudInit, err := os.ReadFile("../deploy/cloud-init/worker.yml")
	require.NoError(t, err, "test should read the worker cloud-init template")
	require.Contains(t, string(cloudInit), "/opt/143/.env.local", "worker cloud-init should write per-host identity to .env.local so deploy.sh can preserve it across deploys")
	require.Contains(t, string(cloudInit), "NODE_ID=${NODE_ID}", "worker cloud-init should populate NODE_ID in .env.local")
	require.Contains(t, string(cloudInit), "WORKER_PRIVATE_IP=${WORKER_PRIVATE_IP}", "worker cloud-init should populate WORKER_PRIVATE_IP in .env.local")
	require.Contains(t, string(cloudInit), "PREVIEW_INTERNAL_BASE_URL=${PREVIEW_INTERNAL_BASE_URL}", "worker cloud-init should populate PREVIEW_INTERNAL_BASE_URL in .env.local")
	require.Contains(t, string(cloudInit), "cat /opt/143/.env.local >> /opt/143/.env", "worker cloud-init should concatenate .env.local into .env so docker compose can interpolate ${WORKER_PRIVATE_IP} etc.")

	provisionScript, err := os.ReadFile("../deploy/scripts/provision.sh")
	require.NoError(t, err, "test should read the provisioning script")
	require.Contains(t, string(provisionScript), "NODE_ID=%s", "provision.sh should write NODE_ID into .env.local")
	require.Contains(t, string(provisionScript), "WORKER_PRIVATE_IP=%s", "provision.sh should write WORKER_PRIVATE_IP into .env.local")
	require.Contains(t, string(provisionScript), "PREVIEW_INTERNAL_BASE_URL=%s", "provision.sh should write PREVIEW_INTERNAL_BASE_URL into .env.local")
	require.Contains(t, string(provisionScript), "/opt/143/.env.local", "provision.sh should target /opt/143/.env.local")
	require.Contains(t, string(provisionScript), "cat /opt/143/.env.local >> /opt/143/.env", "provision.sh should concatenate .env.local into .env after writing both")

	deployScript, err := os.ReadFile("../deploy/scripts/deploy.sh")
	require.NoError(t, err, "test should read the deploy script")
	require.Contains(t, string(deployScript), "cat /opt/143/.env.local >> /opt/143/.env", "deploy.sh worker branch should re-append .env.local into .env on every deploy — without this, every secret refresh wipes the per-host identity")
	require.Contains(t, string(deployScript), "/opt/143/.env.local is missing", "deploy.sh worker branch should abort loudly when .env.local is missing instead of coming up with empty NODE_ID and WORKER_PRIVATE_IP")
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
	require.Contains(t, deployText, "install-log-rotation.sh", "deploy.sh should sync and invoke the install-log-rotation.sh helper on every deploy")
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
	require.Contains(t, provisionText, "install-log-rotation.sh", "provision.sh should invoke install-log-rotation.sh after staging deploy/ so newly-provisioned hosts have rotation in place before services start (closes the provision-to-first-deploy unbounded-growth window)")
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

func TestDeployPrunesDockerArtifactsAfterSuccessfulRollout(t *testing.T) {
	t.Parallel()

	deployScript, err := os.ReadFile("../deploy/scripts/deploy.sh")
	require.NoError(t, err, "test should read deploy script")
	deployText := string(deployScript)

	require.Contains(t, deployText, "prune_docker_deploy_artifacts()", "deploy.sh should define one prune helper so app, worker, and detached worker paths stay aligned")
	require.Contains(t, deployText, `docker container prune -f --filter "until=$prune_until"`, "deploy prune should remove stopped containers after a successful rollout")
	require.Contains(t, deployText, `docker image prune -af --filter "until=$prune_until"`, "deploy prune should remove old unused SHA-tagged images after a successful rollout")
	require.Contains(t, deployText, `docker builder prune -af --filter "until=$prune_until"`, "deploy prune should remove unused build cache after a successful rollout")
	require.Contains(t, deployText, `"DEPLOY_DOCKER_PRUNE=${DEPLOY_DOCKER_PRUNE:-1}"`, "deploy should pass the prune enable/disable knob through SSH to the remote host")
	require.Contains(t, deployText, `"DOCKER_PRUNE_UNTIL=${DOCKER_PRUNE_UNTIL:-24h}"`, "deploy should pass the prune age window through SSH to the remote host")
	require.Contains(t, deployText, `docker image inspect "$sandbox_image"`, "worker prune should verify the sandbox image survived image pruning")
	require.Contains(t, deployText, `docker pull "$sandbox_image"`, "worker prune should re-pull the sandbox image when image pruning removes it")
	require.Contains(t, deployText, `$(declare -f drain_worker_service wait_container_healthy dump_diagnostics prune_docker_deploy_artifacts)`, "detached worker rollovers should embed the prune helper in the host-side script")
	require.Contains(t, deployText, `IMAGE_TAG='$IMAGE_TAG'`, "detached worker rollovers should bake IMAGE_TAG so the prune helper can protect the sandbox image")
	require.Contains(t, deployText, `prune_docker_deploy_artifacts worker`, "detached worker rollovers should prune only after the new worker is healthy")
	require.Contains(t, deployText, `prune_docker_deploy_artifacts "$ROLE"`, "synchronous deploy paths should prune after the rollout and health checks succeed")
	require.Contains(t, deployText, `DEPLOY_DOCKER_PRUNE=0`, "operators should have an explicit escape hatch for incident response or rollback-cache preservation")
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
	require.Contains(t, deployText, "install-docker-dns.sh", "deploy.sh should sync and invoke install-docker-dns.sh on every deploy")
	require.Contains(t, deployText, "DOCKER_DNS_RESOLVERS=(1.1.1.1 8.8.8.8 9.9.9.9)", "deploy.sh should pin three independent resolver operators (Cloudflare, Google, Quad9) so a single-provider outage doesn't take fleet DNS down")
	require.Contains(t, deployText, "sudo -n /opt/143/deploy/scripts/install-docker-dns.sh", "deploy.sh should invoke install-docker-dns.sh via deploy+sudo so missing sudoers fails fast instead of hanging")
	require.Contains(t, deployText, "Retrying docker daemon DNS pinning after sudoers repair", "deploy.sh should retry install-docker-dns.sh after repairing sudoers so the first deploy that introduces the helper succeeds on legacy hosts")
	require.Contains(t, deployText, "warn_docker_dns_skipped", "deploy.sh should warn (not fail the deploy) when the DNS helper can't be installed — DNS hardening is operational, not a hard prerequisite for the rolling deploy")

	bootstrap, err := os.ReadFile("../deploy/scripts/bootstrap.sh")
	require.NoError(t, err, "test should read bootstrap.sh")
	require.Contains(t, string(bootstrap), "/opt/143/deploy/scripts/install-docker-dns.sh *", "bootstrap.sh sudoers Cmnd_Alias must allow install-docker-dns.sh — without it the deploy+sudo path fails on app/worker hosts")

	provision, err := os.ReadFile("../deploy/scripts/provision.sh")
	require.NoError(t, err, "test should read provision.sh")
	provisionText := string(provision)
	require.Contains(t, provisionText, "install-docker-dns.sh 1.1.1.1 8.8.8.8 9.9.9.9", "provision.sh should pin DNS resolvers in /etc/docker/daemon.json before services start so newly-provisioned hosts don't inherit the host's single-upstream resolv.conf")
	require.GreaterOrEqual(t, strings.Count(provisionText, "/opt/143/deploy/scripts/install-docker-dns.sh *"), 3, "provision.sh inline bootstraps for db, logging, and redis must each grant deploy NOPASSWD sudo for install-docker-dns.sh")

	repair, err := os.ReadFile("../deploy/scripts/repair-deploy-sudoers.sh")
	require.NoError(t, err, "test should read repair-deploy-sudoers.sh")
	repairText := string(repair)
	require.Contains(t, repairText, "/opt/143/deploy/scripts/install-docker-dns.sh *", "repair-deploy-sudoers.sh should grant the install-docker-dns.sh sudoers entry — otherwise legacy-host repair via the no-teardown path leaves DNS pinning broken")
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
	require.Contains(t, vectorText, ".hostname, err = get_hostname()", "vector.yaml must enrich each log line with .hostname — DNSProbeFailures groups by hostname to distinguish host-local from fleet-wide failures")

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
	require.Contains(t, deployText, "rm -rf /opt/143/deploy/grafana/provisioning /opt/143/deploy/vmalert/rules", "logging deploy should remove stale provisioned dashboards and rules before syncing repo-owned config")

	compose, err := os.ReadFile("../docker-compose.logging.yml")
	require.NoError(t, err, "test should read logging compose file")
	composeText := string(compose)
	require.Contains(t, composeText, "docker-compose.vector.yml", "logging compose should include the shared Vector collector")
	require.Contains(t, deployText, "SERVER_ROLE=%s", "logging deploy should write SERVER_ROLE=logging for Vector")
	vectorCheck := deployText[strings.Index(deployText, "# Verify Vector is running"):]
	require.Contains(t, vectorCheck, `"$ROLE" = "logging"`, "logging deploy should verify the logging-node Vector collector after stack recreation")

	dashboardProvider, err := os.ReadFile("../deploy/grafana/provisioning/dashboards/dashboards.yml")
	require.NoError(t, err, "test should read Grafana dashboard provider config")
	require.Contains(t, string(dashboardProvider), "disableDeletion: false", "Grafana dashboard provisioning should remove dashboards deleted from the repo")
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
	require.Contains(t, provisionText, "--subnet "+sandboxSubnet, "provision.sh should create 143-sandbox with the pinned subnet so sandbox-dns gets a predictable static IP")
	require.Contains(t, provisionText, `"$EXISTING_SANDBOX_SUBNET" != "`+sandboxSubnet+`"`, "provision.sh should fail loudly when an existing 143-sandbox network has a different subnet — silent reuse breaks the static-IP mapping")
	require.NotContains(t, provisionText, "enable_icc=false", "provision.sh must not disable bridge ICC because sandboxes must reach sandbox-dns on the shared bridge")

	// The sandbox resolv.conf writer is the single source of truth for the
	// nameserver line. provision.sh and deploy.sh both call it so a content
	// change rolls out via routine deploys instead of requiring a fleet-wide
	// reprovision maintenance window.
	resolvScript, err := os.ReadFile("../deploy/scripts/sandbox-resolv-conf.sh")
	require.NoError(t, err, "test should read the sandbox resolv.conf writer")
	require.Contains(t, string(resolvScript), "nameserver "+sandboxDNSIP, "sandbox-resolv-conf.sh should write sandbox-dns's IP into /etc/143/sandbox-resolv.conf")
	require.Contains(t, provisionText, "/opt/143/deploy/scripts/sandbox-resolv-conf.sh", "provision.sh should delegate to the shared writer instead of inlining the file content — keeps provision and deploy byte-aligned")
	deployScript, err := os.ReadFile("../deploy/scripts/deploy.sh")
	require.NoError(t, err, "test should read the deploy script")
	deployText := string(deployScript)
	require.Contains(t, deployText, "/opt/143/deploy/scripts/sandbox-resolv-conf.sh", "deploy.sh should refresh /etc/143/sandbox-resolv.conf on every worker deploy so a content change doesn't strand existing workers on stale DNS")
	require.Contains(t, deployText, "run_sandbox_resolv_conf", "deploy.sh should wrap sandbox-resolv-conf.sh in a retryable helper so legacy workers missing the new sudoers grant self-repair")
	require.Contains(t, deployText, "Retrying sandbox resolv.conf refresh after sudoers repair", "deploy.sh should retry sandbox-resolv-conf.sh after repairing sudoers so the first deploy that introduces the helper succeeds on legacy workers")
	require.Contains(t, deployText, "sudo -n /opt/143/deploy/scripts/sandbox-resolv-conf.sh", "deploy.sh should invoke sandbox-resolv-conf.sh with sudo -n so missing sudoers fails fast instead of hanging in CI")
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
	require.Contains(t, cloudInitText, "--subnet "+sandboxSubnet, "worker cloud-init should create 143-sandbox with the same pinned subnet as provision.sh so sandbox-dns can claim its static IP")
	require.Contains(t, cloudInitText, "/opt/143/deploy/scripts/sandbox-resolv-conf.sh", "worker cloud-init should write /etc/143/sandbox-resolv.conf through the shared writer before starting the worker")
	require.Contains(t, cloudInitText, "cp /tmp/143-repo/Dockerfile.dnsmasq /opt/143/", "worker cloud-init should stage Dockerfile.dnsmasq before docker compose starts so sandbox-dns can build on first boot")
	require.NotContains(t, cloudInitText, "enable_icc=false", "worker cloud-init must leave bridge ICC enabled so first-boot sandboxes can reach sandbox-dns")
	require.Contains(t, provisionText, `"$PROJECT_DIR/Dockerfile.dnsmasq" root@"$HOST":/opt/143/`, "provision.sh should stage Dockerfile.dnsmasq before docker compose starts so sandbox-dns can build on fresh worker provisioning")

	dockerfile, err := os.ReadFile("../Dockerfile.dnsmasq")
	require.NoError(t, err, "test should read the dnsmasq Dockerfile")
	dockerfileText := string(dockerfile)
	require.Contains(t, dockerfileText, "--server=127.0.0.11", "dnsmasq must forward to Docker's embedded resolver — that's the only place preview-infra container names are registered")
	require.Contains(t, dockerfileText, "--no-resolv", "dnsmasq must ignore its own /etc/resolv.conf (which itself points at 127.0.0.11) to avoid a forwarding loop")
}
