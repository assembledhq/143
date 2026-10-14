#!/usr/bin/env bash
# Own a disposable GitHub-hosted Linux daemon with production XFS overlay quotas.
set -euo pipefail

fail() { printf 'review acceptance runtime: %s\n' "$*" >&2; return 1; }

guard_host() {
  [[ ${GITHUB_ACTIONS:-} == true && ${RUNNER_ENVIRONMENT:-} == github-hosted && ${RUNNER_OS:-} == Linux ]] ||
    fail 'requires a disposable GitHub-hosted Linux runner'
  [[ $(uname -s) == Linux && $(uname -m) == x86_64 ]] || fail 'requires Linux amd64'
  [[ ${GITHUB_RUN_ID:-} =~ ^[0-9]+$ && ${GITHUB_RUN_ATTEMPT:-} =~ ^[0-9]+$ ]] || fail 'requires numeric GitHub run identity'
}

validate_paths() {
  python3 - <<'PY'
import os
from pathlib import Path
root = Path(os.environ['RUNNER_TEMP'])
if not root.is_absolute() or not root.is_dir() or root.resolve() != root:
    raise SystemExit('RUNNER_TEMP must be an existing canonical absolute directory')
paths = []
for key in ('REVIEW_ACCEPTANCE_RUNTIME_STATE', 'REVIEW_ACCEPTANCE_ARTIFACT_DIR'):
    value = Path(os.environ[key])
    if not value.is_absolute() or value.resolve() != value or root not in value.parents:
        raise SystemExit(f'{key} must be a canonical child of RUNNER_TEMP without symlinks')
    if '\n' in str(value) or '\r' in str(value):
        raise SystemExit(f'{key} must not contain line breaks')
    paths.append(value)
if paths[0] == paths[1] or paths[0] in paths[1].parents or paths[1] in paths[0].parents:
    raise SystemExit('runtime state and artifacts must be separate directories')
if len(os.fsencode(str(paths[0] / 'docker.sock'))) > 100:
    raise SystemExit('owned Docker socket path is too long')
env = Path(os.environ['GITHUB_ENV'])
if not env.is_absolute() or not env.is_file() or env.resolve() != env or root not in env.parents:
    raise SystemExit('GITHUB_ENV must be an existing canonical file under RUNNER_TEMP')
PY
  [[ ${REVIEW_ACCEPTANCE_NETWORK:-} =~ ^review-acceptance-[a-zA-Z0-9_.-]+$ ]] || fail 'requires a uniquely named acceptance network'
}

require_capacity() {
  # 24 GiB physically allocated backing + 8 GiB kept free outside the image.
  local available_bytes=$1 required_bytes=$((32 * 1024 * 1024 * 1024))
  [[ $available_bytes =~ ^[0-9]+$ ]] || fail 'free capacity is not a byte count'
  (( available_bytes >= required_bytes )) || fail "need at least $required_bytes free bytes before allocating the 24 GiB backing and 8 GiB reserve; found $available_bytes"
}


require_postbuild_capacity() {
  local xfs_available_bytes=$1 external_available_bytes=$2
  local required_xfs_bytes=$((12 * 1024 * 1024 * 1024)) required_external_bytes=$((8 * 1024 * 1024 * 1024))
  [[ $xfs_available_bytes =~ ^[0-9]+$ && $external_available_bytes =~ ^[0-9]+$ ]] || fail 'post-build free capacity is not a byte count'
  (( xfs_available_bytes >= required_xfs_bytes )) || fail "owned XFS needs $required_xfs_bytes free bytes for the unchanged 10 GiB writable quota plus 2 GiB metadata headroom; found $xfs_available_bytes; use a larger runner"
  (( external_available_bytes >= required_external_bytes )) || fail "outside XFS needs $required_external_bytes free bytes for the bounded snapshot and compile/evidence reserve; found $external_available_bytes; use a larger runner"
}

