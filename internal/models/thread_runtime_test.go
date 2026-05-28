package models

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestThreadInboxEntryTypeValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		value   ThreadInboxEntryType
		wantErr bool
	}{
		{name: "user message", value: ThreadInboxEntryTypeUserMessage},
		{name: "human input answer", value: ThreadInboxEntryTypeHumanInputAnswer},
		{name: "control", value: ThreadInboxEntryTypeControl},
		{name: "invalid", value: ThreadInboxEntryType("bogus"), wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.value.Validate()
			if tt.wantErr {
				require.Error(t, err, "Validate should reject invalid inbox entry types")
				return
			}
			require.NoError(t, err, "Validate should accept valid inbox entry types")
		})
	}
}

func TestThreadInboxDeliveryStateValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		value   ThreadInboxDeliveryState
		wantErr bool
	}{
		{name: "pending", value: ThreadInboxDeliveryStatePending},
		{name: "delivering", value: ThreadInboxDeliveryStateDelivering},
		{name: "delivered", value: ThreadInboxDeliveryStateDelivered},
		{name: "unknown delivery", value: ThreadInboxDeliveryStateUnknownDelivery},
		{name: "acked", value: ThreadInboxDeliveryStateAcked},
		{name: "dead letter", value: ThreadInboxDeliveryStateDeadLetter},
		{name: "invalid", value: ThreadInboxDeliveryState("bogus"), wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.value.Validate()
			if tt.wantErr {
				require.Error(t, err, "Validate should reject invalid inbox delivery states")
				return
			}
			require.NoError(t, err, "Validate should accept valid inbox delivery states")
		})
	}
}

func TestThreadInboxDeliverySummaryNormalize(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		summary  ThreadInboxDeliverySummary
		expected ThreadInboxSummaryState
	}{
		{
			name:     "idle when no entries exist",
			summary:  ThreadInboxDeliverySummary{},
			expected: ThreadInboxSummaryStateIdle,
		},
		{
			name:     "pending wins over later acked entries",
			summary:  ThreadInboxDeliverySummary{PendingCount: 1, AckedCount: 4},
			expected: ThreadInboxSummaryStatePending,
		},
		{
			name:     "delivering wins over delivered entries",
			summary:  ThreadInboxDeliverySummary{DeliveringCount: 1, DeliveredCount: 2},
			expected: ThreadInboxSummaryStateDelivering,
		},
		{
			name:     "delivered indicates written but not acked",
			summary:  ThreadInboxDeliverySummary{DeliveredCount: 2, AckedCount: 5},
			expected: ThreadInboxSummaryStateDelivered,
		},
		{
			name:     "unknown delivery wins over pending",
			summary:  ThreadInboxDeliverySummary{PendingCount: 3, UnknownDeliveryCount: 1},
			expected: ThreadInboxSummaryStateUnknownDelivery,
		},
		{
			name:     "dead letter takes precedence",
			summary:  ThreadInboxDeliverySummary{PendingCount: 3, UnknownDeliveryCount: 2, DeadLetterCount: 1},
			expected: ThreadInboxSummaryStateDeadLetter,
		},
		{
			name:     "acked when all known entries are acked",
			summary:  ThreadInboxDeliverySummary{AckedCount: 3},
			expected: ThreadInboxSummaryStateAcked,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			tt.summary.Normalize()

			require.Equal(t, tt.expected, tt.summary.State, "Normalize should compute the expected summary state")
		})
	}
}

func TestThreadInboxSummaryStateValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		value   ThreadInboxSummaryState
		wantErr bool
	}{
		{name: "idle", value: ThreadInboxSummaryStateIdle},
		{name: "pending", value: ThreadInboxSummaryStatePending},
		{name: "delivering", value: ThreadInboxSummaryStateDelivering},
		{name: "delivered", value: ThreadInboxSummaryStateDelivered},
		{name: "unknown delivery", value: ThreadInboxSummaryStateUnknownDelivery},
		{name: "acked", value: ThreadInboxSummaryStateAcked},
		{name: "dead letter", value: ThreadInboxSummaryStateDeadLetter},
		{name: "invalid", value: ThreadInboxSummaryState("bogus"), wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.value.Validate()
			if tt.wantErr {
				require.Error(t, err, "Validate should reject invalid summary states")
				return
			}
			require.NoError(t, err, "Validate should accept valid summary states")
		})
	}
}

func TestThreadRuntimeStatusValidate(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		value   ThreadRuntimeStatus
		wantErr bool
	}{
		{name: "starting", value: ThreadRuntimeStatusStarting},
		{name: "live", value: ThreadRuntimeStatusLive},
		{name: "paused", value: ThreadRuntimeStatusPaused},
		{name: "draining", value: ThreadRuntimeStatusDraining},
		{name: "lost", value: ThreadRuntimeStatusLost},
		{name: "closed", value: ThreadRuntimeStatusClosed},
		{name: "failed", value: ThreadRuntimeStatusFailed},
		{name: "invalid", value: ThreadRuntimeStatus("bogus"), wantErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := tt.value.Validate()
			if tt.wantErr {
				require.Error(t, err, "Validate should reject invalid runtime statuses")
				return
			}
			require.NoError(t, err, "Validate should accept valid runtime statuses")
		})
	}
}

func TestSessionSandboxHolderEnumsValidate(t *testing.T) {
	t.Parallel()

	t.Run("holder kind", func(t *testing.T) {
		t.Parallel()

		tests := []struct {
			name    string
			value   SessionSandboxHolderKind
			wantErr bool
		}{
			{name: "thread runtime", value: SessionSandboxHolderKindThreadRuntime},
			{name: "preview", value: SessionSandboxHolderKindPreview},
			{name: "snapshot", value: SessionSandboxHolderKindSnapshot},
			{name: "operator", value: SessionSandboxHolderKindOperator},
			{name: "invalid", value: SessionSandboxHolderKind("bogus"), wantErr: true},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				t.Parallel()

				err := tt.value.Validate()
				if tt.wantErr {
					require.Error(t, err, "Validate should reject invalid holder kinds")
					return
				}
				require.NoError(t, err, "Validate should accept valid holder kinds")
			})
		}
	})

	t.Run("holder status", func(t *testing.T) {
		t.Parallel()

		tests := []struct {
			name    string
			value   SessionSandboxHolderStatus
			wantErr bool
		}{
			{name: "active", value: SessionSandboxHolderStatusActive},
			{name: "draining", value: SessionSandboxHolderStatusDraining},
			{name: "released", value: SessionSandboxHolderStatusReleased},
			{name: "expired", value: SessionSandboxHolderStatusExpired},
			{name: "invalid", value: SessionSandboxHolderStatus("bogus"), wantErr: true},
		}

		for _, tt := range tests {
			t.Run(tt.name, func(t *testing.T) {
				t.Parallel()

				err := tt.value.Validate()
				if tt.wantErr {
					require.Error(t, err, "Validate should reject invalid holder statuses")
					return
				}
				require.NoError(t, err, "Validate should accept valid holder statuses")
			})
		}
	})
}
