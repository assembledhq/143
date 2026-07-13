package services

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/models"
)

type mockTitleLLM struct {
	responses []string
	err       error
	errs      []error
	calls     int
	prompts   []string
}

func (m *mockTitleLLM) Complete(_ context.Context, _, userPrompt string) (string, error) {
	m.calls++
	m.prompts = append(m.prompts, userPrompt)
	if len(m.errs) > 0 {
		err := m.errs[0]
		m.errs = m.errs[1:]
		if err != nil {
			return "", err
		}
	}
	if m.err != nil {
		return "", m.err
	}
	if len(m.responses) == 0 {
		return "", nil
	}
	response := m.responses[0]
	m.responses = m.responses[1:]
	return response, nil
}

type mockTitleSessionStore struct {
	state         models.SessionTitleState
	getErr        error
	updatedTitle  string
	updatedSource models.SessionTitleSource
	updateErr     error
	pivotIntent   string
	pivotTurn     int
}

func (m *mockTitleSessionStore) UpdateTitleForPivot(_ context.Context, _, _ uuid.UUID, title, intent string, turn int) error {
	m.updatedTitle = title
	m.updatedSource = models.SessionTitleSourceGenerated
	m.pivotIntent = intent
	m.pivotTurn = turn
	return m.updateErr
}

func (m *mockTitleSessionStore) GetTitleState(context.Context, uuid.UUID, uuid.UUID) (models.SessionTitleState, error) {
	return m.state, m.getErr
}

func (m *mockTitleSessionStore) UpdateTitleWithSource(_ context.Context, _, _ uuid.UUID, title string, source models.SessionTitleSource) error {
	m.updatedTitle = title
	m.updatedSource = source
	return m.updateErr
}

type mockTitleMessageStore struct {
	messages []models.SessionMessage
	err      error
}

func (m *mockTitleMessageStore) ListTitleContext(_ context.Context, _, _ uuid.UUID, primaryThreadID *uuid.UUID, afterTurn, limit int) ([]models.SessionMessage, error) {
	if m.err != nil {
		return nil, m.err
	}
	filtered := make([]models.SessionMessage, 0, len(m.messages))
	for _, msg := range m.messages {
		if msg.Role != models.MessageRoleUser || msg.Source != "" {
			continue
		}
		if primaryThreadID != nil && msg.ThreadID != nil && *msg.ThreadID != *primaryThreadID {
			continue
		}
		if len(filtered) == 0 || msg.TurnNumber > afterTurn {
			filtered = append(filtered, msg)
		}
	}
	if len(filtered) > limit+1 {
		filtered = append(filtered[:1], filtered[len(filtered)-limit:]...)
	}
	return filtered, nil
}

type mockTitleThreadStore struct {
	threads []models.SessionThread
	err     error
}

func (m *mockTitleThreadStore) ListBySession(context.Context, uuid.UUID, uuid.UUID) ([]models.SessionThread, error) {
	return m.threads, m.err
}

func titleTestMessages(primaryID, secondaryID uuid.UUID) []models.SessionMessage {
	return []models.SessionMessage{
		{ID: 1, TurnNumber: 0, Role: models.MessageRoleUser, ThreadID: &primaryID, Content: "Fix the authentication redirect loop"},
		{ID: 2, TurnNumber: 1, Role: models.MessageRoleAssistant, ThreadID: &primaryID, Content: "I am now changing billing code"},
		{ID: 3, TurnNumber: 2, Role: models.MessageRoleUser, ThreadID: &secondaryID, Content: "Replace this with a billing export"},
		{ID: 4, TurnNumber: 3, Role: models.MessageRoleUser, ThreadID: &primaryID, Source: models.SessionMessageSourceSystemAutoRepair, Content: "Fix CI automatically"},
		{ID: 5, TurnNumber: 4, Role: models.MessageRoleUser, ThreadID: &primaryID, Content: "Add regression tests"},
	}
}

