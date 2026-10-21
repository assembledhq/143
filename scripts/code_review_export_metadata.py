"""Bounded, allowlisted archive inventory. No outcome normalization or network IO."""
import base64
from datetime import datetime, timezone
import hashlib
import json
import os
from pathlib import Path, PurePosixPath
import re
import stat
import uuid

MAX_MANIFEST = 8 * 1024 * 1024
MAX_METADATA = 256 * 1024 * 1024
MAX_PAGE = 512 * 1024 * 1024
MAX_RECORDS = 100_000
MAX_ACQUISITIONS = 256
MAX_TOTAL_BYTES = 2 * 1024 * 1024 * 1024
MAX_DEPTH = 64
STRING_SPECIAL = re.compile(r'["\\\x00-\x1f]')
PRIMITIVE = re.compile(r"(?:null|true|false|-?(?:0|[1-9][0-9]*)(?:\.[0-9]+)?(?:[eE][+-]?[0-9]+)?)")


class InvalidCustody(ValueError):
    """Never contains input values, filenames, or parser diagnostics."""


def need(condition):
    if not condition:
        raise InvalidCustody("invalid custodian input")


def canonical(value):
    return json.dumps(value, sort_keys=True, separators=(",", ":"), ensure_ascii=True, allow_nan=False).encode()


def sha(raw):
    return hashlib.sha256(raw).hexdigest()


def digest(value):
    need(isinstance(value, str) and re.fullmatch(r"[0-9a-f]{64}", value) is not None)
    return value


def keys(value, expected):
    need(type(value) is dict and set(value) == set(expected))


def integer(value, minimum=0, maximum=MAX_RECORDS):
    need(type(value) is int and minimum <= value <= maximum)
    return value


def provider_number(value):
    if value is None:
        return None
    integer(value, -(2**63), 2**63 - 1)
    return value if value > 0 else None


def identity(value):
    need(isinstance(value, str))
    parsed = uuid.UUID(value)
    need(parsed.int != 0 and str(parsed) == value)
    return value


def timestamp(value):
    need(isinstance(value, str) and len(value) <= 40 and re.fullmatch(r"[0-9]{4}-[0-9]{2}-[0-9]{2}T[0-9]{2}:[0-9]{2}:[0-9]{2}(?:\.[0-9]{1,9})?(?:Z|[+-][0-9]{2}:[0-9]{2})", value) is not None)
    fraction = re.search(r"\.([0-9]+)", value)
    need(fraction is None or not fraction[1][6:].strip("0"))
    parsed = datetime.fromisoformat(value.replace("Z", "+00:00"))
    need(parsed.utcoffset() is not None)
    return parsed.astimezone(timezone.utc)


def git_sha(value):
    need(isinstance(value, str) and re.fullmatch(r"[0-9a-f]{40}(?:[0-9a-f]{24})?", value) is not None)
    return value


def text(value, maximum=512):
    need(isinstance(value, str) and 0 < len(value) <= maximum and all(ord(c) >= 32 for c in value))
    return value


def relative(value):
    text(value, 1024)
    p = PurePosixPath(value)
    need(not p.is_absolute() and str(p) == value and all(part not in (".", "..") for part in p.parts))
    return p.parts


def read_relative(root, name, bound):
    """Open every component without following symlinks, including the root."""
    parts = relative(name)
    directory = os.open(root, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW)
    try:
        for part in parts[:-1]:
            next_directory = os.open(part, os.O_RDONLY | os.O_DIRECTORY | os.O_NOFOLLOW, dir_fd=directory)
            os.close(directory)
            directory = next_directory
        fd = os.open(parts[-1], os.O_RDONLY | os.O_NOFOLLOW | os.O_NONBLOCK, dir_fd=directory)
        with os.fdopen(fd, "rb") as source:
            info = os.fstat(source.fileno())
            need(stat.S_ISREG(info.st_mode) and info.st_nlink == 1 and info.st_size <= bound)
            raw = source.read(bound + 1)
            need(len(raw) <= bound)
            return raw
    finally:
        os.close(directory)