prepare_resolver() {
  # Host-local resolver stubs cannot be reached from the gVisor network namespace.
  # Select systemd's configured upstream file instead of inventing a public DNS
  # server. This records configuration only; HTTPS acceptance proves reachability.
  python3 - "$1" "$2" <<'PY_RESOLVER'
import ipaddress, json, os, sys
from pathlib import Path

def nameservers(path):
    try:
        text = path.read_text()
    except OSError as error:
        return [], error
    result = []
    for line in text.splitlines():
        parts = line.split()
        if len(parts) >= 2 and parts[0] == 'nameserver':
            try:
                address = ipaddress.ip_address(parts[1])
            except ValueError:
                continue
            if address not in result:
                result.append(address)
    return result, None

source = Path(sys.argv[1])
configured, read_error = nameservers(source)
if configured and all(address.is_loopback for address in configured):
    source = Path(sys.argv[2])
    configured, read_error = nameservers(source)
addresses = [str(address) for address in configured
             if not address.is_loopback and not address.is_unspecified and not address.is_multicast]
artifacts = Path(os.environ['REVIEW_ACCEPTANCE_ARTIFACT_DIR'])
(artifacts / 'resolver-configuration.json').write_text(json.dumps({
    'source_path': str(source), 'configured_nameservers': addresses,
    'reachability_verified': False,
}, indent=2) + '\n')
if not addresses:
    reason = '; configured resolver file could not be read' if read_error is not None else ''
    raise SystemExit('review acceptance runtime: no usable configured nameserver in ' + str(source) + reason)
state = Path(os.environ['REVIEW_ACCEPTANCE_RUNTIME_STATE'])
(state / 'resolv.conf').write_text(''.join('nameserver ' + address + '\n' for address in addresses)
                                  + 'options timeout:2 attempts:3\n')
PY_RESOLVER
  chmod 644 "$REVIEW_ACCEPTANCE_RUNTIME_STATE/resolv.conf"
}

clear_docker_client_environment() {
  unset DOCKER_CONTEXT DOCKER_TLS_VERIFY DOCKER_CERT_PATH DOCKER_TLS
  export DOCKER_HOST="unix://$REVIEW_ACCEPTANCE_RUNTIME_STATE/docker.sock"
}

verify_state() {
  python3 - <<'PY'
import json, os, stat
from pathlib import Path
state = Path(os.environ['REVIEW_ACCEPTANCE_RUNTIME_STATE'])
marker = state / 'owner.json'
info = marker.lstat()
if not stat.S_ISREG(info.st_mode) or info.st_uid != os.getuid() or info.st_mode & 0o077:
    raise SystemExit('unsafe runtime ownership record')
expected = {'format': 1, 'state': str(state), 'run_id': os.environ['GITHUB_RUN_ID'],
            'attempt': os.environ['GITHUB_RUN_ATTEMPT'], 'uid': os.getuid(),
            'network': os.environ['REVIEW_ACCEPTANCE_NETWORK']}
if json.loads(marker.read_text()) != expected:
    raise SystemExit('runtime ownership record does not match this run')
PY
}

quota_probe() {
  verify_state
  clear_docker_client_environment
  : "${REVIEW_ACCEPTANCE_IMAGE:?build the acceptance image before probing quotas}"
  # Recheck after image build and builder pruning. A smaller disposable backing
  # must still support the full worker quota, metadata, and outside snapshot.
  local xfs_available_bytes external_available_bytes
  xfs_available_bytes=$(df -B1 --output=avail "$REVIEW_ACCEPTANCE_RUNTIME_STATE/mount" | tail -n 1 | tr -d ' ')
  external_available_bytes=$(df -B1 --output=avail "$RUNNER_TEMP" | tail -n 1 | tr -d ' ')
  python3 - "$xfs_available_bytes" "$external_available_bytes" <<'PY_CAPACITY'
import json, os, sys
from pathlib import Path
xfs_free, external_free = (int(value) for value in sys.argv[1:])
gib = 1024 ** 3
(Path(os.environ['REVIEW_ACCEPTANCE_ARTIFACT_DIR']) / 'postbuild-capacity.json').write_text(json.dumps({
    'xfs_free_bytes': xfs_free, 'external_free_bytes': external_free,
    'container_quota_bytes': 10 * gib, 'required_xfs_free_bytes': 12 * gib,
    'required_external_free_bytes': 8 * gib,
    'ready': xfs_free >= 12 * gib and external_free >= 8 * gib,
}, indent=2) + '\n')
PY_CAPACITY
  require_postbuild_capacity "$xfs_available_bytes" "$external_available_bytes"
  # This intentionally writes actual bytes. truncate/fallocate alone could give a
  # false success with sparse files. The image's Python runs without project code.
  timeout 90s docker run --rm --runtime runsc --network none --memory 256m --cpus 1 \
    --cap-drop ALL --security-opt no-new-privileges --storage-opt size=64M \
    --label "io.143.review-acceptance-state=$REVIEW_ACCEPTANCE_RUNTIME_STATE" \
    --entrypoint python3 "$REVIEW_ACCEPTANCE_IMAGE" -c '
import errno, json, os
path = "/tmp/review-acceptance-quota-probe"
written = 0
try:
    with open(path, "wb", buffering=0) as output:
        for _ in range(96):
            chunk = memoryview(b"q" * 1024 * 1024)
            while chunk:
                count = output.write(chunk)
                written += count
                chunk = chunk[count:]
        os.fsync(output.fileno())
except OSError as error:
    if error.errno not in (errno.ENOSPC, errno.EDQUOT):
        raise
    if not 0 < written <= 64 * 1024 * 1024:
        raise SystemExit("quota exhausted outside expected range")
    print(json.dumps({"result": "pass", "limit_bytes": 64 * 1024 * 1024,
                      "written_bytes": written, "errno": error.errno}))
else:
    raise SystemExit("64 MiB overlay quota did not reject 96 MiB of writes")
' | tee "$REVIEW_ACCEPTANCE_ARTIFACT_DIR/quota-exhaustion-probe.json"
}

