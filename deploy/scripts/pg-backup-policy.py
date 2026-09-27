#!/usr/bin/env python3
"""Attended PostgreSQL backup admission and receipt-qualified local retention.

Uses only Python's standard library. Never downloads/deletes remote objects or
sources shell configuration. An interrupted run leaves a reconciliation marker;
neither age nor a released client lock proves an in-container writer stopped.
"""
import argparse
from contextlib import contextmanager
import datetime as dt
import fcntl
import json
import math
import os
from pathlib import Path
import re
import signal
import subprocess
import sys
import time
import uuid

AWS_IMAGE = 'public.ecr.aws/aws-cli/aws-cli@sha256:749bfaf91d690b9a1768083822d620f96c19defdf9ca2dc227eb3695281fda5b'
AWS_VERSION = 'aws-cli/2.35.11'
GIB = 1024 ** 3


# Operator recovery helpers load this file via importlib from /root or stdin.
# Resolve the co-installed helpers without requiring callers to edit sys.path.
sys.path.insert(0, str(Path(__file__).resolve().parent))
from pg_backup_state import (Refused, require, now, emit, identity, read_json,
                             sync_dir, atomic_json, sha256, invoke)


def expected_cli_version(value):
    return isinstance(value, str) and value.split()[:1] == [AWS_VERSION]


def run_restore_reader(command, env, cleanup_timeout=120):
    """Forward cancellation once, allowing the shell's EXIT cleanup to finish.

    subprocess.run kills its child when a Python signal handler raises. Use a
    separate process group and non-raising handlers instead, retaining the lock
    until cleanup exits or its bounded grace period is exhausted.
    """
    child = None
    cancelled = None
    deadline = None

    def cancel(signum, frame):
        nonlocal cancelled
        if cancelled is None:
            cancelled = signum

    signals = (signal.SIGTERM, signal.SIGHUP, signal.SIGINT)
    previous = {sig: signal.signal(sig, cancel) for sig in signals}
    try:
        child = subprocess.Popen(command, env=env, stdin=subprocess.DEVNULL, start_new_session=True)
        while True:
            if cancelled is not None and deadline is None:
                deadline = time.monotonic() + cleanup_timeout
                try:
                    os.killpg(child.pid, signal.SIGTERM)
                except ProcessLookupError:
                    pass  # Already terminal; wait still reaps the exact child.
            try:
                status = child.wait(timeout=0.1)
                return 128 + cancelled if cancelled else (status if status >= 0 else 128 - status)
            except subprocess.TimeoutExpired:
                if deadline is not None and time.monotonic() >= deadline:
                    os.killpg(child.pid, signal.SIGKILL)
                    child.wait(timeout=10)
                    raise Refused('restore cleanup exceeded grace period; inspect pending operation and owned container')
    finally:
        # On an unexpected Python exception do not strand or immediately kill
        # the reader. Its cleanup may still establish a receipt for inspection.
        try:
            if child is not None and child.poll() is None:
                try:
                    os.killpg(child.pid, signal.SIGTERM)
                    child.wait(timeout=cleanup_timeout)
                except subprocess.TimeoutExpired:
                    os.killpg(child.pid, signal.SIGKILL)
                    child.wait(timeout=10)
                except ProcessLookupError:
                    child.wait(timeout=10)
        finally:
            for sig, handler in previous.items():
                signal.signal(sig, handler)


