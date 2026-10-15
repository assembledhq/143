#!/usr/bin/env python3
"""Small build-only provenance helper. No argument prints a local plan."""
import argparse
import hashlib
import json
import os
from pathlib import Path
import re
import subprocess
import tarfile
import uuid

REPOSITORY = "assembledhq/143"
WORKFLOW = ".github/workflows/token-correlation-build-once.yml"
RECIPES = {"server": "Dockerfile", "sandbox": "sandbox/Dockerfile", "frontend": "Dockerfile.frontend"}
SHA = re.compile(r"[0-9a-f]{40}\Z")
DIGEST = re.compile(r"sha256:[0-9a-f]{64}\Z")


def need(value, message):
    if not value:
        raise RuntimeError(message)


def command(args, cwd=None, timeout=60):
    result = subprocess.run(args, cwd=cwd, capture_output=True, timeout=timeout)
    need(result.returncode == 0, "command failed; inspect the named Actions step")
    return result.stdout.decode().strip()


def binding():
    workspace = Path(os.environ["GITHUB_WORKSPACE"]).resolve()
    workflow = workspace / "workflow-source"
    data = json.loads((workflow / ".github/token-correlation-release.json").read_text())
    need(set(data) == {"release_sha", "release_tree", "recipes"}, "binding fields differ")
    need(SHA.fullmatch(data["release_sha"]) and SHA.fullmatch(data["release_tree"]), "accepted source is unbound")
    need(set(data["recipes"]) == set(RECIPES), "recipe image list differs")
    need(all(re.fullmatch(r"[0-9a-f]{64}", value) for value in data["recipes"].values()), "recipe hashes are unbound")
    expected_ref = "refs/heads/codex/build-token-correlation-" + data["release_sha"][:12]
    commit = os.environ.get("GITHUB_SHA", "")
    need(os.environ.get("GITHUB_ACTIONS") == "true" and os.environ.get("GITHUB_REPOSITORY") == REPOSITORY,
         "hosted repository context differs")
    need(os.environ.get("GITHUB_EVENT_NAME") == "push" and os.environ.get("GITHUB_REF") == expected_ref,
         "one-shot push branch differs")
    need(os.environ.get("GITHUB_RUN_ATTEMPT") == "1" and os.environ.get("GITHUB_RUN_ID", "").isdigit(),
         "reruns are refused; reconcile any earlier publication")
    need(SHA.fullmatch(commit) and os.environ.get("GITHUB_WORKFLOW_SHA") == commit,
         "workflow commit differs from push commit")
    need(os.environ.get("GITHUB_WORKFLOW_REF") == REPOSITORY + "/" + WORKFLOW + "@" + expected_ref,
         "workflow identity differs")
    need(command(["git", "rev-parse", "HEAD"], workflow) == commit, "workflow checkout differs")
    need(not command(["git", "status", "--porcelain", "--untracked-files=all"], workflow), "workflow checkout is dirty")
    need(commit != data["release_sha"], "workflow and release source must be separate commits")
    return data, workspace, commit


def verify_source(data, workspace, image):
    source = workspace / "release"
    need(command(["git", "rev-parse", "HEAD"], source) == data["release_sha"], "release checkout differs")
    need(command(["git", "rev-parse", "HEAD^{tree}"], source) == data["release_tree"], "release tree differs")
    need(not command(["git", "status", "--porcelain", "--untracked-files=all"], source), "release checkout is dirty")
    recipe = hashlib.sha256((source / RECIPES[image]).read_bytes()).hexdigest()
    need(recipe == data["recipes"][image], "recipe digest differs")
    return recipe


def require_absent(result, tag):
    need(result.returncode != 0, "full-SHA tag already exists; never overwrite it")
    error = result.stderr.decode(errors="replace").lower()
    missing = "manifest unknown" in error or tag.lower() + ": not found" in error
    ambiguous = any(value in error for value in ("unauthorized", "denied", "forbidden", "timeout", "connection",
                                                "no such host", "tls", "error getting credentials", "executable",
                                                "401", "403", "429", "500", "502", "503"))
    need(missing and not ambiguous, "registry absence is ambiguous; publication refused")


def read_helper_archive(stream):
    result = None
    with tarfile.open(fileobj=stream, mode="r|") as archive:
        for member in archive:
            need(result is None and member.name in ("143-tools", "./143-tools") and member.isfile()
                 and 0 < member.size <= 128 * 1024 * 1024, "unexpected helper archive member")
            result = hashlib.file_digest(archive.extractfile(member), "sha256").hexdigest()
    need(result is not None, "helper archive is empty")
    return result


