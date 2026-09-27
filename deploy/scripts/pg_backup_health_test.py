#!/usr/bin/env python3
"""Independent health must fail visibly; imports/uploads cannot reset RPO."""
import datetime as dt
import os
from pathlib import Path
import tempfile
import types
import unittest
from unittest import mock

import pg_backup_health as health
from pg_backup_state import Refused, identity, atomic_json


class HealthTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name).resolve()
        self.state = self.root / '.backup-state'
        self.state.mkdir(mode=0o700)
        self.current = dt.datetime(2026, 9, 27, 12, tzinfo=dt.timezone.utc).timestamp()
        patch = mock.patch.object(health.os, 'statvfs', return_value=types.SimpleNamespace(f_bavail=50 * health.GIB, f_frsize=1))
        patch.start()
        self.addCleanup(patch.stop)
        self._real_swap_usage = health.swap_usage
        patch = mock.patch.object(health, 'swap_usage', return_value=0)
        patch.start()
        self.addCleanup(patch.stop)

    def record(self, timestamp='20260927-060000', **extra):
        name = 'onefortythree-' + timestamp + '.dump'
        p = self.root / name
        p.write_bytes(b'valid dump')
        p.chmod(0o600)
        data = dict(file=name, schema=1, identity=identity(p), structural_verified=True,
                    sha256='a' * 64, remote={'bytes': p.stat().st_size},
                    integrity={'kind': 'operator_sha256', 'version_id': 'v', 'evidence': 'downloaded SHA'},
                    verified_at='2026-09-27T11:59:00+00:00', **extra)
        atomic_json(self.state / (name + '.json'), data)
        return data

    def test_freshness_uses_dump_start_not_import_or_upload_completion(self):
        self.record('20260927-050000')
        report = health.collect(self.root, self.current)
        self.assertEqual(report['recovery_age_seconds'], 7 * 3600)
        self.assertTrue(report['recovery_stale'])
        self.record(timeline={'recovery_point_basis': 'pre_dump_lower_bound',
                    'dump_started_at': '2026-09-27T05:30:00+00:00',
                    'upload_completed_at': '2026-09-27T11:59:00+00:00'})
        report = health.collect(self.root, self.current)
        self.assertEqual(report['recovery_age_seconds'], 6.5 * 3600)
        self.assertTrue(report['recovery_stale'])

    def test_holds_do_not_suppress_stale_or_overdue_or_capacity(self):
        with mock.patch.dict(os.environ, BACKUP_ENABLED='false', RESTORE_TEST_ENABLED='false'):
            report = health.collect(self.root, self.current)
        for key in ['backup_held', 'restore_held', 'recovery_stale', 'restore_overdue', 'capacity_low']:
            self.assertTrue(report[key], key)

    def test_failed_or_stuck_attempt_visible_even_without_pending_marker(self):
        for status in ['failed', 'interrupted', 'running']:
            with self.subTest(status=status):
                atomic_json(self.state / 'attempt-test.json', dict(action='backup', status=status,
                            started_at='2026-09-27T06:00:00+00:00', error='lock timeout'))
                self.assertTrue(health.collect(self.root, self.current)['backup_failed'])
        atomic_json(self.state / 'attempt-new.json', dict(action='backup', status='completed',
                    started_at='2026-09-27T11:00:00+00:00'))
        self.assertFalse(health.collect(self.root, self.current)['backup_failed'])

    def test_orphaned_attempt_reports_watchdog_failure_after_successful_cleanup(self):
        atomic_json(self.state / 'attempt-test.json', dict(action='backup', status='running',
                    started_at='2026-09-27T11:50:00+00:00'))
        run = self.state / ('143-backup-' + 'a' * 32)
        run.mkdir()
        atomic_json(run / 'result.json', dict(status='failed', cleanup_verified=True,
                    at='2026-09-27T11:51:00+00:00'))
        self.assertTrue(health.collect(self.root, self.current)['backup_failed'])

    def test_missing_watchdog_and_uncertain_cleanup_remain_visible(self):
        app = '143-backup-' + 'a' * 32
        atomic_json(self.state / 'pending.json', dict(phase='dump', app_name=app,
                    started_at='2026-09-27T06:00:00+00:00'))
        self.assertTrue(health.collect(self.root, self.current)['watchdog_stale'])
        run = self.state / app
        run.mkdir()
        atomic_json(run / 'heartbeat.json', {'at': '2026-09-27T11:59:59+00:00'})
        self.assertFalse(health.collect(self.root, self.current)['watchdog_stale'])
        atomic_json(run / 'result.json', {'status': 'failed', 'cleanup_verified': False})
        self.assertTrue(health.collect(self.root, self.current)['watchdog_stale'])

    def test_receipt_identity_damage_fails_closed(self):
        r = self.record()
        (self.root / r['file']).write_bytes(b'changed')
        with self.assertRaisesRegex(Refused, 'evidence'): health.collect(self.root, self.current)

    def test_prune_window_does_not_count_deleted_copy(self):
        r = self.record()
        (self.root / r['file']).unlink()
        self.assertEqual(health.collect(self.root, self.current)['qualified_copies'], 0)

    def test_bad_clock_and_unknown_legacy_name_are_not_fresh(self):
        for value in ['2026-09-28T00:00:00+00:00', '2026-09-27T12:00:00', 'invalid']:
            with self.subTest(value=value):
                with self.assertRaises((ValueError, Refused)): health.age(value, self.current)
        with self.assertRaisesRegex(Refused, 'unavailable'):
            health.recovery_start({'file': 'unknown.dump'})

    def test_restore_requires_explicit_full_restore_evidence(self):
        record = self.record()
        self.assertTrue(health.collect(self.root, self.current)['restore_overdue'])
        atomic_json(self.state / 'known-good.json', dict(evidence='isolated full restore checks',
                    file=record['file'], sha256=record['sha256'], restored_at='2026-09-27T01:00:00+00:00'))
        self.assertFalse(health.collect(self.root, self.current)['restore_overdue'])

    def test_postdump_watchdog_cannot_hide_behind_successful_dump(self):
        app = '143-backup-' + 'a' * 32
        run = self.state / app
        (run / 'postdump').mkdir(parents=True)
        atomic_json(run / 'result.json', {'status': 'completed'})
        for phase in ('verification', 'upload'):
            for heartbeat, result, expected in [(None, None, True),
                    ('2026-09-27T11:59:59+00:00', None, False),
                    ('2026-09-27T11:58:00+00:00', None, True),
                    ('2026-09-27T11:59:59+00:00', 'failed', True)]:
                with self.subTest(phase=phase, heartbeat=heartbeat, result=result):
                    atomic_json(self.state / 'pending.json', dict(phase=phase, app_name=app,
                                runtime='postdump', started_at='2026-09-27T11:30:00+00:00'))
                    hp, rp = run / 'postdump' / 'heartbeat.json', run / 'postdump' / 'result.json'
                    hp.unlink(missing_ok=True)
                    rp.unlink(missing_ok=True)
                    if heartbeat: atomic_json(hp, {'at': heartbeat})
                    if result: atomic_json(rp, {'status': result})
                    self.assertEqual(health.collect(self.root, self.current)['watchdog_stale'], expected)

    def test_orphaned_attempt_includes_postdump_failure(self):
        atomic_json(self.state / 'attempt-test.json', dict(action='backup', status='running',
                    started_at='2026-09-27T11:50:00+00:00'))
        run = self.state / ('143-backup-' + 'a' * 32) / 'postdump'
        run.mkdir(parents=True)
        atomic_json(run / 'result.json', dict(status='failed', cleanup_verified=True,
                    at='2026-09-27T11:51:00+00:00'))
        self.assertTrue(health.collect(self.root, self.current)['backup_failed'])

    def test_live_swap_and_receipt_resources_remain_visible_after_success(self):
        phases = {'upload_completed': {'at': '2026-09-27T11:59:00+00:00',
                                      'resources': {'db_swap_bytes': 7, 'swap_bytes': 10}}}
        self.record(timeline={'recovery_point_basis': 'pre_dump_lower_bound',
                    'dump_started_at': '2026-09-27T11:30:00+00:00', 'resources': phases})
        for used, high in [(health.GIB // 4, False), (health.GIB // 4 + 1, True)]:
            with self.subTest(used=used), mock.patch.object(health, 'swap_usage', return_value=used):
                report = health.collect(self.root, self.current)
            self.assertEqual(report['swap_bytes'], used)
            self.assertEqual(report['swap_high'], high)
            self.assertEqual(report['latest_backup_resources'], phases)

    def test_swap_reader_rejects_invalid_kernel_sample(self):
        for text, expected in [('SwapTotal: 1024 kB\nSwapFree: 512 kB\n', 512 * 1024),
                               ('SwapTotal: 0 kB\nSwapFree: 0 kB\n', 0),
                               ('SwapTotal: 0 kB\nSwapFree: 1 kB\n', None)]:
            with self.subTest(text=text), mock.patch.object(Path, 'read_text', return_value=text):
                # setUp mocks the call site for platform-independent health tests.
                real = self._real_swap_usage
                if expected is None:
                    with self.assertRaises(Refused): real()
                else:
                    self.assertEqual(real(), expected)


if __name__ == '__main__':
    unittest.main(verbosity=2)
