#!/usr/bin/env python3
"""Run the actual Security Scan steps against synthetic hosted tool caches."""

import json
import os
from pathlib import Path
import re
import shutil
import stat
import subprocess
import sys
import tempfile
import unittest


SCRIPTS = Path(__file__).resolve().parent
WORKFLOW = SCRIPTS.parent / "workflows" / "ci.yml"
BOOTSTRAP = SCRIPTS / "select-security-go.py"
GO_STUB = r'''import json
import os
from pathlib import Path
import sys

root = Path(__file__).resolve().parents[1]
config = json.loads((root / 'fixture.json').read_text())
with Path(os.environ['TRACE']).open('a') as output:
    output.write(json.dumps({'tool': 'go', 'argv': sys.argv[1:], 'root': str(root), 'toolchain': os.environ.get('GOTOOLCHAIN'), 'goenv': os.environ.get('GOENV'), 'gobin': os.environ.get('GOBIN')}) + '\n')
if os.environ.get('GOTOOLCHAIN') != 'local' or os.environ.get('GOENV') != 'off':
    sys.exit(83)
if sys.argv[1:] == ['version']:
    print('go version go' + config['version'] + ' linux/' + config['arch'])
elif sys.argv[1:] == ['env', '-json', 'GOROOT', 'GOOS', 'GOARCH', 'GOHOSTOS', 'GOHOSTARCH']:
    print(json.dumps({'GOROOT': config.get('goroot', str(root)), 'GOOS': 'linux', 'GOARCH': config['arch'], 'GOHOSTOS': 'linux', 'GOHOSTARCH': config['arch']}))
elif sys.argv[1:] == ['install', 'github.com/securego/gosec/v2/cmd/gosec@v2.22.4']:
    gobin = Path(os.environ['GOBIN'])
    if (gobin.stat().st_mode & 0o777) != 0o700:
        sys.exit(84)
    scanner = gobin / 'gosec'
    scanner.write_text('#!' + sys.executable + '\n' + config['scanner'])
    scanner.chmod(0o700)
else:
    sys.exit(85)
'''
GOSEC_STUB = r'''import json
import os
from pathlib import Path
import sys
with Path(os.environ['TRACE']).open('a') as output:
    output.write(json.dumps({'tool': 'gosec', 'argv': sys.argv[1:], 'binary': str(Path(__file__).resolve()), 'toolchain': os.environ.get('GOTOOLCHAIN'), 'gobin': os.environ.get('GOBIN')}) + '\n')
if sys.argv[1:] != ['./...']:
    sys.exit(86)
sys.exit(int(os.environ.get('SCANNER_STATUS', '0')))
'''


def security_job():
    text = WORKFLOW.read_text()
    match = re.search(r"^  security:\n(.*?)(?=^  [a-z][a-z-]*:|\Z)", text, re.MULTILINE | re.DOTALL)
    if not match:
        raise AssertionError("the actual workflow must contain the Security Scan job")
    return match.group(1)


def workflow_run(name):
    lines = security_job().splitlines()
    start = lines.index(f"      - name: {name}")
    scalar = lines[start + 1]
    if not scalar.startswith("        run: "):
        raise AssertionError("the actual Security Scan step must execute a run scalar")
    value = scalar.removeprefix("        run: ")
    if value != "|":
        return value
    result = []
    for line in lines[start + 2:]:
        if line and not line.startswith("          "):
            break
        result.append(line[10:] if line else "")
    return "\n".join(result) + "\n"


