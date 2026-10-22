#!/usr/bin/env python3
"""Exercise the actual workflow shell, including failure and privacy boundaries."""

import json
import os
from pathlib import Path
import subprocess
import tempfile
import textwrap
import unittest


ROOT = Path(__file__).resolve().parents[1]
WORKFLOW = ROOT / "workflows" / "ci.yml"
COLLECTOR = Path(__file__).with_name("collect-ci-postgres-diagnostics.sh")
EVENT = '{"Action":"pass","Package":"example/db","Test":"TestSynthetic"}\n'
PRIVATE_MARKERS = ("synthetic-password-marker", "SELECT fixture_data_marker", "/synthetic/fixture-path-marker")
PRIVATE_ERROR = f"postgres://fixture:{PRIVATE_MARKERS[0]}@db.example.invalid/fixture {PRIVATE_MARKERS[1]} {PRIVATE_MARKERS[2]}"
STUB = r'''#!/usr/bin/env python3
import json
import os
from pathlib import Path
import sys

root = Path(os.environ["STUB_ROOT"])
name = Path(sys.argv[0]).name
with (root / "calls.jsonl").open("a") as calls:
    calls.write(json.dumps([name, *sys.argv[1:]]) + "\n")
if name == "go":
    sys.stdout.write(os.environ["TEST_EVENT"])
    sys.stderr.write("synthetic non-JSON warning\n")
    sys.exit(int(os.environ["GO_STATUS"]))
if name == "tee":
    data = sys.stdin.read()
    Path(sys.argv[1]).write_text(data)
    sys.stdout.write(data)
    sys.exit(int(os.environ["TEE_STATUS"]))
if name == "timeout":
    if os.environ.get("COLLECT_STATUS", "0") != "0":
        sys.stdout.write(os.environ["PRIVATE_ERROR"])
        sys.stderr.write(os.environ["PRIVATE_ERROR"])
        sys.exit(int(os.environ["COLLECT_STATUS"]))
    result = __import__("subprocess").run(sys.argv[2:], check=False)
    sys.exit(result.returncode)
if name == "docker":
    sys.stdout.write('{"synthetic":"aggregate-only"}\n')
    sys.exit(0)
sys.exit("unexpected stub")
'''


def workflow_step(name):
    lines = WORKFLOW.read_text().splitlines()
    start = lines.index(f"      - name: {name}")
    end = next((i for i in range(start + 1, len(lines)) if lines[i].startswith("      - ")), len(lines))
    return lines[start:end]


def run_script(name):
    lines = workflow_step(name)
    start = lines.index("        run: |") + 1
    script = []
    for line in lines[start:]:
        if line and not line.startswith("          "):
            break
        script.append(line)
    return textwrap.dedent("\n".join(script))


