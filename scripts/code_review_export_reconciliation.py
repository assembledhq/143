#!/usr/bin/env python3
"""Offline publication-clock evidence reconciliation; explicit metadata files only.

No network, original source extraction, outcomes, selection or partitioning.
Acceptance pins establish the caller's integrity boundary, not execution authority.
"""
import argparse
from collections import defaultdict
from contextlib import contextmanager
import json
import os
from pathlib import Path
import re
import stat
import sys
import uuid

import code_review_export_metadata as metadata

INPUT_INVALID = "RECONCILIATION_INPUT_INVALID"
SOURCE_UNACCEPTED = "RECONCILIATION_SOURCE_UNACCEPTED"
BINDING_MISMATCH = "RECONCILIATION_BINDING_MISMATCH"
RULESET = "source-publication-clock/v1"
PROFILE = "publication-event-metadata/v1"
MAX_BYTES = 256 * 1024 * 1024
MAX_OBSERVATIONS = 100_000
MAX_FACTS = 200_000
MAX_ARTIFACTS = 4096
MAX_REPOSITORIES = 256
MAX_REFERENCES = 1_000_000
IDENTITY = ("provider_repository_id", "pr_number", "provider_review_id")
SOURCE_FIELDS = (
    "source_kind", "source_id", "source_sha256", "repository_id", "pull_request_id", "session_id",
    "metadata_id", "assessment_id", "source_assessment_id", "previous_assessment_id",
    "previous_published_assessment_id", "superseded_by_assessment_id", "superseded_by_session_id",
    "generation", "provider_review_id", "publication_key", "head_sha", "base_sha", "base_ref",
    "submitted_commit_sha", "created_at", "completed_at", "superseded_at",
)
BASIS_ORIGIN = {
    "authenticated_provider_submission": "provider_submission_event",
    "verified_historical_publication_edit": "publisher_edit_event",
    "provider_object_metadata_only": "provider_review_object",
}
REASONS = (
    "missing_provider_review_id", "missing_pr_identity", "missing_publication_key", "missing_head_sha",
    "missing_source_created_at", "submitted_head_mismatch", "no_authoritative_event", "source_not_authoritative",
    "publication_key_mismatch", "event_head_mismatch", "before_source_creation", "after_acquisition_boundary",
    "event_after_source_observation", "reused_provider_review", "conflicting_event_times",
    "conflicting_original_observations", "frozen_clock_conflict", "conflicting_original_clocks",
    "edit_basis_not_supported_v1", "source_coverage_incomplete",
)
SAFE_FAILURES = (OSError, ValueError, TypeError, KeyError, IndexError, OverflowError, RecursionError, UnicodeError)


class ReconciliationError(Exception):
    """Static public code only; stage is available to independent semantic tests."""
    def __init__(self, code, stage):
        super().__init__(code)
        self.code, self.stage = code, stage


@contextmanager
def stage(number, code=INPUT_INVALID):
    try:
        yield
    except SAFE_FAILURES:
        raise ReconciliationError(code, number) from None


def require(condition):
    metadata.need(condition)


def pattern(expression):
    compiled = re.compile(expression)
    def check(value):
        require(type(value) is str and compiled.fullmatch(value) is not None)
    return check


identifier = pattern(r"[!-~]{1,128}")
selector = pattern(r"[a-z][a-z0-9_]{0,31}")


def versioned_selector(value):
    require(type(value) is str and len(value) <= 128)
    pattern(r"[a-z][a-z0-9_-]*(/[a-z0-9_-]+)+")(value)


def enumeration(*values):
    def check(value):
        require(type(value) is str and value in values)
    return check


def constant(expected):
    def check(value):
        require(type(value) is type(expected) and value == expected)
    return check


def number(value):
    metadata.integer(value, 1, 2**63 - 1)


def optional(validate):
    def check(value):
        if value is not None:
            validate(value)
    return check


