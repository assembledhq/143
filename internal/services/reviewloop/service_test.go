package reviewloop

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/models"
	threadsvc "github.com/assembledhq/143/internal/services/thread"
)

func TestService_StartCreatesReviewThreadLoopPassAndMessage(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	sessionID := uuid.New()
	userID := uuid.New()
	threadID := uuid.New()
	messageID := int64(77)
	snapshotKey := "snapshots/review-loop-start.tar.zst"
	store := &fakeReviewLoopStore{}
	threads := &fakeThreadService{
		session: models.Session{ID: sessionID, OrgID: orgID, AgentType: models.AgentTypeClaudeCode, Status: models.SessionStatusIdle, SandboxState: models.SandboxStateSnapshotted, SnapshotKey: &snapshotKey},
		thread:  models.SessionThread{ID: threadID, SessionID: sessionID, OrgID: orgID, AgentType: models.AgentTypeClaudeCode, Label: "Claude Review"},
		message: models.SessionMessage{ID: messageID, SessionID: sessionID, OrgID: orgID, ThreadID: &threadID},
	}
	svc := NewService(store, threads)

	loop, err := svc.Start(context.Background(), orgID, sessionID, StartReviewLoopRequest{
		MaxPasses:       2,
		StartedByUserID: &userID,
		Source:          models.ReviewLoopSourceManual,
	})
	require.NoError(t, err, "Start should create the review loop")
	require.Equal(t, threadID, *loop.ThreadID, "Start should bind the loop to the review thread")
	require.Equal(t, models.AgentTypeClaudeCode, loop.AgentType, "Start should default to the session agent")
	require.Equal(t, models.ReviewLoopFixModeMinimal, loop.FixMode, "Start should default to minimal fix mode")
	require.Equal(t, &snapshotKey, loop.LoopStartCheckpointKey, "Start should record the snapshot checkpoint for the review loop")
	require.Len(t, store.createdPasses, 1, "Start should create the first pass")
	require.Equal(t, models.ReviewLoopPassStatusReviewing, store.createdPasses[0].Status, "first pass should start in reviewing state")
	require.Equal(t, messageID, store.reviewMessageIDs[0], "Start should persist the review message id on the pass")
	require.Contains(t, threads.sent[0].Message, "/review", "Start should send the native review command")
	require.Len(t, threads.sent[0].Commands, 1, "Start should persist a structured slash command")
	require.Equal(t, "review", threads.sent[0].Commands[0].Name, "structured command should be /review")
}

func TestService_StartUsesGenericReviewPromptForAgentsWithoutReviewCommand(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	sessionID := uuid.New()
	threadID := uuid.New()
	messageID := int64(77)
	snapshotKey := "snapshots/review-loop-amp-start.tar.zst"
	store := &fakeReviewLoopStore{}
	threads := &fakeThreadService{
		session: models.Session{ID: sessionID, OrgID: orgID, AgentType: models.AgentTypeAmp, Status: models.SessionStatusIdle, SandboxState: models.SandboxStateSnapshotted, SnapshotKey: &snapshotKey},
		thread:  models.SessionThread{ID: threadID, SessionID: sessionID, OrgID: orgID, AgentType: models.AgentTypeAmp, Label: "Review"},
		message: models.SessionMessage{ID: messageID, SessionID: sessionID, OrgID: orgID, ThreadID: &threadID},
	}
	svc := NewService(store, threads)

	loop, err := svc.Start(context.Background(), orgID, sessionID, StartReviewLoopRequest{
		MaxPasses: 2,
		Source:    models.ReviewLoopSourceManual,
	})
	require.NoError(t, err, "Start should create an Amp review loop")
	require.Equal(t, models.AgentTypeAmp, loop.AgentType, "Start should run the review loop with Amp")
	require.NotContains(t, threads.sent[0].Message, "/review", "agents without a native review command should receive a natural-language prompt")
	require.Contains(t, threads.sent[0].Message, "Review the current workspace diff", "generic review prompt should preserve the review instruction")
	require.Empty(t, threads.sent[0].Commands, "agents without a native review command should not persist a structured /review command")
}

