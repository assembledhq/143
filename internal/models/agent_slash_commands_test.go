package models

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestSlashCommandToken(t *testing.T) {
	t.Parallel()

	cmd := SlashCommand{Name: "review"}
	require.Equal(t, "/review", cmd.Token(), "Token should prepend a leading slash")
}

func TestSlashCommandsForAgent(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		agentType AgentType
		wantNames []string
	}{
		{name: "claude code", agentType: AgentTypeClaudeCode, wantNames: []string{"init", "review", "clear"}},
		{name: "codex", agentType: AgentTypeCodex, wantNames: []string{"init", "diff", "review"}},
		{name: "gemini cli", agentType: AgentTypeGeminiCLI, wantNames: []string{"help", "compress"}},
		{name: "amp", agentType: AgentTypeAmp, wantNames: []string{"agent", "mode"}},
		{name: "pi (empty)", agentType: AgentTypePi},
		{name: "unknown", agentType: AgentType("nope")},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			catalog := SlashCommandsForAgent(tt.agentType)
			names := make(map[string]struct{}, len(catalog))
			for _, cmd := range catalog {
				names[cmd.Name] = struct{}{}
			}
			for _, want := range tt.wantNames {
				require.Contains(t, names, want, "catalog for %s should include %q", tt.agentType, want)
			}
			if tt.agentType == AgentType("nope") {
				require.Nil(t, catalog, "unknown agent type should return nil catalog")
			}
		})
	}
}

func TestClaudeCodeSlashCommands_DocumentedCoverage(t *testing.T) {
	t.Parallel()

	expected := []string{
		"add-dir", "agents", "autofix-pr", "batch", "branch", "btw", "chrome", "claude-api",
		"clear", "color", "compact", "config", "context", "copy", "cost", "debug", "desktop",
		"diff", "doctor", "effort", "exit", "export", "extra-usage", "fast", "feedback",
		"fewer-permission-prompts", "focus", "heapdump", "help", "hooks", "ide", "init",
		"insights", "install-github-app", "install-slack-app", "keybindings", "login", "logout",
		"loop", "mcp", "memory", "mobile", "model", "passes", "permissions", "plan", "plugin",
		"powerup", "pr-comments", "privacy-settings", "recap", "release-notes", "reload-plugins",
		"remote-control", "remote-env", "rename", "resume", "review", "rewind", "sandbox",
		"schedule", "security-review", "setup-bedrock", "setup-vertex", "simplify", "skills",
		"status", "statusline", "stickers", "tasks", "team-onboarding", "teleport",
		"terminal-setup", "theme", "tui", "ultraplan",
	}

	catalog := SlashCommandsForAgent(AgentTypeClaudeCode)
	names := make(map[string]struct{}, len(catalog))
	for _, cmd := range catalog {
		names[cmd.Name] = struct{}{}
	}

	for _, want := range expected {
		require.Contains(t, names, want, "Claude Code catalog should include %q", want)
	}
	require.Len(t, names, len(expected), "Claude Code catalog should match the documented command set covered by this test")
}

func TestSlashCommandAgentLabel(t *testing.T) {
	t.Parallel()

	require.Equal(t, "Claude Code commands", SlashCommandAgentLabel(AgentTypeClaudeCode))
	require.Equal(t, "Codex commands", SlashCommandAgentLabel(AgentTypeCodex))
	require.Equal(t, "OpenCode commands", SlashCommandAgentLabel(AgentTypeOpenCode))
	require.Equal(t, "Slash commands", SlashCommandAgentLabel(AgentType("nope")))
}

