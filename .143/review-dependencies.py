"""143's locked source-only review recipe and local evidence verifier."""

import hashlib
import json
import os
from pathlib import Path
import re
import shutil
import stat
import subprocess
import sys

CACHE = Path(".143/cache/review")
MODCACHE = CACHE / "go/pkg/mod"
MODULES = CACHE / "go/modules.json"
RECEIPT = CACHE / "dependencies.json"
NPM_MARKER = Path("frontend/node_modules/.143-review-sources.json")
INPUTS = ("go.mod", "go.sum", "frontend/package.json", "frontend/package-lock.json",
          ".143/review-dependencies.sh", ".143/review-dependencies.py")


class EvidenceError(Exception):
    pass


def contained(root, relative, boundary=None):
    relative = Path(relative)
    if relative.is_absolute() or ".." in relative.parts:
        raise EvidenceError("evidence path must be a contained relative path")
    candidate = root / relative
    resolved = candidate.resolve()
    allowed = (root / boundary).resolve() if boundary else root.resolve()
    if not resolved.is_relative_to(allowed):
        raise EvidenceError("evidence path escapes its declared source root")
    return candidate


def owned_root(root, relative):
    current = root
    for component in relative.parts:
        current /= component
        if current.is_symlink():
            raise EvidenceError("review cache and dependency roots must not be symlinks")
    return contained(root, relative)


def digest(path):
    if not path.is_file():
        raise EvidenceError("required input or source file is missing: " + str(path.name))
    return hashlib.sha256(path.read_bytes()).hexdigest()


def read_json(path):
    try:
        value = json.loads(path.read_text())
        if not isinstance(value, dict):
            raise EvidenceError("source evidence JSON must be an object")
        return value
    except (OSError, ValueError) as error:
        raise EvidenceError("missing or invalid source evidence manifest") from error


def input_hashes(root):
    return {name: digest(contained(root, name)) for name in INPUTS}


def validate_inputs(root):
    mod = (root / "go.mod").read_text()
    if re.search(r"^\s*(replace|tool)\b", mod, re.MULTILINE) or (root / "go.work").exists():
        raise EvidenceError("review source recipe does not support Go replacements, tool directives or workspaces")
    package = read_json(root / "frontend/package.json")
    lock = read_json(root / "frontend/package-lock.json")
    if package.get("workspaces") or not isinstance(lock.get("packages"), dict):
        raise EvidenceError("review source recipe requires a standalone npm package lock")
    for entry in lock["packages"].values():
        if entry.get("link") or str(entry.get("resolved", "")).startswith(("file:", "link:", "workspace:")):
            raise EvidenceError("review source recipe does not support local npm dependency links")


def required_modules(root):
    required = {}
    in_block = False
    for raw in (root / "go.mod").read_text().splitlines():
        line = raw.split("//", 1)[0].strip()
        if not line:
            continue
        if line == "require (":
            in_block = True
            continue
        if in_block and line == ")":
            in_block = False
            continue
        if not in_block and not line.startswith("require "):
            continue
        parts = line.split() if in_block else line.split()[1:]
        if len(parts) != 2 or not parts[1].startswith("v") or parts[0] in required:
            raise EvidenceError("review source recipe requires conventional pinned Go require entries")
        required[parts[0]] = parts[1]
    if in_block or not required:
        raise EvidenceError("locked Go require entries are missing or unsupported")
    return required


def atomic_json(path, value):
    temporary = path.with_suffix(path.suffix + ".tmp")
    if path.is_symlink() or temporary.is_symlink():
        raise EvidenceError("review evidence manifests must not be symlinks")
    temporary.write_text(json.dumps(value, indent=2, sort_keys=True) + "\n")
    temporary.replace(path)


def json_stream(raw):
    decoder = json.JSONDecoder()
    remaining = raw.strip()
    while remaining:
        value, end = decoder.raw_decode(remaining)
        yield value
        remaining = remaining[end:].lstrip()


