"""Linux-only backup supervisors. No remote Docker endpoints or production restarts.

The detached supervisor inherits the common flock. Its caller watches it in
turn. Both persist evidence; neither equates a dead Docker CLI with stopped work.
"""
import json
import ipaddress
import math
import os
import re
from pathlib import Path
import signal
import subprocess
import time

from pg_backup_state import (Refused, require, now, identity, read_json,
                             atomic_json, sync_dir, invoke, sha256)

GIB = 1024 ** 3
INTERVAL = 5
MAX_DUMP_SECONDS = 45 * 60
WATCHDOG_TIMEOUT = 45
CLEANUP_TIMEOUT = 120
LABEL = 'dev.143.backup-run'
TRANSFER_CONFIG = b'''[default]
s3 =
    preferred_transfer_client = classic
    max_concurrent_requests = 2
    max_bandwidth = 100MB/s
    multipart_chunksize = 16MB
'''
SAMPLE_LIMIT = 240


def proc_identity(pid):
    # comm may contain spaces or parentheses. Field 22 follows the final ')'.
    fields = Path(f'/proc/{pid}/stat').read_text().rsplit(')', 1)[1].split()
    require(fields[0] != 'Z', 'owner process is a zombie')
    return [Path('/proc/sys/kernel/random/boot_id').read_text().strip(), fields[19]]


def pressure(path):
    rows = {}
    for line in path.read_text().splitlines():
        parts = line.split()
        require(parts and parts[0] in ('some', 'full') and parts[0] not in rows,
                'invalid PSI row')
        values = dict(p.split('=', 1) for p in parts[1:])
        value = float(values['avg10'])
        require(math.isfinite(value) and 0 <= value <= 100, 'invalid PSI telemetry')
        total = values.get('total', '')
        require(re.fullmatch(r'[0-9]+', total), 'invalid PSI total')
        rows[parts[0]] = value
        rows[parts[0] + '_total_us'] = int(total)
    require(set(rows) == {'some', 'full', 'some_total_us', 'full_total_us'}, 'incomplete PSI telemetry')
    return rows


def cgroup_path(pid):
    require(isinstance(pid, int) and pid > 0, 'invalid cgroup process id')
    groups = Path(f'/proc/{pid}/cgroup').read_text().splitlines()
    group = [line[3:] for line in groups if line.startswith('0::')]
    require(len(group) == 1, 'cgroup v2 required')
    base = Path('/sys/fs/cgroup')
    cg = base / group[0].lstrip('/')
    require(cg.resolve().is_relative_to(base) and cg != base, 'invalid process cgroup')
    return cg


def resources(root, db_pid):
    memory = {k: int(v.split()[0]) * 1024 for k, v in
              (line.split(':', 1) for line in Path('/proc/meminfo').read_text().splitlines())}
    vm = dict(line.split() for line in Path('/proc/vmstat').read_text().splitlines())
    cg = cgroup_path(db_pid)
    maximum = int((cg / 'memory.max').read_text())
    current = int((cg / 'memory.current').read_text())
    stats = dict(line.split() for line in (cg / 'memory.stat').read_text().splitlines())
    fs = os.statvfs(root)
    return dict(free_bytes=fs.f_bavail * fs.f_frsize,
                available_bytes=memory['MemAvailable'],
                commit_headroom=memory['CommitLimit'] - memory['Committed_AS'],
                swap_bytes=memory['SwapTotal'] - memory['SwapFree'],
                swap_out_bytes=int(vm['pswpout']) * os.sysconf('SC_PAGE_SIZE'),
                db_headroom=maximum - current,
                db_current_bytes=current, db_anon_bytes=int(stats['anon']),
                db_shmem_bytes=int(stats['shmem']),
                db_swap_bytes=int((cg / 'memory.swap.current').read_text()),
                host_io=pressure(Path('/proc/pressure/io')),
                host_memory=pressure(Path('/proc/pressure/memory')),
                db_io=pressure(cg / 'io.pressure'), db_memory=pressure(cg / 'memory.pressure'),
                monotonic=time.monotonic())


