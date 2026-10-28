#!/usr/bin/env python3
"""Verify complete test selection and failure/coverage handling with a fake Go CLI."""

import importlib.util
import json
import os
from pathlib import Path
import re
import subprocess
import sys
import tempfile
import unittest

SCRIPT = Path(__file__).with_name("run_go_test_shards.py")
spec = importlib.util.spec_from_file_location("run_go_test_shards", SCRIPT)
runner = importlib.util.module_from_spec(spec)
spec.loader.exec_module(runner)
ROOTS = ["TestAlpha", "TestAlphaLonger", "TestBravo", "TestCharlie", "TestDelta", "TestEcho", "FuzzInput", "ExampleValue"]
STUB = r'''#!/usr/bin/env python3
import json
import os
from pathlib import Path
import re
import sys
root = Path(os.environ["STUB_ROOT"])
with (root / "calls.jsonl").open("a") as calls:
    calls.write(json.dumps(sys.argv[1:]) + "\n")
if "-list" in sys.argv:
    print(os.environ["DISCOVERY"])
    sys.exit(int(os.environ.get("DISCOVERY_STATUS", "0")))
invocations = root / "invocations.txt"
index = int(invocations.read_text()) + 1 if invocations.exists() else 1
invocations.write_text(str(index))
pattern = sys.argv[sys.argv.index("-run") + 1]
for name in json.loads(os.environ["ROOTS"]):
    if re.fullmatch(pattern, name):
        print(json.dumps({"Action": "pass", "Test": name}))
print("synthetic compiler warning", file=sys.stderr)
profile = Path(next(arg.split("=", 1)[1] for arg in sys.argv if arg.startswith("-coverprofile=")))
mode = next(arg.split("=", 1)[1] for arg in sys.argv if arg.startswith("-covermode="))
if str(index) != os.environ.get("MISSING_PROFILE_SHARD"):
    text = "invalid profile\n" if str(index) == os.environ.get("BAD_PROFILE_SHARD") else f"mode: {mode}\nexample/db.go:1.1,1.2 1 {index}\n"
    profile.write_text(text)
statuses = json.loads(os.environ.get("GO_STATUSES", "[0,0,0,0]"))
sys.exit(statuses[index - 1])
'''


class SelectionTests(unittest.TestCase):
    def test_complete_disjoint_balanced_stable_root_selection(self):
        found = runner.discover_roots("\n".join(ROOTS + ["BenchmarkIgnored", "ok\texample/db\t0.01s"]))
        groups = runner.partition_roots(found, 4)
        self.assertEqual(groups, runner.partition_roots(list(reversed(found)), 4))
        self.assertEqual(sorted(name for group in groups for name in group), sorted(ROOTS))
        self.assertTrue(all(len(group) == 2 for group in groups))
        self.assertEqual(len(set(name for group in groups for name in group)), len(ROOTS))
        for name in ROOTS:
            matches = [group for group in groups if re.fullmatch(runner.root_pattern(group), name)]
            self.assertEqual(len(matches), 1, "every test, fuzz seed root and example must be selected exactly once")
        self.assertIsNone(re.fullmatch(runner.root_pattern(["TestAlpha"]), "TestAlphaLonger"))
        self.assertEqual(runner.root_pattern(["TestAlpha"]), "^(?:TestAlpha)$", "Go must select the exact root while retaining all its subtests")

    def test_discovery_fails_closed(self):
        cases = ["", "ok\texample/db\t0.01s", "TestAlpha\nTestAlpha\nok\texample/db\t0.01s",
                 "TestAlpha\nTestAlpha/subtest\nok\texample/db\t0.01s", "TestAlpha\nunknown discovery output",
                 "TestAlpha\nok\texample/db\t0.01s\nok\tother/db\t0.01s"]
        for output in cases:
            with self.subTest(output=output), self.assertRaises(ValueError):
                runner.discover_roots(output)
        for roots, shards in [([], 4), (["TestAlpha"], 4), (ROOTS, 0)]:
            with self.subTest(roots=roots, shards=shards), self.assertRaises(ValueError):
                runner.partition_roots(roots, shards)
        with self.assertRaises(ValueError):
            runner.root_pattern([])


