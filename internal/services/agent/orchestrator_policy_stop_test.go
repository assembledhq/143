package agent_test

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"

	"github.com/assembledhq/143/internal/models"
	"github.com/assembledhq/143/internal/services/agent"
)

func TestContinueSession_CodeReviewPolicyStopIsThreadScoped(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		reason         agent.StopReason
		adapterError   bool
		persistFailure bool
		reasonText     string
	}{
		{name: "no progress adapter error", reason: agent.StopReasonNoProgress, adapterError: true, reasonText: "The session stopped after going too long without meaningful progress."},
		{name: "no progress graceful exit", reason: agent.StopReasonNoProgress, reasonText: "The session stopped after going too long without meaningful progress."},
		{name: "absolute ceiling adapter error", reason: agent.StopReasonAbsoluteCeiling, adapterError: true, reasonText: "The session hit the absolute runtime ceiling."},
		{name: "absolute ceiling graceful exit", reason: agent.StopReasonAbsoluteCeiling, reasonText: "The session hit the absolute runtime ceiling."},
		{name: "adapter error persistence failure", reason: agent.StopReasonNoProgress, adapterError: true, persistFailure: true},
		{name: "graceful exit persistence failure", reason: agent.StopReasonAbsoluteCeiling, persistFailure: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			orgID, reviewerThreadID, primaryThreadID := testOrg(), uuid.New(), uuid.New()
			issue := testIssue(orgID)
			issue.Source = models.IssueSourceManual
			session := testRun(orgID, issue.ID)
			session.Origin = models.SessionOriginCodeReview
			session.RevisionContext = []byte(`{"kind":"code_review","github_repo":"acme/backend","github_pr_number":42,"base_sha":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","head_sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`)
			session.InteractionMode = models.SessionInteractionModeSingleRun
			session.ValidationPolicy = models.SessionValidationPolicySkip
			session.Status = models.SessionStatusRunning
			session.CurrentTurn = 1
			session.SnapshotKey = strPtr("snapshots/test/shared-review-checkpoint.tar")
			session.PrimaryThreadID = &primaryThreadID
			priorSnapshot := *session.SnapshotKey
			persistErr := errors.New("temporary reviewer result write failure")
			adapterErr := errors.New("adapter interrupted by runtime policy")
			cancelRegistry := agent.NewCancelRegistry(zerolog.Nop())
			siblingThreadID := uuid.New()
			cancelRegistry.RegisterScopedCause(session.ID, siblingThreadID, func(error) {}, agent.DefaultCancellationSpec)
			defer cancelRegistry.DeregisterScoped(session.ID, siblingThreadID)

			d := defaultDeps()
			d.issues.issue = issue
			d.cancels = cancelRegistry
			d.sessionThreads = &mockSessionThreadStore{getByIDResult: &models.SessionThread{
				ID: reviewerThreadID, OrgID: orgID, SessionID: session.ID,
				Status: models.ThreadStatusRunning, ExecutionMode: models.ThreadExecutionModeReview,
				FilesystemMode: models.ThreadFilesystemModeReadOnly,
			}}
			if tt.persistFailure {
				d.sessionThreads.updateResultErr = persistErr
			}
			d.sessions.getByIDFn = func(uuid.UUID, uuid.UUID) (models.Session, error) { return *session, nil }
			d.messages.messages = []models.SessionMessage{{ID: 1, OrgID: orgID, SessionID: session.ID, ThreadID: &reviewerThreadID, TurnNumber: 1, Role: models.MessageRoleUser, Content: "Review the pull request."}}
			d.snapshots.data = map[string][]byte{priorSnapshot: []byte("shared-review-checkpoint")}
			d.provider.RestoreFn = func(_ context.Context, _ *agent.Sandbox, reader io.Reader) error {
				_, err := io.ReadAll(reader)
				return err
			}
			var snapshots atomic.Int32
			d.provider.SnapshotFn = func(context.Context, *agent.Sandbox) (io.ReadCloser, error) {
				snapshots.Add(1)
				return io.NopCloser(bytes.NewReader([]byte("stopped-review-workspace"))), nil
			}
			d.adapter.executeFn = func(context.Context, *agent.Sandbox, *agent.AgentPrompt, chan<- agent.LogEntry) (*agent.AgentResult, error) {
				require.True(t, cancelRegistry.RequestStopScoped(session.ID, siblingThreadID, agent.StopReasonUserCancel, time.Second), "a sibling may independently receive a user cancellation")
				require.True(t, cancelRegistry.RequestStopScoped(session.ID, reviewerThreadID, tt.reason, time.Second), "policy stop should find the executing reviewer's cancel scope")
				result := &agent.AgentResult{Summary: "Partial reviewer output", AgentSessionID: "stopped-reviewer", ExitCode: 1}
				if tt.adapterError {
					return result, adapterErr
				}
				return result, nil
			}

			err := buildOrchestrator(d).ContinueSession(context.Background(), session, &agent.ContinueSessionOptions{ThreadID: &reviewerThreadID})

			if tt.adapterError {
				require.ErrorIs(t, err, adapterErr, "policy handling should preserve an adapter's original failure")
			}
			if tt.persistFailure {
				require.ErrorIs(t, err, persistErr, "an undurable reviewer stop must be retried by the worker")
				require.NotErrorIs(t, err, agent.ErrCodeReviewThreadFailed, "a failed terminal write must not look like a durably failed reviewer")
				require.Empty(t, d.sessionThreads.resultsForThread(reviewerThreadID), "a rejected result write must leave no durable reviewer failure")
			} else {
				require.ErrorIs(t, err, agent.ErrCodeReviewThreadFailed, "a durable reviewer policy stop should return control to the review controller")
				require.Contains(t, err.Error(), string(tt.reason), "the terminal error should retain its typed runtime stop reason")
				expected := models.SessionResult{
					Error:               strPtr(tt.reasonText + " The latest in-flight work could not be saved."),
					FailureExplanation:  strPtr(tt.reasonText + " The run had to stop before any durable checkpoint was available for this turn."),
					FailureCategory:     strPtr(agent.FailureCategoryTimeout),
					FailureNextSteps:    []string{"Retry the session from scratch", "Split the task into smaller pieces so checkpoints publish sooner", "Reduce broad repository scans or long-running commands in the prompt"},
					FailureRetryAdvised: true,
				}
				require.Equal(t, []models.SessionResult{expected}, d.sessionThreads.resultsForThread(reviewerThreadID), "the exact stopped reviewer should retain structured policy failure and retry guidance")
			}
			require.Empty(t, d.sessions.getResultUpdates(), "one stopped reviewer must not fail the shared review parent")
			require.Empty(t, d.sessions.getTurnUpdates(), "one stopped reviewer must not publish a successful shared turn")
			require.Empty(t, d.sessions.getCheckpointUpdates(), "one stopped reviewer must not publish a checkpoint for the shared parent")
			require.Equal(t, int32(0), snapshots.Load(), "a stopped reviewer must not overwrite the shared workspace snapshot")
			require.Equal(t, priorSnapshot, *session.SnapshotKey, "the stopped reviewer must preserve the existing shared checkpoint identity")
			require.NotContains(t, d.sessionThreads.statusesForThread(primaryThreadID), models.ThreadStatusFailed, "the primary synthesis thread must remain available to the controller")
		})
	}
}

