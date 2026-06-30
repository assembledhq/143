package deploy_test

import (
	"os"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestDemoComposeIsOneHostSeededAPIOnly(t *testing.T) {
	t.Parallel()

	compose, err := os.ReadFile("../docker-compose.demo.yml")
	require.NoError(t, err, "test should read demo compose")
	text := string(compose)

	require.Contains(t, text, "postgres:", "demo compose should run local Postgres")
	require.Contains(t, text, "redis:", "demo compose should run local Redis")
	require.Contains(t, text, "migrate:", "demo compose should include migration job")
	require.Contains(t, text, "demo-seed:", "demo compose should include seed job")
	require.Contains(t, text, "MODE: api", "demo API should run in API-only mode")
	require.Contains(t, text, `DEMO_MODE: "true"`, "demo compose should enable demo mode")
	require.Contains(t, text, `DEMO_READ_ONLY: "true"`, "demo compose should enable read-only mode")
	require.Contains(t, text, "preview-viewer@143.dev", "demo compose should default to viewer direct entry")

	require.NotContains(t, text, "/var/run/docker.sock", "demo compose must not mount Docker socket")
	require.NotContains(t, text, ".env.production.enc", "demo compose must not use production encrypted env")
	require.NotContains(t, text, ".env.demo.enc", "demo compose must not use an encrypted demo env")
	require.NotContains(t, text, "GITHUB_APP_PRIVATE_KEY", "demo compose must not require GitHub App credentials")
	require.NotContains(t, text, "OPENAI_API_KEY", "demo compose must not require LLM keys")
	require.NotContains(t, text, "ANTHROPIC_API_KEY", "demo compose must not require LLM keys")
	require.NotContains(t, text, "CHROME_WS_URL", "demo compose must not run browser/preview infrastructure")
	require.NotContains(t, text, "SESSION_EXECUTOR_IMAGE", "demo compose must not run coding workers")
}

func TestDemoCaddyfileOmitsWildcardPreviewGateway(t *testing.T) {
	t.Parallel()

	caddyfile, err := os.ReadFile("Caddyfile.demo")
	require.NoError(t, err, "test should read demo Caddyfile")
	text := string(caddyfile)

	require.Contains(t, text, "{$DOMAIN:demo.143.dev}", "demo Caddyfile should serve the configured demo domain")
	require.Contains(t, text, "handle /api/*", "demo Caddyfile should route API requests")
	require.Contains(t, text, "@cli_dist path /install.sh /install/* /download/*", "demo Caddyfile should route CLI distribution")
	require.NotContains(t, text, "*.preview", "demo Caddyfile must not configure wildcard previews")
	require.NotContains(t, text, "CLOUDFLARE_API_TOKEN", "demo Caddyfile must not require DNS challenge credentials")
	require.NotContains(t, text, "port 9090", "demo Caddyfile must not route preview gateway traffic")
}

func TestServerImageIncludesDemoSeedTooling(t *testing.T) {
	t.Parallel()

	dockerfile, err := os.ReadFile("../Dockerfile")
	require.NoError(t, err, "test should read server Dockerfile")
	text := string(dockerfile)

	require.Contains(t, text, "-o /bin/demo-seed ./cmd/demo-seed", "server image should build demo-seed")
	require.Contains(t, text, "COPY --from=go-builder /bin/demo-seed /bin/demo-seed", "server image should ship demo-seed")
	require.Contains(t, text, "COPY --from=go-builder /app/.143/seed /demo-seed", "server image should ship canonical seed fragments")
}

func TestDemoProvisionInstallsDeploySSHAccess(t *testing.T) {
	t.Parallel()

	script, err := os.ReadFile("scripts/provision-demo.sh")
	require.NoError(t, err, "test should read demo provision script")
	text := string(script)

	require.Contains(t, text, "mkdir -p /home/deploy/.ssh", "demo provision should create deploy ssh directory")
	require.Contains(t, text, "cp /root/.ssh/authorized_keys /home/deploy/.ssh/authorized_keys", "demo provision should copy root SSH access to deploy user")
	require.Contains(t, text, "chown -R deploy:deploy /home/deploy/.ssh", "demo provision should give deploy ownership of ssh directory")
	require.Contains(t, text, "chmod 700 /home/deploy/.ssh", "demo provision should restrict deploy ssh directory")
	require.Contains(t, text, "chmod 600 /home/deploy/.ssh/authorized_keys", "demo provision should restrict deploy authorized_keys")
}

func TestDemoEnvProvidesProductionRequiredSecrets(t *testing.T) {
	t.Parallel()

	envExample, err := os.ReadFile("../.env.demo.example")
	require.NoError(t, err, "test should read demo env example")
	envText := string(envExample)
	require.Contains(t, envText, "ENCRYPTION_MASTER_KEY=replace-with-generated-encryption-key", "demo env example should document production-required encryption key")
	require.Contains(t, envText, "GITHUB_WEBHOOK_SECRET=replace-with-generated-webhook-secret", "demo env example should document production-required webhook secret")

	script, err := os.ReadFile("scripts/provision-demo.sh")
	require.NoError(t, err, "test should read demo provision script")
	scriptText := string(script)
	require.Contains(t, scriptText, `encryption_master_key="$(openssl rand -hex 32)"`, "demo provision should generate a local encryption key")
	require.Contains(t, scriptText, `github_webhook_secret="$(openssl rand -hex 32)"`, "demo provision should generate a local webhook secret")
	require.Contains(t, scriptText, "ENCRYPTION_MASTER_KEY=$encryption_master_key", "demo provision should write encryption key to .env.demo")
	require.Contains(t, scriptText, "GITHUB_WEBHOOK_SECRET=$github_webhook_secret", "demo provision should write webhook secret to .env.demo")
}

func TestDeployWorkflowBuildsFrontendWithPublicDemoURL(t *testing.T) {
	t.Parallel()

	workflow, err := os.ReadFile("../.github/workflows/deploy.yml")
	require.NoError(t, err, "test should read deploy workflow")
	text := string(workflow)

	require.Contains(t, text, "NEXT_PUBLIC_DEMO_URL=https://demo.143.dev", "production frontend image should bake the public demo CTA URL at Next.js build time")
}
