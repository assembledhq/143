#!/usr/bin/env python3
"""Combine Go profiles, counting each backend statement once across test suites."""

import argparse
from pathlib import Path
import re
import sys

BLOCK = re.compile(r"(.+):(\d+)\.(\d+),(\d+)\.(\d+) (\d+) (\d+)")


def merge_profiles(paths):
    mode = None
    blocks = {}
    for path in paths:
        with Path(path).open() as profile:
            header = profile.readline().strip()
            if header not in {"mode: count", "mode: atomic", "mode: set"}:
                raise ValueError(f"{path}: invalid coverage mode")
            if mode is not None and header != mode:
                raise ValueError("coverage profiles must use the same mode")
            mode = header
            found = False
            for line_number, line in enumerate(profile, 2):
                match = BLOCK.fullmatch(line.rstrip("\n"))
                if match is None:
                    raise ValueError(f"{path}:{line_number}: invalid coverage block")
                file, start_line, start_col, end_line, end_col, statements, count = match.groups()
                start = (int(start_line), int(start_col))
                end = (int(end_line), int(end_col))
                if min(*start, *end) < 1 or start > end:
                    raise ValueError(f"{path}:{line_number}: invalid coverage range")
                key = f"{file}:{start_line}.{start_col},{end_line}.{end_col}"
                statements, count = int(statements), int(count)
                previous_statements, previous_count = blocks.get(key, (statements, 0))
                if statements != previous_statements:
                    raise ValueError(f"{path}:{line_number}: inconsistent statement count")
                if mode == "mode: set" and count > 1:
                    raise ValueError(f"{path}:{line_number}: invalid set-mode count")
                merged_count = max(previous_count, count) if mode == "mode: set" else previous_count + count
                blocks[key] = (statements, merged_count)
                found = found or statements > 0
            if not found:
                raise ValueError(f"{path}: profile has no statements")
    if mode is None:
        raise ValueError("at least one coverage profile is required")
    return mode + "\n" + "".join(
        f"{key} {statements} {count}\n"
        for key, (statements, count) in sorted(blocks.items())
    )


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("output")
    parser.add_argument("profiles", nargs="+")
    args = parser.parse_args()
    try:
        if Path(args.output).resolve() in {Path(p).resolve() for p in args.profiles}:
            raise ValueError("output must not overwrite an input profile")
        # Validate every input before touching output. Any failure exits nonzero
        # before the floor check can use incomplete evidence.
        merged = merge_profiles(args.profiles)
        Path(args.output).write_text(merged)
    except (OSError, ValueError) as error:
        print(f"::error::Unable to combine backend coverage: {error}", file=sys.stderr)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(main())
