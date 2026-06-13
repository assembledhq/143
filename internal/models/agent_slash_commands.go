package models

import "fmt"

// SlashCommand describes a single agent-recognized slash command surfaced in
// the session composer's `/` picker. The catalog is hand-maintained per agent;
// see the per-agent var declarations below for the upstream documentation
// references behind each entry.
type SlashCommand struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	AcceptsArgs bool   `json:"accepts_args"`
}

// Token returns the visible textarea token for a command (e.g. "/review").
func (c SlashCommand) Token() string {
	return "/" + c.Name
}

// ClaudeCodeSlashCommands is the documented Claude Code command catalog.
// Only full-form commands are surfaced; aliases remain valid if typed
// manually, but we keep them out of the picker to preserve signal. Source:
// https://code.claude.com/docs/en/commands.
var ClaudeCodeSlashCommands = []SlashCommand{
	{Name: "add-dir", Description: "Add a working directory for file access during the current session", AcceptsArgs: true},
	{Name: "agents", Description: "Manage agent configurations"},
	{Name: "autofix-pr", Description: "Spawn a web session that watches the current PR and pushes fixes", AcceptsArgs: true},
	{Name: "batch", Description: "Run a large-scale parallel codebase change workflow", AcceptsArgs: true},
	{Name: "branch", Description: "Create a branch of the current conversation", AcceptsArgs: true},
	{Name: "btw", Description: "Ask a quick side question without adding to the conversation", AcceptsArgs: true},
	{Name: "chrome", Description: "Configure Claude in Chrome settings"},
	{Name: "claude-api", Description: "Load Claude API reference material and migration help", AcceptsArgs: true},
	{Name: "init", Description: "Generate a CLAUDE.md from the repo"},
	{Name: "review", Description: "Review pending changes", AcceptsArgs: true},
	{Name: "clear", Description: "Clear conversation context"},
	{Name: "color", Description: "Set the prompt bar color for the current session", AcceptsArgs: true},
	{Name: "compact", Description: "Compact the conversation", AcceptsArgs: true},
	{Name: "config", Description: "Open the settings interface"},
	{Name: "context", Description: "Visualize current context usage as a colored grid"},
	{Name: "copy", Description: "Copy the last assistant response to the clipboard", AcceptsArgs: true},
	{Name: "cost", Description: "Show token usage and cost for the session"},
	{Name: "help", Description: "List available commands"},
	{Name: "model", Description: "Change the active model", AcceptsArgs: true},
	{Name: "debug", Description: "Enable debug logging and troubleshoot issues", AcceptsArgs: true},
	{Name: "desktop", Description: "Continue the current session in the desktop app"},
	{Name: "diff", Description: "Open an interactive diff viewer showing uncommitted changes and per-turn diffs"},
	{Name: "doctor", Description: "Diagnose and verify your Claude Code installation and settings"},
	{Name: "effort", Description: "Set the model effort level", AcceptsArgs: true},
	{Name: "exit", Description: "Exit the CLI"},
	{Name: "export", Description: "Export the current conversation as plain text", AcceptsArgs: true},
	{Name: "extra-usage", Description: "Configure extra usage to keep working when rate limits are hit"},
	{Name: "fast", Description: "Toggle fast mode on or off", AcceptsArgs: true},
	{Name: "feedback", Description: "Submit feedback about Claude Code", AcceptsArgs: true},
	{Name: "fewer-permission-prompts", Description: "Reduce permission prompts by generating allowlist settings"},
	{Name: "focus", Description: "Toggle the focus view"},
	{Name: "heapdump", Description: "Write a JavaScript heap snapshot for diagnosing memory usage"},
	{Name: "hooks", Description: "View hook configurations for tool events"},
	{Name: "ide", Description: "Manage IDE integrations and show status"},
	{Name: "insights", Description: "Generate a report analyzing your Claude Code sessions"},
	{Name: "install-github-app", Description: "Set up the Claude GitHub Actions app for a repository"},
	{Name: "install-slack-app", Description: "Install the Claude Slack app"},
	{Name: "keybindings", Description: "Open or create your keybindings configuration file"},
	{Name: "login", Description: "Sign in to your Anthropic account"},
	{Name: "logout", Description: "Sign out from your Anthropic account"},
	{Name: "loop", Description: "Run a prompt repeatedly while the session stays open", AcceptsArgs: true},
	{Name: "mcp", Description: "Manage MCP server connections and OAuth authentication"},
	{Name: "memory", Description: "Edit CLAUDE.md memory files and auto-memory settings"},
	{Name: "mobile", Description: "Show a QR code to download the Claude mobile app"},
	{Name: "passes", Description: "Share a free week of Claude Code with friends"},
	{Name: "permissions", Description: "Inspect or change Claude Code permissions"},
	{Name: "plan", Description: "Enter plan mode directly from the prompt", AcceptsArgs: true},
	{Name: "plugin", Description: "Manage Claude Code plugins"},
	{Name: "powerup", Description: "Discover Claude Code features through interactive lessons"},
	{Name: "pr-comments", Description: "Fetch and display pull request comments", AcceptsArgs: true},
	{Name: "privacy-settings", Description: "View and update your privacy settings"},
	{Name: "recap", Description: "Generate a one-line summary of the current session"},
	{Name: "release-notes", Description: "View the changelog in an interactive version picker"},
	{Name: "reload-plugins", Description: "Reload all active plugins without restarting"},
	{Name: "remote-control", Description: "Make this session available for remote control"},
	{Name: "remote-env", Description: "Configure the default remote environment for web sessions"},
	{Name: "rename", Description: "Rename the current session", AcceptsArgs: true},
	{Name: "resume", Description: "Resume a conversation by ID or name, or open the session picker", AcceptsArgs: true},
	{Name: "rewind", Description: "Rewind the conversation and/or code to a previous point"},
	{Name: "sandbox", Description: "Toggle sandbox mode"},
	{Name: "schedule", Description: "Create, update, list, or run routines", AcceptsArgs: true},
	{Name: "security-review", Description: "Analyze pending changes for security vulnerabilities"},
	{Name: "setup-bedrock", Description: "Configure Amazon Bedrock authentication, region, and model pins"},
	{Name: "setup-vertex", Description: "Configure Google Vertex AI authentication, project, region, and model pins"},
	{Name: "simplify", Description: "Review recently changed files for simplification opportunities and fix them", AcceptsArgs: true},
	{Name: "skills", Description: "List available skills"},
	{Name: "status", Description: "Open the settings interface on the Status tab"},
	{Name: "statusline", Description: "Configure Claude Code's status line", AcceptsArgs: true},
	{Name: "stickers", Description: "Order Claude Code stickers"},
	{Name: "tasks", Description: "List and manage background tasks"},
	{Name: "team-onboarding", Description: "Generate a team onboarding guide from usage history"},
	{Name: "teleport", Description: "Pull a Claude Code on the web session into this terminal"},
	{Name: "terminal-setup", Description: "Configure terminal keybindings and shortcuts"},
	{Name: "theme", Description: "Change the color theme"},
	{Name: "tui", Description: "Set the terminal UI renderer and relaunch into it", AcceptsArgs: true},
	{Name: "ultraplan", Description: "Draft a plan in an ultraplan session", AcceptsArgs: true},
}

