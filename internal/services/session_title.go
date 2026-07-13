package services

import (
	"context"
	"fmt"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/rs/zerolog"

	"github.com/assembledhq/143/internal/metrics"
	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/prompts"
)

const (
	pivotCheckEveryN            = 10
	maxOriginalDescriptionChars = 2000
	maxCandidateMessageChars    = 1000
	maxCandidateMessages        = 8
	maxTitleLen                 = 120
)

type llmClient interface {
	Complete(ctx context.Context, systemPrompt, userPrompt string) (string, error)
}

type titleSessionStore interface {
	GetTitleState(ctx context.Context, orgID, sessionID uuid.UUID) (models.SessionTitleState, error)
	UpdateTitleWithSource(ctx context.Context, orgID, sessionID uuid.UUID, title string, source models.SessionTitleSource) error
	UpdateTitleForPivot(ctx context.Context, orgID, sessionID uuid.UUID, title, intent string, pivotedAtTurn int) error
}

type titleMessageStore interface {
	ListTitleContext(ctx context.Context, orgID, sessionID uuid.UUID, primaryThreadID *uuid.UUID, afterTurn, limit int) ([]models.SessionMessage, error)
}

type titleThreadStore interface {
	ListBySession(ctx context.Context, orgID, sessionID uuid.UUID) ([]models.SessionThread, error)
}

// SessionTitleService conservatively updates generated session titles after an
// explicit user-authored pivot. The original request remains authoritative.
type SessionTitleService struct {
	llm      llmClient
	sessions titleSessionStore
	messages titleMessageStore
	threads  titleThreadStore
}

func NewSessionTitleService(llm llmClient, sessions titleSessionStore, messages titleMessageStore, threads titleThreadStore) *SessionTitleService {
	return &SessionTitleService{llm: llm, sessions: sessions, messages: messages, threads: threads}
}

// MaybeUpdateTitleForPivot checks for a new primary objective every ten turns.
// It only considers user-authored messages from the primary thread and never
// replaces legacy, manual, or issue-derived titles.
func (s *SessionTitleService) MaybeUpdateTitleForPivot(ctx context.Context, orgID, sessionID uuid.UUID, completedThreadID *uuid.UUID) error {
	state, err := s.sessions.GetTitleState(ctx, orgID, sessionID)
	if err != nil {
		logTitleFailure(ctx, sessionID, models.SessionTitleState{}, "state", err)
		return fmt.Errorf("fetch title state: %w", err)
	}
	if state.CurrentTurn < pivotCheckEveryN || state.CurrentTurn%pivotCheckEveryN != 0 {
		return nil
	}
	if state.TitleSource == models.SessionTitleSourceLegacy || state.TitleSource == models.SessionTitleSourceManual || state.TitleSource == models.SessionTitleSourceIssue {
		logTitleAction(ctx, sessionID, state, "skipped_protected")
		return nil
	}

	primaryThreadID, isPrimary, err := primaryThreadForCompletion(ctx, s.threads, orgID, sessionID, completedThreadID)
	if err != nil {
		logTitleFailure(ctx, sessionID, state, "thread", err)
		return err
	}
	if !isPrimary {
		logTitleAction(ctx, sessionID, state, "skipped_secondary_thread")
		return nil
	}

	afterTurn := -1
	if state.TitlePivotedAtTurn != nil {
		afterTurn = *state.TitlePivotedAtTurn
	}
	msgs, err := s.messages.ListTitleContext(ctx, orgID, sessionID, primaryThreadID, afterTurn, maxCandidateMessages)
	if err != nil {
		logTitleFailure(ctx, sessionID, state, "context", err)
		return fmt.Errorf("fetch messages: %w", err)
	}
	if len(msgs) == 0 {
		logTitleAction(ctx, sessionID, state, "skipped_no_user_messages")
		return nil
	}

	if state.Title == nil || strings.TrimSpace(*state.Title) == "" {
		updated, err := s.generateAndPersistTitle(ctx, orgID, sessionID, msgs[0].Content, models.SessionTitleSourceGenerated)
		if err != nil {
			logTitleFailure(ctx, sessionID, state, "missing_title_generation", err)
			return err
		}
		if updated {
			logTitleAction(ctx, sessionID, state, "generated_missing")
		} else {
			logTitleAction(ctx, sessionID, state, "kept_invalid_generation")
		}
		return nil
	}
	if len(msgs) == 1 {
		logTitleAction(ctx, sessionID, state, "kept_no_later_instructions")
		return nil
	}

	currentIntent := msgs[0].Content
	if state.TitleIntent != nil && strings.TrimSpace(*state.TitleIntent) != "" {
		currentIntent = *state.TitleIntent
	}
	detectionPrompt := buildPivotDetectionPrompt(*state.Title, msgs[0].Content, currentIntent, msgs[1:])
	callCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	decisionText, err := s.llm.Complete(callCtx, prompts.SessionTitlePivotPrompt(), detectionPrompt)
	cancel()
	if err != nil {
		logTitleFailure(ctx, sessionID, state, "classification", err)
		return fmt.Errorf("detect title pivot: %w", err)
	}

	pivotObjective, pivoted := parsePivotDecision(decisionText)
	if !pivoted {
		logTitleAction(ctx, sessionID, state, "kept")
		return nil
	}
	generated, valid, err := s.generateTitle(ctx, pivotObjective)
	if err != nil {
		logTitleFailure(ctx, sessionID, state, "generation", err)
		return err
	}
	if !valid {
		logTitleAction(ctx, sessionID, state, "kept_invalid_generation")
		return nil
	}
	titleChanged := generated != *state.Title
	if err := s.sessions.UpdateTitleForPivot(ctx, orgID, sessionID, generated, pivotObjective, state.CurrentTurn); err != nil {
		logTitleFailure(ctx, sessionID, state, "persistence", err)
		return fmt.Errorf("update pivot title: %w", err)
	}
	if titleChanged {
		logTitleAction(ctx, sessionID, state, "pivoted")
	} else {
		logTitleAction(ctx, sessionID, state, "accepted_pivot_same_title")
	}
	return nil
}