func TestContinueSession_NonReviewPolicyStopPreservesSessionCheckpoint(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		adapterError bool
	}{
		{name: "adapter error", adapterError: true},
		{name: "graceful exit"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			orgID, threadID := testOrg(), uuid.New()
			issue := testIssue(orgID)
			issue.Source = models.IssueSourceManual
			session := testRun(orgID, issue.ID)
			session.Origin = models.SessionOriginManual
			session.Status = models.SessionStatusIdle
			session.CurrentTurn = 1
			session.SnapshotKey = strPtr("snapshots/test/continued-session.tar")
			session.PrimaryThreadID = &threadID
			cancelRegistry := agent.NewCancelRegistry(zerolog.Nop())
			d := defaultDeps()
			d.cancels = cancelRegistry
			d.issues.issue = issue
			d.sessionThreads = &mockSessionThreadStore{}
			d.messages.messages = []models.SessionMessage{{ID: 1, OrgID: orgID, SessionID: session.ID, ThreadID: &threadID, TurnNumber: 2, Role: models.MessageRoleUser, Content: "Continue the session."}}
			d.snapshots.data = map[string][]byte{*session.SnapshotKey: []byte("prior-session-checkpoint")}
			d.provider.RestoreFn = func(_ context.Context, _ *agent.Sandbox, reader io.Reader) error {
				_, err := io.ReadAll(reader)
				return err
			}
			d.provider.SnapshotFn = func(context.Context, *agent.Sandbox) (io.ReadCloser, error) {
				return io.NopCloser(bytes.NewReader([]byte("stopped-session-checkpoint"))), nil
			}
			d.provider.ExecFn = func(_ context.Context, _ *agent.Sandbox, cmd string, _, _ io.Writer) (int, error) {
				if strings.HasPrefix(cmd, "git ") {
					return 0, nil
				}
				return 1, errors.New("exec unavailable in test")
			}
			d.adapter.executeFn = func(context.Context, *agent.Sandbox, *agent.AgentPrompt, chan<- agent.LogEntry) (*agent.AgentResult, error) {
				require.True(t, cancelRegistry.RequestStopScoped(session.ID, threadID, agent.StopReasonNoProgress, time.Second), "policy stop should find the continued session scope")
				result := &agent.AgentResult{Summary: "Interrupted cleanly", AgentSessionID: "stopped-session", ExitCode: 1}
				if tt.adapterError {
					return result, errors.New("adapter stopped")
				}
				return result, nil
			}

			err := buildOrchestrator(d).ContinueSession(context.Background(), session, &agent.ContinueSessionOptions{ThreadID: &threadID})

			require.NoError(t, err, "ordinary session policy stops should retain their handled behavior")
			checkpoints := d.sessions.getCheckpointUpdates()
			require.Len(t, checkpoints, 1, "ordinary policy stops should publish one shared-session checkpoint")
			require.Equal(t, models.RuntimeStopReasonNoProgress, checkpoints[0].stopReason, "ordinary checkpoint metadata should retain the policy reason")
			failures := d.sessions.getResultsWithFailureCategory(agent.FailureCategoryTimeout)
			require.Len(t, failures, 1, "ordinary policy stops should retain a terminal parent result")
			require.Contains(t, *failures[0].result.FailureExplanation, "saved a resumable checkpoint", "ordinary sessions should preserve resume guidance")
		})
	}
}

