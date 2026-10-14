import contextlib
import hashlib
import importlib.util
import io
import json
import os
from pathlib import Path
import subprocess
import sys
import tempfile
import threading
import types
import unittest
from unittest import mock

LAUNCHER = Path(__file__).with_name("review_source_profile.py")
PIN = "f60e397c24cc8f0d7061037c81d5630f4774700602e429f1d428577afbdf9c80"


def fresh_profile():
    spec = importlib.util.spec_from_file_location("source_profile_fixture", LAUNCHER)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    return module


def decode_records(output):
    result = []
    for line in output.splitlines():
        if line.startswith("143_SOURCE_PROFILE_V1 "):
            result.append(json.loads(line.removeprefix("143_SOURCE_PROFILE_V1 ")))
    return result


def fixture_namespace(overrides=None):
    calls = []
    sentinel = object()

    def original(name, result):
        def call(*args, **kwargs):
            calls.append((name, args, kwargs))
            return result
        return call

    class FixturePath:
        def __init__(self, name):
            self.name = name

        write_text = original("write_text", sentinel)

    namespace = {
        "selected_artifacts": original("selected_artifacts", [{"url": "synthetic-private-url"}]),
        "read_once": original("read_once", b"source"),
        "extract": original("extract", sentinel),
        "input_hashes": original("input_hashes", {"synthetic-path": "hash"}),
        "check_source_capacity": original("check_source_capacity", {"entries": 4, "path_bytes": 70}),
        "tree_inventory": original("tree_inventory", {"synthetic-path": "hash"}),
        "remove_tree": original("remove_tree", sentinel),
        "prepare_additional": original("prepare_additional", sentinel),
        "subprocess": types.SimpleNamespace(run=original("run", sentinel)),
        "Path": FixturePath,
        "json": types.SimpleNamespace(dumps=json.dumps),
    }
    namespace.update(overrides or {})
    return namespace, calls, sentinel


