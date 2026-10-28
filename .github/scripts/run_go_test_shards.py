#!/usr/bin/env python3
"""Run every database test in bounded, deterministic sequential root-test shards."""

import argparse
import json
from pathlib import Path
import re
import subprocess
import sys

from merge_go_coverage import merge_profiles

ROOT_NAME = re.compile(r"(?:Test|Fuzz|Example)(?:[A-Z0-9_][A-Za-z0-9_]*)?")
BENCHMARK_NAME = re.compile(r"Benchmark(?:[A-Z0-9_][A-Za-z0-9_]*)?")
SUMMARY = re.compile(r"ok\s+\S+\s+\S+(?:\s+.*)?")
PACKAGE = "./internal/db"


def discover_roots(output):
    roots = []
    summaries = 0
    for line in output.splitlines():
        if ROOT_NAME.fullmatch(line):
            roots.append(line)
        elif BENCHMARK_NAME.fullmatch(line):
            # Ordinary go test does not run benchmarks.
            continue
        elif SUMMARY.fullmatch(line):
            summaries += 1
        else:
            raise ValueError("unexpected database test discovery output")
    if not roots or len(roots) != len(set(roots)) or summaries != 1:
        raise ValueError("database discovery must contain unique test roots and one package summary")
    return sorted(roots)


def partition_roots(roots, count):
    if count < 1 or len(roots) < count or len(roots) != len(set(roots)):
        raise ValueError("every database shard must have distinct, nonempty test roots")
    return [sorted(roots)[index::count] for index in range(count)]


def root_pattern(roots):
    if not roots or any(ROOT_NAME.fullmatch(name) is None for name in roots):
        raise ValueError("refusing an empty or invalid database test selection")
    return "^(?:" + "|".join(re.escape(name) for name in roots) + ")$"


def exit_status(status):
    return status if status >= 0 else 128 - status


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--shards", type=int, default=4)
    parser.add_argument("--diagnostics", type=Path, required=True)
    parser.add_argument("--coverprofile", type=Path, required=True)
    parser.add_argument("--covermode", choices=("count", "atomic"), required=True)
    parser.add_argument("--timeout", choices=("300s", "360s"), required=True)
    parser.add_argument("--race", action="store_true")
    args = parser.parse_args()
    args.diagnostics.mkdir(parents=True, exist_ok=True)
    manifest = {"discovery_status": None, "test_status": 0, "capture_status": 0,
                "coverage_status": "not_run", "shards": []}

    def retain_manifest():
        try:
            (args.diagnostics / "shard-status.json").write_text(json.dumps(manifest, indent=2) + "\n")
        except OSError:
            manifest["capture_status"] = 1
            print("::warning::Could not retain database shard statuses", file=sys.stderr)

    try:
        args.coverprofile.unlink(missing_ok=True)
        discovery_command = ["go", "test", "-list", ".", PACKAGE]
        if args.race:
            discovery_command.append("-race")
        discovery = subprocess.run(discovery_command, capture_output=True, text=True, check=False)
        sys.stderr.write(discovery.stderr)
        manifest["discovery_status"] = exit_status(discovery.returncode)
        if discovery.returncode != 0:
            retain_manifest()
            return manifest["discovery_status"]
        groups = partition_roots(discover_roots(discovery.stdout), args.shards)
    except (OSError, ValueError):
        manifest["discovery_status"] = 1
        retain_manifest()
        print("::error::Unable to discover a complete nonempty database shard plan", file=sys.stderr)
        return 1

    plans = []
    for index, roots in enumerate(groups, 1):
        profile = args.diagnostics / f"coverage-shard-{index}.out"
        command = ["go", "test", "-json", PACKAGE, "-parallel=2", "-count=1",
                   "-coverpkg=./internal/...", f"-coverprofile={profile}",
                   f"-covermode={args.covermode}", f"-timeout={args.timeout}",
                   "-run", root_pattern(roots)]
        if args.race:
            command.append("-race")
        entry = {"index": index, "roots": roots, "root_count": len(roots), "exit_status": None,
                 "profile": profile.name,
                 "command": [f"-coverprofile={profile.name}" if arg.startswith("-coverprofile=") else arg
                             for arg in command]}
        manifest["shards"].append(entry)
        plans.append((command, profile, entry))
    retain_manifest()
    for command, profile, entry in plans:
        try:
            profile.unlink(missing_ok=True)
            result = subprocess.run(command, check=False)
            entry["exit_status"] = exit_status(result.returncode)
        except OSError:
            entry["exit_status"] = 127
            print(f"::error::Unable to execute database shard {entry['index']}", file=sys.stderr)
        if not manifest["test_status"] and entry["exit_status"]:
            manifest["test_status"] = entry["exit_status"]
        retain_manifest()

    if manifest["test_status"]:
        manifest["coverage_status"] = "skipped_failed_tests"
    else:
        try:
            merged = merge_profiles([profile for _, profile, _ in plans])
            args.coverprofile.write_text(merged)
            manifest["coverage_status"] = 0
        except (OSError, ValueError):
            manifest["coverage_status"] = 1
            print("::error::Unable to combine complete database shard coverage", file=sys.stderr)
    retain_manifest()
    return manifest["test_status"] or manifest["capture_status"] or (manifest["coverage_status"] != 0)


if __name__ == "__main__":
    sys.exit(main())
