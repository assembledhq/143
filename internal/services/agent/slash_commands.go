package agent

import (
	"strings"

	"github.com/assembledhq/143/internal/models"
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
	if len(commands) == 0 {
		return message
	}

	var missing []models.SessionInputCommand
	for _, command := range commands {
		if command.Token == "" {
			continue
		}
		if commandTokenPresent(message, command.Token) {
			continue
		}
		missing = append(missing, command)
	}
	if len(missing) == 0 {
		return message
	}

	var b strings.Builder
	for i, command := range missing {
		if i > 0 {
			b.WriteString("\n")
		}
		b.WriteString(command.Token)
		if command.Arguments != "" {
			b.WriteString(" ")
			b.WriteString(command.Arguments)
		}
	}
	if message != "" {
		b.WriteString("\n\n")
		b.WriteString(message)
	}
	return b.String()
}

// commandTokenPresent reports whether token appears in message at a recognized
// position (start of string, or start of a line). Mid-line `/foo` does not
// count, matching the upstream agents' "slash commands are turn-prefix only"
// recognition rule.
func commandTokenPresent(message, token string) bool {
	if message == "" {
		return false
	}
	idx := 0
	for {
		found := strings.Index(message[idx:], token)
		if found < 0 {
			return false
		}
		abs := idx + found
		// Validate that the character before the match is either the start
		// of the string or a newline (we treat leading whitespace on a line
		// as still "start of line" for this check).
		atStart := abs == 0
		afterNewline := false
		if !atStart {
			before := message[abs-1]
			if before == '\n' || before == '\r' {
				afterNewline = true
			} else if before == ' ' || before == '\t' {
				// Walk back to find the prior newline; intervening whitespace is fine.
				j := abs - 1
				for j >= 0 && (message[j] == ' ' || message[j] == '\t') {
					j--
				}
				if j < 0 || message[j] == '\n' || message[j] == '\r' {
					afterNewline = true
				}
			}
		}
		if atStart || afterNewline {
			// Validate that the character after the match is end-of-string,
			// whitespace, or a newline — so `/review` doesn't match `/reviewer`.
			end := abs + len(token)
			if end >= len(message) {
				return true
			}
			next := message[end]
			if next == ' ' || next == '\t' || next == '\n' || next == '\r' {
				return true
			}
		}
		idx = abs + 1
	}
}
