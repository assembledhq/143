package sandbox

import (
	"bytes"
	"context"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/pkg/stdcopy"
)

// DockerClient is the minimal Docker API surface needed for file reading.
type DockerClient interface {
	ContainerExecCreate(ctx context.Context, containerID string, config container.ExecOptions) (container.ExecCreateResponse, error)
	ContainerExecAttach(ctx context.Context, execID string, config container.ExecAttachOptions) (types.HijackedResponse, error)
	ContainerExecInspect(ctx context.Context, execID string) (container.ExecInspect, error)
}

// DockerFileReader implements FileReader by executing commands inside Docker containers.
type DockerFileReader struct {
	client DockerClient
}

// NewDockerFileReader creates a DockerFileReader.
func NewDockerFileReader(client DockerClient) *DockerFileReader {
	return &DockerFileReader{client: client}
}

// unsafePathRE matches shell metacharacters and control characters that could be
// used for command injection or cause unexpected behavior in exec'd commands.
var unsafePathRE = regexp.MustCompile("[`$|;&(){}\\[\\]!<>\\\\\"'\\x00-\\x1f]")

// validateExecPath checks a resolved absolute path for shell-unsafe characters.
// Returns an error if the path contains metacharacters that could allow injection.
func validateExecPath(p string) error {
	if unsafePathRE.MatchString(p) {
		return fmt.Errorf("path contains unsafe characters: %s", p)
	}
	return nil
}

// execCmd runs a command (as a pre-built argv slice, NOT via sh -c) inside a container.
func (d *DockerFileReader) execCmd(ctx context.Context, containerID, workDir string, argv []string) (string, string, int, error) {
	execCfg := container.ExecOptions{
		Cmd:          argv,
		AttachStdout: true,
		AttachStderr: true,
		WorkingDir:   workDir,
		// Force the C locale so utilities (head, sed, ls) emit stderr in a
		// known dialect — isNotFoundStderr relies on the English "No such
		// file or directory" phrase to distinguish ENOENT from other errors.
		Env: []string{"LC_ALL=C", "LANG=C"},
	}

	execResp, err := d.client.ContainerExecCreate(ctx, containerID, execCfg)
	if err != nil {
		return "", "", -1, fmt.Errorf("create exec: %w", err)
	}

	attachResp, err := d.client.ContainerExecAttach(ctx, execResp.ID, container.ExecAttachOptions{})
	if err != nil {
		return "", "", -1, fmt.Errorf("attach exec: %w", err)
	}
	defer attachResp.Close()

	var stdout, stderr bytes.Buffer
	if _, err := stdcopy.StdCopy(&stdout, &stderr, attachResp.Reader); err != nil {
		return "", "", -1, fmt.Errorf("read exec output: %w", err)
	}

	inspect, err := d.client.ContainerExecInspect(ctx, execResp.ID)
	if err != nil {
		return "", "", -1, fmt.Errorf("inspect exec: %w", err)
	}

	return stdout.String(), stderr.String(), inspect.ExitCode, nil
}

// ListDir returns the entries in a directory.
func (d *DockerFileReader) ListDir(ctx context.Context, containerID, workDir, dirPath string) ([]FileEntry, error) {
	absPath := resolvePathInWorkDir(workDir, dirPath)
	if err := validateExecPath(absPath); err != nil {
		return nil, fmt.Errorf("list directory %s: %w", dirPath, err)
	}

	// Use "ls -1ApL" + "stat" as a portable alternative to GNU find -printf,
	// which is not available on Alpine/BusyBox containers.
	// ls -1: one entry per line, -A: include hidden files except . and ..,
	// -p: append / to directories, -L: follow symlinks.
	argv := []string{"ls", "-1ApL", absPath}

	stdout, _, exitCode, err := d.execCmd(ctx, containerID, workDir, argv)
	if err != nil {
		return nil, fmt.Errorf("list directory %s: %w", dirPath, err)
	}
	if exitCode != 0 {
		return nil, fmt.Errorf("list directory %s: not found or not accessible", dirPath)
	}

	var entries []FileEntry
	for _, name := range strings.Split(strings.TrimRight(stdout, "\n"), "\n") {
		if name == "" {
			continue
		}

		entryType := "file"
		if strings.HasSuffix(name, "/") {
			entryType = "dir"
			name = strings.TrimSuffix(name, "/")
		}

		// Construct relative path from workDir
		relPath := dirPath
		if relPath == "" || relPath == "." {
			relPath = name
		} else {
			relPath = relPath + "/" + name
		}

		entries = append(entries, FileEntry{
			Path: relPath,
			Type: entryType,
			Size: 0, // Size omitted for portability; frontend doesn't depend on it for listing.
		})
	}

	return entries, nil
}

