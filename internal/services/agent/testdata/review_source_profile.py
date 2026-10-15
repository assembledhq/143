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
MAX_CHECKPOINTS = 512
MAX_ACTIVE = 64
CHECKPOINT_INTERVAL = 2
# Cumulative operation sums can overlap across the bounded active workers.
MAX_SUM_US = MAX_ACTIVE * 480000000
CHECKPOINT_LIMITS = {
    "sequence": MAX_CHECKPOINTS, "elapsed_us": 480000000,
    "inventory_us": MAX_SUM_US, "inventory_residual_us": MAX_SUM_US,
    "hash_us": MAX_SUM_US, "receipt_us": MAX_SUM_US,
    "receipt_outside_inventory_us": MAX_SUM_US,
    "inventory_yielded": 1000000, "hash_started": 1000000,
    "hash_completed": 1000000, "hash_failed": 1000000,
    "receipt_started": 1000000, "receipt_completed": 1000000,
    "receipt_failed": 1000000, "open_inventory": MAX_ACTIVE,
    "active_inventory": MAX_ACTIVE, "active_hash": MAX_ACTIVE,
    "active_receipt": MAX_ACTIVE,
}
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
                if type(op) is not int or op not in (1, 2, 3, 4):
                    raise ValueError
                if op == 3:
                    invalid = True
                elif op == 4:
                    if phase != 0 or category != 0 or set(metrics) != set(CHECKPOINT_LIMITS):
                        raise ValueError
                    for key, value in metrics.items():
                        if type(value) is not int or not 0 <= value <= CHECKPOINT_LIMITS[key]:
                            raise ValueError
                    if metrics["sequence"] == 0:
                        raise ValueError
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


def diagnostic(method):
    @functools.wraps(method)
    def guarded(*args, **kwargs):
        try:
            return method(*args, **kwargs)
        except BaseException:
            # Diagnostic failures must not replace a helper result or exception.
            invalidate()
            return None
    return guarded


