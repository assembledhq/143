package deploy_test

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

// These fixtures never resolve or decrypt a developer's real private bundle.
func writeFleetAdmissionFixtures(t *testing.T, scripts string) {
	t.Helper()
	resolver := "#!/usr/bin/env bash\nprintf '%s\\n' " + shellQuote(filepath.Join(scripts, "missing-secrets")) + "\n"
	require.NoError(t, os.WriteFile(filepath.Join(scripts, "resolve-secrets-dir.sh"), []byte(resolver), 0755), "fixture should resolve only synthetic secrets")
	require.NoError(t, os.WriteFile(filepath.Join(scripts, "deploy-worker-admission.sh"), []byte("#!/usr/bin/env bash\nexit 0\n"), 0755), "fixture should avoid real admission host access")
}

func admissionFixture(t *testing.T, root string) string {
	t.Helper()
	return `
flock(){ :; }
sync(){ :; }
timeout(){ shift 2; "$@"; }
date(){ echo 100; }
fixture_root=` + shellQuote(root) + `
docker(){ [ "$1" = info ] || { echo unexpected-docker-mutation; return 1; }; echo "$fixture_root"; }
df(){
 if [ "$1" = -Pk ]; then printf 'Filesystem 1024-blocks Used Available Capacity Mounted on\n/dev/test 50000000 1 %s 1%% /data\n' "${free_kib:-1}";
 else printf 'Filesystem Inodes IUsed IFree IUse%% Mounted on\n/dev/test 50000000 1 200000 1%% /data\n'; fi
}
`
}

func TestInitialWorkerAdmissionRejectionIsRetryable(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	functions := extractTopLevelShellFunction(t, workerRolloutScript(t), "worker_rollout_guard", "finish_worker_rollout_guard")
	output, err := runWorkerShell(t, functions, admissionFixture(t, root)+`
worker_rollout_guard attempt-a build-a `+shellQuote(root)+` `+shellQuote(root)+` `+shellQuote(filepath.Join(root, "legacy.lock")))
	require.Error(t, err, "initial insufficient capacity should reject deployment")
	require.NotContains(t, output, "ready:", "rejected admission must never authorize host mutation")
	data, err := os.ReadFile(filepath.Join(root, ".worker-rollout-rejections", "attempt-a", "capacity.json"))
	require.NoError(t, err, "rejection history must preserve exact measurements before unblocking")
	var actual map[string]any
	require.NoError(t, json.Unmarshal(data, &actual), "rejection measurements should be structured")
	require.Equal(t, map[string]any{"attempt": "attempt-a", "build_sha": "build-a", "stage": "pre-pull", "reason": "insufficient_disk", "started_at": float64(100), "finished_at": float64(100), "available_kib": float64(1), "available_inodes": float64(200000), "required_kib": float64(18874368), "required_inodes": float64(200000)}, actual, "rejection receipt must explain which exact gate failed")
	output, err = runWorkerShell(t, functions, admissionFixture(t, root)+`
free_kib=18874368
worker_rollout_guard attempt-b build-a `+shellQuote(root)+` `+shellQuote(root)+` `+shellQuote(filepath.Join(root, "legacy.lock"))+` < /dev/null`)
	require.Error(t, err, "the new attempt intentionally loses its launcher after admission")
	require.Contains(t, output, "ready:attempt-b", "fresh capacity should admit a new attempt without clearing old history")
	attempt, err := os.ReadFile(filepath.Join(root, ".worker-rollout", "attempt"))
	require.NoError(t, err, "post-ready launcher loss must still retain a blocking latch")
	require.Equal(t, "attempt-b\n", string(attempt), "only the new unknown attempt should remain blocking")
}