def sequence(validate, bound, unique=False):
    def check(value):
        require(type(value) is list and len(value) <= bound)
        if unique:
            require(len({metadata.canonical(v) for v in value}) == len(value))
        for item in value:
            validate(item)
    return check


def fields(validators):
    def check(value):
        metadata.keys(value, validators)
        for key, validate in validators.items():
            validate(value[key])
    return check


NEW_TIMESTAMP = re.compile(
    r"(?:000[1-9]|00[1-9][0-9]|0[1-9][0-9]{2}|[1-9][0-9]{3})-[0-9]{2}-[0-9]{2}T"
    r"(?:[01][0-9]|2[0-3]):[0-5][0-9]:[0-5][0-9](?:\.[0-9]{1,6}|\.[0-9]{6}0{1,3})?"
    r"(?:Z|[+-](?:[01][0-9]|2[0-3]):[0-5][0-9])"
)


def new_timestamp(value):
    require(type(value) is str and len(value) <= 40 and NEW_TIMESTAMP.fullmatch(value) is not None)
    metadata.timestamp(value)  # Calendar, precision and UTC-range validation are mandatory.


provider_identity = fields({name: number for name in IDENTITY})
locator = fields({
    "acquisition_id": metadata.identity, "acquisition_manifest_sha256": metadata.digest,
    "page_sha256": metadata.digest, "record_index": lambda v: metadata.integer(v, 0, 99),
    "source_identity": pattern(r"(assessment|legacy):[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}"),
})
references = {
    "locator": locator, "record_sha256": metadata.digest, "source_sha256": metadata.digest,
    "event_ids": sequence(identifier, MAX_FACTS, True),
    "object_observation_ids": sequence(identifier, MAX_FACTS, True),
}
common = {
    "schema_version": constant(1), "dataset_kind": selector, "ruleset": versioned_selector,
    "inventory_sha256": metadata.digest, "catalog_sha256": metadata.digest, "original_catalog_sha256": metadata.digest,
}
extractor = {"source_profile": versioned_selector, "extractor_revision": metadata.git_sha, "extractor_code_sha256": metadata.digest}
coverage = fields({
    "identities": sequence(provider_identity, MAX_OBSERVATIONS, True), "from": optional(new_timestamp),
    "until": new_timestamp, "projection": enumeration("complete", "partial"),
})
accepted_artifact = fields({
    "artifact_id": identifier, "sha256": metadata.digest, "basis": enumeration(*BASIS_ORIGIN),
    "verification_receipt_sha256": metadata.digest, "coverage": optional(coverage),
})
witness_artifact = fields({
    "artifact_id": identifier, "sha256": metadata.digest, "origin_kind": enumeration(*BASIS_ORIGIN.values()), "observed_at": new_timestamp,
})
event = fields({
    **{name: number for name in IDENTITY}, "event_id": identifier, "artifact_id": identifier,
    "commit_sha": metadata.git_sha, "publication_key_sha256": metadata.digest,
    "event_kind": enumeration("review_submitted", "review_body_edited"),
    "clock_kind": enumeration("provider_submitted_at", "provider_edit_event_at"), "event_time": new_timestamp,
    "publication_link_basis": enumeration("provider_event_output_marker", "publisher_event_exact_publication_key"),
})
object_observation = fields({
    **{name: number for name in IDENTITY}, "observation_id": identifier, "artifact_id": identifier,
    "commit_sha": metadata.git_sha, "object_submitted_at": optional(new_timestamp),
})
validate_witness = fields({
    **common, **extractor, "purpose": constant("source_publication_metadata_witness"), "observed_at": new_timestamp,
    "artifacts": sequence(witness_artifact, MAX_ARTIFACTS), "events": sequence(event, MAX_FACTS),
    "object_observations": sequence(object_observation, MAX_FACTS),
    "bindings": sequence(fields(references), MAX_OBSERVATIONS),
})
validate_acceptance = fields({
    **common, **extractor, "purpose": constant("accept_publication_metadata_source"), "witness_sha256": metadata.digest,
    "repository_bindings": sequence(fields({"repository_id": metadata.identity, "provider_repository_id": number}), MAX_REPOSITORIES),
    "artifacts": sequence(accepted_artifact, MAX_ARTIFACTS),
    "operator_attestation": fields({
        "attestor_id": identifier, "attested_at": new_timestamp, "verification_receipt_sha256": metadata.digest,
        "permitted_use": constant("publication_clock_reconstruction_only"), "confers_execution_authorization": constant(False),
        "limitation": constant("operator_attested_projection_original_not_reverified_by_consumer"),
    }),
})