class Document:
    """JSON framing with exact UTF-8 spans; opaque result strings are skipped.

    No nested outcome values are deserialized. Duplicate keys, invalid syntax,
    excessive nesting, nonfinite numbers and malformed escapes fail closed.
    """
    def __init__(self, raw):
        self.raw = raw
        self.data = raw.decode("utf-8")
        self.end = self.value(self.ws(0))
        need(self.ws(self.end) == len(self.data))

    def ws(self, i):
        while i < len(self.data) and self.data[i] in " \r\n\t":
            i += 1
        return i

    def string_end(self, i):
        need(i < len(self.data) and self.data[i] == '"')
        i += 1
        while i < len(self.data):
            match = STRING_SPECIAL.search(self.data, i)
            need(match is not None)
            i = match.start()
            char = self.data[i]
            if char == '"':
                return i + 1
            need(char == "\\" and i + 1 < len(self.data))
            escape = self.data[i + 1]
            if escape == "u":
                need(re.fullmatch(r"[0-9a-fA-F]{4}", self.data[i + 2:i + 6]) is not None)
                i += 6
            else:
                need(escape in '\\"/bfnrt')
                i += 2
        raise InvalidCustody("invalid custodian input")

    def value(self, i, depth=0):
        need(depth <= MAX_DEPTH and i < len(self.data))
        char = self.data[i]
        if char == '"':
            return self.string_end(i)
        if char in "{[":
            close = "}" if char == "{" else "]"
            i = self.ws(i + 1)
            need(i < len(self.data))
            seen = set()
            if self.data[i] == close:
                return i + 1
            while True:
                if char == "{":
                    end = self.string_end(i)
                    key = json.loads(self.data[i:end])
                    need(key not in seen)
                    seen.add(key)
                    i = self.ws(end)
                    need(i < len(self.data) and self.data[i] == ":")
                    i = self.ws(i + 1)
                i = self.ws(self.value(i, depth + 1))
                need(i < len(self.data))
                if self.data[i] == close:
                    return i + 1
                need(self.data[i] == ",")
                i = self.ws(i + 1)
        match = PRIMITIVE.match(self.data, i)
        need(match is not None)
        return match.end()

    def members(self, span=None):
        start, end = span or (self.ws(0), self.end)
        need(self.data[start] == "{")
        result = {}
        i = self.ws(start + 1)
        while self.data[i] != "}":
            key_end = self.string_end(i)
            key = json.loads(self.data[i:key_end])
            need(key not in result)
            i = self.ws(key_end)
            need(self.data[i] == ":")
            i = self.ws(i + 1)
            value_end = self.value(i)
            result[key] = (i, value_end)
            i = self.ws(value_end)
            if self.data[i] == "}":
                break
            need(self.data[i] == ",")
            i = self.ws(i + 1)
        need(i + 1 == end)
        return result

    def items(self, span):
        start, end = span
        need(self.data[start] == "[")
        result = []
        i = self.ws(start + 1)
        while self.data[i] != "]":
            value_end = self.value(i)
            result.append((i, value_end))
            i = self.ws(value_end)
            if self.data[i] == "]":
                break
            need(self.data[i] == ",")
            i = self.ws(i + 1)
        need(i + 1 == end)
        return result

    def bytes(self, span):
        return self.data[span[0]:span[1]].encode("utf-8")

    def scalar(self, fields, key):
        if key not in fields:
            return None
        span = fields[key]
        need(self.data[span[0]] not in "{[")
        return json.loads(self.bytes(span))


def load(raw):
    document = Document(raw)
    return json.loads(document.raw)


def locator_key(row):
    locator = row["locator"]
    return (locator["acquisition_id"], locator["page_sha256"], locator["record_index"], locator["source_identity"])


def nullable(value, validate):
    return None if value in (None, "") else validate(value)


IDENTITY_FIELDS = ("repository_id", "pull_request_id", "session_id", "metadata_id", "assessment_id", "source_assessment_id", "previous_assessment_id", "previous_published_assessment_id", "superseded_by_assessment_id", "superseded_by_session_id")
TEXT_FIELDS = ("head_sha", "base_sha", "base_ref", "submitted_commit_sha")
TIME_FIELDS = ("created_at", "completed_at", "superseded_at")


