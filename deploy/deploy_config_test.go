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