func TestContinueSession_CodeReviewWatchdogStopsOnlyExecutingThread(t *testing.T) {
	t.Parallel()

	orgID, reviewerThreadID, siblingThreadID := testOrg(), uuid.New(), uuid.New()
	issue := testIssue(orgID)
	issue.Source = models.IssueSourceManual
	session := testRun(orgID, issue.ID)
	session.Origin = models.SessionOriginCodeReview
	session.RevisionContext = []byte(`{"kind":"code_review","github_repo":"acme/backend","github_pr_number":42,"base_sha":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","head_sha":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"}`)
	session.InteractionMode = models.SessionInteractionModeSingleRun
	session.ValidationPolicy = models.SessionValidationPolicySkip
	session.Status = models.SessionStatusRunning
	session.CurrentTurn = 1
	session.SnapshotKey = strPtr("snapshots/test/watchdog-review-checkpoint.tar")
	session.PrimaryThreadID = &siblingThreadID
	cancelRegistry := agent.NewCancelRegistry(zerolog.Nop())
	siblingCtx, cancelSibling := context.WithCancelCause(context.Background())
	defer cancelSibling(nil)
	cancelRegistry.RegisterScopedCause(session.ID, siblingThreadID, cancelSibling, agent.DefaultCancellationSpec)
	defer cancelRegistry.DeregisterScoped(session.ID, siblingThreadID)

	d := defaultDeps()
	d.issues.issue = issue
	d.cancels = cancelRegistry
	d.orgs = &mockOrgStore{org: models.Organization{ID: orgID, Settings: []byte(`{
		"max_session_duration_seconds":30,
		"runtime_budgets":{
			"no_progress_timeout_seconds":1,
			"graceful_shutdown_window_seconds":1,
			"checkpoint_finalization_window_seconds":1,
			"automatic_extension_seconds":2,
			"max_automatic_extension_seconds":2,
			"absolute_runtime_ceiling_seconds":5
		}
	}`)}}
	d.sessionThreads = &mockSessionThreadStore{getByIDResult: &models.SessionThread{
		ID: reviewerThreadID, OrgID: orgID, SessionID: session.ID,
		Status: models.ThreadStatusRunning, ExecutionMode: models.ThreadExecutionModeReview,
		FilesystemMode: models.ThreadFilesystemModeReadOnly,
	}}
	d.sessions.getByIDFn = func(uuid.UUID, uuid.UUID) (models.Session, error) { return *session, nil }
	d.messages.messages = []models.SessionMessage{{ID: 1, OrgID: orgID, SessionID: session.ID, ThreadID: &reviewerThreadID, TurnNumber: 1, Role: models.MessageRoleUser, Content: "Review the pull request."}}
	d.snapshots.data = map[string][]byte{*session.SnapshotKey: []byte("shared-watchdog-review-checkpoint")}
	d.provider.RestoreFn = func(_ context.Context, _ *agent.Sandbox, reader io.Reader) error {
		_, err := io.ReadAll(reader)
		return err
	}
	var snapshots atomic.Int32
	d.provider.SnapshotFn = func(context.Context, *agent.Sandbox) (io.ReadCloser, error) {
		snapshots.Add(1)
		return io.NopCloser(bytes.NewReader([]byte("stopped-watchdog-review-workspace"))), nil
	}
	d.adapter.executeFn = func(ctx context.Context, _ *agent.Sandbox, _ *agent.AgentPrompt, _ chan<- agent.LogEntry) (*agent.AgentResult, error) {
		<-ctx.Done()
		return &agent.AgentResult{Summary: "Interrupted by watchdog", ExitCode: 1}, ctx.Err()
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	err := buildOrchestrator(d).ContinueSession(ctx, session, &agent.ContinueSessionOptions{ThreadID: &reviewerThreadID})

	require.ErrorIs(t, err, agent.ErrCodeReviewThreadFailed, "the actual watchdog must durably fail only its executing review thread")
	require.Contains(t, err.Error(), string(agent.StopReasonNoProgress), "the actual watchdog should stop the inactive reviewer for no progress")
	require.NoError(t, siblingCtx.Err(), "the watchdog must leave a healthy sibling execution context running")
	require.Equal(t, agent.StopReasonNone, cancelRegistry.StopReasonScoped(session.ID, siblingThreadID), "the watchdog must leave a healthy sibling's stop reason empty")
	require.Equal(t, []models.ThreadStatus{models.ThreadStatusFailed}, d.sessionThreads.statusesForThread(reviewerThreadID), "the stopped reviewer's terminal failure must be durable before returning the sentinel")
	require.Empty(t, d.sessionThreads.statusesForThread(siblingThreadID), "the watchdog must not terminalize the healthy synthesis thread")
	require.Empty(t, d.sessions.getResultUpdates(), "the review controller must retain ownership of the parent result after a watchdog stop")
	require.Empty(t, d.sessions.getCheckpointUpdates(), "the stopped reviewer must not publish a shared parent checkpoint")
	require.Equal(t, int32(0), snapshots.Load(), "the watchdog must not overwrite the shared parent's snapshot")
	d.sessions.mu.Lock()
	stopRequests := append([]models.RuntimeStopReason(nil), d.sessions.stopRequests...)
	d.sessions.mu.Unlock()
	require.Empty(t, stopRequests, "a reviewer watchdog must not mark the shared parent for the runtime reaper")
}
