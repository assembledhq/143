import contextlib
import concurrent.futures
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
import time
import tracemalloc
import types
import unittest
from unittest import mock

LAUNCHER = Path(__file__).with_name("review_source_profile.py")
PROFILES = []
PIN = "dab54afa0ae04fcc1014150d5b14ed5948dac28a3bdde9752e1a5be79bd2501b"


def fresh_profile():
    spec = importlib.util.spec_from_file_location("source_profile_fixture", LAUNCHER)
    module = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(module)
    original_install = module.install
    def install(namespace):
        progress = original_install(namespace)
        PROFILES.append(progress)
        return progress
    module.install = install
    return module


def decode_records(output, checkpoints=False):
    result = []
    for line in output.splitlines():
        if line.startswith("143_SOURCE_PROFILE_V1 "):
            value = json.loads(line.removeprefix("143_SOURCE_PROFILE_V1 "))
            if checkpoints or value["op"] != 4:
                result.append(value)
    return result


def fixture_namespace(overrides=None):
    calls = []
    sentinel = object()

    def original(name, result):
        def call(*args, **kwargs):
            calls.append((name, args, kwargs))
            return result
        return call

    def inventory(*args, **kwargs):
        calls.append(("iter_tree_inventory", args, kwargs))
        yield "synthetic-path", "hash"

    class FixturePath:
        def __init__(self, name):
            self.name = name

        write_text = original("write_text", sentinel)

    namespace = {
        "selected_artifacts": original("selected_artifacts", [{"url": "synthetic-private-url"}]),
        "read_once": original("read_once", b"source"),
        "extract": original("extract", sentinel),
        "file_hash": original("file_hash", "hash"),
        "_inventory_hash": original("_inventory_hash", ("hash", 6)),
        "input_hashes": original("input_hashes", {"synthetic-path": "hash"}),
        "check_source_capacity": original("check_source_capacity", {"entries": 4, "path_bytes": 70}),
        "tree_inventory": original("tree_inventory", {"synthetic-path": "hash"}),
        "iter_tree_inventory": inventory,
        "write_receipt": original("write_receipt", 1),
        "remove_tree": original("remove_tree", sentinel),
        "prepare_additional": original("prepare_additional", sentinel),
        "subprocess": types.SimpleNamespace(run=original("run", sentinel)),
        "Path": FixturePath,
        "json": types.SimpleNamespace(dumps=json.dumps),
    }
    namespace.update(overrides or {})
    if overrides and "file_hash" in overrides and "_inventory_hash" not in overrides:
        namespace["_inventory_hash"] = lambda *args, **kwargs: (namespace["file_hash"](*args, **kwargs), 6)
    return namespace, calls, sentinel


