package models

import "fmt"

// SlashCommand describes a single agent-recognized slash command surfaced in
// the session composer's `/` picker. The primary catalogs are synchronized from
// upstream docs; see the per-agent var declarations below for references.
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
// Source: https://code.claude.com/docs/en/commands.md.
var ClaudeCodeSlashCommands = []SlashCommand{
	{Name: "add-dir", Description: "Add a working directory for file access during the current session", AcceptsArgs: true},
	{Name: "advisor", Description: "Enable or disable the advisor tool, which consults a second model for guidance at key moments during a task", AcceptsArgs: true},
	{Name: "agents", Description: "Manage agent configurations"},
	{Name: "autofix-pr", Description: "Spawn a Claude Code on the web session that watches the current branch's PR and pushes fixes when CI fails or reviewers leave comments", AcceptsArgs: true},
	{Name: "background", Description: "Detach the current session to run as a background agent and free this terminal", AcceptsArgs: true},
	{Name: "batch", Description: "Orchestrate large-scale changes across a codebase in parallel", AcceptsArgs: true},
	{Name: "branch", Description: "Create a branch of the current conversation at this point, so you can try a different direction without losing the conversation as it stands", AcceptsArgs: true},
	{Name: "btw", Description: "Ask a quick side question without adding to the conversation", AcceptsArgs: true},
	{Name: "cd", Description: "Move this session to a new working directory", AcceptsArgs: true},
	{Name: "chrome", Description: "Configure Claude in Chrome settings"},
	{Name: "claude-api", Description: "Load Claude API reference material for your project's language (Python, TypeScript, Java, Go, Ruby, C#, PHP, or cURL) and Managed Agents reference", AcceptsArgs: true},
	{Name: "clear", Description: "Start a new conversation with empty context", AcceptsArgs: true},
	{Name: "code-review", Description: "Review the current diff for correctness bugs and for reuse, simplification, and efficiency cleanups", AcceptsArgs: true},
	{Name: "color", Description: "Set the prompt bar color for the current session", AcceptsArgs: true},
	{Name: "compact", Description: "Free up context by summarizing the conversation so far", AcceptsArgs: true},
	{Name: "config", Description: "Open the Settings interface to adjust theme, model, output style, and other preferences", AcceptsArgs: true},
	{Name: "context", Description: "Visualize current context usage as a colored grid", AcceptsArgs: true},
	{Name: "copy", Description: "Copy the last assistant response to clipboard", AcceptsArgs: true},
	{Name: "cost", Description: "Alias for /usage"},
	{Name: "debug", Description: "Enable debug logging for the current session and troubleshoot issues by reading the session debug log", AcceptsArgs: true},
	{Name: "deep-research", Description: "Fan out web searches on a question, fetch and cross-check sources, and synthesize a cited report", AcceptsArgs: true},
	{Name: "desktop", Description: "Continue the current session in the Claude Code Desktop app"},
	{Name: "diff", Description: "Open an interactive diff viewer showing uncommitted changes and per-turn diffs"},
	{Name: "doctor", Description: "Diagnose and verify your Claude Code installation and settings"},
	{Name: "effort", Description: "Set the model effort level", AcceptsArgs: true},
	{Name: "exit", Description: "Exit the CLI"},
	{Name: "export", Description: "Export the current conversation as plain text", AcceptsArgs: true},
	{Name: "fast", Description: "Toggle fast mode on or off", AcceptsArgs: true},
	{Name: "feedback", Description: "Submit feedback, report a bug, or share your conversation", AcceptsArgs: true},
	{Name: "fewer-permission-prompts", Description: "Scan your transcripts for common read-only Bash and MCP tool calls, then add a prioritized allowlist to project .claude/settings.json to reduce permission prompts"},
	{Name: "focus", Description: "Toggle the focus view, which shows only your last prompt, a one-line tool-call summary with edit diffstats, and the final response"},
	{Name: "fork", Description: "Spawn a forked subagent: a background subagent that inherits the full conversation and works on the directive while you keep going", AcceptsArgs: true},
	{Name: "goal", Description: "Set a goal: Claude keeps working across turns until the condition is met", AcceptsArgs: true},
	{Name: "heapdump", Description: "Write a JavaScript heap snapshot and a memory breakdown to ~/Desktop, or your home directory on Linux without a Desktop folder, for diagnosing high memory usage"},
	{Name: "help", Description: "Show help and available commands"},
	{Name: "hooks", Description: "View hook configurations for tool events"},
	{Name: "ide", Description: "Manage IDE integrations and show status"},
	{Name: "init", Description: "Initialize project with a CLAUDE.md guide"},
	{Name: "insights", Description: "Generate a report analyzing your Claude Code sessions, including project areas, interaction patterns, and friction points"},
	{Name: "install-github-app", Description: "Install the Claude GitHub App for a repository, with an optional step to set up GitHub Actions workflows and secrets"},
	{Name: "install-slack-app", Description: "Install the Claude Slack app"},
	{Name: "keybindings", Description: "Open your keyboard shortcuts file"},
	{Name: "login", Description: "Sign in to your Anthropic account"},
	{Name: "logout", Description: "Sign out from your Anthropic account"},
	{Name: "loop", Description: "Run a prompt repeatedly while the session stays open", AcceptsArgs: true},
	{Name: "mcp", Description: "Manage MCP server connections and OAuth authentication", AcceptsArgs: true},
	{Name: "memory", Description: "Edit CLAUDE.md memory files, enable or disable auto-memory, and view auto-memory entries"},
	{Name: "mobile", Description: "Show QR code to download the Claude mobile app"},
	{Name: "model", Description: "Switch the AI model and save it as your default for new sessions", AcceptsArgs: true},
	{Name: "passes", Description: "Share a free week of Claude Code with friends"},
	{Name: "permissions", Description: "Manage allow, ask, and deny rules for tool permissions"},
	{Name: "plan", Description: "Enter plan mode directly from the prompt", AcceptsArgs: true},
	{Name: "plugin", Description: "Manage Claude Code plugins", AcceptsArgs: true},
	{Name: "powerup", Description: "Discover Claude Code features through quick interactive lessons with animated demos"},
	{Name: "privacy-settings", Description: "View and update your privacy settings"},
	{Name: "radio", Description: "Open Claude FM lo-fi radio in your browser"},
	{Name: "recap", Description: "Generate a one-line summary of the current session on demand"},
	{Name: "release-notes", Description: "View the changelog in an interactive version picker"},
	{Name: "reload-plugins", Description: "Reload all active plugins to apply pending changes without restarting", AcceptsArgs: true},
	{Name: "reload-skills", Description: "Re-scan skill and command directories so skills added or changed on disk during the session become available without restarting"},
	{Name: "remote-control", Description: "Make this session available for remote control from claude.ai"},
	{Name: "remote-env", Description: "Choose the default environment for cloud agents"},
	{Name: "rename", Description: "Rename the current session and show the name on the prompt bar", AcceptsArgs: true},
	{Name: "resume", Description: "Resume a conversation by ID or name, or open the session picker", AcceptsArgs: true},
	{Name: "review", Description: "Review a GitHub pull request by number, using the same review engine as /code-review", AcceptsArgs: true},
	{Name: "rewind", Description: "Rewind the conversation and/or code to a previous point, or summarize from a selected message"},
	{Name: "run", Description: "Launch and drive your project's app to see a change working in the running app, not just in tests"},
	{Name: "run-skill-generator", Description: "Teach /run and /verify how to build, launch, and drive your project's app from a clean environment by writing a per-project skill"},
	{Name: "sandbox", Description: "Toggle sandbox mode"},
	{Name: "schedule", Description: "Create, update, list, or run routines, which execute on Anthropic-managed cloud infrastructure", AcceptsArgs: true},
	{Name: "scroll-speed", Description: "Adjust mouse wheel scroll speed interactively, with a ruler you can scroll while the dialog is open to preview the change"},
	{Name: "security-review", Description: "Analyze pending changes on the current branch for security vulnerabilities"},
	{Name: "setup-bedrock", Description: "Configure Amazon Bedrock authentication, region, and model pins through an interactive wizard"},
	{Name: "setup-vertex", Description: "Configure Google Vertex AI authentication, project, region, and model pins through an interactive wizard"},
	{Name: "simplify", Description: "Review the changed code for cleanup opportunities and apply the fixes", AcceptsArgs: true},
	{Name: "skills", Description: "List available skills"},
	{Name: "stats", Description: "Alias for /usage"},
	{Name: "status", Description: "Open the Settings interface (Status tab) showing version, model, account, and connectivity"},
	{Name: "statusline", Description: "Configure Claude Code's status line"},
	{Name: "stickers", Description: "Order Claude Code stickers"},
	{Name: "stop", Description: "Stop the current background session"},
	{Name: "tasks", Description: "View and manage everything running in the background"},
	{Name: "team-onboarding", Description: "Generate a team onboarding guide from your Claude Code usage history"},
	{Name: "teleport", Description: "Pull a Claude Code on the web session into this terminal: opens a picker, then fetches the branch and conversation"},
	{Name: "terminal-setup", Description: "Configure terminal keybindings for Shift+Enter and other shortcuts"},
	{Name: "theme", Description: "Change the color theme"},
	{Name: "tui", Description: "Set the terminal UI renderer and relaunch into it with your conversation intact", AcceptsArgs: true},
	{Name: "ultraplan", Description: "Draft a plan in an ultraplan session, review it in your browser, then execute remotely or send it back to your terminal", AcceptsArgs: true},
	{Name: "ultrareview", Description: "Run a deep, multi-agent code review in a cloud sandbox with ultrareview", AcceptsArgs: true},
	{Name: "upgrade", Description: "Open the upgrade page to switch to a higher plan tier"},
	{Name: "usage", Description: "Show session cost, plan usage limits, and activity stats"},
	{Name: "usage-credits", Description: "Configure usage credits to keep working when you hit a limit"},
	{Name: "verify", Description: "Confirm a code change does what it should by building your project's app, running it, and observing the result, rather than relying on tests or type checks"},
	{Name: "voice", Description: "Toggle voice dictation, or enable it in a specific mode", AcceptsArgs: true},
	{Name: "web-setup", Description: "Connect your GitHub account to Claude Code on the web using your local gh CLI credentials"},
	{Name: "workflows", Description: "Open the workflow progress view to watch, pause, resume, or save running and completed workflows"},
}

