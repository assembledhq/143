package worker

import (
	"context"
	"testing"

	"github.com/assembledhq/143/internal/models"
	threadsvc "github.com/assembledhq/143/internal/services/thread"
	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
)

func TestCodeReviewReviewerThreadLabel(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		index    int
		agent    models.AgentType
		fallback bool
		expected string
	}{
		{name: "primary rank label includes index", index: 0, agent: models.AgentTypeCodex, expected: "Code review: codex (1)"},
		{name: "fallback rank label includes fallback index", index: 2, agent: models.AgentTypeCodex, fallback: true, expected: "Code review: codex (fallback 3)"},
		{name: "blank provider still gets reviewer label", index: 1, agent: "", expected: "Code review: reviewer (2)"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.expected, codeReviewReviewerThreadLabel(tt.index, tt.agent, tt.fallback), "reviewer thread label should encode immutable rank identity")
		})
	}
}

func TestEnsureCodeReviewFallbackThreadPreservesReviewerRankIdentity(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	sessionID := uuid.New()
	primaryThreadID := uuid.New()
	userThreadID := uuid.New()
	firstReviewerThreadID := uuid.New()
	secondReviewerThreadID := uuid.New()

	stub := &codeReviewFallbackThreadStub{
		existing: []models.SessionThread{
			{ID: primaryThreadID, OrgID: orgID, SessionID: sessionID, AgentType: models.AgentTypeCodex, Label: "Main", CreatedBySource: models.ThreadCreatedBySourceUser, Status: models.ThreadStatusRunning},
			{ID: userThreadID, OrgID: orgID, SessionID: sessionID, AgentType: models.AgentTypeCodex, Label: "User tab", CreatedBySource: models.ThreadCreatedBySourceUser, Status: models.ThreadStatusIdle},
			{ID: firstReviewerThreadID, OrgID: orgID, SessionID: sessionID, AgentType: models.AgentTypeCodex, ModelOverride: stringPtr(models.DefaultCodexModel), Label: codeReviewReviewerThreadLabel(0, models.AgentTypeCodex, false), CreatedBySource: models.ThreadCreatedBySourceSystem, Status: models.ThreadStatusCompleted},
			{ID: secondReviewerThreadID, OrgID: orgID, SessionID: sessionID, AgentType: models.AgentTypeCodex, ModelOverride: stringPtr(models.DefaultCodexModel), Label: codeReviewReviewerThreadLabel(1, models.AgentTypeCodex, false), CreatedBySource: models.ThreadCreatedBySourceSystem, Status: models.ThreadStatusCompleted},
		},
	}

	results := []models.CodeReviewAgentResult{
		codeReviewTerminalReviewerResultForThread(firstReviewerThreadID, models.AgentTypeCodex, models.DefaultCodexModel),
		codeReviewTerminalReviewerResultForThread(secondReviewerThreadID, models.AgentTypeCodex, models.DefaultCodexModel),
	}

	tests := []struct {
		name                   string
		existing               []models.SessionThread
		results                []models.CodeReviewAgentResult
		input                  threadsvc.CreateThreadInput
		expectThreadID         uuid.UUID
		expectErrIs            error
		expectCreate           bool
		expectArchiveID        uuid.UUID
		expectDifferentFromIDs []uuid.UUID
	}{
		{
			name:     "same-rank recovery reuses the existing reviewer thread",
			existing: append([]models.SessionThread(nil), stub.existing...),
			results:  append([]models.CodeReviewAgentResult(nil), results...),
			input: threadsvc.CreateThreadInput{
				SessionID:       sessionID,
				OrgID:           orgID,
				AgentType:       string(models.AgentTypeCodex),
				Model:           models.DefaultCodexModel,
				Label:           codeReviewReviewerThreadLabel(0, models.AgentTypeCodex, false),
				FileScope:       []string{"internal/worker/code_review_handler.go"},
				ExecutionMode:   models.ThreadExecutionModeReview,
				FilesystemMode:  models.ThreadFilesystemModeReadOnly,
				CreatedBySource: models.ThreadCreatedBySourceSystem,
			},
			expectThreadID: firstReviewerThreadID,
		},
		{
			name:     "later duplicate reviewer rank never reuses an earlier rank when reclaiming a slot",
			existing: append([]models.SessionThread(nil), stub.existing...),
			results:  append([]models.CodeReviewAgentResult(nil), results...),
			input: threadsvc.CreateThreadInput{
				SessionID:       sessionID,
				OrgID:           orgID,
				AgentType:       string(models.AgentTypeCodex),
				Model:           models.DefaultCodexModel,
				Label:           codeReviewReviewerThreadLabel(2, models.AgentTypeCodex, false),
				FileScope:       []string{"internal/worker/code_review_handler.go"},
				ExecutionMode:   models.ThreadExecutionModeReview,
				FilesystemMode:  models.ThreadFilesystemModeReadOnly,
				CreatedBySource: models.ThreadCreatedBySourceSystem,
			},
			expectCreate:           true,
			expectArchiveID:        firstReviewerThreadID,
			expectDifferentFromIDs: []uuid.UUID{firstReviewerThreadID, secondReviewerThreadID},
		},
		{
			name: "later duplicate reviewer rank exhausts capacity when only active identical reviewers remain",
			existing: []models.SessionThread{
				{ID: primaryThreadID, OrgID: orgID, SessionID: sessionID, AgentType: models.AgentTypeCodex, Label: "Main", CreatedBySource: models.ThreadCreatedBySourceUser, Status: models.ThreadStatusRunning},
				{ID: userThreadID, OrgID: orgID, SessionID: sessionID, AgentType: models.AgentTypeCodex, Label: "User tab", CreatedBySource: models.ThreadCreatedBySourceUser, Status: models.ThreadStatusIdle},
				{ID: firstReviewerThreadID, OrgID: orgID, SessionID: sessionID, AgentType: models.AgentTypeCodex, ModelOverride: stringPtr(models.DefaultCodexModel), Label: codeReviewReviewerThreadLabel(0, models.AgentTypeCodex, false), CreatedBySource: models.ThreadCreatedBySourceSystem, Status: models.ThreadStatusRunning},
				{ID: secondReviewerThreadID, OrgID: orgID, SessionID: sessionID, AgentType: models.AgentTypeCodex, ModelOverride: stringPtr(models.DefaultCodexModel), Label: codeReviewReviewerThreadLabel(1, models.AgentTypeCodex, false), CreatedBySource: models.ThreadCreatedBySourceSystem, Status: models.ThreadStatusRunning},
			},
			results: []models.CodeReviewAgentResult{
				{
					Role:          models.CodeReviewAgentRoleReviewer,
					AgentProvider: string(models.AgentTypeCodex),
					AgentModel:    stringPtr(models.DefaultCodexModel),
					Status:        models.CodeReviewAgentResultStatusRunning,
					StructuredResult: marshalCodeReviewReviewerStructuredResult(codeReviewReviewerStructuredResult{
						ReviewerKey:   codeReviewReviewerKey(0, models.AgentTypeCodex),
						ReviewerIndex: 0,
						ThreadID:      firstReviewerThreadID.String(),
						ReadOnly:      true,
					}),
				},
				{
					Role:          models.CodeReviewAgentRoleReviewer,
					AgentProvider: string(models.AgentTypeCodex),
					AgentModel:    stringPtr(models.DefaultCodexModel),
					Status:        models.CodeReviewAgentResultStatusRunning,
					StructuredResult: marshalCodeReviewReviewerStructuredResult(codeReviewReviewerStructuredResult{
						ReviewerKey:   codeReviewReviewerKey(1, models.AgentTypeCodex),
						ReviewerIndex: 1,
						ThreadID:      secondReviewerThreadID.String(),
						ReadOnly:      true,
					}),
				},
			},
			input: threadsvc.CreateThreadInput{
				SessionID:       sessionID,
				OrgID:           orgID,
				AgentType:       string(models.AgentTypeCodex),
				Model:           models.DefaultCodexModel,
				Label:           codeReviewReviewerThreadLabel(2, models.AgentTypeCodex, false),
				FileScope:       []string{"internal/worker/code_review_handler.go"},
				ExecutionMode:   models.ThreadExecutionModeReview,
				FilesystemMode:  models.ThreadFilesystemModeReadOnly,
				CreatedBySource: models.ThreadCreatedBySourceSystem,
			},
			expectErrIs:            errCodeReviewFallbackCapacityExhausted,
			expectCreate:           false,
			expectArchiveID:        uuid.Nil,
			expectDifferentFromIDs: []uuid.UUID{firstReviewerThreadID, secondReviewerThreadID},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			localStub := &codeReviewFallbackThreadStub{
				existing: append([]models.SessionThread(nil), tt.existing...),
			}

			actual, err := ensureCodeReviewFallbackThread(context.Background(), localStub, tt.input, primaryThreadID, tt.results)
			if tt.expectErrIs != nil {
				require.ErrorIs(t, err, tt.expectErrIs, "later duplicate reviewer ranks should stop at capacity instead of reusing another rank's thread")
				require.Nil(t, actual, "capacity exhaustion should not return another reviewer's thread")
				for _, forbiddenID := range tt.expectDifferentFromIDs {
					if actual != nil {
						require.NotEqual(t, forbiddenID, actual.ID, "capacity exhaustion should never surface another rank's thread")
					}
				}
				if tt.expectCreate {
					require.NotNil(t, localStub.input, "capacity exhaustion after create attempt should still record the attempted thread input")
				} else {
					require.Nil(t, localStub.input, "same-rank mismatch should fail before creating a new thread when the cap is already full")
				}
				require.Equal(t, tt.expectArchiveID, localStub.archiveID, "reviewer identity protection should not archive unrelated threads in this capped duplicate scenario")
				return
			}

			require.NoError(t, err, "same-rank recovery should succeed")
			require.NotNil(t, actual, "same-rank recovery should return the matching reviewer thread")
			if tt.expectThreadID != uuid.Nil {
				require.Equal(t, tt.expectThreadID, actual.ID, "reviewer recovery should reuse only the thread with the same immutable rank label")
				require.Nil(t, localStub.input, "same-rank recovery should not create a new reviewer thread")
				require.Equal(t, uuid.Nil, localStub.archiveID, "same-rank recovery should not archive any thread")
				return
			}
			for _, forbiddenID := range tt.expectDifferentFromIDs {
				require.NotEqual(t, forbiddenID, actual.ID, "later duplicate reviewer rank should never reuse another rank's thread")
			}
			require.NotNil(t, localStub.input, "a later duplicate reviewer rank should create a distinct thread when a safe slot is reclaimed")
			require.Equal(t, tt.input.Label, actual.Label, "a later duplicate reviewer rank should preserve its own immutable rank label")
			require.Equal(t, tt.expectArchiveID, localStub.archiveID, "a later duplicate reviewer rank should reclaim at most one eligible persisted reviewer slot")
		})
	}
}