# runsc's default filesystem driver sets limits itself. Docker must emit the
# same cgroupfs paths; --ignore-cgroups would remove the kernel enforcement this
# acceptance suite is required to prove. See gvisor.dev/docs/user_guide/systemd/
# and docs.docker.com/engine/containers/runmetrics/.
require_kernel_cgroup_v2() {
  [[ $(stat -f -c '%T' /sys/fs/cgroup) == cgroup2fs ]] || fail 'requires host cgroup v2 kernel filesystem'
  python3 - <<'PY_CGROUP'
from pathlib import Path
root = Path('/sys/fs/cgroup')
if root.resolve() != root:
    raise SystemExit('host cgroup root must not be redirected')
controllers = set((root / 'cgroup.controllers').read_text().split())
if not {'cpu', 'memory'} <= controllers:
    raise SystemExit('host cgroup v2 requires CPU and memory controllers')
PY_CGROUP
}

write_daemon_config() {
  python3 - <<'PY_CONFIG'
import json, os
from pathlib import Path
state = Path(os.environ['REVIEW_ACCEPTANCE_RUNTIME_STATE'])
config = {'data-root': str(state / 'mount/docker'), 'exec-root': str(state / 'exec'),
          'pidfile': str(state / 'dockerd.pid'), 'hosts': ['unix://' + str(state / 'docker.sock')],
          'storage-driver': 'overlay2', 'features': {'containerd-snapshotter': False},
          'exec-opts': ['native.cgroupdriver=cgroupfs'],
          'runtimes': {'runsc': {'path': '/usr/bin/runsc', 'runtimeArgs': ['--host-uds=open']}},
          'bridge': 'none', 'iptables': True}
(state / 'daemon.json').write_text(json.dumps(config))
PY_CONFIG
}

# The flags command describes compiled defaults, not a running container's
# effective configuration. Keep that distinction and the registered overrides
# explicit instead of inferring a backing medium from current documentation.
record_runsc_flags() {
  python3 - "$REVIEW_ACCEPTANCE_RUNTIME_STATE/daemon.json" "$REVIEW_ACCEPTANCE_ARTIFACT_DIR" <<'PY_FLAGS'
import json, resource, subprocess, sys, tempfile
from pathlib import Path
config = json.loads(Path(sys.argv[1]).read_text())
runtime = config['runtimes']['runsc']
if runtime != {'path': '/usr/bin/runsc', 'runtimeArgs': ['--host-uds=open']}:
    raise SystemExit('review acceptance runtime: unexpected owned runsc registration')
limit = 65536
status, timed_out, execution_error = None, False, False

def bound_output():
    resource.setrlimit(resource.RLIMIT_FSIZE, (limit, limit))

with tempfile.TemporaryFile() as output:
    try:
        result = subprocess.run(['/usr/bin/runsc', 'flags'], stdout=output,
                                stderr=subprocess.STDOUT, timeout=5, check=False,
                                preexec_fn=bound_output)
        status = result.returncode
    except subprocess.TimeoutExpired:
        timed_out = True
    except (OSError, subprocess.SubprocessError):
        execution_error = True
    output.seek(0)
    retained = output.read(limit)
artifacts = Path(sys.argv[2])
(artifacts / 'runsc-flags.txt').write_bytes(retained)
(artifacts / 'runsc-flags-observation.json').write_text(json.dumps({
    'command': ['/usr/bin/runsc', 'flags'], 'exit_status': status,
    'timeout_seconds': 5, 'timed_out': timed_out, 'execution_error': execution_error,
    'retained_bytes': len(retained), 'output_limit_bytes': limit,
    'possibly_truncated': len(retained) == limit,
    'compiled_defaults_only': True, 'registered_runtime': runtime,
    'effective_container_overlay_verified': False,
}, indent=2) + '\n')
if status != 0 or timed_out or execution_error or len(retained) == limit:
    print('review acceptance runtime: warning: optional runsc defaults observation unavailable or bounded', file=sys.stderr)
PY_FLAGS
}

