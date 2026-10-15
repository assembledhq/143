#!/usr/bin/env python3
"""Regression tests for the cross-repetition acceptance runtime gate."""

import copy
import importlib.util
import json
from pathlib import Path
import tempfile
import unittest

MODULE = Path(__file__).with_name("verify-review-acceptance-results.py")
SPEC = importlib.util.spec_from_file_location("review_acceptance_results", MODULE)
VERIFY = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(VERIFY)
SOURCE = "a" * 40


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
        "config_sha256": "d" * 64, "helper_sha256": {".review/prepare.py": "e" * 64},
        "source_sha": SOURCE, "image_id": "sha256:" + "f" * 64, "runtime": "runsc",
        "cpu_limit": 2, "memory_limit_mb": 1024, "disk_limit_gb": 1, "snapshot_limit_bytes": 1024 * 1024 * 1024, "runsc_version": VERIFY.RUNTIME_VERSION,
        "runtime_contract": {"root_overlay_selector": "none", "runsc_path": "/usr/bin/runsc", "runsc_args": VERIFY.RUNTIME_ARGS.copy(), "effective_root_proof_required": True, "all_creations_verified": True},
        "created_containers": 2, "destroyed_containers": 2, "creation_resources": resources,
        "credential_detected": False, "disk_quota_accepted": True,
        "passed": not profile, "acceptance_eligible": not profile, "result_kind": "normal_acceptance",
    }
    if profile:
        result.update(result_kind="diagnostic_profile", diagnostic_execution_succeeded=True)
    return result


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
                actual = VERIFY.validate_results(root, 1 if profile else 2, profile, SOURCE)
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
            ("changed helper", lambda r: r.update(helper_sha256={".review/prepare.py": "0" * 64})),
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
                    VERIFY.validate_results(root, 2, False, SOURCE)

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
                    VERIFY.validate_results(root, 2, False, SOURCE)
        with tempfile.TemporaryDirectory() as temporary:
            root = Path(temporary)
            write_repetitions(root, True)
            with self.assertRaises(VERIFY.ValidationError):
                VERIFY.validate_results(root, 2, False, SOURCE)

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
                VERIFY.validate_results(root, 2, False, SOURCE)

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
                    VERIFY.validate_results(root, 1, True, SOURCE)

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
                    VERIFY.validate_results(Path("/unused"), count, profile, source)


if __name__ == "__main__":
    unittest.main()
