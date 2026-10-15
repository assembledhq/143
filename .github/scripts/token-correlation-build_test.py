#!/usr/bin/env python3
"""Focused offline checks: no Docker, network, credentials or production I/O."""
import hashlib
import importlib.util
import io
import json
import os
from pathlib import Path
import subprocess
import tarfile
import tempfile
import unittest
from unittest.mock import patch

spec = importlib.util.spec_from_file_location("build_helper", Path(__file__).with_name("token-correlation-build.py"))
helper = importlib.util.module_from_spec(spec)
spec.loader.exec_module(helper)


class BuildHelperTests(unittest.TestCase):
    def test_owned_container_accepts_only_the_two_exact_image_identity_forms(self):
        cid, name, run_id = "a" * 64, "owned-helper", "123"
        manifest, config = "sha256:" + "b" * 64, "sha256:" + "c" * 64
        immutable = "ghcr.io/assembledhq/143-sandbox@" + manifest
        cases = [("config digest", config, "config_digest"),
                 ("manifest digest", manifest, "manifest_digest"),
                 ("wrong digest", "sha256:" + "d" * 64, None)]
        for label, identity, expected in cases:
            with self.subTest(label=label):
                row = {"Id": cid, "Name": "/" + name, "Image": identity,
                       "Config": {"Image": immutable, "Labels": {"143.build.run": run_id}},
                       "State": {"Status": "created", "Running": False, "Pid": 0}}
                if expected is None:
                    with self.assertRaisesRegex(RuntimeError, "container identity differs"):
                        helper.validate_helper_container(row, cid, name, immutable, config, run_id)
                else:
                    self.assertEqual(helper.validate_helper_container(row, cid, name, immutable, config, run_id), expected)

    def test_only_definite_tag_absence_allows_publication(self):
        tag = "ghcr.io/assembledhq/143-server:" + "a" * 40
        cases = [
            ("missing manifest", 1, b"ERROR: manifest unknown", True),
            ("exact missing tag", 1, ("ERROR: " + tag + ": not found").encode(), True),
            ("existing", 0, b"", False),
            ("credential helper missing", 1, b"executable file not found", False),
            ("auth despite missing text", 1, b"manifest unknown: unauthorized", False),
            ("transport", 1, b"TLS connection timeout", False),
            ("ambiguous status", 1, b"unexpected HTTP status 404", False),
            ("empty failure", 1, b"", False),
        ]
        for name, code, error, accepted in cases:
            with self.subTest(name=name):
                result = subprocess.CompletedProcess([], code, b"", error)
                if accepted:
                    helper.require_absent(result, tag)
                else:
                    with self.assertRaises(RuntimeError):
                        helper.require_absent(result, tag)

    def test_helper_bytes_are_hashed_without_extraction(self):
        value = b"synthetic helper bytes"
        stream = io.BytesIO()
        with tarfile.open(fileobj=stream, mode="w") as archive:
            member = tarfile.TarInfo("143-tools")
            member.size = len(value)
            archive.addfile(member, io.BytesIO(value))
        stream.seek(0)
        self.assertEqual(helper.read_helper_archive(stream), hashlib.sha256(value).hexdigest())

    def test_helper_archive_rejects_aliases_and_extra_members(self):
        cases = [("../143-tools", False, False), ("143-tools", True, False), ("143-tools", False, True)]
        for name, symlink, extra in cases:
            with self.subTest(name=name, symlink=symlink, extra=extra):
                stream = io.BytesIO()
                with tarfile.open(fileobj=stream, mode="w") as archive:
                    member = tarfile.TarInfo(name)
                    if symlink:
                        member.type, member.linkname = tarfile.SYMTYPE, "elsewhere"
                        archive.addfile(member)
                    else:
                        member.size = 1
                        archive.addfile(member, io.BytesIO(b"x"))
                    if extra:
                        other = tarfile.TarInfo("extra")
                        other.size = 1
                        archive.addfile(other, io.BytesIO(b"y"))
                stream.seek(0)
                with self.assertRaises(RuntimeError):
                    helper.read_helper_archive(stream)

    def test_workflow_source_identity_is_independent_from_release(self):
        release, commit = "a" * 40, "b" * 40
        branch = "refs/heads/codex/build-token-correlation-" + release[:12]
        with tempfile.TemporaryDirectory() as temporary:
            workspace = Path(temporary)
            config = workspace / "workflow-source/.github/token-correlation-release.json"
            config.parent.mkdir(parents=True)
            config.write_text(json.dumps({"release_sha": release, "release_tree": "c" * 40,
                                          "recipes": dict.fromkeys(helper.RECIPES, "d" * 64)}))
            environment = {"GITHUB_WORKSPACE": temporary, "GITHUB_ACTIONS": "true", "GITHUB_REPOSITORY": helper.REPOSITORY,
                           "GITHUB_EVENT_NAME": "push", "GITHUB_REF": branch, "GITHUB_RUN_ATTEMPT": "1",
                           "GITHUB_RUN_ID": "123", "GITHUB_SHA": commit, "GITHUB_WORKFLOW_SHA": commit,
                           "GITHUB_WORKFLOW_REF": helper.REPOSITORY + "/" + helper.WORKFLOW + "@" + branch}
            cases = [("valid", {}, True), ("workflow confusion", {"GITHUB_WORKFLOW_SHA": release}, False),
                     ("wrong branch", {"GITHUB_REF": "refs/heads/main"}, False),
                     ("rerun", {"GITHUB_RUN_ATTEMPT": "2"}, False),
                     ("wrong event", {"GITHUB_EVENT_NAME": "pull_request"}, False)]
            for name, changes, accepted in cases:
                with self.subTest(name=name), patch.dict(os.environ, environment | changes, clear=True), \
                        patch.object(helper, "command", side_effect=lambda args, cwd=None: commit if args[1] == "rev-parse" else ""):
                    if accepted:
                        self.assertEqual(helper.binding()[0]["release_sha"], release)
                    else:
                        with self.assertRaises(RuntimeError):
                            helper.binding()


if __name__ == "__main__":
    unittest.main()
