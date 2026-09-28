#!/usr/bin/env python3
"""Post-dump ownership/resource regressions; Docker and AWS are simulated."""
import hashlib
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
import pg_backup_runtime as runtime
import pg_backup_state as state
from pg_backup_runtime_test import safe_resources


class HashTests(unittest.TestCase):
    def test_hash_drops_each_read_without_changing_bytes(self):
        with tempfile.TemporaryDirectory() as tmp:
            path = Path(tmp) / 'archive'
            content = b'x' * (17 * 1024 ** 2)
            path.write_bytes(content)
            path.chmod(0o600)
            before = state.identity(path)
            with mock.patch.object(state.os, 'posix_fadvise', create=True) as advice, \
                    mock.patch.multiple(state.os, POSIX_FADV_SEQUENTIAL=2, POSIX_FADV_DONTNEED=4, create=True), \
                    mock.patch.object(state.os, 'fsync') as sync:
                tick = mock.Mock()
                actual = state.sha256(path, tick=tick, drop_cache=True)
            self.assertEqual(actual, hashlib.sha256(content).hexdigest())
            self.assertEqual(state.identity(path), before)
            sync.assert_called_once()
            self.assertEqual([c.args[1:] for c in advice.call_args_list], [
                (0, 0, 4), (0, 0, 2), (0, 8 * 1024 ** 2, 4),
                (8 * 1024 ** 2, 8 * 1024 ** 2, 4), (16 * 1024 ** 2, 1024 ** 2, 4)])
            self.assertEqual(tick.call_count, 4, 'monitor must run between chunks and before EOF')

    def test_hash_cancellation_and_cache_error_preserve_archive(self):
        for fault in ('resource', 'cache'):
            with self.subTest(fault=fault), tempfile.TemporaryDirectory() as tmp:
                path = Path(tmp) / 'archive'
                path.write_bytes(b'completed backup')
                path.chmod(0o600)
                before = state.identity(path)
                with mock.patch.object(state.os, 'posix_fadvise', create=True,
                                       side_effect=OSError('cache unavailable') if fault == 'cache' else None), \
                        mock.patch.multiple(state.os, POSIX_FADV_SEQUENTIAL=2, POSIX_FADV_DONTNEED=4, create=True):
                    tick = mock.Mock(side_effect=state.Refused('swap usage at risk'))
                    with self.assertRaises((state.Refused, OSError)):
                        state.sha256(path, tick=tick, drop_cache=True)
                self.assertEqual(state.identity(path), before)
                self.assertEqual(path.read_bytes(), b'completed backup')


class TransferTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name).resolve()
        self.state = self.root / '.backup-state'
        self.state.mkdir(mode=0o700)
        self.app = '143-backup-' + 'a' * 32
        self.partial = self.root / '.new.dump.partial.test'
        self.partial.write_bytes(b'completed dump')
        self.partial.chmod(0o600)
        self.old = self.root / 'older.dump'
        self.old.write_bytes(b'older verified backup')
        self.p = types.SimpleNamespace(root=self.root, state=self.state, reserve=20 * runtime.GIB,
                                      lock_fd=1, verify_timeout=600, container='prod-db')
        patch = mock.patch.object(runtime, 'proc_identity', return_value=['boot', 'start'])
        patch.start()
        self.addCleanup(patch.stop)
        self.guard = runtime.TransferGuard(self.p, self.partial, self.app, {}, 100, 'test-cli')
        self.guard.run.mkdir(parents=True, mode=0o700)
        self.db = dict(id='d' * 64, pid=123, started_at='db-start', restarts=0)
        self.owned = dict(app=self.app, database=self.db, partial=state.identity(self.partial),
                          creation_started=False, creation_complete=False)
        self.guard.owned = dict(self.owned)
        self.guard.db = dict(Image='sha256:db-image', Config={'Volumes': {'/var/lib/postgresql': {}}})
        state.atomic_json(self.guard.run.parent / 'ownership.json', self.owned)
        state.atomic_json(self.guard.run.parent / 'result.json', dict(status='completed', cleanup_verified=True))
        state.atomic_json(self.guard.record, self.owned)
        state.atomic_json(self.state / 'pending.json', dict(app_name=self.app, partial=self.partial.name,
                                                          phase='verification', runtime='postdump'))

    def test_paced_upload_forces_classic_and_readonly_private_config(self):
        import configparser
        self.p.aws_spec = mock.Mock(return_value=(['image', 's3', 'cp'], {'credential': 'private'}))
        with mock.patch.object(self.guard, 'client') as client:
            self.guard.aws(['s3', 'cp'], self.partial)
            self.guard.aws(['s3', 'cp'], self.partial)
        path = self.guard.run / 'aws-config'
        self.assertEqual(path.stat().st_mode & 0o777, 0o600)
        config = configparser.ConfigParser()
        config.read(path)
        settings = dict(line.strip().split(' = ') for line in config['default']['s3'].splitlines() if line.strip())
        self.assertEqual(settings, dict(preferred_transfer_client='classic', max_concurrent_requests='2',
                                       max_bandwidth='100MB/s', multipart_chunksize='16MB'))
        spec = client.call_args.args[1]
        self.assertIn('type=bind,src=' + str(path) + ',dst=/aws-config,readonly', spec)
        self.assertIn('AWS_CONFIG_FILE=/aws-config', spec)
        self.assertNotIn('private', path.read_text())
        path.write_bytes(b'changed')
        with self.assertRaisesRegex(state.Refused, 'configuration changed'):
            self.guard.aws(['s3', 'cp'], self.partial)

    def test_reader_pressure_and_events_survive_threshold_stop_in_bounded_history(self):
        cg = self.root / ('docker-' + 'c' * 64 + '.scope')
        cg.mkdir()
        files = {'memory.current': '100', 'memory.peak': '200', 'memory.max': str(runtime.GIB),
                 'memory.stat': 'anon 20\nfile 80\n', 'memory.swap.current': '0',
                 'memory.events': 'low 0\nhigh 0\nmax 12\noom 0\noom_kill 0\n',
                 'memory.pressure': 'some avg10=2.1\nfull avg10=1.9\n',
                 'io.pressure': 'some avg10=0.5\nfull avg10=0.2\n'}
        for name, content in files.items(): (cg / name).write_text(content)
        with mock.patch.object(runtime, 'cgroup_path', return_value=cg):
            self.guard.observe_client_memory({'Id': 'c' * 64, 'State': {'Pid': 42}})
        with mock.patch.object(self.guard, 'database'), mock.patch.object(runtime, 'resources',
                return_value=dict(safe_resources(), monotonic=10)), mock.patch.object(runtime, 'SAMPLE_LIMIT', 3):
            for _ in range(4): self.guard.tick(force=True, reset_interval=True)
        violating = dict(safe_resources(), monotonic=15, host_memory={'some': 2, 'full': 1.1})
        with mock.patch.object(self.guard, 'database'), mock.patch.object(runtime, 'resources', return_value=violating), \
                mock.patch.object(runtime, 'SAMPLE_LIMIT', 3):
            with self.assertRaisesRegex(state.Refused, 'host_memory'): self.guard.tick(force=True)
        self.guard.fail('host_memory pressure at risk')
        samples = state.read_json(self.guard.run / 'samples.json')
        self.assertEqual(len(samples), 3)
        self.assertEqual(samples[-1]['resources']['host_memory']['full'], 1.1)
        reader = samples[-1]['client_memory']
        self.assertEqual(reader['memory_pressure'], {'some': 2.1, 'full': 1.9})
        self.assertEqual(reader['io_pressure'], {'some': 0.5, 'full': 0.2})
        self.assertEqual(reader['events']['max'], 12)
        self.assertEqual(state.read_json(self.guard.result)['client_memory'], reader)

    def assert_preserved(self):
        self.assertEqual(self.partial.read_bytes(), b'completed dump')
        self.assertEqual(self.old.read_bytes(), b'older verified backup')
        self.assertTrue((self.state / 'pending.json').exists())
        self.assertEqual(list(self.state.glob('*.dump.json')), [], 'failure must never qualify an archive')

    def test_prepare_requires_proven_dump_cleanup_and_unchanged_generation(self):
        for fault in ('cleanup', 'inode', 'generation'):
            with self.subTest(fault=fault):
                state.atomic_json(self.guard.run.parent / 'result.json', dict(status='completed', cleanup_verified=fault != 'cleanup'))
                owned = dict(self.owned, partial=dict(self.owned['partial'], inode=-1)) if fault == 'inode' else self.owned
                state.atomic_json(self.guard.run.parent / 'ownership.json', owned)
                with mock.patch.object(runtime, 'invoke', return_value='unix:///var/run/docker.sock'), \
                        mock.patch.object(self.guard, 'database', side_effect=state.Refused('database generation changed')), \
                        mock.patch.object(self.guard, 'checkpoint') as check:
                    with self.assertRaises(state.Refused): self.guard.prepare()
                check.assert_not_called()
                self.assert_preserved()

    def test_transfer_samples_memory_swap_and_pressure_and_stops_on_violation(self):
        for field, value in [('swap_bytes', runtime.GIB), ('commit_headroom', 1),
                             ('host_memory', {'some': 2, 'full': 2})]:
            with self.subTest(field=field):
                sample = dict(safe_resources(), monotonic=time.monotonic(), **{field: value})
                with mock.patch.object(self.guard, 'database'), mock.patch.object(runtime, 'resources', return_value=sample):
                    with self.assertRaises(state.Refused): self.guard.tick(force=True)
                result = self.guard.fail('resource threshold')
                self.assertEqual(result['phase_resources']['last_observation']['resources'][field], value)
                self.assertTrue(result['cleanup_verified'])
                self.assert_preserved()

    def test_readers_are_owned_bounded_and_success_is_saved_before_removal(self):
        for stage in ('structural', 'upload', 'metadata'):
            with self.subTest(stage=stage):
                calls = []
                def invoke(command, **kwargs):
                    calls.append(command)
                    owned = state.read_json(self.guard.record)
                    if command[:2] == ['docker', 'create']:
                        self.assertTrue(owned['creation_started'])
                        self.assertFalse(owned['creation_complete'])
                        return 'c' * 64
                    if command[:2] == ['docker', 'start']:
                        self.assertEqual(owned['container_id'], 'c' * 64)
                        self.assertTrue(owned['creation_complete'])
                    return '{"Contents":[]}' if command[1] == 'logs' else ''
                def stopped(owned):
                    saved = state.read_json(self.guard.run / (stage + '.json'))
                    self.assertEqual(saved['exit']['code'], 0)
                    self.assertEqual(saved['container_id'], 'c' * 64)
                exited = {'State': {'Status': 'exited', 'ExitCode': 0, 'OOMKilled': False}}
                with mock.patch.object(runtime, 'invoke', side_effect=invoke), \
                        mock.patch.object(self.guard, 'tick'), mock.patch.object(self.guard, 'inspect', return_value=exited), \
                        mock.patch.object(self.guard, 'stop', side_effect=stopped):
                    output = self.guard.client(stage, ['image', 'command'], capture=stage == 'metadata')
                create = calls[0]
                for flag in ('--memory=1g', '--memory-swap=1g', '--cpus=1', '--pids-limit=64',
                             '--read-only', '--pull=never', '--cap-drop=ALL', '--security-opt=no-new-privileges',
                             '/tmp:rw,noexec,nosuid,size=64m'):
                    self.assertIn(flag, create)
                self.assertIn(self.app + '-' + stage, create)
                self.assertIn(runtime.LABEL + '=' + self.app, create)
                self.assertEqual(output, '{"Contents":[]}' if stage == 'metadata' else '')
                self.assertFalse(state.read_json(self.guard.record)['creation_started'])

    def test_structural_check_never_executes_in_live_database_or_creates_data_volume(self):
        with mock.patch.object(self.guard, 'client') as client:
            self.guard.structural_check(self.partial)
        stage, args = client.call_args.args
        self.assertEqual(stage, 'structural')
        self.assertEqual(args[-3:], ['sha256:db-image', '--list', '/backup.dump'])
        self.assertIn('type=bind,src=' + str(self.partial) + ',dst=/backup.dump,readonly', args)
        self.assertIn('/var/lib/postgresql:rw,noexec,nosuid,size=1m', args)
        self.assertNotIn(self.p.container, args)

    def test_failed_or_ambiguous_upload_keeps_marker_archive_and_identity(self):
        for fault in ('create', 'start', 'exit'):
            with self.subTest(fault=fault):
                def invoke(command, **kwargs):
                    if command[1] == fault: raise subprocess.TimeoutExpired('docker', 5)
                    return 'c' * 64 if command[1] == 'create' else ''
                exited = {'State': {'Status': 'exited', 'ExitCode': 9 if fault == 'exit' else 0, 'OOMKilled': False}}
                with mock.patch.object(runtime, 'invoke', side_effect=invoke), \
                        mock.patch.object(self.guard, 'tick'), \
                        mock.patch.object(self.guard, 'inspect', return_value=exited), \
                        mock.patch.object(self.guard, 'stop') as stop:
                    with self.assertRaises((state.Refused, subprocess.TimeoutExpired)):
                        self.guard.client('upload', ['image', 'upload'])
                    result = self.guard.fail('uncertain upload')
                stop.assert_called_once()
                self.assertEqual(result['cleanup_verified'], fault != 'create')
                self.assertTrue(result['reconciliation_required'])
                self.assert_preserved()

    def test_successful_reader_records_residual_pressure_without_failing(self):
        for stage in ('structural', 'upload', 'metadata'):
            with self.subTest(stage=stage):
                self.guard.previous = None
                count = 0
                def sample(*args):
                    nonlocal count
                    count += 1
                    value = dict(safe_resources(), monotonic=time.monotonic())
                    if count >= 3:
                        value.update(swap_bytes=runtime.GIB, host_memory={'some': 4, 'full': 4})
                    return value
                exited = {'State': {'Status': 'exited', 'ExitCode': 0, 'OOMKilled': False}}
                with mock.patch.object(runtime, 'invoke', return_value='c' * 64), \
                        mock.patch.object(self.guard, 'database'), mock.patch.object(runtime, 'resources', side_effect=sample), \
                        mock.patch.object(self.guard, 'inspect', return_value=exited), mock.patch.object(self.guard, 'stop'):
                    self.guard.client(stage, ['image', 'command'])
                self.assertEqual(state.read_json(self.guard.run / (stage + '.json'))['exit']['code'], 0)
                self.assertEqual(self.guard.phase_resources['last_observation']['resources']['swap_bytes'], runtime.GIB)
                self.assertGreater(count, 2)

    def test_active_reader_still_stops_on_pressure(self):
        count = 0
        def sample(*args):
            nonlocal count
            count += 1
            return dict(safe_resources(), monotonic=count * 10,
                        swap_bytes=runtime.GIB if count >= 3 else 0)
        running = {'Id': 'c' * 64, 'State': {'Status': 'running', 'Running': True, 'Pid': 42}}
        with mock.patch.object(runtime, 'invoke', return_value='c' * 64), \
                mock.patch.object(runtime.time, 'monotonic', return_value=100), \
                mock.patch.object(self.guard, 'database'), mock.patch.object(runtime, 'resources', side_effect=sample), \
                mock.patch.object(self.guard, 'inspect', return_value=running), mock.patch.object(self.guard, 'stop') as stop, \
                mock.patch.object(self.guard, 'observe_client_memory') as observe:
            with self.assertRaisesRegex(state.Refused, 'swap usage'):
                self.guard.client('upload', ['image', 'command'])
            self.guard.fail('swap usage at risk')
        stop.assert_called_once()
        observe.assert_called_once()
        self.assert_preserved()

    def test_listing_allows_residual_pressure_but_retains_hard_capacity_gates(self):
        self.guard.phase = 'metadata'
        for field, value, allowed in [('swap_bytes', runtime.GIB, True),
                                     ('host_memory', {'some': 4, 'full': 4}, True),
                                     ('available_bytes', 1, False), ('free_bytes', 1, False),
                                     ('commit_headroom', 1, False), ('db_headroom', 1, False)]:
            with self.subTest(field=field):
                sample = dict(safe_resources(), monotonic=time.monotonic(), **{field: value})
                with mock.patch.object(self.guard, 'database'), mock.patch.object(runtime, 'resources', return_value=sample):
                    if allowed: self.guard.tick(force=True)
                    else:
                        with self.assertRaises(state.Refused): self.guard.tick(force=True)

    def test_slow_create_is_resampled_before_start_without_waiving_capacity(self):
        for bad_capacity in (False, True):
            with self.subTest(bad_capacity=bad_capacity):
                self.guard.previous = None
                clock = [100.0]
                commands, sampled_at = [], []
                def sample(*args):
                    sampled_at.append(clock[0])
                    return dict(safe_resources(), monotonic=clock[0],
                                available_bytes=1 if bad_capacity and clock[0] >= 120 else 4 * runtime.GIB)
                def invoke(command, **kwargs):
                    commands.append(command[1])
                    if command[1] == 'create': clock[0] += 20
                    if command[1] == 'start': clock[0] += 12
                    return 'c' * 64
                exited = {'State': {'Status': 'exited', 'ExitCode': 0, 'OOMKilled': False}}
                with mock.patch.object(runtime.time, 'monotonic', side_effect=lambda: clock[0]), \
                        mock.patch.object(runtime, 'invoke', side_effect=invoke), \
                        mock.patch.object(self.guard, 'database'), mock.patch.object(runtime, 'resources', side_effect=sample), \
                        mock.patch.object(self.guard, 'inspect', return_value=exited), mock.patch.object(self.guard, 'stop'):
                    if bad_capacity:
                        with self.assertRaisesRegex(state.Refused, 'available memory'):
                            self.guard.client('upload', ['image', 'command'])
                    else:
                        self.guard.client('upload', ['image', 'command'])
                self.assertEqual(sampled_at, [100.0, 120.0] if bad_capacity else [100.0, 120.0, 132.0])
                self.assertEqual('start' in commands, not bad_capacity)

    def test_reader_stop_uses_exact_id_and_never_contacts_database_backend(self):
        owned = dict(self.owned, client_name=self.app + '-upload', container_id='c' * 64)
        data = dict(Id='c' * 64, State={'Running': True})
        with mock.patch.object(self.guard, 'inspect', side_effect=[data, dict(data, State={'Running': False}), None]), \
                mock.patch.object(runtime, 'invoke', return_value='') as invoke, \
                mock.patch.object(self.guard, 'query', side_effect=AssertionError('must not contact DB')):
            self.guard.stop(owned)
        self.assertEqual([c.args[0] for c in invoke.call_args_list], [
            ['docker', 'stop', '--time', '5', 'c' * 64], ['docker', 'rm', '-v', 'c' * 64]])
        self.assert_preserved()

    def test_reader_rename_or_replacement_is_not_stopped(self):
        self.guard.client_name = self.app + '-upload'
        for name, label in [('/renamed', self.app), ('/' + self.guard.client_name, 'another-run')]:
            with self.subTest(name=name, label=label):
                data = dict(Id='c' * 64, Name=name, Config={'Labels': {runtime.LABEL: label}})
                with mock.patch.object(runtime, 'invoke', side_effect=['"renamed"\t' + 'c' * 64, json.dumps([data])]) as invoke:
                    with self.assertRaisesRegex(state.Refused, 'ownership mismatch'):
                        self.guard.inspect('c' * 64)
                self.assertEqual(len(invoke.call_args_list), 2, 'only inventory and inspect are allowed')

    def test_watchdog_death_during_hash_keeps_completed_dump(self):
        # Real fork/wait/kill path, with scaled deadlines and no Docker work.
        self.guard.record.unlink()
        self.guard.run.rmdir()
        def stalled(g):
            state.atomic_json(g.record, dict(self.owned))
            state.atomic_json(g.heartbeat, dict(at=runtime.now(), phase='checksum'))
            time.sleep(5)
        with mock.patch.object(runtime.TransferGuard, 'supervise', stalled), \
                mock.patch.object(runtime, 'WATCHDOG_TIMEOUT', .1):
            with self.assertRaisesRegex(state.Refused, 'heartbeat expired'):
                runtime.run_guard(self.guard)
        self.assert_preserved()
        result = state.read_json(self.guard.result)
        self.assertTrue(result['cleanup_verified'])
        self.assertTrue(result['reconciliation_required'])

    def test_caller_disconnect_and_watchdog_death_hold_lock_until_reader_cleanup(self):
        # Real processes, signals and flock, with fake Docker transports only.
        fixture = r'''
import fcntl, os, sys, time, types
from pathlib import Path
from unittest import mock
import pg_backup_runtime as r
from pg_backup_state import atomic_json, identity
from pg_backup_runtime_test import safe_resources
root=Path(sys.argv[1]); state=root/'.backup-state'; state.mkdir(mode=0o700)
partial=root/'.complete.partial'; partial.write_bytes(b'completed'); partial.chmod(0o600)
app='143-backup-'+'b'*32; (state/app).mkdir(mode=0o700)
atomic_json(state/'pending.json', dict(app_name=app,phase='upload',runtime='postdump'))
lock=os.open(root/'lock',os.O_CREAT|os.O_RDWR,0o600); fcntl.flock(lock,fcntl.LOCK_EX)
p=types.SimpleNamespace(root=root,state=state,reserve=20*r.GIB,lock_fd=lock,verify_timeout=600)
def process(pid):
 os.kill(pid,0)
 return ['boot',str(pid)]
def prepare(g):
 g.owned=dict(app=app,partial=identity(partial),container_id='c'*64,database={'pid':123},
              client_name=app+'-upload',creation_started=True,creation_complete=True)
 atomic_json(g.record,g.owned)
 (root/'started').write_text(str(os.getpid()))
def finish(partial,app,timeline,measured,version,g):
 while True: g.tick(force=True); time.sleep(.05)
def stop(g,owned):
 (root/'stopping').write_text('yes'); time.sleep(.5); (root/'stopped').write_text('yes')
def sample(*args):
 s=safe_resources();s['monotonic']=time.monotonic();return s
p.finish_archive=finish
with mock.patch.object(r,'proc_identity',side_effect=process), mock.patch.object(r.TransferGuard,'prepare',prepare), \
 mock.patch.object(r.TransferGuard,'stop',stop), mock.patch.object(r.TransferGuard,'database'), \
 mock.patch.object(r,'resources',side_effect=sample):
 r.protect_transfer(p,partial,app,{},100,'test-cli')
'''
        for mode in ('term', 'parent_kill', 'watchdog_kill'):
            with self.subTest(mode=mode), tempfile.TemporaryDirectory() as tmp:
                root = Path(tmp)
                env = dict(os.environ, PYTHONPATH=str(Path(__file__).parent), PYTHONDONTWRITEBYTECODE='1')
                child = subprocess.Popen([sys.executable, '-c', fixture, str(root)], env=env,
                                         stdout=subprocess.DEVNULL, stderr=subprocess.PIPE, start_new_session=True)
                try:
                    deadline = time.monotonic() + 5
                    while not (root / 'started').exists() and child.poll() is None and time.monotonic() < deadline:
                        time.sleep(.02)
                    self.assertTrue((root / 'started').exists())
                    if mode == 'watchdog_kill':
                        os.kill(int((root / 'started').read_text()), signal.SIGKILL)
                    else:
                        child.send_signal(signal.SIGTERM if mode == 'term' else signal.SIGKILL)
                    if mode == 'parent_kill': child.wait(timeout=2)
                    deadline = time.monotonic() + 5
                    while not (root / 'stopping').exists() and time.monotonic() < deadline: time.sleep(.01)
                    self.assertTrue((root / 'stopping').exists())
                    import fcntl
                    with (root / 'lock').open('rb') as lock:
                        with self.assertRaises(BlockingIOError):
                            fcntl.flock(lock.fileno(), fcntl.LOCK_EX | fcntl.LOCK_NB)
                    result_path = root / '.backup-state' / ('143-backup-' + 'b' * 32) / 'postdump' / 'result.json'
                    deadline = time.monotonic() + 5
                    while not result_path.exists() and time.monotonic() < deadline: time.sleep(.02)
                    self.assertTrue(result_path.exists())
                    self.assertTrue(state.read_json(result_path)['cleanup_verified'])
                    self.assertEqual((root / '.complete.partial').read_bytes(), b'completed')
                    self.assertTrue((root / '.backup-state' / 'pending.json').exists())
                    self.assertNotEqual(child.wait(timeout=2), 0)
                    # Allow the detached child to finish its final os._exit.
                    with (root / 'lock').open('rb') as lock:
                        deadline = time.monotonic() + 2
                        while True:
                            try:
                                fcntl.flock(lock.fileno(), fcntl.LOCK_EX | fcntl.LOCK_NB)
                                break
                            except BlockingIOError:
                                self.assertLess(time.monotonic(), deadline)
                                time.sleep(.01)
                finally:
                    if child.poll() is None: child.kill()
                    child.communicate(timeout=2)


if __name__ == '__main__':
    unittest.main(verbosity=2)