func TestInitialWorkerAdmissionPersistenceFailuresRemainBlocking(t *testing.T) {
	t.Parallel()
	tests := []struct{ name, failure string }{
		{name: "failure receipt cannot be written", failure: `mktemp(){ return 1; }`},
		{name: "failure receipt cannot be synchronized", failure: `sync(){ case "$2" in */.worker-rollout) return 1;; esac; }`},
		{name: "rejection archive cannot be copied", failure: `cp(){ return 1; }`},
		{name: "rejection archive cannot be synchronized", failure: `sync(){ case "$2" in */.worker-rollout-rejections/*) return 1;; esac; }`},
		{name: "archive namespace is occupied", failure: `mkdir -p "$fixture_root/.worker-rollout-rejections/attempt-a"`},
		{name: "unexpected intent file", failure: `cp(){ command cp "$@"; touch "$fixture_root/.worker-rollout/unexpected"; }`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			functions := extractTopLevelShellFunction(t, workerRolloutScript(t), "worker_rollout_guard", "finish_worker_rollout_guard")
			output, err := runWorkerShell(t, functions, admissionFixture(t, root)+tt.failure+`
worker_rollout_guard attempt-a build-a `+shellQuote(root)+` `+shellQuote(root)+` `+shellQuote(filepath.Join(root, "legacy.lock")))
			require.Error(t, err, "persistence uncertainty must reject admission")
			require.NotContains(t, output, "ready:", "persistence failure must never authorize deployment")
			info, err := os.Stat(filepath.Join(root, ".worker-rollout"))
			require.NoError(t, err, "failed rejection persistence must preserve a blocking directory")
			require.True(t, info.IsDir(), "unresolved rejection must remain an intent directory")
		})
	}
}

func TestWorkerAdmissionObservationPreservesHostState(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name                 string
		prior, lockBusy, low bool
		reason               string
	}{
		{name: "sufficient capacity"},
		{name: "retained marker", prior: true, reason: "unresolved worker rollout"},
		{name: "active rollout lock", lockBusy: true, reason: "worker rollout is active"},
		{name: "low capacity", low: true, reason: "insufficient_disk"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			if tt.prior {
				require.NoError(t, os.Mkdir(filepath.Join(root, ".worker-rollout"), 0700), "fixture should preserve an earlier marker")
			}
			if tt.lockBusy {
				require.NoError(t, os.WriteFile(filepath.Join(root, ".worker-rollout.lock"), nil, 0600), "fixture should expose a held lock")
			}
			before, err := os.ReadDir(root)
			require.NoError(t, err, "should record initial host state")
			source, err := os.ReadFile("scripts/deploy-worker-admission.sh")
			require.NoError(t, err, "should load actual admission helper")
			start := strings.Index(string(source), "worker_host_admission() {")
			end := strings.Index(string(source), "\nif [ \"${BASH_SOURCE[0]}\"")
			require.Greater(t, end, start, "fixture should isolate the host function from the SSH entry point")
			function := string(source)[start:end]
			body := admissionFixture(t, root) + "free_kib=18874368\n"
			if tt.low {
				body += "free_kib=1\n"
			}
			if tt.lockBusy {
				body += "flock(){ return 1; }\n"
			}
			output, err := runWorkerShell(t, function, body+"worker_host_admission "+shellQuote(root)+" "+shellQuote(root)+" "+shellQuote(filepath.Join(root, "legacy.lock")))
			if tt.reason != "" {
				require.Error(t, err, "blocked admission must fail")
				require.Contains(t, output, tt.reason, "private diagnostics should identify the blocker")
			} else {
				require.NoError(t, err, "sufficient unlocked capacity should pass: %s", output)
			}
			after, err := os.ReadDir(root)
			require.NoError(t, err, "should inspect state after observational admission")
			require.Equal(t, before, after, "admission must not create or remove host markers or lock files")
			require.NotContains(t, output, "unexpected-docker-mutation", "admission must never start or pull a container")
		})
	}
}

