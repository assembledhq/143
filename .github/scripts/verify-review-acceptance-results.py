#!/usr/bin/env python3
"""Validate the runtime contract shared by isolated acceptance repetitions."""

import argparse
import json
import os
from pathlib import Path, PurePosixPath
import re
import stat
import sys


MAX_RESULT_BYTES = 8 * 1024 * 1024
MAX_MANIFEST_BYTES = 64 * 1024
RUNTIME_VERSION = "runsc version release-20261005.0"
RUNTIME_ARGS = ["--host-uds=open", "--overlay2=none"]
BINDING_FIELDS = (
    "repository", "base_sha", "head_sha", "config_sha256", "helper_sha256",
    "source_sha", "image_id", "runtime", "runsc_version", "cpu_limit", "memory_limit_mb", "disk_limit_gb", "snapshot_limit_bytes",
)


class ValidationError(ValueError):
    """A fixed, safe acceptance-contract failure classification."""


def unique_object(pairs):
    result = {}
    for key, value in pairs:
        if key in result:
            raise ValidationError("duplicate_result_field")
        result[key] = value
    return result


def read_result(path, max_bytes=MAX_RESULT_BYTES):
    try:
        descriptor = os.open(path, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
        with os.fdopen(descriptor, "rb") as stream:
            metadata = os.fstat(stream.fileno())
            if not stat.S_ISREG(metadata.st_mode) or metadata.st_size > max_bytes:
                raise ValidationError("unsafe_or_oversized_result")
            encoded = stream.read(max_bytes + 1)
        if len(encoded) > max_bytes:
            raise ValidationError("unsafe_or_oversized_result")
        result = json.loads(encoded, object_pairs_hook=unique_object)
    except (OSError, UnicodeError, json.JSONDecodeError, RecursionError):
        raise ValidationError("unreadable_or_malformed_result") from None
    if not isinstance(result, dict):
        raise ValidationError("result_must_be_object")
    return result


def binding_signature(result):
    if not isinstance(result, dict) or any(field not in result for field in BINDING_FIELDS):
        raise ValidationError("candidate_binding_missing")
    if any(not isinstance(result[field], str) or not re.fullmatch(r"[0-9a-f]{40}", result[field]) for field in ("base_sha", "head_sha", "source_sha")) or not isinstance(result["config_sha256"], str) or not re.fullmatch(r"[0-9a-f]{64}", result["config_sha256"]):
        raise ValidationError("invalid_immutable_candidate_binding")
    helpers = result["helper_sha256"]
    if not isinstance(helpers, dict) or not helpers:
        raise ValidationError("invalid_helper_binding")
    for path, digest in helpers.items():
        if not isinstance(path, str) or not path.startswith(".143/") or str(PurePosixPath(path)) != path or ".." in PurePosixPath(path).parts or any(character in path for character in "\\\r\n\x00") or not isinstance(digest, str) or not re.fullmatch(r"[0-9a-f]{64}", digest):
            raise ValidationError("invalid_helper_binding")
    if result["runtime"] != "runsc" or result["runsc_version"] != RUNTIME_VERSION or not isinstance(result["repository"], str) or not re.fullmatch(r"[A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+", result["repository"]) or ".." in result["repository"] or not isinstance(result["image_id"], str) or not re.fullmatch(r"sha256:[0-9a-f]{64}", result["image_id"]):
        raise ValidationError("invalid_runtime_or_repository_binding")
    if type(result["cpu_limit"]) not in (int, float) or not 0 < result["cpu_limit"] <= 64 or type(result["memory_limit_mb"]) is not int or not 512 <= result["memory_limit_mb"] <= 131072 or type(result["disk_limit_gb"]) is not int or not 0 < result["disk_limit_gb"] <= 100 or type(result["snapshot_limit_bytes"]) is not int or not 0 < result["snapshot_limit_bytes"] <= 20 * 1024 ** 3:
        raise ValidationError("invalid_resource_binding")
    return {field: result[field] for field in BINDING_FIELDS}


def expected_bindings(manifest, source_sha, image_id):
    if not isinstance(manifest, dict) or set(manifest) != {"version", "repositories"} or type(manifest["version"]) is not int or manifest["version"] != 1:
        raise ValidationError("invalid_expected_manifest")
    repositories = manifest["repositories"]
    if not isinstance(repositories, list) or len(repositories) != 1 or not isinstance(repositories[0], dict):
        raise ValidationError("expected_manifest_requires_one_assembled_repository")
    repository = repositories[0]
    fields = {"name", "full_name", "clone_url", "default_branch", "installation_id", "token_kind", "pr_number", "base_sha", "head_sha", "config_sha256", "helper_sha256", "cpu_limit", "memory_limit_mb", "disk_limit_gb", "snapshot_limit_bytes"}
    if set(repository) != fields or repository["name"] != "assembled":
        raise ValidationError("invalid_expected_repository_manifest")
    expected = {field: repository[field] for field in ("base_sha", "head_sha", "config_sha256", "helper_sha256", "cpu_limit", "memory_limit_mb", "disk_limit_gb", "snapshot_limit_bytes")}
    expected.update(repository=repository["full_name"], source_sha=source_sha, image_id=image_id, runtime="runsc", runsc_version=RUNTIME_VERSION)
    expected = binding_signature(expected)
    branch = repository["default_branch"]
    if repository["clone_url"] != "https://github.com/" + expected["repository"] + ".git" or not isinstance(branch, str) or not branch or branch.startswith("-") or any(character in branch for character in "\r\n\x00") or any(type(repository[field]) is not int or repository[field] <= 0 for field in ("installation_id", "pr_number")) or repository["token_kind"] not in ("github_app_installation", "github_actions_job"):
        raise ValidationError("invalid_expected_repository_identity")
    return expected


def validate_result(result, profile, expected_bindings):
    signature = binding_signature(result)
    if signature != expected_bindings:
        raise ValidationError("pinned_candidate_binding_mismatched")
    contract = result.get("runtime_contract")
    expected = {
        "root_overlay_selector": "none", "runsc_path": "/usr/bin/runsc",
        "runsc_args": RUNTIME_ARGS, "effective_root_proof_required": True,
        "all_creations_verified": True,
    }
    if contract != expected or any(contract.get(key) is not True for key in ("effective_root_proof_required", "all_creations_verified")):
        raise ValidationError("runtime_contract_mismatched_or_unverified")
    created, destroyed = result.get("created_containers"), result.get("destroyed_containers")
    resources = result.get("creation_resources")
    if type(created) is not int or type(destroyed) is not int or created < 2 or created != destroyed or not isinstance(resources, list) or len(resources) != created:
        raise ValidationError("incomplete_container_lifecycle")
    identities = set()
    for resource in resources:
        if not isinstance(resource, dict):
            raise ValidationError("missing_creation_proof")
        identity = resource.get("container_id")
        proof = resource.get("effective_runtime_filesystem")
        kernel = resource.get("kernel_cgroup_proof")
        if not isinstance(identity, str) or not re.fullmatch(r"[0-9a-f]{64}", identity) or identity in identities:
            raise ValidationError("missing_or_reused_container_identity")
        identities.add(identity)
        if not isinstance(proof, dict) or proof.get("verified") is not True or proof.get("cleanup_failed", False) is not False or proof.get("unavailable_reason", "") != "" or proof.get("container_id") != identity or proof.get("root_overlay_selector") != "none":
            raise ValidationError("missing_or_unverified_effective_mode")
        if proof.get("schema") != "runsc_effective_root_v1" or proof.get("runtime_version") != RUNTIME_VERSION or not isinstance(kernel, dict) or kernel.get("verified") is not True or kernel.get("container_id") != identity or proof.get("sandbox_pid") != kernel.get("sandbox_pid") or type(kernel.get("sandbox_pid")) is not int or type(proof.get("sandbox_pid")) is not int or proof["sandbox_pid"] <= 0:
            raise ValidationError("unbound_creation_identity")
        root = proof.get("root")
        if root != {"lower": 1, "upper": 0, "lower_label": "lisafs", "upper_label": "none", "size_classification": "default"} or type(root.get("lower")) is not int or type(root.get("upper")) is not int:
            raise ValidationError("unexpected_effective_root_mode")
    if result.get("credential_detected") is not False or result.get("disk_quota_accepted") is not True:
        raise ValidationError("security_or_quota_gate_failed")
    if profile:
        if result.get("result_kind") != "diagnostic_profile" or result.get("acceptance_eligible") is not False or result.get("passed") is not False or result.get("diagnostic_execution_succeeded") is not True:
            raise ValidationError("incomplete_or_eligible_diagnostic")
    elif result.get("passed") is not True or result.get("acceptance_eligible") is not True or result.get("result_kind") != "normal_acceptance" or "diagnostic_execution_succeeded" in result:
        raise ValidationError("normal_repetition_not_eligible")
    return signature, contract


def validate_results(evidence_root, repetitions, profile, expected_bindings):
    if not isinstance(expected_bindings, dict):
        raise ValidationError("invalid_expected_binding")
    source_sha = expected_bindings.get("source_sha")
    if repetitions != (1 if profile else 2) or not isinstance(source_sha, str) or not re.fullmatch(r"[0-9a-f]{40}", source_sha):
        raise ValidationError("unsupported_repetition_or_source_selection")
    if set(expected_bindings) != set(BINDING_FIELDS):
        raise ValidationError("invalid_expected_binding")
    expected_bindings = binding_signature(expected_bindings)
    baseline = None
    prior_identities = set()
    for ordinal in range(1, repetitions + 1):
        repetition_root = evidence_root / f"repetition-{ordinal}"
        if not repetition_root.is_dir() or repetition_root.is_symlink():
            raise ValidationError("missing_or_linked_repetition")
        metadata = read_result(repetition_root / "repetition.json")
        if type(metadata.get("repetition")) is not int or metadata["repetition"] != ordinal or type(metadata.get("exit_status")) is not int or metadata["exit_status"] != 0 or metadata.get("source_sha") != source_sha or type(metadata.get("requested_repetitions")) is not int or metadata["requested_repetitions"] != repetitions:
            raise ValidationError("repetition_metadata_mismatched_or_failed")
        if metadata.get("root_overlay_selector") != "none" or metadata.get("runsc_args") != RUNTIME_ARGS or metadata.get("acceptance_eligible") is not (not profile) or metadata.get("result_kind") != ("diagnostic_profile" if profile else "normal_acceptance"):
            raise ValidationError("repetition_runtime_or_eligibility_mismatched")
        result = read_result(repetition_root / "assembled.json")
        signature = validate_result(result, profile, expected_bindings)
        identities = {resource["container_id"] for resource in result["creation_resources"]}
        if prior_identities.intersection(identities):
            raise ValidationError("repetitions_reuse_container_identity")
        prior_identities.update(identities)
        if baseline is not None and signature != baseline:
            raise ValidationError("repetitions_use_different_immutable_candidate")
        baseline = signature
    return {"validated": True, "root_overlay_selector": "none", "runsc_args": RUNTIME_ARGS, "requested_repetitions": repetitions, "result_kind": "diagnostic_profile" if profile else "normal_acceptance", "acceptance_eligible": not profile, "normal_repetitions_validated": 0 if profile else repetitions, "scope": "Source/runtime/repetition contract only; guest lifecycle and outer cleanup require independent acceptance evidence. Production runtime activation is separate."}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--evidence-dir", required=True, type=Path)
    parser.add_argument("--repetitions", required=True, type=int, choices=(1, 2))
    parser.add_argument("--profile", action="store_true")
    parser.add_argument("--source-sha", required=True)
    parser.add_argument("--manifest", required=True, type=Path)
    parser.add_argument("--image-id", required=True)
    arguments = parser.parse_args()
    try:
        manifest = read_result(arguments.manifest, max_bytes=MAX_MANIFEST_BYTES)
        expected = expected_bindings(manifest, arguments.source_sha, arguments.image_id)
        summary = validate_results(arguments.evidence_dir, arguments.repetitions, arguments.profile, expected)
    except ValidationError as error:
        summary = {"validated": False, "reason": str(error), "acceptance_eligible": False, "normal_repetitions_validated": 0}
    print(json.dumps(summary, sort_keys=True))
    return 0 if summary["validated"] else 1


if __name__ == "__main__":
    sys.exit(main())
