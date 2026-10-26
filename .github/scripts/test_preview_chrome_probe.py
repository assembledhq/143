#!/usr/bin/env python3
"""Hermetic checks for the browser probe and the actual CI browser step."""

import json
import os
from pathlib import Path
import shutil
import signal
import subprocess
import tempfile
import textwrap
import time
import unittest


ROOT = Path(__file__).resolve().parents[1]
HELPER = Path(__file__).with_name("probe-preview-chrome.sh")
WORKFLOW = ROOT / "workflows" / "ci.yml"
PRIVATE_MARKERS = ("synthetic-private-marker", "/synthetic/private-path", "synthetic-password")
PRIVATE_OUTPUT = " ".join(PRIVATE_MARKERS)
VERSION = "Google Chrome 143.0.1234.56"
STUB = r'''#!/usr/bin/env python3
import json
import os
from pathlib import Path
import subprocess
import sys

root = Path(os.environ["STUB_ROOT"])
name = Path(sys.argv[0]).name
with (root / "calls.jsonl").open("a") as calls:
    calls.write(json.dumps([name, *sys.argv[1:]]) + "\n")
if name == "timeout":
    if sys.argv[1:3] != ["--kill-after=2s", "5s"]:
        sys.exit(98)
    result = subprocess.run(sys.argv[3:], check=False)
    if os.environ.get("TIMEOUT_KILL"):
        os.kill(os.getpid(), 9)
    sys.exit(result.returncode)
if name in ("google-chrome", "chrome with spaces"):
    responses = json.loads(os.environ.get("CHROME_RESPONSES", "[]"))
    if responses:
        attempt = sum(json.loads(line)[0] == name for line in (root / "calls.jsonl").read_text().splitlines())
        response = responses[min(attempt - 1, len(responses) - 1)]
    else:
        response = {"status": int(os.environ["CHROME_STATUS"]), "stdout": os.environ["CHROME_STDOUT"], "stderr": os.environ["CHROME_STDERR"]}
    sys.stdout.write(response["stdout"])
    sys.stderr.write(response["stderr"])
    sys.exit(response["status"])
if name == "wc":
    sys.stderr.write(os.environ["PRIVATE_OUTPUT"])
    sys.exit(19)
if name == "go":
    sys.exit(int(os.environ.get("GO_STATUS", "0")))
sys.exit(99)
'''


def browser_script():
    lines = WORKFLOW.read_text().splitlines()
    start = lines.index("      - name: Run preview browser lifecycle tests")
    start = lines.index("        run: |", start) + 1
    end = next((i for i in range(start, len(lines)) if lines[i].startswith("      - ")), len(lines))
    script = []
    for line in lines[start:end]:
        if line and not line.startswith("          "):
            break
        script.append(line)
    return textwrap.dedent("\n".join(script))


