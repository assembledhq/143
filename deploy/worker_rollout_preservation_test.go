package deploy_test

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func workerRolloutScript(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile("scripts/deploy.sh")
	require.NoError(t, err, "should read the actual rollout implementation")
	return string(data)
}

func runWorkerShell(t *testing.T, source, body string) (string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "bash", "-c", "set -euo pipefail\n"+source+"\n"+body)
	cmd.Dir = t.TempDir()
	output, err := cmd.CombinedOutput()
	require.NoError(t, ctx.Err(), "the preservation scenario should finish within its bound")
	return string(output), err
}

func TestWorkerCandidateVerificationPreservesAllContainers(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name      string
		failure   string
		markFails bool
	}{
		{name: "health failure", failure: "health"},
		{name: "database readiness failure", failure: "heartbeat"},
		{name: "auth verification failure", failure: "auth"},
		{name: "unknown compose outcome", failure: "start"},
		{name: "failed mark is still protected", failure: "health", markFails: true},
		{name: "candidate fails after retirement", failure: "postflight"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			script := workerRolloutScript(t)
			functions := extractShellFunction(t, script, "preserve_failed_worker_candidate", "worker_container_state") +
				extractShellFunction(t, script, "deploy_worker_blue_green", "dump_diagnostics")
			markFailure := "0"
			if tt.markFails {
				markFailure = "1"
			}
			output, err := runWorkerShell(t, functions, `
IMAGE_TAG=0123456789012345678901234567890123456789
WORKER_ROLLOUT_ATTEMPT=worker-attempt-a
COMPOSE_FILE=worker.yml
HEALTH_SERVICE=worker
failure=`+tt.failure+`
mark_failure=`+markFailure+`
trace="$PWD/trace"
record_worker_rollout_state() { printf 'record %s\n' "$*" >>"$trace"; }
list_running_worker_containers() { echo old-container; }
first_running_worker_node_id() { echo old-node; }
read_worker_env_value() { case "$1" in NODE_ID) echo base;; WORKER_PRIVATE_IP) echo 192.0.2.1;; esac; }
ensure_routine_worker_fingerprints_compatible() { :; }
load_worker_endpoint_check_env() { :; }
worker_host_capacity_preflight() { :; }
find_free_worker_port() { echo 8081; }
sanitize_compose_project() { echo project; }
worker_expected_schema_version() { echo 312; }
protect_active_executor_images() { :; }
run_worker_deployctl() {
  printf 'ctl %s\n' "$*" >>"$trace"
  case "$1" in mark-draining) return "$mark_failure";; preview-auth-check) [ "$failure" != auth ];; esac
}
docker() {
  printf 'docker %s\n' "$*" >>"$trace"
  case "$*" in *" up -d "*) [ "$failure" != start ];; *" ps -q "*) echo new-container;; esac
}
wait_container_healthy() {
  printf 'health %s\n' "$*" >>"$trace"
  if [ "$failure" = postflight ]; then
    if [ -e second-health ]; then return 1; fi
    touch second-health
  fi
  [ "$failure" != health ]
}
wait_worker_db_heartbeat() { printf 'heartbeat %s\n' "$*" >>"$trace"; [ "$failure" != heartbeat ]; }
drain_old_worker_containers() { echo old-retirement-complete >>"$trace"; }
trap 'cat "$trace"' EXIT
deploy_worker_blue_green
`)
			require.Error(t, err, "failed candidate verification must fail the deployment")
			require.Contains(t, output, "ctl mark-draining --node-id base-gattempt-a-012345678901 --intent planned_rollout --deploy-id worker-attempt-a", "failure should close admission for only the exact candidate")
			require.NotContains(t, output, "docker stop", "verification failure must never stop a possibly active candidate")
			require.NotContains(t, output, "docker rm", "verification failure must preserve the candidate container")
			if tt.failure != "postflight" {
				require.NotContains(t, output, "old-retirement-complete", "failed initial candidate verification must leave old owners untouched")
			}
			if tt.markFails {
				require.Contains(t, output, "drain outcome unproven", "a failed admission mark must retain the candidate and halt")
			}
		})
	}
}

