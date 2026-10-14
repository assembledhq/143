#!/usr/bin/env python3
"""Required Linux/Docker regression for child reaping under PostgreSQL PID 1.

Run explicitly with python3 -B deploy/scripts/recovery_child_watchdog_test.py.
It never skips: missing Linux, a local Docker daemon, or postgres:18 is a failure.
All databases, containers, SQL, and process signals are disposable fixtures.
"""

import atexit
import hashlib
import json
import os
from pathlib import Path
import shutil
import signal
import subprocess
import sys
import tempfile
import time
import unittest
import uuid


HELPER = Path(__file__).with_name("recovery-child-watchdog.sh")
CONTAINERS = []


def command(*args, timeout=10, input=None, check=True):
    return subprocess.run(args, input=input, text=True, capture_output=True,
                          timeout=timeout, check=check)


def cleanup():
    errors = []
    for container in reversed(CONTAINERS):
        try:
            result = command("docker", "rm", "--force", "--volumes", container,
                             timeout=15, check=False)
            if result.returncode:
                errors.append(result.stderr.strip() or "docker rm failed")
        except (OSError, subprocess.SubprocessError) as error:
            errors.append(str(error))
    return errors


def exit_cleanup():
    for error in cleanup():
        print(f"Fixture cleanup failed: {error}", file=sys.stderr)


atexit.register(exit_cleanup)


def wait_for(predicate, description, seconds=10):
    deadline = time.monotonic() + seconds
    while time.monotonic() < deadline:
        value = predicate()
        if value:
            return value
        time.sleep(0.05)
    raise AssertionError(f"Timed out waiting for {description}")


