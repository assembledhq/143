#!/usr/bin/env python3
"""Regression tests for the cross-repetition acceptance runtime gate."""

import copy
import importlib.util
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import unittest

MODULE = Path(__file__).with_name("verify-review-acceptance-results.py")
SPEC = importlib.util.spec_from_file_location("review_acceptance_results", MODULE)
VERIFY = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(VERIFY)
SOURCE = "a" * 40
IMAGE = "sha256:" + "f" * 64


def result_fixture(profile=False):
    resources = []
    for ordinal in (1, 2):
        identity = str(ordinal) * 64
        resources.append({
            "container_id": identity,
            "effective_runtime_filesystem": {
                "schema": "runsc_effective_root_v1", "runtime_version": VERIFY.RUNTIME_VERSION, "container_id": identity,
                "sandbox_pid": ordinal, "verified": True, "root_overlay_selector": "none",
                "root": {"lower": 1, "upper": 0, "lower_label": "lisafs", "upper_label": "none", "size_classification": "default"},
            },
            "kernel_cgroup_proof": {"container_id": identity, "sandbox_pid": ordinal, "verified": True},
        })
    result = {
        "repository": "example/service", "base_sha": "b" * 40, "head_sha": "c" * 40,
        "config_sha256": "d" * 64, "helper_sha256": {".143/prepare.py": "e" * 64},
        "source_sha": SOURCE, "image_id": IMAGE, "runtime": "runsc",
        "cpu_limit": 2, "memory_limit_mb": 1024, "disk_limit_gb": 1, "snapshot_limit_bytes": 1024 * 1024 * 1024, "runsc_version": VERIFY.RUNTIME_VERSION,
        "runtime_contract": {"root_overlay_selector": "none", "runsc_path": "/usr/bin/runsc", "runsc_args": VERIFY.RUNTIME_ARGS.copy(), "effective_root_proof_required": True, "all_creations_verified": True},
        "created_containers": 2, "destroyed_containers": 2, "creation_resources": resources,
        "credential_detected": False, "disk_quota_accepted": True,
        "passed": not profile, "acceptance_eligible": not profile, "result_kind": "normal_acceptance",
    }
    if profile:
        result.update(result_kind="diagnostic_profile", diagnostic_execution_succeeded=True)
    return result



def manifest_fixture():
    result = result_fixture()
    repository = {field: result[field] for field in (
        "base_sha", "head_sha", "config_sha256", "helper_sha256", "cpu_limit", "memory_limit_mb", "disk_limit_gb", "snapshot_limit_bytes",
    )}
    repository.update(name="assembled", full_name=result["repository"],
                      clone_url="https://github.com/" + result["repository"] + ".git",
                      default_branch="main", installation_id=123, token_kind="github_actions_job", pr_number=456)
    return {"version": 1, "repositories": [repository]}


def expected_fixture():
    return VERIFY.expected_bindings(manifest_fixture(), SOURCE, IMAGE)


def invoke_validator(root, manifest, profile=False, source=SOURCE, image=IMAGE):
    arguments = [sys.executable, "-B", str(MODULE), "--evidence-dir", str(root),
                 "--repetitions", "1" if profile else "2", "--source-sha", source,
                 "--manifest", str(manifest), "--image-id", image]
    if profile:
        arguments.append("--profile")
    return subprocess.run(arguments, capture_output=True, text=True, check=False, timeout=10)


def write_repetitions(root, profile=False):
    count = 1 if profile else 2
    for ordinal in range(1, count + 1):
        folder = root / f"repetition-{ordinal}"
        folder.mkdir()
        result = result_fixture(profile)
        for index, resource in enumerate(result["creation_resources"]):
            identity = f"{ordinal * 10 + index:064x}"
            resource["container_id"] = identity
            resource["effective_runtime_filesystem"]["container_id"] = identity
            resource["kernel_cgroup_proof"]["container_id"] = identity
        (folder / "assembled.json").write_text(json.dumps(result))
        (folder / "repetition.json").write_text(json.dumps({"repetition": ordinal, "exit_status": 0, "source_sha": SOURCE, "requested_repetitions": count, "root_overlay_selector": "none", "runsc_args": VERIFY.RUNTIME_ARGS.copy(), "acceptance_eligible": not profile, "result_kind": "diagnostic_profile" if profile else "normal_acceptance"}))


