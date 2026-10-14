"""Local source-evidence contract tests; all setup commands are mocked."""

import importlib.util
import json
import os
from pathlib import Path
import shutil
import subprocess
import tempfile
import unittest
from unittest.mock import patch
import zipfile

SPEC = importlib.util.spec_from_file_location("review_dependencies", Path(__file__).with_name("review-dependencies.py"))
recipe = importlib.util.module_from_spec(SPEC)
SPEC.loader.exec_module(recipe)


class ReviewDependencySourcesTest(unittest.TestCase):
    def fixture(self):
        directory = tempfile.TemporaryDirectory()
        self.addCleanup(directory.cleanup)
        root = Path(directory.name).resolve()
        (root / ".143").mkdir()
        (root / "frontend").mkdir()
        (root / "go.mod").write_text("module example.test/review\n\ngo 1.26.0\n\nrequire example.test/sdk v1.2.3\n")
        (root / "go.sum").write_text("example.test/sdk v1.2.3 h1:fixture\n")
        package = {"name": "review-frontend", "dependencies": {"fixture-sdk": "1.2.3"}}
        lock = {"lockfileVersion": 3, "packages": {"": package, "node_modules/fixture-sdk": {"version": "1.2.3"}}}
        (root / "frontend/package.json").write_text(json.dumps(package))
        (root / "frontend/package-lock.json").write_text(json.dumps(lock))
        for script in ("review-dependencies.sh", "review-dependencies.py"):
            shutil.copyfile(Path(__file__).with_name(script), root / ".143" / script)
        return root

    def install(self, root, mutate_copies=False, successful_mutation=False, real_go=False):
        calls = []
        real_run = subprocess.run

        def run(args, **kwargs):
            calls.append(args)
            if args[0] == "go":
                self.assertEqual(args, ["go", "mod", "download", "-json"])
                env = kwargs["env"]
                self.assertEqual(env["GOWORK"], "off")
                self.assertEqual(env["GOTOOLCHAIN"], "local")
                self.assertEqual(env["GOFLAGS"], "-mod=readonly -modcacherw")
                self.assertEqual(env["GOMODCACHE"], str(root / recipe.MODCACHE))
                self.assertEqual(kwargs["cwd"], root / recipe.CACHE / "go/staging")
                self.assertFalse((root / recipe.MODCACHE / "example.test/sdk@v1.2.3").exists())
                if real_go:
                    return real_run(args, **kwargs)
                if mutate_copies or successful_mutation:
                    (kwargs["cwd"] / "go.sum").write_text("changed only the ignored copy\n")
                    if mutate_copies:
                        raise subprocess.CalledProcessError(1, args)
                module = root / recipe.MODCACHE / "example.test/sdk@v1.2.3"
                module.mkdir(parents=True, exist_ok=True)
                (module / "go.mod").write_text("module example.test/sdk\n")
                (module / "sdk.go").write_text("package sdk\ntype Permission string\n")
                (module / "sdk.s").write_text("TEXT ·permission(SB),$0-0\n")
                return subprocess.CompletedProcess(args, 0, stdout=json.dumps({"Path": "example.test/sdk", "Version": "v1.2.3", "Dir": str(module)}))
            self.assertEqual(args, ["npm", "ci", "--ignore-scripts", "--no-audit", "--no-fund", "--include=dev"])
            self.assertEqual(kwargs["cwd"], root / "frontend")
            self.assertEqual(kwargs["env"]["npm_config_cache"], str(root / recipe.CACHE / "npm"))
            module = root / "frontend/node_modules/fixture-sdk"
            module.mkdir(parents=True, exist_ok=True)
            (module / "package.json").write_text(json.dumps({"name": "fixture-sdk", "version": "1.2.3"}))
            (module / "index.d.ts").write_text("export type Permission = string;\n")
            (module / "index.js").write_text("export const permission = 'read';\n")
            (root / "frontend/node_modules/.package-lock.json").write_text(json.dumps({"packages": {"node_modules/fixture-sdk": {"version": "1.2.3"}}}))
            return subprocess.CompletedProcess(args, 0)

        with patch.object(recipe.subprocess, "run", side_effect=run):
            recipe.setup(root)
        self.assertEqual(calls, [["go", "mod", "download", "-json"], ["npm", "ci", "--ignore-scripts", "--no-audit", "--no-fund", "--include=dev"]])

    def test_setup_and_verification_are_locked_and_source_only(self):
        root = self.fixture()
        before = recipe.input_hashes(root)
        self.install(root)
        self.assertEqual(before, recipe.input_hashes(root))
        manifest = recipe.read_json(root / recipe.MODULES)
        self.assertEqual(manifest["modules"], [{"module": "example.test/sdk", "version": "v1.2.3", "directory": ".143/cache/review/go/pkg/mod/example.test/sdk@v1.2.3", "source_files": ["go.mod", "sdk.go"], "source_manifest": [["go.mod", "file", len("module example.test/sdk\n".encode())], ["sdk.go", "file", len("package sdk\ntype Permission string\n".encode())], ["sdk.s", "file", len("TEXT ·permission(SB),$0-0\n".encode())]]}])
        with patch.object(recipe.subprocess, "run", side_effect=AssertionError("verification must never run a tool or repair")):
            recipe.verify(root)
            recipe.setup(root)

    def test_missing_or_stale_evidence_fails_local_verification(self):
        cases = ["go_module", "go_source", "go_assembly", "npm_package", "npm_type", "npm_source", "npm_marker", "go_lock", "npm_lock", "escaping_manifest", "escaping_module_source", "omitted_module"]
        for case in cases:
            with self.subTest(case=case):
                root = self.fixture()
                self.install(root)
                module = root / recipe.MODCACHE / "example.test/sdk@v1.2.3"
                if case == "go_module":
                    shutil.rmtree(module)
                elif case == "go_source":
                    (module / "sdk.go").unlink()
                elif case == "go_assembly":
                    (module / "sdk.s").unlink()
                elif case == "npm_package":
                    shutil.rmtree(root / "frontend/node_modules/fixture-sdk")
                elif case == "npm_type":
                    (root / "frontend/node_modules/fixture-sdk/index.d.ts").unlink()
                elif case == "npm_source":
                    (root / "frontend/node_modules/fixture-sdk/index.js").unlink()
                elif case == "npm_marker":
                    (root / recipe.NPM_MARKER).unlink()
                elif case == "go_lock":
                    (root / "go.sum").write_text("changed lock\n")
                elif case == "npm_lock":
                    (root / "frontend/package-lock.json").write_text("{}")
                else:
                    manifest = recipe.read_json(root / recipe.MODULES)
                    if case == "escaping_manifest":
                        manifest["modules"][0]["directory"] = "frontend"
                    elif case == "omitted_module":
                        manifest["modules"] = []
                    else:
                        manifest["modules"][0]["source_files"] = ["../../../../../../../go.mod"]
                    recipe.atomic_json(root / recipe.MODULES, manifest)
                    receipt = recipe.read_json(root / recipe.RECEIPT)
                    receipt["module_manifest_hash"] = recipe.digest(root / recipe.MODULES)
                    recipe.atomic_json(root / recipe.RECEIPT, receipt)
                with patch.object(recipe.subprocess, "run", side_effect=AssertionError("verify must remain local")):
                    with self.assertRaises(recipe.EvidenceError):
                        recipe.verify(root)

    def test_missing_javascript_source_requires_reinstallation(self):
        root = self.fixture()
        self.install(root)
        source = root / "frontend/node_modules/fixture-sdk/index.js"
        source.unlink()
        self.install(root)
        self.assertEqual(source.read_text(), "export const permission = 'read';\n")

    def test_missing_go_assembly_requires_reinstallation(self):
        root = self.fixture()
        self.install(root)
        source = root / recipe.MODCACHE / "example.test/sdk@v1.2.3/sdk.s"
        source.unlink()
        self.install(root)
        self.assertEqual(source.read_text(), "TEXT ·permission(SB),$0-0\n")

    @unittest.skipUnless(shutil.which("go"), "real locked-cache repair requires the Go tool")
    def test_real_go_download_reextracts_missing_assembly_from_locked_archive(self):
        root = self.fixture()
        proxy = root / "proxy/example.test/sdk/@v"
        proxy.mkdir(parents=True)
        module = "module example.test/sdk\n\ngo 1.26.0\n"
        (proxy / "v1.2.3.mod").write_text(module)
        (proxy / "v1.2.3.info").write_text(json.dumps({"Version": "v1.2.3", "Time": "2020-01-01T00:00:00Z"}))
        with zipfile.ZipFile(proxy / "v1.2.3.zip", "w") as archive:
            for name, source in {"go.mod": module, "sdk.go": "package sdk\n", "sdk.s": "TEXT ·permission(SB),$0-0\n"}.items():
                archive.writestr("example.test/sdk@v1.2.3/" + name, source)
        seed = root / "seed"
        seed.mkdir()
        shutil.copyfile(root / "go.mod", seed / "go.mod")
        env = os.environ.copy()
        env.update(GOPROXY=(root / "proxy").as_uri(), GOSUMDB="off", GOWORK="off", GOTOOLCHAIN="local",
                   GOMODCACHE=str(root / "seed-cache"), GOPATH=str(root / "seed-path"), GOCACHE=str(root / "seed-build"))
        subprocess.run(["go", "mod", "download", "-json"], cwd=seed, env=env, check=True, stdout=subprocess.PIPE)
        shutil.copyfile(seed / "go.sum", root / "go.sum")
        before = recipe.input_hashes(root)
        with patch.dict(os.environ, {"GOPROXY": env["GOPROXY"], "GOSUMDB": "off"}):
            self.install(root, real_go=True)
            source = root / recipe.MODCACHE / "example.test/sdk@v1.2.3/sdk.s"
            source.unlink()
            with self.assertRaises(recipe.EvidenceError):
                recipe.verify(root)
            self.install(root, real_go=True)
            self.assertEqual(source.read_text(), "TEXT ·permission(SB),$0-0\n")
            self.assertEqual(before, recipe.input_hashes(root))
            recipe.verify(root)

    def test_failed_download_cannot_change_tracked_locks_or_publish_receipt(self):
        root = self.fixture()
        before = recipe.input_hashes(root)
        with self.assertRaises(subprocess.CalledProcessError):
            self.install(root, mutate_copies=True)
        self.assertEqual(before, recipe.input_hashes(root))
        self.assertEqual((root / recipe.CACHE / "go/staging/go.sum").read_text(), "changed only the ignored copy\n")
        self.assertFalse((root / recipe.RECEIPT).exists())
        self.assertFalse((root / recipe.MODULES).exists())

    def test_successful_download_with_modified_copies_cannot_publish_evidence(self):
        root = self.fixture()
        before = recipe.input_hashes(root)
        with self.assertRaisesRegex(recipe.EvidenceError, "changed copied locks"):
            self.install(root, successful_mutation=True)
        self.assertEqual(before, recipe.input_hashes(root))
        self.assertFalse((root / recipe.RECEIPT).exists())
        self.assertFalse((root / recipe.MODULES).exists())


    def test_unsupported_inputs_fail_before_any_setup_command(self):
        for case in ("relative_replace", "workspace", "npm_workspace", "npm_local_link", "symlinked_module_cache", "symlinked_staged_lock"):
            with self.subTest(case=case):
                root = self.fixture()
                if case == "relative_replace":
                    with (root / "go.mod").open("a") as output:
                        output.write("replace example.test/sdk => ../sdk\n")
                elif case == "workspace":
                    (root / "go.work").write_text("go 1.26.0\nuse .\n")
                elif case == "npm_workspace":
                    package = recipe.read_json(root / "frontend/package.json")
                    package["workspaces"] = ["../sibling"]
                    (root / "frontend/package.json").write_text(json.dumps(package))
                elif case == "npm_local_link":
                    lock = recipe.read_json(root / "frontend/package-lock.json")
                    lock["packages"]["node_modules/fixture-sdk"]["link"] = True
                    (root / "frontend/package-lock.json").write_text(json.dumps(lock))
                elif case == "symlinked_module_cache":
                    (root / recipe.MODCACHE).parent.mkdir(parents=True)
                    (root / recipe.MODCACHE).symlink_to(root / "frontend", target_is_directory=True)
                else:
                    staging = root / recipe.CACHE / "go/staging"
                    staging.mkdir(parents=True)
                    (staging / "go.sum").symlink_to(root / "go.sum")
                before = recipe.input_hashes(root)
                with patch.object(recipe.subprocess, "run", side_effect=AssertionError("incompatible input may not start setup")):
                    with self.assertRaises(recipe.EvidenceError):
                        recipe.setup(root)
                self.assertEqual(before, recipe.input_hashes(root))


if __name__ == "__main__":
    unittest.main()
