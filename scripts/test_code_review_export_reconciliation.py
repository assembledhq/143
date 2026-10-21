"""Independent expected-value tests using frozen invented metadata only."""
from concurrent.futures import ThreadPoolExecutor
from contextlib import redirect_stderr, redirect_stdout
from copy import deepcopy
import hashlib
import io
import json
import os
from pathlib import Path
import stat
import subprocess
import sys
import tempfile
import unittest
from unittest.mock import patch

import code_review_export_reconciliation as producer

FIXTURES = Path(__file__).parent / "testdata/publication_reconciliation_v1"
SCRIPT = Path(__file__).parent / "code_review_export_reconciliation.py"
NAMES = {"inventory": "inventory.json", "witness": "metadata-witness.json", "acceptance": "source-acceptance.json", "output": "expected-reconciliation.json"}


def encode(value):
    return (json.dumps(value, sort_keys=True, separators=(",", ":"), ensure_ascii=True, allow_nan=False) + "\n").encode()


def sha(raw):
    return hashlib.sha256(raw).hexdigest()


def fixtures():
    return {key: json.loads((FIXTURES / name).read_bytes()) for key, name in NAMES.items()}


def repin(values):
    """Synthetic mutation preparation only; never accepts a historical source."""
    for name in ("witness", "acceptance", "output"):
        values[name]["inventory_sha256"] = sha(encode(values["inventory"]))
        for key in ("catalog_sha256", "original_catalog_sha256"):
            values[name][key] = values["inventory"][key]
    for name in ("acceptance", "output"):
        values[name]["witness_sha256"] = sha(encode(values["witness"]))
    values["output"]["source_acceptance_sha256"] = sha(encode(values["acceptance"]))
    return {key: encode(value) for key, value in values.items()}


def invocation(raw):
    return {"mode": "synthetic", "inventory_sha256": sha(raw["inventory"]), "source_acceptance_sha256": sha(raw["acceptance"])}


def run(raw, verify=True, **overrides):
    return producer.reconcile(raw["inventory"], raw["witness"], raw["acceptance"],
                              output_raw=raw["output"] if verify else None, **(invocation(raw) | overrides))


def mutate(values, operation):
    parts = [p.replace("~1", "/").replace("~0", "~") for p in operation["path"][1:].split("/")]
    parent = values[operation["artifact"]]
    for part in parts[:-1]:
        parent = parent[int(part)] if isinstance(parent, list) else parent[part]
    key, kind = parts[-1], operation["op"]
    value = deepcopy(operation.get("value"))
    if isinstance(parent, list):
        if key == "-":
            if kind != "add":
                raise ValueError("invalid synthetic patch")
            parent.append(value)
        elif kind == "remove":
            del parent[int(key)]
        elif kind == "add":
            parent.insert(int(key), value)
        else:
            parent[int(key)] = value
    elif kind == "remove":
        del parent[key]
    else:
        if kind == "replace" and key not in parent:
            raise ValueError("invalid synthetic patch")
        parent[key] = value


def cli_arguments(output):
    manifest = json.loads((FIXTURES / "fixture-manifest.json").read_bytes())
    return ["--mode", "synthetic", "--inventory", str(FIXTURES / NAMES["inventory"]),
            "--witness", str(FIXTURES / NAMES["witness"]), "--source-acceptance", str(FIXTURES / NAMES["acceptance"]),
            "--inventory-sha256", manifest["artifacts"][NAMES["inventory"]],
            "--source-acceptance-sha256", manifest["expected_source_acceptance_sha256"],
            "--output", str(output), "--verify-output", str(FIXTURES / NAMES["output"])]