func TestWorkerRetirementRequiresExactDurableSealAndStoppedReadback(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name        string
		mode        string
		wantSuccess bool
		wantStop    bool
	}{
		{name: "busy owner then sealed", mode: "busy", wantSuccess: true, wantStop: true},
		{name: "unknown seal", mode: "unknown"},
		{name: "wrong generation seal", mode: "wrong-node"},
		{name: "wrong deployment seal", mode: "wrong-attempt"},
		{name: "wrong source seal", mode: "wrong-build"},
		{name: "busy until deadline", mode: "timeout"},
		{name: "stop outcome unknown", mode: "stop-error", wantStop: true},
		{name: "still running after stop", mode: "running-after-stop", wantStop: true},
		{name: "readback failure", mode: "readback-error", wantStop: true},
		{name: "missing initial inspection", mode: "inspect-error"},
		{name: "failed planned drain", mode: "mark-error"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			script := workerRolloutScript(t)
			functions := extractShellFunction(t, script, "worker_container_state", "deploy_worker_blue_green")
			output, err := runWorkerShell(t, functions, `
IMAGE_TAG=0123456789012345678901234567890123456789
mode=`+tt.mode+`
trace="$PWD/trace"
resolve_worker_drain_timeout_seconds() { echo 20; }
worker_container_node_id() { echo old-node; }
sleep() { SECONDS=$((SECONDS + 5)); }
timeout() { shift 2; "$@"; }
docker() {
  printf 'docker %s\n' "$*" >>"$trace"
  if [ "$1" = inspect ]; then
    [ "$mode" != inspect-error ] || return 1
    if [ -e stop-issued ]; then
      [ "$mode" != readback-error ] || return 1
      if [ "$mode" != running-after-stop ]; then echo 'false 0'; return; fi
    fi
    echo 'true 42'
  elif [ "$1" = stop ]; then
    touch stop-issued
    [ "$mode" != stop-error ]
  else
    return 1
  fi
}
run_worker_deployctl() {
  printf 'ctl %s\n' "$*" >>"$trace"
  if [ "$1" = mark-draining ]; then [ "$mode" != mark-error ]; return; fi
  [ "$1" = seal-retirement ] || return 1
  [ "$mode" != unknown ] || return 1
  [ "$mode" != timeout ] || return 3
  if [ "$mode" = busy ] && [ ! -e was-busy ]; then touch was-busy; return 3; fi
  node=old-node; attempt=attempt-a; build="$IMAGE_TAG"
  case "$mode" in wrong-node) node=another-node;; wrong-attempt) attempt=another-attempt;; wrong-build) build=another-build;; esac
  printf '{"sealed":true,"node_id":"%s","deploy_id":"%s","build_sha":"%s","sealed_at":"2026-01-01T00:00:00Z"}\n' "$node" "$attempt" "$build"
}
trap 'cat "$trace"' EXIT
drain_old_worker_containers new-container old-container attempt-a
`)
			if tt.wantSuccess {
				require.NoError(t, err, "busy work should finish before a sealed retirement can complete")
				require.Contains(t, output, "All exact old worker containers are positively stopped", "completion should require final whole-roster stopped readback")
			} else {
				require.Error(t, err, "unknown, busy, mismatched or failed retirement must fail closed")
				require.NotContains(t, output, "All exact old worker containers are positively stopped", "failed retirement must not claim completion")
			}
			if tt.wantStop {
				require.Contains(t, output, "docker stop --time -1 old-container", "only an exact sealed worker may receive an unbounded graceful stop")
			} else {
				require.NotContains(t, output, "docker stop", "an unproven retirement must never signal the worker")
			}
			require.NotContains(t, output, "expire-budget", "routine retirement must preserve current review clocks")
			require.NotContains(t, output, "docker rm", "routine retirement should retain stopped evidence")
		})
	}
}

