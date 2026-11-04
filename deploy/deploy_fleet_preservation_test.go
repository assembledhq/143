package deploy_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestFleetWorkerCanaryWaitsForRetirementAndStopsOnFailure(t *testing.T) {
	t.Parallel()
	serialWorkers := []string{"worker-a:started", "worker-a:retired", "worker-b:started", "worker-b:retired", "worker-c:started", "worker-c:retired"}
	withApp := append([]string{"app:started", "app:complete"}, serialWorkers...)
	tests := []struct {
		name            string
		failedHost      string
		inheritedDetach string
		expected        []string
		wantError       bool
		rollback        bool
		parallelism     string
		roles           string
		extraEnv        []string
		invalidWidth    bool
	}{
		{name: "all workers complete despite inherited detach", inheritedDetach: "1", expected: withApp},
		{name: "canary failure holds every later worker", failedHost: "worker-a", inheritedDetach: "0", expected: []string{"app:started", "app:complete", "worker-a:started", "worker-a:failed"}, wantError: true},
		{name: "later failure holds remaining workers", failedHost: "worker-b", expected: []string{"app:started", "app:complete", "worker-a:started", "worker-a:retired", "worker-b:started", "worker-b:failed"}, wantError: true},
		{name: "binary rollback preserves serial retirement", rollback: true, parallelism: "2", expected: withApp},
		{name: "binary rollback app refusal holds workers", rollback: true, failedHost: "app-host", expected: []string{"app:started", "app:failed"}, wantError: true},
		{name: "worker-only deployment preserves serial retirement", parallelism: "2", roles: "worker", expected: serialWorkers},
		{name: "maintenance deployment preserves serial retirement", parallelism: "2", extraEnv: []string{"DEPLOY_MODE=maintenance"}, expected: withApp},
		{name: "maintenance roles preserve serial retirement", parallelism: "2", roles: "app,worker,redis", expected: append(append([]string(nil), withApp...), "redis:complete")},
		{name: "force deploy preserves serial retirement", parallelism: "2", extraEnv: []string{"FORCE_DEPLOY_WITH_ACTIVE_SESSIONS=1"}, expected: withApp},
		{name: "force interrupt preserves serial retirement", parallelism: "2", extraEnv: []string{"FORCE_INTERRUPT_ACTIVE_RUNTIMES=1"}, expected: withApp},
		{name: "invalid parallelism launches no hosts", parallelism: "3", invalidWidth: true, wantError: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			directory := t.TempDir()
			scripts := filepath.Join(directory, "deploy", "scripts")
			require.NoError(t, os.MkdirAll(scripts, 0o755), "fixture should isolate the actual fleet script")
			fleet, err := os.ReadFile("scripts/deploy-fleet.sh")
			require.NoError(t, err, "fixture should read the actual fleet implementation")
			path := filepath.Join(scripts, "deploy-fleet.sh")
			require.NoError(t, os.WriteFile(path, fleet, 0o755), "fixture should execute the actual fleet implementation")
			fake := `#!/usr/bin/env bash
set -euo pipefail
role="$1"; host="$2"; state="${FAKE_STATE:?}"
if [ "$role" = app ]; then
  printf 'app:started\n' >> "$state/trace"
  if [ "$host" = "${FAKE_FAILED_HOST:-}" ]; then
    printf 'app:failed\n' >> "$state/trace"
    exit 44
  fi
  printf 'app:complete\n' >> "$state/trace"
  exit 0
fi
if [ "$role" = redis ]; then
  printf 'redis:complete\n' >> "$state/trace"
  exit 0
fi
[ "$role" = worker ] || exit 41
[ -z "${WORKER_DEPLOY_DETACH:-}" ] || { echo "worker was detached" >&2; exit 42; }
mkdir "$state/active-worker" || { echo "another worker is still retiring" >&2; exit 43; }
trap 'rmdir "$state/active-worker"' EXIT
printf '%s:started\n' "$host" >> "$state/trace"
# A host result includes old-generation retirement, not just green startup.
sleep 0.05
if [ "$host" = "${FAKE_FAILED_HOST:-}" ]; then
  printf '%s:failed\n' "$host" >> "$state/trace"
  exit 44
fi
printf '%s:retired\n' "$host" >> "$state/trace"
`
			require.NoError(t, os.WriteFile(filepath.Join(scripts, "deploy.sh"), []byte(fake), 0o755), "fixture should replace host access with an isolated fake")
			state := filepath.Join(directory, "state")
			require.NoError(t, os.Mkdir(state, 0o755), "fixture should create isolated completion state")
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			roles := tt.roles
			if roles == "" {
				roles = "app,worker"
			}
			command := exec.CommandContext(ctx, "bash", path, "synthetic-key", "synthetic-tag", roles)
			command.Env = append(os.Environ(), "FLEET_HOSTS=app:app-host,worker:worker-a,worker:worker-b,worker:worker-c,redis:redis-host",
				"DEPLOY_JOBS=4", "WORKER_DEPLOY_JOBS="+tt.parallelism, "FAKE_STATE="+state,
				"FAKE_FAILED_HOST="+tt.failedHost, "WORKER_DEPLOY_DETACH="+tt.inheritedDetach,
				"DEPLOY_MODE=routine", "FORCE_DEPLOY_WITH_ACTIVE_SESSIONS=", "FORCE_INTERRUPT_ACTIVE_RUNTIMES=",
				"BINARY_ROLLBACK_GUARD_TAG=", "BINARY_ROLLBACK_SCHEMA_VERSION=", "DEPLOY_FLEET_LOG_DIR="+filepath.Join(directory, "logs"))
			command.Env = append(command.Env, tt.extraEnv...)
			if tt.rollback {
				command.Env = append(command.Env, "BINARY_ROLLBACK_GUARD_TAG=2222222222222222222222222222222222222222", "BINARY_ROLLBACK_SCHEMA_VERSION=392")
			}
			output, err := command.CombinedOutput()
			if tt.wantError {
				require.Error(t, err, "an incomplete host must hold the remainder of the fleet: %s", output)
				require.NotContains(t, string(output), "Fleet deployment complete.", "an incomplete rollout must not report fleet completion")
			} else {
				require.NoError(t, err, "each host should complete before the next worker starts: %s", output)
				require.Contains(t, string(output), "Worker canary completed", "fleet expansion must follow a completed canary")
				require.Contains(t, string(output), "Fleet deployment complete.", "only fully completed host rollovers may complete the fleet")
			}
			trace, readErr := os.ReadFile(filepath.Join(state, "trace"))
			if tt.invalidWidth {
				require.Contains(t, string(output), "WORKER_DEPLOY_JOBS must be 1 or 2", "invalid width should explain the supported values")
				require.ErrorIs(t, readErr, os.ErrNotExist, "invalid width should be rejected before host work")
				return
			}
			require.NoError(t, readErr, "fixture should retain the exact start and retirement ordering")
			require.Equal(t, tt.expected, strings.Split(strings.TrimSpace(string(trace)), "\n"), "worker rollout must stop at the first incomplete host and preserve canary ordering")
		})
	}
}

