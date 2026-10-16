package providers

import (
	"bytes"
	"context"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/assembledhq/143/internal/services/agent"
	"github.com/docker/docker/api/types/container"
	"github.com/stretchr/testify/require"
)

func reviewSnapshotCommand(t *testing.T, sandbox *agent.Sandbox) string {
	t.Helper()
	var command []string
	mock := &mockDockerClient{containerExecCreateFn: func(_ context.Context, _ string, options container.ExecOptions) (container.ExecCreateResponse, error) {
		command = options.Cmd
		return container.ExecCreateResponse{ID: "review-snapshot"}, nil
	}}
	provider := NewDockerProvider(mock, newTestLogger())
	reader, err := provider.Snapshot(context.Background(), sandbox)
	require.NoError(t, err, "production snapshot should create its actual tar command")
	_, readErr := io.Copy(io.Discard, reader)
	closeErr := reader.Close()
	require.NoError(t, readErr, "synthetic Docker stream should complete")
	require.NoError(t, closeErr, "synthetic snapshot reader should close")
	require.Len(t, command, 3, "snapshot should execute one shell command")
	return command[2]
}

func TestDockerProviderReviewSnapshotRetainsToolsAcrossRepeatedRestore(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name         string
		receipt      string
		linkedLocal  bool
		linkedBin    bool
		linkedParent bool
		preserveTool bool
	}{
		{name: "ordinary coding workspace has unchanged scope"},
		{name: "regular review marker survives empty purpose after restore", receipt: "regular", preserveTool: true},
		{name: "linked receipt cannot widen scope", receipt: "linked"},
		{name: "linked git directory cannot widen scope", receipt: "linked git"},
		{name: "linked local directory cannot widen scope", receipt: "regular", linkedLocal: true},
		{name: "linked bin directory cannot widen scope", receipt: "regular", linkedBin: true},
		{name: "linked home parent cannot widen scope", receipt: "regular", linkedParent: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			root, rootErr := filepath.EvalSymlinks(t.TempDir())
			require.NoError(t, rootErr, "fixture root must have no host path aliases")
			home := filepath.Join(root, "home")
			if tt.linkedParent {
				target := filepath.Join(root, "literal-parent")
				require.NoError(t, os.Mkdir(target, 0o700), "redirected home parent target should exist")
				alias := filepath.Join(root, "linked-parent")
				require.NoError(t, os.Symlink(target, alias), "home parent alias fixture should exist")
				home = filepath.Join(alias, "home")
			}
			work := filepath.Join(home, "repository")
			require.NoError(t, os.MkdirAll(filepath.Join(work, ".143"), 0o700), "isolated workspace should exist")
			require.NoError(t, os.WriteFile(filepath.Join(work, "source.txt"), []byte("pinned source"), 0o600), "workspace source should exist")
			require.NoError(t, os.WriteFile(filepath.Join(work, ".143/config.json"), []byte(`{"dependencies":{"golangci-lint":"2.10.1"}}`), 0o600), "fixture should declare its tool")
			git := filepath.Join(work, ".git")
			if tt.receipt == "linked git" {
				metadata := filepath.Join(root, "redirected-metadata")
				require.NoError(t, os.Mkdir(metadata, 0o700), "redirect target should exist")
				require.NoError(t, os.WriteFile(filepath.Join(metadata, "143-review-evidence.json"), []byte(`{"revision":{}}`), 0o600), "redirected marker should exist")
				require.NoError(t, os.Symlink(metadata, git), "git alias fixture should exist")
			} else {
				require.NoError(t, os.Mkdir(git, 0o700), "literal Git directory should exist")
				if tt.receipt == "regular" {
					require.NoError(t, os.WriteFile(filepath.Join(git, "143-review-evidence.json"), []byte(`{"revision":{}}`), 0o600), "literal review marker should exist")
				} else if tt.receipt == "linked" {
					outside := filepath.Join(root, "redirected-marker")
					require.NoError(t, os.WriteFile(outside, []byte(`{"revision":{}}`), 0o600), "marker redirect target should exist")
					require.NoError(t, os.Symlink(outside, filepath.Join(git, "143-review-evidence.json")), "marker alias fixture should exist")
				}
			}
			local := filepath.Join(home, ".local")
			if tt.linkedLocal {
				outside := filepath.Join(root, "redirected-local")
				require.NoError(t, os.Mkdir(outside, 0o700), "local redirect target should exist")
				require.NoError(t, os.Symlink(outside, local), "local alias fixture should exist")
			}
			if tt.linkedBin {
				require.NoError(t, os.MkdirAll(local, 0o700), "literal local parent should exist")
				target := filepath.Join(root, "redirected-bin")
				require.NoError(t, os.Mkdir(target, 0o700), "bin redirect target should exist")
				require.NoError(t, os.Symlink(target, filepath.Join(local, "bin")), "bin alias fixture should exist")
			}
			require.NoError(t, os.MkdirAll(filepath.Join(local, "bin"), 0o700), "tool directory should exist")
			tool := []byte("#!/bin/sh\nprintf 'golangci-lint version 2.10.1 fixture\\n'\n")
			require.NoError(t, os.WriteFile(filepath.Join(local, "bin/golangci-lint"), tool, 0o755), "trusted local tool fixture should exist")
			require.NoError(t, os.MkdirAll(filepath.Join(local, "private"), 0o700), "unrelated private state should exist")
			require.NoError(t, os.WriteFile(filepath.Join(local, "private/credentials.json"), []byte("synthetic private sibling"), 0o600), "private sibling must stay outside archive")
			for _, directory := range []string{".claude", ".claude.json", ".codex", ".gemini", ".local/share/opencode"} {
				require.NoError(t, os.MkdirAll(filepath.Join(home, directory), 0o700), "optional agent paths should exist for portable host tar")
			}
			purpose := "agent_run"
			if tt.receipt != "" {
				purpose = "prepare_code_review_workspace"
			}
			sandbox := &agent.Sandbox{ID: "fixture", Provider: "docker", HomeDir: home, WorkDir: work, Purpose: purpose}
			for round := 0; round < 2; round++ {
				command := reviewSnapshotCommand(t, sandbox)
				// Use the already-covered gzip transport for host portability and
				// omit GNU tar's missing-path flag: every optional fixture path exists.
				// The production scope selection and archived paths stay exact.
				command = strings.ReplaceAll(command, agent.SnapshotTarCompressFlag, "-z")
				command = strings.ReplaceAll(command, "--ignore-failed-read", "")
				child := exec.Command("/bin/sh", "-c", command)
				var archive, stderr bytes.Buffer
				child.Stdout, child.Stderr = &archive, &stderr
				require.NoError(t, child.Run(), "actual snapshot tar should complete for round %d: %s", round, stderr.String())
				restored, restoreRootErr := filepath.EvalSymlinks(t.TempDir())
				require.NoError(t, restoreRootErr, "restore fixture root must have no host aliases")
				extract := exec.Command("tar", "-x", "-z", "-f", "-", "-C", restored)
				extract.Stdin = bytes.NewReader(archive.Bytes())
				output, err := extract.CombinedOutput()
				require.NoError(t, err, "actual snapshot archive should extract for round %d: %s", round, output)
				homeRelative := strings.TrimPrefix(filepath.ToSlash(sandbox.HomeDir), "/")
				workRelative := strings.TrimPrefix(filepath.ToSlash(sandbox.WorkDir), "/")
				expected := []string{workRelative + "/.143/config.json", workRelative + "/source.txt"}
				if tt.receipt == "regular" {
					expected = append(expected, workRelative+"/.git/143-review-evidence.json")
				}
				if tt.preserveTool {
					expected = append(expected, homeRelative+"/.local/bin/golangci-lint")
					actual, err := os.ReadFile(filepath.Join(restored, homeRelative, ".local/bin/golangci-lint"))
					require.NoError(t, err, "review-declared tool must survive actual snapshot extraction")
					require.Equal(t, tool, actual, "restored tool must retain exact trusted fixture bytes")
					metadata, err := os.Stat(filepath.Join(restored, homeRelative, ".local/bin/golangci-lint"))
					require.NoError(t, err, "restored tool metadata should exist")
					require.Equal(t, os.FileMode(0o755), metadata.Mode().Perm(), "restored tool must retain executable permissions")
				}
				actual := []string{}
				err = filepath.WalkDir(restored, func(name string, entry os.DirEntry, walkErr error) error {
					if walkErr != nil {
						return walkErr
					}
					if entry.Type().IsRegular() {
						relative, err := filepath.Rel(restored, name)
						if err != nil {
							return err
						}
						actual = append(actual, filepath.ToSlash(relative))
					}
					return nil
				})
				require.NoError(t, err, "restored archive inventory should be complete")
				sort.Strings(actual)
				sort.Strings(expected)
				require.Equal(t, expected, actual, "snapshot must preserve exact intended files without unrelated home secrets")
				sandbox = &agent.Sandbox{ID: "restored", Provider: "docker", HomeDir: filepath.Join(restored, homeRelative), WorkDir: filepath.Join(restored, workRelative)}
			}
		})
	}
}
