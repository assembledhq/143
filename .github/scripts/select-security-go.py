#!/usr/bin/env python3
"""Select a complete hosted-runner Go cache entry without downloading a toolchain."""

import json
import os
from pathlib import Path
import re
import stat
import subprocess
import sys
import tempfile


VERSION = re.compile(r"(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)")
ARCHITECTURES = {"X64": ("x64", "amd64"), "ARM64": ("arm64", "arm64")}


def version(value):
    if not VERSION.fullmatch(value):
        raise ValueError("a stable three-part Go version is required")
    return tuple(map(int, value.split(".")))


def absolute_path(name):
    value = os.environ.get(name, "")
    if not value or any(char in value for char in "\r\n\x00") or not Path(value).is_absolute():
        raise ValueError(f"{name} must be an absolute single-line path")
    return Path(value)


def repository_minimum():
    text = (Path(__file__).resolve().parents[2] / "go.mod").read_text()
    required = []
    for directive, prefix in (("go", ""), ("toolchain", "go")):
        values = re.findall(rf"^{directive}\s+(\S+)\s*(?://[^\n]*)?$", text, re.MULTILINE)
        if (directive == "go" and len(values) != 1) or len(values) > 1:
            raise ValueError("go.mod must declare one Go version")
        for value in values:
            if prefix and not value.startswith(prefix):
                raise ValueError("unsupported go.mod toolchain requirement")
            required.append(version(value[len(prefix):]))
    return max(required)


def select_go():
    if os.environ.get("RUNNER_OS") != "Linux" or os.environ.get("RUNNER_ARCH") not in ARCHITECTURES:
        raise ValueError("Security Scan requires a supported Linux hosted-runner architecture")
    cache_arch, go_arch = ARCHITECTURES[os.environ["RUNNER_ARCH"]]
    requested = os.environ.get("GO_VERSION", "")
    if not re.fullmatch(r"(0|[1-9][0-9]*)\.(0|[1-9][0-9]*)", requested):
        raise ValueError("GO_VERSION must select a stable major.minor")
    minor = tuple(map(int, requested.split(".")))
    minimum = repository_minimum()
    if minimum[:2] > minor:
        raise ValueError("the configured Go minor cannot satisfy go.mod")

    cache = absolute_path("RUNNER_TOOL_CACHE").resolve(strict=True)
    tools = cache / "go"  # actions/setup-go's official tool-cache name is lowercase.
    if tools.resolve(strict=True) != tools:
        raise ValueError("the Go cache must not redirect outside its official layout")
    candidates = []
    for entry in tools.iterdir():
        if not VERSION.fullmatch(entry.name):
            continue
        parsed = version(entry.name)
        if parsed[:2] == minor and parsed >= minimum and (entry / f"{cache_arch}.complete").exists():
            candidates.append((parsed, entry))
    if not candidates:
        raise ValueError("no complete cached Go version satisfies the configured minor and go.mod")
    selected, entry = max(candidates)
    root = entry / cache_arch
    marker = entry / f"{cache_arch}.complete"
    binary = root / "bin" / "go"
    if root.resolve(strict=True) != root or binary.resolve(strict=True) != binary:
        raise ValueError("the cached Go entry must not contain path redirects")
    if marker.is_symlink() or not stat.S_ISREG(marker.stat().st_mode) or marker.stat().st_size != 0:
        raise ValueError("the Go cache completion marker must be an empty regular file")
    if not binary.is_file() or not os.access(binary, os.X_OK):
        raise ValueError("the selected cached Go binary is not executable")

    env = {**os.environ, "GOTOOLCHAIN": "local", "GOENV": "off", "GOROOT": str(root),
           "GOOS": "linux", "GOARCH": go_arch, "PATH": f"{root / 'bin'}:{os.environ.get('PATH', '')}"}
    expected = ".".join(map(str, selected))
    actual = subprocess.run([str(binary), "version"], env=env, check=True, capture_output=True, text=True, timeout=15).stdout.strip()
    if actual != f"go version go{expected} linux/{go_arch}":
        raise ValueError("the cached Go binary version or architecture does not match its cache entry")
    actual_env = json.loads(subprocess.run(
        [str(binary), "env", "-json", "GOROOT", "GOOS", "GOARCH", "GOHOSTOS", "GOHOSTARCH"],
        env=env, check=True, capture_output=True, text=True, timeout=15,
    ).stdout)
    if actual_env != {"GOROOT": str(root), "GOOS": "linux", "GOARCH": go_arch, "GOHOSTOS": "linux", "GOHOSTARCH": go_arch}:
        raise ValueError("the cached Go environment does not match the selected native toolchain")
    return root, expected, go_arch


def main():
    root, selected, go_arch = select_go()
    runner_temp = absolute_path("RUNNER_TEMP").resolve(strict=True)
    gobin = Path(tempfile.mkdtemp(prefix="security-gosec-", dir=runner_temp))
    if gobin.resolve(strict=True).parent != runner_temp or stat.S_IMODE(gobin.stat().st_mode) != 0o700:
        raise ValueError("the gosec installation directory must be private and runner-local")
    values = {"GOTOOLCHAIN": "local", "GOENV": "off", "GOROOT": str(root), "GOBIN": str(gobin), "GOOS": "linux", "GOARCH": go_arch}
    with absolute_path("GITHUB_ENV").open("a") as output:
        output.writelines(f"{key}={value}\n" for key, value in values.items())
    with absolute_path("GITHUB_PATH").open("a") as output:
        output.write(f"{root / 'bin'}\n{gobin}\n")
    print(f"Selected cached Go {selected} (linux/{go_arch}).")


if __name__ == "__main__":
    try:
        main()
    except (ValueError, OSError, subprocess.SubprocessError) as error:
        # Do not print subprocess output or runner-local paths into public CI logs.
        print(f"Security Scan Go bootstrap failed: {error if isinstance(error, ValueError) else type(error).__name__}", file=sys.stderr)
        sys.exit(1)