def project(document, span, acquisition_id, manifest_sha, page_sha, record_index, repository_id):
    record = document.members(span)
    source_identity = document.scalar(record, "source_identity")
    raw = document.members(record["raw"])
    kind = document.scalar(raw, "kind")
    need(kind in ("assessment", "legacy"))
    source_id = identity(document.scalar(raw, "id"))
    need(source_identity == kind + ":" + source_id)
    source_span = raw["source"]
    source = document.members(source_span)
    need(document.scalar(source, "id") == source_id)
    result = {"source_kind": kind, "source_id": source_id,
              "source_sha256": sha(document.bytes(source_span)), "record_sha256": sha(document.bytes(span)),
              "locator": {"acquisition_id": acquisition_id, "acquisition_manifest_sha256": manifest_sha,
                          "page_sha256": page_sha, "record_index": record_index, "source_identity": source_identity}}
    for key in IDENTITY_FIELDS:
        result[key] = nullable(document.scalar(source, key), identity)
    need(result["repository_id"] == repository_id)
    for key in TEXT_FIELDS:
        result[key] = nullable(document.scalar(source, key), text if key == "base_ref" else git_sha)
    for key in TIME_FIELDS:
        result[key] = nullable(document.scalar(source, key), lambda v: (timestamp(v), v)[1])
    result["generation"] = nullable(document.scalar(source, "generation"), lambda v: integer(v, 1, 2**63 - 1))
    result["provider_review_id"] = provider_number(document.scalar(source, "github_review_id"))
    key = "publication_key" if kind == "assessment" else "review_output_key"
    result["publication_key"] = nullable(document.scalar(source, key), text)
    publication = nullable(document.scalar(raw, "publication_time"), lambda v: (timestamp(v), v)[1])
    result["publication_time"] = publication
    result["publication_time_availability"] = "reconstructed" if publication else "unavailable"
    # Fixed provenance avoids copying arbitrary provider/body/error strings.
    result["publication_time_provenance"] = "frozen_export_binding" if publication else "not_bound_in_frozen_export"
    pr = document.members(raw["pull_request"]) if "pull_request" in raw and document.bytes(raw["pull_request"]) != b"null" else {}
    pr_id = nullable(document.scalar(pr, "id"), identity)
    need(pr_id is None or pr_id == result["pull_request_id"])
    captured_name = nullable(document.scalar(source, "repository_full_name"), text)
    mirrored_name = nullable(document.scalar(pr, "repository"), text)
    result["repository_full_name"] = captured_name or mirrored_name
    result["repository_name_availability"] = "recorded" if captured_name else "reconstructed" if mirrored_name else "unavailable"
    result["pr_number"] = provider_number(document.scalar(pr, "number"))
    result["stack_availability"] = "unavailable"
    result["legacy_alias_ids"] = []
    if "legacy_aliases" in raw:
        for alias_span in document.items(raw["legacy_aliases"]):
            alias = document.members(alias_span)
            result["legacy_alias_ids"].append(identity(document.scalar(alias, "id")))
    need(len(result["legacy_alias_ids"]) == len(set(result["legacy_alias_ids"])))
    result["legacy_alias_ids"].sort()
    return result


def catalog_input(root, catalog_raw):
    catalog = load(catalog_raw)
    keys(catalog, ("schema_version", "original_catalog", "acquisitions"))
    original = catalog["original_catalog"]
    keys(original, ("file", "sha256"))
    need(sha(read_relative(root, original["file"], MAX_MANIFEST)) == digest(original["sha256"]))
    need(type(catalog["schema_version"]) is int and catalog["schema_version"] == 1)
    entries = catalog["acquisitions"]
    need(type(entries) is list and 0 < len(entries) <= MAX_ACQUISITIONS)
    seen = set()
    for entry in entries:
        keys(entry, ("manifest", "manifest_sha256", "checksums", "checksums_sha256"))
        manifest_raw = read_relative(root, entry["manifest"], MAX_MANIFEST)
        need(sha(manifest_raw) == digest(entry["manifest_sha256"]))
        sums_raw = read_relative(root, entry["checksums"], MAX_MANIFEST)
        need(sha(sums_raw) == digest(entry["checksums_sha256"]))
        manifest = load(manifest_raw)
        keys(manifest, ("version", "acquisition", "pages"))
        need(type(manifest["version"]) is int and manifest["version"] == 1)
        acquisition = manifest["acquisition"]
        validate_acquisition(acquisition)
        acquisition_id = identity(acquisition["id"])
        need(acquisition_id not in seen)
        seen.add(acquisition_id)
        need(type(acquisition["schema_version"]) is int and acquisition["schema_version"] == 1)
        identity(acquisition["filters"]["repository_id"])
        timestamp(acquisition["acquired_at"])
        timestamp(acquisition["expires_at"])
        need(acquisition["consistency"] == "frozen_payloads_repeatable_read")
        need(acquisition["filters"]["include_unresolved_publication_time"] is True)
        total = integer(acquisition["counts"]["records"], 0, 20_000)
        pages = manifest["pages"]
        need(type(pages) is list and 1 <= len(pages) <= max(1, total))
        sums = {}
        for line in sums_raw.decode("ascii").splitlines():
            value, name = line.split("  ", 1)
            need(name not in sums and len(relative(name)) == 1)
            sums[name] = digest(value)
        names = [p["file"] for p in pages] + [PurePosixPath(entry["manifest"]).name]
        need(len(names) == len(set(names)) and set(names) == set(sums))
        need(sums[PurePosixPath(entry["manifest"]).name] == sha(manifest_raw))
        for page in pages:
            keys(page, ("file", "sha256", "request_cursor"))
            need(len(relative(page["file"])) == 1 and sums[page["file"]] == digest(page["sha256"]))
        yield entry, manifest


