#!/usr/bin/env python3
"""Verify one local acquisition archive; never sends a request or releases data."""
import argparse
import base64
import hashlib
import json
import os
from pathlib import Path
import sys

MAX_RECORDS = 20_000
MAX_PAGE_BYTES = 512 * 1024 * 1024
MAX_MANIFEST_BYTES = 8 * 1024 * 1024


class InvalidArchive(ValueError):
    pass


def check(condition, message):
    if not condition:
        raise InvalidArchive(message)


def local_file(directory, name):
    check(isinstance(name, str) and name not in ("", ".", "..") and Path(name).name == name,
          "archive entries must be plain local filenames")
    path = directory / name
    check(not path.is_symlink() and path.is_file(), "archive entry is missing or a symlink")
    return path


def read_bounded(path, limit):
    with path.open("rb") as stream:
        raw = stream.read(limit + 1)
    check(len(raw) <= limit, "archive file exceeds verifier bound")
    return raw


def cursor_position(cursor, acquisition_id, position):
    check(isinstance(cursor, str) and 0 < len(cursor) <= 1024, "invalid cursor in chain")
    try:
        decoded = json.loads(base64.b64decode(cursor + "=" * (-len(cursor) % 4), altchars=b"-_", validate=True))
    except (ValueError, UnicodeError) as exc:
        raise InvalidArchive("invalid cursor encoding") from exc
    check(decoded == {"acquisition_id": acquisition_id, "position": position}, "cursor position or acquisition differs")


def verify(manifest_path, sums_path):
    """Return the exact release body only after all bytes and chain checks pass."""
    directory = manifest_path.parent
    manifest_raw = read_bounded(local_file(directory, manifest_path.name), MAX_MANIFEST_BYTES)
    manifest = json.loads(manifest_raw)
    check(manifest.get("version") == 1, "unsupported archive manifest version")
    acquisition = manifest["acquisition"]
    check(acquisition["schema_version"] == 1, "unsupported export schema version")
    total = acquisition["counts"]["records"]
    check(type(total) is int and 0 <= total <= MAX_RECORDS, "invalid acquisition record count")
    check(acquisition["filters"]["include_unresolved_publication_time"] is True, "unresolved publication evidence must be retained")
    pages = manifest["pages"]
    check(isinstance(pages, list) and 1 <= len(pages) <= max(1, total), "invalid bounded page count")
    sums_raw = read_bounded(sums_path, MAX_MANIFEST_BYTES).decode("ascii")
    sums = {}
    for line in sums_raw.splitlines():
        digest, name = line.split("  ", 1)
        check(len(digest) == 64 and all(c in "0123456789abcdef" for c in digest), "invalid checksum encoding")
        check(name not in sums, "duplicate checksum filename")
        sums[name] = digest
    expected_files = [p["file"] for p in pages] + [manifest_path.name]
    check(len(set(expected_files)) == len(expected_files) and set(sums) == set(expected_files), "checksum inventory differs from manifest")
    check(sums[manifest_path.name] == hashlib.sha256(manifest_raw).hexdigest(), "manifest checksum mismatch")
    identities = set()
    position, cursor = 0, None
    for index, entry in enumerate(pages):
        check(entry["request_cursor"] == cursor, "cursor chain is broken")
        if cursor is not None:
            cursor_position(cursor, acquisition["id"], position)
        raw = read_bounded(local_file(directory, entry["file"]), MAX_PAGE_BYTES)
        digest = hashlib.sha256(raw).hexdigest()
        check(digest == entry["sha256"] == sums[entry["file"]], "page checksum mismatch")
        page = json.loads(raw)
        check(page["acquisition"] == acquisition, "pages do not share one acquisition metadata object")
        records, meta = page["data"], page["meta"]
        check(isinstance(records, list) and len(records) <= 100, "page exceeds API row bound")
        check(meta["returned"] == len(records), "returned count differs from data")
        check(len(records) > 0 or (total == 0 and len(pages) == 1), "empty page cannot advance a nonempty archive")
        for record in records:
            identity = record["source_identity"]
            check(identity == record["raw"]["kind"] + ":" + record["raw"]["id"], "source identity differs from raw source")
            check(identity not in identities, "duplicate source within acquisition")
            identities.add(identity)
        position += len(records)
        check(meta["position"] == position and position <= total, "cumulative cursor position mismatch")
        complete = position == total
        check(meta["complete"] is complete, "completeness marker differs from count")
        cursor = meta["next_cursor"]
        if complete:
            check(cursor is None and index == len(pages) - 1, "terminal page must end chain")
        else:
            cursor_position(cursor, acquisition["id"], position)
    check(position == total and cursor is None, "archive is incomplete")
    return {"repository_id": acquisition["filters"]["repository_id"], "archive": {
        "manifest_sha256": hashlib.sha256(manifest_raw).hexdigest(),
        "archived_records": position, "final_position": position, "verified": True}}


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("--manifest", type=Path, required=True)
    parser.add_argument("--checksums", type=Path, required=True)
    parser.add_argument("--request", type=Path, required=True, help="new private file for release request; no network action")
    args = parser.parse_args()
    try:
        request = verify(args.manifest, args.checksums)
        # Exclusive creation avoids overwriting another acquisition's proof.
        fd = os.open(args.request, os.O_WRONLY | os.O_CREAT | os.O_EXCL, 0o600)
        with os.fdopen(fd, "w") as output:
            json.dump(request, output, indent=2)
            output.write("\n")
    except (OSError, ValueError, KeyError, TypeError):
        # Do not echo evidence, local filenames, or identifiers in error output.
        print("archive verification failed; no release request sent", file=sys.stderr)
        return 1
    print("archive verified; release request written; no release request sent")
    return 0


if __name__ == "__main__":
    sys.exit(main())