class RuntimeContractGateTests(unittest.TestCase):
    def test_complete_profile_and_normal_modes_remain_distinct(self):
        for profile in (False, True):
            with self.subTest(profile=profile), tempfile.TemporaryDirectory() as temporary:
                root = Path(temporary)
                write_repetitions(root, profile)
                actual = VERIFY.validate_results(root, 1 if profile else 2, profile, expected_fixture())
                self.assertEqual(actual["normal_repetitions_validated"], 0 if profile else 2)
                self.assertIs(actual["acceptance_eligible"], not profile)

    def test_incomplete_or_mismatched_repetitions_never_validate(self):
        cases = [
            ("missing restore proof", lambda r: r["creation_resources"].pop()),
            ("unverified restore", lambda r: r["creation_resources"][1]["effective_runtime_filesystem"].update(verified=False)),
            ("wrong restored mode", lambda r: r["creation_resources"][1]["effective_runtime_filesystem"]["root"].update(upper=2, upper_label="self")),
            ("wrong restored identity", lambda r: r["creation_resources"][1]["effective_runtime_filesystem"].update(container_id="0" * 64)),
            ("reused container", lambda r: r["creation_resources"].__setitem__(1, copy.deepcopy(r["creation_resources"][0]))),
            ("lost kernel identity", lambda r: r["creation_resources"][1]["kernel_cgroup_proof"].update(sandbox_pid=3)),
            ("unverified kernel", lambda r: r["creation_resources"][1]["kernel_cgroup_proof"].update(verified=False)),
            ("partial cleanup", lambda r: r.update(destroyed_containers=1)),
            ("wrong selector", lambda r: r["runtime_contract"].update(root_overlay_selector="default")),
            ("wrong loaded args", lambda r: r["runtime_contract"].update(runsc_args=["--host-uds=open"])),
            ("missing proof gate", lambda r: r["runtime_contract"].update(effective_root_proof_required=False)),
            ("unverified contract", lambda r: r["runtime_contract"].update(all_creations_verified=False)),
            ("changed source", lambda r: r.update(source_sha="0" * 40)),
            ("changed recipe", lambda r: r.update(head_sha="0" * 40)),
            ("changed helper", lambda r: r.update(helper_sha256={".143/prepare.py": "0" * 64})),
            ("null helper", lambda r: r.update(helper_sha256=None)),
            ("missing binding", lambda r: r.pop("base_sha")),
            ("changed image", lambda r: r.update(image_id="sha256:" + "0" * 64)),
            ("changed limits", lambda r: r.update(memory_limit_mb=2048)),
            ("changed snapshot bound", lambda r: r.update(snapshot_limit_bytes=2048)),
            ("unsupported runtime release", lambda r: r.update(runsc_version="unsupported")),
            ("unsupported restored release", lambda r: r["creation_resources"][1]["effective_runtime_filesystem"].update(runtime_version="unsupported")),
            ("missing normal kind", lambda r: r.pop("result_kind")),
            ("unknown normal kind", lambda r: r.update(result_kind="unknown")),
            ("failed quota", lambda r: r.update(disk_quota_accepted=False)),
            ("credential failure", lambda r: r.update(credential_detected=True)),
            ("ineligible normal", lambda r: r.update(acceptance_eligible=False)),
            ("failed normal", lambda r: r.update(passed=False)),
            ("profile in normal", lambda r: r.update(result_kind="diagnostic_profile")),
            ("boolean count", lambda r: r.update(created_containers=True)),
            ("boolean proof value", lambda r: r["creation_resources"][0]["effective_runtime_filesystem"]["root"].update(lower=True)),
            ("numeric verification", lambda r: r["runtime_contract"].update(all_creations_verified=1)),
        ]
        for name, mutate in cases:
            with self.subTest(name=name), tempfile.TemporaryDirectory() as temporary:
                root = Path(temporary)
                write_repetitions(root)
                path = root / "repetition-2/assembled.json"
                result = json.loads(path.read_text())
                mutate(result)
                path.write_text(json.dumps(result))
                with self.assertRaises(VERIFY.ValidationError):
                    VERIFY.validate_results(root, 2, False, expected_fixture())

    def test_failed_metadata_or_missing_second_repetition_cannot_earn_credit(self):
        cases = [("exit_status", 1), ("exit_status", False), ("repetition", 1), ("source_sha", "0" * 40), ("requested_repetitions", 1), ("root_overlay_selector", "default"), ("runsc_args", []), ("acceptance_eligible", False), ("result_kind", "diagnostic_profile")]
        for field, value in cases:
            with self.subTest(field=field, value=value), tempfile.TemporaryDirectory() as temporary:
                root = Path(temporary)
                write_repetitions(root)
                path = root / "repetition-2/repetition.json"
                metadata = json.loads(path.read_text())
                metadata[field] = value
                path.write_text(json.dumps(metadata))
                with self.assertRaises(VERIFY.ValidationError):
                    VERIFY.validate_results(root, 2, False, expected_fixture())
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            write_repetitions(root, True)
            with self.assertRaises(VERIFY.ValidationError):
                VERIFY.validate_results(root, 2, False, expected_fixture())

    def test_normal_repetitions_require_fresh_container_identities(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            write_repetitions(root)
            previous = json.loads((root / "repetition-1/assembled.json").read_text())
            path = root / "repetition-2/assembled.json"
            current = json.loads(path.read_text())
            current["creation_resources"] = previous["creation_resources"]
            path.write_text(json.dumps(current))
            with self.assertRaisesRegex(VERIFY.ValidationError, "repetitions_reuse_container_identity"):
                VERIFY.validate_results(root, 2, False, expected_fixture())

    def test_profile_cannot_become_eligible_or_hide_runtime_failure(self):
        for field, value in [("acceptance_eligible", True), ("passed", True), ("diagnostic_execution_succeeded", False)]:
            with self.subTest(field=field), tempfile.TemporaryDirectory() as temporary:
                root = Path(temporary)
                write_repetitions(root, True)
                path = root / "repetition-1/assembled.json"
                result = json.loads(path.read_text())
                result[field] = value
                path.write_text(json.dumps(result))
                with self.assertRaises(VERIFY.ValidationError):
                    VERIFY.validate_results(root, 1, True, expected_fixture())

    def test_expected_bindings_map_the_exact_reviewed_manifest_and_built_image(self):
        expected = expected_fixture()
        self.assertEqual(expected, {field: result_fixture()[field] for field in VERIFY.BINDING_FIELDS},
                         "manifest fields and independent source/image arguments must bind every result field")
        self.assertEqual(set(expected), set(VERIFY.BINDING_FIELDS))

    def test_identical_wrong_normal_repetitions_fail_against_the_manifest(self):
        cases = [
            ("helper", "helper_sha256", {".143/prepare.py": "0" * 64}),
            ("extra helper", "helper_sha256", {".143/prepare.py": "e" * 64, ".143/other.py": "0" * 64}),
            ("config", "config_sha256", "0" * 64),
            ("base", "base_sha", "0" * 40),
            ("head", "head_sha", "0" * 40),
            ("repository", "repository", "example/other"),
            ("cpu", "cpu_limit", 3),
            ("memory", "memory_limit_mb", 2048),
            ("disk", "disk_limit_gb", 2),
            ("snapshot", "snapshot_limit_bytes", 2 * 1024 ** 3),
            ("image", "image_id", "sha256:" + "0" * 64),
            ("platform", "source_sha", "0" * 40),
        ]
        for name, field, value in cases:
            with self.subTest(name=name), tempfile.TemporaryDirectory() as temporary:
                root = Path(temporary)
                write_repetitions(root)
                for ordinal in (1, 2):
                    path = root / f"repetition-{ordinal}/assembled.json"
                    result = json.loads(path.read_text())
                    result[field] = value
                    path.write_text(json.dumps(result))
                    if field == "source_sha":
                        path = root / f"repetition-{ordinal}/repetition.json"
                        metadata = json.loads(path.read_text())
                        metadata["source_sha"] = value
                        path.write_text(json.dumps(metadata))
                with self.assertRaises(VERIFY.ValidationError):
                    VERIFY.validate_results(root, 2, False, expected_fixture())

    def test_manifest_requires_the_exact_version_one_single_assembled_schema(self):
        cases = [
            ("not object", None),
            ("missing repositories", {"version": 1}),
            ("missing version", {"repositories": []}),
            ("extra top field", {**manifest_fixture(), "extra": "unreviewed"}),
            ("wrong version", {**manifest_fixture(), "version": 2}),
            ("boolean version", {**manifest_fixture(), "version": True}),
            ("string version", {**manifest_fixture(), "version": "1"}),
            ("missing entries", {"version": 1, "repositories": []}),
            ("extra entry", {"version": 1, "repositories": manifest_fixture()["repositories"] * 2}),
            ("not array", {"version": 1, "repositories": {"assembled": {}}}),
            ("not repository object", {"version": 1, "repositories": [None]}),
        ]
        for name, manifest in cases:
            with self.subTest(name=name), self.assertRaises(VERIFY.ValidationError):
                VERIFY.expected_bindings(manifest, SOURCE, IMAGE)
        for field in manifest_fixture()["repositories"][0]:
            with self.subTest(missing_field=field):
                manifest = manifest_fixture()
                del manifest["repositories"][0][field]
                with self.assertRaises(VERIFY.ValidationError):
                    VERIFY.expected_bindings(manifest, SOURCE, IMAGE)
        for field, value in (("extra", "unreviewed"), ("name", "other")):
            with self.subTest(field=field):
                manifest = manifest_fixture()
                manifest["repositories"][0][field] = value
                with self.assertRaises(VERIFY.ValidationError):
                    VERIFY.expected_bindings(manifest, SOURCE, IMAGE)

    def test_malformed_expected_fields_fail_before_reading_any_repetition(self):
        cases = [
            ("full_name", None), ("full_name", "example/../other"),
            ("clone_url", "https://credential@github.com/example/service.git"),
            ("clone_url", "https://github.com/example/other.git"),
            ("default_branch", ""), ("default_branch", "-unsafe"), ("default_branch", "main\nunsafe"),
            ("installation_id", True), ("installation_id", 0),
            ("pr_number", True), ("pr_number", 0), ("token_kind", "unknown"),
            ("base_sha", "unknown"), ("head_sha", None), ("config_sha256", "d" * 63),
            ("helper_sha256", {}), ("helper_sha256", None),
            ("helper_sha256", {".143/../unowned.py": "e" * 64}),
            ("helper_sha256", {".143/prepare.py": True}),
            ("cpu_limit", True), ("cpu_limit", 0), ("cpu_limit", 65), ("cpu_limit", float("inf")),
            ("memory_limit_mb", True), ("memory_limit_mb", 0),
            ("disk_limit_gb", True), ("disk_limit_gb", 0),
            ("snapshot_limit_bytes", True), ("snapshot_limit_bytes", 0),
        ]
        for field, value in cases:
            with self.subTest(field=field, value=value):
                manifest = manifest_fixture()
                manifest["repositories"][0][field] = value
                with self.assertRaises(VERIFY.ValidationError):
                    VERIFY.expected_bindings(manifest, SOURCE, IMAGE)
        for source, image in (("unknown", IMAGE), (True, IMAGE), (SOURCE, "unknown"), (SOURCE, None)):
            with self.subTest(source=source, image=image), self.assertRaises(VERIFY.ValidationError):
                VERIFY.expected_bindings(manifest_fixture(), source, image)

    def test_cli_requires_manifest_and_actual_image_and_keeps_profile_ineligible(self):
        for profile in (False, True):
            with self.subTest(profile=profile), tempfile.TemporaryDirectory() as temporary:
                root = Path(temporary)
                write_repetitions(root, profile)
                manifest = root / "manifest.json"
                manifest.write_text(json.dumps(manifest_fixture()))
                result = invoke_validator(root, manifest, profile)
                self.assertEqual(result.returncode, 0, result.stderr + result.stdout)
                summary = json.loads(result.stdout)
                self.assertEqual(summary["normal_repetitions_validated"], 0 if profile else 2)
                self.assertIs(summary["acceptance_eligible"], not profile)
                self.assertNotIn("example/service", result.stdout)
                self.assertNotIn(".143/prepare.py", result.stdout)
                for missing in ("--manifest", "--image-id"):
                    arguments = [sys.executable, "-B", str(MODULE), "--evidence-dir", str(root),
                                 "--repetitions", "1" if profile else "2", "--source-sha", SOURCE]
                    arguments += ["--image-id", IMAGE] if missing == "--manifest" else ["--manifest", str(manifest)]
                    failed = subprocess.run(arguments, capture_output=True, text=True, check=False, timeout=10)
                    self.assertEqual(failed.returncode, 2)
                    self.assertIn(missing, failed.stderr)
                for source, image in (("0" * 40, IMAGE), (SOURCE, "sha256:" + "0" * 64)):
                    failed = invoke_validator(root, manifest, profile, source, image)
                    self.assertEqual(failed.returncode, 1, failed.stderr + failed.stdout)
                    summary = json.loads(failed.stdout)
                    self.assertFalse(summary["validated"])
                    self.assertFalse(summary["acceptance_eligible"])
                    self.assertEqual(summary["normal_repetitions_validated"], 0)

    def test_manifest_read_is_bounded_regular_and_has_no_missing_input_fallback(self):
        for kind in ("missing", "duplicate", "extra", "malformed", "oversized", "linked", "fifo"):
            with self.subTest(kind=kind), tempfile.TemporaryDirectory() as temporary:
                root = Path(temporary)
                write_repetitions(root)
                manifest = root / "manifest.json"
                if kind == "duplicate":
                    manifest.write_text('{"version":1,"version":1,"repositories":[]}')
                elif kind == "extra":
                    manifest.write_text(json.dumps({**manifest_fixture(), "extra": "unreviewed-private-value"}))
                elif kind == "malformed":
                    manifest.write_text("unreviewed-private-value")
                elif kind == "oversized":
                    manifest.write_bytes(b" " * (VERIFY.MAX_MANIFEST_BYTES + 1))
                elif kind == "linked":
                    target = root / "target.json"
                    target.write_text(json.dumps(manifest_fixture()))
                    manifest.symlink_to(target)
                elif kind == "fifo":
                    os.mkfifo(manifest)
                result = invoke_validator(root, manifest)
                self.assertEqual(result.returncode, 1, result.stderr + result.stdout)
                summary = json.loads(result.stdout)
                self.assertFalse(summary["validated"])
                self.assertFalse(summary["acceptance_eligible"])
                self.assertEqual(summary["normal_repetitions_validated"], 0)
                self.assertNotIn("unreviewed-private-value", result.stdout + result.stderr)
                self.assertNotIn(str(root), result.stdout + result.stderr)

    def test_failed_normal_remains_ineligible_even_with_all_creation_proofs(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            write_repetitions(root)
            for ordinal in (1, 2):
                path = root / f"repetition-{ordinal}/assembled.json"
                result = json.loads(path.read_text())
                result.update(passed=False, acceptance_eligible=False)
                path.write_text(json.dumps(result))
            with self.assertRaisesRegex(VERIFY.ValidationError, "normal_repetition_not_eligible"):
                VERIFY.validate_results(root, 2, False, expected_fixture())

    def test_unsafe_or_ambiguous_inputs_are_rejected_without_reading_targets(self):
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            target = root / "target.json"
            target.write_text('{"passed":true,"passed":false}')
            with self.assertRaisesRegex(VERIFY.ValidationError, "duplicate_result_field"):
                VERIFY.read_result(target)
            link = root / "link.json"
            link.symlink_to(target)
            with self.assertRaisesRegex(VERIFY.ValidationError, "unreadable_or_malformed_result"):
                VERIFY.read_result(link)
            target.write_bytes(b" " * (VERIFY.MAX_RESULT_BYTES + 1))
            with self.assertRaisesRegex(VERIFY.ValidationError, "unsafe_or_oversized_result"):
                VERIFY.read_result(target)

    def test_invalid_run_selection_is_rejected(self):
        for count, profile, source in [(1, False, SOURCE), (2, True, SOURCE), (2, False, "unknown")]:
            with self.subTest(count=count, profile=profile, source=source):
                with self.assertRaisesRegex(VERIFY.ValidationError, "unsupported_repetition_or_source_selection"):
                    expected = expected_fixture()
                    expected["source_sha"] = source
                    VERIFY.validate_results(Path("/unused"), count, profile, expected)


if __name__ == "__main__":
    unittest.main()
