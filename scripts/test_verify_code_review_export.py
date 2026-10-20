import base64
import copy
import hashlib
import json
from pathlib import Path
import tempfile
import unittest

from verify_code_review_export import InvalidArchive, verify


class VerifyExportTest(unittest.TestCase):
    def fixture(self, directory, mutation=None):
        acquisition = {"id": "10000000-0000-0000-0000-000000000001", "schema_version": 1,
                       "filters": {"repository_id": "20000000-0000-0000-0000-000000000001", "include_unresolved_publication_time": True},
                       "counts": {"records": 2}}
        cursor = base64.urlsafe_b64encode(json.dumps({"acquisition_id": acquisition["id"], "position": 1}).encode()).decode().rstrip("=")
        pages = []
        for i in range(2):
            source_id = "30000000-0000-0000-0000-%012d" % i
            pages.append({"acquisition": copy.deepcopy(acquisition), "data": [{"source_identity": "assessment:" + source_id,
                          "raw": {"kind": "assessment", "id": source_id}}],
                          "meta": {"returned": 1, "position": i + 1, "complete": i == 1, "next_cursor": cursor if i == 0 else None}})
        manifest = {"version": 1, "acquisition": acquisition, "pages": [
            {"file": "page-0.json", "request_cursor": None}, {"file": "page-1.json", "request_cursor": cursor}]}
        if mutation:
            mutation(pages, manifest)
        for page, entry in zip(pages, manifest["pages"]):
            raw = json.dumps(page).encode()
            (directory / entry["file"]).write_bytes(raw)
            entry["sha256"] = hashlib.sha256(raw).hexdigest()
        manifest_raw = json.dumps(manifest).encode()
        (directory / "archive-manifest.json").write_bytes(manifest_raw)
        sums = [entry["sha256"] + "  " + entry["file"] for entry in manifest["pages"]]
        sums.append(hashlib.sha256(manifest_raw).hexdigest() + "  archive-manifest.json")
        (directory / "SHA256SUMS").write_text("\n".join(sums) + "\n")
        return directory / "archive-manifest.json", directory / "SHA256SUMS"

    def test_lossless_archive_and_failures(self):
        tests = [
            ("complete", None, True),
            ("missing page", lambda p, m: (p.pop(), m["pages"].pop()), False),
            ("different acquisition", lambda p, m: p[1]["acquisition"].update(id="other"), False),
            ("duplicate source", lambda p, m: p[1].update(data=p[0]["data"]), False),
            ("position gap", lambda p, m: p[1]["meta"].update(position=3), False),
            ("changed request cursor", lambda p, m: m["pages"][1].update(request_cursor="wrong"), False),
            ("foreign cursor", lambda p, m: p[0]["meta"].update(next_cursor="wrong"), False),
            ("changed schema", lambda p, m: m["acquisition"].update(schema_version=2), False),
            ("false EOF", lambda p, m: p[1]["meta"].update(complete=False), False),
            ("bounds", lambda p, m: m["acquisition"]["counts"].update(records=20001), False),
        ]
        for name, mutation, valid in tests:
            with self.subTest(name=name), tempfile.TemporaryDirectory() as temp:
                manifest, sums = self.fixture(Path(temp), mutation)
                if valid:
                    expected = {"repository_id": "20000000-0000-0000-0000-000000000001", "archive": {
                        "manifest_sha256": hashlib.sha256(manifest.read_bytes()).hexdigest(), "archived_records": 2, "final_position": 2, "verified": True}}
                    self.assertEqual(expected, verify(manifest, sums))
                else:
                    with self.assertRaises(InvalidArchive):
                        verify(manifest, sums)

    def test_independent_byte_verification(self):
        for name in ("page-0.json", "archive-manifest.json"):
            with self.subTest(name=name), tempfile.TemporaryDirectory() as temp:
                directory = Path(temp)
                manifest, sums = self.fixture(directory)
                with (directory / name).open("ab") as output:
                    output.write(b" ")
                with self.assertRaises(InvalidArchive):
                    verify(manifest, sums)

    def test_empty_acquisition(self):
        def empty(pages, manifest):
            pages.pop()
            manifest["pages"].pop()
            manifest["acquisition"]["counts"]["records"] = 0
            pages[0]["acquisition"] = copy.deepcopy(manifest["acquisition"])
            pages[0]["data"] = []
            pages[0]["meta"] = {"returned": 0, "position": 0, "complete": True, "next_cursor": None}
        with tempfile.TemporaryDirectory() as temp:
            manifest, sums = self.fixture(Path(temp), empty)
            self.assertEqual(0, verify(manifest, sums)["archive"]["archived_records"])


if __name__ == "__main__":
    unittest.main()
