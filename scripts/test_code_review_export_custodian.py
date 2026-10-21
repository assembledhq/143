"""Synthetic archives only. These tests never discover or open operator archives."""
import base64
import copy
from datetime import datetime, timedelta, timezone
from concurrent.futures import ThreadPoolExecutor
import json
from pathlib import Path
import os
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch
import uuid
import zipfile

import code_review_export_metadata as metadata
import code_review_export_custodian as custodian
from code_review_export_metadata import Document, InvalidCustody, canonical, inventory, load, locator_key, sha, validate_inventory
from code_review_export_selection import RULESET, assignments, validate_selection

PROTOCOL = b'''protocol_version = 3
[replay]
decision_version = 2
status = "approved"
scope = "development_calibration"
holdout_access = "disabled"
unresolved_membership = "quarantine"
conflicting_snapshots = "quarantine"
unknown_evidence = "conservative_bounds"
overall_verdict_requires_publication_cohort = true
[window]
confirmed = true
outcome_observation_days = 14
timezone = "UTC"
review_start = 2026-07-15
review_end = 2026-10-18
[split]
unit = "pull_request"
grouping = "stack"
order = "first_review_publication_time"
model_reservation = "latest_whole_groups_first"
fraction_denominator = "remaining_prs_after_model_reservation"
boundary_rounding = "whole_groups_at_or_before_target"
tie_break = "repository_then_lowest_pr"
unknown_stack_membership = "quarantine"
boundary_overlap = "quarantine_publication_overlap"
embargo_basis = "outcome_observation_completion"
embargo_refill = false
outcome_label_cutoff = "full_window_before_next_partition_start"
outcome_embargo_days = 14
model_holdout_fraction = 0.20
development_fraction = 0.60
calibration_fraction = 0.20
review_holdout_fraction = 0.20
'''
SENTINEL = 'SENTINEL_RESULT_opaque_"[\\\\]\\n☃'


def uid(number):
    return str(uuid.UUID(int=number))


def source_record(number):
    clock = (datetime(2026, 7, 15, tzinfo=timezone.utc) + timedelta(days=7 * (number - 1))).isoformat()
    return {"source_identity": "assessment:" + uid(3000 + number), "raw": {
        "kind": "assessment", "id": uid(3000 + number),
        "source": {"id": uid(3000 + number), "repository_id": uid(100), "repository_full_name": "example/repository",
                   "pull_request_id": uid(1000 + number), "session_id": uid(2000 + number), "generation": 1,
                   "head_sha": "a" * 40, "base_sha": "b" * 40, "base_ref": "main",
                   "github_review_id": number, "publication_key": f"synthetic-publication-{number}",
                   "created_at": clock, "completed_at": clock, "decision": SENTINEL,
                   "risk_reason_details": [{"body": SENTINEL}], "structured_outcome": {"body": SENTINEL}},
        "pull_request": {"id": uid(1000 + number), "number": number, "repository": "example/repository", "status": SENTINEL},
        "publication_time": clock, "publication_time_provenance": SENTINEL,
        "agent_results": [{"structured_result": SENTINEL}], "policy": {"prompt": SENTINEL},
        "provider_observations": [{"body": SENTINEL}], "legacy_aliases": []},
        "evidence": {"decision": SENTINEL}, "safeguards": [{"result": SENTINEL}]}