class ReviewSourceProfileTests(unittest.TestCase):
    def assert_numeric_records(self, records):
        for record in records:
            self.assertTrue(all(type(value) is int for value in record.values()))
            self.assertEqual(record["version"], 1)
            self.assertLessEqual(len(json.dumps(record)), 1024)
            if record["op"] == 3:
                self.assertEqual(record, {"version": 1, "op": 3, "phase": 0, "category": 0})
            elif record["op"] == 1:
                self.assertEqual(set(record), {"version", "op", "phase", "category"})
            else:
                self.assertGreaterEqual(record["duration_us"], 0)
                self.assertLessEqual(record["duration_us"], 480000000)
                self.assertIn(record["failed"], (0, 1))

    def test_function_arguments_results_categories_and_existing_totals_are_preserved(self):
        profile = fresh_profile()
        namespace, calls, sentinel = fixture_namespace()
        output = io.StringIO()
        with contextlib.redirect_stdout(output):
            profile.install(namespace)
            values = [
                namespace["selected_artifacts"](deadline=12),
                namespace["read_once"](url="https://registry.npmjs.org/source", deadline=12, maximum=20),
                namespace["extract"](data=sentinel, destination=Path("sources/npm/archive"), kind="tar", spool_directory=sentinel),
                namespace["input_hashes"](paths=[sentinel]),
                namespace["check_source_capacity"](sources=sentinel),
                namespace["tree_inventory"](path=Path("sources/python")),
                namespace["remove_tree"](path=Path("sources/go")),
            ]
        self.assertIs(values[2], sentinel)
        self.assertIs(values[-1], sentinel)
        self.assertEqual(calls[1], ("read_once", (), {"url": "https://registry.npmjs.org/source", "deadline": 12, "maximum": 20}))
        self.assertEqual(calls[2], ("extract", (), {"data": sentinel, "destination": Path("sources/npm/archive"), "kind": "tar", "spool_directory": sentinel}))
        records = decode_records(output.getvalue())
        exits = [r for r in records if r["op"] == 2]
        self.assertEqual([(r["phase"], r["category"]) for r in exits], [(1, 1), (2, 2), (3, 2), (6, 1), (7, 1), (8, 3), (10, 4)])
        self.assertEqual([r.get("items") for r in exits], [1, None, None, 1, 4, 1, None])
        self.assertEqual([r.get("bytes") for r in exits], [None, 6, None, None, 70, None, None])
        self.assertNotIn("synthetic-private", output.getvalue())
        self.assertNotIn("registry.npmjs.org", output.getvalue())
        self.assert_numeric_records(records)

    def test_download_categories_do_not_attribute_unknown_registries_to_python(self):
        cases = [("https://registry.npmjs.org/source", 2), ("https://pypi.org/source", 3), ("https://files.pythonhosted.org/source", 3), ("https://download.pytorch.org/source", 3), ("https://download-r2.pytorch.org/source", 3), ("https://rubygems.org/source", 1), ("https://unknown.invalid/source", 1)]
        for url, category in cases:
            with self.subTest(category=category):
                profile = fresh_profile()
                namespace, _, _ = fixture_namespace()
                output = io.StringIO()
                with contextlib.redirect_stdout(output):
                    profile.install(namespace)
                    self.assertEqual(namespace["read_once"](url=url, deadline=12, maximum=20), b"source")
                records = decode_records(output.getvalue())
                self.assertEqual([r["category"] for r in records], [category, category])
                self.assertNotIn(url, output.getvalue())
                self.assert_numeric_records(records)

    def test_original_exceptions_and_diagnostic_failures_do_not_change_behavior(self):
        class BadDestination:
            @property
            def parent(self):
                raise ValueError("synthetic-private-path")

        class BadTarget:
            def tell(self):
                raise ValueError("synthetic-private-error")

        cases = ["original error", "category error", "metric error", "invalid numeric metric", "clock error"]
        for case in cases:
            with self.subTest(case=case):
                profile = fresh_profile()
                output = io.StringIO()
                fault = RuntimeError("synthetic-private-error")
                result = {"entries": "synthetic-private-url", "path_bytes": 5}
                overrides = {}
                if case == "original error":
                    def original(*args, **kwargs):
                        raise fault
                    overrides["extract"] = original
                elif case == "metric error":
                    overrides["read_once"] = lambda *args, **kwargs: None
                elif case == "invalid numeric metric":
                    overrides["check_source_capacity"] = lambda *args, **kwargs: result
                namespace, _, sentinel = fixture_namespace(overrides)
                with contextlib.redirect_stdout(output), contextlib.ExitStack() as stack:
                    profile.install(namespace)
                    if case == "clock error":
                        stack.enter_context(mock.patch.object(profile.time, "monotonic_ns", side_effect=ValueError("synthetic-private-clock")))
                    if case == "original error":
                        with self.assertRaises(RuntimeError) as caught:
                            namespace["extract"](sentinel, Path("npm/archive"), "tar")
                        self.assertIs(caught.exception, fault)
                    elif case == "category error":
                        self.assertIs(namespace["extract"](sentinel, BadDestination(), "tar"), sentinel)
                    elif case == "metric error":
                        self.assertIsNone(namespace["read_once"]("https://pypi.org/source", 12, 20, target=BadTarget()))
                    elif case == "invalid numeric metric":
                        self.assertIs(namespace["check_source_capacity"](sentinel), result)
                    else:
                        self.assertIs(namespace["extract"](sentinel, Path("npm/archive"), "tar"), sentinel)
                records = decode_records(output.getvalue())
                self.assert_numeric_records(records)
                if case == "original error":
                    self.assertEqual(records[-1]["failed"], 1)
                else:
                    self.assertEqual(records[-1]["op"], 3)
                self.assertNotIn("synthetic-private", output.getvalue())

    def test_go_processing_split_preserves_only_exact_calls_and_cleans_thread_context(self):
        for failure in (None, "before", "download", "after"):
            with self.subTest(failure=failure):
                profile = fresh_profile()
                namespace, calls, sentinel = fixture_namespace()
                fault = RuntimeError("synthetic-go-failure")
                original_run = namespace["subprocess"].run

                def run(*args, **kwargs):
                    if failure == "download" and kwargs.get("args", [])[:4] == ["go", "mod", "download", "-json"]:
                        raise fault
                    return original_run(*args, **kwargs)

                namespace["subprocess"].run = run

                def additional(*args, **kwargs):
                    if failure == "before":
                        raise fault
                    namespace["subprocess"].run(["go", "version"], env=sentinel)
                    value = namespace["subprocess"].run(args=["go", "mod", "download", "-json", "example.invalid/module@v1"], cwd=sentinel)
                    if failure == "after":
                        raise fault
                    return value

                namespace["prepare_additional"] = additional
                output = io.StringIO()
                with contextlib.redirect_stdout(output):
                    profile.install(namespace)
                    namespace["subprocess"].run(["go", "mod", "download", "-json", "outside@v1"])
                    if failure:
                        with self.assertRaises(RuntimeError) as caught:
                            namespace["prepare_additional"](stage=sentinel, deadline=12)
                        self.assertIs(caught.exception, fault)
                    else:
                        self.assertIs(namespace["prepare_additional"](stage=sentinel, deadline=12), sentinel)
                self.assertEqual(profile.local.frames, [])
                records = decode_records(output.getvalue())
                self.assert_numeric_records(records)
                exits = [r for r in records if r["op"] == 2]
                self.assertEqual([r["phase"] for r in exits], [5] if failure == "before" else [5, 4] if failure == "download" else [5, 4, 5])
                self.assertEqual(exits[-1]["failed"], int(bool(failure)))
                self.assertNotIn("example.invalid", output.getvalue())

    def test_phase_entry_precedes_blocking_work_with_thread_overlap(self):
        profile = fresh_profile()
        output = io.StringIO()
        entered = threading.Barrier(3)
        release = threading.Event()
        results, errors = [], []

        def read(*args, **kwargs):
            entered.wait(timeout=5)
            if not release.wait(timeout=5):
                raise RuntimeError("fixture wait expired")
            return b"source"

        namespace, _, _ = fixture_namespace({"read_once": read})

        def worker(url):
            try:
                results.append(namespace["read_once"](url=url, deadline=12, maximum=20))
            except BaseException as error:
                errors.append(error)

        with contextlib.redirect_stdout(output):
            profile.install(namespace)
            threads = [threading.Thread(target=worker, args=(url,)) for url in ("https://registry.npmjs.org/source", "https://pypi.org/source")]
            for thread in threads:
                thread.start()
            try:
                entered.wait(timeout=5)
                self.assertEqual(sorted((r["op"], r["category"]) for r in decode_records(output.getvalue())), [(1, 2), (1, 3)])
            finally:
                release.set()
                for thread in threads:
                    thread.join(timeout=5)
        self.assertEqual(errors, [])
        self.assertEqual(results, [b"source", b"source"])
        self.assertTrue(all(not thread.is_alive() for thread in threads))
        self.assert_numeric_records(decode_records(output.getvalue()))

    def test_overlapping_go_workers_have_independent_processing_frames(self):
        profile = fresh_profile()
        namespace, _, _ = fixture_namespace()
        entered = threading.Barrier(3)
        release = threading.Event()
        results, errors = [], []
        namespace["subprocess"].run = lambda *args, **kwargs: kwargs["marker"]

        def additional(*args, **kwargs):
            entered.wait(timeout=5)
            if not release.wait(timeout=5):
                raise RuntimeError("fixture wait expired")
            return namespace["subprocess"].run(args=["go", "mod", "download", "-json", "example.invalid/module@v1"], marker=kwargs["marker"])

        namespace["prepare_additional"] = additional
        output = io.StringIO()

        def worker(marker):
            try:
                value = namespace["prepare_additional"](marker=marker)
                results.append((value, list(profile.local.frames)))
            except BaseException as error:
                errors.append(error)

        with contextlib.redirect_stdout(output):
            profile.install(namespace)
            threads = [threading.Thread(target=worker, args=(marker,)) for marker in (17, 23)]
            for thread in threads:
                thread.start()
            try:
                entered.wait(timeout=5)
                self.assertEqual([(r["op"], r["phase"]) for r in decode_records(output.getvalue())], [(1, 5), (1, 5)])
            finally:
                release.set()
                for thread in threads:
                    thread.join(timeout=5)
        self.assertEqual(errors, [])
        self.assertEqual(sorted(results), [(17, []), (23, [])])
        self.assertTrue(all(not thread.is_alive() for thread in threads))
        records = decode_records(output.getvalue())
        self.assert_numeric_records(records)
        self.assertEqual(sum(r["op"] == 1 and r["phase"] == 4 for r in records), 2)
        self.assertEqual(sum(r["op"] == 2 and r["phase"] == 4 for r in records), 2)
        self.assertEqual(sum(r["op"] == 1 and r["phase"] == 5 for r in records), 4)
        self.assertEqual(sum(r["op"] == 2 and r["phase"] == 5 for r in records), 4)

    def test_record_cap_numeric_validation_and_output_failure_are_bounded(self):
        profile = fresh_profile()
        output = io.StringIO()
        with contextlib.redirect_stdout(output):
            for _ in range(20050):
                profile.emit(1, 2, 3)
        records = decode_records(output.getvalue())
        self.assertEqual(len(records), 20000)
        self.assertEqual(sum(r["op"] == 3 for r in records), 1)
        self.assertEqual(records[-1]["op"], 3)
        self.assert_numeric_records(records)
        for metrics in ({"duration_us": -1, "failed": 0}, {"duration_us": 480000001, "failed": 0}, {"duration_us": 0, "failed": 2}, {"duration_us": 0, "failed": 0, "bytes": 1073741825}, {"duration_us": 0, "failed": 0, "items": True}, {"duration_us": 0, "failed": 0, "items": 1000001}):
            with self.subTest(metrics=metrics):
                profile = fresh_profile()
                output = io.StringIO()
                with contextlib.redirect_stdout(output):
                    profile.emit(2, 3, 1, **metrics)
                self.assertEqual(decode_records(output.getvalue()), [{"version": 1, "op": 3, "phase": 0, "category": 0}])
        profile = fresh_profile()
        output = io.StringIO()
        writer = mock.Mock()
        attempts = []
        def write(value):
            attempts.append(value)
            if len(attempts) == 1:
                raise OSError("synthetic-private-write")
            return output.write(value)
        writer.write = write
        with mock.patch.object(sys, "stdout", writer):
            profile.emit(1, 2, 3)
            profile.emit(2, 2, 3, duration_us=1, failed=0)
        self.assertEqual(decode_records(output.getvalue()), [{"version": 1, "op": 3, "phase": 0, "category": 0}])

    def test_main_restores_argv_import_path_and_preserves_return_or_exception(self):
        source = b'''from pathlib import Path
import sys, fixture_state
ROOT = Path(__file__).resolve().parent.parent
def main():
    fixture_state.observed = (list(sys.argv), sys.path[0], __file__, str(ROOT), __name__)
    if fixture_state.fault is not None:
        raise fixture_state.fault
    return fixture_state.value
if __name__ == "__main__":
    fixture_state.guard = True
'''
        for option, fault in (([], None), (["--verify"], None), ([], RuntimeError("synthetic-original")), ([], KeyboardInterrupt()), ([], SystemExit(17))):
            with self.subTest(option=option, fault=type(fault).__name__), tempfile.TemporaryDirectory() as temporary:
                root = Path(temporary).resolve()
                (root / ".143").mkdir()
                helper = root / ".143/review-sources.py"
                helper.write_bytes(source)
                state = types.SimpleNamespace(fault=fault, value=object(), guard=False)
                profile = fresh_profile()
                argv, search = ["-c", ".143/review-sources.py", *option], ["wrapper-path", *sys.path]
                previous = Path.cwd()
                try:
                    os.chdir(root)
                    with mock.patch.object(profile, "EXPECTED_SHA256", hashlib.sha256(source).hexdigest()), mock.patch.object(profile, "install"), mock.patch.dict(sys.modules, {"fixture_state": state}), mock.patch.object(sys, "argv", argv), mock.patch.object(sys, "path", search), contextlib.redirect_stdout(io.StringIO()):
                        if fault is None:
                            self.assertIs(profile.main(), state.value)
                        else:
                            with self.assertRaises(type(fault)) as caught:
                                profile.main()
                            self.assertIs(caught.exception, fault)
                        self.assertIs(sys.argv, argv)
                        self.assertIs(sys.path, search)
                finally:
                    os.chdir(previous)
                self.assertEqual(state.observed, ([".143/review-sources.py", *option], str(root / ".143"), str(helper), str(root), "_143_acceptance_profile_target"))
                self.assertFalse(state.guard)

    def run_child(self, root, source, args=(), pinned=True):
        launcher = LAUNCHER.read_text()
        if pinned:
            launcher = launcher.replace(PIN, hashlib.sha256(source).hexdigest(), 1)
        return subprocess.run([sys.executable, "-c", launcher, ".143/review-sources.py", *args], cwd=root, text=True, capture_output=True, timeout=10)

    def test_receipt_serialization_and_write_are_observed_without_recursive_emission(self):
        source = b'''import json, sys, types
from pathlib import Path
from neighbor import VALUE
selected_artifacts = lambda *a, **k: []
read_once = lambda *a, **k: b"source"
extract = lambda *a, **k: None
input_hashes = lambda *a, **k: {}
check_source_capacity = lambda *a, **k: {"entries": 1, "path_bytes": 1}
tree_inventory = lambda *a, **k: {}
remove_tree = lambda *a, **k: None
prepare_additional = lambda *a, **k: None
subprocess = types.SimpleNamespace(run=lambda *a, **k: None)
def main():
    receipt = {"version": 1, "inputs": {"fixture": "hash"}, "files": {"library.py": "hash"}}
    payload = json.dumps(obj=receipt, sort_keys=True, separators=(",", ":"))
    written = Path("receipt.json").write_text(data=payload, encoding="utf-8")
    Path("result.json").write_text(json.dumps({"argv": sys.argv, "neighbor": VALUE, "payload": payload, "written": written}), encoding="utf-8")
if __name__ == "__main__":
    Path("unwanted-main").touch()
'''
        for args in ((), ("--verify",)):
            with self.subTest(args=args), tempfile.TemporaryDirectory() as temporary:
                root = Path(temporary).resolve()
                (root / ".143").mkdir()
                (root / ".143/review-sources.py").write_bytes(source)
                (root / ".143/neighbor.py").write_text("VALUE = 42\n")
                result = self.run_child(root, source, args)
                self.assertEqual(result.returncode, 0, result.stderr)
                records = decode_records(result.stdout)
                self.assert_numeric_records(records)
                self.assertEqual([r["op"] for r in records if r["phase"] == 9], [1, 2, 1, 2])
                self.assertLessEqual(len(records), 20)
                self.assertFalse((root / "unwanted-main").exists())
                actual = json.loads((root / "result.json").read_text())
                expected = '{"files":{"library.py":"hash"},"inputs":{"fixture":"hash"},"version":1}'
                self.assertEqual(actual, {"argv": [".143/review-sources.py", *args], "neighbor": 42, "payload": expected, "written": len(expected)})
                self.assertNotIn(str(root), result.stdout)
                self.assertNotIn("library.py", result.stdout)

    def test_bad_hash_invocation_and_redirected_or_special_helper_refuse_before_import(self):
        source = b'from pathlib import Path\nPath("executed").touch()\n'
        for kind in ("hash", "argument", "linked", "oversized", "fifo"):
            with self.subTest(kind=kind), tempfile.TemporaryDirectory() as temporary:
                root = Path(temporary).resolve()
                (root / ".143").mkdir()
                helper = root / ".143/review-sources.py"
                payload = source
                if kind == "linked":
                    (root / "other.py").write_bytes(source)
                    helper.symlink_to(root / "other.py")
                elif kind == "fifo":
                    os.mkfifo(helper)
                else:
                    payload = b"x" * (1048576 + 1) if kind == "oversized" else source
                    helper.write_bytes(payload)
                result = self.run_child(root, payload, ("--synthetic-private-argument",) if kind == "argument" else (), pinned=kind != "hash")
                self.assertNotEqual(result.returncode, 0)
                self.assertFalse((root / "executed").exists())
                self.assertEqual(decode_records(result.stdout), [{"version": 1, "op": 3, "phase": 0, "category": 0}])
                self.assertNotIn("--synthetic-private-argument", result.stdout + result.stderr)


if __name__ == "__main__":
    unittest.main()
