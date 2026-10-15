#!/usr/bin/env python3
"""Safety contracts for privileged ephemeral acceptance runtime preparation."""

import contextlib
import io
import json
import resource
import os
from pathlib import Path
import subprocess
import tempfile
import unittest
from unittest import mock


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
            "REVIEW_ACCEPTANCE_ROOT_OVERLAY": "", "REVIEW_REAL_REPOSITORY_ACCEPTANCE": "",
            "REVIEW_ACCEPTANCE_RUNTIME": "",
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

    def test_root_overlay_selector_rejects_unknown_values_before_creating_resources_or_config(self):
        for selector in ("self", "root:self", "NONE", "none ", "none\n", "--overlay2=none", "none;touch unsafe"):
            for operation in ("--validate", "", "write_daemon_config"):
                with self.subTest(selector=selector, operation=operation), tempfile.TemporaryDirectory() as directory:
                    root, env = self.fixture(directory)
                    env["REVIEW_ACCEPTANCE_ROOT_OVERLAY"] = selector
                    if operation == "write_daemon_config":
                        (root / "runtime").mkdir()
                        result = subprocess.run(
                            ["bash", "-c", 'source "$1"; write_daemon_config', "contract", str(PREPARE)],
                            env=env, capture_output=True, text=True, check=False, timeout=10)
                        self.assertFalse((root / "runtime/daemon.json").exists(),
                                         "an unknown selector must never write daemon configuration")
                    else:
                        result = self.invoke(PREPARE, env, *([operation] if operation else []))
                        self.assertFalse((root / "runtime").exists(),
                                         "an unknown selector must never allocate runtime state")
                    self.assertNotEqual(result.returncode, 0, result.stdout)
                    self.assertIn("unsupported root overlay selector", result.stderr)
                    self.assertFalse((root / "privileged-command").exists(),
                                     "an unknown selector must fail before any privileged action")

    def test_root_overlay_none_requires_owned_real_repository_runsc_scope_independent_of_profile(self):
        cases = [
            ("normal", {"REVIEW_ACCEPTANCE_PROFILE_SOURCE_HELPER": ""}, True),
            ("explicit normal", {"REVIEW_ACCEPTANCE_PROFILE_SOURCE_HELPER": "0"}, True),
            ("profile", {"REVIEW_ACCEPTANCE_PROFILE_SOURCE_HELPER": "1"}, True),
            ("missing real repository opt-in", {"REVIEW_REAL_REPOSITORY_ACCEPTANCE": ""}, False),
            ("other opt-in", {"REVIEW_REAL_REPOSITORY_ACCEPTANCE": "true"}, False),
            ("missing runtime", {"REVIEW_ACCEPTANCE_RUNTIME": ""}, False),
            ("other runtime", {"REVIEW_ACCEPTANCE_RUNTIME": "runc"}, False),
            ("local", {"GITHUB_ACTIONS": "false"}, False),
            ("self hosted", {"RUNNER_ENVIRONMENT": "self-hosted"}, False),
            ("other OS", {"RUNNER_OS": "macOS"}, False),
            ("missing run", {"GITHUB_RUN_ID": ""}, False),
            ("unsafe paths", {"REVIEW_ACCEPTANCE_RUNTIME_STATE": "/var/lib/docker"}, False),
        ]
        for name, overrides, accepted in cases:
            with self.subTest(name=name), tempfile.TemporaryDirectory() as directory:
                root, env = self.fixture(directory)
                env.update({"REVIEW_ACCEPTANCE_ROOT_OVERLAY": "none",
                            "REVIEW_REAL_REPOSITORY_ACCEPTANCE": "1", "REVIEW_ACCEPTANCE_RUNTIME": "runsc",
                            **overrides})
                result = self.invoke(PREPARE, env, "--validate")
                self.assertEqual(result.returncode == 0, accepted, result.stderr)
                self.assertFalse((root / "runtime").exists(), "selector validation must not allocate state")
                self.assertFalse((root / "privileged-command").exists(),
                                 "selector validation must not invoke privileged commands")
                # Also exercise the public configuration function so sourcing the script
                # cannot bypass the owned real-repository guard for the candidate.
                (root / "runtime").mkdir()
                result = subprocess.run(
                    ["bash", "-c", 'source "$1"; write_daemon_config', "contract", str(PREPARE)],
                    env=env, capture_output=True, text=True, check=False, timeout=10)
                self.assertEqual(result.returncode == 0, accepted, result.stderr)
                self.assertEqual((root / "runtime/daemon.json").exists(), accepted,
                                 "only the owned opted-in scope may write the none registration")

    def test_owned_daemon_preserves_exact_default_and_selected_runtime_configuration(self):
        cases = [("unset", None, "", ["--host-uds=open"]),
                 ("empty", "", "", ["--host-uds=open"]),
                 ("none normal", "none", "", ["--host-uds=open", "--overlay2=none"]),
                 ("none profile", "none", "1", ["--host-uds=open", "--overlay2=none"])]
        for name, selector, profile, arguments in cases:
            with self.subTest(name=name), tempfile.TemporaryDirectory() as directory:
                root, env = self.fixture(directory)
                (root / "runtime").mkdir()
                if selector is None:
                    env.pop("REVIEW_ACCEPTANCE_ROOT_OVERLAY")
                else:
                    env["REVIEW_ACCEPTANCE_ROOT_OVERLAY"] = selector
                if selector == "none":
                    env.update({"REVIEW_REAL_REPOSITORY_ACCEPTANCE": "1", "REVIEW_ACCEPTANCE_RUNTIME": "runsc"})
                env["REVIEW_ACCEPTANCE_PROFILE_SOURCE_HELPER"] = profile
                result = subprocess.run(
                    ["bash", "-c", 'source "$1"; write_daemon_config', "contract", str(PREPARE)],
                    env=env, capture_output=True, text=True, check=False, timeout=10)
                self.assertEqual(result.returncode, 0, result.stderr)
                state = root / "runtime"
                self.assertEqual(json.loads((state / "daemon.json").read_text()), {
                    "data-root": str(state / "mount/docker"), "exec-root": str(state / "exec"),
                    "pidfile": str(state / "dockerd.pid"), "hosts": ["unix://" + str(state / "docker.sock")],
                    "storage-driver": "overlay2", "features": {"containerd-snapshotter": False},
                    "exec-opts": ["native.cgroupdriver=cgroupfs"],
                    "runtimes": {"runsc": {"path": "/usr/bin/runsc", "runtimeArgs": arguments}},
                    "bridge": "none", "iptables": True,
                }, "only the explicitly selected fixed runtime tuple may change")
                self.assertFalse((root / "privileged-command").exists(),
                                 "daemon configuration construction should require no privileged operation")

    def test_runsc_defaults_capture_is_bounded_optional_and_not_effective_configuration(self):
        code = PREPARE.read_text().split("<<'PY_FLAGS'\n", 1)[1].split("\nPY_FLAGS", 1)[0]
        cases = [
            ("success", b"-overlay2 value (default root:self)\n", 0, False, False),
            ("nonzero", b"fixed command failure\n", 2, False, False),
            ("timeout", b"partial defaults\n", None, True, False),
            ("execution error", b"", None, False, True),
            ("preexec limit error", b"", None, False, True),
            ("output limit", b"x" * 65536, -25, False, False),
        ]
        for name, data, status, timed_out, execution_error in cases:
            with self.subTest(name=name), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                artifacts = root / "artifacts"
                artifacts.mkdir()
                config = root / "daemon.json"
                runtime = {"path": "/usr/bin/runsc", "runtimeArgs": ["--host-uds=open"]}
                config.write_text(json.dumps({"runtimes": {"runsc": runtime}}))
                before = config.read_bytes()

                def command(args, *, stdout, stderr, timeout, check, preexec_fn):
                    self.assertEqual(args, ["/usr/bin/runsc", "flags"],
                                     "observation must only execute the fixed read-only command")
                    self.assertEqual((stderr, timeout, check), (subprocess.STDOUT, 5, False))
                    preexec_fn()
                    stdout.write(data)
                    if timed_out:
                        raise subprocess.TimeoutExpired(args, timeout)
                    if execution_error:
                        error_type = subprocess.SubprocessError if name == "preexec limit error" else OSError
                        raise error_type("synthetic-private-error")
                    return subprocess.CompletedProcess(args, status)

                with mock.patch("sys.argv", ["flags", str(config), str(artifacts), json.dumps(runtime), ""]), \
                        mock.patch.object(subprocess, "run", side_effect=command) as run, \
                        mock.patch.object(resource, "setrlimit") as bound, \
                        contextlib.redirect_stderr(io.StringIO()) as diagnostics:
                    exec(compile(code, str(PREPARE), "exec"), {})
                run.assert_called_once()
                bound.assert_called_once_with(resource.RLIMIT_FSIZE, (65536, 65536))
                self.assertEqual(config.read_bytes(), before,
                                 "observation must not change runtime registration")
                self.assertEqual((artifacts / "runsc-flags.txt").read_bytes(), data)
                self.assertEqual(json.loads((artifacts / "runsc-flags-observation.json").read_text()), {
                    "command": ["/usr/bin/runsc", "flags"], "exit_status": status,
                    "timeout_seconds": 5, "timed_out": timed_out, "execution_error": execution_error,
                    "retained_bytes": len(data), "output_limit_bytes": 65536,
                    "possibly_truncated": len(data) == 65536, "compiled_defaults_only": True, "root_overlay_selector": "",
                    "registered_runtime": runtime, "effective_container_overlay_verified": False,
                })
                self.assertNotIn("synthetic-private-error", diagnostics.getvalue(),
                                 "optional execution errors must retain a fixed classification")

    def test_runsc_defaults_capture_rejects_unexpected_registration_before_execution(self):
        code = PREPARE.read_text().split("<<'PY_FLAGS'\n", 1)[1].split("\nPY_FLAGS", 1)[0]
        cases = [
            {"path": "/tmp/untrusted-runsc", "runtimeArgs": ["--host-uds=open"]},
            {"path": "/usr/bin/runsc", "runtimeArgs": ["--host-uds=open", "--ignore-cgroups"]},
        ]
        for runtime in cases:
            with self.subTest(runtime=runtime), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                config = root / "daemon.json"
                config.write_text(json.dumps({"runtimes": {"runsc": runtime}}))
                with mock.patch("sys.argv", ["flags", str(config), str(root),
                                             json.dumps({"path": "/usr/bin/runsc", "runtimeArgs": ["--host-uds=open"]}), ""]), \
                        mock.patch.object(subprocess, "run") as run, \
                        self.assertRaises(SystemExit):
                    exec(compile(code, str(PREPARE), "exec"), {})
                run.assert_not_called()
                self.assertFalse((root / "runsc-flags.txt").exists(),
                                 "untrusted registration must never be executed or claimed")

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
                info = {"Driver": "overlay2", "Runtimes": {"runsc": {"path": "/usr/bin/runsc",
                                                                    "runtimeArgs": ["--host-uds=open"]}},
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

    def test_daemon_preflight_checks_loaded_registration_not_only_the_file(self):
        default = {"path": "/usr/bin/runsc", "runtimeArgs": ["--host-uds=open"]}
        none = {"path": "/usr/bin/runsc", "runtimeArgs": ["--host-uds=open", "--overlay2=none"]}
        cases = [
            ("default", "", default, True),
            ("default with Docker feature observations", "", {**default, "status": {"feature": "observed"}}, True),
            ("none", "none", none, True),
            ("none with Docker feature observations", "none", {**none, "status": {"feature": "observed"}}, True),
            ("missing loaded registration", "", None, False),
            ("empty loaded registration", "", {}, False),
            ("default requested but none loaded", "", none, False),
            ("none requested but default loaded", "none", default, False),
            ("wrong executable", "none", {**none, "path": "/tmp/untrusted-runsc"}, False),
            ("missing arguments", "none", {"path": "/usr/bin/runsc"}, False),
            ("wrong argument order", "none", {**none, "runtimeArgs": list(reversed(none["runtimeArgs"]))}, False),
            ("extra argument", "none", {**none, "runtimeArgs": [*none["runtimeArgs"], "--ignore-cgroups"]}, False),
            ("unexpected runtime option", "none", {**none, "options": {"arbitrary": "option"}}, False),
            ("malformed metadata", "none", {**none, "status": "untrusted"}, False),
        ]
        for name, selector, loaded, accepted in cases:
            with self.subTest(name=name), tempfile.TemporaryDirectory() as directory:
                root, env = self.fixture(directory)
                state = root / "runtime"
                state.mkdir()
                artifacts = root / "artifacts"
                artifacts.mkdir()
                env.update({"REVIEW_ACCEPTANCE_ROOT_OVERLAY": selector,
                            "REVIEW_REAL_REPOSITORY_ACCEPTANCE": "1", "REVIEW_ACCEPTANCE_RUNTIME": "runsc"})
                expected = none if selector == "none" else default
                # A correct file must not excuse a different loaded registration.
                (state / "daemon.json").write_text(json.dumps({"runtimes": {"runsc": expected}}))
                info = {"Driver": "overlay2", "Runtimes": {} if loaded is None else {"runsc": loaded},
                        "CgroupVersion": "2", "CgroupDriver": "cgroupfs",
                        "DriverStatus": [["Backing Filesystem", "xfs"], ["Supports d_type", "true"]],
                        "DockerRootDir": str(state / "mount/docker")}
                (artifacts / "docker-info.json").write_text(json.dumps(info))
                result = subprocess.run(
                    ["bash", "-c", 'source "$1"; verify_daemon_configuration', "contract", str(PREPARE)],
                    env=env, capture_output=True, text=True, check=False, timeout=10)
                self.assertEqual(result.returncode == 0, accepted, result.stderr)
                registration = artifacts / "runtime-registration.json"
                self.assertEqual(registration.exists(), accepted,
                                 "only a matching loaded registration may produce configuration evidence")
                if accepted:
                    self.assertEqual(json.loads(registration.read_text()), {
                        "root_overlay_selector": selector, "registered_runtime": expected,
                        "loaded_runtime_registration_verified": True,
                        "effective_container_overlay_verified": False,
                    }, "loaded registration evidence must not claim an effective container proof")
                else:
                    self.assertIn("loaded runsc registration does not match", result.stderr)
                self.assertFalse((root / "privileged-command").exists(),
                                 "loaded registration verification requires no privilege")

    def test_selected_none_defaults_observation_keeps_compiled_defaults_distinct(self):
        code = PREPARE.read_text().split("<<'PY_FLAGS'\n", 1)[1].split("\nPY_FLAGS", 1)[0]
        runtime = {"path": "/usr/bin/runsc", "runtimeArgs": ["--host-uds=open", "--overlay2=none"]}
        for selected, registered, accepted in (("none", runtime, True),
                                                ("none", {**runtime, "runtimeArgs": ["--host-uds=open"]}, False)):
            with self.subTest(registered=registered), tempfile.TemporaryDirectory() as directory:
                root = Path(directory)
                config = root / "daemon.json"
                config.write_text(json.dumps({"runtimes": {"runsc": registered}}))
                def command(args, *, stdout, **kwargs):
                    self.assertEqual(args, ["/usr/bin/runsc", "flags"])
                    stdout.write(b"-overlay2 value (default root:self)\n")
                    return subprocess.CompletedProcess(args, 0)
                with mock.patch("sys.argv", ["flags", str(config), str(root), json.dumps(runtime), selected]), \
                        mock.patch.object(subprocess, "run", side_effect=command) as run:
                    if accepted:
                        exec(compile(code, str(PREPARE), "exec"), {})
                    else:
                        with self.assertRaises(SystemExit):
                            exec(compile(code, str(PREPARE), "exec"), {})
                self.assertEqual(run.call_count, int(accepted),
                                 "a mismatched configured tuple must fail before observing defaults")
                if accepted:
                    evidence = json.loads((root / "runsc-flags-observation.json").read_text())
                    self.assertEqual(evidence["root_overlay_selector"], "none")
                    self.assertEqual(evidence["registered_runtime"], runtime)
                    self.assertTrue(evidence["compiled_defaults_only"])
                    self.assertFalse(evidence["effective_container_overlay_verified"],
                                     "compiled root:self defaults do not invalidate or prove selected none")

    def test_quota_probe_retains_exact_loaded_mode_without_changing_the_quota_mechanism(self):
        for selector, loaded_selector, accepted in (("", "", True), ("none", "none", True),
                                                    ("none", "", False)):
            with self.subTest(selector=selector, loaded_selector=loaded_selector), tempfile.TemporaryDirectory() as directory:
                root, env = self.fixture(directory)
                state = root / "runtime"
                state.mkdir(mode=0o700)
                artifacts = root / "artifacts"
                artifacts.mkdir()
                record = {"format": 1, "state": str(state), "run_id": "123", "attempt": "2",
                          "uid": os.getuid(), "network": env["REVIEW_ACCEPTANCE_NETWORK"]}
                marker = state / "owner.json"
                marker.write_text(json.dumps(record))
                marker.chmod(0o600)
                arguments = ["--host-uds=open"] + (["--overlay2=none"] if loaded_selector == "none" else [])
                info = {"Driver": "overlay2", "Runtimes": {"runsc": {"path": "/usr/bin/runsc", "runtimeArgs": arguments}},
                        "CgroupVersion": "2", "CgroupDriver": "cgroupfs",
                        "DriverStatus": [["Backing Filesystem", "xfs"], ["Supports d_type", "true"]],
                        "DockerRootDir": str(state / "mount/docker")}
                binary = root / "bin"
                (binary / "df").write_text("#!/bin/sh\necho 13958643712\n")
                (binary / "timeout").write_text('#!/bin/sh\nshift\nexec "$@"\n')
                (binary / "docker").write_text('''#!/usr/bin/env python3
import json, os, sys
from pathlib import Path
with Path(os.environ["DOCKER_CALLS"]).open("a") as output:
    output.write(json.dumps(sys.argv[1:]) + "\\n")
if sys.argv[1:] == ["info", "--format", "{{json .}}"]:
    print(os.environ["DOCKER_INFO"])
elif sys.argv[1] == "run":
    print(json.dumps({"result": "pass", "limit_bytes": 67108864, "written_bytes": 66060288, "errno": 28}))
else:
    raise SystemExit(99)
''')
                for command in ("df", "timeout", "docker"):
                    (binary / command).chmod(0o755)
                calls = root / "docker-calls.jsonl"
                env.update({"REVIEW_ACCEPTANCE_ROOT_OVERLAY": selector,
                            "REVIEW_REAL_REPOSITORY_ACCEPTANCE": "1", "REVIEW_ACCEPTANCE_RUNTIME": "runsc",
                            "REVIEW_ACCEPTANCE_IMAGE": "synthetic-acceptance", "DOCKER_CALLS": str(calls),
                            "DOCKER_INFO": json.dumps(info)})
                result = self.invoke(PREPARE, env, "--probe-quota")
                self.assertEqual(result.returncode == 0, accepted, result.stderr + result.stdout)
                observed = [json.loads(line) for line in calls.read_text().splitlines()]
                self.assertEqual(observed[0], ["info", "--format", "{{json .}}"],
                                 "the quota check must refresh the loaded registration first")
                self.assertEqual(len(observed), 2 if accepted else 1,
                                 "a loaded-mode mismatch must stop before creating the quota container")
                adjacent = artifacts / "quota-probe-runtime-registration.json"
                self.assertEqual(adjacent.exists(), accepted)
                if accepted:
                    self.assertEqual(json.loads(adjacent.read_text()), {
                        "root_overlay_selector": selector,
                        "registered_runtime": {"path": "/usr/bin/runsc", "runtimeArgs": arguments},
                        "loaded_runtime_registration_verified": True,
                        "effective_container_overlay_verified": False,
                    })
                    self.assertEqual(observed[1][:-1], [
                        "run", "--rm", "--runtime", "runsc", "--network", "none", "--memory", "256m", "--cpus", "1",
                        "--cap-drop", "ALL", "--security-opt", "no-new-privileges", "--storage-opt", "size=64M",
                        "--label", "io.143.review-acceptance-state=" + str(state),
                        "--entrypoint", "python3", "synthetic-acceptance", "-c",
                    ], "the same-runtime ENOSPC probe must retain its original finite resource and quota controls")
                    self.assertIn("os.fsync(output.fileno())", observed[1][-1])
                    self.assertIn("0 < written <= 64 * 1024 * 1024", observed[1][-1])
                    self.assertEqual(json.loads((artifacts / "quota-exhaustion-probe.json").read_text()), {
                        "result": "pass", "limit_bytes": 67108864, "written_bytes": 66060288, "errno": 28,
                    })
                self.assertFalse((root / "privileged-command").exists(),
                                 "synthetic quota-contract verification must require no privileged operation")

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
