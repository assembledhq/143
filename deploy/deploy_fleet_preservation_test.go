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

func TestFleetWorkerCanaryWaitsForRetirementAndStopsOnFailure(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name            string
		failedHost      string
		inheritedDetach string
		expected        []string
		wantError       bool
	}{
		{name: "all workers complete despite inherited detach", inheritedDetach: "1", expected: []string{"app:started", "app:complete", "worker-a:started", "worker-a:retired", "worker-b:started", "worker-b:retired", "worker-c:started", "worker-c:retired"}},
		{name: "canary failure holds every later worker", failedHost: "worker-a", inheritedDetach: "0", expected: []string{"app:started", "app:complete", "worker-a:started", "worker-a:failed"}, wantError: true},
		{name: "later failure holds remaining workers", failedHost: "worker-b", expected: []string{"app:started", "app:complete", "worker-a:started", "worker-a:retired", "worker-b:started", "worker-b:failed"}, wantError: true},
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
  printf 'app:started\napp:complete\n' >> "$state/trace"
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
			command := exec.CommandContext(ctx, "bash", path, "synthetic-key", "synthetic-tag", "app,worker")
			command.Env = append(os.Environ(), "FLEET_HOSTS=app:app-host,worker:worker-a,worker:worker-b,worker:worker-c", "DEPLOY_JOBS=4", "FAKE_STATE="+state, "FAKE_FAILED_HOST="+tt.failedHost, "WORKER_DEPLOY_DETACH="+tt.inheritedDetach)
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
			require.NoError(t, readErr, "fixture should retain the exact start and retirement ordering")
			require.Equal(t, tt.expected, strings.Split(strings.TrimSpace(string(trace)), "\n"), "worker rollout must stop at the first incomplete host and preserve canary ordering")
		})
	}
}
