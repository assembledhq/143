package ingestion

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// mockLLMClient implements llm.Client for testing.
type mockLLMClient struct {
	response string
	err      error
	calls    int
}

func (m *mockLLMClient) Complete(_ context.Context, _, _ string) (string, error) {
	m.calls++
	return m.response, m.err
}

func TestShouldSummarize(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		messages []SlackMessage
		expected bool
	}{
		{
			name:     "empty messages returns false",
			messages: []SlackMessage{},
			expected: false,
		},
		{
			name: "single short message returns false",
			messages: []SlackMessage{
				{User: "alice", Text: "hey there", Timestamp: "1700000000.000000"},
			},
			expected: false,
		},
		{
			name: "single long message returns true",
			messages: []SlackMessage{
				{User: "alice", Text: strings.Repeat("a", 50), Timestamp: "1700000000.000000"},
			},
			expected: true,
		},
		{
			name: "thread with only trivial messages returns false",
			messages: []SlackMessage{
				{User: "alice", Text: "ok", Timestamp: "1700000000.000000"},
				{User: "bob", Text: "thanks", Timestamp: "1700000001.000000"},
				{User: "carol", Text: "lgtm", Timestamp: "1700000002.000000"},
			},
			expected: false,
		},
		{
			name: "thread with total chars under 100 returns false",
			messages: []SlackMessage{
				{User: "alice", Text: "This is a real message", Timestamp: "1700000000.000000"},
				{User: "bob", Text: "Another message here", Timestamp: "1700000001.000000"},
			},
			expected: false,
		},
		{
			name: "thread with sufficient non-trivial content returns true",
			messages: []SlackMessage{
				{User: "alice", Text: "We need to fix the production database issue that is causing timeouts for customers", Timestamp: "1700000000.000000"},
				{User: "bob", Text: "I can look into it, the connection pool might be exhausted", Timestamp: "1700000001.000000"},
			},
			expected: true,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := shouldSummarize(tc.messages)
			assert.Equal(t, tc.expected, got)
		})
	}
}

func mustMarshalMessages(t *testing.T, msgs []SlackMessage) json.RawMessage {
	t.Helper()
	data, err := json.Marshal(msgs)
	require.NoError(t, err)
	return data
}

