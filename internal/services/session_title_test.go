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

type mockTitleThreadStore struct {
	threads []models.SessionThread
	err     error
}

func (m *mockTitleThreadStore) ListBySession(_ context.Context, _, _ uuid.UUID) ([]models.SessionThread, error) {
	return m.threads, m.err
}

func titleThreadStoreWithPrimary(primaryThreadID uuid.UUID) *mockTitleThreadStore {
	return &mockTitleThreadStore{
		threads: []models.SessionThread{
			{ID: primaryThreadID},
			{ID: uuid.New()},
		},
	}
}

// --- tests ---

func TestMaybeRegenerateTitle_SkipsWhenNotDue(t *testing.T) {
	t.Parallel()
	llm := &mockLLM{response: "New Title"}
	for _, turn := range []int{0, 1, 2, 4, 5} {
		sessions := &mockTitleSessionStore{
			session: models.Session{CurrentTurn: turn},
		}
		primaryThreadID := uuid.New()
		svc := NewSessionTitleService(llm, sessions, &mockTitleMessageStore{}, titleThreadStoreWithPrimary(primaryThreadID))

		err := svc.MaybeRegenerateTitle(context.Background(), uuid.New(), uuid.New(), &primaryThreadID)
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
	primaryThreadID := uuid.New()
	svc := NewSessionTitleService(llm, sessions, messages, titleThreadStoreWithPrimary(primaryThreadID))

	err := svc.MaybeRegenerateTitle(context.Background(), uuid.New(), uuid.New(), &primaryThreadID)
	require.NoError(t, err)
	require.Equal(t, 1, llm.calls, "should still consult the LLM on due turns for potential topic shifts")
	require.Equal(t, "New title from LLM", sessions.updatedTitle, "should update the title when the model returns a new dominant topic")
}

func TestMaybeRegenerateTitle_FillsMissingTitleWhenDue(t *testing.T) {
	t.Parallel()
	llm := &mockLLM{response: "New title from LLM"}
	sessions := &mockTitleSessionStore{
		session: models.Session{CurrentTurn: 3},
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
	primaryThreadID := uuid.New()
	svc := NewSessionTitleService(llm, sessions, messages, titleThreadStoreWithPrimary(primaryThreadID))

	err := svc.MaybeRegenerateTitle(context.Background(), uuid.New(), uuid.New(), &primaryThreadID)
	require.NoError(t, err, "missing titles should still be generated on regeneration turns")
	require.Equal(t, 1, llm.calls, "should call LLM when session title is missing")
	require.Equal(t, "New title from LLM", sessions.updatedTitle, "should persist the generated title when session title is missing")
}

func TestMaybeRegenerateTitle_SkipsSecondaryThread(t *testing.T) {
	t.Parallel()

	title := "Fix auth redirect loop"
	llm := &mockLLM{response: "Review flaky checkout tests"}
	sessions := &mockTitleSessionStore{
		session: models.Session{Title: &title, CurrentTurn: 3},
	}
	messages := &mockTitleMessageStore{
		messages: []models.SessionMessage{
			{TurnNumber: 0, Role: models.MessageRoleUser, Content: "Fix the auth redirect loop"},
			{TurnNumber: 2, Role: models.MessageRoleUser, Content: "Review flaky checkout tests in a side tab"},
		},
	}
	primaryThreadID := uuid.New()
	secondaryThreadID := uuid.New()
	threads := &mockTitleThreadStore{
		threads: []models.SessionThread{
			{ID: primaryThreadID},
			{ID: secondaryThreadID},
		},
	}
	svc := NewSessionTitleService(llm, sessions, messages, threads)

	err := svc.MaybeRegenerateTitle(context.Background(), uuid.New(), uuid.New(), &secondaryThreadID)
	require.NoError(t, err, "secondary thread completion should not fail title regeneration")
	require.Equal(t, 0, llm.calls, "secondary thread completion should not call the title LLM")
	require.Empty(t, sessions.updatedTitle, "secondary thread completion should not update the session title")
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
	primaryThreadID := uuid.New()
	svc := NewSessionTitleService(llm, sessions, messages, titleThreadStoreWithPrimary(primaryThreadID))

	err := svc.MaybeRegenerateTitle(context.Background(), uuid.New(), uuid.New(), &primaryThreadID)
	require.NoError(t, err, "unchanged titles should be accepted without updating")
	require.Equal(t, 1, llm.calls, "should ask the LLM whether the title still fits")
	require.Empty(t, sessions.updatedTitle, "should not update when the LLM keeps the existing title")
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
	primaryThreadID := uuid.New()
	svc := NewSessionTitleService(llm, sessions, messages, titleThreadStoreWithPrimary(primaryThreadID))

	// Empty response
	llm.response = "   "
	err := svc.MaybeRegenerateTitle(context.Background(), uuid.New(), uuid.New(), &primaryThreadID)
	require.NoError(t, err)
	assert.Empty(t, sessions.updatedTitle)

	// Too long response — reset for next call
	sessions.session.CurrentTurn = 6
	llm.response = string(make([]byte, 121))
	err = svc.MaybeRegenerateTitle(context.Background(), uuid.New(), uuid.New(), &primaryThreadID)
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
	primaryThreadID := uuid.New()
	svc := NewSessionTitleService(llm, sessions, messages, titleThreadStoreWithPrimary(primaryThreadID))

	err := svc.MaybeRegenerateTitle(context.Background(), uuid.New(), uuid.New(), &primaryThreadID)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "llm completion")
}

func TestMaybeRegenerateTitle_NoMessages(t *testing.T) {
	t.Parallel()
	title := "Old"
	llm := &mockLLM{response: "New"}
	sessions := &mockTitleSessionStore{session: models.Session{Title: &title, CurrentTurn: 3}}
	messages := &mockTitleMessageStore{messages: nil}
	primaryThreadID := uuid.New()
	svc := NewSessionTitleService(llm, sessions, messages, titleThreadStoreWithPrimary(primaryThreadID))

	err := svc.MaybeRegenerateTitle(context.Background(), uuid.New(), uuid.New(), &primaryThreadID)
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