func TestFleetAdmissionPrecedesAppMutation(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, failed string
		expected     []string
	}{
		{name: "all workers checked before app", expected: []string{"check:worker-a", "check:worker-b", "deploy:app:app-host", "deploy:worker:worker-a", "deploy:worker:worker-b"}},
		{name: "later worker refusal holds app", failed: "worker-b", expected: []string{"check:worker-a", "check:worker-b"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			scripts := filepath.Join(root, "deploy", "scripts")
			require.NoError(t, os.MkdirAll(scripts, 0755), "fixture should isolate fleet execution")
			fleet, err := os.ReadFile("scripts/deploy-fleet.sh")
			require.NoError(t, err, "fixture should read actual fleet control flow")
			require.NoError(t, os.WriteFile(filepath.Join(scripts, "deploy-fleet.sh"), fleet, 0755), "fixture should run actual fleet script")
			writeFleetAdmissionFixtures(t, scripts)
			require.NoError(t, os.WriteFile(filepath.Join(scripts, "deploy-worker-admission.sh"), []byte(`#!/usr/bin/env bash
set -euo pipefail
printf 'check:%s\n' "$1" >>"$TRACE"
[ "$1" != "$FAILED" ]
`), 0755), "fixture should make host admission observable")
			require.NoError(t, os.WriteFile(filepath.Join(scripts, "deploy.sh"), []byte(`#!/usr/bin/env bash
set -euo pipefail
printf 'deploy:%s:%s\n' "$1" "$2" >>"$TRACE"
`), 0755), "fixture should observe actual mutation ordering without host access")
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			trace := filepath.Join(root, "trace")
			cmd := exec.CommandContext(ctx, "bash", filepath.Join(scripts, "deploy-fleet.sh"), "synthetic-key", "synthetic-tag")
			cmd.Env = append(os.Environ(), "TRACE="+trace, "FAILED="+tt.failed, "FLEET_HOSTS=app:app-host,worker:worker-a,worker:worker-b", "DEPLOY_MODE=routine", "WORKER_DEPLOY_JOBS=1", "DEPLOY_FLEET_LOG_DIR="+filepath.Join(root, "logs"), "BINARY_ROLLBACK_GUARD_TAG=", "BINARY_ROLLBACK_SCHEMA_VERSION=")
			output, err := cmd.CombinedOutput()
			if tt.failed != "" {
				require.Error(t, err, "any worker blocker must reject the entire admission phase")
				require.Contains(t, string(output), "no fleet deployment was started", "failure should explain the untouched app boundary")
			} else {
				require.NoError(t, err, "all admitted hosts should reach normal rollout: %s", output)
			}
			data, err := os.ReadFile(trace)
			require.NoError(t, err, "fixture should retain the actual operation order")
			require.Equal(t, tt.expected, strings.Split(strings.TrimSpace(string(data)), "\n"), "all worker observations must precede the first app mutation")
		})
	}
}