def sidecar_entry(value):
    fields({
        **references, "original_publication_time": optional(metadata.timestamp),
        "disposition": enumeration("matched_event", "unresolved", "preserve_frozen", "frozen_conflict"),
        "reconciled_publication_time": optional(new_timestamp), "reasons": sequence(enumeration(*REASONS), 20, True),
    })(value)
    disposition = value["disposition"]
    if disposition == "matched_event":
        require(value["original_publication_time"] is None and not value["reasons"])
        pattern(r"[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}\.[0-9]{6}Z")(value["reconciled_publication_time"])
    else:
        require(value["reconciled_publication_time"] is None)
        require((value["original_publication_time"] is None) == (disposition == "unresolved"))
        require(bool(value["reasons"]) == (disposition != "preserve_frozen"))


validate_output = fields({
    **common, "purpose": constant("publication_clock_reconciliation"), "witness_sha256": metadata.digest,
    "source_acceptance_sha256": metadata.digest, "entries": sequence(sidecar_entry, MAX_OBSERVATIONS),
})


def reject_number(_value):
    require(False)


def load(raw):
    require(type(raw) is bytes and len(raw) <= MAX_BYTES)
    metadata.Document(raw)  # UTF-8, framing, duplicate keys and depth, before deserialization.
    value = json.loads(raw, parse_float=reject_number, parse_constant=reject_number)
    def unicode_check(item):
        if isinstance(item, str):
            item.encode("utf-8")
        elif isinstance(item, dict):
            for key, child in item.items():
                key.encode("utf-8")
                unicode_check(child)
        elif isinstance(item, list):
            for child in item:
                unicode_check(child)
    unicode_check(value)
    return value


def unique_index(rows, key):
    result = {row[key]: row for row in rows}
    require(len(result) == len(rows))
    return result


def identity_key(row):
    return tuple(row[name] for name in IDENTITY)


def original_group(row, repository_map):
    if row["pull_request_id"] is not None and row["pr_number"] is not None and row["provider_review_id"] is not None:
        return (repository_map.get(row["repository_id"]), row["pr_number"], row["provider_review_id"])
    return None


def reference_count(rows):
    return sum(len(r["event_ids"]) + len(r["object_observation_ids"]) for r in rows)


