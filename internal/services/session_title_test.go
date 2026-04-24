package services

import (
	"context"
	"errors"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/models"
)

// --- mocks ---

type mockLLM struct {
	response string
	err      error
	calls    int
}

func (m *mockLLM) Complete(_ context.Context, _, _ string) (string, error) {
	m.calls++
	return m.response, m.err
}

type mockTitleSessionStore struct {
	session      models.Session
	getErr       error
	updatedTitle string
	updateErr    error
}

func (m *mockTitleSessionStore) GetByID(_ context.Context, _, _ uuid.UUID) (models.Session, error) {
	return m.session, m.getErr
}

func (m *mockTitleSessionStore) UpdateTitle(_ context.Context, _, _ uuid.UUID, title string) error {
	m.updatedTitle = title
	return m.updateErr
}

type mockTitleMessageStore struct {
	messages []models.SessionMessage
	err      error
}

func (m *mockTitleMessageStore) ListBySession(_ context.Context, _, _ uuid.UUID) ([]models.SessionMessage, error) {
	return m.messages, m.err
}

// --- tests ---

func TestMaybeRegenerateTitle_SkipsWhenNotDue(t *testing.T) {
	t.Parallel()
	llm := &mockLLM{response: "New Title"}
	for _, turn := range []int{0, 1, 2, 4, 5} {
		sessions := &mockTitleSessionStore{
			session: models.Session{CurrentTurn: turn},
		}
		svc := NewSessionTitleService(llm, sessions, &mockTitleMessageStore{})

		err := svc.MaybeRegenerateTitle(context.Background(), uuid.New(), uuid.New())
		require.NoError(t, err)
		assert.Equal(t, 0, llm.calls, "should not call LLM on turn %d", turn)
	}
}

func TestMaybeRegenerateTitle_RunsOnThirdTurn(t *testing.T) {
	t.Parallel()
	title := "Old title"
	llm := &mockLLM{response: "New title from LLM"}
	sessions := &mockTitleSessionStore{
		session: models.Session{Title: &title, CurrentTurn: 3},
	}
	messages := &mockTitleMessageStore{
		messages: []models.SessionMessage{
			{TurnNumber: 0, Role: models.MessageRoleUser, Content: "Fix the login bug"},
			{TurnNumber: 0, Role: models.MessageRoleAssistant, Content: "I'll fix it"},
			{TurnNumber: 1, Role: models.MessageRoleUser, Content: "Also update tests"},
			{TurnNumber: 1, Role: models.MessageRoleAssistant, Content: "Done"},
			{TurnNumber: 2, Role: models.MessageRoleUser, Content: "Now refactor auth"},
			{TurnNumber: 2, Role: models.MessageRoleAssistant, Content: "Refactored"},
		},
	}
	svc := NewSessionTitleService(llm, sessions, messages)

	err := svc.MaybeRegenerateTitle(context.Background(), uuid.New(), uuid.New())
	require.NoError(t, err)
	assert.Equal(t, 1, llm.calls)
	assert.Equal(t, "New title from LLM", sessions.updatedTitle)
}

func TestMaybeRegenerateTitle_SkipsUpdateWhenTitleUnchanged(t *testing.T) {
	t.Parallel()
	title := "Same title"
	llm := &mockLLM{response: "Same title"}
	sessions := &mockTitleSessionStore{
		session: models.Session{Title: &title, CurrentTurn: 3},
	}
	messages := &mockTitleMessageStore{
		messages: []models.SessionMessage{
			{TurnNumber: 0, Role: models.MessageRoleUser, Content: "Do something"},
		},
	}
	svc := NewSessionTitleService(llm, sessions, messages)

	err := svc.MaybeRegenerateTitle(context.Background(), uuid.New(), uuid.New())
	require.NoError(t, err)
	assert.Equal(t, 1, llm.calls)
	assert.Empty(t, sessions.updatedTitle, "should not update when title unchanged")
}

func TestMaybeRegenerateTitle_SkipsEmptyOrTooLong(t *testing.T) {
	t.Parallel()
	title := "Old"
	llm := &mockLLM{}
	sessions := &mockTitleSessionStore{session: models.Session{Title: &title, CurrentTurn: 3}}
	messages := &mockTitleMessageStore{
		messages: []models.SessionMessage{
			{TurnNumber: 0, Role: models.MessageRoleUser, Content: "task"},
		},
	}
	svc := NewSessionTitleService(llm, sessions, messages)

	// Empty response
	llm.response = "   "
	err := svc.MaybeRegenerateTitle(context.Background(), uuid.New(), uuid.New())
	require.NoError(t, err)
	assert.Empty(t, sessions.updatedTitle)

	// Too long response — reset for next call
	sessions.session.CurrentTurn = 6
	llm.response = string(make([]byte, 121))
	err = svc.MaybeRegenerateTitle(context.Background(), uuid.New(), uuid.New())
	require.NoError(t, err)
	assert.Empty(t, sessions.updatedTitle)
}