class DatabaseDiagnosticsTests(unittest.TestCase):
    def environment(self, root, **overrides):
        for name in ("go", "tee", "timeout", "docker"):
            path = root / name
            path.write_text(STUB)
            path.chmod(0o755)
        return {
            **os.environ, "PATH": f"{root}:{os.environ['PATH']}",
            "STUB_ROOT": str(root), "RUNNER_TEMP": str(root),
            "POSTGRES_CONTAINER": "synthetic-container", "GITHUB_EVENT_NAME": "pull_request",
            "TEST_EVENT": EVENT, "GO_STATUS": "0", "TEE_STATUS": "0",
            "PRIVATE_ERROR": PRIVATE_ERROR, **overrides,
        }

    def test_original_test_status_and_json_capture(self):
        cases = [(0, 0, 0), (7, 0, 7), (0, 9, 9), (7, 9, 7)]
        for go_status, tee_status, expected in cases:
            with self.subTest(go=go_status, tee=tee_status), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                result = subprocess.run(
                    ["bash", "-eo", "pipefail", "-c", run_script("Run database tests")],
                    cwd=root, env=self.environment(root, GO_STATUS=str(go_status), TEE_STATUS=str(tee_status)),
                    capture_output=True, text=True, timeout=10, check=False,
                )
                self.assertEqual(result.returncode, expected, result.stderr)
                diagnostics = root / "ci-db-diagnostics"
                self.assertEqual((diagnostics / "go-test.jsonl").read_text(), EVENT, "stderr must not corrupt per-test JSON")
                self.assertEqual(json.loads((diagnostics / "go-test.jsonl").read_text())["Test"], "TestSynthetic")
                self.assertEqual((diagnostics / "go-test.stderr.log").read_text(), "synthetic non-JSON warning\n")
                self.assertIn("synthetic non-JSON warning", result.stderr, "stderr must remain visible in CI")
                self.assertEqual((diagnostics / "exit-status.txt").read_text(), f"go_test={go_status}\njson_capture={tee_status}\n")
                calls = [json.loads(line) for line in (root / "calls.jsonl").read_text().splitlines()]
                go = next(call for call in calls if call[0] == "go")
                self.assertEqual(go, ["go", "test", "-json", "./internal/db", "-parallel=2", "-coverpkg=./internal/...", "-coverprofile=coverage-unit-db.out", "-covermode=count", "-timeout=300s"], "diagnostics must preserve PR test coverage, budgets, and bounded fixture overlap")

    def test_bounded_aggregate_only_postgres_snapshot(self):
        for status in (0, 1, 124):
            with self.subTest(status=status), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                result = subprocess.run(
                    ["bash", str(COLLECTOR)], cwd=root,
                    env=self.environment(root, COLLECT_STATUS=str(status)),
                    capture_output=True, text=True, timeout=10, check=False,
                )
                self.assertEqual(result.returncode, 0, "unavailable diagnostics must not mask the test outcome")
                outputs = sorted((root / "ci-db-diagnostics").iterdir())
                self.assertEqual([path.name for path in outputs], ["container-resources.txt", "container-state.json", "postgres-snapshot.json"])
                expected = '{"synthetic":"aggregate-only"}\n' if status == 0 else f'{{"unavailable":true,"exit_status":{status}}}\n'
                self.assertEqual([path.read_text() for path in outputs], [expected] * 3)
                public_output = result.stdout + result.stderr + "".join(path.read_text() for path in outputs)
                for marker in PRIVATE_MARKERS:
                    self.assertNotIn(marker, public_output, "failed commands must not publish raw errors or partial output")
                calls = [json.loads(line) for line in (root / "calls.jsonl").read_text().splitlines()]
                bounded = [call for call in calls if call[0] == "timeout"]
                self.assertEqual(len(bounded), 3)
                self.assertTrue(all(call[1] == "10s" for call in bounded), "every external diagnostic must have a wall-clock bound")
                if status == 0:
                    docker = [call for call in calls if call[0] == "docker"]
                    self.assertEqual([call[1] for call in docker], ["inspect", "stats", "exec"])
                    self.assertNotIn(".State.Error", docker[0][3])
                    self.assertNotIn(".Config.Env", docker[0][3])
                    self.assertIn("default_transaction_read_only=on", docker[2][3])
                    sql = docker[2][-1]
                    for excluded in ("query,", "query ", "datname", "usename", "client_addr", "application_name"):
                        self.assertNotIn(excluded, sql.lower(), "snapshot must not select private identifiers or SQL text")

    def test_failed_step_upload_precedes_database_cleanup(self):
        source = WORKFLOW.read_text()
        collect = "Collect database test PostgreSQL diagnostics"
        upload = "Upload database test diagnostics"
        self.assertLess(source.index(collect), source.index(upload))
        self.assertLess(source.index(upload), source.index("- name: Stop CI Postgres"))
        for name in (collect, upload):
            step = "\n".join(workflow_step(name))
            self.assertIn("!cancelled()", step)
            self.assertIn("steps.database_tests.outcome == 'failure'", step, "failure must not skip its diagnostics")
        self.assertIn("retention-days: 7", "\n".join(workflow_step(upload)))


if __name__ == "__main__":
    unittest.main()
