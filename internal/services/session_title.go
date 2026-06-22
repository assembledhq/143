package services

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/prompts"
)

const (
	regenerateEveryN = 3
	maxMessageChars  = 200
	maxRecentPairs   = 3
	maxTitleLen      = 120
)

type llmClient interface {
	Complete(ctx context.Context, systemPrompt, userPrompt string) (string, error)
}

type titleSessionStore interface {
	GetByID(ctx context.Context, orgID, sessionID uuid.UUID) (models.Session, error)
	UpdateTitle(ctx context.Context, orgID, sessionID uuid.UUID, title string) error
}

type titleMessageStore interface {
	ListBySession(ctx context.Context, orgID, sessionID uuid.UUID) ([]models.SessionMessage, error)
}

type titleThreadStore interface {
	ListBySession(ctx context.Context, orgID, sessionID uuid.UUID) ([]models.SessionThread, error)
}

// SessionTitleService generates and updates session titles using an LLM.
type SessionTitleService struct {
	llm      llmClient
	sessions titleSessionStore
	messages titleMessageStore
	threads  titleThreadStore
}

func NewSessionTitleService(llm llmClient, sessions titleSessionStore, messages titleMessageStore, threads titleThreadStore) *SessionTitleService {
	return &SessionTitleService{
		llm:      llm,
		sessions: sessions,
		messages: messages,
		threads:  threads,
	}
}

// MaybeRegenerateTitle regenerates the session title if the current turn is
// a multiple of regenerateEveryN (3, 6, 9, ...). It is safe to call on every
// turn — it will no-op when regeneration is not due.
func (s *SessionTitleService) MaybeRegenerateTitle(ctx context.Context, orgID, sessionID uuid.UUID, completedThreadID *uuid.UUID) error {
	session, err := s.sessions.GetByID(ctx, orgID, sessionID)
	if err != nil {
		return fmt.Errorf("fetch session: %w", err)
	}

	if session.CurrentTurn < regenerateEveryN || session.CurrentTurn%regenerateEveryN != 0 {
		return nil
	}

	if completedThreadID != nil {
		isPrimary, err := completedThreadIDIsPrimary(ctx, s.threads, orgID, sessionID, *completedThreadID)
		if err != nil {
			return err
		}
		if !isPrimary {
			return nil
		}
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

	title, ok := CleanTitle(generated)
	if !ok || title == currentTitle {
		return nil
	}

	if err := s.sessions.UpdateTitle(ctx, orgID, sessionID, title); err != nil {
		return fmt.Errorf("update title: %w", err)
	}
	return nil
}

func completedThreadIDIsPrimary(ctx context.Context, threads titleThreadStore, orgID, sessionID, completedThreadID uuid.UUID) (bool, error) {
	if completedThreadID == uuid.Nil || threads == nil {
		return true, nil
	}
	sessionThreads, err := threads.ListBySession(ctx, orgID, sessionID)
	if err != nil {
		return false, fmt.Errorf("fetch session threads: %w", err)
	}
	if len(sessionThreads) == 0 {
		return false, nil
	}
	return sessionThreads[0].ID == completedThreadID, nil
}

// CleanTitle trims whitespace and surrounding quotes from a generated title,
// returning false if the result is empty or exceeds maxTitleLen.
func CleanTitle(s string) (string, bool) {
	s = strings.TrimSpace(s)
	s = strings.Trim(s, "\"'")
	if len(s) == 0 || len(s) > maxTitleLen {
		return "", false
	}
	return s, true
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