func TestProjectCommandSpecCommandNameFromPath(t *testing.T) {
	t.Parallel()

	spec := ProjectCommandPaths[AgentTypeClaudeCode]
	require.Equal(t, ".claude/commands", spec.Dir)

	tests := []struct {
		name string
		path string
		want string
	}{
		{name: "top-level command", path: ".claude/commands/review.md", want: "review"},
		{name: "uppercase extension", path: ".claude/commands/review.MD", want: "review"},
		{name: "nested command", path: ".claude/commands/auth/review.md", want: "auth:review"},
		{name: "wrong directory", path: ".other/commands/review.md", want: ""},
		{name: "wrong extension", path: ".claude/commands/review.txt", want: ""},
		{name: "missing extension", path: ".claude/commands/review", want: ""},
		{name: "directory itself", path: ".claude/commands", want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, spec.CommandNameFromPath(tt.path))
		})
	}
}

func TestSupportsProjectCommands(t *testing.T) {
	t.Parallel()

	require.True(t, SupportsProjectCommands(AgentTypeClaudeCode))
	require.True(t, SupportsProjectCommands(AgentTypeCodex))
	require.True(t, SupportsProjectCommands(AgentTypeGeminiCLI))
	require.True(t, SupportsProjectCommands(AgentTypeOpenCode))
	require.Equal(t, ProjectCommandSpec{Dir: ".opencode/commands", FileExtension: "md"}, ProjectCommandPaths[AgentTypeOpenCode], "OpenCode should discover project commands from .opencode/commands/*.md")
	require.False(t, SupportsProjectCommands(AgentTypeAmp))
	require.False(t, SupportsProjectCommands(AgentTypePi))
}

func TestSessionInputCommandValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		command SessionInputCommand
		wantErr string
	}{
		{
			name: "valid",
			command: SessionInputCommand{
				Kind:      "command",
				AgentType: AgentTypeClaudeCode,
				Name:      "review",
				Token:     "/review",
				Display:   "/review",
			},
		},
		{
			name: "blank kind defaults ok",
			command: SessionInputCommand{
				AgentType: AgentTypeCodex,
				Name:      "diff",
				Token:     "/diff",
				Display:   "/diff",
			},
		},
		{
			name: "wrong kind",
			command: SessionInputCommand{
				Kind:      "mention",
				AgentType: AgentTypeClaudeCode,
				Name:      "review",
				Token:     "/review",
				Display:   "/review",
			},
			wantErr: `kind must be "command", got "mention"`,
		},
		{
			name: "invalid agent",
			command: SessionInputCommand{
				AgentType: AgentType("nope"),
				Name:      "review",
				Token:     "/review",
				Display:   "/review",
			},
			wantErr: `invalid agent type: "nope"`,
		},
		{
			name: "missing name",
			command: SessionInputCommand{
				AgentType: AgentTypeClaudeCode,
				Token:     "/review",
				Display:   "/review",
			},
			wantErr: "name is required",
		},
		{
			name: "missing token",
			command: SessionInputCommand{
				AgentType: AgentTypeClaudeCode,
				Name:      "review",
				Display:   "/review",
			},
			wantErr: "token is required",
		},
		{
			name: "token without leading slash",
			command: SessionInputCommand{
				AgentType: AgentTypeClaudeCode,
				Name:      "review",
				Token:     "review",
				Display:   "/review",
			},
			wantErr: `token "review" must start with '/'`,
		},
		{
			name: "missing display",
			command: SessionInputCommand{
				AgentType: AgentTypeClaudeCode,
				Name:      "review",
				Token:     "/review",
			},
			wantErr: "display is required",
		},
		{
			name: "invalid source",
			command: SessionInputCommand{
				AgentType: AgentTypeClaudeCode,
				Name:      "review",
				Token:     "/review",
				Display:   "/review",
				Source:    SessionInputCommandSource("custom"),
			},
			wantErr: `invalid source: "custom"`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.command.Validate()
			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.EqualError(t, err, tt.wantErr)
		})
	}
}