func TestService_StartStoresRequestedFixMode(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	sessionID := uuid.New()
	threadID := uuid.New()
	snapshotKey := "snapshots/review-loop-fix-mode.tar.zst"
	store := &fakeReviewLoopStore{}
	threads := &fakeThreadService{
		session: models.Session{ID: sessionID, OrgID: orgID, AgentType: models.AgentTypeCodex, Status: models.SessionStatusIdle, SandboxState: models.SandboxStateSnapshotted, SnapshotKey: &snapshotKey},
		thread:  models.SessionThread{ID: threadID, SessionID: sessionID, OrgID: orgID, AgentType: models.AgentTypeCodex, Label: "Codex Review"},
		message: models.SessionMessage{ID: 77, SessionID: sessionID, OrgID: orgID, ThreadID: &threadID},
	}
	svc := NewService(store, threads)

	loop, err := svc.Start(context.Background(), orgID, sessionID, StartReviewLoopRequest{
		MaxPasses: 2,
		Source:    models.ReviewLoopSourceManual,
		FixMode:   models.ReviewLoopFixModeExhaustive,
	})

	require.NoError(t, err, "Start should create the review loop with a requested fix mode")
	require.Equal(t, models.ReviewLoopFixModeExhaustive, loop.FixMode, "Start should return the requested fix mode")
	require.Equal(t, models.ReviewLoopFixModeExhaustive, store.createdLoops[0].FixMode, "Start should persist the requested fix mode")
}

func TestService_StartRejectsExistingRunningLoop(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	sessionID := uuid.New()
	threadID := uuid.New()
	snapshotKey := "snapshots/review-loop-existing.tar.zst"
	store := &fakeReviewLoopStore{
		runningLoopBySession: models.SessionReviewLoop{
			ID:        uuid.New(),
			OrgID:     orgID,
			SessionID: sessionID,
			ThreadID:  &threadID,
			Status:    models.ReviewLoopStatusRunning,
			AgentType: models.AgentTypeCodex,
			MaxPasses: 2,
		},
	}
	threads := &fakeThreadService{
		session: models.Session{ID: sessionID, OrgID: orgID, AgentType: models.AgentTypeCodex, Status: models.SessionStatusIdle, SandboxState: models.SandboxStateSnapshotted, SnapshotKey: &snapshotKey},
	}
	svc := NewService(store, threads)

	loop, err := svc.Start(context.Background(), orgID, sessionID, StartReviewLoopRequest{
		MaxPasses: 2,
		Source:    models.ReviewLoopSourceManual,
	})

	require.ErrorIs(t, err, ErrReviewLoopAlreadyRunning, "Start should reject a second running loop for the same session")
	require.Nil(t, loop, "Start should not return a loop when another loop is running")
	require.Empty(t, store.createdLoops, "Start should not create another loop row")
	require.Empty(t, threads.created, "Start should not create an orphan review thread")
}

func TestService_AutoReadinessEnabledUsesOrgPolicy(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	userID := uuid.New()
	tests := []struct {
		name              string
		raw               json.RawMessage
		status            models.ReviewLoopStatus
		triggeredByUserID *uuid.UUID
		userSettings      models.UserSettings
		want              bool
	}{
		{
			name:   "disabled by default",
			raw:    json.RawMessage(`{}`),
			status: models.ReviewLoopStatusClean,
			want:   false,
		},
		{
			name:   "enabled for clean by default states",
			raw:    json.RawMessage(`{"session_automation":{"automatic_follow_through":{"readiness_after_review_loop":true}}}`),
			status: models.ReviewLoopStatusClean,
			want:   true,
		},
		{
			name:   "does not run for terminal state outside policy",
			raw:    json.RawMessage(`{"session_automation":{"automatic_follow_through":{"readiness_after_review_loop":true,"readiness_after_review_loop_states":["failed"]}}}`),
			status: models.ReviewLoopStatusClean,
			want:   false,
		},
		{
			name:              "user on overrides org off",
			raw:               json.RawMessage(`{}`),
			status:            models.ReviewLoopStatusClean,
			triggeredByUserID: &userID,
			userSettings: models.UserSettings{AutomaticPRFollowThrough: &models.AutomaticPRFollowThroughSettings{
				ReadinessAfterReviewLoop: models.AutomaticFollowThroughPreferenceOn,
			}},
			want: true,
		},
		{
			name:              "user off overrides org on",
			raw:               json.RawMessage(`{"session_automation":{"automatic_follow_through":{"readiness_after_review_loop":true}}}`),
			status:            models.ReviewLoopStatusClean,
			triggeredByUserID: &userID,
			userSettings: models.UserSettings{AutomaticPRFollowThrough: &models.AutomaticPRFollowThroughSettings{
				ReadinessAfterReviewLoop: models.AutomaticFollowThroughPreferenceOff,
			}},
			want: false,
		},
	}

	for _, tt := range tests {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			svc := NewService(&fakeReviewLoopStore{}, &fakeThreadService{}, func(s *Service) {
				s.orgs = fakeOrgStore{org: models.Organization{ID: orgID, Settings: tt.raw}}
				s.users = fakeUserSettingsStore{user: models.UserWithSettings{ID: userID, Settings: tt.userSettings}}
			})
			got, err := svc.autoReadinessEnabled(context.Background(), orgID, tt.status, tt.triggeredByUserID)
			require.NoError(t, err, "autoReadinessEnabled should parse organization policy")
			require.Equal(t, tt.want, got, "autoReadinessEnabled should resolve the configured review-loop terminal state")
		})
	}
}

