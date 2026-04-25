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
		{name: "codex", agentType: AgentTypeCodex, wantNames: []string{"init", "diff"}},
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

func TestSlashCommandAgentLabel(t *testing.T) {
	t.Parallel()

	require.Equal(t, "Claude Code commands", SlashCommandAgentLabel(AgentTypeClaudeCode))
	require.Equal(t, "Codex commands", SlashCommandAgentLabel(AgentTypeCodex))
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
