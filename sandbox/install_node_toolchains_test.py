import hashlib
import io
from pathlib import Path
import tarfile
import tempfile
import unittest
from unittest.mock import patch

from install_node_toolchains import install_toolchain


def make_archive(mode, files, symlinks=()):
    body = io.BytesIO()
    with tarfile.open(fileobj=body, mode=mode) as archive:
        for name, payload in files.items():
            member = tarfile.TarInfo(name)
            member.size = len(payload)
            archive.addfile(member, io.BytesIO(payload))
        for name, target in symlinks:
            member = tarfile.TarInfo(name)
            member.type = tarfile.SYMTYPE
            member.linkname = target
            archive.addfile(member)
    return body.getvalue()


class InstallToolchainTest(unittest.TestCase):
    def test_checked_distribution(self):
        cases = [
            ("valid", "22.1.0", "10.1.0", True, True),
            ("bad checksum", "22.1.0", "10.1.0", False, False),
            ("wrong node", "22.2.0", "10.1.0", True, False),
            ("wrong npm", "22.1.0", "11.1.0", True, False),
        ]
        for name, observed_node, observed_npm, valid_hash, succeeds in cases:
            with self.subTest(name=name), tempfile.TemporaryDirectory() as temporary:
                body = io.BytesIO()
                with tarfile.open(fileobj=body, mode="w:xz") as archive:
                    member = tarfile.TarInfo("node-v22.1.0-linux-x64/bin/node")
                    member.size = 4
                    archive.addfile(member, io.BytesIO(b"node"))
                payload = body.getvalue()
                entry = {"node_version": "22.1.0", "npm_version": "10.1.0", "sha256": {
                    "linux-x64": hashlib.sha256(payload).hexdigest() if valid_hash else "0" * 64,
                }}
                destination = Path(temporary) / "toolchains"

                def download(command, **kwargs):
                    self.assertEqual(command[0], "curl")
                    self.assertIn("https://nodejs.org/dist/v22.1.0/node-v22.1.0-linux-x64.tar.xz", command)
                    Path(command[-1]).write_bytes(payload)

                with patch("install_node_toolchains.subprocess.run", side_effect=download), patch(
                    "install_node_toolchains.subprocess.check_output",
                    side_effect=["v" + observed_node, observed_npm],
                ) as execute:
                    if succeeds:
                        install_toolchain(entry, "amd64", destination)
                        self.assertEqual((destination / "22.1.0/bin/node").read_bytes(), b"node")
                    else:
                        with self.assertRaises(ValueError):
                            install_toolchain(entry, "amd64", destination)
                        self.assertFalse(destination.exists())
                    if not valid_hash:
                        execute.assert_not_called()

    def test_reject_unpinned_input_before_download(self):
        for node_version in ["22", "latest", "22.1.0/../../bad", "$(touch bad)"]:
            with self.subTest(node_version=node_version), patch("install_node_toolchains.subprocess.run") as run:
                with self.assertRaises(ValueError):
                    install_toolchain({"node_version": node_version, "npm_version": "10.1.0"}, "amd64", Path("unused"))
                run.assert_not_called()

    def test_npm_variant(self):
        node_root = "node-v22.1.0-linux-x64"
        bundle = "lib/node_modules/npm"
        npm_cli = f"{bundle}/bin/npm-cli.js"
        npx_cli = f"{bundle}/bin/npx-cli.js"
        node_payload = make_archive("w:xz", {
            f"{node_root}/bin/node": b"node",
            f"{node_root}/{npm_cli}": b"10.1.0",
            f"{node_root}/{npx_cli}": b"old npx",
            f"{node_root}/{bundle}/old-only": b"old",
        }, [
            (f"{node_root}/bin/npm", f"../{npm_cli}"),
            (f"{node_root}/bin/npx", f"../{npx_cli}"),
        ])
        legacy = {"node_version": "22.1.0", "npm_version": "10.1.0", "sha256": {
            "linux-x64": hashlib.sha256(node_payload).hexdigest(),
        }}
        cases = [
            ("valid", "package", "11.1.0", True, None),
            ("bad checksum", "package", "11.1.0", False, "npm archive checksum mismatch"),
            ("wrong root", "npm", "11.1.0", True, "unexpected npm archive root"),
            ("root traversal", f"package/../{node_root}", "11.1.0", True, "unexpected npm archive root"),
            ("wrong version", "package", "11.2.0", True, "does not match"),
        ]
        for name, root, version, valid_hash, error in cases:
            with self.subTest(name=name), tempfile.TemporaryDirectory() as temporary:
                npm_files = {
                    f"{root}/bin/npm-cli.js": version.encode(),
                    f"{root}/bin/npx-cli.js": b"new npx",
                }
                if name == "root traversal":
                    npm_files[f"{root}/bin/node"] = b"replacement node"
                npm_payload = make_archive("w:gz", npm_files)
                variant = dict(
                    legacy, directory="22.1.0-npm11.1.0", npm_version="11.1.0",
                    npm_sha512=hashlib.sha512(npm_payload).hexdigest() if valid_hash else "0" * 128,
                )
                destination = Path(temporary) / "toolchains"
                payloads = {
                    f"https://nodejs.org/dist/v22.1.0/{node_root}.tar.xz": node_payload,
                    "https://registry.npmjs.org/npm/-/npm-11.1.0.tgz": npm_payload,
                }

                def download(command, **kwargs):
                    Path(command[-1]).write_bytes(payloads[command[-3]])

                def execute(command, **kwargs):
                    if len(command) == 2:
                        return "v22.1.0"
                    return Path(command[1]).read_text()

                with patch("install_node_toolchains.subprocess.run", side_effect=download) as run, patch(
                    "install_node_toolchains.subprocess.check_output", side_effect=execute,
                ) as check_output:
                    install_toolchain(legacy, "amd64", destination)
                    check_output.reset_mock()
                    target = destination / variant["directory"]
                    if error:
                        with patch("install_node_toolchains.tarfile.open", wraps=tarfile.open) as open_archive:
                            with self.assertRaisesRegex(ValueError, error):
                                install_toolchain(variant, "amd64", destination)
                            if not valid_hash:
                                self.assertEqual(open_archive.call_count, 1)
                        self.assertFalse(target.exists())
                        if not valid_hash or root != "package":
                            check_output.assert_not_called()
                    else:
                        install_toolchain(variant, "amd64", destination)
                        self.assertEqual((target / "bin/npm").readlink(), Path(f"../{npm_cli}"))
                        self.assertEqual((target / "bin/npm").read_text(), "11.1.0")
                        self.assertEqual((target / "bin/npx").readlink(), Path(f"../{npx_cli}"))
                        self.assertEqual((target / "bin/npx").read_text(), "new npx")
                        self.assertFalse((target / bundle / "old-only").exists())
                        run.reset_mock()
                        with self.assertRaisesRegex(ValueError, "duplicate"):
                            install_toolchain(variant, "amd64", destination)
                        run.assert_not_called()
                    self.assertEqual((destination / "22.1.0/bin/npm").read_text(), "10.1.0")

    def test_reject_invalid_variant_before_download(self):
        cases = [
            {"directory": "../22.1.0-npm11.1.0", "npm_sha512": "0" * 128},
            {"directory": "22.1.0"},
            {"directory": "22.1.0-npm11.1.0"},
            {"directory": "22.1.0-npm11.1.0", "npm_sha512": "invalid"},
            {"npm_sha512": "0" * 128},
        ]
        for overrides in cases:
            with self.subTest(overrides=overrides), patch("install_node_toolchains.subprocess.run") as run:
                entry = dict(node_version="22.1.0", npm_version="11.1.0",
                             sha256={"linux-x64": "0" * 64}, **overrides)
                with self.assertRaises(ValueError):
                    install_toolchain(entry, "amd64", Path("unused"))
                run.assert_not_called()


if __name__ == "__main__":
    unittest.main()
