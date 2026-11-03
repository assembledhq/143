import hashlib
import io
from pathlib import Path
import tarfile
import tempfile
import unittest
from unittest.mock import patch

from install_node_toolchains import install_toolchain


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


if __name__ == "__main__":
    unittest.main()