class ExecutionTests(unittest.TestCase):
    def invoke(self, root, race=False, **overrides):
        go = root / "go"
        go.write_text(STUB)
        go.chmod(0o755)
        env = {**os.environ, "PATH": f"{root}:{os.environ['PATH']}", "STUB_ROOT": str(root),
               "ROOTS": json.dumps(ROOTS), "DISCOVERY": "\n".join(ROOTS + ["ok\texample/db\t0.01s"]),
               **overrides}
        command = [sys.executable, "-B", str(SCRIPT), "--diagnostics", str(root / "diagnostics"),
                   "--coverprofile", str(root / "coverage.out"), "--covermode", "atomic" if race else "count",
                   "--timeout", "360s" if race else "300s"]
        if race:
            command.append("--race")
        return subprocess.run(command, cwd=root, env=env, capture_output=True, text=True, timeout=10, check=False)

    def test_all_shards_run_and_coverage_is_complete(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            result = self.invoke(root)
            self.assertEqual(result.returncode, 0, result.stderr)
            self.assertEqual(sorted(json.loads(line)["Test"] for line in result.stdout.splitlines()), sorted(ROOTS))
            self.assertEqual((root / "coverage.out").read_text(), "mode: count\nexample/db.go:1.1,1.2 1 10\n")
            manifest = json.loads((root / "diagnostics/shard-status.json").read_text())
            self.assertEqual([shard["exit_status"] for shard in manifest["shards"]], [0] * 4)
            self.assertEqual(sorted(name for shard in manifest["shards"] for name in shard["roots"]), sorted(ROOTS))
            calls = [json.loads(line) for line in (root / "calls.jsonl").read_text().splitlines()]
            self.assertEqual(calls[0], ["test", "-list", ".", "./internal/db"])
            self.assertEqual(len(calls), 5)
            for command in calls[1:]:
                for required in ("-json", "./internal/db", "-parallel=2", "-count=1", "-coverpkg=./internal/...", "-covermode=count", "-timeout=300s"):
                    self.assertIn(required, command)
            self.assertNotIn(str(root), json.dumps(manifest), "artifact command manifests must omit machine-local output paths")

    def test_race_discovery_matches_execution_build_and_preserves_non_pr_budgets(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            result = self.invoke(root, race=True)
            self.assertEqual(result.returncode, 0, result.stderr)
            calls = [json.loads(line) for line in (root / "calls.jsonl").read_text().splitlines()]
            self.assertEqual(len(calls), 5)
            self.assertTrue(all("-race" in command for command in calls), "race-only roots must be discovered by the same build as execution")
            for command in calls[1:]:
                for required in ("-parallel=2", "-count=1", "-covermode=atomic", "-timeout=360s"):
                    self.assertIn(required, command)
            self.assertTrue((root / "coverage.out").read_text().startswith("mode: atomic\n"))

    def test_first_test_failure_precedes_capture_failure_and_runs_later_shards(self):
        for capture_failure in (False, True):
            with self.subTest(capture_failure=capture_failure), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                (root / "coverage.out").write_text("stale coverage")
                if capture_failure:
                    (root / "diagnostics/shard-status.json").mkdir(parents=True)
                result = self.invoke(root, GO_STATUSES="[7,9,0,0]", MISSING_PROFILE_SHARD="1")
                self.assertEqual(result.returncode, 7, result.stderr)
                self.assertEqual((root / "invocations.txt").read_text(), "4")
                self.assertFalse((root / "coverage.out").exists(), "failed partial profiles must not leave a passing aggregate")
                if not capture_failure:
                    manifest = json.loads((root / "diagnostics/shard-status.json").read_text())
                    self.assertEqual([shard["exit_status"] for shard in manifest["shards"]], [7, 9, 0, 0])
                    self.assertEqual(manifest["coverage_status"], "skipped_failed_tests")

    def test_missing_invalid_coverage_or_capture_blocks_success(self):
        for variant in ("missing", "invalid", "capture"):
            with self.subTest(variant=variant), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                override = {}
                if variant == "capture":
                    (root / "diagnostics/shard-status.json").mkdir(parents=True)
                else:
                    override["MISSING_PROFILE_SHARD" if variant == "missing" else "BAD_PROFILE_SHARD"] = "2"
                result = self.invoke(root, **override)
                self.assertEqual(result.returncode, 1, result.stderr)
                self.assertEqual((root / "invocations.txt").read_text(), "4", "coverage/capture failures must not silently drop later test shards")
                if variant != "capture":
                    self.assertFalse((root / "coverage.out").exists())

    def test_invalid_or_failed_discovery_never_runs_empty_shards(self):
        for override in ({"DISCOVERY": "ok\texample/db\t0.01s"}, {"DISCOVERY_STATUS": "7"}, {"DISCOVERY": "TestAlpha\nok\texample/db\t0.01s"}):
            with self.subTest(override=override), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                result = self.invoke(root, **override)
                self.assertNotEqual(result.returncode, 0)
                self.assertFalse((root / "invocations.txt").exists())


if __name__ == "__main__":
    unittest.main()
