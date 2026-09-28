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
import signal
import subprocess
import sys
import tempfile
import time
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

    def remote(self, name, aws=None, key=None):
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

    def test_operator_can_load_policy_and_health_by_absolute_path(self):
        # No PYTHONPATH or test-side sys.path assistance; model the installed
        # helpers loaded by a root operator from an unrelated current directory.
        code = '''import importlib.util, sys
sys.dont_write_bytecode=True
for path in sys.argv[1:]:
 spec=importlib.util.spec_from_file_location('operator_loaded',path)
 module=importlib.util.module_from_spec(spec);spec.loader.exec_module(module)
 if hasattr(module,'Policy'):
  from pg_backup_runtime import protect_dump
  assert callable(protect_dump)
print('loaded')
'''
        env = dict(os.environ)
        env.pop('PYTHONPATH', None)
        paths = [str(SCRIPT_DIR / name) for name in ['pg-backup-policy.py', 'pg_backup_health.py']]
        for path in paths:
            result = subprocess.run([sys.executable, '-c', code, path], cwd=self.root,
                                    env=env, capture_output=True, text=True, timeout=10)
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertEqual(result.stdout, 'loaded\n')

    def test_attempt_retirement_preserves_active_and_latest_per_action(self):
        for index in range(205):
            action = 'backup' if index == 0 else 'prune'
            status = 'running' if index == 1 else 'failed'
            policy.atomic_json(self.p.state / f'attempt-{index:032x}.json',
                               dict(action=action, status=status, started_at=f'{index:04d}'))
        old = self.archive(1)
        with self.p.locked():
            self.p.retire_attempts()
        hot = {path.name for path in self.p.state.glob('attempt-*.json')}
        self.assertEqual(hot, {f'attempt-{index:032x}.json' for index in [0, 1, *range(5, 205)]})
        retired = {path.name for path in (self.p.state / 'retired' / 'attempts').glob('*.json')}
        self.assertEqual(retired, {f'attempt-{index:032x}.json' for index in range(2, 5)})
        self.assertEqual(old.read_bytes(), b'verified dump')
        self.assertTrue((self.p.state / (old.name + '.json')).exists())

    def canary(self):
        policy.atomic_json(self.p.state / 'checksum-canary.json', dict(cli_image=policy.AWS_IMAGE,
                           cli_version=policy.AWS_VERSION, evidence='isolated checksum canary'))

    def dump(self, args=None, fail=False):
        def fake_dump(partial, app, estimate, exercise):
            self.assertTrue(app.startswith('143-backup-'))
            partial.write_bytes(b'new verified dump')
            if fail:
                raise policy.Refused('dump client failed')
            return {'status': 'completed'}
        def fake_transfer(partial, app, timeline, measured_db, version):
            guard = types.SimpleNamespace(structural_check=self.p.structural_check, checksum=policy.sha256,
                                          aws=self.p.aws, checkpoint=lambda name, **kwargs: None)
            return self.p.finish_archive(partial, app, timeline, measured_db, version, guard)
        with mock.patch.object(self.p, 'dump_archive', side_effect=fake_dump), \
                mock.patch.object(self.p, 'transfer_archive', side_effect=fake_transfer):
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
        self.assertFalse((self.p.state / (old.name + '.json')).exists())
        retired = list((self.p.state / 'retired').glob(old.name + '.*.json'))
        self.assertEqual(len(retired), 1)
        self.assertEqual(policy.read_json(retired[0])['file'], old.name)
        self.assertTrue(latest.exists())
        uploads = [x for x in self.p.operations if x[0] == 'upload']
        self.assertEqual(len(uploads), 1)
        self.assertIn('--checksum-algorithm', uploads[0][2])
        self.assertIn('CRC64NVME', uploads[0][2])
        self.assertNotIn('sync', uploads[0][2])
        records = self.p.inventory()
        self.assertEqual(len(records), 2)
        self.assertEqual(records[0]['integrity']['kind'], 'checksum_upload')
        timeline = records[0]['timeline']
        self.assertEqual(timeline['recovery_point_basis'], 'pre_dump_lower_bound')
        self.assertEqual(timeline['full_restore'], {'status': 'not_verified'})
        points = [timeline[k] for k in ('dump_started_at', 'dump_completed_at', 'structural_verified_at',
                  'local_sha256_at', 'upload_started_at', 'upload_completed_at', 'integrity_verified_at')]
        self.assertEqual(points, sorted(points), 'receipt stages must retain their own ordered timestamps')
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

    def test_post_upload_pressure_preserves_success_receipt_and_pressure_evidence(self):
        paths = [self.archive(n) for n in (1, 2)]
        self.canary()
        def transfer(partial, app, timeline, measured_db, version):
            def checkpoint(name, *, enforce=True):
                if name in ('upload_completed', 'integrity_verified'):
                    self.assertFalse(enforce, 'completed work must record residual pressure without failing')
                    timeline.setdefault('resources', {})[name] = {'resources': {'swap_bytes': policy.GIB}}
            guard = types.SimpleNamespace(structural_check=self.p.structural_check, checksum=policy.sha256,
                                          aws=self.p.aws, checkpoint=checkpoint)
            return self.p.finish_archive(partial, app, timeline, measured_db, version, guard)
        def dump(partial, *args): partial.write_bytes(b'new completed dump')
        with mock.patch.object(self.p, 'dump_archive', side_effect=dump), \
                mock.patch.object(self.p, 'transfer_archive', side_effect=transfer):
            self.p.backup(argparse.Namespace(bootstrap=False, canary=True))
        records = self.p.inventory()
        new = next(r for r in records if r['file'] not in [p.name for p in paths])
        self.assertEqual((self.root / new['file']).read_bytes(), b'new completed dump')
        self.assertEqual(new['timeline']['resources'], {
            'upload_completed': {'resources': {'swap_bytes': policy.GIB}},
            'integrity_verified': {'resources': {'swap_bytes': policy.GIB}}})
        self.assertTrue(all(p.exists() for p in paths))
        self.p.no_pending()

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

    def test_restore_selects_verified_archive_and_requires_cleanup_proof(self):
        paths = [self.archive(n) for n in (1, 2)]
        for status, proof in [(0, True), (42, True), (143, True), (42, False), (0, False)]:
            with self.subTest(status=status, proof=proof):
                def reader(command, env):
                    self.assertEqual(env['BACKUP_ARCHIVE'], str(paths[1]))
                    self.assertTrue(command[1].endswith('restore-test-body.sh'))
                    if proof:
                        receipt = Path(env['RESTORE_CLEANUP_RECEIPT'])
                        receipt.write_text('cleanup-complete\n')
                        receipt.chmod(0o600)
                    return status
                with mock.patch.object(self.p, 'restore_admission'), mock.patch.object(policy, 'run_restore_reader', side_effect=reader):
                    if proof:
                        self.assertEqual(self.p.restore(), status)
                        self.p.no_pending()
                    else:
                        with self.assertRaisesRegex(policy.Refused, 'cleanup unproven'): self.p.restore()
                        with self.assertRaisesRegex(policy.Refused, 'incomplete operation'): self.p.prune()
                self.assertEqual((self.p.state / 'pending.json').exists(), not proof)
                result = policy.read_json(self.p.state / 'last-restore.json')
                self.assertEqual((result['exit_status'], result['cleanup_verified']), (status, proof))
                (self.p.state / 'pending.json').unlink(missing_ok=True)  # Fixture only.

    def test_restore_admission_refuses_production_and_insufficient_docker_space(self):
        self.archive(1)
        for present, available, accepted in [('"143-postgres-1"\n', 100 * policy.GIB, False),
                ('', 20 * policy.GIB + 199, False), ('', 20 * policy.GIB + 200, True)]:
            with self.subTest(present=present, available=available):
                p = policy.Policy()
                with mock.patch.dict(os.environ, DOCKER_HOST='unix:///var/run/docker.sock', DOCKER_CONTEXT=''), \
                        mock.patch.object(policy, 'invoke', side_effect=[present, str(self.root)]) as invoke, \
                        mock.patch.object(policy.os, 'statvfs', return_value=types.SimpleNamespace(f_bavail=available, f_frsize=1)) as space, \
                        mock.patch.object(policy, 'run_restore_reader') as reader:
                    if accepted:
                        p.restore_admission(p.inventory(remote=False)[0])
                        space.assert_called_once_with(self.root)
                    else:
                        with self.assertRaisesRegex(policy.Refused, 'isolated Docker host|insufficient Docker'): p.restore()
                    reader.assert_not_called()
                    self.assertEqual(invoke.call_count, 1 if present else 2)
                    self.assertFalse((p.state / 'pending.json').exists())

    def test_production_guard_compares_literal_names_and_fails_on_listing_errors(self):
        self.archive(1)
        cases = [
            ('"143-postgres-1"\n', True),
            ('"/143-postgres-1"\n', True),
            ('"cache"\n"143-postgres-1,web/db"\n', True),
            ('"143-postgres-10"\n"unrelated"\n', False),
            ('', False),
            ('[]\n', True),
            ('not-json\n', True),
            (policy.Refused('listing unavailable'), True),
        ]
        for listing, refused in cases:
            with self.subTest(listing=listing), \
                    mock.patch.dict(os.environ, DOCKER_HOST='unix:///var/run/docker.sock', DOCKER_CONTEXT=''), \
                    mock.patch.object(policy, 'invoke', side_effect=[listing, str(self.root)]) as call:
                record = self.p.inventory(remote=False)[0]
                if refused:
                    with self.assertRaises((policy.Refused, ValueError)): self.p.restore_admission(record)
                    self.assertEqual(call.call_count, 1)
                else:
                    self.p.restore_admission(record)
                self.assertEqual(call.call_args_list[0].args[0],
                                 ['docker', 'container', 'ls', '--all', '--format', '{{json .Names}}'])
                self.assertFalse((self.p.state / 'pending.json').exists())

    def test_restore_admission_rejects_remote_docker_before_inspecting_local_space(self):
        self.archive(1)
        for host, context in [('tcp://remote:2376', ''), ('', 'remote')]:
            with self.subTest(host=host, context=context), \
                    mock.patch.dict(os.environ, DOCKER_HOST=host, DOCKER_CONTEXT=context), \
                    mock.patch.object(policy, 'invoke', return_value='ssh://remote') as call, \
                    mock.patch.object(policy.os, 'statvfs') as space:
                with self.assertRaisesRegex(policy.Refused, 'local Docker socket'): self.p.restore()
                space.assert_not_called()
                self.assertEqual(call.call_count, 1 if context else 0)

    def test_structural_verification_has_configurable_bounded_timeout(self):
        archive = self.archive(1)
        for seconds in [None, '3600', '86400', '59', '86401']:
            with self.subTest(seconds=seconds), mock.patch.dict(os.environ):
                if seconds is None: os.environ.pop('BACKUP_VERIFY_TIMEOUT_SECONDS', None)
                else: os.environ['BACKUP_VERIFY_TIMEOUT_SECONDS'] = seconds
                if seconds in ('59', '86401'):
                    with self.assertRaisesRegex(policy.Refused, 'verification timeout'): policy.Policy()
                    continue
                p = policy.Policy()
                with mock.patch.object(policy, 'invoke') as call:
                    p.structural_check(archive)
                    self.assertEqual(call.call_args.kwargs['timeout'], int(seconds or '7200'))

    def test_verification_timeout_keeps_uncertain_reader_marker(self):
        path = self.archive(1)
        receipt = self.p.state / (path.name + '.json')
        receipt.unlink()
        remote = self.p.objects[path.name]
        args = argparse.Namespace(file=path.name, sha256=policy.sha256(path), etag=remote['etag'],
                 last_modified=remote['last_modified'], version_id='v', evidence='independent')
        with mock.patch.object(self.p, 'structural_check', side_effect=subprocess.TimeoutExpired('reader', 7200)):
            with self.assertRaises(subprocess.TimeoutExpired): self.p.import_receipt(args)
        self.assertFalse(receipt.exists())
        self.assertEqual(policy.read_json(self.p.state / 'pending.json')['phase'], 'verify')

    def test_dump_partial_is_private_at_creation_with_permissive_umask(self):
        self.archive(1)
        self.archive(2)
        self.canary()
        original = os.open
        def opening(path, flags, mode=0o777):
            fd = original(path, flags, mode)
            if '.partial.' in str(path):
                self.assertEqual(os.fstat(fd).st_mode & 0o777, 0o600)
            return fd
        previous = os.umask(0)
        try:
            with mock.patch.object(policy.os, 'open', side_effect=opening): self.dump()
        finally:
            os.umask(previous)

    def wrapper_fixture(self, name):
        case = self.root / name
        case.mkdir()
        (case / 'tmp').mkdir()
        (case / 'bin').mkdir()
        # Only virtualize capacity; execute the real shell entry point, policy,
        # signal handlers, Popen, reader, and cleanup together.
        python = case / 'bin' / 'python3'
        python.write_text(f'''#!{sys.executable}
import os, runpy, sys, types
os.statvfs = lambda path: types.SimpleNamespace(f_bavail=100 * 1024**3, f_frsize=1)
sys.argv = sys.argv[1:]
runpy.run_path(sys.argv[0], run_name='__main__')
''')
        python.chmod(0o700)
        docker = case / 'bin' / 'docker'
        docker.write_text(f'#!{sys.executable}\n' + '''import json, os, sys, time
from pathlib import Path
root = Path(os.environ['FAKE_CASE_DIR'])
args = sys.argv[1:]
with (root / 'calls').open('a') as f: f.write(json.dumps(args) + '\\n')
cid = 'a' * 64
if args[0] == 'context': print('unix:///var/run/docker.sock')
elif args[:2] == ['container', 'ls']:
    assert args == ['container', 'ls', '--all', '--format', '{{json .Names}}']
    if os.environ['FAKE_CASE'] == 'production': print(json.dumps('143-postgres-1'))
elif args[0] == 'info': print(root)
elif args[0] == 'create':
    Path(args[args.index('--cidfile') + 1]).write_text(cid)
    print(cid)
elif args[0] == 'start':
    assert args == ['start', cid]
elif args[0] == 'exec':
    assert cid in args
    if 'pg_restore' in args:
        (root / 'restored').write_bytes(sys.stdin.buffer.read())
        (root / 'reading').touch()
        if os.environ['FAKE_CASE'] in ('signal', 'cleanup-timeout'): time.sleep(30)
        if os.environ['FAKE_CASE'] == 'restore-failure': sys.exit(42)
    elif 'psql' in args:
        print(2 if os.environ['FAKE_CASE'] == 'few-tables' else 10)
    else: assert 'pg_isready' in args
elif args[0] == 'rm':
    assert args == ['rm', '-f', '-v', cid]
    (root / 'cleanup-started').touch()
    time.sleep(30 if os.environ['FAKE_CASE'] == 'cleanup-timeout' else 0.4)
    if os.environ['FAKE_CASE'] == 'cleanup-failure': sys.exit(55)
    (root / 'cleanup-finished').touch()
else: sys.exit(99)
''')
        docker.chmod(0o700)
        env = dict(os.environ, PATH=str(case / 'bin') + os.pathsep + os.environ['PATH'],
                   TMPDIR=str(case / 'tmp'), FAKE_CASE_DIR=str(case), FAKE_CASE=name,
                   DOCKER_HOST='', DOCKER_CONTEXT='',
                   BACKUP_ATTENDED='true', BACKUP_OBSERVER='test operator', RESTORE_TEST_ENABLED='true')
        return case, env

    def wait_for_file(self, path, child):
        deadline = time.monotonic() + 8
        while not path.exists() and child.poll() is None and time.monotonic() < deadline:
            time.sleep(0.01)
        self.assertTrue(path.exists(), f'child did not reach {path.name}; status {child.poll()}')

    def test_real_wrapper_signal_waits_for_owned_cleanup_and_retains_lock(self):
        archive = self.archive(1)
        for sig, group in [(signal.SIGTERM, False), (signal.SIGHUP, False),
                           (signal.SIGINT, False), (signal.SIGINT, True)]:
            with self.subTest(signal=sig, group=group):
                case, env = self.wrapper_fixture(f'signal-{sig}-{group}')
                env['FAKE_CASE'] = 'signal'
                with (case / 'output').open('w+') as output:
                    child = subprocess.Popen(['bash', str(SCRIPT_DIR / 'restore-test.sh')],
                                             env=env, stdout=output, stderr=output, start_new_session=True)
                    try:
                        self.wait_for_file(case / 'reading', child)
                        if group: os.killpg(child.pid, sig)
                        else: child.send_signal(sig)
                        self.wait_for_file(case / 'cleanup-started', child)
                        # A second signal must not interrupt Docker rm either.
                        child.send_signal(signal.SIGTERM)
                        with self.assertRaisesRegex(policy.Refused, 'lock timeout'):
                            with self.p.locked(): self.fail('reader released lock before cleanup')
                        status = child.wait(timeout=8)
                        output.seek(0)
                        self.assertEqual(status, 128 + sig, output.read())
                    finally:
                        if child.poll() is None: child.kill()
                        child.wait(timeout=8)
                self.assertTrue((case / 'cleanup-finished').exists())
                self.assertEqual((case / 'restored').read_bytes(), archive.read_bytes())
                self.assertFalse((self.p.state / 'pending.json').exists())
                self.assertEqual(list((case / 'tmp').iterdir()), [])
                result = policy.read_json(self.p.state / 'last-restore.json')
                self.assertEqual((result['exit_status'], result['cleanup_verified']), (128 + sig, True))
                self.p.no_pending()

    def test_real_wrapper_failure_clears_only_proven_cleanup(self):
        archive = self.archive(1)
        for scenario, status, clean in [('success', 0, True), ('restore-failure', 42, True),
                                        ('few-tables', 1, True), ('cleanup-failure', 1, False)]:
            with self.subTest(scenario=scenario):
                case, env = self.wrapper_fixture(scenario)
                result = subprocess.run(['bash', str(SCRIPT_DIR / 'restore-test.sh')], env=env,
                                        capture_output=True, timeout=10)
                self.assertEqual(result.returncode, status, result.stdout + result.stderr)
                self.assertEqual((case / 'cleanup-finished').exists(), clean)
                self.assertEqual((self.p.state / 'pending.json').exists(), not clean)
                self.assertEqual(policy.read_json(self.p.state / 'last-restore.json')['cleanup_verified'], clean)
                self.assertEqual(archive.read_bytes(), b'verified dump')
                if not clean:
                    self.assertEqual(len(list((case / 'tmp').glob('*/container-id'))), 1)
                    with self.assertRaisesRegex(policy.Refused, 'incomplete operation'): self.p.prune()

    def test_real_wrapper_refuses_production_before_allocation(self):
        self.archive(1)
        case, env = self.wrapper_fixture('production')
        result = subprocess.run(['bash', str(SCRIPT_DIR / 'restore-test.sh')], env=env,
                                capture_output=True, timeout=10)
        self.assertEqual(result.returncode, 1)
        self.assertIn(b'isolated Docker host', result.stdout)
        self.assertEqual([json.loads(x)[0] for x in (case / 'calls').read_text().splitlines()], ['context', 'container'])
        self.assertFalse((self.p.state / 'pending.json').exists())

    def test_reader_grace_timeout_retains_marker_for_manual_reconciliation(self):
        archive = self.archive(1)
        policy.atomic_json(self.p.state / 'last-restore.json', dict(file='previous.dump', outcome='succeeded',
                           cleanup_verified=True, exit_status=0))
        case, env = self.wrapper_fixture('cleanup-timeout')
        code = '''import importlib.util, sys
sys.dont_write_bytecode=True
spec=importlib.util.spec_from_file_location('p', sys.argv[1]); p=importlib.util.module_from_spec(spec); spec.loader.exec_module(p)
reader=p.run_restore_reader
p.run_restore_reader=lambda command, env: reader(command, env, cleanup_timeout=0.2)
p.os.statvfs=lambda path: type('FS', (), dict(f_bavail=100 * p.GIB, f_frsize=1))()
sys.argv=[sys.argv[1], 'restore']
sys.exit(p.main())
'''
        with (case / 'output').open('w+') as output:
            child = subprocess.Popen([sys.executable, '-c', code, str(SCRIPT_DIR / 'pg-backup-policy.py')],
                                     env=env, stdout=output, stderr=output, start_new_session=True)
            try:
                self.wait_for_file(case / 'reading', child)
                child.send_signal(signal.SIGTERM)
                self.assertEqual(child.wait(timeout=8), 1)
                output.seek(0)
                self.assertIn('cleanup exceeded grace period', output.read())
            finally:
                if child.poll() is None: child.kill()
                child.wait(timeout=8)
        self.assertTrue((case / 'cleanup-started').exists())
        self.assertFalse((case / 'cleanup-finished').exists())
        self.assertTrue((self.p.state / 'pending.json').exists())
        result = policy.read_json(self.p.state / 'last-restore.json')
        self.assertEqual((result['file'], result['outcome'], result['exit_status'], result['cleanup_verified']),
                         (archive.name, 'failed', 1, False))
        self.assertIn('cleanup exceeded grace period', result['error'])
        with self.assertRaisesRegex(policy.Refused, 'incomplete operation'): self.p.no_pending()

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
