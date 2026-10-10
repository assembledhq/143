#!/usr/bin/env python3
"""Exercise CI image-download recovery without Docker or registry access."""

import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest


SCRIPT = Path(__file__).with_name("pull-ci-image.sh")
TARGET = "postgres:17-alpine"
SOURCES = (
    "public.ecr.aws/docker/library/postgres:17-alpine",
    "mirror.gcr.io/library/postgres:17-alpine",
    TARGET,
)
STUB = """#!/usr/bin/env python3
import json
import os
from pathlib import Path
import subprocess
import sys

root = Path(os.environ['STUB_ROOT'])
name = Path(sys.argv[0]).name
with (root / 'calls.jsonl').open('a') as out:
    out.write(json.dumps([name, *sys.argv[1:]]) + '\\n')
if name == 'timeout':
    sys.exit(subprocess.run(sys.argv[3:], check=False).returncode)
if name == 'sleep':
    sys.exit(0)
if sys.argv[1] == 'tag':
    sys.exit(int(os.environ.get('TAG_STATUS', '0')))
if sys.argv[1] != 'pull':
    sys.exit(99)
counter = root / 'attempts'
attempt = int(counter.read_text()) if counter.exists() else 0
counter.write_text(str(attempt + 1))
statuses = json.loads(os.environ['PULL_STATUSES'])
sys.exit(statuses[attempt] if attempt < len(statuses) else 99)
"""


def pull_calls(source):
    return [
        ["timeout", "--kill-after=5s", "90s", "docker", "pull", source],
        ["docker", "pull", source],
    ]


class PullCIImageTests(unittest.TestCase):
    def run_script(self, args, statuses, tag_status=0):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            for name in ("docker", "timeout", "sleep"):
                stub = root / name
                stub.write_text(STUB)
                stub.chmod(0o755)
            env = {
                **os.environ,
                "PATH": f"{root}:{os.environ['PATH']}",
                "STUB_ROOT": str(root),
                "PULL_STATUSES": json.dumps(statuses),
                "TAG_STATUS": str(tag_status),
            }
            result = subprocess.run(
                ["bash", str(SCRIPT), *args],
                env=env,
                capture_output=True,
                text=True,
                timeout=10,
                check=False,
            )
            trace = root / "calls.jsonl"
            calls = [json.loads(line) for line in trace.read_text().splitlines()] if trace.exists() else []
            return result, calls

    def test_registry_recovery(self):
        first = pull_calls(SOURCES[0])
        second = pull_calls(SOURCES[1])
        third = pull_calls(SOURCES[2])
        backoff = [["sleep", "5"]]
        cases = [
            ("primary succeeds", [0], 0, 0, first + [["docker", "tag", SOURCES[0], TARGET]]),
            ("rate limit falls back", [1, 0], 0, 0, first + second + [["docker", "tag", SOURCES[1], TARGET]]),
            ("timeout falls back", [124, 0], 0, 0, first + second + [["docker", "tag", SOURCES[1], TARGET]]),
            ("both mirrors fail", [1, 1, 0], 0, 0, first + second + third + [["docker", "tag", TARGET, TARGET]]),
            ("retry after backoff", [1, 1, 1, 0], 0, 0, first + second + third + backoff + first + [["docker", "tag", SOURCES[0], TARGET]]),
            ("last attempt succeeds", [1, 1, 1, 1, 1, 0], 0, 0, first + second + third + backoff + first + second + third + [["docker", "tag", TARGET, TARGET]]),
            ("all registries fail", [1] * 6, 0, 1, first + second + third + backoff + first + second + third),
            ("tag failure propagates", [0], 1, 1, first + [["docker", "tag", SOURCES[0], TARGET]]),
        ]
        for name, statuses, tag_status, expected_status, expected_calls in cases:
            with self.subTest(name=name):
                result, calls = self.run_script([TARGET, *SOURCES], statuses, tag_status)
                self.assertEqual(result.returncode, expected_status, result.stderr)
                self.assertEqual(calls, expected_calls, "pulls, timeout bounds, backoff, and tagging must match the recovery path")

    def test_requires_target_and_source(self):
        for args in ([], [TARGET]):
            with self.subTest(args=args):
                result, calls = self.run_script(args, [])
                self.assertEqual(result.returncode, 2, "missing image arguments should fail before contacting Docker")
                self.assertEqual(calls, [], "invalid arguments must not pull or tag images")


if __name__ == "__main__":
    unittest.main()