func TestWorkerRolloutGuardRetainsUnknownAttemptsAndRejectsStaleCompletion(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, input, completion        string
		prior, legacyBusy, wantSuccess bool
	}{
		{name: "launcher EOF"},
		{name: "explicit abort", input: "abort attempt-a\n"},
		{name: "missing completion", input: "complete attempt-a\n"},
		{name: "wrong attempt completion", input: "complete attempt-old\n", completion: `{"attempt":"attempt-a","build_sha":"build-a","candidate_verified":true,"old_containers_stopped":true,"candidate_container":"candidate","candidate_node":"candidate-node"}`},
		{name: "wrong source receipt", input: "complete attempt-a\n", completion: `{"attempt":"attempt-a","build_sha":"stale-build","candidate_verified":true,"old_containers_stopped":true,"candidate_container":"candidate","candidate_node":"candidate-node"}`},
		{name: "old generation remains", input: "complete attempt-a\n", completion: `{"attempt":"attempt-a","build_sha":"build-a","candidate_verified":true,"old_containers_stopped":false,"candidate_container":"candidate","candidate_node":"candidate-node"}`},
		{name: "same source prior intent", prior: true},
		{name: "active legacy monitor", legacyBusy: true},
		{name: "joined exact completion", input: "complete attempt-a\n", completion: `{"attempt":"attempt-a","build_sha":"build-a","candidate_verified":true,"old_containers_stopped":true,"candidate_container":"candidate","candidate_node":"candidate-node"}`, wantSuccess: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			root := t.TempDir()
			logRoot := t.TempDir()
			if tt.prior {
				require.NoError(t, os.Mkdir(filepath.Join(root, ".worker-rollout"), 0700), "should create an existing unresolved attempt")
				require.NoError(t, os.WriteFile(filepath.Join(root, ".worker-rollout", "attempt"), []byte("prior-attempt\n"), 0600), "should retain the prior attempt identity")
			}
			if tt.legacyBusy {
				require.NoError(t, os.WriteFile(filepath.Join(logRoot, "drain-worker-old.lock"), nil, 0600), "should create the active legacy monitor lock fixture")
			}
			script := workerRolloutScript(t)
			functions := extractTopLevelShellFunction(t, script, "worker_rollout_guard", "finish_worker_rollout_guard")
			// Mock only flock portability; the actual guard, namespace and receipt
			// checks execute unchanged. Linux concurrency is covered separately.
			mockFlock := "flock() { return 0; }\nsync() { return 0; }\n"
			if tt.legacyBusy {
				mockFlock = `flock() { case "$*" in *drain-worker*) return 1;; *) return 0;; esac; }; sync() { return 0; }` + "\n"
			}
			body := mockFlock + "root=" + shellQuote(root) + "\nlogs=" + shellQuote(logRoot) + "\n"
			if tt.completion != "" {
				body += "mkdir -p \"$root/.worker-rollout\"\nprintf '%s' " + shellQuote(tt.completion) + " >\"$root/receipt-template\"\nrmdir \"$root/.worker-rollout\"\n"
				// Install the receipt once the genuine guard creates its intent.
				body += "(until [ -f \"$root/.worker-rollout/guardian-pid\" ]; do sleep 0.01; done; cp \"$root/receipt-template\" \"$root/.worker-rollout/completion.json\") &\n"
			}
			body += "{ sleep 0.1; printf '%s' " + shellQuote(tt.input) + "; } | worker_rollout_guard attempt-a build-a \"$root\" \"$logs\" \"$root/legacy.lock\"\n"
			output, err := runWorkerShell(t, functions, body)
			if tt.wantSuccess {
				require.NoError(t, err, "only exact positively completed attempts should clear the latch")
				require.Contains(t, output, "complete:attempt-a", "guard should acknowledge this exact completion")
				_, statErr := os.Stat(filepath.Join(root, ".worker-rollout"))
				require.True(t, os.IsNotExist(statErr), "successful attempt should move its latch into a unique receipt")
				_, statErr = os.Stat(filepath.Join(root, ".worker-rollout-receipts", "attempt-a", "completion.json"))
				require.NoError(t, statErr, "successful completion must preserve the durable identity-bound receipt")
			} else {
				require.Error(t, err, "unknown or prior attempts must never be silently cleared")
				if !tt.legacyBusy {
					_, statErr := os.Stat(filepath.Join(root, ".worker-rollout"))
					require.NoError(t, statErr, "failed attempts must preserve the latch for reconciliation")
					if !tt.prior {
						data, readErr := os.ReadFile(filepath.Join(root, ".worker-rollout", "failure.json"))
						require.NoError(t, readErr, "launcher loss must leave durable failure context even without retained stderr")
						var failure map[string]string
						require.NoError(t, json.Unmarshal(data, &failure), "guardian failure context should be structured")
						require.Equal(t, map[string]string{"attempt": "attempt-a", "build_sha": "build-a", "phase": "guardian_failed", "candidate_node": "unknown", "candidate_container": "unknown"}, failure, "guardian failure should bind this attempt and explicitly retain unknown candidate identity")
					}
				}
			}
		})
	}
}

