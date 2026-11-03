package deploy_test

import (
	"os"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestBinaryRollbackSchemaPhase(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile("scripts/deploy.sh")
	require.NoError(t, err, "read the deployed schema phase")
	source := string(raw)
	start := strings.Index(source, "  run_deploy_schema_phase() {")
	require.GreaterOrEqual(t, start, 0, "schema phase must exist")
	end := strings.Index(source[start:], "\n  # Run migrations BEFORE")
	require.Greater(t, end, 0, "schema phase must precede app replacement")
	function := source[start : start+end]
	tests := []struct {
		name, role, guard, fail string
		schema                  string
		expected                string
		wantError               bool
	}{
		{name: "forward app", role: "app", expected: "target|compose -f service.yml run --rm -T --no-deps api /bin/migrate repair-known-dirty\ntarget|compose -f service.yml run --rm -T --no-deps api /bin/migrate up\nreplace\n"},
		{name: "forward worker", role: "worker", expected: "replace\n"},
		{name: "rollback app", role: "app", guard: "new-guard", expected: "new-guard|compose -f service.yml run --rm -T --pull never --no-deps api /bin/migrate verify-inactive-rollback 392\nreplace\n"},
		{name: "rollback worker", role: "worker", guard: "new-guard", expected: "new-guard|compose -f service.yml run --rm -T --pull never --no-deps worker /bin/migrate verify-inactive-rollback 392\nreplace\n"},
		{name: "rollback app retaining credentials", role: "app", guard: "new-guard", schema: "393", expected: "new-guard|compose -f service.yml run --rm -T --pull never --no-deps api /bin/migrate verify-inactive-rollback 393\nreplace\n"},
		{name: "rollback worker retaining credentials", role: "worker", guard: "new-guard", schema: "393", expected: "new-guard|compose -f service.yml run --rm -T --pull never --no-deps worker /bin/migrate verify-inactive-rollback 393\nreplace\n"},
		{name: "393 guard failure blocks worker replacement", role: "worker", guard: "new-guard", schema: "393", fail: "1", wantError: true, expected: "new-guard|compose -f service.yml run --rm -T --pull never --no-deps worker /bin/migrate verify-inactive-rollback 393\n"},
		{name: "guard failure blocks replacement", role: "app", guard: "new-guard", fail: "1", wantError: true, expected: "new-guard|compose -f service.yml run --rm -T --pull never --no-deps api /bin/migrate verify-inactive-rollback 392\n"},
		{name: "forward migration failure blocks replacement", role: "app", fail: "1", wantError: true, expected: "target|compose -f service.yml run --rm -T --no-deps api /bin/migrate repair-known-dirty\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			service := tt.role
			if service == "app" {
				service = "api"
			}
			schema := tt.schema
			if schema == "" {
				schema = "392"
			}
			output, err := runWorkerShell(t, function, "\nROLE="+tt.role+"\nHEALTH_SERVICE="+service+"\nIMAGE_TAG=target\nCOMPOSE_FILE=service.yml\nBINARY_ROLLBACK_GUARD_TAG="+tt.guard+"\nBINARY_ROLLBACK_SCHEMA_VERSION="+schema+"\nFAIL="+tt.fail+`
docker() {
  # Deliberately consume stdin, as compose run does. The real phase must
  # detach stdin so a remote heredoc's remaining commands survive.
  cat >/dev/null
  printf '%s|%s\n' "$IMAGE_TAG" "$*"
  [ -z "$FAIL" ]
}
export -f docker
export ROLE HEALTH_SERVICE IMAGE_TAG COMPOSE_FILE BINARY_ROLLBACK_GUARD_TAG BINARY_ROLLBACK_SCHEMA_VERSION FAIL
`+"export -f run_deploy_schema_phase\nbash <<'REMOTE'\nset -euo pipefail\nrun_deploy_schema_phase\necho replace\nREMOTE\n")
			if tt.wantError {
				require.Error(t, err, "failed schema checks must stop replacement")
			} else {
				require.NoError(t, err, "successful checks should allow replacement")
			}
			require.Equal(t, tt.expected, output, "only the selected migration or read-only inspection commands may run")
		})
	}
}

func TestBinaryRollbackRequiresPinnedInactiveMode(t *testing.T) {
	t.Parallel()
	raw, err := os.ReadFile("scripts/deploy.sh")
	require.NoError(t, err, "read actual argument validation")
	source := string(raw)
	start := strings.Index(source, "validate_binary_rollback() {")
	require.GreaterOrEqual(t, start, 0, "validation must exist")
	end := strings.Index(source[start:], "\nvalidate_binary_rollback\n")
	require.Greater(t, end, 0, "validation must run before deployment")
	function := source[start : start+end]
	tests := []struct {
		name, extra string
		wantError   bool
	}{
		{name: "pinned routine rollback"},
		{name: "pinned schema393 rollback", extra: "BINARY_ROLLBACK_SCHEMA_VERSION=393"},
		{name: "mutable target", extra: "TAG=latest", wantError: true},
		{name: "mutable guard", extra: "BINARY_ROLLBACK_GUARD_TAG=latest", wantError: true},
		{name: "missing guard", extra: "BINARY_ROLLBACK_GUARD_TAG=", wantError: true},
		{name: "missing schema", extra: "BINARY_ROLLBACK_SCHEMA_VERSION=", wantError: true},
		{name: "future schema", extra: "BINARY_ROLLBACK_SCHEMA_VERSION=394", wantError: true},
		{name: "older schema", extra: "BINARY_ROLLBACK_SCHEMA_VERSION=391", wantError: true},
		{name: "schema suffix", extra: "BINARY_ROLLBACK_SCHEMA_VERSION=393x", wantError: true},
		{name: "same image", extra: "TAG=$BINARY_ROLLBACK_GUARD_TAG", wantError: true},
		{name: "stateful role", extra: "ROLE=db", wantError: true},
		{name: "maintenance", extra: "DEPLOY_MODE=maintenance", wantError: true},
		{name: "force", extra: "FORCE_INTERRUPT_ACTIVE_RUNTIMES=1", wantError: true},
		{name: "detached", extra: "WORKER_DEPLOY_DETACH=1", wantError: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			output, err := runWorkerShell(t, function, `
ROLE=app
TAG=1111111111111111111111111111111111111111
BINARY_ROLLBACK_GUARD_TAG=2222222222222222222222222222222222222222
BINARY_ROLLBACK_SCHEMA_VERSION=392
DEPLOY_MODE=routine
FORCE_DEPLOY_WITH_ACTIVE_SESSIONS=
FORCE_INTERRUPT_ACTIVE_RUNTIMES=
WORKER_DEPLOY_DETACH=
`+tt.extra+"\nvalidate_binary_rollback\n")
			if tt.wantError {
				require.Error(t, err, "unsafe rollback arguments must be refused: %s", output)
			} else {
				require.NoError(t, err, "pinned inactive rollback arguments should pass: %s", output)
			}
		})
	}
}