def fixture(root, batches=None):
    if batches is None:
        batches = [[source_record(i) for i in range(1, 11)]]
    catalog = {"schema_version": 1, "acquisitions": []}
    for batch_index, records in enumerate(batches):
        directory = root / f"acquisition-{batch_index}"
        directory.mkdir()
        a = {"id": uid(10 + batch_index), "schema_version": 1,
             "filters": {"repository_id": uid(100), "from": "2026-07-15T00:00:00Z", "until": "2026-10-19T00:00:00Z", "time_basis": "created_or_completed", "include_unresolved_publication_time": True},
             "acquired_at": "2026-10-20T12:00:00Z", "expires_at": "2026-10-21T12:00:00Z",
             "counts": {"records": len(records), "incomplete_substantive_evidence": 7},
             "consistency": "frozen_payloads_repeatable_read"}
        chunks = [records[i:i + 100] for i in range(0, len(records), 100)] or [[]]
        entries, cursor, position = [], None, 0
        for page_index, chunk in enumerate(chunks):
            position += len(chunk)
            complete = position == len(records)
            next_cursor = None if complete else base64.urlsafe_b64encode(canonical({"acquisition_id": a["id"], "position": position})).decode().rstrip("=")
            page = {"acquisition": a, "data": chunk, "meta": {"returned": len(chunk), "position": position, "complete": complete, "next_cursor": next_cursor}}
            raw = json.dumps(page, ensure_ascii=False, indent=2).encode()
            name = f"page-{page_index}.json"
            (directory / name).write_bytes(raw)
            entries.append({"file": name, "sha256": sha(raw), "request_cursor": cursor})
            cursor = next_cursor
        manifest_raw = canonical({"version": 1, "acquisition": a, "pages": entries}) + b"\n"
        (directory / "archive-manifest.json").write_bytes(manifest_raw)
        sums_raw = "".join(e["sha256"] + "  " + e["file"] + "\n" for e in entries).encode() + (sha(manifest_raw) + "  archive-manifest.json\n").encode()
        (directory / "SHA256SUMS").write_bytes(sums_raw)
        catalog["acquisitions"].append({"manifest": f"acquisition-{batch_index}/archive-manifest.json", "manifest_sha256": sha(manifest_raw), "checksums": f"acquisition-{batch_index}/SHA256SUMS", "checksums_sha256": sha(sums_raw)})
    original_raw = canonical({"format": "synthetic-original-catalog", "manifests": [e["manifest_sha256"] for e in catalog["acquisitions"]]}) + b"\n"
    (root / "original-catalog.json").write_bytes(original_raw)
    catalog["original_catalog"] = {"file": "original-catalog.json", "sha256": sha(original_raw)}
    raw = canonical(catalog) + b"\n"
    (root / "catalog.json").write_bytes(raw)
    return raw


def graph_fixture(count=10):
    return {"schema_version": 1, "groups": [
        {"repository_id": uid(100), "stack_id": f"verified-synthetic-{i}", "pull_request_ids": [uid(1000 + i)], "complete": True,
         "provenance": {"availability": "recorded", "kind": "historical_graphite_export", "effective_at": "2026-07-14T00:00:00Z", "observed_at": "2026-10-20T00:00:00Z", "source_sha256": "f" * 64}}
        for i in range(1, count + 1)]}


def documents(index, partitions=("development", "calibration"), graph=None):
    index_raw = canonical(index) + b"\n"
    graph = graph if graph is not None else graph_fixture()
    graph_raw = canonical(graph) + b"\n"
    common = {"schema_version": 1, "ruleset": RULESET, "catalog_sha256": index["catalog_sha256"],
              "inventory_sha256": sha(index_raw), "stack_graph_sha256": sha(graph_raw), "protocol_sha256": sha(PROTOCOL)}
    # Expected 10-PR fixture: 4 / 2 / 2 / 2. Never generated using the validator.
    labels = {i: "development" if i <= 4 else "calibration" if i <= 6 else "review_holdout" if i <= 8 else "model_holdout" for i in range(1, 11)}
    entries = [{"locator": row["locator"], "partition": labels[row["pr_number"]]} for row in index["observations"]]
    plan_raw = canonical(common | {"assignments": entries}) + b"\n"
    selection = common | {"partition_sha256": sha(plan_raw), "partitions": list(partitions), "selected": [e for e in entries if e["partition"] in partitions]}
    selection_raw = canonical(selection) + b"\n"
    return index_raw, graph_raw, PROTOCOL, plan_raw, selection_raw


def write_synthetic_fixture(destination):
    """Create a new invented golden archive and all binding metadata files.

    Never discovers/reads an operator archive. Existing destinations are refused.
    Expected ten-PR partition sizes: 4/2/2/2; one extra identical observation of
    PR 1 remains independent, so selected development+calibration contains seven
    original records. The caller may materialize only this invented fixture.
    """
    root = Path(destination)
    root.mkdir(mode=0o700)
    catalog = fixture(root, [[source_record(i) for i in range(1, 11)], [source_record(1)]])
    index = inventory(root, catalog)
    values = documents(index)
    for name, raw in zip(("inventory.json", "graph.json", "protocol.toml", "partition.json", "selection.json"), values):
        (root / name).write_bytes(raw)
        (root / name).chmod(0o600)
    return {"catalog_sha256": sha(catalog), "selection_sha256": sha(values[-1]),
            "expected_partitions": {"development": 5, "calibration": 2, "review_holdout": 2, "model_holdout": 2},
            "expected_selected_observations": 7}