func TestFleetWorkerParallelRolloutJoinsRetirement(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name   string
		mode   string
		exit   int
		result string
		events []string
	}{
		{
			name:   "another slot advances while a worker drains",
			result: "Fleet deployment complete.",
			events: []string{"app-host:retired", "worker-a:started", "worker-a:retired",
				"worker-b:started", "worker-c:started", "worker-c:retired",
				"worker-d:started", "worker-d:retired", "worker-b:retired"},
		},
		{
			name:   "failed worker closes admission and joins peer",
			mode:   "failure",
			exit:   1,
			result: "FAILED: worker rollout incomplete; joined all started hosts.",
			events: []string{"app-host:retired", "worker-a:started", "worker-a:retired",
				"worker-b:started", "worker-c:started", "worker-b:failed", "worker-c:retired"},
		},
		{
			name:   "termination closes admission and joins peers",
			mode:   "signal",
			exit:   143,
			result: "Interrupted worker rollout; joined all started hosts.",
			events: []string{"app-host:retired", "worker-a:started", "worker-a:retired",
				"worker-b:started", "worker-c:started", "worker-b:retired", "worker-c:retired"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			directory := t.TempDir()
			scripts := filepath.Join(directory, "deploy", "scripts")
			require.NoError(t, os.MkdirAll(scripts, 0o755), "fixture should isolate fleet and host scripts")
			fleet, err := os.ReadFile("scripts/deploy-fleet.sh")
			require.NoError(t, err, "fixture should read the actual fleet script")
			path := filepath.Join(scripts, "deploy-fleet.sh")
			require.NoError(t, os.WriteFile(path, fleet, 0o755), "fixture should execute the actual fleet script")
			fake := `#!/usr/bin/env bash
set -euo pipefail
host="$2"
state="${FAKE_STATE:?}"
record() {
  printf '%s:%s\n' "$host" "$1" >> "$state/trace"
  printf 'host-event:%s:%s\n' "$host" "$1" >> "${FAKE_OUTPUT:?}"
  touch "$state/$host.$1"
}
await() {
  for ((i=0; i<400; i++)); do
    [ ! -f "$1" ] || return 0
    sleep 0.02
  done
  echo "Timed out waiting for $1" >&2
  return 1
}
if [ "$1" = app ]; then
  record retired
  exit 0
fi
[ -z "${WORKER_DEPLOY_DETACH:-}" ]
[ -f "$state/app-host.retired" ]
if [ "$host" != worker-a ]; then
  [ -f "$state/worker-a.retired" ]
fi
record started
if [ "${FAKE_MODE:-}" = signal ] && { [ "$host" = worker-b ] || [ "$host" = worker-c ]; }; then
  await "$state/release"
  record retired
  exit 0
fi
case "$host" in
  worker-b)
    await "$state/worker-c.started"
    if [ "${FAKE_MODE:-}" = failure ]; then
      record failed
      exit 44
    fi
    await "$state/worker-d.retired"
    ;;
  worker-c)
    await "$state/worker-b.started"
    if [ "${FAKE_MODE:-}" = failure ]; then
      await "$DEPLOY_FLEET_LOG_DIR/worker-worker-b.log.failed"
    fi
    ;;
  worker-d)
    [ -z "${FAKE_MODE:-}" ]
    [ -f "$state/worker-c.retired" ]
    [ ! -f "$state/worker-b.retired" ]
    ;;
esac
record retired
`
			require.NoError(t, os.WriteFile(filepath.Join(scripts, "deploy.sh"), []byte(fake), 0o755), "fixture should replace host access with an isolated fake")
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			outputPath := filepath.Join(directory, "output")
			outputFile, err := os.OpenFile(outputPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o600)
			require.NoError(t, err, "fixture should open a shared append-only log for fleet and host events")
			defer func() { require.NoError(t, outputFile.Close(), "fixture should close the shared log") }()
			command := exec.CommandContext(ctx, "bash", path, "synthetic-key", "synthetic-tag", "app,worker")
			command.Stdout, command.Stderr = outputFile, outputFile
			command.Env = append(os.Environ(),
				"FLEET_HOSTS=app:app-host,worker:worker-a,worker:worker-b,worker:worker-c,worker:worker-d",
				"WORKER_DEPLOY_JOBS=2", "WORKER_DEPLOY_DETACH=1", "FAKE_STATE="+directory,
				"DEPLOY_MODE=routine", "FORCE_DEPLOY_WITH_ACTIVE_SESSIONS=", "FORCE_INTERRUPT_ACTIVE_RUNTIMES=",
				"BINARY_ROLLBACK_GUARD_TAG=", "BINARY_ROLLBACK_SCHEMA_VERSION=",
				"FAKE_MODE="+tt.mode, "FAKE_OUTPUT="+outputPath, "DEPLOY_FLEET_LOG_DIR="+filepath.Join(directory, "logs"))
			if tt.mode == "signal" {
				require.NoError(t, command.Start(), "fixture should start the fleet parent")
				done := make(chan error, 1)
				go func() { done <- command.Wait() }()
				require.Eventually(t, func() bool {
					_, bErr := os.Stat(filepath.Join(directory, "worker-b.started"))
					_, cErr := os.Stat(filepath.Join(directory, "worker-c.started"))
					return bErr == nil && cErr == nil
				}, 5*time.Second, 20*time.Millisecond, "both post-canary workers must be active before termination")
				require.NoError(t, command.Process.Signal(syscall.SIGTERM), "fixture should signal only the fleet parent")
				select {
				case exitErr := <-done:
					t.Fatalf("fleet exited before in-flight rollouts were released: %v", exitErr)
				case <-time.After(250 * time.Millisecond):
				}
				require.NoError(t, os.WriteFile(filepath.Join(directory, "release"), nil, 0o600), "fixture should release both workers to complete retirement")
				err = <-done
			} else {
				err = command.Run()
			}
			output, readErr := os.ReadFile(outputPath)
			require.NoError(t, readErr, "fixture should capture fleet and host output in order")
			if tt.exit != 0 {
				var exitErr *exec.ExitError
				require.ErrorAs(t, err, &exitErr, "fleet must return failure after joining started workers: %s", output)
				require.Equal(t, tt.exit, exitErr.ExitCode(), "fleet must preserve the expected failure or interruption status")
				require.NotContains(t, string(output), "Fleet deployment complete.", "a host failure must preclude fleet completion")
			} else {
				require.NoError(t, err, "the second slot must keep advancing while the first retires: %s", output)
			}
			require.Contains(t, string(output), tt.result, "fleet should report the joined result")
			resultIndex := strings.Index(string(output), tt.result)
			for _, event := range tt.events {
				eventIndex := strings.Index(string(output), "host-event:"+event+"\n")
				require.GreaterOrEqual(t, eventIndex, 0, "host event %s must be observed", event)
				require.Greater(t, resultIndex, eventIndex, "fleet result must follow host event %s", event)
			}
			trace, err := os.ReadFile(filepath.Join(directory, "trace"))
			require.NoError(t, err, "fixture should capture starts and retirements")
			require.ElementsMatch(t, tt.events, strings.Split(strings.TrimSpace(string(trace)), "\n"), "the fleet should join started workers and stop admitting hosts after failure")
		})
	}
}
