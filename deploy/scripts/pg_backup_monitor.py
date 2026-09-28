#!/usr/bin/env python3
"""Bounded receipt-only health delivery to a private logging endpoint."""
import argparse
from contextlib import contextmanager
import http.client
import ipaddress
import json
import math
import os
from pathlib import Path
import re
import signal
import sys

sys.path.insert(0, str(Path(__file__).resolve().parent))
import pg_backup_health as health
from pg_backup_state import Refused, now, read_json, require

CONFIG = '/opt/143/backup-monitoring.json'
BACKUP_CRON = '/etc/cron.d/143-pg-backup'
PRIVATE_NETWORKS = tuple(ipaddress.IPv4Network(n) for n in
                         ('10.0.0.0/8', '172.16.0.0/12', '192.168.0.0/16'))
FLAGS = ('backup_held', 'restore_held', 'backup_failed', 'watchdog_stale',
         'recovery_stale', 'restore_overdue', 'capacity_low', 'reserve_at_risk', 'swap_high')
NUMBERS = ('recovery_age_seconds', 'restore_age_seconds', 'qualified_copies', 'free_bytes', 'swap_bytes')


def validate_config(config):
    require(set(config) == {'logging_host', 'monitor_id'}, 'invalid monitoring configuration fields')
    require(isinstance(config['logging_host'], str), 'logging destination must be a literal address')
    host = ipaddress.IPv4Address(config['logging_host'])
    require(any(host in network for network in PRIVATE_NETWORKS), 'logging destination must be RFC1918 IPv4')
    monitor = config['monitor_id']
    require(isinstance(monitor, str) and re.fullmatch(r'[a-z][a-z0-9_-]{0,62}', monitor)
            and monitor != 'disabled', 'invalid monitor ID')
    return dict(logging_host=str(host), monitor_id=monitor)


def schedule_settings(path):
    # Never source cron or the host .env (which also contains credentials).
    require(path.is_file() and not path.is_symlink() and path.stat().st_size < 16384,
            'backup schedule unavailable')
    wanted = {'BACKUP_DIR', 'BACKUP_ENABLED', 'RESTORE_TEST_ENABLED', 'BACKUP_RESERVE_BYTES'}
    settings = {}
    for line in path.read_text().splitlines():
        match = re.match(r'^\s*([A-Z_]+)\s*=(.*)$', line)
        if not match or match[1] not in wanted:
            continue
        key, value = match.groups()
        require(line == key + '=' + value and key not in settings, 'ambiguous backup schedule')
        settings[key] = value
    for key in ('BACKUP_ENABLED', 'RESTORE_TEST_ENABLED'):
        require(settings.get(key) in ('true', 'false'), 'invalid backup schedule hold')
    require(re.fullmatch(r'/[a-zA-Z0-9_./-]+', settings.get('BACKUP_DIR', '')),
            'invalid backup directory')
    if 'BACKUP_RESERVE_BYTES' in settings:
        require(settings['BACKUP_RESERVE_BYTES'].isdigit(), 'invalid backup reserve')
    return settings


def snapshot(cron):
    settings = schedule_settings(cron)
    original = {k: os.environ.get(k) for k in (*settings, 'BACKUP_RESERVE_BYTES')}
    try:
        os.environ.pop('BACKUP_RESERVE_BYTES', None)
        os.environ.update(settings)
        return health.collect(Path(settings['BACKUP_DIR']))
    finally:
        for key, value in original.items():
            if value is None:
                os.environ.pop(key, None)
            else:
                os.environ[key] = value


def event_for(config, report):
    # An explicit scalar allowlist keeps attempts, subprocess errors, paths,
    # archive identifiers and resource histories out of central log payloads.
    event = dict(service='database-backup', event='backup_health', schema=1,
                 monitor_id=config['monitor_id'], observed_at=now(),
                 message='Database backup health', telemetry_failed=0)
    for key in FLAGS:
        require(type(report[key]) is bool, 'invalid health flag')
        event[key] = int(report[key])
    for key in NUMBERS:
        value = report[key]
        require(value is None or (type(value) in (int, float) and math.isfinite(value) and value >= 0),
                'invalid health measurement')
        if value is not None:
            event[key] = value
    age = report['recovery_age_seconds']
    # Report the six-hour objective separately; alert after the dump budget.
    event['recovery_target_missed'] = int(age is None or age > 6 * 3600 + 45 * 60)
    return event


@contextmanager
def deadline(seconds):
    # A socket's timeout alone is insufficient against a peer dripping headers.
    # The collector and entire HTTP exchange must both have wall-clock limits.
    def expired(_signum, _frame):
        raise TimeoutError('backup monitor deadline')
    previous = signal.signal(signal.SIGALRM, expired)
    signal.setitimer(signal.ITIMER_REAL, seconds)
    try:
        yield
    finally:
        signal.setitimer(signal.ITIMER_REAL, 0)
        signal.signal(signal.SIGALRM, previous)


def deliver(config, event):
    config = validate_config(config)
    payload = (json.dumps(event, allow_nan=False, separators=(',', ':')) + '\n').encode()
    require(len(payload) <= 4096, 'oversized health event')
    # Literal RFC1918 IP, fixed port/path, no DNS, proxy environment, redirects,
    # retries or response body. Ingestion time avoids clock drift hiding a host.
    connection = http.client.HTTPConnection(config['logging_host'], 9428, timeout=5)
    try:
        with deadline(5):
            connection.request('POST', '/insert/jsonline?_stream_fields=service,monitor_id&_msg_field=message',
                               body=payload, headers={'Content-Type': 'application/stream+json'})
            response = connection.getresponse()
            require(200 <= response.status < 300, 'health ingestion rejected')
    finally:
        connection.close()


def run(config_path, cron):
    config = validate_config(read_json(config_path))
    try:
        with deadline(10):
            event = event_for(config, snapshot(cron))
    except (OSError, ValueError, KeyError, TypeError, Refused):
        # Deliver failure even when receipts, mounts, holds or the collector are
        # broken. Do not send raw exception text (it may contain file content).
        event = dict(service='database-backup', event='backup_health', schema=1,
                     monitor_id=config['monitor_id'], observed_at=now(),
                     message='Backup telemetry failed', telemetry_failed=1)
    deliver(config, event)
    return event


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--config', type=Path, default=Path(CONFIG))
    parser.add_argument('--backup-cron', type=Path, default=Path(BACKUP_CRON))
    args = parser.parse_args()
    try:
        event = run(args.config, args.backup_cron)
        print(json.dumps(dict(at=now(), event='backup_health_delivery', delivered=True,
                              telemetry_failed=event['telemetry_failed'])), flush=True)
        return event['telemetry_failed']
    except (OSError, ValueError, KeyError, TypeError, Refused, http.client.HTTPException):
        print(json.dumps(dict(at=now(), event='backup_health_delivery', delivered=False)), flush=True)
        return 1


if __name__ == '__main__':
    raise SystemExit(main())
