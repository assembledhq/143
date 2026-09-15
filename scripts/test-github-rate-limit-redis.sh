#!/bin/sh
set -eu

redis_version=7.4.2
redis_sha256=4ddebbf09061cbb589011786febdb34f29767dd7f89dbe712d2b68e808af6a1f
# Sandbox /tmp and /var/tmp are small tmpfs mounts; keep toolchains, build
# scratch, and Redis data on the home filesystem unless explicitly overridden.
harness_root="${GITHUB_RATE_LIMIT_TEST_DIR:-$HOME/.cache/143-github-rate-limit}"
mkdir -p "$harness_root"
tool_root="$harness_root/redis-${redis_version}"
redis_server="${REDIS_SERVER_BIN:-}"
redis_cli="${REDIS_CLI_BIN:-}"

run_root=$(mktemp -d "$harness_root/run.XXXXXX")
node_pids=""
port_lock=""
stop_nodes() {
  # Redis stays in the foreground, so these are children owned by this shell.
  # Never discover PIDs through a shared port or another instance's pidfile.
  for node_pid in $node_pids; do
    kill "$node_pid" >/dev/null 2>&1 || true
  done
  for node_pid in $node_pids; do
    wait "$node_pid" 2>/dev/null || true
  done
  node_pids=""
}
release_ports() {
  if [ -n "$port_lock" ]; then
    rmdir "$port_lock"
    port_lock=""
  fi
}
cleanup() {
  stop_nodes
  release_ports
  rm -rf "$run_root"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM
case "${GOTMPDIR:-}" in
  ""|/tmp|/tmp/|/var/tmp|/var/tmp/) GOTMPDIR="$run_root/tmp" ;;
esac
export TMPDIR="$run_root/tmp" GOTMPDIR GOCACHE="${GOCACHE:-$harness_root/go-build-cache}"
mkdir -p "$TMPDIR" "$GOTMPDIR" "$GOCACHE"

if [ -z "$redis_server" ] || [ -z "$redis_cli" ]; then
  if command -v redis-server >/dev/null 2>&1 && command -v redis-cli >/dev/null 2>&1; then
    redis_server=$(command -v redis-server)
    redis_cli=$(command -v redis-cli)
  else
    redis_server="$tool_root/src/redis-server"
    redis_cli="$tool_root/src/redis-cli"
    if [ ! -x "$redis_server" ] || [ ! -x "$redis_cli" ]; then
      command -v curl >/dev/null 2>&1 || { echo "curl is required to acquire pinned Redis ${redis_version}" >&2; exit 1; }
      command -v make >/dev/null 2>&1 || { echo "make is required to build pinned Redis ${redis_version}" >&2; exit 1; }
      # A cache miss builds in this run's directory so concurrent downloads or
      # builds cannot remove or replace each other's source tree.
      tool_root="$run_root/redis-${redis_version}"
      redis_server="$tool_root/src/redis-server"
      redis_cli="$tool_root/src/redis-cli"
      archive="$run_root/redis-${redis_version}.tar.gz"
      curl -fsSL "https://download.redis.io/releases/redis-${redis_version}.tar.gz" -o "$archive"
      if command -v sha256sum >/dev/null 2>&1; then
        actual_sha=$(sha256sum "$archive" | awk '{print $1}')
      else
        actual_sha=$(shasum -a 256 "$archive" | awk '{print $1}')
      fi
      [ "$actual_sha" = "$redis_sha256" ] || { echo "Redis source checksum mismatch" >&2; exit 1; }
      tar -xzf "$archive" -C "$run_root"
      make -C "$tool_root/src" BUILD_TLS=no MALLOC=libc redis-server redis-cli
    fi
  fi
fi

[ -x "$redis_server" ] || { echo "redis-server infrastructure is unavailable" >&2; exit 1; }
[ -x "$redis_cli" ] || { echo "redis-cli infrastructure is unavailable" >&2; exit 1; }

