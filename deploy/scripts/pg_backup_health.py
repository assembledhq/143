#!/usr/bin/env python3
"""Independent, lock-free backup health snapshot. No DB, S3 or Docker access."""
import datetime as dt
import json
import os
from pathlib import Path
import re
import sys
import time

sys.path.insert(0, str(Path(__file__).resolve().parent))
from pg_backup_state import Refused, require, now, identity, read_json, atomic_json

GIB = 1024 ** 3


def age(value, current):
    parsed = dt.datetime.fromisoformat(value)
    require(parsed.tzinfo is not None, 'timestamp lacks timezone')
    seconds = current - parsed.timestamp()
    require(seconds >= -60, 'timestamp is in the future')
    return max(0, seconds)


def recovery_start(receipt):
    timeline = receipt.get('timeline')
    if timeline:
        require(timeline.get('recovery_point_basis') == 'pre_dump_lower_bound', 'unknown recovery point basis')
        return timeline['dump_started_at']
    # Imported legacy receipts record import time, which is NOT the snapshot.
    match = re.fullmatch(r'[a-zA-Z0-9_]+-(\d{8}-\d{6})(?:-[a-f0-9]{32})?\.dump', receipt['file'])
    require(match is not None, 'legacy snapshot timestamp unavailable')
    return dt.datetime.strptime(match[1], '%Y%m%d-%H%M%S').replace(tzinfo=dt.timezone.utc).isoformat()


def swap_usage():
    memory = {k: int(v.split()[0]) * 1024 for k, v in
              (line.split(':', 1) for line in Path('/proc/meminfo').read_text().splitlines())}
    used = memory['SwapTotal'] - memory['SwapFree']
    require(0 <= used <= memory['SwapTotal'], 'invalid swap telemetry')
    return used


