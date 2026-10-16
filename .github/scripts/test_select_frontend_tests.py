import importlib.util
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch


spec = importlib.util.spec_from_file_location(
    "select_frontend_tests", Path(__file__).with_name("select_frontend_tests.py")
)
selector = importlib.util.module_from_spec(spec)
spec.loader.exec_module(selector)


class FrontendTestSelection(unittest.TestCase):
    def test_shared_inputs_require_full_coverage(self):
        paths = [
            ".github/workflows/ci.yml",
            ".github/scripts/select_frontend_tests.py",
            "frontend/package.json",
            "frontend/package-lock.json",
            "frontend/vitest.config.ts",
            "frontend/tsconfig.json",
            "frontend/src/test/setup.ts",
            "frontend/src/test/mocks/handlers.ts",
            "frontend/src/components/providers.tsx",
            "frontend/src/components/ui/button.tsx",
            "frontend/src/lib/api.ts",
            "frontend/src/app/layout.tsx",
            "frontend/src/app/globals.css",
            "frontend/src/app/(dashboard)/layout.tsx",
        ]
        for path in paths:
            with self.subTest(path=path):
                self.assertTrue(selector.needs_full_suite([path]))

    def test_leaf_changes_retain_affected_tests(self):
        paths = [
            [],
            ["internal/models/issue.go"],
            ["frontend/src/app/(dashboard)/issues/page.tsx"],
            ["frontend/src/app/(dashboard)/issues/page.test.tsx"],
            ["frontend/e2e/session-activity.spec.ts"],
        ]
        for changed in paths:
            with self.subTest(paths=changed):
                self.assertFalse(selector.needs_full_suite(changed))

    def test_events_and_real_git_diffs(self):
        with tempfile.TemporaryDirectory() as directory:
            def git(*args):
                return subprocess.run(
                    ["git", "-C", directory, *args], check=True,
                    capture_output=True, text=True,
                ).stdout.strip()

            def write(path):
                file = Path(directory, path)
                file.parent.mkdir(parents=True, exist_ok=True)
                file.write_text("fixture\n", encoding="utf-8")

            git("init", "--quiet")
            git("config", "user.name", "CI Test")
            git("config", "user.email", "ci@example.test")
            write("frontend/src/test/setup.ts")
            git("add", ".")
            git("commit", "--quiet", "-m", "base")
            base = git("rev-parse", "HEAD")
            write("frontend/src/app/(dashboard)/issues/page.tsx")
            git("add", ".")
            git("commit", "--quiet", "-m", "leaf")

            real_run = subprocess.run

            def run(*args, **kwargs):
                return real_run(*args, cwd=directory, **kwargs)

            with patch.object(selector.subprocess, "run", side_effect=run):
                for event in ("pull_request", "push"):
                    with self.subTest(event=event):
                        self.assertEqual(selector.select_scope(event, base), (False, base))
                for event in ("schedule", "workflow_dispatch"):
                    with self.subTest(event=event):
                        self.assertEqual(selector.select_scope(event, ""), (True, ""))
                self.assertEqual(selector.select_scope("push", "0" * 40), (True, ""))
                with self.assertRaisesRegex(ValueError, "PR base commit is unavailable"):
                    selector.select_scope("pull_request", "missing-base")

                output = Path(directory, "scope-output")
                real_run(
                    [sys.executable, "-B", spec.origin], cwd=directory,
                    env={**os.environ, "GITHUB_EVENT_NAME": "pull_request",
                         "PR_BASE_SHA": base, "GITHUB_OUTPUT": str(output)},
                    check=True, capture_output=True, text=True,
                )
                self.assertEqual(output.read_text(), f"full=false\nbase={base}\n")

                # Moving a shared file out of the shared tree must still force
                # full coverage, even when Git would detect it as a rename.
                leaf_base = git("rev-parse", "HEAD")
                git("mv", "frontend/src/test/setup.ts", "fixture with spaces.ts")
                git("commit", "--quiet", "-m", "move shared setup")
                self.assertEqual(selector.select_scope("pull_request", leaf_base), (True, leaf_base))
                git("mv", "fixture with spaces.ts", "frontend/src/test/setup.ts")
                git("commit", "--quiet", "-m", "restore shared setup")
                restored = git("rev-parse", "HEAD")
                git("rm", "frontend/src/test/setup.ts")
                git("commit", "--quiet", "-m", "delete shared setup")
                self.assertEqual(selector.select_scope("push", restored), (True, restored))

                previous_tip = git("rev-parse", "HEAD")
                git("checkout", "--quiet", "--detach", base)
                write("frontend/src/app/(dashboard)/other/page.tsx")
                git("add", ".")
                git("commit", "--quiet", "-m", "rewritten history")
                self.assertEqual(selector.select_scope("push", previous_tip), (True, ""))


if __name__ == "__main__":
    unittest.main()