class Policy:
    def __init__(self):
        self.root = Path(os.environ.get('BACKUP_DIR', '/backups/postgres'))
        self.state = self.root / '.backup-state'
        self.container = os.environ.get('POSTGRES_CONTAINER', '143-postgres-1')
        self.user = os.environ.get('POSTGRES_USER', 'onefortythree')
        self.database = os.environ.get('POSTGRES_DB', 'onefortythree')
        require(re.fullmatch(r'[a-zA-Z0-9_]+', self.database), 'invalid database name')
        require(re.fullmatch(r'[a-zA-Z0-9_]+', self.user), 'invalid database user')
        self.reserve = int(os.environ.get('BACKUP_RESERVE_BYTES', str(20 * GIB)))
        self.wait = float(os.environ.get('BACKUP_LOCK_TIMEOUT_SECONDS', '60'))
        self.verify_timeout = int(os.environ.get('BACKUP_VERIFY_TIMEOUT_SECONDS', '7200'))
        require(self.reserve >= 20 * GIB, 'reserve cannot be below 20 GiB')
        require(math.isfinite(self.wait) and 0 <= self.wait <= 3600, 'invalid lock timeout')
        require(60 <= self.verify_timeout <= 86400, 'invalid verification timeout')
        self.storage = None
        self.lock_fd = None

    @contextmanager
    def locked(self):
        self.root.mkdir(mode=0o700, parents=True, exist_ok=True)
        require(self.root.resolve() == self.root.absolute(), 'backup path contains a symlink')
        s = self.root.stat()
        require(s.st_uid == os.geteuid() and not s.st_mode & 0o022, 'unsafe backup directory ownership/mode')
        fd = os.open(self.root / '.backup.lock', os.O_RDWR | os.O_CREAT | os.O_NOFOLLOW, 0o600)
        try:
            require(identity(self.root / '.backup.lock')['inode'] == os.fstat(fd).st_ino, 'lock path changed')
            deadline = time.monotonic() + self.wait
            while True:
                try:
                    fcntl.flock(fd, fcntl.LOCK_EX | fcntl.LOCK_NB)
                    break
                except BlockingIOError:
                    require(time.monotonic() < deadline, 'backup/restore lock timeout')
                    time.sleep(min(0.1, max(0, deadline - time.monotonic())))
            self.state.mkdir(mode=0o700, exist_ok=True)
            require(not self.state.is_symlink() and self.state.stat().st_uid == os.geteuid()
                    and not self.state.stat().st_mode & 0o077, 'unsafe state directory')
            self.lock_fd = fd
            yield
        finally:
            self.lock_fd = None
            os.close(fd)

    def no_pending(self):
        require(not (self.state / 'pending.json').exists(), 'incomplete operation: reconcile pending.json and its owned processes before retrying')
        require(not list(self.root.glob('.*.dump.partial.*')), 'unreconciled partial archive; preserve it until writer state is proven')

    def retire_attempts(self):
        """Bound the hot journal without deleting audit records or active runs."""
        require(self.lock_fd is not None, 'attempt retirement requires the common lock')
        records = [(path, read_json(path)) for path in self.state.glob('attempt-*.json')]
        records.sort(key=lambda item: item[1]['started_at'], reverse=True)
        keep = {path.name for path, _ in records[:200]}
        latest_actions = set()
        for path, record in records:
            if record['action'] not in latest_actions:
                keep.add(path.name)
                latest_actions.add(record['action'])
            if record['status'] not in ('completed', 'failed', 'interrupted'):
                keep.add(path.name)
        old = [path for path, _ in records if path.name not in keep]
        if not old:
            return
        retired = self.state / 'retired'
        for directory in (retired, retired / 'attempts'):
            directory.mkdir(mode=0o700, exist_ok=True)
            require(not directory.is_symlink() and directory.stat().st_uid == os.geteuid()
                    and not directory.stat().st_mode & 0o077, 'unsafe retired attempt directory')
        for path in old:
            destination = retired / 'attempts' / path.name
            require(not destination.exists(), 'attempt retirement collision')
            path.rename(destination)
        sync_dir(retired / 'attempts')
        sync_dir(self.state)

    def load_storage(self):
        if self.storage is None:
            p = Path(os.environ.get('BACKUP_STORAGE_CONFIG', '/opt/143/backup-storage.json'))
            require(p.exists(), 'missing JSON storage config; legacy shell sync config is not executed')
            data = read_json(p)
            expected = {'bucket', 'region', 'access_key_id', 'secret_access_key'}
            require(set(data) == expected and all(isinstance(v, str) and v for v in data.values()), 'invalid storage configuration')
            require(re.fullmatch(r'[a-z0-9][a-z0-9.-]{1,61}[a-z0-9]', data['bucket']), 'invalid bucket')
            require(re.fullmatch(r'[a-z0-9-]+', data['region']), 'invalid region')
            self.storage = data
        return self.storage

    def aws_spec(self, args, archive=None):
        cfg = self.load_storage()
        env = dict(os.environ, AWS_ACCESS_KEY_ID=cfg['access_key_id'],
                   AWS_SECRET_ACCESS_KEY=cfg['secret_access_key'], AWS_DEFAULT_REGION=cfg['region'])
        command = ['-e', 'AWS_ACCESS_KEY_ID', '-e', 'AWS_SECRET_ACCESS_KEY',
                   '-e', 'AWS_DEFAULT_REGION', '-e', 'AWS_EC2_METADATA_DISABLED=true']
        if archive is not None:
            command += ['--mount', 'type=bind,src=' + str(archive) + ',dst=/backup.dump,readonly']
        return command + [AWS_IMAGE] + args, env

    def aws(self, args, archive=None, timeout=120):
        command, env = self.aws_spec(args, archive)
        return invoke(['docker', 'run', '--rm', '--memory=1g', '--memory-swap=1g',
                       '--cpus=1', '--pids-limit=64'] + command, env=env, timeout=timeout)

    def remote(self, name, aws=None):
        key = 'postgres/' + name
        result = json.loads((aws or self.aws)(['s3api', 'list-objects-v2', '--bucket', self.load_storage()['bucket'],
                                     '--prefix', key, '--max-keys', '2', '--no-paginate', '--output', 'json']))
        matches = [x for x in result.get('Contents', []) if x['Key'] == key]
        require(len(matches) <= 1, 'ambiguous object listing')
        if not matches:
            return None
        obj = matches[0]
        return dict(bucket=self.load_storage()['bucket'], key=key, bytes=obj['Size'],
                    etag=obj['ETag'], last_modified=obj['LastModified'])

    def db_env(self):
        password = os.environ.get('DB_PASSWORD')
        if not password:
            path = Path(os.environ.get('ENV_FILE', '/opt/143/.env'))
            # Read a single literal value; never source a shell fragment as root.
            lines = [x.split('=', 1)[1] for x in path.read_text().splitlines() if x.startswith('DB_PASSWORD=')]
            require(len(lines) == 1 and lines[0], 'missing/duplicate database password')
            password = lines[0]
        return dict(os.environ, PGPASSWORD=password)

    def database_bytes(self):
        output = invoke(['docker', 'exec', '-e', 'PGPASSWORD', '-e', 'PGOPTIONS=-c statement_timeout=30000',
                         self.container, 'psql', '-U', self.user, '-d', self.database,
                         '-tAc', 'SELECT pg_database_size(current_database())'], env=self.db_env(), timeout=45)
        value = int(output.strip())
        require(value > 0, 'invalid database size')
        return value

    def structural_check(self, path):
        with path.open('rb') as f:
            invoke(['docker', 'exec', '-i', self.container, 'pg_restore', '--list'], stdin=f,
                   stdout=subprocess.DEVNULL, timeout=self.verify_timeout)

    def inventory(self, remote=True):
        records = []
        for path in sorted(self.root.glob('*.dump')):
            require(re.fullmatch(re.escape(self.database) + r'-\d{8}-\d{6}(?:-[0-9a-f]{32})?\.dump', path.name), 'unrecognized archive: ' + path.name)
            current = identity(path)
            receipt_path = self.state / (path.name + '.json')
            require(receipt_path.exists(), 'unverified archive: ' + path.name)
            r = read_json(receipt_path)
            require(r.get('schema') == 1 and r.get('file') == path.name and r.get('identity') == current
                    and r.get('structural_verified') is True, 'stale/invalid receipt: ' + path.name)
            require(re.fullmatch(r'[0-9a-f]{64}', r.get('sha256', '')) and isinstance(r.get('database_bytes'), int)
                    and r['database_bytes'] > 0, 'invalid receipt evidence: ' + path.name)
            require(r.get('integrity', {}).get('kind') in ('operator_sha256', 'checksum_upload'), 'unqualified integrity receipt')
            if r['integrity']['kind'] == 'checksum_upload':
                require(r['integrity'].get('cli_image') == AWS_IMAGE and expected_cli_version(r['integrity'].get('cli_version'))
                        and r['integrity'].get('algorithm') == 'CRC64NVME', 'unqualified upload receipt')
            else:
                require(r['integrity'].get('version_id') and r['integrity'].get('evidence'), 'incomplete independent receipt')
            require(r.get('remote', {}).get('bytes') == current['bytes'] and current['bytes'] > 0, 'receipt size mismatch')
            if remote:
                require(self.remote(path.name) == r['remote'], 'remote object missing or changed: ' + path.name)
            records.append(r)
        return sorted(records, key=lambda r: r['file'], reverse=True)

    def selection(self, records):
        names = [r['file'] for r in records]
        pin_path = self.state / 'known-good.json'
        pin = read_json(pin_path) if pin_path.exists() else None
        if pin:
            require(pin.get('file') in names and pin.get('evidence'), 'known-good pin is missing or invalid')
            record = next(r for r in records if r['file'] == pin['file'])
            require(record['sha256'] == pin.get('sha256'), 'known-good archive changed')
        chosen = names[:1]
        if pin and pin['file'] not in chosen:
            chosen.append(pin['file'])
        for name in names:
            if len(chosen) < 2 and name not in chosen:
                chosen.append(name)
        return chosen

    def can_prune(self):
        p = self.state / 'checksum-canary.json'
        require(p.exists(), 'checksum upload canary has not been independently approved')
        r = read_json(p)
        require(r.get('cli_image') == AWS_IMAGE and expected_cli_version(r.get('cli_version'))
                and r.get('evidence'), 'checksum canary does not match pinned CLI')

    def prune(self):
        self.no_pending()
        records = self.inventory()
        keep = self.selection(records)
        excess = [r for r in records if r['file'] not in keep]
        if not excess:
            return
        require(len(keep) == 2, 'refusing to prune below two completed copies')
        self.can_prune()
        for r in reversed(excess):
            # Recheck the protected pair, every identity and the candidate's
            # remote object immediately before each unlink. No wildcard deletion.
            fresh = self.inventory()
            require(self.selection(fresh) == keep, 'retention selection changed')
            path = self.root / r['file']
            require(identity(path) == r['identity'], 'archive changed before unlink')
            path.unlink()
            sync_dir(self.root)
            retired = self.state / 'retired'
            retired.mkdir(mode=0o700, exist_ok=True)
            require(not retired.is_symlink() and retired.stat().st_uid == os.geteuid()
                    and not retired.stat().st_mode & 0o077, 'unsafe retired receipt directory')
            os.rename(self.state / (path.name + '.json'), retired / (path.name + '.' + uuid.uuid4().hex + '.json'))
            sync_dir(retired)
            sync_dir(self.state)
            emit('pruned', file=r['file'], kept=keep)

    def admission(self, records, database_bytes):
        if records:
            largest = max(r['identity']['bytes'] for r in records)
            baseline = min(r['database_bytes'] for r in records)
            estimate = math.ceil(largest * max(1, database_bytes / baseline) * 1.25)
        else:
            estimate = math.ceil(database_bytes * 1.25)
        fs = os.statvfs(self.root)
        available = fs.f_bavail * fs.f_frsize
        result = dict(available_bytes=available, estimated_dump_bytes=estimate,
                      reserve_bytes=self.reserve, required_bytes=estimate + self.reserve,
                      database_bytes=database_bytes, selected=self.selection(records))
        emit('admission', **result)
        require(available >= estimate + self.reserve, 'insufficient free space for estimated dump plus reserve')
        return result

    def record(self, path, remote, checksum, integrity, database_bytes, expected_identity, timeline=None):
        require(identity(path) == expected_identity, 'archive changed before receipt publication')
        r = dict(schema=1, file=path.name, identity=expected_identity, sha256=checksum,
                 structural_verified=True, verified_at=now(), database_bytes=database_bytes,
                 remote=remote, integrity=integrity)
        if timeline is not None:
            r['timeline'] = timeline
        require(remote and remote['bytes'] == r['identity']['bytes'], 'uploaded object size mismatch')
        atomic_json(self.state / (path.name + '.json'), r)
        return r

    def import_receipt(self, args):
        self.no_pending()
        path = self.root / args.file
        require(path.parent == self.root and path.name.endswith('.dump'), 'invalid archive path')
        before = identity(path)
        remote = self.remote(path.name)
        require(remote and remote['etag'] == args.etag and remote['last_modified'] == args.last_modified,
                'operator object metadata does not match fresh listing')
        require(sha256(path) == args.sha256, 'independent SHA-256 does not match local archive')
        marker = self.state / 'pending.json'
        atomic_json(marker, dict(phase='verify', file=path.name, started_at=now()))
        self.structural_check(path)
        require(identity(path) == before and self.remote(path.name) == remote, 'archive/object changed during verification')
        previous = self.state / (path.name + '.json')
        timeline = read_json(previous).get('timeline') if previous.exists() else None
        if timeline:
            timeline['independent_verification_recorded_at'] = now()
        self.record(path, remote, args.sha256, dict(kind='operator_sha256', version_id=args.version_id,
                    evidence=args.evidence, verified_at=now()), self.database_bytes(), before, timeline)
        marker.unlink()
        sync_dir(self.state)
        emit('independent_receipt_recorded', file=path.name)

    def dump_archive(self, partial, app_name, estimate, exercise):
        from pg_backup_runtime import protect_dump
        return protect_dump(self, partial, app_name, estimate, exercise)

    def transfer_archive(self, partial, app_name, timeline, measured_db, version):
        from pg_backup_runtime import protect_transfer
        return protect_transfer(self, partial, app_name, timeline, measured_db, version)

    def backup(self, args):
        self.no_pending()
        records = self.inventory()
        require(len(records) >= 2 or args.bootstrap, 'two verified archives required; empty installations need explicit --bootstrap')
        if not args.canary:
            self.can_prune()
            self.prune()
        records = self.inventory()
        measured_db = self.database_bytes()
        admission = self.admission(records, measured_db)
        version = self.aws(['--version']).strip()
        require(expected_cli_version(version), 'unexpected AWS CLI version')
        name = self.database + '-' + dt.datetime.now(dt.timezone.utc).strftime('%Y%m%d-%H%M%S') + '-' + uuid.uuid4().hex + '.dump'
        final = self.root / name
        require(self.remote(name) is None, 'new object key already exists; refusing overwrite')
        partial = self.root / ('.' + name + '.partial.' + uuid.uuid4().hex)
        marker = self.state / 'pending.json'
        app_name = '143-backup-' + uuid.uuid4().hex
        timeline = dict(dump_started_at=now(), recovery_point_basis='pre_dump_lower_bound',
                        full_restore=dict(status='not_verified'))
        atomic_json(marker, dict(file=name, partial=partial.name, app_name=app_name,
                                started_at=timeline['dump_started_at'], phase='dump'))
        fd = os.open(partial, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600)
        os.close(fd)
        self.dump_archive(partial, app_name, admission['estimated_dump_bytes'], getattr(args, 'exercise_stop_after', None))
        timeline['dump_completed_at'] = now()
        pending = read_json(marker)
        atomic_json(marker, dict(pending, timeline=timeline, phase='verification', runtime='postdump'))
        result = self.transfer_archive(partial, app_name, timeline, measured_db, version)
        emit('dump_completed', file=result['file'], bytes=result['bytes'], sha256=result['sha256'])
        if not args.canary:
            self.prune()
        emit('backup_completed', file=name, canary=args.canary, retained=[r['file'] for r in self.inventory()])

    def finish_archive(self, partial, app_name, timeline, measured_db, version, guard):
        marker = self.state / 'pending.json'
        pending = read_json(marker)
        require(pending.get('app_name') == app_name and pending.get('partial') == partial.name and
                pending.get('phase') == 'verification', 'pending verification changed')
        name = pending['file']
        final = self.root / name
        require(final.parent == self.root and not final.exists(), 'archive publication target changed')
        with partial.open('rb') as f:
            os.fsync(f.fileno())
        guard.structural_check(partial)
        timeline['structural_verified_at'] = now()
        atomic_json(marker, dict(pending, timeline=timeline, phase='verification'))
        checksum = guard.checksum(partial)
        timeline['local_sha256_at'] = now()
        require(partial.stat().st_size > 0, 'empty dump')
        # link + unlink publishes atomically without replacing any existing file.
        os.link(partial, final)
        partial.unlink()
        sync_dir(self.root)
        file_id = identity(final)
        timeline['upload_started_at'] = now()
        guard.checkpoint('upload_started')
        atomic_json(marker, dict(file=name, app_name=app_name, started_at=timeline['dump_started_at'],
                                phase='upload', runtime='postdump', identity=file_id, sha256=checksum, timeline=timeline))
        guard.aws(['s3', 'cp', '/backup.dump', 's3://' + self.load_storage()['bucket'] + '/postgres/' + name,
                  '--checksum-algorithm', 'CRC64NVME', '--only-show-errors', '--no-follow-symlinks'], archive=final, timeout=7200)
        require(identity(final) == file_id, 'archive changed during upload')
        timeline['upload_completed_at'] = now()
        guard.checkpoint('upload_completed', enforce=False)
        remote = self.remote(name, aws=guard.aws)
        guard.checkpoint('integrity_verified', enforce=False)
        timeline['integrity_verified_at'] = now()
        timeline['integrity_basis'] = 'checksum_upload_not_independent_download'
        self.record(final, remote, checksum, dict(kind='checksum_upload', cli_image=AWS_IMAGE,
                    cli_version=version, algorithm='CRC64NVME', uploaded_at=now()), measured_db, file_id, timeline)
        marker.unlink()
        sync_dir(self.state)
        return dict(file=name, bytes=file_id['bytes'], sha256=checksum)

    def restore_admission(self, record):
        endpoint = os.environ.get('DOCKER_HOST', '')
        if os.environ.get('DOCKER_CONTEXT') or not endpoint:
            endpoint = invoke(['docker', 'context', 'inspect', '--format', '{{.Endpoints.docker.Host}}']).strip()
        require(endpoint.startswith('unix://'), 'restore capacity admission requires a local Docker socket')
        # M2 must use a separate restore host. Even a stopped production
        # container identifies its Docker daemon as unsafe for this drill. List
        # every name and compare literally, avoiding daemon-side name-filter
        # regex and leading-slash assumptions. A listing error refuses
        # admission instead of being interpreted as a missing container.
        listing = invoke(['docker', 'container', 'ls', '--all', '--format', '{{json .Names}}'])
        names = set()
        for line in listing.splitlines():
            value = json.loads(line)
            require(isinstance(value, str) and value, 'invalid Docker container name listing')
            names.update(name.strip().lstrip('/') for name in value.split(','))
        require(self.container.lstrip('/') not in names,
                'restore drill requires an isolated Docker host, not the production database host')
        docker_root = Path(invoke(['docker', 'info', '--format', '{{.DockerRootDir}}']).strip())
        require(docker_root.is_absolute() and docker_root.is_dir(), 'cannot inspect local Docker data filesystem')
        fs = os.statvfs(docker_root)
        available = fs.f_bavail * fs.f_frsize
        estimate = 2 * max(record['database_bytes'], record['identity']['bytes'])
        emit('restore_admission', available_bytes=available, estimated_restore_bytes=estimate,
             required_bytes=estimate + self.reserve, docker_root=str(docker_root))
        require(available >= estimate + self.reserve, 'insufficient Docker filesystem space for restore plus reserve')

    def restore(self):
        self.no_pending()
        records = self.inventory(remote=False)
        require(records, 'no receipt-qualified local archive')
        self.restore_admission(records[0])
        archive = self.root / records[0]['file']
        require(sha256(archive) == records[0]['sha256'], 'restore archive checksum mismatch')
        # Hold the common lock through the reader and its owned-container
        # cleanup. A crashed wrapper leaves a marker, so another backup cannot
        # prune while a surviving Docker client is reading this archive.
        marker = self.state / 'pending.json'
        cleanup = self.state / ('restore-cleanup-' + uuid.uuid4().hex)
        atomic_json(marker, dict(phase='restore', file=archive.name, started_at=now(), cleanup_receipt=cleanup.name))
        # Replace any previous success before launching the reader. A timeout,
        # launch failure, or wrapper crash must not leave it as the latest drill.
        atomic_json(self.state / 'last-restore.json', dict(file=archive.name, started_at=now(),
                    exit_status=None, cleanup_verified=False, outcome='pending'))
        try:
            status = run_restore_reader(['bash', str(Path(__file__).with_name('restore-test-body.sh'))],
                                        dict(os.environ, BACKUP_ARCHIVE=str(archive), RESTORE_CLEANUP_RECEIPT=str(cleanup)))
        except (OSError, ValueError, KeyError, TypeError, IndexError, Refused, subprocess.TimeoutExpired) as exc:
            atomic_json(self.state / 'last-restore.json', dict(file=archive.name, finished_at=now(),
                        exit_status=1, cleanup_verified=False, outcome='failed', error=str(exc)))
            raise
        verified = cleanup.exists() and identity(cleanup)['bytes'] == 17 and cleanup.read_text() == 'cleanup-complete\n'
        atomic_json(self.state / 'last-restore.json', dict(file=archive.name, finished_at=now(),
                    exit_status=status, cleanup_verified=verified, outcome='succeeded' if status == 0 and verified else 'failed'))
        require(verified, 'restore cleanup unproven; inspect pending operation and owned container')
        # A failed drill with proven cleanup must report failure, but must not
        # suspend unrelated backups indefinitely.
        marker.unlink()
        cleanup.unlink()
        sync_dir(self.state)
        emit('restore_completed', file=archive.name, exit_status=status, cleanup_verified=True)
        return status


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    sub = parser.add_subparsers(dest='action', required=True)
    backup = sub.add_parser('backup')
    backup.add_argument('--bootstrap', action='store_true')
    backup.add_argument('--canary', action='store_true')
    backup.add_argument('--exercise-stop-after', type=int, help='attended canary: intentionally stop this dump after 5..300 seconds')
    sub.add_parser('prune')
    sub.add_parser('plan')
    sub.add_parser('restore')
    verify = sub.add_parser('import-receipt')
    for field in ('file', 'sha256', 'version-id', 'etag', 'last-modified', 'evidence'):
        verify.add_argument('--' + field, required=True)
    for action in ('approve-checksums', 'pin-restored'):
        command = sub.add_parser(action)
        for field in ('file', 'sha256', 'evidence'):
            command.add_argument('--' + field, required=True)
    args = parser.parse_args()
    attempt = None
    try:
        flag = 'RESTORE_TEST_ENABLED' if args.action == 'restore' else 'BACKUP_ENABLED'
        # Receipt/plan commands are permitted while schedules are held.
        if args.action in ('backup', 'restore', 'prune'):
            require(os.environ.get(flag, 'true') == 'true', flag + ' is not true; operation held')
        if args.action in ('backup', 'restore', 'prune'):
            require(os.environ.get('BACKUP_ATTENDED') == 'true', 'BACKUP_ATTENDED=true is required until runtime stop and alert delivery are validated')
            require(os.environ.get('BACKUP_OBSERVER', '').strip(), 'BACKUP_OBSERVER must name the attending operator')
            emit('attended_operation', action=args.action, observer=os.environ['BACKUP_OBSERVER'])
        # Outside the common lock so contention/admission failures are durable.
        attempt = start_attempt(args.action)
        if args.action == 'backup' and args.exercise_stop_after is not None:
            require(args.canary and 5 <= args.exercise_stop_after <= 300,
                    'stop exercise requires --canary and a duration of 5..300 seconds')
        policy = Policy()
        with policy.locked():
            if args.action in ('backup', 'restore', 'prune'):
                policy.retire_attempts()
            if args.action == 'backup':
                policy.backup(args)
            elif args.action == 'prune':
                policy.prune()
            elif args.action == 'plan':
                policy.no_pending()
                policy.admission(policy.inventory(), policy.database_bytes())
            elif args.action == 'restore':
                status = policy.restore()
                finish_attempt(attempt, 'completed' if status == 0 else 'failed')
                return status
            elif args.action == 'import-receipt':
                policy.import_receipt(args)
            else:
                policy.no_pending()
                records = policy.inventory()
                matches = [r for r in records if r['file'] == args.file and r['sha256'] == args.sha256]
                require(len(matches) == 1 and args.evidence.strip(), 'missing archive or independent evidence')
                require(sha256(policy.root / args.file) == args.sha256, 'local checksum changed')
                record = matches[0]
                if args.action == 'approve-checksums':
                    require(record['integrity']['kind'] == 'checksum_upload', 'approve a newly uploaded canary, not a legacy import')
                    atomic_json(policy.state / 'checksum-canary.json', dict(cli_image=AWS_IMAGE,
                                cli_version=record['integrity']['cli_version'], evidence=args.evidence,
                                file=args.file, sha256=args.sha256, approved_at=now()))
                else:
                    atomic_json(policy.state / 'known-good.json', dict(file=args.file, sha256=args.sha256,
                                evidence=args.evidence, restored_at=now()))
                    if record.get('timeline'):
                        record['timeline']['full_restore'] = dict(status='operator_verified',
                                                                recorded_at=now(), evidence=args.evidence)
                        atomic_json(policy.state / (args.file + '.json'), record)
                emit(args.action, file=args.file)
        if attempt:
            finish_attempt(attempt, 'completed')
        return 0
    except (OSError, ValueError, KeyError, TypeError, IndexError, Refused, subprocess.TimeoutExpired) as exc:
        if attempt:
            finish_attempt(attempt, 'failed', str(exc))
        emit('failed', action=args.action, error=str(exc))
        return 1
    except SystemExit:
        if attempt:
            finish_attempt(attempt, 'interrupted')
        raise