def cursor_check(value, acquisition_id, position):
    need(isinstance(value, str) and 0 < len(value) <= 1024)
    raw = base64.b64decode(value + "=" * (-len(value) % 4), altchars=b"-_", validate=True)
    need(load(raw) == {"acquisition_id": acquisition_id, "position": position})


def inventory(root, catalog_raw):
    rows, acquisitions, spent = [], [], 0
    for entry, manifest in catalog_input(root, catalog_raw):
        acquisition = manifest["acquisition"]
        acquisition_id = acquisition["id"]
        repository_id = acquisition["filters"]["repository_id"]
        seen = set()
        position, cursor = 0, None
        for page_entry in manifest["pages"]:
            need(page_entry["request_cursor"] == cursor)
            name = str(PurePosixPath(entry["manifest"]).parent / page_entry["file"])
            raw = read_relative(root, name, MAX_PAGE)
            spent += len(raw)
            need(spent <= MAX_TOTAL_BYTES and sha(raw) == page_entry["sha256"])
            document = Document(raw)
            page = document.members()
            keys(page, ("data", "acquisition", "meta"))
            need(load(document.bytes(page["acquisition"])) == acquisition)
            records = document.items(page["data"])
            need(len(records) <= 100 and (records or (acquisition["counts"]["records"] == 0 and len(manifest["pages"]) == 1)))
            for index, span in enumerate(records):
                row = project(document, span, acquisition_id, entry["manifest_sha256"], page_entry["sha256"], index, repository_id)
                source_identity = row["locator"]["source_identity"]
                need(source_identity not in seen)
                seen.add(source_identity)
                rows.append(row)
                need(len(rows) <= MAX_RECORDS)
            position += len(records)
            meta = load(document.bytes(page["meta"]))
            keys(meta, ("next_cursor", "returned", "position", "complete"))
            need(type(meta["returned"]) is int and meta["returned"] == len(records))
            need(type(meta["position"]) is int and meta["position"] == position <= acquisition["counts"]["records"])
            complete = position == acquisition["counts"]["records"]
            need(meta["complete"] is complete)
            cursor = meta["next_cursor"]
            if complete:
                need(cursor is None and page_entry is manifest["pages"][-1])
            else:
                cursor_check(cursor, acquisition_id, position)
        need(position == acquisition["counts"]["records"] and cursor is None)
        # No coverage, safeguard or other outcome-derived counts are copied.
        acquisitions.append({"id": acquisition_id, "manifest_sha256": entry["manifest_sha256"],
                             "schema_version": 1, "filters": acquisition["filters"],
                             "acquired_at": acquisition["acquired_at"], "expires_at": acquisition["expires_at"],
                             "consistency": acquisition["consistency"]})
    return validate_inventory({"schema_version": 1, "catalog_sha256": sha(catalog_raw),
            "original_catalog_sha256": load(catalog_raw)["original_catalog"]["sha256"],
            "acquisitions": sorted(acquisitions, key=lambda a: a["id"]),
            "observations": sorted(rows, key=locator_key)})


def validate_filters(filters):
    keys(filters, ("repository_id", "from", "until", "time_basis", "include_unresolved_publication_time"))
    identity(filters["repository_id"])
    start, until = timestamp(filters["from"]), timestamp(filters["until"])
    need(0 < (until - start).total_seconds() <= 366 * 86400)
    need(filters["time_basis"] in ("created", "completed", "created_or_completed", "published"))
    need(filters["include_unresolved_publication_time"] is True)


def validate_acquisition(acquisition):
    keys(acquisition, ("id", "schema_version", "filters", "acquired_at", "expires_at", "counts", "consistency"))
    identity(acquisition["id"])
    need(type(acquisition["schema_version"]) is int and acquisition["schema_version"] == 1)
    validate_filters(acquisition["filters"])
    need(timestamp(acquisition["expires_at"]) > timestamp(acquisition["acquired_at"]))
    need(acquisition["consistency"] == "frozen_payloads_repeatable_read")
    need(type(acquisition["counts"]) is dict)
    integer(acquisition["counts"]["records"], 0, 20_000)