class PreviewChromeProbeTests(unittest.TestCase):
    def environment(self, root, *, spaces=False, broken_diagnostics=False, **overrides):
        binaries = root / "bin"
        binaries.mkdir()
        runner = root / "runner"
        runner.mkdir()
        names = ["timeout", "google-chrome", "go"]
        if spaces:
            names.append("chrome with spaces")
        if broken_diagnostics:
            names.append("wc")
        for name in names:
            path = binaries / name
            path.write_text(STUB)
            path.chmod(0o755)
        return {
            **os.environ, "PATH": f"{binaries}:{os.environ['PATH']}",
            "STUB_ROOT": str(root), "RUNNER_TEMP": str(runner),
            "CHROME_STDOUT": VERSION + "\n", "CHROME_STDERR": "", "CHROME_STATUS": "0",
            "PRIVATE_OUTPUT": PRIVATE_OUTPUT, "GITHUB_EVENT_NAME": "pull_request", **overrides,
        }

    def calls(self, root):
        path = root / "calls.jsonl"
        return [json.loads(line) for line in path.read_text().splitlines()] if path.exists() else []

    def check_redacted(self, result):
        for marker in PRIVATE_MARKERS:
            self.assertNotIn(marker, result.stdout + result.stderr, "probe output must not reveal raw output or paths")

    def test_one_bounded_attempt_and_sanitized_results(self):
        cases = [
            ("valid", 0, VERSION + "\n", "", 0),
            ("whitespace", 0, VERSION + " \t\n\n", PRIVATE_OUTPUT, 0),
            ("timeout", 124, PRIVATE_OUTPUT, PRIVATE_OUTPUT, 124),
            ("kill_grace", 137, PRIVATE_OUTPUT, PRIVATE_OUTPUT, 137),
            ("other_failure", 23, PRIVATE_OUTPUT, PRIVATE_OUTPUT, 23),
            ("wrong_browser", 0, "Chromium 143.0.1234.56\n", PRIVATE_OUTPUT, 1),
            ("prefixed_private_output", 0, "Google Chrome " + PRIVATE_OUTPUT, PRIVATE_OUTPUT, 1),
            ("suffixed_private_output", 0, VERSION + " " + PRIVATE_OUTPUT, PRIVATE_OUTPUT, 1),
            ("extra_line", 0, VERSION + "\n" + PRIVATE_OUTPUT, PRIVATE_OUTPUT, 1),
            ("embedded_nul", 0, VERSION + "\x00" + PRIVATE_OUTPUT, PRIVATE_OUTPUT, 1),
        ]
        for name, status, stdout, stderr, expected in cases:
            with self.subTest(name=name), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                # A file, rather than an environment variable, transports NUL output.
                env = self.environment(root, spaces=True, CHROME_STATUS=str(status), CHROME_STDOUT=stdout.replace("\x00", ""), CHROME_STDERR=stderr)
                binary = root / "bin" / "chrome with spaces"
                if "\x00" in stdout:
                    binary.write_text("#!/usr/bin/env python3\nimport sys\nsys.stdout.buffer.write(" + repr(stdout.encode()) + ")\n")
                result = subprocess.run(["bash", str(HELPER), str(binary)], env=env, capture_output=True, text=True, timeout=10, check=False)
                self.assertEqual(result.returncode, expected, result.stderr)
                self.assertEqual(result.stdout, VERSION + "\n" if expected == 0 else "", "only a canonical successful version belongs on stdout")
                self.check_redacted(result)
                calls = self.calls(root)
                timeout_calls = [call for call in calls if call[0] == "timeout"]
                self.assertEqual(timeout_calls, [["timeout", "--kill-after=2s", "5s", str(binary), "--version"]], "the probe must make one attempt with its original five-second budget")
                if "\x00" not in stdout:
                    self.assertEqual([call for call in calls if call[0] == "chrome with spaces"], [["chrome with spaces", "--version"]])
                if expected:
                    self.assertRegex(result.stderr, rf"exit_status={expected} elapsed_seconds=[0-9]+ stdout_bytes=[0-9]+ stderr_bytes=[0-9]+\n$")
                else:
                    self.assertEqual(result.stderr, "", "successful probes must not publish captured stderr")
                self.assertEqual(list((root / "runner").iterdir()), [], "private probe captures must be removed")

    def test_invalid_arguments_and_nonexecutable_are_fail_closed(self):
        cases = [("missing_argument", [], 2), ("extra_argument", ["one", "two"], 2), ("missing_binary", ["missing"], 127), ("not_executable", ["fixture"], 126)]
        for name, args, expected in cases:
            with self.subTest(name=name), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                env = self.environment(root)
                (root / "fixture").write_text(PRIVATE_OUTPUT)
                result = subprocess.run(["bash", str(HELPER), *args], cwd=root, env=env, capture_output=True, text=True, timeout=10, check=False)
                self.assertEqual(result.returncode, expected)
                self.assertEqual(result.stdout, "")
                self.check_redacted(result)
                self.assertEqual(self.calls(root), [], "invalid input must never invoke Chrome")

    def test_only_empty_timeout_retries_once(self):
        success = {"status": 0, "stdout": VERSION + "\n", "stderr": ""}
        timeout = {"status": 124, "stdout": "", "stderr": ""}
        cases = [
            ("recovered", [timeout, success], 0, 2),
            ("exhausted", [timeout, timeout, success], 124, 2),
            ("second_failure", [timeout, {"status": 23, "stdout": PRIVATE_OUTPUT, "stderr": PRIVATE_OUTPUT}], 23, 2),
            ("second_invalid_identity", [timeout, {"status": 0, "stdout": VERSION + PRIVATE_OUTPUT, "stderr": PRIVATE_OUTPUT}], 1, 2),
            ("empty_non_timeout", [{"status": 23, "stdout": "", "stderr": ""}, success], 23, 1),
            ("empty_killed", [{"status": 137, "stdout": "", "stderr": ""}, success], 137, 1),
            ("timeout_with_stdout", [{"status": 124, "stdout": PRIVATE_OUTPUT, "stderr": ""}, success], 124, 1),
            ("timeout_with_stderr", [{"status": 124, "stdout": "", "stderr": PRIVATE_OUTPUT}, success], 124, 1),
            ("timeout_with_whitespace", [{"status": 124, "stdout": "\n", "stderr": ""}, success], 124, 1),
        ]
        for name, responses, expected, attempts in cases:
            with self.subTest(name=name), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                env = self.environment(root, spaces=True, CHROME_RESPONSES=json.dumps(responses))
                binary = root / "bin" / "chrome with spaces"
                result = subprocess.run(["bash", str(HELPER), str(binary)], env=env, capture_output=True, text=True, timeout=10, check=False)
                self.assertEqual(result.returncode, expected, result.stderr)
                self.assertEqual(result.stdout, VERSION + "\n" if expected == 0 else "", "only a validated recovered version can reach stdout")
                self.check_redacted(result)
                self.assertNotIn(str(root), result.stderr, "retry diagnostics must not expose executable paths")
                calls = self.calls(root)
                self.assertEqual([call for call in calls if call[0] == "timeout"], [["timeout", "--kill-after=2s", "5s", str(binary), "--version"]] * attempts, "each permitted attempt must retain its exact timeout and executable")
                self.assertEqual([call for call in calls if call[0] == "chrome with spaces"], [["chrome with spaces", "--version"]] * attempts, "the second attempt must be the last")
                retry_messages = result.stderr.count("Preview Chrome probe retrying:")
                self.assertEqual(retry_messages, attempts - 1, "only an eligible first timeout should emit one retry diagnostic")
                if expected:
                    self.assertRegex(result.stderr, rf"exit_status={expected} elapsed_seconds=[0-9]+ stdout_bytes=[0-9]+ stderr_bytes=[0-9]+\n$")
                self.assertEqual(list((root / "runner").iterdir()), [], "retry captures must be removed after success or failure")

    def test_diagnostic_failure_preserves_probe_status(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            env = self.environment(root, broken_diagnostics=True, CHROME_STATUS="124", CHROME_STDOUT=PRIVATE_OUTPUT, CHROME_STDERR=PRIVATE_OUTPUT)
            result = subprocess.run(["bash", str(HELPER), str(root / "bin" / "google-chrome")], env=env, capture_output=True, text=True, timeout=10, check=False)
            self.assertEqual(result.returncode, 124, "unavailable diagnostics must never replace the original timeout")
            self.assertIn("stdout_bytes=unavailable stderr_bytes=unavailable", result.stderr)
            self.check_redacted(result)
            self.assertEqual(list((root / "runner").iterdir()), [])

    def test_shell_signal_diagnostics_are_private(self):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            env = self.environment(root, TIMEOUT_KILL="1", CHROME_STDOUT=PRIVATE_OUTPUT, CHROME_STDERR=PRIVATE_OUTPUT)
            result = subprocess.run(["bash", str(HELPER), str(root / "bin" / "google-chrome")], env=env, capture_output=True, text=True, timeout=10, check=False)
            self.assertEqual(result.returncode, 137, "a killed probe must preserve its signal-derived exit status")
            self.assertEqual(result.stdout, "")
            self.check_redacted(result)
            self.assertNotIn(str(root), result.stderr, "Bash's signal diagnostic must not reveal executable paths")
            self.assertRegex(result.stderr, r"^Preview Chrome probe failed: reason=probe_exit exit_status=137 elapsed_seconds=[0-9]+ stdout_bytes=[0-9]+ stderr_bytes=[0-9]+\n$")
            self.assertEqual(list((root / "runner").iterdir()), [])

    def test_real_gnu_timeout_bounds_cooperative_and_forced_termination(self):
        gnu_timeout = None
        # Discover the real tool before environment() inserts the hermetic stub.
        for name in ("timeout", "gtimeout"):
            candidate = shutil.which(name)
            if candidate:
                version = subprocess.run([candidate, "--version"], capture_output=True, text=True, timeout=2, check=False)
                if version.returncode == 0 and "GNU coreutils" in version.stdout:
                    gnu_timeout = candidate
                    break
        if gnu_timeout is None:
            self.skipTest("GNU coreutils timeout unavailable; standard macOS lacks the real Linux timeout/kill-grace integration")

        sleeper = textwrap.dedent('''\
            #!/usr/bin/env python3
            import json
            import os
            from pathlib import Path
            import signal
            import sys
            import time

            root = Path(os.environ["STUB_ROOT"])
            (root / "chrome.pid").write_text(str(os.getpid()))
            with (root / "calls.jsonl").open("a") as calls:
                calls.write(json.dumps(["google-chrome", *sys.argv[1:]]) + "\\n")
            attempts = len((root / "calls.jsonl").read_text().splitlines())
            if os.environ["RECOVER"] == "1" and attempts == 2:
                print(os.environ["CHROME_VERSION"])
                sys.exit(0)
            if os.environ["IGNORE_TERM"] == "1":
                signal.signal(signal.SIGTERM, signal.SIG_IGN)
            if os.environ["SILENT"] != "1":
                sys.stdout.write(os.environ["PRIVATE_OUTPUT"])
                sys.stdout.flush()
                sys.stderr.write(os.environ["PRIVATE_OUTPUT"])
                sys.stderr.flush()
            while True:
                time.sleep(60)
            ''')
        cases = [
            ("cooperative_with_output", False, False, False, 124, 1, 4),
            ("forced_with_output", True, False, False, 137, 1, 6),
            ("empty_timeout_recovers", False, True, True, 0, 2, 4),
            ("empty_timeout_exhausted", False, True, False, 124, 2, 9),
        ]
        for name, ignore_term, silent, recover, expected, attempts, minimum_seconds in cases:
            with self.subTest(name=name), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                env = self.environment(root, IGNORE_TERM="1" if ignore_term else "0", SILENT="1" if silent else "0", RECOVER="1" if recover else "0", CHROME_VERSION=VERSION)
                timeout = root / "bin" / "timeout"
                timeout.unlink()
                timeout.symlink_to(gnu_timeout)
                binary = root / "bin" / "google-chrome"
                binary.write_text(sleeper)
                started = time.monotonic()
                process = subprocess.Popen(["bash", str(HELPER), str(binary)], env=env, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True, start_new_session=True)
                try:
                    stdout, stderr = process.communicate(timeout=20)
                except subprocess.TimeoutExpired:
                    # Kill only this test's session/process groups on regression.
                    # The fixture itself sleeps in-process, so it has no children.
                    pid_file = root / "chrome.pid"
                    if pid_file.exists():
                        pid = int(pid_file.read_text())
                        try:
                            if os.getsid(pid) == process.pid:
                                os.killpg(os.getpgid(pid), signal.SIGKILL)
                        except ProcessLookupError:
                            pass
                    try:
                        os.killpg(process.pid, signal.SIGKILL)
                    except ProcessLookupError:
                        pass
                    process.communicate(timeout=3)
                    self.fail("real GNU timeout exceeded the bounded probe deadline")
                elapsed = time.monotonic() - started
                result = subprocess.CompletedProcess(process.args, process.returncode, stdout, stderr)
                self.assertEqual(result.returncode, expected, "real GNU timeout must retain cooperative/forced termination status")
                self.assertGreaterEqual(elapsed, minimum_seconds, "the real configured timeout and kill grace must be exercised")
                self.assertLess(elapsed, 20, "at most two probes must remain bounded by their timeout and kill grace")
                self.assertEqual(result.stdout, VERSION + "\n" if expected == 0 else "")
                self.check_redacted(result)
                self.assertNotIn(str(root), result.stderr, "real signal diagnostics must not publish private executable paths")
                if expected:
                    self.assertRegex(result.stderr, rf"Preview Chrome probe failed: reason=probe_exit exit_status={expected} elapsed_seconds=[0-9]+ stdout_bytes=[0-9]+ stderr_bytes=[0-9]+\n$")
                self.assertEqual(result.stderr.count("Preview Chrome probe retrying:"), attempts - 1, "real empty timeouts can retry only once")
                self.assertEqual(self.calls(root), [["google-chrome", "--version"]] * attempts, "real timeout recovery must honor the maximum attempt count")
                self.assertEqual(list((root / "runner").iterdir()), [], "real signal termination must remove private probe captures")

    def test_actual_workflow_blocks_tests_after_probe_failure(self):
        success = {"status": 0, "stdout": VERSION + "\n", "stderr": ""}
        timeout = {"status": 124, "stdout": "", "stderr": ""}
        cases = [
            ("pr_success", "pull_request", [success], 0, 1),
            ("push_success", "push", [success], 0, 1),
            ("pr_recovers", "pull_request", [timeout, success], 0, 2),
            ("push_recovers", "push", [timeout, success], 0, 2),
            ("pr_exhausted", "pull_request", [timeout, timeout, success], 124, 2),
            ("push_exhausted", "push", [timeout, timeout, success], 124, 2),
            ("pr_timeout_with_output", "pull_request", [{"status": 124, "stdout": "", "stderr": PRIVATE_OUTPUT}], 124, 1),
            ("push_other_failure", "push", [{"status": 23, "stdout": "", "stderr": PRIVATE_OUTPUT}], 23, 1),
        ]
        for name, event, responses, expected, attempts in cases:
            with self.subTest(name=name), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                env = self.environment(root, GITHUB_EVENT_NAME=event, CHROME_RESPONSES=json.dumps(responses))
                scripts = root / ".github" / "scripts"
                scripts.mkdir(parents=True)
                shutil.copyfile(HELPER, scripts / HELPER.name)
                result = subprocess.run(["bash", "-eo", "pipefail", "-c", browser_script()], cwd=root, env=env, capture_output=True, text=True, timeout=10, check=False)
                self.assertEqual(result.returncode, expected, result.stderr)
                self.check_redacted(result)
                self.assertEqual([call for call in self.calls(root) if call[0] == "google-chrome"], [["google-chrome", "--version"]] * attempts, "the workflow must honor probe retry bounds")
                go = [call for call in self.calls(root) if call[0] == "go"]
                if expected:
                    self.assertEqual(go, [], "a failed browser identity probe must prevent every browser test")
                    self.assertEqual(result.stdout, "")
                else:
                    mode = "count" if event == "pull_request" else "atomic"
                    expected_go = ["go", "test", "./internal/services/preview", "-run", "^TestBrowserChromeDP", "-parallel=1", "-count=1", "-v", "-coverpkg=./internal/...", "-coverprofile=coverage-browser.out", f"-covermode={mode}"]
                    if event != "pull_request":
                        expected_go.append("-race")
                    expected_go.append("-timeout=180s")
                    self.assertEqual(go, [expected_go], "probe diagnostics must not change browser test selection, race mode or coverage")
                    self.assertEqual(result.stdout, f"Preview test browser: {VERSION}\n")
                self.assertEqual(list((root / "runner").iterdir()), [], "both probe captures and browser aliases must be cleaned")


if __name__ == "__main__":
    unittest.main()
