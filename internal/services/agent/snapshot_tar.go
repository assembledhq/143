package agent

import "strings"

// Snapshot tar format — the single source of truth shared by the two snapshot
// paths so they stay in lockstep going forward:
//
//   - session checkpoints  (providers.DockerProvider.Snapshot / Restore)
//   - preview filesystem   (preview.SnapshotCache create / restore)
//
// Both tar the workspace inside the sandbox (ubuntu:24.04, GNU tar). Keeping the
// exclude list and compression choice here means a change to either applies to
// both — previously the preview path excluded regenerable caches while the
// session path shipped them, which is how a session checkpoint reached ~2 GB.

// commonSnapshotExcludes are workspace entries dropped from BOTH snapshot paths:
// regenerable caches that bloat the archive and are recreated on demand. .git is
// deliberately NOT here — session checkpoints must keep it (resume needs the git
// state), while preview snapshots exclude it separately because the repo clone
// reconstructs it. Build caches like .next/cache and node_modules/.cache are
// intentionally KEPT: they are what makes a restored workspace's next build fast.
var commonSnapshotExcludes = []string{"__pycache__", ".pytest_cache"}

// SnapshotTarExcludeFlags renders the `--exclude=` flags for a snapshot tar
// command. Pass excludeGit=true for preview snapshots (where .git is rebuilt
// from the clone); session checkpoints pass false to retain .git.
func SnapshotTarExcludeFlags(excludeGit bool) string {
	excludes := commonSnapshotExcludes
	if excludeGit {
		// Prepend so .git leads the list, matching the historical preview order.
		excludes = append([]string{".git"}, excludes...)
	}
	flags := make([]string, len(excludes))
	for i, e := range excludes {
		flags[i] = "--exclude=" + e
	}
	return strings.Join(flags, " ")
}

// SnapshotTarCompressFlag is a shell command-substitution that selects the tar
// compression flag at runtime: zstd when the `zstd` binary is present in the
// sandbox (smaller archives on the node_modules/.git-heavy trees we snapshot),
// falling back to gzip otherwise.
//
// The fallback exists because agents can't apt-install at runtime, so the zstd
// binary has to be baked into the sandbox image (sandbox/Dockerfile). If new
// code reaches a worker whose sandbox image predates that addition, snapshotting
// keeps working on gzip instead of failing. Extraction always auto-detects the
// format (`tar xf` reads gzip or zstd by magic bytes), so blobs written either
// way restore without the reader knowing which was used — that is what keeps
// pre-switch gzip snapshots restorable after the cutover.
//
// Intended to be embedded directly in a `tar -c ... -f ...` command run via
// `sh -c`. Both snapshot paths execute through DockerProvider.Exec, which wraps
// the command in `sh -c`, so the substitution is evaluated in the sandbox.
const SnapshotTarCompressFlag = `$(command -v zstd >/dev/null 2>&1 && echo --zstd || echo -z)`
