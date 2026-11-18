#!/usr/bin/env python3
"""Offline archive inventory and raw selected-observation materialization.

Both operations mechanically read archive bytes. They are not authorization to
access a holdout. No credentials, provider calls, review execution or normalization.
"""
import argparse
from collections import defaultdict
import os
from pathlib import Path, PurePosixPath
import tempfile
import stat
import sys
import zipfile

import code_review_export_metadata as metadata
from code_review_export_metadata import Document, canonical, catalog_input, digest, inventory, keys, load, locator_key, need, project, read_relative, sha, validate_inventory
from code_review_export_selection import validate_selection

MAX_BUNDLE = 1024 * 1024 * 1024


def publish(output, write):
    """Publish one complete file atomically, without replacing existing paths."""
    output = Path(output)
    # An operator chooses an existing private parent; archive data cannot choose it.
    need(output.name not in ("", ".", "..") and output.parent.is_dir() and not output.parent.is_symlink())
    fd, temporary = tempfile.mkstemp(prefix=".custodian-", dir=output.parent)
    try:
        with os.fdopen(fd, "w+b") as stream:
            write(stream)
            stream.flush()
            os.fsync(stream.fileno())
        # link is atomic and exclusive on POSIX, including dangling symlink targets.
        os.link(temporary, output, follow_symlinks=False)
    finally:
        try:
            os.unlink(temporary)
        except OSError:
            print("custodian temporary-file cleanup failed", file=sys.stderr)


def materialize(root, catalog_raw, index_raw, graph_raw, protocol_raw, plan_raw, selection_raw, selection_sha256, output):
    need(sha(selection_raw) == digest(selection_sha256))
    index = validate_inventory(load(index_raw))
    need(index["catalog_sha256"] == sha(catalog_raw))
    need(index["original_catalog_sha256"] == load(catalog_raw)["original_catalog"]["sha256"])
    graph, plan, selection = load(graph_raw), load(plan_raw), load(selection_raw)
    selected = validate_selection(index, index_raw, graph, graph_raw, protocol_raw, plan, plan_raw, selection)
    wanted = {locator_key(row): row for row in index["observations"] if locator_key(row) in selected}
    manifests = list(catalog_input(root, catalog_raw))
    actual_headers = []
    for entry, manifest in manifests:
        a = manifest["acquisition"]
        actual_headers.append({"id": a["id"], "manifest_sha256": entry["manifest_sha256"],
                               "schema_version": a["schema_version"], "filters": a["filters"],
                               "acquired_at": a["acquired_at"], "expires_at": a["expires_at"], "consistency": a["consistency"]})
    need(sorted(actual_headers, key=lambda a: a["id"]) == index["acquisitions"])
    pages = {}
    for entry, manifest in manifests:
        a = manifest["acquisition"]
        for descriptor in manifest["pages"]:
            key = (a["id"], descriptor["sha256"])
            need(key not in pages)
            pages[key] = (str(PurePosixPath(entry["manifest"]).parent / descriptor["file"]), entry, manifest)
    need(all((key[0], key[1]) in pages for key in selected))
    selected_by_page = defaultdict(list)
    for key, row in wanted.items():
        selected_by_page[(key[0], key[1])].append(row)
    # Selection, protocol, graph and all assignments are validated BEFORE any page read.
    def write_bundle(stream):
        found, outputs, spent, output_bytes = set(), [], 0, 0
        page_keys = sorted(selected_by_page)
        with zipfile.ZipFile(stream, "w", compression=zipfile.ZIP_STORED, allowZip64=True) as bundle:
            for page_key in page_keys:
                name, entry, manifest = pages[page_key]
                raw = read_relative(root, name, metadata.MAX_PAGE)
                spent += len(raw)
                need(spent <= metadata.MAX_TOTAL_BYTES and sha(raw) == page_key[1])
                document = Document(raw)
                fields = document.members()
                keys(fields, ("data", "acquisition", "meta"))
                a = manifest["acquisition"]
                need(load(document.bytes(fields["acquisition"])) == a)
                spans = document.items(fields["data"])
                need(len(spans) <= 100)
                selected_rows = sorted(selected_by_page[page_key], key=locator_key)
                for row in selected_rows:
                    locator = row["locator"]
                    record_index = locator["record_index"]
                    need(record_index < len(spans))
                    span = spans[record_index]
                    actual = project(document, span, a["id"], entry["manifest_sha256"], page_key[1], record_index, a["filters"]["repository_id"])
                    need(actual == row)
                    payload = document.bytes(span)
                    output_bytes += len(payload)
                    need(output_bytes <= MAX_BUNDLE and len(outputs) < metadata.MAX_RECORDS)
                    member = f"observations/{len(outputs):06d}.json"
                    info = zipfile.ZipInfo(member, date_time=(1980, 1, 1, 0, 0, 0))
                    info.external_attr = (stat.S_IFREG | 0o600) << 16
                    bundle.writestr(info, payload)
                    outputs.append({"member": member, "locator": locator, "record_sha256": sha(payload),
                                    "record_bytes": len(payload), "source_sha256": row["source_sha256"]})
                    found.add(locator_key(row))
            need(found == selected)
            receipt = {"schema_version": 1, "ruleset": selection["ruleset"],
                       "partitions": selection["partitions"], "selection_sha256": sha(selection_raw), "catalog_sha256": sha(catalog_raw),
                       "original_catalog_sha256": index["original_catalog_sha256"],
                       "inventory_sha256": sha(index_raw), "stack_graph_sha256": sha(graph_raw),
                       "protocol_sha256": sha(protocol_raw), "partition_sha256": sha(plan_raw),
                       "acquisitions": index["acquisitions"], "observations": outputs}
            payload = canonical(receipt) + b"\n"
            need(len(payload) <= metadata.MAX_METADATA)
            info = zipfile.ZipInfo("receipt.json", date_time=(1980, 1, 1, 0, 0, 0))
            info.external_attr = (stat.S_IFREG | 0o600) << 16
            bundle.writestr(info, payload)
        need(stream.tell() <= MAX_BUNDLE)
    publish(output, write_bundle)


