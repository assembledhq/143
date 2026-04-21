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