// CodexSlashCommands is the curated catalog of well-known Codex CLI commands.
// Source: https://github.com/openai/codex/blob/main/codex-rs/app-server/README.md
var CodexSlashCommands = []SlashCommand{
	{Name: "init", Description: "Initialize Codex configuration in the repo"},
	{Name: "diff", Description: "Show pending diff"},
	{Name: "review", Description: "Review pending changes", AcceptsArgs: true},
	{Name: "edit", Description: "Edit a file or selection", AcceptsArgs: true},
	{Name: "model", Description: "Change the active model", AcceptsArgs: true},
	{Name: "clear", Description: "Clear conversation context"},
	{Name: "compact", Description: "Compact the conversation"},
}

// AmpSlashCommands is the curated catalog of well-known Amp commands.
// Source: https://ampcode.com/manual
var AmpSlashCommands = []SlashCommand{
	{Name: "agent", Description: "Switch or manage Amp sub-agents", AcceptsArgs: true},
	{Name: "mode", Description: "Switch Amp mode", AcceptsArgs: true},
	{Name: "help", Description: "List available commands"},
}

// PiSlashCommands is intentionally empty in v1: Pi's documented surface is
// leaner, slash commands are not a primary interaction mode, and no canonical
// command list exists upstream to mirror. Add when upstream conventions
// stabilize. Source: https://www.npmjs.com/package/@mariozechner/pi-agent
var PiSlashCommands = []SlashCommand{}