def read_input(path, bound=metadata.MAX_MANIFEST):
    path = Path(path)
    return read_relative(path.parent, path.name, bound)


def main(argv=None):
    parser = argparse.ArgumentParser(description=__doc__)
    subparsers = parser.add_subparsers(dest="operation", required=True)
    for operation in ("inventory", "materialize"):
        subparser = subparsers.add_parser(operation)
        subparser.add_argument("--archive-root", type=Path, required=True)
        subparser.add_argument("--catalog", type=Path, required=True)
        subparser.add_argument("--catalog-sha256", required=True)
        subparser.add_argument("--output", type=Path, required=True)
        subparser.add_argument("--acknowledge-mixed-page-read", action="store_true", required=True,
                               help="acknowledgment only; does not grant authorization to access data")
        if operation == "materialize":
            for name in ("inventory", "graph", "protocol", "partition", "selection"):
                subparser.add_argument("--" + name, type=Path, required=True)
            subparser.add_argument("--selection-sha256", required=True)
    args = parser.parse_args(argv)
    try:
        catalog_raw = read_input(args.catalog)
        need(sha(catalog_raw) == digest(args.catalog_sha256))
        if args.operation == "inventory":
            value = inventory(args.archive_root, catalog_raw)
            validate_inventory(value)
            payload = canonical(value) + b"\n"
            need(len(payload) <= metadata.MAX_METADATA)
            publish(args.output, lambda stream: stream.write(payload))
        else:
            materialize(args.archive_root, catalog_raw, read_input(args.inventory, metadata.MAX_METADATA), read_input(args.graph, metadata.MAX_METADATA),
                        read_input(args.protocol), read_input(args.partition, metadata.MAX_METADATA), read_input(args.selection, metadata.MAX_METADATA),
                        args.selection_sha256, args.output)
    except (OSError, ValueError, TypeError, KeyError, IndexError, OverflowError, RecursionError, UnicodeError, zipfile.BadZipFile):
        # Never include caught exceptions, payloads, IDs, paths or field values.
        print("custodian operation failed; no output published", file=sys.stderr)
        return 1
    print("custodian output published; no review normalization or network action")
    return 0


if __name__ == "__main__":
    raise SystemExit(main())