func TestMaybeRegenerateTitle_LLMError(t *testing.T) {
	t.Parallel()
	title := "Old"
	llm := &mockLLM{err: errors.New("timeout")}
	sessions := &mockTitleSessionStore{session: models.Session{Title: &title, CurrentTurn: 3}}
	messages := &mockTitleMessageStore{
		messages: []models.SessionMessage{
			{TurnNumber: 0, Role: models.MessageRoleUser, Content: "task"},
		},
	}
	svc := NewSessionTitleService(llm, sessions, messages)

	err := svc.MaybeRegenerateTitle(context.Background(), uuid.New(), uuid.New())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "llm completion")
}

func TestMaybeRegenerateTitle_NoMessages(t *testing.T) {
	t.Parallel()
	title := "Old"
	llm := &mockLLM{response: "New"}
	sessions := &mockTitleSessionStore{session: models.Session{Title: &title, CurrentTurn: 3}}
	messages := &mockTitleMessageStore{messages: nil}
	svc := NewSessionTitleService(llm, sessions, messages)

	err := svc.MaybeRegenerateTitle(context.Background(), uuid.New(), uuid.New())
	require.NoError(t, err)
	assert.Equal(t, 0, llm.calls, "should not call LLM with no messages")
}

func TestCleanTitle(t *testing.T) {
	t.Parallel()
	title, ok := CleanTitle("  \"Fix auth bug\"  ")
	assert.True(t, ok)
	assert.Equal(t, "Fix auth bug", title)

	_, ok = CleanTitle("   ")
	assert.False(t, ok)

	_, ok = CleanTitle(string(make([]byte, 121)))
	assert.False(t, ok)
}

func TestNormalizeEditableTitle(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		input     string
		want      string
		expectErr bool
	}{
		{
			name:  "trims surrounding whitespace",
			input: "  Fix auth bug  ",
			want:  "Fix auth bug",
		},
		{
			name:  "allows clearing the custom title",
			input: "   ",
			want:  "",
		},
		{
			name:      "rejects titles longer than the limit",
			input:     string(make([]byte, 121)),
			expectErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := NormalizeEditableTitle(tt.input)
			if tt.expectErr {
				require.Error(t, err, "NormalizeEditableTitle should reject invalid input")
				return
			}

			require.NoError(t, err, "NormalizeEditableTitle should accept valid input")
			require.Equal(t, tt.want, got, "NormalizeEditableTitle should return the normalized title")
		})
	}
}

func TestBuildTitleUserPrompt(t *testing.T) {
	t.Parallel()
	msgs := []models.SessionMessage{
		{Role: models.MessageRoleUser, Content: "Fix the login bug in auth service"},
		{Role: models.MessageRoleAssistant, Content: "I found the issue in auth.go"},
		{Role: models.MessageRoleUser, Content: "Now add tests for it"},
		{Role: models.MessageRoleAssistant, Content: "Tests added"},
	}

	result := buildTitleUserPrompt(msgs)
	assert.Contains(t, result, "Original task:")
	assert.Contains(t, result, "Fix the login bug")
	assert.Contains(t, result, "Recent conversation:")
	assert.Contains(t, result, "User: Now add tests")
	assert.Contains(t, result, "Assistant: Tests added")
}

func TestBuildTitleUserPrompt_TruncatesLongMessages(t *testing.T) {
	t.Parallel()
	longContent := ""
	for i := 0; i < 300; i++ {
		longContent += "a"
	}
	msgs := []models.SessionMessage{
		{Role: models.MessageRoleUser, Content: longContent},
	}

	result := buildTitleUserPrompt(msgs)
	assert.Contains(t, result, "...")
	assert.NotContains(t, result, longContent, "should not contain the full 300-char string")
}

func TestRecentMessages(t *testing.T) {
	t.Parallel()
	msgs := make([]models.SessionMessage, 10)
	for i := range msgs {
		msgs[i] = models.SessionMessage{TurnNumber: i}
	}

	recent := recentMessages(msgs, 4)
	assert.Len(t, recent, 4)
	assert.Equal(t, 6, recent[0].TurnNumber)
	assert.Equal(t, 9, recent[3].TurnNumber)

	// Fewer messages than requested
	small := recentMessages(msgs[:2], 4)
	assert.Len(t, small, 2)
}

func TestTruncate(t *testing.T) {
	t.Parallel()
	assert.Equal(t, "hello", truncate("hello", 10))
	assert.Equal(t, "hel...", truncate("hello world", 3))
	assert.Equal(t, "hello", truncate("  hello  ", 10))
}