// ListDirRecursive returns all file and directory entries under the workspace
// using one bounded recursive find call. It is an optional fast path for
// mention-index construction, where one Docker exec per directory is too
// expensive.
func (d *DockerFileReader) ListDirRecursive(ctx context.Context, containerID, workDir string, maxEntries int, ignoredDirNames []string) ([]FileEntry, error) {
	absPath := resolvePathInWorkDir(workDir, ".")
	if err := validateExecPath(absPath); err != nil {
		return nil, fmt.Errorf("list workspace recursively: %w", err)
	}

	stdout, _, exitCode, err := d.execCmd(ctx, containerID, workDir, recursiveFindArgv(absPath, ignoredDirNames, maxEntries))
	if err != nil {
		return nil, fmt.Errorf("list workspace recursively: %w", err)
	}
	if exitCode != 0 {
		return nil, fmt.Errorf("list workspace recursively: not found or not accessible")
	}

	return appendFindEntries(nil, absPath, stdout, maxEntries), nil
}

// recursiveFindScript labels each path as dir/file via two find passes piped
// through sed, instead of one pass through a shell while-read loop. The shell
// loop cost ~100µs per entry in busybox ash (read + stat + printf per line),
// which is multiple seconds on 50k+ entry workspaces; sed processes the same
// stream at C speed. The root dir itself is emitted by the -type d pass and
// filtered out by appendFindEntries. The sed replacements contain a literal
// tab to match the "kind\tpath" wire format.
const recursiveFindScript = `root=$1
limit=$2
shift 2
if [ "$limit" -le 0 ]; then
  limit=2147483646
fi
limit=$((limit + 1))
{
  find "$root" "$@" -type d -print | sed "s|^|dir	|"
  find "$root" "$@" -type f -print | sed "s|^|file	|"
} | head -n "$limit"`

func recursiveFindArgv(absPath string, ignoredDirNames []string, maxEntries int) []string {
	argv := []string{"sh", "-c", recursiveFindScript, "find-recursive", absPath, strconv.Itoa(maxEntries)}
	ignored := sanitizedFindIgnoredNames(ignoredDirNames)
	if len(ignored) > 0 {
		argv = append(argv, "(")
		for i, name := range ignored {
			if i > 0 {
				argv = append(argv, "-o")
			}
			argv = append(argv, "-name", name)
		}
		argv = append(argv, ")", "-prune", "-o")
	}
	return argv
}

func sanitizedFindIgnoredNames(names []string) []string {
	ignored := make([]string, 0, len(names))
	for _, name := range names {
		name = strings.TrimSpace(name)
		if name == "" || strings.Contains(name, "/") || unsafePathRE.MatchString(name) {
			continue
		}
		ignored = append(ignored, name)
	}
	sort.Strings(ignored)
	return ignored
}

func appendFindEntries(entries []FileEntry, rootAbs, stdout string, maxEntries int) []FileEntry {
	root := strings.TrimRight(filepath.ToSlash(filepath.Clean(rootAbs)), "/")
	for _, raw := range strings.Split(strings.TrimRight(stdout, "\n"), "\n") {
		if maxEntries > 0 && len(entries) >= maxEntries {
			break
		}
		kind, rawPath, ok := strings.Cut(strings.TrimSpace(raw), "\t")
		if !ok {
			continue
		}
		if kind != "dir" && kind != "file" {
			continue
		}
		clean := strings.TrimRight(filepath.ToSlash(filepath.Clean(rawPath)), "/")
		if clean == "" || clean == "." || clean == root {
			continue
		}
		if !strings.HasPrefix(clean, root+"/") {
			continue
		}
		entries = append(entries, FileEntry{
			Path: strings.TrimPrefix(clean, root+"/"),
			Type: kind,
			Size: 0,
		})
	}
	return entries
}

// maxFileReadBytes is the maximum number of bytes to read from a file.
const maxFileReadBytes = 1048576 // 1MB

// ReadFile returns the full content of a file (up to 1MB).
// If the file is larger than 1MB, a truncation notice is appended.
func (d *DockerFileReader) ReadFile(ctx context.Context, containerID, workDir, filePath string) (string, bool, error) {
	absPath := resolvePathInWorkDir(workDir, filePath)
	if err := validateExecPath(absPath); err != nil {
		return "", false, fmt.Errorf("read file %s: %w", filePath, err)
	}

	// Limit to 1MB to prevent reading huge files.
	// Pass path as an argument to head, not through sh -c.
	argv := []string{"head", "-c", fmt.Sprintf("%d", maxFileReadBytes), absPath}
	stdout, stderrOut, exitCode, err := d.execCmd(ctx, containerID, workDir, argv)
	if err != nil {
		return "", false, fmt.Errorf("read file %s: %w", filePath, err)
	}
	if exitCode != 0 {
		trimmed := strings.TrimSpace(stderrOut)
		if isNotFoundStderr(trimmed) {
			return "", false, fmt.Errorf("read file %s: %w", filePath, ErrFileNotFound)
		}
		return "", false, fmt.Errorf("read file %s: %s", filePath, trimmed)
	}

	truncated := len(stdout) >= maxFileReadBytes
	return stdout, truncated, nil
}