func TestService_StartRejectsMissingSnapshot(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	sessionID := uuid.New()
	store := &fakeReviewLoopStore{}
	threads := &fakeThreadService{
		session: models.Session{ID: sessionID, OrgID: orgID, AgentType: models.AgentTypeCodex, Status: models.SessionStatusCompleted, SandboxState: models.SandboxStateSnapshotted},
	}
	svc := NewService(store, threads)

	loop, err := svc.Start(context.Background(), orgID, sessionID, StartReviewLoopRequest{
		MaxPasses: 2,
		Source:    models.ReviewLoopSourceManual,
	})

	require.ErrorIs(t, err, ErrSessionSnapshotExpired, "Start should reject sessions without a restorable snapshot")
	require.Nil(t, loop, "Start should not return a loop without a snapshot")
	require.Empty(t, store.createdLoops, "Start should not create a loop row without a snapshot")
	require.Empty(t, threads.created, "Start should not create a review thread without a snapshot")
}

func TestService_StartCreatesLoopAndFirstPassAtomically(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	sessionID := uuid.New()
	threadID := uuid.New()
	snapshotKey := "snapshots/review-loop-atomic-start.tar.zst"
	store := &fakeReviewLoopStore{
		createLoopWithInitialPassErr: errors.New("insert pass failed"),
	}
	threads := &fakeThreadService{
		session: models.Session{ID: sessionID, OrgID: orgID, AgentType: models.AgentTypeCodex, Status: models.SessionStatusIdle, SandboxState: models.SandboxStateSnapshotted, SnapshotKey: &snapshotKey},
		thread:  models.SessionThread{ID: threadID, SessionID: sessionID, OrgID: orgID, AgentType: models.AgentTypeCodex, Label: "Codex Review"},
		message: models.SessionMessage{ID: 77, SessionID: sessionID, OrgID: orgID, ThreadID: &threadID},
	}
	svc := NewService(store, threads)

	loop, err := svc.Start(context.Background(), orgID, sessionID, StartReviewLoopRequest{
		MaxPasses: 2,
		Source:    models.ReviewLoopSourceManual,
	})

	require.ErrorContains(t, err, "insert pass failed", "Start should surface atomic loop/pass creation failures")
	require.Nil(t, loop, "Start should not return a loop when atomic creation fails")
	require.Empty(t, store.createdLoops, "Start should not leave a standalone running loop when first pass creation fails")
	require.Empty(t, store.createdPasses, "Start should not record a pass when atomic creation fails")
	require.Empty(t, threads.sent, "Start should not send the review command after atomic creation fails")
}

