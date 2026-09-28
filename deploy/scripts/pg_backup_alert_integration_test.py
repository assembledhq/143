#!/usr/bin/env python3
"""Exercise the shipped LogsQL against an isolated VictoriaLogs binary (no Docker).

Usage: python3 deploy/scripts/pg_backup_alert_integration_test.py --binary /path/to/victoria-logs-prod
Use the same pinned version as docker-compose.logging.yml; no production access.
"""
import argparse
import datetime as dt
from http.server import BaseHTTPRequestHandler, ThreadingHTTPServer
import json
import os
from pathlib import Path
import re
import socket
import subprocess
import tempfile
import threading
import time
import urllib.parse
import urllib.request
from unittest import mock

import pg_backup_monitor as monitor


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument('--binary', type=Path, required=True)
    parser.add_argument('--vmalert-binary', type=Path)
    args = parser.parse_args()
    rules_text = (Path(__file__).resolve().parents[1] / 'vmalert/rules/database-backup.yml').read_text()
    rules = dict(re.findall(r"- alert: (\w+)\n\s+expr: '([^\n]+)'", rules_text))
    assert len(rules) == 10, 'all backup alert queries must be exercised'
    with tempfile.TemporaryDirectory() as directory:
        with socket.socket() as sock:
            sock.bind(('127.0.0.1', 0))
            port = sock.getsockname()[1]
        endpoint = f'http://127.0.0.1:{port}'
        opener = urllib.request.build_opener(urllib.request.ProxyHandler({}))
        def request(path, data=None):
            headers = {'Content-Type': 'application/stream+json'} if path.startswith('/insert/') else {}
            req = urllib.request.Request(endpoint + path, data=data, headers=headers)
            with opener.open(req, timeout=5) as response:
                return response.read()
        def query(expression, monitor_id):
            expression = expression.replace('%{BACKUP_MONITOR_ID}', monitor_id)
            data = urllib.parse.urlencode({'query': expression}).encode()
            # This is the endpoint vmalert actually uses; value 0 still returns
            # a series for the no-heartbeat alert rather than disappearing.
            result = json.loads(request('/select/logsql/stats_query', data))
            assert result['status'] == 'success', result
            return result['data']['result']
        def insert(monitor_id, **fields):
            body = dict(service='database-backup', event='backup_health', schema=1,
                        monitor_id=monitor_id, **fields)
            request('/insert/jsonline?_stream_fields=service,monitor_id',
                    (json.dumps(body) + '\n').encode())
        with open(Path(directory) / 'server.log', 'wb') as log:
            process = subprocess.Popen([str(args.binary), '-storageDataPath=' + directory + '/storage',
                '-httpListenAddr=127.0.0.1:' + str(port), '-memory.allowedBytes=256MB', '-search.maxConcurrentRequests=2'],
                stdout=log, stderr=log, stdin=subprocess.DEVNULL)
            try:
                for _ in range(100):
                    if process.poll() is not None:
                        raise RuntimeError(Path(directory, 'server.log').read_text())
                    try:
                        request('/health')
                        break
                    except OSError:
                        time.sleep(.05)
                else:
                    raise RuntimeError('local VictoriaLogs did not start')
                missing = rules['DatabaseBackupTelemetryMissing']
                assert query(missing, 'never-reported'), 'never-started collector must alert'
                insert('another-host', telemetry_failed=0)
                request('/internal/force_flush')
                assert query(missing, 'never-reported'), 'other collectors cannot mask missing expected identity'
                for name, expression in rules.items():
                    assert query(expression, 'disabled') == [], f'disabled must not alert: {name}'
                flags = {
                    'DatabaseBackupTelemetryInvalid': 'telemetry_failed',
                    'DatabaseBackupAttemptFailed': 'backup_failed',
                    'DatabaseBackupWatchdogStale': 'watchdog_stale',
                    'DatabaseBackupRecoveryPointStale': 'recovery_target_missed',
                    'DatabaseBackupCapacityLow': 'capacity_low',
                    'DatabaseBackupReserveLow': 'reserve_at_risk',
                    'DatabaseBackupRestoreOverdue': 'restore_overdue',
                    'DatabaseBackupSwapHigh': 'swap_high',
                    'DatabaseBackupScheduleHeld': 'backup_held',
                }
                for index, (name, flag) in enumerate(flags.items()):
                    monitor_id = 'fixture-' + str(index)
                    insert(monitor_id, **{flag: 1})
                    request('/internal/force_flush')
                    assert query(missing, monitor_id) == [], ('failure event is a live heartbeat',
                        request('/select/logsql/query', urllib.parse.urlencode({'query': '_time:5m | limit 5'}).encode()))
                    result = query(rules[name], monitor_id)
                    assert len(result) == 1 and result[0]['metric']['monitor_id'] == monitor_id, (name, result)
                    insert(monitor_id, **{flag: 0})
                    request('/internal/force_flush')
                    assert query(rules[name], monitor_id) == [], f'latest healthy state must resolve {name}'
                print('PASS: all 10 real LogsQL rules, disabled/no-first-heartbeat/identity isolation/failure/recovery')
                if args.vmalert_binary:
                    # Real evaluation and notification, isolated from every
                    # production destination. Accelerate only test timing.
                    test_rules = re.sub(r'interval: 1m', 'interval: 1s', rules_text)
                    test_rules = re.sub(r'for: \d+m', 'for: 0s', test_rules)
                    rule_path = Path(directory) / 'rules.yml'
                    rule_path.write_text(test_rules)
                    received = []
                    class Receiver(BaseHTTPRequestHandler):
                        def do_POST(self):
                            received.extend(json.loads(self.rfile.read(int(self.headers['Content-Length']))))
                            self.send_response(200)
                            self.end_headers()
                        def log_message(self, *_args):
                            return
                    receiver = ThreadingHTTPServer(('127.0.0.1', 0), Receiver)
                    thread = threading.Thread(target=receiver.serve_forever, daemon=True)
                    thread.start()
                    with socket.socket() as sock:
                        sock.bind(('127.0.0.1', 0))
                        alert_port = sock.getsockname()[1]
                    alert_process = subprocess.Popen([str(args.vmalert_binary), '-rule=' + str(rule_path),
                        '-datasource.url=' + endpoint, '-rule.defaultRuleType=vlogs', '-rule.evalDelay=0s',
                        '-httpListenAddr=127.0.0.1:' + str(alert_port),
                        '-notifier.url=http://127.0.0.1:' + str(receiver.server_port)],
                        env=dict(os.environ, BACKUP_MONITOR_ID='pipeline'), stdout=log, stderr=log,
                        stdin=subprocess.DEVNULL)
                    try:
                        end = time.monotonic() + 20
                        while not received and time.monotonic() < end:
                            time.sleep(.1)
                        assert any(a['labels']['alertname'] == 'DatabaseBackupTelemetryMissing' for a in received), \
                            'real evaluator must notify for count=0'
                        real_connection = monitor.http.client.HTTPConnection
                        def local_connection(_host, _port, **kwargs):
                            return real_connection('127.0.0.1', port, **kwargs)
                        config = dict(logging_host='10.23.0.9', monitor_id='pipeline')
                        report = {**dict.fromkeys(monitor.FLAGS, False), **dict.fromkeys(monitor.NUMBERS, 1)}
                        with mock.patch.object(monitor.http.client, 'HTTPConnection', side_effect=local_connection):
                            monitor.deliver(config, monitor.event_for(config, report))
                        request('/internal/force_flush')
                        end = time.monotonic() + 20
                        while time.monotonic() < end:
                            if any(a['labels']['alertname'] == 'DatabaseBackupTelemetryMissing'
                                   and dt.datetime.fromisoformat(a['endsAt'].replace('Z', '+00:00'))
                                   <= dt.datetime.now(dt.timezone.utc) for a in received):
                                break
                            time.sleep(.1)
                        else:
                            raise AssertionError('real sender/evaluator must deliver a resolved notification')
                        print('PASS: real vmalert firing/resolved delivery to localhost receiver and real HTTP sender')
                    finally:
                        alert_process.terminate()
                        try:
                            alert_process.wait(timeout=5)
                        except subprocess.TimeoutExpired:
                            alert_process.kill()
                            alert_process.wait(timeout=5)
                        receiver.shutdown()
                        receiver.server_close()
                        thread.join(timeout=2)
            finally:
                process.terminate()
                try:
                    process.wait(timeout=10)
                except subprocess.TimeoutExpired:
                    process.kill()
                    process.wait(timeout=5)


if __name__ == '__main__':
    main()
