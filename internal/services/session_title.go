package services

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/prompts"
)

// regenerateEveryN controls how often we regenerate the title (every N turns).
const regenerateEveryN = 3

// maxMessageChars is the maximum characters per message included in the prompt.
const maxMessageChars = 200

// maxRecentPairs is how many recent user+assistant message pairs to include.
const maxRecentPairs = 3

// llmClient is the interface for LLM completion calls.
type llmClient interface {
	Complete(ctx context.Context, systemPrompt, userPrompt string) (string, error)
}

// titleSessionStore defines the session DB operations needed by the title service.
type titleSessionStore interface {
	GetByID(ctx context.Context, orgID, sessionID uuid.UUID) (models.Session, error)
	UpdateTitle(ctx context.Context, orgID, sessionID uuid.UUID, title string) error
}

// titleMessageStore defines the message DB operations needed by the title service.
type titleMessageStore interface {
	ListBySession(ctx context.Context, orgID, sessionID uuid.UUID) ([]models.SessionMessage, error)
}

// SessionTitleService generates and updates session titles using an LLM.
type SessionTitleService struct {
	llm      llmClient
	sessions titleSessionStore
	messages titleMessageStore
	logger   zerolog.Logger
}

// NewSessionTitleService creates a new SessionTitleService.
func NewSessionTitleService(llm llmClient, sessions titleSessionStore, messages titleMessageStore, logger zerolog.Logger) *SessionTitleService {
	return &SessionTitleService{
		llm:      llm,
		sessions: sessions,
		messages: messages,
		logger:   logger,
	}
}

// MaybeRegenerateTitle regenerates the session title if the current turn is
// a multiple of regenerateEveryN (3, 6, 9, ...). It is safe to call on every
// turn — it will no-op when regeneration is not due.
func (s *SessionTitleService) MaybeRegenerateTitle(ctx context.Context, orgID, sessionID uuid.UUID, currentTurn int) error {
	if currentTurn < regenerateEveryN || currentTurn%regenerateEveryN != 0 {
		return nil
	}

	session, err := s.sessions.GetByID(ctx, orgID, sessionID)
	if err != nil {
		return fmt.Errorf("fetch session: %w", err)
	}

	msgs, err := s.messages.ListBySession(ctx, orgID, sessionID)
	if err != nil {
		return fmt.Errorf("fetch messages: %w", err)
	}
	if len(msgs) == 0 {
		return nil
	}

	currentTitle := ""
	if session.Title != nil {
		currentTitle = *session.Title
	}

	userPrompt := buildTitleUserPrompt(msgs)
	systemPrompt := prompts.SessionTitlePrompt(prompts.SessionTitlePromptData{
		CurrentTitle: currentTitle,
	})

	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()

	generated, err := s.llm.Complete(ctx, systemPrompt, userPrompt)
	if err != nil {
		return fmt.Errorf("llm completion: %w", err)
	}

	generated = strings.TrimSpace(generated)
	generated = strings.Trim(generated, "\"'")
	if len(generated) == 0 || len(generated) > 120 {
		return nil
	}

	// Skip update if the title hasn't changed.
	if generated == currentTitle {
		return nil
	}

	if err := s.sessions.UpdateTitle(ctx, orgID, sessionID, generated); err != nil {
		return fmt.Errorf("update title: %w", err)
	}
	return nil
}

// buildTitleUserPrompt constructs a compressed conversation summary for title generation.
func buildTitleUserPrompt(msgs []models.SessionMessage) string {
	var b strings.Builder

	// Include the first user message (the original task).
	for _, m := range msgs {
		if m.Role == models.MessageRoleUser {
			b.WriteString("Original task:\n")
			b.WriteString(truncate(m.Content, maxMessageChars))
			b.WriteString("\n\n")
			break
		}
	}

	// Include the last N message pairs.
	recent := recentMessages(msgs, maxRecentPairs*2)
	if len(recent) > 0 {
		b.WriteString("Recent conversation:\n")
		for _, m := range recent {
			role := "User"
			if m.Role == models.MessageRoleAssistant {
				role = "Assistant"
			}
			b.WriteString(role)
			b.WriteString(": ")
			b.WriteString(truncate(m.Content, maxMessageChars))
			b.WriteString("\n")
		}
	}

	return b.String()
}

// recentMessages returns the last n messages from the list.
func recentMessages(msgs []models.SessionMessage, n int) []models.SessionMessage {
	if len(msgs) <= n {
		return msgs
	}
	return msgs[len(msgs)-n:]
}

// truncate returns s truncated to maxLen characters with "..." appended if needed.
func truncate(s string, maxLen int) string {
	s = strings.TrimSpace(s)
	if len(s) <= maxLen {
		return s
	}
	return s[:maxLen] + "..."
}