func TestService_OnThreadTurnCompleteDirtyThenClean(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	sessionID := uuid.New()
	threadID := uuid.New()
	loopID := uuid.New()
	passID := uuid.New()
	store := &fakeReviewLoopStore{
		runningLoop: models.SessionReviewLoop{
			ID:        loopID,
			OrgID:     orgID,
			SessionID: sessionID,
			ThreadID:  &threadID,
			Status:    models.ReviewLoopStatusRunning,
			AgentType: models.AgentTypeCodex,
			MaxPasses: 2,
			FixMode:   models.ReviewLoopFixModeExhaustive,
		},
		latestPass: models.SessionReviewLoopPass{ID: passID, OrgID: orgID, LoopID: loopID, SessionID: sessionID, PassIndex: 1, Status: models.ReviewLoopPassStatusReviewing},
	}
	threads := &fakeThreadService{message: models.SessionMessage{ID: 10, SessionID: sessionID, OrgID: orgID, ThreadID: &threadID}}
	svc := NewService(store, threads)

	err := svc.OnThreadTurnComplete(context.Background(), orgID, threadID, "Found a missing regression test")
	require.NoError(t, err, "review completion should enqueue decision")
	require.Equal(t, "Found a missing regression test", store.markedReviewOutput, "review output should be stored natively")
	require.Contains(t, threads.sent[0].Message, "REVIEW_CLEAN", "decision prompt should ask for the clean sentinel")
	require.NotNil(t, threads.sent[0].ContinuationDedupeKeyOverride, "review decision prompt should use a dedicated continuation dedupe key")
	require.Equal(t, reviewLoopContinuationDedupeKey(loopID, passID, "decision"), *threads.sent[0].ContinuationDedupeKeyOverride, "review decision prompt should not collide with the currently running review job")

	store.latestPass.Status = models.ReviewLoopPassStatusDeciding
	err = svc.OnThreadTurnComplete(context.Background(), orgID, threadID, "Added the regression test")
	require.NoError(t, err, "dirty decision turn should record fixes and enqueue the next review pass")
	require.Equal(t, "Added the regression test", store.fixSummary, "fix summary should be stored")
	require.Len(t, store.createdPasses, 1, "fix completion should create the confirmation pass")
	require.Equal(t, 2, store.createdPasses[0].PassIndex, "confirmation pass should increment pass_index")
	require.Contains(t, threads.sent[1].Message, "/review", "confirmation pass should run /review again")
	require.NotNil(t, threads.sent[1].ContinuationDedupeKeyOverride, "confirmation review prompt should use a dedicated continuation dedupe key")
	require.Equal(t, reviewLoopContinuationDedupeKey(loopID, store.createdPasses[0].ID, "review"), *threads.sent[1].ContinuationDedupeKeyOverride, "confirmation review prompt should not collide with the decision job")

	store.latestPass = store.createdPasses[0]
	store.latestPass.Status = models.ReviewLoopPassStatusDeciding
	err = svc.OnThreadTurnComplete(context.Background(), orgID, threadID, "REVIEW_CLEAN")
	require.NoError(t, err, "clean decision should complete the loop")
	require.Equal(t, models.ReviewLoopDecisionClean, store.cleanDecision, "clean decision should be persisted")
	require.Equal(t, loopID, store.cleanLoopID, "clean decision should mark the loop clean")
}

func TestService_OnThreadTurnCompleteCleanAutomationLoopEnqueuesOpenPR(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	sessionID := uuid.New()
	threadID := uuid.New()
	loopID := uuid.New()
	passID := uuid.New()
	automationRunID := uuid.New()
	store := &fakeReviewLoopStore{
		runningLoop: models.SessionReviewLoop{
			ID:              loopID,
			OrgID:           orgID,
			SessionID:       sessionID,
			AutomationRunID: &automationRunID,
			ThreadID:        &threadID,
			Status:          models.ReviewLoopStatusRunning,
			AgentType:       models.AgentTypeCodex,
			MaxPasses:       1,
		},
		latestPass: models.SessionReviewLoopPass{
			ID:        passID,
			OrgID:     orgID,
			LoopID:    loopID,
			SessionID: sessionID,
			PassIndex: 1,
			Status:    models.ReviewLoopPassStatusDeciding,
		},
	}
	svc := NewService(store, &fakeThreadService{})

	err := svc.OnThreadTurnComplete(context.Background(), orgID, threadID, "REVIEW_CLEAN")
	require.NoError(t, err, "clean automation review should complete without error")
	require.Equal(t, models.ReviewLoopDecisionClean, store.cleanDecision, "clean decision should be persisted")
	require.Equal(t, loopID, store.cleanLoopID, "clean decision should mark the loop clean")
	require.Equal(t, []string{"clean_open_pr"}, store.events, "clean automation review should durably queue PR creation with the terminal state")
}

func TestService_OnThreadTurnCompleteReviewOutputWithCleanSentinelStopsLoop(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	sessionID := uuid.New()
	threadID := uuid.New()
	loopID := uuid.New()
	passID := uuid.New()
	store := &fakeReviewLoopStore{
		runningLoop: models.SessionReviewLoop{
			ID:        loopID,
			OrgID:     orgID,
			SessionID: sessionID,
			ThreadID:  &threadID,
			Status:    models.ReviewLoopStatusRunning,
			AgentType: models.AgentTypeCodex,
			MaxPasses: 2,
		},
		latestPass: models.SessionReviewLoopPass{
			ID:        passID,
			OrgID:     orgID,
			LoopID:    loopID,
			SessionID: sessionID,
			PassIndex: 1,
			Status:    models.ReviewLoopPassStatusReviewing,
		},
	}
	threads := &fakeThreadService{message: models.SessionMessage{ID: 10, SessionID: sessionID, OrgID: orgID, ThreadID: &threadID}}
	svc := NewService(store, threads)

	err := svc.OnThreadTurnComplete(context.Background(), orgID, threadID, "No remaining correctness bugs.\n\nREVIEW_CLEAN")

	require.NoError(t, err, "review output containing the clean sentinel should complete the loop")
	require.Equal(t, models.ReviewLoopDecisionClean, store.cleanDecision, "review output clean sentinel should be persisted")
	require.Equal(t, loopID, store.cleanLoopID, "review output clean sentinel should mark the loop clean")
	require.Empty(t, threads.sent, "review output clean sentinel should not enqueue a redundant decision prompt")
}