def source_manifest(directory, excluded=()):
    files = []
    for current, directories, names in os.walk(directory, followlinks=False):
        for name in sorted(directories + names):
            item = Path(current) / name
            relative = str(item.relative_to(directory))
            if relative in excluded:
                continue
            contained(directory, relative)
            info = item.lstat()
            if stat.S_ISLNK(info.st_mode):
                files.append([relative, "symlink", os.readlink(item)])
            elif stat.S_ISREG(info.st_mode):
                files.append([relative, "file", info.st_size])
            elif not stat.S_ISDIR(info.st_mode):
                raise EvidenceError("dependency source contains an unsupported filesystem object")
    if not files:
        raise EvidenceError("dependency source files are missing")
    return sorted(files)


def reset_module_cache(root):
    cache = owned_root(root, MODCACHE)
    if cache.exists():
        for current, directories, _ in os.walk(cache, followlinks=False):
            for directory in [Path(current)] + [Path(current) / name for name in directories]:
                if not directory.is_symlink():
                    os.chmod(directory, directory.stat().st_mode | stat.S_IWUSR | stat.S_IXUSR)
        shutil.rmtree(cache)
    cache.mkdir(parents=True)


def module_manifest(root, raw):
    modules = []
    for entry in json_stream(raw):
        if entry.get("Error") or not all(entry.get(key) for key in ("Path", "Version", "Dir")):
            raise EvidenceError("Go download did not provide complete module source evidence")
        directory = Path(entry["Dir"])
        try:
            relative = directory.relative_to(root)
        except ValueError as error:
            raise EvidenceError("Go module source directory escapes the repository cache") from error
        contained(root, relative, MODCACHE)
        sources = sorted(str(path.relative_to(directory)) for path in directory.rglob("*")
                         if path.is_file() and (path.suffix == ".go" or path.name == "go.mod"))
        if not sources:
            raise EvidenceError("downloaded Go module source files are missing")
        modules.append({"module": entry["Path"], "version": entry["Version"],
                        "directory": str(relative), "source_files": sources,
                        "source_manifest": source_manifest(directory)})
    if not modules:
        raise EvidenceError("Go module source manifest is empty")
    return {"gomodcache": str(MODCACHE), "modules": sorted(modules, key=lambda value: value["module"])}


def npm_manifest(root):
    frontend = root / "frontend"
    installed = read_json(frontend / "node_modules/.package-lock.json")
    locked = read_json(frontend / "package-lock.json")["packages"]
    packages = []
    for relative, entry in installed.get("packages", {}).items():
        if not relative:
            continue
        path = Path("frontend") / relative
        directory = contained(root, path, Path("frontend/node_modules"))
        if relative not in locked or entry.get("version") != locked[relative].get("version"):
            raise EvidenceError("installed npm source version does not match the lock")
        metadata = directory / "package.json"
        data = read_json(metadata)
        if data.get("version") != entry.get("version"):
            raise EvidenceError("npm package source metadata is missing or stale")
        # Declaration files are review evidence even when lifecycle scripts
        # intentionally have not produced native executables/generated outputs.
        declarations = sorted(str(path.relative_to(directory)) for path in directory.rglob("*.d.ts")
                              if path.is_file() and "node_modules" not in path.relative_to(directory).parts)
        packages.append({"directory": str(path), "package_hash": digest(metadata), "declarations": declarations})
    if not packages:
        raise EvidenceError("installed npm package sources are missing")
    for name in set(read_json(frontend / "package.json").get("dependencies", {})) | set(read_json(frontend / "package.json").get("devDependencies", {})):
        if "node_modules/" + name not in installed["packages"]:
            raise EvidenceError("required frontend package source is missing: " + name)
    return {"install_lock_hash": digest(frontend / "node_modules/.package-lock.json"),
            "packages": sorted(packages, key=lambda value: value["directory"]),
            "source_manifest": source_manifest(frontend / "node_modules", (NPM_MARKER.name,))}