func newTitleTestService(turn int, source models.SessionTitleSource, responses ...string) (*SessionTitleService, *mockTitleLLM, *mockTitleSessionStore, uuid.UUID, uuid.UUID) {
	title := "Fix authentication redirect loop"
	primaryID := uuid.New()
	secondaryID := uuid.New()
	llm := &mockTitleLLM{responses: responses}
	sessions := &mockTitleSessionStore{state: models.SessionTitleState{Title: &title, TitleSource: source, CurrentTurn: turn}}
	messages := &mockTitleMessageStore{messages: titleTestMessages(primaryID, secondaryID)}
	threads := &mockTitleThreadStore{threads: []models.SessionThread{{ID: primaryID}, {ID: secondaryID}}}
	return NewSessionTitleService(llm, sessions, messages, threads), llm, sessions, primaryID, secondaryID
}

func TestMaybeUpdateTitleForPivot_Cadence(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		turn      int
		wantCalls int
	}{
		{name: "before first check", turn: 9, wantCalls: 0},
		{name: "first check", turn: 10, wantCalls: 1},
		{name: "between checks", turn: 11, wantCalls: 0},
		{name: "second check", turn: 20, wantCalls: 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			svc, llm, _, primaryID, _ := newTitleTestService(tt.turn, models.SessionTitleSourceGenerated, "KEEP")
			err := svc.MaybeUpdateTitleForPivot(context.Background(), uuid.New(), uuid.New(), &primaryID)
			require.NoError(t, err, "pivot evaluation should respect the configured cadence")
			require.Equal(t, tt.wantCalls, llm.calls, "pivot classifier should run only on due turns")
		})
	}
}

func TestMaybeUpdateTitleForPivot_ProtectedTitles(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		source models.SessionTitleSource
	}{
		{name: "legacy", source: models.SessionTitleSourceLegacy},
		{name: "manual", source: models.SessionTitleSourceManual},
		{name: "issue", source: models.SessionTitleSourceIssue},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			svc, llm, sessions, primaryID, _ := newTitleTestService(10, tt.source, "PIVOT\nBuild billing exports")
			err := svc.MaybeUpdateTitleForPivot(context.Background(), uuid.New(), uuid.New(), &primaryID)
			require.NoError(t, err, "protected titles should be left unchanged")
			require.Equal(t, 0, llm.calls, "protected titles should skip the classifier")
			require.Empty(t, sessions.updatedTitle, "protected titles should not be overwritten")
		})
	}
}

func TestMaybeUpdateTitleForPivot_SkipsSecondaryThread(t *testing.T) {
	t.Parallel()

	svc, llm, sessions, _, secondaryID := newTitleTestService(10, models.SessionTitleSourceGenerated, "PIVOT\nBuild billing exports")
	err := svc.MaybeUpdateTitleForPivot(context.Background(), uuid.New(), uuid.New(), &secondaryID)
	require.NoError(t, err, "secondary-thread completion should be ignored")
	require.Equal(t, 0, llm.calls, "secondary-thread completion should not invoke the classifier")
	require.Empty(t, sessions.updatedTitle, "secondary-thread completion should not change the title")
}

func TestMaybeUpdateTitleForPivot_UsesPrimaryUserMessagesOnly(t *testing.T) {
	t.Parallel()

	svc, llm, _, primaryID, _ := newTitleTestService(10, models.SessionTitleSourceGenerated, "KEEP")
	err := svc.MaybeUpdateTitleForPivot(context.Background(), uuid.New(), uuid.New(), &primaryID)
	require.NoError(t, err, "valid primary-thread context should be classified")
	require.Len(t, llm.prompts, 1, "classifier should receive one user prompt")
	require.Contains(t, llm.prompts[0], "Fix the authentication redirect loop", "prompt should include the original request")
	require.Contains(t, llm.prompts[0], "Add regression tests", "prompt should include later primary-thread user instructions")
	require.NotContains(t, llm.prompts[0], "changing billing code", "prompt should exclude assistant activity")
	require.NotContains(t, llm.prompts[0], "Replace this with a billing export", "prompt should exclude secondary-thread instructions")
	require.NotContains(t, llm.prompts[0], "Fix CI automatically", "prompt should exclude system-authored user-role messages")
}

