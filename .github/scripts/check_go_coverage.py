#!/usr/bin/env python3
"""Fail closed when a Go coverage report is invalid or below its minimum."""

import argparse
from decimal import Decimal
import re
import subprocess
import sys


def percentage(value):
    if not re.fullmatch(r"[0-9]+(?:\.[0-9]+)?", value):
        raise ValueError(f"invalid coverage percentage: {value!r}")
    result = Decimal(value)
    if not 0 <= result <= 100:
        raise ValueError(f"coverage percentage outside 0–100: {value!r}")
    return result


def total_coverage(report):
    # Function names and source paths can contain "total" (e.g. totalSize).
    # Only the standalone summary row describes the complete profile.
    totals = [line.strip() for line in report.splitlines() if line.split()[:1] == ["total:"]]
    if len(totals) != 1:
        raise ValueError("expected exactly one total: coverage summary")
    match = re.fullmatch(r"total:\s+\(statements\)\s+([0-9]+(?:\.[0-9]+)?)%", totals[0])
    if not match:
        raise ValueError("malformed total: coverage summary")
    return percentage(match.group(1))


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("profile")
    parser.add_argument("minimum")
    parser.add_argument("target")
    args = parser.parse_args()
    try:
        minimum = percentage(args.minimum)
        target = percentage(args.target)
        if minimum > target:
            raise ValueError("coverage target must be at least the minimum")
        report = subprocess.run(
            ["go", "tool", "cover", "-func", args.profile],
            check=True, capture_output=True, text=True,
        )
        coverage = total_coverage(report.stdout)
    except (OSError, ValueError, subprocess.CalledProcessError) as error:
        print(f"::error::Unable to verify backend coverage: {error}", file=sys.stderr)
        if isinstance(error, subprocess.CalledProcessError) and error.stderr:
            print(error.stderr, file=sys.stderr, end="")
        return 1

    print(f"Total coverage: {coverage}%")
    if coverage < minimum:
        print(f"::error::Backend coverage {coverage}% is below the minimum {minimum}%")
        return 1
    if coverage < target:
        print(f"::warning::Backend coverage {coverage}% is below target {target}%")
    return 0


if __name__ == "__main__":
    sys.exit(main())