def verify(root):
    validate_inputs(root)
    owned_root(root, MODCACHE)
    owned_root(root, Path("frontend/node_modules"))
    receipt = read_json(contained(root, RECEIPT, CACHE))
    if receipt.get("inputs") != input_hashes(root):
        raise EvidenceError("review dependency evidence is stale for the current input locks or recipe")
    modules_path = contained(root, MODULES, CACHE)
    if receipt.get("module_manifest_hash") != digest(modules_path):
        raise EvidenceError("Go module source manifest changed after preparation")
    modules = read_json(modules_path)
    actual_versions = {entry["module"]: entry["version"] for entry in modules.get("modules", [])}
    if actual_versions != required_modules(root) or len(actual_versions) != len(modules.get("modules", [])):
        raise EvidenceError("Go module source manifest does not cover every locked module version")
    if modules.get("gomodcache") != str(MODCACHE) or not modules.get("modules"):
        raise EvidenceError("Go module cache declaration is missing or incompatible")
    for module in modules["modules"]:
        directory = contained(root, module["directory"], MODCACHE)
        if not directory.is_dir() or not module.get("source_files"):
            raise EvidenceError("locked Go module source directory is missing")
        if module.get("source_manifest") != source_manifest(directory):
            raise EvidenceError("locked Go module source files are missing or changed")
        for source in module["source_files"]:
            if not contained(directory, source, Path(".")).is_file():
                raise EvidenceError("locked Go module source file is missing")
    marker = read_json(contained(root, NPM_MARKER, Path("frontend/node_modules")))
    if marker != receipt.get("npm") or marker != npm_manifest(root):
        raise EvidenceError("npm install marker or locked source roots are missing or stale")


def setup(root):
    validate_inputs(root)
    hashes = input_hashes(root)
    owned_root(root, MODCACHE).mkdir(parents=True, exist_ok=True)
    owned_root(root, Path("frontend/node_modules"))
    try:
        verify(root)
        print("[143-review] locked dependency sources already verified")
        return
    except EvidenceError:
        pass
    # Go trusts an existing extracted module directory even after source files
    # disappear. Re-extract this private cache before recording repaired evidence.
    reset_module_cache(root)
    for relative in (RECEIPT, NPM_MARKER):
        marker = contained(root, relative)
        if marker.exists():
            marker.unlink()
    staging = owned_root(root, CACHE / "go/staging")
    staging.mkdir(parents=True, exist_ok=True)
    for name in ("go.mod", "go.sum"):
        if (staging / name).is_symlink():
            raise EvidenceError("copied review locks must not be symlinks")
        (staging / name).write_bytes((root / name).read_bytes())
    env = os.environ.copy()
    env.update(GOMODCACHE=str(root / MODCACHE), GOPATH=str(root / CACHE / "go"),
               GOCACHE=str(root / CACHE / "go/build"), GOWORK="off", GOTOOLCHAIN="local", GOFLAGS="-mod=readonly -modcacherw")
    print("[143-review] downloading exact locked Go dependency sources", flush=True)
    downloaded = subprocess.run(["go", "mod", "download", "-json"], cwd=staging, env=env,
                                check=True, stdout=subprocess.PIPE, text=True)
    for name in ("go.mod", "go.sum"):
        if digest(staging / name) != hashes[name]:
            raise EvidenceError("Go download changed copied locks; tracked inputs remain untouched")
    modules = module_manifest(root, downloaded.stdout)
    print("[143-review] installing locked frontend sources without lifecycle scripts", flush=True)
    npm_env = os.environ.copy()
    npm_env["npm_config_cache"] = str(owned_root(root, CACHE / "npm"))
    subprocess.run(["npm", "ci", "--ignore-scripts", "--no-audit", "--no-fund", "--include=dev"],
                   cwd=root / "frontend", env=npm_env, check=True)
    if hashes != input_hashes(root):
        raise EvidenceError("tracked input locks changed during source preparation")
    npm = npm_manifest(root)
    atomic_json(root / MODULES, modules)
    atomic_json(root / NPM_MARKER, npm)
    atomic_json(root / RECEIPT, {"inputs": hashes, "module_manifest_hash": digest(root / MODULES), "npm": npm})
    verify(root)
    print("[143-review] dependency source evidence verified; no builds or tests were run")


def main():
    root = Path(__file__).resolve().parent.parent
    if sys.argv[1:] not in ([], ["--verify"]):
        raise EvidenceError("usage: sh .143/review-dependencies.sh [--verify]")
    if sys.argv[1:] == ["--verify"]:
        verify(root)
        print("[143-review] local locked dependency sources verified")
    else:
        setup(root)


if __name__ == "__main__":
    try:
        main()
    except (EvidenceError, OSError, ValueError, subprocess.CalledProcessError) as error:
        print("[143-review] source evidence unavailable: " + str(error), file=sys.stderr)
        sys.exit(1)
