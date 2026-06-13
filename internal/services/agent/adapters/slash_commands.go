package adapters

import (
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/agent"
)

// EnsureSlashCommandsInPrompt is the default serialization helper for slash
// commands attached to a manual session message. Every supported coding agent
// today (Claude Code, Codex, OpenCode, Amp) recognizes `/foo` tokens
// natively in the prompt, so the canonical default is "the textarea is the
// source of truth — pass the user's text through verbatim". This helper
// guards the edge case where the structured commands[] payload disagrees with
// the visible text: if a command is in the structured list but absent from
// the prompt (e.g. the user manually edited the token out, or a future
// codepath populates Commands without echoing it in UserMessage), we prepend
// the missing token + arguments so the agent still sees it.
//
// The function is idempotent: calling it twice yields the same string. It
// preserves the user's text exactly as typed when no rewriting is needed.
func EnsureSlashCommandsInPrompt(message string, commands []models.SessionInputCommand) string {
	return agent.EnsureSlashCommandsInPrompt(message, commands)
}
