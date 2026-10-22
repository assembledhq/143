#!/usr/bin/env python3
"""Check backend cache isolation and execute its actual toolchain/path discovery."""

import json
import os
from pathlib import Path
import re
import subprocess
import sys
import tempfile
import unittest


WORKFLOW = Path(__file__).resolve().parent.parent / "workflows" / "ci.yml"
GO_STUB = r'''import json
import os
import sys

fixture = json.loads(os.environ["GO_CACHE_FIXTURE"])
if len(sys.argv) != 3 or sys.argv[1] != "env" or sys.argv[2] not in fixture:
    sys.exit(1)
if sys.argv[2] == os.environ.get("GO_CACHE_FAIL"):
    sys.exit(1)
print(fixture[sys.argv[2]])
'''


def backend_job():
    match = re.search(r"^  backend-test:\n(.*?)(?=^  [a-z][a-z-]*:|\Z)",
                      WORKFLOW.read_text(), re.MULTILINE | re.DOTALL)
    if not match:
        raise AssertionError("Backend Test must remain in the CI workflow")
    return match.group(1)


def backend_step(name):
    match = re.search(rf"^      - name: {re.escape(name)}\n(.*?)(?=^      - |\Z)",
                      backend_job(), re.MULTILINE | re.DOTALL)
    if not match:
        raise AssertionError(f"Backend Test must contain {name}")
    return match.group(1)


def identify_cache(root, event, version="go1.26.3", arch="X64", image="ubuntu24", fail=""):
    stub = root / "go"
    stub.write_text(f"#!{sys.executable}\n{GO_STUB}")
    stub.chmod(0o700)
    fixture = {
        "GOVERSION": version,
        "GOCACHE": str(root / "build cache"),
        "GOMODCACHE": str(root / "module cache"),
    }
    output = root / "output"
    output.touch()
    step = backend_step("Identify backend Go cache")
    run = step.split("        run: |\n", 1)[1]
    script = "\n".join(line.removeprefix("          ") for line in run.splitlines())
    result = subprocess.run(
        ["bash", "-euo", "pipefail", "-c", script],
        env={
            **os.environ, "PATH": f"{root}:{os.environ['PATH']}",
            "GITHUB_EVENT_NAME": event, "RUNNER_OS": "Linux", "RUNNER_ARCH": arch,
            "ImageOS": image,
            "GO_CACHE_FIXTURE": json.dumps(fixture), "GO_CACHE_FAIL": fail,
            "GITHUB_OUTPUT": str(output),
        }, capture_output=True, text=True, timeout=10, check=False,
    )
    return result, output.read_text(), fixture


class BackendGoCacheTests(unittest.TestCase):
    def test_resolved_toolchain_paths_and_build_modes(self):
        cases = [
            ("pull_request", "go1.26.3", "X64", "ubuntu24", "count"),
            ("push", "go1.26.3", "X64", "ubuntu24", "race-atomic"),
            ("schedule", "go1.26.3", "X64", "ubuntu24", "race-atomic"),
            ("workflow_dispatch", "go1.26.3", "X64", "ubuntu24", "race-atomic"),
            ("pull_request", "go1.26.4", "X64", "ubuntu24", "count"),
            ("pull_request", "go1.26.3", "ARM64", "ubuntu24", "count"),
            ("pull_request", "go1.26.3", "X64", "ubuntu26", "count"),
        ]
        for event, version, arch, image, mode in cases:
            with self.subTest(event=event, version=version, arch=arch, image=image), tempfile.TemporaryDirectory() as directory:
                result, output, fixture = identify_cache(Path(directory), event, version, arch, image)
                self.assertEqual(result.returncode, 0, result.stderr)
                self.assertEqual(output, (
                    f"family=backend-go-v1-Linux-{arch}-{image}-{version}\n"
                    f"scope=backend-go-v1-Linux-{arch}-{image}-{version}-{mode}\n"
                    f"build={fixture['GOCACHE']}\nmodules={fixture['GOMODCACHE']}\n"
                ), "cache scope must track the actual toolchain and test mode, with resolved cache paths")

    def test_failed_go_environment_does_not_publish_partial_cache_configuration(self):
        for variable in ("GOVERSION", "GOCACHE", "GOMODCACHE"):
            with self.subTest(variable=variable), tempfile.TemporaryDirectory() as directory:
                result, output, _ = identify_cache(Path(directory), "push", fail=variable)
                self.assertNotEqual(result.returncode, 0, "Go environment discovery failures must stop cache setup")
                self.assertEqual(output, "", "failed discovery must not expose incomplete cache configuration")

    def test_workflow_keeps_backend_cache_separate_and_refreshes_warmed_outputs(self):
        job = backend_job()
        scope = "${{ steps.go-cache.outputs.scope }}"
        dependencies = "${{ hashFiles('go.mod', 'go.sum') }}"
        cache = backend_step("Cache backend Go modules and build outputs")
        setup = re.search(r"      - uses: actions/setup-go@v6\n(.*?)(?=^      - |\Z)",
                          job, re.MULTILINE | re.DOTALL)
        self.assertIsNotNone(setup, "Backend Test must set up Go before identifying its cache")
        self.assertIn("          cache: false\n", setup.group(1),
                      "lightweight jobs must not populate Backend Test's cache namespace")
        self.assertIn("        uses: actions/cache@v5\n", cache,
                      "the cache action must save warmed outputs in its successful-job post step")
        self.assertIn(f"          key: {scope}-{dependencies}-${{{{ github.sha }}}}\n", cache,
                      "dependency and commit changes must rotate immutable cache keys")
        self.assertIn(f"          restore-keys: |\n            {scope}-{dependencies}-\n            {scope}-\n"
                      "            ${{ steps.go-cache.outputs.family }}-\n", cache,
                      "restores must prefer the same build mode and reuse main's compatible backend cache for new PRs")
        self.assertIn(f"          path: |\n            ${{{{ steps.go-cache.outputs.build }}}}\n"
                      f"            ${{{{ steps.go-cache.outputs.modules }}}}\n", cache,
                      "both build objects and downloaded modules must survive between runs")
        self.assertNotIn("        if:", cache, "cache save must follow successful jobs without gating test execution")
        self.assertNotIn("lookup-only:", cache, "successful runs must write new warmed entries")
        reset = backend_step("Reset cached Go test results")
        self.assertEqual(re.findall(r"^        run: (.*)$", reset, re.MULTILINE), ["go clean -testcache"],
                         "restored test results must be expired without deleting compiled objects or modules")
        self.assertNotIn("        if:", reset, "test result invalidation must run for exact hits, fallbacks, and cache misses")
        self.assertLess(job.index("uses: actions/setup-go@v6"), job.index("name: Identify backend Go cache"))
        self.assertLess(job.index("name: Identify backend Go cache"), job.index("name: Cache backend Go modules and build outputs"))
        self.assertLess(job.index("name: Cache backend Go modules and build outputs"), job.index("name: Reset cached Go test results"))
        self.assertLess(job.index("name: Reset cached Go test results"), job.index("name: Test review dependency recipe"))
        self.assertLess(job.index("name: Reset cached Go test results"), job.index("name: Download Go modules"))


if __name__ == "__main__":
    unittest.main()