func TestMaybeUpdateTitleForPivot_SkipsClassifierWithoutLaterInstructions(t *testing.T) {
	t.Parallel()

	svc, llm, sessions, primaryID, _ := newTitleTestService(10, models.SessionTitleSourceGenerated, "PIVOT\nignored")
	// Only the original request exists on the primary thread; no later
	// user-authored instruction can constitute a pivot. The service keeps the
	// existing title set by newTitleTestService without calling the classifier.
	svc.messages = &mockTitleMessageStore{messages: []models.SessionMessage{
		{Role: models.MessageRoleUser, ThreadID: &primaryID, Content: "Fix the authentication redirect loop"},
		{Role: models.MessageRoleAssistant, ThreadID: &primaryID, Content: "Working on it"},
	}}
	err := svc.MaybeUpdateTitleForPivot(context.Background(), uuid.New(), uuid.New(), &primaryID)
	require.NoError(t, err, "a single original request should be handled without error")
	require.Equal(t, 0, llm.calls, "classifier should not run when there are no later user instructions")
	require.Empty(t, sessions.updatedTitle, "title should be kept when no pivot is possible")
}

func TestMaybeUpdateTitleForPivot_UpdatesOnlyForExplicitPivot(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		responses    []string
		wantTitle    string
		wantLLMCalls int
	}{
		{name: "keep", responses: []string{"KEEP"}, wantLLMCalls: 1},
		{name: "malformed fails closed", responses: []string{"Maybe pivot"}, wantLLMCalls: 1},
		{name: "multiline objective fails closed", responses: []string{"PIVOT\nBuild exports\nAnd invoices"}, wantLLMCalls: 1},
		{name: "invalid generated title keeps current", responses: []string{"PIVOT\nBuild a billing export", strings.Repeat("a", maxTitleLen+1)}, wantLLMCalls: 2},
		{name: "same display title still accepts pivot", responses: []string{"PIVOT\nRepair the authentication redirect", "Fix authentication redirect loop"}, wantTitle: "Fix authentication redirect loop", wantLLMCalls: 2},
		{name: "explicit pivot", responses: []string{"PIVOT\nBuild a billing export", "Add billing export"}, wantTitle: "Add billing export", wantLLMCalls: 2},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			svc, llm, sessions, primaryID, _ := newTitleTestService(10, models.SessionTitleSourceGenerated, tt.responses...)
			err := svc.MaybeUpdateTitleForPivot(context.Background(), uuid.New(), uuid.New(), &primaryID)
			require.NoError(t, err, "classifier output should be handled conservatively")
			require.Equal(t, tt.wantLLMCalls, llm.calls, "title generation should run only after an explicit pivot")
			require.Equal(t, tt.wantTitle, sessions.updatedTitle, "only a valid pivot should update the title")
			if tt.wantTitle != "" {
				require.Equal(t, models.SessionTitleSourceGenerated, sessions.updatedSource, "pivot titles should remain generated")
				require.NotEmpty(t, sessions.pivotIntent, "accepted pivots should persist their primary objective")
				require.Equal(t, 10, sessions.pivotTurn, "accepted pivots should persist the evaluation turn")
			}
		})
	}
}

func TestMaybeUpdateTitleForPivot_UsesAcceptedPivotAsBaseline(t *testing.T) {
	t.Parallel()

	svc, llm, sessions, primaryID, _ := newTitleTestService(20, models.SessionTitleSourceGenerated, "KEEP")
	intent := "Build a billing export"
	pivotTurn := 10
	sessions.state.TitleIntent = &intent
	sessions.state.TitlePivotedAtTurn = &pivotTurn
	svc.messages = &mockTitleMessageStore{messages: []models.SessionMessage{
		{ID: 1, TurnNumber: 0, Role: models.MessageRoleUser, ThreadID: &primaryID, Content: "Fix the authentication redirect loop"},
		{ID: 2, TurnNumber: 10, Role: models.MessageRoleUser, ThreadID: &primaryID, Content: "Stop auth work and build a billing export"},
		{ID: 3, TurnNumber: 20, Role: models.MessageRoleUser, ThreadID: &primaryID, Content: "Add CSV formatting"},
	}}

	err := svc.MaybeUpdateTitleForPivot(context.Background(), uuid.New(), uuid.New(), &primaryID)
	require.NoError(t, err, "later checks should use the accepted pivot objective")
	require.Len(t, llm.prompts, 1, "eligible later instruction should invoke the classifier once")
	require.Contains(t, llm.prompts[0], "Current accepted objective:\nBuild a billing export", "prompt should use the persisted accepted objective")
	require.NotContains(t, llm.prompts[0], "Stop auth work", "previously accepted pivot instruction should not be reconsidered")
	require.Contains(t, llm.prompts[0], "Add CSV formatting", "instructions after the accepted pivot should be considered")
}

