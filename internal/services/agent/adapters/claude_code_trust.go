package adapters

import (
	"context"
	"fmt"
	"io"
	"path"

	"github.com/assembledhq/143/internal/services/agent"
)

// ensureClaudeWorkspaceTrust records trust inside the isolated sandbox before
// every launch, including resumes of snapshots created before trust was saved.
// bypassPermissions does not enable project allow rules or MCP headersHelper.
func ensureClaudeWorkspaceTrust(ctx context.Context, provider agent.SandboxProvider, sandbox *agent.Sandbox) error {
	if sandbox == nil || !path.IsAbs(sandbox.HomeDir) || !path.IsAbs(sandbox.WorkDir) {
		return fmt.Errorf("claude workspace trust requires absolute sandbox home and work directories")
	}
	// Isolated mode excludes the repository and user site packages from Python's
	// imports, so project files cannot shadow the standard library used here.
	cmd := fmt.Sprintf("python3 -I -c '%s' '%s' '%s'",
		shellEscapeSingle(claudeWorkspaceTrustScript),
		shellEscapeSingle(sandbox.HomeDir),
		shellEscapeSingle(sandbox.WorkDir),
	)
	exitCode, err := provider.Exec(ctx, sandbox, cmd, io.Discard, io.Discard)
	if err != nil {
		return fmt.Errorf("prepare Claude workspace trust: %w", err)
	}
	if exitCode != 0 {
		// Do not include config contents or subprocess diagnostics in session logs.
		reason := "preparation_failed"
		switch exitCode {
		case 2:
			reason = "invalid_configuration"
		case 3:
			reason = "repository_resolution"
		case 4:
			reason = "filesystem_update"
		}
		return fmt.Errorf("prepare Claude workspace trust: %s (exit code %d)", reason, exitCode)
	}
	return nil
}

// Python is part of the sandbox image. Keep the read/merge/write inside the
// sandbox so private Claude configuration never crosses the provider boundary.
// The lock serializes preparation by sibling threads sharing the same home;
// it does not coordinate Claude's own config writes. Accepted trust is a no-op.
const claudeWorkspaceTrustScript = `
import fcntl
import json
import os
import subprocess
import sys
import tempfile

def require_object(value):
    if not isinstance(value, dict):
        raise ValueError("expected configuration object")
    return value

def reject_constant(value):
    raise ValueError("invalid JSON constant")

def prepare():
    home, workspace = sys.argv[1:]
    workspace = os.path.realpath(workspace)
    # Claude keys repository trust on the main checkout, including when the
    # active workspace is a subdirectory or a linked worktree.
    repository = subprocess.run(
        ["git", "-C", workspace, "rev-parse", "--show-toplevel"],
        stdout=subprocess.DEVNULL, stderr=subprocess.DEVNULL,
    )
    if repository.returncode == 0:
        worktrees = subprocess.run(
            ["git", "-C", workspace, "worktree", "list", "--porcelain", "-z"],
            check=True, stdout=subprocess.PIPE, stderr=subprocess.DEVNULL,
        )
        first = os.fsdecode(worktrees.stdout.split(b"\0", 1)[0])
        if not first.startswith("worktree "):
            raise subprocess.SubprocessError("missing main checkout")
        workspace = os.path.realpath(first[len("worktree "):])

    config_path = os.path.join(home, ".claude.json")
    lock_path = os.path.join(home, ".143-claude-trust.lock")
    with open(lock_path, "a", opener=lambda file, flags: os.open(file, flags, 0o600)) as lock:
        fcntl.flock(lock, fcntl.LOCK_EX)
        try:
            with open(config_path) as source:
                config = require_object(json.load(source, parse_constant=reject_constant))
        except FileNotFoundError:
            config = {}
        projects = require_object(config.get("projects", {}))
        project = require_object(projects.get(workspace, {}))
        if project.get("hasTrustDialogAccepted") is True:
            return
        project["hasTrustDialogAccepted"] = True
        projects[workspace] = project
        config["projects"] = projects

        descriptor, temporary = tempfile.mkstemp(prefix=".143-claude-trust-", dir=home)
        try:
            with os.fdopen(descriptor, "w") as target:
                json.dump(config, target, indent=2, allow_nan=False)
                target.write("\n")
            os.replace(temporary, config_path)
        finally:
            if os.path.exists(temporary):
                os.unlink(temporary)

try:
    prepare()
except ValueError:
    sys.exit(2)
except subprocess.SubprocessError:
    sys.exit(3)
except OSError:
    sys.exit(4)
`