class ReviewSourceProfileTests(unittest.TestCase):
    def tearDown(self):
        with contextlib.redirect_stdout(io.StringIO()):
            while PROFILES:
                PROFILES.pop().close()

    def assert_numeric_records(self, records):
        for record in records:
            self.assertTrue(all(type(value) is int for value in record.values()))
            self.assertEqual(record["version"], 1)
            self.assertLessEqual(len(json.dumps(record)), 1024)
            if record["op"] == 3:
                self.assertEqual(record, {"version": 1, "op": 3, "phase": 0, "category": 0})
            elif record["op"] == 4:
                self.assertEqual(set(record), {"version", "op", "phase", "category", *fresh_profile().CHECKPOINT_LIMITS})
                self.assertEqual((record["phase"], record["category"]), (0, 0))
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
                list(namespace["iter_tree_inventory"](path=Path("sources/python"))),
                namespace["write_receipt"](path=Path("receipt.json"), inputs={}, sources=sentinel, deadline=12),
                namespace["remove_tree"](path=Path("sources/go")),
            ]
        self.assertIs(values[2], sentinel)
        self.assertIs(values[-1], sentinel)
        self.assertEqual(calls[1], ("read_once", (), {"url": "https://registry.npmjs.org/source", "deadline": 12, "maximum": 20}))
        self.assertEqual(calls[2], ("extract", (), {"data": sentinel, "destination": Path("sources/npm/archive"), "kind": "tar", "spool_directory": sentinel}))
        records = decode_records(output.getvalue())
        exits = [r for r in records if r["op"] == 2]
        self.assertEqual([(r["phase"], r["category"]) for r in exits], [(1, 1), (2, 2), (3, 2), (6, 1), (7, 1), (8, 3), (9, 1), (10, 4)])
        self.assertEqual([r.get("items") for r in exits], [1, None, None, 1, 4, 1, 1, None])
        self.assertEqual([r.get("bytes") for r in exits], [None, 6, None, None, 70, None, None, None])
        self.assertNotIn("synthetic-private", output.getvalue())
        self.assertNotIn("registry.npmjs.org", output.getvalue())
        self.assert_numeric_records(records)

    def test_inventory_generator_is_lazy_and_preserves_send_throw_values_and_return(self):
        profile = fresh_profile()
        first, second, third, sent, returned = (object() for _ in range(5))
        fault = ValueError("synthetic-private-throw")
        calls = []

        def inventory(path, *, marker):
            calls.append((path, marker))
            received = yield first
            self.assertIs(received, sent)
            try:
                yield second
            except ValueError as caught:
                self.assertIs(caught, fault)
                yield third
            return returned

        namespace, _, _ = fixture_namespace({"iter_tree_inventory": inventory})
        output = io.StringIO()
        with contextlib.redirect_stdout(output):
            profile.install(namespace)
            with self.assertRaises(TypeError):
                namespace["iter_tree_inventory"](Path("sources/python"))
            iterator = namespace["iter_tree_inventory"](path=Path("sources/python"), marker=sent)
            self.assertEqual(calls, [])
            self.assertEqual(decode_records(output.getvalue()), [])
            with self.assertRaises(TypeError):
                iterator.send(sent)
            self.assertEqual(decode_records(output.getvalue()), [])
            self.assertIs(next(iterator), first)
            self.assertEqual(calls, [(Path("sources/python"), sent)])
            self.assertEqual([(r["op"], r["phase"]) for r in decode_records(output.getvalue())], [(1, 8)])
            self.assertIs(iterator.send(sent), second)
            self.assertIs(iterator.throw(fault), third)
            with self.assertRaises(StopIteration) as stopped:
                next(iterator)
            self.assertIs(stopped.exception.value, returned)
            self.assertIsNone(iterator.close())
        records = decode_records(output.getvalue())
        self.assertEqual([(r["op"], r["phase"], r["category"]) for r in records], [(1, 8, 3), (2, 8, 3)])
        self.assertEqual(records[-1]["items"], 3)
        self.assertEqual(records[-1]["failed"], 0)
        self.assert_numeric_records(records)
        self.assertNotIn("synthetic-private", output.getvalue())
        self.assertNotIn("sources", output.getvalue())

    def test_inventory_generator_close_and_failures_end_once_without_changing_errors(self):
        for mode in ("close before iteration", "close after yield", "original failure", "injected failure", "close failure"):
            with self.subTest(mode=mode):
                profile = fresh_profile()
                fault = RuntimeError("synthetic-private-inventory-error")
                value = object()
                closed = []

                def inventory(path):
                    try:
                        yield value
                        if mode == "original failure":
                            raise fault
                        yield value
                    finally:
                        closed.append(True)
                        if mode == "close failure":
                            raise fault

                namespace, _, _ = fixture_namespace({"iter_tree_inventory": inventory})
                output = io.StringIO()
                with contextlib.redirect_stdout(output):
                    profile.install(namespace)
                    iterator = namespace["iter_tree_inventory"](Path("sources"))
                    if mode == "close before iteration":
                        self.assertIsNone(iterator.close())
                    else:
                        self.assertIs(next(iterator), value)
                        if mode == "close after yield":
                            self.assertIsNone(iterator.close())
                        else:
                            with self.assertRaises(RuntimeError) as caught:
                                if mode == "original failure":
                                    next(iterator)
                                elif mode == "injected failure":
                                    iterator.throw(fault)
                                else:
                                    iterator.close()
                            self.assertIs(caught.exception, fault)
                        self.assertIsNone(iterator.close())
                records = decode_records(output.getvalue())
                if mode == "close before iteration":
                    self.assertEqual(records, [])
                    self.assertEqual(closed, [])
                else:
                    self.assertEqual([(r["op"], r["phase"]) for r in records], [(1, 8), (2, 8)])
                    self.assertEqual(records[-1]["items"], 1)
                    self.assertEqual(records[-1]["failed"], 1)
                    self.assertEqual(closed, [True])
                self.assert_numeric_records(records)
                self.assertNotIn("synthetic-private", output.getvalue())

    def test_receipt_timeout_after_first_yield_marks_inventory_partial_without_changing_error(self):
        profile = fresh_profile()
        fault = TimeoutError("synthetic-private-receipt-deadline")
        yielded, checks = [], []

        def inventory(path):
            for name in ("first", "second"):
                yielded.append(name)
                yield name, "hash"

        def remaining(deadline):
            checks.append(deadline)
            raise fault

        def write_receipt(path, inputs, sources, deadline):
            with path.open("x", encoding="utf-8") as stream:
                stream.write('{"files":{')
                for name, digest in namespace["iter_tree_inventory"](sources):
                    remaining(deadline)
                    stream.write(json.dumps(name) + ":" + json.dumps(digest))
            return len(yielded)

        namespace, _, _ = fixture_namespace({"iter_tree_inventory": inventory, "write_receipt": write_receipt})
        output = io.StringIO()
        with tempfile.TemporaryDirectory() as temporary, contextlib.redirect_stdout(output):
            profile.install(namespace)
            path = Path(temporary, "receipt.json")
            with self.assertRaises(TimeoutError) as caught:
                namespace["write_receipt"](path=path, inputs={}, sources=Path(temporary), deadline=12)
            self.assertIs(caught.exception, fault)
            self.assertEqual(path.read_text(), '{"files":{')
        self.assertEqual(yielded, ["first"])
        self.assertEqual(checks, [12])
        records = decode_records(output.getvalue())
        self.assertEqual([(r["op"], r["phase"]) for r in records], [(1, 9), (1, 8), (2, 8), (2, 9)])
        self.assertEqual([(r["phase"], r["failed"], r.get("items")) for r in records if r["op"] == 2], [(8, 1, 1), (9, 1, None)])
        self.assert_numeric_records(records)
        self.assertNotIn("synthetic-private", output.getvalue())
        self.assertNotIn("first", output.getvalue())

    def test_receipt_wrapper_counts_result_and_never_mutates_global_path_or_json(self):
        profile = fresh_profile()
        original_write, original_dumps = Path.write_text, json.dumps
        namespace, calls, _ = fixture_namespace({"Path": Path, "json": json, "write_receipt": lambda *args, **kwargs: 19})
        original_tree = namespace["tree_inventory"]
        output = io.StringIO()
        with tempfile.TemporaryDirectory() as temporary, contextlib.redirect_stdout(output):
            profile.install(namespace)
            self.assertIs(Path.write_text, original_write)
            self.assertIs(json.dumps, original_dumps)
            self.assertIs(namespace["tree_inventory"], original_tree)
            Path(temporary, "receipt.json").write_text(json.dumps({"version": 1, "inputs": {}, "files": {}}))
            namespace["tree_inventory"](Path(temporary))
            self.assertEqual(decode_records(output.getvalue()), [])
            self.assertEqual(namespace["write_receipt"](path=Path(temporary, "receipt.json"), inputs={}, sources=Path(temporary), deadline=12), 19)
        records = decode_records(output.getvalue())
        self.assertEqual([(r["op"], r["phase"]) for r in records], [(1, 9), (2, 9)])
        self.assertEqual(records[-1]["items"], 19)
        self.assertEqual(records[-1]["failed"], 0)
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

    def test_streaming_prepare_and_verify_entry_points_cover_inventory_and_receipt(self):
        source = b"""import hashlib, json, sys, types
from pathlib import Path
from neighbor import VALUE
ORIGINAL_DUMPS = json.dumps
ORIGINAL_WRITE = Path.write_text
selected_artifacts = lambda *a, **k: []
read_once = lambda *a, **k: b"source"
extract = lambda *a, **k: None
input_hashes = lambda *a, **k: {}
check_source_capacity = lambda *a, **k: {"entries": 1, "path_bytes": 1}
def hash_file_with_size(path):
    data = path.read_bytes()
    return hashlib.sha256(data).hexdigest(), len(data)
def file_hash(path):
    return hash_file_with_size(path)[0]
def _inventory_hash(path):
    return hash_file_with_size(path)
def tree_inventory(*args, **kwargs):
    raise AssertionError("obsolete inventory path must not be called")
remove_tree = lambda *a, **k: None
prepare_additional = lambda *a, **k: None
subprocess = types.SimpleNamespace(run=lambda *a, **k: None)
def iter_tree_inventory(path):
    for item in sorted(path.rglob("*")):
        if item.is_file():
            yield item.relative_to(path).as_posix(), _inventory_hash(item)[0]
def write_receipt(path, inputs, sources, deadline):
    count = 0
    with path.open("x", encoding="utf-8") as stream:
        stream.write('{"files":{')
        for name, digest in iter_tree_inventory(sources):
            if count:
                stream.write(",")
            stream.write(json.dumps(name) + ":" + json.dumps(digest))
            count += 1
        stream.write('},"inputs":')
        json.dump(inputs, stream, sort_keys=True, separators=(",", ":"))
        stream.write(',"version":1}')
    return count
def prepare():
    source = Path("sources/python/library.py")
    source.parent.mkdir(parents=True)
    source.write_bytes(b"source")
    return write_receipt(Path("receipt.json"), {"fixture": "hash"}, Path("sources"), 12)
def verify():
    receipt = json.loads(Path("receipt.json").read_text())
    files = receipt["files"]
    count = len(files)
    for name, digest in iter_tree_inventory(Path("sources")):
        assert files.pop(name, None) == digest
    assert not files
    return count
def main():
    count = verify() if sys.argv[1:] == ["--verify"] else prepare()
    Path("result.json").write_text(json.dumps({"argv": sys.argv, "neighbor": VALUE, "count": count, "global_path_unchanged": Path.write_text is ORIGINAL_WRITE, "global_json_unchanged": json.dumps is ORIGINAL_DUMPS}), encoding="utf-8")
if __name__ == "__main__":
    Path("unwanted-main").touch()
"""
        digest = hashlib.sha256(b"source").hexdigest()
        expected = json.dumps({"version": 1, "inputs": {"fixture": "hash"}, "files": {"python/library.py": digest}}, sort_keys=True, separators=(",", ":"))
        for args in ((), ("--verify",)):
            with self.subTest(args=args), tempfile.TemporaryDirectory() as temporary:
                root = Path(temporary).resolve()
                (root / ".143").mkdir()
                (root / ".143/review-sources.py").write_bytes(source)
                (root / ".143/neighbor.py").write_text("VALUE = 42\n")
                if args:
                    (root / "sources/python").mkdir(parents=True)
                    (root / "sources/python/library.py").write_bytes(b"source")
                    (root / "receipt.json").write_text(expected)
                result = self.run_child(root, source, args)
                self.assertEqual(result.returncode, 0, result.stderr)
                records = decode_records(result.stdout)
                self.assert_numeric_records(records)
                self.assertEqual([r["op"] for r in records if r["phase"] == 8], [1, 2])
                self.assertEqual([r["op"] for r in records if r["phase"] == 9], [] if args else [1, 2])
                self.assertEqual([(r["phase"], r["items"], r["failed"]) for r in records if r["op"] == 2 and r["phase"] in (8, 9)], [(8, 1, 0)] if args else [(8, 1, 0), (9, 1, 0)])
                self.assertLessEqual(len(records), 10)
                self.assertFalse((root / "unwanted-main").exists())
                self.assertEqual((root / "receipt.json").read_text(), expected)
                actual = json.loads((root / "result.json").read_text())
                self.assertEqual(actual, {"argv": [".143/review-sources.py", *args], "neighbor": 42, "count": 1, "global_path_unchanged": True, "global_json_unchanged": True})
                self.assertNotIn(str(root), result.stdout)
                self.assertNotIn("library.py", result.stdout)


    def test_checkpoint_uses_active_iterator_and_nested_hash_time_not_consumer_hashes(self):
        profile = fresh_profile()
        clock = [1000000]
        def advance(value):
            clock[0] += value
        sentinel = "digest"
        def file_hash(path):
            advance(3001)
            return sentinel
        def inventory(path):
            advance(1001)
            self.assertIs(namespace["_inventory_hash"](path)[0], sentinel)
            advance(999)
            yield sentinel
            advance(2000)
        def receipt(*args, **kwargs):
            advance(5000)
            for value in namespace["iter_tree_inventory"](Path("sources")):
                self.assertIs(value, sentinel)
                advance(4000)
                # Hashing by the suspended consumer must not become inventory.
                self.assertIs(namespace["file_hash"](Path("input")), sentinel)
            advance(6000)
            return 1
        namespace, _, _ = fixture_namespace({"iter_tree_inventory": inventory, "file_hash": file_hash, "write_receipt": receipt})
        output = io.StringIO()
        with contextlib.redirect_stdout(output), mock.patch.object(profile.time, "monotonic_ns", side_effect=lambda: clock[0]):
            progress = profile.install(namespace)
            self.assertIs(namespace["file_hash"](Path("unrelated")), sentinel)
            self.assertEqual(namespace["write_receipt"](), 1)
            progress.close()
        checkpoints = [r for r in decode_records(output.getvalue(), True) if r["op"] == 4]
        self.assertEqual({key: checkpoints[-1][key] for key in ("inventory_us", "inventory_residual_us", "hash_us", "receipt_us", "receipt_outside_inventory_us", "inventory_yielded", "hash_started", "hash_completed", "hash_failed", "receipt_started", "receipt_completed", "receipt_failed", "open_inventory", "active_inventory", "active_hash", "active_receipt")},
                         {"inventory_us": 7, "inventory_residual_us": 4, "hash_us": 3, "receipt_us": 25, "receipt_outside_inventory_us": 18, "inventory_yielded": 1, "hash_started": 1, "hash_completed": 1, "hash_failed": 0, "receipt_started": 1, "receipt_completed": 1, "receipt_failed": 0, "open_inventory": 0, "active_inventory": 0, "active_hash": 0, "active_receipt": 0})
        self.assert_numeric_records(checkpoints)
        self.assertFalse(progress.thread.is_alive())
        self.assertIsNone(profile.local.inventory)
        self.assertIsNone(profile.local.receipt)
        self.assertNotIn("input", output.getvalue())

    def test_checkpoint_survives_blocked_hash_and_publisher_stops_after_join(self):
        profile = fresh_profile()
        profile.CHECKPOINT_INTERVAL = 0.01
        entered, release = threading.Event(), threading.Event()
        output = io.StringIO()
        results, errors = [], []
        def file_hash(path):
            if path.name == "unrelated":
                return "outside-digest"
            entered.set()
            if not release.wait(5):
                raise RuntimeError("fixture wait expired")
            return "digest"
        def inventory(path):
            yield "private-path", namespace["_inventory_hash"](path)[0]
        def receipt():
            return sum(1 for _ in namespace["iter_tree_inventory"](Path("sources")))
        namespace, _, _ = fixture_namespace({"iter_tree_inventory": inventory, "file_hash": file_hash, "write_receipt": receipt})
        def worker():
            try:
                results.append(namespace["write_receipt"]())
            except BaseException as error:
                errors.append(error)
        with contextlib.redirect_stdout(output):
            progress = profile.install(namespace)
            thread = threading.Thread(target=worker)
            thread.start()
            try:
                self.assertTrue(entered.wait(5))
                deadline = time.monotonic() + 5
                while not any(r["op"] == 4 for r in decode_records(output.getvalue(), True)) and time.monotonic() < deadline:
                    time.sleep(0.005)
                partial = [r for r in decode_records(output.getvalue(), True) if r["op"] == 4]
                self.assertTrue(partial)
                self.assertEqual({key: partial[-1][key] for key in ("active_inventory", "active_hash", "active_receipt", "inventory_yielded", "hash_started", "hash_completed")}, {"active_inventory": 1, "active_hash": 1, "active_receipt": 1, "inventory_yielded": 0, "hash_started": 1, "hash_completed": 0})
                self.assertGreater(partial[-1]["hash_us"], 0)
                self.assertFalse(any(r["op"] == 2 for r in decode_records(output.getvalue())))
                # The original module binding and unrelated thread remain free
                # of the iterator TLS marker while this hash is blocked.
                self.assertIsNone(getattr(profile.local, "inventory", None))
                self.assertEqual(namespace["file_hash"](Path("unrelated")), "outside-digest")
                with progress.lock:
                    self.assertEqual(progress.counts["hash_started"], 1)
            finally:
                release.set()
                thread.join(5)
                progress.close()
        self.assertEqual(errors, [])
        self.assertEqual(results, [1])
        self.assertFalse(thread.is_alive())
        self.assertFalse(progress.thread.is_alive())
        self.assertNotIn("private-path", output.getvalue())

    def test_checkpoints_keep_large_lazy_inventory_constant_size_and_record_bounded(self):
        profile = fresh_profile()
        profile.CHECKPOINT_INTERVAL = 100
        yielded = [0]
        def file_hash(path):
            return "digest"
        def inventory(path):
            for _ in range(275000):
                yielded[0] += 1
                yield namespace["_inventory_hash"](path)[0]
        namespace, _, _ = fixture_namespace({"iter_tree_inventory": inventory, "file_hash": file_hash})
        output = io.StringIO()
        with contextlib.redirect_stdout(output):
            progress = profile.install(namespace)
            iterator = namespace["iter_tree_inventory"](Path("sources"))
            self.assertEqual(yielded, [0])
            tracemalloc.start()
            try:
                for item in iterator:
                    self.assertEqual(item, "digest")
                _, peak = tracemalloc.get_traced_memory()
            finally:
                tracemalloc.stop()
            progress.close()
        records = decode_records(output.getvalue(), True)
        checkpoints = [r for r in records if r["op"] == 4]
        self.assertEqual(checkpoints[-1]["inventory_yielded"], 275000)
        self.assertEqual(checkpoints[-1]["hash_completed"], 275000)
        self.assertEqual(checkpoints[-1]["hash_completed_bytes"], 275000 * 6)
        self.assertEqual((progress.phases, progress.work), ([], []))
        self.assertEqual(len(progress.counts), 8)
        self.assertLess(peak, 2 * 1024 * 1024)
        self.assertLessEqual(len(records), 6)
        self.assertLessEqual(len(checkpoints), 512)
        self.assertTrue(all(len(json.dumps(record)) <= 1024 for record in records))
        self.assertNotIn("digest", output.getvalue())

    def test_checkpoint_observer_clock_thread_or_stdout_failures_preserve_original_results(self):
        for mode in ("clock", "thread", "stdout"):
            with self.subTest(mode=mode):
                profile = fresh_profile()
                namespace, _, sentinel = fixture_namespace()
                output = io.StringIO()
                with contextlib.redirect_stdout(output), contextlib.ExitStack() as stack:
                    progress = profile.install(namespace)
                    if mode == "clock":
                        stack.enter_context(mock.patch.object(profile.time, "monotonic_ns", side_effect=RuntimeError("private-clock")))
                    elif mode == "thread":
                        stack.enter_context(mock.patch.object(profile.threading.Thread, "start", side_effect=RuntimeError("private-thread")))
                    else:
                        original_emit = profile.emit
                        def emit(op, phase, category, **metrics):
                            if op == 4:
                                with mock.patch.object(sys, "stdout", mock.Mock(write=mock.Mock(side_effect=OSError("private-stdout")))):
                                    original_emit(op, phase, category, **metrics)
                            else:
                                original_emit(op, phase, category, **metrics)
                        stack.enter_context(mock.patch.object(profile, "emit", side_effect=emit))
                    self.assertEqual(list(namespace["iter_tree_inventory"](Path("sources"))), [("synthetic-path", "hash")])
                    progress.close()
                self.assertTrue(profile.invalid)
                records = decode_records(output.getvalue(), True)
                self.assertTrue(any(r["op"] == 3 for r in records) or profile.invalid)
                self.assertNotIn("private-", output.getvalue())

    def test_checkpoint_close_and_hash_error_retain_partial_progress_and_original_exception(self):
        for mode in ("partial close", "hash failure"):
            with self.subTest(mode=mode):
                profile = fresh_profile()
                fault = RuntimeError("private-original-hash")
                def file_hash(path):
                    if mode == "hash failure":
                        raise fault
                    return "digest"
                def inventory(path):
                    yield namespace["_inventory_hash"](path)[0]
                    yield namespace["_inventory_hash"](path)[0]
                namespace, _, _ = fixture_namespace({"iter_tree_inventory": inventory, "file_hash": file_hash})
                output = io.StringIO()
                with contextlib.redirect_stdout(output):
                    progress = profile.install(namespace)
                    iterator = namespace["iter_tree_inventory"](Path("sources"))
                    if mode == "hash failure":
                        with self.assertRaises(RuntimeError) as caught:
                            next(iterator)
                        self.assertIs(caught.exception, fault)
                    else:
                        self.assertEqual(next(iterator), "digest")
                        iterator.close()
                    progress.close()
                checkpoints = [r for r in decode_records(output.getvalue(), True) if r["op"] == 4]
                last = checkpoints[-1]
                self.assertEqual((last["inventory_yielded"], last["hash_started"], last["hash_completed"], last["hash_failed"], last["open_inventory"], last["active_inventory"], last["active_hash"]), (0 if mode == "hash failure" else 1, 1, 1, int(mode == "hash failure"), 0, 0, 0))
                exits = [r for r in decode_records(output.getvalue()) if r["op"] == 2]
                self.assertEqual(exits[-1]["failed"], 1)
                self.assertNotIn("private-original", output.getvalue())

    def test_finalized_timer_cannot_regress_an_inflight_checkpoint_after_lock_contention(self):
        profile = fresh_profile()
        clock = [1000000]
        attempted = threading.Event()
        class Gate:
            def __init__(self):
                self.inner = threading.RLock()
            def __enter__(self):
                if threading.current_thread().name == "finalize-fixture":
                    attempted.set()
                self.inner.acquire()
            def __exit__(self, *args):
                self.inner.release()
        progress = profile.Progress()
        PROFILES.append(progress)
        progress.lock = Gate()
        output = io.StringIO()
        with contextlib.redirect_stdout(output), mock.patch.object(profile.time, "monotonic_ns", side_effect=lambda: clock[0]):
            phase = progress.phase_begin("inventory")
            work = progress.work_begin("inventory")
            with progress.lock:
                clock[0] += 1000
                thread = threading.Thread(target=lambda: progress.work_end(work, yielded=True), name="finalize-fixture")
                thread.start()
                self.assertTrue(attempted.wait(5))
                clock[0] += 4000
                progress.publish()
                clock[0] += 1000
            thread.join(5)
            self.assertFalse(thread.is_alive())
            progress.phase_end(phase, False)
            progress.close()
        checkpoints = [r for r in decode_records(output.getvalue(), True) if r["op"] == 4]
        self.assertEqual([r["inventory_us"] for r in checkpoints], [5, 6, 6])
        self.assertEqual([r["inventory_yielded"] for r in checkpoints], [0, 1, 1])
        self.assertFalse(profile.invalid)

    def test_overlapping_hash_workers_use_wall_union_and_successful_bytes(self):
        profile = fresh_profile()
        clock = [1000000]
        progress = profile.Progress()
        PROFILES.append(progress)
        output = io.StringIO()
        with contextlib.redirect_stdout(output), mock.patch.object(profile.time, "monotonic_ns", side_effect=lambda: clock[0]):
            phase = progress.phase_begin("inventory")
            inventory = progress.work_begin("inventory")
            hashes = []
            for _ in range(4):
                clock[0] += 1000
                hashes.append(progress.work_begin("hash"))
            clock[0] += 1000
            progress.publish()
            clock[0] += 1000
            progress.work_end(hashes[1], completed_bytes=3)
            clock[0] += 1000
            progress.work_end(hashes[0], completed_bytes=5)
            clock[0] += 1000
            progress.work_end(hashes[3], failed=True)
            clock[0] += 1000
            progress.work_end(hashes[2], completed_bytes=7)
            clock[0] += 1000
            progress.work_end(inventory, yielded=True)
            progress.phase_end(phase, False)
            progress.close()
        checkpoints = [r for r in decode_records(output.getvalue(), True) if r["op"] == 4]
        self.assertEqual((checkpoints[0]["active_hash"], checkpoints[0]["active_inventory"], checkpoints[0]["hash_us"], checkpoints[0]["hash_completed_bytes"]), (4, 1, 4, 0))
        self.assertEqual({key: checkpoints[-1][key] for key in ("hash_us", "inventory_us", "inventory_residual_us", "hash_started", "hash_completed", "hash_failed", "hash_completed_bytes", "inventory_yielded")}, {"hash_us": 8, "inventory_us": 10, "inventory_residual_us": 2, "hash_started": 4, "hash_completed": 4, "hash_failed": 1, "hash_completed_bytes": 15, "inventory_yielded": 1})
        self.assertFalse(profile.invalid)

    def test_inventory_workers_need_no_tls_and_finish_before_consumer_suspends(self):
        profile = fresh_profile()
        entered, release = threading.Barrier(5), threading.Event()
        output = io.StringIO()
        errors, values, contexts = [], [], []
        def inventory_hash(path):
            contexts.append(getattr(profile.local, "inventory", None))
            entered.wait(5)
            if not release.wait(5):
                raise RuntimeError("fixture wait expired")
            return "digest", 7
        def inventory(path, deadline=None):
            self.assertEqual(deadline, 123)
            with concurrent.futures.ThreadPoolExecutor(max_workers=4) as executor:
                futures = [executor.submit(namespace["_inventory_hash"], path) for _ in range(4)]
                results = [future.result() for future in futures]
            for digest, _ in results:
                yield digest
        namespace, _, _ = fixture_namespace({"iter_tree_inventory": inventory, "_inventory_hash": inventory_hash})
        def consume():
            try:
                values.append(next(iterator))
            except BaseException as error:
                errors.append(error)
        with contextlib.redirect_stdout(output):
            progress = profile.install(namespace)
            iterator = namespace["iter_tree_inventory"](Path("sources"), deadline=123)
            thread = threading.Thread(target=consume)
            thread.start()
            try:
                entered.wait(5)
                progress.publish()
                partial = [r for r in decode_records(output.getvalue(), True) if r["op"] == 4][-1]
                self.assertEqual((partial["active_hash"], partial["active_inventory"], partial["hash_completed"], partial["hash_completed_bytes"]), (4, 1, 0, 0))
            finally:
                release.set()
                thread.join(5)
            progress.publish()
            suspended = [r for r in decode_records(output.getvalue(), True) if r["op"] == 4][-1]
            self.assertEqual((suspended["active_hash"], suspended["active_inventory"], suspended["open_inventory"], suspended["hash_completed"], suspended["hash_completed_bytes"], suspended["inventory_yielded"]), (0, 0, 1, 4, 28, 1))
            iterator.close()
            progress.close()
        self.assertEqual(errors, [])
        self.assertEqual(values, ["digest"])
        self.assertEqual(contexts, [None] * 4)
        self.assertFalse(thread.is_alive())
        self.assertFalse(profile.invalid)

    def test_prefetched_hash_work_is_limited_to_one_bounded_batch(self):
        for count in (16, 17):
            with self.subTest(count=count):
                profile = fresh_profile()
                progress = profile.Progress()
                PROFILES.append(progress)
                output = io.StringIO()
                with contextlib.redirect_stdout(output):
                    phase = progress.phase_begin("inventory")
                    inventory = progress.work_begin("inventory")
                    for _ in range(count):
                        work = progress.work_begin("hash")
                        progress.work_end(work, completed_bytes=6)
                    progress.publish()
                    self.assertEqual(profile.invalid, count == 17)
                    progress.work_end(inventory, yielded=True)
                    progress.phase_end(phase, False)
                    progress.close()
                records = decode_records(output.getvalue(), True)
                if count == 16:
                    first = next(record for record in records if record["op"] == 4)
                    self.assertEqual((first["hash_started"], first["hash_completed"], first["inventory_yielded"], first["hash_completed_bytes"]), (16, 16, 0, 96))
                else:
                    self.assertEqual([record["op"] for record in records], [3])

    def test_checkpoint_byte_schema_and_headers_are_strict_numeric(self):
        for mode in ("boolean phase", "boolean category", "missing bytes", "null bytes", "fractional bytes", "negative bytes", "oversized bytes"):
            with self.subTest(mode=mode):
                profile = fresh_profile()
                metrics = {key: 0 for key in profile.CHECKPOINT_LIMITS}
                metrics["sequence"] = 1
                phase = category = 0
                if mode == "boolean phase":
                    phase = False
                elif mode == "boolean category":
                    category = False
                elif mode == "missing bytes":
                    del metrics["hash_completed_bytes"]
                else:
                    metrics["hash_completed_bytes"] = {"null bytes": None, "fractional bytes": 0.5, "negative bytes": -1, "oversized bytes": (1 << 40) + 1}[mode]
                output = io.StringIO()
                with contextlib.redirect_stdout(output):
                    profile.emit(4, phase, category, **metrics)
                self.assertEqual(decode_records(output.getvalue(), True), [{"version": 1, "op": 3, "phase": 0, "category": 0}])

    def test_inventory_hash_metrics_reject_invalid_results_without_changing_them(self):
        for value in (("digest", True), ("digest", -1), ("digest", 1.5), ("digest", (1 << 40) + 1), ("digest", 1, 2), (object(), 1), "digest"):
            with self.subTest(kind=type(value).__name__):
                profile = fresh_profile()
                namespace, _, _ = fixture_namespace({"_inventory_hash": lambda *args, **kwargs: value})
                def inventory(path):
                    yield namespace["_inventory_hash"](path)
                namespace["iter_tree_inventory"] = inventory
                output = io.StringIO()
                with contextlib.redirect_stdout(output):
                    progress = profile.install(namespace)
                    iterator = namespace["iter_tree_inventory"](Path("sources"))
                    self.assertIs(next(iterator), value)
                    iterator.close()
                    progress.close()
                self.assertTrue(profile.invalid)
                self.assertEqual(progress.counts["hash_completed_bytes"], 0)
                self.assertNotIn("digest", output.getvalue())

    def test_inventory_hash_outside_advancement_invalidates_only_diagnostic(self):
        profile = fresh_profile()
        namespace, _, _ = fixture_namespace()
        original_file_hash = namespace["file_hash"]
        output = io.StringIO()
        with contextlib.redirect_stdout(output):
            progress = profile.install(namespace)
            self.assertIs(namespace["file_hash"], original_file_hash)
            self.assertEqual(namespace["file_hash"](Path("input")), "hash")
            self.assertFalse(profile.invalid)
            self.assertEqual(namespace["_inventory_hash"](Path("outside")), ("hash", 6))
            progress.close()
        self.assertTrue(profile.invalid)
        self.assertEqual(progress.counts["hash_started"], 0)
        self.assertEqual(progress.work, [])

    def test_unjoined_inventory_worker_invalidates_without_replacing_yield(self):
        profile = fresh_profile()
        entered, release = threading.Event(), threading.Event()
        errors = []
        def inventory_hash(path):
            entered.set()
            if not release.wait(5):
                raise RuntimeError("fixture wait expired")
            return "digest", 7
        def worker(path):
            try:
                namespace["_inventory_hash"](path)
            except BaseException as error:
                errors.append(error)
        def inventory(path):
            thread.start()
            if not entered.wait(5):
                raise RuntimeError("fixture worker did not start")
            yield "original-value"
        namespace, _, _ = fixture_namespace({"iter_tree_inventory": inventory, "_inventory_hash": inventory_hash})
        thread = threading.Thread(target=worker, args=(Path("sources"),))
        output = io.StringIO()
        with contextlib.redirect_stdout(output):
            progress = profile.install(namespace)
            iterator = namespace["iter_tree_inventory"](Path("sources"))
            try:
                self.assertEqual(next(iterator), "original-value")
                self.assertTrue(profile.invalid)
            finally:
                release.set()
                thread.join(5)
                iterator.close()
                progress.close()
        self.assertEqual(errors, [])
        self.assertFalse(thread.is_alive())

    def test_checkpoint_cap_is_independent_of_large_item_count(self):
        profile = fresh_profile()
        output = io.StringIO()
        with contextlib.redirect_stdout(output):
            progress = profile.Progress()
            frame = progress.phase_begin("inventory")
            PROFILES.append(progress)
            for _ in range(513):
                progress.publish()
            progress.phase_end(frame, False)
            progress.close()
        records = decode_records(output.getvalue(), True)
        self.assertEqual(sum(r["op"] == 4 for r in records), 512)
        self.assertEqual(sum(r["op"] == 3 for r in records), 1)
        self.assertTrue(profile.invalid)

    def test_actual_pinned_helper_receipt_bytes_tamper_links_and_fifo_are_preserved(self):
        helper_path = os.environ.get("REVIEW_SOURCE_PROFILE_REAL_HELPER")
        if not helper_path:
            self.skipTest("exact helper supplied separately; synthetic semantics tests still run")
        helper = Path(helper_path)
        source = helper.read_bytes()
        self.assertEqual(hashlib.sha256(source).hexdigest(), PIN)
        # Use exact helper functions, without its network/main entry point.
        for mode in ("canonical", "tampered", "link", "fifo"):
            with self.subTest(mode=mode), tempfile.TemporaryDirectory() as temporary:
                root = Path(temporary)
                source_dir = root / "sources"
                source_dir.mkdir()
                (source_dir / "a.py").write_bytes(b"source")
                (source_dir / "directory").mkdir()
                (source_dir / "directory/z.py").write_bytes(b"nested")
                baseline = types.ModuleType("unobserved_helper")
                baseline.__file__ = str(helper)
                exec(compile(source, str(helper), "exec"), baseline.__dict__)
                observed = types.ModuleType("observed_helper")
                observed.__file__ = str(helper)
                exec(compile(source, str(helper), "exec"), observed.__dict__)
                deadline = time.monotonic() + 10
                canonical = root / "canonical.json"
                self.assertEqual(baseline.write_receipt(canonical, {"fixture": "digest"}, source_dir, deadline), 2)
                expected = canonical.read_bytes()
                if mode == "tampered":
                    (source_dir / "a.py").write_bytes(b"mutated")
                elif mode == "link":
                    (source_dir / "linked").symlink_to(source_dir / "a.py")
                elif mode == "fifo":
                    os.mkfifo(source_dir / "fifo")
                profile = fresh_profile()
                output = io.StringIO()
                observed_receipt = root / "observed.json"
                original_run = subprocess.run
                with contextlib.redirect_stdout(output):
                    progress = profile.install(observed.__dict__)
                    self.assertIs(subprocess.run, original_run)
                    if mode in ("link", "fifo"):
                        with self.assertRaisesRegex(ValueError, "link or special file"):
                            observed.write_receipt(observed_receipt, {"fixture": "digest"}, source_dir, deadline)
                    else:
                        self.assertEqual(observed.write_receipt(observed_receipt, {"fixture": "digest"}, source_dir, deadline), 2)
                    progress.close()
                if mode == "canonical":
                    self.assertFalse(profile.invalid, "complete canonical helper profiling must remain valid")
                    self.assertEqual(observed_receipt.read_bytes(), expected)
                elif mode == "tampered":
                    self.assertNotEqual(observed_receipt.read_bytes(), expected)
                    self.assertEqual(json.loads(observed_receipt.read_bytes())["files"]["a.py"], hashlib.sha256(b"mutated").hexdigest())
                checkpoints = [r for r in decode_records(output.getvalue(), True) if r["op"] == 4]
                self.assertTrue(checkpoints)
                if mode in ("canonical", "tampered"):
                    self.assertEqual(checkpoints[-1]["hash_completed_bytes"], 12 if mode == "canonical" else 13)
                    self.assertEqual((checkpoints[-1]["hash_completed"], checkpoints[-1]["hash_failed"], checkpoints[-1]["active_hash"]), (2, 0, 0))
                self.assertNotIn(str(root), output.getvalue())
                self.assertNotIn("a.py", output.getvalue())

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
