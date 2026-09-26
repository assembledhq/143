#!/usr/bin/env python3
"""Behavior tests with temporary private files and fake DB/S3 transports."""
import argparse
import contextlib
import hashlib
import importlib.util
import io
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import types
import unittest
from unittest import mock

sys.dont_write_bytecode = True

SCRIPT_DIR = Path(__file__).resolve().parent
SPEC = importlib.util.spec_from_file_location('policy', SCRIPT_DIR / 'pg-backup-policy.py')
policy = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(policy)


class FakePolicy(policy.Policy):
    def __init__(self):
        super().__init__()
        self.objects = {}
        self.operations = []
        self.remote_failure = False
        self.upload_failure = False
        self.wrong_upload_size = False
        self.invalid_dump = False

    def load_storage(self):
        return {'bucket': 'example-backups'}

    def remote(self, name):
        self.operations.append(('list', name))
        if self.remote_failure:
            raise policy.Refused('listing denied')
        return self.objects.get(name)

    def database_bytes(self):
        return 100

    def db_env(self):
        return {}

    def structural_check(self, path):
        if self.invalid_dump:
            raise policy.Refused('invalid dump')

    def aws(self, args, archive=None, timeout=120):
        if args == ['--version']:
            return policy.AWS_VERSION + ' Python/3 Linux'
        self.operations.append(('upload', archive.name, args))
        if self.upload_failure:
            raise policy.Refused('upload failed')
        self.objects[archive.name] = self.object_for(archive)
        if self.wrong_upload_size:
            self.objects[archive.name]['bytes'] += 1
        return ''

    @staticmethod
    def object_for(path):
        return dict(bucket='example-backups', key='postgres/' + path.name,
                    bytes=path.stat().st_size, etag='"opaque-multipart-etag"',
                    last_modified='2026-09-26T18:25:31+00:00')


class PolicyTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name).resolve()
        patch = mock.patch.dict(os.environ, {'BACKUP_DIR': str(self.root), 'BACKUP_LOCK_TIMEOUT_SECONDS': '0.05'}, clear=False)
        patch.start()
        self.addCleanup(patch.stop)
        self.p = FakePolicy()
        self.p.state.mkdir(mode=0o700)
        output = contextlib.redirect_stdout(io.StringIO())
        output.__enter__()
        self.addCleanup(output.__exit__, None, None, None)
        space = mock.patch.object(policy.os, 'statvfs', return_value=types.SimpleNamespace(f_bavail=100 * policy.GIB, f_frsize=1))
        space.start()
        self.addCleanup(space.stop)

    def archive(self, day, content=b'verified dump'):
        path = self.root / f'onefortythree-202001{day:02d}-060001.dump'
        path.write_bytes(content)
        path.chmod(0o600)
        remote = self.p.object_for(path)
        self.p.objects[path.name] = remote
        self.p.record(path, remote, hashlib.sha256(content).hexdigest(),
                      dict(kind='operator_sha256', version_id='version-' + str(day), evidence='independent stream'),
                      100, policy.identity(path))
        return path

    def canary(self):
        policy.atomic_json(self.p.state / 'checksum-canary.json', dict(cli_image=policy.AWS_IMAGE,
                           cli_version=policy.AWS_VERSION, evidence='isolated checksum canary'))

    def dump(self, args=None, fail=False):
        def fake_invoke(command, **kwargs):
            self.assertIn('pg_dump', command)
            self.assertIn('PGAPPNAME', command)
            kwargs['stdout'].write(b'new verified dump')
            if fail:
                raise policy.Refused('dump client failed')
            return ''
        with mock.patch.object(policy, 'invoke', side_effect=fake_invoke):
            self.p.backup(args or argparse.Namespace(bootstrap=False, canary=False))

    def test_retention_floor_and_known_good_slot(self):
        for pin, expected in [(None, [2, 3]), (1, [1, 3]), (3, [2, 3])]:
            with self.subTest(pin=pin):
                for f in self.root.glob('*.dump'): f.unlink()
                pin_path = self.p.state / 'known-good.json'
                pin_path.unlink(missing_ok=True)
                paths = {n: self.archive(n) for n in (1, 2, 3)}
                if pin:
                    policy.atomic_json(pin_path, dict(file=paths[pin].name, sha256=policy.sha256(paths[pin]), evidence='full isolated restore'))
                self.canary()
                self.p.prune()
                self.assertEqual(sorted(p.name for p in self.root.glob('*.dump')), [paths[n].name for n in expected])

    def test_old_single_copy_is_never_age_pruned(self):
        old = self.archive(1)
        self.p.prune()
        self.assertEqual(old.read_bytes(), b'verified dump')

    def test_invalid_evidence_or_remote_failure_preserves_every_copy(self):
        for defect in ['missing', 'malformed', 'identity', 'etag', 'size', 'missing_remote', 'listing', 'canary', 'pin', 'skipped_sync', 'unchecked_upload']:
            with self.subTest(defect=defect), tempfile.TemporaryDirectory() as tmp:
                with mock.patch.dict(os.environ, BACKUP_DIR=tmp): p = FakePolicy()
                p.state.mkdir(mode=0o700)
                old_p, old_root = self.p, self.root
                self.p, self.root = p, Path(tmp)
                try:
                    paths = [self.archive(n) for n in (1, 2, 3)]
                    self.canary()
                    rpath = p.state / (paths[0].name + '.json')
                    if defect == 'missing': rpath.unlink()
                    if defect == 'malformed': rpath.write_text('{')
                    if defect == 'identity': paths[0].write_bytes(b'changed')
                    if defect == 'etag': p.objects[paths[0].name] = dict(p.objects[paths[0].name], etag='changed')
                    if defect == 'size': p.objects[paths[0].name] = dict(p.objects[paths[0].name], bytes=999)
                    if defect == 'missing_remote': del p.objects[paths[0].name]
                    if defect == 'listing': p.remote_failure = True
                    if defect == 'canary': (p.state / 'checksum-canary.json').unlink()
                    if defect == 'pin': policy.atomic_json(p.state / 'known-good.json', dict(file='missing.dump', evidence='restore'))
                    if defect in ('skipped_sync', 'unchecked_upload'):
                        r = policy.read_json(rpath)
                        r['integrity'] = dict(kind='sync' if defect == 'skipped_sync' else 'checksum_upload')
                        policy.atomic_json(rpath, r)
                    with self.assertRaises((policy.Refused, ValueError)): p.prune()
                    self.assertEqual(sorted(x.name for x in p.root.glob('*.dump')), [x.name for x in paths])
                finally:
                    self.p, self.root = old_p, old_root

    def test_capacity_reserve_and_database_growth(self):
        self.archive(1, b'x' * 100)
        self.archive(2, b'x' * 120)
        records = self.p.inventory()
        for database, available, accepted, estimate in [(100, 20 * policy.GIB + 150, True, 150),
                (100, 20 * policy.GIB + 149, False, 150), (200, 20 * policy.GIB + 299, False, 300),
                (200, 20 * policy.GIB + 300, True, 300)]:
            with self.subTest(database=database, available=available):
                with mock.patch.object(policy.os, 'statvfs', return_value=types.SimpleNamespace(f_bavail=available, f_frsize=1)):
                    if accepted:
                        self.assertEqual(self.p.admission(records, database)['estimated_dump_bytes'], estimate)
                    else:
                        with self.assertRaisesRegex(policy.Refused, 'insufficient'): self.p.admission(records, database)
        self.assertEqual(len(list(self.root.glob('*.dump'))), 2)

    def test_successful_cycle_uploads_only_new_file_then_keeps_two(self):
        old = self.archive(1)
        latest = self.archive(2)
        self.canary()
        self.dump()
        self.assertFalse(old.exists())
        self.assertTrue(latest.exists())
        uploads = [x for x in self.p.operations if x[0] == 'upload']
        self.assertEqual(len(uploads), 1)
        self.assertIn('--checksum-algorithm', uploads[0][2])
        self.assertIn('CRC64NVME', uploads[0][2])
        self.assertNotIn('sync', uploads[0][2])
        records = self.p.inventory()
        self.assertEqual(len(records), 2)
        self.assertEqual(records[0]['integrity']['kind'], 'checksum_upload')
        self.assertFalse((self.p.state / 'pending.json').exists())
        self.assertEqual(list(self.root.glob('.*.partial.*')), [])

    def test_canary_leaves_three_until_independent_approval(self):
        self.archive(1)
        self.archive(2)
        self.dump(argparse.Namespace(bootstrap=False, canary=True))
        self.assertEqual(len(self.p.inventory()), 3)
        with self.assertRaisesRegex(policy.Refused, 'canary'): self.p.prune()
        self.assertEqual(len(self.p.inventory()), 3)

    def test_bootstrap_is_explicit_and_never_exempts_unknown_archives(self):
        with self.assertRaisesRegex(policy.Refused, 'two verified archives'):
            self.dump(argparse.Namespace(bootstrap=False, canary=True))
        self.dump(argparse.Namespace(bootstrap=True, canary=True))
        self.assertEqual(len(self.p.inventory()), 1)
        unknown = self.root / 'onefortythree-20200101-060001.dump'
        unknown.write_bytes(b'legacy')
        unknown.chmod(0o600)
        with self.assertRaisesRegex(policy.Refused, 'unverified'):
            self.dump(argparse.Namespace(bootstrap=True, canary=True))
        self.assertTrue(unknown.exists())

    def test_capacity_refusal_prevents_dump_and_upload(self):
        self.archive(1)
        self.archive(2)
        self.canary()
        with mock.patch.object(policy.os, 'statvfs', return_value=types.SimpleNamespace(f_bavail=20 * policy.GIB, f_frsize=1)):
            with self.assertRaisesRegex(policy.Refused, 'insufficient'): self.dump()
        self.assertFalse(any(x[0] == 'upload' for x in self.p.operations))
        self.assertFalse((self.p.state / 'pending.json').exists())
        self.assertEqual(len(self.p.inventory()), 2)

    def test_policy_rejects_duplicate_receipt_fields(self):
        path = self.p.state / 'duplicate.json'
        path.write_text('{"schema":1,"schema":2}')
        path.chmod(0o600)
        with self.assertRaisesRegex(policy.Refused, 'duplicate JSON'):
            policy.read_json(path)

    def test_failed_dump_preserves_partial_and_blocks_followup(self):
        paths = [self.archive(n) for n in (1, 2)]
        self.canary()
        with self.assertRaisesRegex(policy.Refused, 'client failed'): self.dump(fail=True)
        self.assertEqual([p.read_bytes() for p in paths], [b'verified dump'] * 2)
        partials = list(self.root.glob('.*.partial.*'))
        self.assertEqual(len(partials), 1)
        self.assertEqual(partials[0].read_bytes(), b'new verified dump')
        with self.assertRaisesRegex(policy.Refused, 'incomplete operation'): self.p.prune()
        self.assertTrue(partials[0].exists())

    def test_failed_upload_or_verification_cannot_qualify_archive(self):
        for defect in ['upload_failure', 'wrong_upload_size', 'invalid_dump']:
            with self.subTest(defect=defect):
                setattr(self.p, defect, True)
                paths = [self.archive(n) for n in (1, 2)]
                self.canary()
                with self.assertRaises(policy.Refused): self.dump()
                self.assertTrue(all(p.exists() for p in paths))
                self.assertEqual(len(list(self.p.state.glob('*.dump.json'))), 2)
                self.assertTrue((self.p.state / 'pending.json').exists())
                # Fixture reconciliation only; production has no age cleanup.
                for f in self.root.glob('*.dump'): f.unlink()
                for f in self.root.glob('.*.partial.*'): f.unlink()
                (self.p.state / 'pending.json').unlink()
                setattr(self.p, defect, False)

    def test_partial_without_marker_also_blocks_admission(self):
        partial = self.root / '.onefortythree-old.dump.partial.abandoned'
        partial.write_bytes(b'possibly still being written')
        os.utime(partial, (1, 1))
        with self.assertRaisesRegex(policy.Refused, 'unreconciled partial'): self.p.no_pending()
        self.assertTrue(partial.exists())

    def test_deletion_failure_is_observable_and_preserves_floor(self):
        paths = [self.archive(n) for n in (1, 2, 3)]
        self.canary()
        with mock.patch.object(Path, 'unlink', side_effect=PermissionError('test refusal')):
            with self.assertRaises(PermissionError): self.p.prune()
        self.assertEqual([p.exists() for p in paths], [True, True, True])

    def test_independent_receipt_requires_exact_hash_and_metadata(self):
        path = self.archive(1)
        receipt = self.p.state / (path.name + '.json')
        receipt.unlink()
        remote = self.p.objects[path.name]
        args = argparse.Namespace(file=path.name, sha256=policy.sha256(path), etag=remote['etag'],
                 last_modified=remote['last_modified'], version_id='independent-version', evidence='CloudShell SHA-256')
        for field, wrong in [('sha256', '0' * 64), ('etag', 'other'), ('last_modified', 'other')]:
            with self.subTest(field=field):
                broken = argparse.Namespace(**dict(vars(args), **{field: wrong}))
                with self.assertRaises(policy.Refused): self.p.import_receipt(broken)
                self.assertFalse(receipt.exists())
        self.p.import_receipt(args)
        self.assertEqual(self.p.inventory()[0]['integrity']['version_id'], 'independent-version')
        self.assertFalse(any(x[0] == 'upload' for x in self.p.operations))

    def test_change_after_hash_cannot_get_receipt(self):
        path = self.archive(1)
        receipt = self.p.state / (path.name + '.json')
        receipt.unlink()
        remote = self.p.objects[path.name]
        args = argparse.Namespace(file=path.name, sha256=policy.sha256(path), etag=remote['etag'],
                 last_modified=remote['last_modified'], version_id='v', evidence='independent')
        def changed():
            path.write_bytes(b'replacement')
            return 100
        with mock.patch.object(self.p, 'database_bytes', side_effect=changed):
            with self.assertRaisesRegex(policy.Refused, 'changed before receipt'): self.p.import_receipt(args)
        self.assertFalse(receipt.exists())

    def test_shared_lock_times_out_without_touching_archives(self):
        path = self.archive(1)
        code = '''import importlib.util, sys
sys.dont_write_bytecode=True
spec=importlib.util.spec_from_file_location('p',sys.argv[1]); p=importlib.util.module_from_spec(spec);spec.loader.exec_module(p)
with p.Policy().locked():
 print('locked',flush=True)
 sys.stdin.readline()
'''
        child = subprocess.Popen([sys.executable, '-c', code, str(SCRIPT_DIR / 'pg-backup-policy.py')],
                                 stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
        try:
            self.assertEqual(child.stdout.readline().strip(), 'locked')
            with self.assertRaisesRegex(policy.Refused, 'lock timeout'):
                with self.p.locked(): self.fail('second owner acquired lock')
            self.assertEqual(path.read_bytes(), b'verified dump')
            child.communicate('\n', timeout=5)
            self.assertEqual(child.returncode, 0)
            with self.p.locked(): pass
        finally:
            if child.poll() is None:
                child.kill()
            child.communicate()

    def test_restore_selects_verified_archive_and_preserves_failure_marker(self):
        paths = [self.archive(n) for n in (1, 2)]
        for status in [0, 42]:
            with self.subTest(status=status):
                with mock.patch.object(policy.subprocess, 'run', return_value=types.SimpleNamespace(returncode=status)) as call:
                    self.assertEqual(self.p.restore(), status)
                    self.assertEqual(call.call_args.kwargs['env']['BACKUP_ARCHIVE'], str(paths[1]))
                    self.assertTrue(call.call_args.args[0][1].endswith('restore-test-body.sh'))
                self.assertEqual((self.p.state / 'pending.json').exists(), status != 0)
        with self.assertRaisesRegex(policy.Refused, 'incomplete operation'): self.p.prune()

    def test_wrappers_refuse_disabled_invalid_and_unattended_before_work(self):
        for script, flag in [('pg-backup.sh', 'BACKUP_ENABLED'), ('restore-test.sh', 'RESTORE_TEST_ENABLED')]:
            for mode, status in [('false', 75), ('invalid', 1), ('true', 1)]:
                with self.subTest(script=script, mode=mode):
                    missing = self.root / 'not-created'
                    env = dict(os.environ, BACKUP_DIR=str(missing), BACKUP_ATTENDED='false', **{flag: mode})
                    r = subprocess.run(['bash', str(SCRIPT_DIR / script)], env=env, capture_output=True)
                    self.assertEqual(r.returncode, status, r.stderr)
                    self.assertFalse(missing.exists())

    def test_aws_transport_does_not_expose_credentials_or_use_sync(self):
        p = policy.Policy()
        p.storage = dict(bucket='test-backups', region='us-east-1', access_key_id='test-id', secret_access_key='literal-$secret')
        archive = self.archive(1)
        with mock.patch.object(policy, 'invoke', return_value='') as call:
            p.aws(['s3', 'cp', '/backup.dump', 's3://test-backups/postgres/new.dump', '--checksum-algorithm', 'CRC64NVME'], archive)
            args = call.call_args.args[0]
            self.assertIn(policy.AWS_IMAGE, args)
            self.assertNotIn('literal-$secret', ' '.join(args))
            self.assertEqual(call.call_args.kwargs['env']['AWS_SECRET_ACCESS_KEY'], 'literal-$secret')
            self.assertNotIn('sync', args)
            self.assertNotIn('--delete', args)

    def test_config_install_preserves_literal_credentials(self):
        target = self.root / 'storage.json'
        env = dict(os.environ, BACKUP_S3_BUCKET='test-backups', BACKUP_S3_REGION='us-east-1',
                   BACKUP_AWS_ACCESS_KEY_ID='test-id', BACKUP_AWS_SECRET_ACCESS_KEY="literal'$(not-executed)\nvalue")
        program = [sys.executable, str(SCRIPT_DIR / 'pg-backup-config.py')]
        generated = subprocess.check_output(program + ['generate'], env=env)
        subprocess.run(program + ['install', '--path', str(target)], input=generated, check=True)
        self.assertEqual(policy.read_json(target)['secret_access_key'], env['BACKUP_AWS_SECRET_ACCESS_KEY'])
        previous = target.read_bytes()
        bad = subprocess.run(program + ['install', '--path', str(target)], input=b'{}', capture_output=True)
        self.assertNotEqual(bad.returncode, 0)
        self.assertEqual(target.read_bytes(), previous)


if __name__ == '__main__':
    unittest.main(verbosity=2)
