#!/usr/bin/env python3
"""Owned-writer failure tests; all Docker/database/resource calls are fake."""
import errno
import fcntl
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
from pg_backup_state import Refused, atomic_json, read_json, identity


def safe_resources():
    pressure = {'some': 0, 'full': 0}
    return dict(free_bytes=50 * runtime.GIB, available_bytes=4 * runtime.GIB,
                commit_headroom=4 * runtime.GIB, db_headroom=2 * runtime.GIB,
                swap_bytes=0, swap_out_bytes=0, monotonic=10,
                host_io=dict(pressure), host_memory=dict(pressure),
                db_io=dict(pressure), db_memory=dict(pressure))


class KernelParsingTests(unittest.TestCase):
    def test_process_identity_handles_parentheses_and_rejects_zombie(self):
        for state in ['S', 'Z']:
            with self.subTest(state=state):
                stat = '42 (dump (worker)) ' + state + ' ' + ' '.join(['0'] * 18 + ['12345'])
                with mock.patch.object(Path, 'read_text', side_effect=[stat, 'boot-id\n']):
                    if state == 'Z':
                        with self.assertRaisesRegex(Refused, 'zombie'): runtime.proc_identity(42)
                    else:
                        self.assertEqual(runtime.proc_identity(42), ['boot-id', '12345'])

    def test_cgroup_path_requires_bounded_v2_process_group(self):
        cases = [('0::/system.slice/docker-c.scope\n', '/sys/fs/cgroup/system.slice/docker-c.scope'),
                 ('2:memory:/docker/c\n', None), ('0::/\n', None), ('0::/../../etc\n', None)]
        for text, expected in cases:
            with self.subTest(text=text), mock.patch.object(Path, 'read_text', return_value=text):
                if expected:
                    self.assertEqual(runtime.cgroup_path(42), Path(expected))
                else:
                    with self.assertRaises(Refused): runtime.cgroup_path(42)


