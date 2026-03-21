package sandbox

import (
	"bytes"
	"context"
	"fmt"
	"path/filepath"
	"regexp"
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

	// Use find with null-delimited output for safe parsing of filenames
	// that may contain tabs or newlines.
	// The format string uses find's own escape sequences (not Go's):
	// \t = tab, \0 = null byte. Go's raw string literal (`...`) passes
	// the backslash-t and backslash-zero through literally, which find
	// then interprets as its own escapes.
	argv := []string{
		"find", absPath, "-maxdepth", "1", "-mindepth", "1",
		"-printf", `%y\t%s\t%P\0`,
	}

	stdout, _, exitCode, err := d.execCmd(ctx, containerID, workDir, argv)
	if err != nil {
		return nil, fmt.Errorf("list directory %s: %w", dirPath, err)
	}
	if exitCode != 0 {
		return nil, fmt.Errorf("list directory %s: not found or not accessible", dirPath)
	}

	var entries []FileEntry
	for _, record := range strings.Split(stdout, "\x00") {
		if record == "" {
			continue
		}
		parts := strings.SplitN(record, "\t", 3)
		if len(parts) < 3 {
			continue
		}

		entryType := "file"
		if parts[0] == "d" {
			entryType = "dir"
		}

		size, _ := strconv.ParseInt(parts[1], 10, 64)
		entryPath := parts[2]

		// Construct relative path from workDir
		relPath := dirPath
		if relPath == "" || relPath == "." {
			relPath = entryPath
		} else {
			relPath = relPath + "/" + entryPath
		}

		entries = append(entries, FileEntry{
			Path: relPath,
			Type: entryType,
			Size: size,
		})
	}

	return entries, nil
}

// ReadFile returns the full content of a file.
func (d *DockerFileReader) ReadFile(ctx context.Context, containerID, workDir, filePath string) (string, error) {
	absPath := resolvePathInWorkDir(workDir, filePath)
	if err := validateExecPath(absPath); err != nil {
		return "", fmt.Errorf("read file %s: %w", filePath, err)
	}

	// Limit to 1MB to prevent reading huge files.
	// Pass path as an argument to head, not through sh -c.
	argv := []string{"head", "-c", "1048576", absPath}
	stdout, stderrOut, exitCode, err := d.execCmd(ctx, containerID, workDir, argv)
	if err != nil {
		return "", fmt.Errorf("read file %s: %w", filePath, err)
	}
	if exitCode != 0 {
		return "", fmt.Errorf("read file %s: %s", filePath, strings.TrimSpace(stderrOut))
	}

	return stdout, nil
}

// ReadFileContext returns lines around a specific line number.
func (d *DockerFileReader) ReadFileContext(ctx context.Context, containerID, workDir, filePath string, line, above, below int) ([]FileLine, error) {
	absPath := resolvePathInWorkDir(workDir, filePath)
	if err := validateExecPath(absPath); err != nil {
		return nil, fmt.Errorf("read context %s:%d: %w", filePath, line, err)
	}

	startLine := line - above
	if startLine < 1 {
		startLine = 1
	}
	endLine := line + below

	// Use sed to extract the line range. Path is passed as an argument.
	argv := []string{"sed", "-n", fmt.Sprintf("%d,%dp", startLine, endLine), absPath}
	stdout, stderrOut, exitCode, err := d.execCmd(ctx, containerID, workDir, argv)
	if err != nil {
		return nil, fmt.Errorf("read context %s:%d: %w", filePath, line, err)
	}
	if exitCode != 0 {
		return nil, fmt.Errorf("read context %s:%d: %s", filePath, line, strings.TrimSpace(stderrOut))
	}

	var lines []FileLine
	for i, content := range strings.Split(stdout, "\n") {
		lineNum := startLine + i
		if lineNum > endLine {
			break
		}
		lines = append(lines, FileLine{
			Number:  lineNum,
			Content: content,
		})
	}

	return lines, nil
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