def start_attempt(action):
    if action not in ('backup', 'prune', 'restore'):
        return None
    root = Path(os.environ.get('BACKUP_DIR', '/backups/postgres'))
    # Use the same private directory validation without taking the writer lock.
    root.mkdir(mode=0o700, parents=True, exist_ok=True)
    require(root.resolve() == root.absolute() and root.stat().st_uid == os.geteuid()
            and not root.stat().st_mode & 0o022, 'unsafe backup root')
    state = root / '.backup-state'
    state.mkdir(mode=0o700, exist_ok=True)
    require(not state.is_symlink() and state.stat().st_uid == os.geteuid()
            and not state.stat().st_mode & 0o077, 'unsafe state directory')
    path = state / ('attempt-' + uuid.uuid4().hex + '.json')
    atomic_json(path, dict(action=action, status='running', started_at=now(), pid=os.getpid()))
    return path


def finish_attempt(path, status, error=None):
    data = read_json(path)
    atomic_json(path, dict(data, status=status, finished_at=now(), error=error))


if __name__ == '__main__':
    # Leave the in-progress receipt on interruption, and report a nonzero exit.
    for sig in (signal.SIGTERM, signal.SIGHUP, signal.SIGINT):
        signal.signal(sig, lambda signum, frame: sys.exit(128 + signum))
    sys.exit(main())