func TestWorkerRolloutRejectsDetachedBeforeHostAccess(t *testing.T) {
	t.Parallel()
	script := workerRolloutScript(t)
	functions := extractTopLevelShellFunction(t, script, "begin_worker_rollout_guard", "apply_static_egress_worker_host_map")
	output, err := runWorkerShell(t, functions, `
WORKER_DEPLOY_DETACH=1
ssh() { echo unexpected-host-access; return 0; }
begin_worker_rollout_guard
`)
	require.Error(t, err, "detached worker mutation must be rejected before contacting the host")
	require.NotContains(t, output, "unexpected-host-access", "rejected detached mode must have no remote effects")
	require.Contains(t, output, "retirement must be joined", "caller should understand why background acknowledgement is insufficient")
	require.NotContains(t, script, `if [ -n "${WORKER_DEPLOY_DETACH:-}" ]`, "typed zero must not accidentally enable detachment")
	require.Less(t, strings.Index(script, "  begin_worker_rollout_guard\n"), strings.Index(script, "Refreshing secrets from .env.production.enc"), "exclusive latch must precede secret/configuration transport")
}

func shellQuote(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'" }

func TestWorkerRolloutTypedZeroWaitsForGuardianCompletion(t *testing.T) {
	t.Parallel()
	script := workerRolloutScript(t)
	functions := extractTopLevelShellFunction(t, script, "remote_shell_quote", "remote_env_assignment") +
		extractTopLevelShellFunction(t, script, "worker_rollout_guard", "apply_static_egress_worker_host_map")
	output, err := runWorkerShell(t, functions, `
WORKER_DEPLOY_DETACH=0
TAG=0123456789012345678901234567890123456789
HOST=example.invalid
SSH_OPTS=()
ssh() {
  printf 'ready:%s\n' "$WORKER_ROLLOUT_ATTEMPT"
  read -r command token
  [ "$command" = complete ] && [ "$token" = "$WORKER_ROLLOUT_ATTEMPT" ]
  printf 'complete:%s\n' "$token"
}
begin_worker_rollout_guard
echo synchronous-worker-returned
`)
	require.NoError(t, err, "typed zero must use the joined foreground path and complete its guardian handshake")
	require.Contains(t, output, "synchronous-worker-returned", "foreground caller should wait for a real guardian readiness acknowledgement")
}

func TestWorkerRolloutSignalsRetainCandidateAndIntent(t *testing.T) {
	t.Parallel()
	script := workerRolloutScript(t)
	functions := extractShellFunction(t, script, "preserve_failed_worker_candidate", "start_worker_generation") +
		extractShellFunction(t, script, "remote_deploy_exit", "compose_services_except")
	output, err := runWorkerShell(t, functions, `
ROLE=worker
ACTIVE_WORKER_CANDIDATE_NODE=exact-candidate-node
STARTED_WORKER_CID=exact-candidate-container
WORKER_ROLLOUT_ATTEMPT=attempt-a
IMAGE_TAG=0123456789012345678901234567890123456789
run_worker_deployctl() { printf 'ctl %s\n' "$*"; }
record_worker_rollout_state() { printf 'record %s\n' "$*"; }
rm() { echo harmless-staged-input-cleanup; }
trap remote_deploy_exit EXIT
trap 'exit 143' TERM
kill -TERM "$$"
`)
	require.Error(t, err, "signal interruption must fail deployment")
	require.Contains(t, output, "ctl mark-draining --node-id exact-candidate-node --intent planned_rollout --deploy-id attempt-a", "signal cleanup should best-effort close only the known candidate admission")
	require.NotContains(t, output, "docker stop", "signal cleanup must preserve candidate work")
	require.NotContains(t, output, ".worker-rollout/attempt", "signal cleanup must not clear the durable attempt latch")
}

func TestWorkerRolloutActualFlockRejectsConcurrentAttempt(t *testing.T) {
	t.Parallel()
	if _, err := exec.LookPath("flock"); err != nil {
		t.Skip("host has no Linux flock; CI runs the actual lock concurrency scenario")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	root, logs := t.TempDir(), t.TempDir()
	script := workerRolloutScript(t)
	functions := extractTopLevelShellFunction(t, script, "worker_rollout_guard", "finish_worker_rollout_guard")
	body := "set -euo pipefail\n" + functions + "\nworker_rollout_guard attempt-a build-a " + shellQuote(root) + " " + shellQuote(logs) + " " + shellQuote(filepath.Join(root, "legacy.lock"))
	first := exec.CommandContext(ctx, "bash", "-c", body)
	in, err := first.StdinPipe()
	require.NoError(t, err, "should provide a live guardian control channel")
	out, err := first.StdoutPipe()
	require.NoError(t, err, "should observe guardian readiness")
	var stderr bytes.Buffer
	first.Stderr = &stderr
	require.NoError(t, first.Start(), "should start the first real host-lock owner")
	t.Cleanup(func() {
		if err := in.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
			t.Logf("guardian control channel cleanup: %v", err)
		}
		if first.Process != nil && first.ProcessState == nil {
			if err := first.Process.Kill(); err != nil && !errors.Is(err, os.ErrProcessDone) {
				t.Logf("guardian process cleanup: %v", err)
			}
		}
	})
	ready, err := bufio.NewReader(out).ReadString('\n')
	require.NoError(t, err, "first guardian should acquire the real lock before admitting mutations")
	require.Equal(t, "ready:attempt-a\n", ready, "readiness should bind the exact lock owner")
	second := exec.CommandContext(ctx, "bash", "-c", strings.Replace(body, "worker_rollout_guard attempt-a", "worker_rollout_guard attempt-b", 1))
	secondOutput, err := second.CombinedOutput()
	require.Error(t, err, "a second real rollout must refuse the active host lock")
	require.Contains(t, string(secondOutput), "holds the host lock", "concurrent refusal must happen before mutation")
	require.NoError(t, first.Process.Signal(syscall.SIGTERM), "should simulate abrupt guardian loss")
	require.Error(t, first.Wait(), "signal loss must leave the attempt incomplete")
	attempt, err := os.ReadFile(filepath.Join(root, ".worker-rollout", "attempt"))
	require.NoError(t, err, "guardian loss must retain the durable intent despite releasing the flock")
	require.Equal(t, "attempt-a\n", string(attempt), "guardian loss must not permit a replacement to inherit or reset the attempt")
}

func TestWorkerRolloutStructuredReceiptsBindExactAttemptAndBuild(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, kind, phase, node, cid, storedAttempt, storedBuild string
		wantSuccess                                              bool
	}{
		{name: "candidate before uncertain create", kind: "candidate", phase: "candidate_starting", node: "candidate-node", cid: "unknown", storedAttempt: "attempt-a", storedBuild: "build-a", wantSuccess: true},
		{name: "exact created candidate", kind: "candidate", phase: "candidate_created", node: "candidate-node", cid: "candidate-container", storedAttempt: "attempt-a", storedBuild: "build-a", wantSuccess: true},
		{name: "specific health failure", kind: "failure", phase: "candidate_health_failed", node: "candidate-node", cid: "candidate-container", storedAttempt: "attempt-a", storedBuild: "build-a", wantSuccess: true},
		{name: "unknown start outcome", kind: "failure", phase: "candidate_start_outcome_unknown", node: "candidate-node", cid: "unknown", storedAttempt: "attempt-a", storedBuild: "build-a", wantSuccess: true},
		{name: "interrupted retirement", kind: "failure", phase: "old_generation_retirement", node: "candidate-node", cid: "candidate-container", storedAttempt: "attempt-a", storedBuild: "build-a", wantSuccess: true},
		{name: "different attempt", kind: "candidate", phase: "candidate_starting", node: "candidate-node", cid: "unknown", storedAttempt: "prior-attempt", storedBuild: "build-a"},
		{name: "different build", kind: "failure", phase: "candidate_health_failed", node: "candidate-node", cid: "candidate-container", storedAttempt: "attempt-a", storedBuild: "prior-build"},
		{name: "invalid receipt type", kind: "unbounded-path", phase: "candidate_created", node: "candidate-node", cid: "candidate-container", storedAttempt: "attempt-a", storedBuild: "build-a"},
		{name: "raw exception is not a phase", kind: "failure", phase: "unexpected raw exception 123", node: "candidate-node", cid: "candidate-container", storedAttempt: "attempt-a", storedBuild: "build-a"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			directory := t.TempDir()
			require.NoError(t, os.WriteFile(filepath.Join(directory, "attempt"), []byte(tt.storedAttempt+"\n"), 0600), "should create the exact durable attempt fixture")
			require.NoError(t, os.WriteFile(filepath.Join(directory, "build-sha"), []byte(tt.storedBuild+"\n"), 0600), "should create the exact durable source fixture")
			script := workerRolloutScript(t)
			function := extractShellFunction(t, script, "record_worker_rollout_state", "start_worker_generation")
			body := "WORKER_ROLLOUT_ATTEMPT=attempt-a\nIMAGE_TAG=build-a\nsync(){ :; }\nrecord_worker_rollout_state " +
				shellQuote(tt.kind) + " " + shellQuote(tt.phase) + " " + shellQuote(tt.node) + " " + shellQuote(tt.cid) + " " + shellQuote(directory)
			output, err := runWorkerShell(t, function, body)
			entries, readErr := os.ReadDir(directory)
			require.NoError(t, readErr, "should inspect all durable receipt artifacts")
			if tt.wantSuccess {
				require.NoError(t, err, "exact scoped state should persist atomically: %s", output)
				data, readErr := os.ReadFile(filepath.Join(directory, tt.kind+".json"))
				require.NoError(t, readErr, "should persist the fixed structured receipt path")
				var actual map[string]string
				require.NoError(t, json.Unmarshal(data, &actual), "the receipt should contain only simple structured identity and phase fields")
				require.Equal(t, map[string]string{
					"attempt": "attempt-a", "build_sha": "build-a", "phase": tt.phase,
					"candidate_node": tt.node, "candidate_container": tt.cid,
				}, actual, "receipt should bind the exact candidate identity, fixed phase and deployment attempt without raw configuration")
				require.Equal(t, 3, len(entries), "successful atomic rename should leave no temporary receipt artifacts")
			} else {
				require.Error(t, err, "unscoped or unsafe state must refuse to write a receipt")
				require.Equal(t, 2, len(entries), "failed identity validation must preserve the existing intent without creating new artifacts")
			}
		})
	}
}