class Progress:
    """Constant-size cumulative counters, including in-flight work at checkpoints.

    elapsed_us starts at the first inventory/receipt phase, not helper startup;
    it is an observation clock and never a renewed execution deadline.
    Inventory residual includes traversal/stat/control and observer overhead.
    Receipt residual includes all work outside inventory advancement, including
    serialization, writes, deadline/control calls and observer overhead. Neither
    is an exclusive allocation measurement or a pure syscall/JSON timer.
    """
    def __init__(self):
        self.lock = threading.Lock()
        self.stop = threading.Event()
        self.thread = None
        self.closed = False
        self.origin = None
        self.sequence = 0
        self.phases = []
        self.work = []
        self.inventory_ns = self.hash_ns = self.receipt_ns = 0
        self.receipt_inventory_ns = 0
        self.counts = {key: 0 for key in ("inventory_yielded", "hash_started", "hash_completed", "hash_failed", "receipt_started", "receipt_completed", "receipt_failed")}

    @diagnostic
    def phase_begin(self, kind):
        with self.lock:
            now = time.monotonic_ns()
            if len(self.phases) >= MAX_ACTIVE:
                raise ValueError
            if self.origin is None:
                self.origin = now
            frame = (kind, now)
            self.phases.append(frame)
            if kind == "receipt":
                self.counts["receipt_started"] += 1
            if self.thread is None:
                self.thread = threading.Thread(target=self.publish_periodically, daemon=True)
                self.thread.start()
            return frame

    @diagnostic
    def work_begin(self, kind, in_receipt=False):
        with self.lock:
            now = time.monotonic_ns()
            if len(self.work) >= MAX_ACTIVE:
                raise ValueError
            frame = (kind, now, in_receipt)
            self.work.append(frame)
            if kind == "hash":
                self.counts["hash_started"] += 1
            return frame

    @diagnostic
    def work_end(self, frame, failed=False, yielded=False):
        if frame is None:
            return
        with self.lock:
            now = time.monotonic_ns()
            self.work.remove(frame)
            elapsed = now - frame[1]
            if elapsed < 0:
                raise ValueError
            if frame[0] == "inventory":
                self.inventory_ns += elapsed
                if frame[2]:
                    self.receipt_inventory_ns += elapsed
                self.counts["inventory_yielded"] += int(yielded)
            else:
                self.hash_ns += elapsed
                self.counts["hash_completed"] += 1
                self.counts["hash_failed"] += int(failed)

    @diagnostic
    def phase_end(self, frame, failed):
        if frame is None:
            return
        with self.lock:
            now = time.monotonic_ns()
            self.phases.remove(frame)
            if frame[0] == "receipt":
                elapsed = now - frame[1]
                if elapsed < 0:
                    raise ValueError
                self.receipt_ns += elapsed
                self.counts["receipt_completed"] += 1
                self.counts["receipt_failed"] += int(failed)
        self.publish()

    @diagnostic
    def publish(self):
        with self.lock:
            if self.origin is None:
                return
            if self.sequence >= MAX_CHECKPOINTS:
                raise ValueError
            now = time.monotonic_ns()
            inventory, hashing, receipt = self.inventory_ns, self.hash_ns, self.receipt_ns
            receipt_inventory = self.receipt_inventory_ns
            active_inventory = active_hash = active_receipt = open_inventory = 0
            for kind, started, in_receipt in self.work:
                elapsed = now - started
                if elapsed < 0:
                    raise ValueError
                if kind == "inventory":
                    active_inventory += 1
                    inventory += elapsed
                    if in_receipt:
                        receipt_inventory += elapsed
                else:
                    active_hash += 1
                    hashing += elapsed
            for kind, started in self.phases:
                if kind == "receipt":
                    active_receipt += 1
                    receipt += now - started
                else:
                    open_inventory += 1
            if min(now - self.origin, inventory - hashing, receipt - receipt_inventory) < 0:
                raise ValueError
            self.sequence += 1
            # Round once, after accumulation/subtraction in monotonic nanoseconds.
            emit(4, 0, 0, sequence=self.sequence, elapsed_us=(now - self.origin) // 1000,
                 inventory_us=inventory // 1000, inventory_residual_us=(inventory - hashing) // 1000,
                 hash_us=hashing // 1000, receipt_us=receipt // 1000,
                 receipt_outside_inventory_us=(receipt - receipt_inventory) // 1000,
                 active_inventory=active_inventory, active_hash=active_hash,
                 active_receipt=active_receipt, open_inventory=open_inventory, **self.counts)

    def publish_periodically(self):
        while not self.stop.wait(CHECKPOINT_INTERVAL):
            self.publish()

    @diagnostic
    def close(self):
        with self.lock:
            if self.closed:
                return
            self.closed = True
        self.stop.set()
        if self.thread is not None:
            self.thread.join(timeout=1)
            if self.thread.is_alive():
                raise RuntimeError
        self.publish()


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
    progress = Progress()
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
            frame = progress.phase_begin("receipt") if name == "write_receipt" else None
            previous_receipt = getattr(local, "receipt", None)
            if frame is not None:
                if previous_receipt is not None:
                    invalidate()
                local.receipt = frame
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
                if name == "write_receipt":
                    local.receipt = previous_receipt
                    progress.phase_end(frame, failed)
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
            frame = progress.phase_begin("inventory")
            failed = True
            items = 0

            class Counted:
                def __iter__(self):
                    return self

                def observed(self, operation, *values, count=True):
                    nonlocal items
                    previous = getattr(local, "inventory", None)
                    if previous is not None:
                        invalidate()
                    work = progress.work_begin("inventory", getattr(local, "receipt", None) is not None)
                    local.inventory = work
                    yielded = False
                    try:
                        value = operation(*values)
                        if count:
                            items += 1
                            yielded = True
                        return value
                    finally:
                        local.inventory = previous
                        progress.work_end(work, yielded=yielded)

                def __next__(self):
                    return self.observed(iterator.__next__)

                def send(self, value):
                    return self.observed(iterator.send, value)

                def throw(self, *values):
                    return self.observed(iterator.throw, *values)

                def close(self):
                    return self.observed(iterator.close, count=False)

            try:
                result = yield from Counted()
                failed = False
                return result
            finally:
                # A close before exhaustion is partial, even when close itself
                # succeeds (for example, a receipt deadline after one hash).
                progress.phase_end(frame, failed)
                end(8, cat, started, failed, items=items)

        return measured()

    namespace["iter_tree_inventory"] = inventory
    original_hash = namespace["file_hash"]

    @functools.wraps(original_hash)
    def file_hash(*args, **kwargs):
        # The TLS marker exists only during generator advancement, never while
        # its consumer is suspended or while input/Go manifest hashes run.
        if getattr(local, "inventory", None) is None:
            return original_hash(*args, **kwargs)
        frame = progress.work_begin("hash")
        failed = True
        try:
            value = original_hash(*args, **kwargs)
            failed = False
            return value
        finally:
            progress.work_end(frame, failed)

    namespace["file_hash"] = file_hash

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
    # Replace only the helper module binding; never mutate subprocess.run
    # process-wide (or Path/json methods used by unrelated callers).
    class SubprocessView:
        def __getattr__(self, name):
            return run if name == "run" else getattr(original_subprocess, name)

    original_subprocess = namespace["subprocess"]
    namespace["subprocess"] = SubprocessView()
    return progress


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
    progress = None
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
        progress = install(module.__dict__)
        return module.main()
    finally:
        if progress is not None:
            progress.close()
        sys.argv, sys.path = original_argv, original_path


if __name__ == "__main__":
    main()
