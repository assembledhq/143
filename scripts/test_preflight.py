"""Exercise routing and real Git ref handling without running expensive tools."""
import importlib.util
import os
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest.mock import patch

SPEC = importlib.util.spec_from_file_location('preflight', Path(__file__).with_name('preflight.py'))
preflight = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(preflight)


class RoutingTests(unittest.TestCase):
    def test_change_routes(self):
        cases = [
            ({'README.md'}, set()),
            ({'internal/cli/client.go'}, {'make lint'}),
            ({'migrations/000001_example.up.sql'}, {'make lint'}),
            ({'frontend/src/features/example.tsx'}, {'npm run typecheck', 'npm run lint', 'npx --no-install vitest run --changed=base --passWithNoTests'}),
        ]
        for files, expected in cases:
            with self.subTest(files=files):
                commands, _ = preflight.plan(files, ['base'])
                self.assertEqual({' '.join(cmd) for cmd, _ in commands}, expected)

    def test_frontend_shared_paths_use_ci_full_suite(self):
        for path in ['frontend/src/components/example.tsx', 'frontend/src/lib/api.ts', 'frontend/components.json']:
            with self.subTest(path=path):
                commands, _ = preflight.plan({path}, ['base'])
                self.assertIn((['npx', '--no-install', 'vitest', 'run'], 'frontend'), commands)
                self.assertFalse(any('--changed=' in arg for cmd, _ in commands for arg in cmd))

    def test_shared_inputs_expand_checks(self):
        for path in ['Makefile', '.github/workflows/ci.yml', '.githooks/pre-push', 'scripts/preflight.py']:
            with self.subTest(path=path):
                commands, _ = preflight.plan({path}, ['base'])
                commands = [' '.join(cmd) for cmd, _ in commands]
                for required in ['make lint', 'npm run build', 'go test ./deploy -count=1 -timeout=120s', 'make test-deploy-scripts']:
                    self.assertIn(required, commands)

    def test_extended_is_explicit(self):
        for extended in [False, True]:
            with self.subTest(extended=extended):
                commands, _ = preflight.plan({'Makefile'}, ['base'], extended)
                self.assertEqual((['make', 'test-pr'], '.') in commands, extended)
                self.assertEqual((['make', 'security-check'], '.') in commands, extended)
                self.assertEqual((['npm', 'run', 'test:session-transcript-e2e'], 'frontend') in commands, extended)

    def test_checks_do_not_inherit_hook_repository_state(self):
        with patch.object(preflight, 'git', return_value='GIT_DIR\nGIT_WORK_TREE\nGIT_INDEX_FILE'), patch.dict(os.environ, {'GIT_DIR': '/fixture/repository', 'GIT_WORK_TREE': '/fixture/worktree', 'GIT_INDEX_FILE': '/fixture/index', 'PREFLIGHT_TEST_MARKER': 'keep'}):
            environment = preflight.check_environment()
        for name in ['GIT_DIR', 'GIT_WORK_TREE', 'GIT_INDEX_FILE']:
            self.assertNotIn(name, environment)
        self.assertEqual(environment['PREFLIGHT_TEST_MARKER'], 'keep')

    def test_ci_runs_preflight_tests_and_tool_versions_match(self):
        root = Path(__file__).resolve().parents[1]
        workflow = (root / '.github/workflows/ci.yml').read_text()
        makefile = (root / 'Makefile').read_text()
        self.assertIn("python3 -B -m unittest discover -s scripts -p 'test_preflight.py'", workflow)
        self.assertIn("- 'scripts/preflight.py'", workflow)
        for version in ['v2.10.1', 'v2.22.4']:
            self.assertIn(version, workflow)
            self.assertIn(version, makefile)