func TestFleetAdmissionUsesResolvedWorkerCapacitySettings(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, bundle, inventory   string
		overrides                 []string
		expected                  string
		decryptFails, skipDecrypt bool
	}{
		{name: "stricter bundle with supplied inventory", inventory: "app:app-host,worker:worker-a", bundle: "WORKER_BLUE_GREEN_MIN_FREE_DISK_MB=32768\nWORKER_BLUE_GREEN_MIN_FREE_INODES=300000\nWORKER_BLUE_GREEN_PULL_DISK_BUDGET_MB=24576\nWORKER_BLUE_GREEN_PULL_INODE_BUDGET=2000000\n", expected: "32768:300000:24576:2000000"},
		{name: "looser bundle with resolved inventory", bundle: "FLEET_HOSTS=app:app-host,worker:worker-a\nWORKER_BLUE_GREEN_MIN_FREE_DISK_MB=1\nWORKER_BLUE_GREEN_MIN_FREE_INODES=3\nWORKER_BLUE_GREEN_PULL_DISK_BUDGET_MB=2\nWORKER_BLUE_GREEN_PULL_INODE_BUDGET=4\n", expected: "1:3:2:4"},
		{name: "nonempty caller overrides bundle", inventory: "app:app-host,worker:worker-a", bundle: "WORKER_BLUE_GREEN_MIN_FREE_DISK_MB=1\nWORKER_BLUE_GREEN_MIN_FREE_INODES=3\nWORKER_BLUE_GREEN_PULL_DISK_BUDGET_MB=2\nWORKER_BLUE_GREEN_PULL_INODE_BUDGET=4\n", overrides: []string{"WORKER_BLUE_GREEN_MIN_FREE_DISK_MB=9", "WORKER_BLUE_GREEN_PULL_INODE_BUDGET=11"}, expected: "9:3:2:11"},
		{name: "complete caller settings need no decrypt", inventory: "app:app-host,worker:worker-a", overrides: []string{"WORKER_BLUE_GREEN_MIN_FREE_DISK_MB=8", "WORKER_BLUE_GREEN_MIN_FREE_INODES=7", "WORKER_BLUE_GREEN_PULL_DISK_BUDGET_MB=6", "WORKER_BLUE_GREEN_PULL_INODE_BUDGET=5"}, expected: "8:7:6:5", skipDecrypt: true},
		{name: "decrypt failure with partial settings holds fleet", inventory: "app:app-host,worker:worker-a", bundle: "WORKER_BLUE_GREEN_MIN_FREE_DISK_MB=1\n", decryptFails: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			scripts := filepath.Join(root, "deploy", "scripts")
			bin := filepath.Join(root, "bin")
			secrets := filepath.Join(root, "synthetic-secrets")
			for _, dir := range []string{scripts, bin, secrets} {
				require.NoError(t, os.MkdirAll(dir, 0755), "fixture should isolate scripts and synthetic bundle")
			}
			fleet, err := os.ReadFile("scripts/deploy-fleet.sh")
			require.NoError(t, err, "fixture should read actual fleet configuration resolution")
			require.NoError(t, os.WriteFile(filepath.Join(scripts, "deploy-fleet.sh"), fleet, 0755), "fixture should run actual fleet logic")
			require.NoError(t, os.WriteFile(filepath.Join(scripts, "resolve-secrets-dir.sh"), []byte("#!/usr/bin/env bash\nprintf '%s\\n' "+shellQuote(secrets)+"\n"), 0755), "fixture should never resolve real production secrets")
			require.NoError(t, os.WriteFile(filepath.Join(secrets, ".env.production.enc"), []byte("synthetic encrypted fixture"), 0600), "fixture should make only a synthetic bundle available")
			require.NoError(t, os.WriteFile(filepath.Join(bin, "sops"), []byte(`#!/usr/bin/env bash
set -euo pipefail
printf 'decrypt\n' >>"$TRACE"
printf '%s' "$BUNDLE"
printf 'UNRELATED_SECRET=synthetic-private-sentinel\n'
if [ "$DECRYPT_FAILS" = 1 ]; then echo synthetic-private-sentinel >&2; exit 2; fi
`), 0755), "fixture decryption should expose only synthetic data")
			record := `#!/usr/bin/env bash
set -euo pipefail
[ -z "${UNRELATED_SECRET:-}" ] || { echo unrelated-secret-was-exported; exit 3; }
printf '%s:%s:%s:%s:%s\n' "${MODE:?}" "${WORKER_BLUE_GREEN_MIN_FREE_DISK_MB:-}" "${WORKER_BLUE_GREEN_MIN_FREE_INODES:-}" "${WORKER_BLUE_GREEN_PULL_DISK_BUDGET_MB:-}" "${WORKER_BLUE_GREEN_PULL_INODE_BUDGET:-}" >>"$TRACE"
`
			require.NoError(t, os.WriteFile(filepath.Join(scripts, "deploy-worker-admission.sh"), []byte("MODE=admission\n"+record), 0755), "fixture should record advisory settings")
			require.NoError(t, os.WriteFile(filepath.Join(scripts, "deploy.sh"), []byte("#!/usr/bin/env bash\nMODE=deploy\n"+record), 0755), "fixture should record the exact settings inherited by locked deployment")
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			trace := filepath.Join(root, "trace")
			cmd := exec.CommandContext(ctx, "bash", filepath.Join(scripts, "deploy-fleet.sh"), "synthetic-key", "synthetic-tag")
			failure := "0"
			if tt.decryptFails {
				failure = "1"
			}
			cmd.Env = append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"), "TRACE="+trace, "BUNDLE="+tt.bundle, "DECRYPT_FAILS="+failure, "FLEET_HOSTS="+tt.inventory, "DEPLOY_MODE=routine", "WORKER_DEPLOY_JOBS=1", "DEPLOY_FLEET_LOG_DIR="+filepath.Join(root, "logs"), "BINARY_ROLLBACK_GUARD_TAG=", "BINARY_ROLLBACK_SCHEMA_VERSION=", "WORKER_BLUE_GREEN_MIN_FREE_DISK_MB=", "WORKER_BLUE_GREEN_MIN_FREE_INODES=", "WORKER_BLUE_GREEN_PULL_DISK_BUDGET_MB=", "WORKER_BLUE_GREEN_PULL_INODE_BUDGET=", "UNRELATED_SECRET=")
			cmd.Env = append(cmd.Env, tt.overrides...)
			output, err := cmd.CombinedOutput()
			expected := []string{"decrypt"}
			if tt.decryptFails {
				require.Error(t, err, "configuration uncertainty must stop before any host work")
				require.Contains(t, string(output), "configuration unavailable", "decryption failure should be explained without private detail")
			} else {
				require.NoError(t, err, "resolved settings should flow to admission and deployment: %s", output)
				if tt.skipDecrypt {
					expected = nil
				}
				expected = append(expected, "admission:"+tt.expected, "deploy:"+tt.expected, "deploy:"+tt.expected)
			}
			data, err := os.ReadFile(trace)
			require.NoError(t, err, "fixture should record configuration resolution and host boundaries")
			require.Equal(t, expected, strings.Split(strings.TrimSpace(string(data)), "\n"), "fleet should decrypt at most once and propagate identical caller-precedence settings")
			require.NotContains(t, string(output), "synthetic-private-sentinel", "bundle values and decryption diagnostics must not leak to public output")
		})
	}
}
