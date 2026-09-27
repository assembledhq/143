"""Linux-only dump supervisor. No remote Docker endpoints or production restarts.

The detached supervisor inherits the common flock. Its caller watches it in
turn. Both persist evidence; neither equates a dead Docker CLI with stopped work.
"""
import json
import ipaddress
import math
import os
from pathlib import Path
import signal
import subprocess
import time

from pg_backup_state import (Refused, require, now, identity, read_json,
                             atomic_json, sync_dir, invoke)

GIB = 1024 ** 3
INTERVAL = 5
MAX_DUMP_SECONDS = 45 * 60
LABEL = 'dev.143.backup-run'


def proc_identity(pid):
    # comm may contain spaces or parentheses. Field 22 follows the final ')'.
    fields = Path(f'/proc/{pid}/stat').read_text().rsplit(')', 1)[1].split()
    require(fields[0] != 'Z', 'owner process is a zombie')
    return [Path('/proc/sys/kernel/random/boot_id').read_text().strip(), fields[19]]


def pressure(path):
    rows = {}
    for line in path.read_text().splitlines():
        parts = line.split()
        values = dict(p.split('=', 1) for p in parts[1:])
        value = float(values['avg10'])
        require(math.isfinite(value) and 0 <= value <= 100, 'invalid PSI telemetry')
        rows[parts[0]] = value
    require(set(rows) == {'some', 'full'}, 'incomplete PSI telemetry')
    return rows


def resources(root, db_pid):
    memory = {k: int(v.split()[0]) * 1024 for k, v in
              (line.split(':', 1) for line in Path('/proc/meminfo').read_text().splitlines())}
    vm = dict(line.split() for line in Path('/proc/vmstat').read_text().splitlines())
    groups = Path(f'/proc/{db_pid}/cgroup').read_text().splitlines()
    group = [line[3:] for line in groups if line.startswith('0::')]
    require(len(group) == 1, 'cgroup v2 required')
    base = Path('/sys/fs/cgroup')
    cg = base / group[0].lstrip('/')
    require(cg.resolve().is_relative_to(base) and cg != base, 'invalid database cgroup')
    maximum = int((cg / 'memory.max').read_text())
    current = int((cg / 'memory.current').read_text())
    fs = os.statvfs(root)
    return dict(free_bytes=fs.f_bavail * fs.f_frsize,
                available_bytes=memory['MemAvailable'],
                commit_headroom=memory['CommitLimit'] - memory['Committed_AS'],
                swap_bytes=memory['SwapTotal'] - memory['SwapFree'],
                swap_out_bytes=int(vm['pswpout']) * os.sysconf('SC_PAGE_SIZE'),
                db_headroom=maximum - current,
                host_io=pressure(Path('/proc/pressure/io')),
                host_memory=pressure(Path('/proc/pressure/memory')),
                db_io=pressure(cg / 'io.pressure'), db_memory=pressure(cg / 'memory.pressure'),
                monotonic=time.monotonic())


def check_resources(sample, previous, reserve, *, admission=False, estimate=0):
    margin = 5 * GIB if admission else 4 * GIB
    require(sample['free_bytes'] >= reserve + margin + (estimate if admission else 0), 'disk reserve at risk')
    require(sample['available_bytes'] >= (3 if admission else 1.5) * GIB, 'available memory at risk')
    require(sample['commit_headroom'] >= (3 if admission else 1) * GIB, 'commit headroom at risk')
    require(sample['db_headroom'] >= (1.5 if admission else 0.5) * GIB, 'database memory limit at risk')
    require(sample['swap_bytes'] <= GIB / 4, 'swap usage at risk')
    for field, part, limit in [('host_io', 'some', 20), ('host_io', 'full', 20),
            ('host_memory', 'full', 1), ('db_io', 'some', 20),
            ('db_memory', 'some', 10), ('db_memory', 'full', 1)]:
        require(sample[field][part] <= limit, field + ' pressure at risk')
    if previous:
        seconds = sample['monotonic'] - previous['monotonic']
        require(0 < seconds <= 30, 'resource monitor delayed')
        delta = sample['swap_out_bytes'] - previous['swap_out_bytes']
        require(0 <= delta <= seconds * 4 * 1024 ** 2, 'swap-out rate at risk')


