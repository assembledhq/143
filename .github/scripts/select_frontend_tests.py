"""Select full coverage for changes outside Vitest's module dependency graph."""

import os
from pathlib import PurePosixPath
import subprocess


def needs_full_suite(paths):
    for path in paths:
        if path.startswith((".github/workflows/", ".github/scripts/")):
            return True
        if not path.startswith("frontend/"):
            continue
        relative = path.removeprefix("frontend/")
        # Package/config changes and shared client infrastructure can affect
        # tests without appearing in their static import graph.
        if len(PurePosixPath(relative).parts) == 1:
            return True
        if relative.startswith(("src/test/", "src/components/", "src/lib/")):
            return True
        if relative.startswith("src/app/"):
            app_path = PurePosixPath(relative)
            if len(app_path.parts) == 3 or app_path.stem in ("layout", "error", "global-error"):
                return True
    return False


def select_scope(event, base):
    if event not in ("pull_request", "push"):
        return True, ""
    resolved = subprocess.run(
        ["git", "rev-parse", "--verify", "--quiet", f"{base}^{{commit}}"],
        capture_output=True,
        text=True,
        check=False,
    )
    if resolved.returncode != 0:
        if event == "pull_request":
            raise ValueError("PR base commit is unavailable")
        return True, ""
    base = resolved.stdout.strip()
    if event == "push":
        ancestor = subprocess.run(
            ["git", "merge-base", "--is-ancestor", base, "HEAD"], check=False,
        )
        if ancestor.returncode == 1:
            # Vitest uses a merge-base diff. A rewritten main history needs
            # full coverage even when the previous tip still resolves.
            return True, ""
        ancestor.check_returncode()
    # Include both sides of renames and deleted shared files. NUL delimiters
    # preserve filenames with whitespace without shell interpretation.
    changed = subprocess.run(
        ["git", "diff", "--no-renames", "--name-only", "-z", base, "HEAD"],
        capture_output=True,
        text=True,
        check=True,
    ).stdout.split("\0")
    return needs_full_suite(changed), base


if __name__ == "__main__":
    event = os.environ["GITHUB_EVENT_NAME"]
    base = os.environ.get("PR_BASE_SHA" if event == "pull_request" else "PUSH_BASE_SHA", "")
    full, base = select_scope(event, base)
    print(f"Frontend tests: {'full suite with coverage' if full else 'affected tests'}")
    with open(os.environ["GITHUB_OUTPUT"], "a", encoding="utf-8") as output:
        output.write(f"full={str(full).lower()}\nbase={base}\n")
