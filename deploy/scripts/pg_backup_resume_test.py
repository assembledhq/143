#!/usr/bin/env python3
"""Attended upload recovery against private files and simulated Docker/S3."""
import argparse
import contextlib
import copy
import hashlib
import io
import json
import os
from pathlib import Path
import tempfile
import unittest
from unittest import mock

from pg_backup_policy_test import policy
import pg_backup_runtime as runtime
from pg_backup_runtime_test import safe_resources
from pg_backup_state import Refused, atomic_json, identity, read_json, sha256


class ResumeTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name).resolve()
        patch = mock.patch.dict(os.environ, BACKUP_DIR=str(self.root), BACKUP_LOCK_TIMEOUT_SECONDS='0')
        patch.start(); self.addCleanup(patch.stop)
        self.p = policy.Policy()
        self.p.storage = dict(bucket='example-backups', region='us-east-1', access_key_id='private', secret_access_key='private')
        self.p.state.mkdir(mode=0o700)
        self.old_app = '143-backup-' + 'a' * 32
        self.dump = self.p.state / self.old_app
        self.old_run = self.dump / 'postdump'
        self.old_run.mkdir(parents=True, mode=0o700)
        self.archive = self.root / ('onefortythree-20260927-152700-' + 'b' * 32 + '.dump')
        self.archive.write_bytes(b'completed preserved dump')
        self.archive.chmod(0o600)
        self.prior = self.root / 'onefortythree-20260927-122839.dump'
        self.prior.write_bytes(b'protected older dump')
        self.prior.chmod(0o600)
        self.file_id = identity(self.archive)
        self.hash = hashlib.sha256(self.archive.read_bytes()).hexdigest()
        self.timeline = dict(dump_started_at='2026-09-27T15:27:01+00:00',
                             dump_completed_at='2026-09-27T15:51:13+00:00',
                             structural_verified_at='2026-09-27T15:51:14+00:00',
                             local_sha256_at='2026-09-27T15:51:36+00:00',
                             recovery_point_basis='pre_dump_lower_bound', full_restore={'status': 'not_verified'})
        self.pending = dict(phase='upload', runtime='postdump', app_name=self.old_app, file=self.archive.name,
                            started_at=self.timeline['dump_started_at'], identity=self.file_id, sha256=self.hash,
                            timeline=self.timeline, database_bytes=1000)
        self.marker = self.p.state / 'pending.json'
        atomic_json(self.marker, self.pending)
        atomic_json(self.dump / 'ownership.json', dict(app=self.old_app, partial=self.file_id))
        atomic_json(self.dump / 'result.json', dict(status='completed', cleanup_verified=True))
        atomic_json(self.old_run / 'ownership.json', dict(app=self.old_app, creation_started=True, creation_complete=True))
        atomic_json(self.old_run / 'result.json', dict(status='failed', cleanup_verified=True))
        self.old_evidence = {p: p.read_bytes() for p in self.dump.rglob('*.json')}
        self.objects, self.calls = {}, []
        self.remote_failure = False
        self.upload_failure = False
        self.upload_ambiguous = False
        self.hash_failure = False
        self.old_reader = False
        self.backend = False
        self.version = policy.AWS_VERSION
        self.generation = dict(id='d' * 64, pid=123, started_at='db-start', restarts=0)
        self.db = dict(Image='sha256:db-image', Config={'Volumes': {'/var/lib/postgresql': {}}})
        patches = [mock.patch.object(runtime, 'invoke', side_effect=self.docker),
                   mock.patch.object(runtime.Guard, 'database', return_value=(self.db, self.generation)),
                   mock.patch.object(runtime.Guard, 'query', side_effect=lambda *a: '[123]' if self.backend else '[]'),
                   mock.patch.object(runtime.TransferGuard, 'client', autospec=True, side_effect=self.client),
                   mock.patch.object(runtime, 'proc_identity', return_value=['boot', 'start']),
                   mock.patch.object(runtime, 'resources', side_effect=lambda *a: dict(safe_resources(), monotonic=runtime.time.monotonic())),
                   mock.patch.object(runtime, 'sha256', side_effect=lambda path, **kwargs: '0' * 64 if self.hash_failure else sha256(path)),
                   mock.patch.object(runtime, 'run_guard', side_effect=self.supervise),
                   mock.patch.object(self.p, 'database_bytes', side_effect=AssertionError('must not rescan database')),
                   mock.patch.object(self.p, 'dump_archive', side_effect=AssertionError('must not dump')),
                   mock.patch.object(self.p, 'prune', side_effect=AssertionError('must not prune'))]
        for patch in patches: patch.start(); self.addCleanup(patch.stop)
        redirect = contextlib.redirect_stdout(io.StringIO())
        redirect.__enter__(); self.addCleanup(redirect.__exit__, None, None, None)

    def docker(self, args, **kwargs):
        if args[:3] == ['docker', 'context', 'inspect']: return 'unix:///var/run/docker.sock'
        if args[:3] == ['docker', 'container', 'ls']: return 'c' * 64 if self.old_reader else ''
        raise AssertionError('unexpected command: ' + str(args))

    def client(self, guard, stage, args, **kwargs):
        self.calls.append((stage, args))
        if stage == 'structural': return ''
        if stage == 'upload':
            if self.upload_failure: raise Refused('upload failed')
            key = args[args.index('cp') + 2].split('s3://example-backups/', 1)[1]
            self.objects[key] = dict(Key=key, Size=self.file_id['bytes'], ETag='opaque-etag', LastModified='2026-09-28T02:30:00+00:00')
            if self.upload_ambiguous: raise Refused('upload acknowledgement lost')
            return ''
        if '--version' in args: return self.version + ' Python/3 Linux'
        if self.remote_failure: raise Refused('listing denied')
        key = args[args.index('--prefix') + 1]
        return json.dumps({'Contents': [self.objects[key]] if key in self.objects else []})

    def supervise(self, guard):
        self.guard = guard
        self.assertIsNotNone(self.p.lock_fd)
        guard.run.mkdir(mode=0o700)
        guard.supervise()
        result = read_json(guard.result)
        if result['status'] != 'completed': raise Refused(result['error'])
        return result

    def resume(self, **kwargs):
        args = argparse.Namespace(original_database_bytes=None, size_evidence=None)
        for k, v in kwargs.items(): setattr(args, k, v)
        with self.p.locked(): self.p.resume_upload(args)

    def assert_preserved(self):
        self.assertEqual(identity(self.archive), self.file_id)
        self.assertEqual(self.prior.read_bytes(), b'protected older dump')
        for path, content in self.old_evidence.items(): self.assertEqual(path.read_bytes(), content)

    def test_resume_uses_new_object_without_changing_recovery_point_or_old_evidence(self):
        self.resume()
        self.assertFalse(self.marker.exists())
        receipt = read_json(self.p.state / (self.archive.name + '.json'))
        self.assertEqual(receipt['sha256'], self.hash)
        self.assertEqual(receipt['identity'], self.file_id)
        self.assertEqual(receipt['database_bytes'], 1000)
        self.assertEqual(receipt['timeline']['dump_started_at'], self.timeline['dump_started_at'])
        self.assertEqual(receipt['timeline']['full_restore'], {'status': 'not_verified'})
        self.assertTrue(receipt['remote']['key'].startswith('postgres/resumed/'))
        self.assertNotEqual(receipt['remote']['key'], 'postgres/' + self.archive.name)
        self.assertEqual(len([stage for stage, _ in self.calls if stage == 'upload']), 1)
        self.assertEqual(self.p.remote(self.archive.name, aws=self.guard.aws, key=receipt['remote']['key']), receipt['remote'])
        self.assertEqual(read_json(self.guard.run.parent / 'pending-before.json'), self.pending)
        self.assert_preserved()

    def test_independent_import_uses_recorded_resumed_key(self):
        self.resume()
        receipt = read_json(self.p.state / (self.archive.name + '.json'))
        args = argparse.Namespace(file=self.archive.name, etag=receipt['remote']['etag'],
                                  last_modified=receipt['remote']['last_modified'], sha256=self.hash,
                                  version_id='downloaded-version', evidence='independent checksum download')
        with mock.patch.object(self.p, 'aws', side_effect=self.guard.aws), \
                mock.patch.object(self.p, 'structural_check'), mock.patch.object(self.p, 'database_bytes', return_value=1000):
            with self.p.locked(): self.p.import_receipt(args)
        updated = read_json(self.p.state / (self.archive.name + '.json'))
        self.assertEqual(updated['remote'], receipt['remote'])
        self.assertEqual(updated['integrity']['kind'], 'operator_sha256')
        self.assertEqual(updated['timeline']['dump_started_at'], self.timeline['dump_started_at'])

    def test_recovery_refuses_ambiguous_or_unproven_state_without_upload(self):
        for fault in ('cleanup', 'dump_result', 'dump_identity', 'reader', 'backend', 'remote', 'listing', 'hash', 'version', 'creation'):
            with self.subTest(fault=fault):
                atomic_json(self.marker, self.pending)
                for path, content in self.old_evidence.items(): path.write_bytes(content)
                self.calls = []
                self.objects = {}
                self.old_reader = fault == 'reader'
                self.backend = fault == 'backend'
                self.remote_failure = fault == 'listing'
                self.hash_failure = fault == 'hash'
                self.version = 'aws-cli/9.0.0' if fault == 'version' else policy.AWS_VERSION
                if fault == 'cleanup': atomic_json(self.old_run / 'result.json', dict(status='failed', cleanup_verified=False))
                if fault == 'dump_result': atomic_json(self.dump / 'result.json', dict(status='failed', cleanup_verified=True))
                if fault == 'dump_identity': atomic_json(self.dump / 'ownership.json', dict(app=self.old_app, partial=dict(self.file_id, inode=-1)))
                if fault == 'creation': atomic_json(self.old_run / 'ownership.json', dict(app=self.old_app, creation_started=True, creation_complete=False))
                if fault == 'remote':
                    key = 'postgres/' + self.archive.name
                    self.objects[key] = dict(Key=key, Size=self.file_id['bytes'], ETag='existing', LastModified='earlier')
                with self.assertRaises(Refused): self.resume()
                self.assertFalse(any(stage == 'upload' for stage, _ in self.calls))
                self.assertEqual(read_json(self.marker), self.pending)
                self.assertFalse((self.p.state / (self.archive.name + '.json')).exists())
                self.assertEqual(identity(self.archive), self.file_id)

    def test_failed_upload_preserves_new_marker_and_original_dump_for_another_attended_attempt(self):
        self.upload_failure = True
        with self.assertRaisesRegex(Refused, 'upload failed'): self.resume()
        pending = read_json(self.marker)
        self.assertNotEqual(pending['app_name'], self.old_app)
        self.assertEqual(pending['dump_app'], self.old_app)
        failed_key = pending['remote_key']
        self.assertTrue(read_json(self.guard.result)['cleanup_verified'])
        self.assertFalse((self.p.state / (self.archive.name + '.json')).exists())
        self.assert_preserved()
        self.upload_failure = False
        self.resume()
        receipt = read_json(self.p.state / (self.archive.name + '.json'))
        self.assertNotEqual(receipt['remote']['key'], failed_key)
        self.assertEqual(receipt['timeline']['dump_started_at'], self.timeline['dump_started_at'])

    def test_lost_upload_ack_never_qualifies_or_overwrites_a_completed_object(self):
        self.upload_ambiguous = True
        with self.assertRaisesRegex(Refused, 'acknowledgement'): self.resume()
        pending = read_json(self.marker)
        existing = copy.deepcopy(self.objects)
        self.upload_ambiguous = False
        with self.assertRaisesRegex(Refused, 'prior object exists'): self.resume()
        self.assertEqual(self.objects, existing)
        self.assertEqual(read_json(self.marker), pending)
        self.assertFalse((self.p.state / (self.archive.name + '.json')).exists())
        self.assert_preserved()

    def test_legacy_size_requires_explicit_original_evidence_and_cannot_override_new_marker(self):
        with self.assertRaisesRegex(Refused, 'cannot override'): self.resume(original_database_bytes=900, size_evidence='admission log')
        legacy = dict(self.pending)
        del legacy['database_bytes']
        atomic_json(self.marker, legacy)
        for args in ({}, {'original_database_bytes': 900}, {'original_database_bytes': 0, 'size_evidence': 'admission log'}):
            with self.subTest(args=args), self.assertRaises(Refused): self.resume(**args)
            self.assertEqual(read_json(self.marker), legacy)
        self.resume(original_database_bytes=900, size_evidence='/root/canary/policy.log admission database_bytes')
        receipt = read_json(self.p.state / (self.archive.name + '.json'))
        self.assertEqual(receipt['database_bytes'], 900)
        self.assertEqual(receipt['timeline']['database_size_evidence'], '/root/canary/policy.log admission database_bytes')
        self.assert_preserved()

    def test_refusal_before_new_attempt_for_invalid_pending_or_changed_archive(self):
        for changes in ({'phase': 'verification'}, {'file': '../wrong.dump'}, {'app_name': '../../wrong'},
                        {'identity': dict(self.file_id, inode=-1)}, {'sha256': 'wrong'},
                        {'started_at': 'newer-than-original'}):
            with self.subTest(changes=changes):
                pending = dict(self.pending, **changes)
                atomic_json(self.marker, pending)
                with self.assertRaises(Refused): self.resume()
                self.assertEqual(read_json(self.marker), pending)
                self.assertEqual(set(self.p.state.glob('143-backup-*')), {self.dump})

    def test_cli_requires_explicit_attendance_before_creating_attempt(self):
        import subprocess
        import sys
        for enabled, attended, observer in [('false', 'true', 'operator'), ('true', 'false', 'operator'), ('true', 'true', '')]:
            with self.subTest(enabled=enabled, attended=attended, observer=observer):
                root = self.root / 'must-not-create'
                env = dict(os.environ, BACKUP_DIR=str(root), BACKUP_ENABLED=enabled,
                           BACKUP_ATTENDED=attended, BACKUP_OBSERVER=observer)
                result = subprocess.run([sys.executable, '-B', str(Path(policy.__file__)), 'resume-upload'],
                                        env=env, capture_output=True, timeout=5)
                self.assertNotEqual(result.returncode, 0)
                self.assertFalse(root.exists())

    def test_remote_key_validation_rejects_unrelated_prefix_or_filename(self):
        for key in ('other/key', 'postgres/resumed/not-a-uuid/' + self.archive.name,
                    'postgres/resumed/' + 'c' * 32 + '/another.dump'):
            with self.subTest(key=key), self.assertRaisesRegex(Refused, 'invalid archive object key'):
                self.p.remote(self.archive.name, aws=self.guard.aws if hasattr(self, 'guard') else mock.Mock(), key=key)


if __name__ == '__main__':
    unittest.main(verbosity=2)
