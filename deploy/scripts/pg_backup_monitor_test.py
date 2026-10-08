#!/usr/bin/env python3
"""Receipt failures, unsafe transport and independent hold-preserving installation."""
import argparse
import importlib.util
import json
import os
from pathlib import Path
import tempfile
import time
import unittest
from unittest import mock

import pg_backup_monitor as monitor
from pg_backup_state import Refused, atomic_json

spec = importlib.util.spec_from_file_location('installer', Path(__file__).with_name('install-pg-backup-monitoring.py'))
installer = importlib.util.module_from_spec(spec)
spec.loader.exec_module(installer)


def healthy_report():
    return {**dict.fromkeys(monitor.FLAGS, False),
            **dict.fromkeys(monitor.NUMBERS, 1), 'recovery_target_seconds': 21600,
            'last_attempt': {'error': 'secret should not escape'}}


class MonitorTests(unittest.TestCase):
    def setUp(self):
        tmp = tempfile.TemporaryDirectory()
        self.addCleanup(tmp.cleanup)
        self.root = Path(tmp.name).resolve()
        self.config = dict(logging_host='10.12.0.9', monitor_id='primary-db')
        self.config_path = self.root / 'config.json'
        atomic_json(self.config_path, self.config)
        self.cron = self.root / 'backup-cron'
        self.cron.write_text('BACKUP_DIR=/backups/postgres\nBACKUP_ENABLED=false\nRESTORE_TEST_ENABLED=false\n')

    def test_only_literal_private_destinations_and_bounded_ids(self):
        for host in ('10.0.0.9', '172.16.0.1', '192.168.1.1'):
            with self.subTest(host=host):
                self.assertEqual(monitor.validate_config(dict(self.config, logging_host=host))['logging_host'], host)
        for host in ('127.0.0.1', '169.254.169.254', '100.64.0.1', '8.8.8.8', 'localhost',
                     '10.0.0.9:9428', 'http://10.0.0.9', '10.0.0.9\n', '::1', '0.0.0.0'):
            with self.subTest(host=host), self.assertRaises((ValueError, Refused)):
                monitor.validate_config(dict(self.config, logging_host=host))
        for value in ('disabled', '', 'x" | stats', 'a' * 64, 'x\nBACKUP_ENABLED=true'):
            with self.subTest(value=value), self.assertRaises(Refused):
                monitor.validate_config(dict(self.config, monitor_id=value))

    def test_schedule_parsing_never_executes_or_silently_changes_hold(self):
        for text in ('BACKUP_ENABLED =false', 'BACKUP_ENABLED=false\nBACKUP_ENABLED=true',
                     'BACKUP_ENABLED=', 'BACKUP_ENABLED=$(touch /tmp/no)', '# BACKUP_ENABLED=false'):
            with self.subTest(text=text):
                self.cron.write_text('BACKUP_DIR=/backups/postgres\nRESTORE_TEST_ENABLED=false\n' + text + '\n')
                with self.assertRaises(Refused): monitor.schedule_settings(self.cron)

    def test_snapshot_uses_current_holds_and_reserve_without_ambient_overrides(self):
        for value in ('false', 'true'):
            with self.subTest(value=value):
                self.cron.write_text(f'BACKUP_DIR=/backups/postgres\nBACKUP_ENABLED={value}\nRESTORE_TEST_ENABLED=false\n')
                def collect(root):
                    self.assertEqual(root, Path('/backups/postgres'))
                    self.assertEqual(os.environ['BACKUP_ENABLED'], value)
                    self.assertEqual(os.environ['RESTORE_TEST_ENABLED'], 'false')
                    self.assertNotIn('BACKUP_RESERVE_BYTES', os.environ)
                    self.assertNotIn('BACKUP_RECOVERY_TARGET_HOURS', os.environ)
                    return healthy_report()
                with mock.patch.dict(os.environ, BACKUP_ENABLED='ambient', BACKUP_RESERVE_BYTES='0', BACKUP_RECOVERY_TARGET_HOURS='24'), \
                     mock.patch.object(monitor.health, 'collect', side_effect=collect):
                    self.assertEqual(monitor.snapshot(self.cron), healthy_report())
                    self.assertEqual(os.environ['BACKUP_ENABLED'], 'ambient')
                    self.assertEqual(os.environ['BACKUP_RESERVE_BYTES'], '0')
                    self.assertEqual(os.environ['BACKUP_RECOVERY_TARGET_HOURS'], '24')

    def test_recovery_target_is_literal_and_comes_from_current_cron(self):
        original = self.cron.read_text()
        self.cron.write_text(original + 'BACKUP_RECOVERY_TARGET_HOURS=12\n')
        def collect(_root):
            self.assertEqual(os.environ['BACKUP_RECOVERY_TARGET_HOURS'], '12')
            return healthy_report()
        with mock.patch.dict(os.environ, BACKUP_RECOVERY_TARGET_HOURS='6'), \
             mock.patch.object(monitor.health, 'collect', side_effect=collect):
            monitor.snapshot(self.cron)
            self.assertEqual(os.environ['BACKUP_RECOVERY_TARGET_HOURS'], '6')
        for line in ('BACKUP_RECOVERY_TARGET_HOURS=25', 'BACKUP_RECOVERY_TARGET_HOURS=',
                     ' BACKUP_RECOVERY_TARGET_HOURS=12', 'BACKUP_RECOVERY_TARGET_HOURS=$(false)',
                     'BACKUP_RECOVERY_TARGET_HOURS=12\nBACKUP_RECOVERY_TARGET_HOURS=6'):
            with self.subTest(line=line):
                self.cron.write_text(original + line + '\n')
                with mock.patch.object(monitor, 'deliver') as send:
                    event = monitor.run(self.config_path, self.cron)
                self.assertEqual(event['telemetry_failed'], 1)
                send.assert_called_once_with(self.config, event)

    def test_failed_collector_still_delivers_an_explicit_failure(self):
        for exc in (Refused('secret'), TimeoutError('secret'), FileNotFoundError('secret'), KeyError('secret')):
            with self.subTest(exc=exc), mock.patch.object(monitor, 'snapshot', side_effect=exc), \
                 mock.patch.object(monitor, 'deliver') as send:
                result = monitor.run(self.config_path, self.cron)
                self.assertEqual(result['telemetry_failed'], 1)
                self.assertNotIn('secret', json.dumps(result))
                send.assert_called_once_with(self.config, result)

    def test_expired_schedule_delivers_held_flag_without_profile_details(self):
        backups = self.root / 'backups'
        state = backups / '.backup-state'
        state.mkdir(mode=0o700, parents=True)
        self.cron.write_text(f'BACKUP_DIR={backups}\nBACKUP_ENABLED=true\nRESTORE_TEST_ENABLED=false\n')
        atomic_json(state / 'scheduled-backup.json', dict(schema=1, owner='private operator',
                    evidence='private approval reference', starts_at='2020-01-01T00:00:00+00:00',
                    expires_at='2020-01-01T01:00:00+00:00', host_memory_full_percent=1, db_memory_full_percent=1))
        with mock.patch.object(monitor.health, 'swap_usage', return_value=0), mock.patch.object(monitor, 'deliver') as send:
            event = monitor.run(self.config_path, self.cron)
        self.assertEqual((event['backup_held'], event['restore_held'], event['telemetry_failed']), (1, 1, 0))
        self.assertNotIn('private', json.dumps(event), 'profile details must stay out of central events')
        send.assert_called_once_with(self.config, event)

    def test_event_is_scalar_bounded_and_snapshot_freshness_has_dump_grace(self):
        for target, age, missed in ((21600, None, 1), (21600, 21600, 0), (21600, 24300, 0),
                                   (21600, 24301, 1), (43200, None, 1), (43200, 43200, 0),
                                   (43200, 45900, 0), (43200, 45901, 1)):
            with self.subTest(target=target, age=age):
                report = healthy_report()
                report['recovery_age_seconds'] = age
                report['recovery_target_seconds'] = target
                event = monitor.event_for(self.config, report)
                self.assertEqual(event['recovery_target_missed'], missed)
                self.assertEqual(event['recovery_target_seconds'], target)
                self.assertNotIn('last_attempt', event)
                self.assertNotIn('secret', json.dumps(event))
                self.assertTrue(all(type(v) in (str, int, float) for v in event.values()))
        for invalid in (float('inf'), -1, True, '100'):
            with self.subTest(invalid=invalid), self.assertRaises(Refused):
                monitor.event_for(self.config, dict(healthy_report(), free_bytes=invalid))
        for invalid in (None, True, 0, 3601, 90000, '43200'):
            with self.subTest(invalid=invalid), self.assertRaises(Refused):
                monitor.event_for(self.config, dict(healthy_report(), recovery_target_seconds=invalid))

    def test_transport_does_not_follow_redirects_retry_or_read_bodies(self):
        for status, success in ((200, True), (204, True), (302, False), (429, False), (500, False)):
            with self.subTest(status=status), mock.patch.object(monitor.http.client, 'HTTPConnection') as factory, \
                 mock.patch.dict(os.environ, HTTP_PROXY='http://credential@public.example:80', HTTPS_PROXY='bad'):
                connection = factory.return_value
                connection.getresponse.return_value.status = status
                if success:
                    monitor.deliver(self.config, {'ok': 1})
                else:
                    with self.assertRaises(Refused): monitor.deliver(self.config, {'ok': 1})
                factory.assert_called_once_with('10.12.0.9', 9428, timeout=5)
                connection.request.assert_called_once()
                connection.getresponse.return_value.read.assert_not_called()
                connection.close.assert_called_once()

    def test_payload_size_and_wall_clock_deadlines(self):
        with mock.patch.object(monitor.http.client, 'HTTPConnection') as factory:
            with self.assertRaises(Refused): monitor.deliver(self.config, {'data': 'a' * 4096})
            factory.assert_not_called()
        started = time.monotonic()
        with self.assertRaises(TimeoutError), monitor.deadline(0.02): time.sleep(1)
        self.assertLess(time.monotonic() - started, 0.5)

    def test_unreadable_private_config_never_attempts_delivery(self):
        self.config_path.chmod(0o644)
        with mock.patch.object(monitor, 'deliver') as send, self.assertRaises(Refused):
            monitor.run(self.config_path, self.cron)
        send.assert_not_called()

    def test_install_is_independent_idempotent_and_preserves_both_holds(self):
        args = argparse.Namespace(logging_host=self.config['logging_host'], monitor_id=self.config['monitor_id'],
            scripts_dir=Path(__file__).parent.resolve(), config=self.root / 'installed.json', backup_cron=self.cron,
            cron_file=self.root / 'health-cron', log=self.root / 'health.log', logrotate_file=self.root / 'rotate')
        before = self.cron.read_bytes()
        with mock.patch.object(monitor, 'deliver') as send:
            installer.install(args)
            send.assert_not_called()
        inode = args.cron_file.stat().st_ino
        args.log.write_text('prior evidence\n')
        installer.install(args)
        self.assertEqual(self.cron.read_bytes(), before)
        self.assertEqual(args.cron_file.stat().st_ino, inode)
        self.assertEqual(args.log.read_text(), 'prior evidence\n')
        self.assertEqual(args.log.stat().st_mode & 0o777, 0o600)
        self.assertEqual(args.config.stat().st_mode & 0o777, 0o600)
        self.assertIn('* * * * * root /usr/bin/timeout', args.cron_file.read_text())
        self.assertNotIn('pg-backup.sh', args.cron_file.read_text())
        self.assertNotIn('restore-test.sh', args.cron_file.read_text())
        self.assertIn('rotate 7', args.logrotate_file.read_text())
        args.cron_file = self.cron
        with self.assertRaises(Refused): installer.install(args)
        self.assertEqual(self.cron.read_bytes(), before)

    def test_install_rejects_link_aliases_without_changing_schedule(self):
        for link in ('symlink', 'hardlink'):
            with self.subTest(link=link):
                destination = self.root / link
                if link == 'symlink':
                    destination.symlink_to(self.cron)
                else:
                    os.link(self.cron, destination)
                args = argparse.Namespace(logging_host=self.config['logging_host'], monitor_id=self.config['monitor_id'],
                    scripts_dir=Path(__file__).parent.resolve(), config=self.root / 'installed.json', backup_cron=self.cron,
                    cron_file=self.root / 'health-cron', log=destination, logrotate_file=self.root / 'rotate')
                before = self.cron.stat()
                contents = self.cron.read_bytes()
                with self.assertRaises(Refused): installer.install(args)
                self.assertEqual(self.cron.read_bytes(), contents)
                self.assertEqual(self.cron.stat().st_mode, before.st_mode)
                self.assertFalse(args.config.exists())


if __name__ == '__main__':
    unittest.main()