def collect(root, current=None):
    current = time.time() if current is None else current
    state = root / '.backup-state'
    require(root.resolve() == root.absolute() and state.is_dir() and not state.is_symlink()
            and state.stat().st_uid == os.geteuid() and not state.stat().st_mode & 0o077,
            'private backup state unavailable')
    fs = os.statvfs(root)
    free = fs.f_bavail * fs.f_frsize
    reserve = int(os.environ.get('BACKUP_RESERVE_BYTES', 20 * GIB))
    require(reserve >= 20 * GIB, 'invalid reserve')
    records = []
    for path in state.glob('*.dump.json'):
        r = read_json(path)
        archive = root / r['file']
        require(archive.parent == root, 'invalid archive receipt path')
        if not archive.exists():
            continue  # A prune can unlink the archive before retiring its receipt.
        require(r.get('schema') == 1 and r.get('structural_verified') is True and
                re.fullmatch('[a-f0-9]{64}', r.get('sha256', '')) and
                r.get('identity') == identity(archive), 'invalid local backup evidence')
        integrity = r.get('integrity', {})
        require(integrity.get('kind') in ('operator_sha256', 'checksum_upload') and
                r.get('remote', {}).get('bytes') == r['identity']['bytes'], 'missing upload evidence')
        if integrity['kind'] == 'operator_sha256':
            require(integrity.get('version_id') and integrity.get('evidence'), 'missing independent evidence')
        else:
            require(integrity.get('algorithm') == 'CRC64NVME' and integrity.get('cli_image')
                    and integrity.get('cli_version'), 'missing checksum upload evidence')
        records.append(r)
    freshness = min((age(recovery_start(r), current) for r in records), default=None)
    largest = max((r['identity']['bytes'] for r in records), default=0)
    attempts = [read_json(p) for p in state.glob('attempt-*.json')]
    relevant = [a for a in attempts if a['action'] in ('backup', 'resume-upload')]
    latest = max(relevant, key=lambda a: a['started_at']) if relevant else None
    failed = bool(latest and (latest['status'] in ('failed', 'interrupted') or
                  (latest['status'] == 'running' and age(latest['started_at'], current) > 3 * 3600)))
    if latest and latest['status'] == 'running':
        # The parent can disappear before updating its attempt, while its
        # detached watchdog completes safe cleanup. Do not hide that failure.
        for path in [*state.glob('143-backup-*/result.json'), *state.glob('143-backup-*/postdump/result.json')]:
            result = read_json(path)
            if result['status'] == 'failed' and age(result['at'], current) <= age(latest['started_at'], current):
                failed = True
    pending = read_json(state / 'pending.json') if (state / 'pending.json').exists() else None
    stalled = False
    if pending and (pending['phase'] == 'dump' or pending.get('runtime') == 'postdump'):
        app = pending.get('app_name', '')
        require(re.fullmatch(r'143-backup-[a-f0-9]{32}', app), 'invalid pending dump identity')
        run = state / app
        if pending.get('runtime') == 'postdump':
            require(pending['phase'] in ('verification', 'upload'), 'invalid post-dump phase')
            run = run / 'postdump'
            recovery_path = state / 'recovery.json'
            if recovery_path.exists():
                recovery = read_json(recovery_path)
                if recovery.get('source_app') == app and recovery.get('file') == pending['file']:
                    recovering_app = recovery.get('app_name', '')
                    require(re.fullmatch(r'143-backup-[a-f0-9]{32}', recovering_app), 'invalid recovery identity')
                    # Preparation preserves the old marker for reconciliation.
                    # Follow the fresh watchdog without changing snapshot age.
                    run = state / recovering_app / 'postdump'
                    pending = dict(pending, started_at=recovery['started_at'])
        heartbeat = run / 'heartbeat.json'
        result = run / 'result.json'
        stalled = (result.exists() and read_json(result)['status'] != 'completed') or (
            age(read_json(heartbeat)['at'], current) > 90 if heartbeat.exists()
            else age(pending['started_at'], current) > 90)
    elif pending:
        stalled = age(pending['started_at'], current) > 3 * 3600
    restore = read_json(state / 'known-good.json') if (state / 'known-good.json').exists() else None
    if restore:
        require(any(r['file'] == restore.get('file') and r['sha256'] == restore.get('sha256') for r in records),
                'full restore pin lacks matching retained archive')
    restored = bool(restore and restore.get('evidence') and restore.get('restored_at'))
    restore_age = age(restore['restored_at'], current) if restored else None
    swap = swap_usage()
    newest = max(records, key=recovery_start) if records else {}
    return dict(time=now(), service='database-backup', event='backup_health', message='Database backup health',
                backup_held=os.environ.get('BACKUP_ENABLED') != 'true',
                restore_held=os.environ.get('RESTORE_TEST_ENABLED') != 'true',
                telemetry_failed=False, backup_failed=failed, watchdog_stale=bool(stalled),
                recovery_age_seconds=freshness, recovery_stale=freshness is None or freshness > 6 * 3600,
                restore_age_seconds=restore_age, restore_overdue=restore_age is None or restore_age > 8 * 86400,
                qualified_copies=len(records), free_bytes=free,
                capacity_low=len(records) < 2 or free < reserve + 5 * GIB + int(largest * 1.25),
                reserve_at_risk=free < reserve + 4 * GIB,
                swap_bytes=swap, swap_high=swap > GIB / 4,
                latest_backup_resources=newest.get('timeline', {}).get('resources'),
                last_attempt=latest, pending_phase=pending['phase'] if pending else None)


def main():
    root = Path(os.environ.get('BACKUP_DIR', '/backups/postgres'))
    try:
        report = collect(root)
        atomic_json(root / '.backup-state' / 'health.json', report)
    except (OSError, ValueError, KeyError, TypeError, Refused) as exc:
        report = dict(time=now(), service='database-backup', event='backup_health',
                      message='Backup telemetry failed', telemetry_failed=True, error=str(exc))
    # No customer data, object credentials or raw subprocess output.
    print(json.dumps(report), flush=True)
    return int(report['telemetry_failed'])


if __name__ == '__main__':
    raise SystemExit(main())