func TestService_OnThreadTurnCompleteCleanAutomationLoopIsAtomicWithOpenPR(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	sessionID := uuid.New()
	threadID := uuid.New()
	loopID := uuid.New()
	passID := uuid.New()
	automationRunID := uuid.New()
	store := &fakeReviewLoopStore{
		runningLoop: models.SessionReviewLoop{
			ID:              loopID,
			OrgID:           orgID,
			SessionID:       sessionID,
			AutomationRunID: &automationRunID,
			ThreadID:        &threadID,
			Status:          models.ReviewLoopStatusRunning,
			AgentType:       models.AgentTypeCodex,
			MaxPasses:       1,
		},
		latestPass: models.SessionReviewLoopPass{
			ID:        passID,
			OrgID:     orgID,
			LoopID:    loopID,
			SessionID: sessionID,
			PassIndex: 1,
			Status:    models.ReviewLoopPassStatusDeciding,
		},
		terminalErr: errors.New("job enqueue failed"),
	}
	svc := NewService(store, &fakeThreadService{})

	err := svc.OnThreadTurnComplete(context.Background(), orgID, threadID, "REVIEW_CLEAN")
	require.Error(t, err, "clean automation review should fail when terminal enqueue fails")
	require.ErrorContains(t, err, "job enqueue failed", "clean automation review should surface the enqueue failure")
	require.Equal(t, uuid.Nil, store.cleanLoopID, "clean automation review should not mark the loop clean without the open_pr job")
	require.Empty(t, store.events, "clean automation review should leave terminal state untouched when the atomic write fails")
}

func TestService_OnThreadTurnCompleteAutomationPassLimitEnqueuesOpenPRGate(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	sessionID := uuid.New()
	threadID := uuid.New()
	loopID := uuid.New()
	passID := uuid.New()
	automationRunID := uuid.New()
	store := &fakeReviewLoopStore{
		runningLoop: models.SessionReviewLoop{
			ID:              loopID,
			OrgID:           orgID,
			SessionID:       sessionID,
			AutomationRunID: &automationRunID,
			ThreadID:        &threadID,
			Status:          models.ReviewLoopStatusRunning,
			AgentType:       models.AgentTypeCodex,
			MaxPasses:       1,
		},
		latestPass: models.SessionReviewLoopPass{
			ID:        passID,
			OrgID:     orgID,
			LoopID:    loopID,
			SessionID: sessionID,
			PassIndex: 1,
			Status:    models.ReviewLoopPassStatusDeciding,
		},
	}
	svc := NewService(store, &fakeThreadService{})

	err := svc.OnThreadTurnComplete(context.Background(), orgID, threadID, "NEEDS_FIX_PASS")
	require.NoError(t, err, "pass-limit automation review should complete the terminal write")
	require.Equal(t, loopID, store.needsHumanLoopID, "pass-limit automation review should mark the loop for human decision")
	require.Equal(t, models.ReviewLoopDecisionNeedsFix, store.needsHumanDecision, "pass-limit automation review should persist the final agent decision")
	require.Equal(t, []string{"needs_human_open_pr"}, store.events, "pass-limit automation review should durably queue the PR gate with the terminal state")
}

func TestService_OnThreadTurnCompleteAutomationDecisionTextWithCleanSentinelEnqueuesOpenPR(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	sessionID := uuid.New()
	threadID := uuid.New()
	loopID := uuid.New()
	passID := uuid.New()
	automationRunID := uuid.New()
	store := &fakeReviewLoopStore{
		runningLoop: models.SessionReviewLoop{
			ID:              loopID,
			OrgID:           orgID,
			SessionID:       sessionID,
			AutomationRunID: &automationRunID,
			ThreadID:        &threadID,
			Status:          models.ReviewLoopStatusRunning,
			AgentType:       models.AgentTypeCodex,
			MaxPasses:       1,
		},
		latestPass: models.SessionReviewLoopPass{
			ID:        passID,
			OrgID:     orgID,
			LoopID:    loopID,
			SessionID: sessionID,
			PassIndex: 1,
			Status:    models.ReviewLoopPassStatusDeciding,
		},
	}
	svc := NewService(store, &fakeThreadService{})

	err := svc.OnThreadTurnComplete(context.Background(), orgID, threadID, "REVIEW_CLEAN with commentary")

	require.NoError(t, err, "automation review decision containing the clean sentinel should complete without error")
	require.Equal(t, models.ReviewLoopDecisionClean, store.cleanDecision, "clean decision should be persisted from explanatory text")
	require.Equal(t, loopID, store.cleanLoopID, "clean decision should mark the loop clean")
	require.Equal(t, []string{"clean_open_pr"}, store.events, "clean automation review should durably queue PR creation with the terminal state")
}