def check_resources(sample, previous, reserve, *, admission=False, estimate=0, capacity_only=False,
                    host_memory_full_percent=1):
    require(type(host_memory_full_percent) is int and host_memory_full_percent in (1, 5),
            'unsupported host memory pressure limit')
    # The attended canary override permits moderate stalls during work, never
    # admission into an already pressured host or weaker actual-memory limits.
    host_memory_limit = 1 if admission else host_memory_full_percent
    margin = 5 * GIB if admission else 4 * GIB
    require(sample['free_bytes'] >= reserve + margin + (estimate if admission else 0), 'disk reserve at risk')
    require(sample['available_bytes'] >= (3 if admission else 1.5) * GIB, 'available memory at risk')
    require(sample['commit_headroom'] >= (3 if admission else 1) * GIB, 'commit headroom at risk')
    require(sample['db_headroom'] >= (1.5 if admission else 0.5) * GIB, 'database memory limit at risk')
    if capacity_only:
        return  # A tiny confirming listing may proceed despite residual swap/PSI.
    require(sample['swap_bytes'] <= GIB / 4, 'swap usage at risk')
    for field, part, limit in [('host_io', 'some', 20), ('host_io', 'full', 20),
            ('host_memory', 'full', host_memory_limit), ('db_io', 'some', 20),
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
        self.host_memory_full_percent = getattr(policy, 'host_memory_full_percent', 1)
        self.run = policy.state / app
        self.record = self.run / 'ownership.json'
        self.result = self.run / 'result.json'
        self.heartbeat = self.run / 'heartbeat.json'
        self.cancel = self.run / 'cancel.json'
        self.owner_pid = os.getpid()
        self.owner_identity = proc_identity(self.owner_pid)
        self.cleanup_telemetry_error = None
        self.client_memory = None
        self.client_name = app
        self.client_limit = 2 * GIB
        self.samples = []
        self.last_dump_sample = None
        self.violating_sample = None

    def inspect(self, target):
        # Absence must be proven by successful inventory, not a failed inspect
        # (daemon/network errors are not evidence that a container is gone).
        listing = invoke(['docker', 'container', 'ls', '-a', '--no-trunc',
                          '--format', '{{json .Names}}\t{{.ID}}'], timeout=5)
        ids = []
        for line in listing.splitlines():
            name_json, cid = line.split('\t')
            name = json.loads(name_json)
            require(isinstance(name, str), 'invalid container name listing')
            # Docker's CLI presents names without '/', inspect uses '/'.
            # Accept either literal form; regex filters never prove absence.
            if name in (self.client_name, '/' + self.client_name) or cid == target:
                require(len(cid) == 64 and all(c in '0123456789abcdef' for c in cid), 'invalid listed container id')
                ids.append(cid)
        if not ids:
            return None
        require(len(ids) == 1, 'ambiguous dump container')
        data = json.loads(invoke(['docker', 'inspect', ids[0]], timeout=5))[0]
        require(data['Config']['Labels'].get(LABEL) == self.app and
                data['Name'] == '/' + self.client_name, 'dump ownership mismatch')
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
        self.cleanup_beat('inspect')
        data = self.inspect(owned.get('container_id'))
        self.cleanup_beat('stop_client')
        if data and data['State']['Running']:
            # Exact client container only; production PostgreSQL is never stopped.
            invoke(['docker', 'stop', '--time', '5', data['Id']], timeout=12)
        self.cleanup_beat('confirm_client_stopped')
        data = self.inspect(owned.get('container_id'))
        require(data is None or not data['State']['Running'], 'dump client still running')
        for action in ('pg_cancel_backend', 'pg_terminate_backend'):
            self.cleanup_beat(action)
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
        self.cleanup_beat('confirm_backend_stopped')
        require(not self.backends(owned), 'dump backend still running')
        self.cleanup_beat('remove_container')
        if data:
            invoke(['docker', 'rm', '-v', data['Id']], timeout=8)
        self.cleanup_beat('confirm_container_removed')
        require(self.inspect(owned.get('container_id')) is None, 'owned container cleanup unproven')

    def cleanup_beat(self, stage, phase='cleanup'):
        # Lack of disk space for telemetry must not prevent stopping the writer.
        # Preserve the error in the terminal evidence if writes become possible.
        try:
            if self.heartbeat.exists() and (self.client_memory is None or self.last_dump_sample is None):
                heartbeat = read_json(self.heartbeat)
                if self.client_memory is None:
                    self.client_memory = heartbeat.get('client_memory')
                # The caller can take over after a watchdog dies. Recover its
                # last observation before replacing the heartbeat with cleanup.
                if self.last_dump_sample is None:
                    self.last_dump_sample = heartbeat.get('last_dump_sample')
                    if heartbeat.get('phase') in ('admission', 'dump') and 'resources' in heartbeat:
                        self.last_dump_sample = heartbeat
                if self.violating_sample is None:
                    self.violating_sample = heartbeat.get('violating_sample')
            atomic_json(self.heartbeat, dict(at=now(), phase=phase, stage=stage,
                                            client_memory=self.client_memory, **self.dump_evidence()))
        except (OSError, ValueError, KeyError, TypeError, Refused) as exc:
            self.cleanup_telemetry_error = str(exc)

    def observe_client_memory(self, data):
        cg = cgroup_path(data['State']['Pid'])
        require(data['Id'] in cg.name, 'dump client cgroup identity mismatch')
        current = int((cg / 'memory.current').read_text())
        peak = int((cg / 'memory.peak').read_text())
        maximum = int((cg / 'memory.max').read_text())
        stats = dict(line.split() for line in (cg / 'memory.stat').read_text().splitlines())
        require(0 <= current <= peak and maximum == self.client_limit, 'invalid backup client memory telemetry')
        self.client_memory = dict(current_bytes=current, peak_observed_bytes=peak,
                                  anon_bytes=int(stats['anon']), file_bytes=int(stats['file']),
                                  swap_bytes=int((cg / 'memory.swap.current').read_text()),
                                  limit_bytes=maximum, observed_at=now())

    def terminal(self, result):
        atomic_json(self.result, dict(result, client_memory=self.client_memory,
                                      cleanup_telemetry_error=self.cleanup_telemetry_error,
                                      **self.dump_evidence()))

    def dump_evidence(self):
        return {key: value for key, value in (
            ('last_dump_sample', self.last_dump_sample), ('violating_sample', self.violating_sample)
        ) if value is not None}

    def observe_dump_resources(self, sample, previous, database, *, admission=False):
        observation = dict(at=now(), phase='admission' if admission else 'dump',
                           resources=sample, client_memory=self.client_memory,
                           database=database, boot_id=self.owner_identity[0],
                           host_memory_full_limit_percent=1 if admission else self.host_memory_full_percent)
        # Keep the triggering sample even if enforcement or a later backend
        # query fails. In-memory evidence also survives a failed history write.
        self.last_dump_sample = observation
        atomic_json(self.heartbeat, observation)
        self.samples.append(observation)
        self.samples = self.samples[-SAMPLE_LIMIT:]
        atomic_json(self.run / 'samples.json', self.samples)
        try:
            check_resources(sample, previous, self.policy.reserve, admission=admission,
                            estimate=self.estimate if admission else 0,
                            host_memory_full_percent=self.host_memory_full_percent)
        except Refused:
            self.violating_sample = observation
            raise

    def cleanup_partial(self, owned):
        self.cleanup_beat('remove_partial')
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
        self.observe_dump_resources(sample, None, generation, admission=True)
        owned.update(database=generation, file_limit=sample['free_bytes'] - self.policy.reserve - 4 * GIB)
        limit = str(owned['file_limit'])
        command = ['docker', 'create', '--name', self.app, '--label', LABEL + '=' + self.app,
                   '--pull=never', '--network', 'container:' + generation['id'], '--restart=no',
                   '--read-only', '--cap-drop=ALL', '--security-opt=no-new-privileges',
                   '--memory=2g', '--memory-swap=2g', '--cpus=1', '--pids-limit=32',
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
        self.cleanup_beat('begin_failure_cleanup')
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
        self.terminal(result)
        return result

    def supervise(self):
        try:
            owned = self.prepare()
            started = time.monotonic()
            atomic_json(self.heartbeat, dict(at=now(), phase='starting', **self.dump_evidence()))
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
                    self.terminal(dict(status='completed', at=now(), cleanup_verified=True))
                    return
                require(data['State']['Running'], 'dump client not running')
                elapsed = time.monotonic() - started
                require(elapsed <= MAX_DUMP_SECONDS, 'dump exceeded 45 minutes')
                require(self.exercise is None or elapsed < self.exercise, 'controlled stop exercise')
                self.database(owned['database'])
                try:
                    self.observe_client_memory(data)
                except FileNotFoundError:
                    # A successful exit may remove /proc and its cgroup between
                    # inspect and sampling. Only a fresh exact-ID exit permits
                    # retrying the terminal path; other telemetry loss fails.
                    latest = self.inspect(owned['container_id'])
                    if latest and latest['State']['Status'] == 'exited':
                        continue
                    raise
                sample = resources(self.policy.root, owned['database']['pid'])
                self.observe_dump_resources(sample, previous, owned['database'])
                self.backends(owned)
                previous = sample
                time.sleep(INTERVAL)
        except (OSError, ValueError, KeyError, TypeError, Refused, subprocess.TimeoutExpired) as exc:
            self.fail(str(exc))


def protect_dump(policy, partial, app, estimate, exercise=None):
    return run_guard(Guard(policy, partial, app, estimate, exercise))


class TransferGuard(Guard):
    """Guard a completed dump through verification and upload; never delete it.

    Hashing runs in the supervised process with drop-behind reads. Every Docker
    reader is bounded and durably owned before creation/start, including the
    final S3 listing. An uncertain upload keeps its pending marker for a human
    to reconcile; LIST size alone cannot establish checksum upload success.
    """
    def __init__(self, policy, partial, app, timeline, measured_db, version):
        super().__init__(policy, partial, app, 0)
        self.run = self.run / 'postdump'
        self.record = self.run / 'ownership.json'
        self.result = self.run / 'result.json'
        self.heartbeat = self.run / 'heartbeat.json'
        self.cancel = self.run / 'cancel.json'
        self.timeline, self.measured_db, self.version = timeline, measured_db, version
        self.client_limit = GIB
        self.previous = None
        self.phase = 'verification'
        self.phase_deadline = time.monotonic() + policy.verify_timeout
        self.phase_resources = {}
        self.samples = []

    def observe_client_memory(self, data):
        super().observe_client_memory(data)
        cg = cgroup_path(data['State']['Pid'])
        require(data['Id'] in cg.name, 'reader cgroup identity mismatch')
        events = {k: int(v) for k, v in (line.split() for line in (cg / 'memory.events').read_text().splitlines())}
        require({'low', 'high', 'max', 'oom', 'oom_kill'} <= events.keys() and
                all(value >= 0 for value in events.values()), 'invalid reader memory events')
        self.client_memory.update(memory_pressure=pressure(cg / 'memory.pressure'),
                                  io_pressure=pressure(cg / 'io.pressure'), events=events)

    def transfer_config(self):
        # Select classic explicitly: CRT ignores the concurrency/bandwidth
        # settings. This per-attempt file contains no credentials.
        path = self.run / 'aws-config'
        if path.exists():
            require(identity(path)['bytes'] == len(TRANSFER_CONFIG) and path.read_bytes() == TRANSFER_CONFIG,
                    'transfer configuration changed')
        else:
            fd = os.open(path, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600)
            with os.fdopen(fd, 'wb') as output:
                output.write(TRANSFER_CONFIG)
                output.flush()
                os.fsync(output.fileno())
            sync_dir(self.run)
        return path

    def prepare(self):
        dump = read_json(self.run.parent / 'ownership.json')
        result = read_json(self.run.parent / 'result.json')
        require(result['status'] == 'completed' and result['cleanup_verified'], 'dump cleanup unproven')
        require(dump['app'] == self.app and all(identity(self.partial)[k] == dump['partial'][k]
                for k in ('device', 'inode')), 'completed dump identity changed')
        require(os.environ.get('DOCKER_HOST', '') in ('', 'unix:///var/run/docker.sock') and
                not os.environ.get('DOCKER_CONTEXT'), 'local default Docker endpoint required')
        endpoint = invoke(['docker', 'context', 'inspect', '--format', '{{.Endpoints.docker.Host}}'], timeout=5).strip()
        require(endpoint == 'unix:///var/run/docker.sock', 'local Linux Docker socket required')
        self.db, generation = self.database(dump['database'])
        self.owned = dict(app=self.app, database=generation, partial=identity(self.partial),
                          owner_pid=self.owner_pid, owner_identity=self.owner_identity,
                          creation_started=False, creation_complete=False)
        atomic_json(self.record, self.owned)
        self.checkpoint('verification_started')

    def tick(self, force=False, *, enforce=True, reset_interval=False):
        require(proc_identity(self.owner_pid) == self.owner_identity, 'backup owner disappeared')
        require(not self.cancel.exists(), 'backup cancelled')
        require(time.monotonic() <= self.phase_deadline, 'backup ' + self.phase + ' timed out')
        if not force and self.previous and time.monotonic() - self.previous['monotonic'] < INTERVAL:
            return
        self.database(self.owned['database'])
        sample = resources(self.policy.root, self.owned['database']['pid'])
        # Persist the violating sample too, so a stop is diagnosable.
        limit = self.host_memory_full_percent if enforce and self.phase != 'metadata' else None
        observation = dict(at=now(), phase=self.phase, resources=sample, client_memory=self.client_memory,
                           host_memory_full_limit_percent=limit)
        atomic_json(self.heartbeat, observation)
        # Bound the history to twenty minutes at the normal cadence. Retain the
        # threshold-crossing sample before raising, not just cleanup telemetry.
        self.samples.append(observation)
        self.samples = self.samples[-SAMPLE_LIMIT:]
        atomic_json(self.run / 'samples.json', self.samples)
        self.phase_resources['last_observation'] = dict(at=observation['at'], phase=self.phase, resources=sample,
                                                       host_memory_full_limit_percent=limit)
        if enforce:
            check_resources(sample, None if reset_interval else self.previous, self.policy.reserve,
                            capacity_only=self.phase == 'metadata',
                            host_memory_full_percent=self.host_memory_full_percent)
        self.previous = sample

    def checkpoint(self, name, *, enforce=True):
        self.tick(force=True, enforce=enforce)
        self.phase_resources[name] = dict(at=now(), resources=self.previous)
        self.timeline['resources'] = self.phase_resources

    def stop(self, owned):
        require(owned['app'] == self.app and owned['client_name'] in
                [self.app + '-' + stage for stage in ('structural', 'upload', 'metadata')],
                'transfer ownership mismatch')
        self.client_name = owned['client_name']
        self.cleanup_beat('inspect_reader', phase=self.phase)
        data = self.inspect(owned.get('container_id'))
        if data and data['State']['Running']:
            self.cleanup_beat('stop_reader', phase=self.phase)
            invoke(['docker', 'stop', '--time', '5', data['Id']], timeout=12)
        self.cleanup_beat('confirm_reader_stopped', phase=self.phase)
        data = self.inspect(owned.get('container_id'))
        require(data is None or not data['State']['Running'], 'backup reader still running')
        if data:
            self.cleanup_beat('remove_reader', phase=self.phase)
            invoke(['docker', 'rm', '-v', data['Id']], timeout=8)
        self.cleanup_beat('confirm_reader_removed', phase=self.phase)
        require(self.inspect(owned.get('container_id')) is None, 'owned reader cleanup unproven')

    def client(self, stage, args, *, env=None, network='none', capture=False, timeout=120):
        self.phase = stage
        self.phase_deadline = time.monotonic() + timeout
        self.client_memory = None
        # No reader is active during the preceding stage's cleanup. Rebase the
        # sample interval, but enforce every entry resource threshold.
        self.tick(force=True, reset_interval=True)
        self.client_name = self.app + '-' + stage
        self.owned.update(client_name=self.client_name, creation_started=True, creation_complete=False)
        self.owned.pop('container_id', None)
        self.owned.pop('exit', None)
        atomic_json(self.record, self.owned)
        command = ['docker', 'create', '--name', self.client_name, '--label', LABEL + '=' + self.app,
                   '--pull=never', '--network', network, '--restart=no', '--read-only',
                   '--cap-drop=ALL', '--security-opt=no-new-privileges', '--memory=1g',
                   '--memory-swap=1g', '--cpus=1', '--pids-limit=64',
                   '--tmpfs', '/tmp:rw,noexec,nosuid,size=64m']
        # Only the bounded S3 metadata JSON needs stdout. Dump TOCs and uploader
        # diagnostics are not retained in Docker's disk-backed log stream.
        command += (['--log-driver=json-file', '--log-opt=max-size=1m', '--log-opt=max-file=1']
                    if capture else ['--log-driver=none'])
        cid = invoke(command + args, env=env, timeout=15).strip()
        require(len(cid) == 64 and all(c in '0123456789abcdef' for c in cid), 'invalid backup reader id')
        self.owned.update(container_id=cid, creation_complete=True)
        atomic_json(self.record, self.owned)
        # Docker create is a bounded command window with no active reader.
        # Recheck capacity immediately before start, independently of its lag.
        self.tick(force=True, reset_interval=True)
        invoke(['docker', 'start', cid], timeout=10)
        while True:
            data = self.inspect(cid)
            require(data is not None, 'owned backup reader disappeared')
            if data['State']['Status'] == 'exited':
                # Save the exact terminal status BEFORE removal or receipt
                # creation. A lost acknowledgement must never be auto-retried.
                self.owned['exit'] = dict(code=data['State']['ExitCode'], oom=data['State']['OOMKilled'], at=now())
                atomic_json(self.record, self.owned)
                atomic_json(self.run / (stage + '.json'), self.owned)
                require(data['State']['ExitCode'] == 0 and not data['State']['OOMKilled'], stage + ' client failed')
                # A successful exit is a known outcome. Residual pressure is
                # recorded, not mistaken for an uncertain/failed upload.
                self.tick(force=True, enforce=False)
                output = invoke(['docker', 'logs', '--tail', '100', cid], timeout=5) if capture else ''
                self.stop(self.owned)
                self.owned['creation_started'] = False
                atomic_json(self.record, self.owned)
                return output
            require(data['State']['Running'], 'backup reader not running')
            try:
                self.observe_client_memory(data)
            except FileNotFoundError:
                latest = self.inspect(cid)
                if latest and latest['State']['Status'] == 'exited':
                    continue
                raise
            self.tick()
            time.sleep(INTERVAL)

    def structural_check(self, path):
        args = ['--mount', 'type=bind,src=' + str(path) + ',dst=/backup.dump,readonly',
                '--entrypoint', 'pg_restore']
        for volume in (self.db['Config'].get('Volumes') or {}):
            require(volume.startswith('/var/lib/postgresql') and ':' not in volume, 'unexpected image volume')
            args += ['--tmpfs', volume + ':rw,noexec,nosuid,size=1m']
        self.client('structural', args + [self.db['Image'], '--list', '/backup.dump'],
                    timeout=self.policy.verify_timeout)

    def checksum(self, path):
        self.phase = 'checksum'
        self.phase_deadline = time.monotonic() + self.policy.verify_timeout
        self.checkpoint('checksum_started')
        checksum = sha256(path, tick=self.tick, drop_cache=True)
        self.checkpoint('checksum_completed', enforce=False)
        return checksum

    def aws(self, args, archive=None, timeout=120):
        spec, env = self.policy.aws_spec(args, archive)
        if archive is not None:
            config = self.transfer_config()
            spec = ['--mount', 'type=bind,src=' + str(config) + ',dst=/aws-config,readonly',
                    '-e', 'AWS_CONFIG_FILE=/aws-config'] + spec
        return self.client('upload' if archive else 'metadata', spec, env=env,
                           network='bridge', capture=archive is None, timeout=timeout)

    def fail(self, reason):
        failed_phase = self.phase
        self.phase = 'cleanup'
        self.cleanup_beat('stop_reader')
        result = dict(status='failed', at=now(), error=reason, cleanup_verified=False,
                      archive_preserved=True, reconciliation_required=True,
                      phase_resources=self.phase_resources, failed_phase=failed_phase)
        try:
            if self.record.exists():
                owned = read_json(self.record)
                if owned['creation_started']:
                    self.stop(owned)
                    require(owned['creation_complete'], 'Docker create outcome ambiguous; reconcile before retry')
                result['cleanup_verified'] = True
        except (OSError, ValueError, KeyError, TypeError, Refused, subprocess.TimeoutExpired) as exc:
            result['cleanup_error'] = str(exc)
        # Never delete the archive, the verification partial or pending marker.
        # Even a matching LIST cannot prove a killed uploader finished checksum validation.
        self.terminal(result)
        return result

    def supervise(self):
        try:
            self.prepare()
            result = self.policy.finish_archive(self.partial, self.app, self.timeline,
                                                self.measured_db, self.version, self)
            self.terminal(dict(status='completed', at=now(), cleanup_verified=True,
                               phase_resources=self.phase_resources, **result))
        except (OSError, ValueError, KeyError, TypeError, Refused, subprocess.TimeoutExpired) as exc:
            self.fail(str(exc))


def protect_transfer(policy, partial, app, timeline, measured_db, version):
    return run_guard(TransferGuard(policy, partial, app, timeline, measured_db, version))


class RecoveryTransferGuard(TransferGuard):
    """Guard an upload retry or independent receipt reconciliation."""
    def __init__(self, policy, archive, app, pending, measured_db, receipt_args=None):
        # Keep the original dump times; a later upload never refreshes its RPO.
        super().__init__(policy, archive, app, dict(pending['timeline']), measured_db, '')
        self.pending = pending
        self.receipt_args = receipt_args

    def prepare(self):
        p = self.policy
        before = read_json(self.run.parent / 'pending-before.json')
        require(read_json(p.state / 'pending.json') == before, 'pending upload changed before recovery')
        old_app = self.pending['app_name']
        dump_app = self.pending.get('dump_app', old_app)
        require(re.fullmatch(r'143-backup-[a-f0-9]{32}', dump_app), 'invalid original dump identity')
        dump = read_json(p.state / dump_app / 'ownership.json')
        dump_result = read_json(p.state / dump_app / 'result.json')
        previous = p.state / old_app / 'postdump'
        owner, result = read_json(previous / 'ownership.json'), read_json(previous / 'result.json')
        require(dump['app'] == dump_app and dump_result['status'] == 'completed' and
                dump_result['cleanup_verified'] is True and
                all(dump['partial'][k] == self.pending['identity'][k] for k in ('device', 'inode')),
                'original dump completion or identity unproven')
        require(owner['app'] == old_app and result['status'] == 'failed' and result['cleanup_verified'] is True,
                'prior upload cleanup unproven')
        require(not owner.get('creation_started') or owner.get('creation_complete') is True,
                'prior reader creation outcome ambiguous')
        require(os.environ.get('DOCKER_HOST', '') in ('', 'unix:///var/run/docker.sock') and
                not os.environ.get('DOCKER_CONTEXT'), 'local default Docker endpoint required')
        endpoint = invoke(['docker', 'context', 'inspect', '--format', '{{.Endpoints.docker.Host}}'], timeout=5).strip()
        require(endpoint == 'unix:///var/run/docker.sock', 'local Linux Docker socket required')
        # Enumerate every container, including renamed/stopped readers. Neither
        # elapsed time nor an old cleanup receipt proves their current absence.
        listing = invoke(['docker', 'container', 'ls', '-a', '--no-trunc',
                          '--format', '{{json .ID}}\t{{json (.Label "' + LABEL + '")}}'], timeout=5)
        for line in listing.splitlines():
            cid_json, label_json = line.split('\t')
            cid, label = json.loads(cid_json), json.loads(label_json)
            require(isinstance(cid, str) and re.fullmatch(r'[a-f0-9]{64}', cid)
                    and isinstance(label, str), 'invalid reader inventory')
            require(label not in {old_app, dump_app}, 'prior owned reader still exists; reconcile before retry')
        self.db, generation = self.database()
        self.owned = dict(app=self.app, database=generation, partial=identity(self.partial),
                          owner_pid=self.owner_pid, owner_identity=self.owner_identity,
                          creation_started=False, creation_complete=False, resumed_from=old_app)
        require(self.owned['partial'] == self.pending['identity'], 'preserved archive changed')
        atomic_json(self.record, self.owned)
        self.checkpoint('resume_started')
        require(not json.loads(self.query(
            "SELECT coalesce(json_agg(pid), '[]') FROM pg_stat_activity WHERE application_name='" + dump_app + "'",
            self.owned)), 'original dump backend still exists')
        # A completed remote object needs reconciliation, even if its size
        # matches. This path cannot independently download it to prove content.
        old_key = self.pending.get('remote_key', 'postgres/' + self.partial.name)
        remote = p.remote(self.partial.name, aws=self.aws, key=old_key)
        if self.receipt_args is None:
            require(remote is None, 'prior object exists; reconcile instead of retrying')
            self.version = self.aws(['--version']).strip()
            require(self.version.split()[:1] == [p.aws_cli_version], 'unexpected AWS CLI version')
        else:
            require(remote and remote['etag'] == self.receipt_args.etag and
                    remote['last_modified'] == self.receipt_args.last_modified and
                    remote['bytes'] == self.pending['identity']['bytes'],
                    'independent object metadata does not match pending upload')
            self.verified_remote = remote
        self.structural_check(self.partial)
        require(self.checksum(self.partial) == self.pending['sha256'] and
                identity(self.partial) == self.pending['identity'], 'preserved archive checksum changed')
        require(read_json(p.state / 'pending.json') == before, 'pending upload changed during recovery')
        if self.receipt_args is not None:
            return  # Keep the failed-upload marker intact until the receipt is durable.
        self.timeline['upload_resumed_at'] = now()
        self.timeline['resumed_from'] = old_app
        if self.pending.get('database_size_evidence'):
            self.timeline['database_size_evidence'] = self.pending['database_size_evidence']
        # A new unique key prevents a delayed completion from the old uploader
        # or another retry from overwriting this attempt's object.
        pending = dict(self.pending, app_name=self.app, dump_app=dump_app,
                       remote_key='postgres/resumed/' + self.app.removeprefix('143-backup-') + '/' + self.partial.name,
                       database_bytes=self.measured_db, timeline=self.timeline)
        atomic_json(p.state / 'pending.json', pending)

    def publish_independent_receipt(self):
        p, args = self.policy, self.receipt_args
        require(p.remote(self.partial.name, aws=self.aws, key=args.key) == self.verified_remote,
                'remote object changed during independent verification')
        before = read_json(self.run.parent / 'pending-before.json')
        require(read_json(p.state / 'pending.json') == before, 'pending upload changed during import')
        self.timeline['independent_verification_recorded_at'] = now()
        if self.pending.get('database_size_evidence'):
            self.timeline['database_size_evidence'] = self.pending['database_size_evidence']
        p.record(self.partial, self.verified_remote, args.sha256,
                 dict(kind='operator_sha256', version_id=args.version_id, evidence=args.evidence, verified_at=now()),
                 self.measured_db, self.pending['identity'], self.timeline)
        (p.state / 'pending.json').unlink()
        sync_dir(p.state)
        return dict(file=self.partial.name, bytes=self.pending['identity']['bytes'], sha256=args.sha256)

    def supervise(self):
        try:
            self.prepare()
            if self.receipt_args is None:
                result = self.policy.upload_archive(self.partial, self.app, self.timeline,
                                                    self.measured_db, self.version, self)
            else:
                result = self.publish_independent_receipt()
            self.terminal(dict(status='completed', at=now(), cleanup_verified=True,
                               phase_resources=self.phase_resources, **result))
        except (OSError, ValueError, KeyError, TypeError, Refused, subprocess.TimeoutExpired) as exc:
            self.fail(str(exc))


def run_guard(guard):
    require(guard.policy.lock_fd is not None, 'backup requires the common lock')
    guard.run.mkdir(mode=0o700)
    cancelled = None

    def cancel(signum, frame):
        nonlocal cancelled
        cancelled = signum

    signals = (signal.SIGTERM, signal.SIGINT, signal.SIGHUP)
    old = {sig: signal.signal(sig, cancel) for sig in signals}
    child = None
    last_heartbeat = None
    cleanup_deadline = None
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
        deadline = time.monotonic() + WATCHDOG_TIMEOUT
        while True:
            if cancelled is not None and not guard.cancel.exists():
                atomic_json(guard.cancel, dict(at=now(), signal=cancelled))
                if cleanup_deadline is None:
                    cleanup_deadline = time.monotonic() + CLEANUP_TIMEOUT
            pid, status = os.waitpid(child, os.WNOHANG)
            if pid:
                child = None
                if not guard.result.exists():
                    guard.fail('backup watchdog exited without a result')
                break
            if guard.heartbeat.exists():
                # Wall clock file age is avoided here; the contents are written
                # by our child and read once per second.
                changed = guard.heartbeat.stat().st_mtime_ns
                if changed != last_heartbeat:
                    last_heartbeat = changed
                    heartbeat = read_json(guard.heartbeat)
                    if heartbeat['phase'] == 'cleanup' and cleanup_deadline is None:
                        cleanup_deadline = time.monotonic() + CLEANUP_TIMEOUT
                    deadline = time.monotonic() + WATCHDOG_TIMEOUT
            if time.monotonic() > (cleanup_deadline or deadline):
                os.kill(child, signal.SIGKILL)
                os.waitpid(child, 0)
                child = None
                # It may have finished after our last waitpid but before SIGKILL.
                # A durable terminal result must not be overwritten by a retry.
                if not guard.result.exists():
                    guard.fail('backup watchdog heartbeat expired')
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