// isNotFoundStderr detects the ENOENT message produced by the small utilities
// we exec inside the sandbox (head, awk — GNU coreutils, busybox, or BSD all
// surface ENOENT with the same phrase). Keeping this in one spot lets
// ReadFile/ReadFileContext surface a single ErrFileNotFound sentinel to
// callers instead of forcing them to pattern-match on stderr themselves.
func isNotFoundStderr(stderr string) bool {
	return strings.Contains(strings.ToLower(stderr), "no such file or directory")
}

// ReadFileContext returns lines around a specific line number.
func (d *DockerFileReader) ReadFileContext(ctx context.Context, containerID, workDir, filePath string, line, above, below int) (FileContextResult, error) {
	absPath := resolvePathInWorkDir(workDir, filePath)
	if err := validateExecPath(absPath); err != nil {
		return FileContextResult{}, fmt.Errorf("read context %s:%d: %w", filePath, line, err)
	}

	startLine := line - above
	if startLine < 1 {
		startLine = 1
	}
	endLine := line + below

	// Capture the requested window and total line count in one exec. Each line
	// gets a stable prefix so empty file lines and arbitrary tabs in content
	// round-trip without ambiguity.
	argv := []string{
		"awk",
		"-v", fmt.Sprintf("start=%d", startLine),
		"-v", fmt.Sprintf("end=%d", endLine),
		`NR >= start && NR <= end { print "L" NR "\t" $0 } END { print "T" NR }`,
		absPath,
	}
	stdout, stderrOut, exitCode, err := d.execCmd(ctx, containerID, workDir, argv)
	if err != nil {
		return FileContextResult{}, fmt.Errorf("read context %s:%d: %w", filePath, line, err)
	}
	if exitCode != 0 {
		trimmed := strings.TrimSpace(stderrOut)
		if isNotFoundStderr(trimmed) {
			return FileContextResult{}, fmt.Errorf("read context %s:%d: %w", filePath, line, ErrFileNotFound)
		}
		return FileContextResult{}, fmt.Errorf("read context %s:%d: %s", filePath, line, trimmed)
	}

	var lines []FileLine
	totalLines := -1
	for _, outputLine := range strings.Split(strings.TrimRight(stdout, "\n"), "\n") {
		if outputLine == "" {
			continue
		}
		if strings.HasPrefix(outputLine, "T") {
			parsedTotal, scanErr := strconv.Atoi(strings.TrimSpace(strings.TrimPrefix(outputLine, "T")))
			if scanErr != nil {
				return FileContextResult{}, fmt.Errorf("parse line count %s:%d: %w", filePath, line, scanErr)
			}
			totalLines = parsedTotal
			continue
		}
		if strings.HasPrefix(outputLine, "L") {
			numberPart, content, ok := strings.Cut(strings.TrimPrefix(outputLine, "L"), "\t")
			if !ok {
				return FileContextResult{}, fmt.Errorf("parse context line %s:%d: missing separator", filePath, line)
			}
			lineNum, scanErr := strconv.Atoi(numberPart)
			if scanErr != nil {
				return FileContextResult{}, fmt.Errorf("parse context line %s:%d: %w", filePath, line, scanErr)
			}
			lines = append(lines, FileLine{
				Number:  lineNum,
				Content: content,
			})
		}
	}
	if totalLines < 0 {
		return FileContextResult{}, fmt.Errorf("parse line count %s:%d: missing total", filePath, line)
	}

	result := FileContextResult{
		Lines:      lines,
		TotalLines: totalLines,
	}
	if len(lines) > 0 {
		result.StartLine = lines[0].Number
		result.EndLine = lines[len(lines)-1].Number
		result.HasMoreAbove = result.StartLine > 1
		result.HasMoreBelow = result.EndLine < totalLines
	}

	return result, nil
}

// resolvePathInWorkDir joins the workDir and a relative path, ensuring
// the result stays within the workDir to prevent directory traversal.
func resolvePathInWorkDir(workDir, relPath string) string {
	if relPath == "" || relPath == "." {
		return workDir
	}

	// Clean the path to resolve any ".." components.
	cleaned := filepath.Clean(relPath)

	// If the path is absolute, it's suspicious — strip the leading slash
	// so it's treated as relative to workDir.
	cleaned = strings.TrimPrefix(cleaned, "/")

	// Join with the work directory.
	joined := filepath.Join(workDir, cleaned)

	// Ensure the result is still under workDir (prevent traversal).
	// Use workDir+"/" to avoid false positives like "/workspaceevil" matching "/workspace".
	if joined != workDir && !strings.HasPrefix(joined, workDir+"/") {
		return workDir
	}

	return joined
}