class Guard:
    def __init__(self, policy, partial, app, estimate, exercise=None):
        self.policy, self.partial, self.app = policy, partial, app
        self.estimate, self.exercise = estimate, exercise
        self.run = policy.state / app
        self.record = self.run / 'ownership.json'
        self.result = self.run / 'result.json'
        self.heartbeat = self.run / 'heartbeat.json'
        self.cancel = self.run / 'cancel.json'
        self.owner_pid = os.getpid()
        self.owner_identity = proc_identity(self.owner_pid)

    def inspect(self, target):
        # Absence must be proven by successful inventory, not a failed inspect
        # (daemon/network errors are not evidence that a container is gone).
        ids = invoke(['docker', 'ps', '-aq', '--no-trunc', '--filter', 'name=^/' + self.app + '$'], timeout=5).split()
        if not ids:
            return None
        require(len(ids) == 1, 'ambiguous dump container')
        data = json.loads(invoke(['docker', 'inspect', ids[0]], timeout=5))[0]
        require(data['Config']['Labels'].get(LABEL) == self.app and
                data['Name'] == '/' + self.app, 'dump ownership mismatch')
        require(not target or data['Id'] == target, 'dump container was replaced')
        return data

    def database(self, expected=None):
        data = json.loads(invoke(['docker', 'inspect', self.policy.container], timeout=5))[0]
        require(data['State']['Running'], 'database container not running')
        generation = dict(id=data['Id'], pid=data['State']['Pid'],
                          started_at=data['State']['StartedAt'], restarts=data['RestartCount'])
        require(expected is None or expected == generation, 'database generation changed')
        return data, generation

    def query(self, sql, owned):
        self.database(owned['database'])
        env = dict(self.policy.db_env(), PGOPTIONS='-c statement_timeout=5000 -c lock_timeout=1000')
        return invoke(['docker', 'exec', '-e', 'PGPASSWORD', '-e', 'PGOPTIONS', owned['database']['id'],
                       'psql', '-XAtw', '-U', self.policy.user, '-d', self.policy.database, '-c', sql],
                      env=env, timeout=8).strip()

    def backends(self, owned):
        # app is a generated UUID name, user/database were validated by Policy.
        sql = ("SELECT coalesce(json_agg(json_build_object('pid',pid,'started',backend_start::text)), '[]') "
               "FROM pg_stat_activity WHERE backend_type='client backend' AND application_name='" + self.app +
               "' AND usename='" + self.policy.user + "' AND datname='" + self.policy.database + "'")
        found = json.loads(self.query(sql, owned))
        require(len(found) <= 1, 'multiple dump backends')
        if found:
            if owned.get('backend'):
                require(found[0] == owned['backend'], 'dump backend identity changed')
            else:
                owned['backend'] = found[0]
                atomic_json(self.record, owned)
        return found

    def stop(self, owned):
        data = self.inspect(owned.get('container_id'))
        if data and data['State']['Running']:
            # Exact client container only; production PostgreSQL is never stopped.
            invoke(['docker', 'stop', '--time', '5', data['Id']], timeout=12)
        data = self.inspect(owned.get('container_id'))
        require(data is None or not data['State']['Running'], 'dump client still running')
        for action in ('pg_cancel_backend', 'pg_terminate_backend'):
            found = self.backends(owned)
            if not found:
                break
            backend = found[0]
            require(isinstance(backend['pid'], int) and backend['pid'] > 0, 'invalid backend pid')
            started = backend['started'].replace("'", "''")
            self.query(f"SELECT {action}(pid) FROM pg_stat_activity WHERE pid={backend['pid']} "
                       f"AND backend_start='{started}'::timestamptz AND application_name='{self.app}' "
                       f"AND usename='{self.policy.user}' AND datname='{self.policy.database}'", owned)
            time.sleep(0.2)
        require(not self.backends(owned), 'dump backend still running')
        if data:
            invoke(['docker', 'rm', '-v', data['Id']], timeout=8)
        require(self.inspect(owned.get('container_id')) is None, 'owned container cleanup unproven')

    def cleanup_partial(self, owned):
        current = identity(self.partial)
        require(all(current[k] == owned['partial'][k] for k in ('device', 'inode')), 'partial file replaced')
        marker = self.policy.state / 'pending.json'
        pending = read_json(marker)
        require(pending.get('app_name') == self.app and pending.get('partial') == self.partial.name
                and pending.get('phase') == 'dump', 'pending operation changed')
        self.partial.unlink()
        sync_dir(self.policy.root)
        marker.unlink()
        sync_dir(self.policy.state)

    def prepare(self):
        owned = dict(app=self.app, partial=identity(self.partial), created_at=now(),
                     owner_pid=self.owner_pid, owner_identity=self.owner_identity,
                     creation_started=False, creation_complete=False)
        atomic_json(self.record, owned)
        require(os.environ.get('DOCKER_HOST', '') in ('', 'unix:///var/run/docker.sock') and
                not os.environ.get('DOCKER_CONTEXT'), 'local default Docker endpoint required')
        endpoint = invoke(['docker', 'context', 'inspect', '--format', '{{.Endpoints.docker.Host}}'], timeout=5).strip()
        require(endpoint == 'unix:///var/run/docker.sock', 'local Linux Docker socket required')
        db, generation = self.database()
        # The installed pg_hba rejects TCP loopback. Reuse the DB's existing
        # Docker address from its own network namespace; no HBA change needed.
        addresses = {n['IPAddress'] for n in db['NetworkSettings']['Networks'].values() if n.get('IPAddress')}
        require(len(addresses) == 1, 'one unambiguous database Docker address required')
        address = addresses.pop()
        parsed = ipaddress.ip_address(address)
        require(parsed.version == 4 and parsed.is_private and not parsed.is_loopback, 'invalid database Docker address')
        sample = resources(self.policy.root, generation['pid'])
        check_resources(sample, None, self.policy.reserve, admission=True, estimate=self.estimate)
        owned.update(database=generation, file_limit=sample['free_bytes'] - self.policy.reserve - 4 * GIB)
        limit = str(owned['file_limit'])
        command = ['docker', 'create', '--name', self.app, '--label', LABEL + '=' + self.app,
                   '--pull=never', '--network', 'container:' + generation['id'], '--restart=no',
                   '--read-only', '--cap-drop=ALL', '--security-opt=no-new-privileges',
                   '--memory=1g', '--memory-swap=1g', '--cpus=1', '--pids-limit=32',
                   '--ulimit', 'fsize=' + limit + ':' + limit,
                   '--mount', 'type=bind,src=' + str(self.partial) + ',dst=/backup.dump',
                   '-e', 'PGPASSWORD', '-e', 'PGAPPNAME', '-e', 'PGOPTIONS', '-e', 'PGCONNECT_TIMEOUT',
                   '--entrypoint', 'pg_dump']
        # Mask image-declared data volumes with tiny tmpfs mounts: no live data
        # volume and no orphaned anonymous PostgreSQL data volume.
        for volume in (db['Config'].get('Volumes') or {}):
            require(volume.startswith('/var/lib/postgresql') and ':' not in volume, 'unexpected image volume')
            command += ['--tmpfs', volume + ':rw,noexec,nosuid,size=1m']
        command += [db['Image'], '-h', address, '-U', self.policy.user,
                    '--no-password', '-Fc', '-f', '/backup.dump', self.policy.database]
        env = dict(self.policy.db_env(), PGAPPNAME=self.app, PGCONNECT_TIMEOUT='3',
                   PGOPTIONS='-c statement_timeout=2700000 -c lock_timeout=60000')
        owned['creation_started'] = True
        atomic_json(self.record, owned)  # Durable intent before Docker create.
        cid = invoke(command, env=env, timeout=15).strip()
        require(len(cid) == 64 and all(c in '0123456789abcdef' for c in cid), 'invalid dump container id')
        owned['container_id'] = cid
        owned['creation_complete'] = True
        atomic_json(self.record, owned)
        return owned

    def fail(self, reason):
        result = dict(status='failed', at=now(), error=reason, cleanup_verified=False)
        try:
            if self.record.exists():
                owned = read_json(self.record)
                if owned['creation_started']:
                    self.stop(owned)
                    require(owned['creation_complete'], 'Docker create outcome ambiguous; preserve reconciliation marker')
                self.cleanup_partial(owned)
                result['cleanup_verified'] = True
        except (OSError, ValueError, KeyError, TypeError, Refused, subprocess.TimeoutExpired) as exc:
            result['cleanup_error'] = str(exc)
        atomic_json(self.result, result)
        return result

    def supervise(self):
        try:
            owned = self.prepare()
            started = time.monotonic()
            atomic_json(self.heartbeat, dict(at=now(), phase='starting'))
            invoke(['docker', 'start', owned['container_id']], timeout=10)
            previous = None
            while True:
                require(proc_identity(self.owner_pid) == self.owner_identity, 'backup owner disappeared')
                require(not self.cancel.exists(), 'backup cancelled')
                data = self.inspect(owned['container_id'])
                require(data is not None, 'owned dump container disappeared')
                if data['State']['Status'] == 'exited':
                    require(data['State']['ExitCode'] == 0 and not data['State']['OOMKilled'], 'dump failed or reached its size limit')
                    self.stop(owned)
                    atomic_json(self.result, dict(status='completed', at=now(), cleanup_verified=True))
                    return
                require(data['State']['Running'], 'dump client not running')
                elapsed = time.monotonic() - started
                require(elapsed <= MAX_DUMP_SECONDS, 'dump exceeded 45 minutes')
                require(self.exercise is None or elapsed < self.exercise, 'controlled stop exercise')
                self.database(owned['database'])
                sample = resources(self.policy.root, owned['database']['pid'])
                check_resources(sample, previous, self.policy.reserve)
                self.backends(owned)
                atomic_json(self.heartbeat, dict(at=now(), phase='dump', resources=sample))
                previous = sample
                time.sleep(INTERVAL)
        except (OSError, ValueError, KeyError, TypeError, Refused, subprocess.TimeoutExpired) as exc:
            self.fail(str(exc))