def validate_helper_container(row, cid, name, immutable, image_config, run_id):
    image_manifest = immutable.rsplit("@", 1)[-1]
    need(DIGEST.fullmatch(image_manifest) and DIGEST.fullmatch(image_config), "image identity digests are invalid")
    need(row["Id"] == cid and row["Name"] == "/" + name and row["Image"] in (image_config, image_manifest),
         "container identity differs")
    need(row["Config"]["Image"] == immutable and row["Config"]["Labels"].get("143.build.run") == run_id,
         "container ownership differs")
    need(row["State"]["Status"] == "created" and not row["State"]["Running"] and row["State"]["Pid"] == 0,
         "helper container was started; refusing further operations")
    return "config_digest" if row["Image"] == image_config else "manifest_digest"


def helper_hash(immutable, image_config, run_id):
    """Read the file from our created, never-started exact-digest container."""
    command(["docker", "pull", "--platform", "linux/amd64", immutable], timeout=600)
    name = "token-correlation-helper-" + run_id + "-" + uuid.uuid4().hex[:12]
    cid = command(["docker", "create", "--pull=never", "--platform", "linux/amd64", "--network", "none", "--name", name,
                   "--label", "143.build.run=" + run_id, "--entrypoint", "/bin/false", immutable])
    need(re.fullmatch(r"[0-9a-f]{64}", cid), "created container identity is invalid")

    def owned():
        row = json.loads(command(["docker", "inspect", cid]))[0]
        return validate_helper_container(row, cid, name, immutable, image_config, run_id)

    try:
        owned()
        proc = subprocess.Popen(["docker", "cp", cid + ":/usr/local/bin/143-tools", "-"],
                                stdout=subprocess.PIPE, stderr=subprocess.PIPE)
        try:
            result = read_helper_archive(proc.stdout)
            need(proc.wait(timeout=60) == 0, "helper copy failed")
            identity_kind = owned()
            return {"helper_sha256": result, "helper_container_image_identity_kind": identity_kind}
        finally:
            if proc.poll() is None:
                proc.terminate()
                proc.wait(timeout=10)
    finally:
        owned()
        command(["docker", "rm", cid])


def main():
    parser = argparse.ArgumentParser(description=__doc__)
    parser.add_argument("phase", nargs="?", choices=("bind", "preflight", "verify"))
    parser.add_argument("--image", choices=tuple(RECIPES))
    args = parser.parse_args()
    if args.phase is None:
        print(json.dumps({"mode": "plan_only", "build_or_publication_executed": False}))
        return
    data, workspace, workflow_commit = binding()
    if args.phase == "bind":
        with open(os.environ["GITHUB_OUTPUT"], "a") as output:
            output.write("release_sha=" + data["release_sha"] + "\n")
        return
    need(args.image is not None, "one fixed image is required")
    recipe = verify_source(data, workspace, args.image)
    image = "ghcr.io/assembledhq/143-" + args.image
    tag = image + ":" + data["release_sha"]
    if args.phase == "preflight":
        result = subprocess.run(["docker", "buildx", "imagetools", "inspect", tag], capture_output=True, timeout=60)
        require_absent(result, tag)
        return
    digest = os.environ.get("PUBLISHED_DIGEST", "")
    need(DIGEST.fullmatch(digest), "build output digest is invalid")
    manifest = json.loads(command(["docker", "buildx", "imagetools", "inspect", "--format", "{{json .Manifest}}", tag]))
    need(manifest.get("digest") == digest and "manifests" not in manifest, "tag does not identify the published single image")
    immutable = image + "@" + digest
    raw_manifest = json.loads(command(["docker", "buildx", "imagetools", "inspect", "--raw", immutable]))
    config_digest = raw_manifest["config"]["digest"]
    need(DIGEST.fullmatch(config_digest), "image configuration digest is invalid")
    config = json.loads(command(["docker", "buildx", "imagetools", "inspect", "--format", "{{json .Image}}", immutable]))
    need(config.get("os") == "linux" and config.get("architecture") == "amd64", "published platform differs")
    need(config.get("config", {}).get("Labels", {}).get("org.opencontainers.image.revision") == data["release_sha"],
         "published revision differs")
    run_id = os.environ["GITHUB_RUN_ID"]
    receipt = {"schema": 1, "release_sha": data["release_sha"], "release_tree": data["release_tree"],
               "recipe_sha256": recipe, "workflow_commit": workflow_commit, "run_id": run_id, "run_attempt": 1,
               "image": args.image, "tag": tag, "manifest_digest": digest, "config_digest": config_digest}
    if args.image == "sandbox":
        receipt.update(helper_hash(immutable, config_digest, run_id))
    final = json.loads(command(["docker", "buildx", "imagetools", "inspect", "--format", "{{json .Manifest}}", tag]))
    need(final.get("digest") == digest, "tag moved during verification")
    out = Path(os.environ["RUNNER_TEMP"]) / ("token-correlation-" + args.image + "-" + run_id)
    out.mkdir(mode=0o700)
    with (out / "receipt.json").open("x") as stream:
        json.dump(receipt, stream, sort_keys=True, indent=2)
        stream.write("\n")
    print(json.dumps(receipt, sort_keys=True))


if __name__ == "__main__":
    main()