func TestWorkerVerifiedCandidateKeepsAdmissionWhenOldRetirementFails(t *testing.T) {
	t.Parallel()
	tests := []struct{ name, mode string }{
		{name: "retirement deadline", mode: "deadline"},
		{name: "unknown retirement seal", mode: "seal-unknown"},
		{name: "unknown stop result", mode: "stop-unknown"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			script := workerRolloutScript(t)
			functions := extractShellFunction(t, script, "preserve_failed_worker_candidate", "dump_diagnostics") +
				extractShellFunction(t, script, "remote_deploy_exit", "compose_services_except")
			trace := filepath.Join(t.TempDir(), "trace")
			output, err := runWorkerShell(t, functions, `
ROLE=worker
IMAGE_TAG=0123456789012345678901234567890123456789
WORKER_ROLLOUT_ATTEMPT=worker-attempt-a
COMPOSE_FILE=worker.yml
HEALTH_SERVICE=worker
trace=`+shellQuote(trace)+`
mode=`+tt.mode+`
record_worker_rollout_state() { printf 'record %s\n' "$*" >>"$trace"; }
list_running_worker_containers() { echo old-container; }
first_running_worker_node_id() { echo old-node; }
worker_container_node_id() { echo old-node; }
read_worker_env_value() { case "$1" in NODE_ID) echo base;; WORKER_PRIVATE_IP) echo 192.0.2.1;; esac; }
ensure_routine_worker_fingerprints_compatible() { :; }
load_worker_endpoint_check_env() { :; }
worker_host_capacity_preflight() { :; }
find_free_worker_port() { echo 8081; }
sanitize_compose_project() { echo project; }
worker_expected_schema_version() { echo 312; }
protect_active_executor_images() { :; }
wait_container_healthy() { :; }
wait_worker_db_heartbeat() { :; }
resolve_worker_drain_timeout_seconds() { echo 2; }
sleep() { SECONDS=$((SECONDS + 5)); }
timeout() { shift 2; "$@"; }
rm() { :; }
docker() {
  printf 'docker %s\n' "$*" >>"$trace"
  case "$*" in *" ps -q "*) echo new-container;; *" up -d "*) return 0;; inspect*) echo 'true 42';; stop*) return 1;; esac
}
run_worker_deployctl() {
  printf 'ctl %s\n' "$*" >>"$trace"
  case "$1" in
    seal-retirement)
      case "$mode" in deadline) return 3;; seal-unknown) return 1;; esac
      printf '{"sealed":true,"node_id":"old-node","deploy_id":"worker-attempt-a","build_sha":"%s","sealed_at":"2026-01-01T00:00:00Z"}\n' "$IMAGE_TAG"
      ;;
  esac
}
deploy_worker_blue_green
`)
			require.Error(t, err, "failed or unknown retirement must retain the unresolved deployment")
			data, readErr := os.ReadFile(trace)
			require.NoError(t, readErr, "should preserve the exact candidate and retirement trace")
			actual := string(data)
			require.Contains(t, actual, "record candidate candidate_verified base-gattempt-a-012345678901 new-container", "candidate validation must precede old retirement")
			require.Contains(t, actual, "record failure old_generation_retirement base-gattempt-a-012345678901 new-container", "unrelated retirement failure must retain fixed durable failure context")
			require.NotContains(t, actual, "ctl mark-draining --node-id base-g", "a verified candidate must stay admitting when retirement of an older generation fails")
			require.Contains(t, output, "verified candidate remains admitting", "failure output should distinguish rollout incompleteness from candidate unsafety")
		})
	}
}