OBSERVATION_FIELDS = set(IDENTITY_FIELDS + TEXT_FIELDS + TIME_FIELDS) | {
    "source_kind", "source_id", "source_sha256", "record_sha256", "locator", "generation",
    "provider_review_id", "publication_key", "publication_time", "publication_time_availability",
    "publication_time_provenance", "pr_number", "stack_availability", "legacy_alias_ids", "repository_full_name", "repository_name_availability"}
LOCATOR_FIELDS = {"acquisition_id", "acquisition_manifest_sha256", "page_sha256", "record_index", "source_identity"}
ACQUISITION_FIELDS = {"id", "manifest_sha256", "schema_version", "filters", "acquired_at", "expires_at", "consistency"}


def validate_inventory(value):
    keys(value, ("schema_version", "catalog_sha256", "original_catalog_sha256", "acquisitions", "observations"))
    need(type(value["schema_version"]) is int and value["schema_version"] == 1)
    digest(value["catalog_sha256"])
    digest(value["original_catalog_sha256"])
    need(type(value["acquisitions"]) is list and 0 < len(value["acquisitions"]) <= MAX_ACQUISITIONS)
    acquisitions = {}
    for acquisition in value["acquisitions"]:
        keys(acquisition, ACQUISITION_FIELDS)
        identifier = identity(acquisition["id"])
        need(identifier not in acquisitions)
        acquisitions[identifier] = acquisition
        digest(acquisition["manifest_sha256"])
        validate_acquisition({k: v for k, v in acquisition.items() if k != "manifest_sha256"} | {"counts": {"records": 0}})
    need(type(value["observations"]) is list and len(value["observations"]) <= MAX_RECORDS)
    seen = set()
    source_acquisitions = set()
    physical_locators = set()
    for row in value["observations"]:
        keys(row, OBSERVATION_FIELDS)
        locator = row["locator"]
        keys(locator, LOCATOR_FIELDS)
        a = acquisitions[identity(locator["acquisition_id"]) ]
        need(digest(locator["acquisition_manifest_sha256"]) == a["manifest_sha256"])
        digest(locator["page_sha256"])
        integer(locator["record_index"], 0, 99)
        need(row["source_kind"] in ("assessment", "legacy"))
        need(locator["source_identity"] == row["source_kind"] + ":" + identity(row["source_id"]))
        need(locator_key(row) not in seen)
        seen.add(locator_key(row))
        physical = locator_key(row)[:3]
        need(physical not in physical_locators)
        physical_locators.add(physical)
        source_key = (a["id"], locator["source_identity"])
        need(source_key not in source_acquisitions)
        source_acquisitions.add(source_key)
        digest(row["record_sha256"])
        digest(row["source_sha256"])
        for name in IDENTITY_FIELDS:
            need(row[name] is None or nullable(row[name], identity) == row[name])
        need(row["repository_id"] == a["filters"]["repository_id"])
        for name in TEXT_FIELDS:
            need(row[name] is None or nullable(row[name], text if name == "base_ref" else git_sha) == row[name])
        for name in TIME_FIELDS + ("publication_time",):
            if row[name] is not None:
                timestamp(row[name])
        for name in ("generation", "provider_review_id", "pr_number"):
            if row[name] is not None:
                integer(row[name], 1, 2**63 - 1)
        if row["publication_key"] is not None:
            text(row["publication_key"])
        known = row["publication_time"] is not None
        if known:
            need(row["created_at"] is not None)
            need(timestamp(row["created_at"]) <= timestamp(row["publication_time"]) <= timestamp(a["acquired_at"]))
        need(row["publication_time_availability"] == ("reconstructed" if known else "unavailable"))
        need(row["publication_time_provenance"] == ("frozen_export_binding" if known else "not_bound_in_frozen_export"))
        need(row["stack_availability"] == "unavailable")
        if row["repository_full_name"] is not None:
            text(row["repository_full_name"])
            need(row["repository_name_availability"] in ("recorded", "reconstructed"))
        else:
            need(row["repository_name_availability"] == "unavailable")
        aliases = row["legacy_alias_ids"]
        need(type(aliases) is list and len(aliases) <= 20_000 and aliases == sorted(set(aliases)))
        for alias in aliases:
            identity(alias)
    return value
