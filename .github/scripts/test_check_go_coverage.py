#!/usr/bin/env python3
"""Regression tests for Go coverage parsing and fail-closed enforcement."""

import contextlib
import io
import subprocess
import unittest
from unittest.mock import patch

import check_go_coverage as gate


class CoverageGateTests(unittest.TestCase):
    def test_report_parsing(self):
        cases = [
            ("summary only", "total: (statements) 75.0%\n", "75.0"),
            ("function named totalSize", "example/total.go:1: totalSize 0.0%\ntotal: (statements) 63.6%\n", "63.6"),
            ("tabs and padding", "  total:\t(statements)\t70.0%  \n", "70.0"),
            ("zero", "total: (statements) 0%", "0"),
            ("complete", "total: (statements) 100.0%", "100.0"),
        ]
        for name, report, expected in cases:
            with self.subTest(name=name):
                self.assertEqual(str(gate.total_coverage(report)), expected, "only the exact summary row should determine coverage")

    def test_invalid_reports_fail_closed(self):
        cases = [
            "", "example.go:1: totalSize 100.0%", "total: (statements) 70.0",
            "total: (statements) NaN%", "total: (statements) -1%",
            "total: (statements) 101%", "total: (functions) 80.0%",
            "total: (statements) 70% extra", "total: (statements) 70%\ntotal: (statements) 80%",
        ]
        for report in cases:
            with self.subTest(report=report):
                with self.assertRaises(ValueError, msg="missing, ambiguous, or malformed summaries must never pass"):
                    gate.total_coverage(report)

    def test_gate_exit_status(self):
        cases = [
            ("below minimum", "69.9", "70", "75", 1, "::error::"),
            ("at minimum", "70.0", "70", "75", 0, "::warning::"),
            ("at target", "75.0", "70", "75", 0, None),
            ("above target", "100.0", "70", "75", 0, None),
            ("zero minimum", "0.0", "0", "0", 0, None),
            ("invalid minimum", "80.0", "NaN", "75", 1, "::error::"),
            ("invalid target", "80.0", "70", "101", 1, "::error::"),
            ("reversed thresholds", "80.0", "75", "70", 1, "::error::"),
        ]
        for name, coverage, minimum, target, expected_status, annotation in cases:
            with self.subTest(name=name):
                output = io.StringIO()
                # This reproduces the report that previously produced an awk
                # syntax error inside an if statement and silently passed CI.
                report = f"example.go:1: totalSize 0.0%\ntotal: (statements) {coverage}%\n"
                with patch("sys.argv", ["check_go_coverage.py", "coverage.out", minimum, target]), \
                     patch.object(gate.subprocess, "run", return_value=subprocess.CompletedProcess([], 0, report, "")) as run, \
                     contextlib.redirect_stdout(output), contextlib.redirect_stderr(output):
                    status = gate.main()
                self.assertEqual(status, expected_status, "the gate must enforce the exact minimum and reject invalid configuration")
                if annotation:
                    self.assertIn(annotation, output.getvalue(), "CI should explain a failed floor or missed target")
                else:
                    self.assertNotIn("::", output.getvalue(), "coverage at or above target should not emit warnings or errors")
                if name in {"invalid minimum", "invalid target", "reversed thresholds"}:
                    run.assert_not_called()
                else:
                    run.assert_called_once_with(["go", "tool", "cover", "-func", "coverage.out"], check=True, capture_output=True, text=True)

    def test_report_generation_failures(self):
        cases = [
            ("missing Go", FileNotFoundError("go unavailable")),
            ("invalid profile", subprocess.CalledProcessError(1, ["go", "tool", "cover"], output="total: (statements) 100%", stderr="invalid profile\n")),
        ]
        for name, error in cases:
            with self.subTest(name=name):
                output = io.StringIO()
                with patch("sys.argv", ["check_go_coverage.py", "coverage.out", "70", "75"]), \
                     patch.object(gate.subprocess, "run", side_effect=error), \
                     contextlib.redirect_stderr(output):
                    status = gate.main()
                self.assertEqual(status, 1, "a failed coverage command must fail even if it emitted a passing summary")
                self.assertIn("::error::Unable to verify", output.getvalue(), "CI should explain why coverage could not be verified")


if __name__ == "__main__":
    unittest.main()