// OpenCodeSlashCommands is intentionally empty in v1. 143 still passes through
// user-entered slash tokens and discovers .opencode/commands/*.md project
// commands, but built-ins should only be surfaced after probing exact names.
var OpenCodeSlashCommands = []SlashCommand{}

// agentSlashCommandCatalog binds an agent's built-in command list to the label
// rendered above it in the picker. Keying both off the same map prevents the
// label switch and the catalog switch from drifting (e.g. renaming an agent
// type and forgetting to update the label).
type agentSlashCommandCatalog struct {
	Label    string
	Commands []SlashCommand
}

var agentSlashCommandCatalogs = map[AgentType]agentSlashCommandCatalog{
	AgentTypeClaudeCode: {Label: "Claude Code commands", Commands: ClaudeCodeSlashCommands},
	AgentTypeCodex:      {Label: "Codex commands", Commands: CodexSlashCommands},
	AgentTypeAmp:        {Label: "Amp commands", Commands: AmpSlashCommands},
	AgentTypePi:         {Label: "Pi commands", Commands: PiSlashCommands},
	AgentTypeOpenCode:   {Label: "OpenCode commands", Commands: OpenCodeSlashCommands},
}

// SlashCommandsForAgent returns the built-in command catalog for an agent. The
// returned slice is the package-level catalog; callers must not mutate it.
func SlashCommandsForAgent(agentType AgentType) []SlashCommand {
	entry, ok := agentSlashCommandCatalogs[agentType]
	if !ok {
		return nil
	}
	return entry.Commands
}

// SlashCommandAgentLabel returns the display label used in the picker section
// header for an agent type's command catalog (e.g. "Claude Code commands").
func SlashCommandAgentLabel(agentType AgentType) string {
	entry, ok := agentSlashCommandCatalogs[agentType]
	if !ok {
		return "Slash commands"
	}
	return entry.Label
}

// ProjectCommandSpec describes where an agent's project-scoped command files
// live in a repository. Used by Phase 3 repo-tree discovery to surface
// user-defined commands in the slash picker.
type ProjectCommandSpec struct {
	// Dir is the repo-relative directory under which project command files
	// live (e.g. ".claude/commands"). Returned tokens are derived from each
	// file's basename relative to this directory.
	Dir string
	// FileExtension is the lowercase extension that identifies a project
	// command file ("md", "toml"). Files without a matching extension are
	// skipped.
	FileExtension string
}

// HasFileExtension reports whether path has the spec's file extension. Returns
// false if the path is empty or the extension does not match.
func (s ProjectCommandSpec) HasFileExtension(path string) bool {
	if s.FileExtension == "" {
		return true
	}
	suffix := "." + s.FileExtension
	return len(path) > len(suffix) && lowerSuffix(path, len(suffix)) == suffix
}

