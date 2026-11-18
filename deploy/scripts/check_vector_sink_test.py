#!/usr/bin/env python3
"""Exercise sink probes without Docker, network access, or deployment secrets."""
import os
from pathlib import Path
import subprocess
import shutil
import time
import tempfile
import unittest

SCRIPT = Path(__file__).with_name("check-vector-sink.sh")


class VectorSinkTest(unittest.TestCase):
    def run_probe(self, mode="reachable", host="effective.example", compose=False, real_timeout=None, interrupt=False, cleanup_mode="success"):
        with tempfile.TemporaryDirectory() as directory:
            root = Path(directory)
            stubs = {
                "timeout": '''#!/usr/bin/env python3
import os, signal, subprocess, sys
assert sys.argv[1] == '--kill-after=2s'
assert sys.argv[2] in ('10s', '5s')
if os.environ.get('REAL_TIMEOUT'):
    os.execv(os.environ['REAL_TIMEOUT'], ['timeout', '--kill-after=0.1s', '0.2s', *sys.argv[3:]])
process = subprocess.Popen(sys.argv[3:], start_new_session=True)
try:
    sys.exit(process.wait(timeout=.8))
except subprocess.TimeoutExpired:
    os.killpg(process.pid, signal.SIGKILL)
    process.wait()
    sys.exit(124)
''',
                "docker": '''#!/usr/bin/env bash
set -euo pipefail
if [ "$1" = rm ]; then
  [ "$2" = -f ]
  printf '%s' "$3" > "$CLEANUP_CAPTURE"
  if [ "$CLEANUP_MODE" = timeout ]; then sleep 3; fi
  exit 0
elif [ "$1" = exec ]; then
  [ "$2" = collector ]
  shift 2
  export VICTORIALOGS_HOST="$ACTUAL_HOST"
else
  [ "$*" != "" ]
  for word in compose -f candidate.yml run --name; do [ "$1" = "$word" ]; shift; done
  printf '%s' "$1" > "$NAME_CAPTURE"
  shift
  for word in -T --no-deps --entrypoint sh vector; do [ "$1" = "$word" ]; shift; done
  export VICTORIALOGS_HOST="$CANDIDATE_HOST"
  set -- sh "$@"
fi
exec "$@"
''',
                "wget": '''#!/usr/bin/env bash
set -euo pipefail
[ "$1" = -q ] && [ "$2" = -T ] && [ "$3" = 5 ]
[ "$4" = -O ] && [ "$5" = /dev/null ]
printf '%s' "$6" > "$URL_CAPTURE"
case "$PROBE_MODE" in
  reachable) exit 0 ;;
  unreachable) echo "private URL must stay hidden" >&2; exit 1 ;;
  timeout) sleep 3 ;;
  resistant) exec python3 -c 'import signal,time; signal.signal(signal.SIGTERM, signal.SIG_IGN); time.sleep(5)' ;;
esac
''',
            }
            for name, content in stubs.items():
                path = root / name
                path.write_text(content)
                path.chmod(0o755)
            capture = root / "url"
            env = dict(os.environ, PATH=f"{root}:{os.environ['PATH']}",
                       ACTUAL_HOST=host, CANDIDATE_HOST="repaired.example",
                       VICTORIALOGS_HOST="outer-incorrect.example", PROBE_MODE=mode,
                       URL_CAPTURE=str(capture), CLEANUP_CAPTURE=str(root / "cleanup"),
                       NAME_CAPTURE=str(root / "name"), REAL_TIMEOUT=real_timeout or "",
                       CLEANUP_MODE=cleanup_mode)
            args = ["--compose", "candidate.yml"] if compose else ["collector"]
            process = subprocess.Popen(["bash", str(SCRIPT), *args], env=env,
                                       stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True)
            if interrupt:
                deadline = time.monotonic() + 2
                while not capture.exists() and time.monotonic() < deadline:
                    time.sleep(.01)
                process.terminate()
            stdout, stderr = process.communicate(timeout=5)
            result = subprocess.CompletedProcess(process.args, process.returncode, stdout, stderr)
            result.cleanup = (root / "cleanup").read_text() if (root / "cleanup").exists() else None
            result.name = (root / "name").read_text() if (root / "name").exists() else None
            return result, capture.read_text() if capture.exists() else None

    def test_reachability_and_effective_environment(self):
        for mode, expected_code in [("reachable", 0), ("unreachable", 1), ("timeout", 1)]:
            with self.subTest(mode=mode):
                result, url = self.run_probe(mode)
                self.assertEqual(result.returncode, expected_code, result.stderr)
                self.assertEqual(url, "http://effective.example:9428/health")
                self.assertNotIn("private URL", result.stderr)
                if expected_code:
                    self.assertIn("local collector health is insufficient", result.stderr)

    def test_missing_container_config_fails(self):
        result, url = self.run_probe(host="")
        self.assertEqual(result.returncode, 1)
        self.assertIsNone(url)
        self.assertIn("no configured VICTORIALOGS_HOST", result.stderr)

    def test_prospective_repair_does_not_require_old_endpoint(self):
        result, url = self.run_probe(host="broken-old.example", compose=True)
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(url, "http://repaired.example:9428/health")
        self.assertEqual(result.cleanup, result.name)
        self.assertTrue(result.name.startswith("143-vector-sink-probe-"))

    def test_candidate_cleanup_after_failure_timeout_and_interruption(self):
        for mode, interrupt in [("unreachable", False), ("timeout", False), ("timeout", True)]:
            with self.subTest(mode=mode, interrupt=interrupt):
                result, _ = self.run_probe(mode=mode, compose=True, interrupt=interrupt)
                self.assertNotEqual(result.returncode, 0)
                self.assertEqual(result.cleanup, result.name)
                self.assertIsNotNone(result.name)

    def test_cleanup_deadline_prevents_reporting_success(self):
        result, _ = self.run_probe(compose=True, cleanup_mode="timeout")
        self.assertEqual(result.returncode, 1)
        self.assertIn("cleanup failed or timed out", result.stderr)
        self.assertEqual(result.cleanup, result.name)

    @unittest.skipUnless(shutil.which("timeout") or shutil.which("gtimeout"), "GNU timeout unavailable")
    def test_real_timeout_kills_term_resistant_probe_and_cleans_container(self):
        started = time.monotonic()
        result, _ = self.run_probe(mode="resistant", compose=True,
                                   real_timeout=shutil.which("timeout") or shutil.which("gtimeout"))
        self.assertNotEqual(result.returncode, 0)
        self.assertLess(time.monotonic() - started, 2)
        self.assertEqual(result.cleanup, result.name)

    def test_missing_container_fails(self):
        result = subprocess.run(["bash", str(SCRIPT)], capture_output=True, text=True)
        self.assertEqual(result.returncode, 1)
        self.assertIn("container not found", result.stderr)


if __name__ == "__main__":
    unittest.main()