// CodexSlashCommands is the curated catalog of well-known Codex CLI commands.
// Source: https://developers.openai.com/codex/cli/slash-commands.md.
var CodexSlashCommands = []SlashCommand{
	{Name: "agent", Description: "Switch the active agent thread"},
	{Name: "approve", Description: "Approve one retry of a recent auto review denial"},
	{Name: "apps", Description: "Browse apps (connectors) and insert them into your prompt"},
	{Name: "archive", Description: "Archive the current session and exit Codex"},
	{Name: "btw", Description: "Start an ephemeral side conversation"},
	{Name: "clear", Description: "Clear the terminal and start a fresh chat"},
	{Name: "compact", Description: "Summarize the visible conversation to free tokens"},
	{Name: "copy", Description: "Copy the latest completed Codex output"},
	{Name: "debug-config", Description: "Print config layer and requirements diagnostics"},
	{Name: "delete", Description: "Permanently delete the current session and exit Codex"},
	{Name: "diff", Description: "Show the Git diff, including files Git isn't tracking yet"},
	{Name: "exit", Description: "Exit the CLI (same as /quit)"},
	{Name: "experimental", Description: "Toggle experimental features"},
	{Name: "fast", Description: "Toggle a Fast service tier when the model catalog exposes one"},
	{Name: "feedback", Description: "Send logs to the Codex maintainers"},
	{Name: "fork", Description: "Fork the current conversation into a new thread"},
	{Name: "goal", Description: "Set, pause, resume, view, or clear a task goal"},
	{Name: "hooks", Description: "View and manage lifecycle hooks"},
	{Name: "ide", Description: "Include open files, current selection, and other IDE context"},
	{Name: "import", Description: "Import Claude Code setup, project files, and recent chats"},
	{Name: "init", Description: "Generate an AGENTS.md scaffold in the current directory"},
	{Name: "keymap", Description: "Remap TUI keyboard shortcuts"},
	{Name: "logout", Description: "Sign out of Codex"},
	{Name: "mcp", Description: "List configured Model Context Protocol (MCP) tools"},
	{Name: "memories", Description: "Configure memory use and generation"},
	{Name: "mention", Description: "Attach a file to the conversation"},
	{Name: "model", Description: "Choose the active model (and reasoning effort, when available)"},
	{Name: "new", Description: "Start a new conversation inside the same CLI session"},
	{Name: "permissions", Description: "Set what Codex can do without asking first"},
	{Name: "personality", Description: "Choose a communication style for responses"},
	{Name: "plan", Description: "Switch to plan mode and optionally send a prompt"},
	{Name: "plugins", Description: "Browse installed and discoverable plugins"},
	{Name: "ps", Description: "Show experimental background terminals and their recent output"},
	{Name: "quit", Description: "Exit the CLI"},
	{Name: "raw", Description: "Toggle raw scrollback mode"},
	{Name: "resume", Description: "Resume a saved conversation from your session list"},
	{Name: "review", Description: "Ask Codex to review your working tree"},
	{Name: "sandbox-add-read-dir", Description: "Grant sandbox read access to an extra directory (Windows only)"},
	{Name: "side", Description: "Start an ephemeral side conversation"},
	{Name: "skills", Description: "Browse and use skills"},
	{Name: "status", Description: "Display session configuration and token usage"},
	{Name: "statusline", Description: "Configure TUI status-line fields interactively"},
	{Name: "stop", Description: "Stop all background terminals"},
	{Name: "theme", Description: "Choose a syntax-highlighting theme"},
	{Name: "title", Description: "Configure terminal window or tab title fields interactively"},
	{Name: "usage", Description: "View account token usage or use a rate-limit reset"},
	{Name: "vim", Description: "Toggle Vim mode for the composer"},
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

// OpenCodeSlashCommands is the documented OpenCode built-in command catalog.
// Source: https://opencode.ai/docs/commands.md.
var OpenCodeSlashCommands = []SlashCommand{
	{Name: "help", Description: "Show OpenCode help"},
	{Name: "init", Description: "Initialize OpenCode project context"},
	{Name: "redo", Description: "Redo the last undone OpenCode action"},
	{Name: "share", Description: "Share the current OpenCode session"},
	{Name: "undo", Description: "Undo the last OpenCode action"},
}

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