def validate_cross_files(index, witness, acceptance, output, raw):
    documents = [witness, acceptance] + ([output] if output is not None else [])
    for document in documents:
        for name, expected in (("inventory_sha256", metadata.sha(raw["inventory"])),
                               ("catalog_sha256", index["catalog_sha256"]), ("original_catalog_sha256", index["original_catalog_sha256"])):
            require(document[name] == expected)
    require(acceptance["witness_sha256"] == metadata.sha(raw["witness"]))
    if output is not None:
        require(output["witness_sha256"] == metadata.sha(raw["witness"]))
        require(output["source_acceptance_sha256"] == metadata.sha(raw["acceptance"]))
        require(reference_count(output["entries"]) <= MAX_REFERENCES)
    for name in ("extractor_revision", "extractor_code_sha256"):
        require(witness[name] == acceptance[name])
    artifacts = {a["artifact_id"]: a for a in witness["artifacts"]}
    accepted = {a["artifact_id"]: a for a in acceptance["artifacts"]}
    require(artifacts.keys() == accepted.keys())
    for key, artifact in artifacts.items():
        require(artifact["sha256"] == accepted[key]["sha256"])
        observed = metadata.timestamp(artifact["observed_at"])
        require(observed <= metadata.timestamp(witness["observed_at"]))
        span = accepted[key]["coverage"]
        if span is not None:
            until = metadata.timestamp(span["until"])
            require(until <= observed)
            require(span["from"] is None or metadata.timestamp(span["from"]) <= until)
    require(metadata.timestamp(witness["observed_at"]) <= metadata.timestamp(acceptance["operator_attestation"]["attested_at"]))
    events = {e["event_id"]: e for e in witness["events"]}
    objects = {o["observation_id"]: o for o in witness["object_observations"]}
    require(len(events) + len(objects) <= MAX_FACTS)
    for fact in (*events.values(), *objects.values()):
        require(fact["artifact_id"] in artifacts)
    require(reference_count(witness["bindings"]) <= MAX_REFERENCES)
    reference_rows = witness["bindings"] + (output["entries"] if output is not None else [])
    for binding in reference_rows:
        require(all(key in events for key in binding["event_ids"]))
        require(all(key in objects for key in binding["object_observation_ids"]))
    require(sum(len(a["coverage"]["identities"]) for a in accepted.values() if a["coverage"] is not None) <= MAX_REFERENCES)
    known = defaultdict(set)
    for row in index["observations"]:
        if row["provider_review_id"] is not None and row["pr_number"] is not None:
            known[(row["repository_id"], row["provider_review_id"])].add(row["pr_number"])
    require(all(len(numbers) == 1 for numbers in known.values()))
    # Bound candidate expansion before authority/binding checks, even for incomplete claims.
    fact_counts = defaultdict(int)
    for fact in (*events.values(), *objects.values()):
        fact_counts[identity_key(fact)] += 1
    providers = defaultdict(set)
    for mapping in acceptance["repository_bindings"]:
        providers[mapping["repository_id"]].add(mapping["provider_repository_id"])
    expanded = 0
    for row in index["observations"]:
        if row["pull_request_id"] is not None and row["pr_number"] is not None and row["provider_review_id"] is not None:
            expanded += sum(fact_counts[(provider, row["pr_number"], row["provider_review_id"])] for provider in providers[row["repository_id"]])
            require(expanded <= MAX_REFERENCES)
    return artifacts, accepted, events, objects


def validate_authority(index, witness, acceptance, output, mode, artifacts, accepted):
    for document in [witness, acceptance] + ([output] if output is not None else []):
        require(document["ruleset"] == RULESET and document["dataset_kind"] == mode and mode in ("synthetic", "historical"))
    require(witness["source_profile"] == acceptance["source_profile"] == PROFILE)
    mappings = acceptance["repository_bindings"]
    repository_map = {m["repository_id"]: m["provider_repository_id"] for m in mappings}
    require(len(repository_map) == len(mappings) == len(set(repository_map.values())))
    require(set(repository_map) == {a["filters"]["repository_id"] for a in index["acquisitions"]})
    groups = {original_group(row, repository_map) for row in index["observations"]} - {None}
    for fact in (*witness["events"], *witness["object_observations"]):
        require(identity_key(fact) in groups)
    for key, artifact in accepted.items():
        require(BASIS_ORIGIN[artifact["basis"]] == artifacts[key]["origin_kind"])
        span = artifact["coverage"]
        if artifact["basis"] == "provider_object_metadata_only":
            require(span is None)
        if span is not None:
            require(all(identity_key(identity) in groups for identity in span["identities"]))
    return repository_map


def candidate_index(facts, field):
    result = defaultdict(list)
    for fact in facts:
        result[identity_key(fact)].append(fact[field])
    return {key: sorted(ids) for key, ids in result.items()}


def validate_bindings(index, witness, repository_map, events, objects):
    by_locator = {metadata.locator_key(b): b for b in witness["bindings"]}
    require(len(by_locator) == len(witness["bindings"]) == len(index["observations"]))
    event_index = candidate_index(events.values(), "event_id")
    object_index = candidate_index(objects.values(), "observation_id")
    for row in index["observations"]:
        binding = by_locator[metadata.locator_key(row)]
        for name in ("locator", "record_sha256", "source_sha256"):
            require(binding[name] == row[name])
        group = original_group(row, repository_map)
        require(sorted(binding["event_ids"]) == event_index.get(group, []))
        require(sorted(binding["object_observation_ids"]) == object_index.get(group, []))
    return by_locator