class Database:
    def __init__(self):
        self.container = command(
            "docker", "run", "--detach", "--network=none", "--cpus=1",
            "--memory=512m", "--pids-limit=128", "--shm-size=64m",
            "--label=143.synthetic-watchdog-test=true",
            "--env=POSTGRES_HOST_AUTH_METHOD=trust", "postgres:18",
        ).stdout.strip()
        CONTAINERS.append(self.container)
        # initdb temporarily starts a server while the entrypoint is still
        # PID 1. Require the final exec'ed postmaster, not that temporary server.
        wait_for(lambda: self.exec("cat", "/proc/1/comm", check=False).stdout.strip() == "postgres" and
                 self.exec("pg_isready", "-U", "postgres", check=False).returncode == 0,
                 "disposable PostgreSQL readiness", seconds=45)
        assert self.exec("cat", "/proc/1/comm").stdout.strip() == "postgres", "PostgreSQL must be PID 1, without an init/subreaper"
        assert self.sql("SHOW server_version_num").startswith("18"), "Fixture must run PostgreSQL 18"
        self.start_time = self.sql("SELECT pg_postmaster_start_time()")
        self.exec("mkdir", "-p", "/tmp/watchdog-fixture")
        command("docker", "cp", str(HELPER), self.container + ":/tmp/watchdog-fixture/helper.sh")
        actual = self.exec("sha256sum", "/tmp/watchdog-fixture/helper.sh").stdout.split()[0]
        assert actual == hashlib.sha256(HELPER.read_bytes()).hexdigest(), "Container must execute the exact shared helper bytes"
        self.persistent_name = "watchdog-continuity-" + uuid.uuid4().hex
        self.persistent_token = "continuity-" + uuid.uuid4().hex
        self.persistent = subprocess.Popen(
            ["docker", "exec", "-i", "--env", "PGAPPNAME=" + self.persistent_name,
             self.container, "psql", "-w", "-X", "-qAt", "-U", "postgres", "-d", "postgres"],
            stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True,
        )
        self.persistent.stdin.write(
            "CREATE TEMP TABLE continuity(value text);\n"
            f"INSERT INTO continuity VALUES ('{self.persistent_token}');\n"
            "SELECT pg_sleep(180);\nSELECT value FROM continuity;\n"
        )
        self.persistent.stdin.close()
        self.persistent.stdin = None
        self.persistent_pid = int(wait_for(
            lambda: self.sql("SELECT pid FROM pg_stat_activity WHERE application_name = "
                             f"'{self.persistent_name}' AND wait_event = 'PgSleep'"),
            "persistent backend sleeping with session-local state",
        ))

    def exec(self, *args, check=True):
        return command("docker", "exec", self.container, *args, check=check)

    def sql(self, sql):
        return self.exec("psql", "-w", "-X", "-qAt", "-U", "postgres", "-d", "postgres", "-c", sql).stdout.strip()

    def process(self, pid):
        result = self.exec("cat", f"/proc/{pid}/stat", check=False)
        if result.returncode:
            return None
        return self.parse_process(result.stdout)

    @staticmethod
    def parse_process(stat):
        left, right = stat.rsplit(") ", 1)
        pid, name = left.split(" (", 1)
        fields = right.split()
        return {"pid": int(pid), "name": name, "state": fields[0], "parent": int(fields[1])}

    def timer(self, caller):
        output = self.exec("/bin/sh", "-c", 'for f in /proc/[0-9]*/stat; do cat "$f" 2>/dev/null || :; done').stdout
        processes = [self.parse_process(line) for line in output.splitlines()]
        watchers = {p["pid"] for p in processes if p["parent"] == caller and p["name"] == "sh"}
        return next((p for p in processes if p["parent"] in watchers and p["name"] == "sleep"), None)

    def signal(self, kind, pid):
        self.exec("/bin/sh", "-c", 'kill -"$1" "$2"', "fixture-signal", kind, str(pid))

    def assert_healthy(self, test):
        test.assertIsNone(self.persistent.poll(), "Persistent backend connection must remain open")
        test.assertEqual(self.start_time, self.sql("SELECT pg_postmaster_start_time()"), "Postmaster must not restart")
        test.assertEqual(str(self.persistent_pid), self.sql(
            "SELECT pid FROM pg_stat_activity WHERE application_name = " + f"'{self.persistent_name}'"),
            "The same backend must retain the session")
        logs = command("docker", "logs", self.container).stderr
        test.assertNotRegex(logs, r"(?:untracked|unrecognized) child process", "No orphan may be reaped by the postmaster")
        test.assertNotIn("terminating any other active server processes", logs, "No crash recovery may interrupt unrelated backends")

    def finish_persistent(self, test):
        self.sql(f"SELECT pg_cancel_backend({self.persistent_pid})")
        stdout, stderr = self.persistent.communicate(timeout=10)
        test.assertEqual(0, self.persistent.returncode, "Persistent psql must finish normally after the explicit cancellation")
        test.assertIn(self.persistent_token, stdout, "Session-local state must survive every watchdog path")
        test.assertIn("canceling statement due to user request", stderr, "Only the fixture's explicit final cancellation is expected")

    def script(self, name, content):
        with tempfile.TemporaryDirectory(prefix="watchdog-synthetic-") as directory:
            source = Path(directory) / name
            source.write_text(content)
            command("docker", "cp", str(source), self.container + ":/tmp/watchdog-fixture/" + name)

    def start_safe(self, sql, seconds, strict_shell=False):
        nonce = uuid.uuid4().hex
        prefix = "/tmp/watchdog-fixture/" + nonce
        self.script("caller.sh", '''#!/bin/sh
. /tmp/watchdog-fixture/helper.sh
prefix=$1
seconds=$2
printf '%s\n' "$$" > "$prefix.caller"
fixture_signal() {
    [ "$(cat /proc/$2/comm 2>/dev/null)" = psql ] || return 0
    kill -"$1" "$2" 2>/dev/null || :
}
fixture_started() { printf '%s\n' "$1" > "$prefix.child"; }
recovery_watchdog_run "$seconds" fixture_signal fixture_started psql -w -X -qAt -U postgres -d postgres -v ON_ERROR_STOP=1
status=$?
printf '%s\n' "$status" > "$prefix.status"
exit "$status"
''')
        process = subprocess.Popen(
            ["docker", "exec", "-i", self.container, "/bin/sh"] + (["-eu"] if strict_shell else []) +
            ["/tmp/watchdog-fixture/caller.sh", prefix, str(seconds)],
            stdin=subprocess.PIPE, stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True,
        )
        process.stdin.write(sql)
        process.stdin.close()
        process.stdin = None
        caller = int(wait_for(lambda: self.exec("cat", prefix + ".caller", check=False).stdout.strip(), "caller PID"))
        child = int(wait_for(lambda: self.exec("cat", prefix + ".child", check=False).stdout.strip(), "owned psql PID"))
        return process, caller, child


