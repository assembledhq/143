package deploy_test

import (
	"os"
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