def global_reasons(index, witness, repository_map):
    sources, clocks, keys = defaultdict(set), defaultdict(set), defaultdict(set)
    for row in index["observations"]:
        identity = row["locator"]["source_identity"]
        sources[identity].add(metadata.canonical({name: row[name] for name in SOURCE_FIELDS}))
        if row["publication_time"] is not None:
            clocks[identity].add(metadata.timestamp(row["publication_time"]))
        if row["provider_review_id"] is not None and row["publication_key"] is not None:
            keys[(repository_map[row["repository_id"]], row["provider_review_id"])].add(metadata.sha(row["publication_key"].encode("utf-8")))
    for event in witness["events"]:
        keys[(event["provider_repository_id"], event["provider_review_id"])].add(event["publication_key_sha256"])
    return ({key for key, values in sources.items() if len(values) > 1},
            {key for key, values in clocks.items() if len(values) > 1},
            {key for key, values in keys.items() if len(values) > 1})


def lifetime_coverage(witness, accepted):
    complete = defaultdict(set)
    for artifact in accepted.values():
        span = artifact["coverage"]
        if span is not None and span["projection"] == "complete" and span["from"] is None and metadata.timestamp(span["until"]) >= metadata.timestamp(witness["observed_at"]):
            for identity in span["identities"]:
                complete[identity_key(identity)].add(artifact["basis"])
    return {key for key, bases in complete.items() if {"authenticated_provider_submission", "verified_historical_publication_edit"} <= bases}


def original_reasons(row):
    reasons = set()
    for field, reason in (("provider_review_id", "missing_provider_review_id"), ("publication_key", "missing_publication_key"),
                          ("head_sha", "missing_head_sha"), ("created_at", "missing_source_created_at")):
        if row[field] is None:
            reasons.add(reason)
    if row["pr_number"] is None or row["pull_request_id"] is None:
        reasons.add("missing_pr_identity")
    if row["submitted_commit_sha"] is not None and row["head_sha"] is not None and row["submitted_commit_sha"] != row["head_sha"]:
        reasons.add("submitted_head_mismatch")
    return reasons


def event_reasons(row, event, cutoff, artifact, accepted):
    reasons = set()
    basis = accepted[event["artifact_id"]]["basis"]
    submission = basis == "authenticated_provider_submission" and event["event_kind"] == "review_submitted" and event["clock_kind"] == "provider_submitted_at"
    edit = basis == "verified_historical_publication_edit" and event["event_kind"] == "review_body_edited" and event["clock_kind"] == "provider_edit_event_at"
    if not (submission or edit):
        reasons.add("source_not_authoritative")
    if event["event_kind"] == "review_body_edited":
        reasons.add("edit_basis_not_supported_v1")
    if row["publication_key"] is not None and event["publication_key_sha256"] != metadata.sha(row["publication_key"].encode("utf-8")):
        reasons.add("publication_key_mismatch")
    if row["head_sha"] is not None and event["commit_sha"] != row["head_sha"]:
        reasons.add("event_head_mismatch")
    clock = metadata.timestamp(event["event_time"])
    if row["created_at"] is not None and clock < metadata.timestamp(row["created_at"]):
        reasons.add("before_source_creation")
    if clock > cutoff:
        reasons.add("after_acquisition_boundary")
    if clock > metadata.timestamp(artifact["observed_at"]):
        reasons.add("event_after_source_observation")
    return reasons, submission, clock