func TestService_OnThreadTurnCompleteEmptyDecisionFailsLoop(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	sessionID := uuid.New()
	threadID := uuid.New()
	loopID := uuid.New()
	passID := uuid.New()
	store := &fakeReviewLoopStore{
		runningLoop: models.SessionReviewLoop{
			ID:        loopID,
			OrgID:     orgID,
			SessionID: sessionID,
			ThreadID:  &threadID,
			Status:    models.ReviewLoopStatusRunning,
			AgentType: models.AgentTypeCodex,
			MaxPasses: 2,
		},
		latestPass: models.SessionReviewLoopPass{
			ID:        passID,
			OrgID:     orgID,
			LoopID:    loopID,
			SessionID: sessionID,
			PassIndex: 1,
			Status:    models.ReviewLoopPassStatusDeciding,
		},
	}
	svc := NewService(store, &fakeThreadService{})

	err := svc.OnThreadTurnComplete(context.Background(), orgID, threadID, " \n\t ")

	require.ErrorIs(t, err, ErrUnrecognizedDecision, "empty review decision should fail instead of consuming another review pass")
	require.Equal(t, loopID, store.failedLoopID, "empty review decision should mark the loop failed")
	require.Empty(t, store.fixSummary, "empty review decision should not be recorded as a fix summary")
	require.Empty(t, store.createdPasses, "empty review decision should not create a confirmation review pass")
}

func TestService_OnThreadTurnFailedMarksRunningLoopFailed(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	sessionID := uuid.New()
	threadID := uuid.New()
	loopID := uuid.New()
	store := &fakeReviewLoopStore{
		runningLoop: models.SessionReviewLoop{
			ID:        loopID,
			OrgID:     orgID,
			SessionID: sessionID,
			ThreadID:  &threadID,
			Status:    models.ReviewLoopStatusRunning,
			AgentType: models.AgentTypeCodex,
			MaxPasses: 2,
		},
	}
	svc := NewService(store, &fakeThreadService{})

	err := svc.OnThreadTurnFailed(context.Background(), orgID, threadID, " sandbox hydrate failed ")

	require.NoError(t, err, "thread failure should terminalize the running review loop")
	require.Equal(t, loopID, store.failedLoopID, "thread failure should mark the active review loop failed")
	require.Equal(t, "sandbox hydrate failed", store.failedSummary, "thread failure should store a trimmed failure summary")
	require.Equal(t, []string{"failed"}, store.events, "manual review-loop failure should not enqueue PR creation")
}

func TestService_OnThreadTurnFailedAutomationEnqueuesOpenPRGate(t *testing.T) {
	t.Parallel()

	orgID := uuid.New()
	sessionID := uuid.New()
	threadID := uuid.New()
	loopID := uuid.New()
	automationRunID := uuid.New()
	store := &fakeReviewLoopStore{
		runningLoop: models.SessionReviewLoop{
			ID:              loopID,
			OrgID:           orgID,
			SessionID:       sessionID,
			AutomationRunID: &automationRunID,
			ThreadID:        &threadID,
			Status:          models.ReviewLoopStatusRunning,
			AgentType:       models.AgentTypeCodex,
			MaxPasses:       1,
		},
	}
	svc := NewService(store, &fakeThreadService{})

	err := svc.OnThreadTurnFailed(context.Background(), orgID, threadID, "agent exited")

	require.NoError(t, err, "automation thread failure should terminalize the running review loop")
	require.Equal(t, loopID, store.failedLoopID, "automation thread failure should mark the review loop failed")
	require.Equal(t, []string{"failed_open_pr"}, store.events, "automation review-loop failure should requeue the PR gate")
}

