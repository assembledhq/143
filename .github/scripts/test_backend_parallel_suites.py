#!/usr/bin/env python3
"""Protect complete test selection and the required check across parallel workers."""

import os
from pathlib import Path
import re
import shutil
import subprocess
import tempfile
import textwrap
import unittest


ROOT = Path(__file__).resolve().parents[1]
WORKFLOW = ROOT / "workflows" / "ci.yml"
SUITES = ("unit", "database", "commands")
PROFILES = {
    "unit": ("coverage-unit-other.out", "coverage-unit-agent.out"),
    "database": (
        "coverage-unit-demoseed.out", "coverage-unit-db.out", "coverage-unit-worker.out",
        "coverage-browser.out", "coverage-integration.out",
    ),
}
TEST_OWNERS = {
    "Test CI helper scripts": "unit",
    "Test review dependency recipe": "unit",
    "Run backend unit tests outside database, demoseed, agent, and worker packages": "unit",
    "Run agent unit tests": "unit",
    "Test offline review export archive utilities": "unit",
    "Validate demo seed against the migrated schema": "database",
    "Run demoseed tests": "database",
    "Run database tests": "database",
    "Run worker unit tests": "database",
    "Run preview browser lifecycle tests": "database",
    "Run integration tests": "database",
    "Verify preview infrastructure volume lifecycle": "database",
    "Run command tests": "commands",
}


def job_source(name):
    match = re.search(rf"^  {re.escape(name)}:\n(.*?)(?=^  [a-z][a-z-]*:|\Z)",
                      WORKFLOW.read_text(), re.MULTILINE | re.DOTALL)
    if match is None:
        raise AssertionError(f"missing workflow job {name}")
    return match.group(1)


def steps(source):
    return ["      - " + step for step in re.split(r"^      - ", source, flags=re.MULTILINE)[1:]]


def named_step(source, name):
    matches = [step for step in steps(source) if step.startswith(f"      - name: {name}\n")]
    if len(matches) != 1:
        raise AssertionError(f"expected exactly one step named {name}")
    return matches[0]


def run_script(step):
    lines = step.splitlines()
    if "        run: |" not in lines:
        return next(line.removeprefix("        run: ") for line in lines if line.startswith("        run: "))
    start = lines.index("        run: |") + 1
    body = []
    for line in lines[start:]:
        if line and not line.startswith("          "):
            break
        body.append(line)
    return textwrap.dedent("\n".join(body))


def upload_paths(step):
    match = re.search(r"^          path: \|\n((?:            [^\n]+\n?)+)", step, re.MULTILINE)
    if match is None:
        raise AssertionError("suite upload must enumerate its coverage profiles")
    return tuple(line.strip() for line in match.group(1).splitlines())