class WatchdogLinuxTest(unittest.TestCase):
    @classmethod
    def setUpClass(cls):
        if sys.platform != "linux":
            raise RuntimeError("This required integration test needs Linux; it does not skip")
        if not shutil.which("docker"):
            raise RuntimeError("This required integration test needs Docker; it does not skip")
        endpoint = os.environ.get("DOCKER_HOST") or command(
            "docker", "context", "inspect", "--format", "{{.Endpoints.docker.Host}}").stdout.strip()
        if not endpoint.startswith("unix://"):
            raise RuntimeError("Only a local Unix-socket Docker daemon is permitted")
        if command("docker", "info", "--format", "{{.OSType}}").stdout.strip() != "linux":
            raise RuntimeError("The Docker daemon must execute Linux containers")
        command("docker", "image", "inspect", "postgres:18")
        if not HELPER.is_file():
            raise RuntimeError("The canonical shared watchdog helper is required")

    def test_safe_exit_paths_preserve_postmaster_and_backend(self):
        database = Database()
        database.sql("CREATE TABLE fixture_commits(path text)")
        cases = [
            ("success", "BEGIN; INSERT INTO fixture_commits VALUES ('success'); COMMIT; SELECT 'committed';\n", 20, 0, "committed"),
            ("certified_no_commit", "BEGIN; INSERT INTO fixture_commits VALUES ('no_commit'); ROLLBACK; SELECT 'no_commit';\n", 20, 0, "no_commit"),
            ("timeout", "BEGIN; INSERT INTO fixture_commits VALUES ('timeout'); SELECT pg_sleep(60); COMMIT;\n", 1, None, None),
            ("sigterm", "BEGIN; INSERT INTO fixture_commits VALUES ('sigterm'); SELECT pg_sleep(60); COMMIT;\n", 20, None, None),
            ("stopped_timer_sigterm", "BEGIN; INSERT INTO fixture_commits VALUES ('stopped'); SELECT pg_sleep(60); COMMIT;\n", 20, None, None),
        ]
        shell_cases = [(strict, case) for strict in (False, True) for case in cases]
        for strict_shell, (name, sql, seconds, expected_status, receipt) in shell_cases:
            with self.subTest(path=name, strict_shell=strict_shell):
                # Use distinct committed rows so both shell modes exercise the
                # same SQL semantics without depending on a previous case.
                database.sql("TRUNCATE fixture_commits")
                if name != "success":
                    database.sql("INSERT INTO fixture_commits VALUES ('success')")
                process, caller, child = database.start_safe(sql, seconds, strict_shell)
                timer = None
                if name in {"sigterm", "stopped_timer_sigterm"}:
                    timer = wait_for(lambda: database.timer(caller), "watchdog timer")
                    if name == "stopped_timer_sigterm":
                        database.signal("STOP", timer["pid"])
                        wait_for(lambda: (database.process(timer["pid"]) or {}).get("state") == "T", "stopped timer")
                    database.signal("TERM", caller)
                    if name == "stopped_timer_sigterm":
                        time.sleep(0.25)
                        owned = database.process(timer["pid"])
                        self.assertIsNotNone(owned, "Stopped timer must remain owned until it can be reaped")
                        self.assertEqual(timer["parent"], owned["parent"], "Cleanup must keep the original watcher alive")
                        self.assertNotEqual(1, owned["parent"], "The safe timer must never become a postmaster child")
                        self.assertIsNone(process.poll(), "Caller must wait for watchdog cleanup")
                        database.signal("CONT", timer["pid"])
                stdout, stderr = process.communicate(timeout=10)
                if expected_status is None:
                    self.assertNotEqual(0, process.returncode, "Interrupted/deadline SQL must fail rather than certify success")
                else:
                    self.assertEqual(expected_status, process.returncode, stderr)
                    self.assertIn(receipt, stdout, "SQL result must preserve the exact success/no-commit receipt")
                self.assertIsNone(database.process(child), "Owned psql child must have been reaped")
                if timer:
                    self.assertIsNone(database.process(timer["pid"]), "Owned timer must have been reaped")
                    self.assertIsNone(database.process(timer["parent"]), "Owned watchdog must have been reaped")
                self.assertEqual("success", database.sql("SELECT path FROM fixture_commits ORDER BY path"), "Only the committed transaction may remain")
                database.assert_healthy(self)
        database.finish_persistent(self)

    def test_unsafe_no_wait_control_reproduces_postmaster_crash(self):
        # The stopped timer makes the original kill-without-wait race
        # deterministic. It is used only in this isolated disposable fixture.
        database = Database()
        database.script("unsafe.sh", '''#!/bin/sh
timer=
trap '[ -z "$timer" ] || kill "$timer" 2>/dev/null; exit 0' INT TERM HUP
sleep 120 </dev/null >/dev/null 2>&1 & timer=$!
printf '%s %s\n' "$$" "$timer" > /tmp/watchdog-fixture/unsafe.pids
wait "$timer"
''')
        process = subprocess.Popen(
            ["docker", "exec", database.container, "/bin/sh", "/tmp/watchdog-fixture/unsafe.sh"],
            stdout=subprocess.PIPE, stderr=subprocess.PIPE, text=True,
        )
        pids = wait_for(lambda: database.exec("cat", "/tmp/watchdog-fixture/unsafe.pids", check=False).stdout.strip(), "unsafe control PIDs")
        watcher, timer = map(int, pids.split())
        database.signal("STOP", timer)
        wait_for(lambda: (database.process(timer) or {}).get("state") == "T", "stopped unsafe timer")
        database.signal("TERM", watcher)
        process.communicate(timeout=10)
        self.assertEqual(0, process.returncode, "Unsafe trap exits before waiting for its timer")
        orphan = wait_for(lambda: (p if (p := database.process(timer)) and p["parent"] == 1 else None), "timer orphan adopted by PostgreSQL PID 1")
        self.assertEqual("sleep", orphan["name"], "Control must orphan the owned timer")
        database.assert_healthy(self)
        database.signal("CONT", timer)
        wait_for(lambda: database.persistent.poll() is not None, "PostgreSQL terminating the unrelated persistent backend")
        stdout, stderr = database.persistent.communicate(timeout=10)
        self.assertNotEqual(0, database.persistent.returncode, "Unsafe child exit must break the existing backend")
        self.assertNotIn(database.persistent_token, stdout, "The interrupted session cannot complete its continuity query")
        self.assertTrue(stderr, "Lost PostgreSQL connection must provide an error")
        logs = command("docker", "logs", database.container).stderr
        self.assertRegex(logs, rf"(?:untracked|unrecognized) child process \(PID {timer}\)", "The exact orphan exit must cause PostgreSQL's crash handling")
        self.assertIn("terminating any other active server processes", logs, "Negative fixture must exercise postmaster crash recovery")


if __name__ == "__main__":
    def deadline(_signum, _frame):
        raise TimeoutError("Watchdog regression exceeded its four-minute hard deadline")

    signal.signal(signal.SIGTERM, deadline)
    if hasattr(signal, "SIGALRM"):
        signal.signal(signal.SIGALRM, deadline)
        signal.alarm(240)
    print(json.dumps({"helper_sha256": hashlib.sha256(HELPER.read_bytes()).hexdigest(), "image": "postgres:18", "required_linux_docker": True}), flush=True)
    try:
        unittest.main(verbosity=2)
    finally:
        if hasattr(signal, "SIGALRM"):
            signal.alarm(0)
        errors = cleanup()
        CONTAINERS.clear()
        if errors:
            raise RuntimeError("Disposable fixture cleanup failed: " + "; ".join(errors))