class InventoryTest(unittest.TestCase):
    def test_allowlist_exact_fingerprints_and_unknowns(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            record = source_record(1)
            record["raw"]["legacy_aliases"] = [{"id": uid(9001), "body": SENTINEL, "decision": SENTINEL}]
            catalog = fixture(root, [[record]])
            index = inventory(root, catalog)
            validate_inventory(index)
            row = index["observations"][0]
            self.assertEqual(metadata.OBSERVATION_FIELDS, set(row))
            self.assertNotIn("SENTINEL_RESULT", canonical(index).decode())
            self.assertNotIn("incomplete_substantive_evidence", canonical(index).decode())
            self.assertEqual([uid(9001)], row["legacy_alias_ids"])
            self.assertEqual("unavailable", row["stack_availability"])
            doc = Document((root / "acquisition-0/page-0.json").read_bytes())
            span = doc.items(doc.members()["data"])[0]
            self.assertEqual(sha(doc.bytes(span)), row["record_sha256"])
            raw_source = doc.members(doc.members(span)["raw"])["source"]
            self.assertEqual(sha(doc.bytes(raw_source)), row["source_sha256"])
            self.assertEqual("reconstructed", row["publication_time_availability"])
            self.assertEqual(0, row["locator"]["record_index"])

    def test_legacy_and_missing_fields_remain_unknown(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            record = source_record(1)
            record["source_identity"] = "legacy:" + uid(3001)
            record["raw"]["kind"] = "legacy"
            record["raw"]["source"] = {"id": uid(3001), "repository_id": uid(100), "pull_request_id": uid(1001), "review_output_key": "synthetic-legacy", "decision": SENTINEL}
            record["raw"]["publication_time"] = None
            record["raw"]["source"]["github_review_id"] = 0
            record["raw"]["pull_request"]["number"] = 0
            index = inventory(root, fixture(root, [[record]]))
            row = index["observations"][0]
            for key in ("head_sha", "base_sha", "base_ref", "generation", "completed_at", "publication_time", "provider_review_id", "pr_number"):
                self.assertIsNone(row[key], key)
            self.assertEqual("synthetic-legacy", row["publication_key"])
            self.assertEqual("reconstructed", row["repository_name_availability"])
            self.assertEqual("unavailable", row["publication_time_availability"])

    def test_repeated_identity_preserves_independent_observations(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            record = source_record(1)
            index = inventory(root, fixture(root, [[record], [copy.deepcopy(record)]]))
            self.assertEqual(2, len(index["observations"]))
            self.assertNotEqual(index["observations"][0]["locator"], index["observations"][1]["locator"])
            self.assertEqual(index["observations"][0]["record_sha256"], index["observations"][1]["record_sha256"])
            validate_inventory(index)

    def test_bad_json_framing_fails_closed(self):
        for raw in (b'{"a":1,"a":2}', b'{"x":{"a":1,"a":2}}', b'{"x":[1,]}', b'{"x":NaN}', b'{"x":01}', b'{"x":"\\q"}', b'{"x":"\x01"}', b'[true]more', b'[' * 66 + b'0' + b']' * 66, b'"\\u12"', b'\xff'):
            with self.subTest(raw=raw[:15]), self.assertRaises((InvalidCustody, UnicodeError)):
                Document(raw)

    def test_json_opaque_strings_are_not_decoded(self):
        doc = Document(b'{"body":"\\ud800\\n\\\"}[]\\t","value":-1.25e+2,"ok":true}')
        self.assertEqual(-125.0, doc.scalar(doc.members(), "value"))
        self.assertEqual(True, doc.scalar(doc.members(), "ok"))

    def test_archive_integrity_and_bounds(self):
        mutations = {
            "page checksum": lambda root: (root / "acquisition-0/page-0.json").write_bytes(b'{}'),
            "manifest checksum": lambda root: (root / "acquisition-0/archive-manifest.json").write_bytes(b'{}'),
            "checksum inventory": lambda root: (root / "acquisition-0/SHA256SUMS").write_text(""),
            "original catalog changed": lambda root: (root / "original-catalog.json").write_bytes(b"changed"),
            "missing page": lambda root: (root / "acquisition-0/page-0.json").unlink(),
        }
        for name, mutation in mutations.items():
            with self.subTest(name=name), tempfile.TemporaryDirectory() as temp:
                root = Path(temp)
                catalog = fixture(root)
                mutation(root)
                with self.assertRaises((InvalidCustody, OSError)):
                    inventory(root, catalog)
        for constant in ("MAX_PAGE", "MAX_TOTAL_BYTES", "MAX_RECORDS"):
            with self.subTest(constant=constant), tempfile.TemporaryDirectory() as temp:
                root = Path(temp)
                catalog = fixture(root)
                with patch.object(metadata, constant, 1), self.assertRaises(InvalidCustody):
                    inventory(root, catalog)

    def test_symlink_and_traversal_inputs_rejected(self):
        for component in ("file", "directory", "traversal", "hardlink"):
            with self.subTest(component=component), tempfile.TemporaryDirectory() as temp:
                root = Path(temp)
                catalog = fixture(root)
                page = root / "acquisition-0/page-0.json"
                if component == "file":
                    page.rename(page.with_suffix(".secret"))
                    page.symlink_to(page.with_suffix(".secret"))
                elif component == "hardlink":
                    os.link(page, page.with_suffix(".other"))
                elif component == "directory":
                    directory = root / "acquisition-0"
                    directory.rename(root / "hidden")
                    directory.symlink_to(root / "hidden", target_is_directory=True)
                else:
                    value = load(catalog)
                    value["acquisitions"][0]["manifest"] = "../private-manifest.json"
                    catalog = canonical(value)
                with self.assertRaises((InvalidCustody, OSError)):
                    inventory(root, catalog)

    def test_lossless_pagination_and_empty_acquisitions(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            records = [source_record(i % 10 + 1) for i in range(101)]
            for i, row in enumerate(records):
                row["raw"]["id"] = row["raw"]["source"]["id"] = uid(5000 + i)
                row["source_identity"] = "assessment:" + uid(5000 + i)
            index = inventory(root, fixture(root, [records, []]))
            self.assertEqual(101, len(index["observations"]))
            self.assertEqual(2, len(index["acquisitions"]))
            self.assertEqual({0, 99}, {index["observations"][0]["locator"]["record_index"], max(r["locator"]["record_index"] for r in index["observations"])})

    def test_source_scope_and_metadata_cannot_contain_nested_outcomes(self):
        cases = ("foreign repository", "different source ID", "nested head", "unknown filter", "impossible clock", "submicrosecond clock")
        for case in cases:
            with self.subTest(case=case), tempfile.TemporaryDirectory() as temp:
                root = Path(temp)
                record = source_record(1)
                if case == "foreign repository": record["raw"]["source"]["repository_id"] = uid(9999)
                if case == "different source ID": record["raw"]["source"]["id"] = uid(9999)
                if case == "nested head": record["raw"]["source"]["head_sha"] = {"body": SENTINEL}
                if case == "impossible clock": record["raw"]["publication_time"] = "2026-07-14T00:00:00Z"
                if case == "submicrosecond clock": record["raw"]["publication_time"] = "2026-07-15T00:00:00.000000001Z"
                raw = fixture(root, [[record]])
                if case == "unknown filter":
                    # Re-pin a malformed manifest to show schema rejects unknown fields
                    # independently of outer checksum validation.
                    catalog = load(raw)
                    entry = catalog["acquisitions"][0]
                    manifest = load((root / entry["manifest"]).read_bytes())
                    manifest["acquisition"]["filters"]["body"] = SENTINEL
                    changed = canonical(manifest)
                    (root / entry["manifest"]).write_bytes(changed)
                    entry["manifest_sha256"] = sha(changed)
                    raw = canonical(catalog)
                with self.assertRaises(InvalidCustody): inventory(root, raw)

    def test_duplicate_physical_row_cannot_claim_another_identity(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            index = inventory(root, fixture(root))
            extra = copy.deepcopy(index["observations"][0])
            extra["source_id"] = uid(9901)
            extra["locator"]["source_identity"] = "assessment:" + uid(9901)
            index["observations"].append(extra)
            with self.assertRaises(InvalidCustody): validate_inventory(index)


class SelectionTest(unittest.TestCase):
    def test_v3_keeps_review_rows_inside_14_day_outcome_cutoff(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            index = inventory(root, fixture(root))
            values = assignments(index, graph_fixture(), PROTOCOL)
            expected = {locator_key(row): "development" if row["pr_number"] <= 4 else "calibration" if row["pr_number"] <= 6 else "review_holdout" if row["pr_number"] <= 8 else "model_holdout" for row in index["observations"]}
            self.assertEqual(expected, values)
            # Last development review is only seven days before calibration.
            self.assertEqual(4, sum(value == "development" for value in values.values()))

    def test_unknown_conflicting_and_shared_provider_metadata_quarantines(self):
        for mutation in ("publication", "head", "stack", "component", "conflict", "provider"):
            with self.subTest(mutation=mutation), tempfile.TemporaryDirectory() as temp:
                root = Path(temp)
                records = [source_record(i) for i in range(1, 11)]
                batches, graph = [records], graph_fixture()
                if mutation == "publication": records[0]["raw"]["publication_time"] = None
                if mutation == "head": records[0]["raw"]["source"]["head_sha"] = ""
                if mutation == "stack": graph["groups"].pop(0)
                if mutation == "component": graph["groups"][0]["pull_request_ids"].append(uid(9999))
                if mutation == "provider": records[1]["raw"]["source"]["github_review_id"] = 1
                if mutation == "conflict":
                    extra = copy.deepcopy(records[0]); extra["raw"]["source"]["decision"] = "another opaque result"
                    batches.append([extra])
                index = inventory(root, fixture(root, batches))
                values = assignments(index, graph, PROTOCOL)
                self.assertEqual({"quarantine"}, {values[locator_key(r)] for r in index["observations"] if r["pr_number"] == 1})
                if mutation == "provider":
                    self.assertEqual({"quarantine"}, {values[locator_key(r)] for r in index["observations"] if r["pr_number"] == 2})

    def test_actual_publication_overlap_quarantines_whole_stack(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            records = [source_record(i) for i in range(1, 11)]
            extra = source_record(1)
            extra["raw"]["id"] = extra["raw"]["source"]["id"] = uid(8001)
            extra["source_identity"] = "assessment:" + uid(8001)
            extra["raw"]["source"]["github_review_id"] = 101
            extra["raw"]["publication_time"] = source_record(5)["raw"]["publication_time"]
            records.append(extra)
            index = inventory(root, fixture(root, [records]))
            values = assignments(index, graph_fixture(), PROTOCOL)
            self.assertEqual({"quarantine"}, {values[locator_key(r)] for r in index["observations"] if r["pr_number"] == 1})
            self.assertEqual({"calibration"}, {values[locator_key(r)] for r in index["observations"] if r["pr_number"] == 5})

    def test_duplicate_or_unproven_graph_membership_rejected(self):
        for mutation in ("duplicate", "incomplete", "current", "unknown key", "boundary"):
            with self.subTest(mutation=mutation), tempfile.TemporaryDirectory() as temp:
                root = Path(temp)
                index = inventory(root, fixture(root))
                graph = graph_fixture()
                if mutation == "duplicate": graph["groups"][1]["pull_request_ids"].append(uid(1001))
                if mutation == "incomplete": graph["groups"][0]["complete"] = False
                if mutation == "current": graph["groups"][0]["provenance"]["kind"] = "current_changeset"
                if mutation == "unknown key": graph["groups"][0]["body"] = SENTINEL
                if mutation == "boundary": graph["groups"][0]["provenance"]["effective_at"] = "2027-01-01T00:00:00Z"
                with self.assertRaises(InvalidCustody): assignments(index, graph, PROTOCOL)

    def test_caller_label_cannot_authorize_holdouts_or_tampered_plans(self):
        for mutation in ("label only", "review holdout", "model holdout", "tamper plan", "duplicate", "digest", "extra field", "old protocol"):
            with self.subTest(mutation=mutation), tempfile.TemporaryDirectory() as temp:
                root = Path(temp)
                index = inventory(root, fixture(root))
                index_raw, graph_raw, protocol_raw, plan_raw, selection_raw = documents(index)
                selection, plan = load(selection_raw), load(plan_raw)
                if mutation in ("label only", "review holdout", "model holdout"):
                    row = next(r for r in index["observations"] if r["pr_number"] == (7 if mutation != "model holdout" else 9))
                    selection["selected"] = [{"locator": row["locator"], "partition": "development"}]
                if mutation == "tamper plan":
                    plan["assignments"][0]["partition"] = "calibration"
                    plan_raw = canonical(plan); selection["partition_sha256"] = sha(plan_raw)
                if mutation == "duplicate": selection["selected"].append(selection["selected"][0])
                if mutation == "digest": selection["inventory_sha256"] = "f" * 64
                if mutation == "extra field": selection["decision"] = SENTINEL
                if mutation == "old protocol": protocol_raw = protocol_raw.replace(b"protocol_version = 3", b"protocol_version = 2")
                with self.assertRaises(InvalidCustody):
                    validate_selection(index, index_raw, load(graph_raw), graph_raw, protocol_raw, plan, plan_raw, selection)

    def test_complete_requested_open_partitions_only(self):
        for partitions, expected_count in ((("development",), 4), (("calibration",), 2), (("development", "calibration"), 6)):
            with self.subTest(partitions=partitions), tempfile.TemporaryDirectory() as temp:
                root = Path(temp)
                index = inventory(root, fixture(root))
                index_raw, graph_raw, protocol_raw, plan_raw, selection_raw = documents(index, partitions=partitions)
                selected = validate_selection(index, index_raw, load(graph_raw), graph_raw, protocol_raw, load(plan_raw), plan_raw, load(selection_raw))
                self.assertEqual(expected_count, len(selected))
                incomplete = load(selection_raw)
                incomplete["selected"].pop()
                with self.assertRaises(InvalidCustody):
                    validate_selection(index, index_raw, load(graph_raw), graph_raw, protocol_raw, load(plan_raw), plan_raw, incomplete)

    def test_protocol_exact_types_and_approved_prerequisites(self):
        mutations = (
            (b'unknown_evidence = "conservative_bounds"', b'unknown_evidence = "optimistic"'),
            (b'overall_verdict_requires_publication_cohort = true', b'overall_verdict_requires_publication_cohort = false'),
            (b'outcome_observation_days = 14', b'outcome_observation_days = 14.0'),
            (b'outcome_embargo_days = 14', b'outcome_embargo_days = true'),
            (b'model_holdout_fraction = 0.20', b'model_holdout_fraction = "0.20"'),
            (b'protocol_version = 3', b'protocol_version = true'),
            (b'decision_version = 2', b'decision_version = true'),
            (b'review_end = 2026-10-18', b'review_end = 2026-10-17'),
        )
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            index = inventory(root, fixture(root))
            for before, after in mutations:
                with self.subTest(before=before), self.assertRaises(InvalidCustody):
                    assignments(index, graph_fixture(), PROTOCOL.replace(before, after))

    def test_alias_connectivity_does_not_split_related_prs(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            records = [source_record(i) for i in range(1, 11)]
            for record in (records[0], records[8]):
                record["raw"]["legacy_aliases"] = [{"id": uid(9001), "body": SENTINEL}]
            index = inventory(root, fixture(root, [records]))
            actual = assignments(index, graph_fixture(), PROTOCOL)
            self.assertEqual({"quarantine"}, {actual[locator_key(r)] for r in index["observations"] if r["pr_number"] in (1, 9)})

    def test_publication_boundary_and_unknown_coverage_do_not_invent_membership(self):
        for clock in ("2026-07-14T23:59:59.999999Z", "2026-10-19T00:00:00Z", None):
            with self.subTest(clock=clock), tempfile.TemporaryDirectory() as temp:
                root = Path(temp)
                records = [source_record(i) for i in range(1, 11)]
                records[0]["raw"]["publication_time"] = clock
                if clock is not None and clock.startswith("2026-07-14"):
                    records[0]["raw"]["source"]["created_at"] = clock
                index = inventory(root, fixture(root, [records]))
                actual = assignments(index, graph_fixture(), PROTOCOL)
                self.assertEqual("quarantine", next(actual[locator_key(r)] for r in index["observations"] if r["pr_number"] == 1))


class MaterializeTest(unittest.TestCase):
    def test_exact_raw_outputs_and_receipt_no_unselected_records(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            batches = [[source_record(i) for i in range(1, 11)], [source_record(1)]]
            catalog = fixture(root, batches)
            index = inventory(root, catalog)
            args = documents(index)
            originals = {str(p.relative_to(root)): sha(p.read_bytes()) for p in root.rglob("*") if p.is_file()}
            output = root / "selected.zip"
            custodian.materialize(root, catalog, *args, sha(args[-1]), output)
            with zipfile.ZipFile(output) as bundle:
                receipt = load(bundle.read("receipt.json"))
                self.assertEqual(7, len(receipt["observations"]))
                self.assertEqual({"receipt.json"} | {r["member"] for r in receipt["observations"]}, set(bundle.namelist()))
                self.assertEqual({1, 2, 3, 4, 5, 6}, {next(r["pr_number"] for r in index["observations"] if r["locator"] == o["locator"]) for o in receipt["observations"]})
                for entry in receipt["observations"]:
                    payload = bundle.read(entry["member"])
                    self.assertEqual(entry["record_sha256"], sha(payload))
                    self.assertEqual(entry["record_bytes"], len(payload))
                    self.assertEqual(entry["locator"]["source_identity"], load(payload)["source_identity"])
                self.assertEqual(sha(args[-1]), receipt["selection_sha256"])
                self.assertEqual(0o600, output.stat().st_mode & 0o777)
            output2 = root / "selected-again.zip"
            custodian.materialize(root, catalog, *args, sha(args[-1]), output2)
            self.assertEqual(output.read_bytes(), output2.read_bytes())
            self.assertEqual(originals, {name: sha((root / name).read_bytes()) for name in originals})

    def test_selection_validation_precedes_page_reads(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            catalog = fixture(root)
            index = inventory(root, catalog)
            args = list(documents(index))
            invalid = load(args[-1])
            invalid["selected"] = [{"locator": next(r["locator"] for r in index["observations"] if r["pr_number"] == 9), "partition": "development"}]
            args[-1] = canonical(invalid)
            original_read = custodian.read_relative
            pages = []
            def tracked_read(root, name, bound):
                if "page-" in name:
                    pages.append(name)
                return original_read(root, name, bound)
            with patch.object(custodian, "read_relative", side_effect=tracked_read), self.assertRaises(InvalidCustody):
                custodian.materialize(root, catalog, *args, sha(args[-1]), root / "not-published.zip")
            self.assertEqual([], pages)
            self.assertFalse((root / "not-published.zip").exists())

    def test_partial_provenance_selection_and_tampered_inventory_fail(self):
        for mutation in ("missing observation", "lying metadata", "unbound manifest"):
            with self.subTest(mutation=mutation), tempfile.TemporaryDirectory() as temp:
                root = Path(temp)
                catalog = fixture(root, [[source_record(i) for i in range(1, 11)], [source_record(1)]])
                index = inventory(root, catalog)
                if mutation == "lying metadata": index["observations"][0]["head_sha"] = "c" * 40
                if mutation == "unbound manifest": index["acquisitions"][0]["acquired_at"] = "2026-10-20T13:00:00Z"
                args = list(documents(index))
                if mutation == "missing observation":
                    value = load(args[-1]); value["selected"].pop(0); args[-1] = canonical(value)
                with self.assertRaises(InvalidCustody): custodian.materialize(root, catalog, *args, sha(args[-1]), root / "absent.zip")
                self.assertFalse((root / "absent.zip").exists())
                self.assertEqual([], list(root.glob(".custodian-*")))

    def test_checksum_change_size_failures_and_existing_outputs_are_atomic(self):
        for mutation in ("page changed", "output bound", "existing file", "symlink", "wrong selection digest"):
            with self.subTest(mutation=mutation), tempfile.TemporaryDirectory() as temp:
                root = Path(temp)
                catalog = fixture(root)
                args = documents(inventory(root, catalog))
                output = root / "selected.zip"
                if mutation == "page changed": (root / "acquisition-0/page-0.json").write_bytes(b'{}')
                if mutation == "existing file": output.write_bytes(b"keep existing evidence")
                if mutation == "symlink": output.symlink_to(root / "not-created")
                with patch.object(custodian, "MAX_BUNDLE", 1 if mutation == "output bound" else custodian.MAX_BUNDLE):
                    with self.assertRaises((InvalidCustody, FileExistsError)):
                        custodian.materialize(root, catalog, *args, "f" * 64 if mutation == "wrong selection digest" else sha(args[-1]), output)
                if mutation == "existing file": self.assertEqual(b"keep existing evidence", output.read_bytes())
                elif mutation == "symlink": self.assertTrue(output.is_symlink()); self.assertFalse((root / "not-created").exists())
                else: self.assertFalse(output.exists())
                self.assertEqual([], list(root.glob(".custodian-*")))

    def test_cli_errors_do_not_disclose_input_payloads_or_paths(self):
        script = Path(__file__).with_name("code_review_export_custodian.py")
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            bad = root / "private-catalog-SENTINEL.json"
            bad.write_bytes(b'{"secret":"SENTINEL_RESULT"}')
            result = subprocess.run([sys.executable, "-B", str(script), "inventory", "--archive-root", str(root), "--catalog", str(bad), "--catalog-sha256", sha(bad.read_bytes()), "--output", str(root / "absent.json"), "--acknowledge-mixed-page-read"], capture_output=True, text=True, check=False)
            self.assertEqual(1, result.returncode)
            self.assertEqual("", result.stdout)
            self.assertEqual("custodian operation failed; no output published\n", result.stderr)
            self.assertFalse((root / "absent.json").exists())

    def test_golden_fixture_cli_roundtrip(self):
        script = Path(__file__).with_name("code_review_export_custodian.py")
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp) / "invented-golden"
            expected = write_synthetic_fixture(root)
            inventory_path = root / "cli-inventory.json"
            common = [sys.executable, "-B", str(script)]
            catalog_args = ["--archive-root", str(root), "--catalog", str(root / "catalog.json"), "--catalog-sha256", expected["catalog_sha256"], "--acknowledge-mixed-page-read"]
            first = subprocess.run(common + ["inventory", *catalog_args, "--output", str(inventory_path)], capture_output=True, text=True, check=False)
            self.assertEqual(0, first.returncode, first.stderr)
            self.assertEqual((root / "inventory.json").read_bytes(), inventory_path.read_bytes())
            args = ["materialize", *catalog_args, "--output", str(root / "selected.zip"), "--selection-sha256", expected["selection_sha256"]]
            for name, extension in (("inventory", "json"), ("graph", "json"), ("protocol", "toml"), ("partition", "json"), ("selection", "json")):
                args.extend(["--" + name, str(root / (name + "." + extension))])
            result = subprocess.run(common + args, capture_output=True, text=True, check=False)
            self.assertEqual(0, result.returncode, result.stderr)
            with zipfile.ZipFile(root / "selected.zip") as bundle:
                receipt = load(bundle.read("receipt.json"))
                self.assertEqual(expected["expected_selected_observations"], len(receipt["observations"]))
                self.assertEqual(["development", "calibration"], receipt["partitions"])

    def test_concurrent_publish_has_exactly_one_complete_winner(self):
        with tempfile.TemporaryDirectory() as temp:
            root = Path(temp)
            catalog = fixture(root)
            args = documents(inventory(root, catalog))
            output = root / "selected.zip"
            with ThreadPoolExecutor(max_workers=2) as pool:
                futures = [pool.submit(custodian.materialize, root, catalog, *args, sha(args[-1]), output) for _ in range(2)]
                errors = [future.exception() for future in futures]
            self.assertEqual(1, sum(error is None for error in errors))
            self.assertEqual(1, sum(isinstance(error, FileExistsError) for error in errors))
            with zipfile.ZipFile(output) as bundle:
                self.assertEqual(6, len(load(bundle.read("receipt.json"))["observations"]))
            self.assertEqual([], list(root.glob(".custodian-*")))


if __name__ == "__main__":
    unittest.main()
