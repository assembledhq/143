package models

import (
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestNewSessionLogResponse(t *testing.T) {
	t.Parallel()

	sessionID := uuid.New()
	now := time.Now()

	tests := []struct {
		name              string
		message           string
		expectedMessage   string
		expectedTruncated bool
	}{
		{
			name:              "short message is returned intact",
			message:           "short output",
			expectedMessage:   "short output",
			expectedTruncated: false,
		},
		{
			name:              "long ascii message is previewed",
			message:           strings.Repeat("a", SessionLogPreviewBytes+12),
			expectedMessage:   strings.Repeat("a", SessionLogPreviewBytes),
			expectedTruncated: true,
		},
		{
			name:              "preview preserves valid utf8",
			message:           strings.Repeat("a", SessionLogPreviewBytes-1) + "界tail",
			expectedMessage:   strings.Repeat("a", SessionLogPreviewBytes-1),
			expectedTruncated: true,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			log := SessionLog{
				ID:        42,
				SessionID: sessionID,
				OrgID:     uuid.New(),
				Timestamp: now,
				Level:     SessionLogLevelOutput,
				Message:   tt.message,
			}

			resp := NewSessionLogResponse(log)

			require.Equal(t, tt.expectedMessage, resp.Message, "response should expose the expected preview message")
			require.Equal(t, tt.expectedTruncated, resp.MessageTruncated, "response should report whether message was truncated")
			require.Equal(t, len([]byte(tt.message)), resp.MessageBytes, "response should report original byte length")
			require.Equal(t, utf8.RuneCountInString(tt.message), resp.MessageChars, "response should report original character length")
			require.True(t, utf8.ValidString(resp.Message), "preview should remain valid UTF-8")
			require.Equal(t, sessionID, resp.SessionID, "response should preserve session identity")
			require.Equal(t, now, resp.CreatedAt, "response should map timestamp to created_at")
		})
	}
}

func TestNewSessionLogDetailResponse(t *testing.T) {
	t.Parallel()

	message := strings.Repeat("detail-", 2_000)
	log := SessionLog{
		ID:        7,
		SessionID: uuid.New(),
		OrgID:     uuid.New(),
		Timestamp: time.Now(),
		Level:     SessionLogLevelOutput,
		Message:   message,
	}

	resp := NewSessionLogDetailResponse(log)

	require.Equal(t, message, resp.Message, "detail response should include the full message")
	require.Equal(t, len([]byte(message)), resp.MessageBytes, "detail response should report original byte length")
	require.Equal(t, utf8.RuneCountInString(message), resp.MessageChars, "detail response should report original character length")
}