func TestSessionInputCommandsValueScan(t *testing.T) {
	t.Parallel()

	commands := SessionInputCommands{
		{
			Kind:      "command",
			AgentType: AgentTypeClaudeCode,
			Name:      "review",
			Token:     "/review",
			Display:   "/review",
			Arguments: "focus on auth",
			Source:    SessionInputCommandSourceBuiltin,
		},
	}

	value, err := commands.Value()
	require.NoError(t, err)
	encoded, ok := value.([]byte)
	require.True(t, ok)
	require.Contains(t, string(encoded), `"name":"review"`)

	var roundTrip SessionInputCommands
	require.NoError(t, roundTrip.Scan(encoded))
	require.Equal(t, commands, roundTrip)

	var empty SessionInputCommands
	require.NoError(t, empty.Scan(nil))
	require.Nil(t, empty)

	var fromString SessionInputCommands
	require.NoError(t, fromString.Scan(string(encoded)))
	require.Equal(t, commands, fromString)

	var bad SessionInputCommands
	require.EqualError(t, bad.Scan(42), "unsupported session input commands type int")
}

func TestSessionInputCommandsValueEmpty(t *testing.T) {
	t.Parallel()

	value, err := SessionInputCommands{}.Value()
	require.NoError(t, err)
	require.Equal(t, []byte("[]"), value)
}

func TestSessionInputCommandsScanEmptyAndInvalid(t *testing.T) {
	t.Parallel()

	var fromEmpty SessionInputCommands
	require.NoError(t, fromEmpty.Scan([]byte{}))
	require.Nil(t, fromEmpty, "empty raw bytes should reset to nil")

	var fromBadJSON SessionInputCommands
	require.Error(t, fromBadJSON.Scan([]byte("not-json")), "malformed JSON should surface an unmarshal error")
}

func TestProjectCommandSpecHasFileExtension(t *testing.T) {
	t.Parallel()

	noExt := ProjectCommandSpec{Dir: "any", FileExtension: ""}
	require.True(t, noExt.HasFileExtension("anything"), "spec with no required extension always matches")
	require.True(t, noExt.HasFileExtension(""), "empty path with no required extension matches")

	mdSpec := ProjectCommandSpec{Dir: ".claude/commands", FileExtension: "md"}
	require.False(t, mdSpec.HasFileExtension(""), "empty path cannot match a required extension")
	require.False(t, mdSpec.HasFileExtension(".md"), "path that is only the suffix should not count as having the extension")
	require.True(t, mdSpec.HasFileExtension("review.md"))
	require.True(t, mdSpec.HasFileExtension("review.MD"), "extension comparison must be case-insensitive")
}

func TestProjectCommandSpecCommandNameFromPathEdgeCases(t *testing.T) {
	t.Parallel()

	emptyDir := ProjectCommandSpec{Dir: "", FileExtension: "md"}
	require.Equal(t, "", emptyDir.CommandNameFromPath(".claude/commands/review.md"), "empty Dir disables matching to avoid prefixing every path")

	noExt := ProjectCommandSpec{Dir: "cmds", FileExtension: ""}
	require.Equal(t, "review", noExt.CommandNameFromPath("cmds/review"), "no required extension returns the basename verbatim")

	mdSpec := ProjectCommandSpec{Dir: "cmds", FileExtension: "md"}
	require.Equal(t, "", mdSpec.CommandNameFromPath("cmds/.md"), "filename consisting only of the extension yields an empty name")
}

func TestLowerSuffix(t *testing.T) {
	t.Parallel()

	require.Equal(t, "", lowerSuffix("hello", 0), "n==0 returns empty")
	require.Equal(t, "", lowerSuffix("hello", -1), "negative n returns empty")
	require.Equal(t, "", lowerSuffix("hi", 5), "n greater than length returns empty")
	require.Equal(t, ".md", lowerSuffix("FOO.MD", 3), "uppercase suffix should be lowercased")
	require.Equal(t, ".md", lowerSuffix("foo.md", 3), "lowercase suffix passes through unchanged")
}