func TestParseDecisionFindsUnambiguousSentinel(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		summary   string
		expected  models.ReviewLoopDecision
		expectErr bool
	}{
		{name: "clean", summary: " REVIEW_CLEAN\n", expected: models.ReviewLoopDecisionClean},
		{name: "needs fix", summary: "\nNEEDS_FIX_PASS ", expected: models.ReviewLoopDecisionNeedsFix},
		{name: "clean with explanatory text", summary: "No remaining issues.\n\nREVIEW_CLEAN", expected: models.ReviewLoopDecisionClean},
		{name: "needs fix first line directive", summary: "NEEDS_FIX_PASS: fixed the regression.", expected: models.ReviewLoopDecisionNeedsFix},
		{name: "ambiguous", summary: "NEEDS_FIX_PASS, not REVIEW_CLEAN", expectErr: true},
		{name: "negated clean mention", summary: "I cannot mark this REVIEW_CLEAN because issue X remains.", expectErr: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got, err := parseDecision(tt.summary)
			if tt.expectErr {
				require.ErrorIs(t, err, ErrUnrecognizedDecision, "parseDecision should reject ambiguous decision text")
				return
			}
			require.NoError(t, err, "parseDecision should accept unambiguous sentinel text")
			require.Equal(t, tt.expected, got, "parseDecision should return the expected sentinel")
		})
	}
}

type fakeReviewLoopStore struct {
	createdLoops                 []models.SessionReviewLoop
	createdPasses                []models.SessionReviewLoopPass
	createLoopWithInitialPassErr error
	runningLoop                  models.SessionReviewLoop
	runningLoopBySession         models.SessionReviewLoop
	latestPass                   models.SessionReviewLoopPass
	reviewMessageIDs             []int64
	markedReviewOutput           string
	fixDecision                  models.ReviewLoopDecision
	cleanDecision                models.ReviewLoopDecision
	cleanLoopID                  uuid.UUID
	needsHumanDecision           models.ReviewLoopDecision
	needsHumanLoopID             uuid.UUID
	failedLoopID                 uuid.UUID
	failedSummary                string
	fixSummary                   string
	terminalErr                  error
	events                       []string
}

func (f *fakeReviewLoopStore) GetPrimaryChangesetID(_ context.Context, _ uuid.UUID, sessionID uuid.UUID) (uuid.UUID, error) {
	return sessionID, nil
}

func (f *fakeReviewLoopStore) CreateLoop(_ context.Context, loop *models.SessionReviewLoop) error {
	loop.ID = uuid.New()
	loop.StartedAt = time.Now().UTC()
	f.createdLoops = append(f.createdLoops, *loop)
	return nil
}

func (f *fakeReviewLoopStore) CreateLoopWithInitialPass(_ context.Context, loop *models.SessionReviewLoop, pass *models.SessionReviewLoopPass) error {
	if f.createLoopWithInitialPassErr != nil {
		return f.createLoopWithInitialPassErr
	}
	if err := f.CreateLoop(context.Background(), loop); err != nil {
		return err
	}
	pass.LoopID = loop.ID
	return f.CreatePass(context.Background(), pass)
}

func (f *fakeReviewLoopStore) CreatePass(_ context.Context, pass *models.SessionReviewLoopPass) error {
	pass.ID = uuid.New()
	now := time.Now().UTC()
	pass.ReviewStartedAt = &now
	f.createdPasses = append(f.createdPasses, *pass)
	f.latestPass = *pass
	return nil
}

func (f *fakeReviewLoopStore) SetPassReviewMessage(_ context.Context, _ uuid.UUID, _ uuid.UUID, messageID int64) error {
	f.reviewMessageIDs = append(f.reviewMessageIDs, messageID)
	return nil
}

func (f *fakeReviewLoopStore) GetRunningLoopByThread(_ context.Context, _ uuid.UUID, _ uuid.UUID) (models.SessionReviewLoop, error) {
	return f.runningLoop, nil
}

func (f *fakeReviewLoopStore) GetRunningLoopBySession(_ context.Context, _ uuid.UUID, _ uuid.UUID) (models.SessionReviewLoop, error) {
	if f.runningLoopBySession.ID == uuid.Nil {
		return models.SessionReviewLoop{}, pgx.ErrNoRows
	}
	return f.runningLoopBySession, nil
}

func (f *fakeReviewLoopStore) GetLatestPass(_ context.Context, _ uuid.UUID, _ uuid.UUID) (models.SessionReviewLoopPass, error) {
	return f.latestPass, nil
}

func (f *fakeReviewLoopStore) MarkPassDeciding(_ context.Context, _ uuid.UUID, _ uuid.UUID, reviewOutput string, _ int64) error {
	f.markedReviewOutput = reviewOutput
	f.latestPass.Status = models.ReviewLoopPassStatusDeciding
	return nil
}

func (f *fakeReviewLoopStore) MarkPassFixing(_ context.Context, _ uuid.UUID, _ uuid.UUID, decision models.ReviewLoopDecision, _ int64) error {
	f.fixDecision = decision
	f.latestPass.Status = models.ReviewLoopPassStatusFixing
	return nil
}

