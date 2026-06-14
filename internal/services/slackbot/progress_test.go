package slackbot

import (
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestNormalizeProgressUpdate(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 6, 13, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name     string
		input    ProgressInput
		expected SlackProgressUpdate
	}{
		{
			name:  "normalizes test update kind",
			input: ProgressInput{UpdateKind: "tests", Title: "npm test", Summary: "unit tests", OccurredAt: now},
			expected: SlackProgressUpdate{
				Kind:       SlackProgressRunningTests,
				Title:      "npm test",
				Summary:    "unit tests",
				OccurredAt: now,
			},
		},
		{
			name:  "infers command update from title",
			input: ProgressInput{Title: "Running command", Summary: "go build ./cmd/server", OccurredAt: now},
			expected: SlackProgressUpdate{
				Kind:       SlackProgressRunningCommand,
				Title:      "Running command",
				Summary:    "go build ./cmd/server",
				OccurredAt: now,
			},
		},
		{
			name:  "terminal failure wins over kind",
			input: ProgressInput{UpdateKind: "tool", Title: "failed", Terminal: true, Failed: true, OccurredAt: now},
			expected: SlackProgressUpdate{
				Kind:       SlackProgressFailed,
				Title:      "failed",
				Terminal:   true,
				OccurredAt: now,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			actual := NormalizeProgressUpdate(tt.input)
			require.Equal(t, tt.expected, actual, "NormalizeProgressUpdate should return the expected Slack-safe progress event")
		})
	}
}

func TestShouldSendProgressUpdate(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 6, 13, 12, 0, 0, 0, time.UTC)
	policy := SlackProgressPolicy{
		MinUpdateInterval:     30 * time.Second,
		AlwaysSendTerminal:    true,
		SuppressDuplicateKind: true,
	}

	tests := []struct {
		name     string
		update   SlackProgressUpdate
		previous SlackProgressPrevious
		expected bool
	}{
		{
			name:     "sends first update",
			update:   SlackProgressUpdate{Kind: SlackProgressRunningAgent, OccurredAt: now},
			expected: true,
		},
		{
			name:     "suppresses rapid non-terminal update",
			update:   SlackProgressUpdate{Kind: SlackProgressRunningCommand, OccurredAt: now},
			previous: SlackProgressPrevious{UpdatedAt: now.Add(-5 * time.Second)},
			expected: false,
		},
		{
			name:     "sends update after debounce interval",
			update:   SlackProgressUpdate{Kind: SlackProgressRunningCommand, OccurredAt: now},
			previous: SlackProgressPrevious{UpdatedAt: now.Add(-45 * time.Second)},
			expected: true,
		},
		{
			name:     "terminal update bypasses debounce",
			update:   SlackProgressUpdate{Kind: SlackProgressCompleted, Terminal: true, OccurredAt: now},
			previous: SlackProgressPrevious{UpdatedAt: now.Add(-5 * time.Second)},
			expected: true,
		},
		{
			name:     "suppresses duplicate kind",
			update:   SlackProgressUpdate{Kind: SlackProgressRunningAgent, OccurredAt: now},
			previous: SlackProgressPrevious{Kind: SlackProgressRunningAgent, UpdatedAt: now.Add(-45 * time.Second)},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			actual := ShouldSendProgressUpdate(tt.update, tt.previous, policy)
			require.Equal(t, tt.expected, actual, "ShouldSendProgressUpdate should enforce the progress policy")
		})
	}
}