func TestSummarizeThreads(t *testing.T) {
	t.Parallel()
	logger := zerolog.Nop()

	t.Run("threads that fail pre-filter get not_actionable analysis", func(t *testing.T) {
		t.Parallel()
		mock := &mockLLMClient{}
		s := NewSlackSummarizer(mock, "test-model", logger)

		threads := []SlackThreadSummary{
			{
				ChannelID:   "C123",
				ChannelName: "general",
				ThreadTS:    "1700000000.000000",
				Messages:    mustMarshalMessages(t, []SlackMessage{{User: "alice", Text: "ok", Timestamp: "1700000000.000000"}}),
			},
		}

		result, err := s.SummarizeThreads(context.Background(), threads)
		require.NoError(t, err)
		require.NotNil(t, result[0].Analysis)
		assert.False(t, result[0].Analysis.Actionable)
		assert.Equal(t, "not_actionable", result[0].Analysis.Category)
		assert.Equal(t, 0, mock.calls, "LLM should not be called for trivial threads")
	})

	t.Run("threads that pass filter get LLM analysis", func(t *testing.T) {
		t.Parallel()
		mock := &mockLLMClient{
			response: `{"actionable": true, "category": "bug_report", "summary": "Database timeout issue", "urgency": "high"}`,
		}
		s := NewSlackSummarizer(mock, "test-model", logger)

		threads := []SlackThreadSummary{
			{
				ChannelID:   "C123",
				ChannelName: "general",
				ThreadTS:    "1700000000.000000",
				Messages: mustMarshalMessages(t, []SlackMessage{
					{User: "alice", Text: "We need to fix the production database issue that is causing timeouts for customers", Timestamp: "1700000000.000000"},
					{User: "bob", Text: "I can look into it, the connection pool might be exhausted", Timestamp: "1700000001.000000"},
				}),
			},
		}

		result, err := s.SummarizeThreads(context.Background(), threads)
		require.NoError(t, err)
		require.NotNil(t, result[0].Analysis)
		assert.True(t, result[0].Analysis.Actionable)
		assert.Equal(t, "bug_report", result[0].Analysis.Category)
		assert.Equal(t, "Database timeout issue", result[0].Analysis.Summary)
		assert.Equal(t, "high", result[0].Analysis.Urgency)
		assert.Equal(t, 1, mock.calls)
	})

	t.Run("LLM returns valid JSON parsed correctly", func(t *testing.T) {
		t.Parallel()
		mock := &mockLLMClient{
			response: `{"actionable": false, "category": "discussion", "summary": "Team standup notes", "urgency": "none"}`,
		}
		s := NewSlackSummarizer(mock, "test-model", logger)

		threads := []SlackThreadSummary{
			{
				ChannelID:   "C123",
				ChannelName: "general",
				ThreadTS:    "1700000000.000000",
				Messages: mustMarshalMessages(t, []SlackMessage{
					{User: "alice", Text: strings.Repeat("standup discussion about project progress and blockers ", 3), Timestamp: "1700000000.000000"},
					{User: "bob", Text: "No blockers from my side, continuing with the feature work", Timestamp: "1700000001.000000"},
				}),
			},
		}

		result, err := s.SummarizeThreads(context.Background(), threads)
		require.NoError(t, err)
		require.NotNil(t, result[0].Analysis)
		assert.False(t, result[0].Analysis.Actionable)
		assert.Equal(t, "discussion", result[0].Analysis.Category)
		assert.Equal(t, "Team standup notes", result[0].Analysis.Summary)
	})

	t.Run("LLM returns JSON wrapped in text extracted correctly", func(t *testing.T) {
		t.Parallel()
		mock := &mockLLMClient{
			response: `Here is my analysis:
{"actionable": true, "category": "feature_request", "summary": "Request for dark mode", "urgency": "low"}
Hope this helps!`,
		}
		s := NewSlackSummarizer(mock, "test-model", logger)

		threads := []SlackThreadSummary{
			{
				ChannelID:   "C123",
				ChannelName: "product",
				ThreadTS:    "1700000000.000000",
				Messages: mustMarshalMessages(t, []SlackMessage{
					{User: "alice", Text: "Several customers have asked about dark mode support for the dashboard interface", Timestamp: "1700000000.000000"},
					{User: "bob", Text: "That would be great, I think we should add it to our roadmap for next quarter", Timestamp: "1700000001.000000"},
				}),
			},
		}

		result, err := s.SummarizeThreads(context.Background(), threads)
		require.NoError(t, err)
		require.NotNil(t, result[0].Analysis)
		assert.True(t, result[0].Analysis.Actionable)
		assert.Equal(t, "feature_request", result[0].Analysis.Category)
		assert.Equal(t, "Request for dark mode", result[0].Analysis.Summary)
	})

	t.Run("LLM returns invalid response graceful fallback", func(t *testing.T) {
		t.Parallel()
		mock := &mockLLMClient{
			err: fmt.Errorf("LLM service unavailable"),
		}
		s := NewSlackSummarizer(mock, "test-model", logger)

		threads := []SlackThreadSummary{
			{
				ChannelID:   "C123",
				ChannelName: "engineering",
				ThreadTS:    "1700000000.000000",
				Messages: mustMarshalMessages(t, []SlackMessage{
					{User: "alice", Text: "The deployment pipeline is broken and we need to fix it urgently before the release", Timestamp: "1700000000.000000"},
					{User: "bob", Text: "Let me check the CI logs, it might be a flaky test causing the failure", Timestamp: "1700000001.000000"},
				}),
			},
		}

		result, err := s.SummarizeThreads(context.Background(), threads)
		require.NoError(t, err)
		require.NotNil(t, result[0].Analysis)
		assert.True(t, result[0].Analysis.Actionable)
		assert.Equal(t, "discussion", result[0].Analysis.Category)
		assert.Equal(t, "low", result[0].Analysis.Urgency)
	})

	t.Run("summary over 200 chars gets truncated", func(t *testing.T) {
		t.Parallel()
		longSummary := strings.Repeat("x", 250)
		mock := &mockLLMClient{
			response: fmt.Sprintf(`{"actionable": true, "category": "bug_report", "summary": %q, "urgency": "medium"}`, longSummary),
		}
		s := NewSlackSummarizer(mock, "test-model", logger)

		threads := []SlackThreadSummary{
			{
				ChannelID:   "C123",
				ChannelName: "bugs",
				ThreadTS:    "1700000000.000000",
				Messages: mustMarshalMessages(t, []SlackMessage{
					{User: "alice", Text: "There is a serious bug in the payment processing module that affects all international transactions", Timestamp: "1700000000.000000"},
					{User: "bob", Text: "I can reproduce this consistently, it seems to be related to currency conversion logic", Timestamp: "1700000001.000000"},
				}),
			},
		}

		result, err := s.SummarizeThreads(context.Background(), threads)
		require.NoError(t, err)
		require.NotNil(t, result[0].Analysis)
		assert.Len(t, []rune(result[0].Analysis.Summary), 200, "summary should be truncated to 200 runes")
	})
}