class SecurityGoBootstrapTests(unittest.TestCase):
    def setUp(self):
        self.directory = tempfile.TemporaryDirectory()
        self.addCleanup(self.directory.cleanup)
        self.root = Path(self.directory.name).resolve()
        self.repo = self.root / "repo"
        scripts = self.repo / ".github" / "scripts"
        scripts.mkdir(parents=True)
        shutil.copyfile(BOOTSTRAP, scripts / BOOTSTRAP.name)
        (self.repo / "go.mod").write_text("module example.com/fixture\n\ngo 1.26.0\n")
        self.cache = self.root / "toolcache"
        (self.cache / "go").mkdir(parents=True)
        self.runner_temp = self.root / "runner-temp"
        self.runner_temp.mkdir()
        self.ambient = self.root / "ambient"
        self.ambient.mkdir()
        for name in ("go", "gosec", "curl", "wget"):
            path = self.ambient / name
            path.write_text(
                f"#!{sys.executable}\nimport json, os, sys\nfrom pathlib import Path\n"
                "with Path(os.environ['TRACE']).open('a') as output:\n"
                f"    output.write(json.dumps({{'tool': {name!r}, 'argv': sys.argv[1:]}}) + '\\n')\n"
                "sys.exit(79)\n"
            )
            path.chmod(0o755)
        self.env_file = self.root / "github-env"
        self.path_file = self.root / "github-path"
        self.env_file.touch()
        self.path_file.touch()
        self.trace = self.root / "trace.jsonl"
        self.env = {
            **os.environ,
            "RUNNER_OS": "Linux", "RUNNER_ARCH": "X64", "GO_VERSION": "1.26",
            "RUNNER_TOOL_CACHE": str(self.cache), "RUNNER_TEMP": str(self.runner_temp),
            "GITHUB_ENV": str(self.env_file), "GITHUB_PATH": str(self.path_file),
            "TRACE": str(self.trace), "PATH": f"{self.ambient}:{os.environ['PATH']}",
            # A poisoned ambient Go environment must not select another toolchain.
            "GOTOOLCHAIN": "go1.99.0+auto", "GOENV": str(self.root / "ambient-goenv"),
            "GOROOT": str(self.ambient), "GOBIN": str(self.ambient),
        }

    def cached_go(self, label="1.26.3", actual=None, cache_arch="x64", go_arch="amd64", complete=True):
        root = self.cache / "go" / label / cache_arch
        (root / "bin").mkdir(parents=True)
        (root / "fixture.json").write_text(json.dumps({"version": actual or label, "arch": go_arch, "scanner": GOSEC_STUB}))
        binary = root / "bin" / "go"
        binary.write_text(f"#!{sys.executable}\n" + GO_STUB)
        binary.chmod(0o755)
        if complete:
            root.with_suffix(".complete").touch()
        return root

    def bootstrap(self):
        return subprocess.run(["bash", "-euo", "pipefail", "-c", workflow_run("Select cached Go")],
                              cwd=self.repo, env=self.env, text=True, capture_output=True, timeout=10, check=False)

    def next_step_env(self):
        env = dict(self.env)
        for line in self.env_file.read_text().splitlines():
            key, value = line.split("=", 1)
            env[key] = value
        env["PATH"] = ":".join(reversed(self.path_file.read_text().splitlines())) + ":" + env["PATH"]
        return env

    def calls(self):
        return [json.loads(line) for line in self.trace.read_text().splitlines()] if self.trace.exists() else []

    def assert_closed(self):
        result = self.bootstrap()
        self.assertNotEqual(result.returncode, 0, "invalid hosted cache evidence must fail the actual bootstrap step")
        self.assertEqual(self.env_file.read_text(), "", "failed validation must not export a toolchain")
        self.assertEqual(self.path_file.read_text(), "", "failed validation must not change downstream PATH")
        self.assertNotIn(str(self.root), result.stderr, "public failures must not disclose runner-local paths")
        self.assertFalse(any(call['argv'][:1] == ['install'] for call in self.calls()), "invalid cache evidence must never install a scanner")

    def test_actual_security_workflow_keeps_scan_contract(self):
        job = security_job()
        self.assertIn("runs-on: ubuntu-latest", job)
        self.assertEqual(job.count("uses:"), 1, "only checkout remains an action dependency in Security Scan")
        self.assertNotIn("setup-go", job, "Security Scan must not download the optional setup action")
        self.assertNotIn("continue-on-error", job)
        self.assertNotIn("if:", workflow_run("Run gosec"))
        self.assertEqual(workflow_run("Select cached Go"), "python3 -B .github/scripts/select-security-go.py")
        self.assertIn("go install github.com/securego/gosec/v2/cmd/gosec@v2.22.4\n", workflow_run("Run gosec"))
        self.assertEqual(workflow_run("Run gosec").strip().splitlines()[-1], "gosec ./...")
        self.assertEqual(WORKFLOW.read_text().count("uses: actions/setup-go@v6"), 4, "other jobs retain their setup-go actions")
        self.assertIn("python3 -B -m unittest discover -s .github/scripts -p 'test_*.py'", WORKFLOW.read_text(), "CI must execute these helper tests")

    def test_numeric_patch_selection_and_next_step_scanner_visibility(self):
        self.cached_go("1.26.9")
        selected = self.cached_go("1.26.10")
        self.cached_go("1.26.11", complete=False)
        self.cached_go("1.27.0")
        self.cached_go("1.26.99-rc1")
        self.cached_go("1.26.010")
        result = self.bootstrap()
        self.assertEqual(result.returncode, 0, result.stderr)
        self.assertEqual(result.stdout, "Selected cached Go 1.26.10 (linux/amd64).\n")
        env = self.next_step_env()
        self.assertEqual(env["GOROOT"], str(selected))
        self.assertEqual(env["GOTOOLCHAIN"], "local")
        self.assertEqual(env["GOENV"], "off")
        self.assertEqual(Path(env["GOBIN"]).parent, self.runner_temp)
        self.assertEqual(stat.S_IMODE(Path(env["GOBIN"]).stat().st_mode), 0o700)
        scan = subprocess.run(["bash", "-euo", "pipefail", "-c", workflow_run("Run gosec")],
                              cwd=self.repo, env=env, text=True, capture_output=True, timeout=10, check=False)
        self.assertEqual(scan.returncode, 0, scan.stderr)
        calls = self.calls()
        self.assertEqual([(call["tool"], call["argv"]) for call in calls], [
            ("go", ["version"]), ("go", ["env", "-json", "GOROOT", "GOOS", "GOARCH", "GOHOSTOS", "GOHOSTARCH"]),
            ("go", ["install", "github.com/securego/gosec/v2/cmd/gosec@v2.22.4"]), ("gosec", ["./..."]),
        ], "the exact workflow must install the pinned scanner with the selected Go and scan every package")
        self.assertEqual(calls[-1]["binary"], str(Path(env["GOBIN"]) / "gosec"))
        self.assertTrue(all(call["toolchain"] == "local" for call in calls))

    def test_scanner_failure_remains_a_failure(self):
        self.cached_go()
        self.assertEqual(self.bootstrap().returncode, 0)
        env = {**self.next_step_env(), "SCANNER_STATUS": "42"}
        result = subprocess.run(["bash", "-euo", "pipefail", "-c", workflow_run("Run gosec")], cwd=self.repo,
                                env=env, capture_output=True, text=True, timeout=10, check=False)
        self.assertEqual(result.returncode, 42, "a scanner finding or failure must fail the actual scan step")

    def test_no_acceptable_cache_fails_without_fallback(self):
        cases = ["empty", "wrong minor", "prerelease", "malformed", "incomplete", "old patch"]
        for name in cases:
            with self.subTest(name=name):
                shutil.rmtree(self.cache / "go")
                (self.cache / "go").mkdir()
                if name == "wrong minor": self.cached_go("1.25.9")
                if name == "prerelease": self.cached_go("1.26.1-rc1")
                if name == "malformed": self.cached_go("1.26.01")
                if name == "incomplete": self.cached_go(complete=False)
                if name == "old patch":
                    self.cached_go("1.26.1")
                    (self.repo / "go.mod").write_text("module example.com/fixture\n\ngo 1.26.2\n")
                self.assert_closed()
        self.assertEqual(self.calls(), [], "a cache miss must not invoke ambient Go or any network fallback")

    def test_cache_evidence_mismatch_fails_closed(self):
        cases = ["binary version", "binary architecture", "goroot", "marker contents", "marker symlink", "not executable", "binary escape", "directory escape"]
        for name in cases:
            with self.subTest(name=name):
                shutil.rmtree(self.cache / "go")
                (self.cache / "go").mkdir()
                root = self.cached_go(actual="1.26.2" if name == "binary version" else None,
                                      go_arch="arm64" if name == "binary architecture" else "amd64")
                marker = root.with_suffix(".complete")
                if name == "goroot":
                    config = json.loads((root / "fixture.json").read_text())
                    config["goroot"] = str(self.ambient)
                    (root / "fixture.json").write_text(json.dumps(config))
                if name == "marker contents": marker.write_text("partial")
                if name == "marker symlink":
                    marker.unlink()
                    marker.symlink_to(self.env_file)
                if name == "not executable": (root / "bin" / "go").chmod(0o644)
                if name == "binary escape":
                    (root / "bin" / "go").unlink()
                    (root / "bin" / "go").symlink_to(self.ambient / "go")
                if name == "directory escape":
                    outside = self.root / "outside-cache"
                    shutil.move(str(root), outside)
                    root.symlink_to(outside, target_is_directory=True)
                self.assert_closed()

    def test_platform_and_path_inputs_fail_closed(self):
        self.cached_go()
        cases = [("RUNNER_OS", "macOS"), ("RUNNER_ARCH", "ARM"), ("RUNNER_TOOL_CACHE", "relative/cache"),
                 ("RUNNER_TEMP", "relative/temp"), ("GO_VERSION", "1.26beta1"), ("GO_VERSION", "1.25")]
        for key, value in cases:
            with self.subTest(key=key, value=value):
                old = self.env[key]
                self.env[key] = value
                self.assert_closed()
                self.env[key] = old

    def test_toolchain_directive_and_arm64(self):
        (self.repo / "go.mod").write_text("module example.com/fixture\n\ngo 1.26.0\ntoolchain go1.26.4\n")
        self.env["RUNNER_ARCH"] = "ARM64"
        self.cached_go("1.26.3", cache_arch="arm64", go_arch="arm64")
        self.assert_closed()
        selected = self.cached_go("1.26.4", cache_arch="arm64", go_arch="arm64")
        self.assertEqual(self.bootstrap().returncode, 0)
        self.assertEqual(self.next_step_env()["GOROOT"], str(selected))
        self.assertEqual(self.next_step_env()["GOARCH"], "arm64")


if __name__ == "__main__":
    unittest.main()
