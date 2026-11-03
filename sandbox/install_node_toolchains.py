"""Install checksum-pinned Node distributions while building the sandbox image."""

import hashlib
import json
from pathlib import Path
import re
import subprocess
import sys
import tarfile
import tempfile


def install_toolchain(entry, architecture, destination):
    node_version = entry["node_version"]
    npm_version = entry["npm_version"]
    if not all(re.fullmatch(r"\d+\.\d+\.\d+", value) for value in (node_version, npm_version)):
        raise ValueError("toolchain versions must be exact")
    platform = {"amd64": "linux-x64", "arm64": "linux-arm64"}[architecture]
    checksum = entry["sha256"][platform]
    if not re.fullmatch(r"[0-9a-f]{64}", checksum):
        raise ValueError("toolchain checksum must be pinned")
    name = f"node-v{node_version}-{platform}"
    target = destination / node_version
    if target.exists():
        raise ValueError("duplicate Node toolchain")
    with tempfile.TemporaryDirectory(prefix="node-toolchain-") as temporary:
        archive = Path(temporary) / "node.tar.xz"
        subprocess.run(
            ["curl", "--fail", "--silent", "--show-error", "--retry", "3",
             "--max-time", "300", f"https://nodejs.org/dist/v{node_version}/{name}.tar.xz",
             "--output", str(archive)],
            check=True,
        )
        with archive.open("rb") as stream:
            actual_checksum = hashlib.file_digest(stream, "sha256").hexdigest()
        if actual_checksum != checksum:
            raise ValueError("Node archive checksum mismatch")
        with tarfile.open(archive) as package:
            if any(not Path(member.name).parts or Path(member.name).parts[0] != name for member in package):
                raise ValueError("unexpected Node archive root")
            package.extractall(temporary, filter="data")
        source = Path(temporary) / name
        environment = {"PATH": f"{source / 'bin'}:/usr/bin:/bin", "HOME": temporary}
        node = str(source / "bin/node")
        npm = str(source / "lib/node_modules/npm/bin/npm-cli.js")
        for command, expected in (([node, "--version"], "v" + node_version),
                                  ([node, npm, "--version"], npm_version)):
            actual = subprocess.check_output(command, env=environment, text=True, timeout=30).strip()
            if actual != expected:
                raise ValueError("Node distribution does not match the toolchain catalog")
        destination.mkdir(parents=True, exist_ok=True)
        source.rename(target)


def main():
    catalog = json.loads(Path(sys.argv[1]).read_text())
    architecture = subprocess.check_output(["dpkg", "--print-architecture"], text=True).strip()
    for entry in catalog["toolchains"]:
        install_toolchain(entry, architecture, Path("/opt/143/node"))


if __name__ == "__main__":
    main()