func TestWorkerVerifiedCandidateKeepsAdmissionForUnrelatedFailures(t *testing.T) {
	t.Parallel()
	for _, phase := range []string{"support_service_verification", "post_rollout_prune", "completion_receipt"} {
		t.Run(phase, func(t *testing.T) {
			t.Parallel()
			script := workerRolloutScript(t)
			functions := extractShellFunction(t, script, "preserve_failed_worker_candidate", "start_worker_generation") +
				extractShellFunction(t, script, "remote_deploy_exit", "compose_services_except")
			output, err := runWorkerShell(t, functions, `
ROLE=worker
ACTIVE_WORKER_CANDIDATE_NODE=candidate-node
STARTED_WORKER_CID=candidate-container
WORKER_ROLLOUT_ATTEMPT=attempt-a
IMAGE_TAG=build-a
WORKER_CANDIDATE_VALIDATED=1
WORKER_ROLLOUT_PHASE=`+phase+`
record_worker_rollout_state() { printf 'record %s\n' "$*"; }
run_worker_deployctl() { printf 'ctl %s\n' "$*"; }
rm() { :; }
false
`)
			require.Error(t, err, "unrelated failure must still fail the rollout and preserve its latch")
			require.Contains(t, output, "record failure "+phase+" candidate-node candidate-container", "exit handling must record the fixed unrelated phase")
			require.NotContains(t, output, "ctl mark-draining", "support or receipt failures must not remove the healthy admitting generation")
		})
	}
}