start_redis() {
  port=$1
  cluster=$2
  node_dir="$run_root/$port"
  mkdir -p "$node_dir"
  cluster_args=""
  if [ "$cluster" = "yes" ]; then
    cluster_args="--cluster-enabled yes --cluster-config-file nodes.conf --cluster-node-timeout 5000"
  fi
  "$redis_server" --bind 127.0.0.1 --protected-mode no --port "$port" \
    --dir "$node_dir" --save "" --appendonly no --daemonize no \
    $cluster_args >"$node_dir/redis.log" 2>&1 &
  started_pid=$!
  node_pids="$node_pids $started_pid"
  attempts=0
  while kill -0 "$started_pid" 2>/dev/null; do
    # PING alone could reach an unrelated instance if our child failed to
    # bind. Confirm this endpoint belongs to the child we just started.
    if grep -q 'Ready to accept connections' "$node_dir/redis.log"; then
      serving_pid=$("$redis_cli" -h 127.0.0.1 -p "$port" info server 2>/dev/null | awk -F: '/^process_id:/ {gsub(/\r/, "", $2); print $2}')
      if [ "$serving_pid" = "$started_pid" ]; then
        return 0
      fi
    fi
    attempts=$((attempts + 1))
    if [ "$attempts" -ge 50 ]; then
      break
    fi
    sleep 0.1
  done
  return 1
}

# Reserve disjoint four-port groups across harness processes. Command ports
# occupy 24000..33999 and Cluster bus ports 34001..43999, so neither range can
# overlap another group's command or bus ports. Unrelated listeners are
# detected by owned-child startup below; release the group and try another.
port_lock_root="$harness_root/ports-$(id -u)"
mkdir -p "$port_lock_root"
slot=$(( $$ % 2500 ))
groups_tried=0
startup_failures=0
while :; do
  if [ "$groups_tried" -ge 2500 ] || [ "$startup_failures" -ge 10 ]; then
    echo "Unable to start an isolated Redis test group" >&2
    exit 1
  fi
  base_port=$((24000 + 4 * slot))
  slot=$(((slot + 1) % 2500))
  groups_tried=$((groups_tried + 1))
  if ! mkdir "$port_lock_root/$base_port" 2>/dev/null; then
    continue
  fi
  port_lock="$port_lock_root/$base_port"
  standalone_port=$base_port
  cluster_port_1=$((base_port + 1))
  cluster_port_2=$((base_port + 2))
  cluster_port_3=$((base_port + 3))
  if start_redis "$standalone_port" no &&
      start_redis "$cluster_port_1" yes &&
      start_redis "$cluster_port_2" yes &&
      start_redis "$cluster_port_3" yes; then
    echo "Redis test group: standalone=$standalone_port cluster=$cluster_port_1,$cluster_port_2,$cluster_port_3"
    break
  fi
  echo "Redis test ports $base_port..$cluster_port_3 unavailable; retrying another group" >&2
  stop_nodes
  release_ports
  startup_failures=$((startup_failures + 1))
done

"$redis_cli" --cluster create \
  "127.0.0.1:$cluster_port_1" "127.0.0.1:$cluster_port_2" "127.0.0.1:$cluster_port_3" \
  --cluster-replicas 0 --cluster-yes >/dev/null

cluster_attempts=0
until "$redis_cli" -h 127.0.0.1 -p "$cluster_port_1" cluster info | grep -q 'cluster_state:ok'; do
  cluster_attempts=$((cluster_attempts + 1))
  if [ "$cluster_attempts" -ge 50 ]; then
    echo "Redis Cluster failed to reach cluster_state:ok" >&2
    exit 1
  fi
  sleep 0.1
done

GITHUB_RATE_LIMIT_REDIS_URL="redis://127.0.0.1:$standalone_port/0" \
GITHUB_RATE_LIMIT_REDIS_CLUSTER_ADDRS="127.0.0.1:$cluster_port_1,127.0.0.1:$cluster_port_2,127.0.0.1:$cluster_port_3" \
go test -race -tags=redis_integration -count=1 -timeout=120s ./internal/services/github/ratelimit
