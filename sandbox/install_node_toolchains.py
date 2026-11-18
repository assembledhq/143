"""Install checksum-pinned Node distributions while building the sandbox image."""

import hashlib
import json
from pathlib import Path
import re
import shutil
import subprocess
import sys
import tarfile
import tempfile


def download_archive(url, archive, algorithm, checksum, distribution):
    subprocess.run(
        ["curl", "--fail", "--silent", "--show-error", "--retry", "3",
         "--max-time", "300", url, "--output", str(archive)],
        check=True,
    )
    with archive.open("rb") as stream:
        actual_checksum = hashlib.file_digest(stream, algorithm).hexdigest()
    if actual_checksum != checksum:
        raise ValueError(f"{distribution} archive checksum mismatch")


def extract_archive(archive, destination, root, distribution):
    with tarfile.open(archive) as package:
        for member in package:
            parts = Path(member.name).parts
            if not parts or parts[0] != root or ".." in parts:
                raise ValueError(f"unexpected {distribution} archive root")
        package.extractall(destination, filter="data")


def install_toolchain(entry, architecture, destination):
    node_version = entry["node_version"]
    npm_version = entry["npm_version"]
    if not all(re.fullmatch(r"\d+\.\d+\.\d+", value) for value in (node_version, npm_version)):
        raise ValueError("toolchain versions must be exact")
    platform = {"amd64": "linux-x64", "arm64": "linux-arm64"}[architecture]
    checksum = entry["sha256"][platform]
    if not re.fullmatch(r"[0-9a-f]{64}", checksum):
        raise ValueError("toolchain checksum must be pinned")
    directory = node_version
    npm_checksum = None
    if "directory" in entry or "npm_sha512" in entry:
        directory = entry.get("directory")
        if directory != f"{node_version}-npm{npm_version}":
            raise ValueError("toolchain variant directory must match its Node and npm versions")
        npm_checksum = entry.get("npm_sha512")
        if not isinstance(npm_checksum, str) or not re.fullmatch(r"[0-9a-f]{128}", npm_checksum):
            raise ValueError("npm archive checksum must be pinned")
    name = f"node-v{node_version}-{platform}"
    target = destination / directory
    if target.exists():
        raise ValueError("duplicate Node toolchain")
    destination.parent.mkdir(parents=True, exist_ok=True)
    with tempfile.TemporaryDirectory(prefix="node-toolchain-", dir=destination.parent) as temporary:
        archive = Path(temporary) / "node.tar.xz"
        download_archive(
            f"https://nodejs.org/dist/v{node_version}/{name}.tar.xz",
            archive, "sha256", checksum, "Node",
        )
        extract_archive(archive, temporary, name, "Node")
        source = Path(temporary) / name
        if npm_checksum is not None:
            archive = Path(temporary) / "npm.tgz"
            download_archive(
                f"https://registry.npmjs.org/npm/-/npm-{npm_version}.tgz",
                archive, "sha512", npm_checksum, "npm",
            )
            npm_stage = Path(temporary) / "npm-stage"
            npm_stage.mkdir()
            extract_archive(archive, npm_stage, "package", "npm")
            npm_bundle = source / "lib/node_modules/npm"
            shutil.rmtree(npm_bundle)
            (npm_stage / "package").rename(npm_bundle)
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