class GitTests(unittest.TestCase):
    def setUp(self):
        # Git exports repository-local variables to hooks. Fixture Git commands
        # must never inherit those and accidentally target the real checkout.
        environment = patch.dict(os.environ, {key: value for key, value in os.environ.items() if not key.startswith('GIT_')}, clear=True)
        environment.start()
        os.environ['GIT_CONFIG_GLOBAL'] = os.devnull
        os.environ['GIT_CONFIG_NOSYSTEM'] = '1'
        self.addCleanup(environment.stop)
        self.temp = tempfile.TemporaryDirectory()
        self.addCleanup(self.temp.cleanup)
        self.root = Path(self.temp.name)
        self.run_git('init', '-q')
        self.run_git('config', 'user.name', 'Fixture')
        self.run_git('config', 'user.email', 'fixture@example.test')
        self.run_git('config', 'core.hooksPath', '/dev/null')
        (self.root / 'old.go').write_text('package example\n')
        self.run_git('add', '.')
        self.run_git('commit', '-qm', 'base')
        self.base = self.run_git('rev-parse', 'HEAD')
        self.run_git('update-ref', 'refs/remotes/origin/main', self.base)
        self.run_git('mv', 'old.go', 'new.go')
        self.run_git('commit', '-qm', 'rename')
        self.head = self.run_git('rev-parse', 'HEAD')
        self.patcher = patch.object(preflight, 'git', self.run_git)
        self.patcher.start()
        self.addCleanup(self.patcher.stop)

    def run_git(self, *args):
        return subprocess.check_output(['git', '-C', str(self.root), *args], text=True, stderr=subprocess.DEVNULL).strip()

    def test_rename_routes_both_paths(self):
        self.assertEqual(preflight.changed(self.base, self.head), {'old.go', 'new.go'})

    def test_push_ranges(self):
        zero = '0' * 40
        cases = [
            ('existing', self.head, self.base, [(self.base, self.head)]),
            ('new', self.head, zero, [(self.base, self.head)]),
            ('deleted', zero, self.base, []),
        ]
        for name, local, remote, expected in cases:
            with self.subTest(name=name):
                line = f'refs/heads/topic {local} refs/heads/topic {remote}\n'
                self.assertEqual(preflight.push_ranges([line]), expected)

    def test_wrong_checkout_rejected(self):
        with self.assertRaisesRegex(ValueError, 'exact HEAD'):
            preflight.push_ranges([f'refs/heads/other {self.base} refs/heads/other {self.head}'])

    def test_unknown_remote_rejected(self):
        with self.assertRaisesRegex(ValueError, 'Fetch the destination'):
            preflight.push_ranges([f'refs/heads/topic {self.head} refs/heads/topic {"1" * 40}'])

    def test_tag_push_rejected(self):
        with self.assertRaisesRegex(ValueError, 'branch pushes only'):
            preflight.push_ranges([f'refs/tags/v1 {self.head} refs/tags/v1 {"0" * 40}'])

    def test_new_branch_without_main_rejected(self):
        self.run_git('update-ref', '-d', 'refs/remotes/origin/main')
        with self.assertRaisesRegex(ValueError, 'Fetch origin/main'):
            preflight.push_ranges([f'refs/heads/topic {self.head} refs/heads/topic {"0" * 40}'])

    def test_force_push_compares_destination_tree(self):
        self.assertFalse(preflight.requires_full_frontend([self.base]))
        self.run_git('reset', '--hard', self.base)
        self.assertTrue(preflight.requires_full_frontend([self.head]))
        commands, _ = preflight.plan({'frontend/src/features/example.tsx'}, [self.head], full_frontend=preflight.requires_full_frontend([self.head]))
        self.assertIn((['npx', '--no-install', 'vitest', 'run'], 'frontend'), commands)
        self.assertEqual(preflight.push_ranges([f'refs/heads/topic {self.base} refs/heads/topic {self.head}']), [(self.head, self.base)])

    def test_hooks_install_and_preserve_custom_configuration(self):
        makefile = Path(__file__).resolve().parents[1] / 'Makefile'
        (self.root / 'Makefile').write_text(makefile.read_text())
        hooks = self.root / '.githooks'
        hooks.mkdir()
        for name in ['pre-commit', 'pre-push']:
            path = hooks / name
            path.write_text('#!/bin/sh\nexit 0\n')
            path.chmod(0o755)
        for configured, expected in [('custom-hooks', 2), ('.githooks', 0)]:
            with self.subTest(configured=configured):
                self.run_git('config', 'core.hooksPath', configured)
                result = subprocess.run(['make', 'hooks-install'], cwd=self.root, text=True, capture_output=True)
                self.assertEqual(result.returncode, expected, result.stderr)
                self.assertEqual(self.run_git('config', '--get', 'core.hooksPath'), configured)

    def test_multiple_updates_checked(self):
        lines = [f'refs/heads/{name} {self.head} refs/heads/{name} {self.base}' for name in ['a', 'b']]
        self.assertEqual(preflight.push_ranges(lines), [(self.base, self.head)] * 2)

    def test_real_hook_propagates_check_failure(self):
        root = Path(__file__).resolve().parents[1]
        (self.root / 'scripts').mkdir()
        (self.root / 'scripts/preflight.py').write_text((root / 'scripts/preflight.py').read_text())
        (self.root / '.github/scripts').mkdir(parents=True)
        (self.root / '.github/scripts/select_frontend_tests.py').write_text((root / '.github/scripts/select_frontend_tests.py').read_text())
        (self.root / '.githooks').mkdir()
        hook = self.root / '.githooks/pre-push'
        hook.write_text((root / '.githooks/pre-push').read_text())
        hook.chmod(0o755)
        self.run_git('add', '.')
        self.run_git('commit', '-qm', 'install fixture hook')
        base = self.run_git('rev-parse', 'HEAD')
        (self.root / 'frontend').mkdir()
        (self.root / 'frontend/component.tsx').write_text('export const example = 1;')
        self.run_git('add', '.')
        self.run_git('commit', '-qm', 'frontend change')
        head = self.run_git('rev-parse', 'HEAD')
        with tempfile.TemporaryDirectory() as binaries:
            npm = Path(binaries) / 'npm'
            npm.write_text('#!/bin/sh\n[ -z "${GIT_DIR+x}" ] || exit 9\n[ -z "${GIT_WORK_TREE+x}" ] || exit 9\necho simulated-check-failure >&2\nexit 7\n')
            npm.chmod(0o755)
            result = subprocess.run([str(hook), 'origin', 'example'], cwd=self.root,
                                    env={**os.environ, 'GIT_DIR': str(self.root / '.git'), 'GIT_WORK_TREE': str(self.root), 'PATH': binaries + os.pathsep + os.environ['PATH']},
                                    input=f'refs/heads/topic {head} refs/heads/topic {base}\n', text=True, capture_output=True)
        self.assertEqual(result.returncode, 1)
        self.assertIn('simulated-check-failure', result.stderr)
        self.assertNotIn('npm run lint', result.stdout)
        self.assertNotIn('checks passed', result.stdout)

    def test_dirty_push_rejected_before_commands(self):
        (self.root / 'untracked').write_text('not published')
        script = Path(__file__).with_name('preflight.py').resolve()
        result = subprocess.run(['python3', str(script), '--push', '--dry-run'], cwd=self.root,
                                input=f'refs/heads/topic {self.head} refs/heads/topic {self.base}\n', text=True, capture_output=True)
        self.assertEqual(result.returncode, 1)
        self.assertIn('Commit or stash', result.stderr)


if __name__ == '__main__':
    unittest.main()
