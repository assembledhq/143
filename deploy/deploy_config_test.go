package deploy_test

import (
	"encoding/json"
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
	require.Contains(t, string(cloudInit), "GITHUB_APP_CLIENT_ID=${GITHUB_APP_CLIENT_ID}", "worker cloud-init should provide the GitHub App user auth client ID")
	require.Contains(t, string(cloudInit), "GITHUB_APP_CLIENT_SECRET=${GITHUB_APP_CLIENT_SECRET}", "worker cloud-init should provide the GitHub App user auth client secret")

	provisionScript, err := os.ReadFile("../deploy/scripts/provision.sh")
	require.NoError(t, err, "test should read the provisioning script")
	require.Contains(t, string(provisionScript), "GITHUB_APP_CLIENT_ID=%s", "worker reprovision path should write the GitHub App user auth client ID into .env")
	require.Contains(t, string(provisionScript), "GITHUB_APP_CLIENT_SECRET=%s", "worker reprovision path should write the GitHub App user auth client secret into .env")
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

	design, err := os.ReadFile("../docs/design/47-logging-victorialogs.md")
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

	bootstrap, err := os.ReadFile("../deploy/scripts/bootstrap.sh")
	require.NoError(t, err, "test should read bootstrap.sh")
	require.Contains(t, string(bootstrap), "/opt/143/deploy/scripts/install-log-rotation.sh *", "bootstrap.sh sudoers Cmnd_Alias must allow install-log-rotation.sh — without it the deploy+sudo path in deploy.sh fails on app/worker hosts")

	provision, err := os.ReadFile("../deploy/scripts/provision.sh")
	require.NoError(t, err, "test should read provision.sh")
	provisionText := string(provision)
	require.Contains(t, provisionText, "install-log-rotation.sh", "provision.sh should invoke install-log-rotation.sh after staging deploy/ so newly-provisioned hosts have rotation in place before services start (closes the provision-to-first-deploy unbounded-growth window)")
	// db/logging/redis bootstraps don't run bootstrap.sh, so each must
	// install its own /etc/sudoers.d/99-deploy or the deploy+sudo path
	// breaks on those roles.
	require.GreaterOrEqual(t, strings.Count(provisionText, "/opt/143/deploy/scripts/install-log-rotation.sh *"), 3, "provision.sh inline bootstraps for db, logging, and redis must each grant deploy NOPASSWD sudo for install-log-rotation.sh")
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