func TestMaybeUpdateTitleForPivot_GeneratesMissingTitleFromOriginal(t *testing.T) {
	t.Parallel()

	svc, llm, sessions, primaryID, _ := newTitleTestService(10, models.SessionTitleSourceGenerated, "Fix auth redirect loop")
	sessions.state.Title = nil
	err := svc.MaybeUpdateTitleForPivot(context.Background(), uuid.New(), uuid.New(), &primaryID)
	require.NoError(t, err, "missing title should be generated from the original request")
	require.Equal(t, 1, llm.calls, "missing title should skip pivot classification")
	require.Equal(t, "Fix auth redirect loop", sessions.updatedTitle, "missing title should use the generated original-intent title")
	require.Equal(t, "Fix the authentication redirect loop", llm.prompts[0], "generation should use only the original request")
}

func TestMaybeUpdateTitleForPivot_LLMError(t *testing.T) {
	t.Parallel()

	svc, llm, _, primaryID, _ := newTitleTestService(10, models.SessionTitleSourceGenerated)
	llm.err = errors.New("timeout")
	err := svc.MaybeUpdateTitleForPivot(context.Background(), uuid.New(), uuid.New(), &primaryID)
	require.ErrorContains(t, err, "detect title pivot", "classifier failures should be returned for worker logging")
}

func TestMaybeUpdateTitleForPivot_GenerationAndPersistenceErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		generation error
		updateErr  error
		wantError  string
	}{
		{name: "generation", generation: errors.New("generation timeout"), wantError: "generate session title"},
		{name: "persistence", updateErr: errors.New("database unavailable"), wantError: "update pivot title"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			svc, llm, sessions, primaryID, _ := newTitleTestService(10, models.SessionTitleSourceGenerated, "PIVOT\nBuild a billing export", "Add billing export")
			llm.errs = []error{nil, tt.generation}
			sessions.updateErr = tt.updateErr

			err := svc.MaybeUpdateTitleForPivot(context.Background(), uuid.New(), uuid.New(), &primaryID)
			require.ErrorContains(t, err, tt.wantError, "pivot failures should propagate with their phase")
		})
	}
}

func TestParsePivotDecision(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		value         string
		wantObjective string
		wantPivot     bool
	}{
		{name: "keep", value: " KEEP ", wantPivot: false},
		{name: "pivot", value: "PIVOT\nBuild checkout analytics", wantObjective: "Build checkout analytics", wantPivot: true},
		{name: "empty", value: "PIVOT\n", wantPivot: false},
		{name: "extra lines", value: "PIVOT\nBuild analytics\nMore", wantPivot: false},
		{name: "wrong case", value: "pivot\nBuild analytics", wantPivot: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			objective, pivot := parsePivotDecision(tt.value)
			require.Equal(t, tt.wantObjective, objective, "parser should return the expected objective")
			require.Equal(t, tt.wantPivot, pivot, "parser should fail closed for invalid output")
		})
	}
}

func TestCleanTitle(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		value string
		want  string
		ok    bool
	}{
		{name: "trim quotes", value: "  \"Fix auth bug\"  ", want: "Fix auth bug", ok: true},
		{name: "empty", value: "   ", ok: false},
		{name: "too long", value: strings.Repeat("a", maxTitleLen+1), ok: false},
		{name: "unicode character limit", value: strings.Repeat("界", maxTitleLen), want: strings.Repeat("界", maxTitleLen), ok: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			title, ok := CleanTitle(tt.value)
			require.Equal(t, tt.want, title, "title cleanup should return the expected value")
			require.Equal(t, tt.ok, ok, "title cleanup should report validity")
		})
	}
}
