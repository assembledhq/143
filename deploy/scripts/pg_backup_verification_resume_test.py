#!/usr/bin/env python3
"""Completed dump recovery, including interruption between rename and upload."""
import argparse
import unittest
from unittest import mock

import pg_backup_resume_test as resume_tests
from pg_backup_state import Refused, atomic_json, identity, read_json
import pg_backup_runtime as runtime


class VerificationResumeTests(unittest.TestCase):
    docker = resume_tests.ResumeTests.docker
    client = resume_tests.ResumeTests.client
    supervise = resume_tests.ResumeTests.supervise
    assert_preserved = resume_tests.ResumeTests.assert_preserved

    def setUp(self):
        resume_tests.ResumeTests.setUp(self)
        self.partial = self.root / ('.' + self.archive.name + '.partial.' + 'c' * 32)
        self.pending = dict(self.pending, phase='verification', partial=self.partial.name)
        for field in ('identity', 'sha256'):
            self.pending.pop(field)
        self.pending['timeline'] = {k: v for k, v in self.timeline.items() if k != 'local_sha256_at'}
        atomic_json(self.marker, self.pending)
        owner = dict(app=self.old_app, partial=self.file_id, database=self.generation,
                     owner_pid=99, owner_identity=['prior-boot', 'prior-start'])
        atomic_json(self.dump / 'ownership.json', owner)
        atomic_json(self.old_run / 'ownership.json', dict(owner, creation_started=False, creation_complete=True,
                                                        client_name=self.old_app + '-structural'))
        self.old_evidence = {p: p.read_bytes() for p in self.dump.rglob('*.json')}

    def resume(self, **kwargs):
        args = argparse.Namespace(original_database_bytes=None, size_evidence=None)
        for key, value in kwargs.items(): setattr(args, key, value)
        with self.p.locked(): self.p.resume_verification(args)

    def test_published_file_recovers_without_redumping_or_changing_recovery_point(self):
        self.resume()
        self.assertFalse(self.marker.exists())
        receipt = read_json(self.p.state / (self.archive.name + '.json'))
        self.assertEqual(receipt['identity'], self.file_id)
        self.assertEqual(receipt['sha256'], self.hash)
        self.assertEqual(receipt['database_bytes'], 1000)
        self.assertEqual(receipt['timeline']['dump_started_at'], self.timeline['dump_started_at'])
        self.assertEqual(receipt['timeline']['full_restore'], {'status': 'not_verified'})
        self.assertEqual(receipt['integrity']['kind'], 'checksum_upload')
        self.assertEqual([s for s, _ in self.calls if s == 'upload'], ['upload'])
        self.assert_preserved()

    def test_partial_is_verified_then_published(self):
        self.archive.rename(self.partial)
        self.resume()
        self.assertFalse(self.partial.exists())
        self.assertFalse(self.marker.exists())
        self.assertEqual(self.archive.read_bytes(), b'completed preserved dump')
        receipt = read_json(self.p.state / (self.archive.name + '.json'))
        self.assertEqual(receipt['identity'], identity(self.archive))
        self.assertEqual(receipt['sha256'], self.hash)

    def test_legacy_size_requires_original_evidence_and_recorded_size_cannot_be_overridden(self):
        with self.assertRaisesRegex(Refused, 'cannot override'):
            self.resume(original_database_bytes=999, size_evidence='wrong')
        self.pending.pop('database_bytes')
        atomic_json(self.marker, self.pending)
        with self.assertRaisesRegex(Refused, 'original database size evidence'): self.resume()
        self.resume(original_database_bytes=1000, size_evidence='original admission event')
        receipt = read_json(self.p.state / (self.archive.name + '.json'))
        self.assertEqual(receipt['database_bytes'], 1000)
        self.assertEqual(receipt['timeline']['database_size_evidence'], 'original admission event')

    def test_unproven_state_never_uploads_or_clears_marker(self):
        for fault in ('cleanup', 'dump_failed', 'changed_file', 'live_owner', 'reader', 'backend',
                      'remote', 'upload_evidence', 'creation', 'version'):
            with self.subTest(fault=fault):
                for path, content in self.old_evidence.items(): path.write_bytes(content)
                (self.old_run / 'upload.json').unlink(missing_ok=True)
                self.calls, self.objects = [], {}
                self.old_reader, self.backend = fault == 'reader', fault == 'backend'
                owner = read_json(self.old_run / 'ownership.json')
                if fault == 'cleanup': atomic_json(self.old_run / 'result.json', dict(status='failed', cleanup_verified=False))
                if fault == 'dump_failed': atomic_json(self.dump / 'result.json', dict(status='failed', cleanup_verified=True))
                if fault == 'changed_file': owner['partial'] = dict(self.file_id, bytes=-1)
                if fault == 'live_owner': owner['owner_identity'] = ['boot', 'start']
                if fault == 'creation': owner.update(creation_started=True, creation_complete=False)
                if fault == 'upload_evidence': atomic_json(self.old_run / 'upload.json', {})
                if fault == 'remote':
                    key = 'postgres/' + self.archive.name
                    self.objects[key] = dict(Key=key, Size=self.file_id['bytes'], ETag='existing', LastModified='earlier')
                self.version = 'aws-cli/9.0.0' if fault == 'version' else self.p.aws_cli_version
                atomic_json(self.old_run / 'ownership.json', owner)
                with self.assertRaises(Refused): self.resume()
                self.assertFalse(any(stage == 'upload' for stage, _ in self.calls))
                self.assertEqual(read_json(self.marker), self.pending)
                self.assertEqual(identity(self.archive), self.file_id)

    def test_transient_checksum_stop_can_resume_next_time(self):
        with mock.patch.object(runtime.VerificationRecoveryGuard, 'checksum', side_effect=Refused('temporary pressure')):
            with self.assertRaisesRegex(Refused, 'temporary pressure'): self.resume()
        self.assertEqual(read_json(self.marker), self.pending)
        self.assertTrue(read_json(self.guard.result)['cleanup_verified'])
        self.resume()
        self.assertFalse(self.marker.exists())
        self.assert_preserved()

    def test_upload_boundary_stop_records_recoverable_marker_before_enforcement(self):
        checkpoint = runtime.VerificationRecoveryGuard.checkpoint
        def stop(guard, name, **kwargs):
            if name == 'upload_started': raise Refused('temporary pressure')
            return checkpoint(guard, name, **kwargs)
        with mock.patch.object(runtime.VerificationRecoveryGuard, 'checkpoint', stop):
            with self.assertRaisesRegex(Refused, 'temporary pressure'): self.resume()
        pending = read_json(self.marker)
        self.assertEqual(pending['phase'], 'upload')
        self.assertEqual(pending['identity'], self.file_id)
        self.assertEqual(pending['sha256'], self.hash)
        self.assertEqual(pending['dump_app'], self.old_app)
        resume_tests.ResumeTests.resume(self)
        self.assertFalse(self.marker.exists())
        self.assert_preserved()

    def test_scheduled_backup_recovers_before_admitting_a_fresh_dump(self):
        # Exercise the real verification/upload recovery and then stop at fresh
        # admission: no database dump may start while the old marker remains.
        sentinel = Refused('fresh admission reached')
        def inventory():
            self.assertFalse(self.marker.exists())
            self.assertTrue((self.p.state / (self.archive.name + '.json')).exists())
            raise sentinel
        with self.p.locked(), mock.patch.object(self.p, 'inventory', side_effect=inventory):
            with self.assertRaisesRegex(Refused, 'fresh admission reached'):
                self.p.backup(argparse.Namespace(bootstrap=False, canary=False))
        self.assert_preserved()


if __name__ == '__main__': unittest.main()