class ReconciliationTest(unittest.TestCase):
    def test_frozen_expected_entries_and_exact_canonical_output(self):
        raw = {key: (FIXTURES / name).read_bytes() for key, name in NAMES.items()}
        manifest = json.loads((FIXTURES / "fixture-manifest.json").read_bytes())
        for name, expected in manifest["artifacts"].items():
            self.assertEqual(expected, sha((FIXTURES / name).read_bytes()), "agreed invented fixtures must remain unchanged")
        pins = {"mode": "synthetic", "inventory_sha256": manifest["artifacts"]["inventory.json"],
                "source_acceptance_sha256": manifest["expected_source_acceptance_sha256"]}
        actual = producer.reconcile(raw["inventory"], raw["witness"], raw["acceptance"], **pins)
        self.assertEqual(raw["output"], actual, "independently computed bytes must equal the hand-declared contract fixture")
        self.assertEqual(actual, run(raw), "verification must independently recompute all entries")
        entries = {encode(row["locator"]): row for row in json.loads(actual)["entries"]}
        cases = json.loads((FIXTURES / "cases.json").read_bytes())["cases"]
        self.assertEqual(44, len(cases), "all frozen scenarios must be exercised")
        for case in cases:
            with self.subTest(case=case["case"]):
                entry = entries[encode(case["locator"])]
                self.assertEqual((case["expected_disposition"], case["expected_reasons"]),
                                 (entry["disposition"], entry["reasons"]), "scenario expectations come from the agreed fixture, not producer logic")

    def test_all_negative_recipes_and_first_failing_stage(self):
        specification = json.loads((FIXTURES / "negative-cases.json").read_bytes())
        self.assertEqual(49, len(specification["cases"]), "all agreed failure recipes must execute")
        for case in specification["cases"]:
            with self.subTest(case=case["case"]):
                values = fixtures()
                for operation in case["mutations"]:
                    mutate(values, operation)
                repin(values)
                for operation in case["after_refresh_mutations"]:
                    mutate(values, operation)
                raw = {key: encode(value) for key, value in values.items()}
                for operation in case["raw_mutations"]:
                    key = operation["artifact"]
                    if "append" in operation:
                        # Execute the real bounded file reader against a sparse oversized file.
                        with tempfile.TemporaryDirectory() as directory:
                            path = Path(directory) / "invented.json"
                            with path.open("wb") as stream:
                                stream.write(raw[key])
                                stream.truncate(len(raw[key]) + len(operation["append"].encode()) * operation["repeat"])
                            with self.assertRaises(producer.ReconciliationError) as error:
                                with producer.stage(1):
                                    producer.read_input(path)
                            self.assertEqual((case["expected_static_error"], case["expected_stage"]),
                                             (error.exception.code, error.exception.stage), "oversized input must fail before content is read")
                        raw = None
                        break
                    before, after = operation["find"].encode(), operation["replace"].encode()
                    self.assertEqual(1, raw[key].count(before), "raw recipe must target one exact synthetic token")
                    raw[key] = raw[key].replace(before, after, 1)
                if raw is None:
                    continue
                with self.assertRaises(producer.ReconciliationError) as error:
                    run(raw, **case["invocation_overrides"])
                self.assertEqual((case["expected_static_error"], case["expected_stage"]),
                                 (error.exception.code, error.exception.stage), "the first contract stage must decide the static error")
                self.assertEqual(error.exception.code, str(error.exception), "exceptions must expose no input values")

    def test_reordering_inputs_does_not_change_sorted_entries(self):
        values = fixtures()
        for key in ("artifacts", "events", "object_observations", "bindings"):
            values["witness"][key].reverse()
        values["inventory"]["observations"].reverse()
        for binding in values["witness"]["bindings"]:
            binding["event_ids"].reverse()
            binding["object_observation_ids"].reverse()
        raw = repin(values)
        self.assertEqual(raw["output"], run(raw), "input order cannot select a favorable event or change canonical entry ordering")

    def test_individual_acquisition_cutoffs_remain_inclusive(self):
        tests = [
            ("2026-07-15T01:00:00Z", "matched_event", []),
            ("2026-07-15T00:59:59.999999Z", "unresolved", ["after_acquisition_boundary"]),
        ]
        for cutoff, disposition, reasons in tests:
            with self.subTest(cutoff=cutoff):
                values = fixtures()
                row = deepcopy(values["inventory"]["observations"][0])
                values["inventory"]["observations"] = [row]
                acquisition = deepcopy(values["inventory"]["acquisitions"][0])
                acquisition["acquired_at"] = cutoff
                values["inventory"]["acquisitions"] = [acquisition]
                binding = values["witness"]["bindings"][0]
                values["witness"]["bindings"] = [binding]
                values["witness"]["events"] = [e for e in values["witness"]["events"] if e["event_id"] in binding["event_ids"]]
                values["witness"]["object_observations"] = []
                identity = {"provider_repository_id": 700100, "pr_number": row["pr_number"], "provider_review_id": row["provider_review_id"]}
                for artifact in values["acceptance"]["artifacts"]:
                    if artifact["coverage"] is not None:
                        artifact["coverage"]["identities"] = [identity]
                actual = json.loads(run(repin(values), verify=False))["entries"][0]
                expected = deepcopy(values["output"]["entries"][0])
                expected.update(disposition=disposition, reasons=reasons,
                                reconciled_publication_time="2026-07-15T01:00:00.000000Z" if not reasons else None)
                self.assertEqual(expected, actual, "one observation's earlier cutoff must not borrow a later acquisition boundary")

    def test_aggregate_bounds_and_output_size_fail_whole_operation(self):
        raw = {key: (FIXTURES / name).read_bytes() for key, name in NAMES.items()}
        tests = [("MAX_REFERENCES", 1, 3), ("MAX_FACTS", 1, 3)]
        for name, bound, expected_stage in tests:
            with self.subTest(bound=name), patch.object(producer, name, bound):
                with self.assertRaises(producer.ReconciliationError) as error:
                    run(raw)
                self.assertEqual((producer.INPUT_INVALID, expected_stage), (error.exception.code, error.exception.stage), "aggregate limits must fail before authority or binding")
        with patch.object(producer, "MAX_BYTES", 3):
            with self.assertRaises(producer.ReconciliationError) as error:
                with producer.stage(1):
                    producer.bounded_canonical({"entries": ["invented"]})
            self.assertEqual((producer.INPUT_INVALID, 1), (error.exception.code, error.exception.stage), "output growth must fail without truncation")

    def test_reference_expansion_is_bounded_before_missing_bindings(self):
        values = fixtures()
        for binding in values["witness"]["bindings"]:
            binding["event_ids"] = []
            binding["object_observation_ids"] = []
        for artifact in values["acceptance"]["artifacts"]:
            if artifact["coverage"] is not None:
                artifact["coverage"]["identities"] = []
        with patch.object(producer, "MAX_REFERENCES", 1):
            with self.assertRaises(producer.ReconciliationError) as error:
                run(repin(values), verify=False)
        self.assertEqual((producer.INPUT_INVALID, 3), (error.exception.code, error.exception.stage),
                         "derived candidate expansion must be bounded before favorable omission can reach binding checks")

    def test_dangling_references_are_cross_file_errors_for_both_readers(self):
        tests = [("witness", "bindings", "event_ids"), ("witness", "bindings", "object_observation_ids"),
                 ("output", "entries", "event_ids"), ("output", "entries", "object_observation_ids")]
        for artifact, collection, field in tests:
            with self.subTest(artifact=artifact, field=field):
                values = fixtures()
                values[artifact][collection][0][field] = ["invented-dangling-reference"]
                with self.assertRaises(producer.ReconciliationError) as error:
                    run(repin(values))
                self.assertEqual((producer.INPUT_INVALID, 3), (error.exception.code, error.exception.stage),
                                 "dangling source or supplied-output references must fail before authority and binding equality")

    def test_exact_input_bytes_cannot_be_replaced_by_reserialized_hash(self):
        raw = {key: (FIXTURES / name).read_bytes() for key, name in NAMES.items()}
        accepted_inventory_sha = sha(raw["inventory"])
        raw["inventory"] = b" " + raw["inventory"]
        with self.assertRaises(producer.ReconciliationError) as error:
            run(raw, inventory_sha256=accepted_inventory_sha)
        self.assertEqual((producer.INPUT_INVALID, 2), (error.exception.code, error.exception.stage),
                         "trusted pins refer to exact original bytes, including whitespace")

    def test_same_review_number_in_two_repositories_does_not_imply_reuse(self):
        values = fixtures()
        row = deepcopy(values["inventory"]["observations"][0])
        acquisition = deepcopy(values["inventory"]["acquisitions"][0])
        # Fresh invented UUIDs preserve the v1 locator and source-identity rules.
        second_repo = "00000000-0000-0000-0000-000000000066"
        acquisition["id"] = "00000000-0000-0000-0000-00000000000c"
        acquisition["filters"]["repository_id"] = second_repo
        row["repository_id"] = second_repo
        row["source_id"] = "00000000-0000-0000-0000-000000009999"
        row["locator"]["source_identity"] = "assessment:" + row["source_id"]
        row["locator"]["acquisition_id"] = acquisition["id"]
        row["publication_key"] = "invented-distinct-key-in-second-repository"
        values["inventory"]["acquisitions"].append(acquisition)
        values["inventory"]["observations"].append(row)
        values["acceptance"]["repository_bindings"].append({"repository_id": second_repo, "provider_repository_id": 700101})
        fact = deepcopy(values["witness"]["events"][0])
        fact.update(event_id="invented-second-repository-event", provider_repository_id=700101,
                    publication_key_sha256=sha(row["publication_key"].encode()))
        values["witness"]["events"].append(fact)
        binding = deepcopy(values["witness"]["bindings"][0])
        binding.update(locator=row["locator"], event_ids=[fact["event_id"]])
        values["witness"]["bindings"].append(binding)
        for artifact in values["acceptance"]["artifacts"]:
            if artifact["coverage"] is not None:
                artifact["coverage"]["identities"].append({"provider_repository_id": 700101, "pr_number": 1, "provider_review_id": 1})
        result = json.loads(run(repin(values), verify=False))
        original = json.loads((FIXTURES / "expected-reconciliation.json").read_bytes())["entries"]
        expected = deepcopy(original[0])
        expected.update(locator=row["locator"], event_ids=[fact["event_id"]])
        self.assertEqual(original + [expected], result["entries"], "provider review numbers are scoped by repository and cannot contaminate another repository's key evidence")

    def test_failed_write_and_existing_symlink_never_publish_partial_output(self):
        with tempfile.TemporaryDirectory() as directory:
            parent = Path(directory)
            output = parent / "output.json"
            with patch.object(producer.os, "fsync", side_effect=OSError("SYNTHETIC_PRIVATE_ERROR")):
                with self.assertRaises(producer.ReconciliationError) as error:
                    with producer.stage(1):
                        producer.publish(output, b"invented")
            self.assertEqual((producer.INPUT_INVALID, 1), (error.exception.code, error.exception.stage), "write errors must stay static")
            self.assertEqual([], list(parent.iterdir()), "failed write must leave neither a partial output nor a temporary payload")
            output.symlink_to(parent / "absent-target")
            with self.assertRaises(producer.ReconciliationError):
                with producer.stage(1):
                    producer.publish(output, b"invented")
            self.assertTrue(output.is_symlink(), "exclusive publication must preserve even a dangling destination symlink")
            self.assertFalse((parent / "absent-target").exists(), "publication cannot follow a destination symlink")

    def test_noncanonical_supplied_output_cannot_be_accepted(self):
        raw = {key: (FIXTURES / name).read_bytes() for key, name in NAMES.items()}
        raw["output"] = b" " + raw["output"]
        with self.assertRaises(producer.ReconciliationError) as error:
            run(raw)
        self.assertEqual((producer.BINDING_MISMATCH, 5), (error.exception.code, error.exception.stage), "semantic equality alone cannot replace exact canonical output equality")

    def test_grammar_blocks_ambiguous_or_result_bearing_inputs(self):
        tests = [
            lambda v: v["witness"].update(decision="SYNTHETIC_RESULT_SENTINEL"),
            lambda v: v["witness"]["bindings"][0]["event_ids"].append(v["witness"]["bindings"][0]["event_ids"][0]),
            lambda v: v["acceptance"]["operator_attestation"].update(confers_execution_authorization=0),
            lambda v: v["witness"]["bindings"][0].update(source_sha256="x" * 64),
            lambda v: v["inventory"]["observations"][0].update(publication_key="\ud800"),
        ]
        for change in tests:
            with self.subTest(change=change):
                values = fixtures()
                change(values)
                with self.assertRaises(producer.ReconciliationError) as error:
                    run(repin(values), source_acceptance_sha256="0" * 64)
                self.assertEqual((producer.INPUT_INVALID, 1), (error.exception.code, error.exception.stage), "grammar must win over stale acceptance without leaking results")

    def test_canonical_time_from_offsets_and_fractional_events(self):
        tests = [
            ("2026-07-15T03:00:00+02:00", "2026-07-15T01:00:00.000000Z"),
            ("2026-07-15T01:00:00.5Z", "2026-07-15T01:00:00.500000Z"),
            ("2026-07-15T01:00:00.123456000Z", "2026-07-15T01:00:00.123456Z"),
        ]
        for event_time, expected_time in tests:
            with self.subTest(event_time=event_time):
                values = fixtures()
                values["witness"]["events"][0]["event_time"] = event_time
                values["output"]["entries"][0]["reconciled_publication_time"] = expected_time
                raw = repin(values)
                self.assertEqual(raw["output"], run(raw), "canonical recovered times must normalize instants without rounding")

    def test_supplied_output_metadata_uses_earlier_validation_stages(self):
        tests = [
            ("witness_sha256", "0" * 64, producer.INPUT_INVALID, 3),
            ("source_acceptance_sha256", "0" * 64, producer.INPUT_INVALID, 3),
            ("dataset_kind", "historical", producer.SOURCE_UNACCEPTED, 4),
            ("ruleset", "unsupported-rules/v1", producer.SOURCE_UNACCEPTED, 4),
            ("decision", "SYNTHETIC_FORBIDDEN_RESULT", producer.INPUT_INVALID, 1),
        ]
        for field, value, code, expected_stage in tests:
            with self.subTest(field=field):
                raw = repin(fixtures())
                output = json.loads(raw["output"])
                output[field] = value
                raw["output"] = encode(output)
                with self.assertRaises(producer.ReconciliationError) as error:
                    run(raw)
                self.assertEqual((code, expected_stage), (error.exception.code, error.exception.stage),
                                 "a supplied sidecar's structural trust checks precede its semantic byte comparison")

    def test_submission_on_object_artifact_cannot_recover_a_clock(self):
        values = fixtures()
        object_artifact = next(a["artifact_id"] for a in values["acceptance"]["artifacts"] if a["basis"] == "provider_object_metadata_only")
        values["witness"]["events"][0]["artifact_id"] = object_artifact
        values["output"]["entries"][0].update(disposition="unresolved", reconciled_publication_time=None,
                                                reasons=["source_not_authoritative"])
        values["output"]["entries"][40]["reasons"] = ["after_acquisition_boundary", "source_not_authoritative"]
        raw = repin(values)
        self.assertEqual(raw["output"], run(raw), "object-only authority fails on both the current and earlier acquisition observations")

    def test_equivalent_offset_coverage_cutoff_preserves_complete_lifetime(self):
        values = fixtures()
        values["acceptance"]["artifacts"][0]["coverage"]["until"] = "2026-10-21T14:00:00+02:00"
        raw = repin(values)
        self.assertEqual(raw["output"], run(raw), "coverage cutoff equality must compare UTC instants, not timestamp spellings")

    def test_cli_requires_exact_flag_names(self):
        tests = [("--source-acceptance-sha256", "--source-acceptance-sha"),
                 ("--inventory-sha256", "--inventory-sha"), ("--verify-output", "--verify")]
        for full, prefix in tests:
            with self.subTest(prefix=prefix), tempfile.TemporaryDirectory() as directory:
                output = Path(directory) / "output.json"
                args = [prefix if value == full else value for value in cli_arguments(output)]
                stdout, stderr = io.StringIO(), io.StringIO()
                with redirect_stdout(stdout), redirect_stderr(stderr):
                    result = producer.main(args)
                self.assertEqual((1, "", "RECONCILIATION_INPUT_INVALID\n"), (result, stdout.getvalue(), stderr.getvalue()),
                                 "abbreviated trust or verification flags must not silently enable an invocation")
                self.assertEqual([], list(Path(directory).iterdir()), "rejected syntax cannot publish output")

    def test_publication_syncs_the_final_directory_after_cleanup(self):
        with tempfile.TemporaryDirectory() as directory:
            output = Path(directory) / "output.json"
            real_sync, calls = os.fsync, []
            def sync(fd):
                kind = "directory" if stat.S_ISDIR(os.fstat(fd).st_mode) else "file"
                calls.append(kind)
                if kind == "directory":
                    self.assertEqual([output], list(Path(directory).iterdir()), "directory sync must persist final link creation and temporary unlink")
                return real_sync(fd)
            with patch.object(producer.os, "fsync", side_effect=sync):
                self.assertFalse(producer.publish(output, b"invented"), "normal publication needs no cleanup warning")
            self.assertEqual(["file", "directory"], calls, "both payload and final directory entry must be synced")

    def test_cli_reports_success_when_only_post_link_cleanup_is_pending(self):
        with tempfile.TemporaryDirectory() as directory:
            output = Path(directory) / "output.json"
            stdout, stderr = io.StringIO(), io.StringIO()
            with patch.object(producer.os, "unlink", side_effect=OSError("SYNTHETIC_PRIVATE_ERROR")):
                with redirect_stdout(stdout), redirect_stderr(stderr):
                    result = producer.main(cli_arguments(output))
            self.assertEqual((0, "reconciliation published; temporary cleanup pending\n", ""),
                             (result, stdout.getvalue(), stderr.getvalue()), "a complete published output must not be reported as an unpublished failure")
            self.assertEqual((FIXTURES / NAMES["output"]).read_bytes(), output.read_bytes(), "the warned output must still contain all exact canonical bytes")
            temporary = [p for p in Path(directory).iterdir() if p.name.startswith(".reconciliation-")]
            self.assertEqual(1, len(temporary), "only this writer's private temporary link may await cleanup")
            self.assertEqual(0o600, stat.S_IMODE(temporary[0].stat().st_mode), "cleanup-pending data must remain private")
            temporary[0].unlink()
            self.assertEqual(1, output.stat().st_nlink, "explicit synthetic cleanup restores a singly linked final output")

    def test_abrupt_writer_exit_leaves_only_a_private_temporary_file(self):
        with tempfile.TemporaryDirectory() as directory:
            output = Path(directory) / "output.json"
            code = ("import os,sys;sys.path.insert(0,sys.argv[1]);import code_review_export_reconciliation as p;"
                    "p.os.link=lambda *a,**k:os._exit(75);p.publish(sys.argv[2],b'invented')")
            result = subprocess.run([sys.executable, "-B", "-c", code, str(SCRIPT.parent), str(output)], capture_output=True, check=False)
            self.assertEqual((75, b"", b""), (result.returncode, result.stdout, result.stderr), "synthetic abrupt termination must happen before final publication")
            temporary = list(Path(directory).iterdir())
            self.assertFalse(output.exists(), "abrupt termination cannot expose an incomplete final output")
            self.assertEqual(1, len(temporary), "uncatchable exits leave a documented private temporary file")
            self.assertTrue(temporary[0].name.startswith(".reconciliation-"), "temporary file must stay in the reserved private namespace")
            self.assertEqual(0o600, stat.S_IMODE(temporary[0].stat().st_mode), "an abandoned temporary payload must remain private")

    def test_unpaired_surrogates_in_keys_and_values_fail_before_pins(self):
        for surrogate in ("\ud800", "\udfff"):
            for artifact in NAMES:
                for place in ("key", "value"):
                    with self.subTest(surrogate=ord(surrogate), artifact=artifact, place=place):
                        values = fixtures()
                        if place == "key":
                            values[artifact][surrogate] = "invented"
                        elif artifact == "inventory":
                            values[artifact]["observations"][0]["publication_key"] = surrogate
                        elif artifact == "witness":
                            values[artifact]["artifacts"][0]["artifact_id"] = surrogate
                        elif artifact == "acceptance":
                            values[artifact]["operator_attestation"]["attestor_id"] = surrogate
                        else:
                            values[artifact]["entries"][0]["event_ids"] = [surrogate]
                        with self.assertRaises(producer.ReconciliationError) as error:
                            run(repin(values), source_acceptance_sha256="0" * 64)
                        self.assertEqual((producer.INPUT_INVALID, 1), (error.exception.code, error.exception.stage),
                                         "unpaired high or low surrogates cannot reach source acceptance or UTF-8 hashing")

    def test_valid_paired_unicode_survives_and_hashes_as_exact_utf8(self):
        values = fixtures()
        key = "invented-\U0001f600-publication"
        values["inventory"]["observations"][24]["publication_key"] = key
        for event in values["witness"]["events"]:
            if event["event_id"] in ("event-21", "event-22"):
                event["publication_key_sha256"] = sha(key.encode("utf-8"))
        raw = repin(values)
        self.assertIn(b"\\ud83d\\ude00", raw["inventory"], "the test must exercise an escaped surrogate pair")
        self.assertEqual(raw["output"], run(raw), "valid paired text must retain exact UTF-8 key matching and publication evidence")
        with producer.stage(1):
            decoded = producer.load(encode({key: key}))
        self.assertEqual({key: key}, decoded, "framing must preserve valid paired characters in keys as well as values")

    def test_per_array_stage_one_caps_precede_candidate_expansion(self):
        tests = [("bindings", 100001), ("events", 200001), ("object_observations", 200001), ("artifacts", 4097)]
        for field, count in tests:
            with self.subTest(field=field):
                witness = fixtures()["witness"]
                witness[field] = [witness[field][0]] * count
                with self.assertRaises(producer.ReconciliationError) as error:
                    with producer.stage(1):
                        producer.validate_witness(witness)
                self.assertEqual((producer.INPUT_INVALID, 1), (error.exception.code, error.exception.stage),
                                 "each array has its contract cap independently of stage-3 aggregate limits")
        acceptance = fixtures()["acceptance"]
        acceptance["repository_bindings"] *= 257
        with self.assertRaises(producer.ReconciliationError) as error:
            with producer.stage(1):
                producer.validate_acceptance(acceptance)
        self.assertEqual((producer.INPUT_INVALID, 1), (error.exception.code, error.exception.stage), "repository-map array cap is a grammar limit")

    def test_directory_sync_failure_reports_io_error_and_preserves_complete_output(self):
        with tempfile.TemporaryDirectory() as directory:
            output = Path(directory) / "output.json"
            real_sync = os.fsync
            def sync(fd):
                if stat.S_ISDIR(os.fstat(fd).st_mode):
                    raise OSError("SYNTHETIC_PRIVATE_ERROR")
                return real_sync(fd)
            stdout, stderr = io.StringIO(), io.StringIO()
            with patch.object(producer.os, "fsync", side_effect=sync):
                with redirect_stdout(stdout), redirect_stderr(stderr):
                    result = producer.main(cli_arguments(output))
            self.assertEqual((1, "", "RECONCILIATION_INPUT_INVALID\n"), (result, stdout.getvalue(), stderr.getvalue()),
                             "unconfirmed directory durability must remain an IO failure with a static code")
            self.assertEqual((FIXTURES / NAMES["output"]).read_bytes(), output.read_bytes(), "post-link IO failure may leave only a complete final payload")
            self.assertEqual([output], list(Path(directory).iterdir()), "a directory-sync error cannot leave a partial published payload")

    def test_cli_private_atomic_exclusive_publication(self):
        with tempfile.TemporaryDirectory() as directory:
            output = Path(directory) / "sidecar.json"
            command = [sys.executable, "-B", str(SCRIPT), *cli_arguments(output)]
            result = subprocess.run(command, capture_output=True, check=False)
            self.assertEqual((0, b""), (result.returncode, result.stderr), "synthetic CLI must produce one complete sidecar")
            self.assertEqual((FIXTURES / NAMES["output"]).read_bytes(), output.read_bytes(), "CLI output must equal independent expected bytes")
            self.assertEqual(0o600, stat.S_IMODE(output.stat().st_mode), "only the operator may read the sidecar")
            again = subprocess.run(command, capture_output=True, check=False)
            self.assertEqual((1, b"", b"RECONCILIATION_INPUT_INVALID\n"), (again.returncode, again.stdout, again.stderr), "retry cannot overwrite an existing sidecar or reveal its path")
            self.assertEqual([output], list(Path(directory).iterdir()), "no partial temporary payload may remain")

    def test_concurrent_publication_has_one_complete_winner(self):
        with tempfile.TemporaryDirectory() as directory:
            output = Path(directory) / "sidecar.json"
            def attempt(payload):
                try:
                    with producer.stage(1):
                        producer.publish(output, payload)
                    return True
                except producer.ReconciliationError:
                    return False
            with ThreadPoolExecutor(max_workers=2) as executor:
                result = list(executor.map(attempt, [b"invented-a", b"invented-b"]))
            self.assertEqual([False, True], sorted(result), "exactly one concurrent writer may publish")
            self.assertIn(output.read_bytes(), [b"invented-a", b"invented-b"], "winning output cannot contain partial or mixed writes")
            self.assertEqual([output], list(Path(directory).iterdir()), "losing writer must clean its temporary file")

    def test_cli_errors_and_io_boundaries_are_static(self):
        tests = [
            ["--invented-unknown", "SYNTHETIC_SECRET_SENTINEL"],
            ["--mode", "synthetic", "--inventory", "SYNTHETIC_MISSING_PATH"],
        ]
        for arguments in tests:
            with self.subTest(arguments=arguments):
                stdout, stderr = io.StringIO(), io.StringIO()
                with redirect_stdout(stdout), redirect_stderr(stderr):
                    result = producer.main(arguments)
                self.assertEqual((1, "", "RECONCILIATION_INPUT_INVALID\n"), (result, stdout.getvalue(), stderr.getvalue()), "CLI syntax errors cannot echo arbitrary arguments or paths")
        with tempfile.TemporaryDirectory() as directory:
            parent = Path(directory)
            target = parent / "input.json"
            target.write_bytes(b"{}")
            symlink = parent / "link.json"
            symlink.symlink_to(target)
            hardlink = parent / "hardlink.json"
            os.link(target, hardlink)
            for path in (symlink, hardlink):
                with self.subTest(path=path.name):
                    with self.assertRaises(producer.ReconciliationError):
                        with producer.stage(1):
                            producer.read_input(path)
            public = parent / "public"
            public.mkdir(mode=0o755)
            public.chmod(0o755)
            with self.assertRaises(producer.ReconciliationError):
                with producer.stage(1):
                    producer.publish(public / "output.json", b"invented")
            self.assertEqual([], list(public.iterdir()), "non-private destinations cannot receive temporary or final data")


if __name__ == "__main__":
    unittest.main()
