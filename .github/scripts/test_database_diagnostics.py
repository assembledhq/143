#!/usr/bin/env python3
"""Exercise the actual workflow shell, including failure and privacy boundaries."""

import json
import os
import shutil
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
    if sys.argv[1] == "list":
        sys.stdout.write(os.environ["GO_PACKAGES"])
        sys.exit(0)
    if "-list" in sys.argv:
        print("\n".join([f"TestSynthetic{i}" for i in range(8)] + ["ok\texample/db\t0.01s"]))
        sys.exit(0)
    for arg in sys.argv:
        if arg.startswith("-coverprofile="):
            mode = next(value.split("=", 1)[1] for value in sys.argv if value.startswith("-covermode="))
            source = "demoseed" if "./internal/demoseed" in sys.argv else "db"
            Path(arg.split("=", 1)[1]).write_text(f"mode: {mode}\nexample/{source}.go:1.1,1.2 1 1\n")
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
        scripts = root / ".github/scripts"
        scripts.mkdir(parents=True, exist_ok=True)
        for name in ("run_go_test_shards.py", "merge_go_coverage.py"):
            shutil.copyfile(Path(__file__).with_name(name), scripts / name)
        for name in ("go", "tee", "timeout", "docker"):
            path = root / name
            path.write_text(STUB)
            path.chmod(0o755)
        return {
            **os.environ, "PATH": f"{root}:{os.environ['PATH']}",
            "STUB_ROOT": str(root), "RUNNER_TEMP": str(root),
            "POSTGRES_CONTAINER": "synthetic-container", "GITHUB_EVENT_NAME": "pull_request",
            "GO_PACKAGES": "github.com/assembledhq/143/cmd/migrate\ngithub.com/assembledhq/143/cmd/demo-seed\ngithub.com/assembledhq/143/cmd/wait-postgres\n",
            "TEST_EVENT": EVENT, "GO_STATUS": "0", "TEE_STATUS": "0",
            "PRIVATE_ERROR": PRIVATE_ERROR, **overrides,
        }

    def test_original_test_status_and_json_capture(self):
        suites = [
            ("Run database tests", "ci-db-diagnostics", None),
            ("Run command tests", "ci-command-diagnostics", "command partitions"),
            ("Run demoseed tests", "ci-demoseed-diagnostics", ["go", "test", "-json", "./internal/demoseed", "-p=1", "-parallel=1", "-count=1", "-coverpkg=./internal/...", "-coverprofile=coverage-unit-demoseed.out", "-covermode=count", "-timeout=300s"]),
        ]
        cases = [(suite, go, tee, expected) for suite in suites for go, tee, expected in [(0, 0, 0), (7, 0, 7), (0, 9, 9), (7, 9, 7)]]
        for (step, folder, arguments), go_status, tee_status, expected in cases:
            with self.subTest(step=step, go=go_status, tee=tee_status), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                result = subprocess.run(
                    ["bash", "-eo", "pipefail", "-c", run_script(step)],
                    cwd=root, env=self.environment(root, GO_STATUS=str(go_status), TEE_STATUS=str(tee_status)),
                    capture_output=True, text=True, timeout=10, check=False,
                )
                self.assertEqual(result.returncode, expected, result.stderr)
                diagnostics = root / folder
                expected_events = {"Run database tests": 4, "Run command tests": 5}.get(step, 1)
                self.assertEqual((diagnostics / "go-test.jsonl").read_text(), EVENT * expected_events, "stderr must not corrupt per-test JSON")
                self.assertEqual([json.loads(line)["Test"] for line in (diagnostics / "go-test.jsonl").read_text().splitlines()], ["TestSynthetic"] * expected_events)
                self.assertEqual((diagnostics / "go-test.stderr.log").read_text(), "synthetic non-JSON warning\n" * expected_events)
                self.assertIn("synthetic non-JSON warning", result.stderr, "stderr must remain visible in CI")
                self.assertEqual((diagnostics / "exit-status.txt").read_text(), f"go_test={go_status}\njson_capture={tee_status}\n")
                calls = [json.loads(line) for line in (root / "calls.jsonl").read_text().splitlines()]
                go = [call for call in calls if call[0] == "go"]
                if arguments == "command partitions":
                    self.assertEqual(go[:3], [
                        ["go", "list", "./cmd/..."],
                        ["go", "test", "-json", "github.com/assembledhq/143/cmd/demo-seed", "github.com/assembledhq/143/cmd/wait-postgres", "-p=1", "-parallel=2", "-count=1", "-timeout=300s"],
                        ["go", "test", "-list", ".", "./cmd/migrate", "-p=1"],
                    ], "ordinary commands must all run once while migration roots are discovered separately")
                    self.assertEqual(len(go), 7, "ordinary command failure must not skip discovery or any migration shard")
                    for command in go[3:]:
                        for required in ("./cmd/migrate", "-p=1", "-parallel=2", "-count=1", "-timeout=300s", "-run"):
                            self.assertIn(required, command)
                        self.assertFalse(any(arg.startswith("-cover") for arg in command), "command sharding must preserve the original uninstrumented build")
                    manifest = json.loads((diagnostics / "migrate-shards/shard-status.json").read_text())
                    self.assertEqual(manifest["package"], "./cmd/migrate")
                    self.assertEqual([shard["exit_status"] for shard in manifest["shards"]], [go_status] * 4)
                    self.assertEqual(sorted(name for shard in manifest["shards"] for name in shard["roots"]), [f"TestSynthetic{i}" for i in range(8)])
                    self.assertEqual((diagnostics / "partition-status.txt").read_text(), f"ordinary_commands={go_status}\nmigration_shards={go_status}\n")
                elif arguments is not None:
                    self.assertEqual(go, [arguments], "diagnostics must preserve PR test coverage, budgets, and bounded fixture overlap")
                else:
                    self.assertEqual(go[0], ["go", "test", "-list", ".", "./internal/db"])
                    self.assertEqual(len(go), 5, "discovery must be followed by all four shards even when one fails")
                    for command in go[1:]:
                        for required in ("-parallel=2", "-count=1", "-coverpkg=./internal/...", "-covermode=count", "-timeout=300s"):
                            self.assertIn(required, command)
                    manifest = json.loads((diagnostics / "shard-status.json").read_text())
                    self.assertEqual([shard["exit_status"] for shard in manifest["shards"]], [go_status] * 4)
                    self.assertEqual(sorted(name for shard in manifest["shards"] for name in shard["roots"]), [f"TestSynthetic{i}" for i in range(8)])

    def test_bounded_aggregate_only_postgres_snapshot(self):
        cases = [(scope, folder, status) for scope, folder in [("database", "ci-db-diagnostics"), ("command", "ci-command-diagnostics"), ("demoseed", "ci-demoseed-diagnostics")] for status in (0, 1, 124)]
        for scope, folder, status in cases:
            with self.subTest(scope=scope, status=status), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                result = subprocess.run(
                    ["bash", str(COLLECTOR)], cwd=root,
                    env=self.environment(root, COLLECT_STATUS=str(status), CI_POSTGRES_DIAGNOSTICS_SCOPE=scope),
                    capture_output=True, text=True, timeout=10, check=False,
                )
                self.assertEqual(result.returncode, 0, "unavailable diagnostics must not mask the test outcome")
                outputs = sorted((root / folder).iterdir())
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

    def test_command_package_discovery_fails_closed_before_any_test_invocation(self):
        for packages in ("", "github.com/assembledhq/143/cmd/migrate\n", "github.com/assembledhq/143/cmd/wait-postgres\n"):
            with self.subTest(packages=packages), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                result = subprocess.run(
                    ["bash", "-eo", "pipefail", "-c", run_script("Run command tests")],
                    cwd=root, env=self.environment(root, GO_PACKAGES=packages),
                    capture_output=True, text=True, timeout=10, check=False,
                )
                self.assertEqual(result.returncode, 1, "incomplete command inventory must not silently select the current directory or skip a partition")
                calls = [json.loads(line) for line in (root / "calls.jsonl").read_text().splitlines()]
                self.assertEqual([call for call in calls if call[0] == "go"], [["go", "list", "./cmd/..."]])
                diagnostics = root / "ci-command-diagnostics"
                self.assertEqual((diagnostics / "go-test.jsonl").read_text(), "")
                self.assertEqual((diagnostics / "exit-status.txt").read_text(), "go_test=1\njson_capture=0\n")

    def test_failed_step_upload_precedes_database_cleanup(self):
        source = WORKFLOW.read_text()
        for scope, identifier in [("database", "database_tests"), ("command", "command_tests"), ("demoseed", "demoseed_tests")]:
            with self.subTest(scope=scope):
                collect = f"Collect {scope} test PostgreSQL diagnostics"
                upload = f"Upload {scope} test diagnostics"
                run = "\n".join(workflow_step(f"Run {scope} tests"))
                self.assertIn(f"id: {identifier}", run)
                self.assertLess(source.index(f"Run {scope} tests"), source.index(collect))
                self.assertLess(source.index(collect), source.index(upload))
                self.assertLess(source.index(upload), source.index("- name: Stop CI Postgres"))
                for name in (collect, upload):
                    step = "\n".join(workflow_step(name))
                    self.assertIn("!cancelled()", step)
                    self.assertIn(f"steps.{identifier}.outcome == 'failure'", step, "failure must not skip its diagnostics")
                self.assertIn("retention-days: 7", "\n".join(workflow_step(upload)))
                if scope != "database":
                    self.assertIn(f"CI_POSTGRES_DIAGNOSTICS_SCOPE: {scope}", "\n".join(workflow_step(collect)))
                    self.assertIn("${{ runner.temp }}/ci-" + scope + "-diagnostics/", "\n".join(workflow_step(upload)))

    def test_demoseed_has_one_complete_bounded_owner_and_merged_coverage(self):
        prefix = "github.com/assembledhq/143/internal/"
        packages = [prefix + name for name in ("db", "demoseed", "services/agent", "worker", "api/handlers", "services/thread")]
        cases = [("pull_request", "count", [], "300s"), ("push", "atomic", ["-race"], "360s")]
        for event, mode, race, package_timeout in cases:
            with self.subTest(event=event), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                env = self.environment(root, GITHUB_EVENT_NAME=event, GO_PACKAGES="\n".join(packages) + "\n")
                for step in ("Run backend unit tests outside database, demoseed, agent, and worker packages", "Run demoseed tests"):
                    result = subprocess.run(
                        ["bash", "-eo", "pipefail", "-c", run_script(step)],
                        cwd=root, env=env, capture_output=True, text=True, timeout=10, check=False,
                    )
                    self.assertEqual(result.returncode, 0, result.stderr)
                calls = [json.loads(line) for line in (root / "calls.jsonl").read_text().splitlines()]
                commands = [call for call in calls if call[:2] == ["go", "test"]]
                self.assertEqual(len(commands), 2, "the package partition must produce exactly one general and one demoseed invocation")
                self.assertEqual(commands[0][3:5], packages[-2:], "unrelated packages must remain in the general batch")
                self.assertNotIn(prefix + "demoseed", commands[0], "the full-schema demoseed package must not overlap the general batch")
                self.assertEqual(commands[1], ["go", "test", "-json", "./internal/demoseed", "-p=1", "-parallel=1", "-count=1", "-coverpkg=./internal/...", "-coverprofile=coverage-unit-demoseed.out", f"-covermode={mode}", *race, f"-timeout={package_timeout}"], "run the complete package with bounded fixtures and the same coverage/race mode, without filtering tests")
                for profile in ("coverage-unit-db.out", "coverage-unit-worker.out", "coverage-unit-agent.out"):
                    (root / profile).write_text(f"mode: {mode}\nexample/remaining.go:1.1,1.2 1 1\n")
                merge_step = workflow_step("Combine backend unit coverage")
                merge_command = next(line.removeprefix("        run: ") for line in merge_step if line.startswith("        run: "))
                merged = subprocess.run(
                    ["bash", "-eo", "pipefail", "-c", merge_command],
                    cwd=root, env=env, capture_output=True, text=True, timeout=10, check=False,
                )
                self.assertEqual(merged.returncode, 0, merged.stderr)
                self.assertEqual((root / "coverage-unit.out").read_text().splitlines(), [f"mode: {mode}", "example/db.go:1.1,1.2 1 1", "example/demoseed.go:1.1,1.2 1 1", "example/remaining.go:1.1,1.2 1 3"], "the actual workflow merge must retain the isolated package's unique coverage contribution")
                upload = "\n".join(workflow_step("Upload coverage report"))
                self.assertIn("            coverage-unit-demoseed.out", upload, "retain the raw demoseed profile when later tests or the coverage floor fail")

    def test_unknown_scope_is_rejected_without_echoing_input(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            result = subprocess.run(
                ["bash", str(COLLECTOR)], cwd=root,
                env=self.environment(root, CI_POSTGRES_DIAGNOSTICS_SCOPE=PRIVATE_ERROR),
                capture_output=True, text=True, timeout=10, check=False,
            )
            self.assertEqual(result.returncode, 1, "only fixed diagnostic destinations are allowed")
            self.assertFalse((root / "calls.jsonl").exists(), "invalid scopes must not execute external commands")
            for marker in PRIVATE_MARKERS:
                self.assertNotIn(marker, result.stdout + result.stderr, "invalid scopes must not publish caller input")


if __name__ == "__main__":
    unittest.main()