def compute_entries(index, witness, accepted, artifacts, events, bindings, repository_map):
    source_conflicts, clock_conflicts, reuse = global_reasons(index, witness, repository_map)
    covered = lifetime_coverage(witness, accepted)
    cutoffs = {a["id"]: metadata.timestamp(a["acquired_at"]) for a in index["acquisitions"]}
    entries = []
    for row in sorted(index["observations"], key=metadata.locator_key):
        binding = bindings[metadata.locator_key(row)]
        original = original_reasons(row)
        frozen = row["publication_time"] is not None
        reasons = set()
        if not frozen or binding["event_ids"]:
            reasons.update(original)
        elif "submitted_head_mismatch" in original:
            reasons.add("submitted_head_mismatch")
        identity = row["locator"]["source_identity"]
        if identity in source_conflicts:
            reasons.add("conflicting_original_observations")
        if identity in clock_conflicts:
            reasons.add("conflicting_original_clocks")
        if (repository_map[row["repository_id"]], row["provider_review_id"]) in reuse:
            reasons.add("reused_provider_review")
        eligible = set()
        for event_id in binding["event_ids"]:
            candidate = events[event_id]
            failures, submission, clock = event_reasons(row, candidate, cutoffs[row["locator"]["acquisition_id"]], artifacts[candidate["artifact_id"]], accepted)
            reasons.update(failures)
            if submission and not failures and not original:
                eligible.add(clock)
        if len(eligible) > 1:
            reasons.add("conflicting_event_times")
        if frozen:
            if any(clock != metadata.timestamp(row["publication_time"]) for clock in eligible):
                reasons.add("frozen_clock_conflict")
            disposition = "frozen_conflict" if reasons else "preserve_frozen"
        else:
            if not binding["event_ids"]:
                reasons.add("no_authoritative_event")
            group = original_group(row, repository_map)
            if group is not None and row["created_at"] is not None and group not in covered:
                reasons.add("source_coverage_incomplete")
            disposition = "unresolved" if reasons else "matched_event"
            require(bool(reasons) or len(eligible) == 1)
        clock = next(iter(eligible)).isoformat(timespec="microseconds").replace("+00:00", "Z") if disposition == "matched_event" else None
        entries.append({
            "locator": row["locator"], "record_sha256": row["record_sha256"], "source_sha256": row["source_sha256"],
            "event_ids": sorted(binding["event_ids"]), "object_observation_ids": sorted(binding["object_observation_ids"]),
            "original_publication_time": row["publication_time"], "disposition": disposition,
            "reconciled_publication_time": clock, "reasons": sorted(reasons),
        })
    return entries


def bounded_canonical(value):
    # Stop output expansion before allocating a complete oversized byte string.
    payload = bytearray()
    encoder = json.JSONEncoder(sort_keys=True, separators=(",", ":"), ensure_ascii=True, allow_nan=False)
    for chunk in encoder.iterencode(value):
        raw = chunk.encode("ascii")
        require(len(payload) + len(raw) + 1 <= MAX_BYTES)
        payload.extend(raw)
    payload.extend(b"\n")
    return bytes(payload)


