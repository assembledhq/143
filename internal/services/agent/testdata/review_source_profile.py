"""Acceptance-only numeric profiling for one exact source helper revision."""
import functools
import hashlib
import json
import os
from pathlib import Path
import stat
import sys
import threading
import time
import types

EXPECTED_SHA256 = "f7e372aef2449752093346c499ee5f2b38aa7c1ba92fa8d0221e5c50627dd7fd"
PREFIX = "143_SOURCE_PROFILE_V1 "
MAX_RECORDS = 20000
_dumps = json.dumps
lock = threading.Lock()
local = threading.local()
records = 0
invalid = False
invalid_emitted = False


def emit(op, phase, category, **metrics):
    global records, invalid, invalid_emitted
    try:
        with lock:
            if records >= MAX_RECORDS or invalid_emitted:
                return
            try:
                if type(op) is not int or op not in (1, 2, 3):
                    raise ValueError
                if op == 3:
                    invalid = True
                elif type(phase) is not int or not 1 <= phase <= 10 or type(category) is not int or not 1 <= category <= 4:
                    raise ValueError
                if op == 1 and metrics:
                    raise ValueError
                if op == 2:
                    if set(metrics) - {"duration_us", "failed", "items", "bytes"} or not {"duration_us", "failed"} <= set(metrics):
                        raise ValueError
                    for key, value in metrics.items():
                        maximum = {"duration_us": 480000000, "failed": 1, "items": 1000000, "bytes": 1073741824}[key]
                        if type(value) is not int or not 0 <= value <= maximum:
                            raise ValueError
            except BaseException:
                invalid = True
            if records == MAX_RECORDS - 1:
                invalid = True
            value = {"version": 1, "op": 3, "phase": 0, "category": 0} if invalid else {"version": 1, "op": op, "phase": phase, "category": category, **metrics}
            records += 1
            try:
                sys.stdout.write(PREFIX + _dumps(value, separators=(",", ":")) + "\n")
                sys.stdout.flush()
                invalid_emitted = invalid
            except BaseException:
                invalid = True
    except BaseException:
        invalid = True


def invalidate():
    emit(3, 0, 0)


def begin(phase, category):
    emit(1, phase, category)
    try:
        return time.monotonic_ns()
    except BaseException:
        invalidate()
        return None


def end(phase, category, started, failed, **metrics):
    try:
        duration = (time.monotonic_ns() - started) // 1000
        emit(2, phase, category, duration_us=duration, failed=int(failed), **metrics)
    except BaseException:
        invalidate()


def argument(args, kwargs, position, name):
    return args[position] if len(args) > position else kwargs[name]


def category(args, kwargs, mode):
    if mode == "download":
        url = argument(args, kwargs, 0, "url")
        if type(url) is not str:
            raise ValueError
        if url.startswith("https://registry.npmjs.org/"):
            return 2
        if url.startswith(("https://pypi.org/", "https://files.pythonhosted.org/", "https://download.pytorch.org/", "https://download-r2.pytorch.org/")):
            return 3
        return 1
    if mode == "extract":
        name = argument(args, kwargs, 1, "destination").parent.name
    elif mode == "path":
        name = argument(args, kwargs, 0, "path").name
    else:
        return 1
    return {"npm": 2, "python": 3, "go": 4}.get(name, 1)


