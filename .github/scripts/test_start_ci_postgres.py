#!/usr/bin/env python3
"""Prove database startup follows successful image recovery and retains its ID."""

import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest

from test_pull_ci_image import STUB, pull_calls


SCRIPT = Path(__file__).with_name("start-ci-postgres.sh")
TARGET = "postgres:17"
PRIMARY = "public.ecr.aws/docker/library/postgres:17"
FALLBACK = "mirror.gcr.io/library/postgres:17"
RUN = [
    "docker", "run", "--detach", "--publish", "5432:5432",
    "--env", "POSTGRES_USER=onefortythree",
    "--env", "POSTGRES_PASSWORD=dev",
    "--env", "POSTGRES_DB=onefortythree",
    "--health-cmd", "pg_isready", "--health-interval", "10s",
    "--health-timeout", "5s", "--health-retries", "5",
    TARGET, "-c", "max_locks_per_transaction=256",
]


class StartCIPostgresTests(unittest.TestCase):
    def test_startup_after_registry_recovery(self):
        recovered = pull_calls(PRIMARY) + pull_calls(FALLBACK) + [["docker", "tag", FALLBACK, TARGET]]
        exhausted = (pull_calls(PRIMARY) + pull_calls(FALLBACK) + pull_calls(TARGET))
        cases = [
            ("primary succeeds", [0], 0, 0, pull_calls(PRIMARY) + [["docker", "tag", PRIMARY, TARGET], RUN], "container_id=fixture-container\n"),
            ("rate limit recovers before startup", [1, 0], 0, 0, recovered + [RUN], "container_id=fixture-container\n"),
            ("all sources fail without starting database", [1] * 6, 0, 1, exhausted + [["sleep", "5"]] + exhausted, ""),
            ("container startup failure propagates", [1, 0], 1, 1, recovered + [RUN], ""),
        ]
        for name, statuses, run_status, status, expected_calls, expected_output in cases:
            with self.subTest(name=name), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                for stub_name in ("docker", "timeout", "sleep"):
                    stub = root / stub_name
                    stub.write_text(STUB)
                    stub.chmod(0o755)
                output = root / "output"
                result = subprocess.run(
                    ["bash", str(SCRIPT)],
                    env={
                        **os.environ, "PATH": f"{root}:{os.environ['PATH']}",
                        "STUB_ROOT": str(root), "PULL_STATUSES": json.dumps(statuses),
                        "RUN_STATUS": str(run_status), "GITHUB_OUTPUT": str(output),
                    }, capture_output=True, text=True, timeout=10, check=False,
                )
                calls = [json.loads(line) for line in (root / "calls.jsonl").read_text().splitlines()]
                self.assertEqual(result.returncode, status, result.stderr)
                self.assertEqual(calls, expected_calls, "only a recovered image may start a database with the required lock budget")
                self.assertEqual(output.read_text() if output.exists() else "", expected_output, "only successful startup may expose a container ID for cleanup")


if __name__ == "__main__":
    unittest.main()