class RuntimeTests(unittest.TestCase):
    def setUp(self):
        self.tmp = tempfile.TemporaryDirectory()
        self.addCleanup(self.tmp.cleanup)
        self.root = Path(self.tmp.name).resolve()
        self.state = self.root / '.backup-state'
        self.state.mkdir(mode=0o700)
        self.app = '143-backup-' + 'a' * 32
        self.partial = self.root / '.new.dump.partial.test'
        self.partial.write_bytes(b'partial')
        self.partial.chmod(0o600)
        self.completed = self.root / 'completed.dump'
        self.completed.write_bytes(b'previous verified backup')
        self.p = types.SimpleNamespace(root=self.root, state=self.state, reserve=20 * runtime.GIB,
                                       container='prod-db', database='example', user='example',
                                       db_env=lambda: {'PGPASSWORD': 'not-in-command'}, lock_fd=1)
        atomic_json(self.state / 'pending.json', dict(app_name=self.app, partial=self.partial.name, phase='dump'))
        patch = mock.patch.object(runtime, 'proc_identity', return_value=['boot', 'start'])
        patch.start()
        self.addCleanup(patch.stop)
        self.guard = runtime.Guard(self.p, self.partial, self.app, 18 * runtime.GIB)
        self.guard.run.mkdir(mode=0o700)
        self.db = dict(id='d' * 64, pid=123, started_at='start', restarts=0)
        self.owned = dict(app=self.app, partial=identity(self.partial), database=self.db,
                          container_id='c' * 64, creation_started=True, creation_complete=True)
        atomic_json(self.guard.record, self.owned)

    def test_resource_boundaries_and_growth(self):
        for field, value, reason in [
                ('free_bytes', 24 * runtime.GIB - 1, 'disk'),
                ('available_bytes', 1.5 * runtime.GIB - 1, 'available memory'),
                ('commit_headroom', runtime.GIB - 1, 'commit'),
                ('db_headroom', runtime.GIB / 2 - 1, 'database memory'),
                ('swap_bytes', runtime.GIB / 4 + 1, 'swap usage')]:
            with self.subTest(field=field):
                s = safe_resources()
                s[field] = value
                with self.assertRaisesRegex(Refused, reason):
                    runtime.check_resources(s, None, self.p.reserve)
        s = safe_resources()
        s['free_bytes'] = 24 * runtime.GIB
        runtime.check_resources(s, None, self.p.reserve)
        with self.assertRaisesRegex(Refused, 'disk'):
            runtime.check_resources(s, None, self.p.reserve, admission=True, estimate=18 * runtime.GIB)
        for delta, elapsed in [(21 * 1024 ** 2, 5), (0, 31), (-1, 5)]:
            with self.subTest(delta=delta, elapsed=elapsed):
                prev = safe_resources()
                s = dict(prev, monotonic=prev['monotonic'] + elapsed, swap_out_bytes=delta)
                with self.assertRaises(Refused): runtime.check_resources(s, prev, self.p.reserve)

    def test_pressure_missing_invalid_and_over_limit(self):
        for contents in ['some avg10=0', 'some avg10=nan\nfull avg10=0', 'some avg10=0\nfull avg10=-1']:
            with self.subTest(contents=contents):
                path = self.root / 'psi'
                path.write_text(contents)
                with self.assertRaises(Refused): runtime.pressure(path)
        for field, part in [('host_io', 'some'), ('host_memory', 'full'), ('db_io', 'some'), ('db_memory', 'full')]:
            with self.subTest(field=field):
                s = safe_resources()
                s[field][part] = 21
                with self.assertRaisesRegex(Refused, 'pressure'): runtime.check_resources(s, None, self.p.reserve)

    def test_admission_enospc_never_touches_completed_archive(self):
        owned = dict(self.owned, creation_started=False, creation_complete=False)
        atomic_json(self.guard.record, owned)
        with mock.patch.object(self.guard, 'prepare', side_effect=OSError(errno.ENOSPC, 'disk full')), \
                mock.patch.object(self.guard, 'stop') as stop:
            self.guard.supervise()
        stop.assert_not_called()
        self.assertEqual(read_json(self.guard.result)['status'], 'failed')
        self.assertTrue(read_json(self.guard.result)['cleanup_verified'])
        self.assertFalse(self.partial.exists())
        self.assertEqual(self.completed.read_bytes(), b'previous verified backup')

    def test_failures_clean_only_after_owned_stop_proof(self):
        for error in ['disk reserve at risk', 'dump failed or reached its size limit', 'monitor failed', 'cancelled']:
            with self.subTest(error=error):
                with mock.patch.object(self.guard, 'stop', side_effect=Refused('cleanup unproven')):
                    result = self.guard.fail(error)
                self.assertFalse(result['cleanup_verified'])
                self.assertTrue(self.partial.exists())
                self.assertTrue((self.state / 'pending.json').exists())
        with mock.patch.object(self.guard, 'stop') as stop:
            result = self.guard.fail('owned stop confirmed')
        stop.assert_called_once_with(self.owned)
        self.assertTrue(result['cleanup_verified'])
        self.assertFalse(self.partial.exists())
        self.assertFalse((self.state / 'pending.json').exists())
        self.assertEqual(self.completed.read_bytes(), b'previous verified backup')

    def test_ambiguous_create_keeps_partial_even_if_absent_now(self):
        atomic_json(self.guard.record, dict(self.owned, creation_complete=False))
        with mock.patch.object(self.guard, 'stop'):
            result = self.guard.fail('create timed out')
        self.assertFalse(result['cleanup_verified'])
        self.assertTrue(self.partial.exists())

    def test_replaced_partial_or_pending_is_never_deleted(self):
        old = dict(self.owned, partial=dict(self.owned['partial'], inode=-1))
        with self.assertRaisesRegex(Refused, 'replaced'): self.guard.cleanup_partial(old)
        atomic_json(self.state / 'pending.json', dict(app_name='someone-else', partial=self.partial.name, phase='dump'))
        with self.assertRaisesRegex(Refused, 'changed'): self.guard.cleanup_partial(self.owned)
        self.assertTrue(self.partial.exists())

    def test_inspection_failure_is_not_container_absence(self):
        with mock.patch.object(runtime, 'invoke', side_effect=Refused('daemon down')):
            with self.assertRaisesRegex(Refused, 'daemon down'): self.guard.inspect('c' * 64)
        with mock.patch.object(runtime, 'invoke', return_value=''):
            self.assertIsNone(self.guard.inspect('c' * 64))

    def test_literal_container_listing_finds_both_name_forms(self):
        data = dict(Id='c' * 64, Name='/' + self.app, Config={'Labels': {runtime.LABEL: self.app}})
        for name in [self.app, '/' + self.app]:
            with self.subTest(name=name):
                listing = json.dumps('unrelated') + '\t' + 'f' * 64 + '\n' + json.dumps(name) + '\t' + 'c' * 64 + '\n'
                with mock.patch.object(runtime, 'invoke', side_effect=[listing, json.dumps([data])]) as call:
                    self.assertEqual(self.guard.inspect('c' * 64), data)
                self.assertEqual(call.call_args_list[0].args[0], ['docker', 'container', 'ls', '-a',
                                 '--no-trunc', '--format', '{{json .Names}}\t{{.ID}}'])
        with mock.patch.object(runtime, 'invoke', return_value=json.dumps(self.app + '-different') + '\t' + 'f' * 64):
            self.assertIsNone(self.guard.inspect('c' * 64))

    def test_renamed_owned_id_is_not_absence(self):
        data = dict(Id='c' * 64, Name='/renamed', Config={'Labels': {runtime.LABEL: self.app}})
        with mock.patch.object(runtime, 'invoke', side_effect=['"renamed"\t' + 'c' * 64, json.dumps([data])]):
            with self.assertRaisesRegex(Refused, 'ownership mismatch'): self.guard.inspect('c' * 64)

    def test_client_peak_memory_is_recorded_with_sample_time(self):
        cg = self.root / ('docker-' + 'c' * 64 + '.scope')
        cg.mkdir()
        for field, value in [('current', 700 * 1024 ** 2), ('peak', 900 * 1024 ** 2), ('max', 2 * runtime.GIB)]:
            (cg / ('memory.' + field)).write_text(str(value))
        (cg / 'memory.stat').write_text('anon 104857600\nfile 629145600\n')
        (cg / 'memory.swap.current').write_text('0')
        with mock.patch.object(runtime, 'cgroup_path', return_value=cg):
            self.guard.observe_client_memory({'Id': 'c' * 64, 'State': {'Pid': 123}})
        self.guard.cleanup_beat('test')
        self.guard.terminal(dict(status='completed', cleanup_verified=True))
        for path in [self.guard.heartbeat, self.guard.result]:
            report = read_json(path)['client_memory']
            self.assertEqual(report['current_bytes'], 700 * 1024 ** 2)
            self.assertEqual(report['peak_observed_bytes'], 900 * 1024 ** 2)
            self.assertEqual(report['limit_bytes'], 2 * runtime.GIB)
            self.assertEqual(report['anon_bytes'], 100 * 1024 ** 2)
            self.assertEqual(report['file_bytes'], 600 * 1024 ** 2)
            self.assertEqual(report['swap_bytes'], 0)
            self.assertTrue(report['observed_at'])

    def test_cleanup_continues_when_heartbeat_cannot_be_written(self):
        original = runtime.atomic_json
        def write(path, value):
            if path == self.guard.heartbeat: raise OSError(errno.ENOSPC, 'heartbeat disk full')
            return original(path, value)
        with mock.patch.object(runtime, 'atomic_json', side_effect=write), mock.patch.object(self.guard, 'stop') as stop:
            self.guard.fail('resource limit')
        stop.assert_called_once_with(self.owned)
        result = read_json(self.guard.result)
        self.assertTrue(result['cleanup_verified'])
        self.assertIn('heartbeat disk full', result['cleanup_telemetry_error'])

    def test_corrupt_heartbeat_cannot_prevent_owned_stop(self):
        self.guard.heartbeat.write_text('{bad json')
        self.guard.heartbeat.chmod(0o600)
        with mock.patch.object(self.guard, 'stop') as stop:
            self.guard.fail('telemetry corrupt')
        stop.assert_called_once_with(self.owned)
        result = read_json(self.guard.result)
        self.assertTrue(result['cleanup_verified'])
        self.assertTrue(result['cleanup_telemetry_error'])

    def test_container_replacement_and_label_mismatch_refused(self):
        for label, cid in [(self.app, 'e' * 64), ('another-run', 'c' * 64)]:
            with self.subTest(label=label, cid=cid):
                data = dict(Id=cid, Name='/' + self.app, Config={'Labels': {runtime.LABEL: label}})
                with mock.patch.object(runtime, 'invoke', side_effect=[json.dumps(self.app) + '\t' + 'c' * 64, json.dumps([data])]):
                    with self.assertRaises(Refused): self.guard.inspect('c' * 64)

    def test_backend_pid_reuse_is_not_cancelled(self):
        owned = dict(self.owned, backend=dict(pid=222, started='old'))
        with mock.patch.object(self.guard, 'query', return_value='[{"pid":222,"started":"new"}]') as query:
            with self.assertRaisesRegex(Refused, 'identity changed'): self.guard.backends(owned)
        self.assertEqual(query.call_count, 1)

    def test_stop_targets_only_owned_container_and_backend(self):
        data = dict(Id='c' * 64, State={'Running': True})
        backend = {'pid': 222, 'started': '2026-09-27 01:00:00+00'}
        with mock.patch.object(self.guard, 'inspect', side_effect=[data, dict(data, State={'Running': False}), None]), \
                mock.patch.object(self.guard, 'backends', side_effect=[[backend], [backend], []]), \
                mock.patch.object(self.guard, 'query', return_value='t') as query, \
                mock.patch.object(runtime, 'invoke', return_value='') as run, mock.patch.object(runtime.time, 'sleep'):
            self.guard.stop(self.owned)
        self.assertEqual([c.args[0] for c in run.call_args_list], [
            ['docker', 'stop', '--time', '5', 'c' * 64], ['docker', 'rm', '-v', 'c' * 64]])
        for call in query.call_args_list:
            sql = call.args[0]
            self.assertIn('pid=222', sql)
            self.assertIn("backend_start='2026-09-27 01:00:00+00'", sql)
            self.assertIn("application_name='" + self.app + "'", sql)
            self.assertIn("usename='example' AND datname='example'", sql)

    def test_supervisor_refuses_owner_loss_cancel_and_monitor_failure(self):
        for cause in ('owner', 'cancel', 'telemetry', 'exercise', 'enospc'):
            with self.subTest(cause=cause):
                if cause == 'cancel': atomic_json(self.guard.cancel, {'requested': True})
                else: self.guard.cancel.unlink(missing_ok=True)
                self.guard.exercise = 0 if cause == 'exercise' else None
                running = dict(State={'Status': 'running', 'Running': True})
                problem = OSError('telemetry missing') if cause == 'telemetry' else None
                if cause == 'enospc': running = dict(State={'Status': 'exited', 'ExitCode': 153, 'OOMKilled': False})
                with mock.patch.object(self.guard, 'prepare', return_value=self.owned), \
                        mock.patch.object(runtime, 'invoke', return_value=''), \
                        mock.patch.object(runtime, 'proc_identity', return_value=['other'] if cause == 'owner' else ['boot', 'start']), \
                        mock.patch.object(self.guard, 'inspect', return_value=running), \
                        mock.patch.object(self.guard, 'database'), \
                        mock.patch.object(self.guard, 'observe_client_memory'), \
                        mock.patch.object(runtime, 'resources', side_effect=problem, return_value=safe_resources()), \
                        mock.patch.object(self.guard, 'fail') as fail:
                    self.guard.supervise()
                fail.assert_called_once()

    def test_completed_dump_does_not_apply_post_dump_pressure_guard(self):
        with mock.patch.object(self.guard, 'prepare', return_value=self.owned), \
                mock.patch.object(runtime, 'invoke', return_value=''), \
                mock.patch.object(self.guard, 'inspect', return_value={'State': {'Status': 'exited', 'ExitCode': 0, 'OOMKilled': False}}), \
                mock.patch.object(self.guard, 'stop') as stop, \
                mock.patch.object(runtime, 'resources', side_effect=Refused('post-dump pressure')) as resources:
            self.guard.supervise()
        resources.assert_not_called()
        stop.assert_called_once_with(self.owned)
        self.assertEqual(read_json(self.guard.result)['status'], 'completed')
        self.assertTrue(self.partial.exists())

    def test_prepared_container_is_bounded_and_uses_exact_db_image(self):
        db = dict(Image='sha256:immutable', Config={'Volumes': {'/var/lib/postgresql': {}}},
                  NetworkSettings={'Networks': {'db': {'IPAddress': '172.18.0.2'}}})
        with mock.patch.object(self.guard, 'database', return_value=(db, self.db)), \
                mock.patch.object(runtime, 'resources', return_value=safe_resources()), \
                mock.patch.dict(os.environ, {'DOCKER_HOST': '', 'DOCKER_CONTEXT': ''}), \
                mock.patch.object(runtime, 'invoke', side_effect=['unix:///var/run/docker.sock', 'c' * 64]) as run:
            result = self.guard.prepare()
        command = run.call_args.args[0]
        for expected in ['--memory=2g', '--memory-swap=2g', '--pids-limit=32', '--cpus=1',
                         '--pull=never', '--read-only', 'sha256:immutable', 'container:' + 'd' * 64]:
            self.assertIn(expected, command)
        self.assertIn('fsize=' + str(26 * runtime.GIB) + ':' + str(26 * runtime.GIB), command)
        self.assertNotIn('not-in-command', ' '.join(command))
        self.assertIn('172.18.0.2', command)
        self.assertNotIn('127.0.0.1', command)
        self.assertTrue(result['creation_complete'])

    def test_dead_watchdog_is_not_success(self):
        # Exercise the real fork/wait path. The injected child exits abruptly.
        for p in self.guard.run.iterdir(): p.unlink()
        self.guard.run.rmdir()
        with mock.patch.object(runtime.Guard, 'supervise', side_effect=lambda self: os._exit(9), autospec=True):
            with self.assertRaisesRegex(Refused, 'watchdog exited'):
                runtime.protect_dump(self.p, self.partial, self.app, 18 * runtime.GIB)
        self.assertTrue(self.partial.exists())
        self.assertEqual(self.completed.read_bytes(), b'previous verified backup')

    def test_slow_cleanup_gets_separate_deadline_and_keeps_terminal_result(self):
        # Scale the budgets down but really fork/wait: cleanup exceeds the
        # ordinary heartbeat timeout and must not be killed or retried.
        def cleanup(g):
            g.cleanup_beat('slow_cleanup')
            time.sleep(1.2)
            g.terminal(dict(status='completed', at=runtime.now(), cleanup_verified=True))
        app = '143-backup-' + 'e' * 32
        with mock.patch.object(runtime.Guard, 'supervise', cleanup), \
                mock.patch.object(runtime.Guard, 'fail', side_effect=AssertionError('must not retry successful cleanup')), \
                mock.patch.object(runtime, 'WATCHDOG_TIMEOUT', .5), \
                mock.patch.object(runtime, 'CLEANUP_TIMEOUT', 3):
            result = runtime.protect_dump(self.p, self.partial, app, 18 * runtime.GIB)
        self.assertEqual(result['status'], 'completed')
        self.assertTrue(result['cleanup_verified'])

    def test_terminal_result_wins_a_watchdog_timeout_race(self):
        def complete(g):
            g.terminal(dict(status='completed', at=runtime.now(), cleanup_verified=True))
            time.sleep(2)  # Durable result exists but child has not exited.
        app = '143-backup-' + 'f' * 32
        with mock.patch.object(runtime.Guard, 'supervise', complete), \
                mock.patch.object(runtime.Guard, 'fail', side_effect=AssertionError('must preserve completed result')), \
                mock.patch.object(runtime, 'WATCHDOG_TIMEOUT', .5):
            result = runtime.protect_dump(self.p, self.partial, app, 18 * runtime.GIB)
        self.assertEqual(result['status'], 'completed')

    def test_client_exit_during_memory_sample_uses_fresh_terminal_state(self):
        running = {'State': {'Status': 'running', 'Running': True}}
        completed = {'State': {'Status': 'exited', 'ExitCode': 0, 'OOMKilled': False}}
        with mock.patch.object(self.guard, 'prepare', return_value=self.owned), \
                mock.patch.object(runtime, 'invoke', return_value=''), \
                mock.patch.object(self.guard, 'inspect', side_effect=[running, completed, completed]), \
                mock.patch.object(self.guard, 'database'), \
                mock.patch.object(self.guard, 'observe_client_memory', side_effect=FileNotFoundError('exited')), \
                mock.patch.object(self.guard, 'stop') as stop:
            self.guard.supervise()
        stop.assert_called_once_with(self.owned)
        self.assertEqual(read_json(self.guard.result)['status'], 'completed')

    def test_real_process_signals_and_parent_death_retain_lock_until_cleanup(self):
        # Real fork, signals and flock, with only external Docker/DB/proc data
        # replaced. Works on macOS too; Linux /proc parsing has separate tests.
        fixture = r'''
import fcntl, os, sys, time, types
from pathlib import Path
from unittest import mock
import pg_backup_runtime as r
from pg_backup_state import atomic_json, identity
from pg_backup_runtime_test import safe_resources
root=Path(sys.argv[1]); state=root/'.backup-state'; state.mkdir(mode=0o700)
partial=root/'.new.dump.partial.test'; partial.write_bytes(b'partial'); partial.chmod(0o600)
app='143-backup-'+'b'*32
atomic_json(state/'pending.json', dict(app_name=app,partial=partial.name,phase='dump'))
lock=os.open(root/'lock',os.O_CREAT|os.O_RDWR,0o600); fcntl.flock(lock,fcntl.LOCK_EX)
p=types.SimpleNamespace(root=root,state=state,reserve=20*r.GIB,lock_fd=lock)
def process(pid):
 os.kill(pid,0)
 return ['boot',str(pid)]
def prepare(g):
 owned=dict(partial=identity(partial),container_id='c'*64,database={'pid':123},creation_started=True,creation_complete=True)
 atomic_json(g.record,owned)
 (root/'started').write_text(str(os.getpid()))
 return owned
def stop(g,owned):
 (root/'stopping').write_text('yes')
 time.sleep(.5)
 (root/'stopped').write_text('yes')
def sample(*args):
 s=safe_resources();s['monotonic']=time.monotonic();return s
with mock.patch.object(r,'proc_identity',side_effect=process), mock.patch.object(r.Guard,'prepare',prepare), \
 mock.patch.object(r.Guard,'stop',stop), mock.patch.object(r,'invoke',return_value=''), \
 mock.patch.object(r.Guard,'inspect',return_value={'State':{'Running':True,'Status':'running'}}), \
 mock.patch.object(r.Guard,'database'), mock.patch.object(r.Guard,'backends'), \
 mock.patch.object(r.Guard,'observe_client_memory'), \
 mock.patch.object(r,'resources',side_effect=sample), mock.patch.object(r,'INTERVAL',.05):
 r.protect_dump(p,partial,app,18*r.GIB)
'''
        for mode in ('term', 'hup', 'int', 'parent_kill', 'watchdog_kill'):
            with self.subTest(mode=mode), tempfile.TemporaryDirectory() as temporary:
                root = Path(temporary)
                env = dict(os.environ, PYTHONPATH=str(Path(__file__).parent), PYTHONDONTWRITEBYTECODE='1')
                child = subprocess.Popen([sys.executable, '-c', fixture, str(root)], env=env,
                                         stdout=subprocess.DEVNULL, stderr=subprocess.PIPE, start_new_session=True)
                try:
                    deadline = time.monotonic() + 5
                    while not (root / 'started').exists() and child.poll() is None and time.monotonic() < deadline:
                        time.sleep(.02)
                    self.assertTrue((root / 'started').exists(), 'fixture must reach guarded dump')
                    if mode == 'watchdog_kill':
                        os.kill(int((root / 'started').read_text()), signal.SIGKILL)
                    else:
                        sig = {'term': signal.SIGTERM, 'hup': signal.SIGHUP, 'int': signal.SIGINT,
                               'parent_kill': signal.SIGKILL}[mode]
                        child.send_signal(sig)
                    if mode == 'parent_kill': child.wait(timeout=2)
                    deadline = time.monotonic() + 5
                    while not (root / 'stopping').exists() and time.monotonic() < deadline:
                        time.sleep(.01)
                    self.assertTrue((root / 'stopping').exists(), 'only owned cleanup must start')
                    with (root / 'lock').open('rb') as lock:
                        with self.assertRaises(BlockingIOError):
                            fcntl.flock(lock.fileno(), fcntl.LOCK_EX | fcntl.LOCK_NB)
                    deadline = time.monotonic() + 5
                    marker = root / '.backup-state' / 'pending.json'
                    while marker.exists() and time.monotonic() < deadline: time.sleep(.02)
                    self.assertFalse(marker.exists(), 'proven cleanup must release only this marker')
                    self.assertFalse((root / '.new.dump.partial.test').exists())
                    self.assertNotEqual(child.wait(timeout=2), 0, 'a cancelled dump must fail')
                finally:
                    if child.poll() is None: child.kill()
                    child.communicate(timeout=2)


if __name__ == '__main__':
    unittest.main(verbosity=2)