func logTitleAction(ctx context.Context, sessionID uuid.UUID, state models.SessionTitleState, action string) {
	metrics.RecordSessionTitleDecision(ctx, string(state.TitleSource), action)
	zerolog.Ctx(ctx).Debug().
		Str("session_id", sessionID.String()).
		Int("current_turn", state.CurrentTurn).
		Str("title_action", action).
		Str("title_source", string(state.TitleSource)).
		Msg("evaluated session title")
}

func logTitleFailure(ctx context.Context, sessionID uuid.UUID, state models.SessionTitleState, phase string, err error) {
	source := string(state.TitleSource)
	if source == "" {
		source = "unknown"
	}
	metrics.RecordSessionTitleDecision(ctx, source, "failed")
	zerolog.Ctx(ctx).Warn().
		Err(err).
		Str("session_id", sessionID.String()).
		Int("current_turn", state.CurrentTurn).
		Str("title_action", "failed").
		Str("title_phase", phase).
		Str("title_source", source).
		Msg("failed to evaluate session title")
}

func (s *SessionTitleService) generateAndPersistTitle(ctx context.Context, orgID, sessionID uuid.UUID, objective string, source models.SessionTitleSource) (bool, error) {
	title, ok, err := s.generateTitle(ctx, objective)
	if err != nil {
		return false, err
	}
	if !ok {
		return false, nil
	}
	if err := s.sessions.UpdateTitleWithSource(ctx, orgID, sessionID, title, source); err != nil {
		return false, fmt.Errorf("update title: %w", err)
	}
	return true, nil
}

func (s *SessionTitleService) generateTitle(ctx context.Context, objective string) (string, bool, error) {
	callCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	generated, err := s.llm.Complete(callCtx, prompts.SessionTitleGenerationPrompt(), truncate(objective, maxOriginalDescriptionChars))
	cancel()
	if err != nil {
		return "", false, fmt.Errorf("generate session title: %w", err)
	}
	title, ok := CleanTitle(generated)
	if !ok {
		return "", false, nil
	}
	return title, true, nil
}

func primaryThreadForCompletion(ctx context.Context, threads titleThreadStore, orgID, sessionID uuid.UUID, completedThreadID *uuid.UUID) (*uuid.UUID, bool, error) {
	if threads == nil {
		return nil, true, nil
	}
	sessionThreads, err := threads.ListBySession(ctx, orgID, sessionID)
	if err != nil {
		return nil, false, fmt.Errorf("fetch session threads: %w", err)
	}
	if len(sessionThreads) == 0 {
		return nil, completedThreadID == nil || *completedThreadID == uuid.Nil, nil
	}
	primaryID := sessionThreads[0].ID
	if completedThreadID != nil && *completedThreadID != uuid.Nil && *completedThreadID != primaryID {
		return &primaryID, false, nil
	}
	return &primaryID, true, nil
}

func buildPivotDetectionPrompt(currentTitle, originalRequest, currentIntent string, recent []models.SessionMessage) string {
	var b strings.Builder
	b.WriteString("Original request:\n")
	b.WriteString(truncate(originalRequest, maxOriginalDescriptionChars))
	b.WriteString("\n\nCurrent accepted objective:\n")
	b.WriteString(truncate(currentIntent, maxOriginalDescriptionChars))
	b.WriteString("\n\nCurrent title:\n")
	b.WriteString(currentTitle)

	if len(recent) > 0 {
		b.WriteString("\n\nLater user instructions:\n")
		for _, message := range recent {
			b.WriteString("- ")
			b.WriteString(truncate(message.Content, maxCandidateMessageChars))
			b.WriteByte('\n')
		}
	}
	return b.String()
}

func parsePivotDecision(value string) (string, bool) {
	value = strings.TrimSpace(value)
	if value == "KEEP" {
		return "", false
	}
	const prefix = "PIVOT\n"
	if !strings.HasPrefix(value, prefix) {
		return "", false
	}
	objective := strings.TrimSpace(strings.TrimPrefix(value, prefix))
	if objective == "" || strings.Contains(objective, "\n") {
		return "", false
	}
	return objective, true
}

// CleanTitle trims whitespace and surrounding quotes from a generated title.
func CleanTitle(s string) (string, bool) {
	s = strings.TrimSpace(s)
	s = strings.Trim(s, "\"'")
	if len(s) == 0 || utf8.RuneCountInString(s) > maxTitleLen {
		return "", false
	}
	return s, true
}

func truncate(s string, maxLen int) string {
	s = strings.TrimSpace(s)
	runes := []rune(s)
	if len(runes) <= maxLen {
		return s
	}
	return string(runes[:maxLen]) + "..."
}