// CommandNameFromPath converts a repo-relative project command file path into
// the canonical command name. Returns the empty string if the path does not
// belong to this spec's directory or does not match the file extension. Names
// nested inside subdirectories use ":" as a separator (Claude Code's
// convention) so `.claude/commands/auth/review.md` becomes "auth:review".
func (s ProjectCommandSpec) CommandNameFromPath(path string) string {
	prefix := s.Dir
	if prefix == "" {
		return ""
	}
	if len(path) <= len(prefix)+1 || path[:len(prefix)] != prefix || path[len(prefix)] != '/' {
		return ""
	}
	rest := path[len(prefix)+1:]
	if !s.HasFileExtension(rest) {
		return ""
	}
	if s.FileExtension != "" {
		rest = rest[:len(rest)-len(s.FileExtension)-1]
	}
	if rest == "" {
		return ""
	}
	out := make([]byte, 0, len(rest))
	for i := 0; i < len(rest); i++ {
		ch := rest[i]
		if ch == '/' {
			out = append(out, ':')
		} else {
			out = append(out, ch)
		}
	}
	return string(out)
}

// ProjectCommandPaths declares where each agent reads project-scoped command
// files from a repository. Agents whose project conventions are not yet
// stable are intentionally omitted; they fall back to the static catalog.
var ProjectCommandPaths = map[AgentType]ProjectCommandSpec{
	AgentTypeClaudeCode: {Dir: ".claude/commands", FileExtension: "md"},
	AgentTypeCodex:      {Dir: ".codex/commands", FileExtension: "md"},
	AgentTypeOpenCode:   {Dir: ".opencode/commands", FileExtension: "md"},
}

// SupportsProjectCommands reports whether an agent type has a registered
// project-scoped command discovery convention.
func SupportsProjectCommands(agentType AgentType) bool {
	_, ok := ProjectCommandPaths[agentType]
	return ok
}

// SessionInputCommandSource is the catalog source for a slash-command picker
// item. "builtin" entries come from the static Go catalog. "project" entries
// come from the repo's per-agent commands directory.
type SessionInputCommandSource string

const (
	SessionInputCommandSourceBuiltin SessionInputCommandSource = "builtin"
	SessionInputCommandSourceProject SessionInputCommandSource = "project"
)

// SessionInputCommand is a single slash command attached to a user message.
// The visible token in the textarea is preserved verbatim; the structured
// command travels alongside so adapters can serialize deterministically.
type SessionInputCommand struct {
	Kind        string                    `json:"kind"`
	AgentType   AgentType                 `json:"agent_type"`
	Name        string                    `json:"name"`
	Token       string                    `json:"token"`
	Display     string                    `json:"display"`
	Description string                    `json:"description,omitempty"`
	Arguments   string                    `json:"arguments,omitempty"`
	Source      SessionInputCommandSource `json:"source,omitempty"`
}

// Validate checks the structural invariants enforced by the API/orchestrator.
// Adapters trust validated commands.
func (c SessionInputCommand) Validate() error {
	if c.Kind != "" && c.Kind != "command" {
		return fmt.Errorf("kind must be \"command\", got %q", c.Kind)
	}
	if err := c.AgentType.Validate(); err != nil {
		return err
	}
	if c.Name == "" {
		return fmt.Errorf("name is required")
	}
	if c.Token == "" {
		return fmt.Errorf("token is required")
	}
	if c.Token[0] != '/' {
		return fmt.Errorf("token %q must start with '/'", c.Token)
	}
	if c.Display == "" {
		return fmt.Errorf("display is required")
	}
	if c.Source != "" && c.Source != SessionInputCommandSourceBuiltin && c.Source != SessionInputCommandSourceProject {
		return fmt.Errorf("invalid source: %q", c.Source)
	}
	return nil
}

// lowerSuffix returns the last n bytes of s lowercased. Used to do a
// case-insensitive extension comparison without allocating for the whole path.
func lowerSuffix(s string, n int) string {
	if n <= 0 || n > len(s) {
		return ""
	}
	out := make([]byte, n)
	for i := 0; i < n; i++ {
		c := s[len(s)-n+i]
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		out[i] = c
	}
	return string(out)
}
