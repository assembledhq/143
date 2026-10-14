#!/usr/bin/env python3
"""Safety contracts for privileged ephemeral acceptance runtime preparation."""

import json
import os
from pathlib import Path
import subprocess
import tempfile
import unittest


PREPARE = Path(__file__).with_name("prepare-review-acceptance-runtime.sh")
CLEANUP = Path(__file__).with_name("cleanup-review-acceptance-runtime.sh")


class ReviewAcceptanceRuntimeTests(unittest.TestCase):
    def fixture(self, directory):
        root = Path(directory).resolve()
        (root / "environment").touch()
        binary = root / "bin"
        binary.mkdir()
        (binary / "uname").write_text("#!/bin/sh\ncase $1 in -s) echo Linux;; -m) echo x86_64;; *) exit 1;; esac\n")
        (binary / "uname").chmod(0o755)
        # A validation failure must never reach a privileged command.
        (binary / "sudo").write_text(f"#!/bin/sh\ntouch '{root}/privileged-command'\nexit 99\n")
        (binary / "sudo").chmod(0o755)
        return root, {
            **os.environ, "GITHUB_ACTIONS": "true", "RUNNER_ENVIRONMENT": "github-hosted",
            "RUNNER_OS": "Linux", "GITHUB_RUN_ID": "123", "GITHUB_RUN_ATTEMPT": "2",
            "RUNNER_TEMP": str(root), "GITHUB_ENV": str(root / "environment"),
            "REVIEW_ACCEPTANCE_RUNTIME_STATE": str(root / "runtime"),
            "REVIEW_ACCEPTANCE_ARTIFACT_DIR": str(root / "artifacts"),
            "REVIEW_ACCEPTANCE_NETWORK": "review-acceptance-123-2",
            "PATH": f"{binary}:{os.environ['PATH']}",
        }

    def invoke(self, script, env, *args):
        return subprocess.run(["bash", str(script), *args], env=env, capture_output=True,
                              text=True, check=False, timeout=10)

    def test_host_and_identity_guards_precede_every_privileged_action(self):
        cases = [
            ("local", {"GITHUB_ACTIONS": "false"}),
            ("self hosted", {"RUNNER_ENVIRONMENT": "self-hosted"}),
            ("other OS", {"RUNNER_OS": "macOS"}),
            ("missing run", {"GITHUB_RUN_ID": ""}),
            ("non numeric attempt", {"GITHUB_RUN_ATTEMPT": "1;touch unsafe"}),
        ]
        for name, overrides in cases:
            for script in (PREPARE, CLEANUP):
                with self.subTest(name=name, script=script.name), tempfile.TemporaryDirectory() as directory:
                    root, env = self.fixture(directory)
                    result = self.invoke(script, {**env, **overrides})
                    self.assertNotEqual(result.returncode, 0, result.stdout)
                    self.assertFalse((root / "runtime").exists(), "unsafe host must not initialize state")
                    self.assertFalse((root / "privileged-command").exists(), "unsafe host must never invoke sudo")

    def test_paths_reject_escape_aliases_overlap_and_environment_injection(self):
        cases = [
            ("outside state", "REVIEW_ACCEPTANCE_RUNTIME_STATE", "/var/lib/docker"),
            ("relative state", "REVIEW_ACCEPTANCE_RUNTIME_STATE", "runtime"),
            ("parent alias", "REVIEW_ACCEPTANCE_RUNTIME_STATE", "{root}/child/../runtime"),
            ("newline", "REVIEW_ACCEPTANCE_RUNTIME_STATE", "{root}/runtime\nDOCKER_HOST=unsafe"),
            ("overlap", "REVIEW_ACCEPTANCE_ARTIFACT_DIR", "{root}/runtime/artifacts"),
            ("environment escape", "GITHUB_ENV", "/tmp/unowned-review-environment"),
            ("network injection", "REVIEW_ACCEPTANCE_NETWORK", "other-network"),
        ]
        for name, key, value in cases:
            with self.subTest(name=name), tempfile.TemporaryDirectory() as directory:
                root, env = self.fixture(directory)
                env[key] = value.format(root=root)
                result = self.invoke(PREPARE, env, "--validate")
                self.assertNotEqual(result.returncode, 0, result.stdout)
                self.assertFalse((root / "privileged-command").exists(), "path failure must precede sudo")

    def test_validation_is_side_effect_free_and_symlink_state_is_rejected(self):
        with tempfile.TemporaryDirectory() as directory:
            root, env = self.fixture(directory)
            for script in (PREPARE, CLEANUP):
                result = self.invoke(script, env, "--validate")
                self.assertEqual(result.returncode, 0, result.stderr)
            self.assertFalse((root / "runtime").exists(), "validation must not allocate runtime state")
            (root / "target").mkdir()
            (root / "runtime").symlink_to(root / "target", target_is_directory=True)
            result = self.invoke(PREPARE, env, "--validate")
            self.assertNotEqual(result.returncode, 0, "symlink state must not redirect privileged resources")

    def test_physical_capacity_requires_backing_plus_external_reserve(self):
        minimum = 32 * 1024 * 1024 * 1024
        for value, accepted in ((minimum - 1, False), (minimum, True), (minimum + 1, True), ("bad", False)):
            with self.subTest(value=value):
                result = subprocess.run(["bash", "-c", 'source "$1"; require_capacity "$2"',
                                         "contract", str(PREPARE), str(value)], capture_output=True,
                                        text=True, check=False, timeout=10)
                self.assertEqual(result.returncode == 0, accepted, result.stderr)

    def test_postbuild_capacity_preserves_full_quota_and_external_reserve(self):
        gib = 1024 ** 3
        cases = [
            ("both exact thresholds", 12 * gib, 8 * gib, True),
            ("extra space", 13 * gib, 9 * gib, True),
            ("XFS one byte short", 12 * gib - 1, 8 * gib, False),
            ("external one byte short", 12 * gib, 8 * gib - 1, False),
            ("quota alone leaves no metadata headroom", 10 * gib, 8 * gib, False),
            ("malformed observation", "unknown", 8 * gib, False),
        ]
        for name, xfs_free, external_free, accepted in cases:
            with self.subTest(name=name):
                result = subprocess.run(["bash", "-c", 'source "$1"; require_postbuild_capacity "$2" "$3"',
                                         "contract", str(PREPARE), str(xfs_free), str(external_free)],
                                        capture_output=True, text=True, check=False, timeout=10)
                self.assertEqual(result.returncode == 0, accepted, result.stderr)

    def test_resolver_configuration_selects_direct_or_systemd_upstream_addresses(self):
        cases = [
            ("direct", "nameserver 192.0.2.53\nnameserver 2001:db8::53\n", None,
             "direct", ["192.0.2.53", "2001:db8::53"]),
            ("stub uses systemd upstream", "nameserver 127.0.0.53\n",
             "nameserver 198.51.100.53\n", "upstream", ["198.51.100.53"]),
            ("mixed direct filters unsafe and duplicate addresses",
             "nameserver 127.0.0.53\nnameserver 0.0.0.0\nnameserver invalid\n"
             "nameserver 192.0.2.53\nnameserver 192.0.2.53\n", None,
             "direct", ["192.0.2.53"]),
        ]
        for name, direct, upstream, selected, expected in cases:
            with self.subTest(name=name), tempfile.TemporaryDirectory() as directory:
                root, env = self.fixture(directory)
                (root / "runtime").mkdir()
                (root / "artifacts").mkdir()
                (root / "direct").write_text(direct)
                if upstream is not None:
                    (root / "upstream").write_text(upstream)
                result = subprocess.run(
                    ["bash", "-c", 'source "$1"; prepare_resolver "$2" "$3"',
                     "contract", str(PREPARE), str(root / "direct"), str(root / "upstream")],
                    env=env, capture_output=True, text=True, check=False, timeout=10)
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertEqual((root / "runtime/resolv.conf").read_text(),
                                 "".join("nameserver " + address + "\n" for address in expected)
                                 + "options timeout:2 attempts:3\n")
                self.assertEqual(json.loads((root / "artifacts/resolver-configuration.json").read_text()),
                                 {"source_path": str(root / selected), "configured_nameservers": expected,
                                  "reachability_verified": False},
                                 "configured resolver evidence must not claim a reachability test")
                self.assertFalse((root / "privileged-command").exists(),
                                 "resolver selection should require no privileged or network operation")

    def test_resolver_configuration_fails_without_configured_upstream_and_never_invents_fallback(self):
        cases = [
            ("missing direct", None, None, "direct"),
            ("missing systemd upstream", "nameserver 127.0.0.53\n", None, "upstream"),
            ("loopback-only systemd upstream", "nameserver 127.0.0.53\n",
             "nameserver 127.0.0.53\nnameserver ::1\n", "upstream"),
            ("empty direct", "# no configured resolver\n", None, "direct"),
            ("invalid or unspecified only", "nameserver invalid\nnameserver 0.0.0.0\n", None, "direct"),
        ]
        for name, direct, upstream, selected in cases:
            with self.subTest(name=name), tempfile.TemporaryDirectory() as directory:
                root, env = self.fixture(directory)
                (root / "runtime").mkdir()
                (root / "artifacts").mkdir()
                if direct is not None:
                    (root / "direct").write_text(direct)
                if upstream is not None:
                    (root / "upstream").write_text(upstream)
                result = subprocess.run(
                    ["bash", "-c", 'source "$1"; prepare_resolver "$2" "$3"',
                     "contract", str(PREPARE), str(root / "direct"), str(root / "upstream")],
                    env=env, capture_output=True, text=True, check=False, timeout=10)
                self.assertNotEqual(result.returncode, 0, result.stdout)
                self.assertIn("no usable configured nameserver", result.stderr)
                self.assertFalse((root / "runtime/resolv.conf").exists(),
                                 "missing upstream must not silently select a public fallback")
                self.assertEqual(json.loads((root / "artifacts/resolver-configuration.json").read_text()),
                                 {"source_path": str(root / selected), "configured_nameservers": [],
                                  "reachability_verified": False})
                self.assertFalse((root / "privileged-command").exists(),
                                 "resolver failure must not reach a privileged command")

    def test_owned_daemon_keeps_runsc_kernel_resource_enforcement_enabled(self):
        with tempfile.TemporaryDirectory() as directory:
            root, env = self.fixture(directory)
            (root / "runtime").mkdir()
            result = subprocess.run(
                ["bash", "-c", 'source "$1"; write_daemon_config', "contract", str(PREPARE)],
                env=env, capture_output=True, text=True, check=False, timeout=10)
            self.assertEqual(result.returncode, 0, result.stderr)
            config = json.loads((root / "runtime/daemon.json").read_text())
            self.assertEqual(config["exec-opts"], ["native.cgroupdriver=cgroupfs"],
                             "Docker and runsc must use the same explicit filesystem cgroup driver")
            self.assertEqual(config["runtimes"]["runsc"],
                             {"path": "/usr/bin/runsc", "runtimeArgs": ["--host-uds=open"]},
                             "runsc must preserve the credential socket transport while enforcing kernel cgroups")
            self.assertFalse((root / "privileged-command").exists(),
                             "daemon configuration construction should require no privileged operation")

    def test_daemon_preflight_requires_matching_cgroup_v2_driver(self):
        cases = [
            ("explicit cgroup v2 filesystem driver", "2", "cgroupfs", True),
            ("legacy cgroup v1", "1", "cgroupfs", False),
            ("missing version", None, "cgroupfs", False),
            ("conflicting systemd driver", "2", "systemd", False),
            ("missing driver", "2", None, False),
        ]
        for name, version, driver, accepted in cases:
            with self.subTest(name=name), tempfile.TemporaryDirectory() as directory:
                root, env = self.fixture(directory)
                (root / "runtime").mkdir()
                (root / "artifacts").mkdir()
                info = {"Driver": "overlay2", "Runtimes": {"runsc": {}},
                        "DriverStatus": [["Backing Filesystem", "xfs"], ["Supports d_type", "true"]],
                        "DockerRootDir": str(root / "runtime/mount/docker")}
                if version is not None:
                    info["CgroupVersion"] = version
                if driver is not None:
                    info["CgroupDriver"] = driver
                (root / "artifacts/docker-info.json").write_text(json.dumps(info))
                result = subprocess.run(
                    ["bash", "-c", 'source "$1"; verify_daemon_configuration', "contract", str(PREPARE)],
                    env=env, capture_output=True, text=True, check=False, timeout=10)
                self.assertEqual(result.returncode == 0, accepted, result.stderr)
                self.assertFalse((root / "privileged-command").exists(),
                                 "unsupported cgroup configuration must fail through a read-only preflight")

    def test_cleanup_and_probe_require_current_private_ownership_record(self):
        cases = [("foreign run", "run_id", "999"), ("foreign state", "state", "/var/lib/docker"),
                 ("foreign network", "network", "other-network"), ("foreign uid", "uid", -1)]
        for name, key, value in cases:
            for script, args in ((PREPARE, ("--probe-quota",)), (CLEANUP, ())):
                with self.subTest(name=name, script=script.name), tempfile.TemporaryDirectory() as directory:
                    root, env = self.fixture(directory)
                    state = root / "runtime"
                    state.mkdir(mode=0o700)
                    record = {"format": 1, "state": str(state), "run_id": "123", "attempt": "2",
                              "uid": os.getuid(), "network": env["REVIEW_ACCEPTANCE_NETWORK"]}
                    record[key] = value
                    marker = state / "owner.json"
                    marker.write_text(json.dumps(record))
                    marker.chmod(0o600)
                    result = self.invoke(script, env, *args)
                    self.assertNotEqual(result.returncode, 0, result.stdout)
                    self.assertFalse((root / "privileged-command").exists(), "foreign state must not reach sudo")


    def test_cleanup_preserves_backing_when_loop_observation_is_uncertain(self):
        cases = [("failed query", 1, "", True, 1),
                 ("still attached", 0, "/dev/loop7: owned backing", True, 1),
                 ("positively detached", 0, "", False, 0)]
        for name, status, output, preserved, expected_status in cases:
            with self.subTest(name=name), tempfile.TemporaryDirectory() as directory:
                root, env = self.fixture(directory)
                state = root / "runtime"
                state.mkdir(mode=0o700)
                record = {"format": 1, "state": str(state), "run_id": "123", "attempt": "2",
                          "uid": os.getuid(), "network": env["REVIEW_ACCEPTANCE_NETWORK"]}
                marker = state / "owner.json"
                marker.write_text(json.dumps(record))
                marker.chmod(0o600)
                backing = state / "backing.xfs"
                backing.write_text("owned allocation must survive uncertainty")
                binary = root / "bin"
                (binary / "sudo").write_text("#!/bin/sh\n[ \"$1 $2 $3\" = \"-n losetup -j\" ] || exit 99\nprintf '%s' \"$LOOP_QUERY_OUTPUT\"\nexit \"$LOOP_QUERY_STATUS\"\n")
                (binary / "df").write_text("#!/bin/sh\necho capacity-fixture\n")
                (binary / "mountpoint").write_text("#!/bin/sh\nexit 1\n")
                for command in ("sudo", "df", "mountpoint"):
                    (binary / command).chmod(0o755)
                result = self.invoke(CLEANUP, {**env, "LOOP_QUERY_STATUS": str(status),
                                              "LOOP_QUERY_OUTPUT": output})
                self.assertEqual(result.returncode, expected_status, result.stderr + result.stdout)
                self.assertEqual(backing.exists(), preserved,
                                 "backing deletion requires successful proof of no mount and no loop association")
                if status:
                    self.assertIn("cannot establish loop detach state; preserving XFS backing", result.stdout)


    def test_cleanup_lists_json_and_detaches_only_the_owned_loop(self):
        cases = [("owned backing", "owned", True), ("foreign backing", "foreign", False),
                 ("reused loop", "reused", False), ("empty observation", "empty", False)]
        for name, observation, detach in cases:
            with self.subTest(name=name), tempfile.TemporaryDirectory() as directory:
                root, env = self.fixture(directory)
                state = root / "runtime"
                state.mkdir(mode=0o700)
                record = {"format": 1, "state": str(state), "run_id": "123", "attempt": "2",
                          "uid": os.getuid(), "network": env["REVIEW_ACCEPTANCE_NETWORK"]}
                marker = state / "owner.json"
                marker.write_text(json.dumps(record))
                marker.chmod(0o600)
                backing = state / "backing.xfs"
                backing.write_text("owned allocation")
                (state / "loop-device").write_text("/dev/loop7")
                records = {"loopdevices": [{"name": "/dev/loop8" if observation == "reused" else "/dev/loop7",
                                            "back-file": "/unowned/backing" if observation == "foreign" else str(backing)}]}
                binary = root / "bin"
                (binary / "sudo").write_text('''#!/bin/sh
case "$*" in
  "-n losetup --list --json --output NAME,BACK-FILE /dev/loop7") printf '%s' "$LOOP_JSON" ;;
  "-n losetup -d /dev/loop7") touch "$DETACHED_MARKER" ;;
  "-n losetup -j "*) if [ ! -f "$DETACHED_MARKER" ]; then echo '/dev/loop7: attached'; fi ;;
  *) exit 99 ;;
esac
''')
                (binary / "df").write_text("#!/bin/sh\necho capacity-fixture\n")
                (binary / "mountpoint").write_text("#!/bin/sh\nexit 1\n")
                for command in ("sudo", "df", "mountpoint"):
                    (binary / command).chmod(0o755)
                detached = root / "detached"
                result = self.invoke(CLEANUP, {**env, "DETACHED_MARKER": str(detached),
                                              "LOOP_JSON": "" if observation == "empty" else json.dumps(records)})
                self.assertEqual(result.returncode, 0 if detach else 1, result.stderr + result.stdout)
                self.assertEqual(detached.exists(), detach, "only positively matched loop ownership permits detach")
                self.assertEqual(backing.exists(), not detach, "uncertain or foreign loop ownership preserves backing")


if __name__ == "__main__":
    unittest.main()