class BackendParallelSuitesTests(unittest.TestCase):
    def test_every_test_group_has_exactly_one_matrix_owner(self):
        source = job_source("backend-test-suites")
        self.assertIn("suite: [unit, database, commands]", source,
                      "the matrix must run every independent suite")
        self.assertIn("fail-fast: false", source,
                      "one failed suite must not cancel the other suites or their diagnostics")
        self.assertIn(
            "runs-on: ${{ matrix.suite == 'unit' && 'ubuntu-latest' || 'blacksmith-4vcpu-ubuntu-2404' }}",
            source, "only the unit suite should use GitHub while database and command suites use Blacksmith",
        )
        self.assertIn("DATABASE_URL: postgres://", source,
                      "each matrix worker must own a local database service")
        self.assertIn("run: bash .github/scripts/start-ci-postgres.sh", source)
        cleanup = named_step(source, "Stop CI Postgres")
        self.assertIn("if: always()", cleanup,
                      "each worker must clean up its own database even after test failure")
        self.assertIn('docker rm --force --volumes "$POSTGRES_CONTAINER"', cleanup)

        observed = {}
        for step in steps(source):
            # Find executable test commands, excluding comments and shell variables.
            if not re.search(r"go test |python3 -B (?:-m unittest|\.143/review-dependencies_test)|run_go_test_shards\.py|make demo-seed-check", step):
                continue
            match = re.match(r"      - name: ([^\n]+)", step)
            self.assertIsNotNone(match, "test groups need a named owner for complete partitioning")
            name = match.group(1)
            guards = re.findall(r"matrix\.suite == '([^']+)'", step)
            self.assertEqual(len(guards), 1, f"{name} must belong to exactly one worker")
            self.assertNotIn(name, observed, "a test group must not be duplicated across matrix steps")
            observed[name] = guards[0]
        self.assertEqual(observed, TEST_OWNERS,
                         "every existing suite must run once; new suites require an explicit owner")

    def test_required_check_rejects_unsuccessful_workers_before_reading_coverage(self):
        source = job_source("backend-test")
        self.assertIn("name: Backend Test\n", source,
                      "branch protection must keep the existing required check name")
        self.assertIn("needs: [changes, backend-test-suites]", source)
        self.assertIn("!cancelled()", source,
                      "failed dependencies must still produce a failed required check")
        worker_gate = re.search(r"^    if: (.+)$", job_source("backend-test-suites"), re.MULTILINE).group(1)
        aggregate_gate = re.search(r"^    if: (.+)$", source, re.MULTILINE).group(1)
        self.assertIn(f"({worker_gate})", aggregate_gate,
                      "the aggregate must use the same change selection as its workers")
        gate = named_step(source, "Verify backend suites")
        self.assertIn("SUITE_RESULT: ${{ needs.backend-test-suites.result }}", gate)
        self.assertLess(source.index("name: Verify backend suites"), source.index("uses: actions/download-artifact@"),
                        "failed/skipped workers must not be accepted because stale artifacts exist")
        for result in ("success", "failure", "cancelled", "skipped", ""):
            with self.subTest(result=result):
                completed = subprocess.run(
                    ["bash", "-euo", "pipefail", "-c", run_script(gate)],
                    env={**os.environ, "SUITE_RESULT": result}, capture_output=True,
                    text=True, timeout=5, check=False,
                )
                self.assertEqual(completed.returncode == 0, result == "success",
                                 "only a complete successful matrix may reach coverage aggregation")
        required = job_source("required-checks")
        self.assertIn('"${{ needs.backend-test.result }}"', required,
                      "the final gate must depend on the unchanged aggregate result")

    def test_coverage_artifacts_have_disjoint_complete_profiles_and_explicit_downloads(self):
        workers = job_source("backend-test-suites")
        aggregate = job_source("backend-test")
        uploads = [step for step in steps(workers) if "uses: actions/upload-artifact@" in step
                   and "name: coverage-suite-" in step]
        self.assertEqual(len(uploads), len(PROFILES),
                         "only the coverage-producing workers should publish profiles")
        observed = {}
        for step in uploads:
            suite = re.search(r"name: coverage-suite-([a-z]+)", step).group(1)
            self.assertIn(f"matrix.suite == '{suite}'", step,
                          "each artifact must be written by its matching worker")
            self.assertIn("overwrite: true", step,
                          "rerunning a suite must replace only that suite's prior coverage")
            self.assertIn("!cancelled()", step,
                          "failed test evidence should remain downloadable")
            observed[suite] = upload_paths(step)
        self.assertEqual(observed, PROFILES,
                         "parallel uploads must partition all seven existing coverage inputs without collisions")
        downloads = [step for step in steps(aggregate) if "uses: actions/download-artifact@" in step]
        self.assertEqual(len(downloads), len(PROFILES))
        self.assertEqual(sorted(re.search(r"          name: ([^\n]+)", step).group(1) for step in downloads),
                         ["coverage-suite-database", "coverage-suite-unit"],
                         "download both required producers explicitly; a partial wildcard match must not pass")
        self.assertTrue(all("pattern:" not in step and "continue-on-error:" not in step for step in downloads))
        self.assertIn("uses: actions/upload-artifact@", named_step(aggregate, "Upload coverage report"))
        self.assertIn("$BACKEND_COVERAGE_MIN", run_script(named_step(aggregate, "Check backend coverage floor")))

    def test_aggregate_merge_requires_every_profile_and_preserves_all_statements(self):
        aggregate = job_source("backend-test")
        script = "\n".join(run_script(named_step(aggregate, name)) for name in
                           ("Combine backend unit coverage", "Combine backend coverage"))
        profiles = tuple(profile for group in PROFILES.values() for profile in group)
        for missing in (None, *profiles):
            with self.subTest(missing=missing), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                scripts = root / ".github/scripts"
                scripts.mkdir(parents=True)
                shutil.copyfile(Path(__file__).with_name("merge_go_coverage.py"), scripts / "merge_go_coverage.py")
                # Only the human-readable go tool summary is stubbed. Profile
                # parsing and completeness checks execute the production merger.
                go = root / "go"
                go.write_text('#!/bin/sh\n[ "$1" = tool ] && [ "$2" = cover ]\n')
                go.chmod(0o700)
                for index, profile in enumerate(profiles):
                    if profile != missing:
                        (root / profile).write_text(f"mode: count\nexample/file{index}.go:1.1,1.2 1 1\n")
                result = subprocess.run(
                    ["bash", "-eo", "pipefail", "-c", script], cwd=root,
                    env={**os.environ, "PATH": f"{root}:{os.environ['PATH']}"},
                    capture_output=True, text=True, timeout=5, check=False,
                )
                self.assertEqual(result.returncode == 0, missing is None,
                                 "missing coverage from any producer must stop the required check")
                if missing is None:
                    self.assertEqual((root / "coverage.out").read_text().splitlines(),
                                     ["mode: count", *[f"example/file{i}.go:1.1,1.2 1 1" for i in range(len(profiles))]],
                                     "combined coverage must retain every worker's unique statements")
                else:
                    self.assertFalse((root / "coverage.out").exists(),
                                     "incomplete evidence must not create a seemingly valid final profile")


if __name__ == "__main__":
    unittest.main()