func runWorkerShellWithClosedErrorPipe(t *testing.T, source, body string) (string, error) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	reader, writer, err := os.Pipe()
	require.NoError(t, err, "should create an actual error-channel pipe")
	require.NoError(t, reader.Close(), "should disconnect the error-channel reader before the failure")
	t.Cleanup(func() {
		if err := writer.Close(); err != nil && !errors.Is(err, os.ErrClosed) {
			t.Logf("error pipe cleanup: %v", err)
		}
	})
	cmd := exec.CommandContext(ctx, "bash", "-c", "set -euo pipefail\n"+source+"\n"+body)
	cmd.Dir = t.TempDir()
	cmd.Stderr = writer
	var output bytes.Buffer
	cmd.Stdout = &output
	err = cmd.Run()
	require.NoError(t, ctx.Err(), "disconnected-channel preservation must finish within its bound")
	return output.String(), err
}

func TestWorkerClosedErrorChannelStillPersistsFailureContext(t *testing.T) {
	t.Parallel()
	t.Run("guardian", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		script := workerRolloutScript(t)
		function := extractTopLevelShellFunction(t, script, "worker_rollout_guard", "finish_worker_rollout_guard")
		_, err := runWorkerShellWithClosedErrorPipe(t, function, `flock(){ :; }; sync(){ :; }
worker_rollout_guard attempt-a build-a `+shellQuote(root)+` `+shellQuote(root)+` `+shellQuote(filepath.Join(root, "legacy.lock")))
		require.Error(t, err, "disconnected guardian control remains an incomplete rollout")
		data, readErr := os.ReadFile(filepath.Join(root, ".worker-rollout", "failure.json"))
		require.NoError(t, readErr, "a broken error pipe must not kill the guardian before persisting failure context")
		var actual map[string]string
		require.NoError(t, json.Unmarshal(data, &actual), "guardian failure context must remain valid JSON")
		require.Equal(t, "guardian_failed", actual["phase"], "guardian should persist its fixed failure phase before diagnostics")
	})
	for _, verified := range []bool{false, true} {
		name := "unverified candidate"
		if verified {
			name = "verified candidate"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			directory := t.TempDir()
			require.NoError(t, os.WriteFile(filepath.Join(directory, "attempt"), []byte("attempt-a\n"), 0600), "should create exact durable attempt")
			require.NoError(t, os.WriteFile(filepath.Join(directory, "build-sha"), []byte("build-a\n"), 0600), "should create exact durable build")
			script := workerRolloutScript(t)
			functions := extractShellFunction(t, script, "preserve_failed_worker_candidate", "start_worker_generation") +
				extractShellFunction(t, script, "remote_deploy_exit", "compose_services_except")
			functions = strings.Replace(functions, "  record_worker_rollout_state() {", "  persisted_worker_rollout_state() {", 1)
			validated := "0"
			if verified {
				validated = "1"
			}
			output, err := runWorkerShellWithClosedErrorPipe(t, functions, `
ROLE=worker
ACTIVE_WORKER_CANDIDATE_NODE=candidate-node
STARTED_WORKER_CID=candidate-container
WORKER_ROLLOUT_ATTEMPT=attempt-a
IMAGE_TAG=build-a
WORKER_ROLLOUT_PHASE=old_generation_retirement
WORKER_CANDIDATE_VALIDATED=`+validated+`
record_worker_rollout_state() { persisted_worker_rollout_state "$@" `+shellQuote(directory)+`; }
sync(){ :; }
run_worker_deployctl(){ printf 'ctl %s\n' "$*"; }
rm(){ :; }
false
`)
			require.Error(t, err, "broken diagnostics must leave deployment incomplete")
			data, readErr := os.ReadFile(filepath.Join(directory, "failure.json"))
			require.NoError(t, readErr, "closed stderr must not prevent atomic failure receipt")
			var actual map[string]string
			require.NoError(t, json.Unmarshal(data, &actual), "failure receipt must remain valid structured JSON")
			require.Equal(t, map[string]string{"attempt": "attempt-a", "build_sha": "build-a", "phase": "old_generation_retirement", "candidate_node": "candidate-node", "candidate_container": "candidate-container"}, actual, "receipt must bind exact failure identity despite stderr loss")
			if verified {
				require.NotContains(t, output, "ctl mark-draining", "unrelated channel failure must leave verified candidate admitting")
			} else {
				require.Contains(t, output, "ctl mark-draining --node-id candidate-node", "unverified candidate must still be admission-drained after failure even with a broken error channel")
			}
		})
	}
}

func TestRoutineWorkerPrunePreservesStoppedGenerationContainers(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name, role, mode   string
		wantContainerPrune bool
	}{
		{name: "routine worker", role: "worker", mode: "routine"},
		{name: "explicit maintenance", role: "worker", mode: "maintenance", wantContainerPrune: true},
		{name: "app", role: "app", mode: "routine", wantContainerPrune: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			script := workerRolloutScript(t)
			function := extractShellFunction(t, script, "prune_unused_docker_resources", "run_worker_session_deploy_guardrail")
			output, err := runWorkerShell(t, function, `IMAGE_TAG=build-a
DEPLOY_MODE=`+tt.mode+`
docker(){ printf 'docker %s\n' "$*"; }
run_worker_deployctl(){ :; }
prune_unused_docker_resources `+tt.role)
			require.NoError(t, err, "safe prune mode should complete")
			if tt.wantContainerPrune {
				require.Contains(t, output, "docker container prune", "explicit maintenance and app pruning retain their existing policy")
			} else {
				require.NotContains(t, output, "docker container prune", "routine deployment must preserve stopped worker logs and exact generation evidence")
			}
			require.Contains(t, output, "docker image prune", "safe image cache cleanup should remain available")
		})
	}
}
