#!/usr/bin/env python3
"""Regression tests for union coverage and rejection of incomplete evidence."""

import contextlib
import io
from pathlib import Path
import tempfile
import unittest
from unittest.mock import patch

import merge_go_coverage as merge


class MergeCoverageTests(unittest.TestCase):
    def profiles(self, root, *contents):
        paths = []
        for index, content in enumerate(contents):
            path = Path(root) / f"suite-{index}.out"
            path.write_text(content)
            paths.append(path)
        return paths

    def test_counts_statement_once_and_preserves_uncovered_blocks(self):
        for mode in ("count", "atomic", "set"):
            with self.subTest(mode=mode), tempfile.TemporaryDirectory() as root:
                paths = self.profiles(root,
                    f"mode: {mode}\nexample/total.go:1.1,2.1 4 0\nexample/total.go:3.1,4.1 2 1\n",
                    f"mode: {mode}\nexample/total.go:1.1,2.1 4 1\nexample/total.go:3.1,4.1 2 0\nexample/zero.go:1.1,2.1 8 0\n",
                )
                expected = f"mode: {mode}\nexample/total.go:1.1,2.1 4 1\nexample/total.go:3.1,4.1 2 1\nexample/zero.go:1.1,2.1 8 0\n"
                self.assertEqual(merge.merge_profiles(paths), expected, "union must retain uncovered backend code and count shared statements once")

    def test_duplicate_blocks_from_test_binaries_add_counts(self):
        with tempfile.TemporaryDirectory() as root:
            paths = self.profiles(root, "mode: atomic\nexample/a.go:1.1,2.1 3 2\nexample/a.go:1.1,2.1 3 4\n", "mode: atomic\nexample/a.go:1.1,2.1 3 1\n")
            self.assertEqual(merge.merge_profiles(paths), "mode: atomic\nexample/a.go:1.1,2.1 3 7\n", "duplicate test binaries must not multiply the denominator")

    def test_invalid_inputs_fail_closed(self):
        cases = {
            "empty": "", "header only": "mode: count\n", "mode": "mode: unknown\n",
            "malformed": "mode: count\nnot a block\n", "negative": "mode: count\na.go:1.1,2.1 2 -1\n",
            "zero line": "mode: count\na.go:0.1,2.1 2 1\n", "reversed": "mode: count\na.go:3.1,2.1 2 1\n",
            "zero-only": "mode: count\na.go:1.1,2.1 0 0\n", "bad set count": "mode: set\na.go:1.1,2.1 2 2\n",
            "conflicting counts": "mode: count\na.go:1.1,2.1 2 1\na.go:1.1,2.1 3 1\n",
        }
        for name, content in cases.items():
            with self.subTest(name=name), tempfile.TemporaryDirectory() as root:
                paths = self.profiles(root, content)
                with self.assertRaises(ValueError, msg="malformed or empty profiles must never contribute a passing report"):
                    merge.merge_profiles(paths)
        with tempfile.TemporaryDirectory() as root:
            paths = self.profiles(root, "mode: count\na.go:1.1,2.1 2 1\n", "mode: atomic\na.go:1.1,2.1 2 1\n")
            with self.assertRaises(ValueError, msg="unit and integration modes must agree"):
                merge.merge_profiles(paths)
            with self.assertRaises(FileNotFoundError, msg="missing suite evidence must fail"):
                merge.merge_profiles([Path(root) / "missing.out"])
            with self.assertRaises(ValueError, msg="no suites must fail"):
                merge.merge_profiles([])

    def test_failed_merge_does_not_write_output_or_overwrite_input(self):
        for scenario in ("missing input", "overwrite input"):
            with self.subTest(scenario=scenario), tempfile.TemporaryDirectory() as root:
                paths = self.profiles(root, "mode: count\na.go:1.1,2.1 2 1\n")
                output = Path(root) / "coverage.out"
                output.write_text("existing evidence")
                args = ["merge_go_coverage.py", str(output), str(paths[0]), str(Path(root) / "missing.out")]
                if scenario == "overwrite input":
                    args = ["merge_go_coverage.py", str(paths[0]), str(paths[0])]
                with patch("sys.argv", args), contextlib.redirect_stderr(io.StringIO()):
                    self.assertEqual(merge.main(), 1, "failed evidence collection must stop CI")
                self.assertEqual(output.read_text(), "existing evidence", "invalid merge must not emit a partial passing profile")
                self.assertEqual(paths[0].read_text(), "mode: count\na.go:1.1,2.1 2 1\n", "source evidence must be preserved")

    def test_cli_writes_complete_union(self):
        with tempfile.TemporaryDirectory() as root:
            paths = self.profiles(root, "mode: count\na.go:1.1,2.1 2 1\n", "mode: count\na.go:3.1,4.1 4 0\n")
            output = Path(root) / "coverage.out"
            with patch("sys.argv", ["merge_go_coverage.py", str(output), *map(str, paths)]):
                self.assertEqual(merge.main(), 0, "valid suites must produce a report")
            self.assertEqual(output.read_text(), "mode: count\na.go:1.1,2.1 2 1\na.go:3.1,4.1 4 0\n", "output must preserve exact block counts")


if __name__ == "__main__":
    unittest.main()