def protect_dump(policy, partial, app, estimate, exercise=None):
    require(policy.lock_fd is not None, 'dump requires the common lock')
    guard = Guard(policy, partial, app, estimate, exercise)
    guard.run.mkdir(mode=0o700)
    cancelled = None

    def cancel(signum, frame):
        nonlocal cancelled
        cancelled = signum

    signals = (signal.SIGTERM, signal.SIGINT, signal.SIGHUP)
    old = {sig: signal.signal(sig, cancel) for sig in signals}
    child = None
    last_heartbeat = None
    try:
        child = os.fork()
        if child == 0:
            os.setsid()
            # SSH/stdout termination cannot interrupt the watchdog. All evidence
            # is atomic on disk. It keeps the inherited flock until exit.
            for sig in signals:
                signal.signal(sig, signal.SIG_IGN)
            fd = os.open(os.devnull, os.O_RDWR)
            for target in (0, 1, 2):
                os.dup2(fd, target)
            os.close(fd)
            try:
                guard.supervise()
            finally:
                os._exit(0)
        deadline = time.monotonic() + 45
        while True:
            if cancelled is not None and not guard.cancel.exists():
                atomic_json(guard.cancel, dict(at=now(), signal=cancelled))
            pid, status = os.waitpid(child, os.WNOHANG)
            if pid:
                child = None
                if not guard.result.exists():
                    guard.fail('dump watchdog exited without a result')
                break
            if guard.heartbeat.exists():
                # Wall clock file age is avoided here; the contents are written
                # by our child and read once per second.
                changed = guard.heartbeat.stat().st_mtime_ns
                if changed != last_heartbeat:
                    last_heartbeat = changed
                    deadline = time.monotonic() + 45
            if time.monotonic() > deadline:
                os.kill(child, signal.SIGKILL)
                os.waitpid(child, 0)
                child = None
                guard.fail('dump watchdog heartbeat expired')
                break
            time.sleep(0.2)
        result = read_json(guard.result)
        require(cancelled is None and result['status'] == 'completed', result.get('error', 'backup cancelled'))
        return result
    finally:
        if child:
            # Unexpected caller failure: ask the surviving watchdog to stop.
            # It retains the lock, so returning does not admit a second writer.
            atomic_json(guard.cancel, dict(at=now(), reason='caller interrupted'))
        for sig, handler in old.items():
            signal.signal(sig, handler)
