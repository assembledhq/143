package agent

import (
	"context"
	"errors"
	"testing"

	"github.com/assembledhq/143/internal/models"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

type appliedLookupInbox struct {
	ThreadInboxStore
	ids []int64
	err error
}

func (s appliedLookupInbox) ListAppliedMessageIDs(context.Context, uuid.UUID, uuid.UUID, uuid.UUID, []int64) ([]int64, error) {
	return s.ids, s.err
}

func TestResolveContinuationInputRequiresPositiveAdmissionEvidence(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name                          string
		queued, legacy, lookupFailure bool
		expectedLatest                int64
		alreadyApplied                bool
		wantError                     string
	}{
		{name: "remaining pending input survives newer applied input", expectedLatest: 2},
		{name: "exact applied queue target remains a no-op", queued: true, expectedLatest: 3, alreadyApplied: true},
		{name: "ambiguous legacy ordering refuses without panic", legacy: true, wantError: "no unadmitted user input"},
		{name: "lookup failure cannot fall back to replay", lookupFailure: true, wantError: "lookup unavailable"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			thread := uuid.New()
			session := &models.Session{ID: uuid.New(), OrgID: uuid.New(), PrimaryThreadID: &thread}
			messages := []models.SessionMessage{{ID: 2, ThreadID: &thread, Role: models.MessageRoleUser, Content: "pending"}, {ID: 3, ThreadID: &thread, Role: models.MessageRoleUser, Content: "applied"}}
			if tt.legacy {
				messages = []models.SessionMessage{{ThreadID: &thread, Role: models.MessageRoleUser, Content: "legacy context"}, {ThreadID: &thread, Role: models.MessageRoleAssistant, Content: "later legacy assistant"}}
			}
			inbox := appliedLookupInbox{ids: []int64{3}}
			if tt.lookupFailure {
				inbox.err = errors.New("lookup unavailable")
			}
			orch := &Orchestrator{sessionMessages: &drainStubMessages{messages: messages}, threadInbox: inbox}
			opts := &ContinueSessionOptions{ThreadID: &thread}
			if tt.queued {
				id := int64(3)
				opts.QueuedMessageID = &id
			}
			input, err := orch.resolveContinuationInput(context.Background(), session, opts)
			if tt.wantError != "" {
				require.ErrorContains(t, err, tt.wantError, "unproved admission must fail closed before provider execution")
				return
			}
			require.NoError(t, err, "resolve remaining actionable input from exact admission evidence")
			require.Equal(t, tt.expectedLatest, input.latest.ID, "ordinary continuation and exact queued retries have distinct boundaries")
			require.Equal(t, tt.alreadyApplied, input.alreadyApplied, "only the applied target may make a queued job a no-op")
			require.Equal(t, []int64{2}, threadRuntimeSeedMessageIDs(input.pending), "applied input can never return as a new seed")
		})
	}
}