def install(namespace):
    def wrap(name, phase, mode=None):
        original = namespace[name]

        @functools.wraps(original)
        def observed(*args, **kwargs):
            try:
                cat = category(args, kwargs, mode)
            except BaseException:
                invalidate()
                cat = 1
            started = begin(phase, cat)
            failed = True
            metrics = {}
            try:
                result = original(*args, **kwargs)
                failed = False
                try:
                    if name in ("selected_artifacts", "input_hashes"):
                        if type(result) not in (dict, list):
                            raise ValueError
                        metrics["items"] = len(result)
                    elif name == "write_receipt":
                        if type(result) is not int:
                            raise ValueError
                        metrics["items"] = result
                    elif name == "check_source_capacity":
                        metrics = {"items": result["entries"], "bytes": result["path_bytes"]}
                    elif name == "read_once":
                        target = args[3] if len(args) > 3 else kwargs.get("target")
                        if type(result) is bytes:
                            metrics["bytes"] = len(result)
                        elif target is not None:
                            metrics["bytes"] = target.tell()
                except BaseException:
                    invalidate()
                    metrics = {}
                return result
            finally:
                end(phase, cat, started, failed, **metrics)
        namespace[name] = observed

    for name, phase, mode in (("selected_artifacts", 1, None), ("read_once", 2, "download"),
                              ("extract", 3, "extract"), ("input_hashes", 6, None),
                              ("check_source_capacity", 7, None), ("write_receipt", 9, None),
                              ("remove_tree", 10, "path")):
        wrap(name, phase, mode)

    original_inventory = namespace["iter_tree_inventory"]

    @functools.wraps(original_inventory)
    def inventory(*args, **kwargs):
        # Bind arguments now, while the original generator body stays lazy.
        iterator = original_inventory(*args, **kwargs)

        def measured():
            try:
                cat = category(args, kwargs, "path")
            except BaseException:
                invalidate()
                cat = 1
            started = begin(8, cat)
            failed = True
            items = 0

            class Counted:
                def __iter__(self):
                    return self

                def observed(self, operation, *values):
                    nonlocal items
                    value = operation(*values)
                    items += 1
                    return value

                def __next__(self):
                    return self.observed(iterator.__next__)

                def send(self, value):
                    return self.observed(iterator.send, value)

                def throw(self, *values):
                    return self.observed(iterator.throw, *values)

                def close(self):
                    return iterator.close()

            try:
                result = yield from Counted()
                failed = False
                return result
            finally:
                # A close before exhaustion is partial, even when close itself
                # succeeds (for example, a receipt deadline after one hash).
                end(8, cat, started, failed, items=items)

        return measured()

    namespace["iter_tree_inventory"] = inventory

    original_additional = namespace["prepare_additional"]
    original_run = namespace["subprocess"].run

    def stop_processing(frame, failed=False):
        started = frame["started"]
        frame["started"] = None
        if started is not None:
            end(5, 4, started, failed)

    @functools.wraps(original_additional)
    def additional(*args, **kwargs):
        if not hasattr(local, "frames"):
            local.frames = []
        frame = {"started": begin(5, 4)}
        local.frames.append(frame)
        failed = True
        try:
            result = original_additional(*args, **kwargs)
            failed = False
            return result
        finally:
            stop_processing(frame, failed)
            local.frames.pop()

    @functools.wraps(original_run)
    def run(*args, **kwargs):
        command = args[0] if args else kwargs.get("args")
        frames = getattr(local, "frames", [])
        if not frames or type(command) is not list or command[:4] != ["go", "mod", "download", "-json"]:
            return original_run(*args, **kwargs)
        frame = frames[-1]
        stop_processing(frame)
        started = begin(4, 4)
        failed = True
        try:
            result = original_run(*args, **kwargs)
            failed = False
            return result
        finally:
            end(4, 4, started, failed)
            if not failed:
                frame["started"] = begin(5, 4)

    namespace["prepare_additional"] = additional
    namespace["subprocess"].run = run


def main():
    try:
        if sys.argv[1:2] != [".143/review-sources.py"] or sys.argv[2:] not in ([], ["--verify"]):
            raise RuntimeError("unsupported acceptance profile invocation")
        helper = Path(".143/review-sources.py")
        if helper.resolve() != Path.cwd() / helper:
            raise RuntimeError("acceptance profile helper must not be redirected")
        descriptor = os.open(helper, os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK)
        with os.fdopen(descriptor, "rb") as source:
            metadata = os.fstat(source.fileno())
            if not stat.S_ISREG(metadata.st_mode) or metadata.st_size > 1048576:
                raise RuntimeError("acceptance profile helper must be a bounded regular file")
            data = source.read(1048577)
        if len(data) > 1048576 or hashlib.sha256(data).hexdigest() != EXPECTED_SHA256:
            raise RuntimeError("acceptance profile helper revision is unsupported")
    except BaseException:
        invalidate()
        raise
    original_argv, original_path = sys.argv, sys.path
    sys.argv = sys.argv[1:]
    sys.path = [str(helper.resolve().parent), *sys.path[1:]]
    try:
        module = types.ModuleType("_143_acceptance_profile_target")
        module.__file__ = str(helper.resolve())
        started = begin(6, 1)
        failed = True
        try:
            exec(compile(data, module.__file__, "exec"), module.__dict__)
            failed = False
        finally:
            end(6, 1, started, failed)
        install(module.__dict__)
        return module.main()
    finally:
        sys.argv, sys.path = original_argv, original_path


if __name__ == "__main__":
    main()