func (f *fakeReviewLoopStore) MarkPassClean(_ context.Context, _ uuid.UUID, loopID, _ uuid.UUID, decision models.ReviewLoopDecision, _ string) error {
	f.cleanLoopID = loopID
	f.cleanDecision = decision
	f.events = append(f.events, "clean")
	return nil
}

func (f *fakeReviewLoopStore) MarkPassCleanAndEnqueueOpenPR(_ context.Context, _ uuid.UUID, loopID, _ uuid.UUID, decision models.ReviewLoopDecision, _ string, _ map[string]any, _ string) error {
	if f.terminalErr != nil {
		return f.terminalErr
	}
	f.cleanLoopID = loopID
	f.cleanDecision = decision
	f.events = append(f.events, "clean_open_pr")
	return nil
}

func (f *fakeReviewLoopStore) MarkPassFixComplete(_ context.Context, _ uuid.UUID, _ uuid.UUID, fixSummary string) error {
	f.fixSummary = fixSummary
	f.latestPass.Status = models.ReviewLoopPassStatusNeedsFix
	return nil
}

func (f *fakeReviewLoopStore) MarkLoopNeedsHumanDecision(_ context.Context, _ uuid.UUID, _ uuid.UUID, _ string) error {
	return nil
}

func (f *fakeReviewLoopStore) MarkPassNeedsHumanDecision(_ context.Context, _ uuid.UUID, loopID, _ uuid.UUID, decision models.ReviewLoopDecision, _ string) error {
	f.needsHumanLoopID = loopID
	f.needsHumanDecision = decision
	f.events = append(f.events, "needs_human")
	return nil
}

func (f *fakeReviewLoopStore) MarkPassNeedsHumanDecisionAndEnqueueOpenPR(_ context.Context, _ uuid.UUID, loopID, _ uuid.UUID, decision models.ReviewLoopDecision, _ string, _ map[string]any, _ string) error {
	if f.terminalErr != nil {
		return f.terminalErr
	}
	f.needsHumanLoopID = loopID
	f.needsHumanDecision = decision
	f.events = append(f.events, "needs_human_open_pr")
	return nil
}

func (f *fakeReviewLoopStore) MarkLoopNeedsHumanDecisionAndEnqueueOpenPR(_ context.Context, _ uuid.UUID, loopID uuid.UUID, _ string, _ map[string]any, _ string) error {
	if f.terminalErr != nil {
		return f.terminalErr
	}
	f.needsHumanLoopID = loopID
	f.events = append(f.events, "needs_human_open_pr")
	return nil
}

func (f *fakeReviewLoopStore) MarkLoopFailed(_ context.Context, _ uuid.UUID, loopID uuid.UUID, summary string) error {
	f.failedLoopID = loopID
	f.failedSummary = summary
	f.events = append(f.events, "failed")
	return nil
}

func (f *fakeReviewLoopStore) MarkLoopFailedAndEnqueueOpenPR(_ context.Context, _ uuid.UUID, loopID uuid.UUID, _ string, _ map[string]any, _ string) error {
	if f.terminalErr != nil {
		return f.terminalErr
	}
	f.failedLoopID = loopID
	f.events = append(f.events, "failed_open_pr")
	return nil
}

type fakeThreadService struct {
	session models.Session
	thread  models.SessionThread
	message models.SessionMessage
	sent    []threadsvc.SendMessageInput
	created []threadsvc.CreateThreadInput
}

type fakeOrgStore struct {
	org models.Organization
	err error
}

func (f fakeOrgStore) GetByID(_ context.Context, _ uuid.UUID) (models.Organization, error) {
	if f.err != nil {
		return models.Organization{}, f.err
	}
	return f.org, nil
}

type fakeUserSettingsStore struct {
	user models.UserWithSettings
	err  error
}

func (f fakeUserSettingsStore) GetByIDGlobalWithSettings(_ context.Context, _ uuid.UUID) (models.UserWithSettings, error) {
	if f.err != nil {
		return models.UserWithSettings{}, f.err
	}
	return f.user, nil
}

func (f *fakeThreadService) GetSession(ctx context.Context, orgID, sessionID uuid.UUID) (models.Session, error) {
	return f.session, nil
}

func (f *fakeThreadService) CreateThread(ctx context.Context, input threadsvc.CreateThreadInput) (*models.SessionThread, error) {
	f.created = append(f.created, input)
	return &f.thread, nil
}

func (f *fakeThreadService) SendMessage(ctx context.Context, input threadsvc.SendMessageInput) (*threadsvc.SendMessageResult, error) {
	f.sent = append(f.sent, input)
	msg := f.message
	msg.ID += int64(len(f.sent) - 1)
	return &threadsvc.SendMessageResult{Message: &msg}, nil
}