verify_daemon_configuration() {
  python3 - <<'PY_DAEMON'
import json, os
from pathlib import Path
artifacts = Path(os.environ['REVIEW_ACCEPTANCE_ARTIFACT_DIR'])
info = json.loads((artifacts / 'docker-info.json').read_text())
if info['Driver'] != 'overlay2' or 'runsc' not in info['Runtimes']:
    raise SystemExit('owned daemon must expose overlay2 and runsc')
if info.get('CgroupVersion') != '2' or info.get('CgroupDriver') != 'cgroupfs':
    raise SystemExit('owned daemon must expose cgroup v2 with the explicit cgroupfs driver')
status = dict(info['DriverStatus'])
if status.get('Backing Filesystem') != 'xfs' or status.get('Supports d_type') != 'true':
    raise SystemExit('overlay2 must use XFS with d_type')
state = Path(os.environ['REVIEW_ACCEPTANCE_RUNTIME_STATE'])
if info['DockerRootDir'] != str(state / 'mount/docker'):
    raise SystemExit('Docker daemon root does not match owned state')
PY_DAEMON
}

prepare_runtime() {
  require_kernel_cgroup_v2
  [[ ! -e $REVIEW_ACCEPTANCE_RUNTIME_STATE && ! -L $REVIEW_ACCEPTANCE_RUNTIME_STATE ]] || fail 'runtime state already exists; refusing reuse'
  umask 077
  mkdir "$REVIEW_ACCEPTANCE_RUNTIME_STATE"
  mkdir -p "$REVIEW_ACCEPTANCE_ARTIFACT_DIR"
  python3 - <<'PY'
import json, os
from pathlib import Path
state = Path(os.environ['REVIEW_ACCEPTANCE_RUNTIME_STATE'])
(state / 'owner.json').write_text(json.dumps({'format': 1, 'state': str(state),
    'run_id': os.environ['GITHUB_RUN_ID'], 'attempt': os.environ['GITHUB_RUN_ATTEMPT'],
    'uid': os.getuid(), 'network': os.environ['REVIEW_ACCEPTANCE_NETWORK']}))
PY
  local artifacts=$REVIEW_ACCEPTANCE_ARTIFACT_DIR state=$REVIEW_ACCEPTANCE_RUNTIME_STATE
  prepare_resolver /etc/resolv.conf /run/systemd/resolve/resolv.conf
  df -B1 "$RUNNER_TEMP" > "$artifacts/host-capacity-before.txt"
  {
    uname -a
    lscpu
    free -b
    lsblk -b -o NAME,SIZE,FSTYPE,MOUNTPOINTS
  } > "$artifacts/host-shape.txt"

  # These are unused preinstalled SDKs on this disposable hosted VM, never runner
  # workspaces, credentials, caches, arbitrary inputs, or the live Docker graph.
  local candidate
  : > "$artifacts/reclaimed-preinstalled-sdks.txt"
  for candidate in /usr/local/lib/android /usr/share/dotnet /usr/share/swift /opt/ghc /opt/hostedtoolcache/CodeQL; do
    if [[ -e $candidate || -L $candidate ]]; then
      [[ ! -L $candidate && $(realpath -- "$candidate") == "$candidate" ]] || fail "refusing symlinked SDK directory: $candidate"
      sudo -n du -sx -B1 -- "$candidate" >> "$artifacts/reclaimed-preinstalled-sdks.txt"
      sudo -n rm -rf --one-file-system -- "$candidate"
    fi
  done
  df -B1 "$RUNNER_TEMP" > "$artifacts/host-capacity-after-reclaim.txt"
  require_capacity "$(df -B1 --output=avail "$RUNNER_TEMP" | tail -n 1 | tr -d ' ')"

  # Install gVisor from the same trusted package source as bootstrap.sh.
  curl -fsSL https://gvisor.dev/archive.key -o "$state/gvisor-archive.key"
  sudo -n gpg --batch --yes --dearmor -o /usr/share/keyrings/gvisor-archive-keyring.gpg "$state/gvisor-archive.key"
  printf '%s\n' 'deb [arch=amd64 signed-by=/usr/share/keyrings/gvisor-archive-keyring.gpg] https://storage.googleapis.com/gvisor/releases release main' |
    sudo -n tee /etc/apt/sources.list.d/gvisor.list >/dev/null
  sudo -n apt-get update
  sudo -n apt-get install -y --no-install-recommends runsc xfsprogs
  /usr/bin/runsc --version > "$artifacts/runsc-version.txt"
  dpkg-query -W runsc xfsprogs > "$artifacts/runtime-packages.txt"

  # fallocate reserves physical capacity; a sparse backing can fail independently
  # of the quota and invalidate the acceptance result.
  fallocate -l 24G "$state/backing.xfs"
  local allocated_blocks block_bytes
  read -r allocated_blocks block_bytes < <(stat -c '%b %B' "$state/backing.xfs")
  (( allocated_blocks * block_bytes >= 24 * 1024 * 1024 * 1024 )) || fail 'XFS backing is not physically reserved'
  (( $(df -B1 --output=avail "$RUNNER_TEMP" | tail -n 1 | tr -d ' ') >= 8 * 1024 * 1024 * 1024 )) || fail 'external 8 GiB reserve is unavailable after allocation'
  sudo -n mkfs.xfs -K -f -m reflink=0 -n ftype=1 "$state/backing.xfs" > "$artifacts/mkfs-xfs.txt"
  local loop
  loop=$(sudo -n losetup --find --show --nooverlap "$state/backing.xfs")
  printf '%s\n' "$loop" > "$state/loop-device"
  mkdir "$state/mount"
  sudo -n mount -t xfs -o pquota "$loop" "$state/mount"
  findmnt -J "$state/mount" > "$artifacts/xfs-mount.json"
  sudo -n xfs_info "$state/mount" > "$artifacts/xfs-info.txt"
  sudo -n xfs_quota -x -c state "$state/mount" > "$artifacts/xfs-quota-state.txt"
  df -B1 "$state/mount" "$RUNNER_TEMP" > "$artifacts/runtime-capacity.txt"

  # This VM is disposable; stopping the default daemon avoids competing graphs.
  # Cleanup never starts/reconfigures it and never prunes its existing data.
  sudo -n systemctl stop docker.socket docker.service
  write_daemon_config
  record_runsc_flags
  sudo -n sh -c 'nohup dockerd --config-file "$1" > "$2" 2>&1 < /dev/null & printf "%s\n" "$!" > "$3"' \
    sh "$state/daemon.json" "$artifacts/dockerd.log" "$state/launch.pid"
  clear_docker_client_environment
  local ready=false ready_deadline=$((SECONDS + 60))
  while (( SECONDS < ready_deadline )); do
    if timeout 3s sudo -n docker --host "$DOCKER_HOST" info >/dev/null 2>&1; then ready=true; break; fi
    sleep 1
  done
  [[ $ready == true ]] || fail 'owned Docker daemon did not become ready in 60 seconds'
  sudo -n chown "$(id -u):$(id -g)" "$state/docker.sock"
  sudo -n chmod 600 "$state/docker.sock"
  docker info --format '{{json .}}' > "$artifacts/docker-info.json"
  docker version --format '{{json .}}' > "$artifacts/docker-version.json"
  verify_daemon_configuration
  {
    printf 'DOCKER_HOST=%s\n' "$DOCKER_HOST"
    printf 'DOCKER_CONTEXT=\nDOCKER_TLS_VERIFY=\nDOCKER_CERT_PATH=\nDOCKER_TLS=\n'
    printf 'REVIEW_REAL_REPO_RESOLV_CONF=%s/resolv.conf\n' "$state"
    printf 'REVIEW_ACCEPTANCE_RUNSC_VERSION=%s\n' "$(head -n 1 "$artifacts/runsc-version.txt")"
  } >> "$GITHUB_ENV"
}

main() {
  guard_host
  validate_paths
  case ${1:-} in
    --validate) ;;
    --probe-quota) quota_probe ;;
    '') prepare_runtime ;;
    *) fail 'usage: prepare-review-acceptance-runtime.sh [--validate|--probe-quota]' ;;
  esac
}

if [[ ${BASH_SOURCE[0]} == "$0" ]]; then main "$@"; fi
