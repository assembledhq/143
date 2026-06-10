package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/assembledhq/143/internal/services/mcp"
)

// previewWaitTimeout bounds `preview create --wait`.
const previewWaitTimeout = 15 * time.Minute

// runPreview implements the human-facing preview commands. `create` infers
// repository and branch from the cwd's git context, so on a laptop inside a
// checkout the whole command is just `143-tools preview create --wait`.
func runPreview(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] == "--help" || args[0] == "-h" {
		fmt.Fprintln(stdout, `Usage:
  143-tools preview create [--repo NAME] [--branch NAME] [--wait]
  143-tools preview status <preview-id>
  143-tools preview list
  143-tools preview stop <preview-id>

create infers --repo from the cwd's git remote and --branch from HEAD when omitted.`)
		return 0
	}

	cfg, err := LoadConfig()
	if err != nil {
		fmt.Fprintf(stderr, "error: %s\n", err)
		return 1
	}
	if cfg.Token == "" || cfg.ServerURL == "" {
		fmt.Fprintln(stderr, "error: not logged in — run `143-tools login`")
		return 1
	}
	executor := &previewToolExecutor{client: NewClient(cfg)}
	ctx, cancel := context.WithTimeout(context.Background(), previewWaitTimeout)
	defer cancel()

	switch args[0] {
	case "create":
		return runPreviewCreate(ctx, executor, args[1:], stdout, stderr)
	case "status":
		if len(args) < 2 {
			fmt.Fprintln(stderr, "Usage: 143-tools preview status <preview-id>")
			return 1
		}
		return printToolResult(executor.status(ctx, mustJSON(map[string]string{"preview_id": args[1]})), stdout, stderr)
	case "list":
		return printToolResult(executor.list(ctx), stdout, stderr)
	case "stop":
		if len(args) < 2 {
			fmt.Fprintln(stderr, "Usage: 143-tools preview stop <preview-id>")
			return 1
		}
		return printToolResult(executor.stop(ctx, mustJSON(map[string]string{"preview_id": args[1]})), stdout, stderr)
	default:
		fmt.Fprintf(stderr, "error: unknown preview subcommand %q\n", args[0])
		return 1
	}
}

func runPreviewCreate(ctx context.Context, executor *previewToolExecutor, args []string, stdout, stderr io.Writer) int {
	var repo, branch string
	wait := false
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--repo":
			if i+1 >= len(args) {
				fmt.Fprintln(stderr, "error: --repo requires a value")
				return 1
			}
			i++
			repo = args[i]
		case "--branch":
			if i+1 >= len(args) {
				fmt.Fprintln(stderr, "error: --branch requires a value")
				return 1
			}
			i++
			branch = args[i]
		case "--wait":
			wait = true
		default:
			fmt.Fprintf(stderr, "error: unknown flag %q\n", args[i])
			return 1
		}
	}

	if repo == "" {
		inferred, err := gitRemoteRepoName()
		if err != nil {
			fmt.Fprintf(stderr, "error: --repo not given and could not infer it from the cwd: %s\n", err)
			return 1
		}
		repo = inferred
	}
	if branch == "" {
		inferred, err := gitCurrentBranch()
		if err != nil {
			fmt.Fprintf(stderr, "error: --branch not given and could not infer it from the cwd: %s\n", err)
			return 1
		}
		branch = inferred
	}

	fmt.Fprintf(stdout, "Creating preview of %s @ %s ...\n", repo, branch)
	result := executor.create(ctx, mustJSON(map[string]string{"repository": repo, "branch": branch}))
	if result.IsError {
		return printToolResult(result, stdout, stderr)
	}

	var created previewView
	if err := json.Unmarshal([]byte(firstText(result)), &created); err != nil {
		return printToolResult(result, stdout, stderr)
	}

	if !wait {
		if created.PreviewURL != nil && *created.PreviewURL != "" {
			fmt.Fprintf(stdout, "Preview %s (%s): %s\n", created.PreviewID, created.Status, *created.PreviewURL)
		} else {
			fmt.Fprintf(stdout, "Preview %s is %s — `143-tools preview status %s` to follow it.\n",
				created.PreviewID, created.Status, created.PreviewID)
		}
		return 0
	}

	// --wait: poll status until the preview is live or fails.
	for {
		select {
		case <-ctx.Done():
			fmt.Fprintf(stderr, "error: timed out waiting for preview %s\n", created.PreviewID)
			return 1
		case <-time.After(3 * time.Second):
		}

		statusResult := executor.status(ctx, mustJSON(map[string]string{"preview_id": created.PreviewID}))
		if statusResult.IsError {
			return printToolResult(statusResult, stdout, stderr)
		}
		var current previewView
		if err := json.Unmarshal([]byte(firstText(statusResult)), &current); err != nil {
			return printToolResult(statusResult, stdout, stderr)
		}
		switch current.Status {
		case "running":
			url := ""
			if current.PreviewURL != nil {
				url = *current.PreviewURL
			}
			fmt.Fprintf(stdout, "Preview is live: %s\n", url)
			return 0
		case "failed", "stopped":
			fmt.Fprintf(stderr, "error: preview %s: %s\n", current.Status, current.Error)
			return 1
		default:
			phase := current.CurrentPhase
			if phase == "" {
				phase = current.Status
			}
			fmt.Fprintf(stdout, "  ... %s\n", phase)
		}
	}
}

func printToolResult(result *mcp.ToolCallResult, stdout, stderr io.Writer) int {
	if result.IsError {
		fmt.Fprintln(stderr, firstText(result))
		return 1
	}
	fmt.Fprintln(stdout, firstText(result))
	return 0
}

func firstText(result *mcp.ToolCallResult) string {
	if len(result.Content) == 0 {
		return ""
	}
	return result.Content[0].Text
}

// gitRemoteURLPattern extracts "owner/repo" from https and ssh remote URLs.
var gitRemoteURLPattern = regexp.MustCompile(`(?:github\.com[:/])([^/]+/[^/]+?)(?:\.git)?$`)

// gitRemoteRepoName reads the cwd's origin remote and extracts the
// "owner/repo" name the server-side repository resolution expects.
func gitRemoteRepoName() (string, error) {
	out, err := exec.Command("git", "remote", "get-url", "origin").Output()
	if err != nil {
		return "", fmt.Errorf("not inside a git checkout with an `origin` remote")
	}
	remote := strings.TrimSpace(string(out))
	m := gitRemoteURLPattern.FindStringSubmatch(remote)
	if m == nil {
		return "", fmt.Errorf("could not parse repository name from remote %q", remote)
	}
	return m[1], nil
}

func gitCurrentBranch() (string, error) {
	out, err := exec.Command("git", "rev-parse", "--abbrev-ref", "HEAD").Output()
	if err != nil {
		return "", fmt.Errorf("not inside a git checkout")
	}
	branch := strings.TrimSpace(string(out))
	if branch == "" || branch == "HEAD" {
		return "", fmt.Errorf("detached HEAD — pass --branch explicitly")
	}
	return branch, nil
}

func mustJSON(v any) json.RawMessage {
	data, _ := json.Marshal(v)
	return data
}
