package cli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

func runChangesets(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "Usage: 143-tools changesets <list|current|status|split-status|diff|create|materialize|verify|publish|publish-stack|restack|import-remote>")
		return 1
	}
	sessionID := os.Getenv("143_SESSION_ID")
	changesetID := ""
	title := ""
	stackedOn := ""
	headSHA := ""
	for i := 1; i < len(args); i++ {
		switch args[i] {
		case "--session-id":
			i++
			if i < len(args) {
				sessionID = args[i]
			}
		case "--changeset":
			i++
			if i < len(args) {
				changesetID = args[i]
			}
		case "--title":
			i++
			if i < len(args) {
				title = args[i]
			}
		case "--stacked-on", "--from":
			i++
			if i < len(args) {
				stackedOn = args[i]
			}
		case "--head-sha":
			i++
			if i < len(args) {
				headSHA = args[i]
			}
		}
	}
	if sessionID == "" {
		fmt.Fprintln(stderr, "error: --session-id is required when 143_SESSION_ID is not set")
		return 1
	}
	cfg, err := LoadConfig()
	if InSandbox() {
		cfg.ServerURL = os.Getenv("INTERNAL_API_URL")
		cfg.Token = os.Getenv("INTERNAL_API_TOKEN")
	}
	if err != nil || cfg.ServerURL == "" || cfg.Token == "" {
		fmt.Fprintln(stderr, "error: 143 API credentials are unavailable")
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	client := NewClient(cfg)
	prefix := "/api/v1"
	if InSandbox() {
		prefix = "/api/v1/internal"
	}
	var method, path string
	var body any
	switch args[0] {
	case "list":
		method, path = http.MethodGet, prefix+"/sessions/"+sessionID+"/changesets"
	case "current":
		method, path = http.MethodGet, prefix+"/sessions/"+sessionID+"/changesets"
	case "status", "split-status":
		method, path = http.MethodGet, prefix+"/sessions/"+sessionID+"/changesets/split-status"
	case "diff":
		if changesetID == "" {
			fmt.Fprintln(stderr, "error: --changeset is required")
			return 1
		}
		if InSandbox() {
			method, path = http.MethodGet, prefix+"/sessions/"+sessionID+"/changesets/diff?changeset_id="+changesetID
		} else {
			method, path = http.MethodGet, prefix+"/sessions/"+sessionID+"/diff?changeset_id="+changesetID
		}
	case "create":
		if strings.TrimSpace(title) == "" {
			fmt.Fprintln(stderr, "error: --title is required")
			return 1
		}
		request := map[string]string{"title": title}
		if stackedOn != "" {
			request["stacked_on_changeset_id"] = stackedOn
		}
		method, path, body = http.MethodPost, prefix+"/sessions/"+sessionID+"/changesets", request
	case "materialize":
		if changesetID == "" {
			fmt.Fprintln(stderr, "error: --changeset is required")
			return 1
		}
		method, path = http.MethodPost, prefix+"/sessions/"+sessionID+"/changesets/"+changesetID+"/materialize"
	case "verify":
		method, path = http.MethodPost, prefix+"/sessions/"+sessionID+"/changesets/verify"
	case "publish":
		if changesetID == "" {
			fmt.Fprintln(stderr, "error: --changeset is required")
			return 1
		}
		method, path = http.MethodPost, prefix+"/sessions/"+sessionID+"/changesets/"+changesetID+"/publish"
	case "publish-stack":
		method, path = http.MethodPost, prefix+"/sessions/"+sessionID+"/changesets/publish-stack"
	case "restack":
		if stackedOn == "" {
			fmt.Fprintln(stderr, "error: --from is required")
			return 1
		}
		method, path = http.MethodPost, prefix+"/sessions/"+sessionID+"/changesets/"+stackedOn+"/restack-descendants"
	case "import-remote":
		if changesetID == "" {
			fmt.Fprintln(stderr, "error: --changeset is required")
			return 1
		}
		if headSHA == "" {
			branchOutput, branchErr := exec.CommandContext(ctx, "git", "branch", "--show-current").Output()
			if branchErr != nil || strings.TrimSpace(string(branchOutput)) == "" {
				fmt.Fprintln(stderr, "error: cannot determine the current changeset branch; pass --head-sha explicitly")
				return 1
			}
			branch := strings.TrimSpace(string(branchOutput))
			remoteOutput, remoteErr := exec.CommandContext(ctx, "git", "ls-remote", "origin", "refs/heads/"+branch).Output()
			if remoteErr != nil {
				fmt.Fprintf(stderr, "error: inspect remote changeset branch: %s\n", remoteErr)
				return 1
			}
			fields := strings.Fields(string(remoteOutput))
			if len(fields) > 0 {
				headSHA = strings.TrimSpace(fields[0])
			}
		}
		if !regexp.MustCompile(`^[0-9a-f]{40}$`).MatchString(headSHA) {
			fmt.Fprintln(stderr, "error: remote changeset branch has no valid SHA")
			return 1
		}
		method, path, body = http.MethodPost, prefix+"/sessions/"+sessionID+"/changesets/"+changesetID+"/import-remote", map[string]string{"head_sha": headSHA}
	default:
		fmt.Fprintf(stderr, "error: unknown changesets action %q\n", args[0])
		return 1
	}
	var response json.RawMessage
	if err := client.Do(ctx, method, path, body, &response); err != nil {
		fmt.Fprintf(stderr, "error: %s\n", err)
		return 1
	}
	if args[0] == "current" {
		var envelope struct {
			Data []map[string]any `json:"data"`
		}
		if err := json.Unmarshal(response, &envelope); err == nil {
			selected := os.Getenv("143_CHANGESET_ID")
			for _, item := range envelope.Data {
				if item["id"] == selected || (selected == "" && item["is_primary"] == true) {
					encoded, _ := json.MarshalIndent(item, "", "  ")
					fmt.Fprintln(stdout, string(encoded))
					return 0
				}
			}
		}
	}
	var pretty any
	if json.Unmarshal(response, &pretty) == nil {
		encoded, _ := json.MarshalIndent(pretty, "", "  ")
		fmt.Fprintln(stdout, string(encoded))
	} else {
		fmt.Fprintln(stdout, string(response))
	}
	return 0
}
