package models

import (
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestSessionStreamEventType_Validate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		value     SessionStreamEventType
		expectErr bool
	}{
		{name: "thread inbox queued", value: SessionStreamEventThreadInboxQueued},
		{name: "thread inbox cleared", value: SessionStreamEventThreadInboxCleared},
		{name: "thread runtime updated", value: SessionStreamEventThreadRuntimeUpdated},
		{name: "workspace generation changed", value: SessionStreamEventWorkspaceGenerationChanged},
		{name: "invalid", value: SessionStreamEventType("bad"), expectErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.value.Validate()
			if tt.expectErr {
				require.Error(t, err, "Validate should reject unknown session stream event types")
				return
			}
			require.NoError(t, err, "Validate should accept known session stream event types")
		})
	}
}

func TestNewThreadRuntimeEventIncludesFailureMetadata(t *testing.T) {
	t.Parallel()

	now := time.Now().UTC()
	explanation := "claude subscription is marked invalid; reconnect required"
	category := "claude_code_auth_expired"
	thread := SessionThread{
		ID:                  uuid.New(),
		SessionID:           uuid.New(),
		OrgID:               uuid.New(),
		Status:              ThreadStatusFailed,
		CurrentTurn:         1,
		PendingMessageCount: 2,
		LastActivityAt:      &now,
		FailureExplanation:  &explanation,
		FailureCategory:     &category,
	}

	actual := NewThreadRuntimeEvent(thread)
	expected := ThreadRuntimeEvent{
		SessionID:           thread.SessionID,
		ThreadID:            thread.ID,
		OrgID:               thread.OrgID,
		Status:              ThreadStatusFailed,
		CurrentTurn:         1,
		PendingMessageCount: 2,
		LastActivityAt:      &now,
		FailureExplanation:  &explanation,
		FailureCategory:     &category,
	}

	require.Equal(t, expected, actual, "thread runtime events should carry failure metadata to the live session UI")
}