def reconcile(inventory_raw, witness_raw, acceptance_raw, *, inventory_sha256, source_acceptance_sha256, mode, output_raw=None):
    """Return canonical sidecar bytes, or a static error with deterministic stage.

    Optional output_raw verifies an independently produced sidecar; it grants no
    historical source acceptance and is never used to choose computed entries.
    """
    with stage(1):
        metadata.digest(inventory_sha256)
        metadata.digest(source_acceptance_sha256)
        selector(mode)
        index, witness, acceptance = load(inventory_raw), load(witness_raw), load(acceptance_raw)
        metadata.validate_inventory(index)
        validate_witness(witness)
        validate_acceptance(acceptance)
        output = load(output_raw) if output_raw is not None else None
        if output is not None:
            validate_output(output)
        unique_index(witness["artifacts"], "artifact_id")
        unique_index(acceptance["artifacts"], "artifact_id")
        unique_index(witness["events"], "event_id")
        unique_index(witness["object_observations"], "observation_id")
    with stage(2, SOURCE_UNACCEPTED):
        require(metadata.sha(acceptance_raw) == source_acceptance_sha256)
    with stage(2):
        require(metadata.sha(inventory_raw) == inventory_sha256)
    raw = {"inventory": inventory_raw, "witness": witness_raw, "acceptance": acceptance_raw}
    with stage(3):
        artifacts, accepted, events, objects = validate_cross_files(index, witness, acceptance, output, raw)
    with stage(4, SOURCE_UNACCEPTED):
        repository_map = validate_authority(index, witness, acceptance, output, mode, artifacts, accepted)
    with stage(5, BINDING_MISMATCH):
        bindings = validate_bindings(index, witness, repository_map, events, objects)
        entries = compute_entries(index, witness, accepted, artifacts, events, bindings, repository_map)
    with stage(1):  # Output size/serialization failures have the public input-error code.
        payload = bounded_canonical({
            **{key: witness[key] for key in common}, "purpose": "publication_clock_reconciliation",
            "witness_sha256": metadata.sha(witness_raw), "source_acceptance_sha256": metadata.sha(acceptance_raw), "entries": entries,
        })
    with stage(5, BINDING_MISMATCH):
        require(output_raw is None or output_raw == payload)
    return payload


def read_input(path):
    path = Path(path)
    return metadata.read_relative(path.parent, path.name, MAX_BYTES)


def publish(output, payload):
    """Exclusive atomic publication relative to one opened private directory."""
    require(type(payload) is bytes and len(payload) <= MAX_BYTES)
    path = Path(output)
    require(path.name not in ("", ".", ".."))
    temporary = ".reconciliation-" + uuid.uuid4().hex
    directory = os.open(path.parent, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
    created, linked, cleanup_pending = False, False, False
    try:
        info = os.fstat(directory)
        require(stat.S_ISDIR(info.st_mode) and info.st_uid == os.geteuid() and info.st_mode & 0o077 == 0)
        fd = os.open(temporary, os.O_WRONLY | os.O_CREAT | os.O_EXCL | os.O_NOFOLLOW, 0o600, dir_fd=directory)
        created = True
        with os.fdopen(fd, "wb") as stream:
            stream.write(payload)
            stream.flush()
            os.fsync(stream.fileno())
        os.link(temporary, path.name, src_dir_fd=directory, dst_dir_fd=directory, follow_symlinks=False)
        linked = True
    finally:
        try:
            if created:
                try:
                    os.unlink(temporary, dir_fd=directory)
                except OSError:
                    if not linked:
                        raise
                    cleanup_pending = True
            if linked:
                os.fsync(directory)
        finally:
            os.close(directory)
    return cleanup_pending


class Parser(argparse.ArgumentParser):
    def error(self, _message):
        raise ReconciliationError(INPUT_INVALID, 1)


def main(argv=None):
    parser = Parser(description=__doc__, allow_abbrev=False)
    parser.add_argument("--mode", required=True)
    for name in ("inventory", "witness", "source-acceptance", "output"):
        parser.add_argument("--" + name, type=Path, required=True)
    parser.add_argument("--inventory-sha256", required=True)
    parser.add_argument("--source-acceptance-sha256", required=True)
    parser.add_argument("--verify-output", type=Path, help="also verify exact canonical bytes from an independent reader")
    try:
        args = parser.parse_args(argv)
        with stage(1):
            inventory_raw, witness_raw, acceptance_raw = (read_input(p) for p in (args.inventory, args.witness, args.source_acceptance))
            output_raw = read_input(args.verify_output) if args.verify_output is not None else None
        payload = reconcile(inventory_raw, witness_raw, acceptance_raw, inventory_sha256=args.inventory_sha256,
                            source_acceptance_sha256=args.source_acceptance_sha256, mode=args.mode, output_raw=output_raw)
        with stage(1):
            cleanup_pending = publish(args.output, payload)
    except ReconciliationError as error:
        print(error.code, file=sys.stderr)
        return 1
    print("reconciliation published; temporary cleanup pending" if cleanup_pending else
          "reconciliation published; no source acquisition or replay action")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
